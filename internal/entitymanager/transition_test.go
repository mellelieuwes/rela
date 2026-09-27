package entitymanager_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// transitionMetaYAML defines a snapshot type whose status is a state machine.
const transitionMetaYAML = `version: "1.0"
entities:
  snapshot:
    label: Snapshot
    plural: snapshots
    id_prefix: "SNAP-"
    id_type: sequential
    properties:
      title:
        type: string
      status:
        type: snapshot-status
types:
  snapshot-status:
    values: [in-review, approved, established, obsolete]
    initial: in-review
    transitions:
      - from: in-review
        to: approved
        guard: approve
      - from: approved
        to: established
        guard: establish
`

// allowAllGuard grants every permission; denyAllGuard grants none.
type allowAllGuard struct{}

func (allowAllGuard) HoldsPermission(context.Context, string, string) bool { return true }

type denyAllGuard struct{}

func (denyAllGuard) HoldsPermission(context.Context, string, string) bool { return false }

func newTransitionManager(t *testing.T, guard statemachine.Guard) *entitymanager.Manager {
	t.Helper()
	meta, err := metamodel.Parse([]byte(transitionMetaYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	machines, err := statemachine.Compile(meta)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:           memstore.New(),
		Meta:            meta,
		Templater:       nopTemplater{},
		Audit:           audit.Nop{},
		ACL:             acl.NopACL{},
		Transitions:     machines,
		FieldGate:       entitymanager.AllowAllFieldGate{},
		Twins:           entitymanager.NoTwinOwnership{},
		TransitionGuard: guard,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return mgr
}

func seedSnapshot(t *testing.T, mgr *entitymanager.Manager, status string) *entity.Entity {
	t.Helper()
	e := entity.New("", "snapshot")
	e.SetString("title", "Q3 register")
	if status != "" {
		e.SetString("status", status)
	}
	res, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("seed create(status=%q): %v", status, err)
	}
	return res.Entity
}

func TestTransition_LegalMovePasses(t *testing.T) {
	mgr := newTransitionManager(t, allowAllGuard{})
	snap := seedSnapshot(t, mgr, "") // enters at initial in-review

	snap.SetString("status", "approved")
	if _, err := mgr.UpdateEntity(context.Background(), snap); err != nil {
		t.Fatalf("legal in-review→approved rejected: %v", err)
	}
}

func TestTransition_IllegalMoveIs422(t *testing.T) {
	mgr := newTransitionManager(t, allowAllGuard{})
	snap := seedSnapshot(t, mgr, "")

	snap.SetString("status", "established") // skips approved
	_, err := mgr.UpdateEntity(context.Background(), snap)
	if !errors.Is(err, statemachine.ErrIllegalTransition) {
		t.Fatalf("expected ErrIllegalTransition, got %v", err)
	}
	// An illegal transition is NOT an ACL denial — it must not surface as 403.
	var fe *acl.ForbiddenError
	if errors.As(err, &fe) {
		t.Fatal("illegal transition must not map to a ForbiddenError (403)")
	}
}

func TestTransition_GuardDeniedIs403(t *testing.T) {
	mgr := newTransitionManager(t, denyAllGuard{})
	snap := seedSnapshot(t, mgr, "")

	snap.SetString("status", "approved") // legal edge, but guard "approve" denied
	_, err := mgr.UpdateEntity(context.Background(), snap)
	var fe *acl.ForbiddenError
	if !errors.As(err, &fe) {
		t.Fatalf("expected *acl.ForbiddenError (403), got %v", err)
	}
	if fe.Decision.RuleKind != "transition-guard" {
		t.Errorf("RuleKind = %q, want transition-guard", fe.Decision.RuleKind)
	}
}

func TestTransition_IllegalEntryOnCreateIs422(t *testing.T) {
	mgr := newTransitionManager(t, allowAllGuard{})
	e := entity.New("", "snapshot")
	e.SetString("title", "bad entry")
	e.SetString("status", "established") // not the initial value
	_, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
	if !errors.Is(err, statemachine.ErrIllegalEntry) {
		t.Fatalf("expected ErrIllegalEntry, got %v", err)
	}
}

func TestTransition_LegalEntryOnCreatePasses(t *testing.T) {
	mgr := newTransitionManager(t, allowAllGuard{})
	// Explicit initial value is fine.
	seedSnapshot(t, mgr, "in-review")
	// Absent is fine (default applies).
	seedSnapshot(t, mgr, "")
}

// RR-NB135: the sync/upsert path (ApplyEntity) must enforce transitions too —
// it is a served write path and must not be a bypass.
func TestTransition_ApplyEntity_EnforcesLegality(t *testing.T) {
	mgr := newTransitionManager(t, allowAllGuard{})
	snap := seedSnapshot(t, mgr, "") // in-review

	// A sync-apply that skips approved (in-review→established) must be rejected.
	snap.SetString("status", "established")
	_, err := mgr.ApplyEntity(context.Background(), snap)
	if !errors.Is(err, statemachine.ErrIllegalTransition) {
		t.Fatalf("ApplyEntity must enforce legality; want ErrIllegalTransition, got %v", err)
	}
}

func TestTransition_ApplyEntity_EnforcesGuard(t *testing.T) {
	mgr := newTransitionManager(t, denyAllGuard{})
	snap := seedSnapshot(t, mgr, "") // in-review

	snap.SetString("status", "approved") // legal edge, guard denied
	_, err := mgr.ApplyEntity(context.Background(), snap)
	var fe *acl.ForbiddenError
	if !errors.As(err, &fe) {
		t.Fatalf("ApplyEntity guard denial must be *acl.ForbiddenError (403), got %v", err)
	}
	if fe.Decision.RuleID != "approve" {
		t.Errorf("RuleID = %q, want the permission name 'approve'", fe.Decision.RuleID)
	}
}

// RR-HETEE: a rejected illegal-entry create must NOT persist a row.
func TestTransition_IllegalEntry_DoesNotPersist(t *testing.T) {
	meta, err := metamodel.Parse([]byte(transitionMetaYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	machines, err := statemachine.Compile(meta)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	st := memstore.New()
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:           st,
		Meta:            meta,
		Templater:       nopTemplater{},
		Audit:           audit.Nop{},
		ACL:             acl.NopACL{},
		Transitions:     machines,
		FieldGate:       entitymanager.AllowAllFieldGate{},
		Twins:           entitymanager.NoTwinOwnership{},
		TransitionGuard: allowAllGuard{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e := entity.New("", "snapshot")
	e.SetString("title", "bad")
	e.SetString("status", "established") // illegal entry
	if _, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{}); !errors.Is(err, statemachine.ErrIllegalEntry) {
		t.Fatalf("want ErrIllegalEntry, got %v", err)
	}
	// No snapshot row must exist — the check runs before the store write, so a
	// rejected illegal entry never persists (and thus never emits a store event).
	count := 0
	for range st.ListEntities(context.Background(), store.EntityQuery{Type: "snapshot"}) {
		count++
	}
	if count != 0 {
		t.Fatalf("illegal-entry create persisted %d row(s) (RR-HETEE regression)", count)
	}
}
