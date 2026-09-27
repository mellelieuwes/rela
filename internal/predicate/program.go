package predicate

import (
	"fmt"
	"sort"
)

// Program is a compiled predicate, ready for repeated evaluation.
//
// A Program is immutable after Compile. It carries no mutable state,
// no caches, and no per-instance memoization. Multiple goroutines
// may call Eval on the same Program concurrently with their own
// Bindings.
type Program struct {
	root        node
	resultTyp   Type
	env         *Env
	attributes  map[string]map[string]struct{}
	vars        map[string]struct{}
	funcs       map[string]struct{}
	traversals  []TraversalSpec
	sqlPortable bool
}

// ResultType returns the static type of the program's top-level
// expression.
func (p *Program) ResultType() Type { return p.resultTyp }

// Attributes returns the statically referenced fields of recordVar in sorted
// order. Dynamic attribute access is rejected by the compiler, so this is a
// complete dependency set for an entity record.
func (p *Program) Attributes(recordVar string) []string {
	set := p.attributes[recordVar]
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// References reports whether the program reads the variable at all —
// bare (passed whole to a host function) or through an attribute. It is
// the question a caller asks before deciding whether a binding for it is
// REQUIRED: a program that never names a variable evaluates identically
// with or without one.
func (p *Program) References(varName string) bool {
	_, ok := p.vars[varName]
	return ok
}

// Functions returns the host functions the program calls, sorted. Like
// [Program.Attributes] it is exact: calls are resolved statically, so a
// caller can decide from this alone whether the program depends on a
// binding one of them closes over.
func (p *Program) Functions() []string {
	out := make([]string, 0, len(p.funcs))
	for name := range p.funcs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// SQLPortable reports whether every node and host function in the program has
// declared target-neutral semantics suitable for a future SQL lowering.
func (p *Program) SQLPortable() bool { return p.sqlPortable }

func (p *Program) inspect() {
	p.attributes = map[string]map[string]struct{}{}
	p.vars = map[string]struct{}{}
	p.funcs = map[string]struct{}{}
	p.traversals = nil
	p.sqlPortable = true
	var visit func(node)
	visit = func(n node) {
		switch x := n.(type) {
		case *constNode:
		case *varNode:
			p.vars[x.name] = struct{}{}
		case *attrNode:
			if v, ok := x.obj.(*varNode); ok {
				if p.attributes[v.name] == nil {
					p.attributes[v.name] = map[string]struct{}{}
				}
				p.attributes[v.name][x.name] = struct{}{}
			}
			visit(x.obj)
		case *callNode:
			p.funcs[x.name] = struct{}{}
			if sig, ok := p.env.lookupFunc(x.name); !ok || !sig.SQLPortable {
				p.sqlPortable = false
			}
			for _, a := range x.args {
				visit(a)
			}
		case *traversalNode:
			// A traversal stays SQL-portable: it exists to be lowered into a
			// store predicate, so marking it non-portable would defeat the
			// one reason the form is compiled statically rather than being a
			// host function.
			p.traversals = append(p.traversals, x.spec)
			visit(x.subject)
			// A constraint value read from a variable is a dependency on it:
			// `{ id = current_user.id }` makes the program need an identity,
			// exactly as `entity.x == current_user.id` does.
			for _, ref := range x.refs {
				visit(ref)
			}
		case *tableArgNode:
			p.sqlPortable = false
		case *relationalNode:
			visit(x.lhs)
			visit(x.rhs)
		case *logicalNode:
			visit(x.lhs)
			visit(x.rhs)
		case *notNode:
			visit(x.expr)
		case *arithmeticNode:
			visit(x.lhs)
			visit(x.rhs)
		case *unaryMinusNode:
			visit(x.expr)
		case *concatNode:
			visit(x.lhs)
			visit(x.rhs)
		default:
			// The node set is sealed (sealedNode), so this is reachable only
			// from a new node type added without extending this walk. Failing
			// loudly is the point: References/Functions/Attributes are exact
			// dependency sets that callers use to decide whether a binding is
			// REQUIRED, and a node silently skipped here would make a program
			// look independent of a variable it reads.
			panic(fmt.Sprintf("predicate: inspect: unhandled node type %T", n))
		}
	}
	visit(p.root)
}

// EvalOption configures a single Eval call. Options stack
// left-to-right; later options override earlier ones.
type EvalOption func(*evalOptions)

type evalOptions struct {
	stepBudget int
}

// defaultStepBudget is the per-Eval node-visit cap. Tuned generous
// for hand-written rules; aggressive against adversarial input.
const defaultStepBudget = 10_000

// WithStepBudget overrides the per-Eval step budget. Must be > 0;
// values <= 0 are clamped to 1 so a misconfigured caller cannot
// disable the budget.
func WithStepBudget(n int) EvalOption {
	return func(o *evalOptions) {
		if n <= 0 {
			n = 1
		}
		o.stepBudget = n
	}
}
