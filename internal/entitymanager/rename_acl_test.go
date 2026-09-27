package entitymanager_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// flakyGetStore returns a non-not-found error from GetEntity and counts
// mutating store calls, so a test can assert that a rename gated behind
// a failed pre-fetch never reaches the actual rename writes.
type flakyGetStore struct {
	store.Store
	err     error
	deletes atomic.Int32
	creates atomic.Int32
}

func (s *flakyGetStore) GetEntity(_ context.Context, _ string) (*entity.Entity, error) {
	return nil, s.err
}

func (s *flakyGetStore) DeleteEntity(ctx context.Context, id string, cascade bool) (*store.DeleteResult, error) {
	s.deletes.Add(1)
	return s.Store.DeleteEntity(ctx, id, cascade)
}

func (s *flakyGetStore) CreateEntity(ctx context.Context, e *entity.Entity) error {
	s.creates.Add(1)
	return s.Store.CreateEntity(ctx, e)
}

// TestRename_FailsClosedOnNonNotFoundFetchError pins that a transient
// GetEntity error during RenameEntity's ACL pre-fetch fails closed: the
// error surfaces and the rename never runs. Previously the ACL block was
// gated on `err == nil`, so any non-not-found error skipped authorization
// entirely and proceeded to rename — a store hiccup turned an ACL-gated
// operation into an ungated one.
func TestRename_FailsClosedOnNonNotFoundFetchError(t *testing.T) {
	sentinel := errors.New("boom: transient backend error")
	st := &flakyGetStore{Store: memstore.New(), err: sentinel}

	// Deny-all ACL: if the gate were skipped (the bug), the rename would
	// proceed without ever consulting the ACL. With the fix the fetch
	// error short-circuits before either the ACL or the rename run.
	deps := entitymanager.Deps{
		Store:       st,
		Meta:        parseMeta(t),
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.ReadOnlyACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	}
	mgr, err := entitymanager.New(deps)
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}

	_, err = mgr.RenameEntity(context.Background(), "REQ-1", "REQ-2", entity.RenameOptions{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the underlying fetch error to surface, got %v", err)
	}
	if got := st.deletes.Load() + st.creates.Load(); got != 0 {
		t.Errorf("rename mutated the store despite a failed pre-fetch: %d write calls", got)
	}
}

// TestRename_NotFoundStillReturnsTypedError pins that a genuine
// not-found during the pre-fetch still falls through to renameEntity's
// ErrEntityNotFound — the fail-closed change must not break the
// not-found path.
func TestRename_NotFoundStillReturnsTypedError(t *testing.T) {
	mgr := newManagerWithAudit(t, audit.Nop{}, nil)

	_, err := mgr.RenameEntity(context.Background(), "REQ-MISSING", "REQ-NEW", entity.RenameOptions{})
	if !errors.Is(err, entitymanager.ErrEntityNotFound) {
		t.Fatalf("expected ErrEntityNotFound, got %v", err)
	}
}

// TestDelete_FailsClosedOnNonNotFoundFetchError pins the same fail-closed
// property for DeleteEntity that the rename test pins for RenameEntity.
//
// Both resolve the target through anyFaceOf, whose first lookup is a plain
// GetEntity. If a non-not-found error there were swallowed, the entity would
// look missing, and the not-found branch skips the ACL check by design — so a
// transient store error would silently turn an ACL-gated delete into an
// ungated one. Rename had this test; delete did not, which is how the same
// defect reached both paths at once during BUG-HC6I2T.
func TestDelete_FailsClosedOnNonNotFoundFetchError(t *testing.T) {
	sentinel := errors.New("boom: transient backend error")
	st := &flakyGetStore{Store: memstore.New(), err: sentinel}

	deps := entitymanager.Deps{
		Store:       st,
		Meta:        parseMeta(t),
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.ReadOnlyACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	}
	mgr, err := entitymanager.New(deps)
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}

	_, err = mgr.DeleteEntity(context.Background(), "REQ-1", false)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the underlying fetch error to surface, got %v", err)
	}
	if got := st.deletes.Load(); got != 0 {
		t.Errorf("delete mutated the store despite a failed pre-fetch: %d delete calls", got)
	}
}
