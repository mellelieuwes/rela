package lock

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// KeyedLockBackend is the cross-process locking capability a store may offer,
// declared HERE at the call site so this package does not import a store
// implementation (the consumer-side-interface rule). pgstore.Store satisfies it
// via AcquireKeyedLock; the wiring site supplies it.
type KeyedLockBackend interface {
	AcquireKeyedLock(ctx context.Context, key string) (release func(), err error)
}

// BackendLocker adapts a [KeyedLockBackend] to [Locker].
//
// This is the tier that matters when several rela-server processes serve one
// database: an in-process mutex excludes nothing between them, whereas the
// backend's lock is visible to every process on that schema.
//
// Each key is first taken in an in-process [MemoryLocker], then in the
// backend. The backend lock pins a pool connection for as long as a caller
// waits for it, so without the local step N same-process waiters on one key
// would pin N connections and could drain the pool for unrelated work. With
// it, at most one connection per key per process waits on the backend.
//
// Distinct keys each pin their own connection. Capping how many are held at
// once is the backend's job, since only it knows its pool size.
type BackendLocker struct {
	backend KeyedLockBackend
	local   *MemoryLocker
}

// NewBackendLocker returns a Locker backed by b.
//
// Nil: rejected — a nil backend would produce a Locker that silently serializes
// nothing, which is the one failure mode a lock must never have.
func NewBackendLocker(b KeyedLockBackend) (*BackendLocker, error) {
	if b == nil {
		return nil, errors.New("lock: NewBackendLocker: backend is required")
	}
	return &BackendLocker{backend: b, local: NewMemoryLocker()}, nil
}

// Acquire implements [Locker].
func (l *BackendLocker) Acquire(ctx context.Context, key string) (func(), error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	// Check before delegating so an expired caller fails identically on both
	// tiers, rather than depending on whether the backend happens to notice.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("lock: acquire %q: %w", key, err)
	}
	releaseLocal, err := l.local.Acquire(ctx, key)
	if err != nil {
		return nil, err
	}
	releaseBackend, err := l.backend.AcquireKeyedLock(ctx, key)
	if err != nil {
		releaseLocal()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			releaseBackend()
			releaseLocal()
		})
	}, nil
}

// For returns the Locker suited to st: a [BackendLocker] when st offers
// [KeyedLockBackend] (pgstore, whose deployments may run several processes),
// a [MemoryLocker] otherwise (fs, memory and sqlite, which are single-process).
//
// st is typed any so this package does not import store; the check is the
// same capability type assertion appbuild uses for optional store features.
// Callers in one process that must exclude each other have to share the
// returned value: two MemoryLockers do not see each other's keys.
func For(st any) Locker {
	if b, ok := st.(KeyedLockBackend); ok {
		l, _ := NewBackendLocker(b) // b is non-nil: the assertion succeeded
		return l
	}
	return NewMemoryLocker()
}
