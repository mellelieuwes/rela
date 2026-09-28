package storetest

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// RunStateTests is the content-states conformance suite (TKT-DOFYR1):
// addressing by (id, face), the write-path row-family invariants,
// default-vs-AllStates query semantics, relation tail faces in both
// match implementations, and the delete/rename cascades over a state
// family. These cases define the contract BEFORE the second backend
// implements — they are what keep storeutil.MatchRelation and pgstore's
// relationWhere from drifting.
func RunStateTests(t *testing.T, f Factory) {
	newState := func(t *testing.T, id, typ, ptr, title string) *entity.Entity {
		t.Helper()
		e := entity.New(id, typ)
		if ptr != "" {
			p, err := entity.ParseFace(ptr)
			require.NoError(t, err)
			e.Face = p
		}
		e.SetString("title", title)
		return e
	}
	mustCreate := func(t *testing.T, s store.Store, e *entity.Entity) {
		t.Helper()
		require.NoError(t, s.CreateEntity(ctx(), e))
	}
	ptr := func(t *testing.T, v string) entity.Face {
		t.Helper()
		p, err := entity.ParseFace(v)
		require.NoError(t, err)
		return p
	}

	t.Run("AddressByPair", func(t *testing.T) {
		s := f(t)
		mustCreate(t, s, newState(t, "PAGE-1", "page", "", "default face"))
		mustCreate(t, s, newState(t, "PAGE-1", "page", "draft", "draft face"))

		// Bare id = the default state.
		got, err := s.GetEntity(ctx(), "PAGE-1")
		require.NoError(t, err)
		assert.Equal(t, "default face", got.GetString("title"))
		assert.True(t, got.Face.IsDefault())

		// The pair addresses the state; the id stays bare on the result.
		draft, err := s.GetEntityState(ctx(), "PAGE-1", ptr(t, "draft"))
		require.NoError(t, err)
		assert.Equal(t, "PAGE-1", draft.ID, "the joined form must never leak into the id")
		assert.Equal(t, ptr(t, "draft"), draft.Face)
		assert.Equal(t, "draft face", draft.GetString("title"))

		// GetEntityState with the zero face ≡ GetEntity.
		def, err := s.GetEntityState(ctx(), "PAGE-1", "")
		require.NoError(t, err)
		assert.Equal(t, "default face", def.GetString("title"))

		// A missing state is ErrNotFound even though siblings exist.
		_, err = s.GetEntityState(ctx(), "PAGE-1", ptr(t, "published"))
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	// An address string is not an id. memstore and fsstore key their index on
	// entity.FormatStateRef, so before this was pinned they resolved
	// "PAGE-1@draft" to the draft row while pgstore and sqlitestore matched it
	// against the id column and found nothing. The memstore-only handler
	// tests passed against every unparsed-address bug (BUG-R1PQY9 is the
	// fourth); this makes every backend answer alike, so they fail instead.
	t.Run("AddressStringIsNotAnID", func(t *testing.T) {
		s := f(t)
		mustCreate(t, s, newState(t, "PAGE-9", "page", "", "default face"))
		mustCreate(t, s, newState(t, "PAGE-9", "page", "draft", "draft face"))

		_, err := s.GetEntity(ctx(), "PAGE-9@draft")
		assert.ErrorIs(t, err, store.ErrNotFound, "GetEntity takes a bare id")

		_, err = s.GetEntityState(ctx(), "PAGE-9@draft", "")
		assert.ErrorIs(t, err, store.ErrNotFound, "GetEntityState takes a bare id")

		// The parsed address reaches the row, through the shared helper too.
		got, err := store.GetEntityAt(ctx(), s, "PAGE-9@draft")
		require.NoError(t, err)
		assert.Equal(t, "draft face", got.GetString("title"))
		got, err = store.GetEntityAt(ctx(), s, "PAGE-9")
		require.NoError(t, err)
		assert.Equal(t, "default face", got.GetString("title"))
	})

	t.Run("FaceIsOpaqueToTheStore", func(t *testing.T) {
		// The store equality-matches face values, never inspects
		// them: an unusual-but-canonical coordinate round-trips
		// unchanged (the multi-axis guarantee, design doc §3.1).
		s := f(t)
		unusual := ptr(t, "x9-z2-a0")
		mustCreate(t, s, newState(t, "PAGE-2", "page", "", "default"))
		e := newState(t, "PAGE-2", "page", "", "odd face")
		e.Face = unusual
		mustCreate(t, s, e)

		got, err := s.GetEntityState(ctx(), "PAGE-2", unusual)
		require.NoError(t, err)
		assert.Equal(t, unusual, got.Face)
		assert.Equal(t, "odd face", got.GetString("title"))
	})

	// HighestID must see an entity that exists ONLY at a named face. It used
	// to scan `face = ''` to count each family once; a type declaring faces
	// stores no row there (BUG-HC6I2T), so the generator saw nothing and
	// minted one id for every entity of the type — two unrelated entities
	// sharing the id the ACL row gate keys on.
	t.Run("HighestIDSeesFacedEntities", func(t *testing.T) {
		s := f(t)
		mustCreate(t, s, newState(t, "PAGE-7", "page", "draft", "only at a face"))

		n, err := s.HighestID(ctx(), "PAGE")
		require.NoError(t, err)
		assert.Equal(t, 7, n, "an entity stored only at a named face must still claim its number")
	})

	t.Run("WriteInvariants", func(t *testing.T) {
		t.Run("NamedFaceNeedsNoZeroCoordinateRow", func(t *testing.T) {
			// A named face is a row in its own right (BUG-HC6I2T). The store
			// used to demand a zero-coordinate row first, which made the bare
			// coordinate a family's mandatory head and meant a type declaring
			// faces could not store its first state without also storing a row
			// belonging to no face.
			s := f(t)
			err := s.CreateEntity(ctx(), newState(t, "PAGE-3", "page", "draft", "no default"))
			require.NoError(t, err, "a named face is a row in its own right")

			got, gerr := s.GetEntityState(ctx(), "PAGE-3", "draft")
			require.NoError(t, gerr)
			assert.Equal(t, "no default", got.GetString("title"))

			_, gerr = s.GetEntity(ctx(), "PAGE-3")
			assert.Error(t, gerr, "nothing was written at the zero coordinate")
		})

		// The ZERO coordinate is checked too. The probe used to be gated on
		// `!e.Face.IsDefault()`, which was complete while every family had a
		// zero-coordinate row to be checked against; with no face privileged
		// (BUG-HC6I2T) a family can be created named-face-first, and then the
		// zero-coordinate create ran no type check at all.
		t.Run("TypeMismatchRejectedOnTheZeroCoordinate", func(t *testing.T) {
			s := f(t)
			mustCreate(t, s, newState(t, "PAGE-8", "page", "draft", "named first"))
			err := s.CreateEntity(ctx(), newState(t, "PAGE-8", "ticket", "", "wrong type"))
			require.Error(t, err, "states share their family's type, whichever face is written first")
		})

		t.Run("TypeMismatchRejected", func(t *testing.T) {
			// Any sibling answers what type a family is — no face heads it.
			s := f(t)
			mustCreate(t, s, newState(t, "PAGE-4", "page", "published", "sibling"))
			err := s.CreateEntity(ctx(), newState(t, "PAGE-4", "ticket", "draft", "wrong type"))
			require.Error(t, err, "states share their family's type")
		})

		t.Run("DuplicateStateConflicts", func(t *testing.T) {
			s := f(t)
			mustCreate(t, s, newState(t, "PAGE-5", "page", "", "default"))
			mustCreate(t, s, newState(t, "PAGE-5", "page", "draft", "draft"))
			err := s.CreateEntity(ctx(), newState(t, "PAGE-5", "page", "draft", "again"))
			assert.ErrorIs(t, err, store.ErrConflict)
		})

		t.Run("UpdateMissingStateNotFound", func(t *testing.T) {
			s := f(t)
			mustCreate(t, s, newState(t, "PAGE-6", "page", "", "default"))
			err := s.UpdateEntity(ctx(), newState(t, "PAGE-6", "page", "draft", "phantom"))
			assert.ErrorIs(t, err, store.ErrNotFound)
		})

		t.Run("UpdateStateCannotRetype", func(t *testing.T) {
			s := f(t)
			mustCreate(t, s, newState(t, "PAGE-7", "page", "", "default"))
			mustCreate(t, s, newState(t, "PAGE-7", "page", "draft", "draft"))
			err := s.UpdateEntity(ctx(), newState(t, "PAGE-7", "ticket", "draft", "retyped"))
			require.Error(t, err)
		})
	})

	t.Run("QueryScope", func(t *testing.T) {
		s := f(t)
		mustCreate(t, s, newState(t, "PAGE-8", "page", "", "default"))
		mustCreate(t, s, newState(t, "PAGE-8", "page", "draft", "draft"))
		mustCreate(t, s, newState(t, "PAGE-9", "page", "", "other"))

		// The zero-value query is today's semantics: default states only.
		defaults := collectIter(t, s.ListEntities(ctx(), store.EntityQuery{Type: "page"}))
		require.Len(t, defaults, 2)
		for _, e := range defaults {
			assert.True(t, e.Face.IsDefault())
		}
		n, err := s.CountEntities(ctx(), store.EntityQuery{Type: "page"})
		require.NoError(t, err)
		assert.Equal(t, 2, n)

		// AllStates is raw storage truth: every state row.
		all := collectIter(t, s.ListEntities(ctx(), store.EntityQuery{Type: "page", AllStates: true}))
		assert.Len(t, all, 3)
		n, err = s.CountEntities(ctx(), store.EntityQuery{Type: "page", AllStates: true})
		require.NoError(t, err)
		assert.Equal(t, 3, n)

		// IDs filters on the BARE id, so IDs+AllStates selects the family.
		family := collectIter(t, s.ListEntities(ctx(), store.EntityQuery{IDs: []string{"PAGE-8"}, AllStates: true}))
		assert.Len(t, family, 2)
	})

	t.Run("DefaultWorldAggregates", func(t *testing.T) {
		// PropertyValues and HighestID are default-world aggregates.
		// The count-ORDER assertion is the load-bearing part: with one
		// "open" default and two "closed" defaults, "closed" must sort
		// first — two states carrying "open" would flip that order if
		// states leaked into the counts.
		s := f(t)
		def := newState(t, "PAGE-20", "page", "", "default")
		def.SetString("status", "open")
		mustCreate(t, s, def)
		for _, p := range []string{"draft", "review"} {
			st := newState(t, "PAGE-20", "page", p, "state "+p)
			st.SetString("status", "open")
			mustCreate(t, s, st)
		}
		for _, id := range []string{"PAGE-21", "PAGE-22"} {
			e := newState(t, id, "page", "", "closed one")
			e.SetString("status", "closed")
			mustCreate(t, s, e)
		}

		vals, err := s.PropertyValues(ctx(), "status", 0)
		require.NoError(t, err)
		assert.Equal(t, []string{"closed", "open"}, vals,
			"states must not inflate suggestion counts (open would outrank closed)")

		high, err := s.HighestID(ctx(), "PAGE")
		require.NoError(t, err)
		assert.Equal(t, 22, high)
	})

	t.Run("RelationTails", func(t *testing.T) {
		s := f(t)
		mustCreate(t, s, newState(t, "PAGE-10", "page", "", "default"))
		mustCreate(t, s, newState(t, "PAGE-10", "page", "draft", "draft"))
		mustCreate(t, s, newState(t, "SPEC-1", "page", "", "target"))

		// Same triple, two tails: two distinct relations.
		_, err := s.CreateRelation(ctx(), "PAGE-10", "references", "SPEC-1", nil)
		require.NoError(t, err)
		_, err = s.CreateRelation(ctx(), "PAGE-10", "references", "SPEC-1",
			&store.RelationData{FromFace: ptr(t, "draft")})
		require.NoError(t, err)
		// The state-tailed duplicate conflicts like any relation.
		_, err = s.CreateRelation(ctx(), "PAGE-10", "references", "SPEC-1",
			&store.RelationData{FromFace: ptr(t, "draft")})
		assert.ErrorIs(t, err, store.ErrConflict)

		// nil FromFace = unfiltered (today's behavior): both edges.
		both := collectRelations(t, s, store.RelationQuery{From: "PAGE-10", Type: "references"})
		require.Len(t, both, 2)

		// A non-nil zero face matches the default tail only.
		var zero entity.Face
		defOnly := collectRelations(t, s, store.RelationQuery{
			From: "PAGE-10", Type: "references", FromFace: &zero,
		})
		require.Len(t, defOnly, 1)
		assert.True(t, defOnly[0].FromFace.IsDefault())

		// A face value matches that tail exactly — including through
		// the fs INDEXED query path (From+Type set), the silent-miss
		// class the relationMeta index fix guards.
		draft := ptr(t, "draft")
		draftOnly := collectRelations(t, s, store.RelationQuery{
			From: "PAGE-10", Type: "references", FromFace: &draft,
		})
		require.Len(t, draftOnly, 1)
		assert.Equal(t, draft, draftOnly[0].FromFace)

		// CountRelations agrees with ListRelations.
		n, err := s.CountRelations(ctx(), store.RelationQuery{From: "PAGE-10", Type: "references", FromFace: &draft})
		require.NoError(t, err)
		assert.Equal(t, 1, n)
	})

	t.Run("HyphenBearingIDsWithFaceTails", func(t *testing.T) {
		// Base ids may contain single hyphens freely; what keeps the
		// relation key/filename parse unambiguous is that BOTH grammars
		// forbid "--" (ValidateID's no-consecutive-hyphens rule and the
		// face grammar's single-hyphen-runs rule). A hyphenated id
		// with a hyphenated face must round-trip through the FROM
		// slot serialization intact.
		s := f(t)
		mustCreate(t, s, newState(t, "MY-PAGE-1", "page", "", "default"))
		mustCreate(t, s, newState(t, "MY-PAGE-1", "page", "draft-nl", "state"))
		mustCreate(t, s, newState(t, "OTHER-DOC-2", "page", "", "target"))

		fp := ptr(t, "draft-nl")
		_, err := s.CreateRelation(ctx(), "MY-PAGE-1", "refers-to", "OTHER-DOC-2",
			&store.RelationData{FromFace: fp})
		require.NoError(t, err)

		got := collectRelations(t, s, store.RelationQuery{From: "MY-PAGE-1", FromFace: &fp})
		require.Len(t, got, 1)
		assert.Equal(t, "MY-PAGE-1", got[0].From)
		assert.Equal(t, fp, got[0].FromFace)
		assert.Equal(t, "OTHER-DOC-2", got[0].To)
	})

	t.Run("DeleteCascadesTheFamily", func(t *testing.T) {
		s := f(t)
		mustCreate(t, s, newState(t, "PAGE-11", "page", "", "default"))
		mustCreate(t, s, newState(t, "PAGE-11", "page", "draft", "draft"))
		mustCreate(t, s, newState(t, "SPEC-2", "page", "", "target"))
		_, err := s.CreateRelation(ctx(), "PAGE-11", "references", "SPEC-2", nil)
		require.NoError(t, err)
		_, err = s.CreateRelation(ctx(), "PAGE-11", "references", "SPEC-2",
			&store.RelationData{FromFace: ptr(t, "draft")})
		require.NoError(t, err)
		_, err = s.CreateRelation(ctx(), "SPEC-2", "links", "PAGE-11", nil)
		require.NoError(t, err)

		res, err := s.DeleteEntity(ctx(), "PAGE-11", true)
		require.NoError(t, err)
		assert.Len(t, res.DeletedEntities, 2, "both states deleted")
		assert.Len(t, res.DeletedRelations, 3, "edges of every tail plus incoming")

		_, err = s.GetEntity(ctx(), "PAGE-11")
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = s.GetEntityState(ctx(), "PAGE-11", ptr(t, "draft"))
		assert.ErrorIs(t, err, store.ErrNotFound)
		left := collectRelations(t, s, store.RelationQuery{EntityID: "PAGE-11"})
		assert.Empty(t, left)
	})

	// --- Per-face delete (TKT-C1XUA8) -----------------------------------
	//
	// The contract these pin is the OPPOSITE of DeleteCascadesTheFamily
	// above, and the pair is deliberate: DeleteEntity addresses the bare id
	// and sweeps the family; DeleteEntityState addresses one face and leaves
	// the family standing. A backend that implemented the second as the
	// first would pass neither.

	t.Run("DeleteStateLeavesSiblingFacesStanding", func(t *testing.T) {
		s := f(t)
		mustCreate(t, s, newState(t, "PAGE-20", "page", "", "default"))
		mustCreate(t, s, newState(t, "PAGE-20", "page", "draft", "draft"))
		mustCreate(t, s, newState(t, "PAGE-20", "page", "published", "published"))

		res, err := s.DeleteEntityState(ctx(), "PAGE-20", ptr(t, "draft"))
		require.NoError(t, err)
		require.Len(t, res.DeletedEntities, 1, "exactly one face deleted")
		assert.Equal(t, ptr(t, "draft"), res.DeletedEntities[0].Face)

		_, err = s.GetEntityState(ctx(), "PAGE-20", ptr(t, "draft"))
		assert.ErrorIs(t, err, store.ErrNotFound, "the discarded face is gone")

		// The siblings must be untouched, contents included — a backend that
		// deleted the family would fail here rather than in a count.
		def, err := s.GetEntity(ctx(), "PAGE-20")
		require.NoError(t, err)
		assert.Equal(t, "default", def.GetString("title"))
		pub, err := s.GetEntityState(ctx(), "PAGE-20", ptr(t, "published"))
		require.NoError(t, err)
		assert.Equal(t, "published", pub.GetString("title"))
	})

	t.Run("DeleteStateTakesItsOwnEdgesButNotInboundOnes", func(t *testing.T) {
		s := f(t)
		mustCreate(t, s, newState(t, "PAGE-21", "page", "", "default"))
		mustCreate(t, s, newState(t, "PAGE-21", "page", "draft", "draft"))
		mustCreate(t, s, newState(t, "SPEC-5", "page", "", "target"))
		mustCreate(t, s, newState(t, "OTHER-5", "page", "", "other"))

		draft := ptr(t, "draft")
		// One edge per tail on the SAME triple: these are two relations.
		_, err := s.CreateRelation(ctx(), "PAGE-21", "references", "SPEC-5", nil)
		require.NoError(t, err)
		_, err = s.CreateRelation(ctx(), "PAGE-21", "references", "SPEC-5",
			&store.RelationData{FromFace: draft})
		require.NoError(t, err)
		// Inbound: belongs to the ENTITY, not to the draft face (§2.3).
		_, err = s.CreateRelation(ctx(), "OTHER-5", "links", "PAGE-21", nil)
		require.NoError(t, err)

		res, err := s.DeleteEntityState(ctx(), "PAGE-21", draft)
		require.NoError(t, err)
		require.Len(t, res.DeletedRelations, 1,
			"only the draft face's own outgoing edge goes with it")
		assert.Equal(t, draft, res.DeletedRelations[0].FromFace)

		// The default face's edge on the same triple SURVIVES. This is the
		// case the tail-dropping bug got wrong: it deleted this one.
		remaining := collectRelations(t, s, store.RelationQuery{From: "PAGE-21"})
		require.Len(t, remaining, 1)
		assert.True(t, remaining[0].FromFace.IsDefault(),
			"the default face's edge must survive its sibling's deletion")

		// Inbound edges survive: an entity elsewhere still points here.
		inbound := collectRelations(t, s, store.RelationQuery{To: "PAGE-21"})
		assert.Len(t, inbound, 1, "heads are entity-level, so inbound edges survive")
	})

	t.Run("DeleteStateOfTheZeroCoordinateLeavesSiblingsReachable", func(t *testing.T) {
		s := f(t)
		mustCreate(t, s, newState(t, "PAGE-22", "page", "", "default"))
		mustCreate(t, s, newState(t, "PAGE-22", "page", "draft", "draft"))

		// The store used to refuse this to avoid "orphaning" the family, on
		// the reasoning that world fallback resolved against the zero row.
		// It resolved there only because `bare_face` pointed a declared face
		// at it (BUG-HC6I2T); with no face privileged, the zero coordinate is
		// one row among siblings and deleting it orphans nothing.
		_, err := s.DeleteEntityState(ctx(), "PAGE-22", "")
		require.NoError(t, err)

		got, gerr := s.GetEntityState(ctx(), "PAGE-22", "draft")
		require.NoError(t, gerr, "the sibling must survive and stay addressable")
		assert.Equal(t, "draft", got.GetString("title"))
	})

	t.Run("DeleteStateOfTheLastFaceRemovesTheEntity", func(t *testing.T) {
		s := f(t)
		mustCreate(t, s, newState(t, "PAGE-23", "page", "", "only"))

		// The default face is deletable when it is the ONLY face: no sibling
		// is orphaned, so the refusal above does not apply.
		res, err := s.DeleteEntityState(ctx(), "PAGE-23", "")
		require.NoError(t, err)
		assert.Len(t, res.DeletedEntities, 1)

		_, err = s.GetEntity(ctx(), "PAGE-23")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("DeleteStateEmitsOnlyItsOwnFaceEvent", func(t *testing.T) {
		s := f(t)
		mustCreate(t, s, newState(t, "PAGE-28", "page", "", "default"))
		mustCreate(t, s, newState(t, "PAGE-28", "page", "draft", "draft"))

		events, cancel := s.Subscribe(16)
		defer cancel()
		_, err := s.DeleteEntityState(ctx(), "PAGE-28", ptr(t, "draft"))
		require.NoError(t, err)

		// EXACTLY ONE delete event, carrying the discarded face's coordinate.
		// A backend that emitted the family's events here would be telling
		// every subscriber the whole entity is gone while the default face
		// still exists — silent, and invisible until someone read the entity
		// through a derived index.
		select {
		case ev := <-events:
			assert.Equal(t, store.EventEntityDeleted, ev.Op)
			assert.Equal(t, ptr(t, "draft"), ev.Face,
				"the event must name the face that was deleted")
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the face-delete event")
		}
		select {
		case ev := <-events:
			t.Errorf("a per-face delete must emit ONE event, got a second: %+v", ev)
		case <-time.After(100 * time.Millisecond):
			// Correct: nothing further.
		}
	})

	t.Run("DeleteStateNotFound", func(t *testing.T) {
		s := f(t)
		mustCreate(t, s, newState(t, "PAGE-24", "page", "", "default"))

		_, err := s.DeleteEntityState(ctx(), "PAGE-24", ptr(t, "nosuchface"))
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = s.DeleteEntityState(ctx(), "NO-SUCH-ENTITY", ptr(t, "draft"))
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	// --- Per-tail relation delete (TKT-C1XUA8) ---------------------------

	t.Run("DeleteRelationStateAddressesTheTailNotTheTriple", func(t *testing.T) {
		s := f(t)
		mustCreate(t, s, newState(t, "PAGE-25", "page", "", "default"))
		mustCreate(t, s, newState(t, "PAGE-25", "page", "published", "published"))
		mustCreate(t, s, newState(t, "SPEC-6", "page", "", "target"))

		pub := ptr(t, "published")
		_, err := s.CreateRelation(ctx(), "PAGE-25", "references", "SPEC-6", nil)
		require.NoError(t, err)
		_, err = s.CreateRelation(ctx(), "PAGE-25", "references", "SPEC-6",
			&store.RelationData{FromFace: pub})
		require.NoError(t, err)

		// Delete the PUBLISHED-tail edge. The regression this guards: every
		// backend's DeleteRelation is default-tail-only, so a caller dropping
		// the tail deleted the DEFAULT edge and reported success while the
		// published edge survived — the wrong edge, silently.
		require.NoError(t, s.DeleteRelationState(ctx(), "PAGE-25", pub, "references", "SPEC-6"))

		remaining := collectRelations(t, s, store.RelationQuery{From: "PAGE-25"})
		require.Len(t, remaining, 1)
		assert.True(t, remaining[0].FromFace.IsDefault(),
			"the default-tail edge must survive deletion of its published-tail sibling")
	})

	t.Run("DeleteRelationStateZeroFaceMatchesDeleteRelation", func(t *testing.T) {
		s := f(t)
		mustCreate(t, s, newState(t, "PAGE-26", "page", "", "default"))
		mustCreate(t, s, newState(t, "PAGE-26", "page", "draft", "draft"))
		mustCreate(t, s, newState(t, "SPEC-7", "page", "", "target"))

		_, err := s.CreateRelation(ctx(), "PAGE-26", "references", "SPEC-7", nil)
		require.NoError(t, err)
		_, err = s.CreateRelation(ctx(), "PAGE-26", "references", "SPEC-7",
			&store.RelationData{FromFace: ptr(t, "draft")})
		require.NoError(t, err)

		// The zero face is the general form's default-tail address, so it
		// must behave exactly as DeleteRelation does — same edge, same result.
		require.NoError(t, s.DeleteRelationState(ctx(), "PAGE-26", "", "references", "SPEC-7"))

		remaining := collectRelations(t, s, store.RelationQuery{From: "PAGE-26"})
		require.Len(t, remaining, 1)
		assert.Equal(t, ptr(t, "draft"), remaining[0].FromFace)
	})

	t.Run("DeleteRelationStateNotFound", func(t *testing.T) {
		s := f(t)
		mustCreate(t, s, newState(t, "PAGE-27", "page", "", "default"))
		mustCreate(t, s, newState(t, "SPEC-8", "page", "", "target"))
		_, err := s.CreateRelation(ctx(), "PAGE-27", "references", "SPEC-8", nil)
		require.NoError(t, err)

		// A tail with no edge is absent, NOT "close enough" to the default
		// edge that does exist.
		err = s.DeleteRelationState(ctx(), "PAGE-27", ptr(t, "draft"), "references", "SPEC-8")
		assert.ErrorIs(t, err, store.ErrNotFound)
		assert.Len(t, collectRelations(t, s, store.RelationQuery{From: "PAGE-27"}), 1,
			"the miss must not have deleted the default-tail edge")
	})

	// --- Per-tail relation update (BUG-64MU2Q) ---------------------------

	t.Run("UpdateRelationStateAddressesTheTailNotTheTriple", func(t *testing.T) {
		s := f(t)
		mustCreate(t, s, newState(t, "PAGE-28", "page", "", "default"))
		mustCreate(t, s, newState(t, "PAGE-28", "page", "published", "published"))
		mustCreate(t, s, newState(t, "SPEC-9", "page", "", "target"))

		pub := ptr(t, "published")
		_, err := s.CreateRelation(ctx(), "PAGE-28", "references", "SPEC-9",
			&store.RelationData{Content: "default edge"})
		require.NoError(t, err)
		_, err = s.CreateRelation(ctx(), "PAGE-28", "references", "SPEC-9",
			&store.RelationData{FromFace: pub, Content: "published edge"})
		require.NoError(t, err)

		// Update the PUBLISHED-tail edge. The regression this guards mirrors
		// the delete one above: UpdateRelation is default-tail-only, so a
		// caller dropping the tail wrote its properties onto the DEFAULT
		// edge and reported success — the wrong edge, silently.
		updated, err := s.UpdateRelationState(ctx(), "PAGE-28", pub, "references", "SPEC-9",
			store.RelationData{Content: "published edge v2"})
		require.NoError(t, err)
		assert.Equal(t, pub, updated.FromFace,
			"the echoed edge must be the one that was addressed")
		assert.Equal(t, "published edge v2", updated.Content)

		edges := collectRelations(t, s, store.RelationQuery{From: "PAGE-28"})
		require.Len(t, edges, 2)
		byFace := map[entity.Face]string{}
		for _, e := range edges {
			byFace[e.FromFace] = e.Content
		}
		assert.Equal(t, "published edge v2", byFace[pub])
		assert.Equal(t, "default edge", byFace[""],
			"the default-tail edge must be untouched by a write to its published-tail sibling")
	})

	t.Run("UpdateRelationStateZeroFaceMatchesUpdateRelation", func(t *testing.T) {
		s := f(t)
		mustCreate(t, s, newState(t, "PAGE-29", "page", "", "default"))
		mustCreate(t, s, newState(t, "PAGE-29", "page", "draft", "draft"))
		mustCreate(t, s, newState(t, "SPEC-10", "page", "", "target"))

		draft := ptr(t, "draft")
		_, err := s.CreateRelation(ctx(), "PAGE-29", "references", "SPEC-10",
			&store.RelationData{Content: "default edge"})
		require.NoError(t, err)
		_, err = s.CreateRelation(ctx(), "PAGE-29", "references", "SPEC-10",
			&store.RelationData{FromFace: draft, Content: "draft edge"})
		require.NoError(t, err)

		// The zero face is the general form's default-tail address, so it
		// must behave exactly as UpdateRelation does — same edge, same result.
		updated, err := s.UpdateRelationState(ctx(), "PAGE-29", "", "references", "SPEC-10",
			store.RelationData{Content: "default edge v2"})
		require.NoError(t, err)
		assert.True(t, updated.FromFace.IsDefault())

		edges := collectRelations(t, s, store.RelationQuery{From: "PAGE-29", FromFace: &draft})
		require.Len(t, edges, 1)
		assert.Equal(t, "draft edge", edges[0].Content,
			"the draft-tail edge must be untouched by a default-tail write")
	})

	t.Run("UpdateRelationStateNotFound", func(t *testing.T) {
		s := f(t)
		mustCreate(t, s, newState(t, "PAGE-30", "page", "", "default"))
		mustCreate(t, s, newState(t, "SPEC-11", "page", "", "target"))
		_, err := s.CreateRelation(ctx(), "PAGE-30", "references", "SPEC-11",
			&store.RelationData{Content: "default edge"})
		require.NoError(t, err)

		// A tail with no edge is absent, NOT "close enough" to the default
		// edge that does exist.
		_, err = s.UpdateRelationState(ctx(), "PAGE-30", ptr(t, "draft"), "references", "SPEC-11",
			store.RelationData{Content: "clobbered"})
		assert.ErrorIs(t, err, store.ErrNotFound)

		edges := collectRelations(t, s, store.RelationQuery{From: "PAGE-30"})
		require.Len(t, edges, 1)
		assert.Equal(t, "default edge", edges[0].Content,
			"the miss must not have written to the default-tail edge")
	})

	t.Run("RenameCascadesTheFamily", func(t *testing.T) {
		s := f(t)
		mustCreate(t, s, newState(t, "PAGE-12", "page", "", "default"))
		mustCreate(t, s, newState(t, "PAGE-12", "page", "draft", "draft"))
		mustCreate(t, s, newState(t, "SPEC-3", "page", "", "target"))
		_, err := s.CreateRelation(ctx(), "PAGE-12", "references", "SPEC-3",
			&store.RelationData{FromFace: ptr(t, "draft")})
		require.NoError(t, err)

		res, err := s.RenameEntity(ctx(), "PAGE-12", "PAGE-99")
		require.NoError(t, err)
		assert.Equal(t, 1, res.RelationsUpdated)

		// Every state re-keyed onto the new id.
		def, err := s.GetEntity(ctx(), "PAGE-99")
		require.NoError(t, err)
		assert.Equal(t, "default", def.GetString("title"))
		draft, err := s.GetEntityState(ctx(), "PAGE-99", ptr(t, "draft"))
		require.NoError(t, err)
		assert.Equal(t, "draft", draft.GetString("title"))
		_, err = s.GetEntityState(ctx(), "PAGE-12", ptr(t, "draft"))
		assert.ErrorIs(t, err, store.ErrNotFound)

		// The tail face rode along.
		moved := collectRelations(t, s, store.RelationQuery{From: "PAGE-99", Type: "references"})
		require.Len(t, moved, 1)
		assert.Equal(t, ptr(t, "draft"), moved[0].FromFace)

		// Any state of an existing entity blocks the rename target.
		mustCreate(t, s, newState(t, "PAGE-13", "page", "", "blocker"))
		_, err = s.RenameEntity(ctx(), "PAGE-99", "PAGE-13")
		assert.ErrorIs(t, err, store.ErrConflict)
	})

	t.Run("EventsFirePerState", func(t *testing.T) {
		s := f(t)
		events, cancel := s.Subscribe(16)
		defer cancel()

		mustCreate(t, s, newState(t, "PAGE-14", "page", "", "default"))
		mustCreate(t, s, newState(t, "PAGE-14", "page", "draft", "draft"))
		_, err := s.DeleteEntity(ctx(), "PAGE-14", true)
		require.NoError(t, err)

		// Bounded blocking receive: backends with async delivery (the pg
		// change-feed bridge) may not have all four buffered yet.
		var faces []string
		timeout := time.After(5 * time.Second)
		for range 4 {
			select {
			case ev := <-events:
				faces = append(faces, string(ev.Face))
			case <-timeout:
				t.Fatalf("timed out waiting for events; got %v", faces)
			}
		}
		// Two creates + two deletes, each carrying its state's face.
		sort.Strings(faces)
		assert.Equal(t, []string{"", "", "draft", "draft"}, faces)
	})
}

// collectRelations drains ListRelations for q.
func collectRelations(t *testing.T, s store.Store, q store.RelationQuery) []*entity.Relation {
	t.Helper()
	var out []*entity.Relation
	for r, err := range s.ListRelations(ctx(), q) {
		require.NoError(t, err)
		out = append(out, r)
	}
	return out
}
