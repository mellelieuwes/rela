package predicate

import (
	"reflect"
	"testing"
)

// prefilterEnv is the request-scoped shape: an entity record with mixed
// property types plus a constant current_user record and the two sugar
// functions a caller would list in PrefilterSpec.ConstFuncs.
func prefilterEnv(t *testing.T) *Env {
	t.Helper()
	env := NewEnv()
	if err := env.DeclareVar("entity", RecordType{
		"assignee": StringType,
		"reporter": StringType,
		"status":   StringType,
		"count":    IntType,
		"done":     BoolType,
		"tags":     ListType{Elem: StringType},
		"watchers": ListType{Elem: StringType},
		"scores":   ListType{Elem: IntType},
	}); err != nil {
		t.Fatalf("declare entity: %v", err)
	}
	if err := env.DeclareVar("current_user", RecordType{
		"id":   StringType,
		"tool": StringType,
	}); err != nil {
		t.Fatalf("declare current_user: %v", err)
	}
	funcs := map[string]FuncSig{
		"is_current_user":  {Params: []Type{StringType}, Return: BoolType, SQLPortable: true},
		"has_current_user": {Params: []Type{ListType{Elem: StringType}}, Return: BoolType, SQLPortable: true},
		// A look-alike the spec does NOT list: proves recognition is
		// opt-in per function, not by shape.
		"is_reporter": {Params: []Type{StringType}, Return: BoolType},
		"any_int":     {Params: []Type{ListType{Elem: IntType}}, Return: BoolType},
	}
	for name, sig := range funcs {
		if err := env.DeclareFunc(name, sig); err != nil {
			t.Fatalf("declare %s: %v", name, err)
		}
	}
	return env
}

// fullSpec is what internal/predicatefns hands over: both records and
// both sugar functions.
var fullSpec = PrefilterSpec{
	RecordVar: "entity",
	ConstVar:  "current_user",
	ConstFuncs: map[string]string{
		"is_current_user":  "id",
		"has_current_user": "id",
	},
}

func TestConstEqualities(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []ConstEquality
	}{
		{
			name: "equality against the constant record is pushable",
			src:  "entity.assignee == current_user.id",
			want: []ConstEquality{{Attribute: "assignee", FromVar: "id"}},
		},
		{
			name: "reversed operand order is the same constraint",
			src:  "current_user.id == entity.assignee",
			want: []ConstEquality{{Attribute: "assignee", FromVar: "id"}},
		},
		{
			name: "string literal equality is pushable",
			src:  "entity.status == 'open'",
			want: []ConstEquality{{Attribute: "status", Value: "open"}},
		},
		{
			name: "an AND chain yields every conjunct",
			src:  "entity.status == 'open' and entity.assignee == current_user.id",
			want: []ConstEquality{
				{Attribute: "assignee", FromVar: "id"},
				{Attribute: "status", Value: "open"},
			},
		},
		{
			name: "nested AND is still a top-level chain",
			src:  "(entity.status == 'open' and entity.reporter == 'x') and entity.assignee == current_user.id",
			want: []ConstEquality{
				{Attribute: "assignee", FromVar: "id"},
				{Attribute: "reporter", Value: "x"},
				{Attribute: "status", Value: "open"},
			},
		},

		// --- the sugar functions ---
		{
			name: "is_current_user over a string attribute is the same equality",
			src:  "is_current_user(entity.assignee)",
			want: []ConstEquality{{Attribute: "assignee", FromVar: "id"}},
		},
		{
			name: "has_current_user over a string list is membership",
			src:  "has_current_user(entity.watchers)",
			want: []ConstEquality{{Attribute: "watchers", FromVar: "id", List: true}},
		},
		{
			name: "sugar composes with the other conjuncts",
			src:  "entity.status == 'open' and is_current_user(entity.assignee) and has_current_user(entity.watchers)",
			want: []ConstEquality{
				{Attribute: "assignee", FromVar: "id"},
				{Attribute: "status", Value: "open"},
				{Attribute: "watchers", FromVar: "id", List: true},
			},
		},
		{
			name: "a sugar call under an OR pushes nothing",
			src:  "is_current_user(entity.assignee) or has_current_user(entity.watchers)",
			want: nil,
		},
		{
			name: "a negated sugar call pushes nothing",
			src:  "not has_current_user(entity.watchers)",
			want: nil,
		},
		{
			name: "a sugar call compared as a boolean is not the bare call shape",
			src:  "is_current_user(entity.assignee) == true",
			want: nil,
		},
		{
			name: "a sugar call over the constant record's own field is not an entity attribute",
			src:  "is_current_user(current_user.tool)",
			want: nil,
		},
		{
			name: "a sugar call over a literal pushes nothing",
			src:  "is_current_user('PERS-JV')",
			want: nil,
		},
		{
			name: "a function the spec does not list is not recognized, whatever its shape",
			src:  "is_reporter(entity.assignee)",
			want: nil,
		},

		// --- soundness restrictions: each of these MUST push nothing ---
		{
			name: "OR pushes nothing: either branch may be false in a matching row",
			src:  "entity.assignee == current_user.id or entity.reporter == current_user.id",
			want: nil,
		},
		{
			name: "an AND under an OR pushes nothing",
			src:  "entity.status == 'open' or (entity.assignee == current_user.id and entity.reporter == 'x')",
			want: nil,
		},
		{
			name: "NOT inverts the sense, so pushes nothing",
			src:  "not (entity.assignee == current_user.id)",
			want: nil,
		},
		{
			name: "inequality pushes nothing",
			src:  "entity.assignee ~= current_user.id",
			want: nil,
		},
		{
			name: "ordered comparison pushes nothing",
			src:  "entity.status > 'open'",
			want: nil,
		},
		{
			name: "field-to-field equality varies per row",
			src:  "entity.assignee == entity.reporter",
			want: nil,
		},
		{
			name: "an empty string literal is not pushed: the store reads it as 'is empty'",
			src:  "entity.status == ''",
			want: nil,
		},
		{
			name: "a typed (int) equality is not pushed: string-form comparison could disagree",
			src:  "entity.count == 3",
			want: nil,
		},
		{
			name: "a bool equality is not pushed either",
			src:  "entity.done == true",
			want: nil,
		},
		{
			name: "the constant record's own fields are not entity attributes",
			src:  "current_user.id == 'PERS-JV'",
			want: nil,
		},
		{
			name: "a mixed AND still pushes the sound conjunct only",
			src:  "entity.assignee == current_user.id and entity.count == 3",
			want: []ConstEquality{{Attribute: "assignee", FromVar: "id"}},
		},
		{
			name: "an OR beside a pushable AND conjunct keeps the conjunct",
			src:  "entity.status == 'open' and (entity.assignee == current_user.id or entity.reporter == 'x')",
			want: []ConstEquality{{Attribute: "status", Value: "open"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := Compile(prefilterEnv(t), tc.src)
			if err != nil {
				t.Fatalf("compile %q: %v", tc.src, err)
			}
			got := prog.ConstEqualities(fullSpec)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d equalities %+v, want %d %+v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("equality %d: got %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestConstEqualities_WithoutConstVar proves the constant record is
// opt-in: a caller that names no ConstVar gets literals only, never a
// field whose value it has no way to supply — and the sugar functions
// go with it, since they are equalities against that same record.
func TestConstEqualities_WithoutConstVar(t *testing.T) {
	prog, err := Compile(prefilterEnv(t),
		"entity.status == 'open' and entity.assignee == current_user.id and is_current_user(entity.reporter)")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	got := prog.ConstEqualities(PrefilterSpec{RecordVar: "entity", ConstFuncs: fullSpec.ConstFuncs})
	want := []ConstEquality{{Attribute: "status", Value: "open"}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestConstEqualities_WithoutConstFuncs proves the sugar functions are
// opt-in independently of the record: a caller that names the record but
// lists no functions gets the `==` shapes only.
func TestConstEqualities_WithoutConstFuncs(t *testing.T) {
	prog, err := Compile(prefilterEnv(t),
		"entity.assignee == current_user.id and is_current_user(entity.reporter)")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	got := prog.ConstEqualities(PrefilterSpec{RecordVar: "entity", ConstVar: "current_user"})
	want := []ConstEquality{{Attribute: "assignee", FromVar: "id"}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestConstEqualities_ListedFuncWithUnsupportedArgType pins that a listed
// function is still only recognized over the two attribute types whose
// store lowering is known — a list of ints is neither.
func TestConstEqualities_ListedFuncWithUnsupportedArgType(t *testing.T) {
	prog, err := Compile(prefilterEnv(t), "any_int(entity.scores)")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	spec := PrefilterSpec{
		RecordVar: "entity", ConstVar: "current_user",
		ConstFuncs: map[string]string{"any_int": "id"},
	}
	if got := prog.ConstEqualities(spec); got != nil {
		t.Fatalf("got %+v, want nil for a list-of-int argument", got)
	}
}

// TestConstEqualities_NilAndEmpty covers the degenerate inputs rather
// than leaving them to a panic at a call site.
func TestConstEqualities_NilAndEmpty(t *testing.T) {
	var nilProg *Program
	if got := nilProg.ConstEqualities(fullSpec); got != nil {
		t.Fatalf("nil program: got %+v, want nil", got)
	}
	prog, err := Compile(prefilterEnv(t), "entity.status == 'open'")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if got := prog.ConstEqualities(PrefilterSpec{ConstVar: "current_user"}); got != nil {
		t.Fatalf("empty RecordVar: got %+v, want nil", got)
	}
	if got := prog.ConstEqualities(PrefilterSpec{RecordVar: "nosuchvar", ConstVar: "current_user"}); got != nil {
		t.Fatalf("unknown RecordVar: got %+v, want nil", got)
	}
}

// TestConstEqualities_DuplicateAttributeReportedOnce pins the documented
// contract that the result is a SUBSET of the program's constraints: the
// Go pass remains authoritative for the one that is dropped.
func TestConstEqualities_DuplicateAttributeReportedOnce(t *testing.T) {
	prog, err := Compile(prefilterEnv(t),
		"entity.status == 'open' and entity.status == 'closed'")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	got := prog.ConstEqualities(fullSpec)
	if len(got) != 1 || got[0].Attribute != "status" {
		t.Fatalf("got %+v, want a single status equality", got)
	}
}

// TestProgram_ReferencesAndFunctions pins the two dependency accessors a
// caller uses to decide whether a binding is required at all.
func TestProgram_ReferencesAndFunctions(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		refsUser  bool
		functions []string
	}{
		{"attribute access references the record", "entity.assignee == current_user.id", true, nil},
		{"a literal comparison references nothing else", "entity.status == 'open'", false, nil},
		{"a sugar call is a function, not a variable reference",
			"is_current_user(entity.assignee)", false, []string{"is_current_user"}},
		{"functions are sorted and deduplicated",
			"has_current_user(entity.watchers) and is_current_user(entity.assignee) and is_current_user(entity.reporter)",
			false, []string{"has_current_user", "is_current_user"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := Compile(prefilterEnv(t), tc.src)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			if got := prog.References("current_user"); got != tc.refsUser {
				t.Fatalf("References(current_user) = %v, want %v", got, tc.refsUser)
			}
			if !prog.References("entity") {
				t.Fatalf("every fixture references entity")
			}
			got := prog.Functions()
			if len(got) != len(tc.functions) {
				t.Fatalf("Functions() = %v, want %v", got, tc.functions)
			}
			for i := range got {
				if got[i] != tc.functions[i] {
					t.Fatalf("Functions() = %v, want %v", got, tc.functions)
				}
			}
		})
	}
}

// TestProgram_InspectCoversEveryNodeType compiles one expression per sealed
// node type and reads the dependency accessors, so the default arm in
// inspect (which panics on an unhandled node) is exercised by construction:
// a node type added without extending the walk fails here, not at runtime.
func TestProgram_InspectCoversEveryNodeType(t *testing.T) {
	env := prefilterEnv(t)
	if err := env.DeclareFunc("with_table", FuncSig{
		Params: []Type{StringType, tableArgType{}}, Return: BoolType,
	}); err != nil {
		t.Fatalf("declare with_table: %v", err)
	}
	value := ValueProfile(StringType)
	number := ValueProfile(IntType)
	tests := []struct {
		name    string
		src     string
		profile *Profile // nil → boolean condition profile
	}{
		{"const + var + attr + relational", "entity.status == 'x'", nil},
		{"logical + not", "not (entity.done and entity.count == 1)", nil},
		{"call", "is_current_user(entity.assignee)", nil},
		{"table argument", "with_table(entity.status, {mode='x'})", nil},
		{"arithmetic", "entity.count + 1", &number},
		{"unary minus", "-entity.count", &number},
		{"concatenation", "entity.status .. 'x'", &value},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				prog *Program
				err  error
			)
			if tc.profile == nil {
				prog, err = Compile(env, tc.src)
			} else {
				prog, err = CompileValue(env, tc.src, *tc.profile)
			}
			if err != nil {
				t.Fatalf("compile %q: %v", tc.src, err)
			}
			if !prog.References("entity") {
				t.Fatalf("%q: entity not reported as referenced", tc.src)
			}
			_ = prog.Functions()
		})
	}
}

func TestConjunction(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		wantOK    bool
		wantEqs   []ConstEquality
		wantPaths [][]string
	}{
		{
			name:      "equalities and traversals on the spine",
			src:       "entity.status == 'open' and related(entity, 'implements') and is_current_user(entity.assignee)",
			wantOK:    true,
			wantEqs:   []ConstEquality{{Attribute: "status", Value: "open"}, {Attribute: "assignee", FromVar: "id"}},
			wantPaths: [][]string{{"implements"}},
		},
		{
			name:      "a lone traversal",
			src:       "related(entity, { 'a', 'b' }, { type = 'C', status = 'done' })",
			wantOK:    true,
			wantPaths: [][]string{{"a", "b"}},
		},
		{
			name:    "an attribute constrained twice is reported twice",
			src:     "entity.status == 'a' and entity.status == 'b'",
			wantOK:  true,
			wantEqs: []ConstEquality{{Attribute: "status", Value: "a"}, {Attribute: "status", Value: "b"}},
		},
		{
			name:    "a membership keeps List",
			src:     "has_current_user(entity.watchers)",
			wantOK:  true,
			wantEqs: []ConstEquality{{Attribute: "watchers", FromVar: "id", List: true}},
		},
		{name: "or is not exact", src: "entity.status == 'a' or related(entity, 'x')"},
		{name: "not is not exact", src: "not related(entity, 'x')"},
		{name: "inequality is not exact", src: "entity.status ~= 'a' and related(entity, 'x')"},
		{name: "a typed equality is not exact", src: "entity.count == 3 and related(entity, 'x')"},
		{name: "an empty literal is not exact", src: "entity.status == '' and related(entity, 'x')"},
		{name: "an unlisted host call is not exact", src: "is_reporter(entity.reporter)"},
		{name: "a comparison between two attributes is not exact", src: "entity.status == entity.reporter"},
		{name: "a literal leaf is not exact", src: "true and related(entity, 'x')"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := Compile(prefilterEnv(t), tc.src)
			if err != nil {
				t.Fatalf("compile %q: %v", tc.src, err)
			}
			eqs, travs, ok := prog.Conjunction(fullSpec)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(eqs, tc.wantEqs) {
				t.Errorf("eqs = %#v, want %#v", eqs, tc.wantEqs)
			}
			var paths [][]string
			for _, s := range travs {
				paths = append(paths, s.Path)
			}
			if !reflect.DeepEqual(paths, tc.wantPaths) {
				t.Errorf("paths = %v, want %v", paths, tc.wantPaths)
			}
		})
	}
}

func TestConjunction_TraversalOnAnotherRecordIsNotExact(t *testing.T) {
	env := prefilterEnv(t)
	prog, err := Compile(env, "related(current_user, 'x')")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, _, ok := prog.Conjunction(fullSpec); ok {
		t.Fatal("a traversal from current_user must not lower as the row's traversal")
	}
}

// A value-selecting `and` is not a conjunction: its operands are not
// constraints on the entity, so neither prefilter may read equalities
// out of it.
func TestPrefilter_IgnoresValueSelection(t *testing.T) {
	env := prefilterEnv(t)
	for _, src := range []string{
		"(entity.status == 'open' and 'a' or 'b') == 'a'",
		"(entity.assignee == current_user.id and 'mine' or 'other') == 'mine'",
	} {
		t.Run(src, func(t *testing.T) {
			prog, err := Compile(env, src)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if eqs := prog.ConstEqualities(fullSpec); len(eqs) != 0 {
				t.Fatalf("ConstEqualities = %v, want none", eqs)
			}
			if _, _, ok := prog.Conjunction(fullSpec); ok {
				t.Fatal("Conjunction must refuse a comparison over a selection")
			}
		})
	}
	// The guard itself: a selecting `and` reached directly is skipped.
	sel := &logicalNode{op: "and", typ: StringType,
		lhs: &relationalNode{op: "==", lhs: &attrNode{obj: &varNode{name: "entity"}, name: "status"}, rhs: &constNode{v: NewString("open")}},
		rhs: &constNode{v: NewString("a")}}
	out := map[string]ConstEquality{}
	collectConstEqualities(sel, fullSpec, out)
	if len(out) != 0 {
		t.Fatalf("collectConstEqualities = %v, want none", out)
	}
	var leaves []node
	collectConjuncts(sel, &leaves)
	if len(leaves) != 1 || leaves[0] != node(sel) {
		t.Fatalf("collectConjuncts split a selection: %v", leaves)
	}
}
