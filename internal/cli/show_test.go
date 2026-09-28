package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/output"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/testutil"
)

// show_test.go focuses on CLI-specific concerns:
//   - error path for missing entity
//   - output rendering for both table and JSON formats
//
// Entity/relation read semantics are covered by the store conformance
// suite (internal/store/storetest). Representative formatting tests
// here are enough to catch regressions in the CLI glue.
//
// Tests dropped during the kong migration:
//   - TestShowCommandRequiresExactlyOneArg: kong enforces positional
//     argument arity at parse time, not Run time. There is no
//     equivalent Run-level entry point to drive from a unit test.

// TestShowEntityRendersRelations covers the full CLI render path: entity
// details plus incoming and outgoing relations in the default (table)
// output. One representative case is enough — the store's own tests
// cover the query side.
func TestShowEntityRendersRelations(t *testing.T) {
	meta := metamodel.DefaultMetamodel()
	seeder := newStoreSeeder(meta)
	seeder.addEntity(testutil.EntityFor(meta, "requirement").ID("REQ-001").
		With("status", "draft"))
	seeder.addEntity(testutil.EntityFor(meta, "decision").ID("DEC-001"))
	seeder.addEntity(testutil.EntityFor(meta, "solution").ID("SOL-001"))
	seeder.addRelation("DEC-001", "addresses", "REQ-001")
	seeder.addRelation("REQ-001", "implements", "SOL-001")
	svc := seeder.build(t).read
	buf := withOutput(t, output.FormatTable)

	cmd := &ShowCmd{ID: "REQ-001"}
	if err := cmd.Run(context.Background(), svc); err != nil {
		t.Fatalf("show command failed: %v", err)
	}

	result := buf.String()
	for _, want := range []string{"REQ-001", "draft", "DEC-001", "addresses", "SOL-001", "implements"} {
		if !strings.Contains(result, want) {
			t.Errorf("expected %q in output, got: %s", want, result)
		}
	}
}

// TestShowEntityNotFound covers the error path for a missing ID.
func TestShowEntityNotFound(t *testing.T) {
	meta := metamodel.DefaultMetamodel()
	seeder := newStoreSeeder(meta)
	svc := seeder.build(t).read
	_ = withOutput(t, output.FormatTable)

	cmd := &ShowCmd{ID: "NONEXISTENT-001"}
	err := cmd.Run(context.Background(), svc)
	if err == nil {
		t.Fatal("expected error for nonexistent entity")
	}

	var notFoundErr *entityNotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Errorf("expected entityNotFoundError, got: %T", err)
	}

	if !strings.Contains(err.Error(), "entity not found") {
		t.Errorf("expected 'entity not found' in error message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "NONEXISTENT-001") {
		t.Errorf("expected 'NONEXISTENT-001' in error message, got: %v", err)
	}
}

// TestShowEntityJSON exercises JSON output shape, which is a CLI
// concern distinct from the table formatter.
func TestShowEntityJSON(t *testing.T) {
	meta := metamodel.DefaultMetamodel()
	seeder := newStoreSeeder(meta)
	seeder.addEntity(testutil.EntityFor(meta, "requirement").ID("REQ-001"))
	svc := seeder.build(t).read
	buf := withOutput(t, output.FormatJSON)

	cmd := &ShowCmd{ID: "REQ-001"}
	if err := cmd.Run(context.Background(), svc); err != nil {
		t.Fatalf("show command failed: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	entityData, ok := result["entity"].(map[string]any)
	if !ok {
		t.Fatal("expected 'entity' key in JSON output")
	}

	if entityData["id"] != "REQ-001" {
		t.Errorf("expected id='REQ-001', got: %v", entityData["id"])
	}
	if entityData["type"] != "requirement" {
		t.Errorf("expected type='requirement', got: %v", entityData["type"])
	}

	props, ok := entityData["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties to be a map")
	}
	if props["title"] == "" {
		t.Errorf("expected title to be non-empty, got: %v", props["title"])
	}
}

// TestEntityNotFoundError covers the custom error type's Error()
// message format — independent of CLI globals.
func TestEntityNotFoundError(t *testing.T) {
	err := &entityNotFoundError{ID: "TEST-123"}

	expected := "entity not found: TEST-123"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

// TestClassifyReadError covers the two branches of the error
// classifier so a future change can't silently regress.
func TestClassifyReadError(t *testing.T) {
	notFound := classifyReadError("REQ-001", store.ErrNotFound)
	var nfe *entityNotFoundError
	if !errors.As(notFound, &nfe) {
		t.Errorf("ErrNotFound should classify as entityNotFoundError, got %T", notFound)
	}

	other := errors.New("some other error")
	passthrough := classifyReadError("REQ-005", other)
	if !errors.Is(passthrough, other) {
		t.Errorf("unknown errors should pass through unchanged, got %v", passthrough)
	}
}

// TestShowFaceAddress pins that `rela show ID@face` lists the entity's
// relations. Relations are keyed on the bare id, so querying them by the typed
// address matched nothing and showed an entity with no relations (BUG-R1PQY9).
// Outgoing edges are the addressed face's own.
func TestShowFaceAddress(t *testing.T) {
	meta := metamodel.DefaultMetamodel()
	seeder := newStoreSeeder(meta)
	seeder.addEntity(testutil.EntityFor(meta, "requirement").ID("REQ-001"))
	draft := testutil.EntityFor(meta, "requirement").ID("REQ-001").WithTitle("draft title").Build()
	draft.Face = "draft"
	seeder.addEntity(testutil.EntityFor(meta, "decision").ID("DEC-001"))
	seeder.addEntity(testutil.EntityFor(meta, "solution").ID("SOL-001"))
	seeder.addEntity(testutil.EntityFor(meta, "solution").ID("SOL-002"))
	ctx := context.Background()
	if err := seeder.s.CreateEntity(ctx, draft); err != nil {
		t.Fatal(err)
	}
	seeder.addRelation("DEC-001", "addresses", "REQ-001")
	seeder.addRelation("REQ-001", "implements", "SOL-001")
	if _, err := seeder.s.CreateRelation(ctx, "REQ-001", "implements", "SOL-002",
		&store.RelationData{FromFace: draft.Face}); err != nil {
		t.Fatal(err)
	}
	svc := seeder.build(t).read
	buf := withOutput(t, output.FormatTable)

	if err := (&ShowCmd{ID: "REQ-001@draft"}).Run(ctx, svc); err != nil {
		t.Fatalf("show command failed: %v", err)
	}

	result := buf.String()
	for _, want := range []string{"draft title", "DEC-001", "SOL-002"} {
		if !strings.Contains(result, want) {
			t.Errorf("expected %q in output, got: %s", want, result)
		}
	}
	if strings.Contains(result, "SOL-001") {
		t.Errorf("the default face's outgoing edge leaked into the draft's: %s", result)
	}
}
