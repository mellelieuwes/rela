package entitymanager

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// copyEngine owns the copy operation: planning, authorization, target
// construction, edge planning and audit.
//
// # Why a separate type (Jeroen's call, TKT-WRLDAPI item 5)
//
// It is a decomposition, not a lint workaround. Copies are already a distinct
// operation — deliberately outside the ordinary-CRUD write contract — and this
// makes that boundary structural rather than conventional. Since TKT-IVSJV6
// removed the wide producer-side `EntityManager` interface, that contract is
// no longer one declared type: each consumer narrows to what it calls, and the
// copy path has its own ([CopyReader], [CopyGuard], [CopyReadGate]) rather
// than riding along on the CRUD ones. `Manager` was at 41 methods against a 40 load
// line, almost all of them private helpers; adding the copy-affordance query
// would have pinned it at the line rather than under it, and the load line is
// a ratchet to narrow, not a budget to spend.
//
// # It holds the Manager, and that is deliberate
//
// The engine needs `authorizeAndAudit`, which is Manager's and is shared with
// every other write path. Duplicating it here would be a second authorization
// implementation — the exact defect this feature's design refuses. So the
// engine composes the manager rather than replacing it.
//
// # planCopy and authorizeCopy stay UNEXPORTED
//
// That is load-bearing, not incidental. [CopiesForSource] computes each
// offer's `Allowed` by running those two — the same code the invoke runs — and
// that is the only reason the hint cannot drift from the write (RULING 11).
// Exporting them, or making invocability computable from outside this package,
// would let a caller answer the question a different way and reintroduce the
// two-authorization-sites defect.
type copyEngine struct{ m *Manager }

// Copy errors. ErrUnknownCopy is caller input (4xx); the guard errors map to
// 403/422 exactly as the statemachine's do, because they ARE the
// statemachine's vocabulary (design doc §9.1: shared guard machinery,
// separate declaration).
var (
	// ErrUnknownCopy names a definition the metamodel does not declare. A
	// request may only invoke a definition BY NAME, so this is the whole of
	// "that copy does not exist" — never a hint that some other mapping
	// might work.
	ErrUnknownCopy = errors.New("entitymanager: no such copy definition")

	// ErrCopySourceMissing means the source face does not exist. Distinct
	// from a denial: the caller asked to copy something that is not there.
	ErrCopySourceMissing = errors.New("entitymanager: copy source face does not exist")

	// ErrCopyTargetRequired: a cross-entity copy names its target id.
	ErrCopyTargetRequired = errors.New("entitymanager: cross-entity copy requires a target id")
	// ErrCopyTargetNotAllowed: a same-entity copy's target IS the source, so
	// a request that names one is asking for a copy the definition cannot do.
	ErrCopyTargetNotAllowed = errors.New("entitymanager: same-entity copy takes no target id")
	// ErrCopyTargetTypeMismatch: the named cross-entity target exists with a
	// different type. Writing it would re-type a stored entity — the
	// corruption [ErrTypeImmutable] exists to prevent.
	ErrCopyTargetTypeMismatch = errors.New("entitymanager: copy target exists with a different type")
)

// CopyRequest invokes a declared copy definition. There is deliberately no
// field for the mapping: a request chooses a NAME, never a shape.
//
// This is the transforms-registry precedent (`?transform=<name>`, never a
// command), and it is what keeps the guard system meaningful — a caller who
// could describe an arbitrary copy could describe one whose guard is
// convenient.
type CopyRequest struct {
	// Definition is the key in the metamodel's `copies:` block.
	Definition string

	// SourceID is the entity the copy reads from. For a same-entity copy it
	// is also the target; for a cross-entity copy the target is created.
	SourceID string

	// TargetID is the cross-entity target's id. Ignored (and must be empty)
	// for a same-entity copy, whose target is SourceID by construction.
	TargetID string
}

// CopyResult reports what a copy produced, in the vocabulary a UI would
// need without deciding anything about presentation.
type CopyResult struct {
	// Definition is the name that was invoked.
	Definition string
	// Entity is the resulting target face, as stored.
	Entity *entity.Entity
	// Created is true when the copy brought the target face into existence
	// rather than overwriting one.
	Created bool
}

// CopyState executes a declared copy definition: write a mapped subset of one
// content state (face) into another, in ONE store transaction, audited.
//
// # The kernel is dumb; the definition carries the policy
//
// Everything policy-shaped — which fields, merge vs replace, the guard —
// lives in the compiled definition. This function writes what it is told.
// That split is what lets the security tests aim at one small function
// rather than at a mapping language.
//
// # store.Tx, and a note for reviewers
//
// entitymanager had NEVER called store.Tx before this (the only non-store
// caller in the tree was internal/datamigration). That is deliberate here,
// not an oversight: a copy writes fields, body and edges, and a half-applied
// copy is a face that claims to be a promotion of something it is not.
//
// Two consequences the contract forces:
//
//   - Every store call inside the transaction goes through the VIEW, never
//     ce.m.deps.Store. A write on the outer store from inside deadlocks on
//     fs/mem and silently bypasses the transaction on pg.
//   - fs/mem have NO ROLLBACK — store.Tx there is a write mutex. A mid-copy
//     failure leaves a partially written target face on those backends. That
//     is the accepted best-effort stance for fsstore (§6.1); pgstore gives
//     real atomicity.
//
// # Audit lands AFTER the commit
//
// A rollback that left an audit record would make the log claim a write that
// never happened, which is worse than a missing record — the log's whole
// value is that it does not lie. This matches the existing "audit the
// durable write" rule, extended past a transaction boundary.
//
// # What this does NOT do
//
// It does not validate the DESTINATION WORLD. §9.2 says a copy into a state
// a world selects is valid iff that world remains a valid graph, but
// `must_hold_in` is TKT-9KZGJO's declared seam and shipping half of it here
// would fork it. That is a boundary, not an omission.
func (ce *copyEngine) copyState(ctx context.Context, req CopyRequest) (*CopyResult, error) {
	ctx = withStoreAttribution(ctx)

	def, ok := ce.m.deps.Meta.Copies[req.Definition]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownCopy, req.Definition)
	}
	plan, err := ce.planCopy(ctx, req, def)
	if err != nil {
		return nil, err
	}

	tx, ok := ce.m.deps.Store.(store.Transactor)
	if !ok {
		// Every backend implements Transactor; a store that does not is a
		// wiring error, and running the copy unsynchronized would be worse
		// than refusing it.
		return nil, errors.New("entitymanager: store does not support transactions")
	}

	// Provenance is attached AFTER planning, so it scopes exactly the writes
	// this copy performs and nothing the planner read. The store stamps it on
	// the target row; the version sweep copies it onto the version it later
	// captures (migration 0013). It is not on the plan struct: the plan
	// describes WHAT to write, and provenance is a property of the write
	// context, carried the same boundary-populated way as attribution.
	// sourceTail is both the coordinate and the name a reader reads: a face
	// has one spelling (BUG-HC6I2T). This used to resolve a separate declared
	// name, because `bare_face` made one face's stored coordinate the empty
	// string and recording it would have dropped the `@draft` from a copy
	// declared `from: policy@draft`. See withCopyOrigin.
	writeCtx := withCopyOrigin(
		ctx, plan.name, plan.sourceID, plan.from.Type, string(plan.sourceTail))

	var result CopyResult
	if err := tx.Tx(writeCtx, func(view store.Store) error {
		res, cerr := applyCopy(writeCtx, ce.m.deps.Meta, view, plan)
		if cerr != nil {
			return cerr
		}
		result = *res
		return nil
	}); err != nil {
		return nil, err
	}

	// AFTER the commit — see the godoc.
	ce.recordCopyAudit(ctx, plan, result)
	return &result, nil
}

// copyPlan is a resolved, authorized copy: the exact bytes to write and where.
// Producing it is where every read, guard and authorization happens, so the
// transaction body stays a pure write.
type copyPlan struct {
	name     string
	def      metamodel.CopyDef
	from     metamodel.CopyTarget
	to       metamodel.CopyTarget
	sourceID string
	targetID string

	// sourceTail and targetTail are the faces this copy reads and writes.
	// A face's declared name IS its stored coordinate (BUG-HC6I2T removed
	// the `bare_face` mapping), so these are the `copies:` spelling taken
	// literally.
	sourceTail entity.Face
	targetTail entity.Face

	// existing is the target face as stored BEFORE the copy, nil when the
	// copy creates it. Probed in planCopy before authorization, because the
	// verdict depends on it: overwriting a stored face is an UPDATE, not a
	// create, and an existing target of another type is refused outright.
	existing *entity.Entity
	// entity is the target face to write, already merged against any existing
	// target (mapped fields written, unmapped untouched — §9.1).
	entity *entity.Entity
	// created records whether the target face existed before.
	created bool
	// edges are the copied relations, already authorized.
	edges []copyEdge
}

type copyEdge struct {
	relType string
	to      string
	// replace means the target face's edges of this type are removed first.
	replace bool
}

// planCopy resolves the definition against the request: reads the source
// face, evaluates the guard, authorizes the write, and merges the target.
//
// # The elevation split lives here (§9.2)
//
//   - SAME-ENTITY copies read the source RAW. Hidden fields travel with the
//     entity, the principal never sees them, and the same policy governs them
//     on the target face — identity preserved, so policy follows. This is the
//     positive form of the never-redact-a-write-prep rule.
//   - CROSS-ENTITY copies read through the CALLER'S visibility gate, so only
//     fields they may read carry. An elevated cross-entity copy would launder
//     fields the principal cannot read into an entity with a different
//     audience — a redaction bypass, and the reason `fields: all` is a load
//     error on that form.
func (ce *copyEngine) planCopy(
	ctx context.Context, req CopyRequest, def metamodel.CopyDef,
) (*copyPlan, error) {
	from, err := metamodel.ParseCopyTarget(def.From)
	if err != nil {
		return nil, fmt.Errorf("entitymanager: copy %q: from: %w", req.Definition, err)
	}
	to, err := metamodel.ParseCopyTarget(def.To)
	if err != nil {
		return nil, fmt.Errorf("entitymanager: copy %q: to: %w", req.Definition, err)
	}

	plan := &copyPlan{
		name: req.Definition, def: def, from: from, to: to,
		sourceID: req.SourceID, targetID: req.SourceID,
		sourceTail: entity.Face(from.Face),
		targetTail: entity.Face(to.Face),
	}
	if def.IsSameEntity() {
		if req.TargetID != "" {
			return nil, fmt.Errorf("%w: %q", ErrCopyTargetNotAllowed, req.Definition)
		}
	} else {
		if req.TargetID == "" {
			return nil, fmt.Errorf("%w: %q", ErrCopyTargetRequired, req.Definition)
		}
		plan.targetID = req.TargetID
	}

	src, err := ce.readCopySource(ctx, def, plan)
	if err != nil {
		return nil, err
	}
	if err := ce.probeCopyTarget(ctx, plan); err != nil {
		return nil, err
	}
	if err := ce.authorizeCopy(ctx, plan); err != nil {
		return nil, err
	}
	if err := ce.buildCopyTarget(ctx, plan, src); err != nil {
		return nil, err
	}
	return plan, nil
}

// probeCopyTarget records whether the target face already exists, and refuses
// a cross-entity target stored under another type.
//
// It runs BEFORE authorizeCopy because the write verdict depends on the
// answer: a stored target is UPDATED, so the principal needs `update` on it,
// where a fresh one needs `create`. Authorizing create and then discovering
// the row exists (the earlier order) let a create-only principal overwrite
// any entity by naming it as the target.
//
// Reading "does the target exist" must not fold a backend failure into "no":
// a transient read error would otherwise become a CreateEntity over a live
// face — a silent full overwrite whose audit record cheerfully says
// created=true. Only ErrNotFound means absent.
func (ce *copyEngine) probeCopyTarget(ctx context.Context, plan *copyPlan) error {
	existing, err := ce.m.deps.Store.GetEntityState(ctx, plan.targetID, plan.targetTail)
	switch {
	case err == nil:
		if existing.Type != plan.to.Type {
			return fmt.Errorf("%w: %s is %q, definition targets %q",
				ErrCopyTargetTypeMismatch, plan.targetID, existing.Type, plan.to.Type)
		}
		plan.existing = existing
	case errors.Is(err, store.ErrNotFound):
		plan.created = true
	default:
		return fmt.Errorf("entitymanager: copy %q: probe target face: %w", plan.name, err)
	}
	return nil
}

// readCopySource performs the elevation split's read half.
func (ce *copyEngine) readCopySource(
	ctx context.Context, def metamodel.CopyDef, plan *copyPlan,
) (*entity.Entity, error) {
	ptr := plan.sourceTail

	if def.IsSameEntity() {
		// RAW and elevated. The read feeds a write, and a redacted read that
		// feeds a write destroys the hidden fields it could not see — the
		// precise bug the never-redact-a-write-prep rule pins.
		e, err := ce.m.deps.Store.GetEntityState(ctx, plan.sourceID, ptr)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrCopySourceMissing, plan.sourceID)
		}
		return e, nil
	}

	// Cross-entity: through the caller's gate. A nil gate means no ACL is
	// wired, which is the CLI/no-policy case — the raw read is then what
	// every other read on that deployment already does.
	if ce.m.deps.CopyVisibility == nil {
		e, err := ce.m.deps.Store.GetEntityState(ctx, plan.sourceID, ptr)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrCopySourceMissing, plan.sourceID)
		}
		return e, nil
	}
	e, ok, err := ce.m.deps.CopyVisibility.Get(ctx, plan.from.Type, plan.sourceID, ptr)
	if err != nil {
		return nil, err
	}
	if !ok {
		// Denied and absent are indistinguishable, by design: whether an
		// entity exists is a genuine secret.
		return nil, fmt.Errorf("%w: %s", ErrCopySourceMissing, plan.sourceID)
	}
	return e, nil
}

// authorizeCopy authorizes the copy.
//
// # Three checks, and why none of them is redundant
//
// §9.2 says a guarded state is writable ONLY by copy definitions naming it as
// target, "each carrying its own guard". That sentence is CONDITIONED on the
// guard existing — it is not a license to skip authorization when a
// definition happens to omit one, which is the hole an earlier cut of this
// function shipped: a principal holding nothing could promote a stranger's
// draft, including fields they could not read.
//
//  1. READ on the source, always. A copy is a read followed by a write, and
//     the elevation split is about which FIELDS travel, not about whether
//     the principal may touch the entity at all. Without this, elevation
//     becomes a way to read what you cannot read.
//  2. The definition's `guard:`, resolved per-entity via
//     HoldsPermissionForEntity, so "the owner of THIS doc may publish it"
//     works without a global grant. Mandatory at LOAD for any definition
//     targeting a non-default face, so the sentence above always applies.
//  3. WRITE on the target, as the ordinary verb the copy performs: `create`
//     when the target face does not exist yet, `update` when it does. A
//     cross-entity copy creates or overwrites an entity with its own
//     audience, so it must not become a way to write entities the principal
//     could not write by hand — and "by hand" includes overwriting an
//     existing one, which is why the probe precedes this function.
//
// A GUARDED copy BETWEEN TWO FACES OF ONE ENTITY is exempt from (3): (2)
// stands in its place. Three conditions, each load-bearing:
//
//   - `Guard.Permission != ""`, not "the target is a non-bare face". Those
//     coincide in one direction only (a non-bare target makes a guard
//     mandatory at LOAD), and keying on the face inverted the rule: a promote
//     into the BARE face demanded `update` on the target type, which is the
//     very grant that makes the face editable by hand — what the guard exists
//     to prevent. Nobody holds `update` on a guarded face by design, so
//     requiring (3) made the shape unexpressible in BOTH directions.
//   - `IsSameEntity()`, so the caller chooses no target: planCopy pins
//     targetID to the source and refuses a caller-supplied one.
//   - `sourceTail != targetTail`, because the whole argument above is about
//     MOVING content between two declared faces. `IsSameEntity()` compares
//     types, not faces, so it also admits `from: t` / `to: t` — a definition
//     whose endpoints coincide, on a type that may declare no faces at all.
//     There is no guarded face there and no promote; the target is the
//     ordinary row `update` governs, so exempting it would hand out
//     "mutate this entity in place, authorized by a permission noun".
//
// Everything else keeps (3). An UNGUARDED same-entity copy is not exempt
// whichever face it targets: an unguarded `revert` into the bare face is
// declarable, and without (3) it was a write anyone who could read the
// published face could perform under a read-only ACL. Reverting the draft is
// editing the draft. A CROSS-entity copy is never exempt, guard or not —
// there the caller picks the target, so a guard-overrules rule would let one
// guard permission write entities the principal could not write by hand; it
// keeps (3) plus the per-edge authorization below.
//
// ELEVATION is not a supported copy path, and the exemption assumes so. A
// bypassACL Manager would neuter only (3) — (1) and (2) go through
// CopyReadGate and CopyGuard, which never consult it — so an elevated copy
// would be authorized in a way nobody designed. No elevated handle carries
// CopyState today: Manager.Elevated returns an autocascade.Mutator, which is
// six write methods without it. Before widening that, decide what elevation
// MEANS for (1) and (2) — waiving (3) alone is not an answer.
//
// A guarded `fields: all` copy writes every property of the target, including
// ones the caller cannot see, and (1) does NOT bound that: PermitsReadFace is
// a row-and-face verdict, not a field one. Publishing the whole document IS
// the operation, so this is intended — but note it is a full-record replace
// (buildCopyTarget discards the existing properties under AllFields), so a
// property living only on the target face does not survive. What (1) does
// bound is coarser and still worth having: the guard says "you may perform
// this promotion", never "you may read this document".
func (ce *copyEngine) authorizeCopy(ctx context.Context, plan *copyPlan) error {
	// (1) READ on the source, at the SOURCE FACE. Same-entity copies read RAW
	// afterwards, which is about hidden FIELDS traveling with the entity — it
	// is not a license to read an entity, or a face of it, the principal has
	// no access to.
	if ce.m.deps.CopyReadGate != nil {
		permitted, err := ce.m.deps.CopyReadGate.PermitsReadFace(
			ctx, plan.from.Type, plan.sourceID, plan.sourceTail)
		if err != nil {
			return err
		}
		if !permitted {
			// Indistinguishable from absent, matching every other read gate.
			return fmt.Errorf("%w: %s", ErrCopySourceMissing, plan.sourceID)
		}
	}

	// Computed ONCE and reused by the exemption below: the exemption is only
	// safe because the guard above already ran, and two textually separated
	// spellings of the same condition could drift apart under a later edit.
	perm := plan.def.Guard.Permission
	guarded := perm != ""
	if guarded {
		if ce.m.deps.CopyGuard == nil {
			// A guarded copy with no guard implementation fails CLOSED,
			// matching the statemachine's nil-guard rule: a guarded edge with
			// no guard is denied, never waved through.
			return &acl.ForbiddenError{Decision: acl.Decision{
				RuleKind: "copy-guard", RuleID: perm,
				Reason: fmt.Sprintf(
					"copy %q requires permission %q but no guard is wired",
					plan.name, perm),
			}}
		}
		if !ce.m.deps.CopyGuard.HoldsPermission(ctx, plan.sourceID, perm) {
			return &acl.ForbiddenError{Decision: acl.Decision{
				RuleKind: "copy-guard", RuleID: perm,
				Reason: fmt.Sprintf(
					"copy %q requires permission %q on %q",
					plan.name, perm, plan.sourceID),
			}}
		}
	}

	if guarded && plan.def.IsSameEntity() && plan.sourceTail != plan.targetTail {
		// (3) does not apply to a guarded face-to-face copy — see above. The
		// guard checked just now IS the authorization for this write.
		return nil
	}
	op := acl.OpUpdate
	if plan.created {
		op = acl.OpCreate
	}
	return ce.m.authorizeAndAudit(ctx, acl.WriteRequest{
		Op:      op,
		Subject: acl.NewEntitySubject(plan.to.Type, plan.targetID, plan.targetTail),
	})
}

// buildCopyTarget merges the source into the target face.
//
// Mapped fields are WRITTEN, unmapped fields are UNTOUCHED on an existing
// target (§9.1's partial-copy semantics); `fields: all` is the full-replace
// promote case, and is only reachable on a same-entity copy because the
// cross-entity form rejects it at load.
//
// The merged target then passes the same structural checks a hand-written
// create or update would: metamodel validation (hard errors only — the
// DEC-HWZHA split) and `unique:` natural keys. The kernel writes straight to
// the store view, so without this a copy was the one entry point that could
// persist a duplicate slug or an out-of-range enum.
func (ce *copyEngine) buildCopyTarget(
	ctx context.Context, plan *copyPlan, src *entity.Entity,
) error {
	target := &entity.Entity{
		ID: plan.targetID, Type: plan.to.Type, Face: plan.targetTail,
		Properties: map[string]any{},
	}
	if plan.existing != nil {
		target.Properties = maps.Clone(plan.existing.Properties)
		if target.Properties == nil {
			target.Properties = map[string]any{}
		}
		target.Content = plan.existing.Content
	}

	if plan.def.AllFields {
		target.Properties = maps.Clone(src.Properties)
		if target.Properties == nil {
			target.Properties = map[string]any{}
		}
		target.Content = src.Content
	}
	for field, tmpl := range plan.def.Fields {
		if v, ok := copyFieldValue(tmpl, src); ok {
			target.Properties[field] = v
		} else {
			// The source has no such property: unset rather than write the
			// empty string a string interpolation would produce, which for
			// an integer or list property is a type violation.
			delete(target.Properties, field)
		}
	}

	hard, _ := partitionValidationErrors(
		ce.m.deps.Meta.ValidateEntity(target.ID, target.Type, target.Properties))
	if len(hard) > 0 {
		return newValidationError(hard)
	}
	// Checked again inside the copy's transaction (applyCopy); this early
	// pass fails the copy before any edge planning when it cannot succeed.
	if err := checkUniqueProperties(ctx, ce.m.deps.Meta, ce.m.deps.Store, target, target.ID); err != nil {
		return err
	}

	plan.entity = target
	edges, err := ce.planCopyEdges(ctx, plan)
	if err != nil {
		return err
	}
	plan.edges = edges
	return nil
}

// planCopyEdges resolves which edges the definition copies, reading the
// SOURCE face's outgoing relations.
//
// An omitted relation type is NOT copied, and that default is load-bearing:
// copying a role-conferring edge grants roles on the target, so a definition
// must name a type before its edges travel (§9.2's first mitigation).
//
// For a CROSS-ENTITY copy each edge is additionally authorized as the acting
// principal — the mandatory runtime half of that mitigation. A copy can
// never create an edge the principal could not create by hand. Same-entity
// elevated copies only touch the entity's own state edges, so the concern
// does not arise there.
func (ce *copyEngine) planCopyEdges(ctx context.Context, plan *copyPlan) ([]copyEdge, error) {
	if len(plan.def.Relations) == 0 {
		return nil, nil
	}
	srcTail := plan.sourceTail
	crossEntity := !plan.def.IsSameEntity()

	var out []copyEdge
	for rel, err := range ce.m.deps.Store.ListRelations(ctx, store.RelationQuery{
		From: plan.sourceID, FromFace: &srcTail,
	}) {
		if err != nil {
			return nil, fmt.Errorf("entitymanager: copy %q: read source edges: %w",
				plan.name, err)
		}
		mode, named := plan.def.Relations[rel.Type]
		if !named {
			continue
		}
		if crossEntity {
			if aerr := ce.m.authorizeAndAudit(ctx, acl.WriteRequest{
				Op: acl.OpCreate,
				Subject: acl.RelationSubject{
					Type: rel.Type, FromType: plan.to.Type, FromID: plan.targetID,
					// The face applyCopyEdges actually writes these edges to
					// (BUG-64MU2Q). Omitting it asked about the default face
					// while the write landed on targetTail — the check-here/
					// write-there split the entity-level gate above already
					// avoids by carrying EntitySubject.Face.
					FromFace: plan.targetTail,
				},
			}); aerr != nil {
				return nil, aerr
			}
		}
		out = append(out, copyEdge{
			relType: rel.Type, to: rel.To, replace: mode == "replace",
		})
	}
	return out, nil
}

// recordCopyAudit emits one record per definition invocation, naming the
// definition, source, target and principal — modeled on OpPurgeVersion's
// shape (§9.1), which records WHAT was done to WHICH subject and never the
// content.
//
// It does NOT reuse purge's Manager bypass: purge is a store-level
// destructive op with no entity write, whereas a copy IS an entity write and
// must not have weaker authorization or attribution than a property edit.
func (ce *copyEngine) recordCopyAudit(ctx context.Context, plan *copyPlan, res CopyResult) {
	p := principal.From(ctx)
	ce.m.deps.Audit.Record(audit.Record{
		Op: audit.OpCopyState,
		Subject: &audit.Subject{
			Kind: "entity", Type: plan.to.Type, ID: plan.targetID,
		},
		Principal:   p,
		TriggeredBy: audit.TriggeredByFrom(ctx),
		Summary: fmt.Sprintf(
			"copy %q: %s -> %s (created=%t)",
			plan.name, copyFaceLabel(plan.sourceID, plan.from),
			copyFaceLabel(plan.targetID, plan.to), res.Created),
	})
}

func copyFaceLabel(id string, t metamodel.CopyTarget) string {
	if t.Face == "" {
		return id
	}
	return id + "@" + t.Face
}

// CopyState executes a declared copy definition. See [copyEngine.copyState]
// for the full contract; this is the Manager-facing entry point and delegates
// without deciding anything.
func (m *Manager) CopyState(ctx context.Context, req CopyRequest) (*CopyResult, error) {
	return (&copyEngine{m: m}).copyState(ctx, req)
}
