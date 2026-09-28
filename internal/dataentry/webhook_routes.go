package dataentry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	entityPkg "github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/markdown"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// DefaultWebhookMaxBodyBytes caps an inbound declarative-webhook body. The
// parsed body becomes an in-memory map that every template interpolation reads,
// so this bounds real memory per concurrent delivery — the action endpoint has
// no such cap, which is precisely the gap this closes.
//
// 1 MiB is generous for the payload shapes this serves (a monitoring alert, a
// form post, an upstream event) while staying small enough that a burst of
// concurrent deliveries cannot exhaust the heap.
const DefaultWebhookMaxBodyBytes int64 = 1 << 20

// webhookMaxAttempts bounds the conflict-retry loop (TKT-1EM4KL). The pipeline
// is conflict-DETECTING rather than locking: a create loser sees a unique
// violation and re-finds; an append loser loses the compare-and-swap,
// re-finds and re-applies.
//
// Every round has a winner, so N concurrent deliveries to one entity need at
// most N rounds. Deliveries run concurrently within a process as well as
// across processes (there is no write lock, TKT-WE0S2K), and
// webhookMaxInFlight admits 8 per process, so the budget matches that. The
// jittered pause between attempts (webhookRetryPause) keeps losers from
// colliding again in lockstep, so a burst usually clears well within it. A
// caller that does not retry is better served by a fast, honest 409 than a
// slow success, and exceeding the budget is reported, never silently dropped.
const webhookMaxAttempts = webhookMaxInFlight

// webhookMaxPauseShift caps the retry pause window at 1ms<<5 = 32ms.
const webhookMaxPauseShift = 5

// webhookRetryPause waits a short random time before the next attempt; the
// window doubles per attempt up to 32ms. It returns ctx's error if ctx ends
// first.
func webhookRetryPause(ctx context.Context, attempt int) error {
	window := time.Millisecond << min(attempt, webhookMaxPauseShift)
	//nolint:gosec // G404: jitter, not a secret
	t := time.NewTimer(rand.N(window))
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// webhookExecTimeout bounds one delivery end to end, so a pathological hook
// cannot pin a request goroutine indefinitely.
const webhookExecTimeout = 30 * time.Second

// errWebhookConflictExhausted reports that the retry budget was spent without a
// clean write. Distinct from a generic failure so the handler can answer 409
// (the caller may usefully retry) rather than 500.
var errWebhookConflictExhausted = errors.New("dataentry: webhook conflict retries exhausted")

// errWebhookBusy reports that the in-flight bound was reached. Answered as 503
// with Retry-After rather than 429: the caller did nothing wrong and is not
// rate-limited per se — the server is momentarily saturated, which is what 503
// means. A monitoring producer treats both as retryable, but 503 is the honest
// one.
var errWebhookBusy = errors.New("dataentry: webhook busy")

// webhookResult is the outcome of one delivery, serialized to the caller.
type webhookResult struct {
	Hook     string `json:"hook"`
	Action   string `json:"action"` // created | updated | no_match
	EntityID string `json:"entity_id,omitempty"`
}

// webhookRouter owns the declarative-webhook pipeline. It is a focused type
// rather than a cluster of methods on App for the reason CLAUDE.md gives: App
// is at its plimsoll method load line, and seven more handler methods there
// would be seven more things reaching into one struct's fields. This holds the
// three collaborators the pipeline actually needs.
//
// The fields are live accessors, not snapshots, because data-entry.yaml is
// reloadable and the store/manager are swapped on rebind — capturing them at
// construction would pin a stale wiring after a reload.
type webhookRouter struct {
	// state reads the current config+metamodel snapshot.
	state func() *Schema
	// write is the write surface (manager, luaDeps).
	write *writeHandler
	// rawStore is the ungated store, used ONLY for the write-prep body re-read
	// in applySteps. Reads that decide what a delivery acts on go through
	// write.luaDeps().VisibleReader instead.
	rawStore store.Store

	// admit bounds how many deliveries may be in the pipeline at once.
	//
	// This endpoint is unauthenticated BY DESIGN (the fronting proxy owns
	// producer auth), so anyone who can reach the port can reach the write
	// path. Each delivery runs a full type scan and a write, so without a bound
	// a flood turns into unbounded concurrent scans that starve the store for
	// every other caller — the SPA, actions, other nodes. There is no
	// authorization gate to move ahead of that work, so the bound IS the
	// mitigation.
	//
	// Buffered channel rather than a semaphore package: the non-blocking send
	// gives "reject immediately when full" for free, which is what a producer
	// wants — a fast 429 it can retry beats a slow success it has timed out on.
	//
	// Built ONCE per router and shared, like cmdexec's pool. A per-request
	// bound would bound nothing.
	admit chan struct{}
}

// webhookMaxInFlight caps concurrent deliveries across all hooks.
//
// Each delivery scans a whole entity type, so admitting many more than this
// buys contention, not throughput. Small enough that a flood is shed rather
// than absorbed, large enough that a legitimate burst from a monitoring fan-out
// (Icinga dispatches notifications concurrently with no cap of its own) is not
// rejected in normal operation.
const webhookMaxInFlight = 8

// registerDeclarativeWebhookRoutes mounts POST /hooks/{id} for every configured
// webhook.
//
// # Reachability
//
// Mounted on the OUTER mux, exactly like registerWebhookRoutes. `inner` is only
// reachable under the `/api/` prefix, so registering a non-/api/ path there
// leaves it unreachable and every request falls through to the SPA catch-all —
// a 200 of HTML with the handler never running (BUG-F3ADZO). The route table in
// router_walk_test.go is what keeps that from recurring.
//
// # Route shape
//
// One pattern per configured hook, resolved at registration from the config
// snapshot. A single `/hooks/{id}` pattern with a runtime lookup would be
// reloadable, but ServeMux gives no way to unregister, so the two would drift
// after a config reload; registering per hook keeps the mounted set and the
// validated set identical at startup. A hook added to data-entry.yaml therefore
// needs a restart, matching how routes behave elsewhere in this package.
// A free function taking the router explicitly rather than an App method: it is
// route wiring, not application behavior, and App is at its plimsoll method load
// line (the same reasoning as dispatchWebhookAction in webhook.go).
func registerDeclarativeWebhookRoutes(mux *http.ServeMux, hooks *webhookRouter) {
	cfg := hooks.state().Cfg
	if len(cfg.Webhooks) == 0 {
		return
	}
	// Deterministic order so registration is reproducible and a duplicate
	// pattern (impossible today — map keys are unique) would fail predictably.
	ids := make([]string, 0, len(cfg.Webhooks))
	for id := range cfg.Webhooks {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		hookID := id
		mux.HandleFunc("POST /hooks/"+hookID, func(w http.ResponseWriter, r *http.Request) {
			hooks.handle(w, r, hookID)
		})
	}
}

// handleDeclarativeWebhook runs one delivery through the configured pipeline.
func (h *webhookRouter) handle(w http.ResponseWriter, r *http.Request, hookID string) {
	// Re-read config per request so a reload that changes a hook's BEHAVIOR
	// takes effect without a restart (only the mounted route set is fixed).
	hook, ok := h.state().Cfg.Webhooks[hookID]
	if !ok {
		// The route exists but the hook no longer does — a reload removed it.
		// A hook id is config, not data, so naming it is not a disclosure (see
		// "The configuration is not a secret" in CLAUDE.md).
		http.Error(w, "unknown webhook: "+hookID, http.StatusNotFound)
		return
	}

	payload, err := readWebhookPayload(r, hook)
	if err != nil {
		var tooLarge *webhookBodyTooLargeError
		if errors.As(err, &tooLarge) {
			http.Error(w, tooLarge.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), webhookExecTimeout)
	defer cancel()

	// Attribute the write to the hook rather than to the data-entry default, so
	// the audit log names which endpoint produced the entity.
	//
	// The name is under principal.ReservedPrefix deliberately. A hook is granted
	// its write rights in acl.yaml — that is the only way it gets any — so this
	// name is BOTH grantable and privileged, exactly the shape the reserved
	// namespace exists to protect. Without the prefix, a deployment using
	// -principal-header would let a caller assert `webhook:icinga-alert` on the
	// ordinary /api/ surface and inherit the hook's grants, because the ACL
	// resolves a principal by matching the raw string and cannot tell where it
	// came from. Reusing the existing prefix inherits principal.IsReserved's
	// refusal at every request-path entry point rather than adding a second
	// boundary that must be remembered.
	ctx = principal.With(ctx, principal.Principal{
		User: principal.ReservedPrefix + "webhook:" + hookID,
		Tool: principal.ToolWebhookReceiver,
	})

	result, err := h.runPipeline(ctx, hookID, hook, payload)
	if err != nil {
		writeWebhookError(w, hookID, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// The response names the entity it wrote, and entity ids are the one
	// identifier rela treats as secret (hence the uniform 404 on reads). This
	// route sits outside /api/, so noCacheMiddleware never runs for it — set the
	// header here rather than relying on a middleware that does not apply.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(hook.Respond.StatusOrDefault())
	_ = json.NewEncoder(w).Encode(result)
}

// writeWebhookError maps a pipeline failure onto a status the producer can act
// on. The distinction matters because senders treat the classes differently: a
// 409 says "your delivery was fine, contention lost" and is worth retrying,
// while a 422 says "this payload cannot produce a valid entity" and is not.
func writeWebhookError(w http.ResponseWriter, hookID string, err error) {
	switch {
	case errors.Is(err, errWebhookBusy):
		// Retry-After is what makes this actionable: a producer that honors it
		// backs off instead of adding to the saturation it just hit.
		w.Header().Set("Retry-After", "2")
		slog.Warn("webhook shed load", "hook", hookID, "max_in_flight", webhookMaxInFlight)
		http.Error(w, "server busy, retry shortly", http.StatusServiceUnavailable)
	case errors.Is(err, errWebhookConflictExhausted):
		slog.Warn("webhook conflict retries exhausted", "hook", hookID)
		http.Error(w, "conflict: concurrent writes to the same entity", http.StatusConflict)
	case errors.Is(err, context.DeadlineExceeded):
		slog.Warn("webhook timed out", "hook", hookID)
		http.Error(w, "webhook processing timed out", http.StatusGatewayTimeout)
	default:
		// Do not echo err: it can carry stored property values from the write
		// path, and the producer is not necessarily entitled to them.
		slog.Error("webhook failed", "hook", hookID, "error", err)
		http.Error(w, "webhook processing failed", http.StatusInternalServerError)
	}
}

// runWebhookPipeline executes find → create-if-missing → then-steps under the
// conflict-retry budget.
//
// Deliveries run concurrently, in this process and across processes. Both
// halves detect a racing writer rather than relying on a lock:
//
//   - CREATE loses on the `unique:` constraint, which the manager checks and
//     writes inside one store.Tx (and pgstore also backs with a derived
//     index), and the retry loop re-finds the winner.
//   - APPEND is a compare-and-swap. Patch.Content is an ABSOLUTE replacement
//     computed from a base read moments earlier, so applySteps pins the patch
//     to that base's version; a racing writer turns the write into a
//     *store.VersionConflictError and the retry loop re-runs the attempt.
func (h *webhookRouter) runPipeline(
	ctx context.Context, hookID string, hook dataentryconfig.Webhook, payload webhookPayload,
) (webhookResult, error) {
	// Shed load BEFORE doing any work. Queueing here instead would mean an
	// unauthenticated flood parks goroutines that each go on to scan the
	// store; a 429 the producer can retry is the better answer. A nil
	// channel means unbounded, which only happens in tests that construct the
	// router directly.
	if h.admit != nil {
		select {
		case h.admit <- struct{}{}:
			defer func() { <-h.admit }()
		default:
			return webhookResult{}, errWebhookBusy
		}
	}

	var lastErr error
	for attempt := range webhookMaxAttempts {
		if err := ctx.Err(); err != nil {
			return webhookResult{}, err
		}
		result, err := h.attempt(ctx, hookID, hook, payload)
		if err == nil {
			return result, nil
		}
		if !isWebhookConflict(err) {
			return webhookResult{}, err
		}
		// A conflict means another writer changed the world between our read
		// and our write. Re-running the WHOLE attempt (re-find, re-derive, re-
		// apply) is what makes the retry correct: it re-reads fresh state
		// rather than replaying a decision made against stale state. This is
		// why declarative steps must stay pure.
		lastErr = err
		slog.Debug("webhook write conflict, retrying",
			"hook", hookID, "attempt", attempt+1, "error", err)
		if attempt+1 < webhookMaxAttempts {
			if err := webhookRetryPause(ctx, attempt+1); err != nil {
				return webhookResult{}, err
			}
		}
	}
	return webhookResult{}, fmt.Errorf("%w: %w", errWebhookConflictExhausted, lastErr)
}

// attemptWebhook is one pass of the pipeline.
func (h *webhookRouter) attempt(
	ctx context.Context, hookID string, hook dataentryconfig.Webhook, payload webhookPayload,
) (webhookResult, error) {
	target, err := h.findTarget(ctx, hook, payload)
	if err != nil {
		return webhookResult{}, err
	}

	action := "updated"
	if target == nil {
		if hook.CreateIfMissing == nil {
			// Find-and-update-only with no match: a legitimate no-op, not an
			// error. Reported so the producer can tell it apart from a write.
			return webhookResult{Hook: hookID, Action: "no_match"}, nil
		}
		created, createErr := h.createEntity(ctx, hook, payload)
		if createErr != nil {
			return webhookResult{}, createErr
		}
		target = created
		action = "created"
	}

	if len(hook.Then) > 0 {
		if err := h.applySteps(ctx, hook, payload, target); err != nil {
			return webhookResult{}, err
		}
	}

	return webhookResult{Hook: hookID, Action: action, EntityID: target.ID}, nil
}

// findWebhookTarget resolves the entity a delivery concerns, or nil when there
// is no match (or no find: block at all).
//
// Reads go through the SAME ACL read path as every other read on this surface —
// the visibility.ScriptReader behind luaDeps().VisibleReader — rather than the
// raw store. That is the BUG-ZWTDH9 rationale applied here: gating on the
// STORED type, redacting `visible:`-hidden fields, and reporting a denied
// entity as a plain miss so this endpoint is not an existence oracle.
//
// The consequence is deliberate: a hook whose principal cannot see the matching
// entity creates a new one rather than silently updating an invisible row.
//
// A nil entity with a nil error is the NO-MATCH result, not an omission: the
// caller distinguishes "found" from "not found" and branches to create, so a
// sentinel error would have to be unwrapped at the one call site to mean the
// same thing. The named returns say so at the signature.
//
//nolint:nilnil // (nil, nil) IS the no-match result; see above.
func (h *webhookRouter) findTarget(
	ctx context.Context, hook dataentryconfig.Webhook, payload webhookPayload,
) (match *entityPkg.Entity, err error) {
	if hook.Find == nil || len(hook.Find.Match) == 0 {
		return nil, nil
	}

	want := make(map[string]string, len(hook.Find.Match))
	for _, prop := range hook.Find.Match {
		expr, ok := hook.Find.Values[prop]
		if !ok {
			// Default: the body field of the same name.
			expr = "{{body." + prop + "}}"
		}
		value := payload.interpolate(expr)
		if value == "" {
			// An empty match value would match every entity missing that
			// property. Refusing is the safe direction: it degrades to
			// "create a new one", never "update an arbitrary one".
			return nil, nil
		}
		want[prop] = value
	}

	reader := h.write.luaDeps().VisibleReader
	if reader == nil {
		return nil, errors.New("dataentry: webhook: no visible reader configured")
	}

	for e, listErr := range reader.ListEntities(ctx, store.EntityQuery{Type: hook.Find.Type}) {
		if listErr != nil {
			return nil, fmt.Errorf("webhook find: %w", listErr)
		}
		if e == nil || !entityMatchesAll(e, want) {
			continue
		}
		// Deterministic winner when the operator's schema does not enforce
		// uniqueness: lowest ID. Without this, which duplicate a delivery
		// appends to would vary per request and the history would interleave.
		if match == nil || e.ID < match.ID {
			match = e
		}
	}
	return match, nil
}

// entityMatchesAll reports whether e has every wanted property value.
func entityMatchesAll(e *entityPkg.Entity, want map[string]string) bool {
	for prop, value := range want {
		if entityPropString(e, prop) != value {
			return false
		}
	}
	return true
}

// entityPropString renders a property as the string a match compares against.
func entityPropString(e *entityPkg.Entity, prop string) string {
	v, ok := e.Properties[prop]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

// createWebhookEntity creates the entity for a find-miss.
//
// No GetEntity pre-check and no uniqueness scan of our own: that would be a
// TOCTOU duplicate of the store's atomic guarantee, which is exactly the
// reasoning Manager.CreateEntity already records. A racing duplicate surfaces
// as store.UniquePropertyError and is retried by the caller, which re-finds and
// then takes the update path.
func (h *webhookRouter) createEntity(
	ctx context.Context, hook dataentryconfig.Webhook, payload webhookPayload,
) (*entityPkg.Entity, error) {
	spec := hook.CreateIfMissing
	createType := hook.CreateType()
	if createType == "" {
		return nil, errors.New("dataentry: webhook: create_if_missing has no resolvable entity type")
	}

	e := &entityPkg.Entity{
		Type:       createType,
		Properties: make(map[string]any, len(spec.Properties)),
	}
	for prop, expr := range spec.Properties {
		e.Properties[prop] = payload.interpolate(expr)
	}
	// Seed the match values too, so an entity created by a find-or-create hook
	// is findable by the SAME key on the next delivery even when the operator
	// listed the property only under find.match. Explicit properties win.
	if hook.Find != nil {
		for _, prop := range hook.Find.Match {
			if _, already := e.Properties[prop]; already {
				continue
			}
			expr, ok := hook.Find.Values[prop]
			if !ok {
				expr = "{{body." + prop + "}}"
			}
			if v := payload.interpolate(expr); v != "" {
				e.Properties[prop] = v
			}
		}
	}
	if spec.Content != "" {
		e.Content = payload.interpolate(spec.Content)
	}

	res, err := h.write.manager.CreateEntity(ctx, e, entityPkg.CreateOptions{Variant: spec.Template})
	if err != nil {
		return nil, fmt.Errorf("webhook create: %w", err)
	}
	if res == nil || res.Entity == nil {
		return nil, errors.New("dataentry: webhook: create returned no entity")
	}
	return res.Entity, nil
}

// applySteps runs the then: steps against target.
//
// Every mutation goes through Manager.PatchEntity naming ONLY what it changes.
// That is the CLAUDE.md rule and it matters especially here: the find read is
// ACL-gated and may be redacted, so a read-modify-write through UpdateEntity
// would carry a redacted view back to the store and erase every property the
// hook's principal could not see.
//
// # Why the body is re-read here
//
// A `set` step names properties, so PatchEntity's own write-prep read is the
// merge base and nothing can be lost. An `append_section` step cannot work that
// way: Patch.Content is an ABSOLUTE replacement, so the appended body has to be
// computed from some base, which makes it a read-modify-write and therefore the
// one step that CAN lose a concurrent write.
// TestWebhookConflict_BlindUpdateLosesAppends demonstrates that loss against
// postgres, to document what is being avoided.
//
// The re-read is the base the append is computed from, and its version is the
// patch's ExpectedVersion. So the write is a compare-and-swap: a writer that
// lands between the re-read and the patch, in this process or another, makes
// PatchEntity fail with *store.VersionConflictError, which isWebhookConflict
// classifies as retryable, and the next attempt re-reads.
//
// On the CREATE path the re-read matters for a second reason: `target` there
// is the entity as constructed, whose Content does not reflect what templates
// and on-create automations actually persisted. Splicing onto the stored body
// is what keeps a template-provided section from being dropped.
//
// The re-read is RAW (the manager's own store handle), matching PatchEntity's
// write-prep read: a redacted body would be written back over the stored one.
func (h *webhookRouter) applySteps(
	ctx context.Context, hook dataentryconfig.Webhook, payload webhookPayload, target *entityPkg.Entity,
) error {
	// Accumulate into one patch so a multi-step hook performs a single write.
	patch := entityPkg.Patch{}
	content := target.Content
	contentChanged := false

	if webhookNeedsBody(hook) {
		fresh, err := h.rawStore.GetEntity(ctx, target.ID)
		if err != nil {
			return fmt.Errorf("webhook re-read body: %w", err)
		}
		content = fresh.Content
		patch.ExpectedVersion = string(store.VersionOf(fresh))
	}

	for i, step := range hook.Then {
		switch {
		case step.AppendSection != nil:
			// interpolate() flattens each substituted value, so the only
			// newlines reaching here are ones the OPERATOR wrote in the
			// template — those are theirs to make, and to get wrong.
			block := payload.interpolate(step.AppendSection.Content)
			content = markdown.AppendToSection(content, step.AppendSection.Section, block)
			contentChanged = true
		case len(step.Set) > 0:
			if patch.Properties == nil {
				patch.Properties = make(map[string]any, len(step.Set))
			}
			for prop, expr := range step.Set {
				patch.Properties[prop] = payload.interpolate(expr)
			}
		default:
			return fmt.Errorf("dataentry: webhook: then[%d] declares no action", i)
		}
	}

	if contentChanged {
		patch.Content = &content
	}
	if patch.IsEmpty() {
		return nil
	}

	if _, err := h.write.manager.PatchEntity(ctx, target.ID, patch); err != nil {
		return fmt.Errorf("webhook apply: %w", err)
	}
	return nil
}

// webhookNeedsBody reports whether any step splices the markdown body, and so
// needs a fresh base to compute the replacement from.
func webhookNeedsBody(hook dataentryconfig.Webhook) bool {
	for _, step := range hook.Then {
		if step.AppendSection != nil {
			return true
		}
	}
	return false
}

// flattenToLine collapses newlines and NULs in ONE INTERPOLATED VALUE, so a
// producer cannot emit its own markdown structure through a template
// substitution.
//
// The threat is the payload, not the template. A `## Heading` arriving inside
// {{body.output}} would become a SIBLING of the section being appended to, so
// every later delivery targeting that section lands above it and the document
// silently reshapes itself.
//
// Applied per value in [webhookPayload.interpolate] rather than to the finished
// string, because the two are different concerns and the finished string cannot
// tell them apart. An operator writing a multi-line `content:` is describing the
// shape they want — their document, their call — and flattening that was an
// over-reach that also made a structured timeline entry impossible to express.
// What no operator can do, deliberately, is hand that power to their producer.
//
// Never returns an error: a value that cannot be represented on one line is
// flattened, not rejected, because refusing would discard an alert whose
// producer will not resend it.
//
// # Only \n and \r, deliberately
//
// Those are the only characters that terminate a line in CommonMark, which is
// what goldmark parses on the write path ([markdown.AppendToSection] splits on
// \n) and what marked renders on the read path. \v, \f, U+0085, U+2028 and
// U+2029 are left alone because they cannot forge a heading: appended after a
// value they stay on the same line and no new heading is parsed.
//
// The safety therefore comes from the PARSER's definition of a line ending, not
// from this function. A future path that splits on Unicode line boundaries —
// notably anything using unicode.IsSpace, which does match \v, \f and U+0085 —
// would put those survivors back in play and need more than this.
//
// Both parsers are held to that definition by tests, because one of them
// diverging is the only way the survivors become dangerous: goldmark by the
// Go tests below, marked by frontend/src/utils/markdownLineEndings.test.ts.
func flattenToLine(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r':
			return ' '
		case 0:
			return -1
		}
		return r
	}, s)
}

// isWebhookConflict reports whether err is a write conflict worth retrying: a
// uniqueness collision on create, or a generic store conflict (the shape a
// stale-state update surfaces as).
func isWebhookConflict(err error) bool {
	// Matching on the unique validation error, not only the store error, is
	// what makes the retry loop reachable at all: the manager's pre-write scan
	// raises it with no store error to wrap.
	//
	// Deliberately narrow: ONLY a unique collision counts. Any other
	// validation failure ("status must be one of...") is a genuine rejection
	// that re-running would reproduce forever, so it must NOT be retried.
	if entitymanager.IsUniqueViolation(err) {
		return true
	}
	// ErrConflict also matches *store.VersionConflictError, which is how a
	// lost append_section compare-and-swap arrives (see applySteps).
	return errors.Is(err, store.ErrConflict)
}

// --- payload ---------------------------------------------------------------

// webhookBodyTooLargeError reports a body over the hook's cap.
type webhookBodyTooLargeError struct{ limit int64 }

func (e *webhookBodyTooLargeError) Error() string {
	return "request body exceeds " + strconv.FormatInt(e.limit, 10) + " bytes"
}

// webhookPayload is the interpolation scope for one delivery: the parsed body,
// the query string, and the ALLOWLISTED headers.
type webhookPayload struct {
	body    map[string]any
	query   url.Values
	headers map[string]string
	now     time.Time
}

// readWebhookPayload reads and parses the request within the hook's body cap.
//
// The cap is enforced with an io.LimitReader at limit+1 bytes so an oversized
// body is DETECTED rather than silently truncated — a truncated JSON body would
// usually fail to parse, but a truncated form body parses fine and would write
// a quietly-wrong entity.
func readWebhookPayload(r *http.Request, hook dataentryconfig.Webhook) (webhookPayload, error) {
	limit := hook.MaxBodyBytes
	if limit <= 0 {
		limit = DefaultWebhookMaxBodyBytes
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return webhookPayload{}, fmt.Errorf("read body: %w", err)
	}
	if int64(len(raw)) > limit {
		return webhookPayload{}, &webhookBodyTooLargeError{limit: limit}
	}

	p := webhookPayload{
		body:    map[string]any{},
		query:   r.URL.Query(),
		headers: extractAllowedHeaders(r, hook.Headers),
		now:     time.Now().UTC(),
	}

	contentType := r.Header.Get("Content-Type")
	mediaType, _, _ := strings.Cut(contentType, ";")
	switch strings.TrimSpace(strings.ToLower(mediaType)) {
	case "application/x-www-form-urlencoded":
		values, parseErr := url.ParseQuery(string(raw))
		if parseErr != nil {
			return webhookPayload{}, fmt.Errorf("parse form body: %w", parseErr)
		}
		for k := range values {
			p.body[k] = values.Get(k)
		}
	default:
		// JSON is the default: it is what every webhook producer this targets
		// sends, and an empty body is legitimate for a hook driven entirely by
		// query parameters.
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p.body); err != nil {
				return webhookPayload{}, fmt.Errorf("parse json body: %w", err)
			}
		}
	}
	return p, nil
}

// extractAllowedHeaders projects ONLY the allowlisted headers.
//
// An allowlist, never pass-through: request headers carry session cookies,
// bearer tokens and proxy-injected identity assertions, and a template that
// could reach any header would let a hook persist one into entity content —
// where it is then served back on every read. The config validator additionally
// refuses the always-wrong names outright.
func extractAllowedHeaders(r *http.Request, allowed []string) map[string]string {
	if len(allowed) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(allowed))
	for _, name := range allowed {
		if v := r.Header.Get(name); v != "" {
			out[strings.ToLower(name)] = v
		}
	}
	return out
}

// interpolate substitutes {{...}} references from the delivery.
//
// Vocabulary, extending the automation template namespace:
//
//	{{body.<path>}}   a field of the parsed body (dotted path into nested objects)
//	{{query.<name>}}  a query-string parameter
//	{{header.<name>}} an ALLOWLISTED request header (case-insensitive)
//	{{now}}           delivery timestamp, RFC 3339 UTC
//	{{today}}         delivery date, YYYY-MM-DD
//
// An unresolved reference becomes the EMPTY STRING rather than being left
// literal. A literal `{{body.host}}` stored in an entity is a silent corruption
// that looks like a template bug forever; empty is visibly missing and cannot
// be mistaken for data. It also means an absent optional field simply omits.
func (p webhookPayload) interpolate(tmpl string) string {
	if !strings.Contains(tmpl, "{{") {
		return tmpl
	}
	var b strings.Builder
	rest := tmpl
	for {
		before, after, found := strings.Cut(rest, "{{")
		b.WriteString(before)
		if !found {
			break
		}
		ref, remainder, closed := strings.Cut(after, "}}")
		if !closed {
			// Unterminated: emit verbatim so the operator sees the typo.
			b.WriteString("{{")
			b.WriteString(after)
			break
		}
		// Flatten the VALUE, not the finished template: the newline that must
		// not survive is a producer-supplied one. See [flattenToLine].
		b.WriteString(flattenToLine(p.resolve(strings.TrimSpace(ref))))
		rest = remainder
	}
	return b.String()
}

// resolve looks up one {{...}} reference, returning "" when absent.
//
// It returns the RAW producer-supplied value, which is not safe to write into a
// document as-is. Use [webhookPayload.interpolate], which flattens each value
// via [flattenToLine]; calling this directly bypasses the one guard that stops a
// payload forging markdown structure.
func (p webhookPayload) resolve(ref string) string {
	switch ref {
	case "now":
		return p.now.Format(time.RFC3339)
	case "today":
		return p.now.Format("2006-01-02")
	}
	namespace, path, ok := strings.Cut(ref, ".")
	if !ok {
		return ""
	}
	switch namespace {
	case "body":
		return stringifyWebhookValue(lookupPath(p.body, path))
	case "query":
		return p.query.Get(path)
	case "header":
		return p.headers[strings.ToLower(path)]
	default:
		return ""
	}
}

// lookupPath walks a dotted path into a decoded JSON object.
func lookupPath(root map[string]any, path string) any {
	var current any = root
	for segment := range strings.SplitSeq(path, ".") {
		obj, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = obj[segment]
		if !ok {
			return nil
		}
	}
	return current
}

// stringifyWebhookValue renders a decoded JSON value for substitution.
//
// Numbers are formatted without a trailing ".0" because encoding/json decodes
// every number as float64: a plain %v would turn an alert's `attempt: 3` into
// "3" but `port: 8080` into "8080" and `1e+06` for a large one, so the integral
// case is normalized explicitly.
func stringifyWebhookValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		// Objects and arrays: JSON, so a nested payload is at least faithfully
		// representable rather than Go-syntax noise.
		encoded, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}
