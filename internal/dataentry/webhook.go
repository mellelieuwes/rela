package dataentry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/principal"
)

// errWebhookActionMissing is returned when the configured provisioning action id
// does not exist in the loaded config — an operator misconfiguration.
var errWebhookActionMissing = errors.New("dataentry: configured webhook action not found")

// errWebhookActionEntityBound is returned when the configured provisioning
// action has available_on. Such an action needs an entity and is gated on
// the invoking user; the webhook supplies neither (TKT-VVS16W).
var errWebhookActionEntityBound = errors.New(
	"dataentry: configured webhook action has available_on and can only run from a detail page")

// WebhookClaims is the verified subset of an inbound webhook the receiver acts
// on. It mirrors jwtauth.WebhookClaims but is declared HERE so the dataentry
// package needn't import jwtauth (the inward-pointing layering rule — the same
// reason the JWT gate takes a local assertionVerifier interface). The wiring
// layer, which may import both, adapts the concrete verifier to this shape.
type WebhookClaims struct {
	Event  string // the event name, e.g. "membership.created"
	UserID string // the subject the event concerns
	OrgID  string // the tenant the event concerns
	ID     string // the webhook id (jti), for replay dedup
}

// webhookMaxBody caps the JWT body the receiver reads before verifying it, so a
// malicious oversized body can't exhaust memory pre-auth. A webhook JWT is a few
// hundred bytes; 64 KiB is generous headroom.
const webhookMaxBody = 64 << 10 // 64 KiB

// webhookActionTimeout bounds the provisioning action (which fetches the user
// from the IdP over HTTP, then upserts). Tighter than lua.DefaultTimeout so a
// slow IdP answers the webhook sender before it gives up and redelivers.
const webhookActionTimeout = 15 * time.Second

// webhookDedupTTL is how long a processed webhook id is remembered so a redelivery
// (IdPs retry on a slow/failed response) is a no-op rather than a duplicate
// provision. The window need only exceed the IdP's retry span.
const webhookDedupTTL = 10 * time.Minute

// webhookVerifier verifies a signed webhook JWT and projects the claims the
// receiver acts on. Declared at the call site (returning the local WebhookClaims
// so this package doesn't import jwtauth) — the wiring layer adapts the concrete
// *jwtauth.WebhookVerifier to it. Also lets the handler be tested with a stub.
type webhookVerifier interface {
	VerifyWebhook(ctx context.Context, raw string) (WebhookClaims, error)
}

// webhookReceiver holds the wiring for the inbound-IdP webhook: the verifier, the
// action it dispatches to, and a small dedup cache. Nil on the App until
// SetWebhookReceiver is called, in which case the route is not mounted.
type webhookReceiver struct {
	verify   webhookVerifier
	actionID string
	seen     *seenSet
}

// SetWebhookReceiver enables the POST /webhooks/idp endpoint: a verified IdP
// callback that dispatches to the named action (which fetches authoritative user
// data and upserts a person entity). Must be called before NewRouter. A nil
// verifier or empty actionID leaves the receiver disabled and the route
// unmounted — matching the inert-when-unconfigured shape of the other optional
// wiring (SetPrincipalResolver, SetSecurityConfig).
func (a *App) SetWebhookReceiver(v webhookVerifier, actionID string) {
	if v == nil || actionID == "" {
		return
	}
	a.webhook = &webhookReceiver{verify: v, actionID: actionID, seen: newSeenSet(webhookDedupTTL)}
}

// registerWebhookRoutes mounts the webhook endpoint when a receiver is
// configured. Called from NewRouter. The route lives OUTSIDE /api/ on purpose:
// it is not the browser API surface, and it authenticates itself by verifying a
// signed JWT body rather than trusting a proxy header or a session cookie. That
// makes it CSRF-immune by construction (a browser cannot forge an ES256
// signature), so it needs neither the same-origin gate nor the CSRF-exempt
// heuristic the sync API relies on.
func (a *App) registerWebhookRoutes(mux *http.ServeMux) {
	if a.webhook == nil {
		return
	}
	// The handler lives on the receiver; the App only supplies the dispatch
	// closure (which routes through the writeHandler: engine, schema). This
	// keeps the HTTP-flow logic off the (already large) App type.
	rec := a.webhook
	mux.HandleFunc("POST /webhooks/idp", func(w http.ResponseWriter, r *http.Request) {
		rec.handle(w, r, func(ctx context.Context, claims WebhookClaims) error {
			return dispatchWebhookAction(ctx, a.write, rec.actionID, claims)
		})
	})
}

// handle verifies an inbound IdP webhook and dispatches it via dispatch. The
// request body is the signed JWT (the IdP sends Content-Type: application/jwt).
// On any verification failure it answers 401 and dispatch is never called. A
// redelivered webhook (same id) is acknowledged with 200 without re-dispatching.
func (rec *webhookReceiver) handle(
	w http.ResponseWriter, r *http.Request, dispatch func(context.Context, WebhookClaims) error,
) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, webhookMaxBody))
	if err != nil {
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}

	claims, err := rec.verify.VerifyWebhook(r.Context(), string(raw))
	if err != nil {
		// Do not echo the reason — a probing client learns nothing beyond "rejected".
		slog.Warn("idp webhook rejected", "error", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// A membership event with no subject is unusable — the action has nothing to
	// fetch. Treat it as a bad request rather than dispatching a no-op.
	if claims.UserID == "" {
		http.Error(w, "missing user_id", http.StatusBadRequest)
		return
	}

	// Dedup on the webhook id (jti). An id-less webhook can't be deduped, so it is
	// always processed — at-least-once is acceptable because the action is
	// idempotent (it upserts by sub).
	if claims.ID != "" && !rec.seen.add(claims.ID) {
		slog.Info("idp webhook deduped", "id", claims.ID, "event", claims.Event)
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := dispatch(r.Context(), claims); err != nil {
		// The action failed (IdP fetch error, script error, ...). Return 502 so the
		// IdP retries; drop the id from the seen-set so the retry isn't deduped.
		if claims.ID != "" {
			rec.seen.forget(claims.ID)
		}
		slog.Warn("idp webhook action failed", "action", rec.actionID, "event", claims.Event, "error", err)
		http.Error(w, "action failed", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// dispatchWebhookAction runs the provisioning action with the webhook's claims as
// params, under the webhook-receiver principal. It runs concurrently with other
// writes, like handleV1Action; an upsert keyed on a `unique:` property stays
// single because the manager checks uniqueness and writes inside one store.Tx.
// A free function taking the writeHandler explicitly (it is dispatch wiring,
// not a route handler).
func dispatchWebhookAction(ctx context.Context, h *writeHandler, actionID string, claims WebhookClaims) error {
	s := h.schema()
	action, ok := s.Cfg.Actions[actionID]
	if !ok {
		// Misconfiguration: the configured action id doesn't exist. Fail loud in
		// the log; the 502 the caller sends will surface it operationally too.
		slog.Error("idp webhook: configured action not found", "action", actionID)
		return errWebhookActionMissing
	}
	// The webhook runs as the webhook-receiver principal, not a user, so an
	// action's `permission:` does not apply here: the verified callback is the
	// authorization. An entity-bound action cannot run here at all.
	if action.AvailableOn != nil {
		slog.Error("idp webhook: configured action has available_on", "action", actionID)
		return errWebhookActionEntityBound
	}

	// Stamp the webhook-receiver principal so the provisioned entity is attributed
	// to the callback, not to the default data-entry "unknown". The action path
	// reads principal.From(ctx).
	ctx = principal.With(ctx, principal.Principal{
		User: "webhook:" + claims.Event,
		Tool: principal.ToolWebhookReceiver,
	})

	params := map[string]string{
		"event":   claims.Event,
		"user_id": claims.UserID,
		"org_id":  claims.OrgID,
	}

	// TKT-YH52OM: the webhook path resolves the SAME `actions:` entry as the
	// HTTP endpoint, so it honors the same `capabilities:` declaration. This
	// is the surface that most needs it to work: an IdP-sync action legitimately
	// calls out over http with a named secret (see examples/idp-sync.lua), and
	// it now must say so in config rather than receiving the whole secrets file
	// by default.
	deps := h.luaDeps()
	deps.Capabilities = luaCapabilities(action.Capabilities)
	_, err := h.engine().ExecuteAction(ctx, action.Script, deps,
		nil, params, webhookActionTimeout, newCorrelationID())
	return err
}

// --- dedup seen-set -------------------------------------------------------

// seenSet is a small TTL set of recently-processed webhook ids. It bounds memory
// by evicting expired entries on each add; the id space per window is tiny (one
// per membership change), so no size cap is needed beyond the TTL sweep.
type seenSet struct {
	mu  sync.Mutex
	ttl time.Duration
	at  map[string]time.Time
	// now is injectable for tests; nil ⇒ time.Now.
	now func() time.Time
}

func newSeenSet(ttl time.Duration) *seenSet {
	return &seenSet{ttl: ttl, at: make(map[string]time.Time)}
}

func (s *seenSet) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// add records id and reports whether it was NEW (true) or already present within
// the TTL (false). Expired entries are swept on each call.
func (s *seenSet) add(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	for k, t := range s.at {
		if now.Sub(t) > s.ttl {
			delete(s.at, k)
		}
	}
	if t, ok := s.at[id]; ok && now.Sub(t) <= s.ttl {
		return false
	}
	s.at[id] = now
	return true
}

// forget drops id so a retried-after-failure webhook isn't deduped.
func (s *seenSet) forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.at, id)
}
