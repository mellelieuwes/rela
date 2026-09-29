package entitymanager_test

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// listEntitiesErrStore yields the wrapped store's entities, then a
// terminal error — modeling a scan that fails partway through. ID
// generation must not run on this partial list.
type listEntitiesErrStore struct {
	store.Store
	err error
}

func (s *listEntitiesErrStore) ListEntities(ctx context.Context, q store.EntityQuery) iter.Seq2[*entity.Entity, error] {
	inner := s.Store.ListEntities(ctx, q)
	return func(yield func(*entity.Entity, error) bool) {
		for e, err := range inner {
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(e, nil) {
				return
			}
		}
		yield(nil, s.err)
	}
}

// listRelationsErrStore yields a terminal error from ListRelations,
// modeling a relation scan that fails before the full incident set is
// counted. The delete-safety gate must not run on partial data.
type listRelationsErrStore struct {
	store.Store
	err error
}

// Tx hands the callback THIS decorator so the ListRelations override stays
// effective inside a transaction; the embedded store's Tx would bypass it.
func (s *listRelationsErrStore) Tx(_ context.Context, fn func(store.Store) error) error {
	return fn(s)
}

func (s *listRelationsErrStore) ListRelations(_ context.Context, _ store.RelationQuery) iter.Seq2[*entity.Relation, error] {
	return func(yield func(*entity.Relation, error) bool) {
		yield(nil, s.err)
	}
}

func newManagerOver(t *testing.T, st store.Store) *entitymanager.Manager {
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

// TestCreate_FailsWhenIDScanErrors pins that auto-ID create surfaces an
// entity-scan error instead of generating an ID from a partial list. A
// truncated scan can hide a high-numbered existing ID, so generating
// from it risks a collision — which createCore now rejects with
// ErrEntityAlreadyExists (a create never overwrites), so a bad scan
// would surface as a spurious conflict rather than data loss.
func TestCreate_FailsWhenIDScanErrors(t *testing.T) {
	sentinel := errors.New("boom: entity scan failed mid-stream")
	st := &listEntitiesErrStore{Store: memstore.New(), err: sentinel}
	mgr := newManagerOver(t, st)

	e := entity.New("", "requirement") // auto-ID path → triggers collectAllIDs
	e.SetString("title", "needs an id")
	_, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the entity-scan error to surface, got %v", err)
	}
}

// TestDelete_FailsWhenRelationScanErrors pins that DeleteEntity refuses
// to proceed when the incident-relation scan errors, rather than
// under-counting and deleting an entity that may still have relations
// (the orphan class issue #888 closed at the deletion step).
func TestDelete_FailsWhenRelationScanErrors(t *testing.T) {
	sentinel := errors.New("boom: relation scan failed")

	// Seed the entity in the underlying store so the GetEntity lookup
	// and ACL check pass; the relation scan is what fails.
	mem := memstore.New()
	target := entity.New("REQ-1", "requirement")
	target.SetString("title", "has relations on disk")
	if err := mem.CreateEntity(context.Background(), target); err != nil {
		t.Fatalf("seed: %v", err)
	}

	st := &listRelationsErrStore{Store: mem, err: sentinel}
	mgr := newManagerOver(t, st)

	_, err := mgr.DeleteEntity(context.Background(), "REQ-1", false)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the relation-scan error to surface, got %v", err)
	}
}
