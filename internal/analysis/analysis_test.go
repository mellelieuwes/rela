package analysis_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/analysis"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/lua"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
	"github.com/Sourcehaven-BV/rela/internal/tracer"
)

// addEntity / addRelation: terse seed helpers that panic on error.
// Used widely below; tests stay focused on the behavior under check.

func addEntity(s store.Store, id, entityType string, props map[string]any) {
	if err := s.CreateEntity(context.Background(), &entity.Entity{
		ID: id, Type: entityType, Properties: props,
	}); err != nil {
		panic(err)
	}
}

func addRelation(s store.Store, from, relType, to string) {
	if _, err := s.CreateRelation(context.Background(), from, relType, to, nil); err != nil {
		panic(err)
	}
}

// newServiceWith builds a Service backed by a fresh memstore. Seed
// runs before tracer is captured so tracer observes the final state.
// LuaReadDeps is wired so RunValidations can construct a validation
// service even when tests don't exercise Lua.
func newServiceWith(t *testing.T, meta *metamodel.Metamodel, seed func(store.Store)) *analysis.Service {
	t.Helper()
	st := memstore.New()
	if seed != nil {
		seed(st)
	}
	tr := tracer.New(st)
	svc, err := analysis.New(analysis.Deps{
		Store:  st,
		Meta:   meta,
		Tracer: tr,
		LuaReadDeps: lua.ReadDeps{
			VisibleReader: st,
			Tracer:        tr,
			Meta:          meta,
		},
	})
	if err != nil {
		t.Fatalf("analysis.New: %v", err)
	}
	return svc
}

func TestFindOrphansWithScope(t *testing.T) {
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"doc": {Label: "Document"},
		},
	}

	svc := newServiceWith(t, meta, func(s store.Store) {
		addEntity(s, "DOC-001", "doc", nil)
		addEntity(s, "DOC-002", "doc", nil)
		addEntity(s, "DOC-003", "doc", nil)
		addRelation(s, "DOC-001", "refs", "DOC-002")
	})

	t.Run("no scope", func(t *testing.T) {
		orphans, _ := svc.FindOrphansWithScope(context.Background(), analysis.Options{})
		if len(orphans) != 1 {
			t.Errorf("got %d orphans, want 1", len(orphans))
		}
		if len(orphans) > 0 && orphans[0].ID != "DOC-003" {
			t.Errorf("orphan = %s, want DOC-003", orphans[0].ID)
		}
	})

	t.Run("with scope including orphan", func(t *testing.T) {
		orphans, _ := svc.FindOrphansWithScope(context.Background(), analysis.Options{
			Scope: map[string]bool{"DOC-003": true},
		})
		if len(orphans) != 1 {
			t.Errorf("got %d orphans, want 1", len(orphans))
		}
	})

	t.Run("with scope excluding orphan", func(t *testing.T) {
		orphans, _ := svc.FindOrphansWithScope(context.Background(), analysis.Options{
			Scope: map[string]bool{"DOC-001": true, "DOC-002": true},
		})
		if len(orphans) != 0 {
			t.Errorf("got %d orphans, want 0", len(orphans))
		}
	})
}

func TestFindDuplicates(t *testing.T) {
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"doc": {Label: "Document"},
		},
	}

	svc := newServiceWith(t, meta, func(s store.Store) {
		addEntity(s, "DOC-001", "doc", map[string]any{"title": "Test Document"})
		addEntity(s, "DOC-002", "doc", map[string]any{"title": "test document"})
		addEntity(s, "DOC-003", "doc", map[string]any{"title": "Different"})
	})

	t.Run("finds duplicates", func(t *testing.T) {
		dups, _ := svc.FindDuplicates(context.Background(), analysis.Options{})
		if len(dups) != 1 {
			t.Errorf("got %d duplicate groups, want 1", len(dups))
		}
		if len(dups) > 0 && len(dups[0].Entities) != 2 {
			t.Errorf("duplicate group has %d entities, want 2", len(dups[0].Entities))
		}
	})

	t.Run("scope filters duplicates", func(t *testing.T) {
		dups, _ := svc.FindDuplicates(context.Background(), analysis.Options{
			Scope: map[string]bool{"DOC-001": true},
		})
		if len(dups) != 0 {
			t.Errorf("got %d duplicate groups, want 0", len(dups))
		}
	})
}

// TestFindGaps_ManualIDsAreSkippedByType pins that a manual-ID type is left out
// even when its ids carry more dashes than its declared prefix: `tw-` parses
// `tw-basecamp-10078439742` to the prefix `tw-basecamp-`, so a prefix match
// never excluded it and the analysis enumerated ~20 million "missing" ids.
func TestFindGaps_ManualIDsAreSkippedByType(t *testing.T) {
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"twin": {IDType: metamodel.IDTypeManual, IDPrefix: "tw-"},
			"req":  {IDType: metamodel.IDTypeSequential, IDPrefix: "REQ-"},
		},
	}
	svc := newServiceWith(t, meta, func(s store.Store) {
		addEntity(s, "tw-basecamp-10078439742", "twin", nil)
		addEntity(s, "tw-basecamp-10098661922", "twin", nil)
		addEntity(s, "REQ-001", "req", nil)
		addEntity(s, "REQ-003", "req", nil)
	})

	gaps, err := svc.FindGaps(context.Background(), analysis.Options{})
	if err != nil {
		t.Fatalf("FindGaps: %v", err)
	}
	if len(gaps) != 1 || gaps[0].Prefix != "REQ-" {
		t.Fatalf("want only the REQ- sequence, got %+v", gaps)
	}
	if got := strings.Join(gaps[0].Missing, ","); got != "REQ-002" {
		t.Errorf("missing = %q, want REQ-002", got)
	}
}

// TestFindGaps_LargeGapIsCountedNotListed pins that a sequence names at most
// 100 missing ids and counts the rest, so one stray large number costs a count,
// not a million strings.
func TestFindGaps_LargeGapIsCountedNotListed(t *testing.T) {
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"req": {IDType: metamodel.IDTypeSequential, IDPrefix: "REQ-"},
		},
	}
	svc := newServiceWith(t, meta, func(s store.Store) {
		addEntity(s, "REQ-001", "req", nil)
		addEntity(s, "REQ-1000000", "req", nil)
		addEntity(s, "REQ-2000000", "req", nil)
	})

	gaps, err := svc.FindGaps(context.Background(), analysis.Options{})
	if err != nil {
		t.Fatalf("FindGaps: %v", err)
	}
	if len(gaps) != 1 {
		t.Fatalf("want one sequence, got %d", len(gaps))
	}
	const total = (1000000 - 1 - 1) + (2000000 - 1000000 - 1)
	gap := gaps[0]
	if len(gap.Missing) != 100 {
		t.Errorf("listed %d missing ids, want 100", len(gap.Missing))
	}
	if gap.Missing[0] != "REQ-002" || gap.Missing[99] != "REQ-101" {
		t.Errorf("listed %s … %s, want REQ-002 … REQ-101", gap.Missing[0], gap.Missing[99])
	}
	if got := len(gap.Missing) + gap.Unlisted; got != total {
		t.Errorf("listed + unlisted = %d, want %d", got, total)
	}
}

func TestFindUniqueViolations(t *testing.T) {
	// persoon.email is unique; nickname is not; aliases is unique+list (skipped).
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"persoon": {Label: "Persoon", Properties: map[string]metamodel.PropertyDef{
				"email":    {Type: "string", Unique: true},
				"nickname": {Type: "string"},
				"aliases":  {Type: "string", Unique: true, List: true},
			}},
			"account": {Label: "Account", Properties: map[string]metamodel.PropertyDef{
				"email": {Type: "string", Unique: true},
			}},
		},
	}

	svc := newServiceWith(t, meta, func(s store.Store) {
		addEntity(s, "PERS-JV", "persoon", map[string]any{"email": "jv@x.com", "nickname": "dup"})
		addEntity(s, "PERS-DUP", "persoon", map[string]any{"email": "jv@x.com", "nickname": "dup"})
		addEntity(s, "PERS-TS", "persoon", map[string]any{"email": "ts@x.com"})
		addEntity(s, "PERS-NONE", "persoon", nil) // empty email — exempt
		// Same email on a different type must NOT collide (scoped per type).
		addEntity(s, "ACC-1", "account", map[string]any{"email": "jv@x.com"})
	})

	t.Run("finds the email collision, not nickname or cross-type", func(t *testing.T) {
		v, _ := svc.FindUniqueViolations(context.Background(), analysis.Options{})
		if len(v) != 1 {
			t.Fatalf("got %d violations, want 1: %+v", len(v), v)
		}
		got := v[0]
		if got.EntityType != "persoon" || got.Property != "email" || got.Value != "jv@x.com" {
			t.Fatalf("violation = %s.%s=%q, want persoon.email=jv@x.com", got.EntityType, got.Property, got.Value)
		}
		if len(got.Entities) != 2 {
			t.Fatalf("collision group has %d entities, want 2 (PERS-JV, PERS-DUP)", len(got.Entities))
		}
	})

	t.Run("scope filters violations", func(t *testing.T) {
		v, _ := svc.FindUniqueViolations(context.Background(), analysis.Options{
			Scope: map[string]bool{"PERS-JV": true}, // only one of the pair in scope
		})
		if len(v) != 0 {
			t.Errorf("got %d violations, want 0 (collision partner out of scope)", len(v))
		}
	})

	t.Run("no unique properties → no work", func(t *testing.T) {
		plain := &metamodel.Metamodel{Entities: map[string]metamodel.EntityDef{
			"doc": {Label: "Doc", Properties: map[string]metamodel.PropertyDef{"title": {Type: "string"}}},
		}}
		s2 := newServiceWith(t, plain, func(s store.Store) {
			addEntity(s, "DOC-1", "doc", map[string]any{"title": "same"})
			addEntity(s, "DOC-2", "doc", map[string]any{"title": "same"})
		})
		if v, _ := s2.FindUniqueViolations(context.Background(), analysis.Options{}); len(v) != 0 {
			t.Errorf("got %d violations, want 0 (no unique props declared)", len(v))
		}
	})
}

func TestCheckCardinality(t *testing.T) {
	minOne := 1
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket":  {Label: "Ticket", IDPrefixes: []string{"TKT-"}},
			"concept": {Label: "Concept", IDPrefixes: []string{"CON-"}},
		},
		Relations: map[string]metamodel.RelationDef{
			"affects": {
				Label:       "affects",
				From:        []string{"ticket"},
				To:          []string{"concept"},
				MinOutgoing: &minOne,
			},
		},
	}

	svc := newServiceWith(t, meta, func(s store.Store) {
		addEntity(s, "TKT-001", "ticket", nil)
		addEntity(s, "TKT-002", "ticket", nil)
		addEntity(s, "CON-001", "concept", nil)
		addRelation(s, "TKT-001", "affects", "CON-001")
	})

	t.Run("finds violations", func(t *testing.T) {
		violations, err := svc.CheckCardinality(context.Background(), analysis.Options{})
		if err != nil {
			t.Fatalf("CheckCardinality: %v", err)
		}
		if len(violations) != 1 {
			t.Errorf("got %d violations, want 1", len(violations))
		}
		if len(violations) > 0 {
			if violations[0].EntityID != "TKT-002" {
				t.Errorf("violation entity = %s, want TKT-002", violations[0].EntityID)
			}
			if violations[0].Constraint != "min_outgoing" {
				t.Errorf("constraint = %s, want min_outgoing", violations[0].Constraint)
			}
		}
	})

	t.Run("scope filters violations", func(t *testing.T) {
		violations, err := svc.CheckCardinality(context.Background(), analysis.Options{
			Scope: map[string]bool{"TKT-001": true},
		})
		if err != nil {
			t.Fatalf("CheckCardinality: %v", err)
		}
		if len(violations) != 0 {
			t.Errorf("got %d violations, want 0", len(violations))
		}
	})
}

// TestCheckCardinality_OrderingAndLabels pins the per-relation contract
// before the TKT-RNBLAC consolidation: min-then-max ordering, outgoing
// block before incoming block, the constraint strings, the
// Required/Actual values, and the incoming-side inverse label. The
// expectations were captured against the four pre-consolidation check
// functions and must not change. Multi-type ordering is pinned by
// TestCheckCardinality_MultipleSourceTypes and the pass-grouping by
// TestCheckCardinality_MinMaxGroupedAcrossTypes.
func TestCheckCardinality_OrderingAndLabels(t *testing.T) {
	one := 1

	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket":  {Label: "Ticket", IDPrefixes: []string{"TKT-"}},
			"concept": {Label: "Concept", IDPrefixes: []string{"CON-"}},
		},
		Relations: map[string]metamodel.RelationDef{
			"depends": {
				Label:       "depends",
				From:        []string{"ticket"},
				To:          []string{"concept"},
				MinOutgoing: &one,
				MaxOutgoing: &one,
				MinIncoming: &one,
				MaxIncoming: &one,
				Inverse:     &metamodel.InverseDef{ID: "needed-by"},
			},
		},
	}

	svc := newServiceWith(t, meta, func(s store.Store) {
		addEntity(s, "TKT-A", "ticket", nil) // 0 outgoing → min_outgoing
		addEntity(s, "TKT-B", "ticket", nil) // 2 outgoing → max_outgoing
		addEntity(s, "TKT-C", "ticket", nil) // 1 outgoing → ok
		addEntity(s, "CON-X", "concept", nil)
		addEntity(s, "CON-Y", "concept", nil) // 2 incoming → max_incoming
		addEntity(s, "CON-Z", "concept", nil) // 0 incoming → min_incoming
		addRelation(s, "TKT-B", "depends", "CON-X")
		addRelation(s, "TKT-B", "depends", "CON-Y")
		addRelation(s, "TKT-C", "depends", "CON-Y")
	})

	got, err := svc.CheckCardinality(context.Background(), analysis.Options{})
	if err != nil {
		t.Fatalf("CheckCardinality: %v", err)
	}

	want := []analysis.CardinalityViolation{
		{EntityID: "TKT-A", RelationType: "depends", Constraint: "min_outgoing", Required: 1, Actual: 0},
		{EntityID: "TKT-B", RelationType: "depends", Constraint: "max_outgoing", Required: 1, Actual: 2},
		// Incoming violations report the inverse label when declared.
		{EntityID: "CON-Z", RelationType: "needed-by", Constraint: "min_incoming", Required: 1, Actual: 0},
		{EntityID: "CON-Y", RelationType: "needed-by", Constraint: "max_incoming", Required: 1, Actual: 2},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d violations, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("violation[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestCheckCardinality_BoundEdgeCases pins the min/max asymmetry: an
// explicit 0 minimum is a skip (nothing can violate it), while an
// explicit 0 maximum is a real constraint (any edge violates it).
func TestCheckCardinality_BoundEdgeCases(t *testing.T) {
	zero := 0

	t.Run("max zero is enforced", func(t *testing.T) {
		meta := &metamodel.Metamodel{
			Entities: map[string]metamodel.EntityDef{
				"ticket":  {Label: "Ticket", IDPrefixes: []string{"TKT-"}},
				"concept": {Label: "Concept", IDPrefixes: []string{"CON-"}},
			},
			Relations: map[string]metamodel.RelationDef{
				"depends": {
					From: []string{"ticket"}, To: []string{"concept"},
					MaxOutgoing: &zero,
				},
			},
		}
		svc := newServiceWith(t, meta, func(s store.Store) {
			addEntity(s, "TKT-A", "ticket", nil)
			addEntity(s, "CON-X", "concept", nil)
			addRelation(s, "TKT-A", "depends", "CON-X")
		})
		got, err := svc.CheckCardinality(context.Background(), analysis.Options{})
		if err != nil {
			t.Fatalf("CheckCardinality: %v", err)
		}
		want := []analysis.CardinalityViolation{
			{EntityID: "TKT-A", RelationType: "depends", Constraint: "max_outgoing", Required: 0, Actual: 1},
		}
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("min zero is skipped", func(t *testing.T) {
		meta := &metamodel.Metamodel{
			Entities: map[string]metamodel.EntityDef{
				"ticket":  {Label: "Ticket", IDPrefixes: []string{"TKT-"}},
				"concept": {Label: "Concept", IDPrefixes: []string{"CON-"}},
			},
			Relations: map[string]metamodel.RelationDef{
				"depends": {
					From: []string{"ticket"}, To: []string{"concept"},
					MinOutgoing: &zero,
				},
			},
		}
		svc := newServiceWith(t, meta, func(s store.Store) {
			addEntity(s, "TKT-A", "ticket", nil) // 0 outgoing, but min is 0
		})
		got, err := svc.CheckCardinality(context.Background(), analysis.Options{})
		if err != nil {
			t.Fatalf("CheckCardinality: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d violations, want 0: %+v", len(got), got)
		}
	})
}

// TestCheckCardinality_MultipleSourceTypes pins the ordering across a
// relation's From types: subjects are visited in From-list order.
func TestCheckCardinality_MultipleSourceTypes(t *testing.T) {
	one := 1
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"bug":     {Label: "Bug", IDPrefixes: []string{"BUG-"}},
			"ticket":  {Label: "Ticket", IDPrefixes: []string{"TKT-"}},
			"concept": {Label: "Concept", IDPrefixes: []string{"CON-"}},
		},
		Relations: map[string]metamodel.RelationDef{
			"affects": {
				From: []string{"ticket", "bug"}, To: []string{"concept"},
				MinOutgoing: &one,
			},
		},
	}
	svc := newServiceWith(t, meta, func(s store.Store) {
		addEntity(s, "BUG-1", "bug", nil)
		addEntity(s, "TKT-1", "ticket", nil)
	})
	got, err := svc.CheckCardinality(context.Background(), analysis.Options{})
	if err != nil {
		t.Fatalf("CheckCardinality: %v", err)
	}
	// From-list order (ticket before bug), not ID order.
	want := []analysis.CardinalityViolation{
		{EntityID: "TKT-1", RelationType: "affects", Constraint: "min_outgoing", Required: 1, Actual: 0},
		{EntityID: "BUG-1", RelationType: "affects", Constraint: "min_outgoing", Required: 1, Actual: 0},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d violations, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("violation[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestCheckCardinality_MinMaxGroupedAcrossTypes is the discriminating
// ordering test: with min AND max violations spread over two From
// types, ALL min violations must come before ALL max violations. A
// single count-and-emit pass would interleave them
// ([TKT-A min, TKT-B max, BUG-A min]) — the exact regression the
// two-pass emission in checkCardinality exists to prevent.
func TestCheckCardinality_MinMaxGroupedAcrossTypes(t *testing.T) {
	one := 1
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket":  {Label: "Ticket", IDPrefixes: []string{"TKT-"}},
			"bug":     {Label: "Bug", IDPrefixes: []string{"BUG-"}},
			"concept": {Label: "Concept", IDPrefixes: []string{"CON-"}},
		},
		Relations: map[string]metamodel.RelationDef{
			"affects": {
				From: []string{"ticket", "bug"}, To: []string{"concept"},
				MinOutgoing: &one, MaxOutgoing: &one,
			},
		},
	}
	svc := newServiceWith(t, meta, func(s store.Store) {
		addEntity(s, "TKT-A", "ticket", nil) // 0 out → min
		addEntity(s, "TKT-B", "ticket", nil) // 2 out → max
		addEntity(s, "BUG-A", "bug", nil)    // 0 out → min
		addEntity(s, "CON-X", "concept", nil)
		addEntity(s, "CON-Y", "concept", nil)
		addRelation(s, "TKT-B", "affects", "CON-X")
		addRelation(s, "TKT-B", "affects", "CON-Y")
	})
	got, err := svc.CheckCardinality(context.Background(), analysis.Options{})
	if err != nil {
		t.Fatalf("CheckCardinality: %v", err)
	}
	want := []analysis.CardinalityViolation{
		{EntityID: "TKT-A", RelationType: "affects", Constraint: "min_outgoing", Required: 1, Actual: 0},
		{EntityID: "BUG-A", RelationType: "affects", Constraint: "min_outgoing", Required: 1, Actual: 0},
		{EntityID: "TKT-B", RelationType: "affects", Constraint: "max_outgoing", Required: 1, Actual: 2},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d violations, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("violation[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// failingCountStore wraps a store.Store and fails every CountRelations
// call, simulating a backend outage during the cardinality scan.
type failingCountStore struct {
	store.Store
	err error
}

func (f *failingCountStore) CountRelations(context.Context, store.RelationQuery) (int, error) {
	return 0, f.err
}

// TestCheckCardinality_CountErrorFailsLoudly pins the TKT-RNBLAC error
// policy: a failing CountRelations must abort the run with a wrapped
// error naming the entity and relation, and must NOT surface as a
// count-0 min violation (the fabricated-violation bug the old
// `n, _ :=` produced).
func TestCheckCardinality_CountErrorFailsLoudly(t *testing.T) {
	one := 1
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket":  {Label: "Ticket", IDPrefixes: []string{"TKT-"}},
			"concept": {Label: "Concept", IDPrefixes: []string{"CON-"}},
		},
		Relations: map[string]metamodel.RelationDef{
			"affects": {
				From: []string{"ticket"}, To: []string{"concept"},
				MinOutgoing: &one,
			},
		},
	}

	st := memstore.New()
	addEntity(st, "TKT-001", "ticket", nil)
	addEntity(st, "CON-001", "concept", nil)
	addRelation(st, "TKT-001", "affects", "CON-001") // satisfied — only the outage could "violate"

	countErr := errors.New("backend down")
	broken := &failingCountStore{Store: st, err: countErr}
	tr := tracer.New(broken)
	svc, err := analysis.New(analysis.Deps{Store: broken, Meta: meta, Tracer: tr,
		LuaReadDeps: lua.ReadDeps{VisibleReader: broken, Tracer: tr, Meta: meta}})
	if err != nil {
		t.Fatalf("analysis.New: %v", err)
	}

	violations, err := svc.CheckCardinality(context.Background(), analysis.Options{})
	if err == nil {
		t.Fatalf("want error, got nil (violations: %+v)", violations)
	}
	if !errors.Is(err, countErr) {
		t.Errorf("error does not wrap the store error: %v", err)
	}
	for _, want := range []string{"TKT-001", "affects"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing context %q", err, want)
		}
	}
	if len(violations) != 0 {
		t.Errorf("got %d violations alongside the error, want none: %+v", len(violations), violations)
	}

	t.Run("AnalyzeAll propagates", func(t *testing.T) {
		if _, err := svc.AnalyzeAll(context.Background(), analysis.Options{}); err == nil {
			t.Error("AnalyzeAll: want error, got nil")
		}
	})
}

func TestAnalyzeAll(t *testing.T) {
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"doc": {Label: "Document", IDPrefixes: []string{"DOC-"}},
		},
	}

	svc := newServiceWith(t, meta, func(s store.Store) {
		addEntity(s, "DOC-001", "doc", nil)
	})

	summary, err := svc.AnalyzeAll(context.Background(), analysis.Options{})
	if err != nil {
		t.Fatalf("AnalyzeAll: %v", err)
	}
	if summary.Orphans != 1 {
		t.Errorf("Orphans = %d, want 1", summary.Orphans)
	}
}

func TestRunValidations(t *testing.T) {
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket": {
				Label:    "Ticket",
				IDPrefix: "TKT-",
				Properties: map[string]metamodel.PropertyDef{
					"status":   {Type: "string"},
					"assignee": {Type: "string"},
				},
			},
		},
		Validations: []metamodel.ValidationRule{
			{
				Name:        "in-progress-needs-assignee",
				Description: "In-progress tickets must have an assignee",
				EntityType:  "ticket",
				When:        []string{"status=in-progress"},
				Then:        []string{"assignee!="},
				Severity:    "error",
			},
		},
	}
	meta.InitAliases()

	svc := newServiceWith(t, meta, func(s store.Store) {
		addEntity(s, "TKT-001", "ticket", map[string]any{"status": "in-progress"})
	})

	result, _ := svc.RunValidations(context.Background(), analysis.Options{})
	violations := result.Violations
	if len(violations) != 1 {
		t.Fatalf("got %d violations, want 1", len(violations))
	}
	if violations[0].EntityID != "TKT-001" {
		t.Errorf("violation entity = %s, want TKT-001", violations[0].EntityID)
	}
}

func TestRunValidationsFiltered(t *testing.T) {
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket": {
				Label:      "Ticket",
				IDPrefix:   "TKT-",
				Properties: map[string]metamodel.PropertyDef{"status": {Type: "string"}},
			},
			"bug": {
				Label:      "Bug",
				IDPrefix:   "BUG-",
				Properties: map[string]metamodel.PropertyDef{"status": {Type: "string"}},
			},
		},
		Validations: []metamodel.ValidationRule{
			{
				Name:       "ticket-rule",
				EntityType: "ticket",
				When:       []string{"status=bad"},
				Then:       []string{"status!=bad"},
				Severity:   "error",
			},
			{
				Name:       "bug-rule",
				EntityType: "bug",
				When:       []string{"status=bad"},
				Then:       []string{"status!=bad"},
				Severity:   "error",
			},
		},
	}
	meta.InitAliases()

	svc := newServiceWith(t, meta, func(s store.Store) {
		addEntity(s, "TKT-001", "ticket", map[string]any{"status": "bad"})
		addEntity(s, "BUG-001", "bug", map[string]any{"status": "bad"})
	})

	t.Run("filter by rule name", func(t *testing.T) {
		res, _ := svc.RunValidationsFiltered(
			context.Background(), analysis.Options{}, []analysis.ValidationFilter{{RuleName: "ticket-rule"}},
		)
		violations := res.Violations
		if len(violations) != 1 || violations[0].RuleName != "ticket-rule" {
			t.Errorf("got %#v, want one ticket-rule violation", violations)
		}
	})

	t.Run("filter by entity type", func(t *testing.T) {
		res, _ := svc.RunValidationsFiltered(
			context.Background(), analysis.Options{}, []analysis.ValidationFilter{{EntityType: "bug"}},
		)
		violations := res.Violations
		if len(violations) != 1 || violations[0].RuleName != "bug-rule" {
			t.Errorf("got %#v, want one bug-rule violation", violations)
		}
	})
}

func TestService_New_RejectsNilDeps(t *testing.T) {
	// Zero-value Metamodel and a real memstore are passed only to
	// advance past earlier nil-checks. None are dereferenced before
	// the next nil-check fires.
	meta := &metamodel.Metamodel{}
	st := memstore.New()
	tr := tracer.New(st)

	cases := []struct {
		name string
		d    analysis.Deps
		want string
	}{
		{"nil store", analysis.Deps{Meta: meta, Tracer: tr}, "Store is required"},
		{"nil meta", analysis.Deps{Store: st, Tracer: tr}, "Meta is required"},
		{"nil tracer", analysis.Deps{Store: st, Meta: meta}, "Tracer is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := analysis.New(tc.d)
			if err == nil {
				t.Fatalf("expected error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}
