// Package lock is the named-mutual-exclusion seam: callers ask for a lock on a
// string key, do work, release. The backend is chosen at the wiring site per
// deployment tier, like [store.Store], state.KV and jobs.Queue.
//
// # Why keyed, and not another mutex
//
// Rela used to have one process-wide write mutex in dataentry, held for the
// duration of a Lua action. TKT-WE0S2K removed it. It was both too coarse and
// too narrow.
//
// Too coarse, because unrelated writes serialized against each other. A burst
// of concurrent requests that touched entirely different entities queued up
// behind one lock and timed out.
//
// Too narrow, because it was per-PROCESS. Several rela-server processes
// against one PostgreSQL database (see docs/postgres-backend.md) shared no
// mutex at all, so it provided nothing there.
//
// A keyed lock addresses both: operations contend only when they name the SAME
// key, and the postgres backend serializes across processes. That is the whole
// point of the seam, and the reason [Locker] takes a key rather than being a
// plain mutex — a backend that ignored the key would be a correct-looking
// implementation of a useless contract, which is why locktest asserts that
// distinct keys do NOT contend.
//
// Most writes do not need this seam: the store's compare-and-swap and
// [store.Transactor.Tx] cover them. The consumer is the attachment service,
// whose read-modify-write spans blob storage and so cannot sit inside a
// store transaction.
//
// # Not a distributed lock service
//
// This is mutual exclusion, not consensus. There are no fencing tokens, no
// leases, and no liveness guarantee: a postgres advisory lock dies with its
// session, which is the correct failure mode here — a crashed holder releases
// rather than wedging every other process forever. Callers must not assume a
// held lock proves the holder is still making progress.
package lock

import (
	"context"
	"errors"
	"strings"
)

// Locker provides named mutual exclusion.
//
// Nil: never returned by a constructor — a New* function returns an error
// rather than a nil Locker, so a caller cannot silently run unserialized.
type Locker interface {
	// Acquire blocks until the lock named key is held, ctx is done, or the
	// backend fails.
	//
	// Bounding the wait is the CALLER's job, via ctx: a request path should
	// pass a deadline so a slow holder surfaces as a timeout rather than an
	// unbounded queue. Acquire returns ctx.Err() (wrapped) when the wait is
	// cut short, and callers distinguish that with errors.Is.
	//
	// Blocking is deliberate, and is the one place this seam departs from
	// pgstore's existing advisory-lock callers. Those use
	// pg_try_advisory_lock and SKIP when another holder has it, which is
	// right for a reconciliation sweep (someone else is already doing the
	// work) and wrong for a request path (skipping means silently dropping
	// the caller's work). A try-variant can be added if a caller genuinely
	// wants it; it is deliberately not the default.
	//
	// The returned release is idempotent and safe to defer. It is non-nil
	// exactly when err is nil.
	//
	// A lock is NOT reentrant: acquiring the same key twice from one
	// goroutine deadlocks. The weaker contract is promised on purpose —
	// a session-scoped postgres advisory lock happens to be reentrant per
	// session while an in-process mutex is not, so guaranteeing reentrancy
	// would make the backends behave differently in a way callers would
	// come to rely on.
	Acquire(ctx context.Context, key string) (release func(), err error)
}

// ValidateKey reports whether key is acceptable to every [Locker] backend.
//
// Backends hash or namespace the key differently — postgres feeds it to
// hashtext, an in-memory backend uses it as a map key, a future filesystem
// backend would resolve it to a path. Holding every backend to one rule is what
// stops a key working on one tier and failing after a migration to another; the
// shared suite in lock/locktest exercises the same key table against all of
// them.
//
// The rules are state.ValidateKey's, minus the clauses that only bite when a
// key becomes a real path, plus a length bound. Concretely, this is STRICTLY
// WEAKER than state.ValidateKey: it accepts a colon ("a:b"), a Windows reserved
// name ("con", "nul.txt", "a/con/b") and rejects a leading slash only as an
// empty segment. Those three are FSKV concerns inherited from
// storage.RootedFS, and no Locker backend resolves a key to a path today.
//
// So a key valid here is NOT necessarily valid as a state key. A caller
// deriving both from one source must validate against state.ValidateKey, which
// is the stricter of the two — do not assume passing this implies passing that.
// If a filesystem-backed Locker is ever added, the missing clauses must come
// with it, and this comment must stop saying they are absent.
func ValidateKey(key string) error {
	if key == "" {
		return errors.New("lock: key must not be empty")
	}
	if len(key) > MaxKeyLen {
		return errors.New("lock: key too long")
	}
	for _, c := range key {
		if c < 0x20 || c == 0x7f {
			return errors.New("lock: control character (including NUL) not allowed in key")
		}
	}
	if strings.ContainsRune(key, '\\') {
		return errors.New("lock: backslash not allowed in key (use forward slash)")
	}
	for seg := range strings.SplitSeq(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return errors.New("lock: traversal or empty segment not allowed in key")
		}
	}
	return nil
}

// MaxKeyLen bounds a lock key. The limit is not a backend constraint —
// postgres hashes the key to a fixed-width int and a map key is unbounded — but
// a caller deriving a key from request data (a webhook payload, say) should hit
// a clear error rather than turning an attacker-supplied megabyte into a map
// entry held for the process lifetime.
const MaxKeyLen = 512
