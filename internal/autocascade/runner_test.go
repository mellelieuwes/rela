package autocascade_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/autocascade"
	"github.com/Sourcehaven-BV/rela/internal/automation"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// --- New / construction tests ---

func TestNew_RejectsNilEngine(t *testing.T) {
	t.Parallel()
	if _, err := autocascade.New(autocascade.Deps{Engine: nil}); err == nil {
		t.Fatal("expected error for nil Engine, got nil")
	}
}

// --- Stub Host for unit tests ---

// stubHost records calls and returns canned results. Only the methods
// each test actually exercises are programmed; the rest return zero
// values that would surface as test failures if Runner called them.
//
// Calls is a single ordered log so action-order tests can assert
// dispatch sequence across the four action paths.
type stubHost struct {
	t *testing.T

	meta  *metamodel.Metamodel
	store store.Store

	// Programmed behavior.
	createErr      error // returned by Host.CreateEntity
	writeRelErr    error // returned by Host.WriteRelation
	existingTarget *entity.Entity
	createCounter  int // suffix for unique auto-IDs

	// Call log.
	Calls []string
}

func (h *stubHost) CreateEntity(ctx context.Context, entityType string, opts autocascade.CreateEntityOptions) (*entity.Entity, error) {
	h.Calls = append(h.Calls, "CreateEntity:"+entityType)
	if h.createErr != nil {
		return nil, h.createErr
	}
	h.createCounter++
	id := opts.ID
	if id == "" {
		id = fmt.Sprintf("AUTO-%03d", h.createCounter)
	}
	e := entity.New(id, entityType)
	maps.Copy(e.Properties, opts.Properties)
	if h.store != nil {
		if err := h.store.CreateEntity(ctx, e); err != nil {
			h.t.Fatalf("stub CreateEntity: %v", err)
		}
	}
	return e, nil
}

func (h *stubHost) WriteEntity(ctx context.Context, e *entity.Entity, _ map[string]string) error {
	h.Calls = append(h.Calls, "WriteEntity:"+e.ID)
	if h.store == nil {
		return nil
	}
	// WriteEntity is an upsert in workspace; reproduce that here by
	// trying Create first, then Update if-and-only-if the failure is
	// store.ErrConflict. Any other error propagates.
	if err := h.store.CreateEntity(ctx, e); err != nil {
		if !errors.Is(err, store.ErrConflict) {
			return err
		}
		return h.store.UpdateEntity(ctx, e)
	}
	return nil
}

func (h *stubHost) WriteRelation(ctx context.Context, r *entity.Relation) error {
	h.Calls = append(h.Calls, "WriteRelation:"+r.From+"--"+r.Type+"-->"+r.To)
	if h.writeRelErr != nil {
		return h.writeRelErr
	}
	if h.store != nil {
		if _, err := h.store.CreateRelation(ctx, r.From, r.Type, r.To, nil); err != nil {
			return err
		}
	}
	return nil
}

func (h *stubHost) DeleteEntity(_ context.Context, entityType, id string, cascade bool) error {
	h.Calls = append(h.Calls, fmt.Sprintf("DeleteEntity:%s:%s:%v", entityType, id, cascade))
	return nil
}

func (h *stubHost) GetEntity(ctx context.Context, id string) (*entity.Entity, error) {
	if h.store == nil {
		return nil, errors.New("stubHost.GetEntity: no store configured")
	}
	return h.store.GetEntity(ctx, id)
}

func (h *stubHost) ValidateRelation(relType, fromType, toType string) error {
	if h.meta == nil {
		return nil // permissive by default; tests opt in by setting meta
	}
	return h.meta.ValidateRelation(relType, fromType, toType)
}

func (h *stubHost) FindExistingRelationTarget(_ context.Context, sourceID, relationType, targetType string) *entity.Entity {
	h.Calls = append(h.Calls, "FindExistingRelationTarget:"+sourceID+":"+relationType+":"+targetType)
	return h.existingTarget
}

// --- Helpers ---

// newRunner constructs a Runner with a freshly built no-rules Engine
// (or the engine supplied by the test). Tests that exercise script
// execution pass a [autocascade.ScriptRunner] via Request.Scripts.
func newRunner(t *testing.T, engine *automation.Engine) *autocascade.Runner {
	t.Helper()
	if engine == nil {
		engine = automation.NewEngine(nil)
	}
	r, err := autocascade.New(autocascade.Deps{Engine: engine})
	if err != nil {
		t.Fatalf("autocascade.New: %v", err)
	}
	return r
}

// emptyRequest returns a Request that triggers no work.
func emptyRequest(trigger *entity.Entity) autocascade.Request {
	return autocascade.Request{
		Trigger: trigger,
		Result:  &automation.Result{},
	}
}

// --- AC5 tests ---

func TestRunnerEmptyResult(t *testing.T) {
	t.Parallel()
	r := newRunner(t, nil)
	trigger := entity.New("REQ-001", "requirement")

	host := &stubHost{t: t}
	outcome, err := r.Process(context.Background(), host, emptyRequest(trigger))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(outcome.EntitiesCreated) != 0 || len(outcome.RelationsCreated) != 0 ||
		len(outcome.Errors) != 0 || len(outcome.Warnings) != 0 {

		t.Errorf("expected empty outcome, got %+v", outcome)
	}
	if len(host.Calls) != 0 {
		t.Errorf("expected no Host calls, got %v", host.Calls)
	}
}

// TestRunnerDepthLimit pins the depth-limit warning wording.
// The format string lived in workspace.go:1082–1086 before the move.
func TestRunnerDepthLimit(t *testing.T) {
	t.Parallel()
	auto := automation.Automation{
		Name: "chain-recursion",
		On: automation.Trigger{
			Entity:  []string{"chain"},
			Created: true,
		},
		Do: []automation.Action{
			{
				CreateEntity: &automation.CreateEntityAction{
					Type: "chain",
				},
			},
		},
	}
	r := newRunner(t, automation.NewEngine([]automation.Automation{auto}))
	host := &stubHost{t: t, store: memstore.New()}

	starter := entity.New("CHAIN-000", "chain")
	req := autocascade.Request{
		Trigger: starter,
		Result: &automation.Result{
			EntitiesToCreate: []automation.EntityToCreate{
				{Type: "chain"},
			},
		},
	}
	outcome, err := r.Process(context.Background(), host, req)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	expectedWarning := fmt.Sprintf("automation iteration limit (%d) reached; 1 pending items skipped", autocascade.MaxDepth)
	found := slices.Contains(outcome.Warnings, expectedWarning)
	if !found {
		t.Errorf("expected warning %q, got warnings: %v", expectedWarning, outcome.Warnings)
	}
}

// TestRunnerIfExistsSkip — existing target found, IfExistsSkip: no
// CreateEntity.
func TestRunnerIfExistsSkip(t *testing.T) {
	t.Parallel()
	r := newRunner(t, nil)
	existing := entity.New("EXISTING-1", "checklist")
	host := &stubHost{t: t, existingTarget: existing}

	trigger := entity.New("REQ-001", "requirement")
	req := autocascade.Request{
		Trigger: trigger,
		Result: &automation.Result{
			EntitiesToCreate: []automation.EntityToCreate{
				{
					Type:                "checklist",
					RelationFromTrigger: "has-checklist",
					IfExists:            automation.IfExistsSkip,
				},
			},
		},
	}
	outcome, err := r.Process(context.Background(), host, req)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	// Existing target reported as "created" (preserves workspace
	// behavior: skipped-existing entities still appear in
	// EntitiesCreated so callers can link to them).
	if len(outcome.EntitiesCreated) != 1 || outcome.EntitiesCreated[0].ID != existing.ID {
		t.Errorf("expected EntitiesCreated=[%s], got %v", existing.ID, outcome.EntitiesCreated)
	}

	for _, c := range host.Calls {
		if strings.HasPrefix(c, "CreateEntity:") {
			t.Errorf("unexpected CreateEntity call: %v", host.Calls)
		}
	}
}

// TestRunnerIfExistsError — existing target found, IfExistsError:
// error recorded, no create.
func TestRunnerIfExistsError(t *testing.T) {
	t.Parallel()
	r := newRunner(t, nil)
	existing := entity.New("EXISTING-1", "checklist")
	host := &stubHost{t: t, existingTarget: existing}

	trigger := entity.New("REQ-001", "requirement")
	req := autocascade.Request{
		Trigger: trigger,
		Result: &automation.Result{
			EntitiesToCreate: []automation.EntityToCreate{
				{
					Type:                "checklist",
					RelationFromTrigger: "has-checklist",
					IfExists:            automation.IfExistsError,
				},
			},
		},
	}
	outcome, err := r.Process(context.Background(), host, req)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(outcome.Errors) == 0 {
		t.Fatal("expected an error in outcome.Errors")
	}
	if !strings.Contains(outcome.Errors[0], "entity already exists") {
		t.Errorf("expected 'entity already exists' error, got %q", outcome.Errors[0])
	}
}

// TestRunnerEntityCreateError — CreateEntity returns error:
// recorded, cascade continues.
func TestRunnerEntityCreateError(t *testing.T) {
	t.Parallel()
	r := newRunner(t, nil)
	host := &stubHost{t: t, createErr: errors.New("simulated create failure"), store: memstore.New()}

	trigger := entity.New("REQ-001", "requirement")
	req := autocascade.Request{
		Trigger: trigger,
		Result: &automation.Result{
			EntitiesToCreate: []automation.EntityToCreate{
				{Type: "checklist"},
			},
		},
	}
	outcome, err := r.Process(context.Background(), host, req)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(outcome.Errors) != 1 {
		t.Fatalf("expected exactly one error, got %v", outcome.Errors)
	}
	if !strings.Contains(outcome.Errors[0], "failed to create automation entity") {
		t.Errorf("expected 'failed to create automation entity' error, got %q", outcome.Errors[0])
	}
	if len(outcome.EntitiesCreated) != 0 {
		t.Errorf("expected no entities created, got %v", outcome.EntitiesCreated)
	}
}

// TestRunnerRelationCreateError — WriteRelation returns error: recorded,
// cascade continues.
func TestRunnerRelationCreateError(t *testing.T) {
	t.Parallel()
	meta, err := metamodel.Parse([]byte(`version: "1.0"
entities:
  requirement:
    label: Requirement
    plural: requirements
    id_prefix: "REQ-"
    id_type: short
    properties:
      title:
        type: string
  decision:
    label: Decision
    plural: decisions
    id_prefix: "DEC-"
    id_type: short
    properties:
      title:
        type: string
relations:
  references:
    label: references
    from: [requirement]
    to: [decision]
`))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	mem := memstore.New()
	target := entity.New("DEC-001", "decision")
	if err := mem.CreateEntity(context.Background(), target); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	host := &stubHost{
		t:           t,
		meta:        meta,
		store:       mem,
		writeRelErr: errors.New("simulated write failure"),
	}

	r := newRunner(t, nil)
	trigger := entity.New("REQ-001", "requirement")
	req := autocascade.Request{
		Trigger: trigger,
		Result: &automation.Result{
			RelationsToCreate: []automation.RelationToCreate{
				{Relation: entity.NewRelation("", "references", "DEC-001")},
			},
		},
	}
	outcome, _ := r.Process(context.Background(), host, req)
	if len(outcome.Errors) != 1 {
		t.Fatalf("expected exactly one error, got %v", outcome.Errors)
	}
	if !strings.Contains(outcome.Errors[0], "failed to create automation relation") {
		t.Errorf("expected 'failed to create automation relation' error, got %q", outcome.Errors[0])
	}
	if len(outcome.RelationsCreated) != 0 {
		t.Errorf("expected no relations created, got %v", outcome.RelationsCreated)
	}
}

// TestRunnerActionOrder pins per-iteration action order: Lua → relations → entities.
func TestRunnerActionOrder(t *testing.T) {
	t.Parallel()
	meta, err := metamodel.Parse([]byte(`version: "1.0"
entities:
  requirement:
    label: Requirement
    plural: requirements
    id_prefix: "REQ-"
    id_type: short
    properties:
      title:
        type: string
  decision:
    label: Decision
    plural: decisions
    id_prefix: "DEC-"
    id_type: short
    properties:
      title:
        type: string
  checklist:
    label: Checklist
    plural: checklists
    id_prefix: "CL-"
    id_type: short
    properties:
      title:
        type: string
relations:
  references:
    label: references
    from: [requirement]
    to: [decision]
`))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	mem := memstore.New()
	if seedErr := mem.CreateEntity(context.Background(), entity.New("DEC-001", "decision")); seedErr != nil {
		t.Fatalf("seed target: %v", seedErr)
	}

	recordingScripts := &recordingScriptRunner{}
	r := newRunner(t, nil)

	host := &stubHost{t: t, meta: meta, store: mem}

	trigger := entity.New("REQ-001", "requirement")
	req := autocascade.Request{
		Trigger: trigger,
		Scripts: recordingScripts,
		Result: &automation.Result{
			LuaToExecute: []automation.LuaToExecute{
				{Code: "print('hi')", AutomationName: "test-lua"},
			},
			RelationsToCreate: []automation.RelationToCreate{
				{Relation: entity.NewRelation("", "references", "DEC-001")},
			},
			EntitiesToCreate: []automation.EntityToCreate{
				{Type: "checklist"},
			},
		},
	}
	outcome, err := r.Process(context.Background(), host, req)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(outcome.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", outcome.Errors)
	}

	if recordingScripts.runCalls != 1 {
		t.Errorf("expected 1 ScriptRunner.Run call, got %d", recordingScripts.runCalls)
	}
	if len(host.Calls) < 2 {
		t.Fatalf("expected at least 2 host calls (WriteRelation, CreateEntity), got %v", host.Calls)
	}
	if !strings.HasPrefix(host.Calls[0], "WriteRelation:") {
		t.Errorf("expected WriteRelation first among host calls, got %v", host.Calls)
	}
	if !strings.HasPrefix(host.Calls[1], "CreateEntity:") {
		t.Errorf("expected CreateEntity second among host calls, got %v", host.Calls)
	}
}

// TestRunnerScriptError pins error-propagation from ScriptRunner.Run
// into Outcome.Errors. The Runner appends the stringified error as-is
// and continues — engine-specific formatting (e.g. lua.ScriptError
// Path patching) is the adapter's responsibility and is tested at
// the workspace layer.
func TestRunnerScriptError(t *testing.T) {
	t.Parallel()
	scripts := &failingScriptRunner{err: errors.New("boom from runner")}
	r := newRunner(t, nil)

	trigger := entity.New("REQ-001", "requirement")
	req := autocascade.Request{
		Trigger: trigger,
		Scripts: scripts,
		Result: &automation.Result{
			LuaToExecute: []automation.LuaToExecute{
				{Code: "error('boom')", AutomationName: "my-automation"},
			},
		},
	}
	outcome, err := r.Process(context.Background(), &stubHost{t: t}, req)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(outcome.Errors) != 1 {
		t.Fatalf("expected exactly one error, got %v", outcome.Errors)
	}
	if outcome.Errors[0] != "boom from runner" {
		t.Errorf("expected error to be 'boom from runner', got %q", outcome.Errors[0])
	}
}

// TestRunnerMissingScriptRunner — a Result with scripted actions but
// no Request.Scripts records an error per action and continues.
func TestRunnerMissingScriptRunner(t *testing.T) {
	t.Parallel()
	r := newRunner(t, nil)

	trigger := entity.New("REQ-001", "requirement")
	req := autocascade.Request{
		Trigger: trigger,
		// Scripts intentionally nil.
		Result: &automation.Result{
			LuaToExecute: []automation.LuaToExecute{
				{Code: "noop()", AutomationName: "uncovered"},
			},
		},
	}
	outcome, err := r.Process(context.Background(), &stubHost{t: t}, req)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(outcome.Errors) != 1 {
		t.Fatalf("expected exactly one error, got %v", outcome.Errors)
	}
	if !strings.Contains(outcome.Errors[0], "no ScriptRunner configured") {
		t.Errorf("expected 'no ScriptRunner configured' error, got %q", outcome.Errors[0])
	}
}

// --- ScriptRunner stubs ---

type recordingScriptRunner struct {
	runCalls int
	actions  []autocascade.ScriptAction
}

func (r *recordingScriptRunner) Run(_ context.Context, a autocascade.ScriptAction, _ autocascade.Mutator) error {
	r.runCalls++
	r.actions = append(r.actions, a)
	return nil
}

type failingScriptRunner struct {
	err error
}

func (f *failingScriptRunner) Run(_ context.Context, _ autocascade.ScriptAction, _ autocascade.Mutator) error {
	return f.err
}

// ctxCapturingScriptRunner records the ctx each scripted action runs under, so
// a test can assert the audit `triggered_by` label the cascade stamped.
type ctxCapturingScriptRunner struct {
	labels []string
}

func (c *ctxCapturingScriptRunner) Run(
	ctx context.Context, _ autocascade.ScriptAction, _ autocascade.Mutator,
) error {
	c.labels = append(c.labels, audit.TriggeredByFrom(ctx))
	return nil
}

// TestRunnerScriptActionTriggeredByLabel pins the label a scripted action runs
// under (TKT-JJRVX9).
//
// The empty-name case is the point. This path used to stamp
// `audit.WithTriggeredBy(ctx, "automation:"+name)` unguarded, which wrote a
// literal dangling `"automation:"` for a nameless automation — an automation
// name is an ordinary optional field on a YAML list entry with no loader
// validation behind it, so "" is reachable. It now routes through
// triggeredByCtx like the relation and entity paths, leaving the ctx untagged
// so the audit layer supplies the generic `automation` label instead.
func TestRunnerScriptActionTriggeredByLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		automation    string
		wantLabel     string
		wantLabelDesc string
	}{
		{
			name:          "named automation is attributed",
			automation:    "notify-owner",
			wantLabel:     "automation:notify-owner",
			wantLabelDesc: "the automation's own name",
		},
		{
			name:          "nameless automation leaves ctx untagged",
			automation:    "",
			wantLabel:     "",
			wantLabelDesc: "no label, so the audit layer falls back to generic \"automation\"",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scripts := &ctxCapturingScriptRunner{}
			r := newRunner(t, nil)
			// The Result carries only a scripted action, so the host is never
			// asked to write anything — a bare metamodel is enough.
			meta, err := metamodel.Parse([]byte(`version: "1.0"
entities:
  requirement:
    label: Requirement
    plural: requirements
    id_prefix: "REQ-"
    id_type: short
    properties:
      title:
        type: string
`))
			if err != nil {
				t.Fatalf("metamodel.Parse: %v", err)
			}
			host := &stubHost{t: t, meta: meta, store: memstore.New()}

			outcome, err := r.Process(context.Background(), host, autocascade.Request{
				Trigger: entity.New("REQ-001", "requirement"),
				Scripts: scripts,
				Result: &automation.Result{
					LuaToExecute: []automation.LuaToExecute{
						{Code: "print('hi')", AutomationName: tc.automation},
					},
				},
			})
			if err != nil {
				t.Fatalf("Process: %v", err)
			}
			if len(outcome.Errors) != 0 {
				t.Fatalf("unexpected errors: %v", outcome.Errors)
			}

			if len(scripts.labels) != 1 {
				t.Fatalf("want 1 script invocation, got %d", len(scripts.labels))
			}
			if scripts.labels[0] != tc.wantLabel {
				t.Errorf("triggered_by: want %q (%s), got %q",
					tc.wantLabel, tc.wantLabelDesc, scripts.labels[0])
			}
		})
	}
}
