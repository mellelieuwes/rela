package predicate

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/yuin/gopher-lua/ast"
)

// FuncRelated is the reserved name of the traversal form:
//
//	related(entity, 'rel-x', { type = 'ticket', status = 'done' })
//	related(entity, { 'rel-x', 'rel-y' }, { type = 'C', prop = 'foo' })
//
// It is ordinary Lua to the author — a call with a string (or list of
// strings) and a table constructor — but it is NOT a host function. The
// walker recognizes the name and compiles the whole form to a
// [traversalNode] at COMPILE time.
//
// That distinction is the point, not an implementation detail. A host
// function's signature would have to declare SQLPortable=false (it reads
// the graph, which no FuncSig can express), and one non-portable call taints
// the WHOLE program — so spelling traversal as a host function would silently
// disable pushdown for every other clause in the same condition. Compiling it
// statically keeps the program portable and gives the lowering an exact,
// inspectable description of what to push down.
const FuncRelated = "related"

// TraversalSpec is the compile-time description of one traversal, exposed so
// a lowering can turn it into a store predicate without re-parsing anything.
//
// It carries no evaluation behavior and no store types: internal/predicate
// depends on nothing (arch_test), so the CALLER maps these names onto its own
// graph vocabulary.
type TraversalSpec struct {
	// Path is the relation types to follow, in order. Always at least one.
	Path []string

	// EntityType is the `type =` ascription, or "" when the author omitted
	// it. Omission is legal HERE — the engine cannot know whether a relation
	// has one target type or several — and is rejected by the metamodel-aware
	// layer when the relation's target is a union.
	EntityType string

	// Props are the remaining table keys with a literal value: property name
	// → required value. Sorted access is via [TraversalSpec.PropNames] so a
	// lowering emits predicates in a deterministic order. The map is shared
	// with the compiled program and with every spec [TraversalSpec.Bind]
	// returns unchanged, so no consumer may write to it.
	Props map[string]Value

	// ID is the `id =` constraint when its value is a literal: the final
	// entity's id must equal it. Nil means the id is unconstrained. Like
	// `type`, the key is reserved: it names the entity, never a property.
	ID Value

	// Refs are the constraints whose value is a field of a record variable
	// (`id = current_user.id`), keyed like Props with "id" for the id. The
	// value is not known at compile time, so a spec with Refs cannot be
	// answered or lowered until [TraversalSpec.Bind] has replaced them with
	// the values of one evaluation. Which variables and fields may appear
	// here is the caller's decision; the engine only requires a string field
	// of a record variable other than the subject.
	Refs map[string]VarRef

	// Subject is the identifier the traversal starts from (`entity` in
	// `related(entity, ...)`), or "" when the first argument is not a bare
	// identifier. The engine accepts any record there; a metamodel-aware
	// caller decides which subjects it can resolve, because it resolves the
	// path from ONE known type and a different record would start elsewhere.
	Subject string
}

// Key returns a canonical string identifying the traversal: two specs with
// equal keys ask the same question of the graph. A caller answering
// traversals in a batch keys its answers by this, since the spec itself
// holds a map and cannot be a map key.
//
// A bound spec and the unbound spec it came from have different keys, and
// two bindings of one spec with different values do too. That is what lets
// a batch answer computed for one identity refuse to serve another.
func (s TraversalSpec) Key() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%q|%q|%q", s.Subject, s.Path, s.EntityType)
	if s.ID != nil {
		fmt.Fprintf(&b, "|id=%s:%#v", s.ID.Type().typeName(), s.ID)
	}
	for _, name := range sortedKeys(s.Props) {
		v := s.Props[name]
		fmt.Fprintf(&b, "|%q=%s:%#v", name, v.Type().typeName(), v)
	}
	for _, name := range sortedKeys(s.Refs) {
		r := s.Refs[name]
		fmt.Fprintf(&b, "|%q=ref:%q.%q", name, r.Var, r.Field)
	}
	return b.String()
}

// PropNames returns the constrained PROPERTY names in sorted order: the keys
// of Props plus those of Refs, without "id", which names the entity rather
// than a property. A caller reading the value of a name must look in Props
// and then Refs; a name found only in Refs has no value until bound.
func (s TraversalSpec) PropNames() []string {
	out := make([]string, 0, len(s.Props)+len(s.Refs))
	for k := range s.Props {
		out = append(out, k)
	}
	for k := range s.Refs {
		if k != ConstraintID {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// VarRef names a field of a record variable used as a constraint value.
type VarRef struct {
	Var   string
	Field string
}

// ConstraintID is the reserved constraint key naming the final entity's id.
const ConstraintID = "id"

// constraintType is the reserved constraint key naming the final entity's
// type.
const constraintType = "type"

// Bind returns the spec with every [TraversalSpec.Refs] entry replaced by
// values[key]: "id" becomes [TraversalSpec.ID], any other key a Props entry.
// The receiver is not modified.
//
// Every value must be a NON-EMPTY string. An empty id would lower to an
// empty endpoint set, which a store reads as "any endpoint", so a missing
// identity would widen the traversal to every related entity. An empty
// property value is refused for the reason a literal one is. Both are errors
// rather than "no match", because under `not related(...)` no match would
// select every row.
func (s TraversalSpec) Bind(values map[string]Value) (TraversalSpec, error) {
	if len(s.Refs) == 0 {
		return s, nil
	}
	out := s
	out.Refs = nil
	out.Props = make(map[string]Value, len(s.Props)+len(s.Refs))
	maps.Copy(out.Props, s.Props)
	for _, key := range sortedKeys(s.Refs) {
		ref := s.Refs[key]
		v, ok := values[key]
		if !ok {
			return TraversalSpec{}, fmt.Errorf("%s: no value bound for %s.%s", FuncRelated, ref.Var, ref.Field)
		}
		str, isStr := v.(String)
		if !isStr || str.String() == "" {
			return TraversalSpec{}, fmt.Errorf("%s: %s.%s is empty or not a string, so %q cannot be compared",
				FuncRelated, ref.Var, ref.Field, key)
		}
		if key == ConstraintID {
			out.ID = str
		} else {
			out.Props[key] = str
		}
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// traversalNode is the compiled traversal. It evaluates to a bool via a
// caller-supplied resolver (see [Bindings.SetTraversal]); with no resolver
// bound, Eval fails rather than guessing an answer.
type traversalNode struct {
	subject node
	spec    TraversalSpec
	// refs holds the attribute node behind each spec.Refs entry, evaluated
	// against the bindings of each Eval to bind the spec.
	refs map[string]node
}

func (*traversalNode) resultType() Type { return BoolType }
func (*traversalNode) sealedNode()      {}

// TraversalFunc answers one compiled traversal against the graph. Bound on
// [Bindings] by a caller that has a store; the engine itself never reads one.
// The spec it receives is always bound: its Refs have been replaced by the
// values of the current evaluation (see [TraversalSpec.Bind]).
//
// Nil: a program containing a traversal fails at Eval when none is bound.
// That is deliberate — returning false would silently turn an unanswerable
// traversal into "no match", which reads as a legitimate result.
type TraversalFunc func(subject Value, spec TraversalSpec) (bool, error)

// walkRelated compiles the `related(...)` form.
//
// Shape: related(<record>, <string|{strings}>, [{table}]). The subject must be
// a record-typed expression (the entity in scope); the path is a string
// literal or a list of them; the optional third argument is the constraint
// table, whose `type` key is the entity-type ascription and whose remaining
// keys are property equalities.
func (w *walker) walkRelated(e *ast.FuncCallExpr) (node, error) {
	if len(e.Args) < 2 || len(e.Args) > 3 {
		return nil, &CompileError{Line: e.Line(), Reason: fmt.Sprintf(
			"%s: expected 2 or 3 args (subject, relation path, optional constraints), got %d",
			FuncRelated, len(e.Args))}
	}

	subject, err := w.walkExpr(e.Args[0])
	if err != nil {
		return nil, err
	}
	if _, ok := subject.resultType().(RecordType); !ok {
		return nil, &CompileError{Line: e.Line(), Reason: fmt.Sprintf(
			"%s: first argument must be an entity record, got %s",
			FuncRelated, subject.resultType().typeName())}
	}

	path, err := relationPath(e.Args[1], e.Line())
	if err != nil {
		return nil, err
	}

	spec := TraversalSpec{Path: path, Props: map[string]Value{}}
	if ident, isIdent := e.Args[0].(*ast.IdentExpr); isIdent {
		spec.Subject = ident.Value
	}
	node := &traversalNode{subject: subject}
	if len(e.Args) == 3 {
		refs, err := w.applyConstraints(e, e.Args[2], &spec)
		if err != nil {
			return nil, err
		}
		node.refs = refs
	}
	node.spec = spec
	return node, nil
}

// applyConstraints folds the optional constraint table into spec: the `type`
// key becomes the entity-type ascription, `id` the id constraint, and every
// other key a property equality. A value is a constant, or a string field of
// a record variable (`current_user.id`), which is recorded in spec.Refs and
// returned as the attribute node that yields it at Eval.
func (w *walker) applyConstraints(
	call *ast.FuncCallExpr, arg ast.Expr, spec *TraversalSpec,
) (map[string]node, error) {
	tbl, ok := arg.(*ast.TableExpr)
	if !ok {
		return nil, &CompileError{Line: call.Line(),
			Reason: FuncRelated + ": constraints must be a table literal ({type='x', prop='y'})"}
	}
	if err := w.enter(tbl.Line()); err != nil {
		return nil, err
	}
	defer w.leave()

	var refs map[string]node
	seen := make(map[string]bool, len(tbl.Fields))
	for _, f := range tbl.Fields {
		if f.Key == nil {
			return nil, &CompileError{Line: tbl.Line(), Reason: "positional table fields are not allowed; use {key='value'}"}
		}
		keyStr, isStr := f.Key.(*ast.StringExpr)
		if !isStr {
			return nil, &CompileError{Line: tbl.Line(), Reason: "table keys must be bare identifiers ({key='value'} form)"}
		}
		key := keyStr.Value
		if seen[key] {
			return nil, &CompileError{Line: tbl.Line(), Reason: fmt.Sprintf("duplicate table key %q", key)}
		}
		seen[key] = true

		if attr, isAttr := f.Value.(*ast.AttrGetExpr); isAttr && key != constraintType {
			ref, n, err := w.constraintRef(call, key, attr, spec.Subject)
			if err != nil {
				return nil, err
			}
			if spec.Refs == nil {
				spec.Refs = map[string]VarRef{}
				refs = map[string]node{}
			}
			spec.Refs[key], refs[key] = ref, n
			continue
		}

		v, err := constValueOf(f.Value)
		if err != nil {
			return nil, &CompileError{Line: tbl.Line(), Reason: fmt.Sprintf("table value for %q: %s", key, err.Error())}
		}
		switch key {
		case constraintType:
			str, isStr := v.(String)
			if !isStr {
				return nil, &CompileError{Line: call.Line(), Reason: FuncRelated + ": 'type' must be a string literal"}
			}
			spec.EntityType = str.String()
		case ConstraintID:
			// An entity id is always a non-empty string, and an empty one
			// would lower to an endpoint set a store reads as "any".
			if str, isStr := v.(String); !isStr || str.String() == "" {
				return nil, &CompileError{Line: call.Line(),
					Reason: FuncRelated + ": 'id' must be a non-empty string or a string field such as current_user.id"}
			}
			spec.ID = v
		default:
			spec.Props[key] = v
		}
	}
	return refs, nil
}

// constraintRef compiles a constraint value written as `<var>.<field>`. It
// must name a string field of a record variable directly (no deeper path),
// and not the traversal's own subject: the subject's fields differ per row,
// so a batch answered once for many rows could not honor them.
func (w *walker) constraintRef(
	call *ast.FuncCallExpr, key string, attr *ast.AttrGetExpr, subject string,
) (VarRef, node, error) {
	n, err := w.walkAttrGet(attr)
	if err != nil {
		return VarRef{}, nil, err
	}
	a, isAttr := n.(*attrNode)
	if !isAttr {
		// coverage-ignore: invariant: walkAttrGet returns *attrNode or an error
		return VarRef{}, nil, &CompileError{Line: call.Line(), Reason: FuncRelated + ": internal: malformed attribute"}
	}
	v, isVar := a.obj.(*varNode)
	if !isVar {
		return VarRef{}, nil, &CompileError{Line: call.Line(), Reason: fmt.Sprintf(
			"%s: the value of %q must be a literal or a field of a variable, such as current_user.id",
			FuncRelated, key)}
	}
	if v.name == subject {
		return VarRef{}, nil, &CompileError{Line: call.Line(), Reason: fmt.Sprintf(
			"%s: the value of %q cannot read %s itself; it must be the same for every row",
			FuncRelated, key, subject)}
	}
	if !a.typ.equalsType(StringType) {
		return VarRef{}, nil, &CompileError{Line: call.Line(), Reason: fmt.Sprintf(
			"%s: %s.%s is a %s; a constraint compares strings", FuncRelated, v.name, a.name, a.typ.typeName())}
	}
	return VarRef{Var: v.name, Field: a.name}, a, nil
}

// relationPath accepts either 'rel-x' or {'rel-x', 'rel-y'} and returns the
// hop list. A chain is written as a LIST rather than by nesting calls because
// the nested spelling would make the subject of the inner call a bool (the
// outer call's result type), which cannot type-check.
func relationPath(e ast.Expr, line int) ([]string, error) {
	switch x := e.(type) {
	case *ast.StringExpr:
		if x.Value == "" {
			return nil, &CompileError{Line: line, Reason: FuncRelated + ": relation type must be non-empty"}
		}
		return []string{x.Value}, nil
	case *ast.TableExpr:
		if len(x.Fields) == 0 {
			return nil, &CompileError{Line: line, Reason: FuncRelated + ": relation path must name at least one relation type"}
		}
		out := make([]string, 0, len(x.Fields))
		for _, f := range x.Fields {
			if f.Key != nil {
				return nil, &CompileError{Line: line, Reason: FuncRelated + ": relation path must be a plain list ({'a', 'b'})"}
			}
			s, ok := f.Value.(*ast.StringExpr)
			if !ok || s.Value == "" {
				return nil, &CompileError{Line: line, Reason: FuncRelated + ": relation path entries must be non-empty string literals"}
			}
			out = append(out, s.Value)
		}
		return out, nil
	default:
		return nil, &CompileError{Line: line, Reason: fmt.Sprintf(
			"%s: relation path must be a string or a list of strings, got %s", FuncRelated, astKindName(e))}
	}
}

// Traversals returns every traversal the program compiles, in the order the
// program inspection walk visits them. A lowering uses this to build store
// predicates; an empty result means the program needs no graph access beyond
// its bindings.
//
// Collected during inspect rather than by a second walk, so the exhaustive
// node switch that already has to stay current is the ONLY one — a new node
// type forgotten here would silently drop a traversal from the lowering while
// the program still evaluated it, which is a pushdown that returns more rows
// than the condition allows.
func (p *Program) Traversals() []TraversalSpec { return p.traversals }

// evalTraversal answers a traversal via the bound resolver.
func (s *evalState) evalTraversal(ctx context.Context, n *traversalNode) (Value, error) {
	if s.bindings.traversal == nil {
		return nil, &EvalError{Reason: fmt.Sprintf(
			"%s: no traversal resolver is bound; this program must be lowered into a "+
				"store query or evaluated with Bindings.SetTraversal", FuncRelated)}
	}
	subject, err := s.eval(ctx, n.subject)
	if err != nil {
		return nil, err
	}
	spec := n.spec
	if len(n.refs) > 0 {
		values := make(map[string]Value, len(n.refs))
		for _, key := range sortedKeys(n.refs) {
			v, evalErr := s.eval(ctx, n.refs[key])
			if evalErr != nil {
				return nil, evalErr
			}
			values[key] = v
		}
		if spec, err = spec.Bind(values); err != nil {
			return nil, &EvalError{Reason: err.Error()}
		}
	}
	ok, err := s.bindings.traversal(subject, spec)
	if err != nil {
		return nil, &EvalError{Reason: fmt.Sprintf("%s: %s", FuncRelated, err.Error())}
	}
	return NewBool(ok), nil
}
