// Package predicate is a small, sandboxed typed expression-evaluation engine
// over named values and host-registered functions. Compile retains the strict
// boolean-predicate profile; CompileValue accepts an explicit scalar profile.
//
// The package parses a strict subset of Lua expression syntax via
// gopher-lua's parse package (already vendored for write-path automation),
// walks the AST against a hard allow-list, translates it to a typed
// internal IR, and evaluates the IR against caller-supplied bindings.
//
// # Lifecycle
//
//	env := predicate.NewEnv()
//	env.DeclareVar("entity", predicate.RecordType{
//	    "status": predicate.StringType,
//	})
//	env.DeclareVar("current_user", predicate.RecordType{})
//	env.DeclareFunc("has_role", predicate.FuncSig{
//	    Params: []predicate.Type{predicate.RecordType{}, predicate.StringType},
//	    Return: predicate.BoolType,
//	})
//
//	prog, err := predicate.Compile(env,
//	    `entity.status == 'review' and has_role(current_user, 'reviewer')`)
//	if err != nil { ... }
//
//	b := predicate.NewBindings()
//	b.SetVar("entity", predicate.NewRecord(map[string]predicate.Value{
//	    "status": predicate.NewString("review"),
//	}))
//	b.SetVar("current_user", predicate.NewRecord(map[string]predicate.Value{}))
//	b.SetFunc("has_role", predicate.FuncFunc(
//	    func(ctx context.Context, args []predicate.Value) (predicate.Value, error) {
//	        return predicate.NewBool(true), nil
//	    },
//	))
//
//	v, err := prog.Eval(ctx, b)
//	// v.(predicate.Bool).Bool() == true
//
// # Concurrency
//
// A *Program is immutable after Compile and is safe to Eval concurrently
// from multiple goroutines, each with its own Bindings. The Eval call
// allocates per-invocation visitor state; no caches or memoization live
// on *Program.
//
// # Equality semantics
//
// Predicates use Lua-flavored equality, not Go-flavored. Comparison
// across most type pairs is a compile-time error; the few mixed-type
// pairs that compile follow this table:
//
//	a is     b is     a == b
//	-------- -------- --------------------------
//	nil      nil      true
//	nil      anything false
//	bool     bool     Go ==
//	number   number   float64 == (single numeric type, see below)
//	string   string   byte-equal (incl. null bytes)
//
// Ordered comparisons (<, <=, >, >=) require two numbers or two strings;
// strings compare lexicographically (byte-wise).
//
// # Numeric model
//
// Numbers are a single type backed by float64, matching Lua 5.1 semantics.
// Integer literals (1, 0xFF), float literals (1.0, 1.5e-3), and exponential
// forms (1e10) all parse to the same Number type. Bindings of Go int are
// promoted to float64 at binding time; values outside the 53-bit integer
// range round per IEEE 754.
//
// The separate Int type (int64) exists for integer-typed metamodel
// properties so comparisons are exact and non-lexicographic — there is no
// Int *literal* syntax, so a number literal compared against an Int-typed
// attribute is coerced to Int at compile time (rejecting a fractional
// literal). See the Typed values section.
//
// # Typed values (Int, Date) and literal coercion
//
// Beyond the scalar primitives, the engine carries two metamodel-driven
// typed values: Int (int64) and Date (time.Time, instant-granular, backing
// both `date` and `datetime` properties). There is no literal syntax for
// either — an integer is written as a number literal and a date as a string
// literal. When one operand of a comparison is an Int/Date-typed expression
// (declared via IntType / DateTypeWithLayout on the Env) and the other is a
// bare literal, the literal is COERCED to the operand's type at COMPILE
// time: a number literal becomes an Int (fractional literals are rejected),
// a string literal is parsed to a Date against the field's declared layout.
//
// The same coercion applies to value selection (below): a literal operand
// of `or` takes its sibling's Int/Date type, a comparison against a
// selection coerces the selection's literal branches, and a top-level
// literal or all-literal selection is coerced to the profile's expected
// type. Coercion does not reach into arithmetic operands or host-function
// arguments at the top level: `-(c and 1 or 2)` stays a Number.
//
// Coercion at compile time is deliberate: it keeps Eval a pure function
// with no parsing and no metamodel access (the Date is already a time.Time
// by the time Eval runs), which is what lets the engine be used on the
// read/ACL path. The metamodel->Env mapping (property type -> predicate
// type, incl. the date layout) lives in internal/predicatefns so this
// package stays dependency-free.
//
// # Value selection
//
// `and`/`or` with bool operands are boolean logic. With a bool condition and
// values of one scalar type (string, number, int, date) they choose a value
// as in Lua: `c and x` yields x when c is true and nil when it is false, and
// `x or y` yields x unless it is nil. A bool is never a chosen value, which
// rules out Lua's `c and false or y` pitfall; a nil value still falls
// through, so `c and entity.opt or y` yields y when opt is absent. A nil
// condition is an eval error, as in boolean logic. Selection is available
// in every profile: it adds no capability a boolean profile could need to
// withhold (the top level must still be bool there), unlike arithmetic,
// which changes what a condition can compute.
//
// # Security model
//
// The walker rejects any AST node not on the allow-list (default-reject
// branch on every switch). Per-field invariants are enforced beyond node
// type — e.g. AttrGetExpr.Key must be *StringExpr, rejecting computed
// attribute access entity[expr]. A compile-time depth budget (default 256)
// defends against stack overflow from adversarially nested expressions;
// a per-Eval step budget (default 10_000) defends against runtime
// exhaustion. Neither budget can be disabled, only raised.
//
// The package does no I/O: no file access, no network, no goroutine
// spawning. It is a pure function from (Program, Bindings) to a Value.
//
// # Target portability
//
// Programs expose their exact static record-field references and whether every
// node/host function declares SQL-portable semantics. This is classification,
// not lowering: a host-only function remains valid in a context that permits it
// and makes only that Program non-portable. Context profiles can restrict the
// accepted feature set, but must never change the semantics of accepted IR.
package predicate
