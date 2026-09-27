package dataentry

import (
	"archive/zip"
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	v1 "github.com/Sourcehaven-BV/rela/internal/apiwire/v1"
	"github.com/Sourcehaven-BV/rela/internal/attachment"
	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// multipartBody builds a multipart/form-data body with a single "file"
// field and returns the body plus its Content-Type header.
func multipartBody(t *testing.T, fileName string, data []byte) (body *bytes.Buffer, contentType string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

// putAttachmentAs invokes the upload handler directly with the gate ctx.
// Type/plural are the fixture's "ticket"/"tickets" (newTestAppV1).
//
//nolint:unparam // entityID is conceptually variable; tests use one fixture.
func putAttachmentAs(ctx context.Context, t *testing.T, app *App, d *acl.Declarative,
	entityID, property, fileName string, data []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := multipartBody(t, fileName, data)
	url := "/api/v1/tickets/" + entityID + "/_attachments/" + property
	req := httptest.NewRequest(http.MethodPut, url, body)
	req.Header.Set("Content-Type", contentType)
	req = req.WithContext(gateCtxFor(ctx, t, d))
	rec := httptest.NewRecorder()
	app.attachments.handleV1AttachmentRoute(rec, req, "ticket", "tickets", entityID, property)
	return rec
}

//nolint:unparam // entityID is conceptually variable; tests use one fixture.
func deleteAttachmentAs(ctx context.Context, t *testing.T, app *App, d *acl.Declarative,
	entityID, property, fileName string,
) *httptest.ResponseRecorder {
	t.Helper()
	url := "/api/v1/tickets/" + entityID + "/_attachments/" + property + "/" + fileName
	req := httptest.NewRequest(http.MethodDelete, url, http.NoBody)
	req = req.WithContext(gateCtxFor(ctx, t, d))
	rec := httptest.NewRecorder()
	app.attachments.handleV1AttachmentFileRoute(rec, req, "ticket", entityID, property, fileName)
	return rec
}

// writeACL grants alice read+update on ticket and denies bob everything.
// An attachment write authorizes as OpUpdate (it mutates the owning
// entity's property), so the role needs Read + Update.
func writeACL(t *testing.T, app *App) *acl.Declarative {
	t.Helper()
	return mustNewACL(t, &acl.Policy{
		Roles: map[string]acl.RoleDef{
			"editor": {Read: []string{"ticket"}, Update: []string{"ticket"}},
			"none":   {},
		},
		Assignments: map[string]string{"alice": "editor", "bob": "none"},
	}, app.store)
}

func bobCtx() context.Context {
	return principal.With(context.Background(), principal.Principal{User: "bob", Tool: principal.ToolDataEntry})
}

// TestAttachmentUpload_MIMERejected pins that the native MIME allowlist rejects
// a disallowed type at upload with 422 (not 500) and does not persist anything.
// The `screenshot` property accepts text/plain, so SVG content (which sniffs as
// XML/HTML and carries script) is rejected — the stored-XSS defense at ingress.
func TestAttachmentUpload_MIMERejected(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{ID: "TKT-001", Type: "ticket", Properties: map[string]any{"title": "T1"}})
	d := writeACL(t, app)
	app.acl = d

	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	rec := putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "screenshot", "evil.svg", svg)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("disallowed upload: got %d, want 422; body=%s", rec.Code, rec.Body)
	}
	// Nothing persisted.
	if e := mustGet(t, app, "TKT-001"); e.GetString("screenshot") != "" {
		t.Errorf("rejected upload must not stamp the property; got %q", e.GetString("screenshot"))
	}
}

// TestAttachmentUpload_RoundTrips pins AC1: an authorized upload persists
// the bytes (readable back via GET) and stamps the property; the response
// carries the new _attachments entry.
func TestAttachmentUpload_RoundTrips(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{ID: "TKT-001", Type: "ticket", Properties: map[string]any{"title": "T1"}})
	d := writeACL(t, app)
	app.acl = d

	rec := putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "screenshot", "shot.txt", []byte("PNGDATA"))
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: got %d, want 200; body=%s", rec.Code, rec.Body)
	}

	// Bytes readable via GET.
	get := getAttachmentAs(aliceCtx(), t, app, d, "ticket", "tickets", "TKT-001", "screenshot", "shot.txt")
	if get.Code != http.StatusOK || get.Body.String() != "PNGDATA" {
		t.Fatalf("GET after upload: got %d body=%q, want 200 \"PNGDATA\"", get.Code, get.Body)
	}

	// Property stamped on the entity.
	e := mustGet(t, app, "TKT-001")
	if got := e.GetString("screenshot"); got != "attachments/TKT-001/screenshot/shot.txt" {
		t.Errorf("property = %q, want the stamped attachment path", got)
	}
}

// TestAttachmentUpload_Replaces pins that a second upload overwrites the
// first.
func TestAttachmentUpload_Replaces(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{ID: "TKT-001", Type: "ticket", Properties: map[string]any{"title": "T1"}})
	d := writeACL(t, app)
	app.acl = d

	putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "screenshot", "a.txt", []byte("first"))
	putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "screenshot", "b.txt", []byte("second"))

	get := getAttachmentAs(aliceCtx(), t, app, d, "ticket", "tickets", "TKT-001", "screenshot", "b.txt")
	if get.Body.String() != "second" {
		t.Errorf("after replace, body = %q, want \"second\"", get.Body)
	}
}

// TestAttachmentDelete_RemovesBytesAndProperty pins AC1 delete: detach
// clears the property and the bytes are gone.
func TestAttachmentDelete_RemovesBytesAndProperty(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{ID: "TKT-001", Type: "ticket", Properties: map[string]any{"title": "T1"}})
	d := writeACL(t, app)
	app.acl = d
	putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "screenshot", "a.txt", []byte("data"))

	del := deleteAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "screenshot", "a.txt")
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d, want 204; body=%s", del.Code, del.Body)
	}

	get := getAttachmentAs(aliceCtx(), t, app, d, "ticket", "tickets", "TKT-001", "screenshot", "a.txt")
	if get.Code != http.StatusNotFound {
		t.Errorf("GET after delete: got %d, want 404", get.Code)
	}
	if e := mustGet(t, app, "TKT-001"); e.GetString("screenshot") != "" {
		t.Errorf("property not cleared after delete: %q", e.GetString("screenshot"))
	}
}

// TestAttachmentWrite_DeniedBeforeBytes pins AC3: a principal without
// write access is denied (403), and crucially NO bytes are written — the
// authorize runs before AttachFile.
func TestAttachmentWrite_DeniedBeforeBytes(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{ID: "TKT-001", Type: "ticket", Properties: map[string]any{"title": "T1"}})
	d := writeACL(t, app)
	app.acl = d

	// bob can't read ticket → upload 404s at the read gate (no existence
	// leak, no bytes).
	rec := putAttachmentAs(bobCtx(), t, app, d, "TKT-001", "screenshot", "x.txt", []byte("data"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bob (no read) upload: got %d, want 404", rec.Code)
	}

	// alice can read but suppose a write-only deny: use a role that reads
	// ticket but writes nothing.
	d2 := mustNewACL(t, &acl.Policy{
		Roles:       map[string]acl.RoleDef{"reader": {Read: []string{"ticket"}}},
		Assignments: map[string]string{"carol": "reader"},
	}, app.store)
	app.acl = d2
	carol := principal.With(context.Background(), principal.Principal{User: "carol", Tool: principal.ToolDataEntry})
	rec = putAttachmentAs(carol, t, app, d2, "TKT-001", "screenshot", "x.txt", []byte("data"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("carol (read, no write) upload: got %d, want 403; body=%s", rec.Code, rec.Body)
	}

	// No bytes were written by either denied attempt.
	app.acl = d
	get := getAttachmentAs(aliceCtx(), t, app, d, "ticket", "tickets", "TKT-001", "screenshot", "x.txt")
	if get.Code != http.StatusNotFound {
		t.Errorf("a denied upload must not write bytes; GET got %d, want 404", get.Code)
	}
}

// TestAttachmentUpload_TooLarge pins AC2: an over-cap upload is rejected
// with 413, on the ingress path, before any bytes persist.
func TestAttachmentUpload_TooLarge(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{ID: "TKT-001", Type: "ticket", Properties: map[string]any{"title": "T1"}})
	// Tighten the cap via config so the test stays small.
	app.State().Cfg.App.MaxAttachmentBytes = 8
	d := writeACL(t, app)
	app.acl = d

	rec := putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "screenshot", "big.bin", []byte("waytoolarge"))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize upload: got %d, want 413; body=%s", rec.Code, rec.Body)
	}

	get := getAttachmentAs(aliceCtx(), t, app, d, "ticket", "tickets", "TKT-001", "screenshot", "big.bin")
	if get.Code != http.StatusNotFound {
		t.Errorf("oversize upload must not persist; GET got %d, want 404", get.Code)
	}
}

// TestAttachmentUpload_MissingFieldOrBadProperty covers the 400 (no file
// field) and 404 (non-file property) negative cases.
func TestAttachmentUpload_MissingFieldOrBadProperty(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{ID: "TKT-001", Type: "ticket", Properties: map[string]any{"title": "T1"}})
	d := writeACL(t, app)
	app.acl = d

	// Wrong form field name → 400.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("notfile", "x.txt")
	_, _ = fw.Write([]byte("data"))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/tickets/TKT-001/_attachments/screenshot", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = req.WithContext(gateCtxFor(aliceCtx(), t, d))
	rec := httptest.NewRecorder()
	app.attachments.handleV1AttachmentRoute(rec, req, "ticket", "tickets", "TKT-001", "screenshot")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing file field: got %d, want 400; body=%s", rec.Code, rec.Body)
	}

	// Non-file property → 404 (no oracle).
	rec = putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "title", "x.txt", []byte("data"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("upload to non-file property: got %d, want 404", rec.Code)
	}
}

// TestMaxAttachmentBytes_ClampsToStoreCap pins RR-81XSCY: a configured
// limit above the store backstop is clamped, so the 413 ceiling can never
// promise more than the store will accept.
func TestMaxAttachmentBytes_ClampsToStoreCap(t *testing.T) {
	app := newTestAppV1(t)

	// Unset → product default.
	if got := maxAttachmentBytes(app.State()); got != DefaultMaxAttachmentBytes {
		t.Errorf("default: got %d, want %d", got, DefaultMaxAttachmentBytes)
	}
	// Lower override honored.
	app.State().Cfg.App.MaxAttachmentBytes = 1024
	if got := maxAttachmentBytes(app.State()); got != 1024 {
		t.Errorf("override: got %d, want 1024", got)
	}
	// Above the store cap → clamped to the store cap.
	app.State().Cfg.App.MaxAttachmentBytes = store.MaxAttachmentBytes * 2
	if got := maxAttachmentBytes(app.State()); got != store.MaxAttachmentBytes {
		t.Errorf("over-cap: got %d, want clamp to %d", got, store.MaxAttachmentBytes)
	}
}

// TestAttachmentDelete_IdempotentWhenNoAttachment pins that deleting a
// property with no attachment still succeeds (204) and clears the
// property (RR-JIRLLU / S5).
func TestAttachmentDelete_IdempotentWhenNoAttachment(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{ID: "TKT-001", Type: "ticket", Properties: map[string]any{"title": "T1"}})
	d := writeACL(t, app)
	app.acl = d

	del := deleteAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "screenshot", "gone.png")
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete with no attachment: got %d, want 204; body=%s", del.Code, del.Body)
	}
	if e := mustGet(t, app, "TKT-001"); e.GetString("screenshot") != "" {
		t.Errorf("property should remain empty: %q", e.GetString("screenshot"))
	}
}

// TestAttachmentUpload_ReplaceOversizeKeepsExisting pins (at the HTTP
// layer) the RR-EISJTE fix: a failed oversize replace must not destroy
// the existing valid attachment.
func TestAttachmentUpload_ReplaceOversizeKeepsExisting(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{ID: "TKT-001", Type: "ticket", Properties: map[string]any{"title": "T1"}})
	d := writeACL(t, app)
	app.acl = d

	// Upload a valid file, then attempt an oversize replace (cap tightened).
	putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "screenshot", "ok.txt", []byte("good"))
	app.State().Cfg.App.MaxAttachmentBytes = 4
	rec := putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "screenshot", "huge.bin", []byte("waytoobig"))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize replace: got %d, want 413", rec.Code)
	}

	// The original must still be downloadable.
	get := getAttachmentAs(aliceCtx(), t, app, d, "ticket", "tickets", "TKT-001", "screenshot", "ok.txt")
	if get.Code != http.StatusOK || get.Body.String() != "good" {
		t.Errorf("failed replace destroyed the original: GET got %d body=%q, want 200 \"good\"", get.Code, get.Body)
	}
}

// attachmentsFor returns the _attachments list for a property on a freshly
// serialized entity.
//
//nolint:unparam // entityID is conceptually variable; tests use one fixture.
func attachmentsFor(t *testing.T, app *App, entityID, property string) []v1.Attachment {
	t.Helper()
	result := app.serializer.forWire(context.Background(), mustGet(t, app, entityID), app.reader.outgoingRelations(context.Background(), entityID), app.Meta(), "tickets")
	if result.Attachments == nil {
		return nil
	}
	return (*result.Attachments)[property]
}

// TestAttachmentUpload_MultiAppendsUpToMax pins the max>1 behavior on the
// `docs` property (max:3): files with different names accumulate, and the
// 4th is rejected with 409. The _attachments wire value is always a list.
func TestAttachmentUpload_MultiAppendsUpToMax(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{ID: "TKT-001", Type: "ticket", Properties: map[string]any{"title": "T1"}})
	d := writeACL(t, app)
	app.acl = d

	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		rec := putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "docs", name, []byte(name))
		if rec.Code != http.StatusOK {
			t.Fatalf("upload %s: got %d, want 200; body=%s", name, rec.Code, rec.Body)
		}
	}
	if got := attachmentsFor(t, app, "TKT-001", "docs"); len(got) != 3 {
		t.Fatalf("docs should hold 3 files; got %d", len(got))
	}

	// 4th over the cap → 409.
	rec := putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "docs", "d.txt", []byte("d"))
	if rec.Code != http.StatusConflict {
		t.Errorf("upload past max: got %d, want 409; body=%s", rec.Code, rec.Body)
	}
	if got := attachmentsFor(t, app, "TKT-001", "docs"); len(got) != 3 {
		t.Errorf("rejected upload must not add a file; got %d", len(got))
	}

	// The property is stamped as a LIST of paths.
	e := mustGet(t, app, "TKT-001")
	paths, _ := e.Properties["docs"].([]string)
	if len(paths) != 3 {
		t.Errorf("docs property should be a 3-element list; got %#v", e.Properties["docs"])
	}
}

// TestAttachmentUpload_MultiAutoSuffix pins that uploading a duplicate name
// to a multi-file property auto-suffixes rather than overwriting.
func TestAttachmentUpload_MultiAutoSuffix(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{ID: "TKT-001", Type: "ticket", Properties: map[string]any{"title": "T1"}})
	d := writeACL(t, app)
	app.acl = d

	putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "docs", "report.txt", []byte("one"))
	putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "docs", "report.txt", []byte("two"))

	got := attachmentsFor(t, app, "TKT-001", "docs")
	if len(got) != 2 {
		t.Fatalf("duplicate name should auto-suffix into 2 files; got %d: %+v", len(got), got)
	}
	names := map[string]bool{got[0].FileName: true, got[1].FileName: true}
	if !names["report.txt"] || !names["report (1).txt"] {
		t.Errorf("expected report.txt + report (1).txt; got %v", names)
	}
}

// TestAttachmentDelete_MultiLeavesSiblings pins that deleting one file from
// a multi-file property leaves the others, and re-stamps the list.
func TestAttachmentDelete_MultiLeavesSiblings(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{ID: "TKT-001", Type: "ticket", Properties: map[string]any{"title": "T1"}})
	d := writeACL(t, app)
	app.acl = d

	putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "docs", "a.txt", []byte("a"))
	putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "docs", "b.txt", []byte("b"))

	del := deleteAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "docs", "a.txt")
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete one of many: got %d, want 204", del.Code)
	}

	got := attachmentsFor(t, app, "TKT-001", "docs")
	if len(got) != 1 || got[0].FileName != "b.txt" {
		t.Errorf("after deleting a.pdf, expected only b.pdf; got %+v", got)
	}
	// b.pdf still downloads.
	g := getAttachmentAs(aliceCtx(), t, app, d, "ticket", "tickets", "TKT-001", "docs", "b.txt")
	if g.Code != http.StatusOK || g.Body.String() != "b" {
		t.Errorf("sibling b.pdf must survive; GET got %d body=%q", g.Code, g.Body)
	}
}

// globalAttachmentsMetamodelYAML is a real metamodel with a *global*
// top-level `attachments:` block (allow: + scan_cmd:). The `report` file
// property deliberately sets neither accept nor scan_cmd, so both the MIME
// allowlist and the scan command must fall through from the global block.
//
// The scan_cmd is a stub shell command that exits non-zero (reject) when the
// uploaded bytes contain the marker "INFECTED", and zero (clean) otherwise.
const globalAttachmentsMetamodelYAML = `
version: "1.0"
types: {}
relations: {}
entities:
  ticket:
    label: Ticket
    id_prefix: "TKT-"
    id_type: sequential
    properties:
      title:
        type: string
        required: true
      report:
        type: file
attachments:
  allow: [text/plain]
  scan_cmd: ["sh", "-c", '! grep -q INFECTED "$1"', "sh", "{in}"]
`

// newAttachmentApp builds an App whose metamodel is parsed from metaYAML through
// the real loader (metamodel.Parse), with alice authorized to write to a seeded
// TKT-001. Shared by the attachment HTTP integration tests so a change to the
// wiring lands in one place.
func newAttachmentApp(t *testing.T, metaYAML string) (*App, *acl.Declarative) {
	t.Helper()

	meta, err := metamodel.Parse([]byte(metaYAML))
	if err != nil {
		t.Fatalf("parse metamodel: %v", err)
	}
	cfg := &dataentryconfig.Config{
		App:        dataentryconfig.AppConfig{Name: "Test App"},
		Forms:      make(map[string]dataentryconfig.Form),
		Lists:      make(map[string]dataentryconfig.List),
		Views:      make(map[string]dataentryconfig.ViewConfig),
		Kanbans:    make(map[string]dataentryconfig.Kanban),
		Navigation: []dataentryconfig.NavigationEntry{},
	}
	app := newAppFromParts(cfg, meta, newFixture())
	d := writeACL(t, app)
	app.acl = d
	seedEntity(app, &entity.Entity{ID: "TKT-001", Type: "ticket", Properties: map[string]any{"title": "T1"}})
	return app, d
}

// newGlobalAttachmentsApp builds an App on globalAttachmentsMetamodelYAML — so
// these HTTP integration tests transitively depend on the BUG-5XIN07 whitelist
// fix: revert it and Parse rejects the `attachments:` key here, failing this
// helper before any upload runs. A real command runner is wired so the global
// scan command actually executes on the upload path.
func newGlobalAttachmentsApp(t *testing.T) (*App, *acl.Declarative) {
	t.Helper()

	app, d := newAttachmentApp(t, globalAttachmentsMetamodelYAML)
	runner, err := attachment.NewCmdRunner(5*time.Second, store.MaxAttachmentBytes)
	if err != nil {
		t.Fatalf("NewCmdRunner: %v", err)
	}
	app.attachmentRunner = runner
	return app, d
}

// TestAttachmentUpload_GlobalAllowlistEnforced is an end-to-end guard that the
// global `attachments.allow:` block gates uploads through the real HTTP handler
// (not via a per-property Accept). A text file passes; a PNG (sniffs as
// image/png, not in the allowlist) is rejected 422. Regression coverage for
// BUG-5XIN07: the metamodel here is parsed through the real loader (see
// newGlobalAttachmentsApp), so reverting the whitelist fix fails this test at
// parse time; no prior test round-tripped the global allowlist through the
// upload path.
func TestAttachmentUpload_GlobalAllowlistEnforced(t *testing.T) {
	app, d := newGlobalAttachmentsApp(t)

	ok := putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "report", "clean.txt", []byte("hello"))
	if ok.Code != http.StatusOK {
		t.Fatalf("allowed text upload: got %d, want 200; body=%s", ok.Code, ok.Body)
	}

	// A real PNG header sniffs as image/png, which the global allowlist omits.
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR")
	bad := putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "report", "pic.png", png)
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("disallowed png upload: got %d, want 422; body=%s", bad.Code, bad.Body)
	}
}

// TestAttachmentUpload_GlobalScanCmdRejects is an end-to-end guard that the
// global `attachments.scan_cmd:` runs on the upload path and rejects an
// "infected" file. A clean text file passes; a text file containing the
// marker the stub scanner flags is rejected. Both go through the same real
// HTTP handler + policy processor + command runner as production.
func TestAttachmentUpload_GlobalScanCmdRejects(t *testing.T) {
	app, d := newGlobalAttachmentsApp(t)

	clean := putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "report", "clean.txt", []byte("all good"))
	if clean.Code != http.StatusOK {
		t.Fatalf("clean upload: got %d, want 200; body=%s", clean.Code, clean.Body)
	}

	infected := putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "report", "virus.txt", []byte("contains INFECTED payload"))
	if infected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("scanned-reject upload: got %d, want 422; body=%s", infected.Code, infected.Body)
	}
	// The rejected upload must not overwrite the clean attachment.
	get := getAttachmentAs(aliceCtx(), t, app, d, "ticket", "tickets", "TKT-001", "report", "clean.txt")
	if get.Code != http.StatusOK || get.Body.String() != "all good" {
		t.Errorf("clean attachment must survive a rejected scan; GET got %d body=%q", get.Code, get.Body)
	}
}

// defaultSafeMetamodelYAML is a real metamodel whose `report` file property
// takes the `default-safe` allowlist — the out-of-box configuration an operator
// gets by not setting `allow:` at all.
const defaultSafeMetamodelYAML = `
version: "1.0"
types: {}
relations: {}
entities:
  ticket:
    label: Ticket
    id_prefix: "TKT-"
    id_type: sequential
    properties:
      title:
        type: string
        required: true
      report:
        type: file
`

// newDefaultSafeApp builds an App whose `report` file property takes the
// `default-safe` allowlist — the out-of-box configuration an operator gets by
// not setting `allow:` at all.
func newDefaultSafeApp(t *testing.T) (*App, *acl.Declarative) {
	t.Helper()
	return newAttachmentApp(t, defaultSafeMetamodelYAML)
}

// zipBytes builds a real ZIP archive — the shape every ZIP-container document
// (docx, odt, epub, …) has on the wire. The member name cannot affect any
// verdict: http.DetectContentType stops at the PK\x03\x04 header.
func zipBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("content.xml")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write([]byte(`<?xml version="1.0"?><x/>`)); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// TestAttachmentUpload_ZipContainerDocuments is the end-to-end guard for
// BUG-TDE1QO: a docx/xlsx/odt/epub sniffs as application/zip while its
// extension claims a concrete type, and the sniff-vs-extension check rejected
// every one of them through the real upload handler under `default-safe`.
func TestAttachmentUpload_ZipContainerDocuments(t *testing.T) {
	app, d := newDefaultSafeApp(t)
	data := zipBytes(t)

	for _, name := range []string{"report.docx", "budget.xlsx", "notes.odt", "book.epub", "plain.zip"} {
		t.Run(name, func(t *testing.T) {
			rec := putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "report", name, data)
			if rec.Code != http.StatusOK {
				t.Fatalf("upload: got %d, want 200; body=%s", rec.Code, rec.Body)
			}
			get := getAttachmentAs(aliceCtx(), t, app, d, "ticket", "tickets", "TKT-001", "report", name)
			if get.Code != http.StatusOK || !bytes.Equal(get.Body.Bytes(), data) {
				t.Errorf("GET: got %d, want 200 with the uploaded bytes", get.Code)
			}
		})
	}
}

// TestAttachmentUpload_ZipExecutableContainersRejected pins that the fix stayed
// narrow: ZIP is also the container for executable formats, so those keep
// failing the mismatch check even though the bytes sniff as an allowed
// application/zip.
func TestAttachmentUpload_ZipExecutableContainersRejected(t *testing.T) {
	app, d := newDefaultSafeApp(t)
	data := zipBytes(t)

	for _, name := range []string{"app.jar", "app.apk", "addon.xpi", "macro.docm", "fake.pdf"} {
		t.Run(name, func(t *testing.T) {
			rec := putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "report", name, data)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("upload: got %d, want 422; body=%s", rec.Code, rec.Body)
			}
		})
	}
}

// TestAttachmentUpload_FullUploadBudgetIs503 pins that an upload arriving
// while every upload slot is taken is refused with 503 before its body is
// read, and nothing is written.
func TestAttachmentUpload_FullUploadBudgetIs503(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{ID: "TKT-001", Type: "ticket", Properties: map[string]any{"title": "T1"}})
	d := writeACL(t, app)
	app.acl = d
	for range attachment.DefaultMaxUploads {
		release, ok := app.attachments.uploads.TryAcquire()
		if !ok {
			t.Fatal("could not fill the upload budget")
		}
		t.Cleanup(release)
	}

	rec := putAttachmentAs(aliceCtx(), t, app, d, "TKT-001", "screenshot", "shot.txt", []byte("DATA"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("upload with a full budget: got %d, want 503; body=%s", rec.Code, rec.Body)
	}
	if got := mustGet(t, app, "TKT-001").GetString("screenshot"); got != "" {
		t.Errorf("property = %q after a refused upload, want empty", got)
	}
}

// TestAttachmentWrite_OwnedPropertyIs422BeforeBytes: an upload (new or same
// name) or delete on a file property an external system owns through a twin
// answers 422 externally_owned, and the attachment list, the bytes and the
// stamped property are all left as they were.
func TestAttachmentWrite_OwnedPropertyIs422BeforeBytes(t *testing.T) {
	app, _ := twinsApp(t, "screenshot")
	d := writeACL(t, app)
	app.acl = d
	ctx := context.Background()
	const stamped = "attachments/" + twinTicket + "/screenshot/a.txt"
	seedAttachment(t, app, twinTicket, "a.txt", []byte("original"))
	e := mustGet(t, app, twinTicket)
	e.Properties["screenshot"] = stamped
	if err := app.store.UpdateEntity(ctx, e); err != nil {
		t.Fatalf("stamp seed: %v", err)
	}

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"upload new name":  putAttachmentAs(aliceCtx(), t, app, d, twinTicket, "screenshot", "b.txt", []byte("new")),
		"upload same name": putAttachmentAs(aliceCtx(), t, app, d, twinTicket, "screenshot", "a.txt", []byte("new")),
		"delete":           deleteAttachmentAs(aliceCtx(), t, app, d, twinTicket, "screenshot", "a.txt"),
	} {
		if rec.Code != http.StatusUnprocessableEntity || !bytes.Contains(rec.Body.Bytes(), []byte("/errors/externally_owned")) {
			t.Errorf("%s: got %d %s, want 422 externally_owned", name, rec.Code, rec.Body)
		}
	}

	infos, err := app.store.ListAttachments(ctx, twinTicket)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(infos) != 1 || infos[0].FileName != "a.txt" {
		t.Errorf("attachments = %+v, want only screenshot/a.txt", infos)
	}
	get := getAttachmentAs(aliceCtx(), t, app, d, "ticket", "tickets", twinTicket, "screenshot", "a.txt")
	if get.Body.String() != "original" {
		t.Errorf("a.txt = %q, want the original bytes", get.Body)
	}
	if got := mustGet(t, app, twinTicket).GetString("screenshot"); got != stamped {
		t.Errorf("screenshot = %q, want it unchanged", got)
	}
}
