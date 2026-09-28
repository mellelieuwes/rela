package attachment_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"errors"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/app"
	"github.com/Sourcehaven-BV/rela/internal/attachment"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/lock"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/project"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/storage"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/templating"
)

// attachmentTestMetamodel adds a file-typed property to the ticket
// type so Attach can exercise its property-resolution paths.
const attachmentTestMetamodel = `version: "1.0"
entities:
  ticket:
    label: Ticket
    plural: tickets
    id_prefix: "T-"
    id_type: sequential
    properties:
      title:
        type: string
      spec:
        type: file
      gallery:
        type: file
        max: 3
`

// attachmentFixture is the bundle setupAttachmentService returns so
// tests can seed entities directly via the store without re-deriving
// it from the service.
type attachmentFixture struct {
	svc  *attachment.Service
	st   store.Store
	mgr  attachment.EntityPatcher
	meta *metamodel.Metamodel
	root string
}

// setupAttachmentService builds a real-FS-backed service on a
// tempdir. Uses app.FSFactory so the store knows where attachments
// live; mirrors the production wiring path.
func setupAttachmentService(t *testing.T) attachmentFixture {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{
		filepath.Join(root, ".rela"),
		filepath.Join(root, "entities", "tickets"),
		filepath.Join(root, "relations"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	metaPath := filepath.Join(root, "metamodel.yaml")
	if err := os.WriteFile(metaPath, []byte(attachmentTestMetamodel), 0o644); err != nil {
		t.Fatal(err)
	}
	meta, err := metamodel.Parse([]byte(attachmentTestMetamodel))
	if err != nil {
		t.Fatalf("parse metamodel: %v", err)
	}
	ctx := &project.Context{
		Root:         root,
		SchemaPath:   metaPath,
		CacheDir:     filepath.Join(root, ".rela"),
		EntitiesDir:  filepath.Join(root, "entities"),
		RelationsDir: filepath.Join(root, "relations"),
	}
	fs := storage.NewSafeFS(storage.NewOsFS())
	factory := &app.FSFactory{FS: fs, Paths: ctx}
	st, err := factory.OpenStore(meta)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       st,
		Meta:        meta,
		Templater:   templating.NewFSTemplater(fs, ctx),
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	svc, err := attachment.New(attachment.Deps{
		Store:         st,
		Meta:          meta,
		EntityManager: mgr,
		Locker:        lock.NewMemoryLocker(),
		Authorizer:    attachment.AllowAllWrites{},
	})
	if err != nil {
		t.Fatalf("attachment.New: %v", err)
	}
	return attachmentFixture{svc: svc, st: st, mgr: mgr, meta: meta, root: root}
}

func TestService_AttachAndList(t *testing.T) {
	f := setupAttachmentService(t)

	e := entity.New("T-1", "ticket")
	e.Properties["title"] = "host"
	if err := f.st.CreateEntity(context.Background(), e); err != nil {
		t.Fatalf("create entity: %v", err)
	}

	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "design.pdf")
	payload := []byte("pretend pdf")
	if err := os.WriteFile(srcPath, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := f.svc.Attach(context.Background(), "T-1", srcPath, "spec")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if result.FileName != "design.pdf" {
		t.Errorf("FileName = %q, want design.pdf", result.FileName)
	}

	onDisk := filepath.Join(f.root, "attachments", "T-1", "spec", "design.pdf")
	got, err := os.ReadFile(onDisk)
	if err != nil {
		t.Fatalf("read attachment: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload on disk = %q, want %q", got, payload)
	}

	infos, err := f.svc.List(context.Background(), "T-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("got %d attachments, want 1", len(infos))
	}
	info := infos[0]
	if info.FileName != "design.pdf" {
		t.Errorf("FileName = %q", info.FileName)
	}
	if info.Property != "spec" {
		t.Errorf("Property = %q", info.Property)
	}
	if !strings.Contains(info.Path, "attachments/T-1/spec/design.pdf") {
		t.Errorf("Path = %q", info.Path)
	}
	if info.ContentType != "application/pdf" {
		t.Errorf("ContentType = %q, want application/pdf", info.ContentType)
	}
}

func TestService_Attach_UnknownEntity(t *testing.T) {
	f := setupAttachmentService(t)
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "x.bin")
	_ = os.WriteFile(src, []byte("x"), 0o644)

	_, err := f.svc.Attach(context.Background(), "T-MISSING", src, "spec")
	if err == nil {
		t.Fatal("expected error for missing entity")
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want store.ErrNotFound (wrapped)", err)
	}
	if !strings.Contains(err.Error(), "T-MISSING") {
		t.Errorf("err = %v, want entity ID in message", err)
	}
}

func TestService_Attach_NonFileProperty(t *testing.T) {
	f := setupAttachmentService(t)

	e := entity.New("T-2", "ticket")
	e.Properties["title"] = "host"
	if err := f.st.CreateEntity(context.Background(), e); err != nil {
		t.Fatalf("create entity: %v", err)
	}

	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "x.bin")
	_ = os.WriteFile(src, []byte("x"), 0o644)

	_, err := f.svc.Attach(context.Background(), "T-2", src, "title")
	if err == nil {
		t.Fatal("expected error for non-file property")
	}
	if !strings.Contains(err.Error(), "not a file type") {
		t.Errorf("err = %v, want non-file-type", err)
	}
}

func TestService_New_RejectsNilDeps(t *testing.T) {
	cases := []struct {
		name string
		d    attachment.Deps
		want string
	}{
		{"nil store", attachment.Deps{Store: nil}, "Store is required"},
		{"nil meta", attachment.Deps{Store: storeStub{}}, "Meta is required"},
		{"nil em", attachment.Deps{Store: storeStub{}, Meta: &metamodel.Metamodel{}}, "EntityManager is required"},
		{"nil locker", attachment.Deps{
			Store: storeStub{}, Meta: &metamodel.Metamodel{}, EntityManager: &countingPatcher{},
		}, "Locker is required"},
		{"nil authorizer", attachment.Deps{
			Store: storeStub{}, Meta: &metamodel.Metamodel{}, EntityManager: &countingPatcher{},
			Locker: lock.NewMemoryLocker(),
		}, "Authorizer is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := attachment.New(tc.d)
			if err == nil {
				t.Fatalf("expected error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

// storeStub satisfies store.Store via embedded nil — type-correct
// but every method would nil-deref. Used ONLY in
// TestService_New_RejectsNilDeps to advance past the Store nil-check
// in New so the subsequent Meta / EntityManager nil-checks can be
// exercised. Never invoked.
type storeStub struct{ store.Store }

// failingReader yields some bytes then errors, to force AttachFile to fail
// AFTER a prior attachment exists — exercising the data-loss ordering.
type failingReader struct{ n int }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n > 0 {
		f.n--
		p[0] = 'x'
		return 1, nil
	}
	return 0, errors.New("simulated mid-stream read error")
}

// TestService_ReplaceFailureKeepsExisting pins RR-N96YV0: at max==1, if the
// new AttachFile fails, the existing attachment must survive (attach-first,
// then delete-old ordering — not delete-then-attach).
func TestService_ReplaceFailureKeepsExisting(t *testing.T) {
	f := setupAttachmentService(t)
	ctx := context.Background()
	e := entity.New("T-1", "ticket")
	require := func(err error, msg string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", msg, err)
		}
	}
	require(f.st.CreateEntity(ctx, e), "create entity")

	def := metamodel.PropertyDef{Type: metamodel.PropertyTypeFile} // max:1
	// Attach a valid original.
	_, err := f.svc.WriteAttachment(ctx, e, def, "spec", "ok.pdf", strings.NewReader("original"))
	require(err, "initial attach")

	// Replace with a different name whose reader fails mid-stream.
	_, err = f.svc.WriteAttachment(ctx, e, def, "spec", "new.pdf", &failingReader{n: 2})
	if err == nil {
		t.Fatal("expected the failing replace to error")
	}

	// The original must still be present and readable.
	rc, readErr := f.st.ReadAttachment(ctx, "T-1", "spec", "ok.pdf")
	if readErr != nil {
		t.Fatalf("original attachment was destroyed by a failed replace: %v", readErr)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != "original" {
		t.Errorf("original bytes = %q, want %q", data, "original")
	}
}

// TestService_MultiAppendAndCap pins append-up-to-max + auto-suffix + the
// ErrAtCapacity sentinel on a max:3 property.
func TestService_MultiAppendAndCap(t *testing.T) {
	f := setupAttachmentService(t)
	ctx := context.Background()
	e := entity.New("T-1", "ticket")
	if err := f.st.CreateEntity(ctx, e); err != nil {
		t.Fatalf("create entity: %v", err)
	}
	def := metamodel.PropertyDef{Type: metamodel.PropertyTypeFile, Max: 3}

	for _, n := range []string{"a.pdf", "b.pdf", "c.pdf"} {
		if _, err := f.svc.WriteAttachment(ctx, e, def, "gallery", n, strings.NewReader(n)); err != nil {
			t.Fatalf("append %s: %v", n, err)
		}
	}
	// Duplicate name auto-suffixes (this would exceed 3 → at-capacity first).
	if _, err := f.svc.WriteAttachment(ctx, e, def, "gallery", "d.pdf", strings.NewReader("d")); !errors.Is(err, attachment.ErrAtCapacity) {
		t.Errorf("4th append: err = %v, want ErrAtCapacity", err)
	}
}

func TestService_DetachFile(t *testing.T) {
	f := setupAttachmentService(t)
	ctx := context.Background()
	e := entity.New("T-1", "ticket")
	if err := f.st.CreateEntity(ctx, e); err != nil {
		t.Fatalf("create entity: %v", err)
	}
	gallery := metamodel.PropertyDef{Type: metamodel.PropertyTypeFile, Max: 3}

	// Unnamed on an empty property: nothing to pick.
	if _, err := f.svc.DetachFile(ctx, e, gallery, "gallery", ""); err == nil {
		t.Fatal("unnamed detach on an empty property: want error")
	}
	// Named but absent: succeeds and reports that nothing was removed.
	if removed, err := f.svc.DetachFile(ctx, e, gallery, "gallery", "gone.pdf"); err != nil || removed != "" {
		t.Fatalf("absent file: removed=%q err=%v, want \"\" and nil", removed, err)
	}

	for _, n := range []string{"a.pdf", "b.pdf"} {
		if _, err := f.svc.WriteAttachment(ctx, e, gallery, "gallery", n, strings.NewReader(n)); err != nil {
			t.Fatalf("attach %s: %v", n, err)
		}
	}
	// Unnamed with two files: the caller must choose.
	if _, err := f.svc.DetachFile(ctx, e, gallery, "gallery", ""); err == nil || !strings.Contains(err.Error(), "a.pdf") {
		t.Fatalf("unnamed detach with two files: err = %v, want one listing the files", err)
	}
	if removed, err := f.svc.DetachFile(ctx, e, gallery, "gallery", "a.pdf"); err != nil || removed != "a.pdf" {
		t.Fatalf("named detach: removed=%q err=%v", removed, err)
	}
	// Unnamed with one file left removes it.
	if removed, err := f.svc.DetachFile(ctx, e, gallery, "gallery", ""); err != nil || removed != "b.pdf" {
		t.Fatalf("unnamed detach: removed=%q err=%v", removed, err)
	}
	got, err := f.st.GetEntity(ctx, "T-1")
	if err != nil {
		t.Fatalf("get entity: %v", err)
	}
	if paths, _ := got.Properties["gallery"].([]string); len(paths) != 0 {
		t.Fatalf("gallery after detaching all = %#v, want empty", got.Properties["gallery"])
	}
}

// TestService_StampPreservesOtherProperties pins that stamping names only the
// file property: an edit made after the caller read the entity survives.
func TestService_StampPreservesOtherProperties(t *testing.T) {
	f := setupAttachmentService(t)
	ctx := context.Background()
	e := entity.New("T-1", "ticket")
	e.SetString("title", "before")
	if err := f.st.CreateEntity(ctx, e); err != nil {
		t.Fatalf("create entity: %v", err)
	}

	// A concurrent edit lands after the caller's read of e.
	edited := e.Clone()
	edited.SetString("title", "after")
	if err := f.st.UpdateEntity(ctx, edited); err != nil {
		t.Fatalf("concurrent edit: %v", err)
	}

	spec := metamodel.PropertyDef{Type: metamodel.PropertyTypeFile}
	res, err := f.svc.WriteAttachment(ctx, e, spec, "spec", "s.pdf", strings.NewReader("s"))
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if e.GetString("spec") != "" {
		t.Error("WriteAttachment modified the caller's entity")
	}
	got, err := f.st.GetEntity(ctx, "T-1")
	if err != nil {
		t.Fatalf("get entity: %v", err)
	}
	if got.GetString("title") != "after" {
		t.Errorf("title = %q, want the concurrent edit preserved", got.GetString("title"))
	}
	if got.GetString("spec") != res.Path || res.Entity == nil || res.Entity.GetString("spec") != res.Path {
		t.Errorf("spec = %q, result %+v", got.GetString("spec"), res)
	}
}

// countingPatcher counts entity writes.
type countingPatcher struct {
	attachment.EntityPatcher
	n int
}

func (c *countingPatcher) PatchEntity(ctx context.Context, id string, p entity.Patch) (*entity.UpdateResult, error) {
	c.n++
	return c.EntityPatcher.PatchEntity(ctx, id, p)
}

// TestService_DeleteAbsentFileDoesNotWrite pins that a retried delete leaves
// the entity alone: every write is audited and runs automations.
func TestService_DeleteAbsentFileDoesNotWrite(t *testing.T) {
	f := setupAttachmentService(t)
	ctx := context.Background()
	e := entity.New("T-1", "ticket")
	if err := f.st.CreateEntity(ctx, e); err != nil {
		t.Fatalf("create entity: %v", err)
	}
	counter := &countingPatcher{EntityPatcher: f.mgr}
	svc, err := attachment.New(attachment.Deps{
		Store: f.st, Meta: f.meta, EntityManager: counter, Locker: lock.NewMemoryLocker(),
		Authorizer: attachment.AllowAllWrites{},
	})
	if err != nil {
		t.Fatalf("attachment.New: %v", err)
	}
	for _, def := range []metamodel.PropertyDef{
		{Type: metamodel.PropertyTypeFile},
		{Type: metamodel.PropertyTypeFile, Max: 3},
	} {
		if _, err := svc.DetachFile(ctx, e, def, "gallery", "gone.pdf"); err != nil {
			t.Fatalf("detach absent file: %v", err)
		}
	}
	if counter.n != 0 {
		t.Fatalf("PatchEntity called %d times for absent files, want 0", counter.n)
	}

	// A stale value naming a file that is gone is still repaired.
	e.Properties["spec"] = "attachments/T-1/spec/gone.pdf"
	if _, err := svc.DetachFile(ctx, e, metamodel.PropertyDef{Type: metamodel.PropertyTypeFile}, "spec", "gone.pdf"); err != nil {
		t.Fatalf("detach stale file: %v", err)
	}
	if counter.n != 1 {
		t.Fatalf("PatchEntity called %d times for a stale value, want 1", counter.n)
	}
}
