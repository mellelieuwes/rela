// Package pgstore provides a PostgreSQL implementation of store.Store.
//
// # Connection ownership
//
// For the query path, pgstore does NOT own a connection or a DSN. New takes an
// injected [DBTX] handle — in production and in tests this is a *pgxpool.Pool.
// The wiring layer (internal/appbuild, behind //go:build postgres) builds the
// pool from the resolved DSN, runs [Migrate], and passes the pool here; it also
// owns closing the pool.
//
// A pool (not a single pgx.Tx) is required: the store opens its own
// transactions for multi-statement atomic operations (RenameEntity,
// cascade DeleteEntity) and must serve concurrent operations safely.
//
// # Change feed (cross-process)
//
// In addition to the injected query pool, a store opened via [Open] owns a
// change-feed listener (see feed.go / listener.go) holding its OWN dedicated
// connection, started in Open and stopped in Close. Each committed write emits
// a NOTIFY on the shared feed channel; the listener turns OTHER processes'
// notifications into store.Events on the same in-process Subscribe() fan-out as
// local writes, so multiple processes against one database see each other's
// changes. Store.Close stops the listener (closing its connection) before
// closing subscriber channels; it does not close the query pool.
//
// Event delivery is best-effort: a subscriber never sees an uncommitted write
// (emit and NOTIFY happen on/after commit), but it may MISS an event (full
// buffer, or a notification lost while disconnected). A seq-watermark catch-up
// recovers missed cross-process writes; consumers also re-snapshot. A store
// built with [New] (no listener wiring, e.g. the conformance harness) has only
// the in-process watcher.
//
// # Search
//
// The full-text search Backend lives in the same database (see search.go);
// the package doc on store.Store anticipates this ("smart backends ... provide
// native implementations sharing the same connection").
package pgstore

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/storeutil"
)

// DBTX is the subset of the pgx API pgstore needs. It is satisfied by
// *pgxpool.Pool, *pgx.Conn, and pgx.Tx. Production and tests inject a pool
// (see package doc for why a bare Tx is unsuitable).
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Store is a PostgreSQL-backed store.Store.
//
// The exported surface is the mandated store.Store interface (a Required-interface
// exception, so its count tracks the interface size) plus two version seams that
// own or hand out lifecycle state and so stay here: StartVersionSweep (the store's
// own reconciliation goroutine, owning s.mu/s.sweep) and VersionStore (a one-line
// accessor handing out the versioning service over this store's shared pool).
// Content VERSIONING itself (entity + relation history reads, version writes,
// purge) is NOT here: it lives in [VersionStore], a separate service injected by
// the composition root, because a store "just stores" and versioning is a distinct
// concern that merely shares the same database (TKT-N0IKN9). That extraction
// dropped this struct from 40→33 exported / 53→41 total.
//
// Do NOT add version/history/purge methods back to *Store: they belong on
// [VersionStore], and re-adding one here would resurrect the type-assert-off-the-
// store pattern this refactor removed. Consumers reach versioning through the
// injected store.VersionService, never by asserting a capability on the store.
//
// +1 for ListEntityHeaders (TKT-1ESTYJ): the store.HeaderReader capability,
// which omits the content column from the SELECT so entity bodies never
// leave the database on scans that only read ids and properties. It belongs
// on *Store — unlike versioning, it is a plain read of the entities table,
// not a separate service — and the whole point is the backend-native
// projection, which cannot be delegated elsewhere.
//
// [Store.UserState] (TKT-CXD0A4) is +1 again, and is deliberately the SAME
// shape as [Store.VersionStore]: a one-line factory handing the shared pool to
// a separate type, called once by the composition root. The subsystem's API
// lives on [UserStateStore]; only the factory is here, so the count grows by
// one per subsystem rather than by its surface. Adding UserStateStore's nine
// methods to *Store is what the paragraph above forbids.
//
// EntityTypeWatermark ([store.TypeWatermark]) is +1 for the row-property
// reason, not the factory one: it reads `max(seq)` over this store's OWN
// entities and deletions rows — the same tables every other method here touches
// — so hoisting it into a service would mean a second handle over those two
// tables for a single index lookup.
//
// That makes TWO capabilities admitted as row-properties (ListEntityHeaders and
// EntityTypeWatermark). A THIRD should not raise these numbers again: extract
// the row-property reads together, the way versioning and user-state were
// extracted as subsystems.
//
// TryMigrationLock (TKT-CPCBR7) is +1 for the row-property reason too: it is
// a session advisory lock that must be pinned to a connection from THIS
// store's pool and schema-qualified with THIS store's schema — a lock handle
// hoisted into a service would still need exactly this pool, gaining a type
// but no separation.
//
// +1 (38→39, TKT-DOFYR1): GetEntityState joined the mandated store.Store
// interface — the required-interface exception, not internal sprawl.
//
// (+2 methods / +2 exported with per-face delete, TKT-C1XUA8:
// DeleteEntityState and DeleteRelationState joined the mandated
// store.Store interface. Required-interface exception, not accreted
// API — the counts ratchet only if store.Store itself narrows.)
//
// (+1 exported, TKT-WRLDAPI: the world-scoped read surface joined the
// mandated store.Store interface on the same terms. Required-interface
// exception, not accreted API.)
//
// (+2 exported, TKT-1U8XYN: GraphQueryHeaders and CountMatched are OPTIONAL
// store capabilities — store.GraphHeaderQueryer, store.MatchedCounter — that
// consumers type-assert off the store handle, exactly as HeaderReader is.
// They have to live on this type to be discoverable that way; a second
// type would not be found by the assertion.)
//
// +1 exported / +2 methods (TKT-34XS2R): UpdateEntityIf joined the mandated
// store.Store interface, and the conditional core it shares with the
// unconditional path is the second. Required-interface exception again — the
// CAS precondition has to be evaluated atomically with the write, so it
// cannot live anywhere but on the type that owns the write.
//
// AcquireKeyedLock (TKT-1K47YD) is +1 for exactly the same reason: same pool,
// same schema qualification, differing only in that the caller names the key
// and the wait blocks. Consumers bind to the narrow lock.Locker seam rather
// than to this type, so the public API this adds to is the store-capability
// surface, not a new coupling point.
//
// GraphPosition (TKT-U9DYW4) is +1 as well: an optional store capability
// (store.PositionQueryer) that must live on the type holding the graph SQL
// builder, consumed through that one-method interface.
//
// +1 exported / +1 method (TKT-HH7PKJ): SwapRelationEndpoints is an OPTIONAL
// store capability (store.BulkMigrator) on the same terms as HeaderReader and
// the TKT-1U8XYN pair above — consumers reach it by type-asserting the store
// handle, so it has to live on this type or the assertion would not find it.
// The whole point of the capability is that the rewrite happens where the rows
// are: endpoints are a relation's identity, so a loop above the store must be
// create-then-delete, which forks version lineage and destroys a self-edge.
//
//plimsoll:max-exported-methods=49
//plimsoll:max-methods=59
type Store struct {
	db           DBTX
	searchTitles SearchTitles
	observers    []store.EntityObserver // notified synchronously after committed entity writes

	// Cross-process change feed (see feed.go / listener.go). Every process
	// LISTENs on one constant channel (feedChannel); these two fields are what
	// route a notification. originID identifies this store's own NOTIFY echoes
	// so the listener can skip them. schema is the schema this store's tables
	// live in, stamped into each payload so receivers on other schemas ignore
	// it — and doubling as the "feed is wired" flag: empty => the producer
	// no-ops, as for a New() store with no listener wiring.
	originID string
	schema   string
	listener *listener // nil unless a listener was started (see startListener)
	sweep    *sweep    // nil unless a version-reconciliation sweep was started

	// uniqueSpecs, when set (at wiring, via SetUniqueSpecProvider), yields the
	// current metamodel's (type, property) unique pairs. The write path uses it
	// to map a derived-unique-index violation (SQLSTATE 23505 on a
	// rela_derived_uniq__* index) back to the property name — see
	// mapUniqueViolation. nil on a New() store: violations then degrade to a
	// property-less UniquePropertyError (still a conflict), never a panic. Stored
	// as an atomic.Pointer so a metamodel reload can swap it without a lock and
	// the concurrent write path never sees a torn value.
	uniqueSpecs atomic.Pointer[[]store.DerivedObjectSpec]

	mu          sync.Mutex // guards subscribers + nextSubID only
	subscribers map[int]chan store.Event
	nextSubID   int
	closed      bool

	// txPending is nil on a normal store. On the tx-bound view a Tx
	// callback receives (tx.go), it buffers in-process observer and
	// subscriber notifications so they are delivered against the parent
	// store only after the outer transaction commits — a subscriber must
	// never observe a write that later rolls back.
	txPending *txPending

	// keyedSlots caps concurrently held keyed locks; see keyedLockSlots.
	keyedSlots chan struct{}
}

// Option configures a Store.
type Option func(*Store)

// WithObserver registers an entity observer notified after committed entity
// writes. A nil observer is dropped silently, matching memstore.WithObserver
// and the app.FSFactory.AddObserver contract, so callers can pass the result
// of an optional search-backend factory without a nil guard.
// WithSearchTitles sets the type-to-title-property map free-text ranking
// uses; see [SearchTitles]. Nil keeps the prefix ranking.
func WithSearchTitles(t SearchTitles) Option {
	return func(s *Store) { s.searchTitles = t }
}

func WithObserver(o store.EntityObserver) Option {
	return func(s *Store) {
		if o == nil {
			return
		}
		s.observers = append(s.observers, o)
	}
}

// compile-time interface check.
var _ store.Store = (*Store)(nil)

// Delegate ID/property validation to the shared helpers so pgstore behaves
// identically to memstore/fsstore. (Relation matching is expressed directly in
// SQL — see relationWhere — rather than via storeutil.MatchRelation.)
var (
	validateID       = storeutil.ValidateID
	validateProperty = storeutil.ValidateProperty
)

// New constructs a Store over the injected handle. It does not run
// migrations — call [Migrate] first (the wiring layer does this once at
// startup; tests do it per schema). Returns an error if db is nil.
func New(db DBTX, opts ...Option) (*Store, error) {
	if db == nil {
		return nil, errors.New("pgstore: nil DBTX")
	}
	s := &Store{
		db:          db,
		originID:    newOriginID(),
		subscribers: make(map[int]chan store.Event),
		keyedSlots:  keyedLockSlots(db),
	}
	// The producer's schema is left empty here (producer no-ops) until a
	// listener is started via Open, which resolves it via resolveSchema. A
	// store built with New and no listener (e.g. the conformance harness)
	// simply emits no cross-process notifications — its in-process watcher is
	// unaffected.
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// --- Freshness ---

// LastModified returns the newest updated_at across entities and relations,
// or the zero time if both tables are empty.
func (s *Store) LastModified(ctx context.Context) (time.Time, error) {
	const q = `
		SELECT max(updated_at) FROM (
			SELECT updated_at FROM entities
			UNION ALL
			SELECT updated_at FROM relations
		) t`
	var t *time.Time
	if err := s.db.QueryRow(ctx, q).Scan(&t); err != nil {
		return time.Time{}, err
	}
	if t == nil {
		return time.Time{}, nil
	}
	return *t, nil
}

// --- Watcher ---

// Subscribe registers a buffered event channel. Events are delivered by
// non-blocking send (dropped when the buffer is full), matching the
// store.Watcher contract.
func (s *Store) Subscribe(bufSize int) (events <-chan store.Event, cancel func()) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ch := make(chan store.Event, bufSize)
	id := s.nextSubID
	s.nextSubID++
	s.subscribers[id] = ch

	cancel = func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.subscribers[id]; ok {
			delete(s.subscribers, id)
			close(ch)
		}
	}
	return ch, cancel
}

// emit delivers an event to every subscriber via a non-blocking send. Delivery
// is intentionally LOSSY and UNORDERED across subscribers — a full subscriber
// buffer drops the event (matching the store.Watcher contract and memstore).
// It is called AFTER a write transaction commits — never while holding a DB
// transaction — so subscribers never observe uncommitted state.
func (s *Store) emit(ev store.Event) {
	if s.txPending != nil { // inside Tx: deliver after the outer commit (tx.go)
		s.txPending.add(func(p *Store) { p.emit(ev) })
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.subscribers {
		select {
		case ch <- ev:
		default:
			// drop — subscriber is slow
		}
	}
}

// emitAll delivers a batch of events in order (used by cascade delete and
// rename, which produce several events per operation).
func (s *Store) emitAll(evs []store.Event) {
	if s.txPending != nil { // inside Tx: deliver after the outer commit (tx.go)
		s.txPending.add(func(p *Store) { p.emitAll(evs) })
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range evs {
		for _, ch := range s.subscribers {
			select {
			case ch <- ev:
			default:
			}
		}
	}
}

// --- Lifecycle ---

// Close tears down the change-feed listener (if any) and the watcher: it stops
// the listener goroutine and closes its dedicated connection, then closes all
// subscriber channels. It does NOT close the injected pool — the wiring layer
// owns the pool's lifecycle.
//
// The listener is stopped FIRST (before subscriber channels close) so it can't
// emit onto a closing channel; it uses its own connection, not the pool, so
// closing it is independent of the pool's lifecycle.
func (s *Store) Close() error {
	// Stop the listener outside the lock — stop() blocks on the goroutine, which
	// may call emit() (which takes s.mu); holding the lock here would deadlock.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	l := s.listener
	s.listener = nil
	sw := s.sweep
	s.sweep = nil
	s.mu.Unlock()

	sw.stop() // nil-safe; blocks until the sweep goroutine exits
	l.stop()  // nil-safe; blocks until the listener goroutine exits

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, ch := range s.subscribers {
		close(ch)
		delete(s.subscribers, id)
	}
	return nil
}
