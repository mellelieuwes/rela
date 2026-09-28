package dataentry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/comments"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// commentsPathPrefix is the reserved route segment for the commentary layer.
const commentsPathPrefix = "/api/v1/_comments/"

// handleV1Comments serves the commentary layer.
//
//	GET    /api/v1/_comments/{type}/{id}              → list a target's comments
//	POST   /api/v1/_comments/{type}/{id}              → add one
//	PATCH  /api/v1/_comments/{type}/{id}/{commentID}  → edit body / resolve
//	DELETE /api/v1/_comments/{type}/{id}/{commentID}  → remove one
//	POST   /api/v1/_comments/{type}/{id}/{commentID}/accept → apply its suggestion
//
// # Gating
//
// Every request resolves TWO things before it touches storage: the target's own
// read verdict, and the relevant `comment:*` permission for that target. The
// read verdict is the floor — a principal who cannot read an entity cannot
// learn anything about its comments, however the comment grants read, because
// otherwise a thread becomes an existence oracle for entities the principal is
// denied.
//
// A denial is reported as an indistinguishable 404 when it would otherwise
// confirm the target exists, and as a 403 naming the missing permission when
// the caller has already proven it can read the target. That split follows the
// repo's rule that entity existence is secret but a config-declared capability
// is not: telling an authorized reader which permission it lacks is the answer
// that helps the operator debug.
//
// # Disabled is absent, not forbidden
//
// With no `comments:` block the service is nil and every route 404s, so a
// project that never enables commenting is indistinguishable from one built
// before the feature existed.
func (h *commentsHandler) handleV1Comments(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		// Not a capability gap worth explaining (unlike version history, which
		// is backend-dependent): commenting is off because this project did
		// not ask for it, so the route simply does not exist.
		writeV1Error(w, r, http.StatusNotFound, "not_found", "Not found", "")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, commentsPathPrefix)
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		writeV1Error(w, r, http.StatusBadRequest, "invalid_path",
			"Path must be /_comments/{type}/{id}[/{commentID}]", "")
		return
	}
	typeName, entityID := parts[0], parts[1]

	// Validate the path segments before they reach storage. The file backend
	// guards itself too, but refusing here keeps an unsafe id from reaching any
	// backend at all, and gives a 400 rather than an opaque store error.
	//
	// The id segment is an ADDRESS (`ID` or `ID@face`) and is parsed here,
	// once, into the bare id and the face (BUG-R1PQY9). Handing the raw
	// segment to a bare-id reader resolves on memstore/fsstore only because
	// their index key is the same string, and matches nothing on the database
	// backends or under a query-shaped read grant.
	ref, refOK := parseEntityRef(entityID)
	if !isSafePathSegment(typeName) || !isSafeStateRefSegment(entityID) || !refOK {
		writeV1Error(w, r, http.StatusBadRequest, "invalid_path",
			"Invalid entity type or id", "")
		return
	}

	// Commentability is metamodel config, not a secret: an operator debugging
	// a missing comment box needs to be told the type is not enabled.
	if !h.commentPolicy().Commentable(typeName) {
		writeV1Error(w, r, http.StatusBadRequest, "comments_not_enabled",
			"Comments are not enabled for this entity type", "")
		return
	}

	addr := commentAddress{typeName: typeName, ref: ref}

	switch {
	case len(parts) == 3 && parts[2] == "resolve":
		h.commentResolveCheck(w, r, addr)
	case len(parts) == 2:
		h.commentCollection(w, r, addr)
	case len(parts) == 3 && parts[2] != "":
		h.commentItem(w, r, addr, parts[2])
	case len(parts) == 4 && parts[2] != "" && parts[3] == "accept":
		h.commentAccept(w, r, addr, parts[2])
	default:
		writeV1Error(w, r, http.StatusBadRequest, "invalid_path",
			"Path must be /_comments/{type}/{id}[/{commentID}]", "")
	}
}

// commentCollection handles the target-level routes.
func (h *commentsHandler) commentCollection(
	w http.ResponseWriter, r *http.Request, addr commentAddress,
) {
	switch r.Method {
	case http.MethodGet:
		h.listComments(w, r, addr)
	case http.MethodPost:
		if !h.refuseIfReadOnly(w, r) {
			h.addComment(w, r, addr)
		}
	case http.MethodOptions:
		w.Header().Set("Allow", "GET, POST, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, POST, OPTIONS")
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
	}
}

// commentItem handles the single-comment routes.
func (h *commentsHandler) commentItem(
	w http.ResponseWriter, r *http.Request, addr commentAddress, commentID string,
) {
	switch r.Method {
	case http.MethodPatch:
		if !h.refuseIfReadOnly(w, r) {
			h.updateComment(w, r, addr, commentID)
		}
	case http.MethodDelete:
		if !h.refuseIfReadOnly(w, r) {
			h.deleteComment(w, r, addr, commentID)
		}
	case http.MethodOptions:
		w.Header().Set("Allow", "PATCH, DELETE, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "PATCH, DELETE, OPTIONS")
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
	}
}

// resolveCheckRequest asks whether a selection could be anchored.
type resolveCheckRequest struct {
	Quote  string `json:"quote"`
	Prefix string `json:"quote_prefix"`
	Suffix string `json:"quote_suffix"`
}

// resolveCheckResponse answers it, with a reason when it could not.
type resolveCheckResponse struct {
	Anchorable bool   `json:"anchorable"`
	Reason     string `json:"reason,omitempty"`
	// SourceQuote is the markdown SOURCE the selection maps to, which is what
	// a suggested replacement replaces. A client pre-filling a suggestion must
	// start from this, not from the rendered selection: the two differ
	// wherever markup sits inside the range, and editing the rendered form
	// would drop that markup on accept. It is a substring of a body the caller
	// may already read.
	SourceQuote string `json:"source_quote,omitempty"`
	// Suggestable reports whether a replacement for this selection would be
	// accepted, so a client can hide the option instead of letting someone
	// type a suggestion that is refused on submit.
	Suggestable bool `json:"suggestable,omitempty"`
}

// commentResolveCheck reports whether a selection can be anchored, WITHOUT
// creating anything.
//
// Exists because the alternative is worse: a user selects text, writes a
// comment, and only then learns it cannot be saved. Some selections genuinely
// cannot anchor — one spanning table cells has no contiguous source range —
// and the UI needs to know before it offers the affordance.
//
// Gated exactly like a create: it reveals whether a string appears in the body,
// which is a read of the body, so `comment:add` plus the target read verdict.
func (h *commentsHandler) commentResolveCheck(
	w http.ResponseWriter, r *http.Request, addr commentAddress,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}
	target, ent, ok := h.gateCommentTarget(w, r, addr)
	if !ok {
		return
	}
	if !h.commentAuthorizer().CanAdd(r.Context(), target) {
		writeV1Error(w, r, http.StatusForbidden, "forbidden",
			"Adding a comment requires the comment:add permission", "")
		return
	}

	var req resolveCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeV1Error(w, r, http.StatusBadRequest, "invalid_body", "Malformed JSON body", "")
		return
	}

	text, err := buildTextAnchor(ent, req.Quote, req.Prefix, req.Suffix)
	if err != nil {
		// A failure here is the ANSWER, not an error: the caller asked whether
		// this selection works, and "no, because…" is a successful reply.
		writeV1JSON(w, http.StatusOK, resolveCheckResponse{Reason: err.Error()})
		return
	}
	writeV1JSON(w, http.StatusOK, resolveCheckResponse{
		Anchorable: true, SourceQuote: text.Quote, Suggestable: comments.Replaceable(text),
	})
}

// commentListResponse is the list wire shape.
//
// An object rather than a bare array so the response can gain fields (a
// truncation flag, an unresolved count) without a breaking change.
type commentListResponse struct {
	Comments []commentWire `json:"comments"`
}

// commentWire is one comment on the wire.
//
// Deliberately NOT the storage type: `detached` is computed per request from
// the live entity, and re-using the stored struct would eventually tempt
// someone to persist it.
// anchorWire is a comment's anchor on the wire.
//
// Named rather than inlined because it is built at three sites and carries the
// stage-2 text descriptors; an anonymous struct repeated four times drifts.
type anchorWire struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	// Quote is the anchored text, echoed for a text anchor so a client can
	// show WHAT was commented on even when the range no longer resolves.
	Quote string `json:"quote,omitempty"`
	// Start and End are byte offsets into the entity body, resolved fresh on
	// every read. Present only for a text anchor that located successfully.
	//
	// They are NOT stored: an offset is invalidated by any edit earlier in the
	// body, which is the entire reason the descriptors exist. A client must
	// slice with these, never with the quote's length.
	Start *int `json:"start,omitempty"`
	End   *int `json:"end,omitempty"`
	// Confidence is the resolver's score for a text anchor (0-1).
	Confidence float64 `json:"confidence,omitempty"`
	// Uncertain marks the middle band: located, but far enough from an exact
	// match that the UI should say the text may have moved.
	Uncertain bool `json:"uncertain,omitempty"`
	// Replacement is the suggested substitute for Quote. A pointer so a
	// deletion suggestion ("") is distinguishable from none.
	Replacement *string `json:"replacement,omitempty"`
}

type commentWire struct {
	ID        string     `json:"id"`
	Author    string     `json:"author"`
	CreatedAt string     `json:"created_at"`
	Anchor    anchorWire `json:"anchor"`
	Body      string     `json:"body"`
	Resolved  bool       `json:"resolved"`
	// Detached reports that the anchor no longer names anything on the target.
	// A soft condition per DEC-HWZHA: the comment is still returned and still
	// readable, flagged so the UI can show it as orphaned rather than pretend
	// it points somewhere.
	Detached bool `json:"detached,omitempty"`
	// Editable and Deletable are UI hints mirroring `_actions`: the server
	// re-authorizes every write, so a client that ignores them gains nothing.
	Editable  bool `json:"editable"`
	Deletable bool `json:"deletable"`
	// Acceptable reports that the suggestion could be applied to the current
	// body: it has one, the comment is open, and the quote still locates
	// unambiguously. It does NOT include the entity update right; a client
	// combines it with the entity's `_actions.update`, and the accept route
	// re-checks everything.
	Acceptable bool `json:"acceptable,omitempty"`
}

func (h *commentsHandler) listComments(
	w http.ResponseWriter, r *http.Request, addr commentAddress,
) {
	ctx := r.Context()
	auth := h.commentAuthorizer()

	// Read floor first: a caller who cannot read the target — or a target that
	// does not exist — gets the same 404, so comments never confirm existence.
	target, ent, ok := h.gateCommentTarget(w, r, addr)
	if !ok {
		return
	}
	if !auth.CanRead(ctx, target) {
		writeV1Error(w, r, http.StatusForbidden, "forbidden",
			"Reading comments requires the comment:read permission", "")
		return
	}

	list, err := h.svc.List(ctx, target)
	if err != nil {
		writeV1Error(w, r, http.StatusInternalServerError, "comments_failed",
			"Could not read comments", "")
		return
	}

	user := principal.From(ctx).User
	anchors := h.liveAnchors(target, ent)
	out := make([]commentWire, 0, len(list))
	for _, c := range list {
		anchor, detached := resolveAnchor(anchors, c.Anchor)
		out = append(out, commentWire{
			ID:         c.ID,
			Author:     c.Author,
			CreatedAt:  c.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			Anchor:     anchor,
			Body:       c.Body,
			Resolved:   c.Resolved,
			Detached:   detached,
			Editable:   auth.CanUpdate(ctx, target, c, user),
			Deletable:  auth.CanDelete(ctx, target, c, user),
			Acceptable: !c.Resolved && anchors.loaded && comments.Acceptable(anchors.body, c.Anchor),
		})
	}

	writeV1JSON(w, http.StatusOK, commentListResponse{Comments: out})
}

// addCommentRequest is the create wire shape.
//
// It carries no author, id or timestamp: those are server-written, so the wire
// type cannot express them rather than accepting and discarding them.
type addCommentRequest struct {
	Anchor struct {
		Kind string `json:"kind"`
		Ref  string `json:"ref"`
		// Quote is the selected body text, for a `text` anchor.
		//
		// The client sends RENDERED text, not offsets: it renders markdown, so
		// its coordinates are not the source's. The server maps the quote back
		// to source through the AST and derives the stored descriptors itself,
		// so a caller cannot persist context that disagrees with the entity.
		Quote string `json:"quote"`
		// QuotePrefix and QuoteSuffix are the rendered text immediately around
		// the selection. They select among REAL occurrences of the quote and
		// can never introduce a location, since the quote must still be found.
		//
		// Without them a repeated quote resolves to the first occurrence — how
		// a comment on "Geordend" came to highlight "Ongeordend".
		QuotePrefix string `json:"quote_prefix"`
		QuoteSuffix string `json:"quote_suffix"`
		// Replacement suggests substitute text for the quote (text anchors
		// only). Null or absent means no suggestion; "" suggests deletion.
		Replacement *string `json:"replacement"`
	} `json:"anchor"`
	Body string `json:"body"`
}

func (h *commentsHandler) addComment(
	w http.ResponseWriter, r *http.Request, addr commentAddress,
) {
	ctx := r.Context()

	target, ent, ok := h.gateCommentTarget(w, r, addr)
	if !ok {
		return
	}
	if !h.commentAuthorizer().CanAdd(ctx, target) {
		writeV1Error(w, r, http.StatusForbidden, "forbidden",
			"Adding a comment requires the comment:add permission", "")
		return
	}

	var req addCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeV1Error(w, r, http.StatusBadRequest, "invalid_body", "Malformed JSON body", "")
		return
	}

	anchor := comments.Anchor{
		Kind:        comments.AnchorKind(req.Anchor.Kind),
		Ref:         req.Anchor.Ref,
		Replacement: req.Anchor.Replacement,
	}
	if anchor.Kind == comments.AnchorText {
		text, aerr := buildTextAnchor(ent, req.Anchor.Quote, req.Anchor.QuotePrefix, req.Anchor.QuoteSuffix)
		if aerr != nil {
			writeV1Error(w, r, http.StatusBadRequest, "invalid_comment", aerr.Error(), "")
			return
		}
		anchor.Text = text
	}

	created, err := h.svc.Add(ctx, target, comments.AddRequest{
		Anchor: anchor,
		Body:   req.Body,
	})
	if err != nil {
		writeCommentError(w, r, err)
		return
	}

	writeV1JSON(w, http.StatusCreated, commentWire{
		ID:        created.ID,
		Author:    created.Author,
		CreatedAt: created.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		Anchor:    newAnchorWire(created.Anchor),
		Body:      created.Body,
		Resolved:  created.Resolved,
		Editable:  true,
		Deletable: true,
	})
}

// updateCommentRequest carries the two mutable fields.
//
// Pointers so "not supplied" is distinguishable from "set to the zero value" —
// resolving a comment must not require echoing its body back.
type updateCommentRequest struct {
	Body     *string `json:"body"`
	Resolved *bool   `json:"resolved"`
}

func (h *commentsHandler) updateComment(
	w http.ResponseWriter, r *http.Request, addr commentAddress, commentID string,
) {
	ctx := r.Context()

	target, existing, ok := h.gateCommentMutation(w, r, addr, commentID, mutationUpdate)
	if !ok {
		return
	}

	var req updateCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeV1Error(w, r, http.StatusBadRequest, "invalid_body", "Malformed JSON body", "")
		return
	}

	body := existing.Body
	if req.Body != nil {
		body = *req.Body
	}
	resolved := existing.Resolved
	if req.Resolved != nil {
		resolved = *req.Resolved
	}

	if err := h.svc.Update(ctx, target, commentID, body, resolved); err != nil {
		writeCommentError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *commentsHandler) deleteComment(
	w http.ResponseWriter, r *http.Request, addr commentAddress, commentID string,
) {
	target, _, ok := h.gateCommentMutation(w, r, addr, commentID, mutationDelete)
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), target, commentID); err != nil {
		writeCommentError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mutationKind distinguishes the two mutating routes for gateCommentMutation.
type mutationKind int

const (
	mutationUpdate mutationKind = iota
	mutationDelete
)

// gateCommentMutation resolves the target, loads the comment, and authorizes
// the mutation — returning the loaded comment so the caller does not re-read it.
//
// The comment is loaded BEFORE the permission check because ownership is a
// property of the stored record: "may I edit this?" cannot be answered without
// knowing who wrote it. A missing comment is a 404 rather than a 403, since by
// this point the caller has proven it can read the target.
func (h *commentsHandler) gateCommentMutation(
	w http.ResponseWriter, r *http.Request,
	addr commentAddress, commentID string, kind mutationKind,
) (comments.Target, comments.Comment, bool) {
	ctx := r.Context()

	target, _, ok := h.gateCommentTarget(w, r, addr)
	if !ok {
		return target, comments.Comment{}, false
	}

	existing, err := h.svc.Get(ctx, target, commentID)
	if err != nil {
		if errors.Is(err, comments.ErrNotFound) {
			writeV1Error(w, r, http.StatusNotFound, "not_found", "Comment not found", "")
			return target, comments.Comment{}, false
		}
		writeV1Error(w, r, http.StatusInternalServerError, "comments_failed",
			"Could not read comments", "")
		return target, comments.Comment{}, false
	}

	user := principal.From(ctx).User
	auth := h.commentAuthorizer()
	allowed := auth.CanUpdate(ctx, target, existing, user)
	perms := "comment:update-own or comment:update-any"
	if kind == mutationDelete {
		allowed = auth.CanDelete(ctx, target, existing, user)
		perms = "comment:delete-own or comment:delete-any"
	}
	if !allowed {
		writeV1Error(w, r, http.StatusForbidden, "forbidden",
			"This action requires "+perms, "")
		return target, comments.Comment{}, false
	}
	return target, existing, true
}

// commentAddress is the parsed target of a comments request: the entity type
// and its address, face included.
type commentAddress struct {
	typeName string
	ref      entityRef
}

// gateCommentTarget resolves the target entity, reporting whether the request
// may proceed.
//
// Both "you may not read this" and "this does not exist" answer with the same
// 404. Checking EXISTENCE as well as the read verdict matters: the read gate
// answers "may this principal read this type", which a nonexistent id passes —
// so gating on the verdict alone would let a comment route confirm that an
// arbitrary id is absent, and (worse) accept comments filed against entities
// that were never there.
//
// The resolved entity is returned so the anchor code reads the SAME row the
// gate admitted. Re-reading it by id would lose the face: the thread is keyed
// by (id, face), and a bare-id read returns whichever face the world selects,
// which is how a draft comment came to be anchored in the published body.
func (h *commentsHandler) gateCommentTarget(
	w http.ResponseWriter, r *http.Request, addr commentAddress,
) (comments.Target, *entity.Entity, bool) {
	target := comments.Target{Type: addr.typeName, ID: addr.ref.ID, Face: addr.ref.Face}
	ent, found, err := h.visibleReader.getVisibleRef(r.Context(), addr.typeName, addr.ref)
	if err != nil {
		writeGateError(w, r, err)
		return target, nil, false
	}
	if !found {
		writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
		return target, nil, false
	}
	// Scope the thread to the face this request actually resolved to
	// (FEAT-9CD2MX). Taken from the RESOLVED entity rather than the address:
	// a bare id under a world resolves to whichever face that world selects,
	// and the thread must match the content on screen.
	target.ID = ent.ID
	target.Face = ent.Face
	return target, ent, true
}

// refuseIfReadOnly denies a comment write on a read-only instance, reporting
// whether it did so.
//
// Checked before any other gate: ReadOnlyACL is a process-wide refusal an
// operator wires for "absolute confidence no writes happen", so it must not be
// satisfiable by any permission in any acl.yaml. Comments bypass the
// entitymanager, so ReadOnlyACL's blanket write deny never sees them — this is
// where that guarantee is kept for the commentary layer.
func (h *commentsHandler) refuseIfReadOnly(w http.ResponseWriter, r *http.Request) bool {
	if h.commentWritesPermitted() {
		return false
	}
	writeV1Error(w, r, http.StatusForbidden, "forbidden",
		"this rela instance is configured read-only", "")
	return true
}

// isSafeStateRefSegment reports whether a path segment is a usable entity
// reference, with or without a face ("TKT-1", "TKT-1@draft").
//
// A separate check rather than widening [isSafePathSegment]: that helper guards
// every other route, and '@' is only meaningful where a face may legitimately
// appear. Both halves are validated on their own terms, so nothing unsafe
// reaches storage — the face half by the entity package's own grammar, which
// is narrower than the id's.
func isSafeStateRefSegment(s string) bool {
	base, face, found := strings.Cut(s, entity.StateRefSeparator)
	if !isSafePathSegment(base) {
		return false
	}
	if !found {
		return true
	}
	// entity.ParseFace is the only sanctioned constructor from external input,
	// and rejects anything outside the declared face grammar.
	_, err := entity.ParseFace(face)
	return err == nil
}

// newAnchorWire projects a stored anchor onto the wire WITHOUT resolving it.
//
// Used on create, where the client already knows where it selected: a text
// anchor's range is resolved per read, so echoing one here would be a second
// code path producing the same number.
func newAnchorWire(a comments.Anchor) anchorWire {
	out := anchorWire{Kind: string(a.Kind), Ref: a.Ref, Replacement: a.Replacement}
	if a.Text != nil {
		out.Quote = a.Text.Quote
	}
	return out
}

// resolveAnchor projects a stored anchor onto the wire and reports whether it
// is currently detached.
//
// Per kind:
//   - property: detached when the name is gone from the entity.
//   - section: never flagged. A section ref resolves against the view config,
//     not the entity, so this per-entity view cannot tell a missing section
//     from a present one, and guessing would badge every section comment.
//   - text: resolved against the body, yielding a fresh range plus a
//     confidence band. Detached when the quote can no longer be located.
//
// An entity that could not be loaded flags nothing: failing to read must not
// make every comment look orphaned.
func resolveAnchor(ctx anchorContext, a comments.Anchor) (anchorWire, bool) {
	out := newAnchorWire(a)
	if !ctx.loaded {
		return out, false
	}

	switch a.Kind {
	case comments.AnchorProperty:
		return out, !ctx.properties[a.Ref]

	case comments.AnchorText:
		m := comments.ResolveText(ctx.body, a.Text)
		if m.Detached {
			return out, true
		}
		start, end := m.Start, m.End
		out.Start, out.End = &start, &end
		out.Confidence = m.Confidence
		out.Uncertain = m.Uncertain
		return out, false

	default:
		return out, false
	}
}

// writeCommentError maps a service error to a response.
//
// Validation failures are 400s (malformed wire format, per DEC-HWZHA's first
// class) and everything unrecognized is a 500 with no detail — a storage error
// message can carry a path, and a path is not the caller's business.
func writeCommentError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, comments.ErrNotFound):
		writeV1Error(w, r, http.StatusNotFound, "not_found", "Comment not found", "")
	case errors.Is(err, comments.ErrEmptyBody),
		errors.Is(err, comments.ErrBodyTooLong),
		errors.Is(err, comments.ErrBodyControlChars),
		errors.Is(err, comments.ErrInvalidAnchor),
		errors.Is(err, comments.ErrInvalidReplacement):
		writeV1Error(w, r, http.StatusBadRequest, "invalid_comment", err.Error(), "")
	case errors.Is(err, comments.ErrTooManyComments):
		writeV1Error(w, r, http.StatusConflict, "too_many_comments", err.Error(), "")
	case errors.Is(err, comments.ErrUnknownAuthor):
		// The caller is authenticated enough to reach here but has no
		// resolvable identity to attribute the comment to.
		writeV1Error(w, r, http.StatusForbidden, "forbidden",
			"Comments require an identified author", "")
	default:
		writeV1Error(w, r, http.StatusInternalServerError, "comments_failed",
			"Could not save the comment", "")
	}
}

// authorizeBodyWrite pre-checks the `update` write the accept will perform,
// recording a denial the way the attachment handler does. It writes the 403
// and returns false on a deny.
func (h *commentsHandler) authorizeBodyWrite(ctx context.Context, w http.ResponseWriter, e *entity.Entity) bool {
	decision := h.acl().AuthorizeWrite(ctx, translateVerb("update", e.Type, e.ID, e.Face))
	if decision.Allow {
		return true
	}
	h.audit().Record(audit.Record{
		Time:        time.Now().UTC(),
		Op:          audit.OpDeniedWrite,
		Subject:     &audit.Subject{Kind: "entity", Type: e.Type, ID: e.ID},
		Principal:   principal.From(ctx),
		TriggeredBy: audit.TriggeredByFrom(ctx),
		Summary: fmt.Sprintf("denied: %s (rule_kind=%s rule_id=%s op=accept-suggestion)",
			decision.Reason, decision.RuleKind, decision.RuleID),
	})
	writeForbiddenIfACLDenied(w, &acl.ForbiddenError{Decision: decision})
	return false
}

// openSuggestion loads the comment an accept applies. It writes the error and
// returns false unless the comment exists, is readable, suggests a
// replacement and is still open.
func (h *commentsHandler) openSuggestion(
	w http.ResponseWriter, r *http.Request, target comments.Target, commentID string,
) (comments.Comment, bool) {
	ctx := r.Context()
	// Before the lookup, so a principal without comment:read cannot tell an
	// existing comment id (403) from an unknown one (404).
	if !h.commentAuthorizer().CanRead(ctx, target) {
		writeV1Error(w, r, http.StatusForbidden, "forbidden",
			"Accepting a suggestion requires the comment:read permission", "")
		return comments.Comment{}, false
	}
	existing, err := h.svc.Get(ctx, target, commentID)
	if err != nil {
		writeCommentError(w, r, err)
		return comments.Comment{}, false
	}
	if existing.Anchor.Replacement == nil {
		writeV1Error(w, r, http.StatusConflict, "no_suggestion",
			"This comment does not suggest a replacement", "")
		return comments.Comment{}, false
	}
	if existing.Resolved {
		writeV1Error(w, r, http.StatusConflict, "comment_resolved",
			"This comment is already resolved", "")
		return comments.Comment{}, false
	}
	return existing, true
}

// acceptResponse is the accept route's success body.
//
// Content is the body the write submitted, so the SPA can show the change at
// once. A backend that reformats on write (fsstore reflows to 80 columns) may
// store it differently; the SPA reloads the view for the stored form. Warnings
// are the soft validation findings of the write (DEC-HWZHA).
type acceptResponse struct {
	Content  string           `json:"content"`
	Warnings []entity.Warning `json:"warnings,omitempty"`
}

// commentAccept applies a comment's suggested replacement to the entity body
// and resolves the comment (TKT-S5C0K3).
//
// # Authorization
//
// Two independent grants, both required. `comment:read` (with the target read
// floor) covers seeing the suggestion. The entity write is authorized by
// PatchEntity itself, exactly as any other body edit, so a principal who could
// not have typed this change cannot accept it either. That write is also
// pre-checked, so a certain denial never touches the comment. `comment:update-*` is
// NOT required to resolve the comment here: the accepter is usually not its
// author, and closing a suggestion by applying it is part of the edit.
//
// The person accepting is the author of record for the body change (audit log,
// version history), in the way whoever merges a pull request is responsible
// for it.
//
// # Order: claim, then write
//
// The comment and the entity live in different stores, so this cannot be
// atomic. Resolving FIRST makes the partial failure "resolved but not applied",
// which the user can see and undo by reopening. The other order fails as
// "applied but still open", and a replacement that keeps the quoted text
// ("brown fox" → "brown fox jumps") still matches, so a retry would apply it
// twice.
//
// The resolve is a conditional [comments.Service.SetResolved], not a write of
// the whole comment. The flip is what stops two requests, on one server
// process or several sharing a database, from both applying the suggestion,
// and it leaves a concurrent edit to the comment's body intact. The patch
// carries the raw row's version, so a body write landing after the reads
// fails the patch instead of being overwritten.
func (h *commentsHandler) commentAccept(
	w http.ResponseWriter, r *http.Request, addr commentAddress, commentID string,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}
	if h.refuseIfReadOnly(w, r) {
		return
	}
	r = h.withProvision(r)
	ctx := r.Context()

	target, visible, ok := h.gateCommentTarget(w, r, addr)
	if !ok {
		return
	}
	existing, ok := h.openSuggestion(w, r, target, commentID)
	if !ok {
		return
	}

	// The splice base and version token come from the RAW row at the face the
	// gate resolved. The visible entity may have redacted properties, and a
	// version token hashed over redacted properties never matches the store.
	raw, found := h.reader.getEntityRef(ctx, entityRef{ID: target.ID, Face: target.Face})
	if !found {
		writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
		return
	}
	if raw.IsLocked() {
		writeV1Error(w, r, http.StatusUnprocessableEntity, "encrypted_inaccessible",
			"Cannot edit an inaccessible entity", "File is git-crypt encrypted; run `git-crypt unlock` first.")
		return
	}
	// The manager stays the authority on the write, but a caller it is certain
	// to refuse must not get to resolve and reopen someone else's comment on
	// the way to that refusal.
	if !h.authorizeBodyWrite(ctx, w, raw) {
		return
	}
	// The accepter reviewed the suggestion against the body they can see. If
	// that differs from the stored body, writing would splice into text they
	// never saw, so refuse. Body redaction does not exist today, so in practice
	// this is a concurrent write landing between the two reads; the message
	// says so.
	if raw.Content != visible.Content {
		writeV1Error(w, r, http.StatusConflict, "suggestion_stale",
			"The entity body changed while accepting; reload and try again", "")
		return
	}

	newBody, err := comments.ApplyReplacement(raw.Content, existing.Anchor)
	if err != nil {
		writeV1Error(w, r, http.StatusConflict, "suggestion_stale",
			"The suggested text no longer matches the entity body", "")
		return
	}

	// Claim the comment. The flip is conditional in the store, so of two
	// accepts racing (even on two server processes) exactly one proceeds.
	claimed, err := h.svc.SetResolved(ctx, target, commentID, true)
	if err != nil {
		writeCommentError(w, r, err)
		return
	}
	if !claimed {
		writeV1Error(w, r, http.StatusConflict, "comment_resolved",
			"This comment is already resolved", "")
		return
	}

	result, err := h.patcher.PatchEntity(ctx, target.Key(), entity.Patch{
		Content:         &newBody,
		ExpectedVersion: string(store.VersionOf(raw)),
	})
	if err != nil {
		if _, uerr := h.svc.SetResolved(ctx, target, commentID, false); uerr != nil {
			slog.ErrorContext(ctx, "accept suggestion: reopen after failed write",
				"target", target.Key(), "comment", commentID, "write_error", err, "reopen_error", uerr)
			writeV1Error(w, r, http.StatusInternalServerError, "comments_failed",
				"The suggestion was not applied, but the comment stayed resolved; reopen it to try again", "")
			return
		}
		if errors.Is(err, entitymanager.ErrEntityNotFound) || errors.Is(err, store.ErrNotFound) {
			writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
			return
		}
		writePatchError(w, r, err)
		return
	}

	resp := acceptResponse{Content: newBody}
	if result != nil {
		if result.Entity != nil {
			resp.Content = result.Entity.Content
		}
		resp.Warnings = result.Warnings
	}
	writeV1JSON(w, http.StatusOK, resp)
}
