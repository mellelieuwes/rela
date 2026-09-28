package pgstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// keyedLockClassKey namespaces the caller-keyed advisory locks, distinct from
// the migrate / reconcile / migration / sweep keys because advisory locks are
// database-GLOBAL.
//
// Unlike those four, the second slot of the two-key form is NOT
// hashtext(current_schema()) but a hash of (schema, caller key): the caller's
// key is the whole point, and the schema is folded into that hash so tenants
// sharing a database still do not exclude each other (the BUG-CA3VY0 class).
const keyedLockClassKey int64 = 0x52_45_4c_4b // "RELK"

// AcquireKeyedLock takes the schema-scoped advisory lock named key, BLOCKING
// until it is held or ctx is done.
//
// It is the cross-process half of the internal/lock seam: two rela-server
// processes against one database contend here, where an in-process mutex gives
// nothing. Consumers take the lock.Locker interface; this is the postgres
// implementation behind it.
//
// Blocking is the difference from every other advisory lock in this package.
// TryMigrationLock and the sweep use pg_try_advisory_lock and skip when another
// holder is active — right when the other holder is doing the same work.
// A keyed lock guards a caller's own critical section, so skipping would drop
// that caller's work; it waits instead, bounded by ctx.
//
// The lock is session-scoped and pinned to ONE pool connection held until
// release — handing it back earlier would silently void the guarantee (the
// sweep-tick rule). A leaked release is therefore also a leaked connection,
// which is why callers must defer it and pass a ctx with a deadline.
//
// This is an optional store capability discovered by type-assert, like
// Formatter and the version capabilities — not part of store.Store.
func (s *Store) AcquireKeyedLock(ctx context.Context, key string) (release func(), err error) {
	pool, isPool := s.db.(*pgxpool.Pool)
	if !isPool {
		// A bare handle (unit-test store) has no pool to pin a session lock
		// to. Error rather than pretend: a caller that got this far chose the
		// store-backed lock and must not silently fall back to no exclusion.
		return nil, errors.New("pgstore: keyed lock requires a pgxpool-backed store")
	}
	if pool.Config().MaxConns < 2 {
		// The lock pins one connection for as long as it is held while the
		// caller's own writes acquire others from the same pool — at
		// pool_max_conns=1 that is a guaranteed self-deadlock, so refuse up
		// front with the remedy in the message.
		//
		// This guard covers ONE holder, not N. Because each held lock pins a
		// connection, C concurrent holders that also write need C+1 <= MaxConns
		// to make progress: at C == MaxConns every connection is a lock and the
		// writes wait for one that only frees on release. keyedSlots enforces
		// that by admitting at most half the pool as holders; further callers
		// wait for a slot, bounded by their ctx deadline. Prefer a conditional
		// write (unique: / If-Match) over a lock when the critical section is a
		// single write anyway.
		return nil, errors.New(
			"pgstore: keyed lock needs pool_max_conns >= 2 (the lock pins one connection while the caller's writes use others)")
	}

	freeSlot, err := takeKeyedSlot(ctx, s.keyedSlots)
	if err != nil {
		return nil, err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		freeSlot()
		return nil, fmt.Errorf("pgstore: keyed lock acquire connection: %w", err)
	}

	// pg_advisory_lock blocks server-side. pgx cancels the in-flight query when
	// ctx is done, which surfaces here as an error.
	//
	// A cancelled acquire is NOT reliably a lock that was never granted: the
	// server can grant it as the cancellation arrives, leaving the session
	// holding a lock this function is about to report as failed. Destroying the
	// connection rather than returning it to the pool is what makes that safe —
	// a session-scoped lock dies with its session, so the key is released
	// instead of being held by a pooled connection nobody knows is a holder.
	if _, err := conn.Exec(ctx,
		`SELECT pg_advisory_lock($1::int, hashtext(current_schema() || '/' || $2::text))`,
		keyedLockClassKey, key,
	); err != nil {
		conn.Hijack().Close(context.WithoutCancel(ctx))
		freeSlot()
		return nil, fmt.Errorf("pgstore: keyed lock %q: %w", key, err)
	}

	var releaseOnce sync.Once
	return func() {
		releaseOnce.Do(func() {
			defer freeSlot()
			// Detached ctx so the unlock still runs during shutdown; releasing
			// the connection would drop the session lock anyway, but an
			// explicit unlock keeps the pair symmetric and returns a clean
			// connection to the pool rather than one carrying a held lock.
			unlockCtx := context.WithoutCancel(ctx)
			if _, err := conn.Exec(unlockCtx,
				`SELECT pg_advisory_unlock($1::int, hashtext(current_schema() || '/' || $2::text))`,
				keyedLockClassKey, key,
			); err != nil {
				// A session lock dies with its session, so a failed unlock
				// cannot wedge the key — but the connection may still carry it,
				// so destroy rather than return it to the pool.
				slog.Warn("pgstore: keyed advisory unlock failed", "key", key, "error", err)
				conn.Hijack().Close(unlockCtx)
				return
			}
			conn.Release()
		})
	}, nil
}

// keyedLockSlots caps how many keyed locks one store holds at once: half the
// pool, at least one. Each holder pins a connection and needs another to
// write, so capping holders here leaves connections for their writes and for
// unrelated requests (see the C+1 <= MaxConns note in
// [Store.AcquireKeyedLock]). Nil for a store without a pool, which cannot take
// a keyed lock anyway.
func keyedLockSlots(db DBTX) chan struct{} {
	pool, isPool := db.(*pgxpool.Pool)
	if !isPool {
		return nil
	}
	return make(chan struct{}, max(1, int(pool.Config().MaxConns)/2))
}

// takeKeyedSlot waits for a free slot in slots and returns the function that
// frees it, or ctx's error if ctx ends first.
func takeKeyedSlot(ctx context.Context, slots chan struct{}) (func(), error) {
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("pgstore: keyed lock wait: %w", ctx.Err())
	}
}

// KeyedLocker is the optional store capability [Store.AcquireKeyedLock]
// implements. Declared here so a consumer can type-assert for it without
// importing internal/lock, and so a second postgres-shaped backend can satisfy
// it (the TKT-L3FNEN "discover by interface, not by concrete type" rule).
type KeyedLocker interface {
	AcquireKeyedLock(ctx context.Context, key string) (release func(), err error)
}

// KeyedLockerFor returns st as a [KeyedLocker], or nil when the store does not
// support cross-process keyed locking. Genuinely nil (not a typed nil) so a
// caller's nil-check falls back to the in-process locker.
func KeyedLockerFor(st any) KeyedLocker {
	if kl, ok := st.(KeyedLocker); ok {
		return kl
	}
	return nil
}
