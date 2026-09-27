package dataentry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/config"
	"github.com/Sourcehaven-BV/rela/internal/migration"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// storeWatcher is the optional capability dataentry needs from a
// store to drive live-reload of entity/relation files. fsstore
// satisfies it; in-memory backends don't. Defined here at the
// consumer per CLAUDE.md "interfaces at the call site".
type storeWatcher interface {
	StartWatching() error
}

// sseEvent represents a Server-Sent Event broadcast to subscribers.
//
// Two shapes flow through the broker:
//
//   - Non-entity events (refresh, config-error, git, git:status): Name + Data are the
//     pre-rendered wire frame, delivered to every subscriber unchanged.
//     They carry no entity identity, so there is nothing to gate.
//   - Entity events (create/update/delete): EntityType is set and Name
//     is empty. These are NOT pre-rendered. handleSSE applies the
//     per-connection ACL read gate to EntityType and, if permitted,
//     writes a type-scoped staleness frame (`event: entity:changed`,
//     `data: {"type": <EntityType>}`). No entity id reaches the wire —
//     the feed is a "your views of type T are stale, re-fetch" signal,
//     and the re-fetch goes through the already-gated REST endpoints
//     (TKT-POT9GQ). The per-connection gate is why entity events can't
//     be pre-rendered in the principal-less pump goroutine.
//
// RelationChange marks a role-conferring relation write (member-of /
// role-relation / containment edge — the types that can alter a
// principal's read verdicts). It is never written to the wire; it
// signals handleSSE to re-derive a fresh read gate and drop its cached
// per-type verdicts so the next entity event re-resolves against
// current membership (RR-K2WKEJ).
type sseEvent struct {
	Name           string // non-entity frame type ("refresh", "config-error", "git", "git:status"); empty for entity/relation events
	Data           string // pre-rendered JSON for non-entity frames
	EntityType     string // set for entity create/update/delete events; gated per-connection
	RelationChange bool   // set for relation writes; invalidates cached verdicts, never written to the wire
}

// eventBroker manages SSE client connections for live-reload notifications.
type eventBroker struct {
	mu      sync.Mutex
	clients map[chan sseEvent]struct{}
}

func newEventBroker() *eventBroker {
	return &eventBroker{clients: make(map[chan sseEvent]struct{})}
}

func (b *eventBroker) subscribe() chan sseEvent {
	ch := make(chan sseEvent, 4)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *eventBroker) unsubscribe(ch chan sseEvent) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
}

// broadcast sends a simple non-entity event (refresh / git).
func (b *eventBroker) broadcast(eventType string) {
	b.broadcastEvent(sseEvent{Name: eventType, Data: eventType})
}

// broadcastEvent fans an event out to every subscriber. Lossy: a full
// client buffer drops the event (the SSE feed is a hint, not a log).
func (b *eventBroker) broadcastEvent(event sseEvent) {
	b.mu.Lock()
	for ch := range b.clients {
		select {
		case ch <- event:
		default: // skip slow clients
		}
	}
	b.mu.Unlock()
}

// broadcastEntityChange fans a type-scoped staleness event. create,
// update, and delete all collapse to the same signal — "type T
// changed, re-fetch" — because the client acts identically on all
// three (invalidate active queries of that type). No entity id is
// carried; handleSSE gates EntityType per-connection before writing
// (TKT-POT9GQ).
func (b *eventBroker) broadcastEntityChange(entityType string) {
	b.broadcastEvent(sseEvent{EntityType: entityType})
}

// broadcastRelationChange signals subscribers to drop their cached
// per-type read verdicts: member-of, role-relation, and containment
// edge writes can all change what a principal may read. Never written
// to the wire (RelationChange is the marker).
func (b *eventBroker) broadcastRelationChange() {
	b.broadcastEvent(sseEvent{RelationChange: true})
}

// broadcastConfigError tells subscribers that a data-entry.yaml reload was
// rejected and the previous config is still serving. The frame carries the
// file name and the error's public message, never a host path.
func (b *eventBroker) broadcastConfigError(ce *configError) {
	data, _ := json.Marshal(map[string]string{"file": ConfigFile, "error": ce.public})
	b.broadcastEvent(sseEvent{Name: "config-error", Data: string(data)})
}

// broadcastGitStatus sends a git status update event.
func (b *eventBroker) broadcastGitStatus() {
	b.broadcastEvent(sseEvent{Name: "git:status", Data: "{}"})
}

// StartWatching begins file watching for live-reload. It does two
// independent things:
//
//  1. Subscribes to `data-entry.yaml` via the config loader so SPA
//     reloads pick up dashboard / palette / form config changes.
//  2. Asks the store to start watching its own entity/relation files
//     (feature-detected via [storeWatcher] — fsstore implements it;
//     in-memory backends don't). The store's observer chain handles
//     reindex automatically; no callback wiring is needed at this
//     layer.
//
// The returned error covers only the config-subscriber failure
// (step 1). Store-watcher errors (step 2) are logged via slog.Warn
// because store watching is a live-reload nice-to-have, not a
// startup requirement.
//
// Stop via [App.StopWatching]. Note: that only releases the config
// subscription — the store-watcher lifecycle is owned by the store
// (closed when the store is closed).
//
// coverage-ignore-func: requires real filesystem events via fsnotify
func (a *App) StartWatching() error {
	// coverage-ignore-start: os-fs-event: startup wiring for fsnotify-backed config subscription, store watcher, and
	// store-event SSE bridge
	// (project marks it coverage-ignore)
	// (1) data-entry.yaml subscription.
	if sub, ok := a.cfgLoader.(config.Subscriber); ok {
		stop, err := sub.Subscribe(context.Background(), ConfigFile, func() {
			// A rejection is already logged and broadcast; nothing more to do.
			_ = a.reloadConfig()
		})
		if err != nil {
			return err
		}
		a.stopConfigWatch = stop
	}

	// (2) Store-level watcher (fsstore). Errors are logged, not
	// returned — store watching is a live-reload nice-to-have, not a
	// startup requirement. Stores that don't implement [storeWatcher]
	// (memstore, etc.) get a debug log instead of silence so
	// "why isn't live-reload working" doesn't require a goose chase.
	if sw, ok := a.store.(storeWatcher); ok {
		if err := sw.StartWatching(); err != nil {
			slog.Warn("store watcher not started", "error", err)
		}
	} else {
		slog.Debug("store does not implement watching; live-reload of external edits disabled",
			"store", fmt.Sprintf("%T", a.store))
	}

	// (3) Store-event -> SSE bridge. Subscribes to the store's change watcher
	// and broadcasts entity changes to connected browsers. This is the path
	// that makes a write by ANOTHER process visible live (the pgstore
	// cross-process feed, TKT-WZYWM9) — and also surfaces fsstore's
	// external-file-edit events, which previously had no consumer. Local API
	// writes flow through here too, so the inline broadcasts in the write
	// handlers were removed to avoid double-broadcasting.
	a.startStoreEventBridge()

	return nil
	// coverage-ignore-end
}

// storeEventBufSize is the buffer for the store-event subscription. The watcher
// drops events on a full buffer (it's lossy by contract), but since each event
// only triggers a re-fetch hint to the browser, a modest buffer plus the
// browser's own re-snapshot keeps the UI consistent.
const storeEventBufSize = 64

// startStoreEventBridge subscribes to the store's change feed and pumps entity
// events to the SSE broker. Only entity create/update/delete are broadcast
// (relations/attachments are not part of the live feed today — matching the
// prior inline-broadcast behavior). Idempotent re-snapshot semantics: a
// duplicate event just nudges the browser to re-fetch again, which is harmless.
//
// # Audit-isolation invariant
//
// The SSE broker NEVER carries audit records. The wire payload is
// `{type, id}` — an entity marker only. Subject.ID / Subject.FromID
// from `denied-write` audit rows must NOT be forwarded here: the
// audit log is the only intended audience for principal-attribution
// detail, and broadcasting it via SSE would leak the principal-to-
// entity topology to every event subscriber.
//
// If a future feature needs an audit-aware SSE channel, it must
// expose a SEPARATE broker, gated by per-subscriber ACL, with its
// own redaction policy. The regression test
// `TestSSE_DoesNotFlowAuditEvents` pins this invariant.
func (a *App) startStoreEventBridge() {
	events, cancel := a.store.Subscribe(storeEventBufSize)
	a.stopStoreWatch = cancel
	go a.pumpStoreEvents(events)
}

// pumpStoreEvents maps store.Events to SSE entity broadcasts until the
// subscription channel closes (on cancel / store Close).
func (a *App) pumpStoreEvents(events <-chan store.Event) {
	for ev := range events {
		switch ev.Op {
		case store.EventEntityCreated, store.EventEntityUpdated, store.EventEntityDeleted:
			// All three collapse to one type-scoped staleness signal:
			// the client acts identically (invalidate active queries of
			// the type), and carrying no id keeps the feed from being a
			// per-entity existence oracle (TKT-POT9GQ).
			a.broker.broadcastEntityChange(ev.EntityType)
		case store.EventRelationCreated, store.EventRelationUpdated, store.EventRelationDeleted:
			// Relation writes are not surfaced as browser refreshes (no
			// broadcast existed for them before), but they CAN change a
			// principal's read verdicts (member-of / role-relation /
			// containment edges). Signal connections to drop their
			// cached per-type verdicts so the next entity event
			// re-resolves against current membership (RR-K2WKEJ).
			a.broker.broadcastRelationChange()
		}
	}
}

// StartGitFetch begins periodic git fetch in the background.
// Returns a stop function to shut down the fetcher.
//
// coverage-ignore-func: background goroutine with timer
func (a *App) StartGitFetch() (stop func()) {
	gitOps := a.gitOps
	cfg := a.State().Cfg.Git

	if gitOps == nil || cfg == nil || cfg.FetchInterval <= 0 {
		return func() {} // no-op if git not configured or fetch disabled
	}

	// coverage-ignore-start: os-fs-event: background goroutine driven by a real time.Ticker running git fetch (project
	// marks it coverage-ignore)
	interval := time.Duration(cfg.FetchInterval) * time.Second
	ticker := time.NewTicker(interval)
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-ticker.C:
				if err := gitOps.Fetch(); err != nil {
					slog.Warn("git fetch error", "error", err)
				} else {
					// Broadcast git status update so UI can refresh
					a.broker.broadcast("git")
				}
			case <-done:
				ticker.Stop()
				return
			}
		}
	}()

	slog.Info("git background fetch started", "interval", interval)
	return func() {
		close(done)
	}
	// coverage-ignore-end
}

// configReadFailedMessage is the config-error text sent when data-entry.yaml
// cannot be read at all. The loader's own error is logged but kept off the
// wire because it can carry the host path of the project directory.
const configReadFailedMessage = "cannot read " + ConfigFile + "; see the server log"

// configError is a rejected data-entry.yaml reload. It carries the message
// safe to show to a browser separately from the error that is logged.
type configError struct {
	public string
	err    error
}

func (e *configError) Error() string { return e.err.Error() }
func (e *configError) Unwrap() error { return e.err }

// publicConfigErrorMessage renders a [loadConfig] error for the config-error
// frame. The text describes operator-authored config, which is not a secret,
// but it must not carry the host path of the project directory. The
// deprecated-syntax error names its file by absolute path, so it is re-rendered
// with the bare file name. Any other message is stripped of the project root as
// a backstop, so a future error source cannot put the path on the wire either.
func publicConfigErrorMessage(err error, root string) string {
	var migErr *migration.Error
	if errors.As(err, &migErr) {
		safe := *migErr
		safe.FilePath = ConfigFile
		return safe.Error()
	}
	msg := err.Error()
	if root = filepath.Clean(root); root != string(filepath.Separator) && root != "." {
		msg = strings.ReplaceAll(msg, root+string(filepath.Separator), "")
	}
	return msg
}

// reloadConfig re-reads data-entry.yaml, publishes a fresh Schema snapshot
// atomically, and tells browsers to refresh. Readers observe either the
// pre-reload or the post-reload snapshot, never a torn state.
//
// The file goes through [loadConfig], the same pipeline as startup. When it
// fails, nothing is published and the previous config keeps serving:
// swapping in a config startup would refuse turns a typo into HTTP 500s at
// first use (TKT-IMBOK). Browsers get a config-error frame instead of a
// refresh, and the returned error is a *configError.
func (a *App) reloadConfig() error {
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	if a.State() == nil {
		return nil
	}

	reject := func(ce *configError) error {
		slog.Warn("config reload rejected; keeping previous config", "error", ce.err)
		a.broker.broadcastConfigError(ce)
		return ce
	}

	meta := a.Meta()
	cfgData, err := a.cfgLoader.Load(context.Background(), ConfigFile)
	if err != nil {
		return reject(&configError{public: configReadFailedMessage, err: err})
	}
	cfg, err := loadConfig(cfgData, meta, a.paths.Root)
	if err != nil {
		return reject(&configError{public: publicConfigErrorMessage(err, a.paths.Root), err: err})
	}
	slog.Info("config reloaded")

	// The provider recomputes the style map + OpenAPI generator (co-derived
	// from cfg/meta) and publishes atomically.
	a.schema.Reload(cfg, meta, true)

	// Re-resolve the palette against the new project palette. The service
	// keeps the previous user override if the on-disk file is broken —
	// better to show stale colors than crash or wipe.
	if err := a.palette.Reresolve(cfg.Palette); err != nil {
		slog.Warn("watcher: keeping previous user palette",
			"file", userPaletteFile, "error", err)
	}
	a.broker.broadcast("refresh")
	return nil
}

// handleSSE serves Server-Sent Events for live-reload notifications.
// Connected browsers receive events when project files change or entities
// are modified. Event types on the wire:
//   - refresh: Files changed, reload needed
//   - config-error: a data-entry.yaml reload was rejected; the previous
//     config keeps serving. Data is {"file": ..., "error": ...}
//   - git / git:status: Git status changed (git:status carries empty JSON)
//   - entity:changed: Entities of a type changed; data is {"type": "..."}
//     with NO id. Create/update/delete all collapse to this single
//     type-scoped staleness signal, ACL-gated per connection — a
//     connection only receives a type its principal may read (TKT-POT9GQ).
func (a *App) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// SSE is reachable only from same-origin or explicitly allow-listed
	// dev origins (enforced by requireSameOrigin middleware). No CORS
	// reflection — that previously let any website subscribe to live
	// project events.

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := a.broker.subscribe()
	defer a.broker.unsubscribe(ch)

	// Send initial keepalive
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	a.runSSELoop(w, r, flusher, ch)
}

// sseFlushInterval bounds how long a coalesced type-change burst waits
// before flushing. A write burst (e.g. a `git pull` landing many files,
// or a cascade) produces many store events in quick succession; instead
// of one wire frame per event we accumulate the affected types over this
// window and emit one `entity:changed` frame per type. The window is a
// hint-latency tradeoff — the client re-fetches a fraction of a second
// later, invisible to users — not a correctness knob.
const sseFlushInterval = 200 * time.Millisecond

// runSSELoop is the per-connection event loop. It is split out from
// handleSSE so tests can drive it with an in-memory writer.
//
// Entity events are gated per-connection: the connection's principal
// read verdict for each entity type is resolved via the read gate and
// cached. A pending set of "stale types" is accumulated and flushed on a
// timer (coalescing bursts, AC5).
//
// A RelationChange marks a role-conferring relation write, which can
// alter the principal's read scope (a member-of edge changes group
// membership; a role-relation / containment edge changes inheritance).
// It re-derives a FRESH read gate (a new acl.Request whose member-of
// closure is walked against the current graph — the connection's
// original Request memoizes that closure for its lifetime, RR-K2WKEJ)
// and clears the cached verdicts so the next entity event re-resolves
// against current membership. The re-derive is coalesced into the same
// flush window so a burst of relation writes triggers one re-walk, not
// one per edge.
//
// Non-entity events (refresh, git) pass through ungated and immediately.
func (a *App) runSSELoop(w io.Writer, r *http.Request, flusher http.Flusher, ch <-chan sseEvent) {
	gate := readGateFromContext(r.Context())
	verdicts := make(map[string]bool) // entityType -> visible; cached per connection
	pending := make(map[string]struct{})
	regate := false // a coalesced relation change is pending → re-derive on flush

	// One timer for the connection's lifetime, created stopped and reset
	// to (re)arm the coalescing window. flushArmed tracks whether the
	// timer is currently running so a burst doesn't keep resetting it.
	flush := time.NewTimer(sseFlushInterval)
	if !flush.Stop() {
		<-flush.C
	}
	defer flush.Stop()
	flushArmed := false
	armFlush := func() {
		if !flushArmed {
			flush.Reset(sseFlushInterval)
			flushArmed = true
		}
	}

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			switch {
			case event.RelationChange:
				// Coalesce: re-derive the gate at most once per flush
				// window (a bulk relation import otherwise walks member-of
				// per edge per connection).
				regate = true
				armFlush()
			case event.EntityType != "":
				// While a re-gate is pending the current gate is known
				// stale, so defer the visibility decision to the flush
				// (which re-checks every pending type against the fresh
				// gate). Otherwise filter at entry to avoid buffering
				// types the principal can't read.
				if regate || a.entityTypeVisible(r.Context(), gate, verdicts, event.EntityType) {
					pending[event.EntityType] = struct{}{}
					armFlush()
				}
			case event.Name != "":
				// Non-entity frame (refresh, git, git:status): pre-rendered, ungated.
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Name, event.Data)
				flusher.Flush()
			default:
				// A zero-value frame is a programming error (every
				// production construction site sets exactly one
				// discriminant). Drop it rather than emit a malformed
				// empty frame.
				slog.Warn("sse: dropping zero-value broker event")
			}
		case <-flush.C:
			if regate {
				gate = a.freshReadGate(r.Context(), gate)
				clear(verdicts)
				regate = false
			}
			for typ := range pending {
				if a.entityTypeVisible(r.Context(), gate, verdicts, typ) {
					data, _ := json.Marshal(map[string]string{"type": typ})
					fmt.Fprintf(w, "event: entity:changed\ndata: %s\n\n", data)
				}
			}
			flusher.Flush()
			clear(pending)
			flushArmed = false
		case <-r.Context().Done():
			return
		}
	}
}

// freshReadGate returns a read gate whose member-of closure is resolved
// against the CURRENT graph, for use after a role-conferring relation
// change invalidates the connection's cached membership. When ACL is a
// *acl.Declarative it opens a new acl.Request for the principal (a fresh
// Globals walk); otherwise (NopACL, or ForPrincipal failure) it keeps
// the existing gate — for NopACL the verdict is constant-true so there
// is nothing to refresh, and on a transient resolve error keeping the
// prior gate is fail-safe (it can only be equal-or-narrower than a stale
// allow, and the next relation change retries).
func (a *App) freshReadGate(ctx context.Context, current readGate) readGate {
	d, ok := a.acl.(*acl.Declarative)
	if !ok || d == nil {
		return current
	}
	req, err := d.ForPrincipal(principal.From(ctx))
	if err != nil {
		slog.Warn("sse: re-deriving read gate failed; keeping prior gate", "err", err)
		return current
	}
	gate, err := newACLReadGate(req)
	if err != nil {
		slog.Warn("sse: re-deriving read gate failed; keeping prior gate", "err", err)
		return current
	}
	return gate
}

// entityTypeVisible returns whether the connection's principal may read
// the type at all, caching the verdict for the connection's lifetime
// (cleared on a relation change). DenyAll → not visible (withhold the
// nudge). AllowAll / Query → visible (the principal can read at least
// some entities of the type, so "the type changed, re-fetch" leaks
// nothing beyond the gated list they could already poll).
//
// Fail-closed: an unresolvable / zero verdict withholds (RR-MTUW2N) —
// the only signal on the wire is the type, so there is no error to
// echo; the miss degrades to "no nudge", and the client's periodic
// reconnect re-fetch reconciles.
func (a *App) entityTypeVisible(ctx context.Context, gate readGate, cache map[string]bool, entityType string) bool {
	if v, ok := cache[entityType]; ok {
		return v
	}
	rqr := gate.ReadQuery(ctx, entityType)
	visible := rqr.AllowAll || rqr.Query != nil
	cache[entityType] = visible
	return visible
}

// noCacheMiddleware sets no-cache headers on dynamic responses so that
// browsers always fetch fresh data after file changes trigger a reload.
// This replaces the previous reloadLockMiddleware, which also held the
// now-deleted App.mu read lock — handlers now get coherent reloadable
// state via a.State() without any lock acquisition.
//
// Static files (/static/*) are served separately and bypass this
// middleware, so they retain normal caching behavior.
//
// Under ACL, API responses are additionally per-principal: when a
// principal header is configured (SetPrincipalHeader), `Vary` names it
// so any cache that ignores the no-store directive still keys on the
// identity header instead of serving principal A's filtered response
// to principal B (TKT-VMD8 AC10, RR-VDTW).
func (a *App) noCacheMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		if a.principalHeader != "" {
			w.Header().Add("Vary", a.principalHeader)
		}
		next.ServeHTTP(w, r)
	})
}
