package dataentry

import (
	"context"
	"errors"
	"fmt"

	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	entityPkg "github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/relresolve"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// ViewConditionMatcher evaluates one view's compiled `condition:` against a
// candidate row.
//
// Consumer-side (see docs/architecture/consumer-side-interfaces.md): dataentry
// declares the one method it needs and the composition root supplies the
// implementation, because the predicate engine that compiles and evaluates the
// expression sits above this package — arch-lint forbids dataentry importing
// `conditionlint` or `predicate`, the same reason next actions reach their
// compiled conditions through [ConditionPrefilterer].
//
// The method takes the request ctx because a condition may name
// `current_user`; the identity is derived from the principal on it by the
// implementation, so one derivation serves every row of the page.
//
// An evaluation error is RETURNED, never swallowed as "no match". A row that
// cannot be judged is not the same as a row judged false: silently dropping it
// would be the BUG-WHEREWIDE failure inverted, narrowing a view with no
// diagnostic. The caller decides what an error means for the request.
//
// MatchPage judges a whole page at once, so a condition using `related(...)`
// costs one store query per traversal rather than one per row. gate
// authorizes each traversal and match answers it; the verdicts come back in
// row order.
type ViewConditionMatcher interface {
	MatchPage(
		ctx context.Context, rows []*entityPkg.Entity, gate relresolve.Gate, match relresolve.Match,
	) ([]bool, error)
}

// ViewConditionLookup returns the matcher for a configured view, or ok=false
// when that view declares no condition.
//
// Keyed by the same (kind, id) pair the config uses, because a list and a
// kanban may legitimately share an id. `kind` is "lists" or "kanbans",
// matching conditionlint.ViewConditionKind — spelled as plain strings here so
// the seam carries no import from the compiling package.
type ViewConditionLookup func(kind, id string) (ViewConditionMatcher, bool)

// ViewConditionFunc compiles every configured view `condition:` against the
// current config and metamodel, returning the lookup plus one message per
// problem.
//
// The consumer-side seam for the predicate compiler, mirroring
// [NextActionMatcherFunc]: arch-lint keeps the condition engine above this
// package, so the composition root supplies an implementation.
//
// Takes cfg + meta at call time rather than returning a prebuilt lookup
// because both reload at runtime — a lookup captured at boot would keep
// evaluating a condition the operator has since edited.
type ViewConditionFunc func(
	cfg *dataentryconfig.Config, meta *metamodel.Metamodel,
) (ViewConditionLookup, []string)

// View kinds and the request parameter that names a list.
//
// The parameter carries an ID, never an expression: the condition itself is
// operator config the server already holds, so a caller can only select among
// views it was already free to name. Nothing about the request can introduce
// a predicate the config did not declare.
const (
	viewKindList   = "lists"
	viewKindKanban = "kanbans"
	// viewKindAction is a detail action's `when:` (TKT-VVS16W), keyed by
	// dataentryconfig.ActionConditionID.
	viewKindAction = "actions"

	// listIDParam names which configured list a generic collection read is
	// serving, so the server can apply that list's condition. Absent means
	// "no view named" — see [viewConditionFor].
	listIDParam = "list_id"
)

// viewConditionFor resolves the matcher for a list, or nil when there is none:
// no id supplied, no lookup wired, or the view declares no condition.
//
// A nil result means "no constraint", which is the deliberate answer for a
// request that names no view. `/api/v1/{plural}` is the generic entity
// endpoint and stays generic: a condition is view PRESENTATION, not
// authorization, so omitting it returns the ACL-scoped superset of the view
// rather than anything the principal may not see. Making the bare endpoint
// refuse instead would break every non-SPA caller (MCP, CLI, integrations)
// that legitimately reads a type directly.
func viewConditionFor(lookup ViewConditionLookup, kind, id string) ViewConditionMatcher {
	if lookup == nil || id == "" {
		return nil
	}
	m, ok := lookup(kind, id)
	if !ok {
		return nil
	}
	return m
}

// AdaptViewConditions converts a compiler that returns a STRUCTURALLY
// identical matcher under its own name into a [ViewConditionFunc].
//
// The composition root cannot name this package's types — dataentry's own
// tests import appbuild, so doing so would close an import cycle — and while
// Go assigns the two INTERFACES freely, it will not assign the function types
// wrapping them. This adapter is that one conversion, kept here (where both
// halves are already visible) rather than inlined at every wiring site.
//
// The type parameter is what makes the drift visible: a supplier whose
// matcher stops satisfying [ViewConditionMatcher] fails to compile at the
// call, not at some later evaluation.
func AdaptViewConditions[M ViewConditionMatcher](
	fn func(*dataentryconfig.Config, *metamodel.Metamodel) (func(kind, id string) (M, bool), []string),
) ViewConditionFunc {
	if fn == nil {
		return nil
	}
	return func(cfg *dataentryconfig.Config, meta *metamodel.Metamodel) (ViewConditionLookup, []string) {
		lookup, problems := fn(cfg, meta)
		if lookup == nil {
			return nil, problems
		}
		return func(kind, id string) (ViewConditionMatcher, bool) {
			m, ok := lookup(kind, id)
			if !ok {
				// Do not return m: a nil M in a non-nil interface is the
				// typed-nil trap, and callers test the result for nil.
				return nil, false
			}
			return m, true
		}, problems
	}
}

// SetViewConditions injects the predicate compiler backing a list's or
// kanban's `condition:`.
//
// Separate from NewApp for the same reason as [App.SetNextActionMatchers]:
// the compiler lives above this package, so the composition root supplies it.
// Rejects nil for the same reason too — a silently absent compiler would
// leave every condition unevaluated, showing rows the operator's view
// excludes.
//
// A deployment that never calls this keeps pre-condition behavior: views
// show their ACL-scoped superset. That is the safe direction, because a
// condition only ever NARROWS; it is not safe in reverse, which is why a
// condition that fails to COMPILE is a startup error instead.
func (a *App) SetViewConditions(fn ViewConditionFunc) error {
	if fn == nil {
		return errors.New("dataentry.SetViewConditions: func must be non-nil")
	}
	a.viewConditions = fn
	return nil
}

// viewCondition resolves the matcher for one view against the CURRENT config
// and metamodel.
//
// Compiles per call rather than caching: config and metamodel reload at
// runtime, so a lookup captured at boot would evaluate an expression the
// operator has since edited — the trap the nextActionMatchers field documents.
// Compilation is a parse of one short expression and the Evaluator memoizes
// programs, so this is cheap next to the store read it gates.
//
// Problems are dropped: validation refused a config whose conditions do not
// compile, so a problem here can only arise in a deployment that skipped
// validation, where the safe answer is the unconstrained (ACL-scoped) view
// rather than a request that fails.
func viewCondition(fn ViewConditionFunc, s *Schema, kind, id string) ViewConditionMatcher {
	if fn == nil || id == "" || s == nil {
		return nil
	}
	lookup, problems := fn(s.Cfg, s.Meta)
	if len(problems) > 0 {
		return nil
	}
	return viewConditionFor(lookup, kind, id)
}

// applyViewCondition filters rows by a view's condition, preserving order.
//
// Returns rows unchanged when there is no matcher, so a view without a
// condition costs nothing. An evaluation error aborts the page rather than
// yielding a partial one — see [ViewConditionMatcher].
//
// The condition is evaluated against the REDACTED entity, never the raw
// stored one. Field redaction otherwise happens at serialization, long after
// this filter has already decided membership — and since a condition may name
// is_current_user(entity.<field>), a condition over a field the reader cannot
// see would decide whether the row appears. That leaks the hidden value one
// bit at a time through row presence/absence, without it ever being
// serialized (the IB-review finding on #1593).
//
// Evaluating post-redaction is sound in the direction that matters: redaction
// REMOVES a hidden property, so it binds Nil, and every current-user form is
// false on Nil. The result is strictly narrower than an unredacted pass for
// those forms — the same asymmetry [ConditionPrefilterer] documents for next
// actions, reached here by the same remedy.
//
// The KEPT rows are the originals, not the redacted copies: redaction for the
// response happens at serialization, and substituting stripped entities here
// would drop properties the reader may legitimately see.
//
// redact is the per-principal field filter. Nil means no redaction, which is
// correct only for a caller that has already redacted or has no field policy;
// a caller holding one must pass it.
func applyViewCondition(
	ctx context.Context, rows []*entityPkg.Entity, m ViewConditionMatcher,
	redact func(context.Context, *entityPkg.Entity) *entityPkg.Entity, st store.GraphQueryer,
) ([]*entityPkg.Entity, error) {
	if m == nil || len(rows) == 0 {
		return rows, nil
	}
	candidates := rows
	if redact != nil {
		candidates = make([]*entityPkg.Entity, len(rows))
		for i, e := range rows {
			candidates[i] = redact(ctx, e)
		}
	}
	verdicts, err := m.MatchPage(ctx, candidates, traversalGateFromContext(ctx).GateTraversal,
		pageMatch(st))
	if err != nil {
		return nil, err
	}
	if len(verdicts) != len(rows) {
		// coverage-ignore: invariant: MatchPage returns one verdict per row
		return nil, fmt.Errorf("view condition: %d verdicts for %d rows", len(verdicts), len(rows))
	}
	kept := make([]*entityPkg.Entity, 0, len(rows))
	for i, e := range rows {
		if verdicts[i] {
			kept = append(kept, e)
		}
	}
	return kept, nil
}

// pageMatch answers a traversal in the request's world, as [applyScope] does,
// so a condition cannot see further than the list it filters. It adds no face
// narrowing: the rows here are already the list's faces, and an allowlist
// built from them would drop default-face rows from a mixed page.
func pageMatch(st store.GraphQueryer) relresolve.Match {
	return func(ctx context.Context, q store.GraphQuery, ids []string) (map[string]bool, error) {
		if st == nil {
			return nil, errors.New("view condition: no store to answer related()")
		}
		return st.MatchingIDs(ctx, stampScope(ctx, q, scopeRequest{}), ids)
	}
}
