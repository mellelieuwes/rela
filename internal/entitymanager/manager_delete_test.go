package entitymanager_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// failingDeleteStore wraps a store and forces DeleteEntity to return a
// sentinel error, modeling an I/O failure during cascade cleanup.
type failingDeleteStore struct {
	store.Store
	err error
}

func (s *failingDeleteStore) DeleteEntity(context.Context, string, bool) (*store.DeleteResult, error) {
	return nil, s.err
}

// Tx hands the callback THIS decorator, not the wrapped store. Without the
// override the embedded Store's Tx would supply its own view and the
// DeleteEntity override above would be invisible inside a transaction —
// the fake would silently stop modeling the failure it exists to model.
func (s *failingDeleteStore) Tx(_ context.Context, fn func(store.Store) error) error {
	return fn(s)
}

// TestDeleteEntity_PropagatesStoreError pins issue #888: a real error from the
// store's cascade delete must surface to the caller, not be swallowed. Before
// the fix, Manager.DeleteEntity looped per-relation and `continue`d past I/O
// errors, deleting the entity anyway and returning success.
func TestDeleteEntity_PropagatesStoreError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("simulated delete failure")
	deps := entitymanager.Deps{
		Store:       &failingDeleteStore{Store: memstore.New(), err: sentinel},
		Meta:        parseMeta(t),
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	}
	mgr, err := entitymanager.New(deps)
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}

	// Seed an entity directly in the underlying store so GetEntity (the
	// pre-delete lookup) succeeds but the delete itself fails. The failing
	// store wraps a memstore; seed via that same memstore through the
	// Manager's create path would also hit the override, so seed the inner
	// store directly.
	inner := deps.Store.(*failingDeleteStore).Store
	if seedErr := inner.CreateEntity(context.Background(), entity.New("REQ-1", "requirement")); seedErr != nil {
		t.Fatalf("seed: %v", seedErr)
	}

	_, err = mgr.DeleteEntity(context.Background(), "REQ-1", true)
	if err == nil {
		t.Fatal("expected DeleteEntity to propagate the store error, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want it to wrap the sentinel store error", err)
	}
}

// TestDeleteEntity_CascadeAuditsReportedRelations pins that the cascade delete
// emits one delete-relation audit record per relation the store reports
// deleting, each carrying the cascade triggered-by, plus the delete-entity
// record (issue #888 — audit preserved after delegating to the store cascade).
func TestDeleteEntity_CascadeAuditsReportedRelations(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()
	deps := entitymanager.Deps{
		Store:       memstore.New(),
		Meta:        parseMeta(t),
		Templater:   nopTemplater{},
		Audit:       mem,
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	}
	mgr, err := entitymanager.New(deps)
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	ctx := context.Background()

	// decision --addresses--> requirement; deleting the requirement cascades
	// the relation. IDs are assigned by the sequential id_type, so use the
	// returned IDs rather than the placeholders passed to entity.New.
	reqRes, err := mgr.CreateEntity(ctx, entity.New("", "requirement"), entity.CreateOptions{})
	if err != nil {
		t.Fatalf("create requirement: %v", err)
	}
	decRes, err := mgr.CreateEntity(ctx, entity.New("", "decision"), entity.CreateOptions{})
	if err != nil {
		t.Fatalf("create decision: %v", err)
	}
	if _, relErr := mgr.CreateRelation(ctx, decRes.Entity.ID, "addresses", reqRes.Entity.ID, entity.RelationOptions{}); relErr != nil {
		t.Fatalf("create relation: %v", relErr)
	}

	before := len(mem.Records())
	res, err := mgr.DeleteEntity(ctx, reqRes.Entity.ID, true)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(res.DeletedRelations) != 1 {
		t.Fatalf("DeletedRelations = %d, want 1", len(res.DeletedRelations))
	}

	var relRecords, entRecords int
	for _, r := range mem.Records()[before:] {
		switch r.Op {
		case audit.OpDeleteRelation:
			relRecords++
			if !strings.HasPrefix(r.TriggeredBy, "cascade:delete-entity:") {
				t.Errorf("relation record triggered_by = %q, want cascade:delete-entity: prefix", r.TriggeredBy)
			}
		case audit.OpDeleteEntity:
			entRecords++
		}
	}
	if relRecords != 1 {
		t.Errorf("delete-relation records = %d, want 1", relRecords)
	}
	if entRecords != 1 {
		t.Errorf("delete-entity records = %d, want 1", entRecords)
	}
}
