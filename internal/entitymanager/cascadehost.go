package entitymanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/autocascade"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// cascadeHost satisfies [autocascade.Host]. It is the surface
// [autocascade.Runner] calls back into during a cascade. cascadeHost
// is constructed per-call inside [Manager.CreateEntity] /
// [Manager.UpdateEntity] (never held as a field) so its lifetime is
// scoped to a single Process call — the form CLAUDE.md's
// "consumer-side interfaces for callbacks" pattern endorses for
// dissolving cycles.
//
// **Important contract:** [cascadeHost.CreateEntity] must NOT fire
// follow-up automation cascades. Runner is the one that schedules
// cascade evaluation on the returned entity; double-cascading would
// enforce [autocascade.MaxDepth] twice and reorder creations.
//
// Audit: cascadeHost emits audit records directly (bypassing
// Manager's recordEntityAudit / recordRelationAudit) because it
// bypasses Manager itself — going through createCore / direct store
// writes to avoid double-cascading. Records carry a triggered_by label
// distinguishing them from direct writes: normally
// `automation:<name>`, stamped on the ctx by the autocascade runner
// (TKT-JJRVX9); `cascade:delete-entity:<id>` for the relation deletes
// under an IfExistsReplace; and a bare `automation` only when the
// automation was declared without a name.
type cascadeHost struct {
	deps Deps
}

// Compile-time assertion.
var _ autocascade.Host = (*cascadeHost)(nil)

// CreateEntity satisfies [autocascade.Host.CreateEntity]. It calls
// the package-level [createCore] helper directly, **without**
// running automations afterward (Runner manages follow-up cascade
// scheduling on the result).
func (h *cascadeHost) CreateEntity(
	ctx context.Context, entityType string, opts autocascade.CreateEntityOptions,
) (*entity.Entity, error) {
	// Cascade-driven creates discard warnings — the autocascade.Host
	// contract returns only (*entity.Entity, error). The Runner doesn't
	// propagate per-step warnings; they'd be merged into the trigger's
	// entity.CreateResult.Warnings if we extended Outcome, but that's a
	// separate change.
	// A cascade cannot name a face: autocascade.CreateEntityOptions carries
	// none, and the automation DSL has no syntax for one. Refusing is the
	// fail-closed answer for a faced type — writing the zero coordinate would
	// mint a row belonging to NO declared face, which nothing can address and
	// no grant covers (BUG-HC6I2T). requireCreateFace states the same rule the
	// ordinary create path enforces, so the two cannot drift.
	if err := h.deps.requireCreateFaceFor(entityType, ""); err != nil {
		return nil, err
	}
	e, _, err := createCore(ctx, h.deps, entityType, createCoreOpts{
		ID:              opts.ID,
		IDPrefix:        opts.IDPrefix,
		TemplateVariant: opts.TemplateVariant,
		Properties:      opts.Properties,
		Content:         opts.Content,
	})
	if err == nil {
		h.recordCascade(ctx, audit.OpCreateEntity, entitySubject(e), "created")
	}
	return e, err
}

// WriteEntity satisfies [autocascade.Host.WriteEntity] by applying set to an
// already-persisted entity.
//
// It is an UPDATE, never an upsert: the Runner only calls WriteEntity to
// persist post-cascade property changes onto an entity it created via
// CreateEntity earlier in the SAME cascade, so the row is guaranteed to
// exist. A create-then-update fallback here would be the lost-update /
// type-re-type vector removed in BUG-ZWTDH9.
//
// It is a compare-and-swap onto the stored row (see
// [writeAutomationProperties]), so a patch that landed after the cascade's
// create is kept.
//
// Note: no audit record here. The earlier CreateEntity already produced
// one audit record for this entity; emitting another for the property-set
// step would double-count the same creation in the audit log.
//
// Constraints are RE-CHECKED against the post-automation values (BUG-KIMZRK).
// createCore validated the candidate as it stood BEFORE automation ran, so a
// value an automation introduces afterwards has never been examined. The
// top-level create path already re-runs the same checks for exactly this
// reason ("the create path must not be the weaker one" — see CreateEntity);
// without them here, an automation could silently persist a duplicate of a
// `unique:` natural key, or a value validation would reject.
func (h *cascadeHost) WriteEntity(ctx context.Context, e *entity.Entity, set map[string]string) error {
	if e == nil { // coverage-ignore: defensive: Runner only calls WriteEntity with an entity it created earlier in the
		// same cascade; never nil
		return nil
	}
	// writeAutomationProperties rejects a computed property in set, so the
	// check needs no computed comparison: next's computed values were just
	// re-evaluated and may legitimately differ from stored's.
	_, err := writeAutomationProperties(ctx, h.deps, e, set, func(_, next *entity.Entity) error {
		if errs := h.deps.Meta.ValidateEntity(next.ID, next.Type, next.Properties); len(errs) > 0 {
			// DEC-HWZHA: only HARD errors abort. Soft conditions (a required
			// property left unset, an out-of-enum value) are tolerated on every
			// other write path and must stay tolerated here, or a cascade would
			// be stricter than a direct edit.
			if hard, _ := partitionValidationErrors(errs); len(hard) > 0 {
				return newValidationError(hard)
			}
		}
		// EnforceCreate, not EnforceUpdate: this row was created moments ago
		// in this same cascade, so the automation's value is still an ENTRY
		// value — it must equal the machine's declared entry, not merely be
		// reachable from it by a legal move. Using EnforceUpdate would wrongly
		// accept a one-hop jump the create path forbids.
		return h.deps.Transitions.EnforceCreate(ctx, next)
	})
	return err
}

// GetEntity satisfies [autocascade.Host.GetEntity] by forwarding to
// the store.
func (h *cascadeHost) GetEntity(ctx context.Context, id string) (*entity.Entity, error) {
	return h.deps.Store.GetEntity(ctx, id)
}

// WriteRelation satisfies [autocascade.Host.WriteRelation] by CREATING
// the automation-generated relation.
//
// It is a create, never an upsert: the Runner only calls WriteRelation
// for freshly built [automation.Result.RelationsToCreate] and trigger
// relations, so the intent is always create. A store.ErrConflict means
// the identical triple already exists — an idempotent re-trigger of the
// same automation, or a concurrent writer that created the same triple
// first. Either way the relation exists, so treat it as a no-op success
// rather than blindly overwriting it (which was the removed create-then-update fallback,
// BUG-ZWTDH9); no audit record is emitted for the no-op since nothing
// was written.
func (h *cascadeHost) WriteRelation(ctx context.Context, r *entity.Relation) error {
	if r == nil { // coverage-ignore: defensive: Runner only calls WriteRelation with freshly built
		// RelationsToCreate/trigger relations; never nil
		return nil
	}
	if _, err := h.deps.Store.CreateRelation(ctx, r.From, r.Type, r.To, &store.RelationData{
		Properties: r.Properties,
		Content:    r.Content,
	}); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil
		}
		return err
	}
	h.recordCascade(ctx, audit.OpCreateRelation, relationSubject(r), "created")
	return nil
}

// ValidateRelation satisfies [autocascade.Host.ValidateRelation] by
// delegating to the metamodel.
func (h *cascadeHost) ValidateRelation(relType, fromType, toType string) error {
	return h.deps.Meta.ValidateRelation(relType, fromType, toType)
}

// DeleteEntity satisfies [autocascade.Host.DeleteEntity]. It mirrors
// [Manager.DeleteEntity]'s incident-relation handling. The entityType
// parameter is informational — the store looks up the type from the
// entity itself.
//
// triggered_by attribution: invoked only from the IfExistsReplace
// path. Stamp `cascade:delete-entity:<id>` on the ctx so the
// cascaded relation deletes are attributed to the replacement
// operation, matching the direct-DeleteEntity convention in Manager.
func (h *cascadeHost) DeleteEntity(ctx context.Context, _, id string, cascade bool) error {
	current, err := h.deps.Store.GetEntity(ctx, id)
	if err != nil {
		// Not an ACL bypass (the triggering automation is already
		// authorized), but reporting a transient store error as "missing"
		// would make a cascade silently skip a replacement it should have
		// performed. Surface the real cause.
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		return fmt.Errorf("%w: %s", ErrEntityNotFound, id)
	}

	incoming, err := collectIncidentRelations(ctx, h.deps.Store, id, store.DirectionIncoming)
	if err != nil {
		return fmt.Errorf("collect incoming relations for %q: %w", id, err)
	}
	outgoing, err := collectIncidentRelations(ctx, h.deps.Store, id, store.DirectionOutgoing)
	if err != nil {
		return fmt.Errorf("collect outgoing relations for %q: %w", id, err)
	}
	if (len(incoming)+len(outgoing)) > 0 && !cascade {
		return ErrHasRelations
	}

	// Delegate to the store's cascade (single lock, fail-secure on a
	// relation-file error) and audit exactly what it reports deleting, the
	// same way Manager.DeleteEntity does. A real error surfaces instead of
	// being swallowed, so a replacement never leaves orphaned relations
	// behind a deleted entity (issue #888).
	res, delErr := h.deps.Store.DeleteEntity(ctx, id, cascade)
	if delErr != nil {
		// Same partial-cascade rule as Manager.DeleteEntity (issue #929): a
		// non-transactional backend reports the relations it already removed,
		// and those removals stick, so they must reach the log. Relations
		// only — the entity survived. Kept in step with the manager's path
		// deliberately: an if_exists:replace delete and a direct delete must
		// not log differently for the same failure.
		if res != nil {
			for _, rel := range res.DeletedRelations {
				h.recordCascade(
					audit.WithTriggeredBy(ctx, "cascade:delete-entity:"+id),
					audit.OpDeleteRelation, relationSubject(rel), "deleted")
			}
		}
		return fmt.Errorf("delete entity: %w", delErr)
	}

	cascadeCtx := ctx
	if cascade && len(res.DeletedRelations) > 0 {
		cascadeCtx = audit.WithTriggeredBy(ctx, "cascade:delete-entity:"+id)
	}
	for _, rel := range res.DeletedRelations {
		h.recordCascade(cascadeCtx, audit.OpDeleteRelation, relationSubject(rel), "deleted")
	}
	h.recordCascade(ctx, audit.OpDeleteEntity, entitySubject(current), "deleted")
	return nil
}

// FindExistingRelationTarget satisfies
// [autocascade.Host.FindExistingRelationTarget].
func (h *cascadeHost) FindExistingRelationTarget(
	ctx context.Context, sourceID, relationType, targetType string,
) *entity.Entity {
	return findExistingRelationTarget(ctx, h.deps.Store, sourceID, relationType, targetType)
}

// entitySubject builds the Subject for an entity-shaped audit record.
func entitySubject(e *entity.Entity) *audit.Subject {
	return &audit.Subject{
		Kind: "entity",
		Type: e.Type,
		ID:   e.ID,
	}
}

// relationSubject builds the Subject for a relation-shaped audit record.
func relationSubject(r *entity.Relation) *audit.Subject {
	return &audit.Subject{
		Kind:         "relation",
		RelationType: r.Type,
		FromID:       r.From,
		ToID:         r.To,
	}
}

// recordCascade emits one audit record from the cascade path.
//
// The ctx label wins when there is one: the autocascade runner stamps
// `automation:<name>` per entry, and a caller may have stamped something more
// specific still (`cascade:delete-entity:<id>`). "automation" is only the
// fallback for a cascade whose originating automation had no name — since
// TKT-JJRVX9 it is no longer the normal case, and this `if` is now what makes
// the runner's per-entry label survive rather than what supplies attribution.
//
// The same `if` is mirrored one layer up in autocascade's triggeredByCtx,
// which likewise declines to overwrite an existing label — that is what keeps
// a scheduler's `schedule:<task>` on rows an automation cascaded underneath it.
//
// One emitter for both entity and relation subjects — the Subject
// face carries the shape; the rest of the Record envelope is
// identical.
func (h *cascadeHost) recordCascade(
	ctx context.Context, op string, subject *audit.Subject, summary string,
) {
	if subject == nil { // coverage-ignore: defensive: all callers pass entitySubject()/relationSubject() results, which
		// are never nil
		return
	}
	if audit.TriggeredByFrom(ctx) == "" {
		ctx = audit.WithTriggeredBy(ctx, "automation")
	}
	h.deps.Audit.Record(audit.Record{
		Time:        time.Now().UTC(),
		Op:          op,
		Subject:     subject,
		Principal:   principal.From(ctx),
		TriggeredBy: audit.TriggeredByFrom(ctx),
		Summary:     summary,
	})
}
