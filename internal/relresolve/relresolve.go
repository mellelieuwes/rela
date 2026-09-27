// Package relresolve answers the `related(...)` traversals of compiled
// predicate programs against a store, in batches (TKT-205V2N).
//
// The predicate engine never reads a store: a program containing a traversal
// fails at Eval unless the caller binds a [predicate.TraversalFunc]. This
// package is the one place that turns a program's traversals into gated store
// queries and hands back that function, so every surface that evaluates
// `related(...)` answers it the same way.
//
// Answering is batched: one [Match] call per distinct traversal over a whole
// candidate set, never one per row. A caller with a single entity passes a
// list of one.
//
// The gate decides what a traversal may see. A principal-facing surface passes
// the principal's gate ([acl.Request.GateTraversal] or an adapter over it); an
// operator- or system-trust surface passes [Ungated]. The answers depend on
// the gate, so they belong to the operation that computed them and must never
// be cached across principals.
package relresolve

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/predicate"
	"github.com/Sourcehaven-BV/rela/internal/predicatefns"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// Gate authorizes one traversal for rows of candidateType and returns the
// store predicate that answers it. [acl.ErrTraversalDenied] means the
// traversal matches nothing; any other error is returned to the caller.
//
// An alias rather than a defined type, so a consumer can restate the func
// type in its own narrow interface without importing this package.
type Gate = func(ctx context.Context, candidateType string, hop acl.TraversalHop) (*store.RelationPredicate, error)

// Match reports, for every id in ids, whether it satisfies q. It is
// [store.GraphQueryer.MatchingIDs], possibly wrapped to stamp a world or
// face restriction onto q. An alias for the same reason as [Gate].
type Match = func(ctx context.Context, q store.GraphQuery, ids []string) (map[string]bool, error)

// Ungated lowers a traversal with no authorization. It is for operator- and
// system-trust surfaces (the CLI, automations) and for deployments with no
// ACL policy, where every read is already unrestricted.
func Ungated(_ context.Context, _ string, hop acl.TraversalHop) (*store.RelationPredicate, error) {
	return acl.UngatedTraversal(hop)
}

// Answers holds, per traversal (keyed by [predicate.TraversalSpec.Key]), the
// candidate ids that satisfy it, and the ids it was asked about. Only those
// ids may be read back: see [Answers.For].
type Answers struct {
	asked  map[string]bool
	bySpec map[string]map[string]bool
}

// Answer answers every spec for the candidate ids, with one gated store query
// per distinct spec.
//
// A traversal the principal may not read at all ([acl.ErrTraversalDenied])
// matches no candidate: a hidden entity is a nonexistent one. Every other
// gate refusal is returned as an error, because "no match" would widen a
// negated traversal.
func Answer(
	ctx context.Context, meta *metamodel.Metamodel, gate Gate, match Match,
	entityType string, specs []predicate.TraversalSpec, ids []string,
) (Answers, error) {
	answers := Answers{asked: make(map[string]bool, len(ids)), bySpec: make(map[string]map[string]bool, len(specs))}
	for _, id := range ids {
		if id == "" {
			// An unpersisted entity has no edges to ask about; "no match"
			// would pass a negated traversal on it.
			return Answers{}, errors.New("related: the entity has no id yet")
		}
		answers.asked[id] = true
	}
	if len(specs) == 0 {
		return answers, nil
	}
	if gate == nil || match == nil {
		return Answers{}, errors.New("relresolve: no gate or store to answer a traversal")
	}
	for _, unbound := range specs {
		spec, err := bindIdentity(ctx, unbound)
		if err != nil {
			return Answers{}, err
		}
		key := spec.Key()
		if _, done := answers.bySpec[key]; done {
			continue
		}
		if len(ids) == 0 {
			answers.bySpec[key] = map[string]bool{}
			continue
		}
		hop, err := Hop(meta, entityType, spec)
		if err != nil {
			return Answers{}, err
		}
		pred, err := gate(ctx, entityType, hop)
		if errors.Is(err, acl.ErrTraversalDenied) {
			answers.bySpec[key] = map[string]bool{}
			continue
		}
		if err != nil {
			return Answers{}, err
		}
		got, err := match(ctx, acl.TraversalQuery(entityType, hop, pred), ids)
		if err != nil {
			return Answers{}, err
		}
		answers.bySpec[key] = got
	}
	return answers, nil
}

// bindIdentity binds a traversal's `current_user.id` constraints to the query
// identity on ctx: the value [predicatefns.BindCurrentUser] binds for the same
// request, so the batch answer and the per-row evaluation read one identity.
// They key their answers by the BOUND spec, so if they ever disagreed the row
// would find no answer and fail, rather than read another identity's answer.
//
// No identity is [predicatefns.ErrNoCurrentUser], never an unbound traversal.
func bindIdentity(ctx context.Context, spec predicate.TraversalSpec) (predicate.TraversalSpec, error) {
	if len(spec.Refs) == 0 {
		return spec, nil
	}
	var identity string
	if q, ok := predicatefns.QueryIdentityFrom(ctx); ok {
		identity = q.ID()
	}
	return predicatefns.BindTraversal(spec, identity)
}

// Hop lowers a compiled traversal into the ungated hop chain a [Gate]
// authorizes. Resolution goes through [predicatefns.ResolveTraversal], the
// same walk load-time validation and index derivation use, so the three
// cannot disagree about where a chain lands.
//
// The author's property and id filters constrain the FINAL hop only;
// intermediate hops constrain type and relation. The spec must be bound (see
// [predicatefns.BindTraversal]): a `current_user.id` constraint has no value
// to lower until it is.
func Hop(meta *metamodel.Metamodel, entityType string, spec predicate.TraversalSpec) (acl.TraversalHop, error) {
	if len(spec.Refs) > 0 {
		return acl.TraversalHop{}, errors.New("related: the traversal reads current_user and has not been bound")
	}
	hops, err := predicatefns.ResolveTraversal(meta, entityType, spec)
	if err != nil {
		return acl.TraversalHop{}, err
	}
	if len(hops) == 0 {
		// coverage-ignore: invariant: the engine refuses an empty relation path at compile
		return acl.TraversalHop{}, errors.New("related: empty relation path")
	}
	props := make([]store.PropPredicate, 0, len(spec.Props))
	for _, name := range spec.PropNames() {
		str, ok := spec.Props[name].(predicate.String)
		if !ok {
			// Load-time validation refuses this; guard rather than lower a
			// comparison the store would read differently.
			return acl.TraversalHop{}, fmt.Errorf("related: property %q must be compared against a string", name)
		}
		props = append(props, store.PropPredicate{
			Property: name, Op: store.PropEqual, Value: str.String(), Scalar: true,
		})
	}
	var endpointIDs []string
	if spec.ID != nil {
		id, ok := spec.ID.(predicate.String)
		if !ok || id.String() == "" {
			// The engine refuses both at compile and Bind refuses them when
			// binding; an empty id would widen to "any endpoint".
			return acl.TraversalHop{}, errors.New("related: id must be a non-empty string")
		}
		endpointIDs = []string{id.String()}
	}

	var next *acl.TraversalHop
	for i := len(hops) - 1; i >= 0; i-- {
		h := &acl.TraversalHop{
			RelationTypes: []string{hops[i].Relation},
			Incoming:      hops[i].Incoming,
			EntityType:    hops[i].Target,
			Next:          next,
		}
		if i == len(hops)-1 {
			h.Props = props
			h.EndpointIDs = endpointIDs
		}
		next = h
	}
	return *next, nil
}

// For answers a traversal for ONE row from precomputed answers.
//
// It refuses rather than guesses: a row the answers were not computed for, a
// subject other than the row itself, or a traversal nobody answered, is an
// error. Returning false would present an unanswered question as a
// legitimate "no match", which passes a negated traversal.
func (a Answers) For(rowID string) predicate.TraversalFunc {
	return func(subject predicate.Value, spec predicate.TraversalSpec) (bool, error) {
		if rowID == "" || !a.asked[rowID] {
			return false, fmt.Errorf("traversal was not answered for %q", rowID)
		}
		rec, ok := subject.(predicate.Record)
		if !ok {
			return false, errors.New("traversal subject is not a record")
		}
		if id, _ := rec.Get("id"); id != predicate.NewString(rowID) {
			return false, errors.New("traversal subject is not the row being evaluated")
		}
		ans, ok := a.bySpec[spec.Key()]
		if !ok {
			return false, fmt.Errorf("traversal %v was not answered", spec.Path)
		}
		return ans[rowID], nil
	}
}

// Binder answers the traversals of programs for one surface: its gate and
// store query are fixed at construction, the metamodel is the one its caller
// compiled against.
type Binder struct {
	meta  *metamodel.Metamodel
	gate  Gate
	match Match
}

// NewBinder builds a Binder. Nil: rejected for all three — a missing gate or
// store would otherwise surface as a traversal error on the first row.
func NewBinder(meta *metamodel.Metamodel, gate Gate, match Match) (*Binder, error) {
	switch {
	case meta == nil:
		return nil, errors.New("relresolve: NewBinder: metamodel is required")
	case gate == nil:
		return nil, errors.New("relresolve: NewBinder: gate is required")
	case match == nil:
		return nil, errors.New("relresolve: NewBinder: match is required")
	}
	return &Binder{meta: meta, gate: gate, match: match}, nil
}

// NewStoreBinder is [NewBinder] answering through st.MatchingIDs. Nil:
// rejected — taking the method value of a nil interface would panic before
// NewBinder could refuse it.
func NewStoreBinder(meta *metamodel.Metamodel, gate Gate, st store.GraphQueryer) (*Binder, error) {
	if st == nil {
		return nil, errors.New("relresolve: NewStoreBinder: store is required")
	}
	return NewBinder(meta, gate, st.MatchingIDs)
}

// Bind answers every traversal in progs for the candidate ids of entityType
// and returns the per-row resolver. Traversals shared between programs (a
// rule's when and then, a type's grants) are answered once.
//
// With no traversal in any program it makes no store call and the returned
// function yields nil, which [predicatefns.Evaluator.MatchesWithTraversals]
// accepts.
func (b *Binder) Bind(
	ctx context.Context, entityType string, ids []string, progs ...*predicate.Program,
) (func(rowID string) predicate.TraversalFunc, error) {
	specs := Specs(progs...)
	if len(specs) == 0 {
		return func(string) predicate.TraversalFunc { return nil }, nil
	}
	answers, err := Answer(ctx, b.meta, b.gate, b.match, entityType, specs, dedupe(ids))
	if err != nil {
		return nil, err
	}
	return answers.For, nil
}

// Specs collects the traversals of progs, deduplicated by key. Nil programs
// are skipped, so a caller can pass an optional when/then pair as is.
func Specs(progs ...*predicate.Program) []predicate.TraversalSpec {
	var out []predicate.TraversalSpec
	seen := map[string]bool{}
	for _, p := range progs {
		if p == nil {
			continue
		}
		for _, s := range p.Traversals() {
			if k := s.Key(); !seen[k] {
				seen[k] = true
				out = append(out, s)
			}
		}
	}
	return out
}

func dedupe(ids []string) []string {
	if len(ids) < 2 {
		return ids
	}
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}
