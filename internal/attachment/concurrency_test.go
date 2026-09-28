package attachment_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/attachment"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/lock"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
)

// raceUploads runs n concurrent WriteAttachment calls to one property, each
// with a distinct file name, and returns each call's error.
func raceUploads(t *testing.T, f attachmentFixture, e *entity.Entity, def metamodel.PropertyDef,
	prop string, n int,
) []error {
	t.Helper()
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			name := fmt.Sprintf("f%d.pdf", i)
			_, errs[i] = f.svc.WriteAttachment(context.Background(), e, def, prop, name, strings.NewReader(name))
		})
	}
	close(start)
	wg.Wait()
	return errs
}

// raceEntity is the entity every race in this file writes to.
const raceEntity = "T-1"

// propertyFiles returns the files the service lists for prop on raceEntity.
func propertyFiles(t *testing.T, f attachmentFixture, prop string) []string {
	t.Helper()
	infos, err := f.svc.List(context.Background(), raceEntity)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var names []string
	for _, info := range infos {
		if info.Property == prop {
			names = append(names, info.FileName)
		}
	}
	return names
}

// TestService_ConcurrentSingleFileUploadsLeaveOneFile pins that concurrent
// replacements of a single-file property settle on exactly one file, and that
// the property stamp names that file (TKT-WE0S2K: the per-property lock
// replaced the process-wide write lock).
func TestService_ConcurrentSingleFileUploadsLeaveOneFile(t *testing.T) {
	f := setupAttachmentService(t)
	e := entity.New(raceEntity, "ticket")
	if err := f.st.CreateEntity(context.Background(), e); err != nil {
		t.Fatalf("create entity: %v", err)
	}
	def := metamodel.PropertyDef{Type: metamodel.PropertyTypeFile}

	for i, err := range raceUploads(t, f, e, def, "spec", 8) {
		if err != nil {
			t.Errorf("upload %d: %v", i, err)
		}
	}

	files := propertyFiles(t, f, "spec")
	if len(files) != 1 {
		t.Fatalf("spec holds %v, want exactly one file", files)
	}
	stored, err := f.st.GetEntity(context.Background(), raceEntity)
	if err != nil {
		t.Fatalf("get entity: %v", err)
	}
	if got := fmt.Sprint(stored.Properties["spec"]); !strings.Contains(got, files[0]) {
		t.Errorf("spec stamp = %q, want it to name %s", got, files[0])
	}
}

// TestService_ConcurrentAppendsRespectCap pins that concurrent appends to a
// multi-file property never exceed its max, and that the stamp lists every
// file that landed.
func TestService_ConcurrentAppendsRespectCap(t *testing.T) {
	f := setupAttachmentService(t)
	e := entity.New(raceEntity, "ticket")
	if err := f.st.CreateEntity(context.Background(), e); err != nil {
		t.Fatalf("create entity: %v", err)
	}
	def := metamodel.PropertyDef{Type: metamodel.PropertyTypeFile, Max: 3}

	ok := 0
	for i, err := range raceUploads(t, f, e, def, "gallery", 8) {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, attachment.ErrAtCapacity):
			t.Errorf("upload %d: %v, want nil or ErrAtCapacity", i, err)
		}
	}
	if ok != 3 {
		t.Errorf("%d uploads succeeded, want 3", ok)
	}

	files := propertyFiles(t, f, "gallery")
	if len(files) != 3 {
		t.Fatalf("gallery holds %v, want 3 files", files)
	}
	stored, err := f.st.GetEntity(context.Background(), raceEntity)
	if err != nil {
		t.Fatalf("get entity: %v", err)
	}
	stamp := fmt.Sprint(stored.Properties["gallery"])
	for _, name := range files {
		if !strings.Contains(stamp, name) {
			t.Errorf("gallery stamp %q is missing %s", stamp, name)
		}
	}
}

// expiredLocker fails every Acquire as if its wait deadline had passed.
type expiredLocker struct{}

func (expiredLocker) Acquire(context.Context, string) (func(), error) {
	return nil, fmt.Errorf("acquire: %w", context.DeadlineExceeded)
}

func TestService_LockWaitExpiryIsErrBusy(t *testing.T) {
	f := setupAttachmentService(t)
	e := entity.New(raceEntity, "ticket")
	if err := f.st.CreateEntity(context.Background(), e); err != nil {
		t.Fatalf("create entity: %v", err)
	}
	svc, err := attachment.New(attachment.Deps{
		Store: f.st, Meta: f.meta, EntityManager: f.mgr, Locker: expiredLocker{}, Authorizer: attachment.AllowAllWrites{},
	})
	if err != nil {
		t.Fatalf("attachment.New: %v", err)
	}
	def := metamodel.PropertyDef{Type: metamodel.PropertyTypeFile}

	_, err = svc.WriteAttachment(context.Background(), e, def, "spec", "a.pdf", strings.NewReader("a"))
	if !errors.Is(err, attachment.ErrBusy) {
		t.Errorf("err = %v, want ErrBusy", err)
	}
	if files := propertyFiles(t, f, "spec"); len(files) != 0 {
		t.Errorf("busy write left files %v", files)
	}
}

// TestService_CallerDeadlineIsNotErrBusy pins that only the service's own
// wait bound is reported as ErrBusy: a caller whose deadline passes while it
// waits gets its own context error.
func TestService_CallerDeadlineIsNotErrBusy(t *testing.T) {
	f := setupAttachmentService(t)
	e := entity.New(raceEntity, "ticket")
	if err := f.st.CreateEntity(context.Background(), e); err != nil {
		t.Fatalf("create entity: %v", err)
	}
	held := lock.NewMemoryLocker()
	release, err := held.Acquire(context.Background(), "attachment/T-1/spec")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	svc, err := attachment.New(attachment.Deps{
		Store: f.st, Meta: f.meta, EntityManager: f.mgr, Locker: held, Authorizer: attachment.AllowAllWrites{},
	})
	if err != nil {
		t.Fatalf("attachment.New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	def := metamodel.PropertyDef{Type: metamodel.PropertyTypeFile}

	_, err = svc.WriteAttachment(ctx, e, def, "spec", "a.pdf", strings.NewReader("a"))
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, attachment.ErrBusy) {
		t.Errorf("err = %v, want the caller's DeadlineExceeded and not ErrBusy", err)
	}
}

// denyWrites refuses every write, standing in for a grant revoked while an
// upload was in flight.
type denyWrites struct{}

var errRevoked = errors.New("revoked")

func (denyWrites) AuthorizeAttachmentWrite(context.Context, *entity.Entity) error { return errRevoked }

// TestService_ReauthorizesUnderLock pins that the service asks its
// authorizer under the property lock, before any store write: a denial there
// leaves the existing file and the property untouched.
func TestService_ReauthorizesUnderLock(t *testing.T) {
	f := setupAttachmentService(t)
	ctx := context.Background()
	e := entity.New(raceEntity, "ticket")
	if err := f.st.CreateEntity(ctx, e); err != nil {
		t.Fatalf("create entity: %v", err)
	}
	def := metamodel.PropertyDef{Type: metamodel.PropertyTypeFile}
	if _, err := f.svc.WriteAttachment(ctx, e, def, "spec", "approved.pdf", strings.NewReader("v1")); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}
	seeded, err := f.st.GetEntity(ctx, raceEntity)
	if err != nil {
		t.Fatal(err)
	}
	denied, err := attachment.New(attachment.Deps{
		Store: f.st, Meta: f.meta, EntityManager: f.mgr,
		Locker: lock.NewMemoryLocker(), Authorizer: denyWrites{},
	})
	if err != nil {
		t.Fatalf("attachment.New: %v", err)
	}

	for name, write := range map[string]func() error{
		"replace": func() error {
			_, err := denied.WriteAttachment(ctx, seeded, def, "spec", "other.pdf", strings.NewReader("v2"))
			return err
		},
		"delete": func() error { return denied.DeleteAttachment(ctx, seeded, def, "spec", "approved.pdf") },
		"detach": func() error {
			_, err := denied.DetachFile(ctx, seeded, def, "spec", "")
			return err
		},
	} {
		if err := write(); !errors.Is(err, errRevoked) {
			t.Errorf("%s: err = %v, want the authorizer's denial", name, err)
		}
	}
	if files := propertyFiles(t, f, "spec"); len(files) != 1 || files[0] != "approved.pdf" {
		t.Errorf("spec holds %v after denied writes, want [approved.pdf]", files)
	}
}
