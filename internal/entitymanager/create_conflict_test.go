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

// conflictOnCreateStore forces CreateEntity to return store.ErrConflict
// (modeling a racing create / duplicate ID landing between any
// pre-check and the write) and counts UpdateEntity calls so a test can
// prove the create path never falls through to an overwrite.
type conflictOnCreateStore struct {
	store.Store
	updateCalls atomic.Int32
}

func (s *conflictOnCreateStore) CreateEntity(_ context.Context, _ *entity.Entity) error {
	return store.ErrConflict
}

func (s *conflictOnCreateStore) UpdateEntity(ctx context.Context, e *entity.Entity) error {
	s.updateCalls.Add(1)
	return s.Store.UpdateEntity(ctx, e)
}

func newManagerOverStore(t *testing.T, st store.Store) *entitymanager.Manager {
	t.Helper()
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       st,
		Meta:        parseMeta(t),
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	return mgr
}

// TestCreate_ConflictDoesNotOverwrite pins that a create whose store
// write conflicts (a racing create / duplicate ID landing on the write)
// is reported as ErrEntityAlreadyExists and never falls through to an
// UpdateEntity. Previously createCore went through upsertEntity
// (Create→conflict→Update), so a racing create silently overwrote the
// other writer's entity. The triggering entity uses an auto-generated
// ID — the conflict-on-write is what matters, independent of how the ID
// was chosen.
func TestCreate_ConflictDoesNotOverwrite(t *testing.T) {
	st := &conflictOnCreateStore{Store: memstore.New()}
	mgr := newManagerOverStore(t, st)

	e := entity.New("", "requirement")
	e.SetString("title", "collides on write")
	_, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})

	if !errors.Is(err, entitymanager.ErrEntityAlreadyExists) {
		t.Fatalf("expected ErrEntityAlreadyExists, got %v", err)
	}
	if got := st.updateCalls.Load(); got != 0 {
		t.Errorf("create fell through to UpdateEntity %d time(s) — must never overwrite on a create", got)
	}
}
