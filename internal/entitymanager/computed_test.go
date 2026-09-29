package entitymanager_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/search"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

const computedMetaYAML = `
version: "1.0"
entities:
  item:
    label: Item
    plural: Items
    id_prefix: "I-"
    id_type: sequential
    properties:
      source:
        type: integer
      doubled:
        type: integer
        computed: entity.source * 2
      search_text:
        type: string
        computed: "'computed-marker'"
relations: {}
types: {}
`

func newComputedManager(t *testing.T, st *memstore.MemStore) *entitymanager.Manager {
	t.Helper()
	meta, err := metamodel.Parse([]byte(computedMetaYAML))
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store: st, Meta: meta, Templater: nopTemplater{}, Audit: audit.Nop{},
		ACL: acl.NopACL{}, Transitions: statemachine.EmptySet(),
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

func TestComputed_CreatePatchUpdateAndApply(t *testing.T) {
	ctx := context.Background()
	mgr := newComputedManager(t, memstore.New())
	e := entity.New("", "item")
	e.Properties["source"] = 3
	created, err := mgr.CreateEntity(ctx, e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if got := created.Entity.Properties["doubled"]; got != int64(6) {
		t.Fatalf("doubled = %v", got)
	}

	updated, err := mgr.PatchEntity(ctx, created.Entity.ID, entity.Patch{Properties: map[string]any{"source": 5}})
	if err != nil {
		t.Fatalf("PatchEntity: %v", err)
	}
	if got := updated.Entity.Properties["doubled"]; got != int64(10) {
		t.Fatalf("patched doubled = %v", got)
	}

	// A whole-record update may carry the unchanged materialized value, but a
	// caller-authored change is rejected.
	whole := updated.Entity.Clone()
	whole.Properties["doubled"] = int64(999)
	if _, updateErr := mgr.UpdateEntity(ctx, whole); !errors.As(updateErr, new(*entitymanager.ComputedWriteError)) {
		t.Fatalf("UpdateEntity computed error = %T %v", updateErr, updateErr)
	}

	// Apply is trusted replica input: stale incoming computed data is ignored
	// and recomputed under the receiving schema.
	apply := updated.Entity.Clone()
	apply.Properties["source"] = 7
	apply.Properties["doubled"] = int64(-1)
	res, err := mgr.ApplyEntity(ctx, apply)
	if err != nil {
		t.Fatalf("ApplyEntity: %v", err)
	}
	if got := res.Entity.Properties["doubled"]; got != int64(14) {
		t.Fatalf("applied doubled = %v", got)
	}
}

func TestComputed_RejectsAuthoredCreateAndPatch(t *testing.T) {
	ctx := context.Background()
	mgr := newComputedManager(t, memstore.New())
	e := entity.New("", "item")
	e.Properties["source"] = 1
	e.Properties["doubled"] = 2
	if _, err := mgr.CreateEntity(ctx, e, entity.CreateOptions{}); !errors.As(err, new(*entitymanager.ComputedWriteError)) {
		t.Fatalf("CreateEntity computed error = %T %v", err, err)
	}
	delete(e.Properties, "doubled")
	created, err := mgr.CreateEntity(ctx, e, entity.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.PatchEntity(ctx, created.Entity.ID, entity.Patch{Properties: map[string]any{"doubled": 4}}); !errors.As(err, new(*entitymanager.ComputedWriteError)) {
		t.Fatalf("PatchEntity computed error = %T %v", err, err)
	}
}

func TestComputed_MaterializedValueReachesSearchIndex(t *testing.T) {
	ctx := context.Background()
	idx := search.NewLinearSearch()
	mgr := newComputedManager(t, memstore.New(memstore.WithObserver(idx)))
	e := entity.New("", "item")
	e.Properties["source"] = 1
	created, err := mgr.CreateEntity(ctx, e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	// The default world: computed properties are indexed on the entity's own
	// face, so an unscoped search resolves it.
	faces, err := idx.Search("computed-marker", 0, store.WorldScope{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(faces) != 1 || faces[0].ID != created.Entity.ID {
		t.Fatalf("search hits = %v, want one for %s", faces, created.Entity.ID)
	}
}
