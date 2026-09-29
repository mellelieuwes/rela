package entitymanager

import (
	"context"
	"errors"
	"fmt"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// upsertOp captures the create-vs-update labels for an apply, derived once from
// whether the target already exists.
type upsertOp struct {
	aclOp   acl.Op
	auditOp string
	summary string
}

// resolveUpsertOp turns the result of an existence probe into the create-or-
// update labels for an upsert.
//
// It fails CLOSED: a non-nil error that is NOT [store.ErrNotFound] (a flaky
// backend, a cancelled context) is returned as an error rather than being
// treated as "does not exist". Treating a transient read failure as a create
// would (a) authorize the wrong ACL verb — create and update are separately
// grantable — and (b) write a "create" audit row for what is really an update,
// corrupting the forensic log during exactly the incident you'd be
// investigating. This mirrors the fail-closed discipline in [Manager.RenameEntity].
func resolveUpsertOp(getErr error, createAudit, updateAudit string) (upsertOp, error) {
	switch {
	case getErr == nil:
		return upsertOp{aclOp: acl.OpUpdate, auditOp: updateAudit, summary: "updated"}, nil
	case errors.Is(getErr, store.ErrNotFound):
		return upsertOp{aclOp: acl.OpCreate, auditOp: createAudit, summary: "created"}, nil
	default:
		return upsertOp{}, getErr
	}
}

// ApplyEntity upserts an entity by its supplied ID, preserving that ID, with
// the full ACL + validation + audit framing of the normal write path but
// WITHOUT running automation or the cascade.
//
// It exists for the sync apply path (FEAT-NJ9FEN): a record edited on one peer
// must be reproduced on the other under its existing ID, and the automations
// that produced any derived records already ran on the origin — re-running them
// here would duplicate side effects and can ping-pong derived changes back
// (TKT-78R2YB; design review RR-L1MY0N, RR-AZMA7T).
//
// How it differs from the human-intent write path:
//
//   - Unlike [Manager.CreateEntity], it does NOT reject an explicit ID for a
//     non-manual id_type, generate an ID, or apply a template / status default.
//     The caller owns the complete entity state (id, type, properties,
//     content); ApplyEntity persists exactly that. PRECONDITION: the caller
//     must supply every field the record should have (including status) — there
//     is no backfill, because automation is suppressed.
//   - Unlike CreateEntity/UpdateEntity, it runs NO automation and NO cascade.
//   - Unlike UpdateEntity/PatchEntity, it does NOT check twin ownership: a
//     replica reproduces a state the origin already accepted, including owned
//     fields its twin sync wrote, so re-checking would make a synced peer
//     refuse its own twin's writes (see rejectTheirsChanges).
//   - Like every write path, it authorizes against the ACL, validates against
//     the metamodel (hard errors abort; soft conditions ride along as
//     warnings), and emits an audit record AFTER the durable write (consistent
//     with Create/Update — a committed write is never left unaudited). The
//     durable write is a direct CreateEntity or UpdateEntity chosen by the
//     resolved intent — never an upsert: a create-intent write that races a
//     concurrent create is rejected (ErrEntityAlreadyExists), not silently
//     turned into a re-typing overwrite (BUG-ZWTDH9).
//
// ID-prefix note: validation includes the metamodel's ID-prefix check, which is
// a HARD error. Sync therefore assumes peers share a metamodel (so a
// peer-minted ID matches a local prefix). A record whose ID matches no local
// prefix is rejected — see TestApplyEntity_RejectsForeignIDPrefix.
//
// The audited op (create vs. update) is chosen from whether the entity already
// exists; a flaky existence probe fails closed (see [resolveUpsertOp]).
func (m *Manager) ApplyEntity(ctx context.Context, e *entity.Entity) (*entity.UpdateResult, error) {
	ctx = withStoreAttribution(ctx)
	if e == nil {
		return nil, errors.New("entitymanager: ApplyEntity: entity is nil")
	}
	if e.ID == "" {
		return nil, errors.New("entitymanager: ApplyEntity: entity ID is empty")
	}
	if e.IsLocked() {
		// The in-memory entity has redacted fields; writing it would replace
		// the on-disk content (typically ciphertext) with the cleartext shell.
		// Same guard the rest of the write path applies.
		return nil, fmt.Errorf("entitymanager: ApplyEntity: entity %s has inaccessible fields", e.ID)
	}

	// The probe addresses the row the body NAMES, not the zero coordinate.
	// GetEntity(id) is GetEntityState(id, zero), which a type declaring faces
	// has no row at — so every faced apply resolved as a CREATE, the update
	// branch below was unreachable, and the ErrFaceImmutable guard that
	// branch carries never ran. A sync body could then be authorized against
	// its own face while a sibling row of the same id already existed
	// (BUG-HC6I2T).
	stored, getErr := m.deps.Store.GetEntityState(ctx, e.ID, e.Face)
	op, err := resolveUpsertOp(getErr, audit.OpCreateEntity, audit.OpUpdateEntity)
	if err != nil {
		return nil, fmt.Errorf("entitymanager: ApplyEntity: existence check for %s: %w", e.ID, err)
	}

	// On UPDATE, the authorization subject must be bound to the RESOURCE, not
	// the body. Type is immutable on update: authorize (and below, validate)
	// against the stored type, and hard-reject a body type that differs — a
	// mismatch is a cross-type write-privilege escalation, not a legal edit
	// (BUG-ZWTDH9). On CREATE (entity does not yet exist) the body type IS the
	// new entity's type, so subjectType is simply e.Type.
	// The same argument applies to FACE, and for the same reason: Face
	// carries `json:"face,omitempty"`, so a sync body can name one, and
	// the store keys its write on stateKey(e.ID, e.Face) — authorizing
	// against a body-supplied face would let the body choose the face it
	// is checked against (BUG-Y0GNSB). Bind to the stored face on update.
	subjectType := e.Type
	subjectFace := e.Face
	if op.aclOp == acl.OpUpdate {
		if e.Type != stored.Type {
			return nil, fmt.Errorf("entitymanager: ApplyEntity: %s: %w (stored %q, body %q)",
				e.ID, ErrTypeImmutable, stored.Type, e.Type)
		}
		if e.Face != stored.Face {
			return nil, fmt.Errorf("entitymanager: ApplyEntity: %s: %w (stored face %q, body face %q)",
				e.ID, ErrFaceImmutable, stored.Face, e.Face)
		}
		subjectType = stored.Type
		subjectFace = stored.Face
	}

	// The same face rule the other three create paths enforce
	// (Manager.CreateEntity, ValidateCreate, cascadeHost.CreateEntity). On
	// UPDATE the face is the stored one and already exists, so there is
	// nothing to check; on CREATE a sync body could otherwise land a row at
	// an undeclared face, or at the zero coordinate of a faced type — a row
	// belonging to no declared face, which nothing can address and no grant
	// covers. Fourth path, same rule, so the four cannot drift.
	//
	// Only when the type is DECLARED: an unknown type is validation's to
	// report, and preempting it here would swap a ValidationError for a bare
	// "unknown entity type" that callers cannot classify.
	if _, known := m.deps.Meta.GetEntityDef(e.Type); known && op.aclOp == acl.OpCreate {
		if err := m.deps.requireCreateFaceFor(e.Type, e.Face); err != nil {
			return nil, err
		}
	}
	if err := m.authorizeAndAudit(ctx, acl.WriteRequest{
		Op:      op.aclOp,
		Subject: acl.NewEntitySubject(subjectType, e.ID, subjectFace),
	}); err != nil {
		return nil, err
	}
	// Apply is a trusted whole-record replica path: ignore any incoming
	// materialized value and recompute under the receiving schema.
	if err := m.deps.Computed.Evaluate(ctx, e); err != nil {
		return nil, err
	}

	// DEC-HWZHA: hard structural errors abort (the API layer maps these to
	// 422); soft conditions surface as warnings on the result.
	hard, soft := partitionValidationErrors(m.deps.Meta.ValidateEntity(e.ID, e.Type, e.Properties))
	if len(hard) > 0 {
		return nil, newValidationError(hard)
	}

	// Enforce enum state machines on the sync/upsert path too (RR-NB135): this
	// is a served write with a real principal, so skipping it would let a sync
	// client land an illegal transition or bypass a guard — the exact hole the
	// "unforgettable" pipeline is meant to close. On an update we diff against
	// the probed `stored` state; on a create we check the entry value. The
	// guard adapter is inert when there is no policy/principal (CLI sync), so
	// only a policy-backed served sync is gated. ApplyEntity runs no automation,
	// so this is the final pre-write state.
	if op.aclOp == acl.OpUpdate {
		if err := m.deps.Transitions.EnforceUpdate(
			ctx, stored, e, m.deps.TransitionGuard, m.deps.TransitionGraph,
		); err != nil {
			return nil, m.mapTransitionError(
				ctx, acl.NewEntitySubject(subjectType, e.ID, subjectFace), err)
		}
	} else if err := m.deps.Transitions.EnforceCreate(ctx, e); err != nil {
		return nil, err
	}

	// Write by RESOLVED intent — never upsert. A create that conflicts
	// (a concurrent create of the same ID landed since the existence
	// probe) is a lost-update + type-re-type vector if it silently
	// updates: reject it as ErrEntityAlreadyExists so the caller re-reads
	// (the sync handler maps it to 409). An update whose row vanished
	// concurrently surfaces as ErrEntityVanishedOnUpdate (wrapping
	// ErrEntityNotFound; the sync handler maps it to a 412 conflict,
	// symmetric with the relation vanished-on-update case). This is what
	// closes the residual on the postgres multi-writer backend
	// (BUG-ZWTDH9).
	//
	// `unique: true` natural keys are enforced atomically with the write,
	// excluding this entity's own prior version (sync upserts an existing ID).
	if err := writeWithUniqueCheck(ctx, m.deps, e, e.ID, func(st store.Store) error {
		return persistApplyEntity(ctx, st, op.aclOp, e)
	}); err != nil {
		return nil, err
	}
	m.recordEntityAudit(ctx, op.auditOp, e, op.summary)

	return &entity.UpdateResult{Entity: e, Warnings: soft}, nil
}

// persistApplyEntity writes e by the resolved apply intent: OpCreate ->
// CreateEntity (a conflict is ErrEntityAlreadyExists — never overwrite);
// OpUpdate -> UpdateEntity (a vanished row is ErrEntityVanishedOnUpdate, which
// wraps ErrEntityNotFound). Neither branch falls back to the other, so a
// create-intent write that races a concurrent create can never become a blind,
// re-typing update.
func persistApplyEntity(ctx context.Context, st store.Store, op acl.Op, e *entity.Entity) error {
	if op == acl.OpCreate {
		if err := st.CreateEntity(ctx, e); err != nil {
			// A derived unique-property index rejects a duplicate PROPERTY value
			// (TKT-3Q0GP1). Check before ErrConflict (which UniquePropertyError
			// also satisfies) so it surfaces as the property 422, not the
			// ID-collision error.
			if ok, mapped := mapUniquePropertyConflict(err); ok {
				return mapped
			}
			if errors.Is(err, store.ErrConflict) {
				return fmt.Errorf("%w: %s", ErrEntityAlreadyExists, e.ID)
			}
			return fmt.Errorf("entitymanager: ApplyEntity: %w", err)
		}
		return nil
	}
	if err := st.UpdateEntity(ctx, e); err != nil {
		if ok, mapped := mapUniquePropertyConflict(err); ok {
			return mapped
		}
		if errors.Is(err, store.ErrNotFound) {
			// The probe saw the entity but the row vanished before the write
			// (a concurrent delete). Surface ErrEntityVanishedOnUpdate, which
			// wraps ErrEntityNotFound so existing errors.Is callers still match
			// while the sync handler can map this narrow race to a 412 conflict
			// (symmetric with the relation vanished-on-update case), distinct
			// from the 404 reserved for a missing relation endpoint.
			return fmt.Errorf("%w: %s", ErrEntityVanishedOnUpdate, e.ID)
		}
		return fmt.Errorf("entitymanager: ApplyEntity: %w", err)
	}
	return nil
}

// ApplyRelation upserts a relation by its from/type/to triple, with the same
// ACL + validation + audit framing as the human-intent relation write path but
// WITHOUT managed-order auto-assignment — the caller's relation already carries
// any order properties from the origin.
//
// Both endpoints must already exist (a relation cannot reference a missing
// entity), so the sync apply layer is responsible for ordering: all entities
// before any relation that references them (design review RR-YHGJHG). A genuine
// missing endpoint returns [ErrEntityNotFound] (the apply layer retries on the
// next pass once the endpoint is applied); a flaky endpoint read fails closed
// with the underlying error, so the retry loop is not spun forever on a
// transient backend fault.
//
// Managed order: unlike CreateRelation/UpdateRelation, ApplyRelation does not
// auto-assign or validate _order_out/_order_in — sync mirrors whatever finite
// order the origin already assigned. The caller (sync) is trusted to carry
// well-formed order values from a peer that produced them through the normal
// write path.
func (m *Manager) ApplyRelation(ctx context.Context, r *entity.Relation) (*entity.Relation, error) {
	ctx = withStoreAttribution(ctx)
	if r == nil {
		return nil, errors.New("entitymanager: ApplyRelation: relation is nil")
	}
	if r.IsLocked() {
		return nil, fmt.Errorf("entitymanager: ApplyRelation: relation %s has inaccessible fields", r.Key())
	}

	fromEntity, err := m.requireEndpoint(ctx, r.From, "source")
	if err != nil {
		return nil, err
	}
	toEntity, err := m.requireEndpoint(ctx, r.To, "target")
	if err != nil {
		return nil, err
	}

	_, getErr := m.deps.Store.GetRelation(ctx, r.From, r.Type, r.To)
	op, err := resolveUpsertOp(getErr, audit.OpCreateRelation, audit.OpUpdateRelation)
	if err != nil {
		return nil, fmt.Errorf("entitymanager: ApplyRelation: existence check for %s: %w", r.Key(), err)
	}

	if aclErr := m.authorizeAndAudit(ctx, acl.WriteRequest{
		Op: op.aclOp,
		Subject: acl.RelationSubject{
			Type:     r.Type,
			FromType: fromEntity.Type, FromID: r.From,
		},
	}); aclErr != nil {
		return nil, aclErr
	}

	if vErr := m.deps.Meta.ValidateRelation(r.Type, fromEntity.Type, toEntity.Type); vErr != nil {
		return nil, fmt.Errorf("entitymanager: ApplyRelation: invalid relation: %w", vErr)
	}

	// Write by RESOLVED intent — never upsert (BUG-ZWTDH9). OpCreate ->
	// CreateRelation (a conflict is ErrRelationAlreadyExists); OpUpdate ->
	// UpdateRelation (a vanished row is ErrRelationNotFound).
	if err := m.persistApplyRelation(ctx, op.aclOp, r); err != nil {
		return nil, err
	}
	m.recordRelationAudit(ctx, op.auditOp, r, op.summary)

	return r, nil
}

// persistApplyRelation writes r by the resolved apply intent, mirroring
// persistApplyEntity: no create-then-update fallback, so a create-intent
// write that races a concurrent create is rejected, not silently merged.
func (m *Manager) persistApplyRelation(ctx context.Context, op acl.Op, r *entity.Relation) error {
	data := store.RelationData{Properties: r.Properties, Content: r.Content}
	if op == acl.OpCreate {
		if _, err := m.deps.Store.CreateRelation(ctx, r.From, r.Type, r.To, &data); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return fmt.Errorf("%w: %s --%s--> %s", ErrRelationAlreadyExists, r.From, r.Type, r.To)
			}
			return fmt.Errorf("entitymanager: ApplyRelation: %w", err)
		}
		return nil
	}
	// In a Tx so it cannot land between UpdateRelation's read and write,
	// which run in one Tx; otherwise that update would write back the row it
	// read and drop this one.
	err := m.deps.Store.Tx(ctx, func(view store.Store) error {
		_, err := view.UpdateRelation(ctx, r.From, r.Type, r.To, data)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: %s --%s--> %s", ErrRelationNotFound, r.From, r.Type, r.To)
		}
		return fmt.Errorf("entitymanager: ApplyRelation: %w", err)
	}
	return nil
}

// requireEndpoint loads a relation endpoint, distinguishing a genuine
// not-found (mapped to [ErrEntityNotFound] so the sync layer retries) from a
// transient store error (returned as-is so the retry loop is not spun forever).
// role is "source" or "target" for the error message.
func (m *Manager) requireEndpoint(ctx context.Context, id, role string) (*entity.Entity, error) {
	ent, err := m.deps.Store.GetEntity(ctx, id)
	switch {
	case err == nil:
		return ent, nil
	case errors.Is(err, store.ErrNotFound):
		return nil, fmt.Errorf("%s %w: %s", role, ErrEntityNotFound, id)
	default:
		return nil, fmt.Errorf("entitymanager: ApplyRelation: load %s endpoint %s: %w", role, id, err)
	}
}
