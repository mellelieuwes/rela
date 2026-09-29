package entitymanager_test

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/autocascade"
	"github.com/Sourcehaven-BV/rela/internal/automation"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
	"github.com/Sourcehaven-BV/rela/internal/templating"
)

// testMetamodelYAML is the minimal metamodel for Manager pipeline tests.
const testMetamodelYAML = `version: "1.0"
entities:
  requirement:
    label: Requirement
    plural: requirements
    id_prefix: "REQ-"
    id_type: sequential
    properties:
      title:
        type: string
        required: true
      status:
        type: status
  decision:
    label: Decision
    plural: decisions
    id_prefix: "DEC-"
    id_type: sequential
    properties:
      title:
        type: string
        required: true
      status:
        type: status
  checklist:
    label: Checklist
    plural: checklists
    id_prefix: "CL-"
    id_type: sequential
    properties:
      title:
        type: string
      status:
        type: status
relations:
  addresses:
    label: Addresses
    from: [decision]
    to: [requirement]
  has-checklist:
    label: HasChecklist
    from: [requirement]
    to: [checklist]
  informed-by:
    label: InformedBy
    from: [requirement]
    to: [decision]
  supersedes:
    label: Supersedes
    from: [requirement]
    to: [decision]
  covers:
    label: Covers
    from: [checklist]
    to: [decision]
types:
  status:
    values: [draft, proposed, accepted]
`

// --- Stubs ---

// nopTemplater satisfies the narrow [entitymanager.TemplateLoader]
// consumer-side interface — two methods, not the full templating
// package surface. Returns nil for every lookup.
type nopTemplater struct{}

func (nopTemplater) EntityTemplate(_ context.Context, _, _ string) (*templating.Template, error) {
	return nil, nil //nolint:nilnil // miss is not an error at this layer
}
func (nopTemplater) RelationTemplate(_ context.Context, _ string) (*templating.Template, error) {
	return nil, nil //nolint:nilnil // miss is not an error at this layer
}

// countingStore wraps a [store.Store] and counts Create/Update/Delete
// calls so tests can pin pipeline-shape invariants.
type countingStore struct {
	store.Store
	creates atomic.Int32
	updates atomic.Int32
	deletes atomic.Int32
	// scans counts ListEntities calls; used to pin that the dry-run
	// validate path doesn't scan the store (RR-8I07).
	scans atomic.Int32
}

func (s *countingStore) CreateEntity(ctx context.Context, e *entity.Entity) error {
	s.creates.Add(1)
	return s.Store.CreateEntity(ctx, e)
}
func (s *countingStore) UpdateEntity(ctx context.Context, e *entity.Entity) error {
	s.updates.Add(1)
	return s.Store.UpdateEntity(ctx, e)
}
func (s *countingStore) UpdateEntityIf(
	ctx context.Context, e *entity.Entity, cond store.UpdateCondition,
) (store.EntityVersion, error) {
	s.updates.Add(1)
	return s.Store.UpdateEntityIf(ctx, e, cond)
}
func (s *countingStore) DeleteEntity(ctx context.Context, id string, cascade bool) (*store.DeleteResult, error) {
	s.deletes.Add(1)
	return s.Store.DeleteEntity(ctx, id, cascade)
}
func (s *countingStore) ListEntities(ctx context.Context, q store.EntityQuery) iter.Seq2[*entity.Entity, error] {
	s.scans.Add(1)
	return s.Store.ListEntities(ctx, q)
}

// failingCreateStore wraps a store and forces the next N CreateEntity
// calls to return a sentinel non-conflict error. Used to verify that
// createCore propagates a non-conflict store error instead of masking
// it as a fall-through to UpdateEntity.
type failingCreateStore struct {
	store.Store
	err         error
	remaining   atomic.Int32
	updateCalls atomic.Int32
}

func (s *failingCreateStore) CreateEntity(ctx context.Context, e *entity.Entity) error {
	if s.remaining.Load() > 0 {
		s.remaining.Add(-1)
		return s.err
	}
	return s.Store.CreateEntity(ctx, e)
}
func (s *failingCreateStore) UpdateEntity(ctx context.Context, e *entity.Entity) error {
	s.updateCalls.Add(1)
	return s.Store.UpdateEntity(ctx, e)
}

// --- Fixture helpers ---

func parseMeta(t *testing.T) *metamodel.Metamodel {
	t.Helper()
	m, err := metamodel.Parse([]byte(testMetamodelYAML))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	return m
}

// newManager builds a Manager with the supplied automations. Passing
// nil disables the automation engine.
func newManager(t *testing.T, automations []automation.Automation) (*entitymanager.Manager, *countingStore) {
	t.Helper()
	cs := &countingStore{Store: memstore.New()}
	meta := parseMeta(t)
	machines, err := statemachine.Compile(meta)
	if err != nil {
		t.Fatalf("statemachine.Compile: %v", err)
	}
	deps := entitymanager.Deps{
		Store:       cs,
		Meta:        meta,
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Transitions: machines,
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	}
	if automations != nil {
		engine := automation.NewEngine(automations)
		runner, rerr := autocascade.New(autocascade.Deps{Engine: engine})
		if rerr != nil {
			t.Fatalf("autocascade.New: %v", rerr)
		}
		deps.Automations = engine
		deps.Cascade = runner
	}
	mgr, err := entitymanager.New(deps)
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	return mgr, cs
}

// createReq is a convenience: create a requirement with the given
// title, fail-fast on error.
func createReq(t *testing.T, mgr *entitymanager.Manager, title string) *entity.Entity {
	t.Helper()
	e := entity.New("", "requirement")
	e.SetString("title", title)
	res, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("createReq(%q): %v", title, err)
	}
	return res.Entity
}

func createDec(t *testing.T, mgr *entitymanager.Manager, title string) *entity.Entity {
	t.Helper()
	e := entity.New("", "decision")
	e.SetString("title", title)
	res, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("createDec(%q): %v", title, err)
	}
	return res.Entity
}

// --- Constructor validation ---

func TestNew_RejectsNilStore(t *testing.T) {
	t.Parallel()
	_, err := entitymanager.New(entitymanager.Deps{
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
		Meta:      parseMeta(t),
		Templater: nopTemplater{},
	})
	if err == nil || !strings.Contains(err.Error(), "Store") {
		t.Fatalf("expected Store-required error, got %v", err)
	}
}

func TestNew_RejectsNilMeta(t *testing.T) {
	t.Parallel()
	_, err := entitymanager.New(entitymanager.Deps{
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
		Store:     memstore.New(),
		Templater: nopTemplater{},
	})
	if err == nil || !strings.Contains(err.Error(), "Meta") {
		t.Fatalf("expected Meta-required error, got %v", err)
	}
}

func TestNew_RejectsNilTemplater(t *testing.T) {
	t.Parallel()
	_, err := entitymanager.New(entitymanager.Deps{
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
		Store:     memstore.New(),
		Meta:      parseMeta(t),
	})
	if err == nil || !strings.Contains(err.Error(), "Templater") {
		t.Fatalf("expected Templater-required error, got %v", err)
	}
}

func TestNew_RejectsNilAudit(t *testing.T) {
	t.Parallel()
	_, err := entitymanager.New(entitymanager.Deps{
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
		Store:     memstore.New(),
		Meta:      parseMeta(t),
		Templater: nopTemplater{},
	})
	if err == nil || !strings.Contains(err.Error(), "Audit") {
		t.Fatalf("expected Audit-required error, got %v", err)
	}
}

func TestNew_RejectsNilACL(t *testing.T) {
	t.Parallel()
	_, err := entitymanager.New(entitymanager.Deps{
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
		Store:     memstore.New(),
		Meta:      parseMeta(t),
		Templater: nopTemplater{},
		Audit:     audit.Nop{},
	})
	if err == nil || !strings.Contains(err.Error(), "ACL") {
		t.Fatalf("expected ACL-required error, got %v", err)
	}
}

func TestNew_RejectsNilTransitions(t *testing.T) {
	t.Parallel()
	_, err := entitymanager.New(entitymanager.Deps{
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
		Store:     memstore.New(),
		Meta:      parseMeta(t),
		Templater: nopTemplater{},
		Audit:     audit.Nop{},
		ACL:       acl.NopACL{},
	})
	if err == nil || !strings.Contains(err.Error(), "Transitions") {
		t.Fatalf("expected Transitions-required error, got %v", err)
	}
}

func TestNew_RejectsAutomationsWithoutCascade(t *testing.T) {
	t.Parallel()
	engine := automation.NewEngine(nil)
	_, err := entitymanager.New(entitymanager.Deps{
		Store:       memstore.New(),
		Meta:        parseMeta(t),
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
		Automations: engine,
	})
	if err == nil || !strings.Contains(err.Error(), "Automations and Cascade") {
		t.Fatalf("expected Automations/Cascade pairing error, got %v", err)
	}
}

func TestNew_AllowsNoAutomation(t *testing.T) {
	t.Parallel()
	if _, err := entitymanager.New(entitymanager.Deps{
		Store:       memstore.New(),
		Meta:        parseMeta(t),
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- AC4: write-count invariants ---

func TestCreate_WritesOnceWithoutAutomation(t *testing.T) {
	t.Parallel()
	mgr, cs := newManager(t, nil)
	createReq(t, mgr, "First")
	if got := cs.creates.Load(); got != 1 {
		t.Errorf("CreateEntity calls = %d, want 1", got)
	}
	if got := cs.updates.Load(); got != 0 {
		t.Errorf("UpdateEntity calls = %d, want 0", got)
	}
}

// TestValidateCreate_PersistsNothing pins the core dry-run contract:
// ValidateCreate runs the create-path validation but performs no store
// write (TKT-3I5U).
func TestValidateCreate_PersistsNothing(t *testing.T) {
	t.Parallel()
	mgr, cs := newManager(t, nil)
	e := entity.New("", "requirement")
	e.SetString("title", "Candidate")

	got, warnings, err := mgr.ValidateCreate(context.Background(), e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("ValidateCreate: %v", err)
	}
	if got == nil {
		t.Fatal("ValidateCreate returned nil candidate entity")
	}
	if len(warnings) != 0 {
		t.Errorf("clean candidate should have no warnings; got %+v", warnings)
	}
	if c := cs.creates.Load(); c != 0 {
		t.Errorf("ValidateCreate must not write: CreateEntity calls = %d, want 0", c)
	}
	if u := cs.updates.Load(); u != 0 {
		t.Errorf("ValidateCreate must not write: UpdateEntity calls = %d, want 0", u)
	}
}

// TestValidateCreate_SoftWarningForRequiredUnset proves a required-but-
// unset field returns as a soft warning (not a hard error), matching
// what CreateEntity would surface on the persisted result.
func TestValidateCreate_SoftWarningForRequiredUnset(t *testing.T) {
	t.Parallel()
	mgr, _ := newManager(t, nil)
	e := entity.New("", "requirement") // title is required, omitted

	_, warnings, err := mgr.ValidateCreate(context.Background(), e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("ValidateCreate should not hard-error on required-unset: %v", err)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w.Path, "title") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a required-unset warning for title; got %+v", warnings)
	}
}

// TestValidateCreate_MatchesCreateWarnings proves dry-run and real
// create produce the SAME soft warnings for the same candidate — the
// shared buildCandidateEntity guarantees no drift (RR-Y85M).
func TestValidateCreate_MatchesCreateWarnings(t *testing.T) {
	t.Parallel()
	dryMgr, _ := newManager(t, nil)
	realMgr, _ := newManager(t, nil)

	mk := func() *entity.Entity {
		e := entity.New("", "requirement") // omit required title
		return e
	}

	_, dryWarnings, err := dryMgr.ValidateCreate(context.Background(), mk(), entity.CreateOptions{})
	if err != nil {
		t.Fatalf("ValidateCreate: %v", err)
	}
	res, err := realMgr.CreateEntity(context.Background(), mk(), entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	if len(dryWarnings) != len(res.Warnings) {
		t.Fatalf("warning count differs: dry=%d real=%d", len(dryWarnings), len(res.Warnings))
	}
	for i := range dryWarnings {
		if dryWarnings[i] != res.Warnings[i] {
			t.Errorf("warning %d differs: dry=%+v real=%+v", i, dryWarnings[i], res.Warnings[i])
		}
	}
}

// TestValidateCreate_NilEntity rejects a nil candidate.
func TestValidateCreate_NilEntity(t *testing.T) {
	t.Parallel()
	mgr, _ := newManager(t, nil)
	if _, _, err := mgr.ValidateCreate(context.Background(), nil, entity.CreateOptions{}); err == nil {
		t.Error("ValidateCreate(nil) should error")
	}
}

// TestValidateCreate_SkipsIDGeneration pins RR-8I07: ValidateCreate must
// NOT scan the entity store to generate a real ID, because the dry-run
// runs per debounced keystroke on the create form. A store-wide scan
// per keystroke would hitch the UI and pile on backend load.
func TestValidateCreate_SkipsIDGeneration(t *testing.T) {
	t.Parallel()
	mgr, cs := newManager(t, nil)
	// Seed two entities so an ID-gen path WOULD see them via ListEntities.
	createReq(t, mgr, "Seed 1")
	createReq(t, mgr, "Seed 2")
	scansBefore := cs.scans.Load()

	e := entity.New("", "requirement")
	e.SetString("title", "Candidate")
	if _, _, err := mgr.ValidateCreate(context.Background(), e, entity.CreateOptions{}); err != nil {
		t.Fatalf("ValidateCreate: %v", err)
	}
	if scans := cs.scans.Load() - scansBefore; scans != 0 {
		t.Errorf("ValidateCreate must not scan the store; got %d ListEntities scans", scans)
	}
}

// TestCreate_WritesTwiceWithAutomationProperty pins the
// "two writes when automation sets a property" pipeline shape.
func TestCreate_WritesTwiceWithAutomationProperty(t *testing.T) {
	t.Parallel()
	const wantStatus = "proposed"
	auto := automation.Automation{
		Name: "set-status-on-create",
		On:   automation.Trigger{Entity: []string{"requirement"}, Created: true},
		Do:   []automation.Action{{Set: "status", Value: wantStatus}},
	}
	mgr, cs := newManager(t, []automation.Automation{auto})

	e := entity.New("", "requirement")
	e.SetString("title", "Automated")
	result, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	// The initial create is a direct CreateEntity (a create must never
	// become an update). The post-automation re-write is now a direct
	// UpdateEntity (the row already exists from the initial create — no
	// upsert fallback anywhere, BUG-ZWTDH9). So creates=1, updates=1.
	if got := cs.creates.Load(); got != 1 {
		t.Errorf("CreateEntity calls = %d, want 1 (initial create only)", got)
	}
	if got := cs.updates.Load(); got != 1 {
		t.Errorf("UpdateEntity calls = %d, want 1 (post-automation re-write)", got)
	}
	if got := result.Entity.GetString("status"); got != wantStatus {
		t.Errorf("status = %q, want %q", got, wantStatus)
	}
}

func TestCreate_SkipAutomation(t *testing.T) {
	t.Parallel()
	auto := automation.Automation{
		Name: "set-status",
		On:   automation.Trigger{Entity: []string{"requirement"}, Created: true},
		Do:   []automation.Action{{Set: "status", Value: "proposed"}},
	}
	mgr, cs := newManager(t, []automation.Automation{auto})

	e := entity.New("", "requirement")
	e.SetString("title", "No Automation")
	_, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{SkipAutomation: true})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if got := cs.updates.Load(); got != 0 {
		t.Errorf("UpdateEntity calls = %d, want 0 (SkipAutomation set)", got)
	}
}

// --- Cascade dispatch from Manager ---

// TestCreate_AutomationCreatesRelatedEntity exercises the cascade
// path: an automation that creates a downstream entity. Verifies the
// outcome lands on entity.CreateResult and that the cascade-driven create
// does NOT re-trigger automation (the no-recursion invariant).
func TestCreate_AutomationCreatesRelatedEntity(t *testing.T) {
	t.Parallel()
	// Single automation: when a requirement is created, create a
	// checklist linked back to it.
	auto := automation.Automation{
		Name: "create-checklist-on-requirement",
		On:   automation.Trigger{Entity: []string{"requirement"}, Created: true},
		Do: []automation.Action{
			{
				CreateEntity: &automation.CreateEntityAction{
					Type:     "checklist",
					Relation: "has-checklist",
				},
			},
		},
	}
	mgr, _ := newManager(t, []automation.Automation{auto})

	e := entity.New("", "requirement")
	e.SetString("title", "Trigger Cascade")
	result, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if len(result.EntitiesCreated) != 1 || result.EntitiesCreated[0].Type != "checklist" {
		t.Errorf("EntitiesCreated = %v, want exactly one checklist", result.EntitiesCreated)
	}
	if len(result.RelationsCreated) != 1 {
		t.Errorf("RelationsCreated len = %d, want 1", len(result.RelationsCreated))
	}
}

// TestCreate_CascadeNoRecursion pins the critical invariant that
// cascade-driven entity creation does NOT itself fire automations.
// We register an automation on "checklist" → "set status" so that, if
// cascadeHost.CreateEntity were to (wrongly) run the automation
// engine, the resulting checklist would have status="accepted". If
// the invariant holds, the cascade-created checklist carries the
// engine's default ("draft") because no automation fired on it.
func TestCreate_CascadeNoRecursion(t *testing.T) {
	t.Parallel()
	const onRequirementMarker = "proposed"
	parentAuto := automation.Automation{
		Name: "create-checklist",
		On:   automation.Trigger{Entity: []string{"requirement"}, Created: true},
		Do: []automation.Action{
			{
				CreateEntity: &automation.CreateEntityAction{
					Type:     "checklist",
					Relation: "has-checklist",
				},
			},
		},
	}
	// If this automation EVER fires, the test fails — proves cascade
	// did not invoke the engine for cascade-created entities.
	childAuto := automation.Automation{
		Name: "should-never-fire-on-cascade-create",
		On:   automation.Trigger{Entity: []string{"checklist"}, Created: true},
		Do:   []automation.Action{{Set: "status", Value: onRequirementMarker}},
	}
	mgr, cs := newManager(t, []automation.Automation{parentAuto, childAuto})

	// NOTE: childAuto WILL fire here, because the cascade's own
	// Runner.Process re-evaluates the engine for cascade-created
	// entities at the runner level — that's intentional (the
	// recursion limit is MaxDepth). We're testing the *Manager-level*
	// no-recursion: the cascade's createEntity does NOT loop back
	// through Manager.CreateEntity, which would re-dispatch the
	// automation engine and double-fire childAuto.
	e := entity.New("", "requirement")
	e.SetString("title", "Parent")
	result, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if len(result.EntitiesCreated) != 1 {
		t.Fatalf("EntitiesCreated len = %d, want 1", len(result.EntitiesCreated))
	}

	// Pin the invariant through store-call counts. Single-dispatch
	// shape: requirement create (1) + cascade checklist create (1) +
	// childAuto's Set persisted via cascade WriteEntity, now a direct
	// UpdateEntity (no upsert probe — BUG-ZWTDH9) = 2 creates, 1 update.
	// If the cascade re-entered Manager.CreateEntity, childAuto would be
	// dispatched a second time, adding another create + update pair
	// (3 creates / 2 updates).
	if got := cs.creates.Load(); got != 2 {
		t.Errorf("store CreateEntity calls = %d, want 2 (recursion would add a 3rd)", got)
	}
	if got := cs.updates.Load(); got != 1 {
		t.Errorf("store UpdateEntity calls = %d, want 1 (recursion would double-fire childAuto)", got)
	}
}

// recordingScripts captures the mutator passed to Run so tests can
// assert Manager populates Request.Mutator with itself.
type recordingScripts struct {
	mutator autocascade.Mutator
	calls   int
}

func (r *recordingScripts) Run(_ context.Context, _ autocascade.ScriptAction, m autocascade.Mutator) error {
	r.calls++
	r.mutator = m
	return nil
}

// TestCreate_PassesManagerAsMutator pins that Manager.runWriteCascade
// supplies itself as Request.Mutator when dispatching, so scripted
// actions can call back through the same write path. Without this,
// future refactors could silently drop the assignment and only
// fail when an actual script tried to mutate.
func TestCreate_PassesManagerAsMutator(t *testing.T) {
	t.Parallel()
	scripts := &recordingScripts{}
	cs := &countingStore{Store: memstore.New()}
	auto := automation.Automation{
		Name: "fires-script",
		On:   automation.Trigger{Entity: []string{"requirement"}, Created: true},
		Do:   []automation.Action{{Lua: "-- noop"}},
	}
	engine := automation.NewEngine([]automation.Automation{auto})
	runner, err := autocascade.New(autocascade.Deps{Engine: engine})
	if err != nil {
		t.Fatalf("autocascade.New: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:        cs,
		Meta:         parseMeta(t),
		Templater:    nopTemplater{},
		Audit:        audit.Nop{},
		ACL:          acl.NopACL{},
		Transitions:  statemachine.EmptySet(),
		FieldGate:    entitymanager.AllowAllFieldGate{},
		Twins:        entitymanager.NoTwinOwnership{},
		Automations:  engine,
		Cascade:      runner,
		ScriptRunner: scripts,
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}

	e := entity.New("", "requirement")
	e.SetString("title", "Trigger")
	if _, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if scripts.calls != 1 {
		t.Fatalf("Run calls = %d, want 1", scripts.calls)
	}
	if scripts.mutator == nil {
		t.Fatal("scripts.mutator = nil; Manager did not populate Request.Mutator")
	}
	// The mutator IS the Manager. Same-face-comparison verifies
	// Manager passes itself rather than constructing an adapter.
	if scripts.mutator != autocascade.Mutator(mgr) {
		t.Errorf("mutator = %p, want manager %p", scripts.mutator, mgr)
	}
}

// --- Update path: oldEntity gate, typed errors ---

func TestUpdate_NotFoundReturnsTypedError(t *testing.T) {
	t.Parallel()
	auto := automation.Automation{
		Name: "should-not-fire",
		On:   automation.Trigger{Entity: []string{"requirement"}, Property: "title"},
		Do:   []automation.Action{{Set: "status", Value: "accepted"}},
	}
	mgr, cs := newManager(t, []automation.Automation{auto})

	e := entity.New("REQ-999", "requirement")
	e.SetString("title", "Nonexistent")
	_, err := mgr.UpdateEntity(context.Background(), e)
	if !errors.Is(err, entitymanager.ErrEntityNotFound) {
		t.Fatalf("expected ErrEntityNotFound, got %v", err)
	}
	if got := cs.creates.Load() + cs.updates.Load(); got != 0 {
		t.Errorf("write calls = %d, want 0", got)
	}
}

// --- Delete path: typed errors, cascade behavior ---

func TestDelete_NotFoundReturnsTypedError(t *testing.T) {
	t.Parallel()
	mgr, _ := newManager(t, nil)
	_, err := mgr.DeleteEntity(context.Background(), "REQ-999", false)
	if !errors.Is(err, entitymanager.ErrEntityNotFound) {
		t.Fatalf("expected ErrEntityNotFound, got %v", err)
	}
}

func TestDelete_HasRelationsRejectsWhenNotCascading(t *testing.T) {
	t.Parallel()
	mgr, _ := newManager(t, nil)
	ctx := context.Background()
	req := createReq(t, mgr, "Linked Source")
	dec := createDec(t, mgr, "Linked Target")
	if _, err := mgr.CreateRelation(ctx, dec.ID, "addresses", req.ID, entity.RelationOptions{}); err != nil {
		t.Fatalf("create relation: %v", err)
	}

	if _, err := mgr.DeleteEntity(ctx, req.ID, false); !errors.Is(err, entitymanager.ErrHasRelations) {
		t.Fatalf("expected ErrHasRelations, got %v", err)
	}
}

func TestDelete_CascadeRemovesIncidentRelations(t *testing.T) {
	t.Parallel()
	mgr, _ := newManager(t, nil)
	ctx := context.Background()
	req := createReq(t, mgr, "Source")
	dec := createDec(t, mgr, "Target")
	if _, err := mgr.CreateRelation(ctx, dec.ID, "addresses", req.ID, entity.RelationOptions{}); err != nil {
		t.Fatalf("create relation: %v", err)
	}

	res, err := mgr.DeleteEntity(ctx, req.ID, true)
	if err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
	if len(res.DeletedEntities) != 1 || res.DeletedEntities[0].ID != req.ID {
		t.Errorf("DeletedEntities = %v, want [%s]", res.DeletedEntities, req.ID)
	}
	if len(res.DeletedRelations) != 1 {
		t.Errorf("DeletedRelations len = %d, want 1", len(res.DeletedRelations))
	}
}

// --- Rename path ---

func TestRename_DryRunDoesNotChangeStore(t *testing.T) {
	t.Parallel()
	mgr, cs := newManager(t, nil)
	ctx := context.Background()
	req := createReq(t, mgr, "To Be Renamed")

	creatBefore := cs.creates.Load()
	updatesBefore := cs.updates.Load()
	deletesBefore := cs.deletes.Load()

	newID := req.ID + "X"
	res, err := mgr.RenameEntity(ctx, req.ID, newID, entity.RenameOptions{DryRun: true})
	if err != nil {
		t.Fatalf("RenameEntity dry: %v", err)
	}
	if res.OldID != req.ID || res.NewID != newID {
		t.Errorf("rename result = %+v, want old=%s new=%s", res, req.ID, newID)
	}

	// No writes after dry-run.
	if got := cs.creates.Load() - creatBefore; got != 0 {
		t.Errorf("dry-run creates = %d, want 0", got)
	}
	if got := cs.updates.Load() - updatesBefore; got != 0 {
		t.Errorf("dry-run updates = %d, want 0", got)
	}
	if got := cs.deletes.Load() - deletesBefore; got != 0 {
		t.Errorf("dry-run deletes = %d, want 0", got)
	}

	// Entity still present at old ID.
	if _, err := cs.GetEntity(ctx, req.ID); err != nil {
		t.Errorf("entity missing at old ID after dry-run: %v", err)
	}
}

func TestRename_AppliesAndRewritesRelations(t *testing.T) {
	t.Parallel()
	mgr, _ := newManager(t, nil)
	ctx := context.Background()
	req := createReq(t, mgr, "Original")
	dec := createDec(t, mgr, "Face")
	if _, err := mgr.CreateRelation(ctx, dec.ID, "addresses", req.ID, entity.RelationOptions{}); err != nil {
		t.Fatalf("create relation: %v", err)
	}

	newID := req.ID + "-renamed"
	res, err := mgr.RenameEntity(ctx, req.ID, newID, entity.RenameOptions{})
	if err != nil {
		t.Fatalf("RenameEntity: %v", err)
	}
	if res.RelationsUpdated != 1 {
		t.Errorf("RelationsUpdated = %d, want 1", res.RelationsUpdated)
	}

	// Old ID gone, new ID present.
	if _, err := mgr.DeleteEntity(ctx, req.ID, false); !errors.Is(err, entitymanager.ErrEntityNotFound) {
		t.Errorf("expected ErrEntityNotFound for old ID, got %v", err)
	}
}

func TestRename_NotFoundReturnsTypedError(t *testing.T) {
	t.Parallel()
	mgr, _ := newManager(t, nil)
	_, err := mgr.RenameEntity(context.Background(), "REQ-999", "REQ-998", entity.RenameOptions{})
	if !errors.Is(err, entitymanager.ErrEntityNotFound) {
		t.Fatalf("expected ErrEntityNotFound, got %v", err)
	}
}

// TestRename_TargetExistsDoesNotOverwrite pins BUG-5QDV6F: renaming onto
// an ID another entity already occupies must fail with
// ErrEntityAlreadyExists and leave the target untouched — never clobber
// it. Routing through the atomic store.RenameEntity makes this the
// store's conflict check (no non-atomic create-then-update window), and
// the counting store confirms the rename performs zero writes.
func TestRename_TargetExistsDoesNotOverwrite(t *testing.T) {
	t.Parallel()
	mgr, cs := newManager(t, nil)
	ctx := context.Background()
	src := createReq(t, mgr, "source")
	dst := createReq(t, mgr, "occupied target")

	creates, updates, deletes := cs.creates.Load(), cs.updates.Load(), cs.deletes.Load()

	_, err := mgr.RenameEntity(ctx, src.ID, dst.ID, entity.RenameOptions{})
	if !errors.Is(err, entitymanager.ErrEntityAlreadyExists) {
		t.Fatalf("expected ErrEntityAlreadyExists, got %v", err)
	}
	if got := cs.creates.Load() - creates; got != 0 {
		t.Errorf("conflicting rename creates = %d, want 0", got)
	}
	if got := cs.updates.Load() - updates; got != 0 {
		t.Errorf("conflicting rename updates = %d, want 0 (must never overwrite the target)", got)
	}
	if got := cs.deletes.Load() - deletes; got != 0 {
		t.Errorf("conflicting rename deletes = %d, want 0", got)
	}

	// Both entities still present at their original IDs, with their
	// original titles — the target was not overwritten by the source.
	gotDst, err := cs.GetEntity(ctx, dst.ID)
	if err != nil {
		t.Fatalf("target entity missing after conflicting rename: %v", err)
	}
	if title := gotDst.GetString("title"); title != "occupied target" {
		t.Errorf("target title = %q, want %q — target was clobbered", title, "occupied target")
	}
	if _, err := cs.GetEntity(ctx, src.ID); err != nil {
		t.Errorf("source entity missing after failed rename: %v", err)
	}
}

// --- Relation methods ---

func TestCreateRelation_DuplicateRejectedTyped(t *testing.T) {
	t.Parallel()
	mgr, _ := newManager(t, nil)
	ctx := context.Background()
	req := createReq(t, mgr, "r")
	dec := createDec(t, mgr, "d")

	if _, err := mgr.CreateRelation(ctx, dec.ID, "addresses", req.ID, entity.RelationOptions{}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := mgr.CreateRelation(ctx, dec.ID, "addresses", req.ID, entity.RelationOptions{})
	if !errors.Is(err, entitymanager.ErrRelationAlreadyExists) {
		t.Fatalf("expected ErrRelationAlreadyExists, got %v", err)
	}
}

func TestCreateRelation_SourceNotFoundTyped(t *testing.T) {
	t.Parallel()
	mgr, _ := newManager(t, nil)
	dec := createDec(t, mgr, "Target")
	_, err := mgr.CreateRelation(context.Background(), "REQ-999", "addresses", dec.ID, entity.RelationOptions{})
	if !errors.Is(err, entitymanager.ErrEntityNotFound) {
		t.Fatalf("expected ErrEntityNotFound, got %v", err)
	}
}

func TestUpdateRelation_MergesProperties(t *testing.T) {
	t.Parallel()
	mgr, _ := newManager(t, nil)
	ctx := context.Background()
	req := createReq(t, mgr, "r")
	dec := createDec(t, mgr, "d")
	if _, err := mgr.CreateRelation(ctx, dec.ID, "addresses", req.ID, entity.RelationOptions{
		Properties: map[string]any{"weight": "high", "extra": "keep"},
	}); err != nil {
		t.Fatalf("create relation: %v", err)
	}

	// Merge a new value and unset "extra".
	rel, err := mgr.UpdateRelation(ctx, dec.ID, "addresses", req.ID, entity.RelationOptions{
		Properties: map[string]any{"weight": "low"},
		MetaUnset:  []string{"extra"},
	})
	if err != nil {
		t.Fatalf("UpdateRelation: %v", err)
	}
	if got := rel.Properties["weight"]; got != "low" {
		t.Errorf("weight = %v, want low", got)
	}
	if _, present := rel.Properties["extra"]; present {
		t.Errorf("extra still present after MetaUnset: %v", rel.Properties)
	}
}

func TestUpdateRelation_NotFoundTyped(t *testing.T) {
	t.Parallel()
	mgr, _ := newManager(t, nil)
	_, err := mgr.UpdateRelation(context.Background(), "DEC-1", "addresses", "REQ-1", entity.RelationOptions{})
	if !errors.Is(err, entitymanager.ErrRelationNotFound) {
		t.Fatalf("expected ErrRelationNotFound, got %v", err)
	}
}

func TestDeleteRelation_RoundTrip(t *testing.T) {
	t.Parallel()
	mgr, _ := newManager(t, nil)
	ctx := context.Background()
	req := createReq(t, mgr, "r")
	dec := createDec(t, mgr, "d")
	if _, err := mgr.CreateRelation(ctx, dec.ID, "addresses", req.ID, entity.RelationOptions{}); err != nil {
		t.Fatalf("create relation: %v", err)
	}
	if err := mgr.DeleteRelation(ctx, dec.ID, "addresses", req.ID); err != nil {
		t.Fatalf("DeleteRelation: %v", err)
	}
	// Second delete is now a "not found" but DeleteRelation wraps as
	// a generic "delete relation" error — we just assert it fails.
	if err := mgr.DeleteRelation(ctx, dec.ID, "addresses", req.ID); err == nil {
		t.Error("expected error deleting already-deleted relation")
	}
}

// --- Upsert error-propagation invariant (regression for C1) ---

// TestCreate_PropagatesNonConflictStoreError pins that createCore
// does NOT mask a non-ErrConflict store failure by falling through to
// UpdateEntity. With the workspace-era bug, a CreateEntity that
// returned a generic I/O error would silently reach UpdateEntity and
// likely return ErrNotFound, hiding the real cause.
func TestCreate_PropagatesNonConflictStoreError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("simulated disk failure")
	cs := &failingCreateStore{
		Store: memstore.New(),
		err:   sentinel,
	}
	cs.remaining.Store(1)

	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       cs,
		Meta:        parseMeta(t),
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e := entity.New("", "requirement")
	e.SetString("title", "Will Fail")
	_, err = mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel propagated, got %v", err)
	}
	if got := cs.updateCalls.Load(); got != 0 {
		t.Errorf("UpdateEntity calls = %d, want 0 (must not mask non-conflict)", got)
	}
}

// --- DEC-HWZHA: soft-validation partitioning ---

// softValidationMetamodel adds an enum property whose missing-value
// case is a soft (warning) condition.
const softValidationMetamodel = `version: "1.0"
entities:
  ticket:
    label: Ticket
    plural: tickets
    id_prefix: "TKT-"
    id_type: sequential
    properties:
      title:
        type: string
        required: true
      priority:
        type: priority
        required: true
relations: {}
types:
  priority:
    values: [low, medium, high]
`

// TestCreate_SoftValidationProducesWarning pins DEC-HWZHA: a
// required-but-missing soft-validation condition surfaces as a entity.Warning
// on entity.CreateResult while the write succeeds.
func TestCreate_SoftValidationProducesWarning(t *testing.T) {
	t.Parallel()
	meta, err := metamodel.Parse([]byte(softValidationMetamodel))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       memstore.New(),
		Meta:        meta,
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Create with missing required `priority` — soft condition under
	// DEC-HWZHA, write succeeds, warning surfaces.
	e := entity.New("", "ticket")
	e.SetString("title", "Soft warning case")
	result, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("Warnings len = %d, want 1: %+v", len(result.Warnings), result.Warnings)
	}
	w := result.Warnings[0]
	if w.Code != "required_property_unset" {
		t.Errorf("entity.Warning.Code = %q, want required_property_unset", w.Code)
	}
	if w.Path != "/properties/priority" {
		t.Errorf("entity.Warning.Path = %q, want /properties/priority", w.Path)
	}
}

// TestCreate_HardValidationStillAborts pins that hard validation
// errors still abort the write (DEC-HWZHA hard class is unchanged).
func TestCreate_HardValidationStillAborts(t *testing.T) {
	t.Parallel()
	mgr, cs := newManager(t, nil)

	// Unknown entity type is a hard error.
	e := entity.New("", "nonexistent_type")
	_, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
	if !strings.Contains(err.Error(), "unknown entity type") {
		t.Errorf("error = %q, want contains 'unknown entity type'", err.Error())
	}
	if got := cs.creates.Load(); got != 0 {
		t.Errorf("CreateEntity calls = %d, want 0 (hard error must abort)", got)
	}
}

// TestUpdate_SoftValidationProducesWarning pins the same DEC-HWZHA
// behavior on the update path.
func TestUpdate_SoftValidationProducesWarning(t *testing.T) {
	t.Parallel()
	meta, err := metamodel.Parse([]byte(softValidationMetamodel))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       memstore.New(),
		Meta:        meta,
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	// Seed an entity that has the required field set.
	e := entity.New("", "ticket")
	e.SetString("title", "Initial")
	e.SetString("priority", "high")
	created, err := mgr.CreateEntity(ctx, e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if len(created.Warnings) != 0 {
		t.Fatalf("seed warnings = %d, want 0", len(created.Warnings))
	}

	// Clear `priority` and update — soft warning.
	updated := &entity.Entity{
		ID:         created.Entity.ID,
		Type:       created.Entity.Type,
		Properties: map[string]any{"title": "Cleared"},
	}
	result, err := mgr.UpdateEntity(ctx, updated)
	if err != nil {
		t.Fatalf("UpdateEntity: %v", err)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("Warnings len = %d, want 1: %+v", len(result.Warnings), result.Warnings)
	}
}
