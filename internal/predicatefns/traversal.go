package predicatefns

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/predicate"
)

// ValidateTraversals checks every traversal a compiled program contains
// against the metamodel, resolving each hop's target entity type.
//
// The engine itself cannot do this: internal/predicate depends on nothing, so
// it accepts `related(entity, 'caused-by', {...})` without knowing whether
// `caused-by` exists or what it points at. This is where that is decided, and
// it is deliberately a LOAD-time check — a condition that cannot be resolved
// is a config error naming the relation, not a query that quietly matches
// nothing.
//
// # Why the type ascription is mandatory on a union
//
// A relation's target is a SET (`RelationDef.To`). When it names more than one
// type, a property reference has no single declared type to be compared
// against: in the live tickets schema, `status` is declared on nearly every
// type with a DIFFERENT enum type each (`ticket_status`, `bug_status`,
// `concept_status`), so `'done'` may be valid for one target and meaningless
// for another. Rather than silently pick one, require `type =`.
//
// A single-target relation needs no ascription — there is nothing to
// disambiguate — which keeps the common case clean.
func ValidateTraversals(meta *metamodel.Metamodel, fromType string, prog *predicate.Program) error {
	if meta == nil || prog == nil {
		return nil
	}
	for _, spec := range prog.Traversals() {
		if err := validateTraversal(meta, fromType, spec); err != nil {
			return err
		}
	}
	return nil
}

// traversalSubject is the only identifier a traversal may start from. The
// path resolves from the type of the row being evaluated, so any other record
// (`current_user`, say) would be validated, indexed and answered as if it
// started from the wrong type.
const traversalSubject = "entity"

func validateTraversal(meta *metamodel.Metamodel, fromType string, spec predicate.TraversalSpec) error {
	target, err := ResolveTraversalTarget(meta, fromType, spec)
	if err != nil {
		return err
	}
	if err := validateTraversalRefs(spec); err != nil {
		return err
	}
	return validateTraversalProps(meta, target, spec)
}

// validateTraversalRefs allows exactly one variable as a constraint value:
// current_user.id. It is the only value that is constant for a request AND
// that every surface answering a traversal can bind the same way
// ([BindTraversal] reads the identity [BindCurrentUser] binds). current_user.tool
// is refused for the reason ConditionPrefilters refuses it: it describes the
// transport, and must not become a membership input.
func validateTraversalRefs(spec predicate.TraversalSpec) error {
	for key, ref := range spec.Refs {
		if ref.Var != VarCurrentUser || ref.Field != FieldCurrentUserID {
			return fmt.Errorf("related: %q compares against %s.%s; the only variable a constraint may read is %s.%s",
				key, ref.Var, ref.Field, VarCurrentUser, FieldCurrentUserID)
		}
	}
	return nil
}

// BindTraversal returns spec with its `current_user.id` constraints replaced
// by identity, for a caller that answers or lowers the traversal outside
// Eval. identity is the value [BindCurrentUser] binds to current_user.id for
// the same request, so the answer and the evaluation read one identity.
//
// A spec with no Refs is returned unchanged. An empty identity is
// [ErrNoCurrentUser], never an unbound spec: an id constraint left empty
// lowers to an endpoint set the store reads as "any endpoint".
func BindTraversal(spec predicate.TraversalSpec, identity string) (predicate.TraversalSpec, error) {
	if len(spec.Refs) == 0 {
		return spec, nil
	}
	if err := validateTraversalRefs(spec); err != nil {
		return predicate.TraversalSpec{}, err
	}
	if identity == "" {
		return predicate.TraversalSpec{}, ErrNoCurrentUser
	}
	values := make(map[string]predicate.Value, len(spec.Refs))
	for key := range spec.Refs {
		values[key] = predicate.NewString(identity)
	}
	return spec.Bind(values)
}

// ResolvedHop is one hop of a traversal after the metamodel has resolved it:
// the canonical relation type, the direction it is walked in, and the entity
// type the hop lands on.
type ResolvedHop struct {
	// Relation is the CANONICAL relation type, even when the author wrote its
	// inverse ID. Stored edges carry only canonical names.
	Relation string

	// Incoming is true when the hop walks the edge backwards (TO -> FROM),
	// which is what writing the relation's inverse ID means.
	Incoming bool

	// Target is the entity type at the far end of the hop.
	Target string
}

// ResolveTraversalTarget walks a traversal's hops and returns the entity type
// the FINAL hop lands on, or an error naming why it cannot be resolved. It is
// [ResolveTraversal] for callers that only need the final type.
func ResolveTraversalTarget(
	meta *metamodel.Metamodel, fromType string, spec predicate.TraversalSpec,
) (string, error) {
	hops, err := ResolveTraversal(meta, fromType, spec)
	if err != nil || len(hops) == 0 {
		return "", err
	}
	return hops[len(hops)-1].Target, nil
}

// ResolveTraversal walks a traversal's hops against the metamodel.
//
// This is the ONE definition of how a chain resolves, shared by validation
// (which surfaces the error at config load), by index derivation in
// internal/queryplan (which treats any error as "derive no index") and by the
// lowering into an ACL-gated store predicate. They must agree: if validation
// accepts a chain that derivation cannot resolve, the condition loads fine
// and its index is silently never created — the query then scans every row
// of the target type, which is the regression the derived index exists to
// prevent. Two copies of this walk WILL drift, and that is the direction they
// drift in.
//
// # Direction
//
// A path element names either a canonical relation type (walked FROM -> TO)
// or the inverse ID a relation declares (walked TO -> FROM). Canonical names
// are tried first; the metamodel loader already refuses an inverse ID that
// shadows a canonical name, so the two lookups cannot both match.
//
// A symmetric relation is refused in either spelling. Its edges are stored
// once, in whichever direction they were written, so walking one direction
// would silently miss the edges written the other way.
func ResolveTraversal(
	meta *metamodel.Metamodel, fromType string, spec predicate.TraversalSpec,
) ([]ResolvedHop, error) {
	if meta == nil {
		return nil, errors.New("related: no metamodel")
	}
	if spec.Subject != traversalSubject {
		return nil, fmt.Errorf("related: the first argument must be %q", traversalSubject)
	}
	hops := make([]ResolvedHop, 0, len(spec.Path))
	current := fromType
	for i, name := range spec.Path {
		relType, incoming, def, err := resolveHopRelation(meta, name)
		if err != nil {
			return nil, err
		}
		// Which side we stand on and which side we walk to depends on the
		// direction; everything after this is direction-agnostic.
		near, far := def.From, def.To
		if incoming {
			near, far = def.To, def.From
		}
		// The near side must admit the type we are standing on. Checking it
		// turns a traversal that could never match into a load error naming
		// the relation, rather than a silently empty result.
		if current != "" && len(near) > 0 && !slices.Contains(near, current) {
			if incoming {
				return nil, fmt.Errorf("related: %q walks %q backwards, which does not end at %q (declared to: %s)",
					name, relType, current, strings.Join(near, ", "))
			}
			return nil, fmt.Errorf("related: relation %q does not start from %q (declared from: %s)",
				relType, current, strings.Join(near, ", "))
		}

		target, err := resolveHopTarget(name, far, i == len(spec.Path)-1, spec.EntityType)
		if err != nil {
			return nil, err
		}
		current = target
		hops = append(hops, ResolvedHop{Relation: relType, Incoming: incoming, Target: target})
	}
	return hops, nil
}

// resolveHopRelation maps one path element to its canonical relation and
// direction.
func resolveHopRelation(
	meta *metamodel.Metamodel, name string,
) (relType string, incoming bool, def *metamodel.RelationDef, err error) {
	relType = name
	def, ok := meta.GetRelationDef(name)
	if !ok {
		owner, isInverse := meta.InverseOwner(name)
		if !isInverse {
			return "", false, nil, fmt.Errorf("related: unknown relation type %q", name)
		}
		relType, incoming = owner, true
		if def, ok = meta.GetRelationDef(owner); !ok {
			return "", false, nil, fmt.Errorf("related: inverse %q names unknown relation %q", name, owner)
		}
	}
	if def.Symmetric {
		return "", false, nil, fmt.Errorf(
			"related: relation %q is symmetric and cannot be traversed: its edges are stored "+
				"in either direction, so one direction would miss some of them", relType)
	}
	return relType, incoming, def, nil
}

// resolveHopTarget picks the type a hop lands on from the far side's
// declared types, applying the `type =` ascription rule. name is the path
// element as written, so errors quote what the author typed.
func resolveHopTarget(name string, far []string, last bool, ascribed string) (string, error) {
	switch {
	case len(far) == 0:
		return "", fmt.Errorf("related: relation %q declares no type on the side it walks to", name)
	case len(far) == 1:
		if last && ascribed != "" && ascribed != far[0] {
			return "", fmt.Errorf("related: type %q does not match the target of %q (%s)",
				ascribed, name, far[0])
		}
		return far[0], nil
	case !last:
		// A union. Only the LAST hop can carry the ascription, so an
		// intermediate union is unresolvable and must be refused rather than
		// guessed.
		return "", fmt.Errorf(
			"related: intermediate relation %q has %d target types (%s) and cannot be "+
				"resolved; a chained hop must traverse single-target relations",
			name, len(far), strings.Join(far, ", "))
	case ascribed == "":
		return "", fmt.Errorf(
			"related: relation %q has %d target types (%s); add type='<one of them>' to "+
				"say which one is meant", name, len(far), strings.Join(far, ", "))
	case !slices.Contains(far, ascribed):
		return "", fmt.Errorf("related: type %q is not a target of %q (declared: %s)",
			ascribed, name, strings.Join(far, ", "))
	default:
		return ascribed, nil
	}
}

// validateTraversalProps checks each constrained property is declared on the
// resolved target type and is pushdown-eligible. The `id` key is not a
// property: the engine has already required a non-empty string for it, and
// it lowers to an endpoint id rather than a property comparison.
//
// Refusing an ineligible property is load-bearing, not fussiness. The whole
// point of the form is that it pushes into SQL; a property the store cannot
// compare would have to be evaluated per row in Go, which is the N+1 the
// collection-read rules exist to prevent — and it would be invisible to the
// storetest.Counting budgets, because it happens above the store's query path.
func validateTraversalProps(meta *metamodel.Metamodel, targetType string, spec predicate.TraversalSpec) error {
	if targetType == "" {
		return nil
	}
	def, ok := meta.GetEntityDef(targetType)
	if !ok {
		return fmt.Errorf("related: unknown entity type %q", targetType)
	}
	for _, name := range spec.PropNames() {
		pd, declared := def.Properties[name]
		if !declared {
			return fmt.Errorf("related: %q has no property %q", targetType, name)
		}
		if pd.List {
			// A list property compares as membership, which IS expressible —
			// but the two backends render a list differently, so the answer
			// would be backend-dependent. Refuse until that is pinned.
			return fmt.Errorf("related: property %q on %q is a list and cannot be filtered in a traversal",
				name, targetType)
		}
		if !metamodel.StringShaped(meta, pd) {
			// The store compares as a string (and the derived index is a
			// string index); an integer or boolean would silently never
			// match in SQL while matching in Go.
			return fmt.Errorf("related: property %q on %q has type %q, which cannot be compared in a traversal",
				name, targetType, pd.Type)
		}
		if _, isRef := spec.Refs[name]; isRef {
			// Bound per request; validateTraversalRefs has checked what it
			// reads, and an empty value is refused when it is bound. A user
			// id is never a declared enum value, so on an enum it would match
			// nothing and `not related(...)` would match every row.
			if enumValues(meta, pd) != nil {
				return fmt.Errorf("related: property %q on %q is an enum and cannot be compared "+
					"against current_user", name, targetType)
			}
			continue
		}
		str, ok := spec.Props[name].(predicate.String)
		if !ok {
			return fmt.Errorf("related: property %q on %q must be compared against a string literal",
				name, targetType)
		}
		if str.String() == "" {
			// An empty string is ambiguous in the store: an unset property
			// and one set to "" read differently per backend.
			return fmt.Errorf("related: property %q on %q must not be compared against an empty string",
				name, targetType)
		}
		// A misspelled enum value would match nothing, so `not related(...)`
		// would quietly match every row.
		if allowed := enumValues(meta, pd); allowed != nil && !slices.Contains(allowed, str.String()) {
			return fmt.Errorf("related: %q is not a declared value of %q on %q (allowed: %s)",
				str.String(), name, targetType, strings.Join(allowed, ", "))
		}
	}
	return nil
}

// enumValues returns the declared values of an enum-shaped property: inline
// `values:` or a custom type's. Nil means the property is not an enum.
func enumValues(meta *metamodel.Metamodel, pd metamodel.PropertyDef) []string {
	if len(pd.Values) > 0 {
		return pd.Values
	}
	if ct, ok := meta.Types[pd.Type]; ok && len(ct.Values) > 0 {
		return ct.Values
	}
	return nil
}
