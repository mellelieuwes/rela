package dataentryconfig

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Sourcehaven-BV/rela/internal/metamodel"
)

// TestValidTopLevelKeysMatchConfigStruct guards the same drift class as
// BUG-5XIN07 (the metamodel loader's whitelist), applied to data-entry.yaml:
// validTopLevelKeys is a hand-maintained duplicate of the Config struct's
// top-level yaml tags, so a newly-added field could be silently rejected by
// checkUnknownKeys. This asserts every top-level yaml tag on Config is
// whitelisted, so such a field fails this test before it can reach a user.
func TestValidTopLevelKeysMatchConfigStruct(t *testing.T) {
	for field := range reflect.TypeFor[Config]().Fields() {
		tag := field.Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue // computed / non-YAML field
		}
		key := strings.Split(tag, ",")[0]
		if key == "" {
			continue
		}
		if !validTopLevelKeys[key] {
			t.Errorf("Config field %q (yaml:%q) is not in validTopLevelKeys — "+
				"checkUnknownKeys will reject a config that uses it", field.Name, key)
		}
	}
}

// testMetamodel returns a metamodel for testing
func testMetamodel() *metamodel.Metamodel {
	m := &metamodel.Metamodel{
		Version: "1.0",
		Types: map[string]metamodel.CustomType{
			"status": {
				Values:  []string{"open", "in-progress", "closed"},
				Default: "open",
			},
			"priority": {
				Values: []string{"high", "medium", "low"},
			},
		},
		Entities: map[string]metamodel.EntityDef{
			"ticket": {
				Label:    "Ticket",
				IDPrefix: "TKT-",
				Properties: map[string]metamodel.PropertyDef{
					"title":    {Type: "string", Required: true},
					"status":   {Type: "status"},
					"priority": {Type: "priority"},
					"assignee": {Type: "string"},
				},
			},
			"category": {
				Label:    "Category",
				IDPrefix: "CAT-",
				Properties: map[string]metamodel.PropertyDef{
					"name":        {Type: "string", Required: true},
					"description": {Type: "string"},
				},
			},
		},
		Relations: map[string]metamodel.RelationDef{
			"belongs-to": {
				Label: "belongs to",
				From:  []string{"ticket"},
				To:    []string{"category"},
			},
			// A heterogeneous relation, so tests can cover the case a
			// third of rela's real relations are in: several `to:` types.
			"relates-to": {
				Label: "relates to",
				From:  []string{"ticket"},
				To:    []string{"ticket", "category"},
			},
			"blocks": {
				Label: "blocks",
				From:  []string{"ticket"},
				To:    []string{"ticket"},
			},
		},
	}
	return m
}

func TestValidateConfig_ValidConfig(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Version: "1.0",
		App:     AppConfig{Name: "Test App"},
		Forms: map[string]Form{
			"create_ticket": {
				EntityType: "ticket",
				Title:      "New Ticket",
				Fields: []FormField{
					{Property: "title"},
					{Property: "status"},
				},
				Relations: []FormRelation{
					{Relation: "belongs-to", Direction: "outgoing"},
				},
			},
		},
		Lists: map[string]List{
			"all_tickets": {
				EntityType: "ticket",
				Title:      "All Tickets",
				Columns: []ListColumn{
					{Property: "title"},
					{Property: "status"},
				},
				CreateForm: "create_ticket",
				DetailView: "ticket_view",
			},
		},
		Views: map[string]ViewConfig{
			"ticket_view": {
				Title: "Ticket View",
				Entry: ViewEntry{Type: "ticket"},
				Traverse: []ViewTraverse{
					{From: "entry", Follow: "blocks", CollectAs: "blocked"},
				},
				Sections: []ViewSection{
					{Source: "entry", Display: "properties"},
					{Source: "blocked", Display: "table"},
				},
			},
		},
		Navigation: []NavigationEntry{
			{Label: "Tickets", List: "all_tickets"},
		},
	}

	data := []byte(`version: "1.0"`)

	err := ValidateConfig(data, cfg, meta)
	if err != nil {
		t.Errorf("expected valid config to pass, got error: %v", err)
	}
}

func TestValidateConfig_UnknownTopLevelKey(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{}

	data := []byte(`
version: "1.0"
form:
  test: {}
`)

	err := ValidateConfig(data, cfg, meta)
	if err == nil {
		t.Fatal("expected error for unknown key 'form'")
	}
	if !strings.Contains(err.Error(), `unknown key "form"`) {
		t.Errorf("expected error about unknown key, got: %v", err)
	}
	if !strings.Contains(err.Error(), `did you mean "forms"`) {
		t.Errorf("expected suggestion for 'forms', got: %v", err)
	}
}

func TestValidateConfig_UnknownFormEntityType(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Forms: map[string]Form{
			"test": {EntityType: "unknown_type"},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for unknown entity type")
	}
	if !strings.Contains(err.Error(), `form "test": unknown entity type "unknown_type"`) {
		t.Errorf("expected error about unknown entity type, got: %v", err)
	}
}

func TestValidateConfig_UnknownFormFieldProperty(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Forms: map[string]Form{
			"test": {
				EntityType: "ticket",
				Fields:     []FormField{{Property: "unknown_prop"}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for unknown property")
	}
	if !strings.Contains(err.Error(), `property "unknown_prop" not in metamodel`) {
		t.Errorf("expected error about unknown property, got: %v", err)
	}
}

// BUG-FB0LN8: clear_when_hidden is allowlist-validated so a typo cannot
// silently resolve to a destructive default.
func TestValidateConfig_ClearWhenHiddenAllowlist(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "no", value: "no"},
		{name: "yes", value: "yes"},
		// TKT-7S5735 landed the propose/commit seam, so "confirm" is now a
		// real policy rather than a promise. The typo cases below still
		// matter: "confrim" must never silently resolve to a destructive
		// default.
		{name: "confirm", value: "confirm"},
		{name: "empty means default", value: ""},
		{name: "off is rejected", value: "off", wantErr: true},
		{name: "false is rejected", value: "false", wantErr: true},
		{name: "true is rejected", value: "true", wantErr: true},
		{name: "typo is rejected", value: "confrim", wantErr: true},
		{name: "never is rejected", value: "never", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Forms: map[string]Form{
					"test": {
						EntityType: "ticket",
						Fields: []FormField{{
							Property:        "assignee",
							VisibleWhen:     "form.status == 'open'",
							ClearWhenHidden: tc.value,
						}},
					},
				},
			}
			err := ValidateConfig([]byte(`version: "1.0"`), cfg, testMetamodel())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for clear_when_hidden %q", tc.value)
				}
				if !strings.Contains(err.Error(), "invalid clear_when_hidden") {
					t.Errorf("expected allowlist error, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Errorf("clear_when_hidden %q should be valid, got: %v", tc.value, err)
			}
		})
	}
}

// The checks must reach STEP-NESTED fields too — a wizard step hiding is the
// motivating case for clear_when_hidden, so validating only top-level
// form.Fields would miss exactly the configs most likely to use it.
func TestValidateConfig_ClearWhenHiddenValidatedInWizardSteps(t *testing.T) {
	cfg := &Config{
		Forms: map[string]Form{
			"test": {
				EntityType: "ticket",
				Steps: []FormStep{{
					Title: "Assignment",
					Fields: []FormField{{
						Property:        "assignee",
						VisibleWhen:     "form.status == 'open'",
						ClearWhenHidden: "off", // invalid
					}},
				}},
			},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, testMetamodel())
	if err == nil {
		t.Fatal("expected error for invalid clear_when_hidden on a step field")
	}
	if !strings.Contains(err.Error(), "invalid clear_when_hidden") {
		t.Errorf("expected allowlist error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "step[0]") {
		t.Errorf("expected the error to locate the step, got: %v", err)
	}
}

// clear_when_hidden without visible_when can never fire — flag it as a
// config mistake rather than letting it sit there looking meaningful.
func TestValidateConfig_ClearWhenHiddenRequiresVisibleWhen(t *testing.T) {
	cfg := &Config{
		Forms: map[string]Form{
			"test": {
				EntityType: "ticket",
				Fields:     []FormField{{Property: "assignee", ClearWhenHidden: "yes"}},
			},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, testMetamodel())
	if err == nil {
		t.Fatal("expected error for clear_when_hidden without visible_when")
	}
	if !strings.Contains(err.Error(), "neither it nor its step has a visible_when") {
		t.Errorf("expected without-visible_when error, got: %v", err)
	}
}

// A field on a CONDITIONAL STEP can be hidden without a visible_when of its
// own — the step hides it. clear_when_hidden is meaningful there, so the
// "would never apply" check must not fire. Caught while building the demo
// project: the naive rule rejected a perfectly valid wizard config.
func TestValidateConfig_ClearWhenHiddenInheritsStepVisibility(t *testing.T) {
	cfg := &Config{
		Forms: map[string]Form{
			"test": {
				EntityType: "ticket",
				Steps: []FormStep{{
					Title:       "Assignment",
					VisibleWhen: "form.status == 'open'", // the step hides the field
					Fields: []FormField{{
						Property:        "assignee",
						ClearWhenHidden: "yes", // no field-level visible_when
					}},
				}},
			},
		},
	}
	if err := ValidateConfig([]byte(`version: "1.0"`), cfg, testMetamodel()); err != nil {
		t.Errorf("a field on a conditional step may set clear_when_hidden, got: %v", err)
	}
}

// ...but a field on an UNCONDITIONAL step still can't: nothing would ever hide it.
func TestValidateConfig_ClearWhenHiddenOnUnconditionalStepRejected(t *testing.T) {
	cfg := &Config{
		Forms: map[string]Form{
			"test": {
				EntityType: "ticket",
				Steps: []FormStep{{
					Title: "Assignment", // no visible_when
					Fields: []FormField{{
						Property:        "assignee",
						ClearWhenHidden: "yes",
					}},
				}},
			},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, testMetamodel())
	if err == nil {
		t.Fatal("expected error: neither the field nor its step can hide it")
	}
	if !strings.Contains(err.Error(), "neither it nor its step has a visible_when") {
		t.Errorf("expected without-visible_when error, got: %v", err)
	}
}

// The YAML footgun: bare `no`/`yes` are booleans under YAML 1.1. yaml.v3
// uses the 1.2 core schema and the field is typed string, so they decode as
// the literal text and land in the allowlist as written. Pinned because a
// regression here would turn `clear_when_hidden: no` into the empty string —
// which still means "no" today, but would silently drift if the default ever
// changed.
func TestFormField_ClearWhenHiddenYAMLScalars(t *testing.T) {
	tests := []struct{ in, want string }{
		{"clear_when_hidden: no", "no"},
		{"clear_when_hidden: yes", "yes"},
		{`clear_when_hidden: "no"`, "no"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			var f FormField
			if err := yaml.Unmarshal([]byte(tc.in), &f); err != nil {
				t.Fatalf("unmarshal %q: %v", tc.in, err)
			}
			if f.ClearWhenHidden != tc.want {
				t.Errorf("got %q, want %q", f.ClearWhenHidden, tc.want)
			}
			if !ValidClearWhenHidden[f.ClearWhenHidden] {
				t.Errorf("%q decoded to %q which is not in the allowlist", tc.in, f.ClearWhenHidden)
			}
		})
	}
}

func TestValidateConfig_InvalidTransitions(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Forms: map[string]Form{
			"test": {
				EntityType: "ticket",
				Fields: []FormField{{
					Property: "status",
					Transitions: map[string][]string{
						"invalid_state": {"open"},
						"open":          {"invalid_target"},
					},
				}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for invalid transitions")
	}
	if !strings.Contains(err.Error(), `invalid from-state "invalid_state"`) {
		t.Errorf("expected error about invalid from-state, got: %v", err)
	}
	if !strings.Contains(err.Error(), `invalid to-state "invalid_target"`) {
		t.Errorf("expected error about invalid to-state, got: %v", err)
	}
}

func TestValidateConfig_UnknownFormRelation(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Forms: map[string]Form{
			"test": {
				EntityType: "ticket",
				Relations:  []FormRelation{{Relation: "unknown_rel"}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for unknown relation")
	}
	if !strings.Contains(err.Error(), `unknown relation "unknown_rel"`) {
		t.Errorf("expected error about unknown relation, got: %v", err)
	}
}

// inverseTestMetamodel parses a metamodel via the real loader so
// inverseOwners is populated — needed for tests that exercise the
// inverse-name and wrong-side branches in validateForms.
func inverseTestMetamodel(t *testing.T) *metamodel.Metamodel {
	t.Helper()
	yaml := `version: "1.0"
types:
  entity_status:
    values: [open, closed]
    default: open
entities:
  from_entity:
    label: From
    id_prefix: "FROM-"
    properties:
      title:
        type: string
        required: true
      status:
        type: entity_status
  to_entity:
    label: To
    id_prefix: "TO-"
    properties:
      title:
        type: string
        required: true
      status:
        type: entity_status
  other_entity:
    label: Other
    id_prefix: "OTH-"
    properties:
      title:
        type: string
        required: true
relations:
  connects_to:
    label: connects to
    from: [from_entity]
    to: [to_entity]
    inverse: connects_from
  multi_source:
    label: multi source
    from: [from_entity, other_entity]
    to: [to_entity]
  depends_on:
    label: depends on
    from: [from_entity]
    to: [from_entity]
`
	m, err := metamodel.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse inverse test metamodel: %v", err)
	}
	return m
}

func TestValidateConfig_FormRelationInverseName(t *testing.T) {
	meta := inverseTestMetamodel(t)
	cfg := &Config{
		Forms: map[string]Form{
			"edit_to": {
				EntityType: "to_entity",
				Relations:  []FormRelation{{Relation: "connects_from"}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for inverse-name relation")
	}
	msg := err.Error()
	// Error must identify the inverse name and point the user at the
	// canonical relation plus direction: incoming.
	if !strings.Contains(msg, `"connects_from"`) {
		t.Errorf("expected error to mention inverse name, got: %v", err)
	}
	if !strings.Contains(msg, `"connects_to"`) {
		t.Errorf("expected error to point at canonical name, got: %v", err)
	}
	if !strings.Contains(msg, "direction: incoming") {
		t.Errorf("expected error to mention direction: incoming, got: %v", err)
	}
}

func TestValidateConfig_FormRelationToSide_InfersIncoming(t *testing.T) {
	meta := inverseTestMetamodel(t)
	// to_entity is ONLY on the TO side of connects_to, so an absent direction
	// has exactly one correct answer and is inferred as incoming rather than
	// defaulting to outgoing and being reported as wrong-side.
	cfg := &Config{
		Forms: map[string]Form{
			"edit_to": {
				EntityType: "to_entity",
				Relations:  []FormRelation{{Relation: "connects_to"}},
			},
		},
	}

	if err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta); err != nil {
		t.Fatalf("expected an unambiguous to-side binding to infer incoming, got: %v", err)
	}
}

func TestValidateConfig_FormRelationWrongSide_HintsWhenDirectionExplicit(t *testing.T) {
	meta := inverseTestMetamodel(t)
	// Explicit outgoing with the form entity on the TO side. The author wrote a
	// direction and picked the wrong one, so flipping it is the actionable fix
	// and the message says so. (An ABSENT direction cannot reach here for this
	// shape — inference would have resolved it to incoming.)
	cfg := &Config{
		Forms: map[string]Form{
			"edit_to": {
				EntityType: "to_entity",
				Relations:  []FormRelation{{Relation: "connects_to", Direction: DirectionOutgoing}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for form entity not on the source side")
	}
	msg := err.Error()
	if !strings.Contains(msg, `"to_entity"`) {
		t.Errorf("expected error to mention form entity type, got: %v", err)
	}
	if !strings.Contains(msg, "direction: incoming") {
		t.Errorf("expected hint to suggest flipping to incoming, got: %v", err)
	}
}

func TestValidateConfig_FormRelationIncomingWrongSide(t *testing.T) {
	meta := inverseTestMetamodel(t)
	// from_entity is the SOURCE side; declaring direction: incoming on it
	// for connects_to is a configuration error.
	cfg := &Config{
		Forms: map[string]Form{
			"edit_from": {
				EntityType: "from_entity",
				Relations:  []FormRelation{{Relation: "connects_to", Direction: DirectionIncoming}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for direction: incoming on the source side")
	}
	if !strings.Contains(err.Error(), `"from_entity"`) {
		t.Errorf("expected error to mention form entity type, got: %v", err)
	}
}

func TestValidateConfig_FormRelationCorrectSide(t *testing.T) {
	meta := inverseTestMetamodel(t)
	// from_entity → connects_to (outgoing default) is the correct shape.
	// to_entity   → connects_to with direction: incoming is also correct.
	cfg := &Config{
		Forms: map[string]Form{
			"edit_from": {
				EntityType: "from_entity",
				Relations:  []FormRelation{{Relation: "connects_to"}},
			},
			"edit_to": {
				EntityType: "to_entity",
				Relations:  []FormRelation{{Relation: "connects_to", Direction: DirectionIncoming}},
			},
		},
	}

	if err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta); err != nil {
		t.Errorf("expected valid config to pass, got: %v", err)
	}
}

func TestValidateConfig_FormRelationMultiSourceSide(t *testing.T) {
	meta := inverseTestMetamodel(t)
	// multi_source allows from_entity OR other_entity as the source.
	// Both should pass when used as the form entity with outgoing default.
	cfg := &Config{
		Forms: map[string]Form{
			"edit_from": {
				EntityType: "from_entity",
				Relations:  []FormRelation{{Relation: "multi_source"}},
			},
			"edit_other": {
				EntityType: "other_entity",
				Relations:  []FormRelation{{Relation: "multi_source"}},
			},
		},
	}

	if err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta); err != nil {
		t.Errorf("expected valid config with multi-type source to pass, got: %v", err)
	}
}

func TestValidateConfig_InvalidRelationDirection(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Forms: map[string]Form{
			"test": {
				EntityType: "ticket",
				Relations:  []FormRelation{{Relation: "blocks", Direction: "both"}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for invalid direction")
	}
	if !strings.Contains(err.Error(), `invalid direction "both"`) {
		t.Errorf("expected error about invalid direction, got: %v", err)
	}
}

func TestValidateConfig_InvalidRelationWidget(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Forms: map[string]Form{
			"test": {
				EntityType: "ticket",
				Relations:  []FormRelation{{Relation: "blocks", Widget: "banana"}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for invalid widget")
	}
	if !strings.Contains(err.Error(), `invalid widget "banana"`) {
		t.Errorf("expected error about invalid widget, got: %v", err)
	}
}

func TestValidateConfig_WizardStepsValid(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Forms: map[string]Form{
			"wizard": {
				EntityType: "ticket",
				Steps: []FormStep{
					{Title: "Basics", Fields: []FormField{{Property: "title"}}},
					{
						Title:       "Details",
						VisibleWhen: "form.status == 'open'",
						Fields:      []FormField{{Property: "priority", RequiredWhen: "form.status == 'open'"}},
						Relations:   []FormRelation{{Relation: "blocks", Direction: "outgoing", Widget: "select"}},
					},
				},
			},
		},
	}

	if err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta); err != nil {
		t.Fatalf("expected valid wizard form, got: %v", err)
	}
}

func TestValidateConfig_WizardStepsAndFlatMutuallyExclusive(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Forms: map[string]Form{
			"mixed": {
				EntityType: "ticket",
				Fields:     []FormField{{Property: "title"}},
				Steps:      []FormStep{{Title: "Step 1", Fields: []FormField{{Property: "status"}}}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error when both fields and steps are set")
	}
	if !strings.Contains(err.Error(), "either top-level fields/relations OR steps") {
		t.Errorf("expected steps-xor-flat error, got: %v", err)
	}
}

func TestValidateConfig_WizardStepUnknownProperty(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Forms: map[string]Form{
			"wizard": {
				EntityType: "ticket",
				Steps: []FormStep{
					{Title: "Step 1", Fields: []FormField{{Property: "unknown_prop"}}},
				},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for unknown property in a step")
	}
	if !strings.Contains(err.Error(), `step[0] field[0] property "unknown_prop" not in metamodel`) {
		t.Errorf("expected step-scoped unknown-property error, got: %v", err)
	}
}

func TestValidateConfig_WizardStepMissingTitle(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Forms: map[string]Form{
			"wizard": {
				EntityType: "ticket",
				Steps:      []FormStep{{Fields: []FormField{{Property: "title"}}}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for step without title")
	}
	if !strings.Contains(err.Error(), "step[0] has no title") {
		t.Errorf("expected missing-title error, got: %v", err)
	}
}

func TestValidateConfig_UnknownListEntityType(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Lists: map[string]List{
			"test": {EntityType: "unknown_type"},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for unknown entity type")
	}
	if !strings.Contains(err.Error(), `list "test": unknown entity type "unknown_type"`) {
		t.Errorf("expected error about unknown entity type, got: %v", err)
	}
}

// TestValidateConfig_ListExportRenderShape covers the shape half of
// export_render validation. Existence on disk is checked at app construction,
// where the project root is known.
func TestValidateConfig_ListExportRenderShape(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		wantErr string
	}{
		{
			name:    "non-lua extension rejected",
			script:  "docs/report.sh",
			wantErr: `list "test": export_render must be a .lua script path`,
		},
		{
			name:    "traversal rejected",
			script:  "../../etc/evil.lua",
			wantErr: `list "test": export_render must be a local path`,
		},
		{
			name:   "valid script accepted",
			script: "docs/report.lua",
		},
		{
			name:   "empty means built-in table",
			script: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			meta := testMetamodel()
			cfg := &Config{
				Lists: map[string]List{
					"test": {EntityType: "ticket", ExportRender: tc.script},
				},
			}

			err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error for export_render %q", tc.script)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("want error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

// TestValidateConfig_ListExportRenderWithoutColumns pins that a list carrying
// only an export_render (no columns) is valid — the script renders whatever it
// likes, so requiring columns would be wrong.
func TestValidateConfig_ListExportRenderWithoutColumns(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Lists: map[string]List{
			"test": {EntityType: "ticket", ExportRender: "docs/report.lua"},
		},
	}

	if err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta); err != nil {
		t.Fatalf("a columns-less list with export_render must be valid, got: %v", err)
	}
}

func TestValidateConfig_UnknownListColumnProperty(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Lists: map[string]List{
			"test": {
				EntityType: "ticket",
				Columns:    []ListColumn{{Property: "unknown_prop"}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for unknown column property")
	}
	if !strings.Contains(err.Error(), `column[0] property "unknown_prop" not in metamodel`) {
		t.Errorf("expected error about unknown column property, got: %v", err)
	}
}

func TestValidateConfig_UnknownListColumnRelation(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Lists: map[string]List{
			"test": {
				EntityType: "ticket",
				Columns:    []ListColumn{{Relation: "unknown_rel"}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for unknown column relation")
	}
	if !strings.Contains(err.Error(), `column[0] references unknown relation "unknown_rel"`) {
		t.Errorf("expected error about unknown column relation, got: %v", err)
	}
}

func TestValidateConfig_InvalidSortDirection(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Lists: map[string]List{
			"test": {
				EntityType: "ticket",
				Sort:       []SortSpec{{Property: "title", Direction: "up"}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for invalid sort direction")
	}
	if !strings.Contains(err.Error(), `invalid direction "up"`) {
		t.Errorf("expected error about invalid direction, got: %v", err)
	}
}

func TestValidateConfig_InvalidFilterOperator(t *testing.T) {
	meta := testMetamodel()
	// `=~` is regex syntax from rela's OTHER filter languages (search,
	// calfeed where, CLI) and was wrongly accepted here for months while
	// no layer below could evaluate it (the SPA degraded it to `eq`).
	cfg := &Config{
		Lists: map[string]List{
			"test": {
				EntityType: "ticket",
				Filters:    []FilterConfig{{Property: "status", Operator: "=~"}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for invalid filter operator")
	}
	if !strings.Contains(err.Error(), `invalid operator "=~"`) {
		t.Errorf("expected error about invalid operator, got: %v", err)
	}
}

// TestValidateConfig_DocumentedFilterOperatorsAccepted pins the full
// documented operator set (docs/data-entry.md "Static Filters") so the
// validator can't drift from the docs/SPA/API again — `in` and `~` were
// documented and evaluable but rejected here until this test existed.
func TestValidateConfig_DocumentedFilterOperatorsAccepted(t *testing.T) {
	meta := testMetamodel()
	for _, op := range []string{"=", "==", "!=", "~", "<", "<=", ">", ">=", "in"} {
		cfg := &Config{
			Lists: map[string]List{
				"test": {
					EntityType: "ticket",
					Filters:    []FilterConfig{{Property: "status", Operator: op, Value: "x"}},
				},
			},
		}
		if err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta); err != nil {
			t.Errorf("operator %q: expected valid, got: %v", op, err)
		}
	}
}

func TestValidateConfig_ListReferencesUnknownForm(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Forms: map[string]Form{
			"create_ticket": {EntityType: "ticket"},
		},
		Lists: map[string]List{
			"test": {
				EntityType: "ticket",
				CreateForm: "nonexistent_form",
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for unknown create_form")
	}
	if !strings.Contains(err.Error(), `references unknown form "nonexistent_form"`) {
		t.Errorf("expected error about unknown form, got: %v", err)
	}
}

func TestValidateConfig_ListReferencesUnknownView(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Views: map[string]ViewConfig{
			"ticket_view": {Entry: ViewEntry{Type: "ticket"}},
		},
		Lists: map[string]List{
			"test": {
				EntityType: "ticket",
				DetailView: "nonexistent_view",
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for unknown detail_view")
	}
	if !strings.Contains(err.Error(), `references unknown view "nonexistent_view"`) {
		t.Errorf("expected error about unknown view, got: %v", err)
	}
}

func TestValidateConfig_ViewUnknownEntryType(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Views: map[string]ViewConfig{
			"test": {Entry: ViewEntry{Type: "unknown_type"}},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for unknown entry type")
	}
	if !strings.Contains(err.Error(), `entry type "unknown_type" not in metamodel`) {
		t.Errorf("expected error about unknown entry type, got: %v", err)
	}
}

func TestValidateConfig_ViewsDuplicateEntryType(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Views: map[string]ViewConfig{
			"ticket_a": {Entry: ViewEntry{Type: "ticket"}},
			"ticket_b": {Entry: ViewEntry{Type: "ticket"}},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for duplicate view entry types")
	}
	msg := err.Error()
	// Both view IDs must appear so the project owner can locate them.
	if !strings.Contains(msg, "ticket_a") || !strings.Contains(msg, "ticket_b") {
		t.Errorf("expected error to list both duplicate view IDs, got: %v", err)
	}
	if !strings.Contains(msg, `entity type "ticket"`) {
		t.Errorf("expected error to name the conflicting entity type, got: %v", err)
	}
}

func TestValidateConfig_ViewTraverseUnknownCollection(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Views: map[string]ViewConfig{
			"test": {
				Entry: ViewEntry{Type: "ticket"},
				Traverse: []ViewTraverse{
					{From: "nonexistent", Follow: "blocks", CollectAs: "blocked"},
				},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for unknown collection in from")
	}
	if !strings.Contains(err.Error(), `unknown collection "nonexistent" in from`) {
		t.Errorf("expected error about unknown collection, got: %v", err)
	}
}

func TestValidateConfig_ViewTraverseUnknownRelation(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Views: map[string]ViewConfig{
			"test": {
				Entry: ViewEntry{Type: "ticket"},
				Traverse: []ViewTraverse{
					{From: "entry", Follow: "block", CollectAs: "blocked"}, // typo: block vs blocks
				},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for unknown relation")
	}
	if !strings.Contains(err.Error(), `unknown relation "block"`) {
		t.Errorf("expected error about unknown relation, got: %v", err)
	}
	if !strings.Contains(err.Error(), `did you mean "blocks"`) {
		t.Errorf("expected suggestion, got: %v", err)
	}
}

func TestValidateConfig_ViewTraverseMissingFollowOrFollowIncoming(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Views: map[string]ViewConfig{
			"test": {
				Entry: ViewEntry{Type: "ticket"},
				Traverse: []ViewTraverse{
					{From: "entry", CollectAs: "blocked"},
				},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for missing follow/follow_incoming")
	}
	if !strings.Contains(err.Error(), "must specify either follow or follow_incoming") {
		t.Errorf("expected error about missing follow, got: %v", err)
	}
}

func TestValidateConfig_ViewTraverseBothFollowAndFollowIncoming(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Views: map[string]ViewConfig{
			"test": {
				Entry: ViewEntry{Type: "ticket"},
				Traverse: []ViewTraverse{
					{From: "entry", Follow: "blocks", FollowIncoming: "blocks", CollectAs: "blocked"},
				},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for both follow and follow_incoming")
	}
	if !strings.Contains(err.Error(), "cannot specify both follow and follow_incoming") {
		t.Errorf("expected error about both follow, got: %v", err)
	}
}

func TestValidateConfig_ViewTraverseMissingCollectAs(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Views: map[string]ViewConfig{
			"test": {
				Entry: ViewEntry{Type: "ticket"},
				Traverse: []ViewTraverse{
					{From: "entry", Follow: "blocks"},
				},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for missing collect_as")
	}
	if !strings.Contains(err.Error(), "must specify collect_as") {
		t.Errorf("expected error about missing collect_as, got: %v", err)
	}
}

func TestValidateConfig_ViewSectionUnknownSource(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Views: map[string]ViewConfig{
			"test": {
				Entry: ViewEntry{Type: "ticket"},
				Sections: []ViewSection{
					{Source: "nonexistent", Display: "table"},
				},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for unknown section source")
	}
	if !strings.Contains(err.Error(), `unknown collection "nonexistent" in source`) {
		t.Errorf("expected error about unknown source, got: %v", err)
	}
}

func TestValidateConfig_ViewSectionInvalidDisplay(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Views: map[string]ViewConfig{
			"test": {
				Entry: ViewEntry{Type: "ticket"},
				Sections: []ViewSection{
					{Source: "entry", Display: "grid"},
				},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for invalid display mode")
	}
	if !strings.Contains(err.Error(), `invalid display mode "grid"`) {
		t.Errorf("expected error about invalid display, got: %v", err)
	}
}

func TestValidateConfig_ViewSectionUnknownFieldProperty(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Views: map[string]ViewConfig{
			"test": {
				Entry: ViewEntry{Type: "ticket"},
				Sections: []ViewSection{
					{Source: "entry", Display: "properties", Fields: []ViewSectionField{{Property: "unknown_prop"}}},
				},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for unknown field property")
	}
	if !strings.Contains(err.Error(), `property "unknown_prop" not in entity`) {
		t.Errorf("expected error about unknown property, got: %v", err)
	}
}

func TestValidateConfig_DashboardInvalidDisplay(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Dashboard: &DashboardConfig{
			Cards: []DashboardCard{
				{Title: "Test", Display: "pie"},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for invalid dashboard display")
	}
	if !strings.Contains(err.Error(), `invalid display mode "pie"`) {
		t.Errorf("expected error about invalid display, got: %v", err)
	}
}

func TestValidateConfig_DashboardBreakdownMissingGroupBy(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Dashboard: &DashboardConfig{
			Cards: []DashboardCard{
				{Title: "Test", Display: "breakdown"},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for breakdown without group_by")
	}
	if !strings.Contains(err.Error(), "uses breakdown display but has no group_by") {
		t.Errorf("expected error about missing group_by, got: %v", err)
	}
}

func TestValidateConfig_DashboardTableMissingColumns(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Dashboard: &DashboardConfig{
			Cards: []DashboardCard{
				{Title: "Test", Display: "table"},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for table without columns")
	}
	if !strings.Contains(err.Error(), "uses table display but has no columns") {
		t.Errorf("expected error about missing columns, got: %v", err)
	}
}

func TestValidateConfig_CommandMissingLabel(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Commands: map[string]CommandConfig{
			"test": {Script: "echo hello", Context: "global"},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for missing label")
	}
	if !strings.Contains(err.Error(), `command "test": label is required`) {
		t.Errorf("expected error about missing label, got: %v", err)
	}
}

func TestValidateConfig_CommandMissingScript(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Commands: map[string]CommandConfig{
			"test": {Label: "Test", Context: "global"},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for missing script")
	}
	if !strings.Contains(err.Error(), `command "test": script is required`) {
		t.Errorf("expected error about missing script, got: %v", err)
	}
}

func TestValidateConfig_CommandInvalidContext(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Commands: map[string]CommandConfig{
			"test": {Label: "Test", Script: "echo", Context: "anywhere"},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for invalid context")
	}
	if !strings.Contains(err.Error(), `invalid context "anywhere"`) {
		t.Errorf("expected error about invalid context, got: %v", err)
	}
}

func TestValidateConfig_CommandUnknownAvailableOnView(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Commands: map[string]CommandConfig{
			"test": {
				Label:       "Test",
				Script:      "echo",
				Context:     "view",
				AvailableOn: &CommandScope{Views: []string{"unknown_view"}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for unknown view")
	}
	if !strings.Contains(err.Error(), `references unknown view "unknown_view"`) {
		t.Errorf("expected error about unknown view, got: %v", err)
	}
}

func TestValidateConfig_StyleUnknownType(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Styles: map[string]map[string]string{
			"unknown_type": {"value": "blue"},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for unknown style type")
	}
	if !strings.Contains(err.Error(), `type "unknown_type" is not defined in metamodel`) {
		t.Errorf("expected error about unknown type, got: %v", err)
	}
}

func TestValidateConfig_StyleValidCustomType(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Styles: map[string]map[string]string{
			"status":   {"open": "blue"},
			"priority": {"high": "red"},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err != nil {
		t.Errorf("expected valid styles to pass, got: %v", err)
	}
}

func TestValidateConfig_NavigationUnknownList(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Navigation: []NavigationEntry{
			{Label: "Test", List: "unknown_list"},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for unknown list in navigation")
	}
	if !strings.Contains(err.Error(), `references unknown list "unknown_list"`) {
		t.Errorf("expected error about unknown list, got: %v", err)
	}
}

func TestValidateConfig_NavigationNestedGroups(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Navigation: []NavigationEntry{
			{
				Group: "Outer",
				Items: []NavigationEntry{
					{Group: "Inner"},
				},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for nested groups")
	}
	if !strings.Contains(err.Error(), "nested groups are not supported") {
		t.Errorf("expected error about nested groups, got: %v", err)
	}
}

func TestValidateConfig_MultipleErrors(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Forms: map[string]Form{
			"test": {EntityType: "unknown1"},
		},
		Lists: map[string]List{
			"test": {EntityType: "unknown2"},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected multiple errors")
	}

	var configErr *ConfigValidationError
	if !errors.As(err, &configErr) {
		t.Fatalf("expected ConfigValidationError, got %T", err)
	}
	if len(configErr.Errors) != 2 {
		t.Errorf("expected 2 errors, got %d: %v", len(configErr.Errors), configErr.Errors)
	}
}

func TestValidateConfig_ViewTraverseWildcardFrom(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Views: map[string]ViewConfig{
			"test": {
				Entry: ViewEntry{Type: "ticket"},
				Traverse: []ViewTraverse{
					{From: "entry", Follow: "blocks", CollectAs: "blocked"},
					{From: "*", Follow: "belongs-to", CollectAs: "categories"},
				},
				Sections: []ViewSection{
					{Source: "categories", Display: "list"},
				},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err != nil {
		t.Errorf("expected wildcard from to be valid, got: %v", err)
	}
}

func TestValidateConfig_ViewSectionUsesPreviousCollectAs(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Views: map[string]ViewConfig{
			"test": {
				Entry: ViewEntry{Type: "ticket"},
				Traverse: []ViewTraverse{
					{From: "entry", Follow: "blocks", CollectAs: "blocked"},
				},
				Sections: []ViewSection{
					{Source: "blocked", Display: "table", Columns: []ListColumn{{Property: "title"}}},
				},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err != nil {
		t.Errorf("expected section using collect_as to be valid, got: %v", err)
	}
}

// TestValidateConfig_Documents is a table-driven sweep of DocumentConfig
// validation. Covers the {command, script} mutual-exclusion rule and the
// two document kinds (entity-anchored vs standalone).
//
// entity_type used to be unconditionally required; TKT-M1AX6P made an empty
// entity_type mean "standalone document" instead. RR-1FA8W's underlying risk —
// that relaxing one field's required-ness silently drops a sibling rule — still
// applies, so the renderer cases below assert that a standalone document is
// still held to the {command, script} rule rather than skipping validation
// wholesale.
func TestValidateConfig_Documents(t *testing.T) {
	meta := testMetamodel()

	// A minimal valid form so doc.edit.form references resolve.
	editForm := Form{EntityType: "ticket", Title: "Edit Requirement", Mode: "edit"}

	cases := []struct {
		name    string
		doc     DocumentConfig
		wantErr string // substring that must appear; "" means expect success
	}{
		{
			name:    "only command is valid",
			doc:     DocumentConfig{Command: []string{"render.sh"}, EntityType: "ticket"},
			wantErr: "",
		},
		{
			name:    "only script is valid",
			doc:     DocumentConfig{Script: "docs/render.lua", EntityType: "ticket"},
			wantErr: "",
		},
		{
			name:    "both command and script is an error",
			doc:     DocumentConfig{Command: []string{"render.sh"}, Script: "docs/render.lua", EntityType: "ticket"},
			wantErr: "mutually exclusive",
		},
		// --- allow_acl_bypass (TKT-Y3JVFK) ---
		{
			name: "elevated read with permission is valid",
			doc: DocumentConfig{
				Script:         "docs/benchmark.lua",
				AllowACLBypass: metamodel.ACLBypassRead,
				Permission:     "report:sales",
			},
			wantErr: "",
		},
		{
			// Elevation is allowed on an entity-anchored document too; both
			// gates then apply (the per-entity read gate AND the permission).
			name: "elevated read on an entity-anchored document is valid",
			doc: DocumentConfig{
				Script:         "docs/benchmark.lua",
				EntityType:     "ticket",
				AllowACLBypass: metamodel.ACLBypassRead,
				Permission:     "report:sales",
			},
			wantErr: "",
		},
		{
			// The load-bearing rule: without a permission an elevated render
			// serves everything the script reads to every principal.
			name: "elevated without permission is an error",
			doc: DocumentConfig{
				Script:         "docs/benchmark.lua",
				AllowACLBypass: metamodel.ACLBypassRead,
			},
			wantErr: "permission is required when allow_acl_bypass is set",
		},
		{
			name: "elevated write is an error",
			doc: DocumentConfig{
				Script:         "docs/benchmark.lua",
				AllowACLBypass: metamodel.ACLBypassWrite,
				Permission:     "report:sales",
			},
			wantErr: "a render is a GET",
		},
		{
			name: "elevated read+write is an error",
			doc: DocumentConfig{
				Script:         "docs/benchmark.lua",
				AllowACLBypass: metamodel.ACLBypassReadWrite,
				Permission:     "report:sales",
			},
			wantErr: "a render is a GET",
		},
		{
			// A command renderer never sees the Lua bindings elevation
			// unlocks, so declaring it names a capability that cannot apply.
			name: "elevated command renderer is an error",
			doc: DocumentConfig{
				Command:        []string{"render.sh"},
				AllowACLBypass: metamodel.ACLBypassRead,
				Permission:     "report:sales",
			},
			wantErr: "applies only to a script renderer",
		},
		{
			// The overwhelmingly common case must stay unaffected: no
			// elevation means no permission requirement.
			name:    "unelevated document needs no permission",
			doc:     DocumentConfig{Script: "docs/render.lua", EntityType: "ticket"},
			wantErr: "",
		},
		{
			name:    "neither command nor script is an error",
			doc:     DocumentConfig{EntityType: "ticket"},
			wantErr: "one of command or script must be set",
		},
		{
			// Omitting entity_type is no longer an error — it declares a
			// standalone document (TKT-M1AX6P).
			name:    "standalone with command is valid",
			doc:     DocumentConfig{Command: []string{"render.sh"}},
			wantErr: "",
		},
		{
			name:    "standalone with script is valid",
			doc:     DocumentConfig{Script: "docs/render.lua"},
			wantErr: "",
		},
		{
			// RR-1FA8W guard: relaxing entity_type must not disable the
			// sibling {command, script} rules for standalone documents.
			name:    "standalone with neither command nor script is an error",
			doc:     DocumentConfig{},
			wantErr: "one of command or script must be set",
		},
		{
			name:    "standalone with both command and script is an error",
			doc:     DocumentConfig{Command: []string{"render.sh"}, Script: "docs/render.lua"},
			wantErr: "mutually exclusive",
		},
		{
			name:    "standalone with a permission is valid",
			doc:     DocumentConfig{Script: "docs/render.lua", Permission: "report:sales"},
			wantErr: "",
		},
		{
			// An edit button navigates to a form for the document's entity;
			// a standalone document has none.
			name: "standalone with an edit block is an error",
			doc: DocumentConfig{
				Script: "docs/render.lua",
				Edit:   &DocumentEdit{Form: "edit_req", Label: "Edit"},
			},
			wantErr: "edit is not supported without entity_type",
		},
		{
			name: "edit block with valid form and label",
			doc: DocumentConfig{
				Command:    []string{"render.sh"},
				EntityType: "ticket",
				Edit:       &DocumentEdit{Form: "edit_req", Label: "Edit requirement"},
			},
			wantErr: "",
		},
		{
			name: "edit.form references unknown form",
			doc: DocumentConfig{
				Command:    []string{"render.sh"},
				EntityType: "ticket",
				Edit:       &DocumentEdit{Form: "bogus", Label: "Edit"},
			},
			wantErr: `edit.form references unknown form "bogus"`,
		},
		{
			name: "edit.form empty when edit block is set",
			doc: DocumentConfig{
				Command:    []string{"render.sh"},
				EntityType: "ticket",
				Edit:       &DocumentEdit{Form: "", Label: "Edit"},
			},
			wantErr: "edit.form is required when edit is set",
		},
		{
			name: "edit.label empty when edit block is set",
			doc: DocumentConfig{
				Command:    []string{"render.sh"},
				EntityType: "ticket",
				Edit:       &DocumentEdit{Form: "edit_req", Label: ""},
			},
			wantErr: "edit.label is required when edit is set",
		},
		{
			name: "edit block with script renderer",
			doc: DocumentConfig{
				Script:     "docs/render.lua",
				EntityType: "ticket",
				Edit:       &DocumentEdit{Form: "edit_req", Label: "Edit"},
			},
			wantErr: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Documents: map[string]DocumentConfig{"spec": tc.doc},
				Forms:     map[string]Form{"edit_req": editForm},
			}
			err := ValidateConfig(nil, cfg, meta)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("expected success, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected error to contain %q, got: %s", tc.wantErr, err.Error())
			}
		})
	}
}

// TestValidateConfig_DocumentsEditBothEmpty pins the contract that an empty
// DocumentEdit produces both error messages. The independent if-branches in
// validateDocuments are correct only if both fire for `Edit: &DocumentEdit{}`.
func TestValidateConfig_DocumentsEditBothEmpty(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Documents: map[string]DocumentConfig{
			"spec": {
				Command:    []string{"render.sh"},
				EntityType: "ticket",
				Edit:       &DocumentEdit{}, // both fields empty
			},
		},
	}
	err := ValidateConfig(nil, cfg, meta)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "edit.form is required when edit is set") {
		t.Errorf("expected edit.form error, got: %s", msg)
	}
	if !strings.Contains(msg, "edit.label is required when edit is set") {
		t.Errorf("expected edit.label error, got: %s", msg)
	}
}

// TestValidateConfig_DocumentsEditFormSuggestion verifies the typo-suggestion
// path matches list.edit_form's user-facing wording.
func TestValidateConfig_DocumentsEditFormSuggestion(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Documents: map[string]DocumentConfig{
			"spec": {
				Command:    []string{"render.sh"},
				EntityType: "ticket",
				Edit:       &DocumentEdit{Form: "EDIT_TICKET", Label: "Edit"},
			},
		},
		Forms: map[string]Form{"edit_ticket": {EntityType: "ticket", Title: "Edit"}},
	}
	err := ValidateConfig(nil, cfg, meta)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `did you mean "edit_ticket"`) {
		t.Errorf("expected typo suggestion, got: %s", err.Error())
	}
}

func TestConfigValidationError_SingleError(t *testing.T) {
	err := &ConfigValidationError{Errors: []string{"single error"}}
	if err.Error() != "single error" {
		t.Errorf("expected 'single error', got %q", err.Error())
	}
}

func TestConfigValidationError_MultipleErrors(t *testing.T) {
	err := &ConfigValidationError{Errors: []string{"error 1", "error 2"}}
	expected := "data-entry config validation errors:\n  - error 1\n  - error 2"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestValidateConfig_FilterControlMissingPropertyAndRelation(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Lists: map[string]List{
			"test": {
				EntityType:     "ticket",
				FilterControls: []FilterControl{{}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for filter_control with neither property nor relation")
	}
	if !strings.Contains(err.Error(), "must specify either property or relation") {
		t.Errorf("expected error about missing property or relation, got: %v", err)
	}
}

func TestValidateConfig_FilterControlUnknownRelation(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Lists: map[string]List{
			"test": {
				EntityType:     "ticket",
				FilterControls: []FilterControl{{Relation: "unknown_rel"}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for filter_control with unknown relation")
	}
	if !strings.Contains(err.Error(), `references unknown relation "unknown_rel"`) {
		t.Errorf("expected error about unknown relation, got: %v", err)
	}
}

func TestValidateConfig_FilterControlValidRelation(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Lists: map[string]List{
			"test": {
				EntityType:     "ticket",
				FilterControls: []FilterControl{{Relation: "belongs-to"}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err != nil {
		t.Errorf("expected valid filter_control relation to pass, got: %v", err)
	}
}

func TestValidateConfig_KanbanFilterControlMissingPropertyAndRelation(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Kanbans: map[string]Kanban{
			"test": {
				EntityType:     "ticket",
				ColumnProperty: "status",
				FilterControls: []FilterControl{{}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for kanban filter_control with neither property nor relation")
	}
	if !strings.Contains(err.Error(), "must specify either property or relation") {
		t.Errorf("expected error about missing property or relation, got: %v", err)
	}
}

func TestValidateConfig_KanbanFilterControlUnknownRelation(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Kanbans: map[string]Kanban{
			"test": {
				EntityType:     "ticket",
				ColumnProperty: "status",
				FilterControls: []FilterControl{{Relation: "unknown_rel"}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected error for kanban filter_control with unknown relation")
	}
	if !strings.Contains(err.Error(), `references unknown relation "unknown_rel"`) {
		t.Errorf("expected error about unknown relation, got: %v", err)
	}
}

func TestValidateActions_ValidScript(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Actions: map[string]Action{
			"today_note": {Script: "today.lua"},
			"find_or_create": {
				Script: "find-or-create.lua",
				Params: map[string]string{"entity_type": "ticket"},
			},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateActions_ValidSet(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Actions: map[string]Action{
			"mark-done": {
				Label: "Done",
				Key:   "d",
				Set:   map[string]string{"status": "closed"},
			},
		},
		Lists: map[string]List{
			"tickets": {
				EntityType: "ticket",
				Columns:    []ListColumn{{Property: "title"}},
				Actions:    []string{"mark-done"},
			},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidAppID(t *testing.T) {
	valid := []string{"dash", "ticket-counter", "a", "a_b-c", strings.Repeat("a", 64)}
	for _, id := range valid {
		if !ValidAppID(id) {
			t.Errorf("ValidAppID(%q) = false, want true", id)
		}
	}
	invalid := []string{"", "Dash", "has space", "has.dot", "has/slash", "..", strings.Repeat("a", 65)}
	for _, id := range invalid {
		if ValidAppID(id) {
			t.Errorf("ValidAppID(%q) = true, want false", id)
		}
	}
}

func TestValidateActions_InvalidID(t *testing.T) {
	meta := testMetamodel()
	tests := []string{"Today_Note", "today note", "today/note", "today..note", "x" + strings.Repeat("y", 64)}
	for _, id := range tests {
		t.Run(id, func(t *testing.T) {
			cfg := &Config{
				Actions: map[string]Action{id: {Script: "x.lua"}},
			}
			err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
			if err == nil {
				t.Fatalf("expected error for invalid ID %q", id)
			}
		})
	}
}

func TestValidateActions_NeitherSetNorScript(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Actions: map[string]Action{"foo": {}},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil || !strings.Contains(err.Error(), "neither script nor set") {
		t.Errorf("expected neither error, got: %v", err)
	}
}

func TestValidateActions_BothSetAndScript(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Actions: map[string]Action{
			"foo": {Script: "x.lua", Set: map[string]string{"status": "closed"}},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil || !strings.Contains(err.Error(), "both script and set") {
		t.Errorf("expected both error, got: %v", err)
	}
}

func TestValidateActions_PathTraversal(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Actions: map[string]Action{"foo": {Script: "../etc/passwd.lua"}},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil || !strings.Contains(err.Error(), "local path") {
		t.Errorf("expected path traversal error, got: %v", err)
	}
}

func TestValidateActions_WrongExtension(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Actions: map[string]Action{"foo": {Script: "script.txt"}},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil || !strings.Contains(err.Error(), ".lua") {
		t.Errorf("expected extension error, got: %v", err)
	}
}

func TestValidateActions_ReservedKey(t *testing.T) {
	meta := testMetamodel()
	for _, key := range []string{"j", "k", "o", "e", "n", "h", "l"} {
		t.Run(key, func(t *testing.T) {
			cfg := &Config{
				Actions: map[string]Action{
					"act": {Label: "Act", Key: key, Set: map[string]string{"status": "closed"}},
				},
			}
			err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
			if err == nil || !strings.Contains(err.Error(), "reserved") {
				t.Errorf("expected reserved key error for %q, got: %v", key, err)
			}
		})
	}
}

func TestValidateActions_InvalidKeyFormat(t *testing.T) {
	meta := testMetamodel()
	for _, key := range []string{"dd", "D", "!", " ", "Enter"} {
		t.Run(key, func(t *testing.T) {
			cfg := &Config{
				Actions: map[string]Action{
					"act": {Label: "Act", Key: key, Set: map[string]string{"status": "closed"}},
				},
			}
			err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
			if err == nil || !strings.Contains(err.Error(), "single lowercase") {
				t.Errorf("expected key format error for %q, got: %v", key, err)
			}
		})
	}
}

func TestValidateActions_DuplicateKeysInList(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Actions: map[string]Action{
			"act1": {Label: "One", Key: "d", Set: map[string]string{"status": "closed"}},
			"act2": {Label: "Two", Key: "d", Set: map[string]string{"status": "open"}},
		},
		Lists: map[string]List{
			"tickets": {
				EntityType: "ticket",
				Columns:    []ListColumn{{Property: "title"}},
				Actions:    []string{"act1", "act2"},
			},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Errorf("expected duplicate key error, got: %v", err)
	}
}

func TestValidateActions_UnknownActionRef(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Lists: map[string]List{
			"tickets": {
				EntityType: "ticket",
				Columns:    []ListColumn{{Property: "title"}},
				Actions:    []string{"nonexistent"},
			},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("expected unknown action error, got: %v", err)
	}
}

func TestValidateActions_MissingLabelInList(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Actions: map[string]Action{
			"act": {Key: "d", Set: map[string]string{"status": "closed"}},
		},
		Lists: map[string]List{
			"tickets": {
				EntityType: "ticket",
				Columns:    []ListColumn{{Property: "title"}},
				Actions:    []string{"act"},
			},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil || !strings.Contains(err.Error(), "must have a label") {
		t.Errorf("expected missing label error, got: %v", err)
	}
}

func TestValidateActions_MissingKeyInList(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Actions: map[string]Action{
			"act": {Label: "Done", Set: map[string]string{"status": "closed"}},
		},
		Lists: map[string]List{
			"tickets": {
				EntityType: "ticket",
				Columns:    []ListColumn{{Property: "title"}},
				Actions:    []string{"act"},
			},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil || !strings.Contains(err.Error(), "must have a key") {
		t.Errorf("expected missing key error, got: %v", err)
	}
}

func TestValidateActions_UnknownPropertyInSet(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Actions: map[string]Action{
			"act": {Label: "Done", Key: "d", Set: map[string]string{"nonexistent": "val"}},
		},
		Lists: map[string]List{
			"tickets": {
				EntityType: "ticket",
				Columns:    []ListColumn{{Property: "title"}},
				Actions:    []string{"act"},
			},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil || !strings.Contains(err.Error(), "unknown property") {
		t.Errorf("expected unknown property error, got: %v", err)
	}
}

func TestValidateActions_ScriptActionWithoutKeyLabel(t *testing.T) {
	// Script-only actions (sidebar) don't need key/label when not referenced by a list
	meta := testMetamodel()
	cfg := &Config{
		Actions: map[string]Action{
			"sidebar-action": {Script: "do-stuff.lua"},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateNavigation_UnknownAction(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Navigation: []NavigationEntry{
			{Label: "Today", Action: "missing_action"},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil || !strings.Contains(err.Error(), `unknown action "missing_action"`) {
		t.Errorf("expected unknown action error, got: %v", err)
	}
}

func TestValidateNavigation_KnownAction(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Actions: map[string]Action{
			"today_note": {Script: "today.lua"},
		},
		Navigation: []NavigationEntry{
			{Label: "Today", Action: "today_note"},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestValidateNavigation_Document covers the `document:` navigation entry
// (TKT-M1AX6P). Only a standalone document can be a navigation target: an
// entity-anchored one needs an entry id the sidebar cannot supply.
func TestValidateNavigation_Document(t *testing.T) {
	meta := testMetamodel()

	cases := []struct {
		name    string
		docs    map[string]DocumentConfig
		wantErr string // substring; "" means expect success
	}{
		{
			name:    "standalone document is a valid nav target",
			docs:    map[string]DocumentConfig{"sales_review": {Script: "docs/sales.lua"}},
			wantErr: "",
		},
		{
			name:    "unknown document is an error",
			docs:    map[string]DocumentConfig{"other": {Script: "docs/other.lua"}},
			wantErr: `unknown document "sales_review"`,
		},
		{
			name: "entity-anchored document is not a valid nav target",
			docs: map[string]DocumentConfig{
				"sales_review": {Script: "docs/sales.lua", EntityType: "ticket"},
			},
			wantErr: `document "sales_review" has entity_type "ticket"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Documents: tc.docs,
				Navigation: []NavigationEntry{
					{Label: "Verkooprapportage", Document: "sales_review"},
				},
			}
			err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("expected success, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected error to contain %q, got: %s", tc.wantErr, err.Error())
			}
		})
	}
}

// TestValidateNavigation_Permission covers `permission:` on a navigation entry
// (TKT-TXDK8U). It is valid on any direct item and rejected on a group: a group
// is a container with no destination, and it already disappears on its own once
// every child is filtered out.
func TestValidateNavigation_Permission(t *testing.T) {
	meta := testMetamodel()

	cases := []struct {
		name    string
		nav     []NavigationEntry
		wantErr string // substring; "" means expect success
	}{
		{
			name:    "permission on a list entry is valid",
			nav:     []NavigationEntry{{Label: "Audit", List: "tickets", Permission: "admin:read"}},
			wantErr: "",
		},
		{
			// Kinds with no config object behind them can be gated too — that
			// is the main reason the field lives on the entry.
			name:    "permission on a settings entry is valid",
			nav:     []NavigationEntry{{Label: "Settings", Settings: true, Permission: "admin:settings"}},
			wantErr: "",
		},
		{
			name: "permission on a group is an error",
			nav: []NavigationEntry{
				{Group: "Admin", Permission: "admin:read", Items: []NavigationEntry{
					{Label: "Audit", List: "tickets"},
				}},
			},
			wantErr: `group "Admin" cannot have a permission`,
		},
		{
			name: "permission on an item inside a group is valid",
			nav: []NavigationEntry{
				{Group: "Admin", Items: []NavigationEntry{
					{Label: "Audit", List: "tickets", Permission: "admin:read"},
				}},
			},
			wantErr: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Lists:      map[string]List{"tickets": {EntityType: "ticket"}},
				Navigation: tc.nav,
			}
			err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("expected success, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected error to contain %q, got: %s", tc.wantErr, err.Error())
			}
		})
	}
}

// TestValidateNavigation_DocumentInGroup pins that the document check recurses
// into groups like the list/kanban/action checks do.
func TestValidateNavigation_DocumentInGroup(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Documents: map[string]DocumentConfig{"spec": {Script: "docs/spec.lua", EntityType: "ticket"}},
		Navigation: []NavigationEntry{
			{Group: "Reports", Items: []NavigationEntry{
				{Label: "Spec", Document: "spec"},
			}},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil || !strings.Contains(err.Error(), `document "spec" has entity_type "ticket"`) {
		t.Errorf("expected entity-anchored rejection inside a group, got: %v", err)
	}
}

func TestValidateEntityViews_Valid(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Views: map[string]ViewConfig{
			"ticket_detail": {Entry: ViewEntry{Type: "ticket"}},
		},
		EntityViews: map[string]EntityViewConfig{
			"ticket": {DetailView: "ticket_detail"},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateEntityViews_AbsentIsValid(t *testing.T) {
	// The pre-migration baseline: no entity_views key at all must validate.
	meta := testMetamodel()
	cfg := &Config{}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateEntityViews_UnknownEntityType(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Views: map[string]ViewConfig{
			"any_view": {Entry: ViewEntry{Type: "ticket"}},
		},
		EntityViews: map[string]EntityViewConfig{
			"unknown_type": {DetailView: "any_view"},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil || !strings.Contains(err.Error(), `entity_views: unknown entity type "unknown_type"`) {
		t.Errorf("expected unknown entity type error, got: %v", err)
	}
}

// An entry that declares nothing at all is refused, so an operator does not
// leave a stanza behind that silently does something.
//
// The message widened from "detail_view is empty" when `duplicate:` became a
// second thing an entry may declare (TKT-Z8K2FS): an entry carrying only a
// duplicate block is legitimate, so the refusal is now about the entry being
// entirely empty rather than about detail_view specifically. See
// TestValidateEntityViews_Duplicate for the duplicate-only case.
func TestValidateEntityViews_EmptyEntryIsError(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		EntityViews: map[string]EntityViewConfig{
			"ticket": {DetailView: ""},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil || !strings.Contains(err.Error(), "declares nothing") {
		t.Errorf("expected empty-entry error, got: %v", err)
	}
}

func TestValidateEntityViews_UnknownDetailView(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Views: map[string]ViewConfig{
			"ticket_detail": {Entry: ViewEntry{Type: "ticket"}},
		},
		EntityViews: map[string]EntityViewConfig{
			"ticket": {DetailView: "nonexistent_view"},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil || !strings.Contains(err.Error(), `references unknown view "nonexistent_view"`) {
		t.Errorf("expected unknown view error, got: %v", err)
	}
}

func TestValidateConfig_UnknownTopLevelKey_EntityViewsAccepted(t *testing.T) {
	// Regression: entity_views: must be in validTopLevelKeys.
	meta := testMetamodel()
	yamlData := []byte(`
version: "1.0"
entity_views:
  ticket:
    detail_view: ticket_detail
views:
  ticket_detail:
    entry:
      type: ticket
`)
	cfg := &Config{
		Views: map[string]ViewConfig{
			"ticket_detail": {Entry: ViewEntry{Type: "ticket"}},
		},
		EntityViews: map[string]EntityViewConfig{
			"ticket": {DetailView: "ticket_detail"},
		},
	}
	err := ValidateConfig(yamlData, cfg, meta)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestValidateApp_DefaultWorld pins the LOAD-TIME refusal.
//
// This matters more than an ordinary config typo: an unvalidated
// `default_world` would silently serve the DEFAULT world to every request
// and look exactly like the feature was never configured — no error, no
// warning, just the wrong faces everywhere. Failing the load is the only
// signal an operator gets.
func TestValidateApp_DefaultWorld(t *testing.T) {
	meta := &metamodel.Metamodel{
		Version: "1.0",
		Worlds: map[string]metamodel.WorldDef{
			"published": {Select: []string{"published"}, Otherwise: "exclude"},
			"site-nl":   {Select: []string{"nl", "en"}, Otherwise: "default"},
		},
	}

	tests := []struct {
		name    string
		world   string
		meta    *metamodel.Metamodel
		wantErr string
	}{
		{name: "unset is fine", world: "", meta: meta},
		{name: "declared world accepted", world: "published", meta: meta},
		{
			// `default` is implicit and total — never declared under `worlds:`,
			// so it must be accepted without being found there.
			name: "the reserved default world is accepted", world: "default", meta: meta,
		},
		{
			name:  "a typo is refused, and the error names what IS declared",
			world: "publsihed", meta: meta,
			wantErr: `"publsihed" is not a declared world`,
		},
		{
			name:  "set with no metamodel is refused rather than assumed valid",
			world: "published", meta: nil,
			wantErr: "no metamodel is available",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{App: AppConfig{DefaultWorld: tt.world}}
			errs := validateApp(cfg, tt.meta)
			if tt.wantErr == "" {
				if len(errs) != 0 {
					t.Fatalf("validateApp(default_world=%q) = %v, want no errors", tt.world, errs)
				}
				return
			}
			if len(errs) == 0 {
				t.Fatalf("validateApp(default_world=%q) = no errors, want %q", tt.world, tt.wantErr)
			}
			if !strings.Contains(errs[0], tt.wantErr) {
				t.Errorf("validateApp(default_world=%q) = %q, want it to contain %q",
					tt.world, errs[0], tt.wantErr)
			}
		})
	}

	t.Run("the refusal lists the declared worlds", func(t *testing.T) {
		// The operator needs to see what they COULD have written; an error that
		// only says "not declared" sends them hunting through schema.yaml.
		cfg := &Config{App: AppConfig{DefaultWorld: "nope"}}
		errs := validateApp(cfg, meta)
		if len(errs) == 0 {
			t.Fatal("expected a refusal")
		}
		for _, w := range []string{"published", "site-nl"} {
			if !strings.Contains(errs[0], w) {
				t.Errorf("error %q does not name the declared world %q", errs[0], w)
			}
		}
	})
}

func TestValidateApp_PlantUMLServerURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr string // substring; "" means valid
	}{
		{name: "empty is valid (disabled)", url: "", wantErr: ""},
		{name: "https is valid", url: "https://plantuml.example.com", wantErr: ""},
		{name: "http is valid", url: "http://localhost:8080", wantErr: ""},
		{name: "https with path is valid", url: "https://example.com/plantuml", wantErr: ""},

		// http is confined to loopback: the SPA puts the diagram source in the
		// URL, so cleartext to anywhere else publishes it on the wire.
		{name: "http to localhost without port is valid", url: "http://localhost", wantErr: ""},
		{name: "http to localhost uppercase is valid", url: "http://LOCALHOST:8080", wantErr: ""},
		{name: "http to 127.0.0.1 is valid", url: "http://127.0.0.1:8080", wantErr: ""},
		{name: "http elsewhere in 127/8 is valid", url: "http://127.1.2.3:8080", wantErr: ""},
		{name: "http to ::1 is valid", url: "http://[::1]:8080", wantErr: ""},
		{name: "https to a remote host stays valid", url: "https://plantuml.internal.example.com", wantErr: ""},
		// Fully-qualified spellings of the same local machine.
		{name: "http to localhost with trailing dot is valid", url: "http://localhost.:8080", wantErr: ""},
		{name: "http to 127.0.0.1 with trailing dot is valid", url: "http://127.0.0.1.:8080", wantErr: ""},
		{name: "http to IPv4-mapped loopback is valid", url: "http://[::ffff:127.0.0.1]:8080", wantErr: ""},
		{name: "http to expanded ::1 is valid", url: "http://[0:0:0:0:0:0:0:1]:8080", wantErr: ""},

		{name: "http to a remote host rejected", url: "http://plantuml.example.com", wantErr: "cleartext"},
		{name: "http to a public IP rejected", url: "http://93.184.216.34", wantErr: "cleartext"},
		// A private range is still a real network segment with other hosts on
		// it, so it gets no exemption.
		{name: "http to an RFC1918 host rejected", url: "http://192.168.1.10:8080", wantErr: "cleartext"},

		// Hosts that only look like loopback. Each of these defeats a naive
		// prefix/suffix/contains check and resolves wherever its owner points it.
		{name: "loopback-prefixed domain rejected", url: "http://localhost.evil.com", wantErr: "cleartext"},
		{name: "loopback-IP-prefixed domain rejected", url: "http://127.0.0.1.evil.com", wantErr: "cleartext"},
		{name: "loopback in userinfo rejected", url: "http://localhost@evil.com", wantErr: "cleartext"},
		{name: "loopback-suffixed domain rejected", url: "http://evil-localhost.com", wantErr: "cleartext"},
		// Stripping one trailing dot must not turn a remote name local.
		{name: "lookalike with trailing dot rejected", url: "http://localhost.evil.com.", wantErr: "cleartext"},
		{name: "double trailing dot rejected", url: "http://localhost..", wantErr: "cleartext"},
		// Unusual encodings of 127.0.0.1 are refused, not decoded (fails safe).
		{name: "decimal-encoded loopback rejected", url: "http://2130706433", wantErr: "cleartext"},
		{name: "unspecified address rejected", url: "http://0.0.0.0:8080", wantErr: "cleartext"},
		{name: "javascript scheme rejected", url: "javascript:alert(1)", wantErr: "scheme must be http or https"},
		{name: "data scheme rejected", url: "data:text/html,x", wantErr: "scheme must be http or https"},
		{name: "protocol-relative rejected", url: "//evil.example", wantErr: "scheme must be http or https"},
		{name: "bare host rejected", url: "evil.example/x", wantErr: "scheme must be http or https"},
		{name: "scheme without host rejected", url: "https://", wantErr: "must include a host"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{App: AppConfig{PlantUMLServerURL: tt.url}}
			// nil metamodel: this table only exercises plantuml_server_url,
			// and the default_world arm short-circuits on an empty value
			// before it would need one.
			errs := validateApp(cfg, nil)
			if tt.wantErr == "" {
				if len(errs) != 0 {
					t.Fatalf("validateApp(%q) = %v, want no errors", tt.url, errs)
				}
				return
			}
			if len(errs) == 0 {
				t.Fatalf("validateApp(%q) = no errors, want error containing %q", tt.url, tt.wantErr)
			}
			if !strings.Contains(errs[0], tt.wantErr) {
				t.Errorf("validateApp(%q) = %q, want substring %q", tt.url, errs[0], tt.wantErr)
			}
		})
	}
}

func TestCollectConfigWarnings_IncomingRelationFilterWrongSide(t *testing.T) {
	meta := testMetamodel() // belongs-to: from ticket, to category

	tests := []struct {
		name        string
		list        List
		wantWarning bool
	}{
		{
			name: "incoming filter on the `to` side is fine",
			list: List{
				EntityType:     "category",
				FilterControls: []FilterControl{{Relation: "belongs-to", Direction: DirectionIncoming}},
			},
			wantWarning: false,
		},
		{
			name: "incoming filter on the `from` side warns",
			list: List{
				EntityType:     "ticket",
				FilterControls: []FilterControl{{Relation: "belongs-to", Direction: DirectionIncoming}},
			},
			wantWarning: true,
		},
		{
			name: "outgoing filter on the `from` side is fine (no direction check)",
			list: List{
				EntityType:     "ticket",
				FilterControls: []FilterControl{{Relation: "belongs-to"}},
			},
			wantWarning: false,
		},
		{
			name: "property filter control never warns",
			list: List{
				EntityType:     "ticket",
				FilterControls: []FilterControl{{Property: "status"}},
			},
			wantWarning: false,
		},
		{
			name: "incoming filter on a self-referential relation is fine",
			list: List{
				EntityType:     "ticket",
				FilterControls: []FilterControl{{Relation: "blocks", Direction: DirectionIncoming}},
			},
			wantWarning: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Lists: map[string]List{"lst": tt.list}}
			warnings := CollectConfigWarnings(cfg, meta)
			if tt.wantWarning {
				if len(warnings) == 0 {
					t.Fatalf("expected a warning, got none")
				}
				if !strings.Contains(warnings[0], "belongs-to") ||
					!strings.Contains(warnings[0], "incoming") {

					t.Errorf("warning missing expected detail: %q", warnings[0])
				}
			} else if len(warnings) != 0 {
				t.Errorf("expected no warnings, got %v", warnings)
			}
		})
	}
}

func TestCollectConfigWarnings_NotFatal(t *testing.T) {
	meta := testMetamodel()
	// A wrong-side incoming filter is a warning, NOT a hard validation error.
	cfg := &Config{
		Lists: map[string]List{
			"lst": {
				EntityType:     "ticket",
				FilterControls: []FilterControl{{Relation: "belongs-to", Direction: DirectionIncoming}},
			},
		},
	}
	if err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta); err != nil {
		t.Fatalf("wrong-side incoming filter should not be a fatal error, got: %v", err)
	}
	if len(CollectConfigWarnings(cfg, meta)) == 0 {
		t.Error("expected a non-fatal warning for the wrong-side incoming filter")
	}
}

// TestCollectConfigWarnings_ConflictingRelationDirections pins RR-9MJRJG: two
// lists of the same entity type configuring the same relation with conflicting
// directions must produce a warning naming the winning (lowest) list ID.
func TestCollectConfigWarnings_ConflictingRelationDirections(t *testing.T) {
	meta := testMetamodel()

	t.Run("conflict warns and names lowest list ID winner", func(t *testing.T) {
		cfg := &Config{Lists: map[string]List{
			"zzz_open": {
				EntityType:     "category",
				FilterControls: []FilterControl{{Relation: "belongs-to", Direction: DirectionIncoming}},
			},
			"aaa_open": {
				EntityType:     "category",
				FilterControls: []FilterControl{{Relation: "belongs-to", Direction: DirectionOutgoing}},
			},
		}}
		warnings := CollectConfigWarnings(cfg, meta)
		found := false
		for _, w := range warnings {
			if strings.Contains(w, "conflicting directions") && strings.Contains(w, `"aaa_open"`) {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected a conflicting-directions warning naming aaa_open, got %v", warnings)
		}
	})

	t.Run("agreeing directions across lists do not warn", func(t *testing.T) {
		cfg := &Config{Lists: map[string]List{
			"a": {EntityType: "ticket", FilterControls: []FilterControl{{Relation: "belongs-to"}}},
			"b": {EntityType: "ticket", FilterControls: []FilterControl{{Relation: "belongs-to", Direction: DirectionOutgoing}}},
		}}
		for _, w := range CollectConfigWarnings(cfg, meta) {
			if strings.Contains(w, "conflicting directions") {
				t.Errorf("unexpected conflict warning: %q", w)
			}
		}
	})

	// A board's controls take part in the resolution (BUG-GEMNW6), so a board
	// disagreeing with a list is ignored at runtime and must be reported; the
	// list wins.
	t.Run("a kanban conflicting with a list warns and the list wins", func(t *testing.T) {
		cfg := &Config{
			Lists: map[string]List{
				"categories": {
					EntityType:     "category",
					FilterControls: []FilterControl{{Relation: "belongs-to", Direction: DirectionIncoming}},
				},
			},
			Kanbans: map[string]Kanban{
				"board": {
					EntityType:     "category",
					FilterControls: []FilterControl{{Relation: "belongs-to", Direction: DirectionOutgoing}},
				},
			},
		}
		found := false
		for _, w := range CollectConfigWarnings(cfg, meta) {
			if strings.Contains(w, "conflicting directions") &&
				strings.Contains(w, `list "categories"`) && strings.Contains(w, "kanban board=outgoing") {

				found = true
			}
		}
		if !found {
			t.Fatalf("expected a conflict warning with list categories winning over kanban board, got %v",
				CollectConfigWarnings(cfg, meta))
		}
	})
}

// TestCollectConfigWarnings_RelationPropertyNameCollision pins RR-0HWAS0: a
// relation FilterControl whose name also names a property of the list's entity
// type (disjoint namespaces) is flagged, because the runtime routes the filter
// to the property.
func TestCollectConfigWarnings_RelationPropertyNameCollision(t *testing.T) {
	// taak has property "belongs_to"; the metamodel also has a global relation
	// "belongs_to". Configuring a relation filter on it collides.
	meta := &metamodel.Metamodel{
		Version: "1.0",
		Entities: map[string]metamodel.EntityDef{
			"taak": {
				Label:    "Taak",
				IDPrefix: "TAAK-",
				Properties: map[string]metamodel.PropertyDef{
					"title":      {Type: "string", Required: true},
					"belongs_to": {Type: "string"},
				},
			},
			"project": {Label: "Project", IDPrefix: "PRJ-", Properties: map[string]metamodel.PropertyDef{"name": {Type: "string"}}},
		},
		Relations: map[string]metamodel.RelationDef{
			"belongs_to": {Label: "belongs to", From: []string{"taak"}, To: []string{"project"}},
		},
	}

	t.Run("relation control colliding with a property warns", func(t *testing.T) {
		cfg := &Config{Lists: map[string]List{
			"taken": {EntityType: "taak", FilterControls: []FilterControl{{Relation: "belongs_to"}}},
		}}
		warnings := CollectConfigWarnings(cfg, meta)
		found := false
		for _, w := range warnings {
			if strings.Contains(w, "collides with a property") && strings.Contains(w, "belongs_to") {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected a name-collision warning, got %v", warnings)
		}
	})

	t.Run("explicit property control disambiguates, no warning", func(t *testing.T) {
		cfg := &Config{Lists: map[string]List{
			"taken": {EntityType: "taak", FilterControls: []FilterControl{
				{Property: "belongs_to"},
				{Relation: "belongs_to"},
			}},
		}}
		for _, w := range CollectConfigWarnings(cfg, meta) {
			if strings.Contains(w, "collides with a property") {
				t.Errorf("unexpected collision warning when a property control disambiguates: %q", w)
			}
		}
	})
}

// TestViewCommandPermissionWarning pins that a `permission:` on a view command
// is surfaced as a warning. The key is inert there (TKT-MJ02AO), and silently
// ignoring it would let an author believe they had granted access.
func TestViewCommandPermissionWarning(t *testing.T) {
	meta := &metamodel.Metamodel{}

	t.Run("view command with permission warns", func(t *testing.T) {
		cfg := &Config{Commands: map[string]CommandConfig{
			"v": {Label: "V", Script: "echo hi", Context: "view", Permission: "command:v"},
		}}
		warnings := CollectConfigWarnings(cfg, meta)
		var found bool
		for _, w := range warnings {
			if strings.Contains(w, `command "v"`) && strings.Contains(w, "ignored for context: view") {
				found = true
			}
		}
		if !found {
			t.Errorf("expected a view-permission warning, got %v", warnings)
		}
	})

	t.Run("view command without permission is silent", func(t *testing.T) {
		cfg := &Config{Commands: map[string]CommandConfig{
			"v": {Label: "V", Script: "echo hi", Context: "view"},
		}}
		for _, w := range CollectConfigWarnings(cfg, meta) {
			if strings.Contains(w, "ignored for context: view") {
				t.Errorf("unexpected warning: %s", w)
			}
		}
	})

	t.Run("non-view command with permission is silent", func(t *testing.T) {
		cfg := &Config{Commands: map[string]CommandConfig{
			"g": {Label: "G", Script: "echo hi", Context: "global", Permission: "command:g"},
		}}
		for _, w := range CollectConfigWarnings(cfg, meta) {
			if strings.Contains(w, "ignored for context: view") {
				t.Errorf("unexpected warning: %s", w)
			}
		}
	})
}

// TestValidateDocuments_LegacyIDPlaceholderRejected pins that a config written
// against the removed {id}/{id_lower} placeholders fails at load with the
// replacement named, rather than silently passing the literal through
// (TKT-QGHNVA).
func TestValidateDocuments_LegacyIDPlaceholderRejected(t *testing.T) {
	meta := testMetamodel()

	for _, arg := range []string{"{id}", "{id_lower}", "--entry={id}"} {
		cfg := &Config{
			Documents: map[string]DocumentConfig{
				"release_notes": {EntityType: "ticket", Command: []string{"my-renderer", arg}},
			},
		}
		err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
		if err == nil {
			t.Fatalf("arg %q: expected an error, got nil", arg)
		}
		if !strings.Contains(err.Error(), "{in}") {
			t.Errorf("arg %q: error should name {in} as the replacement, got: %s", arg, err.Error())
		}
	}
}

// TestValidateConfig_FormRelationSelfReferencing_RequiresDirection pins the one
// case inference cannot resolve: when the form's entity type is BOTH the from
// and the to of the relation, outgoing and incoming are equally valid and mean
// opposite things. Guessing would silently pick one reading, so the author must
// say which.
func TestValidateConfig_FormRelationSelfReferencing_RequiresDirection(t *testing.T) {
	meta := inverseTestMetamodel(t)
	cfg := &Config{
		Forms: map[string]Form{
			"edit_from": {
				EntityType: "from_entity",
				Relations:  []FormRelation{{Relation: "depends_on"}},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected an error for a self-referencing relation with no explicit direction")
	}
	msg := err.Error()
	for _, want := range []string{"depends_on", "from_entity", "direction"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected error to mention %q, got: %v", want, err)
		}
	}
}

// An explicit direction resolves the ambiguity either way.
func TestValidateConfig_FormRelationSelfReferencing_ExplicitDirectionOK(t *testing.T) {
	meta := inverseTestMetamodel(t)
	for _, dir := range []Direction{DirectionOutgoing, DirectionIncoming} {
		t.Run(string(dir), func(t *testing.T) {
			cfg := &Config{
				Forms: map[string]Form{
					"edit_from": {
						EntityType: "from_entity",
						Relations:  []FormRelation{{Relation: "depends_on", Direction: dir}},
					},
				},
			}
			if err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta); err != nil {
				t.Errorf("explicit direction %q should validate, got: %v", dir, err)
			}
		})
	}
}

// TestValidateConfig_EmptyDirectionStringIsNotOutgoing pins the fix for a hole
// in the ambiguity check: `direction: ""` (written but empty, as a templating
// system or config generator emits) used to unmarshal to "outgoing" and so
// walked straight past the self-referencing check that is the whole point of
// requiring an explicit direction. An empty value must be treated the same as
// an absent key — inferred, and rejected when ambiguous.
func TestValidateConfig_EmptyDirectionStringIsNotOutgoing(t *testing.T) {
	meta := inverseTestMetamodel(t)

	t.Run("ambiguous relation still errors", func(t *testing.T) {
		cfg := &Config{
			Forms: map[string]Form{
				"edit_from": {
					EntityType: "from_entity",
					Relations:  []FormRelation{{Relation: "depends_on", Direction: ""}},
				},
			},
		}
		if err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta); err == nil {
			t.Fatal("empty direction must not bypass the self-referencing check")
		}
	})

	t.Run("to-side binding still infers incoming", func(t *testing.T) {
		cfg := &Config{
			Forms: map[string]Form{
				"edit_to": {
					EntityType: "to_entity",
					Relations:  []FormRelation{{Relation: "connects_to", Direction: ""}},
				},
			},
		}
		if err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta); err != nil {
			t.Fatalf("empty direction should infer incoming on a to-side binding, got: %v", err)
		}
	})
}

// TestValidateConfig_AmbiguousDirection_AllSurfaces pins that every surface
// carrying a `direction:` refuses to guess on a self-referencing relation, not
// just form relations. A wrong side is a silent bug on each of them: a list
// column or kanban card renders the wrong neighbors, a filter control filters
// the wrong edge, and a CalDAV collection selects the wrong members.
func TestValidateConfig_AmbiguousDirection_AllSurfaces(t *testing.T) {
	meta := inverseTestMetamodel(t)
	// from_entity is BOTH the from and the to of depends_on.
	const selfRef = "depends_on"

	tests := []struct {
		name string
		cfg  *Config
	}{
		{
			name: "list column",
			cfg: &Config{Lists: map[string]List{
				"l": {EntityType: "from_entity", Columns: []ListColumn{{Relation: selfRef}}},
			}},
		},
		{
			name: "list filter control",
			cfg: &Config{Lists: map[string]List{
				"l": {EntityType: "from_entity", FilterControls: []FilterControl{{Relation: selfRef}}},
			}},
		},
		{
			name: "kanban card field",
			cfg: &Config{Kanbans: map[string]Kanban{
				"k": {
					EntityType:     "from_entity",
					ColumnProperty: "status",
					Card:           KanbanCard{Fields: []KanbanCardField{{Relation: selfRef}}},
				},
			}},
		},
		{
			name: "kanban filter control",
			cfg: &Config{Kanbans: map[string]Kanban{
				"k": {
					EntityType:     "from_entity",
					ColumnProperty: "status",
					FilterControls: []FilterControl{{Relation: selfRef}},
				},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConfig([]byte(`version: "1.0"`), tt.cfg, meta)
			if err == nil {
				t.Fatalf("%s: expected an ambiguity error for a self-referencing relation", tt.name)
			}
			msg := err.Error()
			for _, want := range []string{selfRef, "from_entity", "direction"} {
				if !strings.Contains(msg, want) {
					t.Errorf("expected error to mention %q, got: %v", want, err)
				}
			}
		})
	}
}

// An unambiguous binding on those same surfaces must stay silent — inference
// resolves it, so requiring the key would be noise.
func TestValidateConfig_UnambiguousDirection_AllSurfaces(t *testing.T) {
	meta := inverseTestMetamodel(t)
	// to_entity is only ever the TO of connects_to.
	cfg := &Config{
		Lists: map[string]List{
			"l": {
				EntityType:     "to_entity",
				Columns:        []ListColumn{{Relation: "connects_to"}},
				FilterControls: []FilterControl{{Relation: "connects_to"}},
			},
		},
		Kanbans: map[string]Kanban{
			"k": {
				EntityType:     "to_entity",
				ColumnProperty: "status",
				Card:           KanbanCard{Fields: []KanbanCardField{{Relation: "connects_to"}}},
				FilterControls: []FilterControl{{Relation: "connects_to"}},
			},
		},
	}
	if err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta); err != nil {
		t.Fatalf("unambiguous bindings should validate without an explicit direction, got: %v", err)
	}
}

// TestValidateLists_CreateWorld pins the load-time refusal of a mistyped
// `create_world`.
//
// A typo silently opens the create form in the LIST's own world — precisely
// the behavior the key exists to override, and indistinguishable from never
// having set it. Same failure shape as app.default_world above.
//
// What is deliberately NOT validated: whether the target world resolves a face
// for this type. Linking to another world always works, and a create writes
// the DEFAULT face regardless — it names no face, and no world reaches a
// write — so there is no combination to reject. The real gate is ACL, which is
// per-principal and therefore not a load-time question.
func TestValidateLists_CreateWorld(t *testing.T) {
	meta := &metamodel.Metamodel{
		Version: "1.0",
		Entities: map[string]metamodel.EntityDef{
			"policy": {
				Properties: map[string]metamodel.PropertyDef{"title": {Type: "string"}},
			},
		},
		Worlds: map[string]metamodel.WorldDef{
			"published": {Select: []string{"published"}, Otherwise: "exclude"},
			"editorial": {Select: []string{"draft"}, Otherwise: "default"},
		},
	}

	tests := []struct {
		name        string
		createWorld string
		wantErr     string
	}{
		{name: "unset is fine — the form opens in the list's own world"},
		{name: "a declared world is accepted", createWorld: "editorial"},
		{
			// `default` is implicit and total, never listed under `worlds:`.
			name: "the reserved default world is accepted", createWorld: "default",
		},
		{
			name:        "a typo is refused, and the error names what IS declared",
			createWorld: "editorail",
			wantErr:     `create_world "editorail" is not a declared world`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Lists: map[string]List{
				"policies": {
					EntityType:  "policy",
					Title:       "Policies",
					CreateForm:  "new_policy",
					CreateWorld: tt.createWorld,
					Columns:     []ListColumn{{Property: "title"}},
				},
			}}
			errs := validateLists(cfg, meta)
			if tt.wantErr == "" {
				if len(errs) != 0 {
					t.Fatalf("validateLists(create_world=%q) = %v, want no errors", tt.createWorld, errs)
				}
				return
			}
			if len(errs) == 0 {
				t.Fatalf("validateLists(create_world=%q) = no errors, want %q", tt.createWorld, tt.wantErr)
			}
			joined := strings.Join(errs, "\n")
			if !strings.Contains(joined, tt.wantErr) {
				t.Errorf("validateLists(create_world=%q) = %q, want it to contain %q",
					tt.createWorld, joined, tt.wantErr)
			}
		})
	}
}

// A kanban `condition:` is refused until the board evaluates it server-side.
// Accepting a key that validates and then does nothing is the silent no-op
// BUG-F1LTV0 and BUG-MYN56J are both about. Delete this test — and the guard
// in validateKanbans — when the board honors a condition.
func TestValidateConfig_KanbanConditionRefusedForNow(t *testing.T) {
	mm := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"taak": {Properties: map[string]metamodel.PropertyDef{
				"status": {Type: metamodel.PropertyTypeString},
				"titel":  {Type: metamodel.PropertyTypeString},
			}},
		},
	}
	src := []byte(`
kanbans:
  bord:
    entity_type: taak
    column_property: status
    condition: "entity.status ~= 'gereed'"
    card:
      title: titel
`)
	var cfg Config
	if err := yaml.Unmarshal(src, &cfg); err != nil {
		t.Fatal(err)
	}
	err := ValidateConfig(src, &cfg, mm)
	if err == nil {
		t.Fatal("expected a kanban condition to be refused")
	}
	if !strings.Contains(err.Error(), "condition is not supported on a kanban yet") {
		t.Errorf("error should say why and point elsewhere, got: %v", err)
	}
}

// The same config on a LIST is accepted — the guard is kanban-specific, not a
// blanket refusal of the key.
func TestValidateConfig_ListConditionAccepted(t *testing.T) {
	mm := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"taak": {Properties: map[string]metamodel.PropertyDef{
				"status": {Type: metamodel.PropertyTypeString},
				"titel":  {Type: metamodel.PropertyTypeString},
			}},
		},
	}
	src := []byte(`
lists:
  open:
    entity_type: taak
    condition: "entity.status ~= 'gereed'"
    columns:
      - property: titel
`)
	var cfg Config
	if err := yaml.Unmarshal(src, &cfg); err != nil {
		t.Fatal(err)
	}
	if err := ValidateConfig(src, &cfg, mm); err != nil {
		t.Fatalf("a list condition must be accepted: %v", err)
	}
}

// TestValidateConfig_NestedSection sweeps the `display: nested` checks. Each
// case builds a two-step traverse (entry → blocked → deeper) and varies only
// the section, so a failure points at the rule under test rather than at the
// fixture.
func TestValidateConfig_NestedSection(t *testing.T) {
	twoStep := []ViewTraverse{
		{From: "entry", Follow: "blocks", CollectAs: "blocked"},
		{From: "blocked", Follow: "blocks", CollectAs: "deeper"},
	}

	tests := []struct {
		name     string
		traverse []ViewTraverse
		section  ViewSection
		wantErr  string // substring; empty means the config must validate
	}{
		{
			name:     "valid two-level nesting",
			traverse: twoStep,
			section:  ViewSection{Source: "blocked", Display: "nested", Children: "deeper"},
		},
		{
			name:     "children omitted",
			traverse: twoStep,
			section:  ViewSection{Source: "blocked", Display: "nested"},
			wantErr:  "no children",
		},
		{
			name:     "children names an unknown collection",
			traverse: twoStep,
			section:  ViewSection{Source: "blocked", Display: "nested", Children: "nope"},
			wantErr:  `unknown collection "nope" in children`,
		},
		{
			name:     "children equals source",
			traverse: twoStep,
			section:  ViewSection{Source: "blocked", Display: "nested", Children: "blocked"},
			wantErr:  "under itself",
		},
		{
			// The child rule walks from `entry`, so its results are siblings of
			// the section's rows, not their children. Renders an empty tree.
			name: "children rule does not traverse from source",
			traverse: []ViewTraverse{
				{From: "entry", Follow: "blocks", CollectAs: "blocked"},
				{From: "entry", Follow: "belongs-to", CollectAs: "cats"},
			},
			section: ViewSection{Source: "blocked", Display: "nested", Children: "cats"},
			wantErr: "must start from the section's source",
		},
		{
			// The recursive walk reports ids level by level without retaining
			// which node each came from, so the tree would render flat.
			name: "children collected recursively",
			traverse: []ViewTraverse{
				{From: "entry", Follow: "blocks", CollectAs: "blocked"},
				{From: "blocked", Follow: "blocks", CollectAs: "deeper", Recursive: true},
			},
			section: ViewSection{Source: "blocked", Display: "nested", Children: "deeper"},
			wantErr: "collected recursively",
		},
		{
			name:     "children set on a non-nested display",
			traverse: twoStep,
			section:  ViewSection{Source: "blocked", Display: "list", Children: "deeper"},
			wantErr:  "requires display: nested",
		},
		{
			name:     "columns: refused on a nested section",
			traverse: twoStep,
			section: ViewSection{
				Source: "blocked", Display: DisplayNested, Children: "deeper",
				Columns: []ListColumn{{Property: "title"}},
			},
			wantErr: "takes parent_columns/child_columns",
		},
		{
			// The point of splitting by level: `blocks` is ticket→ticket, so
			// both levels are the SAME type, and only a per-level key can give
			// them different columns.
			name:     "same type at both levels takes different columns",
			traverse: twoStep,
			section: ViewSection{
				Source: "blocked", Display: DisplayNested, Children: "deeper",
				ParentColumns: map[string][]ListColumn{"ticket": {{Property: "status"}}},
				ChildColumns:  map[string][]ListColumn{"ticket": {{Property: "title"}}},
			},
		},
		{
			name:     "child column property absent from its declared type",
			traverse: twoStep,
			section: ViewSection{
				Source: "blocked", Display: DisplayNested, Children: "deeper",
				ChildColumns: map[string][]ListColumn{"ticket": {{Property: "nonesuch"}}},
			},
			wantErr: `property "nonesuch" not in entity "ticket"`,
		},
		{
			name:     "columns declared for a type the level never holds",
			traverse: twoStep,
			section: ViewSection{
				Source: "blocked", Display: DisplayNested, Children: "deeper",
				ChildColumns: map[string][]ListColumn{"category": {{Property: "name"}}},
			},
			wantErr: "which this level never holds",
		},
		{
			// A heterogeneous relation: each reachable type names its own
			// columns, including one the OTHER type does not have.
			name: "heterogeneous children, per-type columns",
			traverse: []ViewTraverse{
				{From: "entry", Follow: "blocks", CollectAs: "blocked"},
				{From: "blocked", Follow: "relates-to", CollectAs: "things"},
			},
			section: ViewSection{
				Source: "blocked", Display: DisplayNested, Children: "things",
				ChildColumns: map[string][]ListColumn{
					"ticket":   {{Property: "status"}},
					"category": {{Property: "name"}},
				},
			},
		},
		{
			name:     "parent_columns on a non-nested display",
			traverse: twoStep,
			section: ViewSection{
				Source: "blocked", Display: "list",
				ParentColumns: map[string][]ListColumn{"ticket": {{Property: "status"}}},
			},
			wantErr: "sets parent_columns but display is",
		},
		// Sort keys are per level, so using the wrong key for the display is
		// refused rather than silently ignored (TKT-9OFGH4).
		{
			name:     "parent_sort and child_sort on a nested display",
			traverse: twoStep,
			section: ViewSection{
				Source: "blocked", Display: DisplayNested, Children: "deeper",
				ParentSort: []SortSpec{{Property: "status"}},
				ChildSort:  []SortSpec{{Property: "status", Direction: "desc"}},
			},
		},
		{
			name:     "sort on a flat display",
			traverse: twoStep,
			section:  ViewSection{Source: "blocked", Display: "table", Sort: []SortSpec{{Property: "status"}}},
		},
		{
			name:     "sort on a nested display",
			traverse: twoStep,
			section: ViewSection{
				Source: "blocked", Display: DisplayNested, Children: "deeper",
				Sort: []SortSpec{{Property: "status"}},
			},
			wantErr: "takes parent_sort/child_sort",
		},
		{
			name:     "child_sort on a flat display",
			traverse: twoStep,
			section: ViewSection{
				Source: "blocked", Display: "list",
				ChildSort: []SortSpec{{Property: "status"}},
			},
			wantErr: "sets child_sort but display is",
		},
		{
			name:     "sort on an undeclared property",
			traverse: twoStep,
			section: ViewSection{
				Source: "blocked", Display: "list",
				Sort: []SortSpec{{Property: "nope"}},
			},
			wantErr: "which no type at this level declares",
		},
		{
			name:     "sort on id is always valid",
			traverse: twoStep,
			section:  ViewSection{Source: "blocked", Display: "list", Sort: []SortSpec{{Property: "id"}}},
		},
		{
			name:     "sort on modified is refused",
			traverse: twoStep,
			section:  ViewSection{Source: "blocked", Display: "list", Sort: []SortSpec{{Property: "modified"}}},
			wantErr:  "which a section cannot order by",
		},
		{
			name:     "sort with an invalid direction",
			traverse: twoStep,
			section: ViewSection{
				Source: "blocked", Display: "list",
				Sort: []SortSpec{{Property: "status", Direction: "sideways"}},
			},
			wantErr: "invalid direction",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Views: map[string]ViewConfig{
				"test": {
					Entry:    ViewEntry{Type: "ticket"},
					Traverse: tc.traverse,
					Sections: []ViewSection{tc.section},
				},
			}}

			err := ValidateConfig([]byte(`version: "1.0"`), cfg, testMetamodel())
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid config, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

// TestValidateConfig_NestedSectionRefusedInSidePanel pins that `display: nested`
// cannot be authored in a form's side panel.
//
// A side panel reuses ViewSection and renders through the same buildSections
// path, so the tree would be BUILT — but v1.SidePanelSection has no field to
// carry it, so it is dropped on the wire and the operator gets a heading with
// nothing under it. Refusing at load is the honest answer (RR-MGMHAC).
func TestValidateConfig_NestedSectionRefusedInSidePanel(t *testing.T) {
	cfg := &Config{
		Forms: map[string]Form{
			"ticket_form": {
				EntityType: "ticket",
				Fields:     []FormField{{Property: "title"}},
				SidePanel: &SidePanelConfig{
					Sections: []ViewSection{
						{Heading: "Children", Source: "entry", Display: DisplayNested, Children: "kids"},
					},
				},
			},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, testMetamodel())
	if err == nil {
		t.Fatal("expected display: nested in a side panel to be rejected")
	}
	if !strings.Contains(err.Error(), "side_panel") ||
		!strings.Contains(err.Error(), "cannot render") {

		t.Errorf("error should name the side panel and say it cannot render, got: %v", err)
	}
}

// TKT-7YHKD1: keep_on_add_another carries a field's value across a
// "Create & add another" reset. The key must survive the round trip on BOTH
// FormField and FormRelation — a missing struct tag would make yaml.v3 drop it
// in silence (the FormRelation.Span failure mode), leaving the validator happy
// and the SPA blind.
func TestValidateConfig_KeepOnAddAnotherRoundTrips(t *testing.T) {
	src := []byte(`version: "1.0"
forms:
  new-ticket:
    entity_type: ticket
    fields:
      - property: title
      - property: assignee
        keep_on_add_another: true
    relations:
      - relation: belongs-to
        keep_on_add_another: true
`)
	var cfg Config
	if err := yaml.Unmarshal(src, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := ValidateConfig(src, &cfg, testMetamodel()); err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}

	form := cfg.Forms["new-ticket"]
	if form.Fields[0].KeepOnAddAnother {
		t.Error("field without the key should be false")
	}
	// Asserting true (not merely "no error") is the point: an absent struct
	// tag makes an absence-assertion pass vacuously.
	if !form.Fields[1].KeepOnAddAnother {
		t.Error("field keep_on_add_another should decode as true")
	}
	if !form.Relations[0].KeepOnAddAnother {
		t.Error("relation keep_on_add_another should decode as true")
	}
}

// A hidden field never renders and is excluded from the editable property set,
// so it has no user-entered value for the reset to carry — the key is inert
// there and that is worth telling the author about.
func TestValidateConfig_KeepOnAddAnotherRejectedOnHiddenField(t *testing.T) {
	cfg := &Config{
		Forms: map[string]Form{
			"test": {
				EntityType: "ticket",
				Fields: []FormField{{
					Property:         "assignee",
					Hidden:           true,
					KeepOnAddAnother: true,
				}},
			},
		},
	}
	err := ValidateConfig([]byte(`version: "1.0"`), cfg, testMetamodel())
	if err == nil {
		t.Fatal("expected error for keep_on_add_another on a hidden field")
	}
	if !strings.Contains(err.Error(), "keep_on_add_another on a hidden field") {
		t.Errorf("expected inert-key error, got: %v", err)
	}
}

// The mode:edit form is deliberately NOT rejected. Mode has no Go consumer and
// getEditFormId falls back to any form for the type, so such a form is still
// reachable as a create form — rejecting here would refuse a working config.
func TestValidateConfig_KeepOnAddAnotherAllowedOnEditModeForm(t *testing.T) {
	cfg := &Config{
		Forms: map[string]Form{
			"test": {
				EntityType: "ticket",
				Mode:       "edit",
				Fields: []FormField{{
					Property:         "assignee",
					KeepOnAddAnother: true,
				}},
			},
		},
	}
	if err := ValidateConfig([]byte(`version: "1.0"`), cfg, testMetamodel()); err != nil {
		t.Errorf("keep_on_add_another on a mode:edit form should be allowed, got: %v", err)
	}
}

// TestValidateDocuments_ReservedExportNameRejected pins the config half of the
// document-export routing premise. `/_documents/{doc}/_export` is the export
// route, so a document literally named "_export" would be unreachable — every
// request for it would route to the export handler instead. The other half (an
// entity id can never be "_export") is guaranteed by entity.ValidateID; this
// rule closes the side that config controls.
func TestValidateDocuments_ReservedExportNameRejected(t *testing.T) {
	meta := testMetamodel()
	cfg := &Config{
		Documents: map[string]DocumentConfig{
			ReservedExportSegment: {EntityType: "ticket", Script: "docs/r.lua"},
		},
	}

	err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta)
	if err == nil {
		t.Fatal("expected a document named \"_export\" to be rejected at load")
	}
	if !strings.Contains(err.Error(), ReservedExportSegment) {
		t.Errorf("error should name the reserved segment, got: %s", err.Error())
	}
}

// A document whose name merely CONTAINS the reserved segment is fine — only the
// exact name collides with the route.
func TestValidateDocuments_NameContainingExportAllowed(t *testing.T) {
	meta := testMetamodel()
	for _, name := range []string{"_exports", "export", "sales_export"} {
		t.Run(name, func(t *testing.T) {
			cfg := &Config{
				Documents: map[string]DocumentConfig{
					name: {EntityType: "ticket", Script: "docs/r.lua"},
				},
			}
			if err := ValidateConfig([]byte(`version: "1.0"`), cfg, meta); err != nil {
				t.Errorf("document %q should be allowed, got: %v", name, err)
			}
		})
	}
}
