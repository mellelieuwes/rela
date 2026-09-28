// Package memstore provides an in-memory implementation of store.Store.
//
// # Concurrency
//
// All state is protected by a single [sync.RWMutex]. Go maps are not safe
// for concurrent access — even a read during a concurrent write on a
// different key causes a runtime panic. The mutex therefore guards every
// map access, not individual entries.
//
// Write methods acquire mu.Lock; read methods acquire mu.RLock.
//
// Iterator methods (ListEntities, ListRelations, Search) snapshot matching
// results into a slice under the read lock, release the lock, then yield
// from the snapshot. This keeps lock duration proportional to the scan,
// not to whatever the caller does in the loop body.
//
// Event emission (emit) is called under mu.Lock. Subscribers receive
// events on buffered channels via non-blocking sends.
package memstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"iter"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/storeutil"
)

// MemStore is an in-memory store implementation.
//
// TODO(TKT-N0IKN9): the exported surface (28) is the mandated store.Store
// interface (incl. Tx, TKT-GXHI8), which consumers depend on directly by
// design. This is a required-interface exception, not accreted public API —
// it tracks the interface size and ratchets only if store.Store itself is
// narrowed.
//
// Total methods crossed the 40 line with the Tx contract (TKT-GXHI8):
// each store.Store write method split into an exported txMu wrapper +
// unexported core so Tx callbacks can re-enter. Interface-driven, not
// accreted sprawl; ratchets with the interface.
//
// +1 for ListEntityHeaders (TKT-1ESTYJ): the store.HeaderReader capability,
// a content-free ListEntities. Implemented natively rather than left to the
// generic fallback because the fallback would clone each entity — bodies
// included — only to drop the body immediately, which is the cost the
// capability exists to remove. Interface-driven like the Tx split above.
//
// (43 → 45 / 29 → 30 with content states, TKT-DOFYR1: GetEntityState
// joined the mandated store.Store interface; rekeyFamily serves the
// family-wide rename that contract requires.)
//
// (+2 methods / +2 exported with per-face delete, TKT-C1XUA8:
// DeleteEntityState and DeleteRelationState joined the mandated
// store.Store interface. Required-interface exception, not accreted
// API — the counts ratchet only if store.Store itself narrows.)
//
// +1 (TKT-9KZGJO): per-face index notification joined the observer
// dispatch when indexers stopped skipping non-default faces. One
// method, on the store that already owns observer fan-out — not
// accreted API.
//
// +1 exported / +2 methods (TKT-34XS2R): UpdateEntityIf joined the mandated
// store.Store interface, and the conditional core it shares with the
// unconditional path is the second. Required-interface exception again — the
// CAS precondition has to be evaluated atomically with the write, so it
// cannot live anywhere but on the type that owns the write.
//
//plimsoll:max-methods=54
//plimsoll:max-exported-methods=34
type MemStore struct {
	// txMu serializes an open Tx against ordinary writers: Tx holds it
	// for the whole callback, every exported write method takes it
	// briefly before mu (lock order: txMu → mu; see tx.go). Readers
	// never take it.
	txMu          sync.Mutex
	mu            sync.RWMutex
	entities      map[string]*entity.Entity   // ID -> entity
	entityOrder   []string                    // sorted entity IDs
	relations     map[string]*entity.Relation // key -> relation
	relationOrder []string                    // sorted relation keys
	attachments   map[string]*attachment      // "entityID/property" -> data
	subscribers   map[int]chan store.Event
	nextSubID     int
	observers     []store.EntityObserver // notified synchronously on entity writes
}

type attachment struct {
	entityID string
	property string
	fileName string
	data     []byte
}

// New creates a new in-memory store.
// Option configures a MemStore.
type Option func(*MemStore)

// WithObserver adds an entity observer that is notified on writes.
// A nil observer is dropped silently so callers can pass the result
// of an optional construction (e.g. a search-backend factory that
// returns nil on failure) without an extra nil guard at every call
// site. Matches the [app.FSFactory.AddObserver] contract.
func WithObserver(o store.EntityObserver) Option {
	return func(m *MemStore) {
		if o == nil {
			return
		}
		m.observers = append(m.observers, o)
	}
}

func New(opts ...Option) *MemStore {
	m := &MemStore{
		entities:    make(map[string]*entity.Entity),
		relations:   make(map[string]*entity.Relation),
		attachments: make(map[string]*attachment),
		subscribers: make(map[int]chan store.Event),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// LastModified scans stored entities and relations for the newest UpdatedAt.
func (m *MemStore) LastModified(_ context.Context) (time.Time, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var newest time.Time
	for _, e := range m.entities {
		if e.UpdatedAt.After(newest) {
			newest = e.UpdatedAt
		}
	}
	for _, r := range m.relations {
		if r.UpdatedAt.After(newest) {
			newest = r.UpdatedAt
		}
	}
	return newest, nil
}

// notifyPut announces every FACE (TKT-9KZGJO): the search indexers key
// documents per face, so a state write indexes its own document rather
// than overwriting the default one. Mirrors fsstore — one place per
// backend, not per observer.
func (m *MemStore) notifyPut(e *entity.Entity) {
	for _, o := range m.observers {
		_ = o.EntityPut(e)
	}
}

// notifyFaceDelete announces the removal of ONE face to the face-aware
// observers only; see [store.FaceObserver] for why the two observer
// kinds are mutually exclusive per delete.
func (m *MemStore) notifyFaceDelete(id string, p entity.Face) {
	for _, o := range m.observers {
		if fo, ok := o.(store.FaceObserver); ok {
			_ = fo.EntityFaceDelete(id, p)
		}
	}
}

// notifyLastFaceDelete tells the bare-id observers the entity is gone.
// Face-aware observers already had one call per face.
func (m *MemStore) notifyLastFaceDelete(id string) {
	for _, o := range m.observers {
		if _, ok := o.(store.FaceObserver); ok {
			continue
		}
		_ = o.EntityDelete(id)
	}
}

// notifyRenamed fans out a rename to all observers. The rename code
// path emits this INSTEAD OF the EntityDelete(oldID)+EntityPut(renamed)
// pair — see store.EntityObserver.EntityRenamed.
func (m *MemStore) notifyRenamed(oldID string, renamed *entity.Entity) {
	for _, o := range m.observers {
		_ = o.EntityRenamed(oldID, renamed)
	}
}

// compile-time interface check
var _ store.Store = (*MemStore)(nil)

// Delegate validation and sorted-slice helpers to storeutil.
var (
	validateID       = storeutil.ValidateID
	validateProperty = storeutil.ValidateProperty
	sortedInsert     = storeutil.SortedInsert
	sortedRemove     = storeutil.SortedRemove
)

// The ENTITY order is keyed by state key ("id" or "id@face") and must
// sort as the tuple (bare id, face) so an entity's states stay
// contiguous — see storeutil.CompareStateKeys for why plain string order
// splits a family. Relations keep plain string order; their keys are not
// state keys.
func entityInsert(s []string, key string) []string {
	return storeutil.SortedInsertFunc(s, key, storeutil.CompareStateKeys)
}

func entityRemove(s []string, key string) []string {
	return storeutil.SortedRemoveFunc(s, key, storeutil.CompareStateKeys)
}

// worldKeep resolves a world over matched entities, returning only each
// entity's prime. A no-op for the default world (TKT-WAV8XP).
func worldKeep(w store.WorldScope, matched []*entity.Entity) []*entity.Entity {
	if w.IsDefaultWorld() || len(matched) == 0 {
		return matched
	}
	cands := make([]storeutil.WorldCandidate, len(matched))
	for i, e := range matched {
		cands[i] = storeutil.WorldCandidate{ID: e.ID, Type: e.Type, Face: e.Face}
	}
	primes := storeutil.WorldPrimes(w, cands)
	kept := make([]*entity.Entity, 0, len(primes))
	for _, e := range matched {
		if p, ok := primes[e.ID]; ok && p == e.Face {
			kept = append(kept, e)
		}
	}
	return kept
}

// idTaken reports whether any key of index case-folds to the same identity as
// id. except is skipped so a rename can ask "does any OTHER entity claim this
// identity?" without self-colliding.
//
// Derived from the entities map on each call rather than kept as a second
// index: entity IDs are written at five sites here, and a parallel map would
// silently drift out of sync at whichever one a future change forgets. The
// scan is O(n) but runs only on create and rename, never on a read path.
//
// Callers must hold m.mu.
// A free function rather than a method — MemStore is at its plimsoll
// max-methods line, and this needs no receiver state beyond the index.
// idTaken folds on the BARE entity id: any state of an entity claims
// the whole identity (TKT-DOFYR1). except is a BARE id matched
// case-folded — it skips that entire family, not one exact key (wider
// than the historical exact-key skip on purpose; a renaming family and
// its states must not self-collide).
func idTaken(index map[string]*entity.Entity, id, except string) bool {
	folded := storeutil.FoldID(id)
	exceptFolded := ""
	if except != "" {
		exceptFolded = storeutil.FoldID(except)
	}
	for _, e := range index {
		if exceptFolded != "" && storeutil.FoldID(e.ID) == exceptFolded {
			continue
		}
		if storeutil.FoldID(e.ID) == folded {
			return true
		}
	}
	return false
}

// famEntry pairs a state's index key with the stored entity during a
// family-wide delete or rename (TKT-DOFYR1).
type famEntry struct {
	key string
	e   *entity.Entity
}

// --- EntityReader ---

func (m *MemStore) GetEntity(ctx context.Context, id string) (*entity.Entity, error) {
	// The bare id IS the default state's key (entity.FormatStateRef with
	// the zero face).
	return m.GetEntityState(ctx, id, "")
}

func (m *MemStore) GetEntityState(_ context.Context, id string, p entity.Face) (*entity.Entity, error) {
	if storeutil.IsStateRef(id) {
		return nil, store.ErrNotFound
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	e, ok := m.entities[entity.FormatStateRef(id, p)]
	if !ok {
		return nil, store.ErrNotFound
	}
	return e.Clone(), nil
}

func (m *MemStore) ListEntities(_ context.Context, q store.EntityQuery) iter.Seq2[*entity.Entity, error] {
	if err := storeutil.ValidateEntityQuery(q); err != nil {
		return func(yield func(*entity.Entity, error) bool) { yield(nil, err) }
	}
	m.mu.RLock()
	idSet := entityIDSet(q.IDs)

	snapshot := make([]*entity.Entity, 0)
	for _, id := range m.entityOrder {
		e := m.entities[id]
		if !matchEntityQuery(e, q, idSet) {
			continue
		}
		snapshot = append(snapshot, e.Clone())
	}
	m.mu.RUnlock()

	snapshot = worldKeep(q.World, snapshot)

	return func(yield func(*entity.Entity, error) bool) {
		for _, e := range snapshot {
			if !yield(e, nil) {
				return
			}
		}
	}
}

// ListEntityHeaders implements store.HeaderReader. Like ListEntities it
// snapshots under the read lock (so the iterator never holds it), but copies
// only the header fields — the body string is not carried into the snapshot,
// and Properties are cloned so a caller cannot mutate stored state through a
// header.
//
// The generic fallback would clone each ENTITY (bodies included) just to
// discard the content a moment later; this keeps a whole-store scan
// proportional to ids and properties rather than to markdown.
func (m *MemStore) ListEntityHeaders(
	_ context.Context, q store.EntityQuery,
) iter.Seq2[store.EntityHeader, error] {
	if err := storeutil.ValidateEntityQuery(q); err != nil {
		return func(yield func(store.EntityHeader, error) bool) { yield(store.EntityHeader{}, err) }
	}
	m.mu.RLock()
	idSet := entityIDSet(q.IDs)

	matched := make([]*entity.Entity, 0)
	for _, id := range m.entityOrder {
		e := m.entities[id]
		if !matchEntityQuery(e, q, idSet) {
			continue
		}
		matched = append(matched, e)
	}
	// Resolve BEFORE projecting: the world picks whole rows, and a
	// header carries the face that identifies which face it is.
	matched = worldKeep(q.World, matched)

	snapshot := make([]store.EntityHeader, 0, len(matched))
	for _, e := range matched {
		snapshot = append(snapshot, store.EntityHeader{
			ID:         e.ID,
			Type:       e.Type,
			Face:       e.Face,
			Properties: maps.Clone(e.Properties),
			UpdatedAt:  e.UpdatedAt,
		})
	}
	m.mu.RUnlock()

	return func(yield func(store.EntityHeader, error) bool) {
		for _, h := range snapshot {
			if !yield(h, nil) {
				return
			}
		}
	}
}

func (m *MemStore) ListEntitiesPage(_ context.Context, q store.EntityQuery) (store.Page[*entity.Entity], error) {
	if err := storeutil.ValidateEntityQuery(q); err != nil {
		return store.Page[*entity.Entity]{}, err
	}
	cursorKey, err := storeutil.DecodeCursor(q.Cursor)
	if err != nil {
		return store.Page[*entity.Entity]{}, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	idSet := entityIDSet(q.IDs)
	matches := func(id string) bool { return matchEntityQuery(m.entities[id], q, idSet) }

	var keys storeutil.PageKeys
	if q.World.IsDefaultWorld() {
		keys = storeutil.PaginateSortedKeysFunc(
			m.entityOrder, cursorKey, q.Limit, matches, storeutil.CompareStateKeys)
	} else {
		// The world path buffers ONE family at a time and counts PRIMES
		// against the limit — see storeutil.PaginateWorldPrimes.
		keys = storeutil.PaginateWorldPrimes(
			m.entityOrder, cursorKey, q.Limit, q.World, matches,
			func(key string) (storeutil.WorldCandidate, bool) {
				e, ok := m.entities[key]
				if !ok {
					return storeutil.WorldCandidate{}, false
				}
				return storeutil.WorldCandidate{ID: e.ID, Type: e.Type, Face: e.Face}, true
			})
	}

	items := make([]*entity.Entity, 0, len(keys.Keys))
	for _, id := range keys.Keys {
		if e, ok := m.entities[id]; ok {
			items = append(items, e.Clone())
		}
	}
	return store.Page[*entity.Entity]{Items: items, NextCursor: keys.NextCursor}, nil
}

// entityIDSet builds the ID-lookup set used by ListEntities and its page
// variant. Returns nil when ids is empty so callers can test with len().
func entityIDSet(ids []string) map[string]bool {
	if len(ids) == 0 {
		return nil
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// matchEntityQuery adapts a stored entity to the shared rule in
// storeutil. The rule itself must not live here: fsstore applies the
// identical filter, and the two byte-similar copies this replaced are
// exactly the drift storetest exists to catch. The nil guard stays
// local — it is this backend's map-miss, not part of the rule.
func matchEntityQuery(e *entity.Entity, q store.EntityQuery, idSet map[string]bool) bool {
	// coverage-ignore-start: defensive invariant: both callers look e up by an id from m.entityOrder, which the write
	// paths keep in lockstep with
	// m.entities — a nil entity here is impossible without a broken index
	if e == nil {
		return false
	}
	return storeutil.MatchEntityQuery(e.Type, e.ID, e.Face, q, idSet)
	// coverage-ignore-end
}

func (m *MemStore) CountEntities(_ context.Context, q store.EntityQuery) (int, error) {
	if err := storeutil.ValidateEntityQuery(q); err != nil {
		return 0, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	idSet := entityIDSet(q.IDs)

	// The default world resolves every row to itself, so counting needs
	// no buffer — and this is the common path for a project that never
	// declares a face, which must stay allocation-free.
	if q.World.IsDefaultWorld() {
		n := 0
		for _, e := range m.entities {
			if matchEntityQuery(e, q, idSet) {
				n++
			}
		}
		return n, nil
	}

	matched := make([]*entity.Entity, 0)
	for _, e := range m.entities {
		if matchEntityQuery(e, q, idSet) {
			matched = append(matched, e)
		}
	}
	// Counts must be world-scoped, not raw: an unscoped tally tells a
	// published-world surface how many unpublished drafts exist.
	return len(worldKeep(q.World, matched)), nil
}

func (m *MemStore) HighestID(_ context.Context, prefix string) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	highest := 0
	pfx := prefix + "-"
	for _, e := range m.entities {
		// Every face is scanned. States share their family's number, so
		// seeing a family more than once is harmless — max is idempotent —
		// while skipping non-default faces made a faced type invisible to
		// the generator entirely (BUG-HC6I2T).
		id := e.ID
		if !strings.HasPrefix(id, pfx) {
			continue
		}
		suffix := id[len(pfx):]
		var n int
		if _, err := fmt.Sscanf(suffix, "%d", &n); err == nil && n > highest {
			highest = n
		}
	}
	return highest, nil
}

func (m *MemStore) PropertyValues(_ context.Context, property string, limit int) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	counts := make(map[string]int)
	for _, e := range m.entities {
		if !e.Face.IsDefault() {
			continue // suggestion counts stay default-world (TKT-DOFYR1)
		}
		if v, ok := e.Properties[property]; ok {
			s := fmt.Sprintf("%v", v)
			if s != "" {
				counts[s]++
			}
		}
	}

	return storeutil.TopValues(counts, limit), nil
}

// --- EntityWriter ---

func (m *MemStore) createEntity(_ context.Context, e *entity.Entity) error {
	if err := validateID(e.ID); err != nil {
		return err
	}
	if err := storeutil.ValidateProperties(e.Properties); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	key := entity.FormatStateRef(e.ID, e.Face)
	if e.Face.IsDefault() {
		// Case-folded so "ABC" conflicts with an existing "abc" — on fsstore
		// they are one file, and the backends must agree on identity
		// (BUG-3RCWNS).
		if idTaken(m.entities, e.ID, "") {
			return store.ErrConflict
		}
	} else {
		// Row-family invariant (TKT-DOFYR1, design doc §6): one type per
		// family — same choke point as fsstore. Any sibling answers, since
		// no face heads a family (BUG-HC6I2T removed the rule that a state
		// required the zero-coordinate row).
		if sib, ok := familyMember(m.entities, e.ID); ok && sib.Type != e.Type {
			return storeutil.StateTypeMismatchError(e.ID, e.Face, e.Type, sib.Type)
		}
		if _, exists := m.entities[key]; exists {
			return store.ErrConflict
		}
	}

	stored := e.Clone()
	stored.Redacted = nil // per-reader ACL artifact, never content (RR-KBWJPV)
	stored.UpdatedAt = time.Now()
	m.entities[key] = stored
	m.entityOrder = entityInsert(m.entityOrder, key)
	m.notifyPut(stored)

	m.emit(store.Event{
		Op:         store.EventEntityCreated,
		EntityType: e.Type,
		EntityID:   e.ID,
		Face:       e.Face,
	})
	return nil
}

func (m *MemStore) updateEntity(ctx context.Context, e *entity.Entity) error {
	_, err := m.updateEntityIf(ctx, e, store.UpdateCondition{})
	return err
}

// updateEntityIf is the single update core: the compare and the write happen
// under ONE acquisition of m.mu, which is what makes this a compare-and-swap
// rather than the check-then-write it replaces. Splitting them — even into two
// methods on the same mutex — would reintroduce the race, because the lock
// would be released in between.
func (m *MemStore) updateEntityIf(
	_ context.Context, e *entity.Entity, cond store.UpdateCondition,
) (store.EntityVersion, error) {
	if err := storeutil.ValidateProperties(e.Properties); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	key := entity.FormatStateRef(e.ID, e.Face)
	existing, exists := m.entities[key]
	if !exists {
		return "", store.ErrNotFound
	}
	if !cond.IsZero() {
		// Versioned against the faced row (`existing`), not a bare-id lookup:
		// since content-states (TKT-DOFYR1) an id names a FAMILY, and comparing
		// against the default face would let a write to one face pass a
		// condition computed from another.
		if actual := store.VersionOf(existing); actual != cond.ExpectedVersion {
			return "", &store.VersionConflictError{
				ID: e.ID, Expected: cond.ExpectedVersion, Actual: actual,
			}
		}
	}
	// Row-family invariant: a non-default state cannot be re-typed away
	// from its family (TKT-DOFYR1, design doc §6).
	if !e.Face.IsDefault() && e.Type != existing.Type {
		return "", storeutil.StateTypeMismatchError(e.ID, e.Face, e.Type, existing.Type)
	}

	stored := e.Clone()
	stored.Redacted = nil // per-reader ACL artifact, never content (RR-KBWJPV)
	stored.UpdatedAt = time.Now()
	m.entities[key] = stored
	m.notifyPut(stored)

	m.emit(store.Event{
		Op:         store.EventEntityUpdated,
		EntityType: e.Type,
		EntityID:   e.ID,
		Face:       e.Face,
	})
	// Computed from `stored`, not from `e`: the stored copy is the one the
	// next reader will see, and it has had Redacted cleared.
	return store.VersionOf(stored), nil
}

func (m *MemStore) deleteEntity(_ context.Context, id string, cascade bool) (*store.DeleteResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Delete addresses the whole state FAMILY of the bare id
	// (TKT-DOFYR1); the scan is defensive so a headless family still
	// deletes cleanly.
	var family []famEntry
	for key, e := range m.entities {
		if e.ID == id {
			family = append(family, famEntry{key, e})
		}
	}
	if len(family) == 0 {
		return nil, store.ErrNotFound
	}
	sort.Slice(family, func(i, j int) bool { return family[i].e.Face < family[j].e.Face })

	// Find related relations; the bare From/To match sweeps every tail.
	var related []*entity.Relation
	for _, r := range m.relations {
		if r.From == id || r.To == id {
			related = append(related, r)
		}
	}

	if !cascade && len(related) > 0 {
		return nil, fmt.Errorf("%w: entity %s has %d relation(s)", store.ErrHasRelations, id, len(related))
	}

	result := &store.DeleteResult{}
	for _, fe := range family {
		result.DeletedEntities = append(result.DeletedEntities, fe.e.Clone())
		delete(m.entities, fe.key)
		m.entityOrder = entityRemove(m.entityOrder, fe.key)
		m.notifyFaceDelete(id, fe.e.Face)
	}
	// The whole family went, so the bare-id observers hear one delete.
	m.notifyLastFaceDelete(id)

	// Cascade attachments — 1:1 ownership.
	for key, a := range m.attachments {
		if a.entityID == id {
			delete(m.attachments, key)
		}
	}

	for _, r := range related {
		result.DeletedRelations = append(result.DeletedRelations, r.Clone())
		key := r.Key()
		delete(m.relations, key)
		m.relationOrder = sortedRemove(m.relationOrder, key)
	}

	for _, fe := range family {
		// Per-state type: the load path tolerates a mistyped state, so
		// one family-wide type would misreport it.
		m.emit(store.Event{
			Op:         store.EventEntityDeleted,
			EntityType: fe.e.Type,
			EntityID:   id,
			Face:       fe.e.Face,
		})
	}
	for _, r := range related {
		m.emit(store.Event{
			Op:           store.EventRelationDeleted,
			RelationType: r.Type,
			From:         r.From,
			To:           r.To,
			Face:         r.FromFace,
		})
	}

	return result, nil
}

// deleteEntityState removes ONE face and only the edges that belong to it
// (TKT-C1XUA8). Contrast deleteEntity above, which sweeps the whole family
// and every incident edge on both sides — reusing that here would make
// discarding a draft destroy the published face and its inbound links.
func (m *MemStore) deleteEntityState(
	_ context.Context, id string, p entity.Face,
) (*store.DeleteResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := entity.FormatStateRef(id, p)
	target, ok := m.entities[key]
	if !ok {
		return nil, store.ErrNotFound
	}

	// OUTGOING edges on this tail go with the face. INCOMING edges do NOT:
	// heads are entity-level (§2.3), so an inbound edge points at the entity
	// and survives its faces.
	var owned []*entity.Relation
	for _, r := range m.relations {
		if r.From == id && r.FromFace == p {
			owned = append(owned, r)
		}
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].Key() < owned[j].Key() })

	result := &store.DeleteResult{DeletedEntities: []*entity.Entity{target.Clone()}}
	delete(m.entities, key)
	m.entityOrder = entityRemove(m.entityOrder, key)

	// Attachments are keyed to the bare id, so they belong to the ENTITY.
	// Only sweep them when this was the last face standing.
	m.notifyFaceDelete(id, p)
	if familySize(m.entities, id) == 0 {
		for k, a := range m.attachments {
			if a.entityID == id {
				delete(m.attachments, k)
			}
		}
		m.notifyLastFaceDelete(id)
	}

	for _, r := range owned {
		result.DeletedRelations = append(result.DeletedRelations, r.Clone())
		rk := r.Key()
		delete(m.relations, rk)
		m.relationOrder = sortedRemove(m.relationOrder, rk)
	}

	m.emit(store.Event{
		Op:         store.EventEntityDeleted,
		EntityType: target.Type,
		EntityID:   id,
		Face:       p,
	})
	for _, r := range owned {
		m.emit(store.Event{
			Op:           store.EventRelationDeleted,
			RelationType: r.Type,
			From:         r.From,
			To:           r.To,
			Face:         r.FromFace,
		})
	}
	return result, nil
}

// familyMember returns any stored state of a bare id, for the invariants
// that need one row of the family and do not care which (TKT-DOFYR1). Map
// iteration order makes the choice arbitrary, which is sound precisely
// because every state of a family shares the property being read.
func familyMember(entities map[string]*entity.Entity, id string) (*entity.Entity, bool) {
	for _, e := range entities {
		if e.ID == id {
			return e, true
		}
	}
	return nil, false
}

// familySize counts the state rows of a bare id.
func familySize(entities map[string]*entity.Entity, id string) int {
	n := 0
	for _, e := range entities {
		if e.ID == id {
			n++
		}
	}
	return n
}

// rekeyFamily moves every state of a family onto newID — clones to
// avoid mutating stored objects — and returns the renamed states in
// family order. Callers must hold m.mu.
func (m *MemStore) rekeyFamily(family []famEntry, newID string) []*entity.Entity {
	renamed := make([]*entity.Entity, 0, len(family))
	for _, fe := range family {
		r := fe.e.Clone()
		r.ID = newID
		r.UpdatedAt = time.Now()
		newKey := entity.FormatStateRef(newID, r.Face)
		m.entities[newKey] = r
		delete(m.entities, fe.key)
		m.entityOrder = entityRemove(m.entityOrder, fe.key)
		m.entityOrder = entityInsert(m.entityOrder, newKey)
		renamed = append(renamed, r)
	}
	return renamed
}

func (m *MemStore) renameEntity(_ context.Context, oldID, newID string) (*store.RenameResult, error) {
	if err := validateID(newID); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Rename addresses the whole state FAMILY of the bare id
	// (TKT-DOFYR1); defensive scan so a headless family still renames.
	var family []famEntry
	for key, e := range m.entities {
		if e.ID == oldID {
			family = append(family, famEntry{key, e})
		}
	}
	if len(family) == 0 {
		return nil, store.ErrNotFound
	}
	sort.Slice(family, func(i, j int) bool { return family[i].e.Face < family[j].e.Face })

	// except=oldID: renaming an entity to a different casing of its own ID
	// (abc -> ABC) is legitimate and must not self-collide. idTaken folds
	// on the bare id, so any state of another entity conflicts.
	if idTaken(m.entities, newID, oldID) {
		return nil, store.ErrConflict
	}

	renamedStates := m.rekeyFamily(family, newID)
	var renamedDefault *entity.Entity
	for _, r := range renamedStates {
		if r.Face.IsDefault() {
			renamedDefault = r
		}
	}
	// EVERY face is renamed (TKT-9KZGJO). Indexes key documents per face, so
	// each needs its own re-key; announcing only the default face would
	// strand every sibling under an id that no longer exists. The default
	// face leads when present, matching fsstore — see the note there.
	if renamedDefault != nil {
		m.notifyRenamed(oldID, renamedDefault)
	}
	for _, r := range renamedStates {
		if r.Face.IsDefault() {
			continue // already announced above
		}
		m.notifyRenamed(oldID, r)
	}

	// Update relations — clone each affected relation
	relationsUpdated := 0
	toRemove := make([]string, 0)
	toAdd := make([]*entity.Relation, 0)
	for key, r := range m.relations {
		if r.From != oldID && r.To != oldID {
			continue
		}
		clone := r.Clone()
		if clone.From == oldID {
			clone.From = newID
		}
		if clone.To == oldID {
			clone.To = newID
		}
		toRemove = append(toRemove, key)
		toAdd = append(toAdd, clone)
		relationsUpdated++
	}
	for _, key := range toRemove {
		delete(m.relations, key)
		m.relationOrder = sortedRemove(m.relationOrder, key)
	}
	for _, r := range toAdd {
		newKey := r.Key()
		m.relations[newKey] = r
		m.relationOrder = sortedInsert(m.relationOrder, newKey)
	}

	// Re-key attachments owned by the renamed entity.
	reKey := make(map[string]*attachment)
	for key, a := range m.attachments {
		if a.entityID != oldID {
			continue
		}
		delete(m.attachments, key)
		a.entityID = newID
		reKey[attachmentKey(newID, a.property, a.fileName)] = a
	}
	maps.Copy(m.attachments, reKey)

	for _, renamed := range renamedStates {
		m.emit(store.Event{
			Op:         store.EventEntityUpdated,
			EntityType: renamed.Type,
			EntityID:   newID,
			Face:       renamed.Face,
		})
	}

	return &store.RenameResult{RelationsUpdated: relationsUpdated}, nil
}

// --- RelationReader ---

// tailKey addresses the edge of a triple carrying EXACTLY tail p. The tail
// is part of a relation's identity, so this is an address and not a filter:
// two tails on one triple are two relations (TKT-C1XUA8).
func tailKey(from string, p entity.Face, relType, to string) string {
	return (&entity.Relation{From: from, FromFace: p, Type: relType, To: to}).Key()
}

// defaultTailKey addresses the DEFAULT-tail edge of a triple. Get and
// update are default-tail-only (TKT-DOFYR1) — see
// store.RelationData.FromFace. Delete has the general form
// deleteRelationState since TKT-C1XUA8.
func defaultTailKey(from, relType, to string) string {
	return tailKey(from, "", relType, to)
}

func (m *MemStore) GetRelation(_ context.Context, from, relType, to string) (*entity.Relation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := defaultTailKey(from, relType, to)
	r, ok := m.relations[key]
	if !ok {
		return nil, store.ErrNotFound
	}
	return r.Clone(), nil
}

func (m *MemStore) ListRelations(_ context.Context, q store.RelationQuery) iter.Seq2[*entity.Relation, error] {
	m.mu.RLock()
	match := storeutil.NewRelationMatcher(q)
	snapshot := make([]*entity.Relation, 0)
	for _, key := range m.relationOrder {
		r := m.relations[key]
		if !match(r) {
			continue
		}
		snapshot = append(snapshot, r.Clone())
	}
	m.mu.RUnlock()

	return func(yield func(*entity.Relation, error) bool) {
		for _, r := range snapshot {
			if !yield(r, nil) {
				return
			}
		}
	}
}

func (m *MemStore) ListRelationsPage(_ context.Context, q store.RelationQuery) (store.Page[*entity.Relation], error) {
	cursorKey, err := storeutil.DecodeCursor(q.Cursor)
	if err != nil {
		return store.Page[*entity.Relation]{}, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	match := storeutil.NewRelationMatcher(q)
	keys := storeutil.PaginateSortedKeys(m.relationOrder, cursorKey, q.Limit, func(key string) bool {
		return match(m.relations[key])
	})

	items := make([]*entity.Relation, 0, len(keys.Keys))
	for _, key := range keys.Keys {
		if r, ok := m.relations[key]; ok {
			items = append(items, r.Clone())
		}
	}
	return store.Page[*entity.Relation]{Items: items, NextCursor: keys.NextCursor}, nil
}

func (m *MemStore) CountRelations(_ context.Context, q store.RelationQuery) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	match := storeutil.NewRelationMatcher(q)
	count := 0
	for _, key := range m.relationOrder {
		if match(m.relations[key]) {
			count++
		}
	}
	return count, nil
}

// --- RelationWriter ---

func (m *MemStore) createRelation(
	_ context.Context, from, relType, to string, data *store.RelationData,
) (*entity.Relation, error) {
	for _, id := range []string{from, to} {
		if err := validateID(id); err != nil {
			return nil, err
		}
	}
	if err := storeutil.ValidateRelationType(relType); err != nil {
		return nil, err
	}
	if data != nil {
		if err := storeutil.ValidateProperties(data.Properties); err != nil {
			return nil, err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	r := entity.NewRelation(from, relType, to)
	r.UpdatedAt = time.Now()
	if data != nil {
		r.FromFace = data.FromFace // tail face is identity (TKT-DOFYR1)
		r.Content = data.Content
		if data.Properties != nil {
			r.Properties = make(map[string]any, len(data.Properties))
			for k, v := range data.Properties {
				r.Properties[k] = entity.CloneValue(v)
			}
		}
	}

	key := r.Key()
	if _, exists := m.relations[key]; exists {
		return nil, store.ErrConflict
	}

	m.relations[key] = r
	m.relationOrder = sortedInsert(m.relationOrder, key)

	m.emit(store.Event{
		Op:           store.EventRelationCreated,
		RelationType: relType,
		From:         from,
		To:           to,
		Face:         r.FromFace,
	})
	return r.Clone(), nil
}

func (m *MemStore) updateRelation(
	ctx context.Context, from, relType, to string, data store.RelationData,
) (*entity.Relation, error) {
	return m.updateRelationState(ctx, from, "", relType, to, data)
}

// updateRelationState writes the edge with EXACTLY this tail. The tail is
// part of a relation's identity, so addressing the wrong one updates a
// different edge rather than failing (BUG-64MU2Q).
func (m *MemStore) updateRelationState(
	_ context.Context, from string, p entity.Face, relType, to string, data store.RelationData,
) (*entity.Relation, error) {
	if err := storeutil.ValidateProperties(data.Properties); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	key := tailKey(from, p, relType, to)
	r, ok := m.relations[key]
	if !ok {
		return nil, store.ErrNotFound
	}

	updated := r.Clone()
	updated.Content = data.Content
	if data.Properties != nil {
		updated.Properties = make(map[string]any, len(data.Properties))
		for k, v := range data.Properties {
			updated.Properties[k] = entity.CloneValue(v)
		}
	} else {
		updated.Properties = nil
	}
	updated.UpdatedAt = time.Now()
	m.relations[key] = updated

	m.emit(store.Event{
		Op:           store.EventRelationUpdated,
		RelationType: relType,
		From:         from,
		To:           to,
		Face:         p,
	})
	return updated.Clone(), nil
}

func (m *MemStore) deleteRelation(ctx context.Context, from, relType, to string) error {
	return m.deleteRelationState(ctx, from, "", relType, to)
}

// deleteRelationState removes the edge with EXACTLY this tail. The tail is
// part of a relation's identity, so addressing the wrong one deletes a
// different edge rather than failing (TKT-C1XUA8).
func (m *MemStore) deleteRelationState(
	_ context.Context, from string, p entity.Face, relType, to string,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := tailKey(from, p, relType, to)
	if _, ok := m.relations[key]; !ok {
		return store.ErrNotFound
	}
	delete(m.relations, key)
	m.relationOrder = sortedRemove(m.relationOrder, key)

	m.emit(store.Event{
		Op:           store.EventRelationDeleted,
		RelationType: relType,
		From:         from,
		To:           to,
		Face:         p,
	})
	return nil
}

// --- Attachments ---

// attachmentKey is the per-file index key: (entityID, property, fileName).
func attachmentKey(entityID, property, fileName string) string {
	return entityID + "/" + property + "/" + fileName
}

func (m *MemStore) attachFile(_ context.Context, entityID, property, fileName string, r io.Reader) error {
	if err := validateProperty(property); err != nil {
		return err
	}
	if err := store.ValidateFileName(fileName); err != nil {
		return err
	}

	// Backstop size guard (shared with fsstore): reject oversize bytes so
	// the in-memory backend is never unbounded. The API layer also caps
	// at ingress.
	data, err := io.ReadAll(storeutil.LimitAttachmentReader(r))
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.entities[entityID]; !ok {
		return store.ErrNotFound
	}

	// Append: key by (entityID, property, fileName). A same-name re-attach
	// replaces only that one file; sibling files on the property are
	// untouched. Cap/suffix policy lives in the write path, not here.
	m.attachments[attachmentKey(entityID, property, fileName)] = &attachment{
		entityID: entityID,
		property: property,
		fileName: fileName,
		data:     data,
	}
	return nil
}

func (m *MemStore) ReadAttachment(_ context.Context, entityID, property, fileName string) (io.ReadCloser, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	a, ok := m.attachments[attachmentKey(entityID, property, fileName)]
	if !ok {
		return nil, store.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(a.data)), nil
}

func (m *MemStore) deleteAttachment(_ context.Context, entityID, property, fileName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := attachmentKey(entityID, property, fileName)
	if _, ok := m.attachments[key]; !ok {
		return store.ErrNotFound
	}
	delete(m.attachments, key)
	return nil
}

func (m *MemStore) ListAttachments(_ context.Context, entityID string) ([]store.AttachmentInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, ok := m.entities[entityID]; !ok {
		return nil, store.ErrNotFound
	}

	var result []store.AttachmentInfo
	for _, a := range m.attachments {
		if a.entityID == entityID {
			result = append(result, store.AttachmentInfo{
				EntityID: a.entityID,
				Property: a.property,
				FileName: a.fileName,
				Size:     int64(len(a.data)),
			})
		}
	}
	return result, nil
}

// --- Watcher ---

func (m *MemStore) Subscribe(bufSize int) (events <-chan store.Event, cancel func()) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ch := make(chan store.Event, bufSize)
	id := m.nextSubID
	m.nextSubID++
	m.subscribers[id] = ch

	cancel = func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.subscribers[id]; ok {
			delete(m.subscribers, id)
			close(ch)
		}
	}
	return ch, cancel
}

// emit sends an event to all subscribers. Non-blocking: drops if full.
// Must be called under mu.Lock.
func (m *MemStore) emit(ev store.Event) {
	for _, ch := range m.subscribers {
		select {
		case ch <- ev:
		default:
			// drop — subscriber is slow
		}
	}
}

// --- Lifecycle ---

func (m *MemStore) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, ch := range m.subscribers {
		close(ch)
		delete(m.subscribers, id)
	}
	return nil
}
