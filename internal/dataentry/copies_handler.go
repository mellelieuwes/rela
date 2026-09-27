package dataentry

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	v1 "github.com/Sourcehaven-BV/rela/internal/apiwire/v1"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
)

// copyService is the copy capability this package needs, declared HERE at the
// call site rather than taken from entitymanager wholesale (TKT-WRLDAPI
// item 5).
//
// # Why not the EntityManager interface
//
// [entitymanager.EntityManager] is the ordinary-CRUD write contract — create,
// update, delete, patch. A copy is a distinct operation, so widening that
// shared interface for one consumer would force every implementer and test
// double to grow two methods they will never call. The precedent is one file
// over in the composition root: EntityManager goes in wide while
// `lua.WriteDeps.EntityManager` is the narrower `lua.Mutator`, satisfied
// structurally.
//
// # It is satisfied by the CONCRETE manager, deliberately
//
// [entitymanager.Manager.CopiesForSource] computes each offer's `Allowed` by
// running the manager's own unexported planning and authorization path — the
// same code the invoke uses. That is what makes the hint unable to drift from
// the write. A design in which this package computed invocability itself would
// type-check and would be exactly the RULING 11 defect: an affordance map that
// says one thing while the write path does another.
type copyService interface {
	// CopyState invokes a declared definition BY NAME. It authorizes
	// internally; callers must not pre-empt that with their own check.
	CopyState(ctx context.Context, req entitymanager.CopyRequest) (*entitymanager.CopyResult, error)
}

// copiesHandler serves the copy surface: list-by-source and invoke-by-name.
//
// # Nil is a WIRING BUG, not a mode
//
// The service is required at construction ([newCopiesHandler] rejects nil)
// rather than nil-checked per request, because the alternative renders a
// wiring failure as a VALID DOMAIN ANSWER: an empty offer list is exactly what
// a face with no declared copies returns, so a missing service would present
// as "the promote button never appears" and send someone hunting through
// schema.yaml for a definition that is correctly declared.
//
// That is the project's constructor rule ("never substitute a no-op or
// sentinel implementation silently — that defers the failure to a downstream
// symptom that is much harder to diagnose"), and it is the same shape as the
// silence-shaped defects this epic has been closing: a check, or a capability,
// whose absence looks like a legitimate result.
type copiesHandler struct {
	copies copyService
}

// newCopiesHandler builds the handler. Nil: rejected — see the type doc.
func newCopiesHandler(copies copyService) (*copiesHandler, error) {
	if copies == nil {
		return nil, errors.New("dataentry: newCopiesHandler: copy service must be non-nil")
	}
	return &copiesHandler{copies: copies}, nil
}

// wireCopies builds BOTH consumers of the copy capability from one manager:
// the `_copies` affordance accessor and the invoke handler. NewApp and the
// test rebind both call it, so a test app carries `_copies` exactly as
// production does — the two halves cannot drift, because there is one place
// that produces them.
//
// The concrete manager is required because Allowed must run the kernel's own
// unexported authorization path (see copyService); NewApp's signature already
// demands it, so there is no "not a manager" case to degrade on. Nil:
// rejected — the manager is a required collaborator of NewApp.
func wireCopies(mgr *entitymanager.Manager) (copyOffersFunc, *copiesHandler, error) {
	aff := entitymanager.CopyAffordances{M: mgr}
	h, err := newCopiesHandler(aff)
	if err != nil {
		return nil, nil, err
	}
	return aff.CopiesForSource, h, nil
}

// handleV1Copies routes `/api/v1/_copies…`.
//
//	POST /_copies/{name}   invoke a definition by name
//
// There is NO list endpoint. Copy offers ride the entity (detail) response
// alongside `_actions`, the way every other affordance in this app is
// delivered — computed at read time from data the client already fetched,
// rather than requiring it to construct a separate lookup key. An earlier
// revision shipped a `GET /_copies`; it was removed because a second endpoint
// for one affordance is the odd shape, not the norm.
func (h *copiesHandler) handleV1Copies(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/_copies"), "/")
	switch {
	case r.Method == http.MethodPost && rest != "":
		h.invoke(w, r, rest)
	case r.Method == http.MethodOptions:
		w.Header().Set("Allow", "POST, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
	default:
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed",
			"Method not allowed",
			"POST /_copies/{name} invokes a declared copy; offers ride the entity response")
	}
}

// copyInvokeRequest is the invoke body. It names a DEFINITION and the entities
// it applies to — never a definition's contents.
//
// That is the transforms-registry precedent: a request may choose a registered
// NAME, never supply the thing itself. If a caller could describe a copy, they
// could describe one whose guard is convenient, and the guard system would be
// decorative. [entitymanager.CopyRequest] is three strings for the same
// reason, so this shape is enforced by the type it maps onto.
type copyInvokeRequest struct {
	SourceID string `json:"source_id"`
	// TargetID is required for a CROSS-ENTITY copy and must be empty for a
	// same-entity one, whose target is the source by construction. The kernel
	// validates this; the field is here so a caller can express it.
	TargetID string `json:"target_id,omitempty"`
}

// invoke executes a declared copy.
//
// # It does NOT re-check the guard, and that is the point
//
// [entitymanager.Manager.CopyState] authorizes internally — a read gate on the
// source, the definition's `guard:` resolved per-entity, and a create check on
// a cross-entity target — and it fails closed when a guarded definition has no
// guard wired. Adding a second check here would create two authorization sites
// that can disagree, which is strictly worse than one: the divergence would be
// invisible, since both would look plausible.
//
// So this handler's whole job on the security axis is to call the kernel and
// TRANSLATE its verdict. The mapping is deliberately asymmetric:
//
//   - [acl.ForbiddenError] -> 403, naming the rule. A copy definition and its
//     guard permission are operator-authored config, so saying which
//     permission was missing helps the operator and conceals nothing.
//   - [entitymanager.ErrCopySourceMissing] -> 404, INDISTINGUISHABLE from a
//     genuinely absent entity. The kernel already collapses "denied" into this
//     error for exactly that reason; rendering it as a 403 here would undo the
//     read gate's work and turn this endpoint into an existence oracle.
//   - the kernel's request-shape sentinels (unknown definition, missing or
//     disallowed target id, target of another type) and a validation
//     failure -> 422, the ordinary "coherent request this surface cannot
//     satisfy", with the kernel's own text (it names config and caller ids).
//   - [entitymanager.TheirsWriteError] (the copy would change a field an
//     external system owns on an existing twinned target) -> 422
//     externally_owned, the same answer every other entity write gives.
//   - everything else -> 500 with a generic detail; the real error is logged,
//     because a wrapped store error in the body leaks backend internals.
func (h *copiesHandler) invoke(w http.ResponseWriter, r *http.Request, name string) {
	if strings.Contains(name, "/") {
		writeV1Error(w, r, http.StatusNotFound, "not_found",
			"No such copy definition", name)
		return
	}
	var body copyInvokeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeV1Error(w, r, http.StatusBadRequest, "invalid_json",
			"Request body is not valid JSON", err.Error())
		return
	}
	if body.SourceID == "" {
		writeV1Error(w, r, http.StatusBadRequest, "invalid_request",
			"source_id is required", "")
		return
	}

	res, err := h.copies.CopyState(r.Context(), entitymanager.CopyRequest{
		Definition: name,
		SourceID:   body.SourceID,
		TargetID:   body.TargetID,
	})
	if err != nil {
		writeCopyError(w, r, err)
		return
	}

	if res == nil || res.Entity == nil {
		// Unreachable through the concrete manager, which never returns
		// (nil, nil). Guarded because copyService is an INTERFACE: a stub or a
		// future implementation that returns an empty success would panic an
		// HTTP handler on the request path, and two lines prevent that.
		writeV1Error(w, r, http.StatusInternalServerError, "copy_failed",
			"Copy returned no result", "")
		return
	}
	writeV1JSON(w, http.StatusOK, v1.CopyResult{
		Definition: res.Definition,
		EntityID:   res.Entity.ID,
		Face:       res.Entity.Face.String(),
		Created:    res.Created,
	})
}

// writeCopyError renders a kernel error. See [copiesHandler.invoke] for why
// the mapping is asymmetric.
func writeCopyError(w http.ResponseWriter, r *http.Request, err error) {
	var forbidden *acl.ForbiddenError
	if errors.As(err, &forbidden) {
		writeV1Error(w, r, http.StatusForbidden, "forbidden",
			"Not permitted", forbidden.Decision.Reason)
		return
	}
	if errors.Is(err, entitymanager.ErrCopySourceMissing) {
		// Byte-identical to a genuinely missing entity. Do NOT "improve" this
		// into a 403 naming the source: the kernel folds a denied read into
		// this error precisely so a caller cannot tell absent from forbidden,
		// and a more helpful status here would reopen that.
		writeV1Error(w, r, http.StatusNotFound, "not_found",
			entityNotFoundTitle, "")
		return
	}
	// Request-shape refusals the kernel names with a sentinel: coherent
	// requests this definition cannot satisfy. Their text is safe — it names
	// the definition and the ids the caller supplied, never store internals.
	for _, sentinel := range []error{
		entitymanager.ErrUnknownCopy, entitymanager.ErrCopyTargetRequired,
		entitymanager.ErrCopyTargetNotAllowed, entitymanager.ErrCopyTargetTypeMismatch,
	} {
		if errors.Is(err, sentinel) {
			writeV1Error(w, r, http.StatusUnprocessableEntity, "copy_failed",
				"Copy could not be applied", err.Error())
			return
		}
	}
	if writeExternallyOwnedIf(w, err) {
		return
	}
	var verr *entitymanager.ValidationError
	if errors.As(err, &verr) {
		writeV1Error(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Copy target fails validation", verr.Error())
		return
	}
	// Anything else is an infrastructure failure inside the kernel or the
	// store. The detail is generic and the real error is logged: a wrapped
	// backend error in a client-visible body is how store internals leak, and
	// it is a 500, not a 422 — the request was fine, the server was not.
	slog.Error("dataentry: copy invoke failed", "err", err)
	writeV1Error(w, r, http.StatusInternalServerError, "copy_failed",
		"Copy could not be applied", "check server logs")
}

// registerCopyRoutes mounts the copy surface when it is wired.
//
// Registration is CONDITIONAL because the handler is only built when the
// entity manager satisfies [copyService] — every production manager does, but
// a test double or an embedding caller's stub may not. An unmounted route
// 404s, which is the honest answer for a capability this build does not have:
// the alternative, a mounted route holding a nil service, is the
// wiring-bug-as-domain-answer shape the handler's doc rejects.
func (a *App) registerCopyRoutes(mux *http.ServeMux) {
	if a.copies == nil {
		return
	}
	mux.HandleFunc("/api/v1/_copies", a.copies.handleV1Copies)
	mux.HandleFunc("/api/v1/_copies/", a.copies.handleV1Copies)
}
