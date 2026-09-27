package twins_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/twins"
	"github.com/Sourcehaven-BV/rela/internal/twins/memtwins"
)

var (
	syncTime   = time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	remoteTime = time.Date(2026, 8, 31, 9, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	laterTime  = remoteTime.Add(time.Hour)
)

// notFoundError and rejectedError carry the structural markers the service
// classifies by, as entitymanager's errors do.
type notFoundError struct{ id string }

func (e notFoundError) Error() string        { return "entity not found: " + e.id }
func (e notFoundError) EntityNotFound() bool { return true }

type rejectedError struct{ msg string }

func (e rejectedError) Error() string       { return e.msg }
func (e rejectedError) WriteRejected() bool { return true }

// entities is an in-test entity store standing in for both the reader
// adapter and the entitymanager sync handle. PatchEntity honors
// ExpectedVersion the way the real stores do, so the service's compare-and-
// swap is exercised.
type entities struct {
	mu      sync.Mutex
	byID    map[string]*entity.Entity
	patches []entity.Patch
	// beforePatch runs before the CAS check, standing in for a concurrent
	// writer that lands between the service's read and its write.
	beforePatch func(e *entity.Entity)
	// afterPatch runs after the patch applies, standing in for an on-update
	// automation.
	afterPatch func(e *entity.Entity)
	// beforeGet runs once, before the next read and outside the lock,
	// standing in for a concurrent change that lands between the service's
	// read of a twin and its read of the entity.
	beforeGet func(id string)
	// reject, when set, refuses every write on its merits.
	reject error
}

func newEntities(es ...*entity.Entity) *entities {
	f := &entities{byID: map[string]*entity.Entity{}}
	for _, e := range es {
		f.byID[e.ID] = e
	}
	return f
}

func (f *entities) GetEntityVersion(_ context.Context, id string) (*entity.Entity, string, error) {
	if hook := f.beforeGet; hook != nil {
		f.beforeGet = nil
		hook(id)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.byID[id]
	if !ok {
		return nil, "", fmt.Errorf("get: %w", notFoundError{id})
	}
	return e.Clone(), string(store.VersionOf(e)), nil
}

func (f *entities) PatchEntity(_ context.Context, id string, p entity.Patch) (*entity.UpdateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patches = append(f.patches, p)
	e, ok := f.byID[id]
	if !ok {
		return nil, notFoundError{id}
	}
	if f.beforePatch != nil {
		f.beforePatch(e)
	}
	if actual := store.VersionOf(e); p.ExpectedVersion != "" && string(actual) != p.ExpectedVersion {
		return nil, fmt.Errorf("write entity: %w", &store.VersionConflictError{
			ID: id, Expected: store.EntityVersion(p.ExpectedVersion), Actual: actual,
		})
	}
	if f.reject != nil {
		return nil, fmt.Errorf("validate: %w", f.reject)
	}
	p.Apply(e)
	if f.afterPatch != nil {
		f.afterPatch(e)
	}
	return &entity.UpdateResult{Entity: e.Clone(), Version: string(store.VersionOf(e))}, nil
}

// set changes an entity as a rela user would.
func (f *entities) set(id, prop string, v any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[id].Properties[prop] = v
}

// setBody changes an entity's body as a rela user would.
func (f *entities) setBody(id, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[id].Content = body
}

func (f *entities) remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.byID, id)
}

// rename moves an entity to a new id, as a rela rename does.
func (f *entities) rename(oldID, newID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.byID[oldID]
	delete(f.byID, oldID)
	e.ID = newID
	f.byID[newID] = e
}

func (f *entities) version(ctx context.Context, t *testing.T, id string) string {
	t.Helper()
	_, v, err := f.GetEntityVersion(ctx, id)
	require.NoError(t, err)
	return v
}

func scenario(id string) *entity.Entity {
	return &entity.Entity{
		ID:   id,
		Type: "scenario",
		Properties: map[string]any{
			"title": "Login", "status": "open", "priority": "low", "estimate": 3, "age": 1,
		},
		Content: "Steps",
	}
}

// testMeta: scenario (with a computed age) has a basecamp pact (status
// theirs, priority shared, title proposed) and a github pact owning
// everything; ticket has none.
func testMeta() *metamodel.Metamodel {
	return &metamodel.Metamodel{Entities: map[string]metamodel.EntityDef{
		"scenario": {
			Properties: map[string]metamodel.PropertyDef{"age": {Computed: "1"}},
			Pacts: map[string]metamodel.PactDef{
				"basecamp": {
					Scope:   "https://app.basecamp.com/1/buckets/2/todolists/3",
					Theirs:  []string{"status"},
					Shared:  []string{"priority"},
					Propose: []string{"title"},
				},
				"github": {Scope: "https://github.com/acme/app", Theirs: []string{"*"}},
			},
		},
		"ticket": {},
	}}
}

type fixture struct {
	svc   *twins.Service
	store *memtwins.Store
	ents  *entities
	meta  *atomic.Pointer[metamodel.Metamodel]
}

func newFixture(t *testing.T, es ...*entity.Entity) fixture {
	t.Helper()
	if len(es) == 0 {
		es = []*entity.Entity{scenario("SC-1")}
	}
	st := memtwins.New()
	ents := newEntities(es...)
	meta := &atomic.Pointer[metamodel.Metamodel]{}
	meta.Store(testMeta())
	svc, err := twins.NewService(st, meta.Load, ents, ents, func() time.Time { return syncTime })
	require.NoError(t, err)
	return fixture{svc: svc, store: st, ents: ents, meta: meta}
}

func (fx fixture) link(ctx context.Context, t *testing.T, entityID, system, externalID string) twins.Twin {
	t.Helper()
	tw, err := fx.svc.Link(ctx, entityID, system, externalID, "https://x/"+externalID, remoteTime)
	require.NoError(t, err)
	return tw
}

// synced links and pushes a basecamp twin, leaving it in sync with rela's
// values as base.
func (fx fixture) synced(ctx context.Context, t *testing.T, entityID, externalID string) twins.Twin {
	t.Helper()
	fx.link(ctx, t, entityID, "basecamp", externalID)
	tw, err := fx.svc.Pushed(ctx, "basecamp", externalID, fx.ents.version(ctx, t, entityID), remoteTime)
	require.NoError(t, err)
	require.Equal(t, twins.StateInSync, tw.State)
	return tw
}

// get reads the basecamp twin of externalID.
func (fx fixture) get(ctx context.Context, t *testing.T, externalID string) twins.Twin {
	t.Helper()
	tw, err := fx.svc.Get(ctx, "basecamp", externalID)
	require.NoError(t, err)
	return tw
}

func TestNewService_RejectsNilDeps(t *testing.T) {
	st, ents := memtwins.New(), newEntities()
	cases := []struct {
		name   string
		store  twins.Store
		meta   func() *metamodel.Metamodel
		reader twins.EntityReader
		writer twins.SyncWriter
	}{
		{"store", nil, testMeta, ents, ents},
		{"metamodel source", st, nil, ents, ents},
		{"reader", st, testMeta, nil, ents},
		{"writer", st, testMeta, ents, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := twins.NewService(tc.store, tc.meta, tc.reader, tc.writer, nil)
			require.Error(t, err)
		})
	}

	svc, err := twins.NewService(st, testMeta, ents, ents, nil)
	require.NoError(t, err, "a nil clock falls back to time.Now")
	require.NotNil(t, svc)
}

func TestLink(t *testing.T) {
	ctx := context.Background()

	t.Run("records a pending twin with no base", func(t *testing.T) {
		fx := newFixture(t)
		tw, err := fx.svc.Link(ctx, "SC-1", "basecamp", "42", "https://x/42", remoteTime)
		require.NoError(t, err)

		require.Equal(t, twins.Target{Type: "scenario", ID: "SC-1"}, tw.Target)
		require.Equal(t, twins.StatePending, tw.State)
		require.Equal(t, "https://x/42", tw.URL)
		require.False(t, tw.HasBase, "a link agrees on nothing")
		require.Empty(t, tw.Base.Properties)
		require.True(t, tw.SyncedAt.IsZero(), "a link is not a sync")
		require.True(t, remoteTime.Equal(tw.RemoteUpdatedAt))
		require.Equal(t, tw.Target, fx.get(ctx, t, "42").Target)
	})

	t.Run("refusals", func(t *testing.T) {
		locked := scenario("SC-L")
		locked.Inaccessible = []entity.InaccessibleField{{Name: entity.InaccessibleFieldContent}}
		fx := newFixture(t, scenario("SC-1"), scenario("SC-2"), locked, &entity.Entity{ID: "TKT-1", Type: "ticket"})
		fx.link(ctx, t, "SC-1", "basecamp", "42")

		cases := []struct {
			name                         string
			entityID, system, externalID string
			want                         error
		}{
			{"type without pacts", "TKT-1", "basecamp", "1", twins.ErrNoPact},
			{"system without a pact", "SC-2", "jira", "1", twins.ErrNoPact},
			{"unsafe external id", "SC-2", "basecamp", "../1", twins.ErrInvalidExternalID},
			{"locked entity", "SC-L", "basecamp", "1", twins.ErrEntityLocked},
			{"external item already linked", "SC-2", "basecamp", "42", twins.ErrExternalIDTaken},
			{"second live twin of the entity in the system", "SC-1", "basecamp", "43", twins.ErrDuplicateTarget},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := fx.svc.Link(ctx, tc.entityID, tc.system, tc.externalID, "", time.Time{})
				require.ErrorIs(t, err, tc.want)
			})
		}
		_, err := fx.svc.Link(ctx, "SC-404", "basecamp", "1", "", time.Time{})
		require.Error(t, err, "a missing entity cannot be linked")

		all, err := fx.svc.List(ctx, twins.Filter{})
		require.NoError(t, err)
		require.Len(t, all, 1, "no refusal stores anything")
	})

	t.Run("a gone twin does not block linking the entity again", func(t *testing.T) {
		fx := newFixture(t)
		fx.link(ctx, t, "SC-1", "basecamp", "42")
		_, err := fx.svc.MarkGone(ctx, "basecamp", "42")
		require.NoError(t, err)

		fx.link(ctx, t, "SC-1", "basecamp", "43")
	})
}

func TestUnlink(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	fx.link(ctx, t, "SC-1", "basecamp", "42")

	require.NoError(t, fx.svc.Unlink(ctx, "basecamp", "42"))
	require.ErrorIs(t, fx.svc.Unlink(ctx, "basecamp", "42"), twins.ErrNotFound)
	_, err := fx.svc.Get(ctx, "basecamp", "42")
	require.ErrorIs(t, err, twins.ErrNotFound)
}

func TestPull(t *testing.T) {
	ctx := context.Background()

	t.Run("a first pull takes theirs and keeps rela's owned values to push", func(t *testing.T) {
		fx := newFixture(t)
		fx.link(ctx, t, "SC-1", "basecamp", "42")

		res, err := fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{
			Properties: map[string]any{"status": "done", "title": "Renamed there", "estimate": 3},
		}, remoteTime, false)
		require.NoError(t, err)

		require.Equal(t, []string{"status"}, res.Applied)
		tw := res.Twin
		require.True(t, tw.HasBase)
		require.Empty(t, tw.Findings, "without a base a differing ours value is a push, not a foreign edit")
		require.Equal(t, twins.StatePending, tw.State)
		require.Equal(t, map[string]any{"status": "done", "title": "Renamed there", "estimate": 3}, tw.Base.Properties)
	})

	t.Run("writes a theirs change with a compare-and-swap and records the sync", func(t *testing.T) {
		fx := newFixture(t)
		fx.synced(ctx, t, "SC-1", "42")
		readVersion := fx.ents.version(ctx, t, "SC-1")

		res, err := fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{
			Properties: map[string]any{"status": "done", "title": "Login"},
		}, laterTime, false)
		require.NoError(t, err)

		require.Equal(t, []entity.Patch{{
			Properties: map[string]any{"status": "done"}, ExpectedVersion: readVersion,
		}}, fx.ents.patches)
		require.Equal(t, []string{"status"}, res.Applied)

		tw := res.Twin
		require.Equal(t, twins.StateInSync, tw.State)
		require.Equal(t, "done", tw.Base.Properties["status"])
		require.Equal(t, fx.ents.version(ctx, t, "SC-1"), tw.BaseVersion, "the write's own post-write version")
		require.Equal(t, syncTime, tw.SyncedAt)
		require.True(t, laterTime.Equal(tw.RemoteUpdatedAt))
		require.Equal(t, tw.BaseVersion, fx.get(ctx, t, "42").BaseVersion)
	})

	t.Run("an identical second pull writes nothing and stays in sync", func(t *testing.T) {
		fx := newFixture(t)
		fx.synced(ctx, t, "SC-1", "42")
		remote := twins.Remote{Properties: map[string]any{"status": "done", "priority": "low"}, Body: new("Steps")}
		_, err := fx.svc.Pull(ctx, "basecamp", "42", remote, laterTime, false)
		require.NoError(t, err)
		written := len(fx.ents.patches)

		res, err := fx.svc.Pull(ctx, "basecamp", "42", remote, laterTime, false)
		require.NoError(t, err)

		require.Len(t, fx.ents.patches, written, "no second write, so no second audit row")
		require.Empty(t, res.Applied)
		require.Equal(t, twins.StateInSync, res.Twin.State)
		require.Equal(t, fx.ents.version(ctx, t, "SC-1"), res.Twin.BaseVersion, "the read version stands")
	})

	t.Run("a concurrent edit fails the pull and leaves the twin untouched", func(t *testing.T) {
		fx := newFixture(t)
		before := fx.synced(ctx, t, "SC-1", "42")
		fx.ents.beforePatch = func(e *entity.Entity) { e.Properties["title"] = "Edited meanwhile" }

		_, err := fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{
			Properties: map[string]any{"status": "done"},
		}, laterTime, false)
		require.ErrorIs(t, err, store.ErrConflict)
		var conflict *store.VersionConflictError
		require.ErrorAs(t, err, &conflict, "the typed conflict must survive for a retry loop")

		require.Equal(t, before, fx.get(ctx, t, "42"))
	})

	t.Run("a rejected write is recorded, not returned, and the base stays", func(t *testing.T) {
		fx := newFixture(t)
		before := fx.synced(ctx, t, "SC-1", "42")
		fx.ents.reject = rejectedError{"status: \"shipped\" is not an allowed value"}

		res, err := fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{
			Properties: map[string]any{"status": "shipped"},
		}, laterTime, false)
		require.NoError(t, err)

		tw := res.Twin
		require.Equal(t, twins.StatePending, tw.State)
		require.Equal(t, []twins.Finding{{
			Kind: twins.FindingRejected, Message: "validate: status: \"shipped\" is not an allowed value",
		}}, tw.Findings)
		require.Equal(t, before.Base, tw.Base, "the base must not advance past a write that did not happen")
		require.Equal(t, before.BaseVersion, tw.BaseVersion)
		require.Equal(t, before.SyncedAt, tw.SyncedAt)
		require.Empty(t, res.Applied)
		require.Equal(t, tw, fx.get(ctx, t, "42"), "the rejection is persisted")
	})

	t.Run("a write that moves an ours field leaves the twin pending", func(t *testing.T) {
		fx := newFixture(t)
		fx.synced(ctx, t, "SC-1", "42")
		// An on-update automation derives the estimate from the new status.
		fx.ents.afterPatch = func(e *entity.Entity) { e.Properties["estimate"] = 0 }

		res, err := fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{
			Properties: map[string]any{"status": "done"},
		}, laterTime, false)
		require.NoError(t, err)

		require.Equal(t, twins.StatePending, res.Twin.State, "rela now holds an estimate to push")
		require.Equal(t, fx.ents.version(ctx, t, "SC-1"), res.Twin.BaseVersion)
	})

	t.Run("a foreign edit is reported, not written", func(t *testing.T) {
		fx := newFixture(t)
		fx.synced(ctx, t, "SC-1", "42")

		res, err := fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{
			Properties: map[string]any{"title": "Renamed there"},
		}, laterTime, false)
		require.NoError(t, err)

		require.Empty(t, fx.ents.patches)
		require.Equal(t, twins.StatePending, res.Twin.State)
		require.Equal(t, []twins.Finding{{
			Field: "title", Kind: twins.FindingForeignEdit, Base: "Login", Ours: "Login", Theirs: "Renamed there",
			Propose: true,
		}}, res.Twin.Findings)
	})

	t.Run("stale pulls", func(t *testing.T) {
		fx := newFixture(t)
		fx.synced(ctx, t, "SC-1", "42")
		remote := twins.Remote{Properties: map[string]any{"status": "done"}}
		_, err := fx.svc.Pull(ctx, "basecamp", "42", remote, laterTime, false)
		require.NoError(t, err)

		_, err = fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{Properties: map[string]any{"status": "open"}},
			remoteTime, false)
		require.ErrorIs(t, err, twins.ErrStalePull)
		require.Equal(t, "done", fx.get(ctx, t, "42").Base.Properties["status"])

		res, err := fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{Properties: map[string]any{"status": "open"}},
			remoteTime, true)
		require.NoError(t, err, "force applies an older state on purpose")
		require.Equal(t, "open", res.Twin.Base.Properties["status"])

		_, err = fx.svc.Pull(ctx, "basecamp", "42", remote, time.Time{}, true)
		require.Error(t, err, "the remote's updated-at time is required")
	})

	t.Run("a deleted entity marks the twin gone", func(t *testing.T) {
		fx := newFixture(t)
		fx.synced(ctx, t, "SC-1", "42")
		fx.ents.remove("SC-1")

		res, err := fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{}, laterTime, false)
		require.NoError(t, err)
		require.Equal(t, twins.StateGone, res.Twin.State)
		require.Equal(t, twins.StateGone, fx.get(ctx, t, "42").State)
	})

	t.Run("refusals", func(t *testing.T) {
		fx := newFixture(t)
		fx.link(ctx, t, "SC-1", "basecamp", "42")

		_, err := fx.svc.Pull(ctx, "basecamp", "43", twins.Remote{}, laterTime, false)
		require.ErrorIs(t, err, twins.ErrNotFound)

		_, err = fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{
			Properties: map[string]any{"body": "sent as a property"},
		}, laterTime, false)
		require.Error(t, err, "the body travels as Remote.Body, never as a property")

		fx.meta.Store(&metamodel.Metamodel{Entities: map[string]metamodel.EntityDef{"scenario": {}}})
		_, err = fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{}, laterTime, false)
		require.ErrorIs(t, err, twins.ErrNoPact, "the pact is read from the live metamodel")
		require.Empty(t, fx.ents.patches)
	})
}

func TestPushed(t *testing.T) {
	ctx := context.Background()

	t.Run("a never-synced twin takes rela's values as its base", func(t *testing.T) {
		fx := newFixture(t)
		fx.link(ctx, t, "SC-1", "basecamp", "42")
		version := fx.ents.version(ctx, t, "SC-1")

		tw, err := fx.svc.Pushed(ctx, "basecamp", "42", version, laterTime)
		require.NoError(t, err)

		require.True(t, tw.HasBase)
		require.Equal(t, scenario("SC-1").Properties, tw.Base.Properties,
			"theirs fields are seeded too: rela's value is the only agreement on record")
		require.Equal(t, "Steps", tw.Base.Body)
		require.Equal(t, version, tw.BaseVersion)
		require.Equal(t, twins.StateInSync, tw.State)
		require.Equal(t, syncTime, tw.SyncedAt)
		require.True(t, laterTime.Equal(tw.RemoteUpdatedAt))
	})

	t.Run("a stale version is refused and changes nothing", func(t *testing.T) {
		fx := newFixture(t)
		before := fx.link(ctx, t, "SC-1", "basecamp", "42")
		version := fx.ents.version(ctx, t, "SC-1")
		fx.ents.set("SC-1", "title", "Edited after the agent read it")

		_, err := fx.svc.Pushed(ctx, "basecamp", "42", version, laterTime)
		require.ErrorIs(t, err, twins.ErrVersionConflict)
		require.Equal(t, before, fx.get(ctx, t, "42"))
	})

	t.Run("ours and shared take the pushed values, theirs keeps its base", func(t *testing.T) {
		fx := newFixture(t)
		fx.synced(ctx, t, "SC-1", "42")
		_, err := fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{
			Properties: map[string]any{"title": "Renamed there"},
		}, laterTime, false)
		require.NoError(t, err)

		fx.ents.set("SC-1", "title", "Login v2")
		fx.ents.set("SC-1", "priority", "high")
		fx.ents.set("SC-1", "status", "drifted") // a privileged write to a theirs field
		fx.ents.mu.Lock()
		delete(fx.ents.byID["SC-1"].Properties, "estimate")
		fx.ents.mu.Unlock()

		tw, err := fx.svc.Pushed(ctx, "basecamp", "42", fx.ents.version(ctx, t, "SC-1"), time.Time{})
		require.NoError(t, err)

		require.Equal(t, "Login v2", tw.Base.Properties["title"])
		require.Equal(t, "high", tw.Base.Properties["priority"])
		require.NotContains(t, tw.Base.Properties, "estimate", "a removed ours property leaves the base")
		require.Equal(t, "open", tw.Base.Properties["status"], "a push never writes a theirs field")
		require.Empty(t, tw.Findings, "the push settles the foreign edit")
		require.Equal(t, twins.StateInSync, tw.State)
		require.True(t, laterTime.Equal(tw.RemoteUpdatedAt), "a zero time keeps the stored one")
	})

	t.Run("conflicts survive a push", func(t *testing.T) {
		fx := newFixture(t)
		fx.synced(ctx, t, "SC-1", "42")
		fx.ents.set("SC-1", "priority", "high")
		pulled, err := fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{
			Properties: map[string]any{"priority": "urgent", "title": "Renamed there"},
		}, laterTime, false)
		require.NoError(t, err)
		require.Equal(t, twins.StateConflict, pulled.Twin.State)

		tw, err := fx.svc.Pushed(ctx, "basecamp", "42", fx.ents.version(ctx, t, "SC-1"), laterTime)
		require.NoError(t, err)

		require.Equal(t, twins.StateConflict, tw.State)
		require.Len(t, tw.Findings, 1)
		require.Equal(t, twins.FindingConflict, tw.Findings[0].Kind)
		require.Equal(t, "low", tw.Base.Properties["priority"], "a conflicting shared field keeps its base")
		require.Equal(t, "Login", tw.Base.Properties["title"])
	})

	t.Run("a deleted entity marks the twin gone", func(t *testing.T) {
		fx := newFixture(t)
		fx.link(ctx, t, "SC-1", "basecamp", "42")
		version := fx.ents.version(ctx, t, "SC-1")
		fx.ents.remove("SC-1")

		tw, err := fx.svc.Pushed(ctx, "basecamp", "42", version, laterTime)
		require.NoError(t, err)
		require.Equal(t, twins.StateGone, tw.State)
	})
}

func pendingByID(t *testing.T, items []twins.PendingItem) map[string]twins.PendingItem {
	t.Helper()
	out := map[string]twins.PendingItem{}
	for _, it := range items {
		out[it.Twin.ExternalID] = it
	}
	return out
}

func TestPending(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t, scenario("SC-1"), scenario("SC-2"), scenario("SC-3"), scenario("SC-4"), scenario("SC-5"))

	fx.link(ctx, t, "SC-1", "basecamp", "1") // never synced
	for _, id := range []string{"2", "3", "4", "5"} {
		fx.synced(ctx, t, "SC-"+id, id)
	}
	fx.ents.set("SC-2", "title", "Changed in rela")
	_, err := fx.svc.MarkGone(ctx, "basecamp", "3")
	require.NoError(t, err)
	fx.ents.remove("SC-4")
	// SC-5 is in sync and unchanged.
	fx.link(ctx, t, "SC-5", "github", "5") // another system

	items, err := fx.svc.Pending(ctx, "basecamp")
	require.NoError(t, err)
	got := pendingByID(t, items)
	require.Len(t, got, 4, "the in-sync, unchanged twin is not listed")

	never := got["1"]
	require.Equal(t, []string{"never synced"}, never.Reasons)
	require.Equal(t, fx.ents.version(ctx, t, "SC-1"), never.Version)
	require.Equal(t, []twins.FieldValue{
		{Field: "estimate", Local: 3},
		{Field: "priority", Local: "low"},
		{Field: "title", Local: "Login"},
		{Field: "body", Local: "Steps"},
	}, never.PushSet, "every ours and shared field; never theirs (status) or computed (age)")

	changed := got["2"]
	require.True(t, changed.LocalChanged)
	require.Equal(t, []string{"changed in rela"}, changed.Reasons)
	require.Equal(t, []twins.FieldValue{{Field: "title", Base: "Login", Local: "Changed in rela"}}, changed.PushSet)
	require.Equal(t, fx.ents.version(ctx, t, "SC-2"), changed.Version)

	require.Equal(t, []string{"gone"}, got["3"].Reasons)
	require.Equal(t, []string{"gone"}, got["4"].Reasons, "a missing entity makes its twin gone")
	require.Equal(t, twins.StateGone, fx.get(ctx, t, "4").State, "and that is persisted")

	all, err := fx.svc.Pending(ctx, "")
	require.NoError(t, err)
	require.Len(t, all, 5, "an empty system lists every system")
}

func TestPending_FindingReasons(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	fx.synced(ctx, t, "SC-1", "42")
	fx.ents.set("SC-1", "priority", "high")
	fx.ents.set("SC-1", "estimate", 8)

	_, err := fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{
		Properties: map[string]any{"priority": "urgent", "title": "Renamed there", "estimate": 5},
	}, laterTime, false)
	require.NoError(t, err)

	items, err := fx.svc.Pending(ctx, "basecamp")
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, []string{
		"changed externally: estimate", "conflict: priority", "proposed externally: title",
	}, items[0].Reasons)
	require.Equal(t, []twins.FieldValue{{Field: "estimate", Base: 3, Local: 8}}, items[0].PushSet,
		"a shared field in conflict waits for a human")
}

func TestPending_UnpushedAfterSync(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	fx.synced(ctx, t, "SC-1", "42")
	fx.ents.set("SC-1", "estimate", 8)

	res, err := fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{Properties: map[string]any{"estimate": 3}},
		laterTime, false)
	require.NoError(t, err)
	require.Equal(t, twins.StatePending, res.Twin.State)

	items, err := fx.svc.Pending(ctx, "basecamp")
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.False(t, items[0].LocalChanged, "nothing changed since the pull")
	require.Equal(t, []string{"unpushed changes in rela"}, items[0].Reasons)
	require.Equal(t, []twins.FieldValue{{Field: "estimate", Base: 3, Local: 8}}, items[0].PushSet)
}

// failingStore fails every read, proving an answer came without store I/O.
type failingStore struct{ twins.Store }

func (failingStore) ForTarget(context.Context, string) ([]twins.Twin, error) {
	return nil, errors.New("store touched")
}

func TestOwnedFields(t *testing.T) {
	ctx := context.Background()

	t.Run("nil without twins", func(t *testing.T) {
		fx := newFixture(t)
		owned, err := fx.svc.OwnedFields(ctx, "scenario", "SC-1")
		require.NoError(t, err)
		require.Nil(t, owned)
	})

	t.Run("a type without pacts answers without touching the store", func(t *testing.T) {
		ents := newEntities()
		svc, err := twins.NewService(failingStore{memtwins.New()}, testMeta, ents, ents, nil)
		require.NoError(t, err)

		owned, err := svc.OwnedFields(ctx, "ticket", "TKT-1")
		require.NoError(t, err)
		require.Nil(t, owned)

		_, err = svc.OwnedFields(ctx, "scenario", "SC-1")
		require.Error(t, err, "a type with pacts does read, and a failed read fails closed")
	})

	t.Run("theirs fields per system, * for a pact owning everything", func(t *testing.T) {
		fx := newFixture(t)
		fx.link(ctx, t, "SC-1", "github", "7")
		fx.link(ctx, t, "SC-1", "basecamp", "42")

		owned, err := fx.svc.OwnedFields(ctx, "scenario", "SC-1")
		require.NoError(t, err)
		require.Equal(t, map[string][]string{
			"status":                {"basecamp"},
			metamodel.PactAllFields: {"github"},
		}, owned)
	})

	t.Run("a computed property is never owned", func(t *testing.T) {
		fx := newFixture(t)
		m := testMeta()
		bc := m.Entities["scenario"].Pacts["basecamp"]
		bc.Theirs = []string{"age", "status"}
		m.Entities["scenario"].Pacts["basecamp"] = bc
		fx.meta.Store(m)
		fx.link(ctx, t, "SC-1", "basecamp", "42")

		owned, err := fx.svc.OwnedFields(ctx, "scenario", "SC-1")
		require.NoError(t, err)
		require.Equal(t, map[string][]string{"status": {"basecamp"}}, owned)
	})

	t.Run("a gone twin owns nothing", func(t *testing.T) {
		fx := newFixture(t)
		fx.link(ctx, t, "SC-1", "basecamp", "42")
		gone, err := fx.svc.MarkGone(ctx, "basecamp", "42")
		require.NoError(t, err)
		require.Equal(t, twins.StateGone, gone.State)

		owned, err := fx.svc.OwnedFields(ctx, "scenario", "SC-1")
		require.NoError(t, err)
		require.Nil(t, owned)
	})

	t.Run("pacts follow the live metamodel", func(t *testing.T) {
		fx := newFixture(t)
		fx.link(ctx, t, "SC-1", "basecamp", "42")
		fx.meta.Store(&metamodel.Metamodel{Entities: map[string]metamodel.EntityDef{"scenario": {}}})

		owned, err := fx.svc.OwnedFields(ctx, "scenario", "SC-1")
		require.NoError(t, err)
		require.Nil(t, owned, "a pact removed from the schema owns nothing")
	})
}

func TestLifecycle(t *testing.T) {
	ctx := context.Background()

	t.Run("EntityRenamed moves the twins and their ownership", func(t *testing.T) {
		fx := newFixture(t, scenario("SC-1"), scenario("SC-9"))
		fx.link(ctx, t, "SC-1", "basecamp", "42")

		require.NoError(t, fx.svc.EntityRenamed(ctx, "SC-1", "SC-9"))

		got, err := fx.svc.ForEntity(ctx, "SC-9")
		require.NoError(t, err)
		require.Len(t, got, 1)
		owned, err := fx.svc.OwnedFields(ctx, "scenario", "SC-9")
		require.NoError(t, err)
		require.Equal(t, map[string][]string{"status": {"basecamp"}}, owned)
	})

	t.Run("EntityRenamed surfaces a collision", func(t *testing.T) {
		fx := newFixture(t, scenario("SC-1"), scenario("SC-2"))
		fx.link(ctx, t, "SC-1", "basecamp", "1")
		fx.link(ctx, t, "SC-2", "basecamp", "2")

		require.ErrorIs(t, fx.svc.EntityRenamed(ctx, "SC-1", "SC-2"), twins.ErrDuplicateTarget)
	})

	t.Run("EntityDeleted marks every twin gone and keeps it", func(t *testing.T) {
		fx := newFixture(t)
		fx.link(ctx, t, "SC-1", "basecamp", "42")
		fx.link(ctx, t, "SC-1", "github", "7")

		require.NoError(t, fx.svc.EntityDeleted(ctx, "SC-1"))

		got, err := fx.store.ForTarget(ctx, "SC-1")
		require.NoError(t, err)
		require.Len(t, got, 2)
		for _, tw := range got {
			require.Equal(t, twins.StateGone, tw.State)
		}
	})

	t.Run("ForEntity marks the twins of a missing entity gone", func(t *testing.T) {
		fx := newFixture(t)
		fx.link(ctx, t, "SC-1", "basecamp", "42")
		fx.ents.remove("SC-1")

		got, err := fx.svc.ForEntity(ctx, "SC-1")
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, twins.StateGone, got[0].State)
		require.Equal(t, twins.StateGone, fx.get(ctx, t, "42").State)
	})
}

// pushSetOf42 returns the push set pending lists for basecamp/42, and the
// version it was read at.
func (fx fixture) pushSetOf42(ctx context.Context, t *testing.T) (push []twins.FieldValue, version string) {
	t.Helper()
	items, err := fx.svc.Pending(ctx, "basecamp")
	require.NoError(t, err)
	item, ok := pendingByID(t, items)["42"]
	require.True(t, ok, "basecamp/42 must be pending")
	return item.PushSet, item.Version
}

// TestSync_GoneTwinRefused pins that pull and pushed refuse a gone twin
// before anything is read or written. Syncing would revive it — and here the
// entity already has a new live twin in the system, so a pull that got as far
// as writing would change the entity and only then fail.
func TestSync_GoneTwinRefused(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	fx.synced(ctx, t, "SC-1", "42")
	gone, err := fx.svc.MarkGone(ctx, "basecamp", "42")
	require.NoError(t, err)
	fx.link(ctx, t, "SC-1", "basecamp", "43")
	version := fx.ents.version(ctx, t, "SC-1")

	_, err = fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{
		Properties: map[string]any{"status": "done"},
	}, laterTime, false)
	require.ErrorIs(t, err, twins.ErrGone)
	_, err = fx.svc.Pushed(ctx, "basecamp", "42", version, laterTime)
	require.ErrorIs(t, err, twins.ErrGone)

	require.Empty(t, fx.ents.patches, "the entity is not written")
	require.Equal(t, gone, fx.get(ctx, t, "42"), "the gone twin stays as it was")
}

// TestPushed_KeepsTheirsFindings pins that a push settles only what it
// pushed. A theirs value rela refused, or a theirs field that drifted in
// rela, is no more settled after a push of ours fields than before it: the
// twin stays pending and keeps the finding.
func TestPushed_KeepsTheirsFindings(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		arise  func(fx fixture) // what makes the pull record the finding
		remote map[string]any
		kind   twins.FindingKind
	}{
		{
			name:   "a rejected pull",
			arise:  func(fx fixture) { fx.ents.reject = rejectedError{"status: \"shipped\" is not an allowed value"} },
			remote: map[string]any{"status": "shipped"},
			kind:   twins.FindingRejected,
		},
		{
			name:   "local drift",
			arise:  func(fx fixture) { fx.ents.set("SC-1", "status", "drifted") },
			remote: map[string]any{"status": "open"},
			kind:   twins.FindingLocalDrift,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t)
			fx.synced(ctx, t, "SC-1", "42")
			tc.arise(fx)
			_, err := fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{Properties: tc.remote}, laterTime, false)
			require.NoError(t, err)
			fx.ents.reject = nil
			fx.ents.set("SC-1", "title", "Login v2") // an ours change to push

			tw, err := fx.svc.Pushed(ctx, "basecamp", "42", fx.ents.version(ctx, t, "SC-1"), time.Time{})
			require.NoError(t, err)

			require.Equal(t, "Login v2", tw.Base.Properties["title"], "the pushed field settles")
			require.Equal(t, twins.StatePending, tw.State)
			require.Len(t, tw.Findings, 1)
			require.Equal(t, tc.kind, tw.Findings[0].Kind)
			require.Equal(t, tw, fx.get(ctx, t, "42"))
			fx.pushSetOf42(ctx, t) // still on the work list
		})
	}
}

// TestConflictDecidedInRela walks the way out of a conflict from the rela
// side: a person sets the shared field in rela, it joins the push set, and
// the confirmed push settles the conflict — even when the agent pulls again
// first with the external side unchanged.
func TestConflictDecidedInRela(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		field  string
		set    func(fx fixture, v string)
		remote func(v string) twins.Remote
		base   func(tw twins.Twin) any
		agreed any // the base before the conflict
	}{
		{
			field:  "priority",
			set:    func(fx fixture, v string) { fx.ents.set("SC-1", "priority", v) },
			remote: func(v string) twins.Remote { return twins.Remote{Properties: map[string]any{"priority": v}} },
			base:   func(tw twins.Twin) any { return tw.Base.Properties["priority"] },
			agreed: "low",
		},
		{
			field:  metamodel.PactBodyField,
			set:    func(fx fixture, v string) { fx.ents.setBody("SC-1", v) },
			remote: func(v string) twins.Remote { return twins.Remote{Body: &v} },
			base:   func(tw twins.Twin) any { return tw.Base.Body },
			agreed: "Steps",
		},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			fx := newFixture(t)
			m := testMeta()
			bc := m.Entities["scenario"].Pacts["basecamp"]
			bc.Shared = []string{metamodel.PactBodyField, "priority"}
			m.Entities["scenario"].Pacts["basecamp"] = bc
			fx.meta.Store(m)
			fx.synced(ctx, t, "SC-1", "42")
			tc.set(fx, "high")
			pulled, err := fx.svc.Pull(ctx, "basecamp", "42", tc.remote("urgent"), laterTime, false)
			require.NoError(t, err)
			require.Equal(t, twins.StateConflict, pulled.Twin.State)
			push, _ := fx.pushSetOf42(ctx, t)
			require.Empty(t, push, "an open conflict waits for a human")

			tc.set(fx, "medium") // the human decides, in rela
			want := []twins.FieldValue{{Field: tc.field, Base: tc.agreed, Local: "medium"}}
			push, _ = fx.pushSetOf42(ctx, t)
			require.Equal(t, want, push)

			repulled, err := fx.svc.Pull(ctx, "basecamp", "42", tc.remote("urgent"), laterTime, false)
			require.NoError(t, err)
			require.Empty(t, fx.ents.patches, "the decided value is not overwritten")
			require.Equal(t, twins.StateConflict, repulled.Twin.State)
			require.Equal(t, pulled.Twin.Findings, repulled.Twin.Findings, "the conflict is carried as it was recorded")
			push, version := fx.pushSetOf42(ctx, t)
			require.Equal(t, want, push, "the decision survives a pull")

			tw, err := fx.svc.Pushed(ctx, "basecamp", "42", version, laterTime)
			require.NoError(t, err)

			require.Equal(t, twins.StateInSync, tw.State)
			require.Empty(t, tw.Findings, "the push settled the conflict")
			require.Equal(t, "medium", tc.base(tw))

			res, err := fx.svc.Pull(ctx, "basecamp", "42", tc.remote("medium"), laterTime, false)
			require.NoError(t, err)
			require.Equal(t, twins.StateInSync, res.Twin.State, "the external side now agrees")
		})
	}
}

// TestPull_ConflictStandsOnlyWhileTheRemoteHoldsStill pins the edges of
// carrying a conflict over: it is re-judged once the external side moves
// again, and settled when rela takes the external value.
func TestPull_ConflictStandsOnlyWhileTheRemoteHoldsStill(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name          string
		local, remote string
		wantState     twins.State
		wantFindings  []twins.Finding
		wantBase      string
	}{
		{
			name: "the external side moved again", local: "medium", remote: "critical",
			wantState: twins.StateConflict,
			wantFindings: []twins.Finding{{
				Field: "priority", Kind: twins.FindingConflict, Base: "low", Ours: "medium", Theirs: "critical",
			}},
			wantBase: "low",
		},
		{
			name: "rela took the external value", local: "urgent", remote: "urgent",
			wantState: twins.StateInSync, wantBase: "urgent",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t)
			fx.synced(ctx, t, "SC-1", "42")
			fx.ents.set("SC-1", "priority", "high")
			_, err := fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{
				Properties: map[string]any{"priority": "urgent"},
			}, laterTime, false)
			require.NoError(t, err)
			fx.ents.set("SC-1", "priority", tc.local)

			res, err := fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{
				Properties: map[string]any{"priority": tc.remote},
			}, laterTime, false)
			require.NoError(t, err)

			require.Equal(t, tc.wantState, res.Twin.State)
			require.Equal(t, tc.wantFindings, res.Twin.Findings)
			require.Equal(t, tc.wantBase, res.Twin.Base.Properties["priority"])
		})
	}
}

// TestRenameDuringSync pins that a rename landing after the service read a
// twin — the CLI next to a server renaming the entity — is not undone by the
// bookkeeping that follows, and does not make the twin gone: the old id is
// missing because the entity moved, not because it was deleted.
func TestRenameDuringSync(t *testing.T) {
	ctx := context.Background()
	// setup returns a synced twin with a change to push, whose entity is
	// renamed by the next entity read, and the entity version before that.
	setup := func(t *testing.T) (fixture, string) {
		t.Helper()
		fx := newFixture(t)
		fx.synced(ctx, t, "SC-1", "42")
		fx.ents.set("SC-1", "title", "Changed in rela")
		version := fx.ents.version(ctx, t, "SC-1")
		fx.ents.beforeGet = func(string) {
			fx.ents.rename("SC-1", "SC-9")
			require.NoError(t, fx.svc.EntityRenamed(ctx, "SC-1", "SC-9"))
		}
		return fx, version
	}
	requireFollowedRename := func(t *testing.T, fx fixture) {
		t.Helper()
		stored := fx.get(ctx, t, "42")
		require.Equal(t, "SC-9", stored.Target.ID, "the rename is not reverted")
		require.Equal(t, twins.StateInSync, stored.State, "the twin is not marked gone")
		owned, err := fx.svc.OwnedFields(ctx, "scenario", "SC-9")
		require.NoError(t, err)
		require.Equal(t, map[string][]string{"status": {"basecamp"}}, owned, "the renamed entity stays guarded")
	}

	t.Run("pending judges the twin against its new entity", func(t *testing.T) {
		fx, _ := setup(t)

		items, err := fx.svc.Pending(ctx, "basecamp")
		require.NoError(t, err)

		require.Len(t, items, 1)
		require.Equal(t, "SC-9", items[0].Twin.Target.ID)
		require.Contains(t, items[0].Reasons, "changed in rela")
		require.Equal(t, []twins.FieldValue{{Field: "title", Base: "Login", Local: "Changed in rela"}}, items[0].PushSet)
		requireFollowedRename(t, fx)
	})

	t.Run("show of the old id no longer lists the twin", func(t *testing.T) {
		fx, _ := setup(t)

		got, err := fx.svc.ForEntity(ctx, "SC-1")
		require.NoError(t, err)
		require.Empty(t, got)
		requireFollowedRename(t, fx)
	})

	t.Run("pull reports the twin stale", func(t *testing.T) {
		fx, _ := setup(t)
		_, err := fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{}, laterTime, false)
		require.ErrorIs(t, err, twins.ErrStale)
		requireFollowedRename(t, fx)
	})

	t.Run("pushed reports the twin stale", func(t *testing.T) {
		fx, version := setup(t)
		_, err := fx.svc.Pushed(ctx, "basecamp", "42", version, laterTime)
		require.ErrorIs(t, err, twins.ErrStale)
		requireFollowedRename(t, fx)
	})
}

// TestPull_OvertakenWhileWriting pins that a pull whose twin changed while it
// wrote the entity records nothing over that change: what landed meanwhile
// stands, and the pull reports why.
func TestPull_OvertakenWhileWriting(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		event func(ctx context.Context, fx fixture) error
		want  error
	}{
		{"a rename retargeted it", func(ctx context.Context, fx fixture) error {
			return fx.svc.EntityRenamed(ctx, "SC-1", "SC-9")
		}, twins.ErrStale},
		{"another sync recorded a base", func(ctx context.Context, fx fixture) error {
			_, err := fx.store.Modify(ctx, "basecamp", "42", func(tw twins.Twin) (twins.Twin, error) {
				tw.BaseVersion, tw.SyncedAt = "pushed-elsewhere", laterTime
				return tw, nil
			})
			return err
		}, twins.ErrStale},
		{"it was marked gone", func(ctx context.Context, fx fixture) error {
			_, err := fx.svc.MarkGone(ctx, "basecamp", "42")
			return err
		}, twins.ErrGone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t)
			fx.synced(ctx, t, "SC-1", "42")
			var meanwhile twins.Twin
			fx.ents.beforePatch = func(*entity.Entity) {
				require.NoError(t, tc.event(ctx, fx))
				meanwhile = fx.get(ctx, t, "42")
			}

			_, err := fx.svc.Pull(ctx, "basecamp", "42", twins.Remote{
				Properties: map[string]any{"status": "done"},
			}, laterTime, false)

			require.ErrorIs(t, err, tc.want)
			require.Equal(t, meanwhile, fx.get(ctx, t, "42"))
		})
	}
}
