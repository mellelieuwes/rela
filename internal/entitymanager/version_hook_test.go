package entitymanager_test

import (
	"context"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// fakeRecorder captures the version records dispatched to it.
type fakeRecorder struct {
	records []entitymanager.VersionRecord
}

func (r *fakeRecorder) RecordVersion(_ context.Context, v entitymanager.VersionRecord) error {
	r.records = append(r.records, v)
	return nil
}

func newVersionManager(t *testing.T) (*entitymanager.Manager, *fakeRecorder) {
	t.Helper()
	rec := &fakeRecorder{}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:           memstore.New(),
		Meta:            parseMeta(t),
		Templater:       nopTemplater{},
		Audit:           audit.Nop{},
		ACL:             acl.NopACL{},
		Transitions:     statemachine.EmptySet(),
		FieldGate:       entitymanager.AllowAllFieldGate{},
		Twins:           entitymanager.NoTwinOwnership{},
		VersionRecorder: rec,
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	return mgr, rec
}

// TestVersionHook_DeleteCapturesFinalState asserts a delete produces exactly one
// version record (op=delete) carrying the pre-delete content and the acting
// principal — and that create did NOT (create/update are the sweep's job).
func TestVersionHook_DeleteCapturesFinalState(t *testing.T) {
	mgr, rec := newVersionManager(t)
	ctx := ctxWithPrincipal("alice", principal.ToolCLI)

	e := entity.New("", "requirement")
	e.Content = "the requirement body"
	e.Properties = map[string]any{"title": "R1"}
	created, err := mgr.CreateEntity(ctx, e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if len(rec.records) != 0 {
		t.Fatalf("create should not record a version (sweep handles create/update); got %d", len(rec.records))
	}

	if _, err := mgr.DeleteEntity(ctx, created.Entity.ID, false); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
	if len(rec.records) != 1 {
		t.Fatalf("delete should record exactly one version; got %d", len(rec.records))
	}
	got := rec.records[0]
	if got.Op != store.VersionOpDelete {
		t.Errorf("op = %q, want delete", got.Op)
	}
	if got.EntityID != created.Entity.ID {
		t.Errorf("entity id = %q, want %q", got.EntityID, created.Entity.ID)
	}
	if got.Content != "the requirement body" {
		t.Errorf("captured content = %q, want the pre-delete body", got.Content)
	}
	if got.PrincipalUser != "alice" || got.PrincipalTool != principal.ToolCLI {
		t.Errorf("attribution = %q/%q, want alice/%s", got.PrincipalUser, got.PrincipalTool, principal.ToolCLI)
	}
	if got.SchemaHash == "" || len(got.Projection) == 0 {
		t.Error("delete version missing schema hash / projection")
	}
}

// TestVersionHook_RenameCarriesPrevID asserts a rename produces one version
// record (op=rename) carrying the OLD id in PrevID and the NEW id as EntityID.
func TestVersionHook_RenameCarriesPrevID(t *testing.T) {
	mgr, rec := newVersionManager(t)
	// A distinct principal (not the "alice" the other tests use) also confirms
	// the rename version carries the acting identity, not a hardcoded one.
	ctx := ctxWithPrincipal("bob", principal.ToolMCP)

	e := entity.New("", "requirement")
	e.Properties = map[string]any{"title": "R1"}
	created, err := mgr.CreateEntity(ctx, e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	oldID := created.Entity.ID
	rec.records = nil // ignore anything before the rename

	const newID = "REQ-9001"
	if _, err := mgr.RenameEntity(ctx, oldID, newID, entity.RenameOptions{}); err != nil {
		t.Fatalf("RenameEntity: %v", err)
	}
	if len(rec.records) != 1 {
		t.Fatalf("rename should record exactly one version; got %d", len(rec.records))
	}
	got := rec.records[0]
	if got.Op != store.VersionOpRename {
		t.Errorf("op = %q, want rename", got.Op)
	}
	if got.EntityID != newID {
		t.Errorf("entity id = %q, want the NEW id %q", got.EntityID, newID)
	}
	if got.PrevID != oldID {
		t.Errorf("prev id = %q, want the OLD id %q", got.PrevID, oldID)
	}
	if got.PrincipalUser != "bob" || got.PrincipalTool != principal.ToolMCP {
		t.Errorf("attribution = %q/%q, want bob/%s", got.PrincipalUser, got.PrincipalTool, principal.ToolMCP)
	}
}

// TestVersionHook_NilRecorderNoPanic asserts the hook is a safe no-op when no
// recorder is wired (fsstore/memstore builds).
func TestVersionHook_NilRecorderNoPanic(t *testing.T) {
	mgr, _ := newManager(t, nil) // newManager wires no VersionRecorder
	ctx := ctxWithPrincipal("alice", principal.ToolCLI)

	e := entity.New("", "requirement")
	e.Properties = map[string]any{"title": "R1"}
	created, err := mgr.CreateEntity(ctx, e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err := mgr.DeleteEntity(ctx, created.Entity.ID, false); err != nil {
		t.Fatalf("DeleteEntity with nil recorder should not error: %v", err)
	}
}
