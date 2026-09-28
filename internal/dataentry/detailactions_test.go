package dataentry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/affordances"
	v1 "github.com/Sourcehaven-BV/rela/internal/apiwire/v1"
	"github.com/Sourcehaven-BV/rela/internal/appbuild"
	"github.com/Sourcehaven-BV/rela/internal/appbuild/appbuildtest"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/project"
	"github.com/Sourcehaven-BV/rela/internal/storage"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// Detail-page actions (TKT-VVS16W). Every case here checks BOTH halves of
// the contract: the `_actions["action:<id>"]` key on the entity GET and the
// POST /_action/{id} gate. They share one decision function, and these tests
// pin that a hidden button is always a refused POST and a shown one runs.

// detailMeta declares a faced `document` with a kind, and a faceless `note`
// the scripts create as a marker that they ran.
func detailMeta(t *testing.T) *metamodel.Metamodel {
	t.Helper()
	m, err := metamodel.Parse([]byte(`
entities:
  document:
    label: Document
    id_prefix: DOC
    faces:
      concept: {}
      approved: {}
    properties:
      title: { type: string }
      kind: { type: string }
      owner: { type: string }
  note:
    label: Note
    id_prefix: NOTE
    properties:
      title: { type: string }
`))
	if err != nil {
		t.Fatalf("parse metamodel: %v", err)
	}
	return m
}

// regenerateScript rewrites the concept's content and leaves a marker note,
// so a test can tell from the store whether the script ran.
const regenerateScript = `
rela.create_entity("note", { title = "ran:" .. entity.id .. "@" .. entity.face .. ":" .. tostring(entity.properties.owner) .. ":redacted=" .. tostring(entity:is_redacted("owner")) })
rela.update_entity(entity.id .. "@" .. entity.face, {}, "regenerated")
return { message = "Regenerated" }
`

// detailPolicy gives alice read+update on documents and create on notes plus
// the regenerate permission; bob gets the same without the permission.
func detailPolicy() *acl.Policy {
	role := func(perms ...string) acl.RoleDef {
		return acl.RoleDef{
			Read:        []string{"document", "note"},
			Update:      []string{"document@concept", "document@approved"},
			Create:      []string{"note"},
			Permissions: perms,
		}
	}
	return &acl.Policy{
		Roles: map[string]acl.RoleDef{
			"editor":   role("documents:regenerate"),
			"reviewer": role(),
			"outsider": {},
		},
		Assignments: map[string]string{"alice": "editor", "bob": "reviewer", "mallory": "outsider"},
	}
}

type detailApp struct {
	app   *App
	decl  *acl.Declarative
	store store.Store
	audit *audit.Memory
}

// newDetailActionApp builds an App over detailMeta with a real script
// directory, a Declarative policy and the production condition compiler.
func newDetailActionApp(t *testing.T, actions map[string]dataentryconfig.Action) detailApp {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "actions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "actions", "regenerate.lua"),
		[]byte(regenerateScript), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := detailMeta(t)
	fs := storage.NewSafeFS(storage.NewOsFS())
	paths := &project.Context{Root: root, CacheDir: filepath.Join(root, ".rela")}

	st := appbuildtest.New(meta, appbuildtest.WithFS(fs, paths)).Store()
	ctx := context.Background()
	for _, e := range []*entity.Entity{
		{ID: "DOC-1", Type: "document", Face: "concept",
			Properties: map[string]any{"title": "SoA", "kind": "soa", "owner": "SECRET-OWNER"}, Content: "old"},
		{ID: "DOC-1", Type: "document", Face: "approved",
			Properties: map[string]any{"title": "SoA", "kind": "soa"}, Content: "old"},
		{ID: "DOC-2", Type: "document", Face: "concept",
			Properties: map[string]any{"title": "Risks", "kind": "risk_register"}},
		{ID: "NOTE-9", Type: "note", Properties: map[string]any{"title": "n"}},
	} {
		if err := st.CreateEntity(ctx, e); err != nil {
			t.Fatalf("seed %s@%s: %v", e.ID, e.Face, err)
		}
	}

	decl := mustNewACL(t, detailPolicy(), st)
	mem := audit.NewMemory()
	svc := appbuildtest.New(meta, appbuildtest.WithFS(fs, paths), appbuildtest.WithStore(st),
		appbuildtest.WithDeclarative(decl), appbuildtest.WithAudit(mem))
	app := newAppFromParts(&Config{}, nil, newFixture())
	rebindApp(app, fs, paths, svc)
	app.acl = decl
	app.schema.Publish(&Schema{Cfg: &Config{Actions: actions}, Meta: meta})
	if err := app.SetViewConditions(AdaptViewConditions(appbuild.ViewConditions)); err != nil {
		t.Fatal(err)
	}
	if err := app.SetSecurityConfig(SecurityConfig{BindAddress: "127.0.0.1:8080"}); err != nil {
		t.Fatal(err)
	}
	return detailApp{app: app, decl: decl, store: st, audit: mem}
}

// regenerateSoA is the ticket's motivating action.
func regenerateSoA() dataentryconfig.Action {
	return dataentryconfig.Action{
		Label:  "Regenerate",
		Script: "regenerate.lua",
		AvailableOn: &dataentryconfig.ActionScope{
			EntityTypes: []string{"document"}, Faces: []string{"concept"},
		},
		When:       "entity.kind == 'soa'",
		Permission: "documents:regenerate",
		Confirm:    dataentryconfig.ActionConfirm{Enabled: true, Text: "Overwrite the concept?"},
	}
}

// offered reports whether GET <address> carries regenerate-soa's affordance
// key.
func (d detailApp) offered(t *testing.T, user, typeName, plural, address string) bool {
	t.Helper()
	rec := getEntityAs(principalCtx(user), t, d.app, d.decl, typeName, plural, address, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s as %s: %d %s", address, user, rec.Code, rec.Body)
	}
	var got v1.Entity
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got.Actions[detailActionKeyPrefix+"regenerate-soa"]
}

// markers counts the notes the script created, i.e. how often it ran.
func (d detailApp) markers(t *testing.T) []string {
	t.Helper()
	var out []string
	for e, err := range d.store.ListEntities(context.Background(), store.EntityQuery{Type: "note"}) {
		if err != nil {
			t.Fatal(err)
		}
		if title, _ := e.Properties["title"].(string); strings.HasPrefix(title, "ran:") {
			out = append(out, title)
		}
	}
	return out
}

func TestDetailAction_AffordanceAndGateAgree(t *testing.T) {
	tests := []struct {
		name       string
		action     func() dataentryconfig.Action
		user       string
		typeName   string
		plural     string
		address    string
		wantStatus int
		wantCode   string
	}{
		{
			name: "all four match", action: regenerateSoA, user: "alice",
			typeName: "document", plural: "documents", address: "DOC-1@concept",
			wantStatus: http.StatusOK,
		},
		{
			name: "wrong type", action: regenerateSoA, user: "alice",
			typeName: "note", plural: "notes", address: "NOTE-9",
			wantStatus: http.StatusForbidden, wantCode: "action_not_available",
		},
		{
			name: "wrong face", action: regenerateSoA, user: "alice",
			typeName: "document", plural: "documents", address: "DOC-1@approved",
			wantStatus: http.StatusForbidden, wantCode: "action_not_available",
		},
		{
			name: "when false", action: regenerateSoA, user: "alice",
			typeName: "document", plural: "documents", address: "DOC-2@concept",
			wantStatus: http.StatusForbidden, wantCode: "action_not_available",
		},
		{
			name: "permission missing", action: regenerateSoA, user: "bob",
			typeName: "document", plural: "documents", address: "DOC-1@concept",
			wantStatus: http.StatusForbidden, wantCode: "permission_required",
		},
		{
			name: "faces omitted matches every face",
			action: func() dataentryconfig.Action {
				a := regenerateSoA()
				a.AvailableOn.Faces = nil
				return a
			},
			user: "alice", typeName: "document", plural: "documents", address: "DOC-1@approved",
			wantStatus: http.StatusOK,
		},
		{
			name: "no when and no permission",
			action: func() dataentryconfig.Action {
				a := regenerateSoA()
				a.When, a.Permission = "", ""
				return a
			},
			user: "bob", typeName: "document", plural: "documents", address: "DOC-2@concept",
			wantStatus: http.StatusOK,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newDetailActionApp(t, map[string]dataentryconfig.Action{"regenerate-soa": tc.action()})
			wantOffered := tc.wantStatus == http.StatusOK
			if got := d.offered(t, tc.user, tc.typeName, tc.plural, tc.address); got != wantOffered {
				t.Errorf("affordance = %v, want %v", got, wantOffered)
			}

			rec := postAction(principalCtx(tc.user), t, d.app, d.decl, "regenerate-soa",
				`{"entity_id":"`+tc.address+`"}`)
			if rec.Code != tc.wantStatus {
				t.Fatalf("POST status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body)
			}
			ran := len(d.markers(t)) > 0
			if ran != wantOffered {
				t.Errorf("script ran = %v, want %v", ran, wantOffered)
			}
			if tc.wantCode != "" && !strings.Contains(rec.Body.String(), "/errors/"+tc.wantCode+`"`) {
				t.Errorf("error body lacks code %q: %s", tc.wantCode, rec.Body)
			}
		})
	}
}

// An unreadable, absent, bare or missing address is the same 404, so the
// endpoint is not an existence oracle, and the script never runs.
func TestDetailAction_UnreadableEntityIsNotFound(t *testing.T) {
	d := newDetailActionApp(t, map[string]dataentryconfig.Action{"regenerate-soa": regenerateSoA()})
	d.decl = mustNewACL(t, &acl.Policy{
		Roles:       map[string]acl.RoleDef{"permonly": {Permissions: []string{"documents:regenerate"}}},
		Assignments: map[string]string{"mallory": "permonly"},
	}, d.store)
	d.app.acl = d.decl

	var first string
	for _, body := range []string{
		`{"entity_id":"DOC-1@concept"}`, // exists, unreadable
		`{"entity_id":"DOC-404@concept"}`,
		`{"entity_id":"DOC-1"}`, // a faced type has no bare row
		`{}`,
		`{"entity_id":"@@"}`,
	} {
		rec := postAction(principalCtx("mallory"), t, d.app, d.decl, "regenerate-soa", body)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404: %s", body, rec.Code, rec.Body)
		}
		if first == "" {
			first = rec.Body.String()
		} else if rec.Body.String() != first {
			t.Errorf("%s: body differs from the unreadable case:\n%s\n%s", body, rec.Body, first)
		}
	}
	if m := d.markers(t); len(m) != 0 {
		t.Errorf("script ran for an unreadable entity: %v", m)
	}
}

// when is judged on the REDACTED entity, and the script receives the same
// redacted entity. Otherwise the button would be a one-bit oracle on a hidden
// value, and the script scope a way to read it.
func TestDetailAction_WhenSeesOnlyVisibleFields(t *testing.T) {
	a := regenerateSoA()
	a.When = "entity.owner == 'SECRET-OWNER'"
	d := newDetailActionApp(t, map[string]dataentryconfig.Action{"regenerate-soa": a})
	d.app.fieldResolver = fakeResolver{fv: FieldVerdicts{Visible: map[string]bool{"owner": false}}}

	if d.offered(t, "alice", "document", "documents", "DOC-1@concept") {
		t.Error("offered on a when that reads a hidden field")
	}
	rec := postAction(aliceCtx(), t, d.app, d.decl, "regenerate-soa", `{"entity_id":"DOC-1@concept"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST status %d, want 403: %s", rec.Code, rec.Body)
	}

	// A negated test on the hidden field would pass on the unset binding, so
	// a when that reads a hidden field is refused outright.
	a.When = "entity.owner ~= 'SECRET-OWNER'"
	d.app.schema.Publish(&Schema{Cfg: &Config{Actions: map[string]dataentryconfig.Action{"regenerate-soa": a}},
		Meta: d.app.State().Meta})
	if d.offered(t, "alice", "document", "documents", "DOC-1@concept") {
		t.Error("offered on a negated when over a hidden field")
	}
	rec = postAction(aliceCtx(), t, d.app, d.decl, "regenerate-soa", `{"entity_id":"DOC-1@concept"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("negated when POST status %d, want 403: %s", rec.Code, rec.Body)
	}

	// Without the hidden-field when, the script runs but sees owner unset.
	a.When = "entity.kind == 'soa'"
	d.app.schema.Publish(&Schema{Cfg: &Config{Actions: map[string]dataentryconfig.Action{"regenerate-soa": a}},
		Meta: d.app.State().Meta})
	rec = postAction(aliceCtx(), t, d.app, d.decl, "regenerate-soa", `{"entity_id":"DOC-1@concept"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status %d: %s", rec.Code, rec.Body)
	}
	m := d.markers(t)
	if len(m) != 1 || strings.Contains(m[0], "SECRET-OWNER") {
		t.Fatalf("script saw a hidden field or did not run once: %v", m)
	}
	// The script must also be told the field was withheld, or a default-if-unset
	// script overwrites a value it could not see.
	if !strings.HasSuffix(m[0], ":redacted=true") {
		t.Errorf("script was not told owner is redacted: %q", m[0])
	}
}

// A when with no compiler wired fails closed rather than offering the action
// to everyone.
func TestDetailAction_WhenWithoutCompilerFailsClosed(t *testing.T) {
	d := newDetailActionApp(t, map[string]dataentryconfig.Action{"regenerate-soa": regenerateSoA()})
	d.app.viewConditions = nil

	if d.offered(t, "alice", "document", "documents", "DOC-1@concept") {
		t.Error("offered with no condition compiler")
	}
	rec := postAction(aliceCtx(), t, d.app, d.decl, "regenerate-soa", `{"entity_id":"DOC-1@concept"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST status %d, want 403: %s", rec.Code, rec.Body)
	}
}

// The script runs as the invoking principal: its writes are audited under
// that principal and land on the addressed face.
func TestDetailAction_WritesAuditedUnderInvoker(t *testing.T) {
	d := newDetailActionApp(t, map[string]dataentryconfig.Action{"regenerate-soa": regenerateSoA()})

	rec := postAction(aliceCtx(), t, d.app, d.decl, "regenerate-soa", `{"entity_id":"DOC-1@concept"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status %d: %s", rec.Code, rec.Body)
	}
	var resp v1.ActionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Message != "Regenerated" {
		t.Fatalf("response %s (err %v)", rec.Body, err)
	}

	concept, err := d.store.GetEntityState(context.Background(), "DOC-1", "concept")
	if err != nil || concept.Content != "regenerated" {
		t.Fatalf("concept content = %q (err %v), want regenerated", concept.Content, err)
	}
	approved, err := d.store.GetEntityState(context.Background(), "DOC-1", "approved")
	if err != nil || approved.Content != "old" {
		t.Errorf("approved face changed: %q (err %v)", approved.Content, err)
	}

	recs := d.audit.Records()
	if len(recs) == 0 {
		t.Fatal("no audit records for the script's writes")
	}
	for _, r := range recs {
		if r.Principal.User != "alice" {
			t.Errorf("audit record %s attributed to %q, want alice", r.Op, r.Principal.User)
		}
	}
}

// permission: holds on every surface, so an ordinary action (no
// available_on) with a permission is refused for a caller without it.
func TestAction_PermissionEnforcedWithoutAvailableOn(t *testing.T) {
	d := newDetailActionApp(t, map[string]dataentryconfig.Action{
		"plain": {Script: "regenerate.lua", Permission: "documents:regenerate"},
	})
	rec := postAction(principalCtx("bob"), t, d.app, d.decl, "plain", `{}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "documents:regenerate") {
		t.Errorf("status %d body %s, want a 403 naming the permission", rec.Code, rec.Body)
	}
}

// Under NopACL the request read gate holds every permission, so permission:
// does not gate (the intent-gate semantics the godoc documents, RR-1BMQQ7).
func TestHoldsActionPermission_NopGateHoldsEverything(t *testing.T) {
	a := dataentryconfig.Action{Permission: "documents:regenerate"}
	if !holdsActionPermission(context.Background(), a) {
		t.Error("permission refused with no ACL configured")
	}
}

// A past version never carries action keys: the action runs against the live
// entity, so a key on a snapshot would offer what the gate refuses.
func TestDetailAction_NotOfferedOnHistoricalSubject(t *testing.T) {
	a := regenerateSoA()
	a.When, a.Permission = "", ""
	d := newDetailActionApp(t, map[string]dataentryconfig.Action{"regenerate-soa": a})
	e, err := d.store.GetEntityState(context.Background(), "DOC-1", "concept")
	if err != nil {
		t.Fatal(err)
	}
	ctx := gateCtxFor(aliceCtx(), t, d.decl)
	if got := d.app.affordances.computeDetailActions(ctx, e); !got[detailActionKeyPrefix+"regenerate-soa"] {
		t.Fatalf("live entity: got %v, want the key (positive control)", got)
	}
	if got := d.app.affordances.computeDetailActions(affordances.WithHistoricalSubject(ctx), e); got != nil {
		t.Errorf("historical subject: got %v, want nil", got)
	}
}

// A config reload between the request's snapshot and the lock refuses the
// entity-bound action instead of gating it on the stale config. The pipe body
// holds the handler after it captured the snapshot, so the reload lands in
// that window deterministically.
func TestDetailAction_ConfigReloadedIsRefused(t *testing.T) {
	d := newDetailActionApp(t, map[string]dataentryconfig.Action{"regenerate-soa": regenerateSoA()})
	pr, pw := io.Pipe()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/_action/regenerate-soa", pr).
		WithContext(gateCtxFor(aliceCtx(), t, d.decl))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		callAction(d.app, req, rec)
	}()

	// Blocks until the handler reads the body, i.e. after it took the snapshot.
	if _, err := pw.Write([]byte(`{"entity_id":`)); err != nil {
		t.Fatal(err)
	}
	d.app.schema.Publish(&Schema{Cfg: d.app.State().Cfg, Meta: d.app.State().Meta})
	if _, err := pw.Write([]byte(`"DOC-1@concept"}`)); err != nil {
		t.Fatal(err)
	}
	_ = pw.Close()
	<-done

	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "/errors/config_reloaded\"") {
		t.Fatalf("status %d, want 409 config_reloaded: %s", rec.Code, rec.Body)
	}
	if m := d.markers(t); len(m) != 0 {
		t.Errorf("script ran on a stale config: %v", m)
	}
}

// A dry-run create has no stored entity to run an action against, so its
// candidate carries no action keys; the real create does.
func TestDetailAction_NotOfferedOnDryRunCandidate(t *testing.T) {
	a := dataentryconfig.Action{
		Label: "Touch", Script: "regenerate.lua",
		AvailableOn: &dataentryconfig.ActionScope{EntityTypes: []string{"note"}},
	}
	d := newDetailActionApp(t, map[string]dataentryconfig.Action{"touch": a})
	create := func(query string) map[string]bool {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/notes"+query,
			strings.NewReader(`{"properties":{"title":"x"}}`)).WithContext(gateCtxFor(aliceCtx(), t, d.decl))
		rec := httptest.NewRecorder()
		d.app.handleV1EntityCollection(rec, req, "note", "notes")
		if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
			t.Fatalf("POST%s: %d %s", query, rec.Code, rec.Body)
		}
		var got v1.Entity
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return got.Actions
	}
	if got := create(""); !got[detailActionKeyPrefix+"touch"] {
		t.Fatalf("created entity: got %v, want the key (positive control)", got)
	}
	if got := create("?dry_run=true"); got[detailActionKeyPrefix+"touch"] {
		t.Errorf("dry-run candidate carries the action key: %v", got)
	}
}
