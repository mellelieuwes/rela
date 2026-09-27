package acl

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/Sourcehaven-BV/rela/internal/store"
)

// TraversalHop describes one hop of a caller-supplied relation traversal,
// before it is authorized. It is the UNGATED request — what the condition
// author asked for — which [Request.GateTraversal] turns into a
// [store.RelationPredicate] the principal is actually allowed to run.
//
// Callers build these from a compiled condition; they must never hand a
// store predicate straight to a query (see the security note on
// [store.RelationPredicate.EndpointMatch]).
type TraversalHop struct {
	// RelationTypes restricts which edges the hop follows. Empty follows
	// any type, matching [store.RelationPredicate.OfTypes].
	RelationTypes []string

	// Incoming walks the edge backwards: the hop stands on the relation's TO
	// side and lands on its FROM side (TKT-CXQEV0).
	Incoming bool

	// EntityType is the type the hop lands on. REQUIRED: the gate resolves a
	// read query for it, and there is no read query for "some type". This
	// is the same ascription that resolves a union-targeted relation, so a
	// caller that already needs it for typing has it to hand.
	EntityType string

	// Props are the endpoint property filters the author wrote.
	Props []store.PropPredicate

	// EndpointIDs restricts the hop to the entities with these ids, as
	// [store.RelationPredicate.Endpoints]. Nil leaves the id unconstrained.
	//
	// Non-nil but EMPTY is refused rather than lowered: the store reads an
	// empty Endpoints as "any endpoint", so a caller that meant "this id" and
	// lost the id (an unresolved current user, say) would otherwise widen the
	// hop to every related entity. A blank id is refused for the same reason.
	EndpointIDs []string

	// Next chains one hop further. Nil ends the chain.
	Next *TraversalHop
}

// ErrTraversalDenied reports that a traversal cannot be authorized: the
// principal may not read a traversed-to type at all, so no rows could
// match. Callers present it as "no candidate matches", which is exact:
// a hidden entity is a nonexistent one.
var ErrTraversalDenied = errors.New("acl: traversal denied")

// ErrTraversalUnsupported reports that a traversal cannot be gated for this
// principal: the far type is only PARTLY readable in a way an endpoint
// predicate cannot express (a face restriction, a disjunctive ceiling, a read
// granted through inheritance), or the property is one the principal may not
// filter on.
//
// Distinct from [ErrTraversalDenied] because "no match" is NOT a safe
// reading here: under `not related(...)` it would match every candidate and
// widen the filter. Callers surface it as an error.
var ErrTraversalUnsupported = errors.New("acl: traversal cannot be gated")

// TraversalQuery builds the query that answers a lowered traversal for rows
// of candidateType: the predicate goes in the slot matching the first hop's
// direction. Built fresh rather than folded into an existing query, whose
// ACL predicate may already occupy that slot.
func TraversalQuery(candidateType string, hop TraversalHop, p *store.RelationPredicate) store.GraphQuery {
	q := store.GraphQuery{EntityType: candidateType}
	if hop.Incoming {
		q.HasInbound = p
	} else {
		q.HasOutbound = p
	}
	return q
}

// UngatedTraversal lowers a traversal with NO authorization. It exists for
// deployments that configure no ACL policy, where every read is already
// unrestricted; anything with a policy goes through [Request.GateTraversal].
func UngatedTraversal(hop TraversalHop) (*store.RelationPredicate, error) {
	return lowerTraversal(hop, func(h TraversalHop) (*store.EndpointPredicate, error) {
		if h.EntityType == "" {
			return nil, errors.New("acl: traversal hop: entity type is required")
		}
		return &store.EndpointPredicate{EntityType: h.EntityType, Props: slices.Clone(h.Props)}, nil
	})
}

// lowerTraversal turns a hop chain into a store predicate, asking endpoint
// for each hop's endpoint filter. It is the ONE place direction and chaining
// are decided, so the gated and ungated lowerings cannot disagree about them.
func lowerTraversal(
	hop TraversalHop, endpoint func(TraversalHop) (*store.EndpointPredicate, error),
) (*store.RelationPredicate, error) {
	if hop.EndpointIDs != nil && (len(hop.EndpointIDs) == 0 || slices.Contains(hop.EndpointIDs, "")) {
		// Not ErrTraversalUnsupported: this is no principal's grant shape but
		// a caller that lost the id, and it must fail as an internal error.
		return nil, fmt.Errorf("acl: traversal hop into %q names an empty endpoint id", hop.EntityType)
	}
	match, err := endpoint(hop)
	if err != nil {
		return nil, err
	}
	if next := hop.Next; next != nil {
		nested, err := lowerTraversal(*next, endpoint)
		if err != nil {
			return nil, err
		}
		slot := &match.HasOutbound
		if next.Incoming {
			slot = &match.HasInbound
		}
		if *slot != nil {
			// The endpoint's read query already constrains its inbound
			// edges, and EndpointPredicate has one slot per direction.
			// Overwriting would drop that row gate; there is no field to
			// merge into. Refuse.
			return nil, fmt.Errorf("%w: chained incoming hop into %q collides with its read gate",
				ErrTraversalUnsupported, hop.EntityType)
		}
		*slot = nested
	}
	return &store.RelationPredicate{
		OfTypes: hop.RelationTypes, Endpoints: slices.Clone(hop.EndpointIDs), EndpointMatch: match,
	}, nil
}

// GateTraversal compiles an untrusted traversal request into a store
// predicate the principal is permitted to evaluate.
//
// # Why this exists
//
// A traversal filters on properties of entities the query does NOT return.
// Nothing about the neighbor is ever serialized, so response redaction never
// fires — the leak is the CORRELATION between the filter and the result set,
// and it is binary-searchable: repeated queries narrow a hidden value
// arbitrarily. Two secrets are at stake, both named in docs/acl-security.md:
// a hidden entity's EXISTENCE, and a redacted field's VALUE.
//
// Three rules, all required, none sufficient alone:
//
//  1. ROW GATE. Each hop's read query for its entity type is ANDed into that
//     hop, so a traversal can only match neighbors the principal may read.
//     DenyAll on any hop fails the whole traversal closed.
//  2. FIELD GATE. A property whose `visible:` grant is CONDITIONAL is not
//     filterable, at all, by anyone. The row gate does not help here: the
//     principal can see the row and still not be entitled to the value, and a
//     filter would reveal it. See [Policy.ConditionallyVisible]. The policy
//     is the Request's own, so no caller can skip this by passing none.
//  3. NO INHERITANCE EXPANSION. Every hop's gate sits inside an endpoint
//     match — a NESTED position, where no backend emits InheritThrough or
//     EntityInheritThrough. A read granted through inheritance is therefore
//     [ErrTraversalUnsupported], never silently dropped.
//
// A denied read on ANY hop returns [ErrTraversalDenied]; a read the
// predicate cannot express returns [ErrTraversalUnsupported]. Neither ever
// widens.
func (r *Request) GateTraversal(
	ctx context.Context, candidateType string, hop TraversalHop,
) (*store.RelationPredicate, error) {
	// An outgoing first hop reads edges owned by the CANDIDATE, and the
	// store reads them from the default state's tail. That tail holds the
	// default face's content-scoped edges, so for a reader granted only
	// named faces of the candidate type it would match on edges from a face
	// they cannot read. An incoming first hop reads the far entity's edges,
	// and gateHop already refuses a face-restricted far type.
	if !hop.Incoming {
		if candidateType == "" {
			return nil, errors.New("acl: traversal: candidate type is required")
		}
		if rq := r.ReadQuery(ctx, candidateType); len(rq.Faces) > 0 {
			return nil, fmt.Errorf("%w: %q is readable only on named faces, whose edges a traversal cannot read",
				ErrTraversalUnsupported, candidateType)
		}
	}
	return lowerTraversal(hop, func(h TraversalHop) (*store.EndpointPredicate, error) {
		return r.gateHop(ctx, h)
	})
}

// gateHop builds one hop's endpoint filter: the author's props plus the rows
// of h.EntityType this principal may read.
func (r *Request) gateHop(ctx context.Context, hop TraversalHop) (*store.EndpointPredicate, error) {
	if hop.EntityType == "" {
		// Without a type there is nothing to authorize against. Refusing is
		// the only safe reading: resolving "any type" would mean composing no
		// read query, i.e. an ungated traversal.
		return nil, errors.New("acl: traversal hop: entity type is required")
	}

	// TWO field checks, and both are needed.
	//
	// The policy-wide one is load-time and principal-independent (see
	// ConditionallyVisible). The ceiling one is necessarily per-principal: a
	// client attenuated by `client_baselines` / `scope_grants` may be denied a
	// field its USER holds unconditionally, and the policy-wide question
	// cannot see that. Without it a traversal filter would reach further than
	// a plain read for the same principal, which is precisely what the
	// ceiling exists to prevent — and it only ever narrows, so refusing more
	// for an attenuated client is the correct direction.
	ceiling := r.FieldCeilingFor(hop.EntityType)
	for _, p := range hop.Props {
		if r.d.policy.ConditionallyVisible(hop.EntityType, p.Property) {
			return nil, fmt.Errorf(
				"%w: property %q on %q is not unconditionally visible and cannot be filtered on",
				ErrTraversalUnsupported, p.Property, hop.EntityType)
		}
		if ceilingHidesField(ceiling, p.Property) {
			return nil, fmt.Errorf(
				"%w: property %q on %q is hidden by the client ceiling %q and cannot be filtered on",
				ErrTraversalUnsupported, p.Property, hop.EntityType, ceiling.Baseline)
		}
	}

	rq := r.ReadQuery(ctx, hop.EntityType)
	if rq.DenyAll {
		return nil, ErrTraversalDenied
	}
	// A face-restricted read means only SOME content states of the endpoint
	// are readable, and [store.EndpointPredicate] has no face field — so the
	// predicate cannot express the restriction. Checked before the query is
	// folded so the refusal cannot be reached with a half-built predicate.
	if len(rq.Faces) > 0 {
		return nil, fmt.Errorf("%w: read of %q is face-restricted", ErrTraversalUnsupported, hop.EntityType)
	}
	// A disjunctive authorization ceiling cannot be expressed inside a single
	// endpoint predicate without flattening it into an OR that
	// EndpointPredicate has no field for.
	if !rq.AllowAll && rq.Query != nil && len(rq.Query.Any) > 0 {
		return nil, fmt.Errorf("%w: read of %q is a disjunction", ErrTraversalUnsupported, hop.EntityType)
	}
	if !rq.AllowAll && rq.Query != nil && !foldsIntoEndpoint(*rq.Query) {
		return nil, fmt.Errorf("%w: read of %q constrains more than an endpoint can carry",
			ErrTraversalUnsupported, hop.EntityType)
	}

	// COPY the caller's props rather than aliasing them. `append` onto the
	// caller's slice may write into its spare capacity, so a TraversalHop
	// reused across two gate calls would have one call's ACL predicates
	// silently overwritten by the next — and the surviving predicate would
	// be the WRONG principal's.
	props := make([]store.PropPredicate, len(hop.Props), len(hop.Props)+len(readQueryProps(rq)))
	copy(props, hop.Props)
	match := &store.EndpointPredicate{EntityType: hop.EntityType, Props: props}

	// Fold the parts of the read query that constrain WHICH ROWS are
	// readable. AllowAll contributes nothing, which is correct rather than an
	// omission.
	if !rq.AllowAll && rq.Query != nil {
		match.Props = append(match.Props, rq.Query.Props...)
		if inb := rq.Query.HasInbound; inb != nil {
			if len(inb.InheritThrough) > 0 || len(inb.EntityInheritThrough) > 0 {
				// See rule 3: this sits in a nested position on every hop,
				// where every backend refuses the expansion.
				return nil, fmt.Errorf("%w: read of %q is granted through inheritance",
					ErrTraversalUnsupported, hop.EntityType)
			}
			match.HasInbound = inb
		}
	}
	return match, nil
}

// foldsIntoEndpoint reports whether a read query uses only the fields gateHop
// folds into an endpoint (EntityType, Props, HasInbound). Anything else is a
// constraint the traversal would silently drop, leaving it wider than a plain
// read, so a new GraphQuery field is refused here until gateHop handles it.
func foldsIntoEndpoint(q store.GraphQuery) bool {
	q.EntityType, q.Props, q.HasInbound = "", nil, nil
	return reflect.ValueOf(q).IsZero()
}

// ConditionallyVisible reports whether a
// property's read visibility is anything less than unconditional for EVERY
// role, in which case it must not be filterable.
//
// Two ways a field can be non-public, and BOTH have to be caught:
//
//  1. A `visible:` grant carrying a `when:` — visibility depends on the row.
//  2. A role declaring a `visible:` block for the type that does NOT list the
//     property. `visible:` is a CLOSED WORLD per role (see
//     applyFieldGrants in internal/affordances/resolver.go, which opts the
//     whole dimension in as soon as a role declaresVisible): everything the
//     role does not name is hidden, with no `when:` string anywhere to find.
//
// Case 2 is the likelier spelling — an operator writes `visible: [name,
// title]` intending `salary` to be hidden — and it is the one a
// `when:`-only check silently misses, returning "filterable" for exactly the
// fields that are most thoroughly hidden.
//
// The question is deliberately POLICY-WIDE and principal-independent. A
// per-principal answer would make the set of filterable properties vary by
// role, so two principals would get different compile results for the same
// condition — and the one who could filter would be an oracle for the one who
// could not. Asking of the policy instead means the refusal is a property of
// the SCHEMA, reported identically to everyone, which is also what lets it be
// a load-time error rather than a request-time one. It is NOT a substitute
// for the per-principal ceiling check; see [Request.GateTraversal].
func (p *Policy) ConditionallyVisible(entityType, property string) bool {
	if p == nil {
		return false
	}
	for _, role := range p.Roles {
		grants, declared := role.Visible[entityType]
		if !declared {
			// This role gates no fields on the type, so it hides nothing.
			continue
		}
		granted := false
		for _, g := range grants {
			if g.Field != property {
				continue
			}
			if g.When != "" {
				return true // case 1: conditional
			}
			granted = true
		}
		if !granted {
			return true // case 2: closed world, not listed
		}
	}
	return false
}

// ceilingHidesField reports whether a compiled field ceiling withholds
// property. Visible is a CLOSED WORLD when non-nil (anything unlisted is
// hidden); Redact is a denial list.
func ceilingHidesField(c FieldCeiling, property string) bool {
	if !c.Constrains() {
		return false
	}
	if slices.Contains(c.Redact, property) {
		return true
	}
	return c.Visible != nil && !slices.Contains(c.Visible, property)
}

// readQueryProps returns the read query's own property predicates, or nil.
// Used only to size the copy in GateTraversal.
func readQueryProps(rq ReadQueryResult) []store.PropPredicate {
	if rq.AllowAll || rq.Query == nil {
		return nil
	}
	return rq.Query.Props
}
