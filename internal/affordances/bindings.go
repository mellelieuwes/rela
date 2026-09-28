package affordances

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/predicate"
	"github.com/Sourcehaven-BV/rela/internal/principal"
)

// RelationLookup is the narrow contract the resolver needs from the
// graph to answer has_relation / count_relations and to resolve local
// roles. Defined at the consumer (CLAUDE.md "interfaces at the call
// site"); the wiring site supplies a snapshot-backed implementation.
//
// OutgoingCounts returns, for fromID, a map of relation type → count
// of outgoing edges of that type. One call answers both has_relation
// (type present) and count_relations (the count), so the binding
// context scans the graph once per resolve rather than once per
// predicate (RR-08AK). HasEdge reports whether a specific edge
// fromID --relType--> toID exists — a targeted query used for
// local-role resolution (principal --role-relation--> entity).
type RelationLookup interface {
	OutgoingCounts(ctx context.Context, fromID string) map[string]int
	HasEdge(ctx context.Context, fromID, relType, toID string) bool
}

// bindingContext carries everything a single Resolver call needs to
// build per-entity predicate Bindings: the principal, the entity, the
// principal's effective role set (globals plus ancestor-conferred plus
// direct local), the principal's globals-only role set, and the graph
// lookup. It is constructed once per resolver call (snapshot-once).
//
// The caller's request context is NOT stored here — it is threaded as
// a method parameter (passes, evalGrants, …) into predicate Eval and
// the host-function calls it makes, matching the predicate package's
// own ctx-as-parameter convention (golangci-lint containedctx) and
// the caller-ctx pattern from TKT-WFB6 / PR#825.
type bindingContext struct {
	principal principal.Principal
	entity    *entity.Entity
	// entityRoles is the per-entity effective role set: globals (incl.
	// group expansion) ∪ ancestor-conferred ∪ direct-local. This is
	// the answer to "does the principal hold role X on this entity"
	// and the source the has_role host function consults (RR-JRPZ).
	entityRoles map[string]bool
	// globalRoles is the globals-only set (no per-entity grants). Used
	// by has_global_role for predicates that want to discriminate
	// "globally a admin" from "admin on this one entity."
	globalRoles map[string]bool
	lookup      RelationLookup
	resolver    *PolicyResolver
	// userID is the principal's identity as it appears on role-relation
	// edges and current_user.id.
	userID string
	// userFuncs are the is_current_user / has_current_user implementations
	// closed over identity(), built ONCE per binding context: newBindings
	// runs per grant per role per entity on the list-render path, and the
	// identity does not change across those calls.
	userFuncs map[string]predicate.FuncFunc

	// outgoing caches the entity's outgoing-edge counts, loaded once
	// on first host-func use (has_relation / count_relations) so a
	// resolve call scans the graph at most once for them.
	outgoing      map[string]int
	outgoingReady bool
}

// outgoingCounts returns the entity's outgoing-edge counts, loading
// and caching them on first call.
//
// For a HISTORICAL subject ([WithHistoricalSubject]) the live store cannot
// answer the entity's as-of-version edges, so this returns an empty map
// WITHOUT consulting the store: has_relation / count_relations then see no
// edges, any conditional `visible:` grant that needs them evaluates false, and
// the field fails CLOSED (TKT-73C6B2). Not consulting the store also avoids a
// misleading live lookup on a since-deleted or drifted id.
func (bc *bindingContext) outgoingCounts(ctx context.Context) map[string]int {
	if IsHistoricalSubject(ctx) {
		return nil
	}
	if !bc.outgoingReady {
		bc.outgoing = bc.lookup.OutgoingCounts(ctx, bc.entity.ID)
		bc.outgoingReady = true
	}
	return bc.outgoing
}

// newBindings builds the predicate Bindings for evaluating a grant's
// predicate against bc's entity. The entity record is coerced from
// the metamodel-declared property types; host functions close over bc.
func (bc *bindingContext) newBindings(meta *metamodel.Metamodel) (*predicate.Bindings, error) {
	b := predicate.NewBindings()

	if err := b.SetVar("entity", bc.entityRecord(meta)); err != nil {
		// coverage-ignore: defensive: SetVar only errors on empty name / nil value; "entity" is a non-empty constant
		// and entityRecord always
		// returns a non-nil predicate.Record
		return nil, err
	}
	// See bindingContext.identity: an unidentified caller binds an EMPTY
	// id, and PolicyResolver.passes has already refused any grant whose
	// clause reads it. The sugar below closes over the same value and never
	// matches on empty (predicatefns.CurrentUserBindings), so even a clause
	// that slipped past that refusal could not match an entity whose
	// property holds the placeholder.
	identity := bc.identity()
	if err := b.SetVar("current_user", predicate.NewRecord(map[string]predicate.Value{
		"id":   predicate.NewString(identity),
		"tool": predicate.NewString(bc.principal.Tool),
	})); err != nil {
		// coverage-ignore: defensive: "current_user" is a non-empty constant and NewRecord always yields a non-nil
		// value, so SetVar cannot
		// error here
		return nil, err
	}

	setters := []struct {
		name string
		fn   predicate.Func
	}{
		{"has_role", predicate.FuncFunc(bc.hasRole)},
		{"has_global_role", predicate.FuncFunc(bc.hasGlobalRole)},
		{"has_relation", predicate.FuncFunc(bc.hasRelation)},
		{"count_relations", predicate.FuncFunc(bc.countRelations)},
		{"string_in_list", predicate.FuncFunc(stringInList)},
	}
	for _, s := range setters {
		if err := b.SetFunc(s.name, s.fn); err != nil {
			// coverage-ignore: defensive: SetFunc only errors on empty name / nil impl; every setter name is a non-
			// empty constant and each fn is
			// a non-nil predicate.FuncFunc
			return nil, err
		}
	}
	// Bound from the same identity as current_user.id above — this
	// package's own resolved principal, not the query-identity context —
	// so the sugar and the explicit comparison agree whichever way
	// identity arrived, including the no-identity case.
	for name, fn := range bc.userFuncs {
		if err := b.SetFunc(name, fn); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// identity is the value current_user.id binds to: the principal's user, or
// "" when the caller is not identified.
//
// The attribution placeholder is not a user. A data-entry server without an
// identity source stamps principal.Unknown on every request, and an unstamped
// ctx reads as the same placeholder; binding either literally would let
// `entity.owner == current_user.id` — an AUTHORIZATION input here, gating
// visible: and actions — match an entity whose owner property happens to hold
// "unknown". Both read as no identity, and a `when:` that needs one is refused
// in passes, which is the same fail-closed reading the query path gets from
// predicatefns.ErrNoCurrentUser.
func (bc *bindingContext) identity() string {
	if bc.userID == principal.Unknown {
		return ""
	}
	return bc.userID
}

// entityRecord coerces the entity's properties into a predicate.Record
// using the metamodel-declared types. Off-type or missing values bind
// as Nil rather than erroring (DR-C2): permissive storage must not
// turn data drift into an Eval failure. id and type are always set.
func (bc *bindingContext) entityRecord(meta *metamodel.Metamodel) predicate.Value {
	fields := map[string]predicate.Value{
		"id":   predicate.NewString(bc.entity.ID),
		"type": predicate.NewString(bc.entity.Type),
	}
	if meta != nil {
		if def, ok := meta.Entities[bc.entity.Type]; ok {
			for name, prop := range def.Properties {
				if _, modeled := propertyPredicateType(meta, prop); !modeled {
					continue
				}
				fields[name] = coerceValue(prop, bc.entity.Properties[name])
			}
		}
	}
	return predicate.NewRecord(fields)
}

// coerceValue best-effort coerces a stored property value to the
// predicate Value matching its metamodel type. Unconvertible or
// missing values become Nil. List properties become a List of coerced
// scalars (single scalar promoted to a one-element list).
func coerceValue(prop metamodel.PropertyDef, raw any) predicate.Value {
	if prop.List {
		return coerceList(prop, raw)
	}
	return coerceScalar(&prop, raw)
}

// coerceList coerces a list-typed property. Surprising-but-deliberate
// fail-soft choices (M3):
//   - A bare scalar (not a slice) is promoted to a one-element list,
//     so a hand-edited `tags: vip` reads the same as `tags: [vip]`.
//   - A nil/absent value is the empty list (so list membership checks
//     are false, never an Eval error).
//   - Non-coercible elements (e.g. a non-string in a string list)
//     become Nil holes rather than failing the whole list.
func coerceList(prop metamodel.PropertyDef, raw any) predicate.Value {
	elems := []predicate.Value{}
	switch v := raw.(type) {
	case []any:
		for _, e := range v {
			elems = append(elems, coerceScalar(&prop, e))
		}
	case nil:
		// empty list
	default:
		// single scalar promoted to one-element list
		elems = append(elems, coerceScalar(&prop, raw))
	}
	return predicate.NewList(elems)
}

// coerceScalar coerces a stored scalar to the predicate Value whose type
// matches predicatefns.ScalarTypeForProp (the shared adapter): integer ->
// Int, date/datetime -> Date, boolean -> Bool, everything else -> String.
// The declared type — not the raw Go type — drives the choice, so the
// bound Value's type always matches the field's DECLARED predicate type
// (RR-4189H: a Number bound to an IntType field, or a String to a
// DateType field, would fail the runtime type check at Eval). Off-type or
// unconvertible values bind as Nil (DR-C2, fail-soft).
func coerceScalar(prop *metamodel.PropertyDef, raw any) predicate.Value {
	if raw == nil {
		return predicate.NewNil()
	}
	switch prop.Type {
	case metamodel.PropertyTypeInteger:
		return coerceInt(raw)
	case metamodel.PropertyTypeDate, metamodel.PropertyTypeDatetime:
		return coerceDate(prop, raw)
	case metamodel.PropertyTypeBoolean:
		return coerceBool(raw)
	default:
		// string / enum / rrule / custom — string-valued.
		if s, ok := raw.(string); ok {
			return predicate.NewString(s)
		}
		return predicate.NewNil()
	}
}

// coerceInt binds an integer field to a predicate Int. It preserves the
// permissive coercion the pre-Phase-2 coerceNumber offered (RR-IRV2WJ,
// pinned by TestResolver_OffTypeProperty_CoercesNotFails): int, int64,
// an integral float64, and a string that parses as an integer all bind;
// a fractional float or non-numeric string binds Nil. Fractional inputs
// bind Nil rather than truncating — silent truncation would corrupt the
// comparison, and IntType has no fractional value to hold.
func coerceInt(raw any) predicate.Value {
	switch v := raw.(type) {
	case int:
		return predicate.NewInt(int64(v))
	case int64:
		return predicate.NewInt(v)
	case float64:
		if v == math.Trunc(v) {
			return predicate.NewInt(int64(v))
		}
	case string:
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return predicate.NewInt(n)
		}
	}
	return predicate.NewNil()
}

// coerceDate binds a date/datetime field to a predicate Date. YAML
// auto-decodes an unquoted date scalar to time.Time and a quoted one to
// string, so BOTH must be handled (RR-WHMVLW: missing the time.Time case
// silently binds Nil and flips ACL grants to deny). A string is parsed
// via metamodel.ParseDateValue against the property's declared format —
// the same parser filter.matchDate uses — never a hand-rolled layout.
// Anything else binds Nil.
func coerceDate(prop *metamodel.PropertyDef, raw any) predicate.Value {
	switch v := raw.(type) {
	case time.Time:
		return predicate.NewDate(v)
	case string:
		if t, err := metamodel.ParseDateValue(v, prop); err == nil {
			return predicate.NewDate(t)
		}
	}
	return predicate.NewNil()
}

func coerceBool(raw any) predicate.Value {
	switch v := raw.(type) {
	case bool:
		return predicate.NewBool(v)
	case string:
		if v == "true" {
			return predicate.NewBool(true)
		}
		if v == "false" {
			return predicate.NewBool(false)
		}
	}
	return predicate.NewNil()
}
