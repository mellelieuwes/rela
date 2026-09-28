package appbuild

import (
	"context"

	"github.com/Sourcehaven-BV/rela/internal/conditionlint"
	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/predicate"
	"github.com/Sourcehaven-BV/rela/internal/relresolve"
)

// ViewConditionMatcher restates the method set internal/dataentry
// declares, WITHOUT importing it: dataentry's own tests import appbuild, so
// naming its types here would close an import cycle. NextActionMatchers
// avoids the same cycle by returning nextaction.Matcher — a type from a third
// package both sides see — but view conditions have no such package, so the
// structural type is restated. Go's structural interfaces make the two
// identical at the call site; a drift would be a compile error at the
// SetViewConditions call in cmd/, which is where they meet.
type ViewConditionMatcher interface {
	MatchPage(ctx context.Context, rows []*entity.Entity, gate relresolve.Gate, match relresolve.Match) ([]bool, error)
}

// ViewConditions compiles the `condition:` of every list and kanban, adapting
// conditionlint's compiler to the seam internal/dataentry declares.
//
// Lives at the composition root for the same reason [NextActionMatchers]
// does: the condition engine sits above the data-entry app, so dataentry
// takes a seam and this bridges the two.
//
// Takes config + metamodel rather than returning a prebuilt lookup because
// both reload at runtime — a lookup captured at boot would keep evaluating a
// condition the operator has since edited.
func ViewConditions(
	cfg *dataentryconfig.Config, meta *metamodel.Metamodel,
) (lookup func(kind, id string) (ViewConditionMatcher, bool), problems []string) {
	compiled, problems := conditionlint.ViewConditionMatchers(cfg, meta)
	if len(problems) > 0 || compiled == nil {
		return nil, problems
	}
	return func(kind, id string) (ViewConditionMatcher, bool) {
		m, ok := compiled(kind, id)
		if !ok {
			// Returning m directly would hand back a non-nil interface
			// wrapping a nil *ViewConditionMatcher — the typed-nil trap
			// NextActionMatchers guards against for the same reason.
			return nil, false
		}
		return viewConditionMatcher{m: m, meta: meta}, true
	}, nil
}

// viewConditionMatcher answers a view condition for a page of rows. Its
// `related(...)` are answered for the whole page first, with the gate and
// store query the caller passes: the request's read gate, so a condition
// never sees an entity the list could not show (TKT-205V2N).
type viewConditionMatcher struct {
	m    *conditionlint.ViewConditionMatcher
	meta *metamodel.Metamodel
}

// EntityAttributes returns the entity fields the condition reads, so a
// caller can refuse a condition over a field the principal cannot see.
func (v viewConditionMatcher) EntityAttributes() []string { return v.m.EntityAttributes() }

// MatchPage returns one verdict per row, in order. An evaluation error is
// returned, never read as "no match".
func (v viewConditionMatcher) MatchPage(
	ctx context.Context, rows []*entity.Entity, gate relresolve.Gate, match relresolve.Match,
) ([]bool, error) {
	bound, err := bindPage(ctx, v.meta, gate, match, v.m.EntityType(), rows, v.m.Program())
	if err != nil {
		return nil, err
	}
	out := make([]bool, len(rows))
	for i, e := range rows {
		if out[i], err = v.m.MatchesWith(ctx, e, bound(e)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// bindPage answers prog's `related(...)` for the rows of entityType and
// returns the per-row resolver. With no traversal it makes no store call and
// every row gets nil. A row of another type gets nil too; its evaluation
// then fails rather than guessing.
func bindPage(
	ctx context.Context, meta *metamodel.Metamodel, gate relresolve.Gate, match relresolve.Match,
	entityType string, rows []*entity.Entity, prog *predicate.Program,
) (func(*entity.Entity) predicate.TraversalFunc, error) {
	if len(prog.Traversals()) == 0 {
		return func(*entity.Entity) predicate.TraversalFunc { return nil }, nil
	}
	b, err := relresolve.NewBinder(meta, gate, match)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, e := range rows {
		if e.Type == entityType {
			ids = append(ids, e.ID)
		}
	}
	bound, err := b.Bind(ctx, entityType, ids, prog)
	if err != nil {
		return nil, err
	}
	return func(e *entity.Entity) predicate.TraversalFunc {
		if e.Type != entityType {
			return nil
		}
		return bound(e.ID)
	}, nil
}
