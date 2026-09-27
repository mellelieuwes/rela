// Package twinstest is the conformance suite every [twins.Store]
// implementation must pass.
//
// It exists because the parts of the contract most likely to diverge between
// backends are the ones nobody notices: the listing order, whether a returned
// twin aliases stored state, whether the identity rules (one live twin per
// entity per system, one twin per external id, a target only a rename moves)
// hold on every write path, and whether concurrent writes all survive. A
// backend that gets those subtly wrong still passes a hand-written smoke test
// and then enforces the wrong ownership in production.
package twinstest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/canonical"
	"github.com/Sourcehaven-BV/rela/internal/twins"
)

// Factory builds a fresh, empty store for one subtest.
type Factory func(t *testing.T) twins.Store

// PairFactory builds two stores over ONE fresh, empty location, standing in
// for two processes (the CLI next to a running server).
type PairFactory func(t *testing.T) (a, b twins.Store)

// twin builds a minimal live twin of entityID in system.
func twin(system, externalID, entityID string) twins.Twin {
	return twins.Twin{
		System:     system,
		ExternalID: externalID,
		URL:        "https://example.com/" + system + "/" + externalID,
		Target:     twins.Target{Type: "scenario", ID: entityID},
		State:      twins.StatePending,
		Base:       twins.Snapshot{Properties: map[string]any{"title": "Title of " + entityID}, Body: "body"},
	}
}

func gone(tw twins.Twin) twins.Twin {
	tw.State = twins.StateGone
	return tw
}

// keys renders twins as "system/external-id" for order assertions.
func keys(list []twins.Twin) []string {
	out := make([]string, len(list))
	for i, tw := range list {
		out[i] = tw.System + "/" + tw.ExternalID
	}
	return out
}

func create(ctx context.Context, t *testing.T, s twins.Store, tws ...twins.Twin) {
	t.Helper()
	for _, tw := range tws {
		require.NoError(t, s.Create(ctx, tw))
	}
}

func forTarget(ctx context.Context, t *testing.T, s twins.Store, entityID string) []string {
	t.Helper()
	got, err := s.ForTarget(ctx, entityID)
	require.NoError(t, err)
	return keys(got)
}

// RunAll runs every conformance suite against f.
func RunAll(t *testing.T, f Factory) {
	t.Helper()
	t.Run("Empty", func(t *testing.T) { RunEmptyTests(t, f) })
	t.Run("CreateUpdate", func(t *testing.T) { RunCreateUpdateTests(t, f) })
	t.Run("DuplicateTarget", func(t *testing.T) { RunDuplicateTargetTests(t, f) })
	t.Run("Modify", func(t *testing.T) { RunModifyTests(t, f) })
	t.Run("ModifyRefusals", func(t *testing.T) { RunModifyRefusalTests(t, f) })
	t.Run("Ordering", func(t *testing.T) { RunOrderingTests(t, f) })
	t.Run("Retarget", func(t *testing.T) { RunRetargetTests(t, f) })
	t.Run("Delete", func(t *testing.T) { RunDeleteTests(t, f) })
	t.Run("Isolation", func(t *testing.T) { RunIsolationTests(t, f) })
	t.Run("Concurrency", func(t *testing.T) { RunConcurrencyTests(t, f) })
	t.Run("RoundTrip", func(t *testing.T) { RunRoundTripTests(t, f) })
}

// RunEmptyTests pins the empty store: misses are [twins.ErrNotFound], and
// listings are empty slices rather than nil (a nil encodes as JSON null).
func RunEmptyTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()
	s := f(t)

	_, err := s.Get(ctx, "basecamp", "1")
	require.ErrorIs(t, err, twins.ErrNotFound)

	byTarget, err := s.ForTarget(ctx, "SC-1")
	require.NoError(t, err)
	require.NotNil(t, byTarget)
	require.Empty(t, byTarget)

	list, err := s.List(ctx, twins.Filter{})
	require.NoError(t, err)
	require.NotNil(t, list)
	require.Empty(t, list)

	require.ErrorIs(t, s.Delete(ctx, "basecamp", "1"), twins.ErrNotFound)
	require.ErrorIs(t, s.Update(ctx, twin("basecamp", "1", "SC-1")), twins.ErrNotFound,
		"Update never creates")
	require.NoError(t, s.Retarget(ctx, "SC-1", "SC-2"), "retargeting an entity without twins is a no-op")
}

// RunCreateUpdateTests pins the identity of a twin: (system, external id),
// case-sensitive, taken once.
func RunCreateUpdateTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("get returns what was created", func(t *testing.T) {
		s := f(t)
		want := twin("basecamp", "1", "SC-1")
		create(ctx, t, s, want)

		got, err := s.Get(ctx, "basecamp", "1")
		require.NoError(t, err)
		requireTwinEqual(t, want, got)
	})

	t.Run("a taken external id is refused, even by a gone twin", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, gone(twin("basecamp", "1", "SC-1")))

		err := s.Create(ctx, twin("basecamp", "1", "SC-2"))
		require.ErrorIs(t, err, twins.ErrExternalIDTaken)

		got, err := s.Get(ctx, "basecamp", "1")
		require.NoError(t, err)
		require.Equal(t, "SC-1", got.Target.ID, "a refused create leaves the occupant alone")
	})

	t.Run("update replaces the twin at its key", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twin("basecamp", "1", "SC-1"))

		updated := twin("basecamp", "1", "SC-1")
		updated.State = twins.StateInSync
		updated.HasBase = true
		updated.BaseVersion = "v2"
		updated.Base = twins.Snapshot{Properties: map[string]any{"status": "done"}}
		require.NoError(t, s.Update(ctx, updated))

		got, err := s.Get(ctx, "basecamp", "1")
		require.NoError(t, err)
		requireTwinEqual(t, updated, got)

		all, err := s.List(ctx, twins.Filter{})
		require.NoError(t, err)
		require.Len(t, all, 1, "an update must not leave a second copy")
	})

	t.Run("update never moves a twin to another entity", func(t *testing.T) {
		// Only Retarget moves a twin; a replacement naming another entity
		// was read before a rename, and storing it would undo the rename.
		s := f(t)
		create(ctx, t, s, twin("basecamp", "1", "SC-1"))

		moved := twin("basecamp", "1", "SC-2")
		moved.State = twins.StateGone
		require.ErrorIs(t, s.Update(ctx, moved), twins.ErrStale)

		got, err := s.Get(ctx, "basecamp", "1")
		require.NoError(t, err)
		requireTwinEqual(t, twin("basecamp", "1", "SC-1"), got)
		require.Equal(t, []string{"basecamp/1"}, forTarget(ctx, t, s, "SC-1"))
		require.Empty(t, forTarget(ctx, t, s, "SC-2"))
	})

	t.Run("the key is system and external id together", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twin("basecamp", "1", "SC-1"), twin("github", "1", "SC-2"))

		got, err := s.Get(ctx, "github", "1")
		require.NoError(t, err)
		require.Equal(t, "SC-2", got.Target.ID)

		_, err = s.Get(ctx, "basecamp", "2")
		require.ErrorIs(t, err, twins.ErrNotFound)
	})

	t.Run("external ids are case-sensitive", func(t *testing.T) {
		// A case-insensitive filesystem must not alias these: they are two
		// items on the external side.
		s := f(t)
		create(ctx, t, s, twin("github", "ABC", "SC-1"), twin("github", "abc", "SC-2"), twin("github", "Abc", "SC-3"))

		for id, entityID := range map[string]string{"ABC": "SC-1", "abc": "SC-2", "Abc": "SC-3"} {
			got, err := s.Get(ctx, "github", id)
			require.NoError(t, err)
			require.Equal(t, entityID, got.Target.ID, "external id %q", id)
		}
		all, err := s.List(ctx, twins.Filter{})
		require.NoError(t, err)
		require.Equal(t, []string{"github/ABC", "github/Abc", "github/abc"}, keys(all))

		require.NoError(t, s.Delete(ctx, "github", "abc"))
		_, err = s.Get(ctx, "github", "ABC")
		require.NoError(t, err, "deleting one case must not delete another")
	})

	t.Run("entity ids are case-sensitive", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twin("basecamp", "1", "SC-a"), twin("basecamp", "2", "SC-A"))

		require.Equal(t, []string{"basecamp/1"}, forTarget(ctx, t, s, "SC-a"))
		require.Equal(t, []string{"basecamp/2"}, forTarget(ctx, t, s, "SC-A"))
	})
}

// RunDuplicateTargetTests pins one LIVE twin per entity per system, on every
// path that can create one. A gone twin is kept for the agent to propagate the
// delete, and must not stand in the way of linking the entity again.
func RunDuplicateTargetTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("a second live twin of one entity in one system is refused", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twin("basecamp", "1", "SC-1"))

		err := s.Create(ctx, twin("basecamp", "2", "SC-1"))
		require.ErrorIs(t, err, twins.ErrDuplicateTarget)

		_, err = s.Get(ctx, "basecamp", "2")
		require.ErrorIs(t, err, twins.ErrNotFound, "a refused create stores nothing")
		require.Equal(t, []string{"basecamp/1"}, forTarget(ctx, t, s, "SC-1"))
	})

	t.Run("a twin in another system is fine", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twin("basecamp", "1", "SC-1"), twin("github", "1", "SC-1"))
		require.Equal(t, []string{"basecamp/1", "github/1"}, forTarget(ctx, t, s, "SC-1"))
	})

	t.Run("a gone twin does not block a new one", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, gone(twin("basecamp", "1", "SC-1")))
		require.NoError(t, s.Create(ctx, twin("basecamp", "2", "SC-1")))
		require.Equal(t, []string{"basecamp/1", "basecamp/2"}, forTarget(ctx, t, s, "SC-1"))
	})

	t.Run("a gone twin may join a live one", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twin("basecamp", "1", "SC-1"))
		require.NoError(t, s.Create(ctx, gone(twin("basecamp", "2", "SC-1"))))
	})

	t.Run("reviving a gone twin next to a live one is refused", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, gone(twin("basecamp", "1", "SC-1")), twin("basecamp", "2", "SC-1"))

		err := s.Update(ctx, twin("basecamp", "1", "SC-1"))
		require.ErrorIs(t, err, twins.ErrDuplicateTarget)

		got, err := s.Get(ctx, "basecamp", "1")
		require.NoError(t, err)
		require.Equal(t, twins.StateGone, got.State, "a refused update leaves the stored twin alone")
	})

	t.Run("updating a twin in place is not a duplicate", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twin("basecamp", "1", "SC-1"))
		require.NoError(t, s.Update(ctx, twin("basecamp", "1", "SC-1")))
	})
}

// RunModifyTests pins the read-modify-write the sync path records its
// bookkeeping through: fn sees the twin as stored, its result is stored whole
// or not at all, and concurrent read-modify-writes are serialized.
func RunModifyTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("fn sees the stored twin; its result is stored and returned", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, fullTwin())

		var seen twins.Twin
		got, err := s.Modify(ctx, "basecamp", "42", func(tw twins.Twin) (twins.Twin, error) {
			seen = tw
			tw.State = twins.StateInSync
			tw.BaseVersion = "v2"
			tw.Findings = nil
			return tw, nil
		})
		require.NoError(t, err)
		requireTwinEqual(t, fullTwin(), seen)

		want := fullTwin()
		want.State, want.BaseVersion, want.Findings = twins.StateInSync, "v2", nil
		requireTwinEqual(t, want, got)
		stored, err := s.Get(ctx, "basecamp", "42")
		require.NoError(t, err)
		requireTwinEqual(t, want, stored)
	})

	t.Run("an error from fn is returned and nothing is stored", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, fullTwin())
		refused := errors.New("decided against it")

		_, err := s.Modify(ctx, "basecamp", "42", func(tw twins.Twin) (twins.Twin, error) {
			tw.State = twins.StateGone
			tw.Base.Properties["title"] = "mutated before refusing"
			return tw, refused
		})
		require.ErrorIs(t, err, refused)

		got, err := s.Get(ctx, "basecamp", "42")
		require.NoError(t, err)
		requireTwinEqual(t, fullTwin(), got)
	})

	t.Run("a missing twin is ErrNotFound and fn is not called", func(t *testing.T) {
		s := f(t)
		called := false
		_, err := s.Modify(ctx, "basecamp", "1", func(tw twins.Twin) (twins.Twin, error) {
			called = true
			return tw, nil
		})
		require.ErrorIs(t, err, twins.ErrNotFound)
		require.False(t, called)
	})

	t.Run("mutating the returned twin does not change the store", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, fullTwin())

		got, err := s.Modify(ctx, "basecamp", "42", func(tw twins.Twin) (twins.Twin, error) { return tw, nil })
		require.NoError(t, err)
		got.Base.Properties["title"] = "mutated"
		got.Findings[0].Field = "mutated"

		again, err := s.Get(ctx, "basecamp", "42")
		require.NoError(t, err)
		requireTwinEqual(t, fullTwin(), again)
	})

	t.Run("concurrent modifications of one twin all land", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twin("basecamp", "1", "SC-1"))
		modifyConcurrently(ctx, t, concurrentCreates, func(int) twins.Store { return s })
		requireAllModificationsLanded(ctx, t, s, concurrentCreates)
	})
}

// RunModifyRefusalTests pins the results Modify refuses, storing nothing: one
// that re-targets the twin — the mark of a twin read before a rename — one
// that changes its key, and one that breaks the duplicate-target rule.
func RunModifyRefusalTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("a result naming another entity is ErrStale and stores nothing", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twin("basecamp", "1", "SC-1"))

		_, err := s.Modify(ctx, "basecamp", "1", func(tw twins.Twin) (twins.Twin, error) {
			tw.Target.ID = "SC-2"
			tw.State = twins.StateGone
			return tw, nil
		})
		require.ErrorIs(t, err, twins.ErrStale)

		got, err := s.Get(ctx, "basecamp", "1")
		require.NoError(t, err)
		requireTwinEqual(t, twin("basecamp", "1", "SC-1"), got)
		require.Empty(t, forTarget(ctx, t, s, "SC-2"))
	})

	t.Run("a result with another key is refused and stores nothing", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twin("basecamp", "1", "SC-1"))

		_, err := s.Modify(ctx, "basecamp", "1", func(tw twins.Twin) (twins.Twin, error) {
			tw.ExternalID = "2"
			return tw, nil
		})
		require.Error(t, err)

		all, err := s.List(ctx, twins.Filter{})
		require.NoError(t, err)
		require.Equal(t, []string{"basecamp/1"}, keys(all))
	})

	t.Run("reviving a gone twin next to a live one is refused", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, gone(twin("basecamp", "1", "SC-1")), twin("basecamp", "2", "SC-1"))

		_, err := s.Modify(ctx, "basecamp", "1", func(tw twins.Twin) (twins.Twin, error) {
			tw.State = twins.StatePending
			return tw, nil
		})
		require.ErrorIs(t, err, twins.ErrDuplicateTarget)

		got, err := s.Get(ctx, "basecamp", "1")
		require.NoError(t, err)
		require.Equal(t, twins.StateGone, got.State)
	})

	t.Run("a twin read before a rename cannot undo it", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twin("basecamp", "1", "SC-1"))
		stale, err := s.Get(ctx, "basecamp", "1")
		require.NoError(t, err)

		require.NoError(t, s.Retarget(ctx, "SC-1", "SC-9"))

		requireStaleWriteRefused(ctx, t, s, stale, "SC-9")
	})
}

// requireStaleWriteRefused pins that stale — a twin read before a rename
// moved it to movedTo — can be written back neither through Modify nor
// through Update, even as gone (what a reader that found the old id missing
// would record), and that the twin stays live on movedTo.
func requireStaleWriteRefused(ctx context.Context, t *testing.T, s twins.Store, stale twins.Twin, movedTo string) {
	t.Helper()
	_, err := s.Modify(ctx, stale.System, stale.ExternalID, func(twins.Twin) (twins.Twin, error) {
		return gone(stale), nil
	})
	require.ErrorIs(t, err, twins.ErrStale)
	require.ErrorIs(t, s.Update(ctx, gone(stale)), twins.ErrStale)

	got, err := s.Get(ctx, stale.System, stale.ExternalID)
	require.NoError(t, err)
	require.Equal(t, movedTo, got.Target.ID, "the rename must not be reverted")
	require.True(t, got.Live(), "the twin must still own its fields")
	require.Equal(t, []string{stale.System + "/" + stale.ExternalID}, forTarget(ctx, t, s, movedTo))
	require.Empty(t, forTarget(ctx, t, s, stale.Target.ID))
}

// modifyConcurrently has n writers each add one finding to basecamp/1, writer
// i through storeFor(i). Every write is a read-modify-write of the same twin,
// so a backend that does not serialize them loses some.
func modifyConcurrently(ctx context.Context, t *testing.T, n int, storeFor func(i int) twins.Store) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Go(func() {
			_, err := storeFor(i).Modify(ctx, "basecamp", "1", func(tw twins.Twin) (twins.Twin, error) {
				tw.Findings = append(tw.Findings, twins.Finding{Field: fmt.Sprintf("f-%02d", i), Kind: twins.FindingForeignEdit})
				return tw, nil
			})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func requireAllModificationsLanded(ctx context.Context, t *testing.T, s twins.Store, n int) {
	t.Helper()
	got, err := s.Get(ctx, "basecamp", "1")
	require.NoError(t, err)
	require.Len(t, got.Findings, n, "every concurrent modification must land")
}

// RunOrderingTests pins the listing contract: List by system then external
// id, ForTarget by system, never storage order.
func RunOrderingTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	seed := func(t *testing.T) twins.Store {
		t.Helper()
		s := f(t)
		// Created out of order on purpose.
		github2 := twin("github", "b-2", "SC-1")
		github2.State = twins.StateConflict
		basecamp9 := twin("basecamp", "9", "SC-2")
		basecamp9.State = twins.StateInSync
		create(ctx, t, s,
			github2,
			basecamp9,
			twin("jira", "X-1", "SC-1"),
			twin("basecamp", "10", "SC-1"),
			twin("github", "a-1", "SC-3"),
		)
		return s
	}

	t.Run("List orders by system then external id", func(t *testing.T) {
		got, err := seed(t).List(ctx, twins.Filter{})
		require.NoError(t, err)
		require.Equal(t, []string{"basecamp/10", "basecamp/9", "github/a-1", "github/b-2", "jira/X-1"}, keys(got))
	})

	t.Run("List filters by system", func(t *testing.T) {
		got, err := seed(t).List(ctx, twins.Filter{System: "github"})
		require.NoError(t, err)
		require.Equal(t, []string{"github/a-1", "github/b-2"}, keys(got))
	})

	t.Run("List filters by state", func(t *testing.T) {
		got, err := seed(t).List(ctx, twins.Filter{States: []twins.State{twins.StateInSync, twins.StateConflict}})
		require.NoError(t, err)
		require.Equal(t, []string{"basecamp/9", "github/b-2"}, keys(got))
	})

	t.Run("List combines system and state", func(t *testing.T) {
		got, err := seed(t).List(ctx, twins.Filter{System: "github", States: []twins.State{twins.StatePending}})
		require.NoError(t, err)
		require.Equal(t, []string{"github/a-1"}, keys(got))
	})

	t.Run("List of an unknown system is empty", func(t *testing.T) {
		got, err := seed(t).List(ctx, twins.Filter{System: "linear"})
		require.NoError(t, err)
		require.Empty(t, got)
	})

	t.Run("ForTarget returns only that entity, by system", func(t *testing.T) {
		require.Equal(t, []string{"basecamp/10", "github/b-2", "jira/X-1"}, forTarget(ctx, t, seed(t), "SC-1"))
	})
}

// RunRetargetTests pins the rename path. The entity manager notifies a rename
// exactly once, so a store that gets this wrong strands every twin of a
// renamed entity at an id nothing resolves to.
func RunRetargetTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("moves every twin of the old id and nothing else", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twin("basecamp", "1", "SC-old"), twin("github", "1", "SC-old"), twin("basecamp", "2", "SC-other"))

		require.NoError(t, s.Retarget(ctx, "SC-old", "SC-new"))

		require.Equal(t, []string{"basecamp/1", "github/1"}, forTarget(ctx, t, s, "SC-new"))
		require.Empty(t, forTarget(ctx, t, s, "SC-old"))
		require.Equal(t, []string{"basecamp/2"}, forTarget(ctx, t, s, "SC-other"))
	})

	t.Run("keeps everything but the target id", func(t *testing.T) {
		s := f(t)
		want := fullTwin()
		want.Target.ID = "SC-old"
		create(ctx, t, s, want)

		require.NoError(t, s.Retarget(ctx, "SC-old", "SC-new"))

		got, err := s.Get(ctx, want.System, want.ExternalID)
		require.NoError(t, err)
		want.Target.ID = "SC-new"
		requireTwinEqual(t, want, got)
	})

	t.Run("a live collision in a shared system changes nothing", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twin("github", "1", "SC-old"), twin("basecamp", "1", "SC-old"), twin("basecamp", "2", "SC-new"))

		err := s.Retarget(ctx, "SC-old", "SC-new")
		require.ErrorIs(t, err, twins.ErrDuplicateTarget)

		require.Equal(t, []string{"basecamp/1", "github/1"}, forTarget(ctx, t, s, "SC-old"), "no twin may move when one cannot")
		require.Equal(t, []string{"basecamp/2"}, forTarget(ctx, t, s, "SC-new"))
	})

	t.Run("a gone twin on either side is no collision", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twin("basecamp", "1", "SC-old"), gone(twin("basecamp", "2", "SC-new")))

		require.NoError(t, s.Retarget(ctx, "SC-old", "SC-new"))
		require.Equal(t, []string{"basecamp/1", "basecamp/2"}, forTarget(ctx, t, s, "SC-new"))
	})

	t.Run("a twin of the new id in another system is no collision", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twin("basecamp", "1", "SC-old"), twin("github", "1", "SC-new"))

		require.NoError(t, s.Retarget(ctx, "SC-old", "SC-new"))
		require.Equal(t, []string{"basecamp/1", "github/1"}, forTarget(ctx, t, s, "SC-new"))
	})

	t.Run("retargeting to the same id is a no-op", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twin("basecamp", "1", "SC-1"))
		require.NoError(t, s.Retarget(ctx, "SC-1", "SC-1"))
		require.Equal(t, []string{"basecamp/1"}, forTarget(ctx, t, s, "SC-1"))
	})
}

// RunDeleteTests pins deletion.
func RunDeleteTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("removes only the named twin", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twin("basecamp", "1", "SC-1"), twin("basecamp", "2", "SC-2"), twin("github", "1", "SC-1"))

		require.NoError(t, s.Delete(ctx, "basecamp", "1"))

		_, err := s.Get(ctx, "basecamp", "1")
		require.ErrorIs(t, err, twins.ErrNotFound)
		all, err := s.List(ctx, twins.Filter{})
		require.NoError(t, err)
		require.Equal(t, []string{"basecamp/2", "github/1"}, keys(all))
		require.Equal(t, []string{"github/1"}, forTarget(ctx, t, s, "SC-1"))
	})

	t.Run("a second delete is ErrNotFound", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twin("basecamp", "1", "SC-1"))
		require.NoError(t, s.Delete(ctx, "basecamp", "1"))
		require.ErrorIs(t, s.Delete(ctx, "basecamp", "1"), twins.ErrNotFound)
	})

	t.Run("a deleted twin frees its key and its entity's slot", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twin("basecamp", "1", "SC-1"))
		require.NoError(t, s.Delete(ctx, "basecamp", "1"))
		require.NoError(t, s.Create(ctx, twin("basecamp", "2", "SC-1")))
		require.NoError(t, s.Create(ctx, twin("basecamp", "1", "SC-9")))
	})
}

// RunIsolationTests pins that returned twins do not alias stored state, in
// either direction — impossible for a persistent backend, so tests written
// against a store that allows it would not transfer.
func RunIsolationTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("mutating a returned twin does not change the store", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, fullTwin())

		got, err := s.Get(ctx, "basecamp", "42")
		require.NoError(t, err)
		got.Base.Properties["title"] = "mutated"
		gotTags, ok := got.Base.Properties["tags"].([]any)
		require.True(t, ok, "tags round-trips as a list")
		gotTags[0] = "mutated"
		got.Findings[0].Field = "mutated"

		listed, err := s.List(ctx, twins.Filter{})
		require.NoError(t, err)
		listed[0].Base.Properties["title"] = "mutated too"

		again, err := s.Get(ctx, "basecamp", "42")
		require.NoError(t, err)
		requireTwinEqual(t, fullTwin(), again)
	})

	t.Run("mutating a twin after a write does not change the store", func(t *testing.T) {
		s := f(t)
		tw := fullTwin()
		create(ctx, t, s, tw)
		tw.Base.Properties["title"] = "mutated"
		tags, ok := tw.Base.Properties["tags"].([]any)
		require.True(t, ok, "tags is a list")
		tags[0] = "mutated"

		got, err := s.Get(ctx, "basecamp", "42")
		require.NoError(t, err)
		requireTwinEqual(t, fullTwin(), got)
	})
}

// concurrentCreates is how many twins the concurrency suites create at once:
// enough racing writers that a lost update is all but certain to show.
const concurrentCreates = 24

// RunConcurrencyTests pins that concurrent writes of different twins all
// survive. A backend doing read-modify-write without serializing (a shared
// index, a directory scan racing a write) would lose some.
func RunConcurrencyTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()
	s := f(t)
	createConcurrently(ctx, t, concurrentCreates, func(int) twins.Store { return s })
	requireAllSurvive(ctx, t, s, concurrentCreates)
}

// createConcurrently creates n twins in parallel, twin i through storeFor(i).
// Twins alternate systems and pairs share an entity, so every create also
// reads and writes that entity's index.
func createConcurrently(ctx context.Context, t *testing.T, n int, storeFor func(i int) twins.Store) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Go(func() {
			system := []string{"basecamp", "github"}[i%2]
			errs <- storeFor(i).Create(ctx,
				twin(system, fmt.Sprintf("item-%02d", i), fmt.Sprintf("SC-%02d", i/2)))
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func requireAllSurvive(ctx context.Context, t *testing.T, s twins.Store, n int) {
	t.Helper()
	all, err := s.List(ctx, twins.Filter{})
	require.NoError(t, err)
	require.Len(t, all, n, "every concurrent create must survive")
	for i := range n / 2 {
		require.Equal(t,
			[]string{fmt.Sprintf("basecamp/item-%02d", 2*i), fmt.Sprintf("github/item-%02d", 2*i+1)},
			forTarget(ctx, t, s, fmt.Sprintf("SC-%02d", i)),
			"every create must land in its entity's index")
	}
}

// RunCrossInstanceTests pins what a backend shared between processes owes:
// what one instance writes the other reads at once (no stale cache), the
// identity rules hold across instances, not just within one, and a
// read-modify-write in one instance is atomic against writes in the other.
func RunCrossInstanceTests(t *testing.T, f PairFactory) {
	t.Helper()
	ctx := context.Background()

	t.Run("a create in one instance is visible in the other", func(t *testing.T) {
		a, b := f(t)
		require.Empty(t, forTarget(ctx, t, b, "SC-1"), "b has read the empty state first")

		create(ctx, t, a, twin("basecamp", "1", "SC-1"))

		require.Equal(t, []string{"basecamp/1"}, forTarget(ctx, t, b, "SC-1"))
		got, err := b.Get(ctx, "basecamp", "1")
		require.NoError(t, err)
		require.Equal(t, "SC-1", got.Target.ID)

		require.NoError(t, b.Delete(ctx, "basecamp", "1"))
		require.Empty(t, forTarget(ctx, t, a, "SC-1"))
	})

	t.Run("concurrent creates from both instances all survive", func(t *testing.T) {
		a, b := f(t)
		createConcurrently(ctx, t, concurrentCreates, func(i int) twins.Store { return []twins.Store{a, b}[i%2] })
		requireAllSurvive(ctx, t, a, concurrentCreates)
	})

	t.Run("the duplicate rule holds across instances", func(t *testing.T) {
		const rounds = 8
		for round := range rounds {
			a, b := f(t)
			entityID := fmt.Sprintf("SC-%d", round)
			var wg sync.WaitGroup
			errs := make([]error, 2)
			for i, s := range []twins.Store{a, b} {
				wg.Go(func() {
					errs[i] = s.Create(ctx, twin("basecamp", fmt.Sprintf("%d-%d", round, i), entityID))
				})
			}
			wg.Wait()

			ok := 0
			for _, err := range errs {
				if err == nil {
					ok++
				} else {
					require.ErrorIs(t, err, twins.ErrDuplicateTarget)
				}
			}
			require.Equal(t, 1, ok, "exactly one of two racing creates may win")
			require.Len(t, forTarget(ctx, t, a, entityID), 1)
		}
	})

	t.Run("Retarget in A between Get and Modify-with-stale-target in B does not revert the target", func(t *testing.T) {
		// B is the CLI listing twins; A is the server renaming the entity
		// while B still holds its read. B then finds the old id missing and
		// records the twin gone — which must not stick.
		a, b := f(t)
		create(ctx, t, a, twin("basecamp", "1", "SC-1"))
		stale, err := b.Get(ctx, "basecamp", "1")
		require.NoError(t, err)

		require.NoError(t, a.Retarget(ctx, "SC-1", "SC-9"))

		requireStaleWriteRefused(ctx, t, b, stale, "SC-9")
		got, err := a.Get(ctx, "basecamp", "1")
		require.NoError(t, err)
		require.Equal(t, "SC-9", got.Target.ID)
	})

	t.Run("concurrent modifications from both instances all land", func(t *testing.T) {
		a, b := f(t)
		create(ctx, t, a, twin("basecamp", "1", "SC-1"))
		modifyConcurrently(ctx, t, concurrentCreates, func(i int) twins.Store { return []twins.Store{a, b}[i%2] })
		requireAllModificationsLanded(ctx, t, b, concurrentCreates)
	})
}

// fullTwin exercises every field a backend must round-trip.
func fullTwin() twins.Twin {
	amsterdam := time.FixedZone("CEST", int((2 * time.Hour).Seconds()))
	return twins.Twin{
		System:     "basecamp",
		ExternalID: "42",
		URL:        "https://app.basecamp.com/5734045/buckets/35926565/todos/42",
		Target:     twins.Target{Type: "scenario", ID: "SC-015"},
		State:      twins.StateConflict,
		HasBase:    true,
		Base: twins.Snapshot{
			Properties: map[string]any{
				"title":      "Login: happy path",
				"points":     3,
				"ratio":      0.25,
				"blocking":   true,
				"tags":       []any{"auth", "web"},
				"owners":     []string{"alice", "bob"},
				"cleared":    nil,
				"looks_bool": "true",
				"looks_num":  "007",
				"looks_null": "null",
				"looks_date": "2026-05-01",
				"multiline":  "first line\nsecond line\n",
				"empty":      "",
			},
			Body: "# Heading\n\nA paragraph with **markdown**.\n",
		},
		BaseVersion:     "0123456789abcdef",
		SyncedAt:        time.Date(2026, 5, 1, 12, 30, 15, 123456789, amsterdam),
		RemoteUpdatedAt: time.Date(2026, 4, 30, 8, 0, 0, 0, time.UTC),
		Findings: []twins.Finding{
			{Field: "status", Kind: twins.FindingConflict, Base: "open", Ours: "doing", Theirs: "done"},
			{Field: "title", Kind: twins.FindingForeignEdit, Base: nil, Ours: "a", Theirs: nil, Propose: true},
			{Field: "body", Kind: twins.FindingLocalDrift, Base: "x", Ours: "y", Theirs: "x"},
			{Kind: twins.FindingRejected, Message: "validation errors:\n  status: not an allowed value"},
		},
	}
}

// RunRoundTripTests pins that every field survives storage: snapshot values
// of every kind (compared the way reconcile compares them, through
// canonical), strings that look like other YAML types, nil, and times.
func RunRoundTripTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("every field survives", func(t *testing.T) {
		s := f(t)
		want := fullTwin()
		create(ctx, t, s, want)

		got, err := s.Get(ctx, want.System, want.ExternalID)
		require.NoError(t, err)
		requireTwinEqual(t, want, got)

		listed, err := s.List(ctx, twins.Filter{})
		require.NoError(t, err)
		require.Len(t, listed, 1)
		requireTwinEqual(t, want, listed[0])
	})

	t.Run("a never-synced twin reads back as never synced", func(t *testing.T) {
		s := f(t)
		create(ctx, t, s, twins.Twin{
			System: "basecamp", ExternalID: "1", Target: twins.Target{Type: "scenario", ID: "SC-1"},
			State: twins.StatePending,
		})

		got, err := s.Get(ctx, "basecamp", "1")
		require.NoError(t, err)
		require.False(t, got.HasBase)
		require.Empty(t, got.Base.Properties)
		require.True(t, got.SyncedAt.IsZero())
		require.True(t, got.RemoteUpdatedAt.IsZero())
		require.Empty(t, got.Findings)
	})
}

// requireTwinEqual compares twins field by field. Snapshot and finding values
// compare through [canonical.EqualValue] — the equality reconcile uses, so a
// backend that returns 3 for 3.0 or []any for []string is still faithful —
// and times compare as instants.
func requireTwinEqual(t *testing.T, want, got twins.Twin) {
	t.Helper()
	require.Equal(t, want.System, got.System)
	require.Equal(t, want.ExternalID, got.ExternalID)
	require.Equal(t, want.URL, got.URL)
	require.Equal(t, want.Target, got.Target)
	require.Equal(t, want.State, got.State)
	require.Equal(t, want.HasBase, got.HasBase)
	require.Equal(t, want.BaseVersion, got.BaseVersion)
	require.True(t, want.SyncedAt.Equal(got.SyncedAt), "SyncedAt: want %v, got %v", want.SyncedAt, got.SyncedAt)
	require.True(t, want.RemoteUpdatedAt.Equal(got.RemoteUpdatedAt),
		"RemoteUpdatedAt: want %v, got %v", want.RemoteUpdatedAt, got.RemoteUpdatedAt)

	require.Equal(t, want.Base.Body, got.Base.Body)
	for k, v := range want.Base.Properties {
		gv, ok := got.Base.Properties[k]
		require.True(t, ok, "property %q must survive, even when nil", k)
		require.True(t, canonical.EqualValue(v, gv), "property %q: want %#v, got %#v", k, v, gv)
	}
	require.Len(t, got.Base.Properties, len(want.Base.Properties), "no property may appear from nowhere")

	require.Len(t, got.Findings, len(want.Findings))
	for i, wf := range want.Findings {
		gf := got.Findings[i]
		require.Equal(t, wf.Field, gf.Field)
		require.Equal(t, wf.Kind, gf.Kind)
		require.Equal(t, wf.Propose, gf.Propose)
		require.Equal(t, wf.Message, gf.Message)
		require.True(t, canonical.EqualValue(wf.Base, gf.Base), "finding %d Base", i)
		require.True(t, canonical.EqualValue(wf.Ours, gf.Ours), "finding %d Ours", i)
		require.True(t, canonical.EqualValue(wf.Theirs, gf.Theirs), "finding %d Theirs", i)
	}
}
