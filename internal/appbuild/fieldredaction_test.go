//go:build !postgres && !memorybackend && !sqlite

// Excludes sqlite for the same reason it excludes postgres: these tests seed a
// project by WRITING MARKDOWN FILES to disk, which only fsstore reads back. On
// a database backend the entity simply is not there, so the failure would look
// like an ACL-redaction bug rather than a fixture that does not apply.
//
// The redaction behavior under test is backend-agnostic — it lives in
// internal/visibility decorators applied at the wiring site — so covering it on
// one backend is sufficient. What is fsstore-specific is only how the fixture
// gets into the store.

package appbuild_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/search"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

// redactionMetamodel declares a type with a property a policy can hide.
const redactionMetamodel = `version: "1.0"
entities:
  person:
    label: Person
    plural: people
    id_prefix: "PERS-"
    id_type: sequential
    properties:
      name:
        type: string
      salary:
        type: string
relations: {}
`

// redactionPolicy hides person.salary from the `viewer` role while leaving the
// row itself readable. That split is the point: row gating alone would let
// every property through, so a test asserting only "the entity is visible"
// would pass with no redaction at all.
const redactionPolicy = `roles:
  viewer:
    read: ["*"]
    visible:
      person:
        - field: name
assignments:
  bob: viewer
`

// writeRedactionProject lays down a project whose policy redacts a field, plus
// one entity carrying a value that must not escape.
func writeRedactionProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeMetamodelBody(t, root, redactionMetamodel)
	writePolicy(t, root, redactionPolicy)

	dir := filepath.Join(root, "entities", "people")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nid: PERS-1\ntype: person\nname: Alice\nsalary: \"99000\"\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "PERS-1.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// bobCtx returns a ctx carrying the principal the policy assigns to `viewer`,
// stamped with the tool the surface under test actually runs as. Both fields
// are required: acl rejects a half-stamped principal and the row gate then
// fails closed, which would make a redaction test pass for the wrong reason
// (nothing returned at all).
func bobCtx(tool string) context.Context {
	return principal.With(context.Background(), principal.Principal{User: "bob", Tool: tool})
}

// TestGatedReads_RedactsHiddenField closes the field-level half of the
// GatedReads limitation (TKT-0XL8MF).
//
// GatedReads previously passed a nil redactor, so this path was ROW gating
// only: an MCP caller who could read `person` at all received every property,
// including ones the same role sees redacted in the UI. Against that code this
// test fails on the salary assertion.
func TestGatedReads_RedactsHiddenField(t *testing.T) {
	root := writeRedactionProject(t)
	svc, err := appbuildOnDisk(t, root)
	if err != nil {
		t.Fatalf("appbuild.New: %v", err)
	}
	defer svc.Close()

	got, err := svc.GatedReads().Reader.GetEntity(bobCtx(principal.ToolMCP), "PERS-1")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got == nil {
		t.Fatal("entity is nil — the row gate hid a row the policy permits reading")
	}
	// The visible property must survive: over-redaction is as wrong as none.
	if got.GetString("name") != "Alice" {
		t.Errorf("name = %q, want \"Alice\" — a permitted field was redacted", got.GetString("name"))
	}
	if v := got.GetString("salary"); v != "" {
		t.Errorf("salary = %q, want redacted to empty — a `visible:`-hidden value "+
			"reached an MCP read; row gating alone does not redact fields", v)
	}
}

// TestScheduledLuaWriteDeps_RedactsHiddenField closes RR-7408F5, the same gap
// on the scheduler path. A scheduled job's reads flow into whatever the job
// sends onward (a prompt, a webhook), so an unredacted property here leaves
// the system entirely.
func TestScheduledLuaWriteDeps_RedactsHiddenField(t *testing.T) {
	root := writeRedactionProject(t)
	svc, err := appbuildOnDisk(t, root)
	if err != nil {
		t.Fatalf("appbuild.New: %v", err)
	}
	defer svc.Close()

	deps := svc.ScheduledLuaWriteDeps()
	if deps.VisibleReader == nil {
		t.Fatal("ScheduledLuaWriteDeps has no VisibleReader")
	}
	got, err := deps.VisibleReader.GetEntity(bobCtx(principal.ToolScheduler), "PERS-1")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got == nil {
		t.Fatal("entity is nil — the row gate hid a row the policy permits reading")
	}
	if got.GetString("name") != "Alice" {
		t.Errorf("name = %q, want \"Alice\" — a permitted field was redacted", got.GetString("name"))
	}
	if v := got.GetString("salary"); v != "" {
		t.Errorf("salary = %q, want redacted to empty — a scheduled job read a "+
			"`visible:`-hidden value (RR-7408F5)", v)
	}
}

// TestNoPolicy_RedactorHidesNothing pins the byte-parity path. A project with
// no acl.yaml must behave exactly as it did pre-ACL; if buildFieldRedactor
// returned a policy-backed redactor (or nil) here instead of NopRedactor, this
// would regress silently for every existing project.
func TestNoPolicy_RedactorHidesNothing(t *testing.T) {
	root := t.TempDir()
	writeMetamodelBody(t, root, redactionMetamodel)
	// deliberately no acl.yaml

	dir := filepath.Join(root, "entities", "people")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nid: PERS-1\ntype: person\nname: Alice\nsalary: \"99000\"\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "PERS-1.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	svc, err := appbuildOnDisk(t, root)
	if err != nil {
		t.Fatalf("appbuild.New: %v", err)
	}
	defer svc.Close()

	got, err := svc.GatedReads().Reader.GetEntity(bobCtx(principal.ToolMCP), "PERS-1")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got == nil {
		t.Fatal("entity is nil — no policy is configured, nothing may be hidden")
	}
	if got.GetString("salary") != "99000" {
		t.Errorf("salary = %q, want \"99000\" — with no acl.yaml the read must be "+
			"byte-identical to pre-ACL behavior", got.GetString("salary"))
	}
}

// TestPolicyWithoutAffordanceGrants_HidesNothing covers the third
// buildFieldRedactor branch: an acl.yaml that gates ROWS but declares no
// `visible:` grants. HasAffordanceGrants is false there, so the redactor must
// be NopRedactor — not a policy-backed one that hides nothing by accident, and
// not an error. Row-only policies are a normal, common configuration.
func TestPolicyWithoutAffordanceGrants_HidesNothing(t *testing.T) {
	root := t.TempDir()
	writeMetamodelBody(t, root, redactionMetamodel)
	writePolicy(t, root, `roles:
  viewer:
    read: ["*"]
assignments:
  bob: viewer
`)

	dir := filepath.Join(root, "entities", "people")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nid: PERS-1\ntype: person\nname: Alice\nsalary: \"99000\"\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "PERS-1.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	svc, err := appbuildOnDisk(t, root)
	if err != nil {
		t.Fatalf("appbuild.New: %v", err)
	}
	defer svc.Close()

	got, err := svc.GatedReads().Reader.GetEntity(bobCtx(principal.ToolMCP), "PERS-1")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got == nil {
		t.Fatal("entity is nil — the policy grants read on this row")
	}
	if got.GetString("salary") != "99000" {
		t.Errorf("salary = %q, want \"99000\" — a policy with no `visible:` grants "+
			"must not redact anything", got.GetString("salary"))
	}
}

// TestGatedReads_RedactsOnListPath pins the LIST surface, which is separate
// code from GetEntity (internal/visibility/luareader.go has its own batching
// path with a per-surviving-row redaction step). It is also the higher-volume
// leak: a script calling list_entities is how bulk hidden data would escape,
// not a single GetEntity.
func TestGatedReads_RedactsOnListPath(t *testing.T) {
	root := writeRedactionProject(t)
	svc, err := appbuildOnDisk(t, root)
	if err != nil {
		t.Fatalf("appbuild.New: %v", err)
	}
	defer svc.Close()

	seen := 0
	for e, err := range svc.GatedReads().Reader.ListEntities(
		bobCtx(principal.ToolMCP), store.EntityQuery{Type: "person"},
	) {
		if err != nil {
			t.Fatalf("ListEntities: %v", err)
		}
		seen++
		if e.GetString("name") != "Alice" {
			t.Errorf("name = %q, want \"Alice\" — a permitted field was redacted on the list path",
				e.GetString("name"))
		}
		if v := e.GetString("salary"); v != "" {
			t.Errorf("salary = %q, want redacted to empty — a `visible:`-hidden value "+
				"escaped via the LIST path even though GetEntity redacts it", v)
		}
	}
	if seen != 1 {
		t.Errorf("listed %d entities, want 1 — the row gate dropped a readable row", seen)
	}
}

// cascadeLeakMetamodel fires an automation on ticket update whose Lua action
// reads the person entity and copies the salary onto the ticket — laundering a
// hidden value onto a field with no `visible:` restriction. If the cascade's
// read is redacted the copy lands as the NO_SALARY marker; if it is not, the
// hidden value itself is written where anyone may read it.
const cascadeLeakMetamodel = `version: "1.0"
entities:
  person:
    label: Person
    plural: people
    id_prefix: "PERS-"
    id_type: sequential
    properties:
      name:
        type: string
      salary:
        type: string
  ticket:
    label: Ticket
    plural: tickets
    id_prefix: "TKT-"
    id_type: sequential
    properties:
      title:
        type: string
      leaked:
        type: string
relations: {}
automations:
  - name: leak-salary
    on:
      entity: [ticket]
      property: title
    do:
      - lua: |
          local p = rela.get_entity("PERS-1")
          local v = "NO_ENTITY"
          if p ~= nil then
            v = "NO_SALARY"
            if p.properties ~= nil and p.properties.salary ~= nil and p.properties.salary ~= "" then
              v = p.properties.salary
            end
          end
          rela.update_entity("TKT-1", {leaked = v})
`

// TestCascadeReadDeps_RedactsHiddenField covers the THIRD read path that passed
// a nil redactor: the static lua.ReadDeps backing automation cascades.
//
// It carried no KNOWN LIMITATION note, which is why the first pass at this work
// missed it — but it is identity-bearing by exactly the definition that
// justified fixing the other two. A cascade fires on the acting user's ctx and
// reads their view (DEC-O59WM4, RR-XC0URX), and a Lua action can send what it
// reads onward just as a scheduled job can.
//
// End-to-end on purpose: it drives a real PatchEntity, a real automation
// trigger, and a real Lua action, then re-reads the STORE (the cascade writes
// after the returned snapshot is taken). Against a nil redactor `leaked` comes
// back "99000".
func TestCascadeReadDeps_RedactsHiddenField(t *testing.T) {
	root := t.TempDir()
	writeMetamodelBody(t, root, cascadeLeakMetamodel)
	writePolicy(t, root, `roles:
  viewer:
    read: ["*"]
    update: ["*"]
    visible:
      person:
        - field: name
assignments:
  bob: viewer
`)

	people := filepath.Join(root, "entities", "people")
	if err := os.MkdirAll(people, 0o755); err != nil {
		t.Fatal(err)
	}
	person := "---\nid: PERS-1\ntype: person\nname: Alice\nsalary: \"99000\"\n---\n"
	if err := os.WriteFile(filepath.Join(people, "PERS-1.md"), []byte(person), 0o600); err != nil {
		t.Fatal(err)
	}
	tickets := filepath.Join(root, "entities", "tickets")
	if err := os.MkdirAll(tickets, 0o755); err != nil {
		t.Fatal(err)
	}
	ticket := "---\nid: TKT-1\ntype: ticket\ntitle: Old\n---\n"
	if err := os.WriteFile(filepath.Join(tickets, "TKT-1.md"), []byte(ticket), 0o600); err != nil {
		t.Fatal(err)
	}

	svc, err := appbuildOnDisk(t, root)
	if err != nil {
		t.Fatalf("appbuild.New: %v", err)
	}
	defer svc.Close()

	ctx := bobCtx(principal.ToolDataEntry)
	res, err := svc.EntityManager().PatchEntity(ctx, "TKT-1", entity.Patch{
		Properties: map[string]any{"title": "New"},
	})
	if err != nil {
		t.Fatalf("PatchEntity: %v", err)
	}
	if len(res.AutomationErrors) != 0 {
		t.Fatalf("automation errors: %v", res.AutomationErrors)
	}

	// Re-read the store: the cascade's write lands after the returned snapshot.
	after, err := svc.Store().GetEntity(ctx, "TKT-1")
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	got := after.GetString("leaked")
	if got == "NO_ENTITY" {
		t.Fatalf("leaked = %q — the cascade could not read the entity at all; "+
			"this test must exercise a real read, not a row-gate denial", got)
	}
	if got != "NO_SALARY" {
		t.Errorf("leaked = %q, want \"NO_SALARY\" — an automation cascade read a "+
			"`visible:`-hidden value and copied it onto a readable field; cascade "+
			"reads are ACL-bound to the acting user (DEC-O59WM4)", got)
	}
}

// searchIDs runs q through s and returns the hit ids, failing on an error.
func searchIDs(ctx context.Context, t *testing.T, s search.Searcher, text string) []string {
	t.Helper()
	var ids []string
	for h, err := range s.Search(ctx, search.Query{Text: text}) {
		if err != nil {
			t.Fatalf("Search(%q): %v", text, err)
		}
		ids = append(ids, h.ID)
	}
	return ids
}

// TestGatedReads_SearchDropsHiddenFieldMatch (TKT-4QSZ8Y): the remote MCP
// search goes through GatedReads, so a match on a `visible:`-hidden value must
// not confirm that value. A match on a visible property still finds the row,
// which proves the drop is field-level rather than the whole type going dark.
func TestGatedReads_SearchDropsHiddenFieldMatch(t *testing.T) {
	root := writeRedactionProject(t)
	svc, err := appbuildOnDisk(t, root)
	if err != nil {
		t.Fatalf("appbuild.New: %v", err)
	}
	defer svc.Close()

	s := svc.GatedReads().Searcher
	if _, ok := s.(*visibility.Searcher); !ok {
		t.Fatalf("GatedReads().Searcher = %T, want *visibility.Searcher", s)
	}
	ctx := bobCtx(principal.ToolMCP)
	if got := searchIDs(ctx, t, s, "Alice"); len(got) != 1 || got[0] != "PERS-1" {
		t.Errorf("search on a visible value = %v, want [PERS-1]", got)
	}
	if got := searchIDs(ctx, t, s, "99000"); len(got) != 0 {
		t.Errorf("search on the hidden salary = %v, want no hits — the hit confirms the value", got)
	}
}

// TestNoPolicy_GatedSearcherIsRaw pins NopACL parity for search: with no
// acl.yaml, GatedReads hands out the raw searcher unchanged.
func TestNoPolicy_GatedSearcherIsRaw(t *testing.T) {
	root := t.TempDir()
	writeMetamodelBody(t, root, redactionMetamodel)
	svc, err := appbuildOnDisk(t, root)
	if err != nil {
		t.Fatalf("appbuild.New: %v", err)
	}
	defer svc.Close()

	if got, want := svc.GatedReads().Searcher, svc.Searcher(); got != want {
		t.Errorf("GatedReads().Searcher = %T, want the raw searcher %T", got, want)
	}
}
