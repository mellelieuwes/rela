package predicate

import (
	"context"
	"fmt"
	"maps"
	"math"
	"time"
)

// Eval evaluates the program against bindings and returns the result
// value or an *EvalError. Safe to call concurrently with distinct
// bindings (see doc.go).
//
// The context is threaded through to any host function the program
// invokes. A nil bindings argument is treated as empty; refer to any
// declared variable or function and Eval returns an *EvalError.
func (p *Program) Eval(ctx context.Context, b *Bindings, opts ...EvalOption) (Value, error) {
	cfg := evalOptions{stepBudget: defaultStepBudget}
	for _, o := range opts {
		o(&cfg)
	}
	state := &evalState{
		bindings:    b,
		stepBudget:  cfg.stepBudget,
		stepCounter: 0,
	}
	return state.eval(ctx, p.root)
}

// evalState carries the per-Eval mutable state. Each Eval call gets a
// fresh state; nothing here is shared with the *Program. ctx is
// threaded through the eval methods rather than stored on the struct
// (golangci-lint containedctx).
type evalState struct {
	bindings    *Bindings
	stepBudget  int
	stepCounter int
}

func (s *evalState) tick() error {
	s.stepCounter++
	if s.stepCounter > s.stepBudget {
		return &EvalError{Reason: fmt.Sprintf("step budget exceeded (limit %d)", s.stepBudget)}
	}
	return nil
}

func (s *evalState) eval(ctx context.Context, n node) (Value, error) {
	if err := s.tick(); err != nil {
		return nil, err
	}
	switch x := n.(type) {
	case *constNode:
		return x.v, nil
	case *varNode:
		return s.evalVar(x)
	case *attrNode:
		return s.evalAttr(ctx, x)
	case *callNode:
		return s.evalCall(ctx, x)
	case *traversalNode:
		return s.evalTraversal(ctx, x)
	case *tableArgNode:
		// Table-arg nodes are handled inside evalCall via the
		// per-call dispatch; reaching here is a bug.
		// coverage-ignore: invariant: tableArgNode is only ever produced as a callNode arg and consumed inside
		// evalCall; it never reaches the
		// top-level eval switch
		return nil, &EvalError{Reason: "internal: tableArgNode outside call context"}
	case *relationalNode:
		return s.evalRelational(ctx, x)
	case *logicalNode:
		return s.evalLogical(ctx, x)
	case *notNode:
		return s.evalNot(ctx, x)
	case *arithmeticNode:
		return s.evalArithmetic(ctx, x)
	case *unaryMinusNode:
		return s.evalUnaryMinus(ctx, x)
	case *concatNode:
		return s.evalConcat(ctx, x)
	default:
		// coverage-ignore: invariant: the node interface is sealed and all 8 concrete node types are enumerated above,
		// so no other node kind
		// can reach this default
		return nil, &EvalError{Reason: fmt.Sprintf("internal: unknown IR node %T", n)}
	}
}

func (s *evalState) evalArithmetic(ctx context.Context, n *arithmeticNode) (Value, error) {
	lhs, err := s.eval(ctx, n.lhs)
	if err != nil {
		return nil, err
	}
	rhs, err := s.eval(ctx, n.rhs)
	if err != nil {
		return nil, err
	}
	if a, ok := lhs.(Int); ok {
		b, ok := rhs.(Int)
		if !ok {
			return nil, &EvalError{Reason: "arithmetic operands do not match compiled int type"}
		}
		return evalIntArithmetic(n.op, a.v, b.v)
	}
	a, aok := lhs.(Number)
	b, bok := rhs.(Number)
	if !aok || !bok {
		return nil, &EvalError{Reason: "arithmetic operands do not match compiled numeric type"}
	}
	switch n.op {
	case "+":
		return NewNumber(a.v + b.v), nil
	case "-":
		return NewNumber(a.v - b.v), nil
	case "*":
		return NewNumber(a.v * b.v), nil
	case "/":
		if b.v == 0 {
			return nil, &EvalError{Reason: "number division by zero"}
		}
		return NewNumber(a.v / b.v), nil
	case "%":
		if b.v == 0 {
			return nil, &EvalError{Reason: "number modulo by zero"}
		}
		return NewNumber(math.Mod(a.v, b.v)), nil
	case "^":
		return NewNumber(math.Pow(a.v, b.v)), nil
	default:
		return nil, &EvalError{Reason: "internal: unsupported arithmetic operator " + n.op}
	}
}

func evalIntArithmetic(op string, a, b int64) (Value, error) {
	var out int64
	switch op {
	case "+":
		out = a + b
		if (b > 0 && out < a) || (b < 0 && out > a) {
			return nil, &EvalError{Reason: "integer addition overflow"}
		}
	case "-":
		out = a - b
		if (b < 0 && out < a) || (b > 0 && out > a) {
			return nil, &EvalError{Reason: "integer subtraction overflow"}
		}
	case "*":
		if (a == math.MinInt64 && b == -1) || (b == math.MinInt64 && a == -1) {
			return nil, &EvalError{Reason: "integer multiplication overflow"}
		}
		out = a * b
		if b != 0 && out/b != a {
			return nil, &EvalError{Reason: "integer multiplication overflow"}
		}
	case "%":
		if b == 0 {
			return nil, &EvalError{Reason: "integer modulo by zero"}
		}
		out = a % b
	default:
		return nil, &EvalError{Reason: "internal: unsupported int arithmetic operator " + op}
	}
	return NewInt(out), nil
}

func (s *evalState) evalUnaryMinus(ctx context.Context, n *unaryMinusNode) (Value, error) {
	v, err := s.eval(ctx, n.expr)
	if err != nil {
		return nil, err
	}
	switch x := v.(type) {
	case Int:
		if x.v == math.MinInt64 {
			return nil, &EvalError{Reason: "integer negation overflow"}
		}
		return NewInt(-x.v), nil
	case Number:
		return NewNumber(-x.v), nil
	default:
		return nil, &EvalError{Reason: "unary minus operand is not numeric"}
	}
}

func (s *evalState) evalConcat(ctx context.Context, n *concatNode) (Value, error) {
	lhs, err := s.eval(ctx, n.lhs)
	if err != nil {
		return nil, err
	}
	rhs, err := s.eval(ctx, n.rhs)
	if err != nil {
		return nil, err
	}
	a, aok := lhs.(String)
	b, bok := rhs.(String)
	if !aok || !bok {
		return nil, &EvalError{Reason: "string concatenation operand is nil or off-type"}
	}
	return NewString(a.v + b.v), nil
}

func (s *evalState) evalVar(n *varNode) (Value, error) {
	v, ok := s.bindings.lookupVar(n.name)
	if !ok {
		return nil, &EvalError{Reason: fmt.Sprintf("binding %q not provided", n.name)}
	}
	if !runtimeTypeAccepts(n.typ, v) {
		return nil, &EvalError{Reason: fmt.Sprintf("binding %q: expected %s, got %s", n.name, n.typ.typeName(), v.Type().typeName())}
	}
	return v, nil
}

// runtimeTypeAccepts checks that a runtime Value is shape-compatible
// with a declared static type. Field-level RecordType validation is a
// compile-time concern; at runtime we only check the value variant.
// A declared RecordType is satisfied by any Record value; a declared
// ListType by any List value. Scalars must match exactly.
func runtimeTypeAccepts(expected Type, got Value) bool {
	switch expected.(type) {
	case RecordType:
		_, ok := got.(Record)
		return ok
	case ListType:
		_, ok := got.(List)
		return ok
	}
	return expected.equalsType(got.Type())
}

func (s *evalState) evalAttr(ctx context.Context, n *attrNode) (Value, error) {
	obj, err := s.eval(ctx, n.obj)
	if err != nil {
		return nil, err
	}
	rec, ok := obj.(Record)
	if !ok {
		return nil, &EvalError{Reason: fmt.Sprintf("attribute %q: expected record, got %s", n.name, obj.Type().typeName())}
	}
	v, present := rec.Get(n.name)
	if !present {
		// The type checker promised this field exists on the declared
		// record shape, but the binding may have omitted it. Treat as
		// nil so the rule author can write `entity.optional == nil`
		// to test for absence.
		return NewNil(), nil
	}
	return v, nil
}

func (s *evalState) evalCall(ctx context.Context, n *callNode) (Value, error) {
	// Look up the host function before evaluating args. A missing host
	// fn fails fast and avoids spending the step budget on subtree
	// evaluation whose result we wouldn't use anyway (RR-CIGK).
	fn, ok := s.bindings.lookupFunc(n.name)
	if !ok {
		return nil, &EvalError{Reason: fmt.Sprintf("host function %q not provided", n.name)}
	}

	args := make([]Value, len(n.args))
	for i, a := range n.args {
		// Table-arg nodes don't go through the normal eval path: they
		// carry constants, so we can materialize the Record directly.
		if ta, ok := a.(*tableArgNode); ok {
			args[i] = NewRecord(copyEntries(ta.entries))
			continue
		}
		v, err := s.eval(ctx, a)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}

	out, err := fn.Call(ctx, args)
	if err != nil {
		return nil, &EvalError{Reason: fmt.Sprintf("host function %q: %v", n.name, err)}
	}
	if out == nil {
		return nil, &EvalError{Reason: fmt.Sprintf("host function %q returned nil; use NewNil() to return the predicate nil value", n.name)}
	}
	if !runtimeTypeAccepts(n.typ, out) {
		return nil, &EvalError{Reason: fmt.Sprintf("host function %q: declared return %s, got %s", n.name, n.typ.typeName(), out.Type().typeName())}
	}
	return out, nil
}

func copyEntries(in map[string]Value) map[string]Value {
	out := make(map[string]Value, len(in))
	maps.Copy(out, in)
	return out
}

func (s *evalState) evalRelational(ctx context.Context, n *relationalNode) (Value, error) {
	lhs, err := s.eval(ctx, n.lhs)
	if err != nil {
		return nil, err
	}
	rhs, err := s.eval(ctx, n.rhs)
	if err != nil {
		return nil, err
	}
	switch n.op {
	case "==":
		return NewBool(valuesEqual(lhs, rhs)), nil
	case "~=":
		return NewBool(!valuesEqual(lhs, rhs)), nil
	case "<", "<=", ">", ">=":
		return evalOrdered(n.op, lhs, rhs)
	default:
		return nil, &EvalError{Reason: fmt.Sprintf("internal: unknown relational op %q", n.op)}
	}
}

// valuesEqual implements Lua-flavored equality (see doc.go). nil
// only equals nil; mismatched types are never equal except via nil.
func valuesEqual(a, b Value) bool {
	if _, an := a.(Nil); an {
		_, bn := b.(Nil)
		return bn
	}
	if _, bn := b.(Nil); bn {
		return false
	}
	switch av := a.(type) {
	case Bool:
		bv, ok := b.(Bool)
		return ok && av.v == bv.v
	case Number:
		bv, ok := b.(Number)
		return ok && av.v == bv.v
	case Int:
		bv, ok := b.(Int)
		return ok && av.v == bv.v
	case Date:
		bv, ok := b.(Date)
		return ok && av.v.Equal(bv.v)
	case String:
		bv, ok := b.(String)
		return ok && av.v == bv.v
	}
	// Records / lists are forbidden at compile time, so reaching this
	// branch is an internal invariant violation.
	// coverage-ignore: invariant: == / ~= only compile for scalar or nil operands (Record/List rejected by
	// checkRelational), so every value
	// reaching valuesEqual matches one of the scalar cases above
	return false
}

func evalOrdered(op string, a, b Value) (Value, error) {
	// Both sides were already type-checked at compile to be the same
	// scalar type (number or string). The defensive ", ok" guards
	// here protect against an IR/type-checker drift bug rather than
	// any reachable Lua input.
	switch av := a.(type) {
	case Number:
		bv, ok := b.(Number)
		// coverage-ignore-start: defensive: compile-time checkRelational guarantees same-type operands; a Number lhs
		// always has a Number rhs, so
		// this drift guard is unreachable
		if !ok {
			return nil, &EvalError{Reason: fmt.Sprintf("internal: ordered cmp %q: rhs type %s mismatches lhs Number", op, b.Type().typeName())}
		}
		// coverage-ignore-end
		return NewBool(cmpNumber(op, av.v, bv.v)), nil
	case Int:
		bv, ok := b.(Int)
		// coverage-ignore-start: defensive: compile-time checkRelational guarantees same-type operands; an Int lhs
		// always has an Int rhs, so this
		// drift guard is unreachable
		if !ok {
			return nil, &EvalError{Reason: fmt.Sprintf("internal: ordered cmp %q: rhs type %s mismatches lhs Int", op, b.Type().typeName())}
		}
		// coverage-ignore-end
		return NewBool(cmpInt(op, av.v, bv.v)), nil
	case Date:
		bv, ok := b.(Date)
		// coverage-ignore-start: defensive: compile-time checkRelational guarantees same-type operands; a Date lhs
		// always has a Date rhs, so this
		// drift guard is unreachable
		if !ok {
			return nil, &EvalError{Reason: fmt.Sprintf("internal: ordered cmp %q: rhs type %s mismatches lhs Date", op, b.Type().typeName())}
		}
		// coverage-ignore-end
		return NewBool(cmpDate(op, av.v, bv.v)), nil
	case String:
		bv, ok := b.(String)
		// coverage-ignore-start: defensive: compile-time checkRelational guarantees same-type operands; a String lhs
		// always has a String rhs, so
		// this drift guard is unreachable
		if !ok {
			return nil, &EvalError{Reason: fmt.Sprintf("internal: ordered cmp %q: rhs type %s mismatches lhs String", op, b.Type().typeName())}
		}
		// coverage-ignore-end
		return NewBool(cmpString(op, av.v, bv.v)), nil
	}
	return nil, &EvalError{Reason: fmt.Sprintf("ordered comparison %q on unsupported type %s", op, a.Type().typeName())}
}

func cmpNumber(op string, x, y float64) bool {
	switch op {
	case "<":
		return x < y
	case "<=":
		return x <= y
	case ">":
		return x > y
	case ">=":
		return x >= y
	}
	return false
}

func cmpInt(op string, x, y int64) bool {
	switch op {
	case "<":
		return x < y
	case "<=":
		return x <= y
	case ">":
		return x > y
	case ">=":
		return x >= y
	}
	return false
}

// cmpDate orders two instants. Mirrors internal/filter.matchDate:
// Before/After for strict, Before||Equal / After||Equal for inclusive.
func cmpDate(op string, x, y time.Time) bool {
	switch op {
	case "<":
		return x.Before(y)
	case "<=":
		return x.Before(y) || x.Equal(y)
	case ">":
		return x.After(y)
	case ">=":
		return x.After(y) || x.Equal(y)
	}
	return false
}

func cmpString(op, x, y string) bool {
	switch op {
	case "<":
		return x < y
	case "<=":
		return x <= y
	case ">":
		return x > y
	case ">=":
		return x >= y
	}
	return false
}

func (s *evalState) evalLogical(ctx context.Context, n *logicalNode) (Value, error) {
	if n.selects() {
		return s.evalSelect(ctx, n)
	}
	lhs, err := s.eval(ctx, n.lhs)
	if err != nil {
		return nil, err
	}
	lb, ok := lhs.(Bool)
	if !ok {
		return nil, &EvalError{Reason: fmt.Sprintf("'%s' lhs: expected bool, got %s", n.op, lhs.Type().typeName())}
	}
	// Short-circuit, Lua-style.
	switch n.op {
	case "and":
		if !lb.v {
			return NewBool(false), nil
		}
	case "or":
		if lb.v {
			return NewBool(true), nil
		}
	}
	rhs, err := s.eval(ctx, n.rhs)
	if err != nil {
		return nil, err
	}
	rb, ok := rhs.(Bool)
	if !ok {
		return nil, &EvalError{Reason: fmt.Sprintf("'%s' rhs: expected bool, got %s", n.op, rhs.Type().typeName())}
	}
	return rb, nil
}

// evalSelect evaluates a value-selecting `and`/`or` with Lua semantics.
// Values are never bool (the walker rejects them), so only nil falls
// through `or`.
func (s *evalState) evalSelect(ctx context.Context, n *logicalNode) (Value, error) {
	lhs, err := s.eval(ctx, n.lhs)
	if err != nil {
		return nil, err
	}
	if n.op == "and" {
		cond, ok := lhs.(Bool)
		if !ok {
			return nil, &EvalError{Reason: "'and' lhs: expected bool, got " + lhs.Type().typeName()}
		}
		if !cond.v {
			return NewNil(), nil
		}
		return s.eval(ctx, n.rhs)
	}
	if _, isNil := lhs.(Nil); !isNil {
		return lhs, nil
	}
	return s.eval(ctx, n.rhs)
}

func (s *evalState) evalNot(ctx context.Context, n *notNode) (Value, error) {
	v, err := s.eval(ctx, n.expr)
	if err != nil {
		return nil, err
	}
	b, ok := v.(Bool)
	if !ok {
		return nil, &EvalError{Reason: "'not': expected bool, got " + v.Type().typeName()}
	}
	return NewBool(!b.v), nil
}
