package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/appbuild"
	"github.com/Sourcehaven-BV/rela/internal/attachment"
	"github.com/Sourcehaven-BV/rela/internal/config"
	"github.com/Sourcehaven-BV/rela/internal/lock"
	relamcp "github.com/Sourcehaven-BV/rela/internal/mcp"
	"github.com/Sourcehaven-BV/rela/internal/script"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// mcpServices owns the per-project services the MCP process needs and
// their lifecycle (Close). It is constructed once by [newMCPServices]
// and held for the lifetime of the MCP process. The MCP server itself
// receives a flattened [mcp.Deps] via [mcpServices.Deps] — it never
// holds a reference to this struct, so `internal/mcp` does not depend
// on this wiring code.
//
// The service stack (store, search, metamodel, entitymanager,
// automation, audit, validator, lua deps) is built by [appbuild] — the
// single composition root shared with rela-server and rela-desktop — so
// the MCP wiring is not a second composition root. Only the watcher
// adapter is MCP-specific (appbuild deliberately has no watcher story).
type mcpServices struct {
	// mu serializes reloads against each other and against Close. Reads of
	// svc go through [mcpServices.current], which takes it briefly — the
	// swap itself is a pointer assignment, and the MCP server publishes its
	// own snapshot atomically, so request handling never blocks on this.
	mu  sync.Mutex
	svc *appbuild.Services
	// origin is the FIRST assembled Services — the one that opened the store
	// and the search index and therefore owns closing them. Reloads assemble
	// successors against those same handles, so only this one may be Closed;
	// superseded successors get CloseAssembly instead. Never reassigned.
	origin  *appbuild.Services
	watcher relamcp.Watcher
	// stopSchemaWatch releases the schema.yaml subscription. Nil until
	// [mcpServices.watchSchema] runs.
	stopSchemaWatch func()
	// closed is set by Close. The watcher's Stop does NOT wait for an
	// in-flight callback, so a change event can be mid-flight when shutdown
	// begins; without this flag that callback would assemble a whole new
	// service generation — job queue, mail worker, GC sweep — against a store
	// Close has already torn down, and nothing would ever stop them.
	closed bool
	// attachLocker serializes the MCP attachment writes of this process per
	// (entity, property). It outlives reloads, so writes under an old and a
	// new schema still exclude each other.
	attachLocker lock.Locker
	// attachUploads bounds this process's concurrent MCP uploads. It
	// outlives reloads for the same reason.
	attachUploads *attachment.Limiter
}

// current returns the live services bundle.
func (s *mcpServices) current() *appbuild.Services {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.svc
}

// newMCPServices discovers the project at startDir, builds the focused
// services via [appbuild.Discover], and returns a bundle whose
// [mcpServices.Deps] builds the [mcp.Deps] handed to the server.
//
// MCP is wired with [acl.NopACL] (allow-all) on purpose: it is a local
// stdio transport, so anyone who can launch `rela mcp` already has
// filesystem write access to the entity files and can edit them
// directly, bypassing any gate. The filesystem is the trust boundary
// here, not the tool surface — policy enforcement on MCP would defend
// nothing. Access control that matters belongs on the deployed HTTP API,
// which serves callers who do not have direct file access. WithACL makes
// this an explicit, justified opt-out rather than a silent default.
func newMCPServices(startDir string) (*mcpServices, error) {
	svc, err := appbuild.Discover(startDir, script.NewEngine(), appbuild.WithACL(acl.NopACL{}))
	if err != nil {
		return nil, err
	}
	return &mcpServices{
		svc:           svc,
		origin:        svc,
		watcher:       &mcpWatcher{store: svc.Store()},
		attachLocker:  lock.For(svc.Store()),
		attachUploads: attachment.NewLimiter(attachment.DefaultMaxUploads),
	}, nil
}

// reload re-reads schema.yaml and rebuilds every metamodel-derived service
// against the store and searcher already open, returning the fresh [mcp.Deps]
// for the caller to publish.
//
// Rebuilding the whole stack — not just swapping the metamodel — is required
// for correctness. `create_entity`'s validation, enum checks and
// relation-target checks run through the entitymanager, which holds its OWN
// *metamodel.Metamodel, as do the compiled transitions, computed properties
// and automations. Refreshing only the read surfaces would leave writes
// validating against the old schema: a half-updated server is harder to
// diagnose than one that never updated at all.
//
// The store and searcher are deliberately reused. They hold the entity data and
// its index, neither of which a schema edit invalidates; reopening them would
// mean a full search reindex on every keystroke-triggered save.
//
// A schema that fails to load or assemble leaves the previous services in
// place and returns the error. The caller logs it and keeps serving — a broken
// intermediate save while the operator is mid-edit must not take the session
// down.
func (s *mcpServices) reload() (relamcp.Deps, error) {
	// Read the current assembly under the lock, then build the successor
	// OUTSIDE it. Assembly re-reads the schema, compiles transitions and
	// computed properties and starts a job queue; holding mu across all of
	// that would make a shutdown arriving mid-reload wait for it.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		// Shutdown won the race with a file event. Returning an error rather
		// than assembling is the whole point: watchSchema logs and moves on.
		return relamcp.Deps{}, errors.New("reload schema: services are closed")
	}
	old := s.svc
	s.mu.Unlock()

	base, err := appbuild.NewSharedBase(old.Base().Config(), appbuild.WithACL(acl.NopACL{}))
	if err != nil {
		return relamcp.Deps{}, fmt.Errorf("reload schema: %w", err)
	}

	// searchCloser is nil on purpose: the closer belongs to the ORIGIN
	// assembly, which retains it and closes it at shutdown. Handing it to the
	// successor too would give two bundles the same closer and close it twice.
	next, err := base.ForReassembly().Assemble(old.Store(), old.Searcher(), old.VisibleSearcher(), nil)
	if err != nil {
		return relamcp.Deps{}, fmt.Errorf("reload schema: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Re-check: Close may have run, or another reload may have landed, while
	// this one was assembling. Either way the successor is already obsolete
	// and must be retired rather than published — otherwise its job queue and
	// mail worker outlive the process's teardown.
	if s.closed || s.svc != old {
		next.CloseAssembly()
		return relamcp.Deps{}, errors.New("reload schema: superseded before publish")
	}

	// Retire the superseded assembly's background workers (job queue, mail,
	// GC sweep) WITHOUT closing the store or searcher the new one now uses.
	old.CloseAssembly()

	s.svc = next
	return s.deps(), nil
}

// watchSchema subscribes to schema.yaml and publishes a rebuilt dependency
// bundle to srv on every change, so an operator editing the schema mid-session
// sees new entity types, enum values, relation targets and validation rules
// without restarting the server (TKT-NU247U).
//
// Watching is a convenience, not a startup requirement: a backend that cannot
// watch (or a subscribe failure) is logged and the server runs with the schema
// it booted with — exactly the pre-TKT-NU247U behavior.
func (s *mcpServices) watchSchema(srv *relamcp.Server) {
	// One snapshot for both reads (CLAUDE.md: capture state once per operation).
	svc := s.current()
	sub, ok := svc.Config().(config.Subscriber)
	if !ok {
		slog.Debug("mcp: config loader does not support watching; schema hot-reload disabled")
		return
	}

	// Subscribe to the schema file that was actually discovered — a project
	// on the legacy metamodel.yaml name must watch that file, not the
	// canonical one it does not have.
	name := filepath.Base(svc.Paths().SchemaPath)

	stop, err := sub.Subscribe(context.Background(), name, func() {
		deps, err := s.reload()
		if err != nil {
			// Keep serving the last-good schema. A save mid-edit is
			// routinely unparseable and must not end the session.
			slog.Warn("mcp: schema reload failed; keeping previous schema", "error", err)
			return
		}
		if err := srv.ReloadDeps(deps); err != nil {
			slog.Warn("mcp: schema reload rejected; keeping previous schema", "error", err)
			return
		}
		// The filename is not logged: it is derived from a discovered path,
		// and the operator already knows which schema this project has. The
		// event is what matters.
		slog.Info("mcp: schema reloaded")
	})
	if err != nil {
		slog.Warn("mcp: schema watcher not started; hot-reload disabled", "error", err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		// Close ran while we were subscribing; nothing will read the handle.
		stop()
		return
	}
	s.stopSchemaWatch = stop
}

// Deps flattens the per-project services into the focused [mcp.Deps]
// the MCP server consumes. Built once at wiring time; the resulting
// value holds domain types only, so the server has no path back to
// this struct or to any composition-root aggregate.
// The three read handles come from [appbuild.Services.GatedReads] rather
// than the raw accessors. Under this command's NopACL wiring they ARE the
// raw store / tracer / validator, so `rela mcp` behaves exactly as before —
// but every MCP read surface (tools, resources, prompts, analyze)
// now reaches the graph through one seam that a networked wiring can
// substitute, instead of each handler holding the store directly. That is
// what makes the remote transport a wiring change rather than a rewrite of
// 34 handlers (TKT-UIR41P).
func (s *mcpServices) Deps() relamcp.Deps {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deps()
}

// deps builds the bundle from the current services. Caller holds mu.
func (s *mcpServices) deps() relamcp.Deps {
	reads := s.svc.GatedReads()
	deps := relamcp.Deps{
		Attachments:   s.attachmentDeps(),
		Store:         reads.Reader,
		Meta:          s.svc.Meta(),
		Tracer:        reads.Tracer,
		Searcher:      reads.Searcher,
		Validator:     reads.Validator,
		EntityManager: s.svc.EntityManager(),
		Config:        s.svc.Config(),
		LuaWriteDeps:  s.svc.LuaWriteDeps(),
		LuaCache:      s.svc.ScriptEngine().LuaCache(),
		Watcher:       s.watcher,
		ProjectRoot:   s.svc.Paths().Root,
	}
	if reads.Traversals != nil {
		deps.Traversals = reads.Traversals
	}
	return deps
}

// attachmentDeps wires the MCP attachment tools like `rela attach`: raw
// store, no command runner (so a configured scan rejects the upload), and the
// store's backstop as the size limit, capped at [relamcp.MaxUploadBytes]. The
// snapshot is built once per assembly because the metamodel only changes on
// reload, which rebuilds the Deps. Caller holds mu.
func (s *mcpServices) attachmentDeps() relamcp.AttachmentDeps {
	snap, err := relamcp.NewAttachmentSnapshot(s.svc.Store(), s.svc.EntityManager(), s.attachLocker, s.svc.ACL(),
		appbuild.TwinOwnership(s.svc), s.svc.Meta(), nil, store.MaxAttachmentBytes)
	return relamcp.AttachmentDeps{
		Snapshot:   func() (relamcp.AttachmentSnapshot, error) { return snap, err },
		Uploads:    s.attachUploads,
		Authorizer: s.svc.ACL(),
		Audit:      s.svc.Audit(),
	}
}

// Close stops the watcher and releases the underlying services (store
// then search backend, in that order). Safe to call repeatedly — both
// the watcher and [appbuild.Services.Close] are idempotent.
func (s *mcpServices) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Set before anything is torn down: a reload callback already blocked on
	// mu must observe this and abort rather than rebuild on a dead store.
	s.closed = true
	if s.stopSchemaWatch != nil {
		s.stopSchemaWatch()
		s.stopSchemaWatch = nil
	}
	if s.watcher != nil {
		s.watcher.Stop()
	}
	// Two steps, because after a reload the current assembly and the one that
	// owns the shared resources are different objects.
	//
	// The CURRENT assembly holds this generation's background services (job
	// queue, mail, GC sweep) and nothing else that is exclusively its own —
	// its store and searcher are the origin's. Stop those workers only.
	//
	// The ORIGIN assembly owns the store and the search closer, so it gets the
	// full Close. When no reload has happened the two are the same object, and
	// Close's internal sync.Once makes the pair collapse to exactly one
	// teardown.
	if s.svc != s.origin {
		s.svc.CloseAssembly()
	}
	return s.origin.Close()
}

// --- Watcher adapter ---

// storeStartStopper is the optional capability MCP needs from the
// store to start / stop its file watcher. Only fsstore implements it;
// in-memory store backends (memstore, used under //go:build
// memorybackend) cannot watch a filesystem and therefore opt out.
// The adapter silently no-ops in that case — see [mcpWatcher.Start]
// for the operator-visible warning log.
type storeStartStopper interface {
	StartWatching() error
	StopWatching()
}

// mcpWatcher wraps the store's file watcher to satisfy mcp.Watcher.
// Pause/Resume are no-ops today: fsstore's external watcher does not
// expose pause/resume (it relies on echoTracker self-echo suppression
// to ignore the store's own writes during rename). Keeping the
// methods in the interface preserves the existing API surface and
// leaves room for a future ExtraDirs/ExtraFiles watcher with pause
// semantics.
type mcpWatcher struct {
	store    store.Store
	onChange func()
}

func (w *mcpWatcher) Start(onChange func()) error {
	w.onChange = onChange
	sw, ok := w.store.(storeStartStopper)
	if !ok {
		// Backend doesn't watch (memstore under -tags memorybackend);
		// MCP change notifications will not fire. Warn so operators
		// running a non-FS build see this rather than silently
		// wondering why subscriptions never deliver.
		slog.Warn("mcp: store backend does not support file watching; change notifications are disabled")
		return nil
	}
	return sw.StartWatching()
}

func (w *mcpWatcher) Stop() {
	if sw, ok := w.store.(storeStartStopper); ok {
		sw.StopWatching()
	}
}

func (w *mcpWatcher) Pause()  {}
func (w *mcpWatcher) Resume() {}
