package storetest

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// RunCASTests runs the compare-and-swap conformance suite for
// [store.EntityWriter.UpdateEntityIf] (TKT-34XS2R).
//
// This is part of RunAll rather than an opt-in section, deliberately. CAS is
// the primitive callers reach for INSTEAD of a process-local mutex, so a
// backend that accepts a stale expected-version is not merely missing a
// feature — it silently returns success for a write that lost a race, which
// is the exact failure the caller adopted CAS to avoid. There is no
// "degrade gracefully" story here, so there is no capability flag.
func RunCASTests(t *testing.T, f Factory) {
	t.Run("MatchingVersionApplies", func(t *testing.T) {
		s := f(t)
		e := entity.New("FEAT-001", "feature")
		e.SetString("title", "Login")
		require.NoError(t, s.CreateEntity(ctx(), e))

		read, err := s.GetEntity(ctx(), e.ID)
		require.NoError(t, err)
		v := store.VersionOf(read)

		read.SetString("title", "Login v2")
		next, err := s.UpdateEntityIf(ctx(), read, store.UpdateCondition{ExpectedVersion: v})
		require.NoError(t, err)

		got, err := s.GetEntity(ctx(), e.ID)
		require.NoError(t, err)
		assert.Equal(t, read.GetString("title"), got.GetString("title"))
		assert.Equal(t, store.VersionOf(got), next,
			"the returned version must be the post-write version, so a caller can chain writes without re-reading")
		assert.NotEqual(t, v, next, "a content change must move the version")
	})

	// A backend that normalizes what it stores (fsstore reflows the body)
	// must return the version of what it stored, not of what it was given:
	// the token is only useful if a later read reproduces it and a chained
	// compare-and-swap accepts it.
	t.Run("ReturnedVersionSurvivesNormalization", func(t *testing.T) {
		s := f(t)
		e := entity.New("FEAT-001", "feature")
		e.SetString("title", "Login")
		require.NoError(t, s.CreateEntity(ctx(), e))

		read, err := s.GetEntity(ctx(), e.ID)
		require.NoError(t, err)
		v := store.VersionOf(read)
		read.Content = "## Notes\n\nA single paragraph that is long enough to be reflowed by a backend " +
			"that wraps markdown before writing it to disk, which is exactly the case this pins.\n\n\n\n- a\n-  b\n"
		next, err := s.UpdateEntityIf(ctx(), read, store.UpdateCondition{ExpectedVersion: v})
		require.NoError(t, err)

		got, err := s.GetEntity(ctx(), e.ID)
		require.NoError(t, err)
		assert.Equal(t, store.VersionOf(got), next,
			"the returned version must be the version of the row as stored, after any normalization")

		got.SetString("title", "Login v2")
		_, err = s.UpdateEntityIf(ctx(), got, store.UpdateCondition{ExpectedVersion: next})
		require.NoError(t, err, "a chained compare-and-swap with the returned version must apply")
	})

	t.Run("StaleVersionConflicts", func(t *testing.T) {
		s := f(t)
		e := entity.New("FEAT-001", "feature")
		e.SetString("title", "Login")
		require.NoError(t, s.CreateEntity(ctx(), e))

		read, err := s.GetEntity(ctx(), e.ID)
		require.NoError(t, err)
		stale := store.VersionOf(read)

		// Someone else writes in between, invalidating `stale`.
		interloper, err := s.GetEntity(ctx(), e.ID)
		require.NoError(t, err)
		interloper.SetString("title", "Written by someone else")
		require.NoError(t, s.UpdateEntity(ctx(), interloper))

		read.SetString("title", "Based on a stale read")
		_, err = s.UpdateEntityIf(ctx(), read, store.UpdateCondition{ExpectedVersion: stale})
		require.Error(t, err)

		// The error shape is the contract (RR-HI9QIU): a conflict that
		// cannot be recognized by errors.As turns a caller's retry loop
		// into dead code, and the failure is silent.
		var conflict *store.VersionConflictError
		require.ErrorAs(t, err, &conflict,
			"conflict must be recoverable with errors.As so callers can retry")
		assert.Equal(t, e.ID, conflict.ID)
		assert.Equal(t, stale, conflict.Expected)
		assert.ErrorIs(t, err, store.ErrConflict,
			"errors.Is(err, ErrConflict) must keep working for callers that only need 'lost a race'")

		// NOTHING was written.
		got, err := s.GetEntity(ctx(), e.ID)
		require.NoError(t, err)
		assert.Equal(t, interloper.GetString("title"), got.GetString("title"),
			"a rejected conditional write must not have applied")
		assert.Equal(t, store.VersionOf(got), conflict.Actual,
			"Actual must be the current stored version, so the caller can retry against it")
	})

	// A caller that reads, is beaten, then re-reads and retries with the
	// version from the conflict error must succeed. This is the loop every
	// read-modify-write consumer will write, so the store owes it a token
	// that actually converges.
	t.Run("RetryWithActualSucceeds", func(t *testing.T) {
		s := f(t)
		e := entity.New("FEAT-001", "feature")
		e.SetString("title", "Login")
		require.NoError(t, s.CreateEntity(ctx(), e))

		read, err := s.GetEntity(ctx(), e.ID)
		require.NoError(t, err)
		stale := store.VersionOf(read)

		other, err := s.GetEntity(ctx(), e.ID)
		require.NoError(t, err)
		other.SetString("status", "open")
		require.NoError(t, s.UpdateEntity(ctx(), other))

		read.SetString("title", "Login v2")
		_, err = s.UpdateEntityIf(ctx(), read, store.UpdateCondition{ExpectedVersion: stale})
		var conflict *store.VersionConflictError
		require.ErrorAs(t, err, &conflict)

		// Re-read, re-apply the intent, retry with the reported version.
		fresh, err := s.GetEntity(ctx(), e.ID)
		require.NoError(t, err)
		fresh.SetString("title", "Login v2")
		_, err = s.UpdateEntityIf(ctx(), fresh, store.UpdateCondition{ExpectedVersion: conflict.Actual})
		require.NoError(t, err)

		got, err := s.GetEntity(ctx(), e.ID)
		require.NoError(t, err)
		assert.Equal(t, "Login v2", got.GetString("title"))
		assert.Equal(t, "open", got.GetString("status"),
			"the retry must preserve the interloper's write, not resurrect the stale base")
	})

	t.Run("MissingEntityIsNotFoundNotConflict", func(t *testing.T) {
		s := f(t)
		ghost := entity.New("FEAT-404", "feature")
		ghost.SetString("title", "Never existed")

		_, err := s.UpdateEntityIf(ctx(), ghost,
			store.UpdateCondition{ExpectedVersion: store.VersionOf(ghost)})
		assert.ErrorIs(t, err, store.ErrNotFound,
			"a deleted/absent row is ErrNotFound: retrying cannot help, so it must not look like a conflict")

		var conflict *store.VersionConflictError
		assert.NotErrorAs(t, err, &conflict,
			"ErrNotFound must not also present as a version conflict, or callers will retry forever")
	})

	// The zero condition is documented as equivalent to an unconditional
	// UpdateEntity. Generic code that threads a condition through can then
	// pass the zero value rather than branching.
	t.Run("ZeroConditionIsUnconditional", func(t *testing.T) {
		s := f(t)
		e := entity.New("FEAT-001", "feature")
		e.SetString("title", "Login")
		require.NoError(t, s.CreateEntity(ctx(), e))

		// Move the stored state so any non-empty expectation would fail.
		bump, err := s.GetEntity(ctx(), e.ID)
		require.NoError(t, err)
		bump.SetString("title", "Moved on")
		require.NoError(t, s.UpdateEntity(ctx(), bump))

		writeThrough := bump.Clone()
		writeThrough.SetString("title", "Unconditional")
		_, err = s.UpdateEntityIf(ctx(), writeThrough, store.UpdateCondition{})
		require.NoError(t, err)

		got, err := s.GetEntity(ctx(), e.ID)
		require.NoError(t, err)
		assert.Equal(t, "Unconditional", got.GetString("title"))
	})

	// The version must be computed from stored CONTENT, not from a
	// wall-clock stamp or a per-write counter: a re-save of identical bytes
	// must not invalidate a token another caller is still holding.
	t.Run("IdenticalRewriteKeepsVersion", func(t *testing.T) {
		s := f(t)
		e := entity.New("FEAT-001", "feature")
		e.SetString("title", "Login")
		require.NoError(t, s.CreateEntity(ctx(), e))

		read, err := s.GetEntity(ctx(), e.ID)
		require.NoError(t, err)
		before := store.VersionOf(read)

		// Re-save byte-identical content.
		require.NoError(t, s.UpdateEntity(ctx(), read.Clone()))

		after, err := s.GetEntity(ctx(), e.ID)
		require.NoError(t, err)
		assert.Equal(t, before, store.VersionOf(after),
			"an identical rewrite must not move the version (UpdatedAt must not be folded in)")

		// ...and a token taken before that rewrite still satisfies a CAS.
		after.SetString("title", "Login v2")
		_, err = s.UpdateEntityIf(ctx(), after, store.UpdateCondition{ExpectedVersion: before})
		require.NoError(t, err)
	})

	// The headline guarantee: N concurrent read-modify-write appenders, each
	// retrying on conflict, must all land. Without CAS the same shape loses
	// writes silently — which is exactly what a plain UpdateEntity does, and
	// is asserted as the contrast below.
	t.Run("ConcurrentAppendersAllLandWithRetry", func(t *testing.T) {
		s := f(t)
		e := entity.New("FEAT-001", "feature")
		e.SetString("title", "Log")
		e.Content = ""
		require.NoError(t, s.CreateEntity(ctx(), e))

		const appenders = 8
		const maxAttempts = 200 // generous: contention is the point, not the budget

		// No trailing newline in the marker: fsstore round-trips the body
		// through markdown, which normalizes trailing whitespace. That is a
		// storage-format detail and NOT part of the CAS contract, so the
		// assertion must not depend on it.
		marker := func(i int) string { return fmt.Sprintf("[line-%d]", i) }

		var wg sync.WaitGroup
		errs := make([]error, appenders)
		for i := range appenders {
			wg.Go(func() {
				line := marker(i)
				for range maxAttempts {
					cur, err := s.GetEntity(ctx(), e.ID)
					if err != nil {
						errs[i] = err
						return
					}
					v := store.VersionOf(cur)
					cur.Content += line
					_, err = s.UpdateEntityIf(ctx(), cur, store.UpdateCondition{ExpectedVersion: v})
					if err == nil {
						return
					}
					var conflict *store.VersionConflictError
					if !errors.As(err, &conflict) {
						errs[i] = err
						return
					}
					// Lost the race — loop, re-read, recompute.
				}
				errs[i] = fmt.Errorf("appender %d exhausted %d attempts", i, maxAttempts)
			})
		}
		wg.Wait()
		for i, err := range errs {
			require.NoErrorf(t, err, "appender %d", i)
		}

		got, err := s.GetEntity(ctx(), e.ID)
		require.NoError(t, err)
		for i := range appenders {
			assert.Containsf(t, got.Content, marker(i),
				"appender %d's write was lost despite CAS", i)
		}
	})
}
