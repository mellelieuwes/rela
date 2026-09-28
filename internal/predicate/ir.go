package predicate

// node is the predicate engine's typed IR. Unlike the raw gopher-lua
// AST, this IR carries only the constructs the engine accepts; the
// switch in eval.go has a default-panic case because every legal
// program produces only these node shapes.
//
// All node types are immutable after Compile.
type node interface {
	resultType() Type
	sealedNode()
}

// constNode is a baked-in scalar literal.
type constNode struct {
	v Value
}

func (n *constNode) resultType() Type { return n.v.Type() }
func (*constNode) sealedNode()        {}

// varNode reads a named binding at eval time.
type varNode struct {
	name string
	typ  Type
}

func (n *varNode) resultType() Type { return n.typ }
func (*varNode) sealedNode()        {}

// attrNode accesses a named field on a record-typed expression.
type attrNode struct {
	obj  node
	name string
	typ  Type
}

func (n *attrNode) resultType() Type { return n.typ }
func (*attrNode) sealedNode()        {}

// callNode invokes a host function. Args are positional, already
// type-checked at compile against the FuncSig.
type callNode struct {
	name string
	args []node
	typ  Type
}

func (n *callNode) resultType() Type { return n.typ }
func (*callNode) sealedNode()        {}

// tableArgNode is a named-args table literal used only inside a
// callNode's args. The keys are always strings; the values are
// always constants (enforced by the walker).
type tableArgNode struct {
	entries map[string]Value
}

func (n *tableArgNode) resultType() Type { return tableArgType{} }
func (*tableArgNode) sealedNode()        {}

// relationalNode is one of ==, ~=, <, <=, >, >=.
type relationalNode struct {
	op       string
	lhs, rhs node
}

func (*relationalNode) resultType() Type { return BoolType }
func (*relationalNode) sealedNode()      {}

// logicalNode is one of `and`, `or`, with Lua-style short-circuit
// evaluation. With bool operands it is boolean logic and typ is BoolType.
// Otherwise it selects a value of the scalar type typ, as in Lua:
// `c and x` yields x when c is true and nil when it is false, and
// `x or y` yields x unless x is nil. The condition of a selecting `and`
// must be a bool; a nil condition is an eval error, unlike SQL, where
// `CASE WHEN NULL` takes the ELSE branch. A SQL lowering must keep the
// error (TKT-WQJGPS); boolean `and`/`or` have the same property.
type logicalNode struct {
	op       string
	lhs, rhs node
	typ      Type
}

func (n *logicalNode) resultType() Type { return n.typ }
func (*logicalNode) sealedNode()        {}

// selects reports whether n chooses a value rather than combining bools.
func (n *logicalNode) selects() bool { return !n.typ.equalsType(BoolType) }

// notNode is unary `not`.
type notNode struct {
	expr node
}

func (*notNode) resultType() Type { return BoolType }
func (*notNode) sealedNode()      {}

// arithmeticNode performs a typed numeric operation. Int operations remain
// Int and are checked for overflow; Number operations follow float64/Lua
// numeric semantics.
type arithmeticNode struct {
	op       string
	lhs, rhs node
	typ      Type
}

func (n *arithmeticNode) resultType() Type { return n.typ }
func (*arithmeticNode) sealedNode()        {}

type unaryMinusNode struct {
	expr node
	typ  Type
}

func (n *unaryMinusNode) resultType() Type { return n.typ }
func (*unaryMinusNode) sealedNode()        {}

type concatNode struct {
	lhs, rhs node
}

func (*concatNode) resultType() Type { return StringType }
func (*concatNode) sealedNode()      {}

// tableArgType is an internal type carried only by table-literal
// nodes passed as function arguments. It is not in the public Type
// surface; callers can't observe or declare it.
type tableArgType struct{}

func (tableArgType) typeName() string       { return "table-arg" }
func (tableArgType) equalsType(o Type) bool { _, ok := o.(tableArgType); return ok }
func (tableArgType) sealedType()            {}
