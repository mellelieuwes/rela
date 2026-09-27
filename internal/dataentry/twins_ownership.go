package dataentry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	v1 "github.com/Sourcehaven-BV/rela/internal/apiwire/v1"
	entityPkg "github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
)

// ownedFieldsFunc answers which fields of an entity external systems own
// through live twins: field ("body" for the content, "*" for every field) →
// owning systems. Satisfied by the twins service's OwnedFields.
type ownedFieldsFunc func(ctx context.Context, entityType, entityID string) (map[string][]string, error)

// OwnedFields lets an ownedFieldsFunc serve as the attachment service's twin
// guard. A nil func owns nothing, matching [affordanceService.twinOwnership].
func (f ownedFieldsFunc) OwnedFields(ctx context.Context, entityType, entityID string) (map[string][]string, error) {
	if f == nil {
		return nil, nil //nolint:nilnil // a nil map owns nothing, per the ownership contract.
	}
	return f(ctx, entityType, entityID)
}

// fieldOwnership is the twin ownership of one entity, expanded against its
// type: "*" becomes every non-computed property plus the body.
//
// The zero value owns nothing, which is the answer for every entity whose type
// declares no pact.
type fieldOwnership struct {
	// props maps each owned property to its owning systems (sorted). The
	// systems are nil when the lookup failed (unknown).
	props map[string][]string
	// content reports whether the body is owned; contentSystems names by whom.
	content        bool
	contentSystems []string
	// unknown means the lookup failed and every field counts as owned: "cannot
	// tell who owns this" must not become "nobody owns this".
	unknown bool
}

func (o fieldOwnership) ownsProperty(name string) bool {
	_, ok := o.props[name]
	return ok
}

// reason is the field verdict's explanation for an owned field. It is shown
// as-is (the SPA does not recase it, DEC-6C1NAA), so it is written here as the
// sentence a reader sees.
func (o fieldOwnership) reason(systems []string) string {
	if o.unknown {
		return "Twin ownership could not be determined"
	}
	return "Owned by " + strings.Join(systems, ", ")
}

// denial refuses a change to the owned field of e. Its reason is the same
// sentence [entitymanager.TheirsWriteError] carries, so a write refused here and
// one refused by the manager's own guard read identically.
func (o fieldOwnership) denial(e *entityPkg.Entity, field string) *AffordanceDenialError {
	d := &AffordanceDenialError{Rule: RuleFieldExternallyOwned, Path: field}
	if o.unknown {
		d.Reason = fmt.Sprintf("twin ownership of %s could not be determined; write refused", e.ID)
		return d
	}
	systems := o.contentSystems
	if field != metamodel.PactBodyField {
		systems = o.props[field]
	}
	d.Reason = (&entitymanager.TheirsWriteError{
		Type: e.Type, ID: e.ID, Fields: []string{field}, Systems: systems,
	}).Error()
	return d
}

// twinOwnership resolves the twin ownership of e, whose type is def.
func (svc affordanceService) twinOwnership(
	ctx context.Context, e *entityPkg.Entity, def *metamodel.EntityDef,
) fieldOwnership {
	return resolveTwinOwnership(ctx, svc.owned, e, def)
}

// resolveTwinOwnership resolves the twin ownership of e, whose type is def,
// through owned.
//
// The lookup runs only for a stored entity of a type that declares a pact: a
// create candidate has no twin yet, and a type without a pact cannot be
// twinned, so neither pays for it. A failed lookup fails closed.
func resolveTwinOwnership(
	ctx context.Context, owned ownedFieldsFunc, e *entityPkg.Entity, def *metamodel.EntityDef,
) fieldOwnership {
	if owned == nil || e == nil || e.ID == "" || def == nil || len(def.Pacts) == 0 {
		return fieldOwnership{}
	}
	raw, err := owned(ctx, e.Type, e.ID)
	if err != nil {
		slog.Warn("dataentry: twin ownership lookup failed; treating every field as owned",
			"entity", e.ID, "err", err)
		o := fieldOwnership{unknown: true, content: true, props: map[string][]string{}}
		for name, pd := range def.Properties {
			if pd.Computed == "" {
				o.props[name] = nil
			}
		}
		return o
	}
	return expandOwnership(raw, def)
}

// expandOwnership turns the service's ownership map into per-property and
// body ownership for a type. Computed properties are never owned.
func expandOwnership(raw map[string][]string, def *metamodel.EntityDef) fieldOwnership {
	if len(raw) == 0 {
		return fieldOwnership{}
	}
	all := raw[metamodel.PactAllFields]
	var o fieldOwnership
	for name, pd := range def.Properties {
		if pd.Computed != "" {
			continue
		}
		if systems := mergeSystems(raw[name], all); len(systems) > 0 {
			if o.props == nil {
				o.props = map[string][]string{}
			}
			o.props[name] = systems
		}
	}
	o.contentSystems = mergeSystems(raw[metamodel.PactBodyField], all)
	o.content = len(o.contentSystems) > 0
	return o
}

// mergeSystems unions two system lists, sorted and deduplicated.
func mergeSystems(a, b []string) []string {
	if len(a)+len(b) == 0 {
		return nil
	}
	out := slices.Concat(a, b)
	slices.Sort(out)
	return slices.Compact(out)
}

// writeExternallyOwned answers a write that changes a field an external system
// owns: 422 with the ownership sentence as the problem detail, which the SPA
// shows as the message. Both refusal sites use it — the affordance check and
// the manager's guard (writePatchError) — so the two cannot drift apart.
func writeExternallyOwned(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = json.NewEncoder(w).Encode(v1.Error{
		Type:   "https://rela.dev/errors/externally_owned",
		Title:  "Field is owned by an external system",
		Status: http.StatusUnprocessableEntity,
		Detail: message,
	})
}

// writeExternallyOwnedIf answers [writeExternallyOwned] when err is (or
// wraps) an [entitymanager.TheirsWriteError], and reports whether it did.
func writeExternallyOwnedIf(w http.ResponseWriter, err error) bool {
	var owned *entitymanager.TheirsWriteError
	if !errors.As(err, &owned) {
		return false
	}
	writeExternallyOwned(w, owned.Error())
	return true
}
