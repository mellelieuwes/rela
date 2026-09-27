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

// fakeRelationRecorder captures the relation version records dispatched to it.
type fakeRelationRecorder struct {
	records []entitymanager.RelationVersionRecord
}

func (r *fakeRelationRecorder) RecordRelationVersion(
	_ context.Context, v entitymanager.RelationVersionRecord,
) error {
	r.records = append(r.records, v)
	return nil
}

func newRelationVersionManager(t *testing.T) (*entitymanager.Manager, *fakeRelationRecorder) {
	t.Helper()
	rec := &fakeRelationRecorder{}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:                   memstore.New(),
		Meta:                    parseMeta(t),
		Templater:               nopTemplater{},
		Audit:                   audit.Nop{},
		ACL:                     acl.NopACL{},
		Transitions:             statemachine.EmptySet(),
		FieldGate:               entitymanager.AllowAllFieldGate{},
		Twins:                   entitymanager.NoTwinOwnership{},
		RelationVersionRecorder: rec,
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	return mgr, rec
}

// seedDecReq creates a decision and a requirement and returns their ids. The
// test metamodel's `addresses` relation goes decision -> requirement.
func seedDecReq(ctx context.Context, t *testing.T, mgr *entitymanager.Manager) (dec, req string) {
	t.Helper()
	d := entity.New("", "decision")
	d.SetString("title", "D1")
	dr, err := mgr.CreateEntity(ctx, d, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("create decision: %v", err)
	}
	r := entity.New("", "requirement")
	r.SetString("title", "R1")
	rr, err := mgr.CreateEntity(ctx, r, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("create requirement: %v", err)
	}
	return dr.Entity.ID, rr.Entity.ID
}

// TestRelationVersionHook_ExplicitDeleteCaptures asserts an explicit
// DeleteRelation records exactly one relation version (op=delete) with the
// pre-delete content and acting principal, and that create does NOT.
func TestRelationVersionHook_ExplicitDeleteCaptures(t *testing.T) {
	mgr, rec := newRelationVersionManager(t)
	ctx := ctxWithPrincipal("alice", principal.ToolCLI)
	dec, req := seedDecReq(ctx, t, mgr)

	body := "why this addresses that"
	_, err := mgr.CreateRelation(ctx, dec, "addresses", req,
		entity.RelationOptions{Content: &body})
	if err != nil {
		t.Fatalf("CreateRelation: %v", err)
	}
	if len(rec.records) != 0 {
		t.Fatalf("create should not record a relation version (sweep handles it); got %d", len(rec.records))
	}

	if err := mgr.DeleteRelation(ctx, dec, "addresses", req); err != nil {
		t.Fatalf("DeleteRelation: %v", err)
	}
	if len(rec.records) != 1 {
		t.Fatalf("delete should record exactly one relation version; got %d", len(rec.records))
	}
	got := rec.records[0]
	if got.Op != store.VersionOpDelete {
		t.Errorf("op = %q, want delete", got.Op)
	}
	if got.From != dec || got.To != req || got.Type != "addresses" {
		t.Errorf("triple = %s--%s--%s, want %s--addresses--%s", got.From, got.Type, got.To, dec, req)
	}
	if got.Content != "why this addresses that" {
		t.Errorf("content = %q, want the pre-delete body", got.Content)
	}
	if got.PrincipalUser != "alice" || got.PrincipalTool != principal.ToolCLI {
		t.Errorf("attribution = %q/%q, want alice/%s", got.PrincipalUser, got.PrincipalTool, principal.ToolCLI)
	}
}

// TestRelationVersionHook_CascadeDeleteCapturesEveryEdge is the RR-181AFY
// regression: deleting an entity with cascade must record a delete version for
// EVERY incident relation, not zero — the store bulk-deletes them below the
// choke-point, so this hook is the only capture point.
func TestRelationVersionHook_CascadeDeleteCapturesEveryEdge(t *testing.T) {
	mgr, rec := newRelationVersionManager(t)
	ctx := ctxWithPrincipal("alice", principal.ToolCLI)

	// One decision addressing two requirements: deleting the decision cascades
	// both `addresses` edges.
	d := entity.New("", "decision")
	d.SetString("title", "D-hub")
	dr, err := mgr.CreateEntity(ctx, d, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("create decision: %v", err)
	}
	dec := dr.Entity.ID
	var reqs []string
	for _, title := range []string{"RA", "RB"} {
		r := entity.New("", "requirement")
		r.SetString("title", title)
		rr, err := mgr.CreateEntity(ctx, r, entity.CreateOptions{})
		if err != nil {
			t.Fatalf("create requirement: %v", err)
		}
		reqs = append(reqs, rr.Entity.ID)
		if _, err := mgr.CreateRelation(ctx, dec, "addresses", rr.Entity.ID, entity.RelationOptions{}); err != nil {
			t.Fatalf("CreateRelation: %v", err)
		}
	}
	rec.records = nil // ignore anything before the cascade delete

	if _, err := mgr.DeleteEntity(ctx, dec, true); err != nil {
		t.Fatalf("cascade DeleteEntity: %v", err)
	}
	if len(rec.records) != len(reqs) {
		t.Fatalf("cascade delete must record one relation version per edge; got %d, want %d",
			len(rec.records), len(reqs))
	}
	for _, got := range rec.records {
		if got.Op != store.VersionOpDelete {
			t.Errorf("op = %q, want delete", got.Op)
		}
		if got.TriggeredBy == "" {
			t.Error("cascade-deleted relation version must carry a triggered_by")
		}
	}
}

// TestRelationVersionHook_RenameStitchesEndpoints asserts an entity rename
// records a `rename` relation version per incident edge, on the NEW triple,
// carrying the pre-rename endpoints — so the edge's history stays continuous
// instead of reading as a delete+create.
//
// This is the MANAGER half of rename-version coverage: it asserts the record
// Manager.RenameEntity emits. The STORE half — that pgstore persists that
// rename version on the surviving rel_record_id (no fork) — is asserted by
// pgstore.TestRelationVersionRenameAtomicPath. Neither is redundant.
func TestRelationVersionHook_RenameStitchesEndpoints(t *testing.T) {
	mgr, rec := newRelationVersionManager(t)
	ctx := ctxWithPrincipal("bob", principal.ToolMCP)
	dec, req := seedDecReq(ctx, t, mgr)

	note := "note"
	if _, err := mgr.CreateRelation(ctx, dec, "addresses", req, entity.RelationOptions{Content: &note}); err != nil {
		t.Fatalf("CreateRelation: %v", err)
	}
	rec.records = nil

	const newReq = "REQ-RENAMED"
	if _, err := mgr.RenameEntity(ctx, req, newReq, entity.RenameOptions{}); err != nil {
		t.Fatalf("RenameEntity: %v", err)
	}

	// Exactly one relation rename version, on the new triple, carrying the old
	// endpoints and the preserved content.
	var renames []entitymanager.RelationVersionRecord
	for _, r := range rec.records {
		if r.Op == store.VersionOpRename {
			renames = append(renames, r)
		}
	}
	if len(renames) != 1 {
		t.Fatalf("rename should record one relation rename version; got %d", len(renames))
	}
	got := renames[0]
	if got.To != newReq {
		t.Errorf("new to = %q, want %q", got.To, newReq)
	}
	if got.PrevTo != req {
		t.Errorf("prev_to = %q, want %q (the pre-rename endpoint)", got.PrevTo, req)
	}
	if got.From != dec {
		t.Errorf("from = %q, want %q (unchanged endpoint)", got.From, dec)
	}
	if got.Content != "note" {
		t.Errorf("content = %q, want preserved 'note'", got.Content)
	}
}
