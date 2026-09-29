package entitymanager_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// raceCreateStore models the postgres multi-writer TOCTOU that BUG-ZWTDH9's
// residual rode on: ApplyEntity's existence probe (GetEntity) observes the id
// as ABSENT — so the intent resolves to CREATE — but a concurrent writer lands
// the id between the probe and the durable write, so CreateEntity conflicts.
//
// GetEntity delegates to the wrapped store (which does NOT yet hold the id), and
// CreateEntity returns store.ErrConflict WITHOUT writing. The wrapped store is
// pre-seeded with the "winner" so the test can assert the loser never
// overwrote or re-typed it. UpdateEntity is counted and delegated, so a
// create-that-fell-through-to-update (the removed upsert fallback) would both
// bump the counter and clobber the seeded winner — exactly what must not happen.
type raceCreateStore struct {
	store.Store
	updateCalls int
}

func (s *raceCreateStore) CreateEntity(_ context.Context, _ *entity.Entity) error {
	return store.ErrConflict
}

func (s *raceCreateStore) UpdateEntity(ctx context.Context, e *entity.Entity) error {
	s.updateCalls++
	return s.Store.UpdateEntity(ctx, e)
}

// GetEntity reports the id as absent so ApplyEntity resolves CREATE intent,
// while the wrapped store separately holds the seeded winner for verification.
func (s *raceCreateStore) GetEntity(_ context.Context, _ string) (*entity.Entity, error) {
	return nil, store.ErrNotFound
}

// GetEntityState is the probe ApplyEntity actually uses (BUG-HC6I2T); it must
// report the same absence or the create intent is never resolved.
func (s *raceCreateStore) GetEntityState(
	_ context.Context, _ string, _ entity.Face,
) (*entity.Entity, error) {
	return nil, store.ErrNotFound
}

// TestApplyEntity_CreateConflict_RejectsAndDoesNotClobber pins that a
// create-intent ApplyEntity whose durable CreateEntity conflicts (a concurrent
// create of the same id) is REJECTED with ErrEntityAlreadyExists and never
// falls through to an UpdateEntity. This closes both residuals at once: the
// lost-update clobber (a racing create is not blindly overwritten) and the
// type-re-type vector (a create-intent write that conflicts can no longer
// become a blind, re-typing update on the postgres multi-writer backend).
func TestApplyEntity_CreateConflict_RejectsAndDoesNotClobber(t *testing.T) {
	inner := memstore.New()
	// The "winner": a secret-typed entity a concurrent writer already landed.
	// The apply under test claims the SAME id with a DIFFERENT type — the
	// re-type attempt must not land.
	meta := typeConfusionMeta(t)
	if err := inner.CreateEntity(context.Background(), &entity.Entity{
		ID: "SECRET-1", Type: "secret", Properties: map[string]any{"title": "winner"},
	}); err != nil {
		t.Fatalf("seed winner: %v", err)
	}

	st := &raceCreateStore{Store: inner}
	mgr, err := entitymanager.New(entitymanager.Deps{
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
		Store:     st, Meta: meta, Templater: nopTemplater{}, Audit: audit.Nop{}, ACL: acl.NopACL{}, Transitions: statemachine.EmptySet(),
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}

	// Create-intent apply (probe says absent) that races a concurrent create.
	_, applyErr := mgr.ApplyEntity(context.Background(), &entity.Entity{
		ID: "SECRET-1", Type: "note", Properties: map[string]any{"title": "loser re-types to note"},
	})
	if applyErr == nil {
		t.Fatal("create-conflict apply succeeded — a racing create was silently overwritten (lost-update residual)")
	}
	if !errors.Is(applyErr, entitymanager.ErrEntityAlreadyExists) {
		t.Fatalf("expected ErrEntityAlreadyExists, got %T: %v", applyErr, applyErr)
	}
	if st.updateCalls != 0 {
		t.Fatalf("create fell through to UpdateEntity %d time(s) — the upsert fallback re-appeared", st.updateCalls)
	}

	// The seeded winner must be untouched: still a secret, original title.
	got, err := inner.GetEntity(context.Background(), "SECRET-1")
	if err != nil {
		t.Fatalf("GetEntity(SECRET-1): %v", err)
	}
	if got.Type != "secret" {
		t.Fatalf("winner was re-typed to %q; the create-conflict became a re-typing overwrite", got.Type)
	}
	if got.GetString("title") != "winner" {
		t.Fatalf("winner title overwritten to %q; the create-conflict clobbered the racing create", got.GetString("title"))
	}
}

// TestApplyEntity_SameTypeCreateConflict_NoClobber is the same-type variant:
// two concurrent creates of the SAME id and SAME type. There is no re-typing
// here, only the lost-update question — the loser must still be rejected, not
// silently overwrite the winner's content.
func TestApplyEntity_SameTypeCreateConflict_NoClobber(t *testing.T) {
	inner := memstore.New()
	meta := typeConfusionMeta(t)
	if err := inner.CreateEntity(context.Background(), &entity.Entity{
		ID: "NOTE-1", Type: "note", Properties: map[string]any{"title": "winner"},
	}); err != nil {
		t.Fatalf("seed winner: %v", err)
	}

	st := &raceCreateStore{Store: inner}
	mgr, err := entitymanager.New(entitymanager.Deps{
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
		Store:     st, Meta: meta, Templater: nopTemplater{}, Audit: audit.Nop{}, ACL: acl.NopACL{}, Transitions: statemachine.EmptySet(),
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}

	_, applyErr := mgr.ApplyEntity(context.Background(), &entity.Entity{
		ID: "NOTE-1", Type: "note", Properties: map[string]any{"title": "loser"},
	})
	if !errors.Is(applyErr, entitymanager.ErrEntityAlreadyExists) {
		t.Fatalf("expected ErrEntityAlreadyExists, got %T: %v", applyErr, applyErr)
	}
	if st.updateCalls != 0 {
		t.Fatalf("same-type create fell through to UpdateEntity %d time(s) — must never clobber", st.updateCalls)
	}
	got, err := inner.GetEntity(context.Background(), "NOTE-1")
	if err != nil {
		t.Fatalf("GetEntity(NOTE-1): %v", err)
	}
	if got.GetString("title") != "winner" {
		t.Fatalf("winner title overwritten to %q; a same-type racing create was clobbered", got.GetString("title"))
	}
}

// raceUpdateStore models the mirror race: ApplyEntity's probe observes the id
// as PRESENT (resolving UPDATE intent), but the row vanishes (a concurrent
// delete) before the durable UpdateEntity, which returns store.ErrNotFound.
type raceUpdateStore struct {
	store.Store
	createCalls int
	stored      *entity.Entity
}

func (s *raceUpdateStore) GetEntity(_ context.Context, _ string) (*entity.Entity, error) {
	return s.stored, nil
}

// GetEntityState is the probe ApplyEntity actually uses (BUG-HC6I2T).
func (s *raceUpdateStore) GetEntityState(
	_ context.Context, _ string, _ entity.Face,
) (*entity.Entity, error) {
	return s.stored, nil
}

func (s *raceUpdateStore) CreateEntity(ctx context.Context, e *entity.Entity) error {
	s.createCalls++
	return s.Store.CreateEntity(ctx, e)
}

func (s *raceUpdateStore) UpdateEntity(_ context.Context, _ *entity.Entity) error {
	return store.ErrNotFound
}

// TestApplyEntity_UpdateVanished_RejectsWithoutCreating pins that an
// update-intent apply whose row vanished concurrently surfaces as
// ErrEntityNotFound and never falls through to a CreateEntity (which would
// resurrect a deleted record). The old upsert did the opposite direction, but
// the invariant is symmetric: an update never becomes a create.
func TestApplyEntity_UpdateVanished_RejectsWithoutCreating(t *testing.T) {
	meta := typeConfusionMeta(t)
	st := &raceUpdateStore{
		Store:  memstore.New(),
		stored: &entity.Entity{ID: "NOTE-1", Type: "note", Properties: map[string]any{"title": "v1"}},
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
		Store:     st, Meta: meta, Templater: nopTemplater{}, Audit: audit.Nop{}, ACL: acl.NopACL{}, Transitions: statemachine.EmptySet(),
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}

	// Same type as stored (so the type-immutability guard passes) → resolves
	// UPDATE intent → durable UpdateEntity returns ErrNotFound.
	_, applyErr := mgr.ApplyEntity(context.Background(), &entity.Entity{
		ID: "NOTE-1", Type: "note", Properties: map[string]any{"title": "v2"},
	})
	if !errors.Is(applyErr, entitymanager.ErrEntityNotFound) {
		t.Fatalf("expected ErrEntityNotFound, got %T: %v", applyErr, applyErr)
	}
	if st.createCalls != 0 {
		t.Fatalf("update fell through to CreateEntity %d time(s) — an update must never become a create", st.createCalls)
	}
}
