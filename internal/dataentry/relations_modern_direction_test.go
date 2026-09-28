package dataentry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// Tests for TKT-GFQK: the unified PATCH reconciler accepts the
// inverse name as a body key and writes the canonical edge with the
// path entity on the target side.

// newDirectionTestApp builds an app via metamodel.Parse so the
// `inverseOwners` map is populated by the load pass (which is what
// resolveDirection consults at runtime). Cannot reuse
// newReverseRelationsTestApp because that constructs the Metamodel
// literal-style and never populates inverseOwners.
func newDirectionTestApp(t *testing.T) *App {
	t.Helper()
	yaml := `version: "1.0"
entities:
  feature:
    label: Feature
    id_prefix: "FEAT-"
    properties:
      title:
        type: string
        required: true
relations:
  blocks:
    label: blocks
    from: [feature]
    to: [feature]
    inverse: blockedBy
    properties:
      reason:
        type: string
`
	meta, err := metamodel.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse metamodel: %v", err)
	}
	cfg := &dataentryconfig.Config{
		App:        dataentryconfig.AppConfig{Name: "Direction Test", Description: "x"},
		Forms:      map[string]dataentryconfig.Form{},
		Lists:      map[string]dataentryconfig.List{},
		Views:      map[string]dataentryconfig.ViewConfig{},
		Kanbans:    map[string]dataentryconfig.Kanban{},
		Navigation: []dataentryconfig.NavigationEntry{},
	}
	return newAppFromParts(cfg, meta, newFixture())
}

// seedDirectionFixture seeds FEAT-A --blocks--> FEAT-B with a reason.
func seedDirectionFixture(t *testing.T, app *App) (sourceID, targetID string) {
	t.Helper()
	sourceID, targetID = "FEAT-A", "FEAT-B"
	seedEntity(app, &entity.Entity{ID: sourceID, Type: "feature", Properties: map[string]any{"title": "source"}})
	seedEntity(app, &entity.Entity{ID: targetID, Type: "feature", Properties: map[string]any{"title": "target"}})
	if _, err := app.store.CreateRelation(
		context.Background(),
		sourceID, "blocks", targetID,
		&store.RelationData{Properties: map[string]any{"reason": "test block"}},
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return sourceID, targetID
}

// TestApplyRelationsModern_IncomingBodyKey_BothEndpoints PATCHes
// FEAT-B with the inverse body key `blockedBy` and asserts the
// canonical edge `FEAT-A --blocks--> FEAT-B` exists, verified through
// BOTH outgoingRelations(source) and incomingRelations(target).
// Asserting both endpoints catches the "wrote the reversed edge"
// failure mode the design review flagged (F11).
func TestApplyRelationsModern_IncomingBodyKey_BothEndpoints(t *testing.T) {
	app := newDirectionTestApp(t)
	sourceID, targetID := seedDirectionFixture(t, app)

	// PATCH FEAT-B's `blockedBy` to keep FEAT-A as a source.
	body := `{"relations":{"blockedBy":{"data":[{"type":"feature","id":"` + sourceID + `","meta":{"reason":"updated reason"}}]}}}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/features/"+targetID, strings.NewReader(body))
	rec := httptest.NewRecorder()
	app.write.handleV1UpdateEntity(rec, req, "feature", "features", targetID)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// outgoingRelations(source) must include the edge.
	outEdges := app.reader.outgoingRelations(context.Background(), sourceID)
	if len(outEdges) != 1 {
		t.Fatalf("expected 1 outgoing edge from %s, got %d", sourceID, len(outEdges))
	}
	if outEdges[0].From != sourceID || outEdges[0].To != targetID || outEdges[0].Type != "blocks" {
		t.Errorf("edge has wrong endpoints/type: from=%s type=%s to=%s",
			outEdges[0].From, outEdges[0].Type, outEdges[0].To)
	}
	if outEdges[0].Properties["reason"] != "updated reason" {
		t.Errorf("expected meta upsert via inverse body key, got reason=%v",
			outEdges[0].Properties["reason"])
	}

	// incomingRelations(target) must include the SAME edge.
	inEdges := app.reader.incomingRelations(context.Background(), targetID)
	if len(inEdges) != 1 {
		t.Fatalf("expected 1 incoming edge to %s, got %d", targetID, len(inEdges))
	}
	if inEdges[0].From != sourceID || inEdges[0].To != targetID {
		t.Errorf("incoming view wrong: from=%s to=%s", inEdges[0].From, inEdges[0].To)
	}
}

// TestApplyRelationsModern_IncomingDelete asserts that omitting an
// existing incoming peer from the desired set deletes the underlying
// canonical edge (from peer to path entity).
func TestApplyRelationsModern_IncomingDelete(t *testing.T) {
	app := newDirectionTestApp(t)
	sourceID, targetID := seedDirectionFixture(t, app)

	// PATCH FEAT-B's `blockedBy` to an empty set — drops the source.
	body := `{"relations":{"blockedBy":{"data":[]}}}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/features/"+targetID, strings.NewReader(body))
	rec := httptest.NewRecorder()
	app.write.handleV1UpdateEntity(rec, req, "feature", "features", targetID)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := len(app.reader.outgoingRelations(context.Background(), sourceID)); got != 0 {
		t.Errorf("expected 0 outgoing from %s after delete, got %d", sourceID, got)
	}
	if got := len(app.reader.incomingRelations(context.Background(), targetID)); got != 0 {
		t.Errorf("expected 0 incoming to %s after delete, got %d", targetID, got)
	}
}

// TestApplyRelationsModern_IncomingNoOp asserts re-PATCHing with the
// SAME incoming set produces zero writes. Autosave (TKT-E6094) relies
// on this — a re-save of an unchanged form must not bump mtimes.
func TestApplyRelationsModern_IncomingNoOp(t *testing.T) {
	app := newDirectionTestApp(t)
	sourceID, targetID := seedDirectionFixture(t, app)

	preEdge := app.reader.outgoingRelations(context.Background(), sourceID)[0]
	body := `{"relations":{"blockedBy":{"data":[{"type":"feature","id":"` + sourceID + `","meta":{"reason":"test block"}}]}}}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/features/"+targetID, strings.NewReader(body))
	rec := httptest.NewRecorder()
	app.write.handleV1UpdateEntity(rec, req, "feature", "features", targetID)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	postEdge := app.reader.outgoingRelations(context.Background(), sourceID)[0]
	// No new edge, no property drift.
	if postEdge.Properties["reason"] != preEdge.Properties["reason"] {
		t.Errorf("no-op should not change properties: before=%v after=%v",
			preEdge.Properties["reason"], postEdge.Properties["reason"])
	}
}

// TestApplyRelationsModern_IncomingWarnings_DirectionField asserts
// that soft warnings emitted under an inverse body key carry
// Direction: "incoming" and reference the body-key path.
func TestApplyRelationsModern_IncomingWarnings_DirectionField(t *testing.T) {
	app := newDirectionTestApp(t)
	_, targetID := seedDirectionFixture(t, app)

	// `severity` is not declared on `blocks` — surfaces unknown_meta_key.
	body := `{"relations":{"blockedBy":{"data":[{"type":"feature","id":"FEAT-A","meta":{"severity":"high"}}]}}}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/features/"+targetID, strings.NewReader(body))
	rec := httptest.NewRecorder()
	app.write.handleV1UpdateEntity(rec, req, "feature", "features", targetID)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Warnings []Warning `json:"warnings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var found bool
	for _, w := range resp.Warnings {
		if w.Code == "unknown_meta_key" {
			if w.Direction != "incoming" {
				t.Errorf("expected Direction=incoming on warning, got %q", w.Direction)
			}
			if !strings.Contains(w.Path, "blockedBy") {
				t.Errorf("warning path should reference body key, got %q", w.Path)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("expected unknown_meta_key warning in response, got %+v", resp.Warnings)
	}
}

// TestApplyRelationsModern_MixedCanonicalAndInverse asserts that a
// body with BOTH `blocks` and `blockedBy` keys writes edges in both
// directions when the path entity is in different desired sets (no
// self-loop). Verifies AC6.
func TestApplyRelationsModern_MixedCanonicalAndInverse(t *testing.T) {
	app := newDirectionTestApp(t)
	// FEAT-A is in the middle: blocks FEAT-B, blocked by FEAT-C.
	for _, id := range []string{"FEAT-A", "FEAT-B", "FEAT-C"} {
		seedEntity(app, &entity.Entity{ID: id, Type: "feature", Properties: map[string]any{"title": id}})
	}

	body := `{"relations":{
		"blocks":{"data":[{"type":"feature","id":"FEAT-B"}]},
		"blockedBy":{"data":[{"type":"feature","id":"FEAT-C"}]}
	}}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/features/FEAT-A", strings.NewReader(body))
	rec := httptest.NewRecorder()
	app.write.handleV1UpdateEntity(rec, req, "feature", "features", "FEAT-A")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// FEAT-A → FEAT-B (outgoing)
	outA := app.reader.outgoingRelations(context.Background(), "FEAT-A")
	if len(outA) != 1 || outA[0].To != "FEAT-B" {
		t.Errorf("FEAT-A should have 1 outgoing to FEAT-B, got %+v", outA)
	}
	// FEAT-C → FEAT-A (incoming view from FEAT-A's perspective)
	inA := app.reader.incomingRelations(context.Background(), "FEAT-A")
	if len(inA) != 1 || inA[0].From != "FEAT-C" {
		t.Errorf("FEAT-A should have 1 incoming from FEAT-C, got %+v", inA)
	}
}

// TestApplyRelationsModern_SelfLoopShapeConflict asserts that a body
// with BOTH canonical and inverse keys for the same relation, where
// the path entity is in both desired sets, returns 400 shape_conflict.
func TestApplyRelationsModern_SelfLoopShapeConflict(t *testing.T) {
	app := newDirectionTestApp(t)
	seedEntity(app, &entity.Entity{ID: "FEAT-A", Type: "feature", Properties: map[string]any{"title": "A"}})

	body := `{"relations":{
		"blocks":{"data":[{"type":"feature","id":"FEAT-A"}]},
		"blockedBy":{"data":[{"type":"feature","id":"FEAT-A"}]}
	}}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/features/FEAT-A", strings.NewReader(body))
	rec := httptest.NewRecorder()
	app.write.handleV1UpdateEntity(rec, req, "feature", "features", "FEAT-A")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 shape_conflict, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "shape_conflict") {
		t.Errorf("response should include shape_conflict code, got: %s", rec.Body.String())
	}
}

// TestApplyRelationsModern_ConcurrentPatchesNever500 races PATCHes that add,
// update and drop the same edge. Each reconciler reads the edge set without a
// lock, so another request can create or delete an edge between its read and
// its write. Those races must resolve to the desired state, never to a 500.
func TestApplyRelationsModern_ConcurrentPatchesNever500(t *testing.T) {
	app := newDirectionTestApp(t)
	sourceID, targetID := seedDirectionFixture(t, app)

	keep := `{"relations":{"blockedBy":{"data":[{"type":"feature","id":"` + sourceID +
		`","meta":{"reason":"r%d"}}]}}}`
	drop := `{"relations":{"blockedBy":{"data":[]}}}`

	const racers = 16
	codes := make(chan int, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Go(func() {
			body := drop
			if i%2 == 0 {
				body = fmt.Sprintf(keep, i)
			}
			req := httptest.NewRequest(http.MethodPatch, "/api/v1/features/"+targetID, strings.NewReader(body))
			rec := httptest.NewRecorder()
			app.write.handleV1UpdateEntity(rec, req, "feature", "features", targetID)
			if rec.Code >= http.StatusInternalServerError {
				t.Errorf("concurrent relation PATCH: got %d: %s", rec.Code, rec.Body.String())
			}
			codes <- rec.Code
		})
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != http.StatusOK && code != http.StatusConflict {
			t.Errorf("concurrent relation PATCH: got %d, want 200 or 409", code)
		}
	}
}
