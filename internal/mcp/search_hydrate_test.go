package mcp

import (
	"context"
	"fmt"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/search"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
	"github.com/Sourcehaven-BV/rela/internal/store/storetest"
)

// seedHits stores n default-face tickets plus one entity that exists only on
// the "published" face, and returns a hit for each.
func seedHits(t *testing.T, n int) (*storetest.Counting, []search.Hit) {
	t.Helper()
	ctx := context.Background()
	st := memstore.New()
	var hits []search.Hit
	for i := range n {
		e := newEntity(fmt.Sprintf("TKT-%03d", i), "ticket", fmt.Sprintf("ticket %d", i))
		if err := st.CreateEntity(ctx, e); err != nil {
			t.Fatalf("seed %s: %v", e.ID, err)
		}
		hits = append(hits, search.Hit{ID: e.ID, Type: e.Type})
	}
	published, err := entity.ParseFace("published")
	if err != nil {
		t.Fatal(err)
	}
	pol := newEntity("POL-1", "policy", "retention policy")
	pol.Face = published
	if err := st.CreateEntity(ctx, pol); err != nil {
		t.Fatalf("seed POL-1: %v", err)
	}
	hits = append(hits, search.Hit{ID: pol.ID, Type: pol.Type, Face: published})
	return storetest.NewCounting(st), hits
}

// TestHydrateHits_FacedHitSurvives (RR-27FAH8): a faced type has no default
// row, so a hit on one face must be read on that face, not through GetEntity.
func TestHydrateHits_FacedHitSurvives(t *testing.T) {
	st, hits := seedHits(t, 1)
	missing := search.Hit{ID: "TKT-GONE", Type: "ticket"}
	got, err := hydrateHits(context.Background(), st, append(hits, missing))
	if err != nil {
		t.Fatalf("hydrateHits: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("summaries = %v, want the ticket and the faced policy (the missing hit dropped)", got)
	}
	pol := got[1]
	if pol["id"] != "POL-1" || pol["face"] != "published" || pol["title"] != "retention policy" {
		t.Errorf("faced summary = %v", pol)
	}
}

// TestHydrateHits_ReadBudget (RR-ZLOVOE): hydration costs the same number of
// store reads at 10 hits as at 50.
func TestHydrateHits_ReadBudget(t *testing.T) {
	reads := func(n int) int {
		st, hits := seedHits(t, n)
		st.Reset()
		if _, err := hydrateHits(context.Background(), st, hits); err != nil {
			t.Fatalf("hydrateHits: %v", err)
		}
		return st.Reads()
	}
	if at10, at50 := reads(10), reads(50); at10 != at50 {
		t.Errorf("reads grow with hits: %d at 10, %d at 50", at10, at50)
	}
}
