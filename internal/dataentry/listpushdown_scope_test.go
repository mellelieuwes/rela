package dataentry

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/appbuild"
	"github.com/Sourcehaven-BV/rela/internal/appbuild/appbuildtest"
	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
	"github.com/Sourcehaven-BV/rela/internal/store/storetest"
)

// A query scope that lowers exactly is served by the store, and the store's
// page must be the Go path's page (TKT-XKCNCL).

const scopePushdownSchema = `version: "1.0"
namespace: https://example.org/test#
entities:
  ticket:
    label: Ticket
    plural: tickets
    id_prefix: TKT
    properties:
      title: {type: string, required: true}
      status: {type: string}
      assignee: {type: string}
    query_scopes:
      impl-open: "related(entity, 'implements', { status = 'open' })"
      reported: "related(entity, 'reportedBy')"
      open-reported-impl: "entity.status == 'open' and related(entity, 'implements') and related(entity, 'reportedBy')"
      chain: "related(entity, {'implements', 'ownedBy'})"
      mine: "is_current_user(entity.assignee) and related(entity, 'implements')"
      not-reported: "not related(entity, 'reportedBy')"
      mine-reported: "related(entity, 'reportedBy', { id = current_user.id })"
      mine-owned-open: "entity.status == 'open' and related(entity, {'implements', 'ownedBy'}, { id = current_user.id })"
      not-mine-reported: "not related(entity, 'reportedBy', { id = current_user.id })"
  feature:
    label: Feature
    plural: features
    id_prefix: FEAT
    properties:
      title: {type: string, required: true}
      status: {type: string}
    query_scopes:
      impl-reported: "related(entity, {'implementedBy', 'reportedBy'})"
  user:
    label: User
    plural: users
    id_prefix: USR
    properties:
      title: {type: string}
relations:
  implements: {label: implements, from: [ticket], to: [feature], inverse: implementedBy}
  editor-of: {label: editor of, from: [user], to: [ticket]}
  reports: {label: reports, from: [user], to: [ticket], inverse: reportedBy}
  owns: {label: owns, from: [user], to: [feature], inverse: ownedBy}
`

// lowerableScopes are the fixture's scopes that queryplan.LowerScope accepts.
var lowerableScopes = map[string]bool{
	"impl-open": true, "reported": true, "open-reported-impl": true, "chain": true, "mine": true,
	"mine-reported": true, "mine-owned-open": true,
}

// newScopePushdownApp seeds twelve tickets over a counting store. Every
// relation varies with the ticket number, so each scope keeps a different
// subset, and titles collide so the id tiebreak is exercised.
//
// Principals: alice reads everything; bob reads tickets ONLY through
// editor-of (so his read gate occupies HasInbound) and users and features
// through a role; dave reads tickets and features but not users, so a
// traversal into users is denied for him; erin reads no tickets at all.
func newScopePushdownApp(t *testing.T) (*App, *acl.Declarative, *storetest.Counting) {
	t.Helper()
	return newScopePushdownAppOn(t, memstore.New())
}

// newScopePushdownAppOn is newScopePushdownApp over base, so the comparison
// can run on every backend.
func newScopePushdownAppOn(t *testing.T, base store.Store) (*App, *acl.Declarative, *storetest.Counting) {
	t.Helper()
	meta, err := metamodel.Parse([]byte(scopePushdownSchema))
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	cfg := &dataentryconfig.Config{
		Forms:   make(map[string]dataentryconfig.Form),
		Lists:   make(map[string]dataentryconfig.List),
		Views:   make(map[string]dataentryconfig.ViewConfig),
		Kanbans: make(map[string]dataentryconfig.Kanban),
	}
	counting := storetest.NewCounting(base)
	app := newAppFromParts(cfg, meta, newFixture(), appbuildtest.WithStore(counting))
	if err := app.SetQueryScopeResolver(AdaptQueryScopes(appbuild.QueryScopes)); err != nil {
		t.Fatalf("wire query scopes: %v", err)
	}
	for i := 1; i <= 3; i++ {
		seedEntity(app, &entity.Entity{ID: fmt.Sprintf("FEAT-%d", i), Type: "feature", Properties: map[string]any{
			"title": fmt.Sprintf("Feature %d", i), "status": []string{"open", "done", "open"}[i-1],
		}})
	}
	for _, u := range []string{"alice", "bob", "dave"} {
		seedEntity(app, &entity.Entity{ID: u, Type: "user", Properties: map[string]any{"title": u}})
	}
	seedRelation(app, &entity.Relation{From: "alice", Type: "owns", To: "FEAT-1"})
	seedRelation(app, &entity.Relation{From: "bob", Type: "owns", To: "FEAT-3"})
	titles := []string{"b", "a", "B", "c", "a", "b"}
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("TKT-%02d", i)
		props := map[string]any{
			"title":  titles[i%len(titles)],
			"status": []string{"open", "done", "open", "blocked"}[i%4],
		}
		if i%3 != 0 {
			props["assignee"] = []string{"alice", "bob"}[i%2]
		}
		seedEntity(app, &entity.Entity{ID: id, Type: "ticket", Properties: props})
		if i%4 != 0 {
			seedRelation(app, &entity.Relation{From: id, Type: "implements", To: fmt.Sprintf("FEAT-%d", i%3+1)})
		}
		if i%2 == 0 {
			seedRelation(app, &entity.Relation{From: []string{"alice", "dave"}[i%4/2], Type: "reports", To: id})
		}
		if i <= 7 {
			seedRelation(app, &entity.Relation{From: "bob", Type: "editor-of", To: id})
		}
	}
	d := mustNewACL(t, &acl.Policy{
		Roles: map[string]acl.RoleDef{
			"all":      {Read: []string{"ticket", "feature", "user"}},
			"browse":   {Read: []string{"feature", "user"}},
			"tickets":  {Read: []string{"ticket", "feature"}},
			"features": {Read: []string{"feature"}},
			"editor":   {Read: []string{"ticket"}},
		},
		RoleRelations: map[string]acl.RoleRelationDef{"editor-of": {Confers: "editor"}},
		Assignments:   map[string]string{"alice": "all", "bob": "browse", "dave": "tickets", "erin": "features"},
	}, app.store)
	app.acl = d
	return app, d, counting
}

func TestListPushdown_ScopeMatchesGoPath(t *testing.T) {
	app, d, counting := newScopePushdownApp(t)
	assertScopeMatchesGoPath(t, app, d, counting)
}

// assertScopeMatchesGoPath serves every scope for every principal through the
// list API and compares each page and total with the Go path.
func assertScopeMatchesGoPath(t *testing.T, app *App, d *acl.Declarative, counting *storetest.Counting) {
	t.Helper()
	scopes := []string{
		"impl-open", "reported", "open-reported-impl", "chain", "mine", "not-reported",
		"mine-reported", "mine-owned-open", "not-mine-reported",
	}
	for _, user := range []string{"alice", "bob", "dave", "erin"} {
		for _, scope := range scopes {
			for _, rq := range []string{"", "sort=title", "sort=-title", "filter%5Bstatus%5D=open&sort=title"} {
				for _, pg := range []struct{ page, perPage int }{{1, 3}, {2, 3}, {1, 50}} {
					q := "query_scope=" + scope
					if rq != "" {
						q += "&" + rq
					}
					q += fmt.Sprintf("&per_page=%d&page=%d", pg.perPage, pg.page)
					t.Run(user+" "+q, func(t *testing.T) {
						counting.Reset()
						resp, rec := listEntitiesAs(principalCtx(user), t, app, d, "ticket", "tickets", q)
						if rec.Code != http.StatusOK {
							t.Fatalf("status %d: %s", rec.Code, rec.Body)
						}
						calls := counting.Calls()

						ctx, err := bindQueryIdentity(gateCtxFor(principalCtx(user), t, d), mustScope(t, app, scope))
						if err != nil {
							t.Fatal(err)
						}
						wantIDs, wantTotal := goPath(ctx, t, app, "ticket", parseQuery(q), pg.page, pg.perPage)
						var gotIDs []string
						for _, e := range resp.Data {
							gotIDs = append(gotIDs, e.ID)
						}
						if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
							t.Errorf("rows differ: served %v, go %v", gotIDs, wantIDs)
						}
						if resp.Meta.Total != wantTotal {
							t.Errorf("total: served %d, go %d", resp.Meta.Total, wantTotal)
						}

						// The comparison above is only worth something if the
						// served page came from the store. A lowerable scope
						// for a principal who may read tickets must never
						// answer its traversal over a candidate set.
						if lowerableScopes[scope] && user != "erin" && calls["MatchingIDs"] != 0 {
							t.Errorf("%s was not pushed down for %s: %s", scope, user, counting)
						}
						if !lowerableScopes[scope] && user != "erin" && calls["CountMatched"] != 0 {
							t.Errorf("%s must stay on the Go path: %s", scope, counting)
						}
					})
				}
			}
		}
	}
}

// bob reads tickets only through editor-of, so his read gate sits in
// HasInbound. An incoming scope must narrow within that gate, never replace
// it: every served ticket is one bob edits AND one somebody reports.
func TestListPushdown_IncomingScopeKeepsRelationGate(t *testing.T) {
	app, d, counting := newScopePushdownApp(t)
	resp, rec := listEntitiesAs(principalCtx("bob"), t, app, d, "ticket", "tickets", "query_scope=reported&per_page=50")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got []string
	for _, e := range resp.Data {
		got = append(got, e.ID)
	}
	// Edited by bob: TKT-01..07. Reported: even numbers.
	want := []string{"TKT-02", "TKT-04", "TKT-06"}
	if strings.Join(got, ",") != strings.Join(want, ",") || resp.Meta.Total != len(want) {
		t.Fatalf("got %v (total %d), want %v", got, resp.Meta.Total, want)
	}
	if counting.Calls()["CountMatched"] != 1 {
		t.Errorf("expected the pushed path: %s", counting)
	}
}

// dave may not read users, so `related(entity, 'reportedBy')` is denied for
// him. The answer is an empty page, as on the Go path, and it is reached
// without reading the ticket type.
func TestListPushdown_DeniedTraversalIsEmptyWithoutAScan(t *testing.T) {
	app, d, counting := newScopePushdownApp(t)
	counting.Reset()
	resp, rec := listEntitiesAs(principalCtx("dave"), t, app, d, "ticket", "tickets", "query_scope=reported")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(resp.Data) != 0 || resp.Meta.Total != 0 {
		t.Fatalf("got %d rows, total %d; want none", len(resp.Data), resp.Meta.Total)
	}
	calls := counting.Calls()
	if calls["ListEntityHeaders"] != 0 || calls["GraphQueryHeaders"] != 0 || calls["CountMatched"] != 0 {
		t.Errorf("a denied traversal read the store: %s", counting)
	}
}

// A traversal the gate refuses must fail the request as the Go path does,
// never answer an empty page. For bob, the chain lands on ticket, which he
// reads through editor-of, and then walks backwards: GateTraversal refuses
// that shape, so Lower declines and the Go path reports the 422.
func TestListPushdown_RefusedTraversalFailsTheRequest(t *testing.T) {
	app, d, _ := newScopePushdownApp(t)
	_, rec := listEntitiesAs(principalCtx("bob"), t, app, d, "feature", "features", "query_scope=impl-reported")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "query_scope_unsupported") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func mustScope(t *testing.T, app *App, name string) resolvedQueryScope {
	t.Helper()
	scope, err := viewQueryScope(app.queryScopes, app.Cfg(), app.Meta(), "ticket", name)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

// The position endpoint shares the page's plan, so a lowered scope narrows
// both: the store-answered position agrees with the Go path, and a denied
// traversal is a 404 without a read.
func TestPosition_ScopeMatchesGoPath(t *testing.T) {
	app, d, counting := newScopePushdownApp(t)
	for _, user := range []string{"alice", "bob", "dave"} {
		for scopeName := range lowerableScopes {
			t.Run(user+" "+scopeName, func(t *testing.T) {
				scope := ScopeDescriptor{Source: "list", Type: "ticket", Sort: "title", QueryScope: scopeName}
				ctx, err := bindQueryIdentity(gateCtxFor(principalCtx(user), t, d), mustScope(t, app, scopeName))
				if err != nil {
					t.Fatal(err)
				}
				want, total := goPath(ctx, t, app, "ticket", scope.toQuery(), 1, 1000)
				for i, id := range want {
					req := httptest.NewRequest(http.MethodGet, positionURL(t, id, scope), http.NoBody).WithContext(ctx)
					rec := httptest.NewRecorder()
					pos, handled := storePosition(app, rec, req, scope, id)
					if !handled || pos == nil {
						t.Fatalf("%s: store path declined or failed: %d %s", id, rec.Code, rec.Body)
					}
					if pos.Current != i+1 || pos.Total != total {
						t.Errorf("%s: store %d/%d, go %d/%d", id, pos.Current, pos.Total, i+1, total)
					}
				}
				// dave may not read users, so a scope walking reportedBy is
				// denied for him rather than merely empty.
				if user != "dave" || !strings.Contains(scopeName, "reported") {
					return
				}
				counting.Reset()
				req := httptest.NewRequest(http.MethodGet, positionURL(t, "TKT-02", scope), http.NoBody).WithContext(ctx)
				rec := httptest.NewRecorder()
				pos, handled := storePosition(app, rec, req, scope, "TKT-02")
				if !handled || pos != nil || rec.Code != http.StatusNotFound {
					t.Fatalf("empty scope: handled=%v pos=%v code=%d", handled, pos, rec.Code)
				}
				if calls := counting.Calls(); calls["GraphQuery"] != 0 || calls["GraphCount"] != 0 {
					t.Errorf("an empty scope read the store: %s", counting)
				}
			})
		}
	}
}

// A traversal constrained by current_user.id answers per caller, in the store.
// The equivalence harness above proves the pushed page equals the Go page;
// this pins what the page IS, so both paths returning nothing cannot pass.
func TestListPushdown_CurrentUserTraversal(t *testing.T) {
	app, d, counting := newScopePushdownApp(t)
	for _, tc := range []struct {
		user, scope string
		want        []string
	}{
		// alice reports TKT-04, 08 and 12; dave reports 02, 06 and 10.
		{"alice", "mine-reported", []string{"TKT-04", "TKT-08", "TKT-12"}},
		{"bob", "mine-reported", nil},
		// dave may not read users, so his own reports do not count.
		{"dave", "mine-reported", nil},
		// alice owns FEAT-1, bob FEAT-3; only open tickets implementing them.
		{"alice", "mine-owned-open", []string{"TKT-06"}},
		{"bob", "mine-owned-open", []string{"TKT-02"}},
	} {
		t.Run(tc.user+" "+tc.scope, func(t *testing.T) {
			counting.Reset()
			resp, rec := listEntitiesAs(principalCtx(tc.user), t, app, d, "ticket", "tickets",
				"query_scope="+tc.scope+"&per_page=50")
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			var got []string
			for _, e := range resp.Data {
				got = append(got, e.ID)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") || resp.Meta.Total != len(tc.want) {
				t.Fatalf("got %v (total %d), want %v", got, resp.Meta.Total, tc.want)
			}
			if tc.user != "dave" && (counting.Calls()["CountMatched"] != 1 || counting.Calls()["MatchingIDs"] != 0) {
				t.Errorf("expected the pushed path: %s", counting)
			}
		})
	}
}
