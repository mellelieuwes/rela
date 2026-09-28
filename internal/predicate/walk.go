package predicate

import (
	"fmt"

	"github.com/yuin/gopher-lua/ast"
)

func (w *walker) walkIdent(e *ast.IdentExpr) (node, error) {
	t, ok := w.env.lookupVar(e.Value)
	if !ok {
		// IdentExpr is also how a function reference appears when the
		// caller writes the name without parens. We don't accept bare
		// function references — every reachable identifier must be a
		// declared variable. (A function call goes through walkCall.)
		if _, isFunc := w.env.lookupFunc(e.Value); isFunc {
			return nil, &CompileError{Line: e.Line(), Reason: fmt.Sprintf("function %q must be called, not referenced as a value", e.Value)}
		}
		return nil, &CompileError{Line: e.Line(), Reason: fmt.Sprintf("unknown identifier %q", e.Value)}
	}
	return &varNode{name: e.Value, typ: t}, nil
}

func (w *walker) walkAttrGet(e *ast.AttrGetExpr) (node, error) {
	// Per-field invariant (RR-V0OE): the Key must be a StringExpr. Both
	// `entity.status` (dot-sugar) and `entity['status']` (bracket form
	// with a string literal key) lower to the same AttrGetExpr shape in
	// gopher-lua, so we accept both; they are semantically identical.
	// A non-literal key, e.g. `entity[has_role('x')]`, lowers to
	// AttrGetExpr{Key: *FuncCallExpr} and is rejected here.
	keyStr, ok := e.Key.(*ast.StringExpr)
	if !ok {
		return nil, &CompileError{Line: e.Line(), Reason: "computed attribute access (entity[expr]) is not allowed; use a string literal key"}
	}

	obj, err := w.walkExpr(e.Object)
	if err != nil {
		return nil, err
	}
	rt, ok := obj.resultType().(RecordType)
	if !ok {
		return nil, &CompileError{Line: e.Line(), Reason: "attribute access requires a record value, got " + obj.resultType().typeName()}
	}
	ft, ok := rt[keyStr.Value]
	if !ok {
		return nil, &CompileError{Line: e.Line(), Reason: fmt.Sprintf("unknown attribute %q on record", keyStr.Value)}
	}
	return &attrNode{obj: obj, name: keyStr.Value, typ: ft}, nil
}

func (w *walker) walkRelational(e *ast.RelationalOpExpr) (node, error) {
	switch e.Operator {
	case "==", "~=", "<", "<=", ">", ">=":
		// allowed
	default:
		return nil, &CompileError{Line: e.Line(), Reason: fmt.Sprintf("unsupported relational operator %q", e.Operator)}
	}
	lhs, err := w.walkExpr(e.Lhs)
	if err != nil {
		return nil, err
	}
	rhs, err := w.walkExpr(e.Rhs)
	if err != nil {
		return nil, err
	}
	// Literal coercion (RR-A3EZR): predicate has no int/date literal
	// syntax — `entity.due < '2026-02-01'` writes the date as a string
	// literal and `entity.count > 9` writes the int as a number literal.
	// When exactly one side is an int/date-typed expression and the
	// other is a bare literal constNode, retype the literal to match at
	// COMPILE time (parse the date string against the field's layout;
	// convert the number to int64). This keeps eval pure — no parsing,
	// no metamodel lookup at Eval — and lets the same-type checker below
	// pass. Coercion is symmetric (either operand may be the literal).
	lhs, rhs, err = w.coerceLiteralOperands(lhs, rhs, e.Line())
	if err != nil {
		return nil, err
	}
	if err := checkRelational(e.Operator, lhs.resultType(), rhs.resultType(), e.Line()); err != nil {
		return nil, err
	}
	return &relationalNode{op: e.Operator, lhs: lhs, rhs: rhs}, nil
}

// coerceLiteralOperands retypes a bare literal operand to the int/date
// type of the opposite operand, at compile time. It returns the
// (possibly rewritten) operands. A literal that cannot be coerced to
// the target type is a CompileError (e.g. a malformed date string, or a
// non-integer number literal against an IntType field). If neither
// side is a coercible literal-vs-typed pairing, the operands pass
// through unchanged and the normal type check runs.
func (w *walker) coerceLiteralOperands(lhs, rhs node, line int) (newLHS, newRHS node, err error) {
	if coerced, handled, cErr := coerceOneLiteral(lhs, rhs.resultType(), line); handled {
		return coerced, rhs, cErr
	}
	if coerced, handled, cErr := coerceOneLiteral(rhs, lhs.resultType(), line); handled {
		return lhs, coerced, cErr
	}
	return lhs, rhs, nil
}

// coerceOneLiteral attempts to retype `lit` (only if it is a bare
// literal constNode) to `target` (only if target is Int or Date). The
// handled return reports whether this pairing was processed (a coercion
// was attempted) — distinguishing "coerced or errored" from "not
// applicable, leave operands alone."
func coerceOneLiteral(lit node, target Type, line int) (out node, handled bool, err error) {
	if ln, ok := lit.(*logicalNode); ok && ln.selects() {
		return coerceSelection(ln, target, line)
	}
	cn, ok := lit.(*constNode)
	if !ok {
		return lit, false, nil
	}
	switch {
	case target.equalsType(IntType):
		v, cErr := coerceIntLiteral(cn.v, line)
		if cErr != nil {
			return lit, true, cErr
		}
		return &constNode{v: v}, true, nil
	case target.equalsType(DateType):
		layout := ""
		if dt, ok := target.(dateType); ok {
			layout = dt.layout
		}
		v, cErr := coerceDateLiteral(cn.v, layout, line)
		if cErr != nil {
			return lit, true, cErr
		}
		return &constNode{v: v}, true, nil
	}
	return lit, false, nil
}

// coerceSelection coerces the literal value branches of a selecting
// logicalNode to target, so `c and 10 or 0` can be an int and
// `c and '2026-01-01' or entity.due` a date. The condition of an `and` is
// left alone. The node is rebuilt, never mutated.
func coerceSelection(n *logicalNode, target Type, line int) (out node, handled bool, err error) {
	lhs, rhs := n.lhs, n.rhs
	var lh, rh bool
	if n.op == "or" {
		if lhs, lh, err = coerceOneLiteral(lhs, target, line); err != nil {
			return n, true, err
		}
	}
	if rhs, rh, err = coerceOneLiteral(rhs, target, line); err != nil {
		return n, true, err
	}
	if !lh && !rh {
		return n, false, nil
	}
	typ, err := logicalType(n.op, lhs.resultType(), rhs.resultType(), line)
	if err != nil {
		return n, true, err
	}
	return &logicalNode{op: n.op, lhs: lhs, rhs: rhs, typ: typ}, true, nil
}

// maxExactIntLiteral is 2^53, the largest magnitude at which every
// integer is exactly representable in a float64. Number literals are
// parsed to float64 (there is no int literal syntax), so an int-field
// comparison against a literal at or beyond this magnitude cannot be
// coerced without silent rounding — and Go's out-of-range float→int
// conversion is implementation-defined (§ Conversions), which would make
// the comparison arch-dependent. We reject such literals at compile
// rather than mis-coerce them (RR-O0LM1). Real int-property comparisons
// live far below 2^53; a literal above it is a mistake, not a use case.
const maxExactIntLiteral = 1 << 53

// coerceIntLiteral converts a number-literal Value to an Int, rejecting
// a fractional value (e.g. `entity.count > 1.5` against an int field)
// and a value too large to represent exactly as an int (see
// maxExactIntLiteral). A String literal against an int field is a type
// mismatch left for the checker to report, so it is passed through
// unchanged; only a Number that isn't a safe integer errors here.
func coerceIntLiteral(v Value, line int) (Value, error) {
	n, ok := v.(Number)
	if !ok {
		// Not a number literal (e.g. a string) — don't coerce; let the
		// same-type checker produce the clearer "cannot compare" error.
		return v, nil
	}
	// Range-check before any float→int64 conversion: out-of-range
	// float→int conversion is implementation-defined, so the guard must
	// dominate both conversions below (also keeps CodeQL happy about the
	// hex-literal ParseUint value flowing in from parseLuaNumber).
	if n.v >= maxExactIntLiteral || n.v <= -maxExactIntLiteral {
		return nil, &CompileError{Line: line, Reason: fmt.Sprintf("integer literal %v is too large to be an exact int (must be within ±2^53)", n.v)}
	}
	if n.v != float64(int64(n.v)) {
		return nil, &CompileError{Line: line, Reason: fmt.Sprintf("the non-integer literal %v cannot be an int", n.v)}
	}
	return NewInt(int64(n.v)), nil
}

// coerceDateLiteral parses a string-literal Value into a Date using the
// field's layout (or built-in defaults when layout is empty). A
// non-string literal is left unchanged for the checker to reject.
func coerceDateLiteral(v Value, layout string, line int) (Value, error) {
	s, ok := v.(String)
	if !ok {
		return v, nil
	}
	t, err := parseDateLiteral(s.v, layout)
	if err != nil {
		return nil, &CompileError{Line: line, Reason: err.Error()}
	}
	return NewDate(t), nil
}

// checkRelational enforces the equality / ordering rules documented in
// doc.go. == and ~= allow nil-vs-anything (always false unless both
// are nil); other pairings must be same-type. Ordered comparisons
// require both number or both string.
func checkRelational(op string, lt, rt Type, line int) error {
	switch op {
	case "==", "~=":
		// Either operand may be nil; that's the only legal cross-type
		// comparison. Otherwise both sides must be the same scalar
		// type. Records and lists can't be compared.
		if _, ok := lt.(RecordType); ok {
			return &CompileError{Line: line, Reason: "records cannot be compared with == or ~="}
		}
		if _, ok := rt.(RecordType); ok {
			return &CompileError{Line: line, Reason: "records cannot be compared with == or ~="}
		}
		if _, ok := lt.(ListType); ok {
			return &CompileError{Line: line, Reason: "lists cannot be compared with == or ~="}
		}
		if _, ok := rt.(ListType); ok {
			return &CompileError{Line: line, Reason: "lists cannot be compared with == or ~="}
		}
		// Nil is the only legal cross-type. Same-type for the rest.
		if isNil(lt) || isNil(rt) {
			return nil
		}
		if !lt.equalsType(rt) {
			return &CompileError{Line: line, Reason: fmt.Sprintf("cannot compare %s with %s", lt.typeName(), rt.typeName())}
		}
		return nil
	case "<", "<=", ">", ">=":
		if !lt.equalsType(rt) {
			return &CompileError{Line: line, Reason: fmt.Sprintf("ordered comparison requires same type, got %s and %s", lt.typeName(), rt.typeName())}
		}
		if !isOrderedType(lt) {
			return &CompileError{Line: line, Reason: "ordered comparison requires number, int, date, or string, got " + lt.typeName()}
		}
		return nil
	}
	return &CompileError{Line: line, Reason: fmt.Sprintf("unknown relational operator %q", op)}
}

func isNil(t Type) bool { return t.equalsType(NilType) }

// isOrderedType reports whether a type supports ordered comparison
// (<, <=, >, >=). Numbers, ints, dates, and strings order; bools,
// nil, records, and lists do not.
func isOrderedType(t Type) bool {
	return t.equalsType(NumberType) || t.equalsType(IntType) ||
		t.equalsType(DateType) || t.equalsType(StringType)
}

func (w *walker) walkArithmetic(e *ast.ArithmeticOpExpr) (node, error) {
	lhs, err := w.walkExpr(e.Lhs)
	if err != nil {
		return nil, err
	}
	rhs, err := w.walkExpr(e.Rhs)
	if err != nil {
		return nil, err
	}
	// Number literals are the Lua syntax for integer constants. Coerce them
	// when paired with an Int expression, exactly as comparisons do.
	lhs, rhs, err = w.coerceLiteralOperands(lhs, rhs, e.Line())
	if err != nil {
		return nil, err
	}
	lt, rt := lhs.resultType(), rhs.resultType()
	if !lt.equalsType(rt) || (!lt.equalsType(IntType) && !lt.equalsType(NumberType)) {
		return nil, &CompileError{Line: e.Line(), Reason: fmt.Sprintf("arithmetic %q requires matching int or number operands, got %s and %s", e.Operator, lt.typeName(), rt.typeName())}
	}
	switch e.Operator {
	case "+", "-", "*", "%":
		return &arithmeticNode{op: e.Operator, lhs: lhs, rhs: rhs, typ: lt}, nil
	case "/", "^":
		if lt.equalsType(IntType) {
			return nil, &CompileError{Line: e.Line(), Reason: fmt.Sprintf("operator %q is not available for int expressions", e.Operator)}
		}
		return &arithmeticNode{op: e.Operator, lhs: lhs, rhs: rhs, typ: NumberType}, nil
	default:
		return nil, &CompileError{Line: e.Line(), Reason: fmt.Sprintf("unsupported arithmetic operator %q", e.Operator)}
	}
}

func (w *walker) walkConcat(e *ast.StringConcatOpExpr) (node, error) {
	lhs, err := w.walkExpr(e.Lhs)
	if err != nil {
		return nil, err
	}
	rhs, err := w.walkExpr(e.Rhs)
	if err != nil {
		return nil, err
	}
	if !lhs.resultType().equalsType(StringType) || !rhs.resultType().equalsType(StringType) {
		return nil, &CompileError{Line: e.Line(), Reason: fmt.Sprintf("string concatenation requires string operands, got %s and %s", lhs.resultType().typeName(), rhs.resultType().typeName())}
	}
	return &concatNode{lhs: lhs, rhs: rhs}, nil
}

func (w *walker) walkLogical(e *ast.LogicalOpExpr) (node, error) {
	switch e.Operator {
	case "and", "or":
		// allowed
	default:
		return nil, &CompileError{Line: e.Line(), Reason: fmt.Sprintf("unsupported logical operator %q", e.Operator)}
	}
	lhs, err := w.walkExpr(e.Lhs)
	if err != nil {
		return nil, err
	}
	rhs, err := w.walkExpr(e.Rhs)
	if err != nil {
		return nil, err
	}
	if e.Operator == "or" {
		// Both operands of a selecting `or` are values, so a literal
		// takes the int/date type of its sibling as in a comparison.
		lhs, rhs, err = w.coerceLiteralOperands(lhs, rhs, e.Line())
		if err != nil {
			return nil, err
		}
	}
	typ, err := logicalType(e.Operator, lhs.resultType(), rhs.resultType(), e.Line())
	if err != nil {
		return nil, err
	}
	return &logicalNode{op: e.Operator, lhs: lhs, rhs: rhs, typ: typ}, nil
}

// logicalType type-checks `and`/`or` and returns the result type: bool for
// boolean logic, or the selected value type (see logicalNode). A value must
// be a string, number, int or date; a bool value would bring back Lua's
// `c and false or y` pitfall, which is why bool operands always mean logic.
func logicalType(op string, lt, rt Type, line int) (Type, error) {
	if lt.equalsType(BoolType) && rt.equalsType(BoolType) {
		return BoolType, nil
	}
	if op == "and" {
		if !lt.equalsType(BoolType) {
			return nil, &CompileError{Line: line, Reason: "'and' requires bool on left, got " + lt.typeName()}
		}
		if err := checkSelectable(op, "right", rt, line); err != nil {
			return nil, err
		}
		return rt, nil
	}
	if err := checkSelectable(op, "left", lt, line); err != nil {
		return nil, err
	}
	if err := checkSelectable(op, "right", rt, line); err != nil {
		return nil, err
	}
	if !lt.equalsType(rt) {
		return nil, &CompileError{Line: line, Reason: fmt.Sprintf(
			"'or' operands must have the same type, got %s and %s", lt.typeName(), rt.typeName())}
	}
	return preferLayout(lt, rt), nil
}

// checkSelectable rejects an operand that cannot be a selected value.
func checkSelectable(op, side string, t Type, line int) error {
	switch {
	case t.equalsType(NilType):
		return &CompileError{Line: line, Reason: fmt.Sprintf(
			"'%s' cannot select nil on %s: 'c and nil or y' always yields y", op, side)}
	case t.equalsType(StringType), t.equalsType(NumberType), t.equalsType(IntType), t.equalsType(DateType):
		return nil
	case t.equalsType(BoolType):
		return &CompileError{Line: line, Reason: fmt.Sprintf(
			"'%s' mixes bool and value operands: use bool on both sides for logic, "+
				"or 'c and x or y' with values of one type to choose a value", op)}
	}
	return &CompileError{Line: line, Reason: fmt.Sprintf(
		"'%s' requires bool, string, number, int or date on %s, got %s", op, side, t.typeName())}
}

// preferLayout returns whichever of two equal types carries a date parse
// layout, so a later literal coercion against the selection uses it. When
// both carry one, the left operand's layout wins.
func preferLayout(a, b Type) Type {
	if da, ok := a.(dateType); ok && da.layout == "" {
		if db, ok := b.(dateType); ok {
			return db
		}
	}
	return a
}

func (w *walker) walkNot(e *ast.UnaryNotOpExpr) (node, error) {
	inner, err := w.walkExpr(e.Expr)
	if err != nil {
		return nil, err
	}
	if !inner.resultType().equalsType(BoolType) {
		return nil, &CompileError{Line: e.Line(), Reason: "'not' requires bool, got " + inner.resultType().typeName()}
	}
	return &notNode{expr: inner}, nil
}

func (w *walker) walkCall(e *ast.FuncCallExpr) (node, error) {
	// Method-call form t:m() is explicitly rejected; Receiver != nil
	// indicates the parser saw a colon.
	if e.Receiver != nil {
		return nil, &CompileError{Line: e.Line(), Reason: "method-call syntax (obj:method()) is not allowed"}
	}
	// AdjustRet flags multi-value return semantics (e.g. `f(g())`
	// where g returns multiple values that adjust to f's arity).
	// We only support single-value returns.
	if e.AdjustRet {
		return nil, &CompileError{Line: e.Line(), Reason: "multi-value return adjustment is not allowed"}
	}

	// The callee must be a bare identifier referring to a declared
	// function. We do not allow first-class function values: no
	// (expr)(args), no entity.method(args).
	ident, ok := e.Func.(*ast.IdentExpr)
	if !ok {
		return nil, &CompileError{Line: e.Line(), Reason: "function call must target a declared function name"}
	}
	// The traversal form is recognized BEFORE function lookup: `related` is
	// a reserved name compiled to its own node, never dispatched as a host
	// function (see FuncRelated).
	if ident.Value == FuncRelated {
		return w.walkRelated(e)
	}
	sig, ok := w.env.lookupFunc(ident.Value)
	if !ok {
		// Variables-named-as-funcs go via the "unknown function"
		// branch so the message points the author at the right fix.
		if _, isVar := w.env.lookupVar(ident.Value); isVar {
			return nil, &CompileError{Line: e.Line(), Reason: fmt.Sprintf("%q is a variable, not a function", ident.Value)}
		}
		return nil, &CompileError{Line: e.Line(), Reason: fmt.Sprintf("unknown function %q", ident.Value)}
	}

	args, err := w.walkCallArgs(e, sig)
	if err != nil {
		return nil, err
	}
	return &callNode{name: ident.Value, args: args, typ: sig.Return}, nil
}

func (w *walker) walkCallArgs(e *ast.FuncCallExpr, sig FuncSig) ([]node, error) {
	// Special case: a single table-literal argument is the named-args
	// form has_relation('x', {status='open'}). gopher-lua produces a
	// FuncCallExpr with Args = []Expr{*TableExpr{...}} for that.
	// We translate the table into a tableArgNode and type-check it
	// against the corresponding FuncSig parameter (also a table-arg).
	out := make([]node, len(e.Args))
	for i, arg := range e.Args {
		if tbl, ok := arg.(*ast.TableExpr); ok {
			n, err := w.walkTableArg(tbl)
			if err != nil {
				return nil, err
			}
			out[i] = n
		} else {
			n, err := w.walkExpr(arg)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
	}

	// Arity check.
	fixed := len(sig.Params)
	if sig.Variadic == nil {
		if len(out) != fixed {
			return nil, &CompileError{Line: e.Line(), Reason: fmt.Sprintf("function %q: expected %d arg(s), got %d", funcNameOf(e), fixed, len(out))}
		}
	} else {
		if len(out) < fixed {
			return nil, &CompileError{Line: e.Line(), Reason: fmt.Sprintf("function %q: expected at least %d arg(s), got %d", funcNameOf(e), fixed, len(out))}
		}
	}

	// Type check fixed params.
	for i := range fixed {
		expected := sig.Params[i]
		got := out[i].resultType()
		if !typeAccepts(expected, got) {
			return nil, &CompileError{Line: e.Line(), Reason: fmt.Sprintf("function %q: param %d: expected %s, got %s", funcNameOf(e), i, expected.typeName(), got.typeName())}
		}
	}
	// Type check variadic params.
	if sig.Variadic != nil {
		for i := fixed; i < len(out); i++ {
			got := out[i].resultType()
			if !typeAccepts(sig.Variadic, got) {
				return nil, &CompileError{Line: e.Line(), Reason: fmt.Sprintf("function %q: variadic arg %d: expected %s, got %s", funcNameOf(e), i, sig.Variadic.typeName(), got.typeName())}
			}
		}
	}
	return out, nil
}

func funcNameOf(e *ast.FuncCallExpr) string {
	if id, ok := e.Func.(*ast.IdentExpr); ok {
		return id.Value
	}
	return "<unknown>"
}

// typeAccepts reports whether a value of type `got` can flow into a
// position expecting type `expected`.
//
//   - AnyType accepts any value.
//   - A RecordType param accepts a tableArgType (the {key='value'} named
//     args form). Per-field validation is the host function's job; we
//     don't reach into the table at compile time because the named-args
//     form is intentionally untyped on the call side.
//   - Otherwise the types must match by equalsType.
func typeAccepts(expected, got Type) bool {
	if expected.equalsType(AnyType) {
		return true
	}
	if _, isRecord := expected.(RecordType); isRecord {
		// A RecordType param accepts any Record-shaped value: a
		// table-arg literal (named-args form) or another record
		// from a binding. Field-level structural checks are not
		// done at this layer; declaring RecordType{} means "any
		// record." A future change could narrow this to require
		// the actual record's fields to be a superset of the
		// declared shape, but the current ACL use cases don't
		// need that yet.
		if _, isTableArg := got.(tableArgType); isTableArg {
			return true
		}
		if _, isRecordGot := got.(RecordType); isRecordGot {
			return true
		}
	}
	return expected.equalsType(got)
}

// walkTableArg accepts a TableExpr only in the form {key='value', ...}
// — string keys via the sugar form, constant values.
func (w *walker) walkTableArg(t *ast.TableExpr) (node, error) {
	if err := w.enter(t.Line()); err != nil {
		return nil, err
	}
	defer w.leave()

	entries := make(map[string]Value, len(t.Fields))
	for _, f := range t.Fields {
		if f.Key == nil {
			return nil, &CompileError{Line: t.Line(), Reason: "positional table fields are not allowed; use {key='value'}"}
		}
		keyStr, ok := f.Key.(*ast.StringExpr)
		if !ok {
			return nil, &CompileError{Line: t.Line(), Reason: "table keys must be bare identifiers ({key='value'} form)"}
		}
		v, err := constValueOf(f.Value)
		if err != nil {
			return nil, &CompileError{Line: t.Line(), Reason: fmt.Sprintf("table value for %q: %s", keyStr.Value, err.Error())}
		}
		if _, dup := entries[keyStr.Value]; dup {
			return nil, &CompileError{Line: t.Line(), Reason: fmt.Sprintf("duplicate table key %q", keyStr.Value)}
		}
		entries[keyStr.Value] = v
	}
	return &tableArgNode{entries: entries}, nil
}

// constValueOf accepts only ConstExpr-style AST nodes for table values.
// Nested expressions are explicitly rejected to keep the named-args
// form simple and the IR validation trivial.
func constValueOf(e ast.Expr) (Value, error) {
	switch x := e.(type) {
	case *ast.TrueExpr:
		return NewBool(true), nil
	case *ast.FalseExpr:
		return NewBool(false), nil
	case *ast.NilExpr:
		return NewNil(), nil
	case *ast.NumberExpr:
		f, err := parseLuaNumber(x.Value)
		if err != nil {
			return nil, err
		}
		return NewNumber(f), nil
	case *ast.StringExpr:
		return NewString(x.Value), nil
	default:
		return nil, fmt.Errorf("must be a constant (string, number, bool, or nil); got %s", astKindName(e))
	}
}

// astKindName maps gopher-lua AST node types to user-friendly strings
// for error messages. Falls back to a generic label for nodes we
// don't have a specific name for — but every kind we'd encounter in
// the predicate-language grammar surface should have a clean name.
func astKindName(e ast.Expr) string {
	switch e.(type) {
	case *ast.FuncCallExpr:
		return "function call"
	case *ast.IdentExpr:
		return "identifier"
	case *ast.AttrGetExpr:
		return "attribute access"
	case *ast.TableExpr:
		return "table literal"
	case *ast.RelationalOpExpr:
		return "comparison expression"
	case *ast.LogicalOpExpr:
		return "boolean expression"
	case *ast.UnaryNotOpExpr:
		return "not-expression"
	case *ast.ArithmeticOpExpr:
		return "arithmetic expression"
	case *ast.StringConcatOpExpr:
		return "string concatenation"
	case *ast.UnaryMinusOpExpr:
		return "unary minus"
	case *ast.UnaryLenOpExpr:
		return "length operator"
	case *ast.FunctionExpr:
		return "function literal"
	case *ast.Comma3Expr:
		return "varargs (...)"
	}
	return "non-constant expression"
}
