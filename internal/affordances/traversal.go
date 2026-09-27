package affordances

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/predicate"
	"github.com/Sourcehaven-BV/rela/internal/predicatefns"
)

// TraversalBinder answers the `related(...)` traversals of `when:` programs
// for a batch of entities of one type (TKT-205V2N). Defined at the consumer;
// the wiring site supplies a raw-store binder. A grant is an authorization
// decision, so it reads the graph as it is, like has_relation does, and not
// through the reader it is deciding for.
type TraversalBinder interface {
	Bind(ctx context.Context, entityType string, ids []string, progs ...*predicate.Program) (
		func(rowID string) predicate.TraversalFunc, error)
}

// Option configures [New].
type Option func(*PolicyResolver)

// WithTraversals wires the binder that answers `related(...)` in `when:`.
// Without it, a `when:` using related() is a compile error.
func WithTraversals(b TraversalBinder) Option {
	return func(r *PolicyResolver) { r.traversals = b }
}

// errNotAnswerable is why a traversing grant is denied without a store call.
var errNotAnswerable = errors.New("related(...) cannot be answered here")

// traversalMemo holds answers primed for a batch of rows, keyed by entity
// type then id. It lives on the ctx of ONE operation for ONE principal and is
// never shared beyond it.
//
// The key is shared by every [PolicyResolver]. That is safe because the
// answers come from the raw graph, not from any principal's reads, and
// relresolve.Answers.For refuses a traversal it was not asked, which denies
// the grant. Do not turn a miss into a guess.
type traversalMemo struct {
	mu   sync.Mutex
	rows map[string]map[string]rowAnswer
}

type rowAnswer struct {
	fn  predicate.TraversalFunc
	err error
}

type traversalMemoKey struct{}

// PrimeTraversals answers, for every row, the `related(...)` of every grant
// on its type, with one store query per distinct traversal and type, and
// returns a ctx carrying the answers. [PolicyResolver.FieldVerdicts],
// RelationVerdicts and TransitionVerdicts on that ctx then read them instead
// of querying per row. A row that was not primed is answered live, so priming
// is an optimization and never a correctness condition.
//
// Priming again on the returned ctx adds to the same memo, so a caller that
// works in chunks primes each chunk. Rows the memo already holds are skipped.
func (r *PolicyResolver) PrimeTraversals(ctx context.Context, rows []*entity.Entity) context.Context {
	if r.traversals == nil || len(r.traversalProgs) == 0 {
		return ctx
	}
	memo, ok := ctx.Value(traversalMemoKey{}).(*traversalMemo)
	if !ok {
		memo = &traversalMemo{rows: map[string]map[string]rowAnswer{}}
		ctx = context.WithValue(ctx, traversalMemoKey{}, memo)
	}
	idsByType := map[string][]string{}
	memo.mu.Lock()
	for _, e := range rows {
		if e == nil || e.Face != "" || e.ID == "" || len(r.traversalProgs[e.Type]) == 0 {
			continue // answered (or refused) live
		}
		if _, done := memo.rows[e.Type][e.ID]; done {
			continue
		}
		idsByType[e.Type] = append(idsByType[e.Type], e.ID)
	}
	memo.mu.Unlock()
	for typ, ids := range idsByType {
		bound, err := r.traversals.Bind(ctx, typ, ids, r.traversalProgs[typ]...)
		memo.mu.Lock()
		if memo.rows[typ] == nil {
			memo.rows[typ] = map[string]rowAnswer{}
		}
		for _, id := range ids {
			if err != nil {
				memo.rows[typ][id] = rowAnswer{err: err}
			} else {
				memo.rows[typ][id] = rowAnswer{fn: bound(id)}
			}
		}
		memo.mu.Unlock()
	}
	return ctx
}

// traversalFor returns the answers to e's grant traversals: primed if ctx
// carries them, otherwise from one live bind for e alone.
//
// It refuses a historical subject (the live graph does not describe the
// entity as of that version) and a row on a named face (the store answers
// from the default face's edges). The caller denies the grant, which is the
// closed direction.
func (r *PolicyResolver) traversalFor(ctx context.Context, e *entity.Entity) (predicate.TraversalFunc, error) {
	switch {
	case isHistoricalSubject(ctx):
		return nil, fmt.Errorf("%w: historical subject", errNotAnswerable)
	case e.Face != "":
		return nil, fmt.Errorf("%w: face %q", errNotAnswerable, e.Face)
	case r.traversals == nil:
		// coverage-ignore: invariant: New refuses a traversing `when:` without a binder
		return nil, fmt.Errorf("%w: no store is wired", errNotAnswerable)
	}
	memo, _ := ctx.Value(traversalMemoKey{}).(*traversalMemo)
	if memo != nil {
		memo.mu.Lock()
		ans, hit := memo.rows[e.Type][e.ID]
		memo.mu.Unlock()
		if hit {
			return ans.fn, ans.err
		}
	}
	ans := rowAnswer{}
	if bound, err := r.traversals.Bind(ctx, e.Type, []string{e.ID}, r.traversalProgs[e.Type]...); err != nil {
		ans.err = err
	} else {
		ans.fn = bound(e.ID)
	}
	// A primed ctx keeps the live answer too, so the next verdict call for
	// e in the same operation does not query again.
	if memo != nil {
		memo.mu.Lock()
		if memo.rows[e.Type] == nil {
			memo.rows[e.Type] = map[string]rowAnswer{}
		}
		memo.rows[e.Type][e.ID] = ans
		memo.mu.Unlock()
	}
	return ans.fn, ans.err
}

// warnConditionallyVisible logs a load warning for a grant or transition
// traversal that filters on a property the policy does not show to everyone.
// The rule is kept: it reads the raw graph, so its verdict is correct, but a
// principal who cannot see that property can learn its value from the
// verdict. The same filter in a view or next-action condition is refused per
// request by the reader's traversal gate.
func warnConditionallyVisible(
	meta *metamodel.Metamodel, policy *acl.Policy, entityType, where string, prog *predicate.Program,
) {
	for _, spec := range prog.Traversals() {
		hops, err := predicatefns.ResolveTraversal(meta, entityType, spec)
		if err != nil || len(hops) == 0 {
			continue // compile reports it
		}
		target := hops[len(hops)-1].Target
		for _, prop := range spec.PropNames() {
			if policy.ConditionallyVisible(target, prop) {
				slog.Warn("acl: a when: related() filters on a property not visible to every role",
					"where", where, "type", target, "property", prop)
			}
		}
	}
}

// refuseIdentityTraversal refuses a `when:` traversal constrained by
// current_user.id.
//
// A grant's traversals are answered by the binder, which reads the identity
// from the request context, while the grant itself binds current_user from
// this resolver's own principal resolution. Two derivations of "who is
// calling" would only ever disagree by failing the grant, but a grant is
// authorization, and it must not depend on them agreeing. Relation
// ownership as an authorization input is what `role_relations:` is for.
func refuseIdentityTraversal(prog *predicate.Program) error {
	for _, spec := range prog.Traversals() {
		if len(spec.Refs) > 0 {
			return fmt.Errorf("%s(...) cannot compare against %s.%s in acl.yaml; use role_relations to grant "+
				"through a relation to the user", predicate.FuncRelated, predicatefns.VarCurrentUser,
				predicatefns.FieldCurrentUserID)
		}
	}
	return nil
}
