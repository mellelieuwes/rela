package dataentry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	v1 "github.com/Sourcehaven-BV/rela/internal/apiwire/v1"
	"github.com/Sourcehaven-BV/rela/internal/entity"
)

// searchLimited performs GET /api/v1/_search with a `limit` parameter.
func searchLimited(
	ctx context.Context, t *testing.T, app *App, q, limit string,
) (v1.ListResponse, *httptest.ResponseRecorder) {
	t.Helper()
	params := url.Values{"q": {q}}
	if limit != "" {
		params.Set("limit", limit)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/_search?"+params.Encode(), http.NoBody)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	app.handleV1Search(rec, req)

	var resp v1.ListResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode search response: %v\nbody: %s", err, rec.Body)
		}
	}
	return resp, rec
}

func limitedIDs(resp v1.ListResponse) []string {
	out := make([]string, 0, len(resp.Data))
	for _, e := range resp.Data {
		out = append(out, e.ID)
	}
	return out
}

// TestSearchLimit_RecentlyModified pins the mention menu's "recently modified"
// query: sorted newest first, then cut to `limit`.
func TestSearchLimit_RecentlyModified(t *testing.T) {
	app := newTestAppV1(t)
	for _, id := range []string{"TKT-001", "TKT-002", "TKT-003"} {
		seedEntity(app, &entity.Entity{ID: id, Type: "ticket", Properties: map[string]any{"title": id}})
	}
	// Touch the oldest so it becomes the newest.
	if err := app.store.UpdateEntity(context.Background(), &entity.Entity{
		ID: "TKT-001", Type: "ticket", Properties: map[string]any{"title": "touched"},
	}); err != nil {
		t.Fatal(err)
	}

	resp, rec := searchLimited(context.Background(), t, app, "type:ticket sort:modified:desc", "2")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /_search: %d %s", rec.Code, rec.Body)
	}
	got := limitedIDs(resp)
	if len(got) != 2 || got[0] != "TKT-001" || got[1] != "TKT-003" {
		t.Errorf("got %v, want [TKT-001 TKT-003]", got)
	}
	if resp.Meta.Total != 2 {
		t.Errorf("meta.total = %d, want 2", resp.Meta.Total)
	}
}

// TestSearchLimit_CountsOnlyVisibleRows pins that the cut happens after the
// read gate: newer hidden rows must not take the visible rows' slots.
func TestSearchLimit_CountsOnlyVisibleRows(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{ID: "TKT-001", Type: "ticket", Properties: map[string]any{"title": "a"}})
	seedEntity(app, &entity.Entity{ID: "TKT-002", Type: "ticket", Properties: map[string]any{"title": "b"}})
	for _, id := range []string{"FEAT-001", "FEAT-002", "FEAT-003"} {
		seedEntity(app, &entity.Entity{ID: id, Type: "feature", Properties: map[string]any{"title": id}})
	}

	d := mustNewACL(t, &acl.Policy{
		Roles:       map[string]acl.RoleDef{"viewer": {Read: []string{"ticket"}}},
		Assignments: map[string]string{"alice": "viewer"},
	}, app.store)
	app.acl = d

	ctx := gateCtxFor(aliceCtx(), t, d)
	resp, rec := searchLimited(ctx, t, app, "type:ticket,feature sort:modified:desc", "2")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /_search: %d %s", rec.Code, rec.Body)
	}
	got := limitedIDs(resp)
	if len(got) != 2 || got[0] != "TKT-002" || got[1] != "TKT-001" {
		t.Errorf("got %v, want the two visible tickets [TKT-002 TKT-001]", got)
	}
}

func TestSearchLimit_Invalid(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{ID: "TKT-001", Type: "ticket", Properties: map[string]any{"title": "a"}})

	for _, q := range []string{"type:ticket", ""} {
		for _, limit := range []string{"0", "-1", "abc", "101"} {
			t.Run(q+"/"+limit, func(t *testing.T) {
				_, rec := searchLimited(context.Background(), t, app, q, limit)
				if rec.Code != http.StatusBadRequest {
					t.Errorf("q=%q limit=%s: got %d, want 400", q, limit, rec.Code)
				}
			})
		}
	}
}

// TestSearchTypeParam_ScopesFreeText pins that `type` scopes a free-text
// search, and that an undeclared type matches nothing rather than widening.
func TestSearchTypeParam_ScopesFreeText(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{ID: "TKT-001", Type: "ticket", Properties: map[string]any{"title": "alpha"}})
	seedEntity(app, &entity.Entity{ID: "FEAT-001", Type: "feature", Properties: map[string]any{"title": "alpha"}})

	for _, tc := range []struct {
		name, typ string
		want      []string
	}{
		{"declared type", "ticket", []string{"TKT-001"}},
		{"undeclared type", "ticket sort:title", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := url.Values{"q": {"alpha"}, "type": {tc.typ}}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/_search?"+params.Encode(), http.NoBody)
			rec := httptest.NewRecorder()
			app.handleV1Search(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /_search: %d %s", rec.Code, rec.Body)
			}
			var resp v1.ListResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if got := limitedIDs(resp); !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
