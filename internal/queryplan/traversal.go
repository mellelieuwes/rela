package queryplan

import (
	"log/slog"
	"slices"
	"strings"

	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/predicate"
	"github.com/Sourcehaven-BV/rela/internal/predicatefns"
	"github.com/Sourcehaven-BV/rela/internal/search/searchparser"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// TraversalIndexSpecs derives the derived-query index specs a condition's
// traversals need, on the TRAVERSED-TO type.
//
// # Why this is a second spec rather than more properties on the first
//
// A derived index is PARTIAL on its own type:
//
//	CREATE INDEX ... ON entities ((properties->>'status'))
//	  WHERE type = 'concept' AND jsonb_typeof(properties->'status') = 'string';
//
// and [store.DerivedObjectSpec] carries exactly one Type. A traversal filters
// rows of the type at the far END of the relation, so the index it needs sits
// on a different type from the query's own — no number of extra Properties on
// the query's spec can produce it.
//
// Without this, an endpoint filter is CORRECT but unindexed: measured on
// PG 18.6, the join fell back to scanning every row of the target type
// (5000 scanned to find 10). With it the plan is an index scan. See
// TKT-RELTRV and the EXPLAIN test in pgstore.
//
// Only the FINAL hop's properties are indexable: intermediate hops are
// resolved by relation-type lookups (served by relations_type_from/to_idx),
// not by property comparison.
func TraversalIndexSpecs(
	prog *predicate.Program, meta *metamodel.Metamodel, fromType string,
) []store.DerivedObjectSpec {
	if prog == nil || meta == nil {
		return nil
	}
	byType := map[string][]string{}
	for _, spec := range prog.Traversals() {
		target, props := traversalIndexTarget(meta, fromType, spec)
		if target == "" || len(props) == 0 {
			continue
		}
		byType[target] = append(byType[target], props...)
	}

	out := make([]store.DerivedObjectSpec, 0, len(byType))
	for target, props := range byType {
		slices.Sort(props)
		out = append(out, store.DerivedObjectSpec{
			Kind: store.DerivedQueryIndex, Type: target, Properties: slices.Compact(props),
		})
	}
	// Deterministic order: the reconciler treats an absent desired object as
	// permission to DROP, so an unstable set would churn indexes between runs.
	slices.SortFunc(out, func(a, b store.DerivedObjectSpec) int {
		return strings.Compare(a.Type, b.Type)
	})
	return out
}

// traversalIndexTarget resolves the final hop's entity type and returns the
// constrained properties that are pushdown-eligible on it.
//
// Resolution is delegated to [predicatefns.ResolveTraversalTarget] — the same
// walk validation uses — rather than reimplemented. A second copy would drift,
// and the drift shows up as: the condition loads fine, the index is silently
// never derived, and the query scans every row of the target type.
//
// It returns ("", nil) for anything unresolvable, or a property that is not
// string-shaped. That is the same answer as "derives no index", which is the
// safe direction: a missing index costs a scan, whereas an index derived for
// the wrong shape would be reconciled and never used.
func traversalIndexTarget(
	meta *metamodel.Metamodel, fromType string, spec predicate.TraversalSpec,
) (targetType string, props []string) {
	current, err := predicatefns.ResolveTraversalTarget(meta, fromType, spec)
	if err != nil || current == "" {
		return "", nil
	}

	// PropNames leaves out an `id` constraint, which is answered by the
	// relation's own endpoint column and needs no property index. A property
	// compared against current_user.id is indexed like a literal one: the
	// value differs per request, the column does not.
	for _, name := range spec.PropNames() {
		if !stringComparableOnEveryType(meta, []string{current}, name) {
			continue
		}
		_, isLiteral := spec.Props[name].(predicate.String)
		_, isRef := spec.Refs[name]
		if !isLiteral && !isRef {
			continue
		}
		props = append(props, name)
	}
	if len(props) == 0 {
		return "", nil
	}
	return current, props
}

// scopeTraversalSpecs returns the traversal index specs of EVERY declared
// query scope, not only the ones a list names. A request may select any
// declared scope by name, and each traversal in it issues one
// [store.Store.MatchingIDs] query against the far-end type, so a scope a list
// does not mention is still a query shape the store serves.
//
// A scope that does not compile contributes nothing; the metamodel loader
// (scopes.Compile) has already refused it at boot. [conditionTraversalSpecs]
// covers the data-entry conditions.
func scopeTraversalSpecs(meta *metamodel.Metamodel) []store.DerivedObjectSpec {
	var (
		ev  *predicatefns.Evaluator
		out []store.DerivedObjectSpec
	)
	for _, typeName := range meta.EntityTypes() {
		def, _ := meta.GetEntityDef(typeName)
		names := make([]string, 0, len(def.QueryScopes))
		for name := range def.QueryScopes {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			source := def.QueryScopes[name]
			if !strings.Contains(source, "related") {
				continue // cheap pre-check; most scopes never traverse
			}
			if ev == nil {
				ev = predicatefns.NewEvaluator(meta)
			}
			prog, err := ev.CompileWithCurrentUser(typeName, source)
			if err != nil {
				slog.Warn("queryplan: query scope skipped for traversal index derivation",
					"type", typeName, "scope", name, "error", err)
				continue
			}
			out = append(out, TraversalIndexSpecs(prog, meta, typeName)...)
		}
	}
	return out
}

// conditionTraversalSpecs returns the traversal index specs of every list
// and next-action condition. Each traversal in one issues a
// [store.Store.MatchingIDs] query per page, the same shape a scope's does.
//
// A condition that does not compile contributes nothing; conditionlint has
// already refused it at config load.
func conditionTraversalSpecs(cfg *dataentryconfig.Config, meta *metamodel.Metamodel) []store.DerivedObjectSpec {
	var (
		ev  *predicatefns.Evaluator
		out []store.DerivedObjectSpec
	)
	add := func(typeName, source string) {
		if !strings.Contains(source, "related") {
			return
		}
		if ev == nil {
			ev = predicatefns.NewEvaluator(meta)
		}
		prog, err := ev.CompileWithCurrentUser(typeName, source)
		if err != nil {
			slog.Warn("queryplan: condition skipped for traversal index derivation",
				"type", typeName, "error", err)
			return
		}
		out = append(out, TraversalIndexSpecs(prog, meta, typeName)...)
	}
	// Map order is fine: StaticIndexSpecs dedups and sorts the result.
	for _, list := range cfg.Lists {
		add(list.EntityType, list.Condition)
	}
	for _, src := range cfg.NextActions {
		switch {
		case src.Condition == "":
		case src.Context != "":
			// A context source's candidate is the viewed entity.
			add(src.Context, src.Condition)
		case src.Query != "":
			for _, typeName := range searchparser.ParseQuery(src.Query).EntityTypes {
				add(typeName, src.Condition)
			}
		}
	}
	return out
}
