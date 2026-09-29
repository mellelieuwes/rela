package dataentry

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
)

const conflictedTicket = `<<<<<<< HEAD
---
id: TKT-001
type: ticket
title: Ours
---
=======
---
id: TKT-001
type: ticket
title: Theirs
---
>>>>>>> incoming
Body text.
`

const conflictedRelation = `<<<<<<< HEAD
---
from: TKT-001
relation: blocks
to: TKT-002
note: ours
---
=======
---
from: TKT-001
relation: blocks
to: TKT-002
note: theirs
---
>>>>>>> incoming
`

// newConflictTestApp builds an app bound to a real on-disk project root
// — the conflict endpoints read and write project files directly.
func newConflictTestApp(t *testing.T) (app *App, root string) {
	t.Helper()
	app = newTestAppV1(t)
	root = t.TempDir()
	bindRepo(app, root)
	return app, root
}

func writeProjectFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func postConflictResolve(app *App, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/_conflicts/resolve", strings.NewReader(body))
	rec := httptest.NewRecorder()
	app.write.handleV1ConflictResolve(rec, req)
	return rec
}

func TestV1ConflictDetailPathTraversal(t *testing.T) {
	app, root := newConflictTestApp(t)
	// A file outside the project root that a traversal could read.
	secret := filepath.Join(filepath.Dir(root), "secret.md")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tests := []struct {
		name string
		path string
	}{
		{"relative traversal to existing file", "../secret.md"},
		{"relative traversal to missing file", "../../nope.md"},
		{"absolute path", "/etc/passwd"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/_conflicts/"+tc.path, http.NoBody)
			rec := httptest.NewRecorder()
			app.handleV1ConflictRoutes(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Errorf("expected 403, got %d: %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "top secret") {
				t.Error("response leaked file content from outside the project")
			}
		})
	}
}

func TestV1ConflictDetailContainedPath(t *testing.T) {
	app, root := newConflictTestApp(t)
	writeProjectFile(t, root, "entities/ticket/TKT-001.md", conflictedTicket)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/_conflicts/entities/ticket/TKT-001.md", http.NoBody)
	rec := httptest.NewRecorder()
	app.handleV1ConflictRoutes(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "TKT-001") {
		t.Errorf("expected detail for TKT-001, got: %s", rec.Body.String())
	}
}

func TestV1ConflictDetailMissingFile(t *testing.T) {
	app, _ := newConflictTestApp(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/_conflicts/entities/ticket/MISSING.md", http.NoBody)
	rec := httptest.NewRecorder()
	app.handleV1ConflictRoutes(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestV1ConflictResolvePathTraversal(t *testing.T) {
	app, root := newConflictTestApp(t)
	// An existing conflicted file outside the project root: without
	// containment the resolve endpoint would happily rewrite it.
	outside := filepath.Join(filepath.Dir(root), "outside.md")
	if err := os.WriteFile(outside, []byte(conflictedTicket), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	rec := postConflictResolve(app, `{"path":"../outside.md","content_choice":"theirs"}`)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != conflictedTicket {
		t.Error("file outside the project root was modified")
	}
}

func TestV1ConflictResolveACLDenied(t *testing.T) {
	app, root := newConflictTestApp(t)
	path := writeProjectFile(t, root, "entities/ticket/TKT-001.md", conflictedTicket)
	app.acl = acl.ReadOnlyACL{}
	sink := audit.NewMemory()
	app.auditSink = sink

	rec := postConflictResolve(app, `{"path":"entities/ticket/TKT-001.md","content_choice":"ours"}`)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "read-only-acl") {
		t.Errorf("expected structured ACL deny body, got: %s", rec.Body.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != conflictedTicket {
		t.Error("denied resolve still modified the file")
	}

	var denied bool
	for _, r := range sink.Records() {
		if r.Op == audit.OpDeniedWrite && r.Subject != nil && r.Subject.ID == "TKT-001" {
			denied = true
		}
	}
	if !denied {
		t.Errorf("expected a denied-write audit record for TKT-001, got %+v", sink.Records())
	}
}

func TestV1ConflictResolveEntityWritesAndAudits(t *testing.T) {
	app, root := newConflictTestApp(t)
	path := writeProjectFile(t, root, "entities/ticket/TKT-001.md", conflictedTicket)
	sink := audit.NewMemory()
	app.auditSink = sink

	rec := postConflictResolve(app,
		`{"path":"entities/ticket/TKT-001.md","property_choices":{"title":"theirs"},"content_choice":"theirs"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(got), "<<<<<<<") {
		t.Errorf("resolved file still contains conflict markers:\n%s", got)
	}
	if !strings.Contains(string(got), "Theirs") {
		t.Errorf("expected theirs-side title in resolved file, got:\n%s", got)
	}

	var audited bool
	for _, r := range sink.Records() {
		if r.Op == audit.OpUpdateEntity && r.Subject != nil &&
			r.Subject.Kind == "entity" && r.Subject.ID == "TKT-001" {

			audited = true
		}
	}
	if !audited {
		t.Errorf("expected an update-entity audit record for TKT-001, got %+v", sink.Records())
	}
}

func TestV1ConflictResolveRelationWritesAndAudits(t *testing.T) {
	app, root := newConflictTestApp(t)
	path := writeProjectFile(t, root, "relations/TKT-001--blocks--TKT-002.md", conflictedRelation)
	sink := audit.NewMemory()
	app.auditSink = sink

	rec := postConflictResolve(app,
		`{"path":"relations/TKT-001--blocks--TKT-002.md","content_choice":"ours"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(got), "<<<<<<<") {
		t.Errorf("resolved file still contains conflict markers:\n%s", got)
	}

	var audited bool
	for _, r := range sink.Records() {
		if r.Op == audit.OpUpdateRelation && r.Subject != nil &&
			r.Subject.Kind == "relation" && r.Subject.RelationType == "blocks" {

			audited = true
		}
	}
	if !audited {
		t.Errorf("expected an update-relation audit record, got %+v", sink.Records())
	}
}

// conflictedTwinTicket is twinTicket in conflict: "ours" carries the stored
// values, "theirs" another title and body.
const conflictedTwinTicket = `<<<<<<< HEAD
---
id: TKT-001
type: ticket
title: Secret launch codename
status: open
---
The body.
=======
---
id: TKT-001
type: ticket
title: Theirs
status: open
---
Their body.
>>>>>>> incoming
`

// TestV1ConflictResolve_TwinOwnership: a resolution bypasses entitymanager,
// so the handler itself must refuse one that changes a field an external
// system owns through a twin — 422 externally_owned, the file untouched. A
// resolution that keeps the owned value passes.
func TestV1ConflictResolve_TwinOwnership(t *testing.T) {
	const path = "entities/ticket/TKT-001.md"
	cases := []struct {
		name   string
		theirs []string
		body   string
		want   int
	}{
		{"owned title changed", []string{"title"},
			`{"path":"` + path + `","property_choices":{"title":"theirs"},"content_choice":"theirs"}`,
			http.StatusUnprocessableEntity},
		{"owned body rewritten", []string{"body"},
			`{"path":"` + path + `","property_choices":{"title":"theirs"},"content_choice":"manual","manual_content":"Mine."}`,
			http.StatusUnprocessableEntity},
		{"owned title kept", []string{"title"},
			`{"path":"` + path + `","property_choices":{"title":"ours"},"content_choice":"theirs"}`,
			http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, svc := twinsApp(t, tc.theirs...)
			root := t.TempDir()
			bindRepo(app, root)
			SetTwins(app, svc) // bindRepo rebuilt the twins handler
			file := writeProjectFile(t, root, path, conflictedTwinTicket)

			rec := postConflictResolve(app, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("got %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
			got, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if tc.want == http.StatusOK {
				return
			}
			if !strings.Contains(rec.Body.String(), "/errors/externally_owned") {
				t.Errorf("body = %s, want the externally_owned problem", rec.Body)
			}
			if string(got) != conflictedTwinTicket {
				t.Errorf("a refused resolution rewrote the file:\n%s", got)
			}
		})
	}
}
