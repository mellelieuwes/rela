package fsstore

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/storeutil"
)

// --- EntityReader ---

func (s *FSStore) GetEntity(ctx context.Context, id string) (*entity.Entity, error) {
	// The bare id IS the default state's index key (stateKey(id, zero)).
	return s.GetEntityState(ctx, id, "")
}

func (s *FSStore) GetEntityState(_ context.Context, id string, p entity.Face) (*entity.Entity, error) {
	if storeutil.IsStateRef(id) {
		return nil, store.ErrNotFound
	}
	key := stateKey(id, p)
	s.mu.RLock()
	meta, ok := s.entities[key]
	s.mu.RUnlock()

	if !ok {
		return nil, store.ErrNotFound
	}
	return s.loadEntityMeta(meta)
}

func (s *FSStore) ListEntities(_ context.Context, q store.EntityQuery) iter.Seq2[*entity.Entity, error] {
	if err := storeutil.ValidateEntityQuery(q); err != nil {
		return func(yield func(*entity.Entity, error) bool) { yield(nil, err) }
	}
	s.mu.RLock()
	idSet := entityIDSet(q.IDs)

	// Collect matching metas from index.
	matches := make([]entityMeta, 0)
	for _, key := range s.entityOrder {
		if !matchEntityQuery(s.entities[key], q, idSet) {
			continue
		}
		matches = append(matches, s.entities[key])
	}
	s.mu.RUnlock()

	matches = keepPrimes(q.World, matches)

	return func(yield func(*entity.Entity, error) bool) {
		for _, m := range matches {
			e, err := s.loadEntityMeta(m)
			if err != nil {
				if !yield(nil, err) {
					return
				}
				continue
			}
			if !yield(e, nil) {
				return
			}
		}
	}
}

func (s *FSStore) ListEntitiesPage(_ context.Context, q store.EntityQuery) (store.Page[*entity.Entity], error) {
	if err := storeutil.ValidateEntityQuery(q); err != nil {
		return store.Page[*entity.Entity]{}, err
	}
	cursorKey, err := storeutil.DecodeCursor(q.Cursor)
	if err != nil {
		return store.Page[*entity.Entity]{}, err
	}

	s.mu.RLock()
	idSet := entityIDSet(q.IDs)
	matches := func(id string) bool { return matchEntityQuery(s.entities[id], q, idSet) }

	var keys storeutil.PageKeys
	if q.World.IsDefaultWorld() {
		keys = storeutil.PaginateSortedKeysFunc(
			s.entityOrder, cursorKey, q.Limit, matches, storeutil.CompareStateKeys)
	} else {
		// The world path buffers ONE family at a time and counts PRIMES
		// against the limit — see storeutil.PaginateWorldPrimes.
		keys = storeutil.PaginateWorldPrimes(
			s.entityOrder, cursorKey, q.Limit, q.World, matches,
			func(key string) (storeutil.WorldCandidate, bool) {
				m, ok := s.entities[key]
				if !ok {
					return storeutil.WorldCandidate{}, false
				}
				return storeutil.WorldCandidate{ID: m.ID, Type: m.Type, Face: m.Face}, true
			})
	}

	// Capture metas while still holding the lock — loading needs the
	// type and face and we must not do I/O under the lock.
	pairs := make([]entityMeta, 0, len(keys.Keys))
	for _, key := range keys.Keys {
		if meta, ok := s.entities[key]; ok {
			pairs = append(pairs, meta)
		}
	}
	s.mu.RUnlock()

	items := make([]*entity.Entity, 0, len(pairs))
	for _, p := range pairs {
		e, err := s.loadEntityMeta(p)
		if err != nil {
			return store.Page[*entity.Entity]{}, err
		}
		items = append(items, e)
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

// matchEntityQuery adapts this backend's index metadata to the shared
// rule in storeutil. The rule itself must not live here: memstore
// applies the identical filter, and the two byte-similar copies this
// replaced are exactly the drift storetest exists to catch.
func matchEntityQuery(m entityMeta, q store.EntityQuery, idSet map[string]bool) bool {
	return storeutil.MatchEntityQuery(m.Type, m.ID, m.Face, q, idSet)
}

func (s *FSStore) CountEntities(_ context.Context, q store.EntityQuery) (int, error) {
	if err := storeutil.ValidateEntityQuery(q); err != nil {
		return 0, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	idSet := entityIDSet(q.IDs)

	// The default world resolves every row to itself, so counting needs
	// no buffer — and this is the common path for a project that never
	// declares a face, which must stay allocation-free.
	if q.World.IsDefaultWorld() {
		n := 0
		for _, meta := range s.entities {
			if matchEntityQuery(meta, q, idSet) {
				n++
			}
		}
		return n, nil
	}

	matches := make([]entityMeta, 0)
	for _, meta := range s.entities {
		if matchEntityQuery(meta, q, idSet) {
			matches = append(matches, meta)
		}
	}
	// Counts must be world-scoped, not raw: an unscoped tally tells a
	// published-world surface how many unpublished drafts exist.
	return len(keepPrimes(q.World, matches)), nil
}

// keepPrimes resolves a world over matched index metadata, returning
// only each entity's prime. A no-op for the default world.
func keepPrimes(w store.WorldScope, metas []entityMeta) []entityMeta {
	if w.IsDefaultWorld() || len(metas) == 0 {
		return metas
	}
	cands := make([]storeutil.WorldCandidate, len(metas))
	for i, m := range metas {
		cands[i] = storeutil.WorldCandidate{ID: m.ID, Type: m.Type, Face: m.Face}
	}
	primes := storeutil.WorldPrimes(w, cands)
	kept := make([]entityMeta, 0, len(primes))
	for _, m := range metas {
		if p, ok := primes[m.ID]; ok && p == m.Face {
			kept = append(kept, m)
		}
	}
	return kept
}

func (s *FSStore) HighestID(_ context.Context, prefix string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	highest := 0
	pfx := prefix + "-"
	for _, meta := range s.entities {
		// Every face is scanned. States share their family's number, so
		// seeing a family more than once is harmless — max is idempotent —
		// while skipping non-default faces made a faced type invisible to
		// the generator entirely (BUG-HC6I2T).
		id := meta.ID
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

func (s *FSStore) PropertyValues(_ context.Context, property string, limit int) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	counts, ok := s.propCache[property]
	if !ok {
		return []string{}, nil
	}

	return storeutil.TopValues(counts, limit), nil
}

// --- EntityWriter ---

// idTaken reports whether any key of index case-folds to the same identity as
// id. except is skipped so a rename can ask "does any OTHER entity claim this
// identity?" without self-colliding.
//
// Scans the existing index on each call rather than maintaining a second
// case-folded map: the entity index is mutated at several sites, and a
// parallel index would silently drift at whichever one a future change
// forgets to update. Runs only on create and rename, never on a read path.
//
// A free function rather than a method — FSStore is at its plimsoll
// max-methods line, and this needs no receiver state beyond the index.
// Callers must hold s.mu.
// idTaken folds on the BARE entity id (meta.ID): any state of an
// entity claims the whole identity, so a new id collides with an
// existing family regardless of which states it has.
//
// except is a BARE id matched case-folded — it skips that entire
// family, not one exact key (a rename may change its own casing, and
// its states must not self-collide either). Wider than the historical
// exact-key skip on purpose.
// familyMember returns any stored state of a bare id, for the invariants
// that need one row of the family and do not care which (TKT-DOFYR1). Map
// iteration order makes the choice arbitrary, which is sound precisely
// because every state of a family shares the property being read.
func familyMember(index map[string]entityMeta, id string) (entityMeta, bool) {
	for _, m := range index {
		if m.ID == id {
			return m, true
		}
	}
	return entityMeta{}, false
}

func idTaken(index map[string]entityMeta, id, except string) bool {
	folded := storeutil.FoldID(id)
	exceptFolded := ""
	if except != "" {
		exceptFolded = storeutil.FoldID(except)
	}
	for _, meta := range index {
		if exceptFolded != "" && storeutil.FoldID(meta.ID) == exceptFolded {
			continue
		}
		if storeutil.FoldID(meta.ID) == folded {
			return true
		}
	}
	return false
}

func (s *FSStore) createEntity(_ context.Context, e *entity.Entity) error {
	if err := storeutil.ValidateID(e.ID); err != nil {
		return err
	}
	if err := storeutil.ValidateProperties(e.Properties); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := stateKey(e.ID, e.Face)
	if e.Face.IsDefault() {
		// Case-folded: on a case-insensitive filesystem (macOS, Windows)
		// "ABC" and "abc" are the same file, so a byte-exact check here
		// would let the write silently overwrite the existing entity
		// (BUG-3RCWNS).
		if idTaken(s.entities, e.ID, "") {
			return store.ErrConflict
		}
	} else {
		// Row-family invariant (TKT-DOFYR1, design doc §6): a state must
		// share the family's type. Enforced at the store so every direct
		// writer hits one choke point. Any sibling answers, since no face
		// heads a family (BUG-HC6I2T removed the rule that a state required
		// the zero-coordinate row).
		if sib, ok := familyMember(s.entities, e.ID); ok && sib.Type != e.Type {
			return storeutil.StateTypeMismatchError(e.ID, e.Face, e.Type, sib.Type)
		}
		if _, exists := s.entities[key]; exists {
			return store.ErrConflict
		}
	}

	// Write to disk
	stored := e.Clone()
	stored.UpdatedAt = time.Now()
	if err := s.writeEntity(stored); err != nil {
		return err
	}

	// Update index
	s.entities[key] = entityMeta{ID: e.ID, Type: e.Type, Face: e.Face}
	s.entityOrder = storeutil.SortedInsertFunc(s.entityOrder, key, storeutil.CompareStateKeys)
	if stored.Face.IsDefault() {
		addEntityToCache(s.propCache, stored)
	}
	s.notifyPut(stored)

	s.emit(store.Event{
		Op:         store.EventEntityCreated,
		EntityType: e.Type,
		EntityID:   e.ID,
		Face:       e.Face,
	})
	return nil
}

func (s *FSStore) updateEntity(ctx context.Context, e *entity.Entity) error {
	_, err := s.updateEntityIf(ctx, e, store.UpdateCondition{})
	return err
}

// updateEntityIf is the single update core: the version compare and the file
// write happen under ONE acquisition of s.mu, which is what makes this a
// compare-and-swap rather than a check-then-write.
//
// The compare uses the entity freshly loaded from DISK (`old`, which this
// method already needed for the prop-cache diff), not the in-memory index.
// That is deliberate and is the reason fsstore can support CAS at all: a hand
// edit or a `git pull` changes the file without any store write, and a token
// derived from the actual bytes catches that where a per-write counter would
// not.
func (s *FSStore) updateEntityIf(
	_ context.Context, e *entity.Entity, cond store.UpdateCondition,
) (store.EntityVersion, error) {
	if err := storeutil.ValidateProperties(e.Properties); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	key := stateKey(e.ID, e.Face)
	meta, exists := s.entities[key]
	if !exists {
		return "", store.ErrNotFound
	}
	// Row-family invariant: a non-default state cannot be re-typed away
	// from its family (TKT-DOFYR1, design doc §6).
	if !e.Face.IsDefault() && e.Type != meta.Type {
		return "", storeutil.StateTypeMismatchError(e.ID, e.Face, e.Type, meta.Type)
	}

	// Load old entity for prop cache diff — and, when conditional, for the
	// CAS compare.
	old, err := s.loadEntityMeta(meta)
	if err != nil {
		return "", err
	}

	if !cond.IsZero() {
		if actual := store.VersionOf(old); actual != cond.ExpectedVersion {
			return "", &store.VersionConflictError{
				ID: e.ID, Expected: cond.ExpectedVersion, Actual: actual,
			}
		}
	}

	stored := e.Clone()
	stored.UpdatedAt = time.Now()
	if err := s.writeEntity(stored); err != nil {
		return "", err
	}

	// The entity's type determines its file path, so a type change wrote the
	// record at the NEW type's path — the file at the old path must go too,
	// or it would resurrect the stale record on the next reload. Part of the
	// type-change-on-update store contract (storetest UpdateChangesType).
	if meta.Type != e.Type {
		oldKey := s.layout.entityFileKey(meta.Type, e.ID)
		if err := s.rooted.Remove(oldKey); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		s.echoes.Forget(s.layout.absPath(oldKey))
	}

	// Update index
	s.entities[key] = entityMeta{ID: e.ID, Type: e.Type, Face: e.Face}
	if e.Face.IsDefault() {
		removeEntityFromCache(s.propCache, old)
		addEntityToCache(s.propCache, stored)
	}
	s.notifyPut(stored)

	s.emit(store.Event{
		Op:         store.EventEntityUpdated,
		EntityType: e.Type,
		EntityID:   e.ID,
		Face:       e.Face,
	})
	return store.VersionOf(stored), nil
}

// forgetRelations drops index entries for relations whose files have been
// removed from disk. Used on the abort paths of a cascade delete so the
// in-memory view does not keep asserting the existence of files the caller is
// about to record as deleted (TKT-A23L87).
//
// A package function rather than a method on FSStore: the store sits on the
// plimsoll method load line, and this needs only the two index fields.
// Caller must hold s.mu.
func forgetRelations(s *FSStore, metas []relationMeta) {
	for _, rm := range metas {
		key := rm.key()
		delete(s.relations, key)
		s.relationOrder = storeutil.SortedRemove(s.relationOrder, key)
	}
}

// forgetStates drops index entries for state files already removed from disk,
// the entity-side counterpart to forgetRelations. Same reason: on a partial
// cascade delete the caller records these as deleted, so the index must stop
// listing them (TKT-A23L87). states is index-aligned with metas.
// Caller must hold s.mu.
func (s *FSStore) forgetStates(metas []entityMeta, states []*entity.Entity) {
	for i, meta := range metas {
		key := stateKey(meta.ID, meta.Face)
		delete(s.entities, key)
		s.entityOrder = storeutil.SortedRemoveFunc(s.entityOrder, key, storeutil.CompareStateKeys)
		if meta.Face.IsDefault() {
			removeEntityFromCache(s.propCache, states[i])
		}
		s.notifyFaceDelete(meta.ID, meta.Face)
	}
}

// stateFamily returns every indexed state of the bare id, sorted by
// face (default first), plus every relation touching the id on
// either endpoint. Defensive family scan (works for a headless family
// tolerated from disk, design doc §6); the bare From/To match sweeps
// every tail face. Callers must hold s.mu.
func (s *FSStore) stateFamily(id string) (family []entityMeta, related []relationMeta) {
	for _, meta := range s.entities {
		if meta.ID == id {
			family = append(family, meta)
		}
	}
	sort.Slice(family, func(i, j int) bool { return family[i].Face < family[j].Face })
	for _, rm := range s.relations {
		if rm.From == id || rm.To == id {
			related = append(related, rm)
		}
	}
	// s.relations is a map, so range order is randomized. Sort by the same
	// (from, type, to, face) identity the key is built from: a partially
	// failed cascade must remove — and therefore report — the same relations
	// in the same order on every run, or DeletedRelations is nondeterministic.
	sort.Slice(related, func(i, j int) bool {
		a, b := related[i], related[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.To != b.To {
			return a.To < b.To
		}
		return a.FromFace < b.FromFace
	})
	return family, related
}

func (s *FSStore) deleteEntity(_ context.Context, id string, cascade bool) (*store.DeleteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Delete addresses the whole state FAMILY of the bare id
	// (TKT-DOFYR1): every state row plus all their relations.
	family, related := s.stateFamily(id)
	if len(family) == 0 {
		return nil, store.ErrNotFound
	}

	if !cascade && len(related) > 0 {
		return nil, fmt.Errorf("%w: entity %s has %d relation(s)", store.ErrHasRelations, id, len(related))
	}

	// Load every state for the result and prop cache.
	states := make([]*entity.Entity, 0, len(family))
	for _, meta := range family {
		e, err := s.loadEntityMeta(meta)
		if err != nil {
			return nil, err
		}
		states = append(states, e)
	}

	// Load relations for result. Index-aligned with `related` — one append per
	// entry, no skips — which the removal loop below relies on to pair a
	// removed file with the relation value it represents. Keep it that way.
	deletedRelations := make([]*entity.Relation, 0, len(related))
	for _, rm := range related {
		r, loadErr := s.loadRelationMeta(rm)
		if loadErr != nil {
			r = entity.NewRelation(rm.From, rm.Type, rm.To)
			r.FromFace = rm.FromFace
		}
		deletedRelations = append(deletedRelations, r)
	}

	// Delete relation files first, then the entity files. A real removal
	// error (not "already gone") aborts before the entity files are touched
	// and before the in-memory index is mutated, so the entity is never
	// removed while one of its relations could not be — fail-secure, no
	// orphaned-from entity (BUG-C20T / issue #888). Not transactional: a
	// relation file removed before a later failure stays removed, but the
	// whole op runs under s.mu and the index is updated only on success.
	//
	// Because those removals stick, an abort returns a PARTIAL DeleteResult
	// alongside the error, naming exactly what came off disk (TKT-A23L87 /
	// issue #929). Without it the caller's audit loop reads a nil result and
	// the log denies a deletion that really happened.
	//
	// DeletedEntities stays empty on both abort paths below, and that is
	// exactly true rather than approximately: the relation loop aborts before
	// any entity file is touched, and the entity-file removal aborts on the
	// first failure, before that state is recorded as deleted. There is no
	// path that returns an error with a state file already gone unreported —
	// attachment-dir failure is non-fatal for precisely that reason (below).
	//
	// A file that was ALREADY gone (os.IsNotExist) is not counted — this call
	// did not delete it, and auditing it would over-report.
	removed := make([]*entity.Relation, 0, len(related))
	removedMeta := make([]relationMeta, 0, len(related))
	for i, rm := range related {
		key := s.layout.relationFileKeyMeta(rm)
		if err := s.rooted.Remove(key); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				// Drop the index entries for the files that DID come off disk
				// before handing back the partial result. The caller is about
				// to record those deletions in the audit log, and an index
				// still listing them would have ListRelations returning rows
				// the log says are gone — a live contradiction that outlives
				// the failed call (TKT-A23L87). The entity index is untouched:
				// the entity files are still there.
				forgetRelations(s, removedMeta)
				return &store.DeleteResult{DeletedRelations: removed},
					fmt.Errorf("delete relation file %s: %w", rm.key(), err)
			}
			// Already gone: not deleted BY US, so not reported and not counted.
			// Worth a signal — it means the index and disk had drifted before
			// this call, which nothing else would surface.
			slog.Warn("fsstore: relation file already absent during cascade delete",
				"from", rm.From, "type", rm.Type, "to", rm.To)
		} else {
			removed = append(removed, deletedRelations[i])
			removedMeta = append(removedMeta, rm)
		}
		s.echoes.Forget(s.layout.absPath(key))
	}
	// Same partial-result contract for the state files: a family can be
	// several files, and a failure on the second one leaves the first
	// genuinely deleted. Report the states that came off disk rather than
	// nil, and forget them in the index so it does not keep asserting rows
	// the audit log has recorded as gone (TKT-A23L87).
	removedStates := make([]*entity.Entity, 0, len(family))
	removedFamily := make([]entityMeta, 0, len(family))
	for i, meta := range family {
		key := s.layout.entityFileKey(meta.Type, stateKey(meta.ID, meta.Face))
		if err := s.rooted.Remove(key); err != nil && !errors.Is(err, os.ErrNotExist) {
			forgetRelations(s, removedMeta)
			s.forgetStates(removedFamily, removedStates)
			return &store.DeleteResult{
				DeletedEntities:  removedStates,
				DeletedRelations: removed,
			}, err
		}
		removedStates = append(removedStates, states[i])
		removedFamily = append(removedFamily, meta)
		s.echoes.Forget(s.layout.absPath(key))
	}

	// Cascade attachments — under the per-entity layout the attachment
	// directory is owned 1:1 by the entity.
	//
	// A failure here is NON-FATAL, deliberately. The entity file and every
	// relation file are already gone by this point, so the delete has
	// materially succeeded; returning an error would tell the caller the
	// operation failed and leave the audit log denying a deletion that
	// happened — the exact failure mode TKT-A23L87 exists to remove, one
	// layer down. An orphaned attachment directory is the lesser evil, and
	// `analyze` already reports orphaned attachment dirs.
	//
	// Note removeAttachmentDir prunes the in-memory attachment index BEFORE
	// any I/O that can fail, so the index is consistent either way.
	if err := s.removeAttachmentDir(id); err != nil {
		slog.Warn("fsstore: entity deleted but its attachment directory could not be removed",
			"entity", id, "error", err)
	}

	// Update index
	for i, meta := range family {
		key := stateKey(meta.ID, meta.Face)
		delete(s.entities, key)
		s.entityOrder = storeutil.SortedRemoveFunc(s.entityOrder, key, storeutil.CompareStateKeys)
		if meta.Face.IsDefault() {
			removeEntityFromCache(s.propCache, states[i])
		}
		s.notifyFaceDelete(meta.ID, meta.Face)
	}
	// The whole family went, so the bare-id observers hear one delete.
	s.notifyLastFaceDelete(id)

	for _, rm := range related {
		key := rm.key()
		delete(s.relations, key)
		s.relationOrder = storeutil.SortedRemove(s.relationOrder, key)
	}

	result := &store.DeleteResult{
		DeletedEntities:  states,
		DeletedRelations: deletedRelations,
	}
	s.emitFamilyDeleted(family, related)
	return result, nil
}

// deleteEntityState removes ONE face and only the edges belonging to it
// (TKT-C1XUA8). Contrast deleteEntity above, which sweeps the whole family
// and every incident edge on both sides.
//
// Keeps that function's fail-secure file ordering: relation files first,
// then the entity file, with a real removal error aborting before the
// in-memory index is touched.
func (s *FSStore) deleteEntityState(
	_ context.Context, id string, p entity.Face,
) (*store.DeleteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := stateKey(id, p)
	meta, ok := s.entities[key]
	if !ok {
		return nil, store.ErrNotFound
	}

	e, err := s.loadEntityMeta(meta)
	if err != nil {
		return nil, err
	}

	// OUTGOING edges on this tail go with the face. INCOMING edges do NOT:
	// heads are entity-level (§2.3), so an inbound edge points at the entity
	// and survives its faces.
	var owned []relationMeta
	for _, rm := range s.relations {
		if rm.From == id && rm.FromFace == p {
			owned = append(owned, rm)
		}
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].key() < owned[j].key() })

	deletedRelations := make([]*entity.Relation, 0, len(owned))
	for _, rm := range owned {
		r, loadErr := s.loadRelationMeta(rm)
		if loadErr != nil {
			r = entity.NewRelation(rm.From, rm.Type, rm.To)
			r.FromFace = rm.FromFace
		}
		deletedRelations = append(deletedRelations, r)
	}

	for _, rm := range owned {
		fileKey := s.layout.relationFileKeyMeta(rm)
		if rerr := s.rooted.Remove(fileKey); rerr != nil && !os.IsNotExist(rerr) {
			return nil, fmt.Errorf("delete relation file %s: %w", rm.key(), rerr)
		}
		s.echoes.Forget(s.layout.absPath(fileKey))
	}
	entKey := s.layout.entityFileKey(meta.Type, key)
	if rerr := s.rooted.Remove(entKey); rerr != nil && !os.IsNotExist(rerr) {
		return nil, rerr
	}
	s.echoes.Forget(s.layout.absPath(entKey))

	// The attachment directory is owned 1:1 by the ENTITY, not by a face, so
	// it only goes when the last face does — a discarded draft must not
	// destroy attachments the surviving faces serve.
	//
	// This runs BEFORE the index mutations below, matching deleteEntity's
	// fail-secure ordering: every fallible filesystem operation completes
	// first, so an error returns with the in-memory index still matching what
	// is on disk. Removing it afterwards would leave a caller who saw an
	// error with the entity already gutted from the index — a divergence
	// nothing reconciles until restart.
	lastFace := s.familySize(id) == 1
	if lastFace {
		if aerr := s.removeAttachmentDir(id); aerr != nil {
			return nil, aerr
		}
	}

	delete(s.entities, key)
	s.entityOrder = storeutil.SortedRemoveFunc(s.entityOrder, key, storeutil.CompareStateKeys)
	if p.IsDefault() {
		removeEntityFromCache(s.propCache, e)
	}
	for _, rm := range owned {
		rk := rm.key()
		delete(s.relations, rk)
		s.relationOrder = storeutil.SortedRemove(s.relationOrder, rk)
	}

	// Face-aware observers are told exactly which face went. Bare-id
	// observers can only express "the entity is gone", so they are
	// notified solely when the last face goes — otherwise they would
	// de-index an entity that still exists.
	s.notifyFaceDelete(id, p)
	if lastFace {
		s.notifyLastFaceDelete(id)
	}

	s.emitFamilyDeleted([]entityMeta{meta}, owned)
	return &store.DeleteResult{
		DeletedEntities:  []*entity.Entity{e},
		DeletedRelations: deletedRelations,
	}, nil
}

// familySize counts the indexed state rows of a bare id. Callers must hold
// s.mu.
func (s *FSStore) familySize(id string) int {
	n := 0
	for _, meta := range s.entities {
		if meta.ID == id {
			n++
		}
	}
	return n
}

// emitFamilyDeleted emits per-state entity-deleted events and per-edge
// relation-deleted events for a cascaded family delete. Each event
// carries its own state's type — the load path tolerates a mistyped
// state on disk, so famType-for-all would misreport it.
func (s *FSStore) emitFamilyDeleted(family []entityMeta, related []relationMeta) {
	for _, meta := range family {
		s.emit(store.Event{
			Op:         store.EventEntityDeleted,
			EntityType: meta.Type,
			EntityID:   meta.ID,
			Face:       meta.Face,
		})
	}
	for _, rm := range related {
		s.emit(store.Event{
			Op:           store.EventRelationDeleted,
			RelationType: rm.Type,
			From:         rm.From,
			To:           rm.To,
			Face:         rm.FromFace,
		})
	}
}

// rewriteRelationFiles rewrites every relation file in metas, moving
// endpoints from oldID to newID; the tail face rides along
// unchanged. Old files are removed after the new ones are written.
func (s *FSStore) rewriteRelationFiles(metas []relationMeta, oldID, newID string) error {
	for _, rm := range metas {
		r, loadErr := s.loadRelationMeta(rm)
		if loadErr != nil {
			r = entity.NewRelation(rm.From, rm.Type, rm.To)
			r.FromFace = rm.FromFace
		}
		if r.From == oldID {
			r.From = newID
		}
		if r.To == oldID {
			r.To = newID
		}
		if writeErr := s.writeRelation(r); writeErr != nil {
			return writeErr
		}
		oldKey := s.layout.relationFileKeyMeta(rm)
		_ = s.rooted.Remove(oldKey)
		s.echoes.Forget(s.layout.absPath(oldKey))
	}
	return nil
}

func (s *FSStore) renameEntity(_ context.Context, oldID, newID string) (*store.RenameResult, error) {
	if err := storeutil.ValidateID(newID); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Rename addresses the whole state FAMILY of the bare id
	// (TKT-DOFYR1): every state file is re-keyed onto the new id, and
	// every relation of every state follows.
	family, toUpdate := s.stateFamily(oldID)
	if len(family) == 0 {
		return nil, store.ErrNotFound
	}

	// except=oldID: an entity may change its own casing (abc -> ABC).
	// idTaken folds on the bare id, so ANY state of another entity under
	// the new id is a conflict.
	if idTaken(s.entities, newID, oldID) {
		return nil, store.ErrConflict
	}

	// Load, re-id, and write every state; remember the default state
	// for the observers.
	var renamedDefault *entity.Entity
	renamedStates := make([]*entity.Entity, 0, len(family))
	for _, meta := range family {
		e, err := s.loadEntityMeta(meta)
		if err != nil {
			return nil, err
		}
		renamed := e.Clone()
		renamed.ID = newID
		renamed.UpdatedAt = time.Now()
		if err := s.writeEntity(renamed); err != nil {
			return nil, err
		}
		renamedStates = append(renamedStates, renamed)
		if renamed.Face.IsDefault() {
			renamedDefault = renamed
		}
	}

	if err := s.rewriteRelationFiles(toUpdate, oldID, newID); err != nil {
		return nil, err
	}

	// Delete old entity files.
	for _, meta := range family {
		oldKey := s.layout.entityFileKey(meta.Type, stateKey(meta.ID, meta.Face))
		_ = s.rooted.Remove(oldKey)
		s.echoes.Forget(s.layout.absPath(oldKey))
	}

	// Move the attachment directory (if any) onto the new ID.
	if err := s.renameAttachmentDir(oldID, newID); err != nil {
		return nil, err
	}

	// Update entity index.
	for _, meta := range family {
		oldKey := stateKey(meta.ID, meta.Face)
		delete(s.entities, oldKey)
		s.entityOrder = storeutil.SortedRemoveFunc(s.entityOrder, oldKey, storeutil.CompareStateKeys)
		newKey := stateKey(newID, meta.Face)
		s.entities[newKey] = entityMeta{ID: newID, Type: meta.Type, Face: meta.Face}
		s.entityOrder = storeutil.SortedInsertFunc(s.entityOrder, newKey, storeutil.CompareStateKeys)
	}
	// EVERY face is renamed (TKT-9KZGJO). Indexes key documents per face, so
	// each one needs its own re-key; announcing only the default face would
	// strand every sibling under an id that no longer exists.
	//
	// The default face goes FIRST when it exists. An index implements
	// EntityRenamed as "drop the old family, insert this face", so the first
	// call does the removal and later ones add siblings back — starting with
	// a non-default face would work identically, but leading with the
	// family's identity row keeps the notification order matching every
	// other path.
	//
	// A headless family (tolerated from disk, design doc §6) has no default
	// face and is announced through the faces it does have. Under the Step-1
	// bare-id keying that had to notify NOTHING, since handing an observer a
	// non-default state would overwrite the bare-id document; per-face
	// keying removes that hazard.
	if renamedDefault != nil {
		s.notifyRenamed(oldID, renamedDefault)
	}
	for _, renamed := range renamedStates {
		if renamed.Face.IsDefault() {
			continue // already announced above
		}
		s.notifyRenamed(oldID, renamed)
	}

	// Update relation index.
	for _, rm := range toUpdate {
		oldKey := rm.key()
		delete(s.relations, oldKey)
		s.relationOrder = storeutil.SortedRemove(s.relationOrder, oldKey)

		newFrom, newTo := rm.From, rm.To
		if newFrom == oldID {
			newFrom = newID
		}
		if newTo == oldID {
			newTo = newID
		}
		newMeta := relationMeta{From: newFrom, Type: rm.Type, To: newTo, FromFace: rm.FromFace}
		newKey := newMeta.key()
		s.relations[newKey] = newMeta
		s.relationOrder = storeutil.SortedInsert(s.relationOrder, newKey)
	}

	for _, renamed := range renamedStates {
		s.emit(store.Event{
			Op:         store.EventEntityUpdated,
			EntityType: renamed.Type,
			EntityID:   newID,
			Face:       renamed.Face,
		})
	}

	return &store.RenameResult{RelationsUpdated: len(toUpdate)}, nil
}

// writeEntity writes an entity to disk using temp-file + rename.
func (s *FSStore) writeEntity(e *entity.Entity) error {
	return s.codec.writeEntityFile(e)
}
