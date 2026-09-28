package pgstore_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/lock"
	"github.com/Sourcehaven-BV/rela/internal/lock/locktest"
	"github.com/Sourcehaven-BV/rela/internal/store/pgstore"
)

// TestKeyedLock_Conformance runs the shared lock.Locker suite against the real
// PostgreSQL backend, so it is held to exactly the same contract as the
// in-process locker rather than to a prose approximation of it.
func TestKeyedLock_Conformance(t *testing.T) {
	_ = testDSN(t)
	locktest.RunAll(t, func(tb testing.TB) lock.Locker {
		tb.Helper()
		// A fresh schema per subtest: advisory locks are database-global, so
		// subtests sharing a schema would contend on the same keys.
		// Four connections give a keyed-lock capacity of two, so two distinct
		// keys can be held at once as DistinctKeysDoNotContend requires.
		pool := newScopedPoolSized(tb.(*testing.T), 4)
		st, err := pgstore.New(pool)
		require.NoError(tb, err)
		l, err := lock.NewBackendLocker(st)
		require.NoError(tb, err)
		return l
	})
}

// TestKeyedLock_ExclusiveAcrossStores pins the property the in-process locker
// cannot provide and the whole postgres backend exists for: two SEPARATE store
// instances (standing in for two rela-server processes) exclude each other on
// the same key.
func TestKeyedLock_ExclusiveAcrossStores(t *testing.T) {
	_ = testDSN(t)
	pool := newScopedPool(t)
	a, err := pgstore.New(pool)
	require.NoError(t, err)
	b, err := pgstore.New(pool)
	require.NoError(t, err)
	ctx := context.Background()

	release, err := a.AcquireKeyedLock(ctx, "incident/web01")
	require.NoError(t, err)

	// b must NOT get the same key while a holds it.
	waitCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	_, err = b.AcquireKeyedLock(waitCtx, "incident/web01")
	require.Error(t, err, "a second store must block on a key the first holds")

	release()

	// After release b proceeds.
	got, err := b.AcquireKeyedLock(ctx, "incident/web01")
	require.NoError(t, err, "release must hand the key to the waiting store")
	got()
}

// TestKeyedLock_DistinctKeysDoNotContendAcrossStores is the burst property: two
// processes locking DIFFERENT keys never wait on each other. If this regressed,
// a monitoring fan-out would serialize database-wide.
func TestKeyedLock_DistinctKeysDoNotContendAcrossStores(t *testing.T) {
	_ = testDSN(t)
	pool := newScopedPool(t)
	a, err := pgstore.New(pool)
	require.NoError(t, err)
	b, err := pgstore.New(pool)
	require.NoError(t, err)
	ctx := context.Background()

	relA, err := a.AcquireKeyedLock(ctx, "incident/web01")
	require.NoError(t, err)
	defer relA()

	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	relB, err := b.AcquireKeyedLock(waitCtx, "incident/db02")
	require.NoError(t, err, "a different key must not contend")
	relB()
}

// TestKeyedLock_ScopedPerSchema pins the tenant-isolation clause: the same key
// in two schemas is two different locks. Advisory locks are database-global, so
// without the schema in the hash one tenant's webhook would block another's —
// the BUG-CA3VY0 class.
func TestKeyedLock_ScopedPerSchema(t *testing.T) {
	_ = testDSN(t)
	poolA := newScopedPool(t)
	poolB := newScopedPool(t)
	a, err := pgstore.New(poolA)
	require.NoError(t, err)
	b, err := pgstore.New(poolB)
	require.NoError(t, err)
	ctx := context.Background()

	relA, err := a.AcquireKeyedLock(ctx, "shared-key")
	require.NoError(t, err)
	defer relA()

	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	relB, err := b.AcquireKeyedLock(waitCtx, "shared-key")
	require.NoError(t, err, "the same key in a different schema must be a different lock")
	relB()
}

// TestKeyedLockerFor_DiscoversCapability pins the type-assert discovery used at
// the wiring site.
func TestKeyedLockerFor_DiscoversCapability(t *testing.T) {
	_ = testDSN(t)
	st, err := pgstore.New(newScopedPool(t))
	require.NoError(t, err)

	require.NotNil(t, pgstore.KeyedLockerFor(st), "a pgstore must offer the keyed-lock capability")
	require.Nil(t, pgstore.KeyedLockerFor(struct{}{}), "an unrelated type must not")
}

// TestKeyedLock_CancelledAcquireDoesNotWedgeOrLeak pins the Hijack().Close()
// path, which has no in-process equivalent and so is invisible to locktest.
//
// A cancelled acquire may have been GRANTED as the cancellation arrived, so the
// connection is destroyed rather than pooled — a session-scoped lock dies with
// its session, releasing the key. Two things can go wrong and only a live
// server shows either: the key stays held (wedged for the process lifetime), or
// the destroyed connections are not replaced and the pool drains. The loop runs
// more times than MaxConns so a leak exhausts the pool rather than passing by
// luck.
func TestKeyedLock_CancelledAcquireDoesNotWedgeOrLeak(t *testing.T) {
	_ = testDSN(t)
	st, err := pgstore.New(newScopedPool(t))
	require.NoError(t, err)
	ctx := context.Background()

	rel, err := st.AcquireKeyedLock(ctx, "wedge/probe")
	require.NoError(t, err)

	for range 10 {
		wctx, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
		_, waitErr := st.AcquireKeyedLock(wctx, "wedge/probe")
		require.Error(t, waitErr, "a waiter must not acquire a key another holder has")
		cancel()
	}

	rel()

	got, err := st.AcquireKeyedLock(ctx, "wedge/probe")
	require.NoError(t, err, "key wedged or pool exhausted after cancelled acquires")
	got()

	other, err := st.AcquireKeyedLock(ctx, "wedge/other")
	require.NoError(t, err, "pool drained by destroyed connections")
	other()
}

// TestKeyedLock_HoldersCannotStarveThePool pins the capacity cap on a real
// pool. Each held key pins one connection, so without the cap four holders on a
// MaxConns=2 pool would take every connection and the store write each holder
// makes under its lock would wait forever.
func TestKeyedLock_HoldersCannotStarveThePool(t *testing.T) {
	_ = testDSN(t)
	st, err := pgstore.New(newScopedPool(t))
	require.NoError(t, err)

	locker, ok := lock.For(st).(*lock.BackendLocker)
	require.True(t, ok, "a pgstore must get the backend locker")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const holders = 4
	errs := make(chan error, holders)
	for i := range holders {
		go func() {
			id := fmt.Sprintf("E-%d", i)
			release, acqErr := locker.Acquire(ctx, "starve/"+id)
			if acqErr != nil {
				errs <- acqErr
				return
			}
			defer release()
			errs <- st.CreateEntity(ctx, entity.New(id, "ticket"))
		}()
	}
	for range holders {
		require.NoError(t, <-errs, "a holder's write must not starve for a connection")
	}
}
