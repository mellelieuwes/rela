package predicate

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/yuin/gopher-lua/ast"
	"github.com/yuin/gopher-lua/parse"
)

// CompileOption configures Compile.
type CompileOption func(*compileOptions)

type compileOptions struct {
	maxDepth int
}

// Profile selects the expression features and result contract available to a
// compilation context. Profiles change availability, never semantics.
type Profile struct {
	Expected           Type
	AllowArithmetic    bool
	AllowConcatenation bool
	RequireSQLPortable bool
}

// ValueProfile returns the general pure scalar-expression profile used by
// materialized computations.
func ValueProfile(expected Type) Profile {
	return Profile{Expected: expected, AllowArithmetic: true, AllowConcatenation: true}
}

// defaultMaxDepth caps walker recursion so a pathologically nested
// AST cannot produce a runtime stack overflow.
const defaultMaxDepth = 256

// WithMaxDepth overrides the compile-time depth budget. Clamped to >= 1.
func WithMaxDepth(n int) CompileOption {
	return func(o *compileOptions) {
		if n <= 0 {
			n = 1
		}
		o.maxDepth = n
	}
}

// Compile parses source as a single Lua expression, walks the AST
// against the predicate engine's allow-list, and type-checks against
// env. The returned *Program is safe for concurrent evaluation.
//
// A nil env is rejected. The source must be a single expression
// (statements, multi-return-value, and leading `return` are all
// rejected with a *CompileError naming the failure mode).
func Compile(env *Env, source string, opts ...CompileOption) (prog *Program, err error) {
	return compile(env, source, Profile{Expected: BoolType}, opts...)
}

// CompileValue compiles a pure value expression under profile. Unlike Compile,
// the top-level result need not be bool.
func CompileValue(env *Env, source string, profile Profile, opts ...CompileOption) (prog *Program, err error) {
	if profile.Expected == nil {
		return nil, &CompileError{Reason: "profile expected type must be non-nil"}
	}
	return compile(env, source, profile, opts...)
}

func compile(env *Env, source string, profile Profile, opts ...CompileOption) (prog *Program, err error) {
	if env == nil {
		return nil, &CompileError{Reason: "env must be non-nil"}
	}

	cfg := compileOptions{maxDepth: defaultMaxDepth}
	for _, o := range opts {
		o(&cfg)
	}

	cleaned, perr := preprocess(source)
	if perr != nil {
		return nil, perr
	}

	// Belt-and-suspenders: gopher-lua's parse.Parse already converts
	// its own internal yacc-driven panics into typed errors, so this
	// recover is unreachable today. It exists for two contingencies:
	// (a) a panic added by future walker changes, and (b) a hypothetical
	// regression where gopher-lua removes its top-level recover.
	// Either way, an unexpected panic surfaces as a typed ParseError
	// instead of bringing down the host. Pinned by
	// TestCompile_RecoversParserPanics (RR-S84L, RR-674Z).
	// coverage-ignore-start: defensive: gopher-lua's parse.Parse converts its own yacc panics into typed errors, so no
	// reachable input panics;
	// this recover body is belt-and-suspenders (pinned by TestCompile_RecoversParserPanics which asserts no panic
	// occurs)
	defer func() {
		if r := recover(); r != nil {
			err = &ParseError{Msg: fmt.Sprintf("parser panic recovered: %v", r)}
			prog = nil
		}
	}()
	// coverage-ignore-end

	wrapped := "return " + cleaned
	chunk, parseErr := parse.Parse(strings.NewReader(wrapped), "<predicate>")
	if parseErr != nil {
		// The wrapped parse failed. Try parsing the *unwrapped* source
		// as a full chunk; if that succeeds, the user wrote valid Lua
		// that simply isn't an expression (an assignment, a do-block,
		// a goto, etc.). Producing a predicate-domain error for that
		// case beats forwarding gopher-lua's "syntax error near '='"
		// which gives the rule author no usable hint.
		if stmts, secondErr := parse.Parse(strings.NewReader(cleaned), "<predicate>"); secondErr == nil && len(stmts) > 0 {
			if msg := describeStmtAsNonExpression(stmts[0]); msg != "" {
				return nil, &CompileError{Line: stmts[0].Line(), Reason: msg}
			}
		}
		return nil, translateParseError(parseErr)
	}

	// coverage-ignore-start: defensive: the wrapped source is "return "+cleaned, and a successful parse of a single
	// leading `return` statement
	// always yields exactly one chunk element
	if len(chunk) != 1 {
		return nil, &CompileError{Reason: "source must contain exactly one expression"}
	}
	// coverage-ignore-end
	retStmt, ok := chunk[0].(*ast.ReturnStmt)
	// coverage-ignore-start: defensive: chunk[0] parsed from a "return ..." wrapper is always an *ast.ReturnStmt when
	// the wrapped parse succeeds
	if !ok {
		return nil, &CompileError{Reason: "source must be an expression"}
	}
	// coverage-ignore-end
	if len(retStmt.Exprs) != 1 {
		return nil, &CompileError{Reason: "multiple return values are not supported"}
	}

	w := &walker{env: env, maxDepth: cfg.maxDepth, profile: profile}
	root, walkErr := w.walkExpr(retStmt.Exprs[0])
	if walkErr != nil {
		return nil, walkErr
	}

	if !root.resultType().equalsType(profile.Expected) {
		// A literal or an all-literal selection takes the expected int/date
		// type, as it would against a typed operand.
		coerced, handled, cErr := coerceOneLiteral(root, profile.Expected, retStmt.Exprs[0].Line())
		if cErr != nil {
			return nil, cErr
		}
		if handled {
			root = coerced
		}
	}
	if !root.resultType().equalsType(profile.Expected) && !root.resultType().equalsType(NilType) {
		return nil, &CompileError{Reason: "top-level expression must be " + profile.Expected.typeName() + ", got " + root.resultType().typeName()}
	}

	program := &Program{
		root:      root,
		resultTyp: root.resultType(),
		env:       env,
	}
	program.inspect()
	if profile.RequireSQLPortable && !program.sqlPortable {
		return nil, &CompileError{Reason: "expression is not SQL-portable"}
	}
	return program, nil
}

// describeStmtAsNonExpression returns a domain-friendly message for
// a top-level Lua statement that isn't an expression. Empty string
// means "no specific message; let the parser error stand."
//
// Mapping to user-facing wording rather than gopher-lua's token-near
// chatter — the rule author wrote a statement, the engine wants an
// expression, and the engine should say so plainly.
func describeStmtAsNonExpression(s ast.Stmt) string {
	switch s.(type) {
	case *ast.AssignStmt, *ast.LocalAssignStmt:
		return "assignment is not allowed; did you mean '==' for equality?"
	case *ast.DoBlockStmt:
		return "do/end blocks are not allowed"
	case *ast.WhileStmt:
		return "while loops are not allowed"
	case *ast.RepeatStmt:
		return "repeat/until loops are not allowed"
	case *ast.IfStmt:
		return "if statements are not allowed (use 'c and x or y' to choose a value)"
	case *ast.NumberForStmt, *ast.GenericForStmt:
		return "for loops are not allowed"
	case *ast.FuncDefStmt:
		return "function definitions are not allowed"
	case *ast.ReturnStmt:
		return "source must be an expression, not a 'return' statement"
	case *ast.BreakStmt:
		return "break is not allowed"
	case *ast.LabelStmt, *ast.GotoStmt:
		return "labels and goto are not allowed"
	}
	return ""
}

// translateParseError converts gopher-lua's parse error into a typed
// *ParseError. gopher-lua formats its errors as
//
//	"<name> line:N(column:M) near 'TOK':   syntax error"
//
// We parse that pattern out so the line/col fields land on the typed
// error and the message ParseError.Error() prints stays clean. If the
// shape doesn't match (future gopher-lua change), we degrade to a
// position-less message with the gopher-lua text passed through.
func translateParseError(err error) error {
	raw := err.Error()
	// Strip the synthetic "return " prefix the engine prepends so the
	// "near 'X'" token snippets read naturally.
	raw = strings.ReplaceAll(raw, "return ", "")

	line, col, near, ok := parseGopherLuaError(raw)
	if !ok {
		return &ParseError{Msg: raw}
	}
	// Subtract one column for the synthetic "return " prefix (7 chars)
	// only when the reported column was inside the wrapper. Heuristic:
	// for line 1, column 1-7 is impossible (we prepended 7 chars), so
	// shift; otherwise keep as-is. The shift is best-effort.
	if line == 1 && col > 7 {
		col -= 7
	}
	msg := "syntax error"
	if near != "" {
		msg = fmt.Sprintf("syntax error near %q", near)
	}
	return &ParseError{Line: line, Col: col, Msg: msg}
}

// parseGopherLuaError extracts (line, col, near-token) from a
// gopher-lua-formatted parse error. Matches the canonical pattern
// `... line:N(column:M) near 'X': ...` lenient on surrounding text.
func parseGopherLuaError(s string) (line, col int, near string, ok bool) {
	_, after, ok := strings.Cut(s, "line:")
	if !ok {
		return 0, 0, "", false
	}
	rest := after
	var n int
	if _, e := fmt.Sscanf(rest, "%d", &n); e != nil {
		return 0, 0, "", false
	}
	line = n

	j := strings.Index(rest, "column:")
	if j < 0 {
		return line, 0, "", true
	}
	rest = rest[j+len("column:"):]
	if _, e := fmt.Sscanf(rest, "%d", &n); e != nil {
		return line, 0, "", true
	}
	col = n

	// Extract the near-token between single quotes after "near".
	k := strings.Index(rest, "near '")
	if k < 0 {
		return line, col, "", true
	}
	rest = rest[k+len("near '"):]
	before0, _, ok0 := strings.Cut(rest, "'")
	if !ok0 {
		return line, col, "", true
	}
	near = before0
	return line, col, near, true
}

// walker translates the gopher-lua AST into the predicate IR while
// enforcing the allow-list and the per-field invariants. Depth-bounded
// to defend against adversarially nested input (RR-XKNO).
type walker struct {
	env      *Env
	maxDepth int
	depth    int
	profile  Profile
}

func (w *walker) enter(line int) *CompileError {
	w.depth++
	if w.depth > w.maxDepth {
		return &CompileError{Line: line, Reason: fmt.Sprintf("expression nests too deeply (limit %d)", w.maxDepth)}
	}
	return nil
}

func (w *walker) leave() { w.depth-- }

func (w *walker) walkExpr(e ast.Expr) (node, error) {
	if err := w.enter(e.Line()); err != nil {
		return nil, err
	}
	defer w.leave()

	switch x := e.(type) {
	case *ast.TrueExpr:
		return &constNode{v: NewBool(true)}, nil
	case *ast.FalseExpr:
		return &constNode{v: NewBool(false)}, nil
	case *ast.NilExpr:
		return &constNode{v: NewNil()}, nil
	case *ast.NumberExpr:
		f, err := parseLuaNumber(x.Value)
		if err != nil {
			return nil, &CompileError{Line: x.Line(), Reason: err.Error()}
		}
		return &constNode{v: NewNumber(f)}, nil
	case *ast.StringExpr:
		return &constNode{v: NewString(x.Value)}, nil
	case *ast.IdentExpr:
		return w.walkIdent(x)
	case *ast.AttrGetExpr:
		return w.walkAttrGet(x)
	case *ast.RelationalOpExpr:
		return w.walkRelational(x)
	case *ast.LogicalOpExpr:
		return w.walkLogical(x)
	case *ast.UnaryNotOpExpr:
		return w.walkNot(x)
	case *ast.FuncCallExpr:
		return w.walkCall(x)
	case *ast.ArithmeticOpExpr:
		if !w.profile.AllowArithmetic {
			return nil, &CompileError{Line: x.Line(), Reason: "arithmetic operators are not allowed"}
		}
		return w.walkArithmetic(x)
	case *ast.StringConcatOpExpr:
		if !w.profile.AllowConcatenation {
			return nil, &CompileError{Line: x.Line(), Reason: "string concatenation (..) is not allowed"}
		}
		return w.walkConcat(x)

	// Explicitly named rejects produce a clearer error than the
	// default-reject fallthrough.
	case *ast.FunctionExpr:
		return nil, &CompileError{Line: x.Line(), Reason: "function literals are not allowed"}
	case *ast.UnaryMinusOpExpr:
		return w.walkUnaryMinus(x)
	case *ast.UnaryLenOpExpr:
		return nil, &CompileError{Line: x.Line(), Reason: "length operator (#) is not allowed"}
	case *ast.Comma3Expr:
		return nil, &CompileError{Line: x.Line(), Reason: "varargs (...) are not allowed"}
	case *ast.TableExpr:
		return nil, &CompileError{Line: x.Line(), Reason: "table literals are allowed only as a single function-call argument"}

	default:
		// Default-reject: any AST node type not enumerated above is
		// disallowed. New gopher-lua releases that introduce new node
		// types break here visibly until we triage them.
		// coverage-ignore: unreachable-default: every ast.Expr node type gopher-lua's current grammar can produce is
		// enumerated in the cases
		// above (accepted or explicitly rejected); this default only fires if a future gopher-lua release adds a new
		// expr
		// node kind
		return nil, &CompileError{Line: e.Line(), Reason: "unsupported expression kind: " + astKindName(e)}
	}
}

func (w *walker) walkUnaryMinus(x *ast.UnaryMinusOpExpr) (node, error) {
	// A negative numeric literal is folded so legacy predicate comparisons
	// retain their historical support without enabling general arithmetic.
	if num, ok := x.Expr.(*ast.NumberExpr); ok {
		f, err := parseLuaNumber(num.Value)
		if err != nil {
			return nil, &CompileError{Line: x.Line(), Reason: err.Error()}
		}
		return &constNode{v: NewNumber(-f)}, nil
	}
	if !w.profile.AllowArithmetic {
		return nil, &CompileError{Line: x.Line(), Reason: "unary minus is allowed only on a numeric literal (e.g. -100)"}
	}
	inner, err := w.walkExpr(x.Expr)
	if err != nil {
		return nil, err
	}
	if !inner.resultType().equalsType(IntType) && !inner.resultType().equalsType(NumberType) {
		return nil, &CompileError{Line: x.Line(), Reason: "unary minus requires int or number, got " + inner.resultType().typeName()}
	}
	return &unaryMinusNode{expr: inner, typ: inner.resultType()}, nil
}

// parseLuaNumber accepts Lua number lexical forms (`1`, `1.0`, `0xFF`,
// `1e10`, `1.5e-3`). All forms produce a float64 in the IR. Hex
// literals are capped at 53 bits: beyond that a float64 IR would
// silently round the value — the same mis-coercion RR-O0LM1 rejects
// for decimal int literals — and the cap makes the value provably in
// range for the float→int64 coercion in coerceIntLiteral.
func parseLuaNumber(s string) (float64, error) {
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		u, err := strconv.ParseUint(s[2:], 16, 53)
		if err != nil {
			if errors.Is(err, strconv.ErrRange) {
				return 0, fmt.Errorf("hex number literal %q exceeds 2^53 and cannot be represented exactly", s)
			}
			// coverage-ignore: defensive: gopher-lua's lexer validates hex syntax before a 0x literal reaches here, so
			// ParseUint can only fail
			// with ErrRange (handled above), never a syntax error
			return 0, fmt.Errorf("invalid hex number literal %q", s)
		}
		return float64(u), nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number literal %q", s)
	}
	return f, nil
}
