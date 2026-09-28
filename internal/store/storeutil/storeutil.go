// Package storeutil provides shared helpers for store.Store implementations.
//
// Functions here are used by both memstore and fsstore to avoid duplicating
// validation, filtering, and sorted-slice maintenance logic.
package storeutil

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// ValidateID is the store-boundary gate for entity IDs. It delegates to
// [entity.ValidateID], which owns the single ID grammar and documents why
// each rule exists.
//
// This function used to carry its own, looser rule (rejecting only empty,
// "--", path separators, and control characters). That was the validator
// actually enforced on every write path, while the stricter
// [entity.ValidateID] ran only on the manual-ID and rename paths — so
// generated IDs, the importer, and direct store writes could persist IDs
// containing spaces, shell metacharacters, or Unicode homoglyphs. The two
// have been collapsed into one rule (TKT-IZGF7T); do not reintroduce a
// store-local variant.
//
// It stays a named function rather than an alias for two reasons: it is the
// validity oracle for the storetest fuzz harness, and it prefixes errors with
// "store: " so a rejection surfaced from a backend names the layer that
// refused the write.
func ValidateID(id string) error {
	if err := entity.ValidateID(id); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// IsStateRef reports whether id is a serialized state reference (`ID@face`)
// rather than a bare entity id.
//
// memstore and fsstore key their entity index on [entity.FormatStateRef], so
// without this check a lookup by "TKT-1@draft" hits the draft row by
// coincidence of the key, while pgstore and sqlitestore match the string
// against the id column and find nothing. The coincidence hid four
// unparsed-address bugs from memstore-only tests (BUG-R1PQY9). Refusing the
// string in the entity getters makes every backend answer them the same: an
// address must be parsed with [entity.ParseStateRef] before it reaches a
// lookup. Other lookups keyed on the index (attachment existence checks) do
// not apply it yet.
func IsStateRef(id string) bool {
	return strings.Contains(id, entity.StateRefSeparator)
}

// ValidateRelationType rejects relation types that would cause
// relation-key collisions or storage hazards. The rules mirror
// [ValidateID]: relation types are embedded in the same
// from--type--to key format (so `--` collides), become path segments
// in fsstore relation filenames (so separators nest directories), and
// appear in the pgstore change-feed payload whose field separator is
// a control character (internal/store/pgstore/feed.go already
// documents that assumption). Previously each backend hand-rolled a
// subset of these checks inline; the shared rule is also the validity
// oracle for the storetest fuzz harness (TKT-PCLGGL).
func ValidateRelationType(relType string) error {
	if relType == "" {
		return errors.New("store: empty relation type")
	}
	if strings.Contains(relType, "--") {
		return fmt.Errorf("store: relation type %q contains consecutive dashes", relType)
	}
	if strings.ContainsAny(relType, "/\\") {
		return fmt.Errorf("store: relation type %q contains path separator", relType)
	}
	for i := range len(relType) {
		if relType[i] < 0x20 || relType[i] == 0x7f {
			return fmt.Errorf("store: relation type %q contains control character", relType)
		}
	}
	return nil
}

// ValidateProperty rejects property names that would cause
// attachment key collisions in the entityID/property format.
func ValidateProperty(prop string) error {
	if prop == "" {
		return errors.New("store: empty property name")
	}
	if strings.Contains(prop, "/") {
		return fmt.Errorf("store: property name %q contains slash", prop)
	}
	return nil
}

// FoldID returns the case-folded form of an entity ID, for use as an
// identity key rather than as a display or storage value.
//
// Two IDs that fold to the same value are ONE entity (BUG-3RCWNS). This is
// not a stylistic rule — it is forced by fsstore, which writes "<id>.md" and
// so inherits the host filesystem's case behavior: on macOS and Windows
// "abc" and "ABC" are the same file. memstore and pgstore
// (id TEXT COLLATE "C") are byte-exact and would otherwise keep them as two
// separate entities.
//
// The backends must agree, because entities move between them: migrating a
// project that holds both "abc" and "ABC" into fsstore would silently drop
// one, and `rela sync` between a byte-exact and a case-folding store has no
// defined convergence. Enforcing identity in the shared layer is what keeps
// the backends substitutable (FEAT-CO4YP); the conformance suite pins it via
// CreateRejectsCaseVariantID.
//
// Uses strings.ToLower rather than strings.EqualFold's full Unicode folding
// because entity IDs are ASCII by construction (see entity.ValidateID), so
// the simple mapping is total over the legal input set and, unlike Unicode
// folding, cannot map two distinct ASCII IDs together.
//
// Callers must keep storing the ORIGINAL id — this value is only ever a
// lookup key. Casing is preserved in the entity and on disk.
func FoldID(id string) string {
	return strings.ToLower(id)
}

// SortedInsert adds key to a sorted slice, maintaining sort order.
func SortedInsert(s []string, key string) []string {
	i, _ := slices.BinarySearch(s, key)
	return slices.Insert(s, i, key)
}

// CompareStateKeys orders two entity state keys ("id" or "id@face")
// as the TUPLE (bare id, face), which is what pgstore's
// `ORDER BY id ASC, face ASC` does. One ordering contract across all
// three backends (TKT-WAV8XP).
//
// Plain string comparison on the joined key is NOT equivalent, and the
// difference is a correctness bug rather than a cosmetic one. The
// separator '@' is 0x40 and the digits are 0x30-0x39, so '@' sorts AFTER
// any digit and a numerically-prefixed sibling lands INSIDE the family:
//
//	string order: PAGE-1  PAGE-10  PAGE-10@draft  PAGE-1@draft  PAGE-2
//	tuple order:  PAGE-1  PAGE-1@draft  PAGE-10  PAGE-10@draft  PAGE-2
//
// World resolution buffers one family and decides at end-of-family, and
// the fallback verdict is a decision about ABSENCE — so a family split
// across a page boundary yields a WRONG prime, not a slow one (an entity
// dropped under `otherwise: exclude`, or its default face served when a
// published row exists further down). With `id_type: sequential` that
// starts at ten entities.
//
// A comparator rather than a lower-sorting separator on purpose: a
// separator encodes the invariant into an ASCII accident that a future
// id-grammar change would break silently.
func CompareStateKeys(a, b string) int {
	aID, aPtr, _ := strings.Cut(a, entity.StateRefSeparator)
	bID, bPtr, _ := strings.Cut(b, entity.StateRefSeparator)
	if c := strings.Compare(aID, bID); c != 0 {
		return c
	}
	return strings.Compare(aPtr, bPtr)
}

// SortedInsertFunc adds key to a slice sorted by cmp.
func SortedInsertFunc(s []string, key string, cmp func(a, b string) int) []string {
	i, _ := slices.BinarySearchFunc(s, key, cmp)
	return slices.Insert(s, i, key)
}

// SortedRemoveFunc removes key from a slice sorted by cmp. The key must
// exist — callers should only call this after confirming presence.
func SortedRemoveFunc(s []string, key string, cmp func(a, b string) int) []string {
	i, found := slices.BinarySearchFunc(s, key, cmp)
	if !found {
		panic("storeutil: SortedRemoveFunc called with missing key: " + key)
	}
	return slices.Delete(s, i, i+1)
}

// EncodeCursor turns a sort key into an opaque pagination cursor.
// Callers MUST NOT parse cursors — round-trip only via DecodeCursor.
func EncodeCursor(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

// DecodeCursor recovers the sort key from a cursor produced by EncodeCursor.
// Returns an error for malformed input; an empty cursor decodes to "".
func DecodeCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", fmt.Errorf("store: invalid cursor: %w", err)
	}
	return string(b), nil
}

// PageKeys holds the result of a paginated key scan: the sort keys that
// landed on this page (in order) and a cursor pointing past the last
// emitted key when more results exist.
type PageKeys struct {
	Keys       []string
	NextCursor string
}

// PaginateSortedKeys walks a pre-sorted slice of keys, selecting the
// next page of keys that satisfy match. It starts strictly after
// cursorKey — the key returned as the previous NextCursor is not
// re-emitted. When limit <= 0, every matching key is returned and
// NextCursor is "".
//
// NextCursor is set iff a matching key exists after the last emitted
// key, so an empty NextCursor is a reliable "no more results" signal.
// This costs one extra match call past the cut-off on the final page.
//
// Callers load the concrete items from the returned keys — keeping
// loads out of this helper lets each backend handle load errors in
// its own idiom (skip missing for in-memory, propagate I/O errors
// for fsstore).
func PaginateSortedKeys(
	sortedKeys []string,
	cursorKey string,
	limit int,
	match func(key string) bool,
) PageKeys {
	start := 0
	if cursorKey != "" {
		i, found := slices.BinarySearch(sortedKeys, cursorKey)
		start = i
		if found {
			start = i + 1
		}
	}

	page := PageKeys{}
	for i := start; i < len(sortedKeys); i++ {
		key := sortedKeys[i]
		if !match(key) {
			continue
		}
		if limit > 0 && len(page.Keys) == limit {
			// Already full and found another match — more results exist.
			page.NextCursor = EncodeCursor(page.Keys[len(page.Keys)-1])
			return page
		}
		page.Keys = append(page.Keys, key)
	}
	return page
}

// PaginateSortedKeysFunc is [PaginateSortedKeys] over a slice sorted by
// cmp rather than by plain string order (TKT-WAV8XP; see
// [CompareStateKeys]).
//
// A cursor issued before the ordering changed is a MISMATCHED cursor,
// which [store.EntityReader.ListEntitiesPage] documents as
// implementation-defined — cursors are opaque and valid only for the
// same query on the same store. It restarts from the beginning rather
// than resuming at a binary-search position derived under the old
// ordering, because that position would silently SKIP rows, which a
// paging caller cannot distinguish from end-of-results. Same reasoning
// as the unparseable-cursor restart (TKT-DOFYR1 PR-B).
func PaginateSortedKeysFunc(
	sortedKeys []string,
	cursorKey string,
	limit int,
	match func(key string) bool,
	cmp func(a, b string) int,
) PageKeys {
	start := 0
	if cursorKey != "" {
		i, found := slices.BinarySearchFunc(sortedKeys, cursorKey, cmp)
		start = i
		if found {
			start = i + 1
		}
	}

	page := PageKeys{}
	for i := start; i < len(sortedKeys); i++ {
		key := sortedKeys[i]
		if !match(key) {
			continue
		}
		if limit > 0 && len(page.Keys) == limit {
			page.NextCursor = EncodeCursor(page.Keys[len(page.Keys)-1])
			return page
		}
		page.Keys = append(page.Keys, key)
	}
	return page
}

// SortedRemove removes key from a sorted slice.
// The key must exist — callers should only call this after confirming presence.
func SortedRemove(s []string, key string) []string {
	i, found := slices.BinarySearch(s, key)
	if !found {
		panic("storeutil: SortedRemove called with missing key: " + key)
	}
	return slices.Delete(s, i, i+1)
}

// StateTypeMismatchError is the shared rejection for a state whose type
// diverges from its family's (TKT-DOFYR1, design doc §6).
//
// The family invariant that survived BUG-HC6I2T. Its companion rule — "a
// non-default state requires the DEFAULT row" — went with the bare face: it
// made the zero coordinate a family's mandatory head, so a faced type could
// not store its first state without first storing a row belonging to no
// face. A family is now simply the rows sharing an id, and any one of them
// answers what type they are.
func StateTypeMismatchError(id string, p entity.Face, got, want string) error {
	return fmt.Errorf("state %s@%s type %q does not match the entity's type %q", id, p, got, want)
}

// MatchRelation returns true if a relation matches the given query.
//
// The tail-face filter is nil-permissive (TKT-DOFYR1): a nil
// q.FromFace matches every tail — identity edges and all states —
// which is today's behavior for faceless projects; non-nil matches
// by equality only (the store never inspects face contents).
func MatchRelation(r *entity.Relation, q store.RelationQuery) bool {
	return NewRelationMatcher(q)(r)
}

// NewRelationMatcher compiles q once — the EntityIDs set in particular —
// into a predicate for a scan. A loop over N relations must use this rather
// than [MatchRelation], which would rebuild the batch set per row.
func NewRelationMatcher(q store.RelationQuery) func(*entity.Relation) bool {
	var set map[string]struct{}
	if q.EntityIDs != nil {
		set = make(map[string]struct{}, len(q.EntityIDs))
		for _, id := range q.EntityIDs {
			set[id] = struct{}{}
		}
	}
	return func(r *entity.Relation) bool { return matchRelation(r, q, set) }
}

func matchRelation(r *entity.Relation, q store.RelationQuery, set map[string]struct{}) bool {
	if q.Type != "" && r.Type != q.Type {
		return false
	}
	if q.From != "" && r.From != q.From {
		return false
	}
	if q.FromFace != nil && r.FromFace != *q.FromFace {
		return false
	}
	if q.To != "" && r.To != q.To {
		return false
	}
	if q.EntityID != "" && !endpointMatches(r, q.Direction, func(id string) bool { return id == q.EntityID }) {
		return false
	}
	if q.EntityIDs != nil {
		if !endpointMatches(r, q.Direction, func(id string) bool { _, ok := set[id]; return ok }) {
			return false
		}
	}
	return true
}

// endpointMatches reports whether the endpoint(s) Direction selects on r
// satisfy want: the source for outgoing, the target for incoming, either
// for both.
func endpointMatches(r *entity.Relation, dir store.Direction, want func(id string) bool) bool {
	switch dir {
	case store.DirectionOutgoing:
		return want(r.From)
	case store.DirectionIncoming:
		return want(r.To)
	default: // DirectionBoth
		return want(r.From) || want(r.To)
	}
}

// ValidateEntityQuery rejects a query whose fields contradict each
// other. Today that is AllStates together with a non-default World
// (TKT-WAV8XP): AllStates is raw storage truth and world resolution is
// its opposite, so honoring both is impossible and honoring one
// silently is a precedence rule nobody remembers.
//
// It lives here rather than in each backend so every implementation
// inherits the same refusal — a backend that forgot the check would
// answer a contradictory query with a plausible-looking result, which
// is worse than an error. Pinned by the shared conformance suite.
func ValidateEntityQuery(q store.EntityQuery) error {
	if q.AllStates && !q.World.IsDefaultWorld() {
		return fmt.Errorf(
			"%w: AllStates and World are mutually exclusive — AllStates is raw storage "+
				"truth, a World resolves each entity to one state", store.ErrInvalidQuery)
	}
	return nil
}

// MatchEntityQuery reports whether an entity with the given type,
// bare id and face satisfies q's Type, IDs and AllStates filters.
// idSet must be pre-computed from q.IDs (see the backends' entityIDSet)
// and matches the BARE id, so IDs+AllStates selects every state of the
// listed entities.
//
// It takes the three fields rather than an *entity.Entity because
// fsstore matches against its in-memory index metadata, which
// deliberately holds no loaded entity — the previous byte-similar
// copies in fsstore and memstore had diverged in signature only.
//
// This is NOT world resolution and cannot be: a world picks at most one
// state per entity, which is a per-FAMILY ranked choice, so no
// per-row predicate can express it. World-scoped listing resolves
// primes separately, after matching.
//
// Under a non-default World the face filter is WIDENED rather than
// applied: every state of a family is a candidate, and resolution
// chooses among them afterwards. Keeping the default-only filter here
// would discard the very rows the chain selects, leaving a world able
// to return only default states — the failure would look like "the
// world does nothing" rather than an error.
func MatchEntityQuery(entityType, id string, p entity.Face, q store.EntityQuery, idSet map[string]bool) bool {
	if !q.AllStates && q.World.IsDefaultWorld() && !p.IsDefault() {
		return false
	}
	if q.Type != "" && entityType != q.Type {
		return false
	}
	if len(idSet) > 0 && !idSet[id] {
		return false
	}
	// FaceIn narrows the candidate set (TKT-O7R2A1). Nil means every face, so
	// a pre-faces caller is unaffected. Applied here — before any world
	// ranking — so an entity whose top-choice face is excluded falls through
	// to the next candidate rather than vanishing.
	if len(q.FaceIn) > 0 && !slices.Contains(q.FaceIn, p) {
		return false
	}
	return true
}

// WorldCandidate is [store.WorldCandidate]; the fs/mem/sqlite listings build
// it from index metadata rather than loaded entities.
type WorldCandidate = store.WorldCandidate

// WorldPrimes selects, per bare id, the single state row that world w
// resolves to and returns the winning face per id — an id absent from the
// result contributes nothing. It is [store.ResolveWorldPrimes] with the
// provenance dropped, for the listing paths that only need the face; the
// rules live there, in ONE place, and the fs/mem index sorts by
// [CompareStateKeys] so callers can buffer a WHOLE family before deciding.
func WorldPrimes(w store.WorldScope, candidates []WorldCandidate) map[string]entity.Face {
	resolved := store.ResolveWorldPrimes(w, candidates)
	if resolved == nil {
		return nil
	}
	out := make(map[string]entity.Face, len(resolved))
	for id, r := range resolved {
		out[id] = r.Face
	}
	return out
}

// PaginateWorldPrimes walks a contiguously-ordered key slice and returns
// up to limit PRIMES, plus the cursor to resume after the last family it
// emitted (TKT-WAV8XP).
//
// The limit counts primes, not candidate rows: an entity holding three
// faces consumes one slot of a page, not three. Memory is bounded by ONE
// FAMILY, not by the result set — which is the whole reason the index
// sorts by [CompareStateKeys]. Buffering everything and cutting
// afterwards would be correct but O(all matching rows) per page, and a
// page-at-a-time scan that decided mid-family would produce a WRONG
// prime, since the fallback verdict is a decision about absence.
//
// load maps a key to its candidate; a key that is absent (concurrently
// deleted) is skipped.
//
// match MUST be family-constant — it may only test properties shared by
// every row of a family (type, bare id), never a per-row property. Rows
// it rejects never reach [WorldPrimes], and resolution decides on
// ABSENCE: filtering away the published row makes the chain look
// unsatisfied, so the fallback fires and the default face is served in a
// world that meant to replace or exclude it. [MatchEntityQuery] is
// family-constant today (Type and IDs only). A per-row predicate pushed
// in here — a property filter, an updated-at bound, a face fast path
// — silently breaks world resolution in the serve-the-wrong-face
// direction; resolve the family first and filter the primes afterwards.
func PaginateWorldPrimes(
	sortedKeys []string,
	cursorKey string,
	limit int,
	w store.WorldScope,
	match func(key string) bool,
	load func(key string) (WorldCandidate, bool),
) PageKeys {
	start := 0
	if cursorKey != "" {
		i, found := slices.BinarySearchFunc(sortedKeys, cursorKey, CompareStateKeys)
		start = i
		if found {
			start = i + 1
		}
		// The cursor names the last PRIME emitted, and a prime's family
		// has been decided in full — so resume at the next FAMILY, not the
		// next key. Resuming mid-family re-buffers the cursor entity's
		// remaining rows as a partial family, and a partial family resolves
		// to whatever it still contains: under `select: [draft, published]`
		// page 1 emits PAGE-1@draft and page 2 would emit PAGE-1@published,
		// the same entity twice with a lower-ranked face second.
		cursorID, _, _ := strings.Cut(cursorKey, entity.StateRefSeparator)
		for start < len(sortedKeys) {
			id, _, _ := strings.Cut(sortedKeys[start], entity.StateRefSeparator)
			if id != cursorID {
				break
			}
			start++
		}
	}

	page := PageKeys{}
	var famKeys []string
	var famCands []WorldCandidate

	// flush resolves one buffered family and appends its prime, if any.
	// Returns false when the page is full and a further prime exists.
	flush := func() bool {
		if len(famCands) == 0 {
			return true
		}
		primes := WorldPrimes(w, famCands)
		defer func() { famKeys, famCands = famKeys[:0], famCands[:0] }()
		for i, c := range famCands {
			p, ok := primes[c.ID]
			if !ok || p != c.Face {
				continue
			}
			if limit > 0 && len(page.Keys) == limit {
				page.NextCursor = EncodeCursor(page.Keys[len(page.Keys)-1])
				return false
			}
			page.Keys = append(page.Keys, famKeys[i])
		}
		return true
	}

	curID := ""
	for i := start; i < len(sortedKeys); i++ {
		key := sortedKeys[i]
		c, ok := load(key)
		if !ok {
			continue
		}
		// A family boundary: everything about the previous id has been
		// seen, so it is safe to decide its prime.
		if c.ID != curID {
			if !flush() {
				return page
			}
			curID = c.ID
		}
		if !match(key) {
			continue
		}
		famKeys = append(famKeys, key)
		famCands = append(famCands, c)
	}
	flush()
	return page
}

// LimitAttachmentReader wraps r so reads fail with
// store.ErrAttachmentTooLarge once they exceed store.MaxAttachmentBytes.
// This is the shared backstop guard every store backend applies to
// AttachFile, so no backend is ever unbounded regardless of caller.
// Thin alias over store.CapAttachmentReader at the backstop cap.
func LimitAttachmentReader(r io.Reader) io.Reader {
	return store.CapAttachmentReader(r, store.MaxAttachmentBytes)
}
