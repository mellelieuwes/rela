package predicate

import (
	"context"
	"strings"
	"testing"
)

func traversalEnv(t *testing.T) *Env {
	t.Helper()
	env := NewEnv()
	if err := env.DeclareVar("entity", RecordType{"status": StringType}); err != nil {
		t.Fatal(err)
	}
	return env
}

// The form must compile to a traversal the lowering can read back exactly —
// path, type ascription and property constraints — because that description
// IS what gets pushed into the store.
func TestRelated_CompilesToAnInspectableSpec(t *testing.T) {
	prog, err := Compile(traversalEnv(t),
		`related(entity, 'caused-by', { type = 'ticket', status = 'done' })`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	specs := prog.Traversals()
	if len(specs) != 1 {
		t.Fatalf("expected 1 traversal, got %d", len(specs))
	}
	s := specs[0]
	if len(s.Path) != 1 || s.Path[0] != "caused-by" {
		t.Errorf("path = %v", s.Path)
	}
	if s.EntityType != "ticket" {
		t.Errorf("entity type = %q", s.EntityType)
	}
	if got := s.PropNames(); len(got) != 1 || got[0] != "status" {
		t.Fatalf("prop names = %v", got)
	}
	if v, ok := s.Props["status"].(String); !ok || v.String() != "done" {
		t.Errorf("status prop = %#v", s.Props["status"])
	}
}

// A traversal must NOT taint SQLPortable. That is the whole reason the form is
// compiled statically instead of being a host function: one non-portable node
// disables pushdown for every other clause in the same condition.
func TestRelated_StaysSQLPortable(t *testing.T) {
	prog, err := Compile(traversalEnv(t),
		`entity.status == 'open' and related(entity, 'caused-by', { type = 'ticket' })`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !prog.SQLPortable() {
		t.Fatal("a traversal must stay SQL-portable, or it defeats its own purpose")
	}
	// It must also not be mistaken for a host function.
	for _, fn := range prog.Functions() {
		if fn == FuncRelated {
			t.Fatalf("%s must not be reported as a host function", FuncRelated)
		}
	}
}

func TestRelated_ChainedPath(t *testing.T) {
	prog, err := Compile(traversalEnv(t),
		`related(entity, { 'rel-x', 'rel-y' }, { type = 'C' })`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	got := prog.Traversals()[0].Path
	if len(got) != 2 || got[0] != "rel-x" || got[1] != "rel-y" {
		t.Fatalf("path = %v", got)
	}
}

func TestRelated_CompileErrors(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"no args", `related()`, "expected 2 or 3 args"},
		{"too many args", `related(entity, 'a', {}, {})`, "expected 2 or 3 args"},
		{"subject not a record", `related('nope', 'a')`, "must be an entity record"},
		{"empty relation", `related(entity, '')`, "must be non-empty"},
		{"empty path list", `related(entity, {})`, "at least one relation type"},
		{"keyed path list", `related(entity, {a='b'})`, "plain list"},
		{"non-string path", `related(entity, 42)`, "must be a string or a list"},
		{"non-table constraints", `related(entity, 'a', 'b')`, "must be a table literal"},
		{"computed constraint value", `related(entity, 'a', {status=f()})`, "must be a constant"},
		{"subject field as value", `related(entity, 'a', {status=entity.status})`, "cannot read entity itself"},
		{"id from the subject", `related(entity, 'a', {id=entity.status})`, "cannot read entity itself"},
		{"nested field", `related(entity, 'a', {id=current_user.org.id})`, "must be a literal or a field of a variable"},
		{"non-string field", `related(entity, 'a', {id=current_user.admin})`, "a constraint compares strings"},
		{"unknown field", `related(entity, 'a', {id=current_user.nope})`, "unknown attribute"},
		{"undeclared variable", `related(entity, 'a', {id=someone.id})`, "someone"},
		{"type from a variable", `related(entity, 'a', {type=current_user.id})`, "must be a constant"},
		{"empty id", `related(entity, 'a', {id=''})`, "'id' must be a non-empty string"},
		{"numeric id", `related(entity, 'a', {id=1})`, "'id' must be a non-empty string"},
		{"duplicate key", `related(entity, 'a', {id='x', id=current_user.id})`, "duplicate table key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(currentUserTraversalEnv(t), tc.src)
			if err == nil {
				t.Fatalf("expected a compile error for %q", tc.src)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}

// currentUserTraversalEnv adds a current_user record to traversalEnv, the
// shape internal/predicatefns declares for a request-scoped condition.
func currentUserTraversalEnv(t *testing.T) *Env {
	t.Helper()
	env := traversalEnv(t)
	if err := env.DeclareVar("current_user", RecordType{
		"id": StringType, "admin": BoolType, "org": RecordType{"id": StringType},
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.DeclareFunc("f", FuncSig{Return: StringType}); err != nil {
		t.Fatal(err)
	}
	return env
}

// `id` is a reserved key naming the final entity, and a value may be read from
// a variable. Both must survive into the spec a lowering reads, and the
// program must report that it reads the variable: that is what makes a scope
// using it identity-dependent.
func TestRelated_IDAndVariableConstraints(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		wantID    Value
		wantRefs  map[string]VarRef
		wantProps []string
		readsUser bool
	}{
		{
			name: "literal id", src: `related(entity, 'r', { id = 'USR-1' })`,
			wantID: NewString("USR-1"), wantProps: []string{},
		},
		{
			name: "id from current_user", src: `related(entity, 'r', { id = current_user.id })`,
			wantRefs:  map[string]VarRef{"id": {Var: "current_user", Field: "id"}},
			wantProps: []string{}, readsUser: true,
		},
		{
			name: "property from current_user", src: `related(entity, 'r', { status = current_user.id, type = 't' })`,
			wantRefs:  map[string]VarRef{"status": {Var: "current_user", Field: "id"}},
			wantProps: []string{"status"}, readsUser: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := Compile(currentUserTraversalEnv(t), tc.src)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			s := prog.Traversals()[0]
			if s.ID != tc.wantID {
				t.Errorf("ID = %#v, want %#v", s.ID, tc.wantID)
			}
			if len(s.Refs) != len(tc.wantRefs) {
				t.Fatalf("refs = %v, want %v", s.Refs, tc.wantRefs)
			}
			for k, want := range tc.wantRefs {
				if s.Refs[k] != want {
					t.Errorf("ref %q = %v, want %v", k, s.Refs[k], want)
				}
			}
			if got := strings.Join(s.PropNames(), ","); got != strings.Join(tc.wantProps, ",") {
				t.Errorf("prop names = %q, want %q", got, tc.wantProps)
			}
			if _, isProp := s.Props["id"]; isProp {
				t.Error("id must never be read as a property")
			}
			if got := prog.References("current_user"); got != tc.readsUser {
				t.Errorf("References(current_user) = %v, want %v", got, tc.readsUser)
			}
			if !prog.SQLPortable() {
				t.Error("a variable constraint must not taint SQL portability")
			}
		})
	}
}

// Eval binds each variable constraint from the evaluation's own bindings and
// hands the resolver a spec with no Refs left. An empty value is an error,
// never an unconstrained traversal: an empty id would lower to an endpoint
// set the store reads as "any endpoint".
func TestRelated_EvalBindsVariableConstraints(t *testing.T) {
	prog, err := Compile(currentUserTraversalEnv(t),
		`not related(entity, 'r', { id = current_user.id, status = current_user.id })`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	eval := func(user string) (TraversalSpec, error) {
		t.Helper()
		var seen TraversalSpec
		b := NewBindings()
		if setErr := b.SetVar("entity", NewRecord(map[string]Value{"status": NewString("open")})); setErr != nil {
			t.Fatal(setErr)
		}
		if setErr := b.SetVar("current_user", NewRecord(map[string]Value{
			"id": NewString(user), "admin": NewBool(false),
			"org": NewRecord(map[string]Value{"id": NewString("ORG")}),
		})); setErr != nil {
			t.Fatal(setErr)
		}
		b.SetTraversal(func(_ Value, spec TraversalSpec) (bool, error) {
			seen = spec
			return false, nil
		})
		_, evalErr := prog.Eval(context.Background(), b)
		return seen, evalErr
	}

	seen, err := eval("USR-1")
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if len(seen.Refs) != 0 || seen.ID != NewString("USR-1") || seen.Props["status"] != NewString("USR-1") {
		t.Fatalf("resolver saw an unbound or wrong spec: %+v", seen)
	}
	other, err := eval("USR-2")
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if other.Key() == seen.Key() {
		t.Error("two identities must not share a traversal key")
	}
	if _, err := eval(""); err == nil {
		t.Fatal("an empty identity must fail the Eval, not answer the negation")
	}
}

// Bind is the lowering's route to the same result, and must refuse exactly
// what Eval refuses. The receiver is shared (it belongs to the compiled
// program), so binding must not write into it.
func TestTraversalSpec_Bind(t *testing.T) {
	prog, err := Compile(currentUserTraversalEnv(t), `related(entity, 'r', { id = current_user.id, status = 'x' })`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	spec := prog.Traversals()[0]
	for _, tc := range []struct {
		name   string
		values map[string]Value
		ok     bool
	}{
		{"bound", map[string]Value{"id": NewString("USR-1")}, true},
		{"missing", map[string]Value{}, false},
		{"empty", map[string]Value{"id": NewString("")}, false},
		{"not a string", map[string]Value{"id": NewNumber(1)}, false},
		{"nil", map[string]Value{"id": NewNil()}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := spec.Bind(tc.values)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if tc.ok && (got.ID != NewString("USR-1") || len(got.Refs) != 0 || got.Props["status"] != NewString("x")) {
				t.Errorf("bound spec = %+v", got)
			}
		})
	}
	if spec.ID != nil || len(spec.Refs) != 1 || len(spec.Props) != 1 {
		t.Fatalf("Bind modified the compiled spec: %+v", spec)
	}
}

// An unbound traversal must FAIL rather than answer false: a false would
// present an unanswerable traversal as a legitimate "no match", which is the
// wrong direction for a condition gating access or an automation.
func TestRelated_UnboundResolverFailsRatherThanAnsweringFalse(t *testing.T) {
	prog, err := Compile(traversalEnv(t), `related(entity, 'caused-by')`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	b := NewBindings()
	if err := b.SetVar("entity", NewRecord(map[string]Value{"status": NewString("open")})); err != nil {
		t.Fatal(err)
	}
	if _, err := prog.Eval(context.Background(), b); err == nil {
		t.Fatal("an unbound traversal resolver must fail the Eval")
	}
}

func TestRelated_EvaluatesThroughTheBoundResolver(t *testing.T) {
	prog, err := Compile(traversalEnv(t), `related(entity, 'caused-by', { type = 'ticket' })`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	var seen TraversalSpec
	b := NewBindings()
	if setErr := b.SetVar("entity", NewRecord(map[string]Value{"status": NewString("open")})); setErr != nil {
		t.Fatal(setErr)
	}
	b.SetTraversal(func(_ Value, spec TraversalSpec) (bool, error) {
		seen = spec
		return true, nil
	})
	v, err := prog.Eval(context.Background(), b)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if got, ok := v.(Bool); !ok || !got.Bool() {
		t.Fatalf("expected true, got %#v", v)
	}
	if seen.EntityType != "ticket" {
		t.Errorf("resolver saw spec %+v", seen)
	}
}

// The subject is recorded so a metamodel-aware caller can refuse a traversal
// that does not start from the row entity (TKT-CXQEV0).
func TestRelated_RecordsTheSubject(t *testing.T) {
	prog, err := Compile(traversalEnv(t), `related(entity, 'caused-by')`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if got := prog.Traversals()[0].Subject; got != "entity" {
		t.Fatalf("subject = %q, want entity", got)
	}
}

// Key identifies the question a traversal asks: equal for the same question
// regardless of how the constraint table was ordered, and different when any
// part of it differs — including a value's type, so '1' and 1 never share an
// answer.
func TestTraversalSpec_Key(t *testing.T) {
	key := func(src string) string {
		t.Helper()
		prog, err := Compile(currentUserTraversalEnv(t), src)
		if err != nil {
			t.Fatalf("compile %q: %v", src, err)
		}
		return prog.Traversals()[0].Key()
	}
	base := key(`related(entity, 'r', { type = 't', a = 'x', b = 'y' })`)
	if got := key(`related(entity, 'r', { b = 'y', type = 't', a = 'x' })`); got != base {
		t.Errorf("reordered table changed the key:\n%s\n%s", base, got)
	}
	for _, src := range []string{
		`related(entity, 'q', { type = 't', a = 'x', b = 'y' })`,
		`related(entity, { 'r', 'r' }, { type = 't', a = 'x', b = 'y' })`,
		`related(entity, 'r', { type = 'u', a = 'x', b = 'y' })`,
		`related(entity, 'r', { type = 't', a = 'x', b = 'z' })`,
		`related(entity, 'r', { type = 't', a = 'x' })`,
		`related(entity, 'r', { type = 't', a = 'x', b = 1 })`,
		`related(entity, 'r', { type = 't', a = 'x', b = 'y', id = 'x' })`,
		`related(entity, 'r', { type = 't', a = 'x', b = current_user.id })`,
	} {
		if key(src) == base {
			t.Errorf("%s shares a key with the base spec", src)
		}
	}
}
