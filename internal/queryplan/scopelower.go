package queryplan

import (
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/predicate"
	"github.com/Sourcehaven-BV/rela/internal/predicatefns"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// ScopeLowering is a query scope rewritten as store predicates that REPLACE
// its Go evaluation (TKT-XKCNCL). Props are pushed as they are; each of
// Traversals still has to pass the principal's traversal gate before it
// becomes a [store.DirectedRelation], which is why they stay specs here.
type ScopeLowering struct {
	Props      []store.PropPredicate
	Traversals []predicate.TraversalSpec
}

// LowerScope decides whether a query scope on entityType can be answered by
// the store alone, and if so returns the predicates that answer it.
//
// It is the eligibility half only. The index columns a scope contributes are
// still derived by [ConditionIndexProperties], deliberately: the Go path
// pushes a scope's equalities as a pre-filter even when the scope as a whole
// does not lower, and those reads need the index too. Every Props entry
// returned here is one of those columns. The index exists only for the scope
// a sorted list names, though, so a lowered scope chosen by `?query_scope=`
// or on an unsorted list may still scan; that costs time, never rows.
//
// # Exact, not a superset
//
// [ConditionPrefilters] may drop a conjunct because the Go pass re-checks
// every row. Nothing re-checks a lowered scope, so each leaf of the AND spine
// must produce exactly one predicate with the same meaning, or ok is false and
// the caller keeps the Go path. In particular:
//
//   - a membership (`has_current_user(list)`) declines: the store reads it
//     correctly but the derived index does not serve it
//   - an attribute constrained twice declines rather than keep one binding
//   - an equality the metamodel gate refuses (an undeclared property, an
//     integer, boolean or list, or `id`) declines
//   - `current_user.tool` declines, as in [ConditionPrefilters]
//   - a `current_user` reference with no identity declines, so the Go path
//     can fail the request closed rather than this matching the unset rows.
//     That includes a traversal constrained by `current_user.id`: the
//     returned Traversals are BOUND to identity, and a traversal that cannot
//     be bound is never returned unbound, since its id would lower to an
//     empty endpoint set, which a store reads as "any endpoint"
//
// identity is the request's query identity, empty when there is none. It is
// an argument, never state: the resolver that calls this is shared across
// principals.
func LowerScope(
	prog *predicate.Program, meta *metamodel.Metamodel, entityType, identity string,
) (ScopeLowering, bool) {
	if prog == nil || meta == nil || entityType == "" {
		return ScopeLowering{}, false
	}
	eqs, traversals, ok := prog.Conjunction(predicatefns.CurrentUserPrefilterSpec())
	if !ok {
		return ScopeLowering{}, false
	}
	types := []string{entityType}
	seen := make(map[string]bool, len(eqs))
	props := make([]store.PropPredicate, 0, len(eqs))
	for _, eq := range eqs {
		if eq.List || seen[eq.Attribute] || !stringComparableOnEveryType(meta, types, eq.Attribute) {
			return ScopeLowering{}, false
		}
		seen[eq.Attribute] = true
		value := eq.Value
		switch eq.FromVar {
		case "":
		case predicatefns.FieldCurrentUserID:
			if identity == "" {
				return ScopeLowering{}, false
			}
			value = identity
		default:
			return ScopeLowering{}, false
		}
		props = append(props, store.PropPredicate{
			Property: eq.Attribute, Op: store.PropEqual, Value: value, Scalar: true,
		})
	}
	bound := make([]predicate.TraversalSpec, 0, len(traversals))
	for _, spec := range traversals {
		b, err := predicatefns.BindTraversal(spec, identity)
		if err != nil {
			return ScopeLowering{}, false
		}
		bound = append(bound, b)
	}
	return ScopeLowering{Props: props, Traversals: bound}, true
}
