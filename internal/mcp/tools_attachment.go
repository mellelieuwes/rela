package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/attachment"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// MaxUploadBytes is the largest file attach_file accepts. It is lower than the
// web upload limit because an MCP upload arrives as base64 inside a JSON-RPC
// body and is held in memory several times over (request body, argument
// string, decoded bytes, processor buffer), where the web path streams a
// multipart body to disk.
const MaxUploadBytes = 16 << 20

// MaxInlineReadBytes is the largest file read_attachment returns. The bytes
// travel inline in the tool result, so the cap keeps one call from filling a
// model's context or the transport.
const MaxInlineReadBytes = 10 << 20

// maxRequestBodyBytes bounds one MCP request on either transport: an
// attach_file call at [MaxUploadBytes] plus headroom for the JSON-RPC envelope
// and the other arguments. The go-sdk defaults (4 MiB over HTTP, 16 MiB per
// stdio frame) would refuse most uploads, and over stdio an oversize frame
// ends the whole session rather than failing one call.
var maxRequestBodyBytes = int64(base64.StdEncoding.EncodedLen(MaxUploadBytes)) + 1<<20

// stdioTransport is the `rela mcp` transport, with its frame limit raised to
// [maxRequestBodyBytes].
func stdioTransport() *mcpgo.StdioTransport {
	return &mcpgo.StdioTransport{MaxLineLength: int(maxRequestBodyBytes)}
}

// smallRequestBytes is the go-sdk's default HTTP body limit. Requests up to
// this size are admitted without queuing, as before TKT-R6U15C.
const smallRequestBytes = 4 << 20

// largeRequestSlots is how many larger HTTP requests may be in flight at once.
const largeRequestSlots = 2

// largeRequestGate queues HTTP requests that may be larger than
// [smallRequestBytes] BEFORE the go-sdk reads their body. The SDK buffers the
// whole body, and the tool then holds several copies of an upload while it
// scans and writes it, so unbounded concurrency would let any
// authenticated caller exhaust memory. A queued request holds a connection,
// not memory, which is how the web upload path behaves too. A request with no
// Content-Length counts as large.
type largeRequestGate struct{ slots chan struct{} }

func newLargeRequestGate(n int) largeRequestGate {
	return largeRequestGate{slots: make(chan struct{}, n)}
}

func (g largeRequestGate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength >= 0 && r.ContentLength <= smallRequestBytes {
			next.ServeHTTP(w, r)
			return
		}
		select {
		case g.slots <- struct{}{}:
			defer func() { <-g.slots }()
		case <-r.Context().Done():
			http.Error(w, "request cancelled while queued", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AttachmentDeps is what the attachment tools need beyond [Deps.Store], which
// they use for the gated entity read that every attachment operation starts
// with.
type AttachmentDeps struct {
	// Snapshot returns the attachment services for ONE tool call. Each call
	// resolves it once and uses the result throughout, so the property check
	// and the upload policy come from the same metamodel. The remote wiring
	// backs it with the data-entry App's live schema, so an operator's policy
	// edit reaches MCP uploads when it reaches web uploads. An error fails
	// the tool call.
	Snapshot func() (AttachmentSnapshot, error)

	// Uploads bounds concurrent uploads. Share the process's one
	// [attachment.Limiter] with every other upload path.
	Uploads *attachment.Limiter

	// Authorizer decides the `update` write an attachment change amounts to,
	// BEFORE any bytes are stored. The manager authorizes again when it
	// stamps the property, but by then the bytes have landed: a denied caller
	// could otherwise overwrite a same-name file at `max: 1`.
	Authorizer WriteAuthorizer

	// Audit records denied and rejected writes. Nil: rejected — use
	// [audit.Nop] for "record nothing".
	Audit audit.Audit
}

// AttachmentSnapshot is one consistent view of the attachment policy.
type AttachmentSnapshot struct {
	Meta    *metamodel.Metamodel
	Service *attachment.Service

	// MaxUploadBytes is the effective upload limit, at most [MaxUploadBytes].
	MaxUploadBytes int64
}

// WriteAuthorizer is the one ACL method the attachment write tools call. The
// wiring site supplies the project's acl.ACL.
type WriteAuthorizer interface {
	AuthorizeWrite(ctx context.Context, req acl.WriteRequest) acl.Decision
}

// NewAttachmentSnapshot builds the attachment services over st for meta. The
// processor applies the metamodel's MIME allowlist and, when runner is
// non-nil, its scan and transform commands; with a nil runner a configured
// scan rejects the upload (fail closed). limit is capped at [MaxUploadBytes].
// twins refuses uploads and deletes on a file property an external system
// owns.
//
// st is the raw store: [attachment.Service] reads and writes attachment bytes
// through it. The tools only reach it after the gated entity read has
// admitted the caller.
//
// locker serializes writers to one (entity, property). Pass the locker every
// other attachment writer in the process uses, so MCP and web writes to one
// property exclude each other. authz re-authorizes each write under that lock;
// pass the same authorizer as [AttachmentDeps.Authorizer].
func NewAttachmentSnapshot(
	st store.Store, em attachment.EntityPatcher, locker attachment.Locker, authz WriteAuthorizer,
	twins attachment.TwinOwnership, meta *metamodel.Metamodel, runner attachment.CommandRunner, limit int64,
) (AttachmentSnapshot, error) {
	if authz == nil {
		return AttachmentSnapshot{}, errors.New("mcp: NewAttachmentSnapshot: authz is required")
	}
	svc, err := attachment.New(attachment.Deps{
		Store:         st,
		Meta:          meta,
		EntityManager: em,
		Locker:        locker,
		Authorizer:    attachmentAuthorizer{authz},
		Twins:         twins,
		Processor:     attachment.NewPolicyProcessor(meta, runner),
	})
	if err != nil {
		return AttachmentSnapshot{}, err
	}
	return AttachmentSnapshot{Meta: meta, Service: svc, MaxUploadBytes: min(limit, MaxUploadBytes)}, nil
}

// attachmentAuthorizer adapts a [WriteAuthorizer] to the attachment
// service's re-check under the property lock.
type attachmentAuthorizer struct{ authz WriteAuthorizer }

func (a attachmentAuthorizer) AuthorizeAttachmentWrite(ctx context.Context, e *entity.Entity) error {
	decision := a.authz.AuthorizeWrite(ctx, acl.WriteRequest{
		Op:      acl.OpUpdate,
		Subject: acl.NewEntitySubject(e.Type, e.ID, e.Face),
	})
	if decision.Allow {
		return nil
	}
	return &acl.ForbiddenError{Decision: decision}
}

func (d AttachmentDeps) validate() error {
	switch {
	case d.Snapshot == nil:
		return errors.New("mcp: Deps.Attachments.Snapshot is required")
	case d.Uploads == nil:
		return errors.New("mcp: Deps.Attachments.Uploads is required")
	case d.Authorizer == nil:
		return errors.New("mcp: Deps.Attachments.Authorizer is required")
	case d.Audit == nil:
		return errors.New("mcp: Deps.Attachments.Audit is required")
	}
	return nil
}

// attachmentReader is what the read tools need from the attachment service.
type attachmentReader interface {
	List(ctx context.Context, entityID string) ([]attachment.Info, error)
	Open(ctx context.Context, entityID, property, fileName string) (io.ReadCloser, error)
}

// attachmentWriter is what the write tools need from the attachment service.
type attachmentWriter interface {
	WriteAttachment(
		ctx context.Context, e *entity.Entity, propDef metamodel.PropertyDef, propName, rawFileName string, r io.Reader,
	) (*attachment.Result, error)
	DetachFile(
		ctx context.Context, e *entity.Entity, propDef metamodel.PropertyDef, property, fileName string,
	) (string, error)
}

// attachmentHandler serves the four attachment tools.
//
// Every tool reads the entity through the gated store first, so a hidden
// entity, a hidden face and a nonexistent id produce the same answer. The
// same holds one level down: a file property hidden by `visible:` answers
// like an empty one. Attachment bytes are only touched after both checks.
type attachmentHandler struct {
	store GraphReader
	deps  AttachmentDeps
}

func selAttach(h handlerSet) attachmentHandler { return h.attach }

// attachmentNotFound is the answer for a missing file and for a file on a
// property the caller may not see.
func attachmentNotFound(id, property, fileName string) *mcpgo.CallToolResult {
	return errorResult(fmt.Sprintf("attachment not found: %s/%s/%s", id, property, fileName))
}

// gatedEntity reads id through the gated store. A hidden and a nonexistent
// entity both answer "entity not found". Any other read failure is logged and
// answered generically, so an outage is not mistaken for a missing entity.
func (h attachmentHandler) gatedEntity(ctx context.Context, id string) (*entity.Entity, *mcpgo.CallToolResult) {
	e, err := h.store.GetEntity(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound) || (err == nil && e == nil):
		return nil, errorResult("entity not found: " + id)
	case err != nil:
		slog.Warn("mcp: attachment entity read failed", "err", err, "entity", id)
		return nil, errorResult("reading the entity failed")
	}
	return e, nil
}

// fileProperty resolves property to a declared `file` property of e's type.
// Undeclared and non-file properties get an explicit error: the metamodel is
// not a secret.
func fileProperty(meta *metamodel.Metamodel, e *entity.Entity, property string) (metamodel.PropertyDef, error) {
	def, ok := meta.GetEntityDef(e.Type)
	if !ok {
		return metamodel.PropertyDef{}, fmt.Errorf("unknown entity type: %s", e.Type)
	}
	pd, ok := def.Properties[property]
	if !ok {
		return metamodel.PropertyDef{}, fmt.Errorf("property %q not defined for entity type %s", property, e.Type)
	}
	if pd.Type != metamodel.PropertyTypeFile {
		return metamodel.PropertyDef{}, fmt.Errorf("property %q is not a file type (is %s)", property, pd.Type)
	}
	return pd, nil
}

// visibleFileProperty reports whether property is a declared file property of
// e's type that the caller may see. The gated reader lists every property it
// withheld in e.Redacted.
func visibleFileProperty(meta *metamodel.Metamodel, e *entity.Entity, property string) bool {
	_, err := fileProperty(meta, e, property)
	return err == nil && !slices.Contains(e.Redacted, property)
}

func toolListAttachments() *mcpgo.Tool {
	return newTool("list_attachments",
		withDescription("List the files attached to an entity's file-type properties"),
		withString("id", required(), description("Entity ID")),
	)
}

func toolReadAttachment() *mcpgo.Tool {
	return newTool("read_attachment",
		withDescription(fmt.Sprintf("Read one attached file. Raster images are returned as image content, "+
			"UTF-8 text as text, and anything else as a base64 blob. Files over %d bytes are refused.",
			MaxInlineReadBytes)),
		withString("id", required(), description("Entity ID")),
		withString("property", required(), description("File-type property holding the file")),
		withString("file_name", required(), description("File name, as returned by list_attachments")),
	)
}

func toolAttachFile() *mcpgo.Tool {
	return newTool("attach_file",
		withDescription(fmt.Sprintf("Attach a file to an entity's file-type property. On a single-file "+
			"property this replaces the current file; on a multi-file property it adds one, renaming it on a "+
			"name clash. The upload policy (allowed types, scanning, size) applies. Maximum %d bytes.",
			MaxUploadBytes)),
		withString("id", required(), description("Entity ID")),
		withString("property", required(), description("File-type property to attach to")),
		withString("file_name", required(), description("File name, including extension")),
		withString("content", required(), description("File content, base64-encoded (standard alphabet, padded)")),
	)
}

func toolDeleteAttachment() *mcpgo.Tool {
	return newTool("delete_attachment",
		withDescription("Remove a file from an entity's file-type property. Deleting a named file that "+
			"is not there succeeds."),
		withString("id", required(), description("Entity ID")),
		withString("property", required(), description("File-type property holding the file")),
		withString("file_name", description("File to remove. May be omitted when the property holds exactly one file")),
	)
}

// attachmentListing is one row of list_attachments output.
type attachmentListing struct {
	Property    string `json:"property"`
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

func (h attachmentHandler) handleListAttachments(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	args := newToolRequest(request)
	id, err := args.RequireString("id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	id = trimID(id)

	snap, err := h.deps.Snapshot()
	if err != nil {
		slog.Error("mcp: attachment services unavailable", "err", err)
		return errorResult("attachment services unavailable"), nil
	}
	e, failed := h.gatedEntity(ctx, id)
	if failed != nil {
		return failed, nil
	}

	var files attachmentReader = snap.Service
	infos, err := files.List(ctx, e.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		slog.Warn("mcp: list attachments failed", "err", err, "entity", e.ID)
		return errorResult("listing attachments failed"), nil
	}

	out := make([]attachmentListing, 0, len(infos))
	for _, info := range infos {
		if !visibleFileProperty(snap.Meta, e, info.Property) {
			continue
		}
		out = append(out, attachmentListing{
			Property: info.Property, FileName: info.FileName, ContentType: info.ContentType, Size: info.Size,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Property != out[j].Property {
			return out[i].Property < out[j].Property
		}
		return out[i].FileName < out[j].FileName
	})

	text, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	return textResult(string(text)), nil
}

// attachmentArgs reads the id/property/file_name triple common to the tools.
func attachmentArgs(args toolRequest, fileNameRequired bool) (id, property, fileName string, err error) {
	if id, err = args.RequireString("id"); err != nil {
		return "", "", "", err
	}
	if property, err = args.RequireString("property"); err != nil {
		return "", "", "", err
	}
	property = strings.TrimSpace(property)
	if fileNameRequired {
		if fileName, err = args.RequireString("file_name"); err != nil {
			return "", "", "", err
		}
		if strings.TrimSpace(fileName) == "" {
			return "", "", "", errors.New("file_name must not be empty")
		}
	} else if _, present := args.GetArguments()["file_name"]; present {
		// Present but mistyped must not fall back to "", which would remove
		// the property's only file.
		if fileName, err = args.RequireString("file_name"); err != nil {
			return "", "", "", err
		}
	}
	// Stored names never contain a separator (store.NormalizeFileName strips
	// them on write, and the stores look names up rather than join paths), so
	// a name with one cannot match. Refusing it says so plainly.
	if strings.ContainsAny(fileName, "/\\\x00") {
		return "", "", "", errors.New("file_name must be a plain file name, not a path")
	}
	return trimID(id), property, fileName, nil
}

func (h attachmentHandler) handleReadAttachment(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	id, property, fileName, err := attachmentArgs(newToolRequest(request), true)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	snap, err := h.deps.Snapshot()
	if err != nil {
		slog.Error("mcp: attachment services unavailable", "err", err)
		return errorResult("attachment services unavailable"), nil
	}
	e, failed := h.gatedEntity(ctx, id)
	if failed != nil {
		return failed, nil
	}
	if _, err = fileProperty(snap.Meta, e, property); err != nil {
		return errorResult(err.Error()), nil
	}
	if slices.Contains(e.Redacted, property) {
		return attachmentNotFound(id, property, fileName), nil
	}

	var files attachmentReader = snap.Service
	rc, err := files.Open(ctx, e.ID, property, fileName)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return attachmentNotFound(id, property, fileName), nil
		}
		// Store errors may name tables, columns or paths: log, don't echo.
		slog.Warn("mcp: read attachment failed", "err", err, "entity", e.ID, "property", property)
		return errorResult("reading the attachment failed"), nil
	}
	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, MaxInlineReadBytes+1))
	if err != nil {
		slog.Warn("mcp: read attachment failed", "err", err, "entity", e.ID, "property", property)
		return errorResult("reading the attachment failed"), nil
	}
	if len(data) > MaxInlineReadBytes {
		return errorResult(fmt.Sprintf("attachment %s is larger than the %d-byte inline read limit",
			fileName, MaxInlineReadBytes)), nil
	}

	return &mcpgo.CallToolResult{Content: []mcpgo.Content{attachmentContent(e.ID, property, fileName, data)}}, nil
}

// inlineImageTypes are the image types returned as image content. SVG is
// absent on purpose: it is active content, so it goes out as a blob.
var inlineImageTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

// baseType is mimeType without parameters.
func baseType(mimeType string) string {
	base, _, _ := mime.ParseMediaType(mimeType)
	return base
}

// passiveBlobTypes are the MIME types a blob keeps. Every other type,
// including SVG, is labeled application/octet-stream: the label is
// the only signal an MCP client gets, and the web download path's nosniff and
// sandbox headers have no equivalent here.
var passiveBlobTypes = map[string]bool{
	"application/pdf": true, "application/json": true, "text/plain": true, "text/csv": true,
}

// attachmentContent wraps file bytes in the content type a client can use
// best. The type comes from the file extension, like the web download path.
// Go's built-in extension table omits common text types such as .txt and .md
// and relies on the host's mime.types for them, so an unknown extension falls
// back to sniffing the bytes.
func attachmentContent(entityID, property, fileName string, data []byte) mcpgo.Content {
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(fileName)))
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	base, _, _ := mime.ParseMediaType(mimeType)
	switch {
	case inlineImageTypes[base] && baseType(http.DetectContentType(data)) == base:
		// The bytes must agree with the extension: a client passing a
		// mislabelled image to a model API can have the whole request refused.
		return &mcpgo.ImageContent{Data: data, MIMEType: base}
	case strings.HasPrefix(base, "text/") && utf8.Valid(data):
		return &mcpgo.TextContent{Text: string(data)}
	}
	if !passiveBlobTypes[base] {
		base = "application/octet-stream"
	}
	return &mcpgo.EmbeddedResource{Resource: &mcpgo.ResourceContents{
		URI: "rela://attachment/" + url.PathEscape(entityID) + "/" + url.PathEscape(property) + "/" +
			url.PathEscape(fileName),
		MIMEType: base,
		Blob:     data,
	}}
}

// writePreflight runs the checks every write shares, in the web path's order:
// gated read, declared file property, visible property, unlocked entity, and
// the `update` authorization. It returns the entity to write, or the result
// to answer with.
func (h attachmentHandler) writePreflight(
	ctx context.Context, snap AttachmentSnapshot, id, property, fileName string,
) (*entity.Entity, metamodel.PropertyDef, *mcpgo.CallToolResult) {
	e, failed := h.gatedEntity(ctx, id)
	if failed != nil {
		return nil, metamodel.PropertyDef{}, failed
	}
	propDef, err := fileProperty(snap.Meta, e, property)
	if err != nil {
		return nil, metamodel.PropertyDef{}, errorResult(err.Error())
	}
	if slices.Contains(e.Redacted, property) {
		return nil, metamodel.PropertyDef{}, attachmentNotFound(id, property, fileName)
	}
	if e.IsLocked() {
		return nil, metamodel.PropertyDef{}, errorResult(
			"cannot edit an inaccessible entity: file is git-crypt encrypted; run `git-crypt unlock` first")
	}

	decision := h.deps.Authorizer.AuthorizeWrite(ctx, acl.WriteRequest{
		Op:      acl.OpUpdate,
		Subject: acl.NewEntitySubject(e.Type, e.ID, e.Face),
	})
	if !decision.Allow {
		h.deps.Audit.Record(audit.AttachmentWriteDenied(ctx, e.Type, e.ID,
			decision.Reason, decision.RuleKind, decision.RuleID))
		return nil, metamodel.PropertyDef{}, errorResult((&acl.ForbiddenError{Decision: decision}).Error())
	}
	return e, propDef, nil
}

// attachResult is the attach_file output.
type attachResult struct {
	Path     string `json:"path"`
	FileName string `json:"file_name"`
}

func (h attachmentHandler) handleAttachFile(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	args := newToolRequest(request)
	id, property, fileName, err := attachmentArgs(args, true)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	content, err := args.RequireString("content")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	snap, err := h.deps.Snapshot()
	if err != nil {
		slog.Error("mcp: attachment services unavailable", "err", err)
		return errorResult("attachment services unavailable"), nil
	}
	e, propDef, denied := h.writePreflight(ctx, snap, id, property, fileName)
	if denied != nil {
		return denied, nil
	}
	release, admitted := h.deps.Uploads.TryAcquire()
	if !admitted {
		return errorResult("too many uploads in progress; retry shortly"), nil
	}
	defer release()

	// Refuse an oversize upload from its encoded length, before decoding.
	// DecodedLen would round up to a multiple of 3 and refuse a file just at
	// the limit; the exact decoded size is checked below.
	if int64(len(content)) > int64(base64.StdEncoding.EncodedLen(int(snap.MaxUploadBytes))) {
		return errorResult(fmt.Sprintf("attachment too large: maximum size is %d bytes", snap.MaxUploadBytes)), nil
	}
	data, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return errorResult("content is not valid base64: " + err.Error()), nil
	}
	if int64(len(data)) > snap.MaxUploadBytes {
		return errorResult(fmt.Sprintf("attachment too large: maximum size is %d bytes", snap.MaxUploadBytes)), nil
	}

	var files attachmentWriter = snap.Service
	res, err := files.WriteAttachment(ctx, e, propDef, property, fileName, bytes.NewReader(data))
	if err != nil {
		return h.writeError(ctx, e, property, fileName, snap.MaxUploadBytes, err), nil
	}

	out := attachResult{Path: res.Path, FileName: res.FileName}
	text, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	return textResult(string(text)), nil
}

// writeError maps a WriteAttachment failure to a tool error, auditing a
// processor rejection the way the web upload path does.
func (h attachmentHandler) writeError(
	ctx context.Context, e *entity.Entity, property, fileName string, limit int64, err error,
) *mcpgo.CallToolResult {
	switch {
	case errors.Is(err, attachment.ErrRejected):
		reason := attachment.RejectionReason(err)
		h.deps.Audit.Record(audit.AttachmentRejected(ctx, e.Type, e.ID, property, fileName, reason))
		return errorResult("attachment rejected: " + reason)
	case errors.Is(err, attachment.ErrAtCapacity):
		return errorResult(fmt.Sprintf("property %q already holds the maximum number of attachments", property))
	case errors.Is(err, store.ErrAttachmentTooLarge):
		return errorResult(fmt.Sprintf("attachment too large: maximum size is %d bytes", limit))
	}
	return callerError(e.ID, property, err)
}

// callerError answers a failed attachment write. Errors written for the
// caller (an ACL denial, a validation failure, an externally owned property,
// an ambiguous delete) are returned as they are. Anything else may be a store
// error naming tables, columns or paths, so it is logged and answered
// generically.
func callerError(entityID, property string, err error) *mcpgo.CallToolResult {
	var denied *acl.ForbiddenError
	var invalid *entitymanager.ValidationError
	var owned *entitymanager.TheirsWriteError
	if errors.As(err, &denied) || errors.As(err, &invalid) || errors.As(err, &owned) ||
		errors.Is(err, attachment.ErrNoFileToDetach) {

		return errorResult(err.Error())
	}
	slog.Warn("mcp: attachment write failed", "err", err, "entity", entityID, "property", property)
	return errorResult("the attachment write failed")
}

func (h attachmentHandler) handleDeleteAttachment(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	id, property, fileName, err := attachmentArgs(newToolRequest(request), false)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	snap, err := h.deps.Snapshot()
	if err != nil {
		slog.Error("mcp: attachment services unavailable", "err", err)
		return errorResult("attachment services unavailable"), nil
	}
	e, propDef, denied := h.writePreflight(ctx, snap, id, property, fileName)
	if denied != nil {
		return denied, nil
	}

	var files attachmentWriter = snap.Service
	removed, err := files.DetachFile(ctx, e, propDef, property, fileName)
	if err != nil {
		return callerError(e.ID, property, err), nil
	}
	if removed == "" {
		return textResult(fmt.Sprintf("%s was not attached to %s.%s; nothing to remove", fileName, id, property)), nil
	}
	return textResult(fmt.Sprintf("Removed %s from %s.%s", removed, id, property)), nil
}
