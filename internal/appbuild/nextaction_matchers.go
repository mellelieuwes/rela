package appbuild

import (
	"context"
	"errors"
	"fmt"

	"github.com/Sourcehaven-BV/rela/internal/conditionlint"
	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/nextaction"
	"github.com/Sourcehaven-BV/rela/internal/predicate"
	"github.com/Sourcehaven-BV/rela/internal/predicatefns"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/queryplan"
	"github.com/Sourcehaven-BV/rela/internal/relresolve"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// nextActionMatcher adapts one compiled condition to the two seams dataentry
// declares: [nextaction.Matcher] (the authoritative per-candidate pass) and
// dataentry.ConditionPrefilterer (the store-side pre-filter). appbuild cannot
// name the latter — dataentry's tests import appbuild, so the edge would
// cycle — which is why dataentry's own test suite asserts the adapter
// satisfies it (TestNextAction_CurrentUserConditionEndToEnd).
//
// Neither method derives an identity. Both read the [predicatefns.QueryIdentity]
// that [nextActionRequestScope] stamped ONCE per request, so the pre-filter
// and the Go pass cannot disagree about whose rows they select, and a
// resolve over N candidates costs one derivation, not N.
type nextActionMatcher struct {
	m    *conditionlint.NextActionMatcher
	meta *metamodel.Metamodel
}

// Match evaluates the whole condition against the identity on ctx.
func (w nextActionMatcher) Match(ctx context.Context, e *entity.Entity) (bool, error) {
	ok, err := w.m.Match(ctx, e)
	if errors.Is(err, predicatefns.ErrNoCurrentUser) {
		// Translate to the engine's sentinel so the HTTP layer can name the
		// misconfiguration without importing the predicate engine.
		return false, fmt.Errorf("%w: %w", nextaction.ErrIdentityRequired, err)
	}
	return ok, err
}

// MatchAllWith judges every candidate, answering the condition's
// `related(...)` for all of them first: one store query per traversal and
// entity type. gate authorizes each traversal and match answers it; the
// caller passes the request's read gate, so a condition never sees an
// entity the reader could not (TKT-205V2N).
func (w nextActionMatcher) MatchAllWith(
	ctx context.Context, es []*entity.Entity, gate relresolve.Gate, match relresolve.Match,
) ([]bool, error) {
	bound := map[string]func(*entity.Entity) predicate.TraversalFunc{}
	for _, t := range w.m.Types() {
		prog, _ := w.m.Program(t)
		b, err := bindPage(ctx, w.meta, gate, match, t, es, prog)
		if errors.Is(err, predicatefns.ErrNoCurrentUser) {
			// A related() constraint on current_user.id is answered here,
			// before any row is matched.
			return nil, fmt.Errorf("%w: %w", nextaction.ErrIdentityRequired, err)
		}
		if err != nil {
			return nil, err
		}
		bound[t] = b
	}
	out := make([]bool, len(es))
	for i, e := range es {
		var traversal predicate.TraversalFunc
		if b, ok := bound[e.Type]; ok {
			traversal = b(e)
		}
		ok, err := w.m.MatchWith(ctx, e, traversal)
		if errors.Is(err, predicatefns.ErrNoCurrentUser) {
			return nil, fmt.Errorf("%w: %w", nextaction.ErrIdentityRequired, err)
		}
		if err != nil {
			return nil, err
		}
		out[i] = ok
	}
	return out, nil
}

// Prefilters lowers the condition's store-evaluable conjuncts for the types
// the condition was compiled against (queryplan.ConditionPrefilters). With
// no identity on ctx the current-user conjuncts push nothing; Match then
// fails the same condition closed, so the request is refused rather than
// answered with the wrong rows.
func (w nextActionMatcher) Prefilters(ctx context.Context, meta *metamodel.Metamodel) []store.PropPredicate {
	types := w.m.Types()
	if len(types) == 0 {
		return nil
	}
	prog, ok := w.m.Program(types[0])
	if !ok {
		return nil
	}
	var identity string
	if q, stamped := predicatefns.QueryIdentityFrom(ctx); stamped {
		identity = q.ID()
	}
	return queryplan.ConditionPrefilters(prog, meta, types, identity)
}

// nextActionRequestScope stamps the query identity on ctx once per
// next-action resolve. It is the ONE place the identity is derived for this
// surface (dataentry.NextActionRequestScope).
//
// A boundary-stamped identity already on ctx is honored — but when the
// request also carries a principal, the two must AGREE. A stamp naming a
// different user than the principal means two layers disagree about who is
// calling, and silently preferring either would evaluate every condition for
// the wrong person with no signal — the hazard dataentry's attachACLRequest
// refuses for an ACL request whose principal disagrees with ctx. That is
// [nextaction.ErrIdentityConflict], a refusal of the whole request, distinct
// from the per-source [nextaction.ErrIdentityRequired] an unauthenticated
// caller gets. Nothing stamps upstream in production today (that boundary
// stamp is TKT-ZQV9O5), so this is the guard that keeps that future change
// from widening quietly.
func nextActionRequestScope(ctx context.Context) (context.Context, error) {
	stamped, hasStamp := predicatefns.QueryIdentityFrom(ctx)
	derived, hasPrincipal := queryIdentityFor(ctx)
	switch {
	case hasStamp && hasPrincipal && stamped.ID() != derived.ID():
		return ctx, nextaction.ErrIdentityConflict
	case hasStamp:
		return ctx, nil
	case hasPrincipal:
		return predicatefns.WithQueryIdentity(ctx, derived), nil
	}
	return ctx, nil
}

// queryIdentityFor derives the query identity from the principal on ctx.
//
// It does NOT call the ACL resolver: on the data-entry API the router has
// already run resolvePrincipalEntity, so principal.User IS the resolved user
// entity id when the policy has a user_entity_type and the principal matched
// one, with RawUser holding the original identifier. When no resolution
// happened, User is the raw identifier and that is what graph data can be
// compared against (predicatefns.QueryIdentity documents the fallback).
//
// Two states yield no identity, and both must: an unstamped ctx, and the
// "unknown" attribution placeholder the data-entry server stamps when it has
// no identity source. Neither is a user, and comparing either against
// `entity.assignee` would be a match against nobody at best and against an
// entity literally assigned to "unknown" at worst.
//
// One degradation is inherited from the router and worth knowing: when the
// principal_property lookup fails with a BACKEND error, resolvePrincipalEntity
// keeps the raw principal, so this derives the raw identifier while the
// operator's condition compares against user-entity ids. A positive
// identity comparison then matches nothing for that request rather than
// erroring, and a negated one (`not related(..., { id = current_user.id })`)
// matches every row the reader can already see. Neither reaches past the read
// gate; predicatefns.ResolveQueryIdentity is the stricter shape the
// boundary stamp (TKT-ZQV9O5) should adopt.
func queryIdentityFor(ctx context.Context) (predicatefns.QueryIdentity, bool) {
	p, ok := principal.Stamped(ctx)
	if !ok || p.User == "" || p.User == principal.Unknown {
		return predicatefns.QueryIdentity{}, false
	}
	q := predicatefns.QueryIdentity{Raw: p.User, Tool: p.Tool}
	if p.RawUser != "" && p.RawUser != p.User {
		q.EntityID = p.User
		q.Raw = p.RawUser
	}
	return q, true
}

// NextActionMatchers compiles the `condition:` of every next-action source,
// adapting conditionlint's compiler to the seam internal/dataentry declares.
//
// Lives at the composition root for the same reason the userstate backend
// does: the condition/policy engine sits above the data-entry app, so
// dataentry takes a seam and this bridges the two.
//
// Takes config + metamodel rather than returning a prebuilt lookup because
// both reload at runtime — a lookup captured at boot would keep evaluating a
// condition the operator has since edited.
//
// The second result is the per-request scope binder (dataentry.NextActionRequestScope):
// the handler applies it to the request ctx once, before resolving, and every
// matcher then reads the identity it stamped. Returned alongside the lookup
// because the two are one contract — matchers compiled here evaluate only
// against an identity stamped here.
func NextActionMatchers(
	cfg *dataentryconfig.Config, meta *metamodel.Metamodel,
) (lookup func(string) (nextaction.Matcher, bool), scope func(context.Context) (context.Context, error),
	problems []string,
) {
	compiled, issues := conditionlint.NextActionMatchers(cfg, meta)
	if len(issues) > 0 || compiled == nil {
		return nil, nil, issues
	}
	return func(id string) (nextaction.Matcher, bool) {
		m, ok := compiled(id)
		if !ok {
			// Returning m directly would hand back a non-nil interface
			// wrapping a nil *NextActionMatcher — the classic typed-nil trap.
			return nil, false
		}
		return nextActionMatcher{m: m, meta: meta}, true
	}, nextActionRequestScope, nil
}
