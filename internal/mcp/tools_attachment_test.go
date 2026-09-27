package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/appbuild"
	"github.com/Sourcehaven-BV/rela/internal/appbuild/appbuildtest"
	"github.com/Sourcehaven-BV/rela/internal/attachment"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/lock"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

const (
	docID     = "DOC-001"
	lockedID  = "DOC-LOCKED"
	vaultID   = "VLT-001"
	missingID = "VLT-999"
)

// pngBytes is the PNG signature plus padding: enough for content sniffing.
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)

func attachmentMeta() *metamodel.Metamodel {
	fileProps := map[string]metamodel.PropertyDef{
		"title": {Type: "string"},
		"file":  {Type: metamodel.PropertyTypeFile},
		"files": {Type: metamodel.PropertyTypeFile, Max: 3},
		"pair":  {Type: metamodel.PropertyTypeFile, Max: 2},
		"cover": {Type: metamodel.PropertyTypeFile, Accept: []string{"image/png"}},
	}
	return &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"doc":   {Label: "Doc", IDPrefix: "DOC", Properties: fileProps},
			"vault": {Label: "Vault", IDPrefix: "VLT", Properties: fileProps},
		},
	}
}

// attachFixture is an MCP server over attachmentMeta, read through the
// production gated seam.
type attachFixture struct {
	srv   *Server
	svc   *appbuild.Services
	audit *audit.Memory
}

type attachOpts struct {
	policy *acl.Policy
	limit  int64
	// twins overrides the attachment ownership guard; nil uses the build's.
	twins attachment.TwinOwnership
}

func newAttachFixture(t *testing.T, opts attachOpts) attachFixture {
	t.Helper()
	meta := attachmentMeta()
	st := memstore.New()
	ctx := context.Background()

	locked := newEntity(lockedID, "doc", "locked")
	locked.Inaccessible = []entity.InaccessibleField{{Name: "title", Reason: entity.InaccessibleReasonGitCrypt}}
	for _, e := range []*entity.Entity{newEntity(docID, "doc", "doc"), newEntity(vaultID, "vault", "vault"), locked} {
		if err := st.CreateEntity(ctx, e); err != nil {
			t.Fatalf("seed %s: %v", e.ID, err)
		}
	}

	buildOpts := []appbuildtest.Option{appbuildtest.WithStore(st)}
	if opts.policy != nil {
		d, err := acl.NewDeclarative(opts.policy, acl.NewStoreGraph(st), st)
		if err != nil {
			t.Fatalf("acl.NewDeclarative: %v", err)
		}
		buildOpts = append(buildOpts, appbuildtest.WithDeclarative(d))
	}
	svc := appbuildtest.New(meta, buildOpts...)
	t.Cleanup(func() { _ = svc.Close() })

	limit := opts.limit
	if limit == 0 {
		limit = store.MaxAttachmentBytes
	}
	var twins attachment.TwinOwnership = appbuild.TwinOwnership(svc)
	if opts.twins != nil {
		twins = opts.twins
	}
	snap, err := NewAttachmentSnapshot(svc.Store(), svc.EntityManager(), lock.NewMemoryLocker(), svc.ACL(),
		twins, meta, nil, limit)
	if err != nil {
		t.Fatalf("NewAttachmentSnapshot: %v", err)
	}
	sink := &audit.Memory{}

	reads := svc.GatedReads()
	deps := Deps{
		Store:         reads.Reader,
		Meta:          meta,
		Tracer:        reads.Tracer,
		Searcher:      svc.Searcher(),
		Validator:     reads.Validator,
		EntityManager: svc.EntityManager(),
		Config:        svc.Config(),
		LuaWriteDeps:  svc.LuaWriteDeps(),
		Watcher:       nopWatcher{},
		ProjectRoot:   t.TempDir(),
		Attachments: AttachmentDeps{
			Snapshot:   func() (AttachmentSnapshot, error) { return snap, nil },
			Uploads:    attachment.NewLimiter(attachment.DefaultMaxUploads),
			Authorizer: svc.ACL(),
			Audit:      sink,
		},
	}
	srv, err := NewServer(deps, "test", WithPrincipal(principal.Principal{User: "tester", Tool: principal.ToolMCP}))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.logger = slog.New(slog.DiscardHandler)
	return attachFixture{srv: srv, svc: svc, audit: sink}
}

// call invokes one attachment tool handler directly with ctx's principal.
func (f attachFixture) call(ctx context.Context, t *testing.T, tool string, args map[string]any) *mcpgo.CallToolResult {
	t.Helper()
	h := group(f.srv, selAttach)
	handlers := map[string]func(context.Context, *mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error){
		"list_attachments":  h.handleListAttachments,
		"read_attachment":   h.handleReadAttachment,
		"attach_file":       h.handleAttachFile,
		"delete_attachment": h.handleDeleteAttachment,
	}
	res, err := handlers[tool](ctx, makeToolRequest(args))
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return res
}

func (f attachFixture) attach(
	ctx context.Context, t *testing.T, id, property, fileName string, data []byte,
) *mcpgo.CallToolResult {
	t.Helper()
	return f.call(ctx, t, "attach_file", map[string]any{
		"id": id, "property": property, "file_name": fileName,
		"content": base64.StdEncoding.EncodeToString(data),
	})
}

func (f attachFixture) list(ctx context.Context, t *testing.T, id string) []attachmentListing {
	t.Helper()
	res := f.call(ctx, t, "list_attachments", map[string]any{"id": id})
	mustSucceed(t, res)
	var out []attachmentListing
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	return out
}

// stored returns the raw stored value of property on id.
func (f attachFixture) stored(t *testing.T, id, property string) any {
	t.Helper()
	e, err := f.svc.Store().GetEntity(context.Background(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return e.Properties[property]
}

func resultText(t *testing.T, res *mcpgo.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("empty result content")
	}
	tc, ok := res.Content[0].(*mcpgo.TextContent)
	if !ok {
		t.Fatalf("content is %T, want text", res.Content[0])
	}
	return tc.Text
}

func mustSucceed(t *testing.T, res *mcpgo.CallToolResult) {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, res))
	}
}

func mustFail(t *testing.T, res *mcpgo.CallToolResult, want string) {
	t.Helper()
	if !res.IsError {
		t.Fatalf("expected a tool error containing %q, got success", want)
	}
	if got := resultText(t, res); !strings.Contains(got, want) {
		t.Fatalf("error = %q, want it to contain %q", got, want)
	}
}

func fileNames(ls []attachmentListing) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.Property+"/"+l.FileName)
	}
	return out
}

func TestAttachments_RoundTrip(t *testing.T) {
	t.Parallel()
	f := newAttachFixture(t, attachOpts{})
	ctx := context.Background()

	res := f.attach(ctx, t, docID, "file", "notes.txt", []byte("hello"))
	mustSucceed(t, res)
	var written attachResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &written); err != nil {
		t.Fatalf("decode attach result: %v", err)
	}
	if written.FileName != "notes.txt" || f.stored(t, docID, "file") != written.Path {
		t.Fatalf("attach result %+v, stored %v", written, f.stored(t, docID, "file"))
	}

	ls := f.list(ctx, t, docID)
	if len(ls) != 1 || ls[0].FileName != "notes.txt" || ls[0].Size != 5 {
		t.Fatalf("listing = %+v", ls)
	}

	read := f.call(ctx, t, "read_attachment", map[string]any{"id": docID, "property": "file", "file_name": "notes.txt"})
	if got := resultText(t, read); got != "hello" {
		t.Fatalf("read = %q, want hello", got)
	}

	// A single-file property replaces the current file.
	mustSucceed(t, f.attach(ctx, t, docID, "file", "other.txt", []byte("bye")))
	if got := fileNames(f.list(ctx, t, docID)); len(got) != 1 || got[0] != "file/other.txt" {
		t.Fatalf("after replace: %v", got)
	}

	// With one file present, the name may be omitted.
	mustSucceed(t, f.call(ctx, t, "delete_attachment", map[string]any{"id": docID, "property": "file"}))
	if got := f.list(ctx, t, docID); len(got) != 0 {
		t.Fatalf("after delete: %+v", got)
	}
	if v := f.stored(t, docID, "file"); v != nil && v != "" {
		t.Fatalf("property after delete = %#v, want empty", v)
	}
}

func TestAttachments_MultiFileSuffixAndCapacity(t *testing.T) {
	t.Parallel()
	f := newAttachFixture(t, attachOpts{})
	ctx := context.Background()

	mustSucceed(t, f.attach(ctx, t, docID, "files", "a.txt", []byte("1")))
	mustSucceed(t, f.attach(ctx, t, docID, "files", "a.txt", []byte("2")))
	got := fileNames(f.list(ctx, t, docID))
	if len(got) != 2 || got[0] != "files/a (1).txt" || got[1] != "files/a.txt" {
		t.Fatalf("listing = %v", got)
	}
	if paths, ok := f.stored(t, docID, "files").([]string); !ok || len(paths) != 2 {
		t.Fatalf("stored files = %#v, want two paths", f.stored(t, docID, "files"))
	}

	mustSucceed(t, f.attach(ctx, t, docID, "pair", "x.txt", []byte("x")))
	mustSucceed(t, f.attach(ctx, t, docID, "pair", "y.txt", []byte("y")))
	mustFail(t, f.attach(ctx, t, docID, "pair", "z.txt", []byte("z")), "maximum number of attachments")
}

func TestAttachments_ReadReturnsTypedContent(t *testing.T) {
	t.Parallel()
	f := newAttachFixture(t, attachOpts{})
	ctx := context.Background()

	mustSucceed(t, f.attach(ctx, t, docID, "files", "pic.png", pngBytes))
	res := f.call(ctx, t, "read_attachment", map[string]any{"id": docID, "property": "files", "file_name": "pic.png"})
	mustSucceed(t, res)
	img, ok := res.Content[0].(*mcpgo.ImageContent)
	if !ok || img.MIMEType != "image/png" || !bytes.Equal(img.Data, pngBytes) {
		t.Fatalf("content = %#v, want png image", res.Content[0])
	}
}

func TestAttachmentContent(t *testing.T) {
	t.Parallel()
	pdf := []byte("%PDF-1.4\n")
	tests := []struct {
		name     string
		fileName string
		data     []byte
		want     string // "text", "image", or the blob MIME type
	}{
		{"png is an image", "a.png", pngBytes, "image"},
		{"svg is active content, so an opaque blob", "a.svg", []byte("<svg/>"), "application/octet-stream"},
		{"html is plain text, never rendered", "a.html", []byte("<html><script>x</script>"), "text"},
		{"png extension on other bytes is an opaque blob", "a.png", []byte("not an image"), "application/octet-stream"},
		{"pdf is a blob", "a.pdf", pdf, "application/pdf"},
		{"unknown extension with text sniffs as text", "a.zzz", []byte("plain words"), "text"},
		{"markdown is text on any host", "a.md", []byte("# heading"), "text"},
		{"invalid UTF-8 text is a blob", "a.txt", []byte{'a', 0xff, 'b'}, "text/plain"},
		{"binary is an octet-stream blob", "a.zzz", []byte{0x00, 0x01, 0x02}, "application/octet-stream"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := attachmentContent(docID, "files", tc.fileName, tc.data)
			switch v := c.(type) {
			case *mcpgo.TextContent:
				if tc.want != "text" {
					t.Fatalf("got text, want %s", tc.want)
				}
			case *mcpgo.ImageContent:
				if tc.want != "image" {
					t.Fatalf("got image, want %s", tc.want)
				}
			case *mcpgo.EmbeddedResource:
				if v.Resource.MIMEType != tc.want {
					t.Fatalf("blob MIME = %s, want %s", v.Resource.MIMEType, tc.want)
				}
				if wantURI := "rela://attachment/" + docID + "/files/" + url.PathEscape(tc.fileName); v.Resource.URI != wantURI {
					t.Fatalf("URI = %s, want %s", v.Resource.URI, wantURI)
				}
			default:
				t.Fatalf("unexpected content %T", c)
			}
		})
	}
}

func TestAttachments_InlineReadCap(t *testing.T) {
	t.Parallel()
	f := newAttachFixture(t, attachOpts{})
	ctx := context.Background()

	big := bytes.Repeat([]byte("a"), MaxInlineReadBytes+1)
	if err := f.svc.Store().AttachFile(ctx, docID, "file", "big.txt", bytes.NewReader(big)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res := f.call(ctx, t, "read_attachment", map[string]any{"id": docID, "property": "file", "file_name": "big.txt"})
	mustFail(t, res, "inline read limit")
}

func TestAttachments_UploadTooLarge(t *testing.T) {
	t.Parallel()
	f := newAttachFixture(t, attachOpts{limit: 8})
	ctx := context.Background()

	mustFail(t, f.attach(ctx, t, docID, "file", "a.txt", bytes.Repeat([]byte("a"), 9)), "attachment too large")
	if got := f.list(ctx, t, docID); len(got) != 0 {
		t.Fatalf("oversize upload was stored: %+v", got)
	}
	// Exactly at the limit is allowed, although 8 is not a multiple of 3.
	mustSucceed(t, f.attach(ctx, t, docID, "file", "a.txt", bytes.Repeat([]byte("a"), 8)))
}

func TestAttachments_ArgumentErrors(t *testing.T) {
	t.Parallel()
	f := newAttachFixture(t, attachOpts{})
	ctx := context.Background()

	tests := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"invalid base64", "attach_file",
			map[string]any{"id": docID, "property": "file", "file_name": "a.txt", "content": "!!"}, "not valid base64"},
		{"non-file property", "attach_file",
			map[string]any{"id": docID, "property": "title", "file_name": "a.txt", "content": "aGk="}, "not a file type"},
		{"undeclared property", "read_attachment",
			map[string]any{"id": docID, "property": "nope", "file_name": "a.txt"}, "not defined"},
		{"empty file name", "read_attachment",
			map[string]any{"id": docID, "property": "file", "file_name": " "}, "must not be empty"},
		{"path as file name", "read_attachment",
			map[string]any{"id": docID, "property": "file", "file_name": "../x.txt"}, "plain file name"},
		{"backslash in file name", "delete_attachment",
			map[string]any{"id": docID, "property": "file", "file_name": `..\x.txt`}, "plain file name"},
		{"non-string file name", "delete_attachment",
			map[string]any{"id": docID, "property": "file", "file_name": 123}, "not a string"},
		{"missing file", "read_attachment",
			map[string]any{"id": docID, "property": "file", "file_name": "gone.txt"}, "attachment not found"},
		{"unknown entity", "list_attachments", map[string]any{"id": missingID}, "entity not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mustFail(t, f.call(ctx, t, tc.tool, tc.args), tc.want)
		})
	}
}

func TestAttachments_RejectedUploadIsAudited(t *testing.T) {
	t.Parallel()
	f := newAttachFixture(t, attachOpts{})
	ctx := context.Background()

	mustFail(t, f.attach(ctx, t, docID, "cover", "a.txt", []byte("not an image")), "attachment rejected")

	recs := f.audit.Records()
	if len(recs) != 1 {
		t.Fatalf("audit records = %d, want 1", len(recs))
	}
	if recs[0].Op != audit.OpDeniedWrite || !strings.Contains(recs[0].Summary, "op=attachment-write") {
		t.Fatalf("audit record = %+v", recs[0])
	}
	if got := f.list(ctx, t, docID); len(got) != 0 {
		t.Fatalf("rejected upload was stored: %+v", got)
	}
}

func TestAttachments_Delete(t *testing.T) {
	t.Parallel()
	f := newAttachFixture(t, attachOpts{})
	ctx := context.Background()

	// A named file that is not there: success, so a retry is safe.
	gone := f.call(ctx, t, "delete_attachment",
		map[string]any{"id": docID, "property": "files", "file_name": "gone.txt"})
	mustSucceed(t, gone)
	if got := resultText(t, gone); !strings.Contains(got, "nothing to remove") {
		t.Fatalf("absent delete = %q, want it to say nothing was removed", got)
	}

	mustFail(t, f.call(ctx, t, "delete_attachment", map[string]any{"id": docID, "property": "files"}),
		"has no attachment")

	mustSucceed(t, f.attach(ctx, t, docID, "files", "a.txt", []byte("a")))
	mustSucceed(t, f.attach(ctx, t, docID, "files", "b.txt", []byte("b")))
	mustFail(t, f.call(ctx, t, "delete_attachment", map[string]any{"id": docID, "property": "files"}), "b.txt")

	mustSucceed(t, f.call(ctx, t, "delete_attachment",
		map[string]any{"id": docID, "property": "files", "file_name": "a.txt"}))
	if got := fileNames(f.list(ctx, t, docID)); len(got) != 1 || got[0] != "files/b.txt" {
		t.Fatalf("after delete: %v", got)
	}
	if paths, ok := f.stored(t, docID, "files").([]string); !ok || len(paths) != 1 {
		t.Fatalf("stored files = %#v, want one path", f.stored(t, docID, "files"))
	}
}

func TestAttachments_LockedEntityRefusesWrites(t *testing.T) {
	t.Parallel()
	f := newAttachFixture(t, attachOpts{})
	ctx := context.Background()

	mustFail(t, f.attach(ctx, t, lockedID, "file", "a.txt", []byte("a")), "inaccessible")
	mustFail(t, f.call(ctx, t, "delete_attachment",
		map[string]any{"id": lockedID, "property": "file", "file_name": "a.txt"}), "inaccessible")
}

// ownsFile owns every entity's `file` property for basecamp.
type ownsFile struct{}

func (ownsFile) OwnedFields(context.Context, string, string) (map[string][]string, error) {
	return map[string][]string{"file": {"basecamp"}}, nil
}

// TestAttachments_OwnedPropertyRefusesWrites: attach_file and
// delete_attachment on a file property an external system owns answer the
// ownership sentence and leave the stored bytes alone.
func TestAttachments_OwnedPropertyRefusesWrites(t *testing.T) {
	t.Parallel()
	f := newAttachFixture(t, attachOpts{twins: ownsFile{}})
	ctx := context.Background()
	if err := f.svc.Store().AttachFile(ctx, docID, "file", "a.txt", strings.NewReader("original")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mustFail(t, f.attach(ctx, t, docID, "file", "a.txt", []byte("clobbered")), "owned by basecamp")
	mustFail(t, f.call(ctx, t, "delete_attachment",
		map[string]any{"id": docID, "property": "file", "file_name": "a.txt"}), "owned by basecamp")

	rc, err := f.svc.Store().ReadAttachment(ctx, docID, "file", "a.txt")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	defer rc.Close()
	if got, _ := io.ReadAll(rc); string(got) != "original" {
		t.Fatalf("bytes = %q, want original", got)
	}
	// An unowned property of the same entity still takes uploads.
	mustSucceed(t, f.attach(ctx, t, docID, "files", "b.txt", []byte("b")))
}

func TestAttachments_ConcurrentAttachRespectsMax(t *testing.T) {
	t.Parallel()
	f := newAttachFixture(t, attachOpts{})
	ctx := context.Background()

	const writers = 8
	h := group(f.srv, selAttach)
	var wg sync.WaitGroup
	results := make([]*mcpgo.CallToolResult, writers)
	errs := make([]error, writers)
	for i := range writers {
		wg.Go(func() {
			results[i], errs[i] = h.handleAttachFile(ctx, makeToolRequest(map[string]any{
				"id": docID, "property": "pair", "file_name": fmt.Sprintf("f%d.txt", i),
				"content": base64.StdEncoding.EncodeToString([]byte("x")),
			}))
		})
	}
	wg.Wait()

	ok := 0
	for i, r := range results {
		if errs[i] != nil {
			t.Fatalf("writer %d: %v", i, errs[i])
		}
		if !r.IsError {
			ok++
		}
	}
	if ok != 2 {
		t.Fatalf("%d writers succeeded, want 2", ok)
	}
	if got := f.list(ctx, t, docID); len(got) != 2 {
		t.Fatalf("stored %d files, want 2: %+v", len(got), got)
	}
}

// aclPolicy: every role reads docs; nobody reads vaults. bob may update docs,
// alice may not, and carol may update but sees only the title.
func aclPolicy() *acl.Policy {
	return &acl.Policy{
		Roles: map[string]acl.RoleDef{
			"reader": {Read: []string{"doc"}},
			"editor": {Read: []string{"doc"}, Update: []string{"doc"}},
			"narrow": {
				Read: []string{"doc"}, Update: []string{"doc"},
				Visible: map[string][]acl.FieldGrant{"doc": {{Field: "title"}}},
			},
		},
		Assignments: map[string]string{"alice": "reader", "bob": "editor", "carol": "narrow"},
	}
}

func as(user string) context.Context {
	return principal.With(context.Background(), principal.Principal{User: user, Tool: principal.ToolMCP})
}

// TestAttachments_ACL_HiddenIsIndistinguishableFromAbsent: every tool answers
// a hidden entity exactly as it answers a nonexistent one.
func TestAttachments_ACL_HiddenIsIndistinguishableFromAbsent(t *testing.T) {
	t.Parallel()
	f := newAttachFixture(t, attachOpts{policy: aclPolicy()})
	ctx := as("bob")

	for _, tool := range []string{"list_attachments", "read_attachment", "attach_file", "delete_attachment"} {
		t.Run(tool, func(t *testing.T) {
			t.Parallel()
			args := func(id string) map[string]any {
				return map[string]any{"id": id, "property": "file", "file_name": "a.txt", "content": "aGk="}
			}
			hidden := f.call(ctx, t, tool, args(vaultID))
			absent := f.call(ctx, t, tool, args(missingID))
			if !hidden.IsError || !absent.IsError {
				t.Fatalf("hidden error=%v, absent error=%v; want both errors", hidden.IsError, absent.IsError)
			}
			h := strings.ReplaceAll(resultText(t, hidden), vaultID, "<id>")
			a := strings.ReplaceAll(resultText(t, absent), missingID, "<id>")
			if h != a {
				t.Fatalf("hidden answer %q differs from absent answer %q", h, a)
			}
		})
	}
}

func TestAttachments_ACL_DeniedWriteLeavesBytes(t *testing.T) {
	t.Parallel()
	f := newAttachFixture(t, attachOpts{policy: aclPolicy()})

	mustSucceed(t, f.attach(as("bob"), t, docID, "file", "a.txt", []byte("original")))

	mustFail(t, f.attach(as("alice"), t, docID, "file", "a.txt", []byte("clobbered")), "forbidden")
	mustFail(t, f.call(as("alice"), t, "delete_attachment",
		map[string]any{"id": docID, "property": "file", "file_name": "a.txt"}), "forbidden")

	rc, err := f.svc.Store().ReadAttachment(context.Background(), docID, "file", "a.txt")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "original" {
		t.Fatalf("bytes = %q, want original", got)
	}

	recs := f.audit.Records()
	if len(recs) != 2 {
		t.Fatalf("audit records = %d, want 2", len(recs))
	}
	for _, r := range recs {
		if r.Op != audit.OpDeniedWrite || !strings.Contains(r.Summary, "op=attachment-write") {
			t.Fatalf("audit record = %+v", r)
		}
	}

	// alice may still read what she may not write.
	read := f.call(as("alice"), t, "read_attachment", map[string]any{"id": docID, "property": "file", "file_name": "a.txt"})
	if resultText(t, read) != "original" {
		t.Fatalf("alice read = %q", resultText(t, read))
	}
}

func TestAttachments_ACL_HiddenPropertyAnswersNotFound(t *testing.T) {
	t.Parallel()
	f := newAttachFixture(t, attachOpts{policy: aclPolicy()})
	mustSucceed(t, f.attach(as("bob"), t, docID, "file", "a.txt", []byte("secret")))

	carol := as("carol")
	if got := f.list(carol, t, docID); len(got) != 0 {
		t.Fatalf("carol lists %+v, want nothing", got)
	}
	args := map[string]any{"id": docID, "property": "file", "file_name": "a.txt", "content": "aGk="}
	for _, tool := range []string{"read_attachment", "attach_file", "delete_attachment"} {
		mustFail(t, f.call(carol, t, tool, args), "attachment not found")
	}
}

// TestAttachments_HTTPAcceptsLargeUpload pins the raised request body limit:
// a full-size upload fits. The go-sdk default of 4 MiB would refuse it before
// any handler ran.
func TestAttachments_HTTPAcceptsLargeUpload(t *testing.T) {
	t.Parallel()
	f := newAttachFixture(t, attachOpts{})
	ts := httptest.NewServer(f.srv.HTTPHandler())
	t.Cleanup(ts.Close)

	data := bytes.Repeat([]byte("a"), MaxUploadBytes)
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "attach_file", "arguments": map[string]any{
			"id": docID, "property": "file", "file_name": "big.txt",
			"content": base64.StdEncoding.EncodeToString(data),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.ContentLength = -1 // no length: the request must pass the large-request gate
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, msg)
	}
	if got := f.list(context.Background(), t, docID); len(got) != 1 || got[0].Size != int64(len(data)) {
		t.Fatalf("listing = %+v", got)
	}
}

// TestAttachments_StdioAcceptsLargeUpload pins the raised stdio frame limit.
// The go-sdk default (16 MiB) is below a full-size upload once base64-encoded,
// and an oversize frame ends the session instead of failing one call.
func TestAttachments_StdioAcceptsLargeUpload(t *testing.T) {
	t.Parallel()
	f := newAttachFixture(t, attachOpts{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- f.srv.mcp.Run(ctx, &mcpgo.IOTransport{
			Reader: inR, Writer: outW, MaxLineLength: stdioTransport().MaxLineLength,
		})
	}()

	data := bytes.Repeat([]byte("a"), MaxUploadBytes)
	frames := []map[string]any{
		{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
			"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
			"clientInfo": map[string]any{"name": "t", "version": "0"},
		}},
		{"jsonrpc": "2.0", "method": "notifications/initialized"},
		{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{
			"name": "attach_file", "arguments": map[string]any{
				"id": docID, "property": "file", "file_name": "big.txt",
				"content": base64.StdEncoding.EncodeToString(data),
			},
		}},
	}
	go func() {
		enc := json.NewEncoder(inW)
		for _, fr := range frames {
			if err := enc.Encode(fr); err != nil {
				return
			}
		}
	}()

	dec := json.NewDecoder(outR)
	for {
		var msg struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			t.Fatalf("reading response (session ended?): %v; run: %v", err, <-done)
		}
		if msg.ID != 2 {
			continue
		}
		if msg.Error != nil || bytes.Contains(msg.Result, []byte(`"isError":true`)) {
			t.Fatalf("attach_file failed: %s %s", msg.Error, msg.Result)
		}
		break
	}
	if got := f.list(context.Background(), t, docID); len(got) != 1 || got[0].Size != int64(len(data)) {
		t.Fatalf("listing = %+v", got)
	}
}

func TestLargeRequestGate(t *testing.T) {
	t.Parallel()
	gate := newLargeRequestGate(1)
	var served int
	var mu sync.Mutex
	h := gate.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		mu.Lock()
		served++
		mu.Unlock()
	}))
	gate.slots <- struct{}{} // the only slot is taken

	small := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, small)
	if served != 1 {
		t.Fatal("a small request queued behind the large-request gate")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	large := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader("{}"))
	large.ContentLength = -1
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, large)
	if served != 1 || rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("large request with no free slot: served=%d status=%d, want 1 and 503", served, rec.Code)
	}

	<-gate.slots
	large = httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	large.ContentLength = smallRequestBytes + 1
	h.ServeHTTP(httptest.NewRecorder(), large)
	if served != 2 || len(gate.slots) != 0 {
		t.Fatalf("large request with a free slot: served=%d slots held=%d, want 2 and 0", served, len(gate.slots))
	}
}
