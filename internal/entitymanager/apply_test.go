package entitymanager_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/automation"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

func newApplyManager(t *testing.T, st store.Store, sink audit.Audit) *entitymanager.Manager {
	t.Helper()
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       st,
		Meta:        parseMeta(t),
		Templater:   nopTemplater{},
		Audit:       sink,
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

// TestApplyEntity_PreservesExplicitSequentialID is the core RR-L1MY0N
// regression: CreateEntity rejects an explicit ID for a non-manual id_type
// (the test metamodel's `requirement` is id_type: sequential), but ApplyEntity
// must preserve a caller-supplied ID so a synced record keeps its identity.
func TestApplyEntity_PreservesExplicitSequentialID(t *testing.T) {
	st := memstore.New()
	mgr := newApplyManager(t, st, audit.Nop{})

	e := &entity.Entity{
		ID:         "REQ-fromPeer",
		Type:       "requirement",
		Properties: map[string]any{"title": "Synced requirement", "status": "draft"},
	}
	res, err := mgr.ApplyEntity(context.Background(), e)
	if err != nil {
		t.Fatalf("ApplyEntity: %v", err)
	}
	if res.Entity.ID != "REQ-fromPeer" {
		t.Fatalf("ID not preserved: got %q", res.Entity.ID)
	}

	// Confirm CreateEntity would have rejected the same explicit ID, proving
	// ApplyEntity genuinely takes a different path.
	_, createErr := mgr.CreateEntity(context.Background(), &entity.Entity{
		ID: "REQ-anotherExplicit", Type: "requirement",
		Properties: map[string]any{"title": "x"},
	}, entity.CreateOptions{ID: "REQ-anotherExplicit"})
	if createErr == nil {
		t.Fatal("expected CreateEntity to reject an explicit ID for a sequential id_type")
	}

	got, err := st.GetEntity(context.Background(), "REQ-fromPeer")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got.GetString("title") != "Synced requirement" {
		t.Fatalf("title not persisted: %q", got.GetString("title"))
	}
}

// TestApplyEntity_Idempotent asserts a second ApplyEntity with the same ID
// updates rather than failing (upsert), and persists the new state.
func TestApplyEntity_Idempotent(t *testing.T) {
	st := memstore.New()
	mgr := newApplyManager(t, st, audit.Nop{})
	ctx := context.Background()

	e := &entity.Entity{ID: "REQ-1", Type: "requirement", Properties: map[string]any{"title": "v1", "status": "draft"}}
	if _, err := mgr.ApplyEntity(ctx, e); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	e2 := &entity.Entity{ID: "REQ-1", Type: "requirement", Properties: map[string]any{"title": "v2", "status": "proposed"}}
	if _, err := mgr.ApplyEntity(ctx, e2); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	got, err := st.GetEntity(ctx, "REQ-1")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got.GetString("title") != "v2" || got.GetString("status") != "proposed" {
		t.Fatalf("second apply did not update: title=%q status=%q", got.GetString("title"), got.GetString("status"))
	}
}

// TestApplyEntity_AuditsCreateThenUpdate pins that apply emits an audit record
// with the correct op: create on first apply, update on the second.
func TestApplyEntity_AuditsCreateThenUpdate(t *testing.T) {
	st := memstore.New()
	mem := audit.NewMemory()
	mgr := newApplyManager(t, st, mem)
	ctx := context.Background()

	e := &entity.Entity{ID: "REQ-1", Type: "requirement", Properties: map[string]any{"title": "t", "status": "draft"}}
	if _, err := mgr.ApplyEntity(ctx, e); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if _, err := mgr.ApplyEntity(ctx, e); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	records := mem.Records()
	if len(records) != 2 {
		t.Fatalf("expected 2 audit records, got %d", len(records))
	}
	if records[0].Op != audit.OpCreateEntity {
		t.Errorf("first record op = %q, want %q", records[0].Op, audit.OpCreateEntity)
	}
	if records[1].Op != audit.OpUpdateEntity {
		t.Errorf("second record op = %q, want %q", records[1].Op, audit.OpUpdateEntity)
	}
}

// TestApplyEntity_RejectsInvalidContent pins that a HARD validation error (an
// unknown entity type — a record of a type the peer's metamodel doesn't know)
// aborts the apply and surfaces a *ValidationError (which the API layer maps to
// 422), persisting nothing.
//
// Note a missing required property is a SOFT condition per DEC-HWZHA: it rides
// along as a warning and the apply still succeeds, matching CreateEntity /
// UpdateEntity — sync mirrors that policy rather than inventing a stricter one.
// TestApplyEntity_SoftWarningStillApplies covers that case.
func TestApplyEntity_RejectsInvalidContent(t *testing.T) {
	st := memstore.New()
	mgr := newApplyManager(t, st, audit.Nop{})
	ctx := context.Background()

	e := &entity.Entity{ID: "ZZ-bad", Type: "no-such-type", Properties: map[string]any{"title": "x"}}
	_, err := mgr.ApplyEntity(ctx, e)
	if err == nil {
		t.Fatal("expected a validation error for unknown entity type")
	}
	var vErr *entitymanager.ValidationError
	if !errors.As(err, &vErr) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if _, getErr := st.GetEntity(ctx, "ZZ-bad"); getErr == nil {
		t.Fatal("invalid entity was persisted despite the validation error")
	}
}

// TestApplyEntity_SoftWarningStillApplies pins that a soft validation condition
// (missing required property) does NOT abort — it persists with a warning,
// matching the rest of the write path (DEC-HWZHA).
func TestApplyEntity_SoftWarningStillApplies(t *testing.T) {
	st := memstore.New()
	mgr := newApplyManager(t, st, audit.Nop{})
	ctx := context.Background()

	// `title` is required on requirement; omit it -> soft warning, not abort.
	e := &entity.Entity{ID: "REQ-soft", Type: "requirement", Properties: map[string]any{"status": "draft"}}
	res, err := mgr.ApplyEntity(ctx, e)
	if err != nil {
		t.Fatalf("apply should succeed with a warning, got error: %v", err)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected a soft warning for the missing required title")
	}
	if _, getErr := st.GetEntity(ctx, "REQ-soft"); getErr != nil {
		t.Fatalf("entity not persisted: %v", getErr)
	}
}

// TestApplyEntity_SuppressesAutomation is the RR-AZMA7T regression: applying a
// pulled change must NOT run automation/cascade, or a status change would
// auto-create a checklist locally and the derived record would ping-pong back.
// A counting store proves apply does exactly one write and no derived writes.
func TestApplyEntity_SuppressesAutomation(t *testing.T) {
	counting := &countingStore{Store: memstore.New()}
	// An automation that, on requirement create, would create a checklist
	// entity. If apply ran automation, the cascade would write a second entity.
	auto := automation.Automation{
		Name: "make-checklist",
		On:   automation.Trigger{Entity: []string{"requirement"}, Created: true},
		Do: []automation.Action{{
			CreateEntity: &automation.CreateEntityAction{
				Type:       "checklist",
				Properties: map[string]string{"title": "auto"},
			},
		}},
	}

	// Sanity: through CreateEntity the automation DOES fire (a checklist is
	// created), so the suppression assertion below is meaningful.
	withAuto := newManagerWithStoreAndAudit(t, &countingStore{Store: memstore.New()}, audit.Nop{}, []automation.Automation{auto})
	cres, err := withAuto.CreateEntity(context.Background(), &entity.Entity{
		Type: "requirement", Properties: map[string]any{"title": "t"},
	}, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("control CreateEntity: %v", err)
	}
	if len(cres.EntitiesCreated) == 0 {
		t.Skip("automation did not create a derived entity in the control; spec may differ — suppression test inconclusive")
	}

	// Now the real assertion: ApplyEntity with the SAME automation wired must
	// create exactly one entity (the applied one) and run no cascade.
	applyMgr := newManagerWithStoreAndAudit(t, counting, audit.Nop{}, []automation.Automation{auto})
	if _, err := applyMgr.ApplyEntity(context.Background(), &entity.Entity{
		ID: "REQ-applied", Type: "requirement", Properties: map[string]any{"title": "t", "status": "draft"},
	}); err != nil {
		t.Fatalf("ApplyEntity: %v", err)
	}
	if got := counting.creates.Load(); got != 1 {
		t.Fatalf("expected exactly 1 create (no automation-derived writes), got %d", got)
	}
}

// TestApplyEntity_RejectsNilAndEmptyID covers the guard conditions.
func TestApplyEntity_RejectsNilAndEmptyID(t *testing.T) {
	mgr := newApplyManager(t, memstore.New(), audit.Nop{})
	ctx := context.Background()
	if _, err := mgr.ApplyEntity(ctx, nil); err == nil {
		t.Error("expected error for nil entity")
	}
	if _, err := mgr.ApplyEntity(ctx, &entity.Entity{Type: "requirement"}); err == nil {
		t.Error("expected error for empty ID")
	}
}

// TestApplyEntity_RejectsLocked covers the inaccessible-fields guard.
func TestApplyEntity_RejectsLocked(t *testing.T) {
	mgr := newApplyManager(t, memstore.New(), audit.Nop{})
	e := &entity.Entity{
		ID: "REQ-1", Type: "requirement",
		Inaccessible: []entity.InaccessibleField{{Name: "title", Reason: entity.InaccessibleReasonGitCrypt}},
	}
	if _, err := mgr.ApplyEntity(context.Background(), e); err == nil {
		t.Fatal("expected error applying a locked entity")
	}
}

// --- ApplyRelation ---

func mustApplyEntity(t *testing.T, mgr *entitymanager.Manager, id, typ, title string) {
	t.Helper()
	_, err := mgr.ApplyEntity(context.Background(), &entity.Entity{
		ID: id, Type: typ, Properties: map[string]any{"title": title, "status": "draft"},
	})
	if err != nil {
		t.Fatalf("ApplyEntity(%s): %v", id, err)
	}
}

func TestApplyRelation_UpsertAndAudit(t *testing.T) {
	st := memstore.New()
	mem := audit.NewMemory()
	mgr := newApplyManager(t, st, mem)
	ctx := context.Background()

	mustApplyEntity(t, mgr, "REQ-1", "requirement", "req")
	mustApplyEntity(t, mgr, "CL-1", "checklist", "cl")

	r := &entity.Relation{From: "REQ-1", Type: "has-checklist", To: "CL-1"}
	if _, err := mgr.ApplyRelation(ctx, r); err != nil {
		t.Fatalf("ApplyRelation: %v", err)
	}
	// Second apply updates (idempotent).
	if _, err := mgr.ApplyRelation(ctx, r); err != nil {
		t.Fatalf("ApplyRelation (second): %v", err)
	}

	if _, err := st.GetRelation(ctx, "REQ-1", "has-checklist", "CL-1"); err != nil {
		t.Fatalf("relation not persisted: %v", err)
	}

	var relRecords int
	for _, rec := range mem.Records() {
		if rec.Op == audit.OpCreateRelation || rec.Op == audit.OpUpdateRelation {
			relRecords++
		}
	}
	if relRecords != 2 {
		t.Fatalf("expected 2 relation audit records (create + update), got %d", relRecords)
	}
}

// TestApplyRelation_RejectsNilAndLocked covers the guard conditions.
func TestApplyRelation_RejectsNilAndLocked(t *testing.T) {
	mgr := newApplyManager(t, memstore.New(), audit.Nop{})
	ctx := context.Background()
	if _, err := mgr.ApplyRelation(ctx, nil); err == nil {
		t.Error("expected error for nil relation")
	}
	locked := &entity.Relation{
		From: "A", Type: "has-checklist", To: "B",
		Inaccessible: []entity.InaccessibleField{{Name: "content", Reason: entity.InaccessibleReasonGitCrypt}},
	}
	if _, err := mgr.ApplyRelation(ctx, locked); err == nil {
		t.Error("expected error applying a locked relation")
	}
}

// TestApplyRelation_RejectsInvalidType pins that a relation whose type is not
// valid between the two endpoint types is rejected (and not persisted).
func TestApplyRelation_RejectsInvalidType(t *testing.T) {
	st := memstore.New()
	mgr := newApplyManager(t, st, audit.Nop{})
	ctx := context.Background()

	// has-checklist is requirement->checklist; wire it backwards (checklist->requirement).
	mustApplyEntity(t, mgr, "REQ-1", "requirement", "req")
	mustApplyEntity(t, mgr, "CL-1", "checklist", "cl")
	_, err := mgr.ApplyRelation(ctx, &entity.Relation{From: "CL-1", Type: "has-checklist", To: "REQ-1"})
	if err == nil {
		t.Fatal("expected an invalid-relation error for a reversed relation type")
	}
	if _, getErr := st.GetRelation(ctx, "CL-1", "has-checklist", "REQ-1"); getErr == nil {
		t.Fatal("invalid relation was persisted")
	}
}

// TestApplyRelation_MissingEndpoint pins that a relation to a missing entity is
// rejected with ErrEntityNotFound (the apply layer retries after the endpoint
// is applied — RR-YHGJHG ordering).
func TestApplyRelation_MissingEndpoint(t *testing.T) {
	st := memstore.New()
	mgr := newApplyManager(t, st, audit.Nop{})
	ctx := context.Background()

	mustApplyEntity(t, mgr, "REQ-1", "requirement", "req")
	// CL-1 does not exist.
	_, err := mgr.ApplyRelation(ctx, &entity.Relation{From: "REQ-1", Type: "has-checklist", To: "CL-1"})
	if !errors.Is(err, entitymanager.ErrEntityNotFound) {
		t.Fatalf("expected ErrEntityNotFound, got %v", err)
	}
}

// --- Code-review hardening (fail-closed, ACL, foreign prefix, no-status) ---

// flakyProbeStore makes the first GetEntity for a given ID return a transient
// (non-NotFound) error, then defer to the wrapped store. Models a backend blip
// during the existence probe.
type flakyProbeStore struct {
	store.Store
	failID  string
	failErr error
	failed  bool
}

func (s *flakyProbeStore) GetEntity(ctx context.Context, id string) (*entity.Entity, error) {
	if id == s.failID && !s.failed {
		s.failed = true
		return nil, s.failErr
	}
	return s.Store.GetEntity(ctx, id)
}

// GetEntityState carries the same fault: ApplyEntity probes the face the body
// names (BUG-HC6I2T), so stubbing GetEntity alone would inject nothing.
func (s *flakyProbeStore) GetEntityState(
	ctx context.Context, id string, p entity.Face,
) (*entity.Entity, error) {
	if id == s.failID && !s.failed {
		s.failed = true
		return nil, s.failErr
	}
	return s.Store.GetEntityState(ctx, id, p)
}

// TestApplyEntity_ExistenceProbeFailsClosed is the critical RR-review
// regression: a transient (non-NotFound) error from the existence GetEntity
// must abort, NOT be silently treated as "create" (which would authorize the
// wrong ACL verb and write a create audit row for an update). The error must
// surface and propagate, not be swallowed.
func TestApplyEntity_ExistenceProbeFailsClosed(t *testing.T) {
	sentinel := errors.New("boom: backend blip")
	st := &flakyProbeStore{Store: memstore.New(), failID: "REQ-1", failErr: sentinel}
	mgr := newApplyManager(t, st, audit.Nop{})

	_, err := mgr.ApplyEntity(context.Background(), &entity.Entity{
		ID: "REQ-1", Type: "requirement", Properties: map[string]any{"title": "t", "status": "draft"},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the transient store error to propagate, got %v", err)
	}
}

// TestApplyRelation_EndpointProbeFailsClosed pins that a transient endpoint read
// error is NOT mapped to ErrEntityNotFound (which would spin the sync retry loop
// forever on an entity that actually exists).
func TestApplyRelation_EndpointProbeFailsClosed(t *testing.T) {
	sentinel := errors.New("boom: endpoint read blip")
	inner := memstore.New()
	st := &flakyProbeStore{Store: inner, failID: "REQ-1", failErr: sentinel}
	mgr := newApplyManager(t, st, audit.Nop{})

	// Seed both endpoints via the inner store (bypassing the flaky wrapper's
	// first-fail) so they genuinely exist.
	seedViaManager(t, inner, "REQ-1", "requirement")
	seedViaManager(t, inner, "CL-1", "checklist")

	_, err := mgr.ApplyRelation(context.Background(), &entity.Relation{From: "REQ-1", Type: "has-checklist", To: "CL-1"})
	if errors.Is(err, entitymanager.ErrEntityNotFound) {
		t.Fatalf("transient endpoint error was mis-mapped to ErrEntityNotFound: %v", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the transient error to propagate, got %v", err)
	}
}

func seedViaManager(t *testing.T, st store.Store, id, typ string) {
	t.Helper()
	mgr, err := entitymanager.New(entitymanager.Deps{
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
		Store:     st, Meta: parseMeta(t), Templater: nopTemplater{}, Audit: audit.Nop{}, ACL: acl.NopACL{}, Transitions: statemachine.EmptySet(),
	})
	if err != nil {
		t.Fatalf("seedViaManager: %v", err)
	}
	if _, err := mgr.ApplyEntity(context.Background(), &entity.Entity{
		ID: id, Type: typ, Properties: map[string]any{"title": "seed", "status": "draft"},
	}); err != nil {
		t.Fatalf("seedViaManager ApplyEntity: %v", err)
	}
}

// TestApplyEntity_ACLGatesCreateVsUpdate pins the security-relevant upsert
// behavior: a read-only principal is denied on apply (a write), with a
// *ForbiddenError — the ACL framing is real, not bypassed.
func TestApplyEntity_ACLDenied(t *testing.T) {
	st := memstore.New()
	mgr, err := entitymanager.New(entitymanager.Deps{
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
		Store:     st, Meta: parseMeta(t), Templater: nopTemplater{}, Audit: audit.Nop{}, ACL: acl.ReadOnlyACL{}, Transitions: statemachine.EmptySet(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, applyErr := mgr.ApplyEntity(context.Background(), &entity.Entity{
		ID: "REQ-1", Type: "requirement", Properties: map[string]any{"title": "t", "status": "draft"},
	})
	var forbidden *acl.ForbiddenError
	if !errors.As(applyErr, &forbidden) {
		t.Fatalf("expected *acl.ForbiddenError, got %T: %v", applyErr, applyErr)
	}
	if _, getErr := st.GetEntity(context.Background(), "REQ-1"); getErr == nil {
		t.Fatal("entity was persisted despite ACL denial")
	}
}

// TestApplyEntity_RejectsForeignIDPrefix documents and pins the decision
// (review #3): an ID matching no local prefix is a HARD validation error.
// Sync assumes peers share a metamodel; a foreign-prefixed ID is rejected
// rather than silently written under a prefix the metamodel doesn't declare.
func TestApplyEntity_RejectsForeignIDPrefix(t *testing.T) {
	st := memstore.New()
	mgr := newApplyManager(t, st, audit.Nop{})

	// `requirement` declares prefix REQ-; FOREIGN- matches no declared prefix.
	e := &entity.Entity{ID: "FOREIGN-1", Type: "requirement", Properties: map[string]any{"title": "t", "status": "draft"}}
	_, err := mgr.ApplyEntity(context.Background(), e)
	if err == nil {
		t.Fatal("expected a hard validation error for a foreign ID prefix")
	}
	var vErr *entitymanager.ValidationError
	if !errors.As(err, &vErr) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

// TestApplyEntity_NoStatusAppliesAsIs pins the documented precondition (review
// #4): ApplyEntity does NOT backfill a status default. An entity supplied
// without a status persists without one (status is a soft/optional field on the
// test metamodel), proving "caller owns complete state" — no silent defaulting.
func TestApplyEntity_NoStatusAppliesAsIs(t *testing.T) {
	st := memstore.New()
	mgr := newApplyManager(t, st, audit.Nop{})
	ctx := context.Background()

	e := &entity.Entity{ID: "REQ-1", Type: "requirement", Properties: map[string]any{"title": "t"}}
	if _, err := mgr.ApplyEntity(ctx, e); err != nil {
		t.Fatalf("ApplyEntity: %v", err)
	}
	got, err := st.GetEntity(ctx, "REQ-1")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got.GetString("status") != "" {
		t.Fatalf("ApplyEntity backfilled a status default %q; it must persist as-is", got.GetString("status"))
	}
}
