package entitymanager_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// seedCASEntity creates one entity and returns it along with its current
// store version.
func seedCASEntity(t *testing.T, st store.Store) (*entity.Entity, store.EntityVersion) {
	t.Helper()
	e := entity.New("REQ-001", "requirement")
	e.SetString("title", "Login")
	require.NoError(t, st.CreateEntity(context.Background(), e))

	stored, err := st.GetEntity(context.Background(), e.ID)
	require.NoError(t, err)
	return stored, store.VersionOf(stored)
}

// TestPatchEntity_ExpectedVersionAppliesWhenUnchanged pins the happy path:
// a patch carrying the current version lands normally.
func TestPatchEntity_ExpectedVersionAppliesWhenUnchanged(t *testing.T) {
	st := memstore.New()
	mgr := newManagerOverStore(t, st)
	stored, version := seedCASEntity(t, st)

	_, err := mgr.PatchEntity(context.Background(), stored.ID, entity.Patch{
		Properties:      map[string]any{"title": "Login v2"},
		ExpectedVersion: string(version),
	})
	require.NoError(t, err)

	got, err := st.GetEntity(context.Background(), stored.ID)
	require.NoError(t, err)
	assert.Equal(t, "Login v2", got.GetString("title"))
}

// TestPatchEntity_ResultVersionIsTheStoredVersion pins that UpdateResult.Version
// is the token of the row the write left behind, so a caller can chain a
// compare-and-swap off it without re-reading — and that the chained CAS lands.
func TestPatchEntity_ResultVersionIsTheStoredVersion(t *testing.T) {
	st := memstore.New()
	mgr := newManagerOverStore(t, st)
	stored, before := seedCASEntity(t, st)

	res, err := mgr.PatchEntity(context.Background(), stored.ID, entity.Patch{
		Properties: map[string]any{"title": "Login v2"},
	})
	require.NoError(t, err)

	got, err := st.GetEntity(context.Background(), stored.ID)
	require.NoError(t, err)
	assert.Equal(t, string(store.VersionOf(got)), res.Version)
	assert.NotEqual(t, string(before), res.Version, "the version must move with the write")

	_, err = mgr.PatchEntity(context.Background(), stored.ID, entity.Patch{
		Properties:      map[string]any{"title": "Login v3"},
		ExpectedVersion: res.Version,
	})
	require.NoError(t, err, "a CAS on the returned version must apply")
}

// TestPatchEntity_StaleExpectedVersionSurvivesAsTypedConflict is the
// RR-HI9QIU guard, and the reason this test exists as its own file.
//
// In RR-HI9QIU a store conflict was re-presented by entitymanager as a FRESH
// validation error carrying no wrapped cause. errors.As therefore could not
// see it, the webhook's isWebhookConflict returned false, and its retry loop
// became unreachable — measured as 8 of 8 concurrent losers receiving a 500
// with zero retries. The code read as correct; only the error plumbing was
// wrong.
//
// So it is not enough that PatchEntity rejects a stale write. The rejection
// must arrive as something a caller can MATCH, through every translation
// between the store and the caller. Assert the recoverable shape, not just
// the failure.
func TestPatchEntity_StaleExpectedVersionSurvivesAsTypedConflict(t *testing.T) {
	st := memstore.New()
	mgr := newManagerOverStore(t, st)
	stored, staleVersion := seedCASEntity(t, st)

	// Someone else writes, invalidating staleVersion.
	interloper := stored.Clone()
	interloper.SetString("title", "Written by someone else")
	require.NoError(t, st.UpdateEntity(context.Background(), interloper))

	_, err := mgr.PatchEntity(context.Background(), stored.ID, entity.Patch{
		Properties:      map[string]any{"title": "Based on a stale read"},
		ExpectedVersion: string(staleVersion),
	})
	require.Error(t, err)

	var conflict *store.VersionConflictError
	require.ErrorAs(t, err, &conflict,
		"the conflict MUST survive entitymanager's translation as a typed error — "+
			"RR-HI9QIU is what happens when it does not")
	assert.Equal(t, stored.ID, conflict.ID)
	assert.Equal(t, conflict.Expected, staleVersion)
	require.ErrorIs(t, err, store.ErrConflict,
		"errors.Is(err, store.ErrConflict) must also match")

	got, err := st.GetEntity(context.Background(), stored.ID)
	require.NoError(t, err)
	assert.Equal(t, "Written by someone else", got.GetString("title"),
		"the rejected patch must have written nothing")
}

// TestPatchEntity_EmptyExpectedVersionNeverFailsOnConflict pins that a patch
// with no ExpectedVersion is never rejected for a write the caller did not
// see: the manager retries against the newer row, and the patch lands.
func TestPatchEntity_EmptyExpectedVersionNeverFailsOnConflict(t *testing.T) {
	st := memstore.New()
	mgr := newManagerOverStore(t, st)
	stored, _ := seedCASEntity(t, st)

	interloper := stored.Clone()
	interloper.SetString("title", "Moved on")
	require.NoError(t, st.UpdateEntity(context.Background(), interloper))

	_, err := mgr.PatchEntity(context.Background(), stored.ID, entity.Patch{
		Properties: map[string]any{"title": "Unconditional"},
	})
	require.NoError(t, err, "a patch with no ExpectedVersion must not fail on a conflict")

	got, err := st.GetEntity(context.Background(), stored.ID)
	require.NoError(t, err)
	assert.Equal(t, "Unconditional", got.GetString("title"))
}

// TestPatchEntity_ConflictRetryLoopConverges exercises the loop a real
// caller writes — the shape the webhook append path uses. If the conflict
// error were unmatchable this test would hang at the errors.As check and
// fail, which is the point.
func TestPatchEntity_ConflictRetryLoopConverges(t *testing.T) {
	st := memstore.New()
	mgr := newManagerOverStore(t, st)
	stored, staleVersion := seedCASEntity(t, st)

	interloper := stored.Clone()
	interloper.SetString("status", "open")
	require.NoError(t, st.UpdateEntity(context.Background(), interloper))

	ctx := context.Background()
	version := staleVersion
	var lastErr error
	for range 5 {
		_, err := mgr.PatchEntity(ctx, stored.ID, entity.Patch{
			Properties:      map[string]any{"title": "Login v2"},
			ExpectedVersion: string(version),
		})
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = err
		var conflict *store.VersionConflictError
		require.ErrorAs(t, err, &conflict, "retry loop needs a matchable conflict")
		version = conflict.Actual // retry against the reported current version
	}
	require.NoError(t, lastErr, "the retry loop must converge")

	got, err := st.GetEntity(ctx, stored.ID)
	require.NoError(t, err)
	assert.Equal(t, "Login v2", got.GetString("title"))
	assert.Equal(t, "open", got.GetString("status"),
		"the retry must preserve the interloper's property, not resurrect the stale base")
}
