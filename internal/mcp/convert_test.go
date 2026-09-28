package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
	"github.com/Sourcehaven-BV/rela/internal/testutil"
	"github.com/Sourcehaven-BV/rela/internal/tracer"
)

// testMeta returns a shared metamodel for convert tests with all needed entity types.
func testMeta() *metamodel.Metamodel {
	return testutil.NewMetamodel().
		DefineEntity("requirement").
		Label("Requirement").
		IDPrefix("REQ-").
		Prop("title", metamodel.PropertyTypeString, true).
		Prop("status", "status", false).
		End().
		DefineEntity("decision").
		Label("Decision").
		IDPrefix("DEC-").
		Prop("title", metamodel.PropertyTypeString, true).
		Prop("status", "status", false).
		Prop("priority", metamodel.PropertyTypeString, false).
		End().
		DefineEntity("solution").
		Label("Solution").
		IDPrefix("SOL-").
		Prop("title", metamodel.PropertyTypeString, true).
		End().
		DefineEntity("component").
		Label("Component").
		IDPrefix("CMP-").
		Prop("title", metamodel.PropertyTypeString, false).
		End().
		DefineEntity("test").
		Label("Test").
		Prop("title", metamodel.PropertyTypeString, false).
		End().
		WithRelation("addresses", "Addresses", []string{"solution"}, []string{"requirement"}).
		WithRelation("implements", "Implements", []string{"component"}, []string{"solution"}).
		WithRelation("motivates", "Motivates", []string{"requirement"}, []string{"decision"}).
		WithCustomType("status", []string{"draft", "proposed", "accepted", "rejected"}).
		Build()
}

// makeToolRequest creates a CallToolRequest with the given arguments.
func makeToolRequest(args map[string]any) *mcpgo.CallToolRequest {
	raw, err := json.Marshal(args)
	if err != nil {
		panic("makeToolRequest: marshal args: " + err.Error())
	}
	return &mcpgo.CallToolRequest{
		Params: &mcpgo.CallToolParamsRaw{
			Arguments: raw,
		},
	}
}

// buildEntity returns a built entity as *entity.Entity.
func buildEntity(b *testutil.EntityBuilder) *entity.Entity {
	return b.Build()
}

// seedEntity creates an entity in the store.
func seedEntity(t *testing.T, st store.Store, e *entity.Entity) {
	t.Helper()
	if err := st.CreateEntity(context.Background(), e); err != nil {
		t.Fatalf("CreateEntity(%s): %v", e.ID, err)
	}
}

// seedRelation creates a relation in the store.
func seedRelation(t *testing.T, st store.Store, from, relType, to string) {
	t.Helper()
	if _, err := st.CreateRelation(context.Background(), from, relType, to, nil); err != nil {
		t.Fatalf("CreateRelation(%s--%s--%s): %v", from, relType, to, err)
	}
}

func TestConvertStoreEntity_WithoutRelations(t *testing.T) {
	t.Parallel()
	meta := testMeta()
	st := memstore.New()
	e := buildEntity(testutil.EntityFor(meta, "requirement").ID("REQ-001").With("title", "Test requirement").WithContent("Some content"))
	seedEntity(t, st, e)

	result, err := convertStoreEntity(context.Background(), e, st, meta, entityView{content: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed entityJSON
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if parsed.ID != e.ID {
		t.Errorf("expected ID %s, got %s", e.ID, parsed.ID)
	}
	if parsed.Type != "requirement" {
		t.Errorf("expected type requirement, got %s", parsed.Type)
	}
	if parsed.Content != "Some content" {
		t.Errorf("expected content 'Some content', got %s", parsed.Content)
	}
	if parsed.Relations != nil {
		t.Error("expected no relations when includeRelations=false")
	}
	if parsed.Properties["title"] != "Test requirement" {
		t.Errorf("expected title 'Test requirement', got %v", parsed.Properties["title"])
	}
}

func TestConvertStoreEntity_WithRelations(t *testing.T) {
	t.Parallel()
	meta := testMeta()
	st := memstore.New()
	e1 := buildEntity(testutil.EntityFor(meta, "requirement").ID("REQ-001"))
	e2 := buildEntity(testutil.EntityFor(meta, "solution").ID("SOL-001"))
	seedEntity(t, st, e1)
	seedEntity(t, st, e2)
	seedRelation(t, st, e2.ID, "addresses", e1.ID)

	result, err := convertStoreEntity(context.Background(), e1, st, meta, entityView{relations: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed entityJSON
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if parsed.Relations == nil {
		t.Fatal("expected relations to be present")
	}
	if len(parsed.Relations.Incoming["addresses"]) != 1 {
		t.Errorf("expected 1 incoming 'addresses' relation, got %d",
			len(parsed.Relations.Incoming["addresses"]))
	}
	if parsed.Relations.Incoming["addresses"][0].ID != e2.ID {
		t.Errorf("expected incoming from %s, got %s",
			e2.ID, parsed.Relations.Incoming["addresses"][0].ID)
	}
}

func TestConvertStoreEntity_NoRelationsPresent(t *testing.T) {
	t.Parallel()
	meta := testMeta()
	st := memstore.New()
	e := buildEntity(testutil.EntityFor(meta, "requirement").ID("REQ-001"))
	seedEntity(t, st, e)

	result, err := convertStoreEntity(context.Background(), e, st, meta, entityView{relations: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed entityJSON
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if parsed.Relations != nil {
		t.Error("expected nil relations when entity has no connections")
	}
}

func TestConvertStoreEntitySummary(t *testing.T) {
	t.Parallel()
	meta := testMeta()
	e := buildEntity(testutil.EntityFor(meta, "requirement").ID("REQ-001").With("title", "My Title").With("status", "accepted"))

	result := convertStoreEntitySummary(meta, e)

	want := entitySummary{ID: e.ID, Type: e.Type, Title: "My Title", Status: "accepted"}
	if result != want {
		t.Errorf("summary = %+v, want %+v", result, want)
	}
}

func TestConvertStoreEntitySummary_NoTitleNoStatus(t *testing.T) {
	t.Parallel()
	meta := testMeta()
	e := buildEntity(testutil.EntityFor(meta, "requirement").ID("REQ-002").Without("title").Without("status"))

	text, err := marshalJSON(convertStoreEntitySummary(meta, e))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"id":"REQ-002","type":"requirement"}`; text != want {
		t.Errorf("summary = %s, want %s", text, want)
	}
}

func TestConvertStoreRelation(t *testing.T) {
	t.Parallel()
	r := &entity.Relation{
		From:       "SOL-001",
		Type:       "addresses",
		To:         "REQ-001",
		Properties: map[string]any{"rationale": "because"},
		Content:    "Relation content",
	}

	result, err := convertStoreRelation(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed relationJSON
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if parsed.From != r.From {
		t.Errorf("expected from %s, got %s", r.From, parsed.From)
	}
	if parsed.Type != r.Type {
		t.Errorf("expected type %s, got %s", r.Type, parsed.Type)
	}
	if parsed.To != r.To {
		t.Errorf("expected to %s, got %s", r.To, parsed.To)
	}
	if parsed.Content != r.Content {
		t.Errorf("expected content '%s', got %s", r.Content, parsed.Content)
	}
	if parsed.Properties["rationale"] != r.Properties["rationale"] {
		t.Errorf("expected property rationale=%v, got %v", r.Properties["rationale"], parsed.Properties["rationale"])
	}
}

func TestConvertStoreRelation_NoProperties(t *testing.T) {
	t.Parallel()
	r := &entity.Relation{From: "SOL-001", Type: "addresses", To: "REQ-001"}

	result, err := convertStoreRelation(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed relationJSON
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if parsed.From != r.From {
		t.Errorf("expected from %s, got %s", r.From, parsed.From)
	}
	if parsed.Content != "" {
		t.Errorf("expected empty content, got %s", parsed.Content)
	}
}

func TestConvertTraceResult(t *testing.T) {
	t.Parallel()
	tr := &tracer.TraceResult{
		ID:         "REQ-001",
		Type:       "requirement",
		Title:      "Root Req",
		Properties: map[string]any{"title": "Root Req"},
		Depth:      0,
		Children: []*tracer.TraceResult{
			{
				ID:         "SOL-001",
				Type:       "solution",
				Title:      "Child Sol",
				Properties: map[string]any{"title": "Child Sol"},
				Depth:      1,
				Relation:   "addresses",
				Incoming:   true,
			},
		},
	}

	result, err := convertTraceResult(tr, testMeta())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed traceNodeJSON
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if parsed.ID != "REQ-001" {
		t.Errorf("expected ID REQ-001, got %s", parsed.ID)
	}
	if parsed.Title != "Root Req" {
		t.Errorf("expected title Root Req, got %q", parsed.Title)
	}
	if len(parsed.Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(parsed.Children))
	}
	child := parsed.Children[0]
	if child.ID != "SOL-001" {
		t.Errorf("expected child ID SOL-001, got %s", child.ID)
	}
	if child.Relation != "addresses" {
		t.Errorf("expected child relation addresses, got %s", child.Relation)
	}
	if !child.Incoming {
		t.Error("expected child to be incoming")
	}
}

func TestConvertTraceResult_Nil(t *testing.T) {
	t.Parallel()
	node := convertTraceNode(nil, testMeta())
	if node != nil {
		t.Error("expected nil result for nil input")
	}
}

func TestConvertPathSteps(t *testing.T) {
	t.Parallel()
	steps := []tracer.PathStep{
		{ID: "REQ-001", Type: "requirement", Title: "Start"},
		{ID: "SOL-001", Type: "solution", Title: "Middle", Relation: "addresses"},
		{ID: "CMP-001", Type: "component", Title: "End", Relation: "implements"},
	}

	result, err := convertPathSteps(steps, func(s tracer.PathStep) string { return s.Title })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed []pathStepJSON
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(parsed) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(parsed))
	}
	if parsed[0].ID != "REQ-001" {
		t.Errorf("expected first step ID REQ-001, got %s", parsed[0].ID)
	}
	if parsed[1].Relation != "addresses" {
		t.Errorf("expected second step relation 'addresses', got %s", parsed[1].Relation)
	}
	if parsed[2].Title != "End" {
		t.Errorf("expected third step title 'End', got %s", parsed[2].Title)
	}
}

func TestConvertPathSteps_Empty(t *testing.T) {
	t.Parallel()
	result, err := convertPathSteps([]tracer.PathStep{}, func(tracer.PathStep) string { return "" })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "[]") {
		t.Errorf("expected empty array, got %s", result)
	}
}

func TestBuildStoreRelations_NoEdges(t *testing.T) {
	t.Parallel()
	meta := testMeta()
	st := memstore.New()
	e := buildEntity(testutil.EntityFor(meta, "requirement").ID("REQ-001"))
	seedEntity(t, st, e)

	rels := buildStoreRelations(context.Background(), e.ID, st, meta)
	if rels != nil {
		t.Error("expected nil relations for entity with no edges")
	}
}

func TestBuildStoreRelations_OutgoingOnly(t *testing.T) {
	t.Parallel()
	meta := testMeta()
	st := memstore.New()
	sol := buildEntity(testutil.EntityFor(meta, "solution").ID("SOL-001").With("title", "Solution"))
	req := buildEntity(testutil.EntityFor(meta, "requirement").ID("REQ-001").With("title", "Requirement"))
	seedEntity(t, st, sol)
	seedEntity(t, st, req)
	seedRelation(t, st, sol.ID, "addresses", req.ID)

	rels := buildStoreRelations(context.Background(), sol.ID, st, meta)
	if rels == nil {
		t.Fatal("expected non-nil relations")
	}
	if len(rels.Outgoing["addresses"]) != 1 {
		t.Errorf("expected 1 outgoing addresses relation, got %d", len(rels.Outgoing["addresses"]))
	}
	if rels.Outgoing["addresses"][0].ID != req.ID {
		t.Errorf("expected target %s, got %s", req.ID, rels.Outgoing["addresses"][0].ID)
	}
	if rels.Outgoing["addresses"][0].Title != req.Properties["title"] {
		t.Errorf("expected title '%v', got %s", req.Properties["title"], rels.Outgoing["addresses"][0].Title)
	}
	if rels.Incoming != nil {
		t.Error("expected no incoming relations")
	}
}

func TestBuildStoreRelations_IncomingOnly(t *testing.T) {
	t.Parallel()
	meta := testMeta()
	st := memstore.New()
	req := buildEntity(testutil.EntityFor(meta, "requirement").ID("REQ-001").With("title", "Requirement"))
	sol := buildEntity(testutil.EntityFor(meta, "solution").ID("SOL-001").With("title", "Solution"))
	seedEntity(t, st, req)
	seedEntity(t, st, sol)
	seedRelation(t, st, sol.ID, "addresses", req.ID)

	rels := buildStoreRelations(context.Background(), req.ID, st, meta)
	if rels == nil {
		t.Fatal("expected non-nil relations")
	}
	if rels.Outgoing != nil {
		t.Error("expected no outgoing relations")
	}
	if len(rels.Incoming["addresses"]) != 1 {
		t.Errorf("expected 1 incoming addresses relation, got %d", len(rels.Incoming["addresses"]))
	}
	if rels.Incoming["addresses"][0].ID != sol.ID {
		t.Errorf("expected source %s, got %s", sol.ID, rels.Incoming["addresses"][0].ID)
	}
}

func TestBuildStoreRelations_BothDirections(t *testing.T) {
	t.Parallel()
	meta := testMeta()
	st := memstore.New()
	seedEntity(t, st, buildEntity(testutil.EntityFor(meta, "requirement").ID("REQ-001").With("title", "Req")))
	seedEntity(t, st, buildEntity(testutil.EntityFor(meta, "solution").ID("SOL-001").With("title", "Sol")))
	seedEntity(t, st, buildEntity(testutil.EntityFor(meta, "decision").ID("DEC-001").With("title", "Dec")))
	seedRelation(t, st, "SOL-001", "addresses", "REQ-001")
	seedRelation(t, st, "REQ-001", "motivates", "DEC-001")

	rels := buildStoreRelations(context.Background(), "REQ-001", st, meta)
	if rels == nil {
		t.Fatal("expected non-nil relations")
	}
	if rels.Outgoing == nil {
		t.Fatal("expected outgoing relations")
	}
	if rels.Incoming == nil {
		t.Fatal("expected incoming relations")
	}
	if len(rels.Outgoing["motivates"]) != 1 {
		t.Errorf("expected 1 outgoing motivates, got %d", len(rels.Outgoing["motivates"]))
	}
	if len(rels.Incoming["addresses"]) != 1 {
		t.Errorf("expected 1 incoming addresses, got %d", len(rels.Incoming["addresses"]))
	}
}

func TestConvertStoreRelationsList(t *testing.T) {
	t.Parallel()
	relations := []*entity.Relation{
		{From: "SOL-001", Type: "addresses", To: "REQ-001", Properties: map[string]any{"weight": "high"}},
		{From: "CMP-001", Type: "implements", To: "SOL-001"},
	}

	parsed := convertStoreRelationsList(relations)

	if len(parsed) != 2 {
		t.Fatalf("expected 2 relations, got %d", len(parsed))
	}
	if parsed[0].From != "SOL-001" {
		t.Errorf("expected first relation from SOL-001, got %s", parsed[0].From)
	}
	if parsed[0].Type != "addresses" {
		t.Errorf("expected first relation type addresses, got %s", parsed[0].Type)
	}
	if parsed[1].To != "SOL-001" {
		t.Errorf("expected second relation to SOL-001, got %s", parsed[1].To)
	}
}

func TestConvertStoreRelationsList_Empty(t *testing.T) {
	t.Parallel()
	result, err := marshalJSON(convertStoreRelationsList([]*entity.Relation{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "[]" {
		t.Errorf("expected empty array, got %s", result)
	}
}

func TestSortStoreRelations(t *testing.T) {
	t.Parallel()
	relations := []*entity.Relation{
		{From: "SOL-001", Type: "implements", To: "REQ-001"},
		{From: "SOL-001", Type: "addresses", To: "REQ-001"},
		{From: "CMP-001", Type: "implements", To: "SOL-001"},
	}

	sortStoreRelations(relations)

	if relations[0].From != "CMP-001" {
		t.Errorf("expected first from CMP-001, got %s", relations[0].From)
	}
	if relations[1].Type != "addresses" {
		t.Errorf("expected second type addresses, got %s", relations[1].Type)
	}
	if relations[2].Type != "implements" {
		t.Errorf("expected third type implements, got %s", relations[2].Type)
	}
}

func TestSortStoreRelations_ByTo(t *testing.T) {
	t.Parallel()
	relations := []*entity.Relation{
		{From: "SOL-001", Type: "addresses", To: "REQ-002"},
		{From: "SOL-001", Type: "addresses", To: "REQ-001"},
	}

	sortStoreRelations(relations)

	if relations[0].To != "REQ-001" {
		t.Errorf("expected first to REQ-001, got %s", relations[0].To)
	}
	if relations[1].To != "REQ-002" {
		t.Errorf("expected second to REQ-002, got %s", relations[1].To)
	}
}

func TestSortStoreRelations_Empty(_ *testing.T) {
	var relations []*entity.Relation
	sortStoreRelations(relations)
}

func TestMarshalJSON(t *testing.T) {
	t.Parallel()
	data := map[string]string{"key": "value"}
	result, err := marshalJSON(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != `{"key":"value"}` {
		t.Errorf("expected JSON with key/value, got %s", result)
	}
}

func TestConvertTraceResult_DeepNesting(t *testing.T) {
	t.Parallel()
	tr := &tracer.TraceResult{
		ID:    "REQ-001",
		Type:  "requirement",
		Title: "Root",
		Depth: 0,
		Children: []*tracer.TraceResult{
			{
				ID:       "SOL-001",
				Type:     "solution",
				Title:    "Level 1",
				Depth:    1,
				Relation: "addresses",
				Children: []*tracer.TraceResult{
					{
						ID:       "CMP-001",
						Type:     "component",
						Title:    "Level 2",
						Depth:    2,
						Relation: "implements",
					},
				},
			},
		},
	}

	result, err := convertTraceResult(tr, testMeta())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed traceNodeJSON
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(parsed.Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(parsed.Children))
	}
	if len(parsed.Children[0].Children) != 1 {
		t.Fatalf("expected 1 grandchild, got %d", len(parsed.Children[0].Children))
	}
	grandchild := parsed.Children[0].Children[0]
	if grandchild.ID != "CMP-001" {
		t.Errorf("expected grandchild ID CMP-001, got %s", grandchild.ID)
	}
	if grandchild.Relation != "implements" {
		t.Errorf("expected grandchild relation implements, got %s", grandchild.Relation)
	}
}

func TestConvertStoreEntity_WithProperties(t *testing.T) {
	t.Parallel()
	meta := testMeta()
	st := memstore.New()
	e := buildEntity(testutil.EntityFor(meta, "decision").ID("DEC-001").With("title", "Use Go").With("status", "accepted").With("priority", "high"))
	seedEntity(t, st, e)

	result, err := convertStoreEntity(context.Background(), e, st, meta, entityView{content: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, `"DEC-001"`) {
		t.Error("expected JSON to contain entity ID")
	}
	if !strings.Contains(result, `"decision"`) {
		t.Error("expected JSON to contain entity type")
	}
	if !strings.Contains(result, `"Use Go"`) {
		t.Error("expected JSON to contain title")
	}
}

func TestExtractProperties_MapArgument(t *testing.T) {
	t.Parallel()
	req := makeToolRequest(map[string]any{
		"properties": map[string]any{
			"title":  "Test",
			"status": "draft",
		},
	})

	props := extractProperties(req)
	if props == nil {
		t.Fatal("expected non-nil properties")
	}
	if props["title"] != "Test" {
		t.Errorf("expected title 'Test', got %v", props["title"])
	}
	if props["status"] != "draft" {
		t.Errorf("expected status 'draft', got %v", props["status"])
	}
}

func TestExtractProperties_JSONString(t *testing.T) {
	t.Parallel()
	req := makeToolRequest(map[string]any{
		"properties": `{"title":"From JSON","priority":"high"}`,
	})

	props := extractProperties(req)
	if props == nil {
		t.Fatal("expected non-nil properties from JSON string")
	}
	if props["title"] != "From JSON" {
		t.Errorf("expected title 'From JSON', got %v", props["title"])
	}
}

func TestExtractProperties_NoProperties(t *testing.T) {
	t.Parallel()
	req := makeToolRequest(map[string]any{
		"id": "REQ-001",
	})

	props := extractProperties(req)
	if props != nil {
		t.Error("expected nil properties when key is missing")
	}
}

func TestExtractProperties_InvalidJSON(t *testing.T) {
	t.Parallel()
	req := makeToolRequest(map[string]any{
		"properties": "not valid json",
	})

	props := extractProperties(req)
	if props != nil {
		t.Error("expected nil properties for invalid JSON string")
	}
}

func TestExtractProperties_UnsupportedType(t *testing.T) {
	t.Parallel()
	req := makeToolRequest(map[string]any{
		"properties": 42,
	})

	props := extractProperties(req)
	if props != nil {
		t.Error("expected nil properties for unsupported type")
	}
}

func TestExtractPropertiesAllowNil_PreservesNil(t *testing.T) {
	t.Parallel()
	req := makeToolRequest(map[string]any{
		"properties": map[string]any{
			"foo": nil,
			"bar": "value",
		},
	})
	props := extractPropertiesAllowNil(req)
	if props == nil {
		t.Fatal("expected non-nil map when nil entries are present")
	}
	if len(props) != 2 {
		t.Errorf("expected 2 entries (nil counted), got %d: %v", len(props), props)
	}
	if v, ok := props["foo"]; !ok || v != nil {
		t.Errorf("expected foo=nil to be preserved, got ok=%v v=%v", ok, v)
	}
	if props["bar"] != "value" {
		t.Errorf("expected bar=value, got %v", props["bar"])
	}
}

func TestExtractPropertiesAllowNil_OnlyNilEntriesStillReturnsMap(t *testing.T) {
	t.Parallel()
	// Critical: a delete-only call must yield len()>0 so the "no updates specified"
	// guard does not reject it.
	req := makeToolRequest(map[string]any{
		"properties": map[string]any{"foo": nil},
	})
	props := extractPropertiesAllowNil(req)
	if len(props) != 1 {
		t.Fatalf("expected len=1 for nil-only map, got %d (props=%v)", len(props), props)
	}
}

func TestExtractPropertiesAllowNil_StringJSONNullDeletes(t *testing.T) {
	t.Parallel()
	// JSON-string fallback must preserve null as nil entry.
	req := makeToolRequest(map[string]any{
		"properties": `{"foo": null, "bar": "x"}`,
	})
	props := extractPropertiesAllowNil(req)
	if props == nil {
		t.Fatal("expected non-nil map from JSON string")
	}
	if v, ok := props["foo"]; !ok || v != nil {
		t.Errorf("expected foo=nil from JSON string, got ok=%v v=%v", ok, v)
	}
	if props["bar"] != "x" {
		t.Errorf("expected bar=x, got %v", props["bar"])
	}
}

func TestExtractPropertiesAllowNil_FiltersEmptyString(t *testing.T) {
	t.Parallel()
	req := makeToolRequest(map[string]any{
		"properties": map[string]any{"foo": ""},
	})
	props := extractPropertiesAllowNil(req)
	if props != nil {
		t.Errorf("expected nil for properties containing only empty string, got %v", props)
	}
}

func TestExtractPropertiesAllowNil_NoArg(t *testing.T) {
	t.Parallel()
	req := makeToolRequest(map[string]any{"id": "REQ-001"})
	props := extractPropertiesAllowNil(req)
	if props != nil {
		t.Errorf("expected nil when properties arg is missing, got %v", props)
	}
}

func TestExtractPropertiesAllowNil_InvalidJSON(t *testing.T) {
	t.Parallel()
	req := makeToolRequest(map[string]any{
		"properties": "not valid json",
	})
	props := extractPropertiesAllowNil(req)
	if props != nil {
		t.Errorf("expected nil for invalid JSON string, got %v", props)
	}
}

func TestExtractPropertiesAllowNil_JSONNullArg(t *testing.T) {
	t.Parallel()
	// JSON `null` as the entire properties value: ambiguous (no map to interpret).
	// Treat as missing/malformed, not as an empty map.
	req := makeToolRequest(map[string]any{
		"properties": "null",
	})
	props := extractPropertiesAllowNil(req)
	if props != nil {
		t.Errorf("expected nil for JSON 'null' as properties arg, got %v", props)
	}
}
