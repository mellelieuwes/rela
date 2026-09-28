package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"

	mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
	"github.com/Sourcehaven-BV/rela/internal/testutil"
)

// makeTestFixture returns the shared metamodel and a store seeded with
// the canonical test graph (3 requirements, 1 decision, 1 relation).
// Used by makeTestServer (handler-level tests) and the dispatch tests
// (which build the server through the production NewServer instead).
func makeTestFixture(t *testing.T) (*metamodel.Metamodel, *memstore.MemStore) {
	t.Helper()

	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"requirement": {
				Label:    "Requirement",
				IDPrefix: "REQ",
				Properties: map[string]metamodel.PropertyDef{
					"title":    {Type: "string", Required: true},
					"status":   {Type: "string"},
					"priority": {Type: "string"},
				},
			},
			"decision": {
				Label:    "Decision",
				IDPrefix: "DEC",
				Properties: map[string]metamodel.PropertyDef{
					"title":  {Type: "string", Required: true},
					"status": {Type: "string"},
				},
			},
		},
		Relations: map[string]metamodel.RelationDef{
			"addresses": {
				Label: "addresses",
				From:  []string{"decision"},
				To:    []string{"requirement"},
			},
		},
	}

	st := memstore.New()
	ctx := context.Background()
	for _, e := range []*entity.Entity{
		testutil.EntityFor(meta, "requirement").ID("REQ-001").With("status", "accepted").Build(),
		testutil.EntityFor(meta, "requirement").ID("REQ-002").With("status", "draft").Build(),
		testutil.EntityFor(meta, "requirement").ID("REQ-003").With("status", "accepted").Build(),
		testutil.EntityFor(meta, "decision").ID("DEC-001").With("status", "accepted").Build(),
	} {
		if err := st.CreateEntity(ctx, e); err != nil {
			t.Fatalf("seed entity %s: %v", e.ID, err)
		}
	}
	if _, err := st.CreateRelation(ctx, "DEC-001", "addresses", "REQ-001", nil); err != nil {
		t.Fatalf("seed relation: %v", err)
	}

	return meta, st
}

// makeTestServer creates a Server with a populated store for handler testing.
func makeTestServer(t *testing.T) *Server {
	t.Helper()
	srv, _ := makeTestServerWithStore(t)
	return srv
}

// makeTestServerWithStore additionally returns the backing store, for the few
// tests that need to seed extra rows. Deps.Store is the narrow read-only
// GraphReader (writes go through EntityManager), so a test cannot reach a
// writer through the server — by design.
func makeTestServerWithStore(t *testing.T) (*Server, *memstore.MemStore) {
	t.Helper()

	meta, st := makeTestFixture(t)
	deps := newTestDeps(t, meta, st)
	srv := &Server{logger: slog.New(slog.DiscardHandler)}
	setDeps(srv, deps)
	return srv, st
}

func getResultText(t *testing.T, result *mcpgo.CallToolResult) string {
	t.Helper()
	return result.Content[0].(*mcpgo.TextContent).Text
}

func isErrorResult(result *mcpgo.CallToolResult) bool {
	return result.IsError
}

// --- Entity handler tests ---

func TestHandleListEntities_All(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	result, err := s.handleListEntities(context.Background(), &mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entities := decodeEntityPage(t, getResultText(t, result)).Entities
	if len(entities) != 4 {
		t.Errorf("expected 4 entities, got %d", len(entities))
	}
}

func TestHandleListEntities_ByType(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"type": "requirement"})
	result, err := s.handleListEntities(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entities := decodeEntityPage(t, getResultText(t, result)).Entities
	if len(entities) != 3 {
		t.Errorf("expected 3 requirements, got %d", len(entities))
	}
}

func TestHandleListEntities_WithFilter(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{
		"type":   "requirement",
		"filter": "entity.status == 'accepted'",
	})
	result, err := s.handleListEntities(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entities := decodeEntityPage(t, getResultText(t, result)).Entities
	if len(entities) != 2 {
		t.Errorf("expected 2 accepted requirements, got %d", len(entities))
	}
}

func TestHandleListEntities_WithPagination(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{
		"limit":  float64(2),
		"offset": float64(1),
	})
	result, err := s.handleListEntities(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entities := decodeEntityPage(t, getResultText(t, result)).Entities
	if len(entities) != 2 {
		t.Errorf("expected 2 entities with limit=2 offset=1, got %d", len(entities))
	}
	if page := decodeEntityPage(t, getResultText(t, result)); page.Total != 4 || !page.HasMore {
		t.Errorf("expected total 4 and has_more, got %+v", page)
	}
}

func TestHandleListEntities_Errors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"unknown type", map[string]any{"type": "nonsense"}, "unknown entity type"},
		{"filter without type", map[string]any{"filter": "entity.status == 'x'"}, "filter requires type"},
		{"unknown property", map[string]any{"type": "requirement", "filter": "entity.nope == 'x'"}, "invalid filter"},
		{"unknown related property", map[string]any{
			"type": "decision", "filter": "related(entity, 'addresses', { nope = 'x' })",
		}, "has no property"},
		{"unknown relation", map[string]any{"type": "decision", "filter": "related(entity, 'nope')"}, "invalid filter"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := makeTestServer(t)
			result, err := s.handleListEntities(context.Background(), makeToolRequest(tc.args))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			text := getResultText(t, result)
			if !isErrorResult(result) || !strings.Contains(text, tc.want) {
				t.Errorf("want error containing %q, got %s", tc.want, text)
			}
		})
	}
}

func TestHandleListEntities_RelatedFilter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		filter string
		want   []string
	}{
		{"has relation", "related(entity, 'addresses')", []string{"DEC-001"}},
		{"target property matches", "related(entity, 'addresses', { status = 'accepted' })", []string{"DEC-001"}},
		{"target property differs", "related(entity, 'addresses', { status = 'draft' })", nil},
		{"negated", "not related(entity, 'addresses')", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := makeTestServer(t)
			result, err := s.handleListEntities(context.Background(), makeToolRequest(map[string]any{
				"type": "decision", "filter": tc.filter,
			}))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if isErrorResult(result) {
				t.Fatalf("unexpected error result: %s", getResultText(t, result))
			}
			var got []string
			for _, e := range decodeEntityPage(t, getResultText(t, result)).Entities {
				got = append(got, e.ID)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestHandleListEntities_RelatedRefusedWithoutBinder pins the fallback: with
// no traversal binder wired, a related(...) filter is an error, never an
// ungated answer.
func TestHandleListEntities_RelatedRefusedWithoutBinder(t *testing.T) {
	t.Parallel()
	meta, st := makeTestFixture(t)
	deps := newTestDeps(t, meta, st)
	deps.Traversals = nil
	s := &Server{logger: slog.New(slog.DiscardHandler)}
	setDeps(s, deps)

	result, err := s.handleListEntities(context.Background(), makeToolRequest(map[string]any{
		"type": "decision", "filter": "related(entity, 'addresses')",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text := getResultText(t, result); !isErrorResult(result) || !strings.Contains(text, "related(...) is not supported") {
		t.Errorf("want a refusal, got %s", text)
	}
}

func TestHandleListEntities_DisplayTitle(t *testing.T) {
	t.Parallel()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"asset": {
				IDPrefix:        "ASSET",
				DisplayProperty: "naam",
				Properties:      map[string]metamodel.PropertyDef{"naam": {Type: "string"}},
			},
		},
	}
	st := memstore.New()
	e := &entity.Entity{ID: "ASSET-1", Type: "asset", Properties: map[string]any{"naam": "Hetzner"}}
	if err := st.CreateEntity(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	srv := &Server{logger: slog.New(slog.DiscardHandler)}
	setDeps(srv, newTestDeps(t, meta, st))

	result, err := srv.handleListEntities(context.Background(), &mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatal(err)
	}
	entities := decodeEntityPage(t, getResultText(t, result)).Entities
	if len(entities) != 1 || entities[0].Title != "Hetzner" {
		t.Errorf("summary did not resolve display_property: %+v", entities)
	}
}

// entityPage and relationPage mirror the paged list results.
type entityPage struct {
	Total    int             `json:"total"`
	HasMore  bool            `json:"has_more"`
	Entities []entitySummary `json:"entities"`
}

type relationPage struct {
	Total     int            `json:"total"`
	HasMore   bool           `json:"has_more"`
	Relations []relationJSON `json:"relations"`
}

func decodeEntityPage(t *testing.T, text string) entityPage {
	t.Helper()
	var p entityPage
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		t.Fatalf("failed to parse JSON %s: %v", text, err)
	}
	return p
}

func decodeRelationPage(t *testing.T, text string) relationPage {
	t.Helper()
	var p relationPage
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		t.Fatalf("failed to parse JSON %s: %v", text, err)
	}
	return p
}

func TestHandleShowEntity(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"id": "REQ-001"})
	result, err := s.handleShowEntity(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := getResultText(t, result)
	if !strings.Contains(text, "REQ-001") {
		t.Error("expected result to contain entity ID")
	}
	if !strings.Contains(text, "title") {
		t.Error("expected result to contain title property")
	}
}

func TestHandleShowEntity_NotFound(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"id": "NONEXISTENT"})
	result, err := s.handleShowEntity(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isErrorResult(result) {
		t.Error("expected error result for nonexistent entity")
	}
	text := getResultText(t, result)
	if !strings.Contains(text, "entity not found") {
		t.Errorf("expected 'entity not found' error, got %s", text)
	}
}

func TestHandleSearchEntities(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"query": "accepted"})
	result, err := s.handleSearchEntities(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var entities []entitySummary
	if err := json.Unmarshal([]byte(getResultText(t, result)), &entities); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	// Should match REQ-001, REQ-003, and DEC-001 (all have status=accepted)
	if len(entities) < 1 {
		t.Errorf("expected at least 1 match, got %d", len(entities))
	}
}

func TestHandleSearchEntities_ByType(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{
		"query": "accepted",
		"type":  "decision",
	})
	result, err := s.handleSearchEntities(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var entities []entitySummary
	if err := json.Unmarshal([]byte(getResultText(t, result)), &entities); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	// Only DEC-001 is a decision with "accepted"
	if len(entities) != 1 {
		t.Errorf("expected 1 decision matching 'accepted', got %d", len(entities))
	}
}

func TestHandleUpdateEntity_NoUpdates(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"id": "REQ-001"})
	result, err := s.handleUpdateEntity(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isErrorResult(result) {
		t.Error("expected error when no updates specified")
	}
	text := getResultText(t, result)
	if !strings.Contains(text, "no updates specified") {
		t.Errorf("expected 'no updates specified' error, got %s", text)
	}
}

func TestHandleUpdateEntity_NotFound(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{
		"id":         "NONEXISTENT",
		"properties": map[string]any{"title": "new"},
	})
	result, err := s.handleUpdateEntity(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isErrorResult(result) {
		t.Error("expected error for nonexistent entity")
	}
}

func TestHandleUpdateEntity_DeletesPropertyOnNil(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	// REQ-001 starts with status=accepted; null should remove it.
	req := makeToolRequest(map[string]any{
		"id":         "REQ-001",
		"properties": map[string]any{"status": nil},
	})
	result, err := s.handleUpdateEntity(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isErrorResult(result) {
		t.Fatalf("expected success, got error: %s", getResultText(t, result))
	}
	updated, getErr := s.deps().Store.GetEntity(context.Background(), "REQ-001")
	if getErr != nil {
		t.Fatalf("get entity: %v", getErr)
	}
	if _, present := updated.Properties["status"]; present {
		t.Errorf("expected status to be removed, but it is still present: %v", updated.Properties["status"])
	}
}

func TestHandleUpdateEntity_DeleteOnlyCallSurvivesGuard(t *testing.T) {
	t.Parallel()
	// AC 7: a delete-only call must NOT trigger the "no updates specified" guard.
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{
		"id":         "REQ-001",
		"properties": map[string]any{"status": nil},
	})
	result, err := s.handleUpdateEntity(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isErrorResult(result) {
		text := getResultText(t, result)
		if strings.Contains(text, "no updates specified") {
			t.Fatalf("delete-only call wrongly hit the no-updates-specified guard: %s", text)
		}
		t.Fatalf("unexpected error: %s", text)
	}
}

func TestHandleUpdateEntity_DeleteAbsentPropertyIsNoOp(t *testing.T) {
	t.Parallel()
	// `priority` is in the metamodel but not set on REQ-001; deleting it should be a no-op.
	s := makeTestServer(t)
	before, _ := s.deps().Store.GetEntity(context.Background(), "REQ-001")
	if _, present := before.Properties["priority"]; present {
		t.Fatalf("test setup: REQ-001 should not have a priority property")
	}

	req := makeToolRequest(map[string]any{
		"id":         "REQ-001",
		"properties": map[string]any{"priority": nil},
	})
	result, err := s.handleUpdateEntity(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isErrorResult(result) {
		t.Fatalf("expected success on no-op delete, got error: %s", getResultText(t, result))
	}
	after, _ := s.deps().Store.GetEntity(context.Background(), "REQ-001")
	if _, present := after.Properties["priority"]; present {
		t.Errorf("priority should remain absent")
	}
	if after.GetString("status") != before.GetString("status") {
		t.Errorf("status changed unexpectedly: was %q, now %q", before.GetString("status"), after.GetString("status"))
	}
}

func TestHandleUpdateEntity_DeleteRequiredPropertyRejected(t *testing.T) {
	t.Parallel()
	// `title` is required; attempting to delete it must surface an actionable error
	// rather than silently producing a now-invalid entity.
	s := makeTestServer(t)
	before, _ := s.deps().Store.GetEntity(context.Background(), "REQ-001")
	beforeTitle := before.GetString("title")

	req := makeToolRequest(map[string]any{
		"id":         "REQ-001",
		"properties": map[string]any{"title": nil},
	})
	result, err := s.handleUpdateEntity(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isErrorResult(result) {
		t.Fatalf("expected error when deleting required property, got success")
	}
	if !strings.Contains(getResultText(t, result), "required") {
		t.Errorf("expected 'required' in error message, got %s", getResultText(t, result))
	}
	// Entity must be unchanged.
	after, _ := s.deps().Store.GetEntity(context.Background(), "REQ-001")
	if after.GetString("title") != beforeTitle {
		t.Errorf("entity must be unchanged after rejected delete: title was %q, now %q", beforeTitle, after.GetString("title"))
	}
}

func TestHandleUpdateEntity_JSONStringPropertiesNullDeletes(t *testing.T) {
	t.Parallel()
	// End-to-end check that the JSON-string `properties` fallback also supports null-as-delete.
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{
		"id":         "REQ-001",
		"properties": `{"status": null}`,
	})
	result, err := s.handleUpdateEntity(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isErrorResult(result) {
		t.Fatalf("expected success, got error: %s", getResultText(t, result))
	}
	updated, _ := s.deps().Store.GetEntity(context.Background(), "REQ-001")
	if _, present := updated.Properties["status"]; present {
		t.Errorf("status should be removed when sent as JSON string with null")
	}
}

func TestHandleUpdateEntity_DeleteUnknownPropertyRejected(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{
		"id":         "REQ-001",
		"properties": map[string]any{"unknown_prop": nil},
	})
	result, err := s.handleUpdateEntity(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isErrorResult(result) {
		t.Error("expected error for unknown property name")
	}
	if !strings.Contains(getResultText(t, result), "unknown properties") {
		t.Errorf("expected 'unknown properties' error, got %s", getResultText(t, result))
	}
}

func TestHandleUpdateEntity_MixedSetAndUnset(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{
		"id": "REQ-001",
		"properties": map[string]any{
			"status": nil,           // delete
			"title":  "Renamed Req", // set
		},
	})
	result, err := s.handleUpdateEntity(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isErrorResult(result) {
		t.Fatalf("expected success, got error: %s", getResultText(t, result))
	}
	updated, _ := s.deps().Store.GetEntity(context.Background(), "REQ-001")
	if _, present := updated.Properties["status"]; present {
		t.Errorf("status should be removed")
	}
	if got := updated.GetString("title"); got != "Renamed Req" {
		t.Errorf("expected title 'Renamed Req', got %q", got)
	}
}

func TestHandleUpdateEntity_EmptyStringIsNoOp(t *testing.T) {
	t.Parallel()
	// AC 8: empty string is silently filtered, so it must NOT delete an existing value
	// AND it must NOT itself satisfy the "no updates specified" guard alone.
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{
		"id":         "REQ-001",
		"properties": map[string]any{"status": ""},
	})
	result, err := s.handleUpdateEntity(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Empty-string-only properties get filtered to an empty set, so the guard fires.
	if !isErrorResult(result) {
		t.Fatalf("expected 'no updates specified' since empty string is filtered, got success")
	}
	if !strings.Contains(getResultText(t, result), "no updates specified") {
		t.Errorf("expected 'no updates specified', got %s", getResultText(t, result))
	}
	// And the existing status is untouched.
	updated, _ := s.deps().Store.GetEntity(context.Background(), "REQ-001")
	if got := updated.GetString("status"); got != "accepted" {
		t.Errorf("status should remain 'accepted', got %q", got)
	}
}

func TestHandleUpdateEntity_SetAndOverwriteStillWorks(t *testing.T) {
	t.Parallel()
	// AC 3: regression guard for the existing positive set/overwrite path.
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{
		"id":         "REQ-001",
		"properties": map[string]any{"status": "rejected"},
	})
	result, err := s.handleUpdateEntity(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isErrorResult(result) {
		t.Fatalf("expected success, got error: %s", getResultText(t, result))
	}
	updated, _ := s.deps().Store.GetEntity(context.Background(), "REQ-001")
	if got := updated.GetString("status"); got != "rejected" {
		t.Errorf("expected status 'rejected', got %q", got)
	}
}

func TestUpdateEntityToolDescriptionMentionsNullDelete(t *testing.T) {
	t.Parallel()
	const phrase = "null removes"
	tool := toolUpdateEntity()
	if !strings.Contains(strings.ToLower(tool.Description), phrase) {
		t.Errorf("tool description should mention %q, got: %q", phrase, tool.Description)
	}
	// InputSchema is raw JSON under the go-sdk, so decode before asserting.
	raw, ok := tool.InputSchema.(json.RawMessage)
	if !ok {
		t.Fatalf("InputSchema is %T, want json.RawMessage", tool.InputSchema)
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode input schema: %v", err)
	}
	propsSchema, ok := schema.Properties["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties schema not found or wrong type: %#v", schema.Properties["properties"])
	}
	desc, _ := propsSchema["description"].(string)
	if !strings.Contains(strings.ToLower(desc), phrase) {
		t.Errorf("properties arg description should mention %q, got: %q", phrase, desc)
	}
}

func TestHandleDeleteEntity_NotFound(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"id": "NONEXISTENT"})
	result, err := s.handleDeleteEntity(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isErrorResult(result) {
		t.Error("expected error for nonexistent entity")
	}
}

func TestHandleDeleteEntity_NoCascade(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	// DEC-001 has a relation, so delete without cascade should fail
	req := makeToolRequest(map[string]any{"id": "DEC-001", "cascade": false})
	result, err := s.handleDeleteEntity(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isErrorResult(result) {
		t.Error("expected error when deleting entity with relations and cascade=false")
	}
	text := getResultText(t, result)
	if !strings.Contains(text, "relation(s)") {
		t.Errorf("expected relation count in error, got %s", text)
	}
}

// --- Relation handler tests ---

func TestHandleListRelations_All(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	result, err := s.handleListRelations(context.Background(), &mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rels := decodeRelationPage(t, getResultText(t, result)).Relations
	if len(rels) != 1 {
		t.Errorf("expected 1 relation, got %d", len(rels))
	}
}

func TestHandleListRelations_ByType(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"type": "addresses"})
	result, err := s.handleListRelations(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rels := decodeRelationPage(t, getResultText(t, result)).Relations
	if len(rels) != 1 {
		t.Errorf("expected 1 addresses relation, got %d", len(rels))
	}
}

func TestHandleListRelations_ByFrom(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"from": "DEC-001"})
	result, err := s.handleListRelations(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rels := decodeRelationPage(t, getResultText(t, result)).Relations
	if len(rels) != 1 {
		t.Errorf("expected 1 relation from DEC-001, got %d", len(rels))
	}
}

func TestHandleListRelations_NoMatch(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"to": "REQ-003"})
	result, err := s.handleListRelations(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	page := decodeRelationPage(t, getResultText(t, result))
	if page.Total != 0 || len(page.Relations) != 0 || page.HasMore {
		t.Errorf("expected an empty page, got %+v", page)
	}
}

func TestHandleListRelations_UnknownType(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"type": "implements"})
	result, err := s.handleListRelations(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isErrorResult(result) {
		t.Errorf("an unknown relation type must be an error, not an empty list: %s", getResultText(t, result))
	}
}

func TestHandleListRelations_Pagination(t *testing.T) {
	t.Parallel()
	s, st := makeTestServerWithStore(t)
	// Add another relation for pagination testing
	if _, err := st.CreateRelation(context.Background(), "DEC-001", "addresses", "REQ-002", nil); err != nil {
		t.Fatalf("seed relation: %v", err)
	}

	req := makeToolRequest(map[string]any{"limit": float64(1)})
	result, err := s.handleListRelations(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rels := decodeRelationPage(t, getResultText(t, result)).Relations
	if len(rels) != 1 {
		t.Errorf("expected 1 relation with limit=1, got %d", len(rels))
	}
	if page := decodeRelationPage(t, getResultText(t, result)); page.Total != 2 || !page.HasMore {
		t.Errorf("expected total 2 and has_more, got %+v", page)
	}
}

func TestHandleCreateRelation_MissingFields(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	// Missing "type".
	req := makeToolRequest(map[string]any{
		"from": "DEC-001",
		"to":   "REQ-001",
	})
	result, err := s.handleCreateRelation(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isErrorResult(result) {
		t.Error("expected error for missing type")
	}

	// Missing "from".
	req = makeToolRequest(map[string]any{
		"type": "addresses",
		"to":   "REQ-001",
	})
	result, err = s.handleCreateRelation(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isErrorResult(result) {
		t.Error("expected error for missing from")
	}

	// Missing "to".
	req = makeToolRequest(map[string]any{
		"from": "DEC-001",
		"type": "addresses",
	})
	result, err = s.handleCreateRelation(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isErrorResult(result) {
		t.Error("expected error for missing to")
	}
}

func TestHandleCreateEntity_RejectsCustomIDForShortType(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{
		"type":       "requirement",
		"id":         "my-custom-id",
		"properties": map[string]any{"title": "Nope"},
	})
	result, err := s.handleCreateEntity(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isErrorResult(result) {
		t.Fatal("expected error result for custom ID on short-ID type")
	}
	text := getResultText(t, result)
	// Pin on "custom ID" so the test fails if the message stops naming the
	// caller's input, rather than just mentioning "short" for unrelated reasons.
	for _, want := range []string{"requirement", "short", "my-custom-id", "custom ID"} {
		if !strings.Contains(text, want) {
			t.Errorf("error text %q missing %q", text, want)
		}
	}
}

func TestHandleDeleteRelation_MissingFields(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	// Missing "type".
	req := makeToolRequest(map[string]any{
		"from": "DEC-001",
		"to":   "REQ-001",
	})
	result, err := s.handleDeleteRelation(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isErrorResult(result) {
		t.Error("expected error for missing type")
	}
}

func TestHandleDeleteRelation_NotFound(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{
		"from": "REQ-001",
		"type": "nonexistent",
		"to":   "REQ-002",
	})
	result, err := s.handleDeleteRelation(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isErrorResult(result) {
		t.Error("expected error for nonexistent relation")
	}
}

// --- Trace handler tests ---

func TestHandleTrace(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"id": "REQ-001"})
	result, err := group(s, selTrace).handleTrace(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := getResultText(t, result)
	if !strings.Contains(text, "REQ-001") {
		t.Error("expected trace result to contain root ID")
	}
}

func TestHandleTrace_NotFound(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"id": "NONEXISTENT"})
	result, err := group(s, selTrace).handleTrace(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isErrorResult(result) {
		t.Error("expected error for nonexistent entity")
	}
}

func TestHandleTrace_Upstream(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"id": "REQ-001", "direction": "upstream"})
	result, err := group(s, selTrace).handleTrace(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := getResultText(t, result)
	if !strings.Contains(text, "REQ-001") || !strings.Contains(text, "DEC-001") {
		t.Errorf("expected upstream trace from REQ-001 to reach DEC-001, got %s", text)
	}
}

func TestHandleTrace_UnknownDirection(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"id": "REQ-001", "direction": "sideways"})
	result, err := group(s, selTrace).handleTrace(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isErrorResult(result) {
		t.Errorf("expected an error for an unknown direction, got %s", getResultText(t, result))
	}
}

func TestHandleFindPath(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"from": "DEC-001", "to": "REQ-001"})
	result, err := group(s, selTrace).handleFindPath(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := getResultText(t, result)
	if !strings.Contains(text, "DEC-001") || !strings.Contains(text, "REQ-001") {
		t.Error("expected path to contain both entities")
	}
}

func TestHandleFindPath_NoPath(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"from": "REQ-002", "to": "REQ-003"})
	result, err := group(s, selTrace).handleFindPath(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := getResultText(t, result)
	if !strings.Contains(text, "No path found") {
		t.Errorf("expected 'No path found' message, got %s", text)
	}
}

func TestHandleFindPath_NotFound(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"from": "NONEXISTENT", "to": "REQ-001"})
	result, err := group(s, selTrace).handleFindPath(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isErrorResult(result) {
		t.Error("expected error for nonexistent entity")
	}
}

// --- Analysis handler tests ---

func TestHandleAnalyzeOrphans(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	result, err := s.handleAnalyzeOrphans(context.Background(), &mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := getResultText(t, result)
	// REQ-002, REQ-003 are orphans (no relations)
	if !strings.Contains(text, `"check":"orphans","count":2`) {
		t.Errorf("expected two orphan entities, got %s", text)
	}
}

func TestHandleAnalyzeOrphans_ByType(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"type": "decision"})
	result, err := s.handleAnalyzeOrphans(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := getResultText(t, result)
	// DEC-001 has a relation, so no orphan decisions
	if !strings.Contains(text, "No orphan entities found") {
		t.Errorf("expected no orphan decisions, got %s", text)
	}
}

func TestHandleAnalyzeCardinality_NoViolations(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	result, err := s.handleAnalyzeCardinality(context.Background(), &mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := getResultText(t, result)
	if !strings.Contains(text, "All cardinality constraints satisfied") {
		t.Errorf("expected no violations, got %s", text)
	}
}

func TestHandleAnalyzeCardinality_WithViolation(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	// Set a minimum cardinality that won't be met
	minVal := 5
	meta := s.deps().Meta
	meta.Relations["addresses"] = metamodel.RelationDef{
		From:        []string{"decision"},
		To:          []string{"requirement"},
		MinOutgoing: &minVal,
	}
	result, err := s.handleAnalyzeCardinality(context.Background(), &mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := getResultText(t, result)
	if !strings.Contains(text, `"check":"cardinality"`) {
		t.Errorf("expected violations, got %s", text)
	}
}

func TestHandleAnalyzeProperties(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	result, err := s.handleAnalyzeProperties(context.Background(), &mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// All entities should be valid
	text := getResultText(t, result)
	if isErrorResult(result) {
		t.Errorf("unexpected error result: %s", text)
	}
}

func TestHandleAnalyzeValidations_NoRules(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	result, err := s.handleAnalyzeValidations(context.Background(), &mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := getResultText(t, result)
	if !strings.Contains(text, "No custom validation rules") {
		t.Errorf("expected 'No custom validation rules' message, got %s", text)
	}
}

// --- Schema handler tests ---

func TestHandleSchema_Overview(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	result, err := group(s, selSchemaRes).handleSchema(context.Background(), &mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed struct {
		EntityTypes   []entityTypeSummary   `json:"entity_types"`
		RelationTypes []relationTypeSummary `json:"relation_types"`
	}
	if err := json.Unmarshal([]byte(getResultText(t, result)), &parsed); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if len(parsed.EntityTypes) != 2 {
		t.Errorf("expected 2 entity types, got %d", len(parsed.EntityTypes))
	}
	if len(parsed.RelationTypes) != 1 {
		t.Errorf("expected 1 relation type, got %d", len(parsed.RelationTypes))
	}
	for _, et := range parsed.EntityTypes {
		if et.Name == "requirement" && et.Count != 3 {
			t.Errorf("requirement count = %d, want 3", et.Count)
		}
	}
}

func TestHandleSchema_EntityType(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"type": "requirements"})
	result, err := group(s, selSchemaRes).handleSchema(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := getResultText(t, result)
	var parsed entityTypeDetail
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if parsed.Name != "requirement" {
		t.Errorf("plural type name not resolved: %s", parsed.Name)
	}
	if !parsed.Properties["title"].Required {
		t.Errorf("title should be required: %s", text)
	}
	if len(parsed.Incoming) != 1 || parsed.Incoming[0].Relation != "addresses" {
		t.Errorf("expected incoming addresses relation, got %+v", parsed.Incoming)
	}
	// Zero-valued fields must not be serialized; that is what made the old
	// list_entity_types answer unusably large.
	if strings.Contains(text, "ScanCmd") || strings.Contains(text, `"list":false`) {
		t.Errorf("schema detail serializes zero values: %s", text)
	}
}

func TestHandleSchema_RelationType(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"type": "addresses"})
	result, err := group(s, selSchemaRes).handleSchema(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed relationTypeDetail
	if err := json.Unmarshal([]byte(getResultText(t, result)), &parsed); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if parsed.Name != "addresses" || parsed.Count != 1 {
		t.Errorf("unexpected relation detail: %+v", parsed)
	}
}

func TestHandleSchema_UnknownType(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := makeToolRequest(map[string]any{"type": "nonsense"})
	result, err := group(s, selSchemaRes).handleSchema(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isErrorResult(result) {
		t.Errorf("expected an error for an unknown type, got %s", getResultText(t, result))
	}
}

// TestHandleSchema_ExactNameBeforePlural pins the lookup order: a relation
// named like a plural of an entity type resolves to the relation.
func TestHandleSchema_ExactNameBeforePlural(t *testing.T) {
	t.Parallel()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"test": {IDPrefix: "T", Properties: map[string]metamodel.PropertyDef{"title": {Type: "string"}}},
		},
		Relations: map[string]metamodel.RelationDef{
			"tests": {From: []string{"test"}, To: []string{"test"}},
		},
		Validations: []metamodel.ValidationRule{
			{Name: "global-rule"},
			{Name: "other-type", EntityType: "elsewhere", Description: "not for test"},
		},
	}
	srv := &Server{logger: slog.New(slog.DiscardHandler)}
	setDeps(srv, newTestDeps(t, meta, memstore.New()))

	result, err := group(srv, selSchemaRes).handleSchema(context.Background(), makeToolRequest(map[string]any{"type": "tests"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var rel relationTypeDetail
	if jsonErr := json.Unmarshal([]byte(getResultText(t, result)), &rel); jsonErr != nil || rel.From == nil {
		t.Errorf("tests should resolve to the relation, got %s", getResultText(t, result))
	}

	// A rule with no entity type applies to every type, and a rule with no
	// description is listed by name.
	result, err = group(srv, selSchemaRes).handleSchema(context.Background(), makeToolRequest(map[string]any{"type": "test"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var detail entityTypeDetail
	if err := json.Unmarshal([]byte(getResultText(t, result)), &detail); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if !slices.Equal(detail.Validations, []string{"global-rule"}) {
		t.Errorf("validations = %v, want [global-rule]", detail.Validations)
	}
}

func TestHandleListEntities_NonPositiveLimitUsesDefault(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	for _, limit := range []float64{0, -1} {
		result, err := s.handleListEntities(context.Background(), makeToolRequest(map[string]any{"limit": limit}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// The fixture has 4 entities, under the default of 50; the point is
		// that the call succeeds and returns the page rather than erroring.
		if page := decodeEntityPage(t, getResultText(t, result)); len(page.Entities) != page.Total {
			t.Errorf("limit %v: got %d of %d", limit, len(page.Entities), page.Total)
		}
	}
	if got := limitArg(newToolRequest(makeToolRequest(map[string]any{"limit": float64(-5)})), 7); got != 7 {
		t.Errorf("limitArg(-5) = %d, want the default 7", got)
	}
}

func TestServerInstructions(t *testing.T) {
	t.Parallel()
	meta := &metamodel.Metamodel{Entities: map[string]metamodel.EntityDef{
		"ticket10": {}, "ticket2": {}, "bug": {},
	}}
	got := serverInstructions(meta)
	if !strings.Contains(got, "Entity types: bug, ticket2, ticket10.") {
		t.Errorf("types missing or not in natural order: %s", got)
	}
	if empty := serverInstructions(&metamodel.Metamodel{}); strings.Contains(empty, "Entity types") {
		t.Errorf("empty metamodel should list no types: %s", empty)
	}
}

// --- Resource handler tests ---

func TestHandleReadEntity(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := &mcpgo.ReadResourceRequest{Params: &mcpgo.ReadResourceParams{}}
	req.Params.URI = "rela://entity/requirement/REQ-001"
	contents, err := group(s, selSchemaRes).handleReadEntity(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(contents.Contents) != 1 {
		t.Fatalf("expected 1 content, got %d", len(contents.Contents))
	}
	text := contents.Contents[0].Text
	if !strings.Contains(text, "REQ-001") {
		t.Error("expected entity ID in response")
	}
}

func TestHandleReadEntity_TypeMismatch(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := &mcpgo.ReadResourceRequest{Params: &mcpgo.ReadResourceParams{}}
	req.Params.URI = "rela://entity/decision/REQ-001"
	_, err := group(s, selSchemaRes).handleReadEntity(context.Background(), req)
	if err == nil {
		t.Error("expected error for type mismatch")
	}
	if !strings.Contains(err.Error(), "not decision") {
		t.Errorf("expected type mismatch error, got %v", err)
	}
}

func TestHandleReadEntity_NotFound(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := &mcpgo.ReadResourceRequest{Params: &mcpgo.ReadResourceParams{}}
	req.Params.URI = "rela://entity/requirement/REQ-999"
	_, err := group(s, selSchemaRes).handleReadEntity(context.Background(), req)
	if err == nil {
		t.Error("expected error for nonexistent entity")
	}
}

func TestHandleReadEntity_InvalidURI(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := &mcpgo.ReadResourceRequest{Params: &mcpgo.ReadResourceParams{}}
	req.Params.URI = "rela://entity/onlyone"
	_, err := group(s, selSchemaRes).handleReadEntity(context.Background(), req)
	if err == nil {
		t.Error("expected error for invalid URI")
	}
}

func TestHandleReadRelation(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := &mcpgo.ReadResourceRequest{Params: &mcpgo.ReadResourceParams{}}
	req.Params.URI = "rela://relation/DEC-001/addresses/REQ-001"
	contents, err := group(s, selSchemaRes).handleReadRelation(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(contents.Contents) != 1 {
		t.Fatalf("expected 1 content, got %d", len(contents.Contents))
	}
	text := contents.Contents[0].Text
	if !strings.Contains(text, "DEC-001") {
		t.Error("expected relation from ID in response")
	}
}

func TestHandleReadRelation_NotFound(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	req := &mcpgo.ReadResourceRequest{Params: &mcpgo.ReadResourceParams{}}
	req.Params.URI = "rela://relation/REQ-001/nonexistent/REQ-002"
	_, err := group(s, selSchemaRes).handleReadRelation(context.Background(), req)
	if err == nil {
		t.Error("expected error for nonexistent relation")
	}
}

// --- Helper function tests ---

func TestResolveType(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	tests := []struct {
		input    string
		expected string
	}{
		{"requirement", "requirement"},
		{"requirements", "requirement"},
		{"decision", "decision"},
		{"decisions", "decision"},
		{"unknown", "unknown"}, // falls through
	}
	for _, tt := range tests {
		got := group(s, selTypes).resolveType(tt.input)
		if got != tt.expected {
			t.Errorf("resolveType(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestResolveEntityType(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	resolved, def, err := group(s, selTypes).resolveEntityType("requirement")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != "requirement" {
		t.Errorf("expected 'requirement', got %s", resolved)
	}
	if def == nil {
		t.Error("expected non-nil entity def")
	}
}

func TestResolveEntityType_Unknown(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	_, _, err := group(s, selTypes).resolveEntityType("nonexistent")
	if err == nil {
		t.Error("expected error for unknown type")
	}
}

func TestApplyPagination(t *testing.T) {
	t.Parallel()
	items := []int{1, 2, 3, 4, 5}

	// No pagination
	result := applyPagination(items, 0, 0)
	if len(result) != 5 {
		t.Errorf("expected 5 items, got %d", len(result))
	}

	// Limit only
	result = applyPagination(items, 0, 3)
	if len(result) != 3 {
		t.Errorf("expected 3 items with limit=3, got %d", len(result))
	}

	// Offset only
	result = applyPagination(items, 2, 0)
	if len(result) != 3 {
		t.Errorf("expected 3 items with offset=2, got %d", len(result))
	}

	// Both
	result = applyPagination(items, 1, 2)
	if len(result) != 2 {
		t.Errorf("expected 2 items with offset=1 limit=2, got %d", len(result))
	}

	// Offset beyond length
	result = applyPagination(items, 10, 0)
	if result != nil {
		t.Errorf("expected nil for offset beyond length, got %v", result)
	}
}
