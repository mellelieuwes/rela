// Package appbuild assembles the focused services every project
// entry point (rela-server, rela-desktop, future bindings) needs from
// a project directory. It replaces the legacy workspace.Discover path
// for those entry points: callers receive a [Services] holding
// individually-constructed collaborators (store, metamodel,
// entitymanager, searcher, tracer, validator, templater, config
// loader, state KV) rather than a god-object.
//
// What's not here, and why:
//
//   - lua.WriteDeps: derived per-invocation from the static lua read
//     deps plus the per-call write handle. Built by callers that
//     actually invoke scripts (scheduler tick, script command,
//     automation cascade) — see [Services.LuaWriteDeps].
//   - lua.Cache: an implementation detail of *script.Engine. Callers
//     that need it ask the engine via [Services.ScriptEngine].
//   - File watching: each domain owns its own watch story
//     (fsstore self-watches; dataentry subscribes to data-entry.yaml).
//     [Services] has no watcher methods.
package appbuild

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/affordances"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/autocascade"
	"github.com/Sourcehaven-BV/rela/internal/automation"
	"github.com/Sourcehaven-BV/rela/internal/caldavalias"
	"github.com/Sourcehaven-BV/rela/internal/comments"
	"github.com/Sourcehaven-BV/rela/internal/computed"
	"github.com/Sourcehaven-BV/rela/internal/config"
	"github.com/Sourcehaven-BV/rela/internal/datamigration"
	"github.com/Sourcehaven-BV/rela/internal/datamigration/memmigstate"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/jobs"
	"github.com/Sourcehaven-BV/rela/internal/lua"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/project"
	"github.com/Sourcehaven-BV/rela/internal/relresolve"
	"github.com/Sourcehaven-BV/rela/internal/schedulerstate"
	"github.com/Sourcehaven-BV/rela/internal/schedulerstate/kvstate"
	"github.com/Sourcehaven-BV/rela/internal/scopes"
	"github.com/Sourcehaven-BV/rela/internal/script"
	"github.com/Sourcehaven-BV/rela/internal/search"
	"github.com/Sourcehaven-BV/rela/internal/state"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/storage"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/templating"
	"github.com/Sourcehaven-BV/rela/internal/tracer"
	"github.com/Sourcehaven-BV/rela/internal/userstate"
	"github.com/Sourcehaven-BV/rela/internal/userstate/kvuserstate"
	"github.com/Sourcehaven-BV/rela/internal/userstate/memuserstate"
	"github.com/Sourcehaven-BV/rela/internal/validator"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
	"github.com/Sourcehaven-BV/rela/internal/worlds"
)

// Services exposes the focused collaborators a project entry point
// needs as method accessors. Construct via [Discover] or [New].
//
// Method form (not exported fields) is the established pattern in
// this codebase: it lets *Services satisfy consumer-side service
// interfaces — `scheduler.WorkspaceProvider`, the data-entry app's
// constructor inputs — through structural typing, without adapters
// at the wiring site.
//
// Every exported method is a one-line accessor for a collaborator this facade
// constructs; the count tracks the number of wired services, not an accreting
// public API. Versions() (TKT-N0IKN9) took it to 21 — a documented facade
// exception, not a ratchet target. ScheduledLuaWriteDeps() (TKT-ZF2DTV) takes
// it to 22: it is a scheduler.WorkspaceProvider interface method, so it must be
// exported; the two redactor-parameterized variants behind it are unexported
// precisely to keep this surface from growing by three. CalDAVAliases()
// (TKT-WAA092) takes it to 23: the alias service is constructed here and
// consumed by cmd/rela-server when wiring the data-entry App, so it has to
// cross the package boundary — there is no in-package caller to hide it behind.
// GatedReads() (TKT-UIR41P) takes it to 24: it returns the ACL-bound read
// handles an identity-bearing consumer needs, and it is deliberately ONE method
// returning a bundle rather than three accessors — the three handles must come
// from the same gate to stay consistent, and splitting them would both grow this
// surface by three and let a caller mix a gated reader with a raw tracer.
// UserState() (TKT-CXD0A4) takes it to 25, for the same reason as the others:
// the backend is chosen here (per build tag) and handed to the App at the
// wiring site.
//
// The exported count is the service-accessor surface: Services IS the wiring
// facade, so each new subsystem it composes adds one getter. Ratchet this down
// by splitting the bundle (TKT-N0IKN9), not by hiding an accessor.
//
// Develop raised this to 26; the three recipient-scoped scheduler methods
// take it to 29. They are exported because scheduler consumes them through
// narrow capability interfaces; they do not add general Services getters.
// Comments() (TKT-FIO205) takes it to 31: the commentary service is chosen and
// constructed here and handed to the App at the wiring site, exactly like
// CalDAVAliases() and UserState(). It is one more instance of the pattern this
// comment already describes — a new subsystem composed by the facade — not a
// new kind of growth, and the fix remains splitting the bundle (TKT-N0IKN9)
// rather than hiding the getter.
//
// 30 → 33 (TKT-NU247U): [Services.CloseAssembly] and [Services.Base], the two
// halves of re-assembling a base against an already-open store, plus the
// getter develop added alongside them. All are lifecycle operations on THIS
// bundle rather than new getters onto its contents, so they belong here and
// not on a narrower type.
//
// 33 → 34 (TKT-XCJ0Y2): [Services.MigState], the per-store migration record.
// It is a genuine sibling of [Services.State] rather than a getter onto it —
// state.KV is node-local cache, this describes what the CONTENT conforms to,
// and the two have different backends for that reason. The CLI needs it to
// build the `rela migrate` commands, and this bundle is the only boundary it
// can cross. Still a ratchet target under TKT-N0IKN9.
//
// 34 → 35 (BUG-TKL08E): [Services.SchedulerState], a scheduler.WorkspaceProvider
// method. The run-state backend is chosen here to match the job queue's reach,
// the same per-recipe choice as [Services.MigState].
//
//plimsoll:max-exported-methods=35
type Services struct {
	fs    storage.FS
	paths *project.Context
	meta  *metamodel.Metamodel
	store store.Store
	// worlds is the compiled world set, carried so a caller can build a
	// world-bound read surface (WorldSurface). Tenant-independent and
	// metamodel-derived, like meta — it is copied from the SharedBase
	// rather than recompiled per assembly.
	worlds worlds.Compiled
	// versions is the content-versioning service (history reads, version writes,
	// purge), a separate concern injected by the backend recipe — nil on builds
	// without versioning (fs/mem; fsstore uses git). Consumers bind the narrow
	// sub-interface they need; the umbrella is the nil-able field type only.
	versions store.VersionService
	searcher search.Searcher
	// visibleSearcher is the ACL-scoped search seam (TKT-BA8BSX):
	// the generic search.NewVisible wrapper on the fs/memory builds,
	// the pgstore-native implementation on the postgres build.
	visibleSearcher search.VisibleSearcher
	userState       userstate.Store
	entityManager   *entitymanager.Manager
	tracer          tracer.Tracer
	validator       validator.Validator
	templater       templating.Templater
	cfgLoader       config.Loader
	stateKV         state.KV
	migState        datamigration.StateStore
	schedulerState  schedulerstate.Store
	// jobQueue is the background-job seam (TKT-YOED3R). Its backend is a
	// per-tier choice made by the recipe: ephemeral in-process on fs/mem,
	// durable PostgreSQL on the postgres build. Torn down in Close.
	jobQueue      jobs.Queue
	caldavAliases *caldavalias.Service
	// comments is the commentary layer (internal/comments). Nil when the
	// metamodel declares no `comments:` block — the feature then does not
	// exist, and the data-entry app serves no comment routes.
	comments     *comments.Service
	scriptEngine *script.Engine
	searchCloser io.Closer
	acl          acl.ACL
	// aclDeclarative is set when buildACL constructs a Declarative; nil
	// for NopACL, ReadOnlyACL, or when Declarative construction fails.
	aclDeclarative *acl.Declarative
	aclPolicy      *acl.Policy
	audit          audit.Audit
	// mailStop drains and stops the mail worker. Per-assembled like gcStop,
	// and bounded by the outbox's drain timeout so it cannot hang shutdown.
	mailStop func()
	mail     *mailRuntime

	// gcStop terminates this store's data-migration GC sweep goroutine
	// (TKT-0C57FS). Per-assembled, torn down in Close like searchCloser.
	gcStop func()
	// fieldRedactor applies field-level `visible:` policy to the unattended
	// and identity-bearing read paths (TKT-425426). Never nil after
	// construction: [visibility.NopRedactor] when no policy declares
	// affordance grants, so the no-policy path stays byte-identical.
	//
	// It is a FIELD, not an accessor, on purpose. Services sits at its
	// plimsoll max-exported-methods ceiling, and the underlying resolver
	// must be fully built before it is shared — [affordances.PolicyResolver.WithMachines]
	// is the one mutator on an otherwise-immutable value and is safe only
	// during single-threaded wiring. Constructing here, once, preserves that.
	fieldRedactor visibility.FieldRedactor

	// base is the SharedBase this Services was assembled from. Retained so a
	// host can build a successor base from the same Config (see
	// [Services.Base]). Nil for a NewFromCollaborators-built Services.
	base *SharedBase

	closeOnce sync.Once
	closeErr  error
	// assemblyCloseOnce guards the per-assembly teardown so that a
	// CloseAssembly followed by a Close (the reload path's final shutdown)
	// stops the background services exactly once.
	assemblyCloseOnce sync.Once
}

// FS returns the project filesystem.
func (s *Services) FS() storage.FS { return s.fs }

// Paths returns the project context (root, metamodel path, etc.).
func (s *Services) Paths() *project.Context { return s.paths }

// Base returns the [SharedBase] this Services was assembled from, or nil when
// it was built by [NewFromCollaborators] (which takes pre-built collaborators
// and so has no base).
//
// Exposed for hosts that re-assemble: a schema hot-reload needs the base's
// [SharedBase.Config] to build a successor base against the same project
// inputs (TKT-NU247U).
func (s *Services) Base() *SharedBase { return s.base }

// Meta returns the loaded metamodel.
func (s *Services) Meta() *metamodel.Metamodel { return s.meta }

// Store returns the authoritative store.
func (s *Services) Store() store.Store { return s.store }

// Versions returns the content-versioning service, or nil on a build without
// versioning (fs/mem). Consumers must nil-check and bind the narrow sub-interface
// they use (history read, purge, …) rather than the umbrella.
func (s *Services) Versions() store.VersionService { return s.versions }

// Searcher returns the search service (a sentinel error-searcher when
// the search backend failed to construct).
func (s *Services) Searcher() search.Searcher { return s.searcher }

// VisibleSearcher returns the ACL-scoped search seam. Per-backend: the
// generic scope-filter wrapper on the fs/memory builds, the native
// SQL-composed implementation on the postgres build.
func (s *Services) VisibleSearcher() search.VisibleSearcher { return s.visibleSearcher }

// newUserState builds the next-action per-user state backend.
//
// Durable by default, over the same state.KV that already holds the
// scheduler's last-run timestamps and the document render cache — this state
// has exactly that character (persists between runs, not tracked source), so
// it gets the same seam and the same `.rela/` home rather than a second
// persistence mechanism beside it.
//
// Falls back to the in-memory backend if the KV is unavailable, and says so.
// Losing a snooze across a restart is a repeated suggestion, not lost data;
// refusing to boot over it would be the wrong trade. The warning matters
// because the degradation is otherwise invisible — the feature keeps working
// and just forgets.
//
// Still build-agnostic: a multi-process deployment wants the postgres backend
// (this one is last-writer-wins across processes), and that becomes a
// per-recipe choice when it lands.
func newUserState(st store.Store, kv state.KV) userstate.Store {
	// A store-native backend wins where one exists (postgres): it is the only
	// one safe for the multi-process deployment, where the KV document's
	// whole-file rewrite is last-writer-wins. Resolved by a build-tagged
	// helper, mirroring versionServiceFor — the fs/memory builds return nil
	// and neither know nor link the postgres implementation.
	if s := storeUserStateFor(st); s != nil {
		return s
	}
	if kv == nil {
		slog.Warn("next-action state: no state KV; snoozes and mutes will not survive a restart")
		return memuserstate.New()
	}
	s, err := kvuserstate.New(kv)
	if err != nil {
		slog.Warn("next-action state: falling back to in-memory", "error", err)
		return memuserstate.New()
	}
	return s
}

// UserState returns the next-action per-user state backend (snooze / mute /
// cooldown).
//
// Build-agnostic today: every build gets the in-memory backend, because this
// state is disposable — losing it costs a user one repeated suggestion, not
// data. When a durable backend lands it becomes a per-recipe choice like the
// store, and only the recipes change; this accessor and its consumers do not.
func (s *Services) UserState() userstate.Store { return s.userState }

// EntityManager returns the production write path.
//
// The concrete *[entitymanager.Manager], not an interface: it is the sole
// implementation, and handing out the concrete type lets each consumer
// declare its own narrow write interface at its call site (CLAUDE.md
// "interfaces at the call site") and be satisfied structurally, with no
// wiring change here. It is also what lets internal/cli assert the
// id-preserving sync applier against a concrete type rather than
// interface-to-interface (TKT-IVSJV6).
func (s *Services) EntityManager() *entitymanager.Manager { return s.entityManager }

// ACL returns the authorization gate wired into entitymanager. Exposed
// so entry points (rela-server) can render operator warnings based on
// the active policy — e.g. "non-loopback bind without an acl.yaml" —
// without re-reading the file. The returned value is the exact ACL
// the Manager consults.
func (s *Services) ACL() acl.ACL { return s.acl }

// ACLPolicy returns the *acl.Policy parsed from acl.yaml, or nil when
// no policy file was present or the ACL was injected via [WithACL].
// Exposed so the data-entry server can build the policy-backed
// affordance resolver from the same policy the Manager authorizes
// against, without re-reading the file.
func (s *Services) ACLPolicy() *acl.Policy { return s.aclPolicy }

// ACLDeclarative returns the concrete *acl.Declarative when the wired
// ACL is one (the default when acl.yaml is present and parses); nil
// when ACL is NopACL or a test injected something else via [WithACL].
//
// Exposed so the affordance resolver can be built with the same
// Declarative the Manager uses — keeping the group expansion,
// containment, and Source attribution consistent across write authz
// and affordance verdicts. The field is set at construction time
// alongside `acl`; no runtime type assertion at the accessor.
func (s *Services) ACLDeclarative() *acl.Declarative { return s.aclDeclarative }

// Audit returns the audit sink wired into entitymanager. Exposed so
// dataentry handlers can emit `denied-write` rows for short-circuit
// rejections (affordance gates) that never reach the manager.
func (s *Services) Audit() audit.Audit { return s.audit }

// Tracer returns the graph-traversal service.
func (s *Services) Tracer() tracer.Tracer { return s.tracer }

// Validator returns the entity validator wired to the store + meta +
// Lua read deps.
func (s *Services) Validator() validator.Validator { return s.validator }

// Templater returns the entity/relation template service.
func (s *Services) Templater() templating.Templater { return s.templater }

// ScriptEngine returns the Lua script engine. Callers that need the
// engine's shared lua.Cache (for [lua.WithCache] when building runtimes
// directly) reach it via [script.Engine.LuaCache].
func (s *Services) ScriptEngine() *script.Engine { return s.scriptEngine }

// Config returns the project's data-entry config loader.
func (s *Services) Config() config.Loader { return s.cfgLoader }

// State returns the .rela cache-directory KV (or a sentinel error-KV
// when no cache dir is available).
func (s *Services) State() state.KV { return s.stateKV }

// MigState returns the per-store migration record: which migrations have run
// and the shape the content conforms to.
//
// Distinct from [Services.State] on purpose (TKT-XCJ0Y2). state.KV is
// node-local cache — the render cache, scheduler run stamps, user settings —
// and is gitignored on the filesystem tier. The migration record describes the
// CONTENT, so it has to travel with the content: a committed file in the
// project on fs, a row in the tenant's schema on postgres, a row in rela.db on
// sqlite.
func (s *Services) MigState() datamigration.StateStore { return s.migState }

// Jobs returns the background-job queue, already started.
//
// The return type deliberately omits [jobs.Lifecycle]: starting and stopping
// the queue is the composition root's concern, and a consumer that could Close
// it would be shutting background work down for every other subsystem. Services
// keeps the lifecycle handle privately and tears it down in Close.
//
// Register handlers at wiring time; the dispatcher resolves a handler per job,
// so registration after start is fine. Durability depends on the build —
// ephemeral on fs/desktop, durable on postgres.
//
// Nil: never — assemble fails rather than returning a Services with no
// queue, since a nil queue would turn every Enqueue into a panic at the
// call site rather than a wiring error here.
func (s *Services) Jobs() jobs.Client { return s.jobQueue }

// SchedulerState returns the scheduler's task and run records. It satisfies
// scheduler.WorkspaceProvider. The backend matches the job queue's reach:
// postgres when the queue is durable, kvstate over [Services.State] otherwise.
func (s *Services) SchedulerState() schedulerstate.Store { return s.schedulerState }

// CalDAVAliases is the CalDAV<->rela resource alias service. Never nil: the
// service is always constructed (an empty table is the normal first-run state),
// so consumers need no nil check.
func (s *Services) CalDAVAliases() *caldavalias.Service { return s.caldavAliases }

// Comments returns the commentary service, or nil when the metamodel declares
// no enabled `comments:` block.
//
// Nil means the feature does not exist for this project: the data-entry app
// serves no comment routes and no storage is created. Callers must nil-check
// rather than assume a usable service.
func (s *Services) Comments() *comments.Service { return s.comments }

// LuaReadDeps materializes the read-only Lua capability bundle with
// UNRESTRICTED reads — the operator-trust-boundary wiring used by the CLI
// and the docs runtime, where whoever runs the binary already has the
// project files (RR-17DMC).
//
// Request-scoped and scheduled callers must use Services.luaReadDepsFor
// instead, which binds reads to an identity (DEC-O59WM4).
//
// Cheap to call; rebuild per-runtime so future metamodel reloads propagate.
func (s *Services) LuaReadDeps() lua.ReadDeps {
	root := ""
	if s.paths != nil {
		root = s.paths.Root
	}
	return lua.ReadDeps{
		VisibleReader: visibility.Unrestricted(s.store),
		Tracer:        s.tracer,
		Searcher:      s.searcher,
		Meta:          s.meta,
		ProjectRoot:   root,
	}
}

// LuaReadDepsFor materializes a read bundle whose reads are ACL-bound to
// whatever principal is on the ctx AT CALL TIME (DEC-O59WM4). Use it for
// every identity-bearing path: data-entry requests, automations/cascades
// (which run on the triggering user's ctx), and scheduled tasks (which run
// on their `system:*` principal).
//
// redactor supplies field-level verdicts. Callers that have an affordance
// resolver (data-entry) pass one; callers that do not may pass
// [visibility.NopRedactor], which yields row-gating without field
// redaction — weaker, but never wrong, and the row gate is the part that
// keeps whole entities out of a prompt.
//
// Identity is deliberately NOT captured here: the scheduler stamps its
// principal on the per-task ctx while the deps bundle is built separately,
// so binding an identity at construction would make that principal
// invisible. The wrapped reader resolves it per call.
//
// Falls back to unrestricted reads when no Declarative ACL is configured —
// that is the NopACL path, byte-identical to pre-ACL behavior, not a
// bypass. A construction failure does NOT: it REFUSES, via
// [visibility.DenyReader] / [visibility.DenyTracer] (RR-GKCZO5). See
// [scriptEntityReader] for why an unattended path must not degrade to the
// raw store.
func (s *Services) luaReadDepsFor(redactor visibility.FieldRedactor) lua.ReadDeps {
	deps := s.LuaReadDeps()
	deps.VisibleReader = scriptEntityReader(s.store, s.aclDeclarative, redactor)
	deps.Tracer = scriptTracer(s.tracer, s.store, s.aclDeclarative, redactor)
	return deps
}

// scriptEntityReader returns the read-out handle for a script runtime: ACL
// bound to the ctx principal when a Declarative policy exists, otherwise
// the raw store (the NopACL path — byte-identical to pre-ACL behavior, not
// a bypass).
//
// When a policy IS configured but the gate cannot be built, this REFUSES
// (returns [visibility.DenyReader]) rather than degrading to the raw store
// (RR-GKCZO5). These deps back unattended paths — automation cascades and
// scheduled tasks — where quietly reverting to full-graph reads means an
// unbounded, silent disclosure into whatever the job sends onward. An
// operator who configured a policy has stated intent; honoring it by
// failing loudly beats ignoring it by failing open.
func scriptEntityReader(
	st store.Store, d *acl.Declarative, redactor visibility.FieldRedactor,
) lua.EntityReader {
	reader, _ := scriptReads(st, d, redactor)
	return reader
}

// scriptReads returns the principal-bound script reader and the traversal gate
// that answers `related(...)` under the SAME tier: unrestricted reads get
// [relresolve.Ungated], policy reads get the ctx principal's gate, and a
// refused reader gets a gate that refuses too. Deriving both in one place is
// what keeps a validation rule's traversal from seeing more than its reads.
func scriptReads(
	st store.Store, d *acl.Declarative, redactor visibility.FieldRedactor,
) (scriptReader lua.EntityReader, traversalGate relresolve.Gate) {
	if d == nil {
		// Named, not bare: this is the NopACL path and the single largest
		// ungated read surface in the tree, so it must show up in
		// `grep -rn visibility.Unrestricted` like every other one
		// (TKT-1WV50C).
		return visibility.Unrestricted(st), relresolve.Ungated
	}
	if redactor == nil {
		redactor = visibility.NopRedactor{}
	}
	gate, err := visibility.NewDeclarativeGate(d)
	if err != nil {
		slog.Error("appbuild: ACL gate unavailable; script reads REFUSED", "err", err)
		return visibility.DenyReader{}, refuseTraversal
	}
	reader, err := visibility.NewPolicyReader(gate, redactor, st)
	if err != nil {
		slog.Error("appbuild: policy reader unavailable; script reads REFUSED", "err", err)
		return visibility.DenyReader{}, refuseTraversal
	}
	sr, err := visibility.NewScriptReader(reader, st, gate)
	if err != nil {
		slog.Error("appbuild: script reader unavailable; script reads REFUSED", "err", err)
		return visibility.DenyReader{}, refuseTraversal
	}
	return sr, gate.GateTraversal
}

// refuseTraversal pairs with [visibility.DenyReader]: reads are refused, so
// traversals are too, as an error rather than "no match".
func refuseTraversal(context.Context, string, acl.TraversalHop) (*store.RelationPredicate, error) {
	return nil, fmt.Errorf("%w: script reads are refused", acl.ErrTraversalUnsupported)
}

// scriptTracer returns the traversal handle for a script runtime, wrapped
// in the visibility decorator when a Declarative policy exists. The trace
// bindings are identical either way — gating is entirely inside the
// decorator (hidden nodes pruned with their subtrees, paths through hidden
// intermediates withheld, titles falling back to IDs).
func scriptTracer(
	tr tracer.Tracer, st store.Store, d *acl.Declarative, redactor visibility.FieldRedactor,
) tracer.Tracer {
	if d == nil {
		return tr
	}
	if redactor == nil {
		redactor = visibility.NopRedactor{}
	}
	gate, err := visibility.NewDeclarativeGate(d)
	if err != nil {
		slog.Error("appbuild: ACL gate unavailable; traversal REFUSED", "err", err)
		return visibility.DenyTracer{}
	}
	vt, err := visibility.NewVisibleTracer(tr, gate, redactor, st)
	if err != nil {
		slog.Error("appbuild: visible tracer unavailable; traversal REFUSED", "err", err)
		return visibility.DenyTracer{}
	}
	return vt
}

// LuaWriteDeps materializes the read-write Lua capability bundle with
// UNRESTRICTED reads (see [Services.LuaReadDeps] for when that is right).
// EntityManager goes in as the concrete *entitymanager.Manager; the
// lua.WriteDeps.EntityManager field is narrower (lua.Mutator) and
// accepts any structural match.
func (s *Services) LuaWriteDeps() lua.WriteDeps {
	return lua.WriteDeps{
		ReadDeps:      s.LuaReadDeps(),
		EntityManager: s.entityManager,
	}
}

// LuaWriteDepsFor is [Services.LuaWriteDeps] with ACL-bound reads — the
// scheduler's wiring (its per-task principal governs what the script can
// see; privilege comes from acl.yaml assignments, never from task config).
// Writes are unaffected: they continue through entitymanager's own ACL,
// with rela.bypass_acl as the sole escalation.
func (s *Services) luaWriteDepsFor(redactor visibility.FieldRedactor) lua.WriteDeps {
	return lua.WriteDeps{
		ReadDeps:      s.luaReadDepsFor(redactor),
		EntityManager: s.entityManager,
	}
}

// ScheduledLuaWriteDeps satisfies scheduler.WorkspaceProvider. Reads are
// ACL-bound to the task's principal (stamped on the per-task ctx), with
// privileges coming from acl.yaml — a job sees what its identity may see,
// nothing more (DEC-O59WM4).
//
// Field-level `visible:` redaction APPLIES here (TKT-0XL8MF): a job whose
// identity may read `person` receives that entity with the same properties
// redacted as a human with the same role sees in the UI. This closed
// RR-7408F5, which documented the earlier row-gating-only behavior.
func (s *Services) ScheduledLuaWriteDeps() lua.WriteDeps {
	return s.luaWriteDepsFor(s.fieldRedactor)
}

// GatedReads returns the read handles bound to whatever principal is on the
// ctx AT CALL TIME — the reader, the traversal handle, and a validator whose
// candidate set comes from that same gated reader.
//
// This is the read bundle for an identity-bearing, non-HTTP consumer. The MCP
// server is the caller: its handlers, resources, prompts, analyze and export
// surfaces all read through the returned reader, so gating is decided once
// here rather than per handler (DEC-ZBI39P).
//
// The validator matters as much as the reader. `Services.Validator()` is built
// over the RAW store, which is right for the unattended paths that own it —
// but a validation rule evaluated for a requester must not read rows the
// requester cannot see, or a hidden value reaches a violation message
// (TKT-3FL2S6). So this builds a second validator over the gated reader.
//
// Identity is deliberately NOT captured here: the wrapped handles resolve the
// ctx principal per call, so ONE bundle serves every request. Under NopACL
// (no acl.yaml) these are the raw store/tracer — byte-identical to pre-ACL
// behavior, not a bypass. A construction failure REFUSES via
// visibility.DenyReader / DenyTracer rather than degrading to raw reads
// (RR-GKCZO5).
//
// Gating is BOTH row-level and field-level (TKT-425426): the reader prunes
// entities the principal may not see, and redacts `visible:`-hidden ENTITY
// PROPERTY values on the ones it returns. The validator is built over that same
// reader, so a violation message cannot quote a value the requester cannot read
// (the field-level half of TKT-3FL2S6).
//
// SCOPE: field redaction covers entity properties only. Relation meta is NOT
// redacted here — [gatedGraphReader.GetRelation] returns the raw edge once both
// endpoints pass the row gate, and relation-level
// `visible:` grants (acl.RelationGrant.Visible, honored on the dataentry wire
// via affordances.RelationFieldVerdicts) are not consulted. Relations are gated
// at the ROW level on both endpoints; their properties are not. Do not read the
// paragraph above as covering them. Tracked as TKT-0RBFN0 (IB-review #1 on
// PR #1400), which also covers ListRelations: that path IS row-gated, but
// visibility.PolicyReader implements only FilterRelations, so a surviving edge
// still carries all of its meta.
func (s *Services) GatedReads() GatedReadBundle {
	reader, gate := scriptReads(s.store, s.aclDeclarative, s.fieldRedactor)
	tr := scriptTracer(s.tracer, s.store, s.aclDeclarative, s.fieldRedactor)

	deps := s.LuaReadDeps()
	deps.VisibleReader = reader
	deps.Tracer = tr
	deps.Searcher = gatedSearcher(s.searcher, s.visibleSearcher, s.aclDeclarative, s.fieldRedactor, s.meta)

	return GatedReadBundle{
		Reader: gatedGraphReader{
			rows: reader, raw: s.store, gateEndpoints: s.aclDeclarative != nil,
		},
		Tracer:    tr,
		Validator: newValidator(reader, s.meta, deps, gate, s.store),
		Searcher:  deps.Searcher,
		LuaReads:  deps,
	}
}

// GatedReadBundle is the result of [Services.GatedReads]: the read handles an
// identity-bearing consumer needs, each ACL-bound to the ctx principal at call
// time.
type GatedReadBundle struct {
	Reader    GatedGraphReader
	Tracer    tracer.Tracer
	Validator validator.Validator

	// Searcher gates hits by the principal's read scope, drops hits that
	// matched only hidden properties, and clears the indexed title (see
	// [visibility.Searcher]). Take titles from Reader.
	Searcher search.Searcher

	// LuaReads is the Lua read bundle over the same gated reader, tracer and
	// searcher. It grants no capabilities and holds no elevated handle; a
	// writer runtime pairs it with an EntityManager.
	LuaReads lua.ReadDeps
}

// gatedSearcher returns the search handle for an identity-bearing consumer.
// Under NopACL it is the raw searcher (byte-identical to pre-ACL behavior). A
// construction failure REFUSES via [visibility.DenySearcher] rather than
// degrading to raw hits (RR-GKCZO5).
func gatedSearcher(
	raw search.Searcher, vs search.VisibleSearcher, d *acl.Declarative,
	redactor visibility.FieldRedactor, meta *metamodel.Metamodel,
) search.Searcher {
	if d == nil {
		return raw
	}
	if redactor == nil {
		redactor = visibility.NopRedactor{}
	}
	gate, err := visibility.NewDeclarativeGate(d)
	if err != nil {
		slog.Error("appbuild: ACL gate unavailable; search REFUSED", "err", err)
		return visibility.DenySearcher{}
	}
	types := make([]string, 0, len(meta.Entities))
	for name := range meta.Entities {
		types = append(types, name)
	}
	gs, err := visibility.NewSearcher(vs, gate, redactor, types)
	if err != nil {
		slog.Error("appbuild: gated searcher unavailable; search REFUSED", "err", err)
		return visibility.DenySearcher{}
	}
	return gs
}

// GatedGraphReader is the row-and-tally read surface returned by
// [Services.GatedReads]. Row reads are ACL-gated; the two counts are not —
// see [gatedGraphReader] for why.
type GatedGraphReader interface {
	GetEntity(ctx context.Context, id string) (*entity.Entity, error)
	ListEntities(ctx context.Context, q store.EntityQuery) iter.Seq2[*entity.Entity, error]
	GetRelation(ctx context.Context, from, relType, to string) (*entity.Relation, error)
	ListRelations(ctx context.Context, q store.RelationQuery) iter.Seq2[*entity.Relation, error]
	CountEntities(ctx context.Context, q store.EntityQuery) (int, error)
	CountRelations(ctx context.Context, q store.RelationQuery) (int, error)
}

// gatedGraphReader composes the ACL-gated row reader with the raw store for
// the two operations the gated reader does not provide.
//
// The split is deliberate, and matches the line `internal/dataentry` already
// draws (`analyzeService.relCounts` is documented "raw (ungated) on purpose"):
//
//   - GetEntity / ListEntities / ListRelations go through `rows`, so a hidden
//     entity is absent and a hidden edge is not listed.
//   - CountEntities / CountRelations go to the raw store. A count is
//     STRUCTURAL: it says how many rows of a declared type exist, never which.
//     Entity *existence* is the secret the row gate protects; an aggregate
//     tally of a type the metamodel already publishes is not.
//   - GetRelation answers not-found unless both endpoints pass the row gate,
//     then reads the raw store. Holding two ids is not the same as being
//     allowed to read them: without the endpoint check, a caller could learn
//     that a hidden entity exists and is linked (TKT-4QSZ8Y). The edge's meta
//     VALUES are not redacted: relations carry no field-level redaction on
//     this path today (see docs/acl-security.md and TKT-0RBFN0).
//
// If either judgement changes, this is the one type to fix.
type gatedGraphReader struct {
	rows lua.EntityReader
	raw  store.Store

	// gateEndpoints requires both endpoints of a GetRelation to be readable.
	// Off under NopACL, where every entity is readable and a dangling edge
	// must stay readable as before.
	gateEndpoints bool
}

func (g gatedGraphReader) GetEntity(ctx context.Context, id string) (*entity.Entity, error) {
	return g.rows.GetEntity(ctx, id)
}

func (g gatedGraphReader) ListEntities(
	ctx context.Context, q store.EntityQuery,
) iter.Seq2[*entity.Entity, error] {
	return g.rows.ListEntities(ctx, q)
}

func (g gatedGraphReader) ListRelations(
	ctx context.Context, q store.RelationQuery,
) iter.Seq2[*entity.Relation, error] {
	return g.rows.ListRelations(ctx, q)
}

func (g gatedGraphReader) GetRelation(
	ctx context.Context, from, relType, to string,
) (*entity.Relation, error) {
	if g.gateEndpoints && (!visibility.Readable(ctx, g.rows, from) || !visibility.Readable(ctx, g.rows, to)) {
		return nil, store.ErrNotFound
	}
	return g.raw.GetRelation(ctx, from, relType, to)
}

func (g gatedGraphReader) CountEntities(ctx context.Context, q store.EntityQuery) (int, error) {
	return g.raw.CountEntities(ctx, q)
}

func (g gatedGraphReader) CountRelations(ctx context.Context, q store.RelationQuery) (int, error) {
	return g.raw.CountRelations(ctx, q)
}

// Collaborators bundles the fully-built dependencies of a [Services]
// instance. Exposed so external test fixtures (`appbuildtest`) and
// alternative composition roots can assemble a Services without
// poking at unexported fields. Production callers go through [New] /
// [Discover] instead.
//
// Every field is required. [NewFromCollaborators] validates them. The
// production wiring builds a Services from a real filesystem, real
// metamodel, real entity manager, etc.; test fixtures supply
// in-memory equivalents (see `appbuildtest`). There is no production
// code path that runs without a complete Services — making any of
// these optional would force every downstream consumer to nil-check
// what it depends on.
//
// The one nuance: SearchCloser may be nil when the search backend
// does not own a closable resource (the error-Searcher placeholder
// has nothing to close).
type Collaborators struct {
	FS       storage.FS
	Paths    *project.Context
	Meta     *metamodel.Metamodel
	Store    store.Store
	Searcher search.Searcher
	// VisibleSearcher may be nil: the constructor then derives the
	// generic search.NewVisible(Searcher, Store) wrapper, which is the
	// correct implementation for every in-process store. Only wire it
	// explicitly to exercise a native implementation.
	VisibleSearcher search.VisibleSearcher
	EntityManager   *entitymanager.Manager
	Tracer          tracer.Tracer
	Validator       validator.Validator
	Templater       templating.Templater
	CfgLoader       config.Loader
	StateKV         state.KV
	ScriptEngine    *script.Engine
	ACL             acl.ACL
	Audit           audit.Audit

	// Declarative is the optional concrete *acl.Declarative the test
	// is wiring. When non-nil, [Services.ACLDeclarative] returns it;
	// the affordance resolver path then composes against the same
	// resolver the write path uses (RR-FGJR). When nil — typical when
	// ACL is [acl.NopACL] or [acl.ReadOnlyACL] —
	// [Services.ACLDeclarative] returns nil and the dataentry
	// resolver selector falls through to [NopFieldVerdictResolver].
	//
	// If you set Declarative, ACL must reference the same value
	// (typically ACL == Declarative). The constructor enforces this.
	Declarative *acl.Declarative

	// SearchCloser may be nil — see type doc.
	SearchCloser io.Closer
}

// NewFromCollaborators assembles a [Services] from pre-built
// collaborators. Used by external test packages that want to swap
// individual collaborators (e.g. inject a fake store) without going
// through the full production wiring of [New].
//
// Returns an error when any required field is nil. See [Collaborators]
// for the contract.
func NewFromCollaborators(c Collaborators) (*Services, error) {
	if c.FS == nil {
		return nil, errors.New("appbuild.NewFromCollaborators: FS is required")
	}
	if c.Paths == nil {
		return nil, errors.New("appbuild.NewFromCollaborators: Paths is required")
	}
	if c.Meta == nil {
		return nil, errors.New("appbuild.NewFromCollaborators: Meta is required")
	}
	if c.Store == nil {
		return nil, errors.New("appbuild.NewFromCollaborators: Store is required")
	}
	if c.Searcher == nil {
		return nil, errors.New("appbuild.NewFromCollaborators: Searcher is required")
	}
	if c.EntityManager == nil {
		return nil, errors.New("appbuild.NewFromCollaborators: EntityManager is required")
	}
	if c.Tracer == nil {
		return nil, errors.New("appbuild.NewFromCollaborators: Tracer is required")
	}
	if c.Validator == nil {
		return nil, errors.New("appbuild.NewFromCollaborators: Validator is required")
	}
	if c.Templater == nil {
		return nil, errors.New("appbuild.NewFromCollaborators: Templater is required")
	}
	if c.CfgLoader == nil {
		return nil, errors.New("appbuild.NewFromCollaborators: CfgLoader is required")
	}
	if c.StateKV == nil {
		return nil, errors.New("appbuild.NewFromCollaborators: StateKV is required")
	}
	if c.ScriptEngine == nil {
		return nil, errors.New("appbuild.NewFromCollaborators: ScriptEngine is required")
	}
	if c.ACL == nil {
		return nil, errors.New("appbuild.NewFromCollaborators: ACL is required")
	}
	if c.Audit == nil {
		return nil, errors.New("appbuild.NewFromCollaborators: Audit is required (use audit.Nop{} to opt out)")
	}
	if c.Declarative != nil && c.ACL != acl.ACL(c.Declarative) {
		return nil, errors.New("appbuild.NewFromCollaborators: when Declarative is set, ACL must reference the same value")
	}
	var aclPolicy *acl.Policy
	if c.Declarative != nil {
		aclPolicy = c.Declarative.Policy()
	}
	visible := c.VisibleSearcher
	if visible == nil {
		v, err := search.NewVisible(c.Searcher, c.Store)
		// coverage-ignore-start: defensive: NewVisible only errors on nil Searcher/Store or a Store that isn't an
		// EntityReader; c.Searcher/c.Store
		// are validated non-nil above and every store.Store is an EntityReader
		if err != nil {
			return nil, fmt.Errorf("appbuild.NewFromCollaborators: derive VisibleSearcher: %w", err)
		}
		// coverage-ignore-end
		visible = v
	}
	// Compile worlds from the supplied metamodel so this construction path
	// offers the same world surface as the base-assembly path. A failure
	// here is a metamodel error and must surface, not silently yield a
	// Services whose WorldSurface always 404s.
	compiledWorlds, err := worlds.Compile(c.Meta)
	if err != nil {
		return nil, fmt.Errorf("appbuild.NewFromCollaborators: compile worlds: %w", err)
	}
	fieldRedactor, err := buildFieldRedactor(c.Meta, c.Store, c.Declarative)
	if err != nil {
		return nil, fmt.Errorf("appbuild.NewFromCollaborators: %w", err)
	}
	return &Services{
		fs:              c.FS,
		paths:           c.Paths,
		meta:            c.Meta,
		worlds:          compiledWorlds,
		store:           c.Store,
		searcher:        c.Searcher,
		visibleSearcher: visible,
		userState:       newUserState(c.Store, c.StateKV),
		entityManager:   c.EntityManager,
		tracer:          c.Tracer,
		validator:       c.Validator,
		templater:       c.Templater,
		cfgLoader:       c.CfgLoader,
		stateKV:         c.StateKV,
		// This construction path has no project directory to commit a file
		// into, so the migration record is in-memory. Callers that need a
		// durable one build Services through New/Discover.
		migState:       memmigstate.New(),
		scriptEngine:   c.ScriptEngine,
		searchCloser:   c.SearchCloser,
		acl:            c.ACL,
		aclDeclarative: c.Declarative,
		aclPolicy:      aclPolicy,
		audit:          c.Audit,
		fieldRedactor:  fieldRedactor,
	}, nil
}

// buildFieldRedactor constructs the field-level `visible:` redactor shared by
// every appbuild-wired read path (TKT-0XL8MF). Before this existed, appbuild
// could not build an affordance resolver at all, so the unattended and
// identity-bearing paths were ROW-gated only and every property of a readable
// entity came through — including ones a human with the same role sees
// redacted in the UI.
//
// Returns [visibility.NopRedactor] (hide nothing) when no policy is wired or
// the policy declares no affordance grants. Those are ordinary configurations
// — NopACL, or an acl.yaml with only row rules — not failures, and the
// no-policy path must stay byte-identical to pre-ACL behavior.
//
// A policy that DOES declare grants but fails to compile is an error, not a
// fallback: it is returned to the caller, which aborts construction. Degrading
// to NopRedactor there would silently serve unredacted properties to an
// operator who had asked for redaction — the fail-open this whole path exists
// to prevent (RR-GKCZO5).
//
// The resolver is built to completion here — including WithMachines — before
// it escapes into a redactor, which is what keeps its documented
// "safe for concurrent use after construction" guarantee true.
func buildFieldRedactor(
	meta *metamodel.Metamodel, st store.Store, d *acl.Declarative,
) (visibility.FieldRedactor, error) {
	if d == nil {
		return visibility.NopRedactor{}, nil
	}
	// Read the policy through Declarative, never a second channel — the two
	// must not drift (RR-WTLD).
	policy := d.Policy()
	if policy == nil || !policy.HasAffordanceGrants() {
		return visibility.NopRedactor{}, nil
	}

	resolver, err := affordances.New(meta, storeRelationLookup{st: st}, d,
		affordances.WithTraversals(ungatedBinder(meta, st)))
	if err != nil {
		return nil, fmt.Errorf("appbuild: compiling acl.yaml affordance predicates: %w", err)
	}
	machines, err := statemachine.Compile(meta, statemachine.WithTraversals(ungatedBinder(meta, st)))
	if err != nil {
		return nil, fmt.Errorf("appbuild: compiling state machines: %w", err)
	}
	resolver.WithMachines(machines)

	redactor, err := visibility.NewPolicyRedactor(resolver)
	if err != nil {
		return nil, fmt.Errorf("appbuild: build field redactor: %w", err)
	}
	return redactor, nil
}

// buildAutomation wires the automation engine + cascade runner from
// the metamodel. Returns (nil, nil, nil) when the metamodel declares
// no automations — Manager treats that as "automation disabled".
//
// A condition's `related(...)` is answered ungated from st: an automation is
// system policy, so what it sees must not depend on who made the write.
func buildAutomation(meta *metamodel.Metamodel, st store.Store) (*automation.Engine, *autocascade.Runner, error) {
	if len(meta.Automations) == 0 {
		return nil, nil, nil
	}
	traversals, err := relresolve.NewStoreBinder(meta, relresolve.Ungated, st)
	if err != nil {
		return nil, nil, fmt.Errorf("build automation engine: %w", err)
	}
	autoEngine, err := automation.NewEngineFromMetamodel(meta, meta.Automations, automation.WithTraversals(traversals))
	if err != nil {
		return nil, nil, fmt.Errorf("build automation engine: %w", err)
	}
	cascadeRunner, err := autocascade.New(autocascade.Deps{Engine: autoEngine})
	// coverage-ignore-start: defensive: autocascade.New only errors on a nil Engine; autoEngine is freshly built by
	// NewEngineFromMetamodel just
	// above and is never nil
	if err != nil {
		return nil, nil, fmt.Errorf("build autocascade runner: %w", err)
	}
	// coverage-ignore-end
	return autoEngine, cascadeRunner, nil
}

// Option configures construction of a [Services] bundle. Options are
// optional; production callers typically pass none. Used by entry
// points that need to swap a focused collaborator at startup —
// today, `rela-server --read-only` injects [acl.ReadOnlyACL] via
// [WithACL].
type Option func(*options)

type options struct {
	acl acl.ACL

	// databaseURL, when non-empty, supplies the DSN that [Discover] would
	// otherwise read from the environment. Consumed by [Discover] only:
	// [New] takes the DSN from [Config.DatabaseURL], because a caller
	// building a Config already decides where the data lives.
	databaseURL string
}

// WithACL overrides the auto-loaded ACL with the supplied
// implementation. Default behavior (no option) is to load `acl.yaml`
// from the project root via [acl.LoadPolicy]; on `os.ErrNotExist`
// the default falls back to [acl.NopACL] (allow-all). WithACL is
// how `rela-server --read-only` injects [acl.ReadOnlyACL]: the
// option always wins, even when an `acl.yaml` is present, so the
// flag is an unconditional override.
//
// Tests should prefer NewForTest + WithTestACL over driving this
// path directly.
func WithACL(a acl.ACL) Option {
	return func(o *options) { o.acl = a }
}

// WithDatabaseURL supplies the PostgreSQL DSN explicitly, instead of letting
// [Discover] read it from $RELA_DATABASE_URL. The option always wins over the
// environment, so a caller that knows where a project's data lives never has to
// mutate process state to say so.
//
// This exists so that *which database a project uses* is an argument rather than
// an ambient property. Two [Services] can then be constructed in one process
// against different databases — impossible via the environment, which is global
// and shared. `rela-desktop` already builds a fresh bundle per project switch,
// and a future multi-tenant server resolves a DSN per tenant.
//
// The invariant this must not break: a DSN carries a password, so it must never
// reach a command line. Passing one here in Go code is fine — sourcing it from a
// flag is not. See [Config.DatabaseURL].
//
// Ignored by the FS and memory builds, which have no DSN.
func WithDatabaseURL(dsn string) Option {
	return func(o *options) { o.databaseURL = dsn }
}

// resolveDatabaseURL decides which DSN [Discover] hands to [New]: an explicit
// [WithDatabaseURL] wins, otherwise $RELA_DATABASE_URL. getenv is injected so
// the precedence is testable without mutating process environment — which is
// itself the ambient-state problem this seam removes.
//
// Split out of Discover because on the FS and memory builds the resolved DSN is
// never consumed, so a test could not otherwise distinguish "option honored"
// from "option ignored" without a live PostgreSQL.
func resolveDatabaseURL(opts []Option, getenv func(string) string) string {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.databaseURL != "" {
		return o.databaseURL
	}
	return getenv("RELA_DATABASE_URL")
}

// loadACLPolicy reads `acl.yaml` from projectRoot and returns the
// parsed [*acl.Policy], or (nil, nil) when the file is genuinely
// missing (in which case the caller falls back to NopACL — no policy
// declared, no access control desired).
//
// A malformed acl.yaml returns a non-nil error: silently degrading to
// NopACL on parse failure would invert the operator's intent and boot
// the server allow-all on a typo. Per CLAUDE.md "Constructors reject
// nil required fields ... never substitute a no-op silently."
//
// Separated from [buildACL] so the caller can open the store between
// the two phases — v1's [acl.Declarative] needs a [acl.Graph] backed
// by the store.
func loadACLPolicy(projectRoot string) (*acl.Policy, error) {
	policy, err := acl.LoadPolicy(filepath.Join(projectRoot, "acl.yaml"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// File genuinely absent → caller falls back to NopACL by
			// intent. Returning (nil, nil) keeps the call site's
			// "if aclPolicy != nil" check simple; a sentinel error
			// would force every caller to errors.Is unnecessarily.
			return nil, nil //nolint:nilnil // (nil, nil) = no policy intended; caller checks aclPolicy != nil
		}
		return nil, fmt.Errorf("appbuild: load acl.yaml: %w", err)
	}
	// Debug, not Info: this is wiring shared by every CLI command, and an
	// INFO line on each `rela list` trains operators to filter the logger —
	// which would defeat the point. `rela acl audit` is the surface that
	// reports the block at operator-facing volume.
	if types := policy.RelationWriteGrantTypes(); len(types) > 0 {
		slog.Debug("acl: relation_grants active", "relation_types", types)
	}
	return policy, nil
}

// buildACL constructs the production ACL from a policy + a store. The
// store backs the [acl.Graph] adapter the resolver needs for member-of
// walks and ancestor probes. A nil policy yields [acl.NopACL]
// (allow-all) — the absence of acl.yaml is taken as "no access control
// intended," which is different from a malformed acl.yaml (handled in
// [loadACLPolicy]).
//
// Returns both the [acl.ACL] interface (consumed by entitymanager) and
// the concrete *acl.Declarative (consumed by the affordance resolver).
// When the result is a Declarative, both returns reference the same
// value — making the "write authz and affordance verdicts share one
// resolver" invariant textual rather than implicit-via-type-assertion.
// For NopACL the second return is nil.
//
// An error from [acl.NewDeclarative] is propagated, not downgraded:
// the operator wrote a policy and the resolver couldn't accept it; the
// server must fail to boot rather than silently allow-all.
// metamodelView adapts *metamodel.Metamodel to acl.MetamodelView. The acl
// package deliberately does not depend on internal/metamodel
// (.go-arch-lint.yml), so it declares the narrow view it needs and the
// wiring site — which imports both — supplies this adapter. Uniqueness is
// computed from the already-exported EntityDef accessors, keeping it out
// of Metamodel's public API (plimsoll load line).
type metamodelView struct{ m *metamodel.Metamodel }

func (v metamodelView) HasEntityType(entityType string) bool {
	return v.m.HasEntityType(entityType)
}

func (v metamodelView) HasRelationType(relationType string) bool {
	_, ok := v.m.Relations[relationType]
	return ok
}

func (v metamodelView) PropertyInfo(entityType, property string) acl.PropertyInfo {
	def, ok := v.m.GetEntityDef(entityType)
	if !ok {
		return acl.PropertyInfo{}
	}
	pd, ok := def.PropertyDefs()[property]
	if !ok {
		return acl.PropertyInfo{}
	}
	return acl.PropertyInfo{Exists: true, Unique: pd.Unique, List: pd.List}
}

func buildACL(policy *acl.Policy, meta *metamodel.Metamodel, st store.Store) (acl.ACL, *acl.Declarative, error) {
	if policy == nil {
		return acl.NopACL{}, nil, nil
	}
	// Schema-dependent policy validation (principal_property references a
	// real, unique property; user_entity_type is a declared type). Run
	// here rather than in acl.LoadPolicy because the acl package
	// deliberately does not depend on metamodel; a mistake must fail the
	// boot, not silently mis-resolve identities at runtime.
	if err := policy.ValidateAgainstMetamodel(metamodelView{meta}); err != nil {
		return nil, nil, fmt.Errorf("appbuild: validate acl policy against metamodel: %w", err)
	}
	warnUngatedMembership(policy)
	// `st` is passed twice: once via NewStoreGraph (the Graph
	// adapter the resolver uses for member-of / ancestor walks), and
	// once as the GraphQueryer (executes store.MatchingIDs for
	// Request.PermitsRead / PermitsReadMany). The store.Store
	// interface embeds both — RR-U06D. A future backend or
	// store-wrapping decorator (audit, metrics) MUST forward
	// GraphQueryer or this compiles while the read gate silently uses
	// the wrong store.
	//
	// WithPrincipalLookup supplies the store-backed resolver for the
	// `principal_property` substitution; NewDeclarative requires it iff
	// the policy enables that lookup (else it is an inert dependency).
	d, err := acl.NewDeclarative(policy, acl.NewStoreGraph(st), st,
		acl.WithPrincipalLookup(acl.NewStorePrincipalLookup(st)))
	// coverage-ignore-start: defensive: NewDeclarative only errors on nil policy/graph/queryer or a missing principal
	// lookup; policy is non-nil
	// (guarded above), graph and queryer are non-nil (NewStoreGraph(st)/st), and WithPrincipalLookup is always supplied
	if err != nil {
		return nil, nil, fmt.Errorf("appbuild: build acl.Declarative: %w", err)
	}
	// coverage-ignore-end
	return d, d, nil
}

// warnUngatedMembership logs a prominent startup warning when the loaded
// policy leaves the membership-relation self-promotion path open: a group
// named in `assignments:` confers a privileged role, but writes to the
// membership relation carry no `requires_permission` gate, so anyone who can
// write that edge can grant themselves the role (RR-7O6Q, TKT-T31NKT).
//
// **Warning, not a refusal.** The hole is pre-existing and the shape is a
// reasonable one inside a trusted team, where the trust boundary is who can
// write to the project at all. Hard-failing the boot would break those
// deployments on upgrade for a risk they have already accepted. The refusal
// is scoped to the case that genuinely changes the stakes — a policy that
// also grants read on a non-default world, where the same hole becomes a
// mechanism for leaking unpublished content — and lands with the world grant
// syntax it needs to be evaluated (TKT-DN37J2).
//
// The condition comes from [acl.Policy.MembershipSelfPromotionOpen], the same
// predicate behind the `rela acl audit` A1-ungated-membership finding, so the
// boot warning and the linter can never disagree.
// warnUndeclaredFaces logs a startup warning when the store holds
// content-state rows that no metamodel declaration can account for: data
// a schema change (or hand edit) stranded. Warning, never a refusal — a
// schema edit must not brick the project; `rela analyze states` lists the
// rows and the data-migration system (FEAT-T3EF5A) is the remedy.
//
// SCOPE (TKT-WAV8XP): the probe only runs for a project where NO entity
// type declares faces. Under TKT-DOFYR1 that was every project, so the
// probe was unconditional; now a project that declares content states has
// legitimate state rows, and counting them would warn about perfectly
// declared drafts on every boot. Warning fatigue on a boot diagnostic is
// how the genuinely stranded case stops being noticed, so the coarse
// probe steps aside for the precise one: `analyze states` subtracts the
// declared set PER TYPE and is authoritative for those projects.
//
// The probe is two COUNTs, not a scan — and therefore NOT a snapshot:
// on a live multi-writer store (the pg listener starts in Open, before
// assemble) a write between the counts can skew the number either way,
// including suppressing the warning on a busy boot. The count is
// ADVISORY; the authoritative report is `rela analyze states`. A store
// error only skips the warning (boot diagnostics must not fail the
// boot) with a Debug trace so the absence is explainable.
func warnUndeclaredFaces(st store.Store, meta *metamodel.Metamodel, projectRoot string) {
	if declaresFaces(meta) {
		return
	}
	ctx := context.Background()
	all, err := st.CountEntities(ctx, store.EntityQuery{AllStates: true})
	if err != nil {
		slog.Debug("appbuild: content-state probe failed; skipping warning", "error", err)
		return
	}
	defaults, err := st.CountEntities(ctx, store.EntityQuery{})
	if err != nil {
		slog.Debug("appbuild: content-state probe failed; skipping warning", "error", err)
		return
	}
	if states := all - defaults; states > 0 {
		slog.Warn("appbuild: store holds content-state rows that no metamodel declaration accounts for",
			"project", projectRoot,
			"state_rows", states,
			"detail", "rela analyze states",
			"remedy", "detection only in this version; the data-migration system is the remedy")
	}
}

// declaresFaces reports whether any entity type declares content
// states, i.e. whether the coarse two-COUNT probe above would produce
// false positives for this project.
func declaresFaces(meta *metamodel.Metamodel) bool {
	if meta == nil {
		return false
	}
	for _, def := range meta.Entities {
		if len(def.Faces) > 0 {
			return true
		}
	}
	return false
}

func warnUngatedMembership(policy *acl.Policy) {
	if !policy.MembershipSelfPromotionOpen() {
		return
	}
	rel := policy.EffectiveMembershipRelation()
	slog.Warn("appbuild: ACL membership relation is NOT gated — self-promotion is possible; "+
		"any principal who can write this relation can grant themselves an assigned privileged role",
		"relation", rel,
		"fix", fmt.Sprintf("set role_relations.%s.requires_permission and grant that permission only to admins", rel),
		"docs", "docs/acl-security.md",
		"audit", "rela acl audit")
}

// Config carries the inputs every build of [New] needs, plus
// backend-specific configuration that only some builds consume.
//
// The build-agnostic fields (FS, Paths, ScriptEngine, Audit) are
// required by every scenario — even the postgres build still reads the
// metamodel and templates from the filesystem (see Paths). DatabaseURL
// is consumed only by the postgres build and ignored by the FS and
// memory builds; this is the seam where backend-specific configuration
// enters the composition root without forcing other builds to
// acknowledge it through shared parameters.
type Config struct {
	FS           storage.FS
	Paths        *project.Context
	ScriptEngine *script.Engine
	Audit        audit.Audit

	// DatabaseURL is the PostgreSQL connection string. The caller decides
	// where it comes from: [Discover] takes it from [WithDatabaseURL] or, as a
	// fallback, $RELA_DATABASE_URL; a caller building a Config directly (e.g.
	// rela-desktop, or a per-tenant lookup) simply sets it.
	//
	// The invariant is that a DSN must **never reach a command line** — it
	// carries a password, and a flag would put it in `ps` output and shell
	// history. That is why no binary exposes a --database-url flag. It is NOT
	// an invariant that the value come from the environment; passing it in Go
	// code is fine and is what makes two differently-backed Services
	// constructible in one process.
	//
	// Consumed only by the postgres build; empty (and ignored) in the
	// FS/memory builds.
	DatabaseURL string
}

// validate nil-checks the four build-agnostic collaborators. Each build's
// New calls it first; backend-specific validation (e.g. a required DSN)
// lives in that build's recipe.
func (c Config) validate() error {
	if c.FS == nil {
		return errors.New("appbuild.New: Config.FS is required")
	}
	if c.Paths == nil {
		return errors.New("appbuild.New: Config.Paths is required")
	}
	if c.ScriptEngine == nil {
		return errors.New("appbuild.New: Config.ScriptEngine is required")
	}
	if c.Audit == nil {
		return errors.New("appbuild.New: Config.Audit is required (use audit.Nop{} to opt out)")
	}
	return nil
}

// Discover resolves the project at startDir and constructs every
// service the entry points need. scriptEngine is the long-lived Lua
// engine; production callers pass [script.NewEngine].
//
// Discover constructs a production [audit.Filesystem] under
// .rela/audit/ and resolves the database URL (postgres build) from
// [WithDatabaseURL] when supplied, falling back to the RELA_DATABASE_URL
// environment variable. Neither source is a command-line flag, which is the
// property that matters: a DSN carries a password and must not land in `ps`
// output or shell history. The entry point caller is responsible for stamping
// [principal.Principal] onto the request context (this varies per binary — cli,
// mcp, scheduler, data-entry server).
func Discover(startDir string, scriptEngine *script.Engine, opts ...Option) (*Services, error) {
	fs := storage.NewSafeFS(storage.NewOsFS())
	paths, err := project.Discover(startDir, fs)
	if err != nil {
		return nil, fmt.Errorf("discover project: %w", err)
	}
	return buildAt(fs, paths, scriptEngine, opts...)
}

// At is [Discover] for a caller that already knows the project root: it
// resolves dir AS the root instead of walking up from it (TKT-SK2QQW).
//
// The distinction matters for anything that BUILT the directory it is about to
// open — the docs capture server standing up a throwaway fixture project being
// the case this exists for. Discovery would walk out of that temp directory and,
// if the temp path happened to sit under a real project, open THAT one: seeding
// fixture entities into a live database and taking its single-writer lock. The
// failure is silent and depends on where TMPDIR points.
//
// Everything downstream is identical to Discover — same audit sink, same DSN
// resolution, same caller responsibility for stamping the principal.
func At(dir string, scriptEngine *script.Engine, opts ...Option) (*Services, error) {
	fs := storage.NewSafeFS(storage.NewOsFS())
	paths, err := project.At(dir, fs)
	if err != nil {
		return nil, fmt.Errorf("open project at %s: %w", dir, err)
	}
	return buildAt(fs, paths, scriptEngine, opts...)
}

// buildAt is the shared tail of [Discover] and [At]: everything after the
// project root is resolved. Split so the two entry points cannot drift in what
// they wire — only in HOW they answer "which directory is the root".
func buildAt(
	fs storage.FS, paths *project.Context, scriptEngine *script.Engine, opts ...Option,
) (*Services, error) {
	// Shared startup path for cli, mcp, scheduler and the data-entry server —
	// warns at most once per process.
	project.WarnIfLegacySchema(paths)
	auditSink, auditErr := audit.NewFilesystem(filepath.Join(paths.CacheDir, "audit"))
	// coverage-ignore-start: defensive: audit.NewFilesystem only errors on an empty dir; filepath.Join(paths.CacheDir,
	// "audit") is never empty
	// because project.Discover always sets a non-empty CacheDir
	if auditErr != nil {
		return nil, fmt.Errorf("build audit sink: %w", auditErr)
	}

	// coverage-ignore-end
	return New(Config{
		FS:           fs,
		Paths:        paths,
		ScriptEngine: scriptEngine,
		Audit:        auditSink,
		DatabaseURL:  resolveDatabaseURL(opts, os.Getenv),
	}, opts...)
}

// SharedBase holds the build-agnostic inputs resolved by [prepare] and
// consumed by [assemble]: the validated config, applied options, the
// resolved ACL (+ parsed policy), and the loaded metamodel. The
// per-scenario New recipes thread this between prepare → openBackend →
// assemble so the shared steps are written exactly once.
// SharedBase is the tenant-independent half of construction: the validated
// config, the applied options, the parsed `acl.yaml` policy, and the loaded
// metamodel. Nothing in it is derived from a store, so ONE base can be built
// per process and assembled against several stores.
//
// Build it with [NewSharedBase]; turn it into a [Services] with
// [SharedBase.Assemble] (which the per-backend New recipes call for you).
//
// # What is shared and what is not
//
// The split is NOT along the [Services] field list, which is the intuitive but
// wrong reading. `acl.Declarative` is constructed from the STORE — it needs a
// store-backed `acl.Graph` for group expansion and containment inheritance —
// so the ACL *policy* is shared while the ACL *evaluator* is per-store. Same
// for `lua.ReadDeps`, which closes over the store. That is precisely why ACL
// construction is deferred out of this type and into [SharedBase.Assemble].
//
// # Shared values must not be mutated during assembly
//
// `meta` and `aclPolicy` are POINTERS handed to every assembled Services. A
// mutation through either during assembly would be visible to every other
// consumer of the same base — a cross-tenant defect in a multi-tenant host, and
// a cross-project one on the desktop. Assembly only reads them (the metamodel
// consumers derive new values: `statemachine.Compile`, `NewEngineFromMetamodel`),
// and TestSharedBase_AssemblyDoesNotMutateSharedValues pins it.
type SharedBase struct {
	cfg       Config
	opts      options
	acl       acl.ACL
	aclPolicy *acl.Policy
	meta      *metamodel.Metamodel
	worlds    worlds.Compiled
	// reassembly marks a base built to re-assemble against an ALREADY-OPEN
	// store, so [assemble] skips the store-open-only steps. Set by
	// [SharedBase.ForReassembly]; false for a base that will open its own store.
	reassembly bool
}

// IsReassembly reports whether this base is marked to re-assemble against an
// already-open store (see [SharedBase.ForReassembly]).
func (b *SharedBase) IsReassembly() bool { return b.reassembly }

// ForReassembly returns a copy of this base marked as re-assembling against a
// store that is already open — the schema hot-reload case (TKT-NU247U).
//
// It skips the store-open-only work in [SharedBase.Assemble]: today that is
// the postgres derived-schema reconciliation, which issues DDL and is
// documented boot-only on `pgstore.Store.Reconcile`. Everything metamodel-
// derived is still rebuilt, which is the point of reloading.
//
// Use it for EVERY assembly after the first against a given store. A base
// used to open a store must not be marked.
func (b *SharedBase) ForReassembly() *SharedBase {
	next := *b
	next.reassembly = true
	return &next
}

// Worlds returns the compiled world scopes this base was built from.
//
// Compiled once here because compilation is metamodel-derived and therefore
// tenant-independent — the same reason `meta` lives on the base. It is
// compiled at boot so an invalid face name fails startup rather than the
// first request that needs a world.
//
// Consumed through [CompiledWorlds], which the data-entry app's `SetWorlds`
// uses to resolve a `?world=` name to its scope (TKT-WAV8XP PR-D).
func (b *SharedBase) Worlds() worlds.Compiled { return b.worlds }

// Meta returns the loaded metamodel this base was built from. Exposed so a host
// holding one base can answer "what schema am I serving?" without assembling a
// Services first.
func (b *SharedBase) Meta() *metamodel.Metamodel { return b.meta }

// Config returns the validated [Config] this base was built from.
//
// Exposed so a host can build a SUCCESSOR base from the same inputs — which is
// how a schema hot-reload re-reads `schema.yaml` without re-discovering the
// project (TKT-NU247U). The returned value is a copy of a struct of handles;
// mutating it does not affect this base, but the FS, ScriptEngine and Audit it
// names are shared, which is exactly what makes the successor equivalent.
func (b *SharedBase) Config() Config { return b.cfg }

// Paths returns the project context this base was built from.
func (b *SharedBase) Paths() *project.Context { return b.cfg.Paths }

// NewSharedBase builds the tenant-independent half of construction once:
// validate config, apply options, parse `acl.yaml`, load and validate the
// metamodel. It opens no store and touches no database.
//
// Use it when one process serves several stores from one project
// configuration — a multi-tenant host (RES-D54281), or any caller that would
// otherwise re-read and re-validate the same metamodel per store. The
// single-store path is [New] / [Discover], which call this internally.
func NewSharedBase(cfg Config, opts ...Option) (*SharedBase, error) {
	return prepare(cfg, opts)
}

// prepare runs the build-agnostic front half of construction: validate
// config, apply options (so a caller-supplied [WithACL] wins over the
// auto-loaded policy), resolve the ACL, and load the metamodel from
// disk. Every build's New calls this before opening its backend.
//
// Resolving the ACL here (rather than in each recipe) lets us tell
// "operator chose NopACL explicitly" from "operator passed nothing and
// the project has no acl.yaml" — both end up NopACL, but only the
// latter triggers the "consider adding an acl.yaml" warning an entry
// point may render.
func prepare(cfg Config, opts []Option) (*SharedBase, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	var o options
	for _, opt := range opts {
		opt(&o)
	}
	resolvedACL := o.acl
	var aclPolicy *acl.Policy
	if resolvedACL == nil {
		// Load the policy YAML up front; ACL construction is deferred
		// to [assemble] because the v1 [acl.Declarative] needs a
		// store-backed [acl.Graph] adapter and the store isn't open
		// yet at this point in the build.
		var err error
		aclPolicy, err = loadACLPolicy(cfg.Paths.Root)
		if err != nil {
			return nil, err
		}
	}

	meta, _, err := metamodel.NewFSLoader(cfg.FS, cfg.Paths.SchemaPath).Load(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load metamodel: %w", err)
	}

	// Compile the declared worlds here, at assembly, so a bad face name is
	// a startup failure rather than a lurking runtime one. The loader checks
	// world STRUCTURE; the face GRAMMAR is checked here because metamodel
	// may not import entity under arch-lint (TKT-WAV8XP, internal/worlds).
	compiledWorlds, err := worlds.Compile(meta)
	if err != nil {
		return nil, fmt.Errorf("compile worlds: %w", err)
	}

	// Compile the declared query scopes for the same reason, and at the same
	// point: a scope that does not compile must fail the BOOT, not the first
	// page that happens to use it. Compiling here rather than only in the
	// data-entry wiring means every entry point pays the check, so `rela
	// validate` cannot report clean on a schema rela-server would refuse.
	//
	// The result is discarded — dataentry compiles its own, from a metamodel
	// that reloads at runtime (see appbuild.QueryScopes). This call is the
	// gate, not the supply.
	compiledScopes, err := scopes.Compile(meta)
	if err != nil {
		return nil, fmt.Errorf("compile query scopes: %w", err)
	}
	warnQueryScopes(&compiledScopes, meta, aclPolicy)
	if problems := QueryScopeTraversalFieldErrors(&compiledScopes, meta, aclPolicy); len(problems) > 0 {
		return nil, fmt.Errorf("compile query scopes: %s", strings.Join(problems, "; "))
	}

	return &SharedBase{
		cfg: cfg, opts: o, acl: resolvedACL, aclPolicy: aclPolicy,
		meta: meta, worlds: compiledWorlds,
	}, nil
}

// assemble runs the build-agnostic back half: it takes the opened store
// + searcher (built by the per-scenario openBackend) and wires every
// remaining collaborator — automation, tracer, templater, config loader,
// lua read deps, entitymanager, validator, state KV — into a [Services].
//
// Keeping this shared is the invariant that prevents the three New
// recipes from drifting: a recipe may CHOOSE and ORDER backend steps,
// but build-agnostic wiring lives here and nowhere else.
//
// visible may be nil: assemble then derives the generic
// search.NewVisible(searcher, st) wrapper, the correct ACL-scoped
// search implementation for every in-process store. The postgres
// recipe passes its native implementation instead.

// resolveACL settles which ACL the services use, now that the store is open.
//
// Deferred out of prepare because acl.Declarative needs a store-backed Graph.
// A missing policy yields NopACL, but an error from the Declarative
// constructor is propagated — booting allow-all on a policy that failed to
// parse would silently disable authorization.
func resolveACL(base *SharedBase, st store.Store) (acl.ACL, *acl.Declarative, error) {
	if base.acl == nil {
		return buildACL(base.aclPolicy, base.meta, st)
	}
	// RR-36UL: when WithACL was passed a *acl.Declarative, surface it as the
	// declarative value too so the affordance resolver path picks it up.
	// Without this, a caller wiring WithACL(declarative) silently gets
	// NopFieldVerdictResolver because Services.ACLDeclarative() returns nil.
	d, _ := base.acl.(*acl.Declarative)
	return base.acl, d, nil
}

// Assemble wires this base against one opened store into a [Services].
//
// Call it once per store. The base is reusable: every value it holds is
// tenant-independent, and assembly only reads them (see the type doc). The
// per-backend New recipes call this for you after opening their store; a
// multi-store host calls it directly, once per store.
//
// The caller owns closing the returned [Services] — and only that Services.
// [Services.Close] tears down the store and search closer it was assembled
// with, never anything belonging to the base, so evicting one assembled
// Services leaves the base and every sibling usable.
//
// visible may be nil: Assemble then derives the generic
// search.NewVisible(searcher, st) wrapper, which is correct for every
// in-process store. The postgres recipe passes its native implementation.
func (b *SharedBase) Assemble(
	st store.Store, searcher search.Searcher,
	visible search.VisibleSearcher, searchCloser io.Closer,
) (*Services, error) {
	return assemble(b, st, searcher, visible, searchCloser, backendOverrides{})
}

// buildEntityManager assembles the write-path manager from the collaborators
// assemble has already derived. Extracted purely to keep assemble within the
// funlen budget; it makes no decisions of its own.
func buildEntityManager(
	base *SharedBase, st store.Store, aliases entitymanager.AliasRewriter,
	templater entitymanager.TemplateLoader, resolvedACL acl.ACL,
	autoEngine *automation.Engine, cascadeRunner *autocascade.Runner,
	readDeps lua.ReadDeps, versions store.VersionService, tw TransitionWiring,
	computedSet *computed.Set,
) (*entitymanager.Manager, error) {
	mgr, err := entitymanager.New(entitymanager.Deps{
		AliasRewriter:           aliases,
		Store:                   st,
		Meta:                    base.meta,
		Templater:               templater,
		Audit:                   base.cfg.Audit,
		ACL:                     resolvedACL,
		Automations:             autoEngine,
		Cascade:                 cascadeRunner,
		ScriptRunner:            cascadeScriptRunner(base.cfg.ScriptEngine, readDeps, st, base.cfg.Audit),
		VersionRecorder:         versionRecorderFor(versions),
		RelationVersionRecorder: relationVersionRecorderFor(versions),
		Computed:                computedSet,
		Transitions:             tw.Enforcer,
		FieldGate:               entitymanager.AllowAllFieldGate{},
		TransitionGuard:         tw.Guard,
		TransitionGraph:         tw.Graph,
		// The copy deps (TKT-WRLDAPI item 5). Before this, NONE of the three
		// was wired in any deployment — so every guarded copy 403'd
		// ("no guard is wired") and every copy's source read took the
		// no-policy branch, which CopyReadGate's own godoc says must never
		// happen on a deployment that has a policy.
		//
		// CopyGuard reuses tw.Guard: entitymanager.CopyGuard and
		// statemachine.Guard are the same shape ON PURPOSE, so a copy's
		// `guard:` and a transition's ask the identical question of the
		// identical implementation and cannot drift into asking different
		// ones.
		CopyGuard:      tw.Guard,
		CopyReadGate:   tw.ReadGate,
		CopyVisibility: tw.Visibility,
	})
	if err != nil {
		return nil, fmt.Errorf("build entitymanager: %w", err)
	}
	return mgr, nil
}

// resolveVisibleSearcher derives an ACL-scoped searcher when the caller did not
// supply one. A recipe may pass its own (the postgres build has a DB-backed
// implementation); everything else gets the generic decorator.
func resolveVisibleSearcher(
	visible search.VisibleSearcher, searcher search.Searcher, st store.Store,
) (search.VisibleSearcher, error) {
	if visible != nil {
		return visible, nil
	}
	v, err := search.NewVisible(searcher, st)
	if err != nil {
		return nil, fmt.Errorf("appbuild: derive VisibleSearcher: %w", err)
	}
	return v, nil
}

// backgroundServices holds the optional per-assembled subsystems and the stop
// functions Services.Close tears down. Grouped so assemble stays readable as
// more optional services are added — each is independently nil-able and none
// may fail boot.
type backgroundServices struct {
	gcStop   func()
	mailStop func()
	mail     *mailRuntime
}

// startBackgroundServices launches the optional per-store subsystems: the
// data-migration gate and GC sweep, and the mail outbox worker.
//
// Neither may fail boot. Each returns a stop function that is always safe to
// call, so Close needs no nil checks beyond the ones it already has.
func startBackgroundServices(
	base *SharedBase, st store.Store, stateKV state.KV, migState datamigration.StateStore,
	cfgLoader config.Loader, versions store.VersionService,
) backgroundServices {
	cfg := base.cfg

	gcStop := startDataMigration(
		stateKV, migState, base.meta, st, cfg.Audit, versions, cfg.Paths.CacheDir,
		hasMigrationsVia(cfgLoader),
	)

	// Mail is optional and never fails boot. The outbox itself is discarded
	// here: nothing can enqueue until the declarative layer (TKT-U2R7GU)
	// lands, and storing a handle no code reads would look wired when it is
	// not. Only the stop function is retained, because Close genuinely uses it.
	mailRuntime, mailStop := startMailRuntime(cfg.Paths)

	return backgroundServices{
		gcStop:   gcStop,
		mailStop: mailStop,
		mail:     mailRuntime,
	}
}

// resolveACLAndRedactor resolves the ACL and derives the field redactor that
// depends on it. They are returned together because the redactor is a function
// of the resolved Declarative: splitting them invites a caller that has one
// without the other, which is precisely the state that left three read paths
// ungated (TKT-BUYEW1).
func resolveACLAndRedactor(
	base *SharedBase, st store.Store,
) (acl.ACL, *acl.Declarative, visibility.FieldRedactor, error) {
	resolvedACL, aclDeclarative, err := resolveACL(base, st)
	if err != nil {
		return nil, nil, nil, err
	}
	fieldRedactor, err := buildFieldRedactor(base.meta, st, aclDeclarative)
	if err != nil {
		return nil, nil, nil, err
	}
	return resolvedACL, aclDeclarative, fieldRedactor, nil
}

// cascadeReadDeps builds the static lua.ReadDeps backing automation cascades.
//
// Cascade reads are ACL-BOUND to the ACTING USER (DEC-O59WM4, RR-XC0URX): an
// automation fires in response to someone's write and runs on that person's
// ctx, so it reads their view — symmetric with its write path, which already
// gates through entitymanager and needs an explicit allow_acl_bypass to
// elevate. Escalating reads is deliberately NOT a config default; it is
// TKT-ACSBSA (an admin-handle extension), so a cascade that needs more must ask
// for it in the open.
//
// Field-level `visible:` redaction applies here too (TKT-BUYEW1) — a Lua action
// can send what it reads onward exactly as a scheduled job can, so it must not
// see property values the same principal has redacted everywhere else.
func cascadeReadDeps(
	st store.Store, tr tracer.Tracer, searcher search.Searcher,
	meta *metamodel.Metamodel, projectRoot string,
	d *acl.Declarative, redactor visibility.FieldRedactor,
) lua.ReadDeps {
	return lua.ReadDeps{
		VisibleReader: scriptEntityReader(st, d, redactor),
		Tracer:        scriptTracer(tr, st, d, redactor),
		Searcher:      searcher,
		Meta:          meta,
		ProjectRoot:   projectRoot,
	}
}

// backendOverrides are the services a recipe supplies because they come from
// something only that recipe holds — today, a database handle it opened.
//
// A struct rather than more parameters on [assemble]: every field is
// optional, they arrive together from one recipe, and appending a third
// positional nil to four call sites is how a signature becomes unreadable.
// The zero value means "derive everything from the filesystem", which is what
// the fs, memory and postgres recipes pass.
type backendOverrides struct {
	// projectConfig replaces the filesystem config loader. The sqlite recipe
	// supplies one because its database may CARRY the project's config, which
	// it layers behind the files.
	projectConfig config.Loader

	// migState replaces the file-backed migration record (TKT-XCJ0Y2).
	// Supplied by the database recipes so a tenant's position lives with its
	// data; nil selects the committed-file backend.
	migState datamigration.StateStore
	// stateKV replaces the filesystem state store. Supplied for the same
	// reason and from the same handle: state written beside the database
	// rather than inside it would be left behind when the file is shipped.
	stateKV state.KV

	// commentStore replaces the filesystem comment store (TKT-OGTVJW).
	//
	// Supplied by the database recipes from the handle they already own, for
	// two different reasons. Postgres NEEDS it: filecomments is node-local, so
	// under the multi-process deployment docs/postgres-backend.md documents, a
	// comment posted through one node is invisible to every other. SQLite wants
	// it so commentary travels with rela.db, the same call versioning made.
	//
	// Nil leaves the filesystem backend in place, which is correct for the fs,
	// memory and desktop tiers.
	commentStore comments.Store

	// schedulerState replaces the state.KV-backed scheduler run-state
	// (BUG-TKL08E). Supplied by the postgres recipe, whose job queue is
	// durable and shared: a run queued by one process may execute on
	// another, so its record must live where both can see it. Nil selects
	// kvstate over the state store, which is right for every tier whose
	// queue is in-process.
	schedulerState schedulerstate.Store
}

// assemble builds the services bundle from an opened store.
//
// Overrides are passed in rather than derived here because they come from a
// database handle that belongs to the recipe that opened it, and assemble is
// deliberately build-agnostic.
func assemble(
	base *SharedBase, st store.Store, searcher search.Searcher,
	visible search.VisibleSearcher, searchCloser io.Closer,
	overrides backendOverrides,
) (svc *Services, retErr error) {
	cfg := base.cfg

	visible, err := resolveVisibleSearcher(visible, searcher, st)
	if err != nil {
		return nil, err
	}

	warnUndeclaredFaces(st, base.meta, base.cfg.Paths.Root)
	resolvedACL, aclDeclarative, fieldRedactor, err := resolveACLAndRedactor(base, st)
	if err != nil {
		return nil, err
	}

	autoEngine, cascadeRunner, err := buildAutomation(base.meta, st)
	// coverage-ignore-start: defensive: buildAutomation only errors when autocascade.New fails, which requires a nil
	// Engine that buildAutomation
	// never produces (see the scupper there)
	if err != nil {
		return nil, err
	}
	// coverage-ignore-end

	tr := tracer.New(st)
	templater := templating.NewFSTemplater(cfg.FS, cfg.Paths)
	cfgLoader := overrides.projectConfig
	if cfgLoader == nil {
		cfgLoader = config.NewFSLoader(cfg.FS, cfg.Paths.Root)
	}

	// Build the static lua read deps once — the ScriptRunner (automation
	// cascades) is constructed with these.
	readDeps := cascadeReadDeps(st, tr, searcher, base.meta, cfg.Paths.Root,
		aclDeclarative, fieldRedactor)

	tw, err := CompileTransitions(base.meta, st, resolvedACL)
	if err != nil {
		return nil, fmt.Errorf("compile transitions: %w", err)
	}
	computedSet, err := computed.Compile(base.meta)
	if err != nil {
		return nil, fmt.Errorf("compile computed properties: %w", err)
	}

	// Content versioning is a separate injected service (pgstore and
	// sqlitestore; nil on the fs/memory builds, which have no versioning
	// store — fsstore uses git), NOT a store capability the manager
	// type-asserts. Derived once here and threaded into the recorders and the
	// Services bundle.
	versions := versionServiceFor(st)

	// A recipe-supplied state store wins over the per-backend one, which wins
	// over the filesystem. Same precedence as the config loader, and for the
	// same reason.
	backendKV := overrides.stateKV
	if backendKV == nil {
		backendKV = stateKVFor(st)
	}
	migState, err := buildMigState(cfg.FS, cfg.Paths, overrides.migState, backendKV)
	if err != nil {
		return nil, err
	}

	stateKV, aliases, jobQueue, err := buildRuntimeServices(cfg.FS, cfg.Paths, base, backendKV)
	// coverage-ignore-start: defensive: entitymanager.New only errors on a nil required dep
	// (Store/Meta/Templater/Audit/ACL/Transitions);
	// assemble supplies all of them non-nil (Audit validated in Config.validate, Transitions is Compile's non-nil Set,
	// resolvedACL is never nil)
	if err != nil {
		return nil, err
	}
	// buildRuntimeServices STARTS the job queue (worker pool; on postgres a
	// connection pool too), so every error path below this point must stop it.
	// Boot could get away without this — a failed boot exits the process — but
	// assembly is now also a RELOAD path (TKT-NU247U): a schema that loads yet
	// fails to assemble would otherwise leak one queue per save.
	defer func() {
		if retErr != nil {
			closeJobQueue(jobQueue)
		}
	}()

	// Comments are keyed by target entity id, so the service must learn about
	// renames and deletes. It rides the AliasRewriter hook rather than
	// store.EntityObserver for the reason that hook documents: stores fire the
	// observer with the error discarded, which is fine for a rebuildable search
	// index but not for records that exist ONLY in the comment store.
	commentSvc, err := buildComments(cfg.FS, cfg.Paths, base.meta, overrides.commentStore)
	if err != nil {
		return nil, err
	}
	schedState := overrides.schedulerState
	if schedState == nil {
		if schedState, err = kvstate.New(stateKV); err != nil {
			return nil, err
		}
	}
	// coverage-ignore-end

	// Upstream's parameter order (readDeps after cascadeRunner) with the
	// comment fanout wrapping the alias rewriter.
	mgr, err := buildEntityManager(base, st, newAliasFanout(aliases, commentSvc), templater, resolvedACL,
		autoEngine, cascadeRunner, readDeps, versions, tw, computedSet)
	// coverage-ignore-start: defensive: buildStateKV only errors when NewRootedFS fails on a non-empty CacheDir, which
	// requires filepath.Abs to
	// fail — unreachable with valid inputs (see the scupper in buildStateKV)
	if err != nil {
		return nil, err
	}
	// coverage-ignore-end

	val, err := validator.New(st, base.meta, readDeps, ungatedBinder(base.meta, st))
	if err != nil { // coverage-ignore: defensive: validator.New only fails on a nil dep; all are built above
		return nil, err
	}

	// Start the backend's version-reconciliation sweep (postgres and sqlite
	// builds; a no-op elsewhere). It captures create/update versions for
	// settled entities; rename/delete are captured synchronously via the
	// entitymanager hook above.
	startVersionSweepIfSupported(st, base.meta)

	// Reconcile the derived schema (the database builds; a no-op elsewhere):
	// derive query and list indexes from data-entry.yaml (both), and on
	// postgres synthesize the metamodel's `unique: true` properties into
	// partial unique indexes so uniqueness is enforced atomically, publishing
	// the current unique pairs so a violation can be attributed to a property
	// (TKT-3Q0GP1).
	// Failures degrade to warnings — a derived-schema problem never fails boot.
	//
	// STORE-OPEN ONLY, skipped on re-assembly. `pgstore.Store.Reconcile` is
	// documented boot-only and says re-reconciling on a live reload "needs its
	// own debounce/lock policy for issuing DDL off a file watcher" — that
	// policy does not exist, so a reload must not issue DDL. Two things would
	// go wrong if it did: CREATE/DROP INDEX on every editor autosave, and a
	// metamodel saved mid-edit that still parses but has lost a `unique:`
	// declaration would DROP the live index (desired-state reconciliation
	// treats absent as delete). A `unique:` added without a restart stays
	// enforced by the application scan; `rela db reconcile` applies it.
	if !base.reassembly {
		reconcileDerivedSchemaIfSupported(context.Background(), st, base, cfgLoader)
	}

	// Evaluate the data-migration gate (adopt compatible schema-shape
	// changes, warn on incompatible ones) and start the drift GC sweep
	// (TKT-0C57FS). Never fails boot; the stop func is torn down in Close —
	// per-assembled, like the search closer.
	background := startBackgroundServices(base, st, stateKV, migState, cfgLoader, versions)

	return newServices(
		base, st, background, searcher, visible, searchCloser, mgr, tr, val,
		templater, cfgLoader, stateKV, migState, jobQueue, aliases, commentSvc, versions,
		resolvedACL, aclDeclarative, fieldRedactor, schedState,
	), nil
}

// newServices bundles the assembled collaborators into the Services value.
// It is pure field assignment, split out of assemble so that function stays
// within its length budget: the bundle grows with every new collaborator,
// and that growth should not push the assembly logic over the limit.
func newServices(
	base *SharedBase, st store.Store, background backgroundServices,
	searcher search.Searcher, visible search.VisibleSearcher, searchCloser io.Closer,
	mgr *entitymanager.Manager, tr tracer.Tracer, val validator.Validator,
	templater templating.Templater, cfgLoader config.Loader, stateKV state.KV,
	migState datamigration.StateStore, jobQueue jobs.Queue, aliases *caldavalias.Service, commentSvc *comments.Service,
	versions store.VersionService, resolvedACL acl.ACL, aclDeclarative *acl.Declarative,
	fieldRedactor visibility.FieldRedactor, schedState schedulerstate.Store,
) *Services {
	cfg := base.cfg
	return &Services{
		base:            base,
		gcStop:          background.gcStop,
		mailStop:        background.mailStop,
		mail:            background.mail,
		fs:              cfg.FS,
		paths:           cfg.Paths,
		meta:            base.meta,
		worlds:          base.worlds,
		store:           st,
		versions:        versions,
		searcher:        searcher,
		visibleSearcher: visible,
		userState:       newUserState(st, stateKV),
		entityManager:   mgr,
		tracer:          tr,
		validator:       val,
		templater:       templater,
		cfgLoader:       cfgLoader,
		stateKV:         stateKV,
		migState:        migState,
		schedulerState:  schedState,
		jobQueue:        jobQueue,
		caldavAliases:   aliases,
		comments:        commentSvc,
		scriptEngine:    cfg.ScriptEngine,
		searchCloser:    searchCloser,
		acl:             resolvedACL,
		aclDeclarative:  aclDeclarative,
		aclPolicy:       base.aclPolicy,
		audit:           cfg.Audit,
		fieldRedactor:   fieldRedactor,
	}
}

// versionRecorder adapts a store.VersionWriter to the entitymanager's
// consumer-side VersionRecorder, translating the identically-shaped record. It
// exists only to keep entitymanager depending on its own narrow interface
// rather than on store.VersionWriter directly.
type versionRecorder struct {
	w store.VersionWriter
}

// coverage-ignore-start: postgres-only: versionRecorder is only constructed by versionRecorderFor with a non-nil
// VersionService, which
// versionServiceFor supplies only in the postgres build; the default/memory build returns nil so this method is never
// wired
func (r versionRecorder) RecordVersion(ctx context.Context, v entitymanager.VersionRecord) error {
	return r.w.WriteVersion(ctx, store.VersionInput{
		EntityID:      v.EntityID,
		Face:          v.Face,
		Op:            v.Op,
		PrevID:        v.PrevID,
		Type:          v.Type,
		Content:       v.Content,
		Properties:    v.Properties,
		SchemaHash:    v.SchemaHash,
		Projection:    v.Projection,
		PrincipalUser: v.PrincipalUser,
		PrincipalTool: v.PrincipalTool,
		TriggeredBy:   v.TriggeredBy,
	})
}

// coverage-ignore-end

// versionRecorderFor returns a synchronous version recorder when a versioning
// service is wired (pgstore), or nil when none is (fsstore/memstore — where the
// entitymanager's version hook then no-ops). vs is the injected version service
// (an untyped nil on non-versioning builds); a nil service yields a nil recorder
// so the manager's nil check works. Takes the service rather than type-asserting
// the store — versioning is a separate injected concern, not a store capability.
func versionRecorderFor(vs store.VersionService) entitymanager.VersionRecorder {
	if vs == nil {
		return nil
	}
	return versionRecorder{w: vs} // coverage-ignore: postgres-only: vs is non-nil only in the postgres build
	// (versionServiceFor returns nil in default/memory builds), so this non-nil return is unreachable here
}

// relationVersionRecorder adapts a store.RelationVersionWriter to the
// entitymanager's consumer-side RelationVersionRecorder. RecordID is left 0 so
// the store resolves the surrogate lineage id from the composite key at write
// time (correct for the synchronous pre-delete / post-rename capture) — the
// key includes FromFace, so a state-tailed edge resolves to its OWN lineage
// rather than the default tail's (TKT-JAROC3).
type relationVersionRecorder struct {
	w store.RelationVersionWriter
}

// coverage-ignore-start: postgres-only: relationVersionRecorder is only constructed by relationVersionRecorderFor with
// a non-nil
// VersionService, supplied only in the postgres build; the default/memory build returns nil so this method is never
// wired
func (r relationVersionRecorder) RecordRelationVersion(
	ctx context.Context, v entitymanager.RelationVersionRecord,
) error {
	return r.w.WriteRelationVersion(ctx, store.RelationVersionInput{
		From:          v.From,
		FromFace:      v.FromFace,
		Type:          v.Type,
		To:            v.To,
		Op:            v.Op,
		PrevFrom:      v.PrevFrom,
		PrevTo:        v.PrevTo,
		Content:       v.Content,
		Properties:    v.Properties,
		SchemaHash:    v.SchemaHash,
		Projection:    v.Projection,
		PrincipalUser: v.PrincipalUser,
		PrincipalTool: v.PrincipalTool,
		TriggeredBy:   v.TriggeredBy,
	})
}

// coverage-ignore-end

// relationVersionRecorderFor mirrors versionRecorderFor for relation versions.
func relationVersionRecorderFor(vs store.VersionService) entitymanager.RelationVersionRecorder {
	if vs == nil {
		return nil
	}
	return relationVersionRecorder{w: vs} // coverage-ignore: postgres-only: vs is non-nil only in the postgres build
	// (versionServiceFor returns nil in default/memory builds), so this non-nil return is unreachable here
}

// (startVersionSweepIfSupported is defined per build tag in
// versionsweep_shared.go — compiled into the postgres and sqlite builds, where
// it starts the backend's reconciliation sweep — and in
// versionsweep_nosweep.go, where every other build no-ops. Discovery is by
// type assertion on store.VersionSweeper, which keeps this build-agnostic file
// and the shared resolver alike free of any backend import. assemble calls it
// above.)

// jobQueueShutdownTimeout bounds how long [Services.Close] waits for the job
// queue to stop. A queue that will not drain must not wedge process shutdown.
//
// Build-agnostic on purpose: both tiers get the same shutdown budget, so it
// lives here rather than once per build-tagged jobqueue_*.go file.
const jobQueueShutdownTimeout = 5 * time.Second

// CloseAssembly stops the resources this Services started during its own
// [SharedBase.Assemble] — the mail worker, the data-migration GC sweep and the
// background-job queue — and leaves the store and search backend running.
//
// It exists for ONE caller shape: a host that re-assembles a base against a
// store it already owns, and must retire the superseded Services without
// tearing down the store underneath the new one. The MCP schema hot-reload
// (TKT-NU247U) is that caller — every Assemble starts a fresh job queue with
// its own worker pool (on postgres, a second connection pool and a LISTEN
// connection too), so reloading without this would leak one per schema edit.
//
// It is NOT a lighter Close. The store and searcher outlive this call by
// design, so a caller that owns them must still close them separately —
// [Services.Close] on the LAST assembled Services does that, and running both
// is safe because each runs its teardown exactly once.
//
// Safe to call repeatedly, and concurrently with Close: the shared sync.Once
// runs the teardown exactly once. Calling Close after CloseAssembly still
// closes the store and search backend.
//
// It is NOT safe against a concurrent [Services.Jobs] — the teardown clears
// the queue handle, and that field carries no lock. Retire an assembly only
// once nothing is reaching for its background services.
func (s *Services) CloseAssembly() {
	s.assemblyCloseOnce.Do(s.stopBackgroundServices)
}

// stopBackgroundServices stops the per-assembly background workers. Shared by
// [Services.Close] and [Services.CloseAssembly] so the two teardown paths
// cannot drift on WHICH services count as per-assembly — the distinction the
// whole seam rests on.
//
// Mail goes first: the worker may still be delivering and its drain is
// bounded, so stopping it before anything else gives in-flight sends their
// best chance without risking a hung shutdown.
func (s *Services) stopBackgroundServices() {
	if s.mailStop != nil {
		s.mailStop()
		s.mailStop = nil
	}
	if s.gcStop != nil {
		s.gcStop()
		s.gcStop = nil
	}
	if s.jobQueue != nil {
		closeJobQueue(s.jobQueue)
		s.jobQueue = nil
	}
}

// closeJobQueue stops q within the shutdown budget. Shared by the teardown
// path and by assemble's error path, so a queue is never dropped un-closed.
//
// Nil: accepted (no-op).
func closeJobQueue(q jobs.Queue) {
	if q == nil {
		return
	}
	// Bounded: a queue that will not drain must not wedge shutdown.
	ctx, cancel := context.WithTimeout(context.Background(), jobQueueShutdownTimeout)
	defer cancel()
	if err := q.Close(ctx); err != nil {
		slog.Warn("appbuild: failed to close job queue", "error", err)
	}
}

// Close releases resources held by Services: the per-assembly background
// services first, then the store (so any in-flight observer callbacks
// complete), then the search backend.
//
// The job queue is now drained BEFORE the store closes, where it used to be
// closed after. That ordering is the safer one on its own merits — a job
// handler may read or write the store, and draining first means a worker
// cannot be left running against a closed one — and it is what lets
// [Services.CloseAssembly] share this teardown.
//
// Safe to call repeatedly and from multiple goroutines; the close
// sequence runs exactly once. Subsequent calls return the same nil
// (no errors are returned from the close path today — store close
// failures are slog.Warn'd).
func (s *Services) Close() error {
	s.closeOnce.Do(func() {
		// Shared with CloseAssembly, and guarded by its own sync.Once, so a
		// Close following a CloseAssembly does not stop these twice.
		s.assemblyCloseOnce.Do(s.stopBackgroundServices)

		// After the queue drained, so no job handler still records a run.
		// Never fails on either backend; it only marks the store closed.
		if s.schedulerState != nil {
			_ = s.schedulerState.Close()
		}

		if s.store != nil {
			if lc, ok := s.store.(store.Lifecycle); ok {
				// coverage-ignore-start: defensive: fsstore/memstore Close does not return an error under normal test
				// conditions, so this warn-on-close-
				// error branch is unreachable
				if err := lc.Close(); err != nil {
					slog.Warn("appbuild: failed to close store", "error", err)
				}
				// coverage-ignore-end
			}
		}
		if s.searchCloser != nil {
			_ = s.searchCloser.Close()
			s.searchCloser = nil
		}
	})
	return s.closeErr
}

// buildJobQueue builds the background-job queue for this build.
//
// jobQueueFor is the per-tier choice: an ephemeral in-process queue by default,
// a durable PostgreSQL-backed one under the postgres tag. Every assembled
// Services gets one — a nil queue would surface as a panic at an enqueue site
// rather than as a wiring error here.
//
// Called once per assembled Services, which in every current entry point means
// once per process (see cmd/rela-server, cmd/rela-desktop, the CLI commands).
// The queue owns a worker pool, and on postgres a connection pool, so it is a
// process-scoped resource — not something to build per request.
//
// KNOWN COST: this is EAGER, and the scheduler is currently the only consumer
// of Services.Jobs. So every short-lived CLI command (show, list, trace, …)
// builds and starts a queue it never enqueues to. On the default build that is
// cheap (~14µs and a few goroutines); on the postgres build it is not — neoq's
// backend runs a golang-migrate check and opens a SECOND connection pool before
// Start adds a LISTEN connection and its workers, all on the startup path of a
// command that may only be reading one entity.
//
// Deliberately not made lazy here: deferring construction to the first Jobs()
// call would move queue-construction errors from wiring time to first use,
// which is a change to the accessor's contract rather than an optimization.
// Worth doing if postgres CLI startup latency becomes a complaint.
func buildJobQueue(base *SharedBase) (jobs.Queue, error) {
	q, err := jobQueueFor(base)
	if err != nil {
		return nil, fmt.Errorf("appbuild: build job queue: %w", err)
	}
	// Started here rather than left to the first producer. An unstarted queue
	// rejects every Enqueue with jobs.ErrNotStarted, so handing one out from
	// Services.Jobs would be a trap: the accessor would look wired while
	// every call through it failed.
	//
	// Handlers registered after Start still work — the dispatcher resolves
	// them per job — so this does not constrain when subsystems register.
	if err := q.Start(context.Background()); err != nil {
		return nil, fmt.Errorf("appbuild: start job queue: %w", err)
	}
	return q, nil
}

// buildRuntimeServices builds the per-assembly runtime services that are not
// derived from the store's contents: the state store, the CalDAV alias service
// riding on it, and the background-job queue.
//
// Grouped into one call so assemble reads as composition rather than a run of
// near-identical build-and-check blocks.
func buildRuntimeServices(
	fs storage.FS, paths *project.Context, base *SharedBase, backendKV state.KV,
) (state.KV, *caldavalias.Service, jobs.Queue, error) {
	stateKV, aliases, err := buildStateAndAliases(fs, paths, backendKV)
	if err != nil {
		return nil, nil, nil, err
	}
	jobQueue, err := buildJobQueue(base)
	if err != nil {
		return nil, nil, nil, err
	}
	return stateKV, aliases, jobQueue, nil
}

// buildStateAndAliases builds the per-user state store and the CalDAV alias
// service that rides on it.
//
// The alias service is its own injected concern (like the version service),
// not a store capability. Both are built BEFORE the entitymanager because its
// rename/delete hooks take the alias service: only the write choke-point knows
// old->new, so an alias rewrite has to ride along with the write.
//
// backendKV, when non-nil, is a store-backed state store (pgstore's — see
// stateKVFor) and wins over the filesystem. That is what makes the render
// cache, user settings, the operator logo and the CalDAV alias table shared by
// every process serving a schema instead of node-local (TKT-VC27L3). It is nil
// on the fs/memory builds, where a project IS one directory on one machine and
// the filesystem is the right home.
func buildStateAndAliases(
	fs storage.FS, paths *project.Context, backendKV state.KV,
) (state.KV, *caldavalias.Service, error) {
	stateKV := backendKV
	if stateKV == nil {
		var err error
		stateKV, err = buildStateKV(fs, paths)
		if err != nil {
			return nil, nil, err
		}
	}
	aliases, err := caldavalias.New(context.Background(), stateKV)
	if err != nil {
		// A corrupt alias table must NOT be fatal here.
		//
		// This runs on EVERY appbuild path — `rela list`, `analyze`, the MCP
		// server, the desktop app — the vast majority of which never serve
		// CalDAV. Failing hard meant a truncated file in the gitignored cache
		// dir killed every command on a project with no `caldav:` block at all,
		// citing a subsystem the user had never enabled.
		//
		// The fail-loud reasoning (caldavalias.ErrCorrupt) is still right where
		// it applies: starting CalDAV with an empty table makes every synced
		// client re-create its entries as new entities, doubling a user's list.
		// That check now lives at the serving boundary — registerCalDAVRoutes
		// refuses to mount without a healthy table — so the failure lands on
		// the path that can actually cause the damage.
		// Name the right location: with a database-backed state store the
		// aliases are a row, not a file, and pointing an operator at a path
		// that does not exist would send them hunting for the wrong thing.
		location := filepath.Join(paths.CacheDir, "caldav", "aliases.json")
		if backendKV != nil {
			location = "database state store, key caldav/aliases.json"
		}
		slog.Warn("caldav alias table unreadable; CalDAV will refuse to serve. "+
			"Clear it to start fresh (synced clients will re-create their entries).",
			"location", location,
			"error", err)
		return stateKV, nil, nil
	}
	return stateKV, aliases, nil
}

// buildStateKV returns a state.KV rooted at paths.CacheDir, or a
// sentinel-error KV when the cache dir is unavailable.
//
// Workspace.State() panicked on a malformed cache path because
// workspace was a process-singleton; appbuild is constructed per
// project on a long-running desktop, so an invalid cache dir bubbles
// up as a New() error that LoadProject can surface to the UI instead
// of crashing the host.
func buildStateKV(fs storage.FS, paths *project.Context) (state.KV, error) {
	if fs == nil || paths == nil || paths.CacheDir == "" {
		return nopKV{}, nil
	}
	rfs, err := storage.NewRootedFS(fs, paths.CacheDir)
	// coverage-ignore-start: defensive: NewRootedFS only errors on nil fs / empty root / filepath.Abs failure; fs and a
	// non-empty CacheDir are
	// guaranteed by the guard above, and filepath.Abs does not fail for these inputs
	if err != nil {
		return nil, fmt.Errorf("build state KV: invalid root %q: %w", paths.CacheDir, err)
	}
	// coverage-ignore-end
	return state.NewFSKV(rfs), nil
}

// nopKV is the fallback state.KV used when no cache directory is
// available. Get returns [os.ErrNotExist] so callers that treat
// missing-key as a normal state (e.g. scheduler reading a never-set
// last-run timestamp) continue to work; Put/Delete silently no-op.
//
// This deliberately differs from workspace.nopState (which returned
// "no repository configured" from every method): scheduler-style
// state callers expect missing-key to be silent, not a hard error.
// A "no backend" condition is the same as an empty backend from the
// caller's point of view.
type nopKV struct{}

func (nopKV) Get(context.Context, string) ([]byte, error) { return nil, os.ErrNotExist }
func (nopKV) Put(context.Context, string, []byte) error   { return nil }
func (nopKV) Delete(context.Context, string) error        { return nil }

// ungatedBinder answers `related(...)` against the raw store, for the
// operator- and system-trust paths. It cannot fail: meta and st are required
// by every caller before it gets here.
func ungatedBinder(meta *metamodel.Metamodel, st store.Store) *relresolve.Binder {
	b, err := relresolve.NewStoreBinder(meta, relresolve.Ungated, st)
	if err != nil {
		// coverage-ignore: invariant: meta and st are non-nil at every call site
		panic(err)
	}
	return b
}

// newValidator builds the principal-bound validator of [Services.GatedReads].
// Its traversals go through gate, the same tier as reader.
func newValidator(
	reader lua.EntityReader, meta *metamodel.Metamodel, deps lua.ReadDeps, gate relresolve.Gate, st store.Store,
) validator.Validator {
	b, err := relresolve.NewStoreBinder(meta, gate, st)
	if err == nil {
		var v *validator.GenericValidator
		if v, err = validator.New(reader, meta, deps, b); err == nil {
			return v
		}
	}
	// coverage-ignore: invariant: every argument is non-nil at the one call site
	panic(err)
}
