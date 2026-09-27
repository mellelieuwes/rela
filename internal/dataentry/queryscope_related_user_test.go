package dataentry

import (
	"context"
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
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
	"github.com/Sourcehaven-BV/rela/internal/store/storetest"
)

// relatedUserSchema is the atlas case (TKT-NXELMW): responsibility for a task
// is a RELATION from a persoon, not a property of the task, and the persoon is
// the user entity a principal resolves to.
const relatedUserSchema = `version: "1.0"
namespace: https://example.org/test#
entities:
  taak:
    label: Taak
    plural: taken
    id_prefix: TAAK
    properties:
      title: {type: string, required: true}
      status: {type: string}
    query_scopes:
      mijn: "entity.status ~= 'gereed' and related(entity, 'heeft_verantwoordelijke', { id = current_user.id })"
      alles-van-mij: "related(entity, 'heeft_verantwoordelijke', { id = current_user.id })"
      niet-van-mij: "not related(entity, 'heeft_verantwoordelijke', { id = current_user.id })"
  persoon:
    label: Persoon
    plural: personen
    id_prefix: PER
    properties:
      title: {type: string}
      pratique_sub: {type: string}
relations:
  verantwoordelijk_voor:
    label: verantwoordelijk voor
    from: [persoon]
    to: [taak]
    inverse: heeft_verantwoordelijke
`

// newRelatedUserApp seeds two personen and four tasks. PER-1 is responsible
// for TAAK-1 (open) and TAAK-2 (gereed); PER-2 for TAAK-3; nobody for TAAK-4.
func newRelatedUserApp(t *testing.T) (*App, *acl.Declarative, *storetest.Counting) {
	t.Helper()
	app, counting := newRelatedUserAppNoACL(t)
	d, err := acl.NewDeclarative(&acl.Policy{
		UserEntityType:    "persoon",
		PrincipalProperty: "pratique_sub",
		Roles:             map[string]acl.RoleDef{"everyone": {Read: []string{"*"}}},
	}, acl.NewStoreGraph(counting), counting, acl.WithPrincipalLookup(acl.NewStorePrincipalLookup(counting)))
	if err != nil {
		t.Fatalf("NewDeclarative: %v", err)
	}
	app.acl = d
	return app, d, counting
}

// newRelatedUserAppNoACL is the same fixture with no policy, as a project
// without acl.yaml serves it.
func newRelatedUserAppNoACL(t *testing.T) (*App, *storetest.Counting) {
	t.Helper()
	meta, err := metamodel.Parse([]byte(relatedUserSchema))
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	cfg := &dataentryconfig.Config{
		Forms:   make(map[string]dataentryconfig.Form),
		Lists:   make(map[string]dataentryconfig.List),
		Views:   make(map[string]dataentryconfig.ViewConfig),
		Kanbans: make(map[string]dataentryconfig.Kanban),
	}
	counting := storetest.NewCounting(memstore.New())
	app := newAppFromParts(cfg, meta, newFixture(), appbuildtest.WithStore(counting))
	if err := app.SetQueryScopeResolver(AdaptQueryScopes(appbuild.QueryScopes)); err != nil {
		t.Fatalf("wire query scopes: %v", err)
	}
	for _, e := range []*entity.Entity{
		{ID: "PER-1", Type: "persoon", Properties: map[string]any{"title": "Alice", "pratique_sub": "sub-alice"}},
		{ID: "PER-2", Type: "persoon", Properties: map[string]any{"title": "Bob", "pratique_sub": "sub-bob"}},
		{ID: "TAAK-1", Type: "taak", Properties: map[string]any{"title": "a", "status": "open"}},
		{ID: "TAAK-2", Type: "taak", Properties: map[string]any{"title": "b", "status": "gereed"}},
		{ID: "TAAK-3", Type: "taak", Properties: map[string]any{"title": "c", "status": "open"}},
		{ID: "TAAK-4", Type: "taak", Properties: map[string]any{"title": "d", "status": "open"}},
	} {
		seedEntity(app, e)
	}
	for _, r := range []*entity.Relation{
		{From: "PER-1", Type: "verantwoordelijk_voor", To: "TAAK-1"},
		{From: "PER-1", Type: "verantwoordelijk_voor", To: "TAAK-2"},
		{From: "PER-2", Type: "verantwoordelijk_voor", To: "TAAK-3"},
	} {
		seedRelation(app, r)
	}
	return app, counting
}

// relatedUserList lists taken as the router would serve raw principal user:
// resolvePrincipalEntity maps it to its persoon first, as it does on /api/.
func relatedUserList(t *testing.T, app *App, d *acl.Declarative, user, scope string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/taken?query_scope="+scope, http.NoBody)
	ctx := principal.With(context.Background(), principal.Principal{User: user, Tool: principal.ToolDataEntry})
	ctx = resolvePrincipalEntity(ctx, d, req, false)
	_, rec := listEntitiesAs(ctx, t, app, d, "taak", "taken", "query_scope="+scope)
	return rec
}

func TestQueryScope_RelatedToCurrentUser(t *testing.T) {
	app, d, counting := newRelatedUserApp(t)
	for _, tc := range []struct {
		name, user, scope string
		want              []string
		pushed            bool
	}{
		// `~=` keeps mijn on the Go path; the traversal is still answered
		// with one gated store query.
		{"alice mijn", "sub-alice", "mijn", []string{"TAAK-1"}, false},
		{"bob mijn", "sub-bob", "mijn", []string{"TAAK-3"}, false},
		{"alice alles", "sub-alice", "alles-van-mij", []string{"TAAK-1", "TAAK-2"}, true},
		{"bob alles", "sub-bob", "alles-van-mij", []string{"TAAK-3"}, true},
		{"alice niet", "sub-alice", "niet-van-mij", []string{"TAAK-3", "TAAK-4"}, false},
		// An identified principal who maps to no persoon keeps the raw
		// identifier, which is no persoon's id: nothing matches.
		{"unmapped alles", "sub-mallory", "alles-van-mij", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counting.Reset()
			rec := relatedUserList(t, app, d, tc.user, tc.scope)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			if got := idsFromListBody(t, rec); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("rows = %v, want %v", got, tc.want)
			}
			calls := counting.Calls()
			if tc.pushed && (calls["CountMatched"] != 1 || calls["MatchingIDs"] != 0) {
				t.Errorf("expected the scope to be answered by the store query: %s", counting)
			}
			if !tc.pushed && calls["MatchingIDs"] != 1 {
				t.Errorf("expected one batched traversal query on the Go path: %s", counting)
			}
		})
	}
}

// An unidentified caller has no current_user.id. With an ACL policy the
// request never gets this far (ForPrincipal refuses an unstamped principal);
// without one it does, as the "unknown" placeholder the server stamps with no
// identity source. Every such scope must then fail the request rather than
// answer it: an unbound id reaching the store would be "any persoon", and
// under `not` no match would be every task.
func TestQueryScope_RelatedToCurrentUserWithoutIdentityFails(t *testing.T) {
	app, counting := newRelatedUserAppNoACL(t)
	for _, scope := range []string{"mijn", "alles-van-mij", "niet-van-mij"} {
		for _, user := range []string{principal.Unknown, ""} {
			t.Run(scope+" "+user, func(t *testing.T) {
				counting.Reset()
				req := httptest.NewRequest(http.MethodGet, "/api/v1/taken?query_scope="+scope, http.NoBody)
				if user != "" {
					req = req.WithContext(principal.With(req.Context(),
						principal.Principal{User: user, Tool: principal.ToolDataEntry}))
				}
				rec := httptest.NewRecorder()
				app.handleV1ListEntities(rec, req, "taak", "taken")
				if rec.Code == http.StatusOK {
					t.Fatalf("an identity scope without an identity answered 200: %s", rec.Body)
				}
				if strings.Contains(rec.Body.String(), "TAAK-") {
					t.Fatalf("the error leaked rows: %s", rec.Body)
				}
				if n := counting.Calls()["MatchingIDs"] + counting.Calls()["CountMatched"]; n != 0 {
					t.Errorf("the traversal reached the store without an identity: %s", counting)
				}
			})
		}
	}
	// The same app answers an identified caller, so the failure above is the
	// missing identity and not a broken fixture.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/taken?query_scope=alles-van-mij", http.NoBody)
	req = req.WithContext(principal.With(req.Context(), principal.Principal{User: "PER-2", Tool: principal.ToolDataEntry}))
	rec := httptest.NewRecorder()
	app.handleV1ListEntities(rec, req, "taak", "taken")
	if got := idsFromListBody(t, rec); strings.Join(got, ",") != "TAAK-3" {
		t.Fatalf("identified caller without ACL: %v", got)
	}
}

// A list `condition:` binds current_user.id the same way a scope does, and
// fails the page for a caller without an identity.
func TestViewCondition_RelatedToCurrentUser(t *testing.T) {
	withCondition := func(t *testing.T, app *App) {
		t.Helper()
		app.Cfg().Lists["taken"] = dataentryconfig.List{
			EntityType: "taak", Title: "Taken",
			Condition: "not related(entity, 'heeft_verantwoordelijke', { id = current_user.id })",
		}
		if err := app.SetViewConditions(AdaptViewConditions(appbuild.ViewConditions)); err != nil {
			t.Fatalf("wire view conditions: %v", err)
		}
	}

	app, d, _ := newRelatedUserApp(t)
	withCondition(t, app)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/taken", http.NoBody)
	ctx := resolvePrincipalEntity(principal.With(context.Background(),
		principal.Principal{User: "sub-alice", Tool: principal.ToolDataEntry}), d, req, false)
	_, rec := listEntitiesAs(ctx, t, app, d, "taak", "taken", "list_id=taken")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := idsFromListBody(t, rec); strings.Join(got, ",") != "TAAK-3,TAAK-4" {
		t.Fatalf("alice's not-mine list = %v, want TAAK-3,TAAK-4", got)
	}

	anon, _ := newRelatedUserAppNoACL(t)
	withCondition(t, anon)
	req = httptest.NewRequest(http.MethodGet, "/api/v1/taken?list_id=taken", http.NoBody)
	req = req.WithContext(principal.With(req.Context(),
		principal.Principal{User: principal.Unknown, Tool: principal.ToolDataEntry}))
	rec = httptest.NewRecorder()
	anon.handleV1ListEntities(rec, req, "taak", "taken")
	if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "TAAK-") {
		t.Fatalf("a current_user condition without an identity answered: %d %s", rec.Code, rec.Body)
	}
}

// A principal whose role cannot read the user entity type cannot see its own
// persoon, so the hop lands on nothing: mijn is empty and niet-van-mij is
// every task the reader can see. Hidden means nonexistent, including to its
// owner.
func TestQueryScope_RelatedToCurrentUserUnreadableUserType(t *testing.T) {
	app, _, counting := newRelatedUserApp(t)
	d, err := acl.NewDeclarative(&acl.Policy{
		UserEntityType:    "persoon",
		PrincipalProperty: "pratique_sub",
		Roles:             map[string]acl.RoleDef{"everyone": {Read: []string{"taak"}}},
		Assignments:       map[string]string{"*": "everyone"},
	}, acl.NewStoreGraph(counting), counting, acl.WithPrincipalLookup(acl.NewStorePrincipalLookup(counting)))
	if err != nil {
		t.Fatalf("NewDeclarative: %v", err)
	}
	app.acl = d
	for _, tc := range []struct {
		scope string
		want  []string
	}{
		{"alles-van-mij", nil},
		{"niet-van-mij", []string{"TAAK-1", "TAAK-2", "TAAK-3", "TAAK-4"}},
	} {
		t.Run(tc.scope, func(t *testing.T) {
			rec := relatedUserList(t, app, d, "sub-alice", tc.scope)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			if got := idsFromListBody(t, rec); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("rows = %v, want %v", got, tc.want)
			}
		})
	}
}
