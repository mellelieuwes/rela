package queryplan_test

import (
	"slices"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/predicate"
	"github.com/Sourcehaven-BV/rela/internal/predicatefns"
	"github.com/Sourcehaven-BV/rela/internal/queryplan"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

func TestLowerScope(t *testing.T) {
	meta := conditionMeta()
	const identity = "PERS-JV"

	tests := []struct {
		name      string
		src       string
		identity  string
		wantOK    bool
		wantProps []store.PropPredicate
		wantTrav  int
		wantID    string
	}{
		{
			name:   "equalities and a traversal lower together",
			src:    "entity.status == 'open' and related(entity, 'implements') and is_current_user(entity.assignee)",
			wantOK: true, identity: identity, wantTrav: 1,
			wantProps: []store.PropPredicate{
				{Property: "status", Op: store.PropEqual, Value: "open", Scalar: true},
				{Property: "assignee", Op: store.PropEqual, Value: identity, Scalar: true},
			},
		},
		{name: "a lone traversal lowers", src: "related(entity, 'implements')", wantOK: true, wantTrav: 1},
		{
			name: "equalities alone lower", src: "entity.status == 'open'", wantOK: true,
			wantProps: []store.PropPredicate{{Property: "status", Op: store.PropEqual, Value: "open", Scalar: true}},
		},
		{name: "no identity declines an identity scope", src: "is_current_user(entity.assignee)"},
		{name: "a membership declines", src: "has_current_user(entity.watchers)", identity: identity},
		{name: "a repeated attribute declines", src: "entity.status == 'a' and entity.status == 'b'"},
		{name: "a typed property declines", src: "entity.count == 3"},
		{name: "current_user.tool declines", src: "entity.assignee == current_user.tool", identity: identity},
		{name: "or declines", src: "entity.status == 'a' or related(entity, 'implements')"},
		{name: "not related declines", src: "not related(entity, 'implements')"},
		{
			name:   "a current_user traversal lowers bound to the identity",
			src:    "entity.status == 'open' and related(entity, 'ownedBy', { id = current_user.id })",
			wantOK: true, identity: identity, wantTrav: 1, wantID: identity,
			wantProps: []store.PropPredicate{{Property: "status", Op: store.PropEqual, Value: "open", Scalar: true}},
		},
		{
			name:   "a literal id lowers",
			src:    "related(entity, 'ownedBy', { id = 'PERS-X' })",
			wantOK: true, wantTrav: 1, wantID: "PERS-X",
		},
		// Declining is the fail-closed direction: the Go path then reports
		// ErrNoCurrentUser. Lowering would need an id, and an empty one
		// reaches the store as "any endpoint".
		{name: "no identity declines a current_user traversal", src: "related(entity, 'ownedBy', { id = current_user.id })"},
	}
	ev := predicatefns.NewEvaluator(meta)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := ev.CompileWithCurrentUser("task", tc.src)
			if err != nil {
				t.Fatalf("compile %q: %v", tc.src, err)
			}
			got, ok := queryplan.LowerScope(prog, meta, "task", tc.identity)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if !slices.Equal(got.Props, tc.wantProps) {
				t.Errorf("props = %+v, want %+v", got.Props, tc.wantProps)
			}
			if len(got.Traversals) != tc.wantTrav {
				t.Errorf("traversals = %d, want %d", len(got.Traversals), tc.wantTrav)
			}
			for _, spec := range got.Traversals {
				if len(spec.Refs) != 0 {
					t.Errorf("a lowered traversal is still unbound: %+v", spec)
				}
				if tc.wantID != "" && spec.ID != predicate.NewString(tc.wantID) {
					t.Errorf("id = %#v, want %q", spec.ID, tc.wantID)
				}
			}
		})
	}
}

// A lowered scope pushes only columns the derived index already covers, so
// pushing it down never probes an unindexed shape (RR-3MMGN9).
func TestLowerScope_PropsAreIndexColumns(t *testing.T) {
	meta := conditionMeta()
	ev := predicatefns.NewEvaluator(meta)
	for _, src := range []string{
		"entity.status == 'open' and is_current_user(entity.assignee)",
		"entity.assignee == current_user.id and related(entity, 'implements')",
	} {
		prog, err := ev.CompileWithCurrentUser("task", src)
		if err != nil {
			t.Fatalf("compile %q: %v", src, err)
		}
		got, ok := queryplan.LowerScope(prog, meta, "task", "PERS-JV")
		if !ok {
			t.Fatalf("%q did not lower", src)
		}
		indexed := queryplan.ConditionIndexProperties(prog, meta, []string{"task"})
		for _, p := range got.Props {
			if !slices.Contains(indexed, p.Property) {
				t.Errorf("%q pushes %q, which no derived index covers (%v)", src, p.Property, indexed)
			}
		}
	}
}

// The metamodel gate is applied against the type being lowered: a property
// that type does not declare as a string declines.
func TestLowerScope_GatesOnTheLoweredType(t *testing.T) {
	meta := conditionMeta()
	prog, err := predicatefns.NewEvaluator(meta).CompileWithCurrentUser("task", "entity.status == 'a'")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := queryplan.LowerScope(prog, meta, "undeclared", ""); ok {
		t.Error("an equality on an undeclared type lowered")
	}
}

func TestLowerScope_NilInputs(t *testing.T) {
	meta := conditionMeta()
	prog, err := predicatefns.NewEvaluator(meta).CompileWithCurrentUser("task", "entity.status == 'a'")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := queryplan.LowerScope(nil, meta, "task", ""); ok {
		t.Error("nil program lowered")
	}
	if _, ok := queryplan.LowerScope(prog, nil, "task", ""); ok {
		t.Error("nil metamodel lowered")
	}
	if _, ok := queryplan.LowerScope(prog, meta, "", ""); ok {
		t.Error("empty type lowered")
	}
}
