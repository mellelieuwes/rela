package entitymanager_test

import (
	"context"
	"errors"
	"reflect"
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

// fakeTwins owns fields per entity id; err makes every lookup fail.
type fakeTwins struct {
	owned map[string]map[string][]string
	err   error
}

func (f fakeTwins) OwnedFields(_ context.Context, _, entityID string) (map[string][]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.owned[entityID], nil
}

// twinFixture: TASK-1 is twinned (status owned by basecamp, unless a case
// overrides), TASK-2 has no twin.
func newTwinManager(
	t *testing.T, twins entitymanager.TwinOwnership, autos []automation.Automation, scripts autocascade.ScriptRunner,
) (*entitymanager.Manager, store.Store) {
	t.Helper()
	meta, err := metamodel.Parse([]byte(patchMetamodelYAML))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	st := memstore.New()
	deps := entitymanager.Deps{
		Store:        st,
		Meta:         meta,
		Templater:    nopTemplater{},
		Audit:        audit.Nop{},
		ACL:          acl.NopACL{},
		Transitions:  statemachine.EmptySet(),
		FieldGate:    entitymanager.AllowAllFieldGate{},
		Twins:        twins,
		ScriptRunner: scripts,
	}
	if autos != nil {
		deps.Automations = automation.NewEngine(autos)
		runner, rerr := autocascade.New(autocascade.Deps{Engine: deps.Automations})
		if rerr != nil {
			t.Fatalf("autocascade.New: %v", rerr)
		}
		deps.Cascade = runner
	}
	mgr, err := entitymanager.New(deps)
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	for _, id := range []string{"TASK-1", "TASK-2"} {
		seedTask(t, st, id, map[string]any{"title": "Title", "status": "open", "notes": "n"}, "Body")
	}
	return mgr, st
}

// writer is the write surface every handle under test exposes.
type writer interface {
	PatchEntity(ctx context.Context, id string, p entity.Patch) (*entity.UpdateResult, error)
	UpdateEntity(ctx context.Context, e *entity.Entity) (*entity.UpdateResult, error)
}

type handleKind int

const (
	handleNormal handleKind = iota
	handleElevated
	handleTwinSync
)

func pick(mgr *entitymanager.Manager, h handleKind) writer {
	switch h {
	case handleElevated:
		return mgr.Elevated()
	case handleTwinSync:
		return entitymanager.TwinSyncWriter(mgr)
	default:
		return mgr
	}
}

type twinCase struct {
	name        string
	owned       map[string][]string // TASK-1's ownership
	handle      handleKind
	id          string
	patch       entity.Patch
	edit        func(e *entity.Entity) // UpdateEntity: mutate the stored entity
	wantFields  []string               // nil = the write must succeed
	wantSystems []string
}

var statusByBasecamp = map[string][]string{"status": {"basecamp"}}

func twinCases() []twinCase {
	return []twinCase{
		{
			name: "owned value changed", owned: statusByBasecamp, id: "TASK-1",
			patch:      entity.Patch{Properties: map[string]any{"status": "done"}},
			edit:       func(e *entity.Entity) { e.Properties["status"] = "done" },
			wantFields: []string{"status"}, wantSystems: []string{"basecamp"},
		},
		{
			name: "owned value unchanged", owned: statusByBasecamp, id: "TASK-1",
			patch: entity.Patch{Properties: map[string]any{"status": "open", "title": "New"}},
			edit:  func(e *entity.Entity) { e.Properties["title"] = "New" },
		},
		{
			name: "owned value removed", owned: statusByBasecamp, id: "TASK-1",
			patch:      entity.Patch{MetaUnset: []string{"status"}},
			edit:       func(e *entity.Entity) { delete(e.Properties, "status") },
			wantFields: []string{"status"}, wantSystems: []string{"basecamp"},
		},
		{
			name:  "owned body changed",
			owned: map[string][]string{"body": {"github"}}, id: "TASK-1",
			patch:      entity.Patch{Content: new("Rewritten")},
			edit:       func(e *entity.Entity) { e.Content = "Rewritten" },
			wantFields: []string{"body"}, wantSystems: []string{"github"},
		},
		{
			name:  "owned body unchanged",
			owned: map[string][]string{"body": {"github"}}, id: "TASK-1",
			patch: entity.Patch{Content: new("Body"), Properties: map[string]any{"status": "done"}},
			edit:  func(e *entity.Entity) { e.Properties["status"] = "done" },
		},
		{
			name:  "star owns properties and body",
			owned: map[string][]string{"*": {"basecamp"}, "status": {"jira"}}, id: "TASK-1",
			patch: entity.Patch{Properties: map[string]any{"status": "done"}, Content: new("x")},
			edit: func(e *entity.Entity) {
				e.Properties["status"] = "done"
				e.Content = "x"
			},
			wantFields: []string{"body", "status"}, wantSystems: []string{"basecamp", "jira"},
		},
		{
			name: "entity without a twin", owned: statusByBasecamp, id: "TASK-2",
			patch: entity.Patch{Properties: map[string]any{"status": "done"}, Content: new("x")},
			edit:  func(e *entity.Entity) { e.Properties["status"] = "done" },
		},
		{
			name: "elevated handle still rejected", owned: statusByBasecamp, id: "TASK-1", handle: handleElevated,
			patch:      entity.Patch{Properties: map[string]any{"status": "done"}},
			edit:       func(e *entity.Entity) { e.Properties["status"] = "done" },
			wantFields: []string{"status"}, wantSystems: []string{"basecamp"},
		},
		{
			name: "twin-sync handle allowed", owned: statusByBasecamp, id: "TASK-1", handle: handleTwinSync,
			patch: entity.Patch{Properties: map[string]any{"status": "done"}},
			edit:  func(e *entity.Entity) { e.Properties["status"] = "done" },
		},
	}
}

func assertTwinVerdict(t *testing.T, st store.Store, tc twinCase, before *entity.Entity, err error) {
	t.Helper()
	if tc.wantFields == nil {
		if err != nil {
			t.Fatalf("write refused: %v", err)
		}
		return
	}
	var theirs *entitymanager.TheirsWriteError
	if !errors.As(err, &theirs) {
		t.Fatalf("err = %T %v, want *TheirsWriteError", err, err)
	}
	if !reflect.DeepEqual(theirs.Fields, tc.wantFields) || !reflect.DeepEqual(theirs.Systems, tc.wantSystems) {
		t.Fatalf("fields/systems = %v/%v, want %v/%v", theirs.Fields, theirs.Systems, tc.wantFields, tc.wantSystems)
	}
	if theirs.ID != tc.id || theirs.Type != "task" {
		t.Fatalf("error names %s %s, want task %s", theirs.Type, theirs.ID, tc.id)
	}
	after := mustGet(t, st, tc.id)
	if !reflect.DeepEqual(after.Properties, before.Properties) || after.Content != before.Content {
		t.Fatalf("a refused write changed the entity: %+v", after)
	}
}

func TestPatchEntity_TwinOwnership(t *testing.T) {
	t.Parallel()
	for _, tc := range twinCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			twins := fakeTwins{owned: map[string]map[string][]string{"TASK-1": tc.owned}}
			mgr, st := newTwinManager(t, twins, nil, nil)
			before := mustGet(t, st, tc.id)
			_, err := pick(mgr, tc.handle).PatchEntity(context.Background(), tc.id, tc.patch)
			assertTwinVerdict(t, st, tc, before, err)
		})
	}
}

func TestUpdateEntity_TwinOwnership(t *testing.T) {
	t.Parallel()
	for _, tc := range twinCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			twins := fakeTwins{owned: map[string]map[string][]string{"TASK-1": tc.owned}}
			mgr, st := newTwinManager(t, twins, nil, nil)
			before := mustGet(t, st, tc.id)
			edited := before.Clone()
			tc.edit(edited)
			_, err := pick(mgr, tc.handle).UpdateEntity(context.Background(), edited)
			assertTwinVerdict(t, st, tc, before, err)
		})
	}
}

// TestTwinOwnership_LookupErrorFailsClosed: "cannot tell who owns this" must
// refuse the write, not permit it.
func TestTwinOwnership_LookupErrorFailsClosed(t *testing.T) {
	t.Parallel()
	boom := errors.New("twin store unreadable")
	mgr, st := newTwinManager(t, fakeTwins{err: boom}, nil, nil)

	_, err := mgr.PatchEntity(context.Background(), "TASK-2", entity.Patch{Properties: map[string]any{"title": "x"}})
	if !errors.Is(err, boom) {
		t.Fatalf("PatchEntity err = %v, want the lookup error", err)
	}
	edited := mustGet(t, st, "TASK-2")
	edited.Properties["title"] = "x"
	if _, err := mgr.UpdateEntity(context.Background(), edited); !errors.Is(err, boom) {
		t.Fatalf("UpdateEntity err = %v, want the lookup error", err)
	}
	if got := mustGet(t, st, "TASK-2").Properties["title"]; got != "Title" {
		t.Fatalf("title = %v, want the write refused", got)
	}
}

func TestNew_RejectsNilTwins(t *testing.T) {
	t.Parallel()
	_, err := entitymanager.New(entitymanager.Deps{
		Store: memstore.New(), Meta: parseMeta(t), Templater: nopTemplater{},
		Audit: audit.Nop{}, ACL: acl.NopACL{}, Transitions: statemachine.EmptySet(),
		FieldGate: entitymanager.AllowAllFieldGate{},
	})
	if err == nil || !strings.Contains(err.Error(), "Twins is required") {
		t.Fatalf("err = %v, want Twins is required", err)
	}
}

// TestTwinSyncWriter_DoesNotLeakIntoNestedCascade mirrors
// TestManager_Elevated_DoesNotLeakIntoNestedCascade: a sync write triggers a
// cascade, and the Mutator handed to the cascade's Lua script must NOT carry
// the twin-sync capability — a script writing an owned field is refused.
func TestTwinSyncWriter_DoesNotLeakIntoNestedCascade(t *testing.T) {
	t.Parallel()
	scripts := &recordingScripts{}
	autos := []automation.Automation{{
		Name: "fires-on-title",
		On:   automation.Trigger{Entity: []string{"task"}, Property: "title", Becomes: "Synced"},
		Do:   []automation.Action{{Lua: "-- noop"}},
	}}
	twins := fakeTwins{owned: map[string]map[string][]string{"TASK-1": {"title": {"basecamp"}, "status": {"basecamp"}}}}
	mgr, st := newTwinManager(t, twins, autos, scripts)

	if _, err := entitymanager.TwinSyncWriter(mgr).PatchEntity(context.Background(), "TASK-1",
		entity.Patch{Properties: map[string]any{"title": "Synced"}}); err != nil {
		t.Fatalf("twin-sync PatchEntity: %v", err)
	}
	if scripts.calls != 1 || scripts.mutator == nil {
		t.Fatalf("cascade script not invoked with a mutator (calls=%d)", scripts.calls)
	}

	_, err := scripts.mutator.PatchEntity(context.Background(), "TASK-1",
		entity.Patch{Properties: map[string]any{"status": "done"}})
	var theirs *entitymanager.TheirsWriteError
	if !errors.As(err, &theirs) {
		t.Fatalf("LEAK: nested-cascade write err = %v, want *TheirsWriteError — twin-sync propagated", err)
	}
	if got := mustGet(t, st, "TASK-1").Properties["status"]; got != "open" {
		t.Fatalf("status = %v, want the nested write refused", got)
	}
}

// TestTwinOwnership_AutomationSetIsNotGated: an automation `set` action is a
// system write, so it may change an owned field a caller could not.
func TestTwinOwnership_AutomationSetIsNotGated(t *testing.T) {
	t.Parallel()
	autos := []automation.Automation{{
		Name: "close-on-title",
		On:   automation.Trigger{Entity: []string{"task"}, Property: "title", Becomes: "Close"},
		Do:   []automation.Action{{Set: "status", Value: "done"}},
	}}
	twins := fakeTwins{owned: map[string]map[string][]string{"TASK-1": statusByBasecamp}}
	mgr, st := newTwinManager(t, twins, autos, nil)

	if _, err := mgr.PatchEntity(context.Background(), "TASK-1",
		entity.Patch{Properties: map[string]any{"title": "Close"}}); err != nil {
		t.Fatalf("PatchEntity: %v", err)
	}
	if got := mustGet(t, st, "TASK-1").Properties["status"]; got != "done" {
		t.Fatalf("status = %v, want the automation's set applied", got)
	}
}

// TestCopyState_TwinOwnership: a cross-entity copy into an EXISTING twinned
// target is an update, so it may not change an owned field of that target —
// refused before anything is written. An owned field the copy leaves at its
// current value passes, as for UpdateEntity.
func TestCopyState_TwinOwnership(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		owned      map[string][]string
		wantFields []string // nil = the copy must succeed
	}{
		{name: "owned field changed", owned: map[string][]string{"title": {"basecamp"}}, wantFields: []string{"title"}},
		{name: "every field owned", owned: map[string][]string{"*": {"basecamp"}}, wantFields: []string{"title"}},
		{name: "owned field unchanged", owned: map[string][]string{"secret": {"basecamp"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			meta, err := metamodel.Parse([]byte(copyMetaYAML))
			if err != nil {
				t.Fatalf("metamodel.Parse: %v", err)
			}
			st := memstore.New()
			mgr, err := entitymanager.New(entitymanager.Deps{
				Store: st, Meta: meta, Templater: nopTemplater{}, Audit: audit.Nop{}, ACL: acl.NopACL{},
				Transitions: statemachine.EmptySet(), FieldGate: entitymanager.AllowAllFieldGate{},
				Twins: fakeTwins{owned: map[string]map[string][]string{"TKT-2": tc.owned}},
			})
			if err != nil {
				t.Fatalf("entitymanager.New: %v", err)
			}
			ctx := context.Background()
			for id, title := range map[string]string{"TKT-1": "Source", "TKT-2": "Mine"} {
				if serr := st.CreateEntity(ctx, &entity.Entity{
					ID: id, Type: "ticket", Properties: map[string]any{"title": title, "secret": "s"},
				}); serr != nil {
					t.Fatalf("seed %s: %v", id, serr)
				}
			}
			before := mustGet(t, st, "TKT-2")

			_, err = mgr.CopyState(ctx, entitymanager.CopyRequest{
				Definition: "spawn-followup", SourceID: "TKT-1", TargetID: "TKT-2",
			})
			if tc.wantFields == nil {
				if err != nil {
					t.Fatalf("CopyState: %v", err)
				}
				if got := mustGet(t, st, "TKT-2").Properties["title"]; got != "Follow-up: Source" {
					t.Fatalf("title = %v, want the copy applied", got)
				}
				return
			}
			var owned *entitymanager.TheirsWriteError
			if !errors.As(err, &owned) {
				t.Fatalf("err = %v, want *TheirsWriteError", err)
			}
			if !reflect.DeepEqual(owned.Fields, tc.wantFields) {
				t.Errorf("Fields = %v, want %v", owned.Fields, tc.wantFields)
			}
			if after := mustGet(t, st, "TKT-2"); !reflect.DeepEqual(after.Properties, before.Properties) {
				t.Errorf("target changed despite the refusal: %v -> %v", before.Properties, after.Properties)
			}
		})
	}
}

func TestTheirsWriteError_Message(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  entitymanager.TheirsWriteError
		want string
	}{
		{
			name: "one field",
			err:  entitymanager.TheirsWriteError{Type: "scenario", ID: "SC-015", Fields: []string{"status"}, Systems: []string{"basecamp"}},
			want: "field status of SC-015 is owned by basecamp (Twin); change it there",
		},
		{
			name: "several fields",
			err: entitymanager.TheirsWriteError{
				Type: "scenario", ID: "SC-015", Fields: []string{"body", "status"}, Systems: []string{"basecamp", "jira"},
			},
			want: "fields body, status of SC-015 are owned by basecamp, jira (Twin); change it there",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}
