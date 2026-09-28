package dataentry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/attachment"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	entityPkg "github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// DefaultMaxAttachmentBytes is the product-wide default cap on a single
// attachment, applied at the HTTP upload ingress. It is generously sized
// for the expected use (screenshots, PDFs, office docs), not media. A
// deployment can override it via dataentryconfig; the store backends also
// enforce their own backstop guard so no path is ever unbounded.
const DefaultMaxAttachmentBytes = 64 << 20 // 64 MiB

// maxAttachmentUploadHeadroom is added to the content cap for the
// multipart envelope (boundaries, headers), mirroring the theme upload
// path's maxLogoUploadBytes margin.
const maxAttachmentUploadHeadroom = 16 * 1024

// handleV1AttachmentRoute dispatches the property-level route
// /api/v1/{plural}/{id}/_attachments/{property}: PUT/POST uploads a file
// (appending up to the property's `max`), GET is not used at this level
// (the entity's `_attachments` map already lists the files; downloads use
// the per-file route below). Writes inherit the entity's `update`
// permission.
func (h *attachmentHandler) handleV1AttachmentRoute(
	w http.ResponseWriter, r *http.Request, typeName, plural, entityID, property string,
) {
	switch r.Method {
	case http.MethodPut, http.MethodPost:
		h.handleV1PutAttachment(w, r, typeName, plural, entityID, property)
	default:
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
	}
}

// handleV1AttachmentFileRoute dispatches the per-file route
// /api/v1/{plural}/{id}/_attachments/{property}/{fileName}: GET downloads
// that file's bytes, DELETE detaches it. Reads inherit the entity's read
// permission, deletes inherit `update`.
func (h *attachmentHandler) handleV1AttachmentFileRoute(
	w http.ResponseWriter, r *http.Request, typeName, entityID, property, fileName string,
) {
	switch r.Method {
	case http.MethodGet:
		h.handleV1GetAttachment(w, r, typeName, entityID, property, fileName)
	case http.MethodDelete:
		h.handleV1DeleteAttachment(w, r, typeName, entityID, property, fileName)
	default:
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
	}
}

// handleV1GetAttachment streams one file attached to a `file`-type
// property of an entity:
//
//	GET /api/v1/{plural}/{id}/_attachments/{property}/{fileName}
//
// Access inherits the owning entity's read permission — a caller who
// cannot read the entity cannot read its attachment. The gate runs
// BEFORE any store lookup so a hidden id and a nonexistent id are
// indistinguishable (404, no body difference, no timing side channel —
// same RR-NGMI invariant as handleV1GetEntity).
//
// The bytes are resolved from (entityID, property, fileName) only; the
// path string stored in the entity's frontmatter is never parsed or
// trusted, so a renamed entity resolves correctly by its current id and
// there is no caller-supplied-path traversal surface. The fileName comes
// from the URL but is only ever a store key (never a filesystem path the
// handler builds), and the store's ValidateFileName rejects separators.
func (h *attachmentHandler) handleV1GetAttachment(
	w http.ResponseWriter, r *http.Request, typeName, entityID, property, fileName string,
) {
	ctx := r.Context()
	s := h.schema()

	// ACL gate first — before any store access (see handleV1GetEntity).
	if !h.gateRead(w, r, typeName, entityID) {
		return
	}

	// The gate above authorized by (type, id), which a `type@face` grant is
	// invisible to, and this reader is the raw store — so the face half is
	// owed here, as on the relations route (TKT-O7R2A1). A file lives on ONE
	// face's property, so serving it discloses that face's content.
	entity, found := h.reader.getEntity(ctx, entityID)
	if !found || entity.Type != typeName || !faceReadable(ctx, entity.Type, entity.Face) {
		writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
		return
	}

	// The property must be a declared `file`-type property on this entity
	// type, and visible to this viewer. Anything else 404s — we never reveal
	// whether some other (or hidden) property or path exists.
	if !isFileProperty(s, typeName, property) || h.isPropertyHidden(ctx, entity, property) {
		writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
		return
	}

	rc, err := h.store.ReadAttachment(ctx, entityID, property, fileName)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
			return
		}
		// Don't leak backend error strings (table/column/path names) —
		// same rationale as writeGateError. Log server-side, 500 client-side.
		slog.Warn("dataentry: read attachment failed",
			"err", err, "entity", entityID, "property", property)
		writeV1Error(w, r, http.StatusInternalServerError, "attachment_read_failed",
			"Reading the attachment failed", "check server logs")
		return
	}
	defer rc.Close()

	setHardenedDownloadHeaders(w.Header(), contentTypeForFilename(fileName), fileName)

	if _, err := io.Copy(w, rc); err != nil {
		// Headers (and likely some bytes) are already written; we can't
		// change the status now. Log and move on.
		slog.Warn("dataentry: streaming attachment failed",
			"err", err, "entity", entityID, "property", property)
	}
}

// handleV1PutAttachment uploads (or replaces) the file attached to a
// `file`-type property:
//
//	PUT|POST /api/v1/{plural}/{id}/_attachments/{property}   (multipart, field "file")
//
// Writing an attachment mutates the owning entity's property, so it
// inherits the entity's `update` permission. The write is authorized
// up front (before any bytes are written) to avoid orphaning a file on a
// late deny — see attachment.Service.Attach's orphan note.
func (h *attachmentHandler) handleV1PutAttachment(
	w http.ResponseWriter, r *http.Request, typeName, plural, entityID, property string,
) {
	r = h.withProvision(r)
	ctx := r.Context()
	// Capture the state snapshot once: the cap the handler enforces and the
	// cap the attachment service enforces must come from the same metamodel
	// (CLAUDE.md "capture state once per operation").
	s := h.schema()
	limit := maxAttachmentBytes(s)

	entity, ok := h.attachmentWritePreflight(w, r, s, typeName, entityID, property)
	if !ok {
		return
	}

	// Take an upload slot before reading the body, which is when the bytes
	// start to use memory and disk.
	releaseUpload, admitted := h.uploads.TryAcquire()
	if !admitted {
		writeAttachmentBusy(w, r, attachment.ErrBusy)
		return
	}
	defer releaseUpload()

	// Cap the request body at ingress: MaxBytesReader makes ParseMultipartForm
	// and the FormFile read fail with *http.MaxBytesError once the limit is
	// crossed, which we map to 413. The store backends enforce their own cap
	// as a backstop, but this rejects an oversize upload before buffering it.
	r.Body = http.MaxBytesReader(w, r.Body, limit+maxAttachmentUploadHeadroom)
	if err := r.ParseMultipartForm(limit + maxAttachmentUploadHeadroom); err != nil {
		if isMaxBytesError(err) {
			writeAttachmentTooLarge(w, r, limit)
			return
		}
		writeV1Error(w, r, http.StatusBadRequest, "invalid_multipart",
			"Invalid multipart body", err.Error())
		return
	}

	// Clean up any on-disk temp files the multipart parser spilled past its
	// in-memory threshold. Go's server removes them when the request body
	// closes, but for a 64 MiB upload path being explicit is cheap insurance.
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeV1Error(w, r, http.StatusBadRequest, "missing_file",
			"Missing form field \"file\"", "")
		return
	}
	defer file.Close()

	// The ingress MaxBytesReader bounds the whole multipart request, but its
	// envelope headroom means it doesn't precisely cap the file *content*.
	// header.Size is the part's declared length — reject early when it's over.
	if header.Size > limit {
		writeAttachmentTooLarge(w, r, limit)
		return
	}

	// Delegate the cap / suffix / write-order / re-stamp policy to the
	// shared attachment service so the HTTP and CLI paths apply identical
	// rules (and the same data-loss-safe attach-then-delete ordering).
	// Cap the reader at the configured limit (which clamps ≤ the store
	// backstop) so an under-declared multipart part still can't exceed it.
	svc, err := h.attachmentService(s)
	if err != nil {
		slog.Warn("dataentry: attachment service unavailable", "err", err)
		writeV1Error(w, r, http.StatusInternalServerError, "attachment_write_failed",
			"Writing the attachment failed", "check server logs")
		return
	}
	propDef := filePropertyDef(s, typeName, property)
	capped := store.CapAttachmentReader(file, limit)
	written, err := svc.WriteAttachment(ctx, entity, propDef, property, header.Filename, capped)
	if err != nil {
		h.auditRejectedUpload(ctx, entity, property, header.Filename, err)
		writeAttachmentWriteError(w, r, limit, err)
		return
	}
	entity = written.Entity

	result := h.serializer.forWire(ctx, entity, h.reader.outgoingRelations(ctx, entity.ID), s.Meta, plural)
	writeV1JSON(w, http.StatusOK, result)
}

// auditRejectedUpload records an upload the attachment processor refused —
// a disallowed MIME type, or a failed/positive scan (TKT-6O8D0L, CONTROL-8-15).
//
// A refused upload is a security-relevant exception: it may be an attempt to
// place a disallowed file type or malware into the project, and "what did this
// user try to upload that they weren't allowed to?" is exactly the forensic
// question the audit log exists to answer. The ACL denial on this same handler
// was already recorded; this closes the gap for the policy denial beside it.
//
// Deliberately the SAME op as the ACL denial (OpDeniedWrite) rather than a new
// one: an operator filtering `op == "denied-write"` should see both kinds of
// refused upload without knowing there are two. The Summary distinguishes them.
//
// Records ONLY ErrRejected. A size cap, an at-capacity property or a transient
// I/O failure are ordinary client or server errors, not security events, and
// auditing them would dilute the signal this record carries.
//
// Lives at the call site rather than inside writeAttachmentWriteError because
// that helper is a package function with neither the audit sink nor the entity
// in scope — and the record needs both.
func (h *attachmentHandler) auditRejectedUpload(
	ctx context.Context, e *entityPkg.Entity, property, fileName string, err error,
) {
	if !errors.Is(err, attachment.ErrRejected) {
		return
	}
	h.audit().Record(audit.AttachmentRejected(ctx, e.Type, e.ID, property, fileName,
		attachment.RejectionReason(err)))
}

// writeAttachmentWriteError maps a Service.WriteAttachment failure to the
// right HTTP status: 413 (size), 409 (at capacity), 503 (another write to
// the property held it too long), 403 (ACL deny from the entity update), or
// 422 (validation / other).
func writeAttachmentWriteError(w http.ResponseWriter, r *http.Request, limit int64, err error) {
	if isAttachmentTooLarge(err) {
		writeAttachmentTooLarge(w, r, limit)
		return
	}
	if writeAttachmentBusy(w, r, err) {
		return
	}
	if errors.Is(err, attachment.ErrAtCapacity) {
		writeV1Error(w, r, http.StatusConflict, "attachment_limit",
			"Property already holds the maximum number of attachments", "")
		return
	}
	// A processor rejection (disallowed MIME type, failed/positive scan) is a
	// client error, not a server fault: 422 with the reason, not a 500.
	if errors.Is(err, attachment.ErrRejected) {
		writeV1Error(w, r, http.StatusUnprocessableEntity, "attachment_rejected",
			"Attachment rejected", attachment.RejectionReason(err))
		return
	}
	if writeForbiddenIfACLDenied(w, err) {
		return
	}
	slog.Warn("dataentry: attachment write failed", "err", err, "path", r.URL.Path)
	writeV1Error(w, r, http.StatusUnprocessableEntity, "validation_failed",
		"Validation failed", err.Error())
}

// writeAttachmentBusy answers 503 with Retry-After when err is
// [attachment.ErrBusy], and reports whether it did.
func writeAttachmentBusy(w http.ResponseWriter, r *http.Request, err error) bool {
	if !errors.Is(err, attachment.ErrBusy) {
		return false
	}
	w.Header().Set("Retry-After", "1")
	writeV1Error(w, r, http.StatusServiceUnavailable, "attachment_busy",
		"Another write to this attachment property is in progress; retry", "")
	return true
}

// attachmentCmdTimeout bounds each external scan/transform command. Generous
// for a single synchronous upload of small-ish files; the runner also caps
// output size at the per-attachment limit.
const attachmentCmdTimeout = 60 * time.Second

// sandboxReporter is the one thing warnIfScanCannotRun needs from the command
// runner. Declared here, at the call site, rather than widening
// [attachment.CommandRunner] — which is deliberately narrow — and so that the
// warning's branches can be exercised with a stub on every platform instead of
// depending on whether the test host happens to have a working sandbox.
type sandboxReporter interface {
	// SandboxErr reports why commands cannot be confined, or nil when they can.
	SandboxErr() error
}

// warnIfScanCannotRun warns when the metamodel configures a virus scan that this
// host cannot actually run, because every upload to a scanned property will then
// be REJECTED (scanning is fail-closed) rather than silently stored unscanned.
//
// The operator otherwise learns this from a 422 on someone's first upload, and
// the confinement line alone does not connect the two facts: it reports the
// sandbox posture without knowing a scan was configured.
//
// Nil: a nil runner means the constructor failed — one of the two states warned
// about, so it is accepted rather than rejected, and buildErr carries the reason.
// The two causes stay distinct in the message: a failed constructor is not a
// sandbox problem, and blaming systemd for it would send the operator to the
// wrong file.
func warnIfScanCannotRun(meta *metamodel.Metamodel, runner sandboxReporter, buildErr error) {
	if !metamodel.NewAttachmentPolicy(meta).HasConfiguredScan() {
		return // nothing would be scanned; a broken sandbox rejects nothing
	}
	// Both messages are spelled out in full rather than composed from a shared
	// prefix: TestSlogMessagesAreConstant requires a literal, since a computed
	// message is the log-injection sink it guards against. The duplication is
	// the price of that check, and it is the safer trade.
	if runner == nil {
		slog.Warn("attachments: a virus scan is configured but the command runner could not "+
			"be built, so EVERY upload to a scanned property will be rejected",
			"err", buildErr, "docs", attachmentSecurityDoc)
		return
	}
	if err := runner.SandboxErr(); err != nil {
		slog.Warn("attachments: a virus scan is configured but no working sandbox is "+
			"available, so EVERY upload to a scanned property will be rejected",
			"err", err, "docs", attachmentSecurityDoc)
	}
}

// attachmentSecurityDoc is the generated guide path, which is what an operator
// reading the log will look for on disk.
const attachmentSecurityDoc = "docs/attachment-security.md"

// probeAttachmentCommands checks, at startup, that every scan/transform binary
// referenced by the metamodel's file properties is resolvable on PATH, warning
// (never failing) for any that are missing — so an operator learns a typo or an
// uninstalled tool at boot rather than on the first upload.
func probeAttachmentCommands(meta *metamodel.Metamodel, runner *attachment.CmdRunner) {
	seen := map[string]bool{}
	probe := func(cmd []string) {
		if len(cmd) == 0 || seen[cmd[0]] {
			return
		}
		seen[cmd[0]] = true
		if err := runner.Probe(cmd); err != nil {
			slog.Warn("attachments: configured command not found", "binary", cmd[0], "err", err)
		}
	}
	if meta.Attachments != nil {
		probe(meta.Attachments.ScanCmd)
	}
	for _, def := range meta.Entities {
		for _, prop := range def.Properties {
			if prop.Type != metamodel.PropertyTypeFile {
				continue
			}
			probe(prop.ScanCmd)
			for _, step := range prop.Transform {
				probe(step.Cmd)
			}
		}
	}
}

// aclAttachmentAuthorizer re-runs the preflight's `update` decision for the
// attachment service, under its property lock. A denial is audited like the
// preflight's and returned as an [acl.ForbiddenError], which the handlers
// already map to 403.
type aclAttachmentAuthorizer struct {
	acl   acl.ACL
	audit audit.Audit
}

func (a aclAttachmentAuthorizer) AuthorizeAttachmentWrite(ctx context.Context, e *entityPkg.Entity) error {
	decision := a.acl.AuthorizeWrite(ctx, translateVerb("update", e.Type, e.ID, e.Face))
	if decision.Allow {
		return nil
	}
	a.audit.Record(audit.AttachmentWriteDenied(ctx, e.Type, e.ID,
		decision.Reason, decision.RuleKind, decision.RuleID))
	return &acl.ForbiddenError{Decision: decision}
}

// attachmentService builds the shared attachment write-policy service from
// the App's dependencies and the GIVEN state snapshot. Cheap (a struct
// wrapper). Takes the snapshot explicitly so the service enforces the same
// metamodel the handler gated on — see "capture state once per operation".
func (h *attachmentHandler) attachmentService(s *Schema) (*attachment.Service, error) {
	return attachment.New(attachment.Deps{
		Store:         h.store,
		Meta:          s.Meta,
		EntityManager: h.manager,
		Locker:        h.locker,
		Authorizer:    aclAttachmentAuthorizer{acl: h.acl(), audit: h.audit()},
		// Native MIME allowlist + (when a command runner is wired) scan/
		// transform. h.runner is nil out-of-box → MIME validation only.
		Processor: attachment.NewPolicyProcessor(s.Meta, h.runner()),
	})
}

// filePropertyDef returns the metamodel def for a property from the given
// snapshot (callers gate on isFileProperty first, so a file def is expected).
func filePropertyDef(s *Schema, typeName, property string) metamodel.PropertyDef {
	if def, ok := s.Meta.GetEntityDef(typeName); ok {
		return def.Properties[property]
	}
	return metamodel.PropertyDef{}
}

// handleV1DeleteAttachment detaches one file from a `file`-type property:
//
//	DELETE /api/v1/{plural}/{id}/_attachments/{property}/{fileName}
//
// Inherits the entity's `update` permission (a deny is handled up front in
// attachmentWritePreflight, before anything is touched). The bytes are
// removed, then the property is re-stamped from the store's remaining
// files and persisted. Idempotent: deleting a missing file still
// re-stamps and returns 204.
func (h *attachmentHandler) handleV1DeleteAttachment(
	w http.ResponseWriter, r *http.Request, typeName, entityID, property, fileName string,
) {
	r = h.withProvision(r)
	ctx := r.Context()
	s := h.schema()

	entity, ok := h.attachmentWritePreflight(w, r, s, typeName, entityID, property)
	if !ok {
		return
	}

	svc, err := h.attachmentService(s)
	if err != nil {
		slog.Warn("dataentry: attachment service unavailable", "err", err)
		writeV1Error(w, r, http.StatusInternalServerError, "attachment_delete_failed",
			"Deleting the attachment failed", "check server logs")
		return
	}
	propDef := filePropertyDef(s, typeName, property)
	if err := svc.DeleteAttachment(ctx, entity, propDef, property, fileName); err != nil {
		if writeAttachmentBusy(w, r, err) || writeForbiddenIfACLDenied(w, err) {
			return
		}
		slog.Warn("dataentry: delete attachment failed", "err", err, "path", r.URL.Path)
		writeV1Error(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Validation failed", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// attachmentWritePreflight runs the shared front matter for an attachment
// write: read-gate (uniform 404), load the entity, validate the property
// is a declared `file` type, reject a locked (inaccessible) entity, and
// authorize the `update` write UP FRONT so a deny never reaches the store.
// Returns the loaded entity and true when the write may proceed.
func (h *attachmentHandler) attachmentWritePreflight(
	w http.ResponseWriter, r *http.Request, s *Schema, typeName, entityID, property string,
) (*entityPkg.Entity, bool) {
	ctx := r.Context()

	// Read-gate first: a hidden or nonexistent id yields a uniform 404
	// (RR-NGMI), the same as the read path.
	if !h.gateRead(w, r, typeName, entityID) {
		return nil, false
	}

	entity, found := h.reader.getEntity(ctx, entityID)
	if !found || entity.Type != typeName {
		writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
		return nil, false
	}
	if !isFileProperty(s, typeName, property) || h.isPropertyHidden(ctx, entity, property) {
		writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
		return nil, false
	}
	if entity.IsLocked() {
		writeV1Error(w, r, http.StatusUnprocessableEntity, "encrypted_inaccessible",
			"Cannot edit an inaccessible entity", "File is git-crypt encrypted; run `git-crypt unlock` first.")
		return nil, false
	}

	// Authorize the `update` write up front (mirrors authorizeConflictResolve)
	// so an ACL deny happens before any bytes are written to the store.
	decision := h.acl().AuthorizeWrite(ctx, translateVerb("update", entity.Type, entity.ID, entity.Face))
	if !decision.Allow {
		h.audit().Record(audit.AttachmentWriteDenied(ctx, entity.Type, entity.ID,
			decision.Reason, decision.RuleKind, decision.RuleID))
		writeForbiddenIfACLDenied(w, &acl.ForbiddenError{Decision: decision})
		return nil, false
	}
	return entity, true
}

// maxAttachmentBytes returns the effective per-attachment cap for the
// given app state: the configured override (or the product default),
// clamped to the store backstop. Clamping guarantees the 413 detail never
// promises a ceiling higher than the store will actually accept — a
// misconfigured `max_attachment_bytes` above store.MaxAttachmentBytes
// can't make the error message lie.
func maxAttachmentBytes(s *Schema) int64 {
	limit := int64(DefaultMaxAttachmentBytes)
	if n := s.Cfg.App.MaxAttachmentBytes; n > 0 {
		limit = n
	}
	if limit > store.MaxAttachmentBytes {
		limit = store.MaxAttachmentBytes
	}
	return limit
}

// writeAttachmentTooLarge emits the 413 problem+json body for an
// over-cap upload.
func writeAttachmentTooLarge(w http.ResponseWriter, r *http.Request, limit int64) {
	writeV1Error(w, r, http.StatusRequestEntityTooLarge, "attachment_too_large",
		"Attachment too large", fmt.Sprintf("maximum size is %d bytes", limit))
}

// isMaxBytesError reports whether err is (or wraps) an *http.MaxBytesError.
func isMaxBytesError(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}

// isAttachmentTooLarge reports whether a store AttachFile error is the
// backend's own size-cap rejection (the per-store backstop).
func isAttachmentTooLarge(err error) bool {
	return errors.Is(err, store.ErrAttachmentTooLarge)
}

// isFileProperty reports whether property is a declared `file`-type
// property on the given entity type.
func isFileProperty(s *Schema, typeName, property string) bool {
	def, ok := s.Meta.GetEntityDef(typeName)
	if !ok {
		return false
	}
	pd, ok := def.Properties[property]
	return ok && pd.Type == metamodel.PropertyTypeFile
}

// isPropertyHidden reports whether property is hidden from the current
// viewer by field-visibility policy. Attachment read/write endpoints 404
// on a hidden property so its files (and download URLs) never leak — the
// same boundary stripHiddenProperties / `_fields` enforce on the entity
// response.
func (h *attachmentHandler) isPropertyHidden(ctx context.Context, e *entityPkg.Entity, property string) bool {
	return !h.fields().FieldVerdicts(ctx, e).IsVisible(property)
}

// contentTypeForFilename infers a MIME type from a filename extension,
// defaulting to application/octet-stream (browsers prompt a download for
// that, the right default for an unknown type). The store does not
// persist content type on every backend, so we always derive it here.
func contentTypeForFilename(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return "application/octet-stream"
	}
	if mt := mime.TypeByExtension(ext); mt != "" {
		return mt
	}
	return "application/octet-stream"
}

// setHardenedDownloadHeaders is the shared hardening for endpoints that serve
// user-influenced bytes as a download (attachments, view exports): force a
// download rather than inline rendering (Content-Disposition: attachment),
// never let the browser sniff a different (e.g. text/html) type, and sandbox
// any active content — so an SVG/HTML payload can't execute as stored XSS in
// the app's origin. The filename is sanitized with [safeAttachmentFilename].
func setHardenedDownloadHeaders(hdr http.Header, contentType, filename string) {
	hdr.Set("Content-Type", contentType)
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	hdr.Set("Content-Disposition", `attachment; filename="`+safeAttachmentFilename(filename)+`"`)
}

// safeAttachmentFilename strips characters that could break out of the
// Content-Disposition filename token (quotes, control chars, path
// separators) while preserving the extension so the downloaded file
// keeps a sensible name. The stem and extension are sanitized separately
// with the theme filename allowlist (which collapses `.`), then rejoined
// with a single dot.
func safeAttachmentFilename(name string) string {
	base := filepath.Base(name)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)

	cleanStem := strings.Trim(unsafeFilenameRe.ReplaceAllString(stem, "_"), "_")
	if cleanStem == "" {
		cleanStem = "attachment"
	}
	cleanExt := strings.Trim(unsafeFilenameRe.ReplaceAllString(strings.TrimPrefix(ext, "."), "_"), "_")
	if cleanExt == "" {
		return cleanStem
	}
	return cleanStem + "." + cleanExt
}
