package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/testutil"
)

// list_test.go only covers CLI-specific behavior: entity-type resolution
// via aliases/plurals. Pure graph iteration (ListByType / AllNodes /
// empty-store) is covered by the store conformance suite in
// internal/store/storetest/query.go and does not need to be duplicated
// here.

func TestResolveEntityTypeWithAlias(t *testing.T) {
	meta, err := metamodel.Parse([]byte(testutil.AliasMetamodelYAML()))
	if err != nil {
		t.Fatalf("failed to parse metamodel: %v", err)
	}

	tests := []struct {
		name      string
		input     string
		wantType  string
		wantError bool
	}{
		{name: "canonical name", input: "requirement", wantType: "requirement"},
		{name: "alias req", input: "req", wantType: "requirement"},
		{name: "plural form", input: "requirements", wantType: "requirement"},
		{name: "alias ctrl", input: "ctrl", wantType: "control"},
		{name: "plural controls", input: "controls", wantType: "control"},
		{name: "unknown type", input: "unknown", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := resolveEntityType(meta, tt.input)
			if tt.wantError {
				if err == nil {
					t.Errorf("resolveEntityType(%q) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Errorf("resolveEntityType(%q) unexpected error: %v", tt.input, err)
				return
			}
			if resolved != tt.wantType {
				t.Errorf("resolveEntityType(%q) = %q, want %q", tt.input, resolved, tt.wantType)
			}
		})
	}
}

// TestListTypeParsingEdgeCases tests edge cases for entity type resolution
// including entity types and aliases that end in 's' (like "bus", "autobus").
func TestListTypeParsingEdgeCases(t *testing.T) {
	meta := testutil.NewMetamodel().
		DefineEntity("requirement").
		Label("Requirement").
		IDPrefix("REQ-").
		Aliases("req").
		Prop("title", metamodel.PropertyTypeString, true).
		Prop("status", "status", true).
		End().
		DefineEntity("bus").
		Label("Bus").
		IDPrefix("BUS-").
		Aliases("autobus").
		Prop("title", metamodel.PropertyTypeString, true).
		End().
		WithCustomTypeDefault("status", []string{"draft", "accepted"}, "draft").
		Build()

	tests := []struct {
		name      string
		input     string
		wantType  string
		wantError bool
	}{
		{name: "canonical name requirement", input: "requirement", wantType: "requirement"},
		{name: "alias req", input: "req", wantType: "requirement"},
		{name: "plural requirements", input: "requirements", wantType: "requirement"},
		{name: "canonical name bus (ends in s)", input: "bus", wantType: "bus"},
		{name: "alias autobus (ends in s)", input: "autobus", wantType: "bus"},
		{name: "plural buses", input: "buses", wantType: "bus"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := resolveEntityType(meta, tt.input)
			if tt.wantError {
				if err == nil {
					t.Errorf("resolveEntityType(%q) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Errorf("resolveEntityType(%q) unexpected error: %v", tt.input, err)
				return
			}
			if resolved != tt.wantType {
				t.Errorf("resolveEntityType(%q) = %q, want %q", tt.input, resolved, tt.wantType)
			}
		})
	}
}

func TestListCommandWithUnknownType(t *testing.T) {
	meta := metamodel.DefaultMetamodel()

	_, err := resolveEntityType(meta, "nonexistent")
	if err == nil {
		t.Error("expected error for unknown entity type")
	}
	if !strings.Contains(err.Error(), "unknown entity type") {
		t.Errorf("expected 'unknown entity type' in error, got: %v", err)
	}
}

func filterTestMeta(t *testing.T) *metamodel.Metamodel {
	t.Helper()
	m, err := metamodel.Parse([]byte(`
entities:
  ticket:
    label: Ticket
    id_prefix: T
    properties:
      title: { type: string }
      status: { type: string }
      priority: { type: integer }
`))
	if err != nil {
		t.Fatalf("parse metamodel: %v", err)
	}
	return m
}

func tkt(id, title, status string, priority int) *entity.Entity {
	e := entity.New(id, "ticket")
	e.Properties["title"] = title
	e.Properties["status"] = status
	e.Properties["priority"] = priority
	return e
}

// TestApplyListFilters covers --filter (predicate), --where (legacy,
// transpiled), their combination, and an `or` the old syntax can't express.
func TestApplyListFilters(t *testing.T) {
	meta := filterTestMeta(t)
	all := []*entity.Entity{
		tkt("T-1", "alpha", "ready", 1),
		tkt("T-2", "beta", "done", 5),
		tkt("T-3", "gamma", "ready", 9),
	}
	ids := func(es []*entity.Entity) []string {
		out := make([]string, len(es))
		for i, e := range es {
			out[i] = e.ID
		}
		return out
	}
	eq := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	tests := []struct {
		name   string
		where  []string
		filter string
		want   []string
	}{
		{"where equality", []string{"status=ready"}, "", []string{"T-1", "T-3"}},
		{"where numeric ordering", []string{"priority>4"}, "", []string{"T-2", "T-3"}},
		{"filter predicate eq", nil, "entity.status == 'ready'", []string{"T-1", "T-3"}},
		{"filter or (impossible in --where)", nil,
			"entity.status == 'done' or entity.priority > 8", []string{"T-2", "T-3"}},
		{"filter negative-literal-free numeric", nil, "entity.priority >= 5", []string{"T-2", "T-3"}},
		{"where + filter combined (ANDed)", []string{"status=ready"}, "entity.priority > 5", []string{"T-3"}},
		{"no filters returns all", nil, "", []string{"T-1", "T-2", "T-3"}},
		{"filter value selection", nil,
			"(entity.priority > 4 and 'high' or 'low') == 'high'", []string{"T-2", "T-3"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := applyListFilters(context.Background(), all, tc.where, tc.filter, "ticket", meta, nil)
			if err != nil {
				t.Fatalf("applyListFilters: %v", err)
			}
			if !eq(ids(got), tc.want) {
				t.Errorf("got %v, want %v", ids(got), tc.want)
			}
		})
	}
}

// TestApplyListFilters_Errors pins clear errors for a bad --filter and
// missing entity type.
func TestApplyListFilters_Errors(t *testing.T) {
	meta := filterTestMeta(t)
	all := []*entity.Entity{tkt("T-1", "a", "ready", 1)}
	if _, err := applyListFilters(context.Background(), all, nil, "entity.nope == 'x'", "ticket", meta, nil); err == nil {
		t.Error("expected error for unknown property in --filter")
	}
	if _, err := applyListFilters(context.Background(), all, []string{"status=ready"}, "", "", meta, nil); err == nil {
		t.Error("expected error when entity type omitted")
	}
}
