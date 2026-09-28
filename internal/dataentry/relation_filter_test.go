package dataentry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	v1 "github.com/Sourcehaven-BV/rela/internal/apiwire/v1"
	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
)

// relationFilterMeta is a small metamodel exercising both relation
// directions: taak--verantwoordelijk_voor(incoming)-->persoon and
// taak--belongs_to(outgoing)-->project.
func relationFilterMeta() *metamodel.Metamodel {
	return &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"taak": {
				Label:         "Taak",
				IDPrefix:      "TAAK-",
				Properties:    map[string]metamodel.PropertyDef{"title": {Type: "string", Required: true}},
				PropertyOrder: []string{"title"},
			},
			"persoon": {
				Label:         "Persoon",
				IDPrefix:      "PER-",
				Properties:    map[string]metamodel.PropertyDef{"name": {Type: "string", Required: true}},
				PropertyOrder: []string{"name"},
			},
			"project": {
				Label:         "Project",
				IDPrefix:      "PRJ-",
				Properties:    map[string]metamodel.PropertyDef{"name": {Type: "string", Required: true}},
				PropertyOrder: []string{"name"},
			},
		},
		Relations: map[string]metamodel.RelationDef{
			// persoon is responsible FOR taak: edge points persoon -> taak,
			// so from a taak's perspective the persoon is the incoming source.
			"verantwoordelijk_voor": {
				Label: "verantwoordelijk voor",
				From:  []string{"persoon"},
				To:    []string{"taak"},
			},
			"belongs_to": {
				Label: "belongs to",
				From:  []string{"taak"},
				To:    []string{"project"},
			},
		},
	}
}

// relationFilterApp builds an App whose taak list carries relation filter
// controls (incoming verantwoordelijk_voor + outgoing belongs_to) and seeds a
// small graph. Returns the app.
func relationFilterApp(t *testing.T) *App {
	t.Helper()
	return relationFilterAppWithConfig(t, &dataentryconfig.Config{
		App: dataentryconfig.AppConfig{Name: "Test"},
		Lists: map[string]dataentryconfig.List{
			"taken": {
				EntityType: "taak",
				Title:      "Taken",
				FilterControls: []dataentryconfig.FilterControl{
					{Relation: "verantwoordelijk_voor", Direction: dataentryconfig.DirectionIncoming},
					{Relation: "belongs_to"}, // default outgoing
				},
			},
		},
	})
}

// relationFilterAppWithConfig seeds relationFilterApp's graph under cfg.
func relationFilterAppWithConfig(t *testing.T, cfg *dataentryconfig.Config) *App {
	t.Helper()
	meta := relationFilterMeta()

	f := newFixture()
	// People
	f.AddNode(&entity.Entity{ID: "PER-001", Type: "persoon", Properties: map[string]any{"name": "Jeroen Vloothuis"}})
	f.AddNode(&entity.Entity{ID: "PER-002", Type: "persoon", Properties: map[string]any{"name": "Alice"}})
	// Projects
	f.AddNode(&entity.Entity{ID: "PRJ-001", Type: "project", Properties: map[string]any{"name": "Apollo"}})
	f.AddNode(&entity.Entity{ID: "PRJ-002", Type: "project", Properties: map[string]any{"name": "Gemini"}})
	// Tasks (seeded out of title order to prove sort wins)
	f.AddNode(&entity.Entity{ID: "TAAK-002", Type: "taak", Properties: map[string]any{"title": "B task"}})
	f.AddNode(&entity.Entity{ID: "TAAK-001", Type: "taak", Properties: map[string]any{"title": "A task"}})
	f.AddNode(&entity.Entity{ID: "TAAK-003", Type: "taak", Properties: map[string]any{"title": "C task"}})
	f.AddNode(&entity.Entity{ID: "TAAK-004", Type: "taak", Properties: map[string]any{"title": "D task"}})

	// Jeroen is responsible for TAAK-001 and TAAK-003; Alice for TAAK-002.
	f.AddEdge(entity.NewRelation("PER-001", "verantwoordelijk_voor", "TAAK-001"))
	f.AddEdge(entity.NewRelation("PER-001", "verantwoordelijk_voor", "TAAK-003"))
	f.AddEdge(entity.NewRelation("PER-002", "verantwoordelijk_voor", "TAAK-002"))
	// TAAK-004 has no responsible person.

	// belongs_to (outgoing) targets.
	f.AddEdge(entity.NewRelation("TAAK-001", "belongs_to", "PRJ-001")) // Apollo
	f.AddEdge(entity.NewRelation("TAAK-002", "belongs_to", "PRJ-002")) // Gemini
	f.AddEdge(entity.NewRelation("TAAK-003", "belongs_to", "PRJ-001")) // Apollo

	return newAppFromParts(cfg, meta, f)
}

// listTaken drives the list handler and returns the returned entity IDs.
// query is a url.Values that is safely encoded (values may contain spaces).
func listTaken(t *testing.T, app *App, query url.Values) []string {
	t.Helper()
	path := "/api/v1/taaks"
	if enc := query.Encode(); enc != "" {
		path += "?" + enc
	}
	req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
	rec := httptest.NewRecorder()
	app.handleV1ListEntities(rec, req, "taak", "taaks")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status: got %d, body %s", rec.Code, rec.Body.String())
	}
	var resp v1.ListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	ids := make([]string, 0, len(resp.Data))
	for _, e := range resp.Data {
		ids = append(ids, e.ID)
	}
	return ids
}

func TestV1ListRelationFilter(t *testing.T) {
	tests := []struct {
		name    string
		query   url.Values
		wantIDs []string
	}{
		{
			name:    "incoming relation filter keeps only matching rows",
			query:   url.Values{"filter[verantwoordelijk_voor]": {"Jeroen Vloothuis"}, "sort": {"title"}},
			wantIDs: []string{"TAAK-001", "TAAK-003"},
		},
		{
			name:    "incoming relation filter for a different person",
			query:   url.Values{"filter[verantwoordelijk_voor]": {"Alice"}},
			wantIDs: []string{"TAAK-002"},
		},
		{
			name:    "incoming relation filter with no matches returns empty",
			query:   url.Values{"filter[verantwoordelijk_voor]": {"Nobody"}},
			wantIDs: []string{},
		},
		{
			name:    "outgoing relation filter (default direction)",
			query:   url.Values{"filter[belongs_to]": {"Apollo"}, "sort": {"title"}},
			wantIDs: []string{"TAAK-001", "TAAK-003"},
		},
		{
			name:    "outgoing relation filter different target",
			query:   url.Values{"filter[belongs_to]": {"Gemini"}},
			wantIDs: []string{"TAAK-002"},
		},
		{
			name:    "empty relation filter value is no constraint",
			query:   url.Values{"filter[verantwoordelijk_voor]": {""}, "sort": {"title"}},
			wantIDs: []string{"TAAK-001", "TAAK-002", "TAAK-003", "TAAK-004"},
		},
		{
			name: "relation + property filter combine (property still works)",
			query: url.Values{
				"filter[verantwoordelijk_voor]": {"Jeroen Vloothuis"},
				"filter[title]":                 {"A task"},
			},
			wantIDs: []string{"TAAK-001"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := relationFilterApp(t)
			got := listTaken(t, app, tt.query)
			if !equalStringSlices(got, tt.wantIDs) {
				t.Errorf("ids = %v, want %v", got, tt.wantIDs)
			}
		})
	}
}

// TestV1ListRelationFilter_ControlOnKanbanOnly pins BUG-GEMNW6: a board sends
// its filters to the list endpoint, so a relation control that only a kanban
// configures must be honored there. Before the fix only list controls were
// consulted, `filter[belongs_to]` fell through to the property pass, and the
// board's relation filter matched nothing.
func TestV1ListRelationFilter_ControlOnKanbanOnly(t *testing.T) {
	app := relationFilterAppWithConfig(t, &dataentryconfig.Config{
		App: dataentryconfig.AppConfig{Name: "Test"},
		Lists: map[string]dataentryconfig.List{
			"taken": {EntityType: "taak", Title: "Taken"},
		},
		Kanbans: map[string]dataentryconfig.Kanban{
			"bord": {
				EntityType:     "taak",
				Title:          "Bord",
				ColumnProperty: "status",
				FilterControls: []dataentryconfig.FilterControl{{Relation: "belongs_to"}},
			},
		},
	})

	got := listTaken(t, app, url.Values{"filter[belongs_to]": {"Apollo"}, "sort": {"title"}})
	if !equalStringSlices(got, []string{"TAAK-001", "TAAK-003"}) {
		t.Errorf("ids = %v, want [TAAK-001 TAAK-003]", got)
	}
}

// TestV1ListPropertyFilterUnaffected confirms a pure property filter (no
// relation involved) still behaves exactly as before the relation-filter pass.
func TestV1ListPropertyFilterUnaffected(t *testing.T) {
	app := relationFilterApp(t)
	got := listTaken(t, app, url.Values{"filter[title]": {"C task"}})
	if !equalStringSlices(got, []string{"TAAK-003"}) {
		t.Errorf("property filter ids = %v, want [TAAK-003]", got)
	}
}

// TestV1PositionRelationFilter checks that scope nav over a relation-filtered
// list yields prev/next/total consistent with the list ordering. Both go
// through scopedSortedEntities, so the relation filter must apply identically.
func TestV1PositionRelationFilter(t *testing.T) {
	app := relationFilterApp(t)

	// The list, sorted by title, of tasks Jeroen is responsible for:
	// TAAK-001 (A task), TAAK-003 (C task).
	scope := ScopeDescriptor{
		Source:  "list",
		Type:    "taak",
		Filters: map[string]string{"filter[verantwoordelijk_voor]": "Jeroen Vloothuis"},
		Sort:    "title",
	}

	// Sanity: the list handler returns exactly these two, in this order.
	gotList := listTaken(t, app, url.Values{
		"filter[verantwoordelijk_voor]": {"Jeroen Vloothuis"},
		"sort":                          {"title"},
	})
	if !equalStringSlices(gotList, []string{"TAAK-001", "TAAK-003"}) {
		t.Fatalf("precondition list ids = %v, want [TAAK-001 TAAK-003]", gotList)
	}

	t.Run("first entity", func(t *testing.T) {
		rec, pos := getPosition(t, app, "TAAK-001", scope)
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d", rec.Code)
		}
		if pos.Current != 1 || pos.Total != 2 {
			t.Errorf("current/total = %d/%d, want 1/2", pos.Current, pos.Total)
		}
		if pos.Prev != nil {
			t.Errorf("prev = %v, want nil", pos.Prev)
		}
		if pos.Next == nil || pos.Next.ID != "TAAK-003" {
			t.Errorf("next = %v, want TAAK-003", pos.Next)
		}
	})

	t.Run("last entity", func(t *testing.T) {
		rec, pos := getPosition(t, app, "TAAK-003", scope)
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d", rec.Code)
		}
		if pos.Current != 2 || pos.Total != 2 {
			t.Errorf("current/total = %d/%d, want 2/2", pos.Current, pos.Total)
		}
		if pos.Prev == nil || pos.Prev.ID != "TAAK-001" {
			t.Errorf("prev = %v, want TAAK-001", pos.Prev)
		}
		if pos.Next != nil {
			t.Errorf("next = %v, want nil", pos.Next)
		}
	})

	t.Run("entity filtered out is not in scope", func(t *testing.T) {
		// TAAK-002 (Alice's) is excluded by the filter, so it must 404.
		rec, _ := getPosition(t, app, "TAAK-002", scope)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status: got %d, want 404", rec.Code)
		}
	})
}

// TestV1ListRelationFilterOperatorFailsClosed pins RR-6RF60V: an operator
// segment on a relation filter that the relation pass does not support must
// fail CLOSED (drop to zero rows), never fail open by returning the whole
// unfiltered list. Before the fix, `filter[<rel>][ne]=X` parsed the key with
// TrimPrefix/TrimSuffix, produced the garbage relation name `<rel>][ne`, was
// skipped by BOTH passes, and silently returned every row.
func TestV1ListRelationFilterOperatorFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		query   url.Values
		wantIDs []string
	}{
		{
			// `ne` IS supported: tasks NOT responsible-to Jeroen.
			name:    "ne operator is supported (complement)",
			query:   url.Values{"filter[verantwoordelijk_voor][ne]": {"Jeroen Vloothuis"}, "sort": {"title"}},
			wantIDs: []string{"TAAK-002", "TAAK-004"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := relationFilterApp(t)
			got := listTaken(t, app, tt.query)
			if !equalStringSlices(got, tt.wantIDs) {
				t.Errorf("ids = %v, want %v (fail-open would return all 4 tasks)", got, tt.wantIDs)
			}
		})
	}

	// Unsupported operators on relation filters reject the request (400,
	// invalid_filter) — never fail open, and no longer the silent
	// zero-rows drop (which read as "no data" instead of naming the typo).
	rejected := []url.Values{
		{"filter[verantwoordelijk_voor][contains]": {"Jeroen"}},
		{"filter[belongs_to][weird]": {"Apollo"}},
	}
	for _, q := range rejected {
		t.Run("rejects "+q.Encode(), func(t *testing.T) {
			app := relationFilterApp(t)
			path := "/api/v1/taaks?" + q.Encode()
			req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
			rec := httptest.NewRecorder()
			app.handleV1ListEntities(rec, req, "taak", "taaks")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (body %s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "invalid_filter") {
				t.Errorf("expected invalid_filter code in body, got %s", rec.Body.String())
			}
		})
	}
}

// TestV1ListPropertyRelationNameCollision pins RR-0HWAS0: when an entity
// property and a metamodel relation share a name, a `filter_control` declared
// as a PROPERTY must filter on the property value, not be silently mis-routed
// to the relation pass. The config discriminator (an explicit property control)
// is authoritative over the name-based relation lookup.
func TestV1ListPropertyRelationNameCollision(t *testing.T) {
	meta := relationFilterMeta()
	// Give taak a scalar property named `belongs_to` — colliding with the
	// existing global relation `belongs_to`.
	taak := meta.Entities["taak"]
	taak.Properties["belongs_to"] = metamodel.PropertyDef{Type: "string"}
	taak.PropertyOrder = append(taak.PropertyOrder, "belongs_to")
	meta.Entities["taak"] = taak

	cfg := &dataentryconfig.Config{
		App: dataentryconfig.AppConfig{Name: "Test"},
		Lists: map[string]dataentryconfig.List{
			"taken": {
				EntityType: "taak",
				Title:      "Taken",
				// Declared as a PROPERTY control for the colliding name.
				FilterControls: []dataentryconfig.FilterControl{
					{Property: "belongs_to"},
				},
			},
		},
	}

	f := newFixture()
	f.AddNode(&entity.Entity{ID: "PRJ-001", Type: "project", Properties: map[string]any{"name": "Apollo"}})
	// Two tasks: property belongs_to differs, and each ALSO has a belongs_to
	// EDGE to Apollo, so a mis-route to the relation pass would match BOTH.
	f.AddNode(&entity.Entity{ID: "TAAK-001", Type: "taak", Properties: map[string]any{"title": "A", "belongs_to": "team-x"}})
	f.AddNode(&entity.Entity{ID: "TAAK-002", Type: "taak", Properties: map[string]any{"title": "B", "belongs_to": "team-y"}})
	f.AddEdge(entity.NewRelation("TAAK-001", "belongs_to", "PRJ-001"))
	f.AddEdge(entity.NewRelation("TAAK-002", "belongs_to", "PRJ-001"))

	app := newAppFromParts(cfg, meta, f)

	// Filtering on the PROPERTY value team-x must return ONLY TAAK-001. A
	// mis-route to the relation pass would filter on the belongs_to EDGE target
	// title ("Apollo"), which team-x never matches → wrong (empty), or if the
	// value were "Apollo" it would wrongly return both.
	got := listTaken(t, app, url.Values{"filter[belongs_to]": {"team-x"}})
	if !equalStringSlices(got, []string{"TAAK-001"}) {
		t.Errorf("property filter ids = %v, want [TAAK-001] (relation mis-route bug returns wrong set)", got)
	}
}

// TestV1ListUncontrolledRelationNotFilterable pins RR-B0JPPL: a relation with
// NO configured filter_control is not filterable. Before the fix, any metamodel
// relation could be filtered by URL param regardless of whether a control
// exposed it (the pass gated only on GetRelationDef existing). A
// `filter[<uncontrolled-rel>]` param must NOT apply a relation filter — it
// falls through to the property pass and, absent a matching property, fails
// closed (zero rows) rather than silently filtering by an unexposed relation.
func TestV1ListUncontrolledRelationNotFilterable(t *testing.T) {
	meta := relationFilterMeta()
	// Config with NO filter controls at all.
	cfg := &dataentryconfig.Config{
		App:   dataentryconfig.AppConfig{Name: "Test"},
		Lists: map[string]dataentryconfig.List{"taken": {EntityType: "taak", Title: "Taken"}},
	}

	f := newFixture()
	f.AddNode(&entity.Entity{ID: "PRJ-001", Type: "project", Properties: map[string]any{"name": "Apollo"}})
	f.AddNode(&entity.Entity{ID: "TAAK-001", Type: "taak", Properties: map[string]any{"title": "A"}})
	f.AddNode(&entity.Entity{ID: "TAAK-002", Type: "taak", Properties: map[string]any{"title": "B"}})
	f.AddEdge(entity.NewRelation("TAAK-001", "belongs_to", "PRJ-001")) // Apollo
	// TAAK-002 has no belongs_to edge.

	app := newAppFromParts(cfg, meta, f)

	// belongs_to is a real relation but has no control. A relation filter would
	// return [TAAK-001]; the correct (not-filterable) behavior routes it to the
	// property pass, where `belongs_to` is not a property → fail closed, zero
	// rows. Either way it must NOT return exactly [TAAK-001] via relation logic.
	got := listTaken(t, app, url.Values{"filter[belongs_to]": {"Apollo"}})
	if len(got) == 1 && got[0] == "TAAK-001" {
		t.Errorf("uncontrolled relation was filtered (got %v); relation filtering must require a configured control", got)
	}
	if !equalStringSlices(got, []string{}) {
		t.Errorf("ids = %v, want [] (uncontrolled relation key fails closed as a property)", got)
	}
}

// TestV1ListRelationFilterACL pins RR-HK1XNO: a relation filter must not become
// an inference channel for entities the caller cannot read. Matching neighbor
// titles are resolved through the read gate, so filtering by the title of a
// HIDDEN neighbor does NOT include the rows edged to it (which would otherwise
// leak the hidden neighbor's existence and title via row inclusion).
func TestV1ListRelationFilterACL(t *testing.T) {
	app := relationFilterApp(t)

	// alice may read taak (and project) but NOT persoon. So the incoming
	// verantwoordelijk_voor sources (the persoons) are hidden.
	d := mustNewACL(t, &acl.Policy{
		Roles:       map[string]acl.RoleDef{"viewer": {Read: []string{"taak", "project"}}},
		Assignments: map[string]string{"alice": "viewer"},
	}, app.store)
	app.acl = d

	listAs := func(t *testing.T, query url.Values) []string {
		t.Helper()
		path := "/api/v1/taaks"
		if enc := query.Encode(); enc != "" {
			path += "?" + enc
		}
		req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
		req = req.WithContext(gateCtxFor(aliceCtx(), t, d))
		rec := httptest.NewRecorder()
		app.handleV1ListEntities(rec, req, "taak", "taaks")
		if rec.Code != http.StatusOK {
			t.Fatalf("list status: got %d, body %s", rec.Code, rec.Body.String())
		}
		var resp v1.ListResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		ids := make([]string, 0, len(resp.Data))
		for _, e := range resp.Data {
			ids = append(ids, e.ID)
		}
		return ids
	}

	t.Run("filter by hidden neighbor title returns no rows", func(t *testing.T) {
		// Jeroen (a persoon) is hidden from alice; filtering taak by his title
		// must NOT surface TAAK-001/TAAK-003 (which would leak his existence).
		got := listAs(t, url.Values{"filter[verantwoordelijk_voor]": {"Jeroen Vloothuis"}, "sort": {"title"}})
		if len(got) != 0 {
			t.Errorf("ids = %v, want [] — hidden neighbor title must not be an inference channel", got)
		}
	})

	t.Run("filter by VISIBLE neighbor title still works (gate not over-filtering)", func(t *testing.T) {
		// project is readable, so the outgoing belongs_to filter still matches.
		got := listAs(t, url.Values{"filter[belongs_to]": {"Apollo"}, "sort": {"title"}})
		if !equalStringSlices(got, []string{"TAAK-001", "TAAK-003"}) {
			t.Errorf("ids = %v, want [TAAK-001 TAAK-003] — visible-neighbor filter must still match", got)
		}
	})
}
