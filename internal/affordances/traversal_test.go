package affordances_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/affordances"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/predicate"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
)

// stubBinder answers every traversal with answer[rowID] and counts binds.
type stubBinder struct {
	answer map[string]bool
	err    error
	binds  int
}

func (b *stubBinder) Bind(
	_ context.Context, _ string, _ []string, _ ...*predicate.Program,
) (func(string) predicate.TraversalFunc, error) {
	b.binds++
	if b.err != nil {
		return nil, b.err
	}
	return func(rowID string) predicate.TraversalFunc {
		return func(predicate.Value, predicate.TraversalSpec) (bool, error) { return b.answer[rowID], nil }
	}, nil
}

// status is writable only on a ticket that implements no feature yet.
const relatedPolicy = `
roles:
  triager:
    fields:
      ticket:
        - field: status
          when: "not related(entity, 'implements')"
assignments:
  alice: triager
`

func TestResolver_RelatedWhen(t *testing.T) {
	t.Parallel()
	b := &stubBinder{answer: map[string]bool{"T-LINKED": true}}
	r, err := affordances.New(testMeta(t), newStubLookup(), declFor(t, policyFromYAML(t, relatedPolicy)),
		affordances.WithTraversals(b))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !statusWritable(r.FieldVerdicts(ctxAs("alice"), ticket("T-FREE", nil))) {
		t.Error("T-FREE implements nothing: status should be writable")
	}
	if statusWritable(r.FieldVerdicts(ctxAs("alice"), ticket("T-LINKED", nil))) {
		t.Error("T-LINKED implements a feature: status should be denied")
	}
}

// Priming answers a page with one bind; verdicts on the primed ctx make no
// further store call, however many rows the page has.
func TestResolver_PrimeTraversalsBindsOncePerPage(t *testing.T) {
	t.Parallel()
	for _, n := range []int{10, 50} {
		b := &stubBinder{answer: map[string]bool{}}
		r, err := affordances.New(testMeta(t), newStubLookup(), declFor(t, policyFromYAML(t, relatedPolicy)),
			affordances.WithTraversals(b))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		rows := make([]*entity.Entity, n)
		for i := range rows {
			rows[i] = ticket(strings.Repeat("T", i+1), nil)
		}
		ctx := r.PrimeTraversals(ctxAs("alice"), rows)
		for _, e := range rows {
			r.FieldVerdicts(ctx, e)
			r.RelationVerdicts(ctx, e)
		}
		if b.binds != 1 {
			t.Fatalf("n=%d: binds = %d, want 1", n, b.binds)
		}
	}
}

// A primed bind error is kept for the page: every row on it is denied, and the
// store is not asked again per row.
func TestResolver_PrimedBindErrorDenies(t *testing.T) {
	t.Parallel()
	b := &stubBinder{err: errors.New("store down")}
	r, err := affordances.New(testMeta(t), newStubLookup(), declFor(t, policyFromYAML(t, relatedPolicy)),
		affordances.WithTraversals(b))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rows := []*entity.Entity{ticket("T-1", nil), ticket("T-2", nil)}
	ctx := r.PrimeTraversals(ctxAs("alice"), rows)
	for _, e := range rows {
		if statusWritable(r.FieldVerdicts(ctx, e)) {
			t.Errorf("%s: a grant whose traversal failed must be denied", e.ID)
		}
	}
	if b.binds != 1 {
		t.Fatalf("binds = %d, want 1", b.binds)
	}
}

// A row that was not primed is answered once and kept, so the next verdict
// call on the same ctx does not query again.
func TestResolver_LiveAnswerIsKeptOnPrimedCtx(t *testing.T) {
	t.Parallel()
	b := &stubBinder{answer: map[string]bool{}}
	r, err := affordances.New(testMeta(t), newStubLookup(), declFor(t, policyFromYAML(t, relatedPolicy)),
		affordances.WithTraversals(b))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := r.PrimeTraversals(ctxAs("alice"), []*entity.Entity{ticket("T-1", nil)})
	late := ticket("T-2", nil)
	r.FieldVerdicts(ctx, late)
	r.FieldVerdicts(ctx, late)
	r.RelationVerdicts(ctx, late)
	if b.binds != 2 {
		t.Fatalf("binds = %d, want 2 (the page, then T-2 once)", b.binds)
	}
}

// A row the page did not prime is answered live, never as "no match": under
// `not related(...)` a missing answer would otherwise grant.
func TestResolver_UnprimedRowAnsweredLive(t *testing.T) {
	t.Parallel()
	b := &stubBinder{answer: map[string]bool{"T-LINKED": true}}
	r, err := affordances.New(testMeta(t), newStubLookup(), declFor(t, policyFromYAML(t, relatedPolicy)),
		affordances.WithTraversals(b))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := r.PrimeTraversals(ctxAs("alice"), []*entity.Entity{ticket("T-OTHER", nil)})
	if statusWritable(r.FieldVerdicts(ctx, ticket("T-LINKED", nil))) {
		t.Error("unprimed T-LINKED: status should be denied")
	}
	if b.binds != 2 {
		t.Fatalf("binds = %d, want 2 (the prime and one live bind)", b.binds)
	}
}

func TestResolver_RelatedDeniesWhenUnanswerable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		b    *stubBinder
		ctx  func() context.Context
		e    func() *entity.Entity
	}{
		{"bind error", &stubBinder{err: errors.New("store down")},
			func() context.Context { return ctxAs("alice") }, func() *entity.Entity { return ticket("T-1", nil) }},
		{"historical subject", &stubBinder{},
			func() context.Context { return affordances.WithHistoricalSubject(ctxAs("alice")) },
			func() *entity.Entity { return ticket("T-1", nil) }},
		{"named face", &stubBinder{}, func() context.Context { return ctxAs("alice") },
			func() *entity.Entity { e := ticket("T-1", nil); e.Face = entity.Face("en"); return e }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, err := affordances.New(testMeta(t), newStubLookup(), declFor(t, policyFromYAML(t, relatedPolicy)),
				affordances.WithTraversals(tc.b))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if statusWritable(r.FieldVerdicts(tc.ctx(), tc.e())) {
				t.Error("an unanswerable related() must deny the grant")
			}
		})
	}
}

func TestResolver_RelatedRefusedAtLoad(t *testing.T) {
	t.Parallel()
	p := policyFromYAML(t, relatedPolicy)
	if _, err := affordances.New(testMeta(t), newStubLookup(), declFor(t, p)); err == nil {
		t.Error("no binder: want a compile error")
	}
	bad := policyFromYAML(t, strings.Replace(relatedPolicy, "'implements'", "'nope'", 1))
	if _, err := affordances.New(testMeta(t), newStubLookup(), declFor(t, bad),
		affordances.WithTraversals(&stubBinder{})); err == nil {
		t.Error("unknown relation: want a compile error")
	}
	// A grant must not depend on a traversal bound to the caller's identity;
	// see refuseIdentityTraversal.
	mine := policyFromYAML(t, strings.Replace(relatedPolicy,
		"'implements')", "'implements', { id = current_user.id })", 1))
	_, err := affordances.New(testMeta(t), newStubLookup(), declFor(t, mine), affordances.WithTraversals(&stubBinder{}))
	if err == nil || !strings.Contains(err.Error(), "role_relations") {
		t.Errorf("current_user in a when: traversal: want a compile error naming role_relations, got %v", err)
	}
}

// A transition verdict is served to principals like a grant's, so a transition
// when: filtering on a property some role cannot see earns the same warning.
// Not parallel: it swaps the default logger.
func TestResolver_WithMachinesWarnsOnHiddenTraversalFilter(t *testing.T) {
	meta := testMeta(t)
	meta.Types = map[string]metamodel.CustomType{"flow": {
		Values:  []string{"a", "b"},
		Initial: "a",
		Transitions: []metamodel.TransitionDef{
			{From: "a", To: "b", When: "related(entity, 'implements', { title = 'x' })"},
		},
	}}
	def := meta.Entities["ticket"]
	def.Properties["stage"] = metamodel.PropertyDef{Type: "flow"}
	meta.Entities["ticket"] = def
	machines, err := statemachine.Compile(meta, statemachine.WithTraversals(&stubBinder{}))
	if err != nil {
		t.Fatal(err)
	}
	r, err := affordances.New(meta, newStubLookup(), declFor(t, policyFromYAML(t, `
roles:
  viewer:
    read: ["*"]
    visible:
      feature: []
`)))
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)
	r.WithMachines(machines)

	if out := buf.String(); !strings.Contains(out, "transition ticket.stage a→b") || !strings.Contains(out, "property=title") {
		t.Fatalf("want a warning naming the transition and property, got %q", out)
	}
}
