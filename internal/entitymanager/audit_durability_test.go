package entitymanager_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/autocascade"
	"github.com/Sourcehaven-BV/rela/internal/automation"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// failingUpdateStore wraps a store and forces every UpdateEntity and
// UpdateEntityIf call to
// return a sentinel error. The initial CreateEntity still lands, so this
// models a transient write failure on the post-automation re-write: the
// entity is durably on disk but the second persist fails.
type failingUpdateStore struct {
	store.Store
	err error
}

func (s *failingUpdateStore) UpdateEntity(_ context.Context, _ *entity.Entity) error {
	return s.err
}

func (s *failingUpdateStore) UpdateEntityIf(
	_ context.Context, _ *entity.Entity, _ store.UpdateCondition,
) (store.EntityVersion, error) {
	return "", s.err
}

func newManagerWithStoreAndAudit(
	t *testing.T, st store.Store, sink audit.Audit, automations []automation.Automation,
) *entitymanager.Manager {
	t.Helper()
	deps := entitymanager.Deps{
		Store:       st,
		Meta:        parseMeta(t),
		Templater:   nopTemplater{},
		Audit:       sink,
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	}
	if automations != nil {
		engine := automation.NewEngine(automations)
		runner, err := autocascade.New(autocascade.Deps{Engine: engine})
		if err != nil {
			t.Fatalf("autocascade.New: %v", err)
		}
		deps.Automations = engine
		deps.Cascade = runner
	}
	mgr, err := entitymanager.New(deps)
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	return mgr
}

// TestCreate_AuditsDurableWriteWhenPostAutomationUpsertFails pins that
// the audit record reflects the entity createCore durably persisted,
// even when the post-automation re-write fails. Previously the audit was
// recorded only after the cascade, so a failure in the second persist
// returned an error and left the on-disk entity unaudited.
func TestCreate_AuditsDurableWriteWhenPostAutomationUpsertFails(t *testing.T) {
	sentinel := errors.New("boom: transient write failure")
	st := &failingUpdateStore{Store: memstore.New(), err: sentinel}
	mem := audit.NewMemory()

	// An automation that sets a property forces the second persist
	// (Create→conflict→Update), which this store fails.
	auto := automation.Automation{
		Name: "set-status-on-create",
		On:   automation.Trigger{Entity: []string{"requirement"}, Created: true},
		Do:   []automation.Action{{Set: "status", Value: "proposed"}},
	}
	mgr := newManagerWithStoreAndAudit(t, st, mem, []automation.Automation{auto})

	e := entity.New("", "requirement")
	e.SetString("title", "durable but second-write fails")
	_, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
	if err == nil {
		t.Fatal("expected the post-automation write failure to surface as an error")
	}

	records := mem.Records()
	if len(records) != 1 {
		t.Fatalf("want exactly 1 audit record for the durable create, got %d", len(records))
	}
	if records[0].Op != audit.OpCreateEntity {
		t.Errorf("Op = %q, want %q", records[0].Op, audit.OpCreateEntity)
	}
	if records[0].Subject == nil || records[0].Subject.Type != "requirement" {
		t.Errorf("Subject = %+v, want an entity subject of type requirement", records[0].Subject)
	}
}

// TestUpdate_AuditsBeforeCascade pins that the update audit record is
// emitted once the entity is persisted, on the cascade-enabled path —
// the record must not be gated on cascade success.
func TestUpdate_AuditsBeforeCascade(t *testing.T) {
	mem := audit.NewMemory()
	auto := automation.Automation{
		Name: "noop-on-title-change",
		On:   automation.Trigger{Entity: []string{"requirement"}, Property: "title"},
		Do:   []automation.Action{{Set: "status", Value: "accepted"}},
	}
	mgr := newManagerWithAudit(t, mem, []automation.Automation{auto})

	e := entity.New("", "requirement")
	e.SetString("title", "to be updated")
	created, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	updated := created.Entity
	updated.SetString("title", "updated title")
	if _, err := mgr.UpdateEntity(context.Background(), updated); err != nil {
		t.Fatalf("UpdateEntity: %v", err)
	}

	var updates int
	for _, r := range mem.Records() {
		if r.Op == audit.OpUpdateEntity {
			updates++
		}
	}
	if updates != 1 {
		t.Fatalf("want exactly 1 update-entity audit record, got %d", updates)
	}
}
