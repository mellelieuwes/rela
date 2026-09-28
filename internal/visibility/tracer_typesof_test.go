package visibility_test

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
	"github.com/Sourcehaven-BV/rela/internal/tracer"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

// FindOrphans resolves each orphan id's TYPE before gating it. That
// resolution has two paths — a batched header read and a per-id fallback —
// and the gate's verdicts must not depend on which one ran. These tests
// drive both and assert the same visible set, because a divergence would be
// an ACL bug that only appears on stores lacking the header capability.

// orphanBase is a stub tracer returning fixed orphan ids.
type orphanBase struct{ ids []string }

func (o orphanBase) TraceFrom(context.Context, string, int) *tracer.TraceResult { return nil }
func (o orphanBase) TraceTo(context.Context, string, int) *tracer.TraceResult   { return nil }
func (o orphanBase) FindPath(context.Context, string, string) []tracer.PathStep { return nil }
func (o orphanBase) HasCycle(context.Context, string) bool                      { return false }
func (o orphanBase) FindOrphans(context.Context) ([]string, error) {
	return o.ids, nil
}

// getterOnly exposes ONLY GetEntity, so VisibleTracer cannot take the
// batched header path and must fall back to per-id resolution.
type getterOnly struct{ st store.Store }

func (g getterOnly) GetEntity(ctx context.Context, id string) (*entity.Entity, error) {
	return g.st.GetEntity(ctx, id)
}

func (g getterOnly) GetEntityState(ctx context.Context, id string, face entity.Face) (*entity.Entity, error) {
	return g.st.GetEntityState(ctx, id, face)
}

// headerErrGetter satisfies store.EntityReader but fails its header scan,
// exercising the fail-closed fallback inside the batched path.
type headerErrGetter struct{ st store.Store }

func (h headerErrGetter) GetEntity(ctx context.Context, id string) (*entity.Entity, error) {
	return h.st.GetEntity(ctx, id)
}

func (h headerErrGetter) GetEntityState(ctx context.Context, id string, face entity.Face) (*entity.Entity, error) {
	return h.st.GetEntityState(ctx, id, face)
}

func (h headerErrGetter) ListEntities(
	_ context.Context, _ store.EntityQuery,
) iter.Seq2[*entity.Entity, error] {
	return func(yield func(*entity.Entity, error) bool) {
		yield(nil, errors.New("header scan exploded"))
	}
}

func (h headerErrGetter) ListEntitiesPage(
	context.Context, store.EntityQuery,
) (store.Page[*entity.Entity], error) {
	return store.Page[*entity.Entity]{}, nil
}
func (h headerErrGetter) CountEntities(context.Context, store.EntityQuery) (int, error) {
	return 0, nil
}
func (h headerErrGetter) HighestID(context.Context, string) (int, error) { return 0, nil }
func (h headerErrGetter) PropertyValues(context.Context, string, int) ([]string, error) {
	return nil, nil
}

func seedOrphanWorld(t *testing.T) store.Store {
	t.Helper()
	ctx := context.Background()
	st := memstore.New()
	for _, e := range []*entity.Entity{
		{ID: "TKT-1", Type: "ticket", Properties: map[string]any{"title": "One"}},
		{ID: "TKT-2", Type: "ticket", Properties: map[string]any{"title": "Two"}},
		{ID: "SEC-1", Type: "secret", Properties: map[string]any{"title": "Hush"}},
	} {
		if err := st.CreateEntity(ctx, e); err != nil {
			t.Fatalf("seed %s: %v", e.ID, err)
		}
	}
	return st
}

func newOrphanTracer(t *testing.T, get visibility.EntityGetter, ids []string) tracer.Tracer {
	t.Helper()
	vt, err := visibility.NewVisibleTracer(
		orphanBase{ids: ids}, typeGate{allow: "ticket"}, visibility.NopRedactor{}, get)
	if err != nil {
		t.Fatalf("NewVisibleTracer: %v", err)
	}
	return vt
}

func TestVisibleTracer_FindOrphansGatesIdenticallyOnBothTypePaths(t *testing.T) {
	st := seedOrphanWorld(t)
	ids := []string{"TKT-1", "SEC-1", "TKT-2"}
	want := []string{"TKT-1", "TKT-2"}

	for _, tc := range []struct {
		name string
		get  visibility.EntityGetter
	}{
		{"batched header path", getterWithHeaders{st}},
		{"per-id fallback (getter cannot list)", getterOnly{st}},
		{"per-id fallback (header scan errored)", headerErrGetter{st}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newOrphanTracer(t, tc.get, ids).FindOrphans(context.Background())
			if err != nil {
				t.Fatalf("FindOrphans: %v", err)
			}
			if len(got) != len(want) {
				t.Fatalf("want %v, got %v", want, got)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("want %v, got %v", want, got)
				}
			}
		})
	}
}

// An id whose entity has vanished must drop, not pass: it never reaches the
// per-type gate probe, so it can never be marked visible.
func TestVisibleTracer_FindOrphansDropsUnresolvableIDs(t *testing.T) {
	st := seedOrphanWorld(t)
	// GONE-1 is not in the store.
	ids := []string{"TKT-1", "GONE-1"}

	for _, tc := range []struct {
		name string
		get  visibility.EntityGetter
	}{
		{"batched header path", getterWithHeaders{st}},
		{"per-id fallback", getterOnly{st}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newOrphanTracer(t, tc.get, ids).FindOrphans(context.Background())
			if err != nil {
				t.Fatalf("FindOrphans: %v", err)
			}
			for _, id := range got {
				if id == "GONE-1" {
					t.Fatal("a vanished entity must not survive the gate")
				}
			}
			if len(got) != 1 || got[0] != "TKT-1" {
				t.Fatalf("want [TKT-1], got %v", got)
			}
		})
	}
}

func TestVisibleTracer_FindOrphansEmpty(t *testing.T) {
	st := seedOrphanWorld(t)
	got, err := newOrphanTracer(t, getterWithHeaders{st}, nil).FindOrphans(context.Background())
	if err != nil {
		t.Fatalf("FindOrphans: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want no orphans, got %v", got)
	}
}

// getterWithHeaders is the full store, which memstore implements including
// store.HeaderReader — the batched path.
type getterWithHeaders struct{ st store.Store }

func (g getterWithHeaders) GetEntity(ctx context.Context, id string) (*entity.Entity, error) {
	return g.st.GetEntity(ctx, id)
}

func (g getterWithHeaders) GetEntityState(ctx context.Context, id string, face entity.Face) (*entity.Entity, error) {
	return g.st.GetEntityState(ctx, id, face)
}

func (g getterWithHeaders) ListEntities(
	ctx context.Context, q store.EntityQuery,
) iter.Seq2[*entity.Entity, error] {
	return g.st.ListEntities(ctx, q)
}

func (g getterWithHeaders) ListEntitiesPage(
	ctx context.Context, q store.EntityQuery,
) (store.Page[*entity.Entity], error) {
	return g.st.ListEntitiesPage(ctx, q)
}

func (g getterWithHeaders) CountEntities(ctx context.Context, q store.EntityQuery) (int, error) {
	return g.st.CountEntities(ctx, q)
}

func (g getterWithHeaders) HighestID(ctx context.Context, prefix string) (int, error) {
	return g.st.HighestID(ctx, prefix)
}

func (g getterWithHeaders) PropertyValues(
	ctx context.Context, property string, limit int,
) ([]string, error) {
	return g.st.PropertyValues(ctx, property, limit)
}

func (g getterWithHeaders) ListEntityHeaders(
	ctx context.Context, q store.EntityQuery,
) iter.Seq2[store.EntityHeader, error] {
	return store.ListEntityHeaders(ctx, g.st, q)
}
