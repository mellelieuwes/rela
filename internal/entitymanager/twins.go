package entitymanager

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Sourcehaven-BV/rela/internal/canonical"
	"github.com/Sourcehaven-BV/rela/internal/entity"
)

// twinBodyField and twinAllFields are the reserved field names of the
// ownership map [TwinOwnership.OwnedFields] returns: the markdown content,
// and "every field". They mirror metamodel.PactBodyField and
// metamodel.PactAllFields; the twins service builds the map from those.
const (
	twinBodyField = "body"
	twinAllFields = "*"
)

// TwinOwnership answers which fields of an entity an external system owns
// through a twin (a Pact's `theirs` fields). Defined at the call site
// (CLAUDE.md consumer-side interfaces); the twins service satisfies it.
//
// The map is keyed by field name — a property, "body" for the content, or
// "*" when a pact owns every field — and lists the owning systems. A nil or
// empty map means the entity has no twin, or no twin owns anything.
type TwinOwnership interface {
	OwnedFields(ctx context.Context, entityType, entityID string) (map[string][]string, error)
}

// NoTwinOwnership reports that no field is externally owned. It is the
// explicit opt-out for a deployment whose schema declares no pacts and for
// tests — named and passed deliberately, never the result of leaving
// [Deps.Twins] nil.
type NoTwinOwnership struct{}

// OwnedFields implements [TwinOwnership] by owning nothing.
func (NoTwinOwnership) OwnedFields(context.Context, string, string) (map[string][]string, error) {
	return nil, nil //nolint:nilnil // a nil map owns nothing, per the [TwinOwnership] contract.
}

// TheirsWriteError reports a caller-authored change to a field an external
// system owns through a twin. The change belongs in that system; the twin
// sync then brings it into rela. Callers may errors.As this into a
// surface-specific 422 response.
type TheirsWriteError struct {
	Type    string
	ID      string
	Fields  []string // sorted; "body" names the content
	Systems []string // sorted owners of those fields
}

func (e *TheirsWriteError) Error() string {
	noun, verb := "field", "is"
	if len(e.Fields) > 1 {
		noun, verb = "fields", "are"
	}
	return fmt.Sprintf("%s %s of %s %s owned by %s (Twin); change it there",
		noun, strings.Join(e.Fields, ", "), e.ID, verb, strings.Join(e.Systems, ", "))
}

// TwinSyncWriter returns a throwaway handle over m whose PatchEntity and
// UpdateEntity skip the twin-ownership check and nothing else: ACL,
// validation, transitions, unique constraints and audit all still apply.
// It is the write capability of the twin sync, which is how an owned field
// legitimately changes in rela. Only appbuild hands it out, to the twins
// service.
//
// A package function rather than a method because Manager is at its method
// cap. The capability never propagates: a cascade the sync write triggers
// dispatches through gated(), which strips it, so an automation or Lua
// script cannot use it to write owned fields.
func TwinSyncWriter(m *Manager) *Manager {
	return &Manager{deps: m.deps, twinSync: true}
}

// rejectTheirsChanges refuses a caller-authored update that changes a field
// an external system owns through a twin. It is change-based, like
// [rejectComputedChanges]: an owned field whose value is unchanged — a
// same-value save, a whole-entity form save, a `rela sync push` — passes.
// Values compare with canonical's normalizing equality, so a decoder's
// []any vs []string or int vs float64 is not a change.
//
// It runs on [Manager.PatchEntity] (after the merge),
// [Manager.UpdateEntity] and [Manager.CopyState] into an existing target,
// including under elevation: bypass_acl lifts the
// ACL, not the ownership contract. Only the [TwinSyncWriter] handle skips
// it. System writes are not gated, for the reason [Deps.FieldGate] gives:
// automation `set` actions (applied inside updateCore), cascade writes
// through the cascade host, and [Manager.ApplyEntity] (the replica
// channel). Automation Lua scripts write through gated().PatchEntity and
// ARE gated.
//
// A lookup error fails closed: the write is refused with that error, since
// "cannot tell who owns this" must not become "nobody owns this".
func rejectTheirsChanges(ctx context.Context, deps Deps, old, updated *entity.Entity) error {
	owned, err := deps.Twins.OwnedFields(ctx, old.Type, old.ID)
	if err != nil {
		return fmt.Errorf("entitymanager: twin ownership of %s: %w", old.ID, err)
	}
	if len(owned) == 0 {
		return nil
	}
	var fields, systems []string
	reject := func(field string) {
		fields = append(fields, field)
		systems = append(systems, owned[field]...)
		systems = append(systems, owned[twinAllFields]...)
	}
	isOwned := func(field string) bool {
		return len(owned[field]) > 0 || len(owned[twinAllFields]) > 0
	}
	for name, nv := range updated.Properties {
		if isOwned(name) && !canonical.EqualValue(old.Properties[name], nv) {
			reject(name)
		}
	}
	for name, ov := range old.Properties {
		if _, kept := updated.Properties[name]; !kept && isOwned(name) && !canonical.EqualValue(ov, nil) {
			reject(name)
		}
	}
	if isOwned(twinBodyField) && !canonical.EqualBody(old.Content, updated.Content) {
		reject(twinBodyField)
	}
	if len(fields) == 0 {
		return nil
	}
	slices.Sort(fields)
	slices.Sort(systems)
	return &TheirsWriteError{
		Type: old.Type, ID: old.ID,
		Fields: slices.Compact(fields), Systems: slices.Compact(systems),
	}
}
