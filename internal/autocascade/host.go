// Package autocascade executes the side effects of automation results
// produced by [automation.Engine]. Engine is the pure decision-maker;
// Runner here is the executor that turns its [automation.Result] into
// actual writes, recursive automation invocation, and Lua script
// execution.
//
// Runner does not hold a back-reference to whatever invokes it. Instead
// it declares a [Host] interface naming exactly the methods it calls
// back into, and the caller passes a Host on every [Runner.Process]
// call. This is the consumer-side-interface pattern documented in
// CLAUDE.md under "Consumer-side interfaces for callbacks and cycles".
// During the workspace decomposition (FEAT-workspace), Workspace
// satisfies Host directly; once entitymanager.Manager exists, it will
// satisfy Host instead. Per-call passing avoids the constructor cycle
// either way.
package autocascade

import (
	"context"

	"github.com/Sourcehaven-BV/rela/internal/entity"
)

// Host is what Runner needs from its caller to execute a cascade.
// Each method documents where Runner invokes it. The interface is
// narrow on purpose — only the operations Runner actually calls.
//
// Every method takes a context — Runner threads its per-Process ctx
// (which carries `principal.Principal` from the originator and
// `audit.TriggeredBy` set by Runner before script execution) so the
// Host's audit emission inherits the right attribution.
type Host interface {
	// CreateEntity creates a new entity from the supplied options
	// (ID generation, template application, validation, persistence)
	// and returns the result.
	//
	// **Contract:** the implementation must NOT fire follow-up
	// automation cascades from within this call. Runner is the one
	// that schedules cascade evaluation on the returned entity, and
	// double-cascading would enforce [MaxDepth] twice and reorder
	// entity creation. The bare write semantics are roughly
	// "everything workspace.createEntityCore does, including
	// validation, minus the post-write automation event."
	CreateEntity(ctx context.Context, entityType string, opts CreateEntityOptions) (*entity.Entity, error)

	// WriteEntity persists set — the [automation.Result.PropertiesSet] of
	// an on-create automation — onto e, an entity that already went through
	// [Host.CreateEntity] earlier in the cascade. No ID generation, no
	// template, no automation.
	//
	// It takes the property set rather than a whole entity because the row
	// may have changed since the cascade created it: the host applies set to
	// the STORED row, so a concurrent writer's change is kept.
	WriteEntity(ctx context.Context, e *entity.Entity, set map[string]string) error

	// GetEntity reads an entity by ID. Runner uses it to verify that
	// targets of automation-generated relations exist before
	// validating the (from-type, type, to-type) tuple.
	GetEntity(ctx context.Context, id string) (*entity.Entity, error)

	// WriteRelation creates the relation in the store, treating a
	// pre-existing identical relation as a no-op success (create-then-
	// noop-on-conflict, never an update — cascade relations are
	// property-less, so there is nothing to overwrite). Runner uses it
	// for entries in [automation.Result.RelationsToCreate] and for
	// trigger relations attached to automation-created entities.
	WriteRelation(ctx context.Context, r *entity.Relation) error

	// ValidateRelation checks whether a relation of the given type
	// from `fromType` to `toType` is admissible per the active
	// metamodel. Runner calls it before persisting automation-
	// generated relations.
	ValidateRelation(relType, fromType, toType string) error

	// DeleteEntity removes an entity, cascading to its relations
	// when cascade is true. Runner calls this only via the
	// IfExistsReplace path of [automation.EntityToCreate].
	//
	// entityType is informational — implementations may ignore it.
	// It's in the signature so alternative implementations have the
	// type at hand without re-fetching.
	DeleteEntity(ctx context.Context, entityType, id string, cascade bool) error

	// FindExistingRelationTarget returns the existing target entity
	// of the given relation type from the source entity, if any.
	// Returns nil if no such relation exists. Runner uses it to
	// implement the IfExists behaviors (Skip / Error / Replace).
	FindExistingRelationTarget(ctx context.Context, sourceID, relationType, targetType string) *entity.Entity
}

// CreateEntityOptions configures a [Host.CreateEntity] call.
type CreateEntityOptions struct {
	// ID is a caller-supplied ID. Empty means auto-generate from
	// IDPrefix or the metamodel-defined default prefix for the type.
	ID string

	// IDPrefix overrides the default ID prefix from the metamodel.
	// Ignored when ID is non-empty or when the entity type uses
	// manual IDs.
	IDPrefix string

	// TemplateVariant selects an entity template variant. Empty
	// means the default template.
	TemplateVariant string

	// Properties seeds the new entity's properties, overriding
	// template defaults.
	Properties map[string]any

	// Content is the markdown body content. Overrides the template's
	// content when non-empty.
	Content string
}
