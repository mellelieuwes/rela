package entitymanager_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/comments"
	"github.com/Sourcehaven-BV/rela/internal/comments/memcomments"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// recordingRewriter captures the hook calls so a test can assert the
// entitymanager reported old→new, which is the one moment that link is knowable.
type recordingRewriter struct {
	renames [][2]string
	deletes []string
	// faceDeletes records `id@face` per face-delete notification.
	faceDeletes []string
	renameErr   error
	deleteErr   error
}

func (r *recordingRewriter) EntityRenamed(_ context.Context, oldID, newID string) error {
	r.renames = append(r.renames, [2]string{oldID, newID})
	return r.renameErr
}

func (r *recordingRewriter) EntityDeleted(_ context.Context, id string) error {
	r.deletes = append(r.deletes, id)
	return r.deleteErr
}

func (r *recordingRewriter) EntityFaceDeleted(_ context.Context, id string, face entity.Face) error {
	r.faceDeletes = append(r.faceDeletes, entity.FormatStateRef(id, face))
	return r.deleteErr
}

func aliasHookMeta(t *testing.T) *metamodel.Metamodel {
	t.Helper()
	return &metamodel.Metamodel{
		Version: "1.0",
		Entities: map[string]metamodel.EntityDef{
			"task": {
				Label: "Task", IDPrefix: "TSK-", DisplayProperty: "title",
				Properties: map[string]metamodel.PropertyDef{
					"title": {Type: metamodel.PropertyTypeString},
				},
			},
		},
	}
}

func aliasHookManager(t *testing.T, rw entitymanager.AliasRewriter) (*entitymanager.Manager, *memstore.MemStore) {
	t.Helper()
	st := memstore.New()
	mgr, err := entitymanager.New(entitymanager.Deps{
		FieldGate:     entitymanager.AllowAllFieldGate{},
		Store:         st,
		Meta:          aliasHookMeta(t),
		Templater:     nopTemplater{},
		Audit:         audit.Nop{},
		ACL:           acl.NopACL{},
		Transitions:   statemachine.EmptySet(),
		AliasRewriter: rw,
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	return mgr, st
}

// TestAliasHook_FiresOnRename is the load-bearing case. The rename choke-point
// is the ONLY place old→new is knowable — a later sweep sees an ordinary update
// — so if the hook does not fire here, a CalDAV client sees a delete plus a
// create and the user's to-do silently duplicates.
func TestAliasHook_FiresOnRename(t *testing.T) {
	rw := &recordingRewriter{}
	mgr, st := aliasHookManager(t, rw)
	seedTask(t, st, "TSK-old", map[string]any{"title": "a task"}, "")

	if _, err := mgr.RenameEntity(t.Context(), "TSK-old", "TSK-new", entity.RenameOptions{}); err != nil {
		t.Fatalf("RenameEntity: %v", err)
	}

	if len(rw.renames) != 1 {
		t.Fatalf("rename hook fired %d times, want 1", len(rw.renames))
	}
	if got := rw.renames[0]; got != [2]string{"TSK-old", "TSK-new"} {
		t.Errorf("hook received %v, want [TSK-old TSK-new]", got)
	}
}

// TestAliasHook_NotFiredOnDryRun: a planned rename changes nothing, so
// rewriting an alias would point it at an id that does not exist.
func TestAliasHook_NotFiredOnDryRun(t *testing.T) {
	rw := &recordingRewriter{}
	mgr, st := aliasHookManager(t, rw)
	seedTask(t, st, "TSK-old", map[string]any{"title": "a task"}, "")

	if _, err := mgr.RenameEntity(t.Context(), "TSK-old", "TSK-new",
		entity.RenameOptions{DryRun: true}); err != nil {
		t.Fatalf("RenameEntity(dry-run): %v", err)
	}
	if len(rw.renames) != 0 {
		t.Errorf("a dry-run rename fired the alias hook: %v", rw.renames)
	}
}

func TestAliasHook_FiresOnDelete(t *testing.T) {
	rw := &recordingRewriter{}
	mgr, st := aliasHookManager(t, rw)
	seedTask(t, st, "TSK-gone", map[string]any{"title": "a task"}, "")

	if _, err := mgr.DeleteEntity(t.Context(), "TSK-gone", false); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
	if len(rw.deletes) != 1 || rw.deletes[0] != "TSK-gone" {
		t.Errorf("delete hook received %v, want [TSK-gone]", rw.deletes)
	}
}

// TestAliasHook_FiresOnFaceDelete pins BUG-R1PQY9: a face delete fired no
// hook, so a subscriber keyed per face (comment threads) kept the deleted
// face's data, and a face of the same name created later inherited it. The
// entity survives, so EntityDeleted would be wrong: it would drop the sibling
// faces' data too.
func TestAliasHook_FiresOnFaceDelete(t *testing.T) {
	rw := &recordingRewriter{}
	mgr, st := aliasHookManager(t, rw)
	seedTask(t, st, "TSK-1", map[string]any{"title": "a task"}, "")
	if err := st.CreateEntity(t.Context(), &entity.Entity{
		ID: "TSK-1", Type: "task", Face: "draft", Properties: map[string]any{"title": "draft"},
	}); err != nil {
		t.Fatalf("seed draft face: %v", err)
	}

	if _, err := mgr.DeleteEntityFace(t.Context(), "TSK-1", "draft"); err != nil {
		t.Fatalf("DeleteEntityFace: %v", err)
	}
	if len(rw.faceDeletes) != 1 || rw.faceDeletes[0] != "TSK-1@draft" {
		t.Errorf("face-delete hook received %v, want [TSK-1@draft]", rw.faceDeletes)
	}
	if len(rw.deletes) != 0 {
		t.Errorf("a face delete fired the entity-delete hook: %v", rw.deletes)
	}
}

// TestFaceDelete_DropsTheRealCommentThread drives the face delete into a real
// comments.Service rather than a recorder, so the id and face the Manager
// passes are checked against the key the comment store actually files under.
func TestFaceDelete_DropsTheRealCommentThread(t *testing.T) {
	svc, err := comments.NewService(memcomments.New(), nil)
	if err != nil {
		t.Fatalf("comments.NewService: %v", err)
	}
	mgr, st := aliasHookManager(t, svc)
	seedTask(t, st, "TSK-1", map[string]any{"title": "a task"}, "")
	if err := st.CreateEntity(t.Context(), &entity.Entity{
		ID: "TSK-1", Type: "task", Face: "draft", Properties: map[string]any{"title": "draft"},
	}); err != nil {
		t.Fatalf("seed draft face: %v", err)
	}
	ctx := principal.With(t.Context(), principal.Principal{User: "alice", Tool: "test"})
	for _, face := range []entity.Face{"", "draft"} {
		if _, err := svc.Add(ctx, comments.Target{Type: "task", ID: "TSK-1", Face: face},
			comments.AddRequest{Anchor: comments.Anchor{Kind: comments.AnchorProperty, Ref: "title"}, Body: "x"}); err != nil {
			t.Fatalf("seed comment on %q: %v", face, err)
		}
	}

	if _, err := mgr.DeleteEntityFace(ctx, "TSK-1", "draft"); err != nil {
		t.Fatalf("DeleteEntityFace: %v", err)
	}

	for face, want := range map[entity.Face]int{"draft": 0, "": 1} {
		got, err := svc.List(ctx, comments.Target{Type: "task", ID: "TSK-1", Face: face})
		if err != nil {
			t.Fatalf("List %q: %v", face, err)
		}
		if len(got) != want {
			t.Errorf("face %q has %d comments after the draft delete, want %d", face, len(got), want)
		}
	}
}

// TestAliasHook_ErrorDoesNotFailTheWrite pins the best-effort contract: the
// store change already happened and cannot be unwound, so reporting an error
// for a write that DID land would be worse than logging. The residual risk (an
// orphaned alias) is documented on AliasRewriter.
func TestAliasHook_ErrorDoesNotFailTheWrite(t *testing.T) {
	rw := &recordingRewriter{
		renameErr: errors.New("alias store unavailable"),
		deleteErr: errors.New("alias store unavailable"),
	}
	mgr, st := aliasHookManager(t, rw)
	seedTask(t, st, "TSK-old", map[string]any{"title": "a task"}, "")

	if _, err := mgr.RenameEntity(t.Context(), "TSK-old", "TSK-new", entity.RenameOptions{}); err != nil {
		t.Fatalf("a failing alias hook must not fail the rename: %v", err)
	}
	// And the rename really landed.
	if _, err := st.GetEntity(t.Context(), "TSK-new"); err != nil {
		t.Errorf("renamed entity is missing: %v", err)
	}

	if _, err := mgr.DeleteEntity(t.Context(), "TSK-new", false); err != nil {
		t.Fatalf("a failing alias hook must not fail the delete: %v", err)
	}
}

// TestAliasHook_NilIsANoOp: the capability is optional, so a build without a
// CalDAV alias service must behave exactly as before.
func TestAliasHook_NilIsANoOp(t *testing.T) {
	mgr, st := aliasHookManager(t, nil)
	seedTask(t, st, "TSK-old", map[string]any{"title": "a task"}, "")

	if _, err := mgr.RenameEntity(t.Context(), "TSK-old", "TSK-new", entity.RenameOptions{}); err != nil {
		t.Fatalf("RenameEntity with no rewriter: %v", err)
	}
	if _, err := st.GetEntity(t.Context(), "TSK-new"); err != nil {
		t.Errorf("rename did not land: %v", err)
	}
	if _, err := mgr.DeleteEntity(t.Context(), "TSK-new", false); err != nil {
		t.Fatalf("DeleteEntity with no rewriter: %v", err)
	}
}
