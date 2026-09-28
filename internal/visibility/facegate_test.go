package visibility

import (
	"context"
	"errors"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
	"github.com/Sourcehaven-BV/rela/internal/tracer"
)

// A face grant is the second half of a read permission, and it can only be
// applied AFTER the row is loaded — an entity's face is not knowable from its
// (type, id). [RowGate] answers only the first half, so before [FaceGate]
// existed every read-out path that used a Reader was face-blind.
//
// That was a cross-principal content leak in production shape: the view
// surface served a draft body to a principal granted only `policy@published`,
// while the entity route 404'd the very same face (TKT-O7R2A1). The gate lives
// here, in the one reader all those paths already route through, rather than
// being hand-placed at each call site — four such sites had accumulated and
// two of them were still ungated when this was written.

// faceRowGate is a RowGate that also declares permitted faces per type.
type faceRowGate struct {
	// permitted maps entity type -> readable faces. A type absent from the
	// map is unrestricted, matching the "empty means every face" contract.
	permitted map[string][]entity.Face
	err       error
}

func (g faceRowGate) PermitsRead(context.Context, string, string) (bool, error) {
	return true, nil
}

func (g faceRowGate) PermitsReadMany(
	_ context.Context, _ string, ids []string,
) (map[string]bool, error) {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func (g faceRowGate) PermittedFaces(_ context.Context, entityType string) ([]entity.Face, error) {
	if g.err != nil {
		return nil, g.err
	}
	return g.permitted[entityType], nil
}

// plainRowGate permits every row and does NOT implement FaceGate — the shape
// of every pre-existing gate and test double.
type plainRowGate struct{}

func (plainRowGate) PermitsRead(context.Context, string, string) (bool, error) { return true, nil }

func (plainRowGate) PermitsReadMany(
	_ context.Context, _ string, ids []string,
) (map[string]bool, error) {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// faceGetter serves one entity, at a face the test chooses.
type faceGetter struct{ e *entity.Entity }

func (g faceGetter) GetEntityState(context.Context, string, entity.Face) (*entity.Entity, error) {
	if g.e == nil {
		return nil, errors.New("not found")
	}
	return g.e, nil
}

const published = entity.Face("published")

func draftPolicy() *entity.Entity {
	return &entity.Entity{ID: "POL-1", Type: "policy", Content: "draft body"}
}

func publishedPolicy() *entity.Entity {
	return &entity.Entity{ID: "POL-1", Type: "policy", Face: published, Content: "published body"}
}

func TestFaceGate_GetDeniesAnUngrantedFace(t *testing.T) {
	// The grant names `published`; the store holds the DRAFT face, which is
	// the empty/zero Face. That pairing is the leak's exact shape: the bare
	// face serializes as "" so a naive check passes it.
	r, err := NewPolicyReader(
		faceRowGate{permitted: map[string][]entity.Face{"policy": {published}}},
		NopRedactor{},
		faceGetter{e: draftPolicy()},
	)
	if err != nil {
		t.Fatalf("NewPolicyReader: %v", err)
	}

	got, ok, gerr := r.Get(context.Background(), "policy", "POL-1")
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if ok {
		t.Fatalf("Get served face %q to a principal granted only %q; content=%q",
			got.Face, published, got.Content)
	}
}

func TestFaceGate_GetServesTheGrantedFace(t *testing.T) {
	// The mirror of the test above. Without it, a gate that denied
	// EVERYTHING would pass — an outage reads as a successful denial.
	r, err := NewPolicyReader(
		faceRowGate{permitted: map[string][]entity.Face{"policy": {published}}},
		NopRedactor{},
		faceGetter{e: publishedPolicy()},
	)
	if err != nil {
		t.Fatalf("NewPolicyReader: %v", err)
	}

	got, ok, gerr := r.Get(context.Background(), "policy", "POL-1")
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if !ok {
		t.Fatal("Get denied the granted face")
	}
	if got.Face != published {
		t.Errorf("got face %q, want %q", got.Face, published)
	}
}

// An empty permitted set means EVERY face, not "no faces".
//
// This is the backward-compatibility contract and it is deliberately NOT the
// fail-closed direction: a world resolves each entity through its chain and
// never serves the default face, so a bare grant clamped to the default would
// read nothing at all under any world — a total outage rather than a
// narrowing. Writes differ, and do fail closed, because they address a face by
// id and never pass through a world.
func TestFaceGate_EmptyGrantMeansEveryFace(t *testing.T) {
	for _, tc := range []struct {
		name string
		e    *entity.Entity
	}{
		{"default face", draftPolicy()},
		{"named face", publishedPolicy()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewPolicyReader(
				faceRowGate{permitted: map[string][]entity.Face{}},
				NopRedactor{},
				faceGetter{e: tc.e},
			)
			if err != nil {
				t.Fatalf("NewPolicyReader: %v", err)
			}
			if _, ok, gerr := r.Get(context.Background(), "policy", "POL-1"); gerr != nil || !ok {
				t.Errorf("an unrestricted grant must read every face; ok=%v err=%v", ok, gerr)
			}
		})
	}
}

// A gate that does not implement FaceGate grants every face — the behavior a
// project declaring no faces has always had, and what keeps every existing
// RowGate implementation working unchanged.
func TestFaceGate_NonFaceGateIsUnrestricted(t *testing.T) {
	r, err := NewPolicyReader(plainRowGate{}, NopRedactor{}, faceGetter{e: publishedPolicy()})
	if err != nil {
		t.Fatalf("NewPolicyReader: %v", err)
	}
	if _, ok, gerr := r.Get(context.Background(), "policy", "POL-1"); gerr != nil || !ok {
		t.Errorf("a gate without FaceGate must not restrict faces; ok=%v err=%v", ok, gerr)
	}
}

// A gate failure HIDES, matching the package's fail-closed contract. An error
// here must not be mistaken for "no restriction" — that is the direction that
// leaks.
func TestFaceGate_GateErrorFailsClosed(t *testing.T) {
	r, err := NewPolicyReader(
		faceRowGate{err: errors.New("gate down")},
		NopRedactor{},
		faceGetter{e: publishedPolicy()},
	)
	if err != nil {
		t.Fatalf("NewPolicyReader: %v", err)
	}
	if _, ok, _ := r.Get(context.Background(), "policy", "POL-1"); ok {
		t.Error("a gate error must hide the row, not reveal it")
	}
}

// Filter and FilterHeaders enforce the same verdict as Get.
//
// Both are asserted because they are separate loops over separate types, and
// the leak that motivated this reached production through the collection path
// while the single-entity path was already gated.
func TestFaceGate_FilterAndHeadersAgreeWithGet(t *testing.T) {
	gate := faceRowGate{permitted: map[string][]entity.Face{"policy": {published}}}
	r, err := NewPolicyReader(gate, NopRedactor{}, faceGetter{e: publishedPolicy()})
	if err != nil {
		t.Fatalf("NewPolicyReader: %v", err)
	}
	ctx := context.Background()

	kept := r.Filter(ctx, []*entity.Entity{draftPolicy(), publishedPolicy()})
	if len(kept) != 1 {
		t.Fatalf("Filter kept %d rows, want 1 (the published face only)", len(kept))
	}
	if kept[0].Face != published {
		t.Errorf("Filter kept face %q; the denied draft leaked", kept[0].Face)
	}

	heads := r.FilterHeaders(ctx, []store.EntityHeader{
		{ID: "POL-1", Type: "policy"},
		{ID: "POL-1", Type: "policy", Face: published},
	})
	if len(heads) != 1 {
		t.Fatalf("FilterHeaders kept %d rows, want 1", len(heads))
	}
	if heads[0].Face != published {
		t.Errorf("FilterHeaders kept face %q; the denied draft leaked", heads[0].Face)
	}
}

// TestVisibleTracer_IsFaceGated: the base tracer reads every node's DEFAULT
// face, so the row gate alone surfaced draft titles and properties to a
// principal granted only `ticket@published` — while PolicyReader.Get on the
// same entity correctly reported not-found. The decorator now applies the face
// gate to the default face per type.
func TestVisibleTracer_IsFaceGated(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	for _, e := range []*entity.Entity{
		{ID: "TKT-1", Type: "ticket", Properties: map[string]any{"title": "SECRET DRAFT"}},
		{ID: "TKT-2", Type: "ticket", Properties: map[string]any{"title": "SECRET DRAFT TWO"}},
	} {
		if err := st.CreateEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateRelation(ctx, "TKT-1", "blocks", "TKT-2", nil); err != nil {
		t.Fatal(err)
	}
	base := tracer.New(st)

	// Control: with every face permitted the trace carries the draft title,
	// so the absence below is the gate's doing and not an empty fixture.
	open, err := NewVisibleTracer(base, faceRowGate{}, NopRedactor{}, st)
	if err != nil {
		t.Fatal(err)
	}
	if res := open.TraceFrom(ctx, "TKT-1", 3); res == nil || res.Title != "SECRET DRAFT" {
		t.Fatalf("precondition: an unrestricted trace must show the draft; got %+v", res)
	}

	gated, err := NewVisibleTracer(base,
		faceRowGate{permitted: map[string][]entity.Face{"ticket": {"published"}}},
		NopRedactor{}, st)
	if err != nil {
		t.Fatal(err)
	}
	res := gated.TraceFrom(ctx, "TKT-1", 3)
	if res != nil {
		t.Errorf("a published-only principal must see nothing of a draft-only "+
			"trace; got root %q with %d children", res.Title, len(res.Children))
	}
}
