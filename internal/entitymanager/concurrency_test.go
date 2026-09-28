package entitymanager_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/autocascade"
	"github.com/Sourcehaven-BV/rela/internal/automation"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/storage"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/fsstore"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// These tests pin what the removed dataentry write mutex used to provide by
// accident (TKT-WE0S2K): each check-then-write in the manager is atomic on
// its own, whichever backend it runs on. They race goroutines through the
// public Manager API; run them with -race.
//
// concBackends is extended by the sqlite and postgres build-tagged files, so
// `just test-postgres` runs the same scenarios across processes' worth of
// connections.

const concurrencyMetamodelYAML = `version: "1.0"
entities:
  note:
    label: Note
    plural: notes
    id_prefix: "NOTE-"
    id_type: sequential
    properties:
      title:
        type: string
      p0: {type: string}
      p1: {type: string}
      p2: {type: string}
      p3: {type: string}
      p4: {type: string}
      p5: {type: string}
      p6: {type: string}
      p7: {type: string}
  person:
    label: Person
    plural: persons
    id_prefix: "PERS-"
    id_type: sequential
    properties:
      email:
        type: string
        unique: true
relations:
  links:
    label: links
    from: [note]
    to: [note]
  lists:
    label: lists
    from: [person]
    to: [note]
    orderable: outgoing
`

type concBackend struct {
	name string
	open func(t *testing.T) store.Store
}

var concBackends = []concBackend{
	{name: "memstore", open: func(*testing.T) store.Store { return memstore.New() }},
	{name: "fsstore", open: openConcFSStore},
}

func openConcFSStore(t *testing.T) store.Store {
	t.Helper()
	fs := storage.NewMemFS()
	rooted, err := storage.NewRootedFS(fs, "/")
	require.NoError(t, err)
	st, err := fsstore.New(fsstore.Config{
		FS: fs, Rooted: rooted,
		EntitiesKey: "entities", RelationsKey: "relations",
		AttachmentsKey: "attachments", CacheKey: ".rela",
		Schemas: map[string]store.EntityTypeSchema{
			"note":   {Plural: "notes"},
			"person": {Plural: "persons"},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func concurrencyManager(t *testing.T, st store.Store) *entitymanager.Manager {
	t.Helper()
	meta, err := metamodel.Parse([]byte(concurrencyMetamodelYAML))
	require.NoError(t, err)
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       st,
		Meta:        meta,
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
	})
	require.NoError(t, err)
	return mgr
}

// openConcBackend opens b's store and a manager over it.
func openConcBackend(t *testing.T, b concBackend) (store.Store, *entitymanager.Manager) {
	t.Helper()
	st := b.open(t)
	return st, concurrencyManager(t, st)
}

// race runs fn(i) for i in [0, n) concurrently and returns each call's error.
func race(n int, fn func(i int) error) []error {
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			errs[i] = fn(i)
		})
	}
	close(start)
	wg.Wait()
	return errs
}

func mustCreateNote(t *testing.T, mgr *entitymanager.Manager, title string) *entity.Entity {
	t.Helper()
	e := entity.New("", "note")
	e.SetString("title", title)
	res, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
	require.NoError(t, err)
	return res.Entity
}

func TestConcurrency_GeneratedIDsAreDistinct(t *testing.T) {
	for _, backend := range concBackends {
		t.Run(backend.name, func(t *testing.T) {
			_, mgr := openConcBackend(t, backend)
			const n = 16
			ids := make([]string, n)
			errs := race(n, func(i int) error {
				e := entity.New("", "note")
				e.SetString("title", fmt.Sprintf("note %d", i))
				res, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
				if err == nil {
					ids[i] = res.Entity.ID
				}
				return err
			})
			seen := map[string]bool{}
			for i, err := range errs {
				require.NoError(t, err, "create %d", i)
				assert.False(t, seen[ids[i]], "id %s minted twice", ids[i])
				seen[ids[i]] = true
			}
		})
	}
}

func TestConcurrency_UniqueValueAdmitsOneCreate(t *testing.T) {
	for _, backend := range concBackends {
		t.Run(backend.name, func(t *testing.T) {
			st, mgr := openConcBackend(t, backend)
			const n = 8
			errs := race(n, func(int) error {
				e := entity.New("", "person")
				e.SetString("email", "alice@example.com")
				_, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
				return err
			})
			ok := 0
			for _, err := range errs {
				if err == nil {
					ok++
					continue
				}
				assert.True(t, entitymanager.IsUniqueViolation(err), "want a unique violation, got %v", err)
			}
			assert.Equal(t, 1, ok, "exactly one create may claim the value")

			count := 0
			for _, err := range st.ListEntities(context.Background(), store.EntityQuery{Type: "person"}) {
				require.NoError(t, err)
				count++
			}
			assert.Equal(t, 1, count)
		})
	}
}

func TestConcurrency_DisjointPatchesAllLand(t *testing.T) {
	for _, backend := range concBackends {
		t.Run(backend.name, func(t *testing.T) {
			st, mgr := openConcBackend(t, backend)
			note := mustCreateNote(t, mgr, "target")
			const n = 8
			errs := race(n, func(i int) error {
				_, err := mgr.PatchEntity(context.Background(), note.ID, entity.Patch{
					Properties: map[string]any{fmt.Sprintf("p%d", i): "set"},
				})
				return err
			})
			for i, err := range errs {
				require.NoError(t, err, "patch %d", i)
			}
			got, err := st.GetEntity(context.Background(), note.ID)
			require.NoError(t, err)
			for i := range n {
				assert.Equal(t, "set", got.GetString(fmt.Sprintf("p%d", i)), "patch %d was lost", i)
			}
		})
	}
}

func TestConcurrency_RelationUpdatesToDisjointKeysAllLand(t *testing.T) {
	for _, backend := range concBackends {
		t.Run(backend.name, func(t *testing.T) {
			st, mgr := openConcBackend(t, backend)
			ctx := context.Background()
			a := mustCreateNote(t, mgr, "a")
			b := mustCreateNote(t, mgr, "b")
			_, err := mgr.CreateRelation(ctx, a.ID, "links", b.ID, entity.RelationOptions{})
			require.NoError(t, err)

			const n = 8
			errs := race(n, func(i int) error {
				_, updErr := mgr.UpdateRelation(ctx, a.ID, "links", b.ID, entity.RelationOptions{
					Properties: map[string]any{fmt.Sprintf("k%d", i): "v"},
				})
				return updErr
			})
			for i, err := range errs {
				require.NoError(t, err, "update %d", i)
			}
			rel, err := st.GetRelation(ctx, a.ID, "links", b.ID)
			require.NoError(t, err)
			for i := range n {
				assert.Equal(t, "v", rel.Properties[fmt.Sprintf("k%d", i)], "update %d was lost", i)
			}
		})
	}
}

func TestConcurrency_ManagedOrderIsDistinct(t *testing.T) {
	for _, backend := range concBackends {
		t.Run(backend.name, func(t *testing.T) {
			st, mgr := openConcBackend(t, backend)
			ctx := context.Background()
			p := entity.New("", "person")
			res, err := mgr.CreateEntity(ctx, p, entity.CreateOptions{})
			require.NoError(t, err)
			owner := res.Entity.ID

			const n = 8
			targets := make([]string, n)
			for i := range n {
				targets[i] = mustCreateNote(t, mgr, fmt.Sprintf("item %d", i)).ID
			}
			errs := race(n, func(i int) error {
				_, err := mgr.CreateRelation(ctx, owner, "lists", targets[i], entity.RelationOptions{})
				return err
			})
			for i, err := range errs {
				require.NoError(t, err, "create relation %d", i)
			}

			seen := map[float64]bool{}
			for r, err := range st.ListRelations(ctx, store.RelationQuery{From: owner, Type: "lists"}) {
				require.NoError(t, err)
				v, ok := entitymanager.FiniteOrder(r.Properties[metamodel.OrderPropertyOut])
				require.True(t, ok, "relation to %s has no order", r.To)
				assert.False(t, seen[v], "order %v assigned twice", v)
				seen[v] = true
			}
			assert.Len(t, seen, n)
		})
	}
}

// interleavingStore runs afterCreate once, right after the first successful
// CreateEntity, standing in for a concurrent writer that commits between a
// create and the manager's post-automation rewrite.
type interleavingStore struct {
	store.Store
	once        sync.Once
	afterCreate func(ctx context.Context, e *entity.Entity)
}

func (s *interleavingStore) CreateEntity(ctx context.Context, e *entity.Entity) error {
	if err := s.Store.CreateEntity(ctx, e); err != nil {
		return err
	}
	s.once.Do(func() { s.afterCreate(ctx, e) })
	return nil
}

// TestConcurrency_PostAutomationRewriteKeepsInterleavedWrite pins that the
// rewrite of an on-create automation's properties is a compare-and-swap over
// the stored row: a write landing between the create and the rewrite survives.
func TestConcurrency_PostAutomationRewriteKeepsInterleavedWrite(t *testing.T) {
	t.Parallel()
	inner := memstore.New()
	st := &interleavingStore{Store: inner}
	st.afterCreate = func(ctx context.Context, e *entity.Entity) {
		stored, err := inner.GetEntity(ctx, e.ID)
		require.NoError(t, err)
		next := stored.Clone()
		next.SetString("p0", "concurrent")
		require.NoError(t, inner.UpdateEntity(ctx, next))
	}

	meta, err := metamodel.Parse([]byte(concurrencyMetamodelYAML))
	require.NoError(t, err)
	engine := automation.NewEngine([]automation.Automation{{
		Name: "stamp-on-create",
		On:   automation.Trigger{Entity: []string{"note"}, Created: true},
		Do:   []automation.Action{{Set: "p7", Value: "automated"}},
	}})
	runner, err := autocascade.New(autocascade.Deps{Engine: engine})
	require.NoError(t, err)
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       st,
		Meta:        meta,
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Automations: engine,
		Cascade:     runner,
	})
	require.NoError(t, err)

	created := mustCreateNote(t, mgr, "raced")
	got, err := inner.GetEntity(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, "concurrent", got.GetString("p0"), "the interleaved write was overwritten")
	assert.Equal(t, "automated", got.GetString("p7"), "the automation's property was not written")
}
