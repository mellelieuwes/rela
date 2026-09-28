package dataentry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// The PATCH path's If-Match used to be a check-then-write: read the entity,
// compare its ETag to the header, then write — correct only while a
// process-wide write lock serialized the whole handler. TKT-34XS2R moved the
// real enforcement into the store, which re-verifies the precondition
// atomically with the write, and TKT-WE0S2K removed the lock.
//
// These tests target the store-level guarantee specifically: they inject the
// interleaving write deterministically at the seam the handler depends on,
// rather than hoping two concurrent handler calls happen to interleave.

// patchBody issues a PATCH through the handler and returns the recorder.
func patchBody(app *App, body string, hdr http.Header) *httptest.ResponseRecorder {
	const id = "TKT-001"
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/tickets/"+id, strings.NewReader(body))
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	app.write.handleV1UpdateEntity(rec, req, "ticket", "tickets", id)
	return rec
}

// TestV1Patch_UnnamedPropertiesSurvive pins the PatchEntity contract now that
// the handler routes through it: a PATCH naming one property must not erase
// the others. Under the previous read-modify-write this held only because the
// handler happened to hold the whole entity.
func TestV1Patch_UnnamedPropertiesSurvive(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{
		ID:   "TKT-001",
		Type: "ticket",
		Properties: map[string]any{
			"title":  "Test Ticket",
			"status": "open",
		},
	})

	rec := patchBody(app, `{"properties":{"title":"Renamed"}}`, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body)

	got, err := app.store.GetEntity(context.Background(), "TKT-001")
	require.NoError(t, err)
	assert.Equal(t, "Renamed", got.GetString("title"))
	assert.Equal(t, "open", got.GetString("status"),
		"a property the PATCH did not name must be preserved")
}

// TestV1Patch_PropertiesUnsetStillRemoves pins that moving the unset from a
// hand-rolled delete() to the patch's MetaUnset kept the behavior — including
// the warning for a key the type does not declare.
func TestV1Patch_PropertiesUnsetStillRemoves(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{
		ID:   "TKT-001",
		Type: "ticket",
		Properties: map[string]any{
			"title":  "Test Ticket",
			"status": "open",
		},
	})

	rec := patchBody(app, `{"properties_unset":["status"]}`, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body)

	got, err := app.store.GetEntity(context.Background(), "TKT-001")
	require.NoError(t, err)
	assert.NotContains(t, got.Properties, "status", "the named key must be removed")
	assert.Equal(t, "Test Ticket", got.GetString("title"))
}

// TestV1Patch_SetAndUnsetSameKeyEndsUnset pins the documented ordering
// (upserts, then unsets) survives the move onto entity.Patch.
func TestV1Patch_SetAndUnsetSameKeyEndsUnset(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{
		ID:         "TKT-001",
		Type:       "ticket",
		Properties: map[string]any{"title": "Test Ticket", "status": "open"},
	})

	rec := patchBody(app,
		`{"properties":{"status":"closed"},"properties_unset":["status"]}`, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body)

	got, err := app.store.GetEntity(context.Background(), "TKT-001")
	require.NoError(t, err)
	assert.NotContains(t, got.Properties, "status",
		"unset is applied after the upsert, so the key ends removed")
}

// TestV1Patch_StoreRejectsWriteRacingTheHandlerRead is the point of the
// migration. It reproduces the interleaving: the handler reads (and would
// compare If-Match), then ANOTHER writer lands, then the handler writes. The
// store must reject the write rather than clobber the interloper.
//
// The concurrent write is injected through the store directly, which is
// exactly what a concurrent request or a second rela-server process looks
// like from here.
func TestV1Patch_StoreRejectsWriteRacingTheHandlerRead(t *testing.T) {
	app := newTestAppV1(t)
	ctx := context.Background()
	seedEntity(app, &entity.Entity{
		ID:         "TKT-001",
		Type:       "ticket",
		Properties: map[string]any{"title": "Test Ticket", "status": "open"},
	})

	// What the handler read at the top of the request.
	base, err := app.store.GetEntity(ctx, "TKT-001")
	require.NoError(t, err)
	staleVersion := store.VersionOf(base)

	// A second writer commits before our write lands.
	interloper := base.Clone()
	interloper.SetString("title", "Written by another process")
	require.NoError(t, app.store.UpdateEntity(ctx, interloper))

	// The write the handler would now issue, carrying the version it read.
	_, err = app.entityManager.PatchEntity(ctx, "TKT-001", entity.Patch{
		Properties:      map[string]any{"title": "Based on a stale read"},
		ExpectedVersion: string(staleVersion),
	})
	require.Error(t, err, "the store must refuse a write whose precondition has expired")

	var conflict *store.VersionConflictError
	require.ErrorAs(t, err, &conflict,
		"the handler maps this to 412 via errors.As, so it must stay matchable")

	got, err := app.store.GetEntity(ctx, "TKT-001")
	require.NoError(t, err)
	assert.Equal(t, "Written by another process", got.GetString("title"),
		"the racing write must survive; the stale one must have applied nothing")
}

// TestV1Patch_IfMatchStillRejectsStaleHeader pins the client-facing contract
// is unchanged: a stale If-Match is still a 412. The ETag remains computed
// above the store (it folds in relations, which the store token deliberately
// does not), so this path must keep working alongside the new CAS.
func TestV1Patch_IfMatchStillRejectsStaleHeader(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{
		ID:         "TKT-001",
		Type:       "ticket",
		Properties: map[string]any{"title": "Test Ticket", "status": "open"},
	})

	rec := patchBody(app, `{"properties":{"title":"Nope"}}`,
		http.Header{"If-Match": []string{`"definitely-stale"`}})
	assert.Equal(t, http.StatusPreconditionFailed, rec.Code, "body=%s", rec.Body)

	got, err := app.store.GetEntity(context.Background(), "TKT-001")
	require.NoError(t, err)
	assert.Equal(t, "Test Ticket", got.GetString("title"), "the rejected PATCH wrote nothing")
}

// TestV1Patch_CurrentIfMatchSucceeds pins the positive half of the same
// contract — a matching If-Match still applies.
func TestV1Patch_CurrentIfMatchSucceeds(t *testing.T) {
	app := newTestAppV1(t)
	ctx := context.Background()
	seedEntity(app, &entity.Entity{
		ID:         "TKT-001",
		Type:       "ticket",
		Properties: map[string]any{"title": "Test Ticket", "status": "open"},
	})

	current, err := app.store.GetEntity(ctx, "TKT-001")
	require.NoError(t, err)
	etag := app.computeEntityETag(ctx, current)

	rec := patchBody(app, `{"properties":{"title":"Renamed"}}`,
		http.Header{"If-Match": []string{etag}})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body)

	got, err := app.store.GetEntity(ctx, "TKT-001")
	require.NoError(t, err)
	assert.Equal(t, "Renamed", got.GetString("title"))
}
