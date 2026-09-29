package entitymanager_test

import (
	"context"
	"errors"
	"strings"
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
)

// patchMetamodelYAML mirrors the CalDAV/VTODO shape that motivated the
// primitive: a task carrying a handful of properties, only some of which
// any given partial writer knows how to express.
const patchMetamodelYAML = `version: "1.0"
entities:
  task:
    label: Task
    plural: tasks
    id_type: manual
    properties:
      title:
        type: string
      status:
        type: string
      due:
        type: string
      salary:
        type: string
      notes:
        type: string
`

func newPatchManager(t *testing.T, gate entitymanager.FieldWriteGate) (*entitymanager.Manager, store.Store) {
	t.Helper()
	meta, err := metamodel.Parse([]byte(patchMetamodelYAML))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	st := memstore.New()
	if gate == nil {
		gate = entitymanager.AllowAllFieldGate{}
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       st,
		Meta:        meta,
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   gate,
		Twins:       entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	return mgr, st
}

// seedTask writes a task directly to the store so the fixture is
// independent of the write path under test.
func seedTask(t *testing.T, st store.Store, id string, props map[string]any, body string) {
	t.Helper()
	e := entity.New(id, "task")
	e.Properties = props
	e.Content = body
	if err := st.CreateEntity(context.Background(), e); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func mustGet(t *testing.T, st store.Store, id string) *entity.Entity {
	t.Helper()
	got, err := st.GetEntity(context.Background(), id)
	if err != nil {
		t.Fatalf("GetEntity %s: %v", id, err)
	}
	return got
}

// TestPatchEntity_PreservesUnnamedProperties is the INVERSE test — the one
// that would have caught this bug class at its origin (TKT-80EWGM AC2).
//
// A caller that can only see a subset of an entity's properties patches
// exactly one of them. Every property it did not name must survive
// byte-identical, INCLUDING the ones it could never have read. Under the
// old GetEntity→merge→UpdateEntity idiom a redacted-read caller silently
// erased those; with a patch, forgetting is a no-op.
func TestPatchEntity_PreservesUnnamedProperties(t *testing.T) {
	t.Parallel()
	mgr, st := newPatchManager(t, nil)

	seedTask(t, st, "TASK-1", map[string]any{
		"title":  "Original",
		"status": "todo",
		"due":    "2026-01-01",
		"salary": "secret-value",
		"notes":  "hidden-notes",
	}, "original body")

	newTitle := "Updated"
	if _, err := mgr.PatchEntity(context.Background(), "TASK-1", entity.Patch{
		Properties: map[string]any{"title": newTitle},
	}); err != nil {
		t.Fatalf("PatchEntity: %v", err)
	}

	got := mustGet(t, st, "TASK-1")
	if got.Properties["title"] != newTitle {
		t.Errorf("title = %v, want %q", got.Properties["title"], newTitle)
	}
	// The whole point: everything unnamed is untouched.
	for k, want := range map[string]string{
		"status": "todo",
		"due":    "2026-01-01",
		"salary": "secret-value",
		"notes":  "hidden-notes",
	} {
		if got.Properties[k] != want {
			t.Errorf("property %q = %v, want %q (unnamed properties must survive)", k, got.Properties[k], want)
		}
	}
	if got.Content != "original body" {
		t.Errorf("body = %q, want it untouched", got.Content)
	}
}

// TestPatchEntity_SetUnsetAbsent pins the three distinct behaviors that
// make a patch a patch: set changes, unset removes, absent preserves.
func TestPatchEntity_SetUnsetAbsent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		patch    entity.Patch
		wantDue  any
		wantHave bool
	}{
		{
			name:     "set overwrites",
			patch:    entity.Patch{Properties: map[string]any{"due": "2027-12-31"}},
			wantDue:  "2027-12-31",
			wantHave: true,
		},
		{
			name:     "unset removes",
			patch:    entity.Patch{MetaUnset: []string{"due"}},
			wantHave: false,
		},
		{
			name:     "absent preserves",
			patch:    entity.Patch{Properties: map[string]any{"title": "other"}},
			wantDue:  "2026-01-01",
			wantHave: true,
		},
		{
			name: "set and unset same key ends cleared",
			patch: entity.Patch{
				Properties: map[string]any{"due": "2027-12-31"},
				MetaUnset:  []string{"due"},
			},
			wantHave: false,
		},
		{
			name:     "unset absent key is a no-op not an error",
			patch:    entity.Patch{MetaUnset: []string{"notes"}},
			wantDue:  "2026-01-01",
			wantHave: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mgr, st := newPatchManager(t, nil)
			seedTask(t, st, "TASK-1", map[string]any{
				"title": "Original",
				"due":   "2026-01-01",
			}, "")

			if _, err := mgr.PatchEntity(context.Background(), "TASK-1", tc.patch); err != nil {
				t.Fatalf("PatchEntity: %v", err)
			}

			got := mustGet(t, st, "TASK-1")
			val, have := got.Properties["due"]
			if have != tc.wantHave {
				t.Fatalf("due present = %v, want %v (props=%v)", have, tc.wantHave, got.Properties)
			}
			if tc.wantHave && val != tc.wantDue {
				t.Errorf("due = %v, want %v", val, tc.wantDue)
			}
		})
	}
}

// TestPatchEntity_BodyTriState pins the *string tri-state: nil leaves the
// body alone, empty-string clears it, non-empty replaces it. Without the
// pointer there is no way to say "don't touch the body".
func TestPatchEntity_BodyTriState(t *testing.T) {
	t.Parallel()
	empty := ""
	replacement := "new body"

	tests := []struct {
		name    string
		content *string
		want    string
	}{
		{name: "nil leaves body untouched", content: nil, want: "original body"},
		{name: "empty string clears body", content: &empty, want: ""},
		{name: "non-empty replaces body", content: &replacement, want: "new body"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mgr, st := newPatchManager(t, nil)
			seedTask(t, st, "TASK-1", map[string]any{"title": "T"}, "original body")

			if _, err := mgr.PatchEntity(context.Background(), "TASK-1", entity.Patch{
				Content: tc.content,
			}); err != nil {
				t.Fatalf("PatchEntity: %v", err)
			}

			if got := mustGet(t, st, "TASK-1").Content; got != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPatchEntity_RemindersVTODOShape is the CalDAV fixture this ticket
// unblocks: Apple Reminders PUTs only the fields VTODO models and drops
// everything else. Applied as a whole-entity save that would erase the
// rest; as a patch it must not.
func TestPatchEntity_RemindersVTODOShape(t *testing.T) {
	t.Parallel()
	mgr, st := newPatchManager(t, nil)

	seedTask(t, st, "TASK-1", map[string]any{
		"title":  "Renew passport",
		"status": "todo",
		"due":    "2026-06-30",
		"salary": "not-in-vtodo",
		"notes":  "also-not-in-vtodo",
	}, "detail body")

	// Exactly what Reminders sends: SUMMARY, STATUS, DUE. Nothing else.
	if _, err := mgr.PatchEntity(context.Background(), "TASK-1", entity.Patch{
		Properties: map[string]any{
			"title":  "Renew passport",
			"status": "doing",
			"due":    "2026-07-15",
		},
	}); err != nil {
		t.Fatalf("PatchEntity: %v", err)
	}

	got := mustGet(t, st, "TASK-1")
	if got.Properties["status"] != "doing" || got.Properties["due"] != "2026-07-15" {
		t.Errorf("VTODO fields not applied: %v", got.Properties)
	}
	if got.Properties["salary"] != "not-in-vtodo" || got.Properties["notes"] != "also-not-in-vtodo" {
		t.Errorf("properties VTODO cannot express were erased: %v", got.Properties)
	}
	if got.Content != "detail body" {
		t.Errorf("body = %q, want untouched (VTODO sent no DESCRIPTION)", got.Content)
	}
}

// TestPatchEntity_NotFound pins the uniform not-found error.
func TestPatchEntity_NotFound(t *testing.T) {
	t.Parallel()
	mgr, _ := newPatchManager(t, nil)

	_, err := mgr.PatchEntity(context.Background(), "TASK-404", entity.Patch{
		Properties: map[string]any{"title": "x"},
	})
	if !errors.Is(err, entitymanager.ErrEntityNotFound) {
		t.Fatalf("err = %v, want ErrEntityNotFound", err)
	}
}

// TestPatchEntity_EmptyID rejects an empty id rather than reading one.
func TestPatchEntity_EmptyID(t *testing.T) {
	t.Parallel()
	mgr, _ := newPatchManager(t, nil)

	if _, err := mgr.PatchEntity(context.Background(), "", entity.Patch{}); err == nil {
		t.Fatal("expected an error for an empty id")
	}
}

// TestPatchEntity_NonStringValuesSurvive pins that patching does not
// stringify property values — SetString would have.
func TestPatchEntity_NonStringValuesSurvive(t *testing.T) {
	t.Parallel()
	mgr, st := newPatchManager(t, nil)
	seedTask(t, st, "TASK-1", map[string]any{"title": "T"}, "")

	if _, err := mgr.PatchEntity(context.Background(), "TASK-1", entity.Patch{
		Properties: map[string]any{"notes": []any{"a", "b"}},
	}); err != nil {
		t.Fatalf("PatchEntity: %v", err)
	}

	got := mustGet(t, st, "TASK-1")
	list, ok := got.Properties["notes"].([]any)
	if !ok {
		t.Fatalf("notes = %T, want []any (value was stringified)", got.Properties["notes"])
	}
	if len(list) != 2 {
		t.Errorf("notes = %v, want 2 elements", list)
	}
}

// TestPatchEntity_NilPropertiesEntity covers an entity stored with no
// property map at all — Clone allocates one, so the patch must still work.
//
// The unset-only sub-case is the subtle one: Apply only allocates a map
// when Properties is non-empty, so a MetaUnset-only patch runs delete()
// against a possibly-nil map. That is a no-op in Go rather than a panic,
// but the invariant is load-bearing (both MCP and CLI can emit unset-only
// patches) and worth pinning rather than leaving to language trivia.
func TestPatchEntity_NilPropertiesEntity(t *testing.T) {
	t.Parallel()

	t.Run("set on a nil property map", func(t *testing.T) {
		t.Parallel()
		mgr, st := newPatchManager(t, nil)
		seedTask(t, st, "TASK-1", nil, "")

		if _, err := mgr.PatchEntity(context.Background(), "TASK-1", entity.Patch{
			Properties: map[string]any{"title": "set-on-nil-map"},
		}); err != nil {
			t.Fatalf("PatchEntity: %v", err)
		}
		if got := mustGet(t, st, "TASK-1").Properties["title"]; got != "set-on-nil-map" {
			t.Errorf("title = %v, want it set", got)
		}
	})

	t.Run("unset-only against a nil property map does not panic", func(t *testing.T) {
		t.Parallel()
		mgr, st := newPatchManager(t, nil)
		seedTask(t, st, "TASK-2", nil, "body")

		if _, err := mgr.PatchEntity(context.Background(), "TASK-2", entity.Patch{
			MetaUnset: []string{"never-existed"},
		}); err != nil {
			t.Fatalf("PatchEntity: %v", err)
		}
		if got := mustGet(t, st, "TASK-2").Content; got != "body" {
			t.Errorf("body = %q, want it untouched", got)
		}
	})
}

// TestPatch_ApplyOnNilMap pins the same invariant at the unit level, on
// the value type itself, so a future change of property container fails
// here rather than only through the manager.
func TestPatch_ApplyOnNilMap(t *testing.T) {
	t.Parallel()

	e := &entity.Entity{ID: "X-1", Type: "task"} // Properties is nil
	entity.Patch{MetaUnset: []string{"nope"}}.Apply(e)

	if len(e.Properties) != 0 {
		t.Errorf("Properties = %v, want empty", e.Properties)
	}
}

// --- Field-write gate ---

// recordingGate refuses writes touching any denied property and records
// what it was asked about, so tests can assert BOTH the decision and that
// the gate was consulted at all.
type recordingGate struct {
	denied  map[string]bool
	calls   int
	lastSet map[string]any
	lastUn  []string
}

func (g *recordingGate) CheckFieldWrite(
	_ context.Context, _ *entity.Entity, set map[string]any, unset []string,
) error {
	g.calls++
	g.lastSet, g.lastUn = set, unset
	for k := range set {
		if g.denied[k] {
			return errors.New("field write denied: " + k)
		}
	}
	for _, k := range unset {
		if g.denied[k] {
			return errors.New("field unset denied: " + k)
		}
	}
	return nil
}

// TestPatchEntity_FieldGateDeniesSetAndUnset pins parity: a property the
// caller may not author cannot be set OR unset. Unset parity matters —
// erasing a value you cannot see is still writing it.
func TestPatchEntity_FieldGateDeniesSetAndUnset(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		patch entity.Patch
	}{
		{"set denied property", entity.Patch{Properties: map[string]any{"salary": "999"}}},
		{"unset denied property", entity.Patch{MetaUnset: []string{"salary"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gate := &recordingGate{denied: map[string]bool{"salary": true}}
			mgr, st := newPatchManager(t, gate)
			seedTask(t, st, "TASK-1", map[string]any{"title": "T", "salary": "original"}, "")

			if _, err := mgr.PatchEntity(context.Background(), "TASK-1", tc.patch); err == nil {
				t.Fatal("expected the field gate to refuse the write")
			}
			if got := mustGet(t, st, "TASK-1").Properties["salary"]; got != "original" {
				t.Errorf("salary = %v, want %q — a refused write must not persist", got, "original")
			}
		})
	}
}

// TestPatchEntity_FieldGateAllowsUngatedProperty proves the gate is a
// filter, not a blanket block, and that it sees the real diff.
func TestPatchEntity_FieldGateAllowsUngatedProperty(t *testing.T) {
	t.Parallel()
	gate := &recordingGate{denied: map[string]bool{"salary": true}}
	mgr, st := newPatchManager(t, gate)
	seedTask(t, st, "TASK-1", map[string]any{"title": "T", "salary": "original"}, "")

	if _, err := mgr.PatchEntity(context.Background(), "TASK-1", entity.Patch{
		Properties: map[string]any{"title": "allowed"},
		MetaUnset:  []string{"notes"},
	}); err != nil {
		t.Fatalf("PatchEntity: %v", err)
	}

	if gate.calls != 1 {
		t.Errorf("gate consulted %d times, want exactly 1", gate.calls)
	}
	if _, ok := gate.lastSet["title"]; !ok {
		t.Errorf("gate saw set=%v, want it to include the upserted key", gate.lastSet)
	}
	if len(gate.lastUn) != 1 || gate.lastUn[0] != "notes" {
		t.Errorf("gate saw unset=%v, want [notes]", gate.lastUn)
	}
	// The gated property was never named, so it is untouched.
	if got := mustGet(t, st, "TASK-1").Properties["salary"]; got != "original" {
		t.Errorf("salary = %v, want it preserved", got)
	}
}

// TestPatchEntity_RunsFullPipeline pins AC1: a patch is a first-class
// write, not a store poke. Automation must fire and audit must be emitted
// — PatchEntity is NOT in the {Create,Update,Delete,Rename} set that
// internal/entitymanager/CLAUDE.md says inherits audit mechanically, so
// this is verified rather than assumed.
func TestPatchEntity_RunsFullPipeline(t *testing.T) {
	t.Parallel()

	meta, err := metamodel.Parse([]byte(patchMetamodelYAML))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	st := memstore.New()
	sink := audit.NewMemory()
	auto := automation.Automation{
		Name: "stamp-notes-on-status-change",
		On:   automation.Trigger{Entity: []string{"task"}, Property: "status"},
		Do:   []automation.Action{{Set: "notes", Value: "touched-by-automation"}},
	}
	engine := automation.NewEngine([]automation.Automation{auto})
	runner, err := autocascade.New(autocascade.Deps{Engine: engine})
	if err != nil {
		t.Fatalf("autocascade.New: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       st,
		Meta:        meta,
		Templater:   nopTemplater{},
		Audit:       sink,
		ACL:         acl.NopACL{},
		Automations: engine,
		Cascade:     runner,
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}

	seedTask(t, st, "TASK-1", map[string]any{"title": "T", "status": "todo"}, "")

	if _, err := mgr.PatchEntity(context.Background(), "TASK-1", entity.Patch{
		Properties: map[string]any{"status": "doing"},
	}); err != nil {
		t.Fatalf("PatchEntity: %v", err)
	}

	got := mustGet(t, st, "TASK-1")
	if got.Properties["notes"] != "touched-by-automation" {
		t.Errorf("automation did not fire on the patch path: %v", got.Properties)
	}
	if len(sink.Records()) == 0 {
		t.Error("no audit record emitted for a patch — audit must not depend on the method name")
	}
}

// TestPatchEntity_AutomationNotFieldGated pins RR-00ERM9. The gate
// constrains what a PRINCIPAL may author, not what the SYSTEM derives.
// If automation output were gated, a user who cannot author `notes` could
// never trigger an automation that sets it — breaking ordinary workflow
// automations. This test is what stops a future "consistency" change from
// silently doing that.
func TestPatchEntity_AutomationNotFieldGated(t *testing.T) {
	t.Parallel()

	meta, err := metamodel.Parse([]byte(patchMetamodelYAML))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	st := memstore.New()
	// The caller may NOT author `notes` …
	gate := &recordingGate{denied: map[string]bool{"notes": true}}
	// … but an automation sets exactly that property.
	auto := automation.Automation{
		Name: "stamp-notes-on-status-change",
		On:   automation.Trigger{Entity: []string{"task"}, Property: "status"},
		Do:   []automation.Action{{Set: "notes", Value: "set-by-automation"}},
	}
	engine := automation.NewEngine([]automation.Automation{auto})
	runner, err := autocascade.New(autocascade.Deps{Engine: engine})
	if err != nil {
		t.Fatalf("autocascade.New: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       st,
		Meta:        meta,
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Automations: engine,
		Cascade:     runner,
		Transitions: statemachine.EmptySet(),
		FieldGate:   gate,
		Twins:       entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}

	seedTask(t, st, "TASK-1", map[string]any{"title": "T", "status": "todo"}, "")

	// Patching `status` is allowed; the automation's write to the denied
	// `notes` must not be blocked by the caller's field gate.
	if _, err := mgr.PatchEntity(context.Background(), "TASK-1", entity.Patch{
		Properties: map[string]any{"status": "doing"},
	}); err != nil {
		t.Fatalf("PatchEntity: %v — automation output must not be field-gated", err)
	}
	if got := mustGet(t, st, "TASK-1").Properties["notes"]; got != "set-by-automation" {
		t.Errorf("notes = %v, want the automation's value to land", got)
	}
}

// denyWritesACL refuses every write, standing in for a caller with no
// write authority on the entity.
type denyWritesACL struct{}

func (denyWritesACL) AuthorizeWrite(context.Context, acl.WriteRequest) acl.Decision {
	return acl.Decision{Allow: false, RuleKind: "read-only", RuleID: "test-deny-all"}
}

// TestPatchEntity_GateRunsAfterAuthorize is the SECURITY regression net
// for RR-32XA5V. The field gate must not be consulted for a caller who is
// not authorized to write the entity at all.
//
// Field verdicts are value-dependent, so if the gate ran first an
// unauthorized caller could distinguish "entity absent" from "entity
// present but this field is denied" from "present and allowed" — turning a
// refused write into an oracle for entity existence AND for stored
// property values. Both are data, and data is secret.
//
// The assertion is deliberately behavioral rather than positional: the
// gate must record ZERO calls, and the denial must be the ACL's.
func TestPatchEntity_GateRunsAfterAuthorize(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		id     string
		exists bool
	}{
		{name: "existing entity", id: "TASK-1", exists: true},
		{name: "nonexistent entity", id: "TASK-404"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			meta, err := metamodel.Parse([]byte(patchMetamodelYAML))
			if err != nil {
				t.Fatalf("metamodel.Parse: %v", err)
			}
			st := memstore.New()
			gate := &recordingGate{denied: map[string]bool{"salary": true}}
			mgr, err := entitymanager.New(entitymanager.Deps{
				Store:       st,
				Meta:        meta,
				Templater:   nopTemplater{},
				Audit:       audit.Nop{},
				ACL:         denyWritesACL{},
				Transitions: statemachine.EmptySet(),
				FieldGate:   gate,
				Twins:       entitymanager.NoTwinOwnership{},
			})
			if err != nil {
				t.Fatalf("entitymanager.New: %v", err)
			}
			if tc.exists {
				seedTask(t, st, tc.id, map[string]any{"title": "T", "salary": "secret"}, "")
			}

			// Patch a property the gate WOULD refuse. The ACL must win.
			_, patchErr := mgr.PatchEntity(context.Background(), tc.id, entity.Patch{
				Properties: map[string]any{"salary": "999"},
			})
			if patchErr == nil {
				t.Fatal("expected the write to be refused")
			}
			if gate.calls != 0 {
				t.Errorf("field gate was consulted %d times for an unauthorized caller; "+
					"it must run strictly after authorization (RR-32XA5V)", gate.calls)
			}
			if strings.Contains(patchErr.Error(), "denied: salary") {
				t.Errorf("error leaked a field-level verdict to an unauthorized caller: %v", patchErr)
			}
		})
	}
}

// TestPatchEntity_ElevationSkipsFieldGate pins RR-BA1NIV: elevation is
// TOTAL. A half-elevated handle that can write any entity but silently
// drops some property writes is the confusing contract the elevated-read
// seam exists to avoid, and the operator holding bypass_acl can read
// acl.yaml anyway — the gate conceals nothing from them.
func TestPatchEntity_ElevationSkipsFieldGate(t *testing.T) {
	t.Parallel()
	gate := &recordingGate{denied: map[string]bool{"salary": true}}
	mgr, st := newPatchManager(t, gate)
	seedTask(t, st, "TASK-1", map[string]any{"title": "T", "salary": "original"}, "")

	// Assert, don't skip: if the elevated handle ever stops exposing
	// PatchEntity this must fail loudly rather than quietly not running.
	elevated, ok := mgr.Elevated().(interface {
		PatchEntity(context.Context, string, entity.Patch) (*entity.UpdateResult, error)
	})
	if !ok {
		t.Fatalf("elevated mutator (%T) does not expose PatchEntity", mgr.Elevated())
	}

	if _, err := elevated.PatchEntity(context.Background(), "TASK-1", entity.Patch{
		Properties: map[string]any{"salary": "999"},
	}); err != nil {
		t.Fatalf("elevated PatchEntity: %v", err)
	}
	if gate.calls != 0 {
		t.Errorf("field gate consulted %d times under elevation, want 0 (elevation is total)", gate.calls)
	}
	if got := mustGet(t, st, "TASK-1").Properties["salary"]; got != "999" {
		t.Errorf("salary = %v, want the elevated write to land", got)
	}
}

// TestPatchEntity_LockedEntityRefused pins RR-0QWLRC. A git-crypt-locked
// entity reads as a shell; merging onto it and saving would write the
// cleartext shell over the ciphertext — the same erasure this primitive
// exists to prevent, arriving via encryption instead of redaction.
func TestPatchEntity_LockedEntityRefused(t *testing.T) {
	t.Parallel()
	mgr, st := newPatchManager(t, nil)

	locked := entity.New("TASK-LOCKED", "task")
	locked.Properties = map[string]any{"title": "Locked"}
	locked.Inaccessible = []entity.InaccessibleField{
		{Name: "salary", Reason: entity.InaccessibleReasonGitCrypt},
	}
	if err := st.CreateEntity(context.Background(), locked); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := mgr.PatchEntity(context.Background(), "TASK-LOCKED", entity.Patch{
		Properties: map[string]any{"title": "Overwritten"},
	})
	if err == nil {
		t.Fatal("expected PatchEntity to refuse a locked entity")
	}
	if !strings.Contains(err.Error(), "inaccessible") {
		t.Errorf("err = %v, want it to name the inaccessible fields", err)
	}
	if got := mustGet(t, st, "TASK-LOCKED").Properties["title"]; got != "Locked" {
		t.Errorf("title = %v, want the locked entity untouched", got)
	}
}
