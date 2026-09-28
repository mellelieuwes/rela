package dataentryconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestUserDefaultsResolvePropertyDefault(t *testing.T) {
	ud := &UserDefaults{
		Defaults: map[string]string{
			"priority": "medium",
			"reporter": "jeroen",
		},
		Overrides: []DefaultOverride{
			{
				Types: []string{"ticket", "bug"},
				Defaults: map[string]string{
					"priority": "high",
					"status":   "triaged",
				},
			},
			{
				Types: []string{"decision"},
				Defaults: map[string]string{
					"status": "proposed",
				},
			},
		},
	}

	t.Run("global default", func(t *testing.T) {
		got := ud.ResolvePropertyDefault("component", "priority")
		if got != "medium" {
			t.Errorf("expected 'medium', got %q", got)
		}
	})

	t.Run("global default for reporter", func(t *testing.T) {
		got := ud.ResolvePropertyDefault("ticket", "reporter")
		if got != "jeroen" {
			t.Errorf("expected 'jeroen', got %q", got)
		}
	})

	t.Run("override takes precedence over global", func(t *testing.T) {
		got := ud.ResolvePropertyDefault("ticket", "priority")
		if got != "high" {
			t.Errorf("expected 'high', got %q", got)
		}
	})

	t.Run("override for second type in list", func(t *testing.T) {
		got := ud.ResolvePropertyDefault("bug", "priority")
		if got != "high" {
			t.Errorf("expected 'high', got %q", got)
		}
	})

	t.Run("override-only property", func(t *testing.T) {
		got := ud.ResolvePropertyDefault("ticket", "status")
		if got != "triaged" {
			t.Errorf("expected 'triaged', got %q", got)
		}
	})

	t.Run("different override group", func(t *testing.T) {
		got := ud.ResolvePropertyDefault("decision", "status")
		if got != "proposed" {
			t.Errorf("expected 'proposed', got %q", got)
		}
	})

	t.Run("unknown property returns empty", func(t *testing.T) {
		got := ud.ResolvePropertyDefault("ticket", "nonexistent")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("nil UserDefaults returns empty", func(t *testing.T) {
		var nilUD *UserDefaults
		got := nilUD.ResolvePropertyDefault("ticket", "priority")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})
}

func TestUserDefaultsResolveRelationDefault(t *testing.T) {
	ud := &UserDefaults{
		RelationDefaults: map[string]string{
			"reported-by": "jeroen",
		},
		Overrides: []DefaultOverride{
			{
				Types: []string{"ticket"},
				RelationDefaults: map[string]string{
					"assigned-to": "jeroen",
				},
			},
		},
	}

	t.Run("global relation default", func(t *testing.T) {
		got := ud.ResolveRelationDefault("decision", "reported-by")
		if got != "jeroen" {
			t.Errorf("expected 'jeroen', got %q", got)
		}
	})

	t.Run("override relation default", func(t *testing.T) {
		got := ud.ResolveRelationDefault("ticket", "assigned-to")
		if got != "jeroen" {
			t.Errorf("expected 'jeroen', got %q", got)
		}
	})

	t.Run("no override for other entity types", func(t *testing.T) {
		got := ud.ResolveRelationDefault("decision", "assigned-to")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("unknown relation returns empty", func(t *testing.T) {
		got := ud.ResolveRelationDefault("ticket", "nonexistent")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("nil UserDefaults returns empty", func(t *testing.T) {
		var nilUD *UserDefaults
		got := nilUD.ResolveRelationDefault("ticket", "reported-by")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})
}

func TestFilterControlKey(t *testing.T) {
	t.Run("returns relation when set", func(t *testing.T) {
		fc := FilterControl{Relation: "belongs_to", Property: "status"}
		if got := fc.Key(); got != "belongs_to" {
			t.Errorf("expected 'belongs_to', got %q", got)
		}
	})

	t.Run("returns property when relation empty", func(t *testing.T) {
		fc := FilterControl{Property: "status"}
		if got := fc.Key(); got != "status" {
			t.Errorf("expected 'status', got %q", got)
		}
	})

	t.Run("returns empty when both empty", func(t *testing.T) {
		fc := FilterControl{}
		if got := fc.Key(); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})
}

func TestFilterControlIsRelation(t *testing.T) {
	t.Run("true when relation set", func(t *testing.T) {
		fc := FilterControl{Relation: "belongs_to"}
		if !fc.IsRelation() {
			t.Error("expected true")
		}
	})

	t.Run("false when relation empty", func(t *testing.T) {
		fc := FilterControl{Property: "status"}
		if fc.IsRelation() {
			t.Error("expected false")
		}
	})
}

func TestFilterControlQueryParamKey(t *testing.T) {
	t.Run("property filter", func(t *testing.T) {
		fc := FilterControl{Property: "status"}
		if got := fc.QueryParamKey(); got != "filter_status" {
			t.Errorf("expected 'filter_status', got %q", got)
		}
	})

	t.Run("relation filter", func(t *testing.T) {
		fc := FilterControl{Relation: "belongs_to"}
		if got := fc.QueryParamKey(); got != "filter_belongs_to" {
			t.Errorf("expected 'filter_belongs_to', got %q", got)
		}
	})
}

func TestFilterControlCurrentValue(t *testing.T) {
	t.Run("returns value when present", func(t *testing.T) {
		fc := FilterControl{Property: "status"}
		query := map[string][]string{"filter_status": {"open"}}
		if got := fc.CurrentValue(query); got != "open" {
			t.Errorf("expected 'open', got %q", got)
		}
	})

	t.Run("returns empty when not present", func(t *testing.T) {
		fc := FilterControl{Property: "status"}
		query := map[string][]string{}
		if got := fc.CurrentValue(query); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("relation filter", func(t *testing.T) {
		fc := FilterControl{Relation: "belongs_to"}
		query := map[string][]string{"filter_belongs_to": {"category-1"}}
		if got := fc.CurrentValue(query); got != "category-1" {
			t.Errorf("expected 'category-1', got %q", got)
		}
	})
}

func TestDirection_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    Direction
		wantErr string
	}{
		{
			// A written-but-empty value must stay empty, NOT collapse to
			// outgoing: empty means "infer from the metamodel", and collapsing
			// it here would let `direction: ""` bypass the ambiguity check on
			// a self-referencing relation.
			name: "empty stays empty so inference owns the decision",
			yaml: `direction: ""`,
			want: "",
		},
		{
			name: "bare key stays empty",
			yaml: `direction:`,
			want: "",
		},
		{
			name: "outgoing",
			yaml: `direction: outgoing`,
			want: DirectionOutgoing,
		},
		{
			name: "incoming",
			yaml: `direction: incoming`,
			want: DirectionIncoming,
		},
		{
			name:    "invalid direction",
			yaml:    `direction: both`,
			wantErr: `invalid direction "both"`,
		},
		{
			name:    "invalid direction sideways",
			yaml:    `direction: sideways`,
			wantErr: `invalid direction "sideways"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg struct {
				Direction Direction `yaml:"direction"`
			}
			err := yaml.Unmarshal([]byte(tt.yaml), &cfg)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.Direction != tt.want {
				t.Errorf("got %q, want %q", cfg.Direction, tt.want)
			}
		})
	}
}

func TestKanbanCardField_Unmarshal(t *testing.T) {
	// A single card.fields list mixing the legacy property-only shape with
	// the new relation shapes must all unmarshal into KanbanCardField.
	const src = `
title: title
fields:
  - property: status
  - property: priority
    label: Priority
  - relation: verantwoordelijk_voor
    direction: incoming
  - relation: blocks
`
	var card KanbanCard
	if err := yaml.Unmarshal([]byte(src), &card); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if card.Title != "title" {
		t.Errorf("title: got %q, want %q", card.Title, "title")
	}
	if len(card.Fields) != 4 {
		t.Fatalf("fields: got %d, want 4", len(card.Fields))
	}

	// Legacy property-only field.
	if got := card.Fields[0]; got.Property != "status" || got.Relation != "" {
		t.Errorf("fields[0]: got %+v, want property=status", got)
	}
	// Property field with label.
	if got := card.Fields[1]; got.Property != "priority" || got.Label != "Priority" {
		t.Errorf("fields[1]: got %+v, want property=priority label=Priority", got)
	}
	// Incoming relation field: direction decodes via Direction.UnmarshalYAML.
	if got := card.Fields[2]; got.Relation != "verantwoordelijk_voor" || !got.Direction.IsIncoming() {
		t.Errorf("fields[2]: got %+v, want relation=verantwoordelijk_voor direction=incoming", got)
	}
	// Relation field with no direction: the key is absent so UnmarshalYAML
	// never runs and the zero-value Direction ("") stands — treated as
	// outgoing everywhere (IsIncoming is false).
	if got := card.Fields[3]; got.Relation != "blocks" || got.Direction.IsIncoming() {
		t.Errorf("fields[3]: got %+v, want relation=blocks direction=outgoing", got)
	}
}

func TestListHeaderFooter_YAMLAndJSON(t *testing.T) {
	// header/footer decode from YAML and serialize to JSON so the SPA (which
	// resolves the header/description alias client-side) receives both fields.
	const src = `
entity_type: risico
title: Risicoregister
header: |
  This register is **ISO 27001** scope.
footer: See the security officer.
`
	var list List
	if err := yaml.Unmarshal([]byte(src), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, want := list.Header, "This register is **ISO 27001** scope.\n"; got != want {
		t.Errorf("header: got %q, want %q", got, want)
	}
	if got, want := list.Footer, "See the security officer."; got != want {
		t.Errorf("footer: got %q, want %q", got, want)
	}

	data, err := json.Marshal(list)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	if _, ok := out["header"]; !ok {
		t.Errorf("json missing header key: %s", data)
	}
	if _, ok := out["footer"]; !ok {
		t.Errorf("json missing footer key: %s", data)
	}
}

func TestList_EmptyHeaderFooterOmittedFromJSON(t *testing.T) {
	// omitempty keeps the wire payload clean when no info regions are set, so
	// the SPA renders nothing and legacy lists are unaffected.
	data, err := json.Marshal(List{EntityType: "risico", Title: "Risicoregister"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "header") || strings.Contains(string(data), "footer") {
		t.Errorf("expected no header/footer keys, got %s", data)
	}
}

func TestKanbanHeaderFooter_YAMLAndJSON(t *testing.T) {
	// Kanban info regions mirror the list ones: they decode from YAML and reach
	// the SPA over _config, which resolves and renders them client-side.
	const src = `
entity_type: ticket
title: Ticket Board
column_property: status
header: |
  Cards move **left to right**.
footer: Ask the maintainers before reopening a done ticket.
`
	var board Kanban
	if err := yaml.Unmarshal([]byte(src), &board); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, want := board.Header, "Cards move **left to right**.\n"; got != want {
		t.Errorf("header: got %q, want %q", got, want)
	}
	if got, want := board.Footer, "Ask the maintainers before reopening a done ticket."; got != want {
		t.Errorf("footer: got %q, want %q", got, want)
	}

	data, err := json.Marshal(board)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	if _, ok := out["header"]; !ok {
		t.Errorf("json missing header key: %s", data)
	}
	if _, ok := out["footer"]; !ok {
		t.Errorf("json missing footer key: %s", data)
	}
}

func TestKanban_EmptyHeaderFooterOmittedFromJSON(t *testing.T) {
	// omitempty keeps the wire payload clean when no info regions are set, so
	// existing boards render exactly as before.
	data, err := json.Marshal(Kanban{EntityType: "ticket", Title: "Ticket Board"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "header") || strings.Contains(string(data), "footer") {
		t.Errorf("expected no header/footer keys, got %s", data)
	}
}

func TestDirection_IsIncoming(t *testing.T) {
	t.Run("incoming returns true", func(t *testing.T) {
		if !DirectionIncoming.IsIncoming() {
			t.Error("expected true")
		}
	})

	t.Run("outgoing returns false", func(t *testing.T) {
		if DirectionOutgoing.IsIncoming() {
			t.Error("expected false")
		}
	})

	t.Run("empty returns false", func(t *testing.T) {
		var d Direction
		if d.IsIncoming() {
			t.Error("expected false")
		}
	})
}

func TestConfigRelationFilterDirection(t *testing.T) {
	cfg := &Config{
		Lists: map[string]List{
			"taken": {
				EntityType: "taak",
				FilterControls: []FilterControl{
					{Relation: "verantwoordelijk_voor", Direction: DirectionIncoming},
					{Relation: "belongs_to"}, // default outgoing
				},
			},
			"other_type": {
				EntityType:     "persoon",
				FilterControls: []FilterControl{{Relation: "verantwoordelijk_voor"}},
			},
		},
		// A board fetches through the list endpoint too: its controls count,
		// after every list (BUG-GEMNW6).
		Kanbans: map[string]Kanban{
			"bord": {
				EntityType: "taak",
				FilterControls: []FilterControl{
					{Relation: "toegewezen_aan", Direction: DirectionIncoming},
					{Relation: "belongs_to", Direction: DirectionIncoming}, // conflicts with the list
				},
			},
		},
	}

	tests := []struct {
		name       string
		entityType string
		relation   string
		wantDir    Direction
		wantOK     bool
	}{
		{"incoming resolves", "taak", "verantwoordelijk_voor", DirectionIncoming, true},
		{"outgoing default resolves", "taak", "belongs_to", DirectionOutgoing, true},
		{"other entity type isolated", "persoon", "verantwoordelijk_voor", DirectionOutgoing, true},
		{"control only on a kanban resolves", "taak", "toegewezen_aan", DirectionIncoming, true},
		{"unknown relation for type", "taak", "missing", DirectionOutgoing, false},
		{"unknown entity type", "widget", "belongs_to", DirectionOutgoing, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, ok := cfg.RelationFilterDirection(tt.entityType, tt.relation)
			if dir != tt.wantDir || ok != tt.wantOK {
				t.Errorf("RelationFilterDirection(%q, %q) = (%q, %v), want (%q, %v)",
					tt.entityType, tt.relation, dir, ok, tt.wantDir, tt.wantOK)
			}
		})
	}
}

// TestConfigRelationFilterDirection_DeterministicOverConflicts pins RR-9MJRJG:
// when two lists of the same entity type configure the same relation with
// conflicting directions, RelationFilterDirection must resolve to the lowest
// list ID deterministically — NOT randomly over map iteration order. Running
// many times must always yield the same answer.
func TestConfigRelationFilterDirection_DeterministicOverConflicts(t *testing.T) {
	cfg := &Config{
		Lists: map[string]List{
			// "aaa" < "zzz": lowest list ID wins → outgoing.
			"zzz": {
				EntityType:     "taak",
				FilterControls: []FilterControl{{Relation: "belongs_to", Direction: DirectionIncoming}},
			},
			"aaa": {
				EntityType:     "taak",
				FilterControls: []FilterControl{{Relation: "belongs_to", Direction: DirectionOutgoing}},
			},
		},
	}

	// Repeat enough times that map-iteration randomization would surface a
	// flip if the resolver weren't sorted.
	for i := range 100 {
		dir, ok := cfg.RelationFilterDirection("taak", "belongs_to")
		if !ok || dir != DirectionOutgoing {
			t.Fatalf("iteration %d: got (%q, %v), want (outgoing, true) — non-deterministic resolution", i, dir, ok)
		}
	}
}

// TestConfigHasPropertyFilterControl pins the discriminator helper used to
// resolve a property/relation name collision in favor of an explicit property
// control (RR-0HWAS0).
func TestConfigHasPropertyFilterControl(t *testing.T) {
	cfg := &Config{
		Lists: map[string]List{
			"taken": {
				EntityType: "taak",
				FilterControls: []FilterControl{
					{Property: "belongs_to"}, // property control sharing a relation name
					{Relation: "verantwoordelijk_voor"},
				},
			},
		},
	}
	if !cfg.HasPropertyFilterControl("taak", "belongs_to") {
		t.Error("HasPropertyFilterControl(taak, belongs_to) = false, want true")
	}
	if cfg.HasPropertyFilterControl("taak", "verantwoordelijk_voor") {
		t.Error("HasPropertyFilterControl(taak, verantwoordelijk_voor) = true (it's a relation control), want false")
	}
	if cfg.HasPropertyFilterControl("persoon", "belongs_to") {
		t.Error("HasPropertyFilterControl for wrong type = true, want false")
	}
}
