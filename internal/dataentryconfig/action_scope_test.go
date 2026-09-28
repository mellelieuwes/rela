package dataentryconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Sourcehaven-BV/rela/internal/metamodel"
)

// actionScopeMeta declares a faced `document` type and a faceless `note`.
func actionScopeMeta() *metamodel.Metamodel {
	return &metamodel.Metamodel{Entities: map[string]metamodel.EntityDef{
		"document": {Faces: map[string]metamodel.FaceDef{"concept": {}, "approved": {}}},
		"note":     {},
	}}
}

// TestValidateActions_AvailableOn pins the load-time rules for a detail-page
// action (TKT-VVS16W).
func TestValidateActions_AvailableOn(t *testing.T) {
	tests := []struct {
		name    string
		action  Action
		wantErr string
	}{
		{
			name: "types and declared faces",
			action: Action{Script: "r.lua", AvailableOn: &ActionScope{
				EntityTypes: []string{"document"}, Faces: []string{"concept"},
			}},
		},
		{
			name:   "faces omitted on a faceless type",
			action: Action{Script: "r.lua", AvailableOn: &ActionScope{EntityTypes: []string{"note"}}},
		},
		{
			name:    "no entity types",
			action:  Action{Script: "r.lua", AvailableOn: &ActionScope{}},
			wantErr: "needs at least one entity type",
		},
		{
			name:    "unknown entity type",
			action:  Action{Script: "r.lua", AvailableOn: &ActionScope{EntityTypes: []string{"nope"}}},
			wantErr: `unknown entity type "nope"`,
		},
		{
			name: "undeclared face",
			action: Action{Script: "r.lua", AvailableOn: &ActionScope{
				EntityTypes: []string{"document"}, Faces: []string{"draft"},
			}},
			wantErr: `face "draft" is not declared on entity type "document"`,
		},
		{
			name: "face on a faceless type",
			action: Action{Script: "r.lua", AvailableOn: &ActionScope{
				EntityTypes: []string{"document", "note"}, Faces: []string{"concept"},
			}},
			wantErr: `face "concept" is not declared on entity type "note"`,
		},
		{
			name: "set action cannot be offered on a detail page",
			action: Action{Set: map[string]string{"status": "done"}, AvailableOn: &ActionScope{
				EntityTypes: []string{"note"},
			}},
			wantErr: "has available_on but no script",
		},
		{
			name:    "when without available_on",
			action:  Action{Script: "r.lua", When: "entity.kind == 'soa'"},
			wantErr: "has when but no available_on",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Actions: map[string]Action{"regen": tc.action}}
			errs := validateActions(cfg, actionScopeMeta())
			joined := strings.Join(errs, "\n")
			if tc.wantErr == "" {
				if len(errs) > 0 {
					t.Fatalf("expected no errors, got: %s", joined)
				}
				return
			}
			if !strings.Contains(joined, tc.wantErr) {
				t.Fatalf("expected an error containing %q, got: %s", tc.wantErr, joined)
			}
		})
	}
}

// TestValidate_EntityBoundActionReferences refuses every surface that cannot
// send the entity address an action with available_on requires (RR-2ITHME).
func TestValidate_EntityBoundActionReferences(t *testing.T) {
	bound := Action{
		Script: "r.lua", Label: "Regenerate", Key: "g",
		AvailableOn: &ActionScope{EntityTypes: []string{"note"}},
	}
	tests := []struct {
		name  string
		check func(cfg *Config) []string
		cfg   *Config
	}{
		{
			name:  "list",
			cfg:   &Config{Lists: map[string]List{"notes": {EntityType: "note", Actions: []string{"regen"}}}},
			check: func(cfg *Config) []string { return validateActions(cfg, actionScopeMeta()) },
		},
		{
			name: "navigation entry",
			cfg:  &Config{},
			check: func(cfg *Config) []string {
				return validateNavEntry(NavigationEntry{Label: "Regen", Action: "regen"}, cfg)
			},
		},
		{
			name: "next-action offer",
			cfg:  &Config{},
			check: func(cfg *Config) []string {
				return validateNextActionOffer("next_actions[x]", 0, NextActionOffer{Action: "regen"}, cfg)
			},
		},
		{
			name: "next-action pick_one",
			cfg:  &Config{},
			check: func(cfg *Config) []string {
				return validatePickOne("next_actions[x]", NextActionPickOne{Query: "type:note", Action: "regen"}, cfg)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.cfg.Actions = map[string]Action{"regen": bound}
			joined := strings.Join(tc.check(tc.cfg), "\n")
			if !strings.Contains(joined, `references action "regen", which has available_on`) {
				t.Fatalf("expected an entity-bound reference error, got: %s", joined)
			}
		})
	}
}

// TestActionConfirm_Shapes pins that `confirm:` accepts a boolean or the
// dialog text, and serves the text when there is one.
func TestActionConfirm_Shapes(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		want     ActionConfirm
		wantJSON string
	}{
		{name: "absent", yaml: `script: a.lua`, want: ActionConfirm{}, wantJSON: `{"script":"a.lua"}`},
		{name: "true", yaml: "script: a.lua\nconfirm: true", want: ActionConfirm{Enabled: true},
			wantJSON: `{"script":"a.lua","confirm":true}`},
		{name: "false", yaml: "script: a.lua\nconfirm: false", want: ActionConfirm{},
			wantJSON: `{"script":"a.lua"}`},
		{name: "text", yaml: "script: a.lua\nconfirm: \"Overwrite the concept?\"",
			want:     ActionConfirm{Enabled: true, Text: "Overwrite the concept?"},
			wantJSON: `{"script":"a.lua","confirm":"Overwrite the concept?"}`},
		{name: "quoted true stays text", yaml: "script: a.lua\nconfirm: \"true\"",
			want:     ActionConfirm{Enabled: true, Text: "true"},
			wantJSON: `{"script":"a.lua","confirm":"true"}`},
		{name: "quoted yes stays text", yaml: "script: a.lua\nconfirm: 'yes'",
			want:     ActionConfirm{Enabled: true, Text: "yes"},
			wantJSON: `{"script":"a.lua","confirm":"yes"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var a Action
			if err := yaml.Unmarshal([]byte(tc.yaml), &a); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if a.Confirm != tc.want {
				t.Errorf("confirm = %+v, want %+v", a.Confirm, tc.want)
			}
			got, err := json.Marshal(a)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.wantJSON {
				t.Errorf("json = %s, want %s", got, tc.wantJSON)
			}
		})
	}
}

// TestActionConfirm_RejectsOtherShapes refuses a list or map, which is
// neither of the two documented forms.
func TestActionConfirm_RejectsOtherShapes(t *testing.T) {
	tests := []struct{ name, value, wantErr string }{
		{"list", "[yes]", "confirm must be a boolean or a string"},
		{"number", "1", "confirm must be a boolean or a string"},
		{"unquoted yes", "yes", "is not a boolean"},
		{"unquoted Off", "Off", "is not a boolean"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var a Action
			err := yaml.Unmarshal([]byte("script: a.lua\nconfirm: "+tc.value), &a)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestAction_DetailKeysNotServed pins that the visibility inputs stay off the
// wire: the server publishes the verdict, not the rule (TKT-VVS16W).
func TestAction_DetailKeysNotServed(t *testing.T) {
	a := Action{
		Script: "r.lua", When: "entity.kind == 'soa'", Permission: "documents:regenerate",
		AvailableOn: &ActionScope{EntityTypes: []string{"document"}},
	}
	got, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"available_on", "when", "permission", "AvailableOn", "When", "Permission"} {
		if strings.Contains(string(got), key) {
			t.Errorf("wire carries %q: %s", key, got)
		}
	}
}

func TestActionConditionID_RoundTrips(t *testing.T) {
	action, et, ok := SplitActionConditionID(ActionConditionID("regenerate-soa", "document"))
	if !ok || action != "regenerate-soa" || et != "document" {
		t.Fatalf("got (%q, %q, %v)", action, et, ok)
	}
}
