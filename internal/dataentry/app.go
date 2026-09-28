package dataentry

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/attachment"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/caldavalias"
	"github.com/Sourcehaven-BV/rela/internal/config"
	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/git"
	"github.com/Sourcehaven-BV/rela/internal/lock"
	"github.com/Sourcehaven-BV/rela/internal/lua"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/migration"
	"github.com/Sourcehaven-BV/rela/internal/openapi"
	"github.com/Sourcehaven-BV/rela/internal/project"
	"github.com/Sourcehaven-BV/rela/internal/relresolve"
	"github.com/Sourcehaven-BV/rela/internal/script"
	"github.com/Sourcehaven-BV/rela/internal/search"
	"github.com/Sourcehaven-BV/rela/internal/state"
	"github.com/Sourcehaven-BV/rela/internal/storage"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/templating"
	"github.com/Sourcehaven-BV/rela/internal/tracer"
	"github.com/Sourcehaven-BV/rela/internal/userstate"
	"github.com/Sourcehaven-BV/rela/internal/validator"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

// ConfigFile is the conventional filename for data-entry configuration within a rela project.
const ConfigFile = dataentryconfig.ConfigFile

// userDefaultsFile is the filename for user-specific default values within the .rela directory.
const userDefaultsFile = "user-defaults.yaml"

// userPaletteFile is the filename for user-specific palette overrides within the .rela directory.
const userPaletteFile = "palette.yaml"

// appEntityWriter is the write surface App itself calls — the CalDAV write
// path, entity/relation history restore, and the value it hands to the Lua
// writer runtime. See the internal/entitymanager package doc for the
// consumer-side rule this follows (TKT-IVSJV6).
//
// Exactly the seven methods App invokes. RenameEntity and ValidateCreate are
// absent: the SPA has no rename affordance (renames are a CLI and MCP
// operation), and the form's dry-run preview lives on writeHandler, which
// declares its own narrower entityMutator.
//
// This stays minimal because NewApp takes the CONCRETE *entitymanager.Manager
// and passes that to the sub-handler constructors, rather than distributing
// this narrowed value. Otherwise App would have to hold the union of its own
// calls and every child's — ValidateCreate for writeHandler, and so on — which
// is the "wide because it is a distributor" problem this ticket removed one
// level up. A composition root taking the concrete type is the right shape;
// each handler narrows at its own field.
type appEntityWriter interface {
	CreateEntity(ctx context.Context, e *entity.Entity, opts entity.CreateOptions) (*entity.CreateResult, error)
	UpdateEntity(ctx context.Context, e *entity.Entity) (*entity.UpdateResult, error)
	PatchEntity(ctx context.Context, id string, p entity.Patch) (*entity.UpdateResult, error)
	DeleteEntity(ctx context.Context, id string, cascade bool) (*entity.DeleteResult, error)
	CreateRelation(
		ctx context.Context, from, relType, to string, opts entity.RelationOptions,
	) (*entity.Relation, error)
	UpdateRelation(
		ctx context.Context, from, relType, to string, opts entity.RelationOptions,
	) (*entity.Relation, error)
	DeleteRelation(ctx context.Context, from, relType, to string) error
}

// App is the central application struct for the data-entry server.
//
// # Concurrency model
//
// The co-derived reload core (config, metamodel, style map, OpenAPI
// generator) lives in an immutable [Schema] published via [schemaProvider]'s
// atomic.Pointer. Handlers call a.State() once at entry and work against a
// coherent snapshot for the duration of the request — no lock acquisition, no
// risk of observing a half-reloaded world. Independently-owned reloadable
// state (logo, palette, user defaults) lives in its own self-synchronized
// service, not the snapshot.
//
// Reloads (triggered by the file watcher via reloadConfig) derive a new Schema
// and publish it atomically via a.schema.Reload. The previous snapshot is
// garbage-collected once no reader holds it.
//
// Mutations run concurrently; there is no App-level write lock
// (TKT-WE0S2K). Readers go through a.State(). reloadMu serializes the
// reload itself.
//
// TODO(TKT-R68TV8): App is a god-object. Decompose toward the
// 40-method load line — extract the API/serialization/relation services into
// their own types. Ratchet this number DOWN as methods move out; never up
// EXCEPT for a new required route handler (App owns one method per registered
// HTTP route by the router's design). The sync route cluster (16 methods) moved
// to syncHandler (170 → 154); the command cluster (11 methods) moved to
// commandHandler (154 → 143); the attachment cluster (12 methods) moved to
// attachmentHandler / package functions (143 → 131); the write nucleus —
// entity/relation CRUD, clone, conflict-resolve, and the modern relations
// reconciler (18 methods) — moved to writeHandler (131 → 114); the Lua
// action handler joined it (115 → 114, from a base that had absorbed the
// DEC-O59WM4 script-read helpers), completing the write surface: every
// write path now lives on or routes through writeHandler. The
// views cluster — view traversal, section building, the /_views,
// /_sidepanel, /_sidebar handlers, and the form-resolution helpers (16
// methods) — moved to viewsHandler, three receiver-free helpers became
// package functions, and the dead ungated server-rendered nav path (5
// methods, #1043) was deleted (114 → 90).
//
// The directive lags the real count (TKT-N0IKN9 tracks decomposing App); it is
// a ratchet target, not a budget to spend. SetUserState took it from 98 to 99 —
// it follows the existing SetSecurityConfig / SetJWTGate setter idiom rather
// than becoming a 12th positional NewApp parameter — and the CalDAV alias
// setter that landed alongside it took the count to 100, a later one to 101,
// and redactedForSuggestion to 102 — the field-redaction seam the next-action
// candidate path needs, which has to reach affordanceService.
//
// TKT-BDG8U9 adds [App.SetRemoteMCP] on the same terms — the public opt-in
// setter for the remote MCP endpoint, matching that setter idiom. The rest of
// that feature deliberately stays OFF App: `registerMCPRoute` takes its
// handler as a parameter and `toolForPath` is a package function, so the
// mount cost one method rather than three.
//
// TKT-N8XQ2R adds [App.SetNextActionMatchers] on the same terms (103 -> 104):
// the predicate compiler backing a source's `condition:` lives above this
// package, so it arrives through the same setter idiom rather than a 13th
// positional NewApp parameter. The compiling and matching themselves stay OFF
// App entirely — conditionlint owns them and appbuild bridges — so the feature
// cost one method, not a subsystem.
// TKT-DN37J2 adds [App.SetWorlds] on the same terms, and the same discipline
// held for the rest: `resolveWorld`, `attachWorld` and `worldCapablePath` are
// all package functions taking what they need, so request-level world
// selection cost ONE method rather than four.
//
// The theme/settings/palette cluster (12 methods — /_theme/logo CRUD, the
// /_theme export/import pair, and the /_settings + /_palette CRUD) moved to
// appearanceHandler (TKT-8AJ1PM, 104 → 92).
//
// The search-query pipeline (5 methods — executeQuery and its free-text
// branch, the list `?q=` id-set helper, and the sort / property-filter passes
// they share) moved to queryService, and the dead isRelationLinked was deleted
// (TKT-SJ0LRS, 92 → 86).
//
// TKT-WRLDAPI item 4 (world-scoped relations) cost ZERO methods, and that was
// not free discipline — it was written as five methods first and plimsoll
// failed the build. They became package functions taking their seams
// explicitly (`worldOutgoingForEntity`, `worldNeighborsForPage`,
// `includeCandidates`, `defaultWorldCandidates`, `worldCandidates`, plus
// `SetWorldNeighbors`), which reads better anyway: the seams a world-scoped
// link read depends on are named in the signature rather than reached through
// this struct. The load line doing its job is worth recording, because the
// habit it interrupts is the one that produced the pre-extraction 104.
//
// The merge of FEAT-9CD2MX with develop adds SetWorlds on top of develop's
// extractions (86 → 87). Neither side grew it carelessly — the worlds side
// cost exactly one method and said why above — but the integration is where
// the count actually moves, so it is recorded here rather than in either
// branch.
//
// 87 → 88 for SetComments (TKT-FIO205). The commentary routes themselves live
// on commentsHandler (the exportHandler pattern), so the subsystem cost App
// exactly one method: the public wiring setter, which belongs here alongside
// SetCalDAVAliases and SetUserState. Still a ratchet target — see TKT-N0IKN9.
//
// SetComments is also App's 21st EXPORTED method, one past the default line of
// 20. It is a wiring setter in the established shape (SetCalDAVAliases,
// SetUserState, SetWorlds), and the alternative — reaching into App from
// appbuild to assign the field — would trade a named seam for a hidden one.
// The routes themselves are on commentsHandler, so the public surface grew by
// exactly the one setter. Ratchet target, as above.
//
// TKT-EVR2TU adds [App.SetQueryScopeResolver] on the same terms (21 -> 22):
// the scope compiler backing `query_scopes:` lives above this package, so it
// arrives through the same setter idiom. The rest stayed OFF App exactly as
// the next-action feature did — `viewQueryScope`, `queryScopeParam` and
// `scopedSortedEntitiesScoped` are package functions taking what they need,
// and the compiling lives in internal/scopes with appbuild bridging. The
// feature cost one method, not a subsystem. Ratchet target, as above.
//
// 89 -> 90 for SetViewConditions (TKT-LPLZ1V), and 22 -> 23 exported, on the
// same terms: it is a wiring setter in the established shape, and the
// alternative — appbuild assigning the field directly — trades a named seam
// for a hidden one.
//
// The subsystem cost App exactly that one method, and not by luck. The
// condition lookup was written as an App method first and plimsoll failed the
// build; it became a package function taking its seams explicitly
// (`viewCondition`), which reads better for the reason TKT-WRLDAPI's note
// gives — what a lookup depends on is named in the signature rather than
// reached through this struct. Recorded because the load line interrupting
// that habit is the whole point of it. Ratchet target, as above.
//
//plimsoll:max-methods=91
//plimsoll:max-exported-methods=23
type App struct {
	// Primitives — immutable after NewApp.
	fs    storage.FS
	paths *project.Context

	// Core services. Some are passed in (store, entityManager,
	// searcher); the rest are constructed from primitives inside
	// NewApp.
	store store.Store
	// versions is the content-versioning service (entity + relation history
	// reads), a pgstore-only injected concern — nil on fs/mem builds, where the
	// history endpoints return 501. The history handlers bind the narrow
	// sub-interface they need rather than type-asserting the store.
	versions      store.VersionService
	entityManager appEntityWriter

	// caldavAliases links CalDAV resources to entities. Optional: nil when no
	// alias service is wired, in which case the CalDAV routes are not served
	// (a collection with no way to remember client-created resources would
	// duplicate every to-do on the next sync).
	caldavAliases *caldavalias.Service
	// worlds resolves a `?world=` name to its compiled scope. Nil until
	// [App.SetWorlds] is called, in which case the App serves the default
	// world only and refuses any other `?world=` — see world.go.
	worlds WorldLookup
	// copies serves the copy surface (list-by-source, invoke-by-name).
	// Constructed in NewApp from the entity manager, which is required, so
	// this is never nil — see copiesHandler's doc for why a nil-and-skip
	// design would render a wiring bug as a valid domain answer.
	copies *copiesHandler
	// worldNeighbors resolves an entity's LINKS under the request's world
	// (TKT-WRLDAPI item 4). Nil until [SetWorldNeighbors] is called, in
	// which case a world-bound response carries no relations — the pre-item-4
	// behavior, which is safe but is the gap RULING 12 closed. Separate from
	// `worlds` because the two are injected from different places: the world
	// LOOKUP is a compiled map, this is a store-backed capability.
	worldNeighbors *worldNeighbors
	// comments owns the commentary routes (TKT-FIO205). Always non-nil; the
	// SERVICE inside it is nil when the metamodel declares no enabled
	// `comments:` block, which is the "feature absent" signal — the routes 404
	// and no storage is touched.
	comments *commentsHandler
	searcher search.Searcher
	// visibleSearcher is the ACL-scoped search seam (TKT-BA8BSX):
	// executeQuery routes free-text searches through it so /_search
	// and the _position search scope only ever see hits the request
	// principal may read. Per-backend wiring: search.NewVisible over
	// the regular searcher on fs/memory builds, pgstore-native on the
	// postgres build.
	visibleSearcher search.VisibleSearcher
	// userState backs the next-action layer's per-user snooze / mute /
	// cooldown records. NOT graph content: a snooze is a fact about one
	// person's relationship to a suggestion, and storing it as an entity
	// would flood the audit log and the postgres version sweep (cooldown
	// alone writes on every render). Nil when the deployment wires no
	// backend — the next-action endpoints then report "not configured"
	// rather than silently forgetting what users asked to hide.
	userState userstate.Store

	// nextActionMatchers compiles a source's `condition:` into a predicate
	// matcher. Injected because the compiler lives above this package (it
	// needs predicatefns + the metamodel), the same way the composition root
	// picks the userstate backend while consumers take the seam.
	//
	// Takes the live config + metamodel rather than a prebuilt lookup: both
	// reload at runtime, and a lookup captured at boot would evaluate a stale
	// condition after an operator edits data-entry.yaml. Nil when no
	// deployment wired it — sources declaring a condition then fail engine
	// construction rather than silently matching everything.
	nextActionMatchers NextActionMatcherFunc

	// viewConditions resolves a list's or kanban's compiled `condition:`.
	// Injected for the same reason nextActionMatchers is: the compiler lives
	// above this package, and arch-lint forbids dataentry importing
	// conditionlint or predicate.
	//
	// Nil when no deployment wired it, which means every view behaves as it
	// did before conditions existed. That is the safe direction here — a
	// condition only ever NARROWS a view, so an unwired lookup shows a
	// superset of the configured view and never anything the ACL would
	// withhold. It is not safe for the reverse, which is why a condition
	// that fails to COMPILE is a startup error rather than a silent widening.
	viewConditions ViewConditionFunc

	// queryScopes compiles `query_scopes:`; see SetQueryScopeResolver.
	queryScopes QueryScopeResolverFunc

	// visibleReader is the ACL-bounded entity-read seam (TKT-N26KLB): the
	// entity-read analog of visibleSearcher. Read handlers gate single-GET
	// and include-filtering through it so the read gate is applied
	// structurally rather than by per-call-site convention. Wraps the same
	// `store` handle; the gate is resolved per-request from the context.
	visibleReader visibleReader
	// reader is the ungated entity/relation read seam over the store. Extracted
	// from App (TKT-N26KLB); a single-dep leaf shared by read/write/affordance
	// paths. ACL scoping lives in visibleReader, not here.
	reader entityReader
	// viewReader is the row-gating + field-redacting read-out seam
	// (visibility.PolicyReader, DEC-ZBI39P) for the view pipeline. executeView
	// loads raw from the store during traversal (a where: filter may reference
	// a hidden property, and edges are walked by id), then routes the assembled
	// result through viewReader so section builders receive already-redacted
	// entities — closing BUG-9QL9XV (property values) and BUG-R9EHKV (titles,
	// via Redact's _title fallback) structurally rather than per-field in
	// sections.go. Same wiring as the export handler's visReader.
	viewReader visibility.Reader
	tracer     tracer.Tracer
	validator  validator.Validator
	// analyze runs the read-only graph-analysis checks. Extracted from App
	// (TKT-N26KLB M5.1); holds its own {store, tracer, validator} and takes
	// the metamodel snapshot per call.
	analyze analyzeService
	// affordances computes the _actions/field/relation affordance maps and runs
	// write-time affordance validation. Extracted from App (TKT-N26KLB M5.2);
	// shares the same acl.ACL as the write path (contract-test invariant).
	affordances affordanceService
	// serializer renders an entity into its v1.Entity wire shape. Extracted from
	// App (TKT-N26KLB); pure transform — handlers pass the entity's already-
	// loaded outgoing relations, the serializer does no loading.
	serializer entitySerializer
	// logo owns the user-uploaded sidebar logo — persistence AND the served
	// in-memory cache — self-synchronized. Extracted from the schema snapshot so the
	// logo no longer rides the App-wide snapshot.
	logo *logoStore
	// palette owns the user palette override and the resolved palette (derived
	// from Cfg.Palette + the override). Self-synchronized; extracted from
	// Schema. The reload/save paths hand it the current Cfg.Palette so it can
	// recompute — see paletteService.Reresolve.
	palette *paletteService
	// settings owns the per-user default values (create-form/relation defaults).
	// Self-synchronized; extracted from the schema snapshot.
	settings *settingsService
	// sync owns the /api/sync/ route cluster (fs-client ↔ pg-server
	// replication). Extracted from App (TKT-R68TV8); holds narrow store/deleter
	// surfaces.
	sync *syncHandler
	// commands owns the user-configured command surface (SSE shell-exec,
	// file/URL launchers, command resolution). Extracted from App (TKT-R68TV8);
	// holds narrow closures over the schema snapshot, Services bundle, project
	// root, and the view executor.
	commands *commandHandler
	// attachments owns the entity-attachment routes (upload/download/detach).
	// Extracted from App (TKT-R68TV8); closures over the swappable acl/audit/
	// field-resolver collaborators.
	attachments *attachmentHandler
	// export owns the view-export routes (transform list, entity/list export).
	// Extracted from App (TKT-JF5JI8) to keep App under its plimsoll method cap.
	export *exportHandler

	// write owns the entity/relation CRUD + clone + conflict-resolve write
	// nucleus (TKT-R68TV8 M5.4).
	write *writeHandler
	// views owns the read-only view-assembly surface: view traversal,
	// section building, and the /_views, /_sidepanel, /_sidebar endpoints
	// (TKT-R68TV8). This surface never mutates.
	views *viewsHandler
	// appearance owns the theme/settings/palette surface: /_theme/logo,
	// /_theme/export, /_theme/import, /_settings, /_palette (TKT-8AJ1PM).
	// Pure glue over the self-synchronized logo/palette/settings services.
	appearance *appearanceHandler
	// queries owns the search-query pipeline: executeQuery (shared by
	// /_search, the `_position` search scope and the next-action engine),
	// the list endpoint's `?q=` id-set helper, and the sort / property-filter
	// passes those share (TKT-SJ0LRS). Read-only.
	queries *queryService
	// gantt owns the read-only gantt tree endpoint (TKT-MW28U5). Like views,
	// it never mutates.
	gantt     *ganttHandler
	templater templating.Templater
	cfgLoader config.Loader
	kv        state.KV
	acl       acl.ACL

	// attachmentLocker serializes attachment writers to one (entity,
	// property), for web uploads and remote MCP tools alike. Built once from
	// the store by [lock.For], so on postgres it also excludes other
	// processes.
	attachmentLocker attachment.Locker

	// attachmentUploads bounds concurrent uploads in this process, web and
	// remote MCP together.
	attachmentUploads *attachment.Limiter

	// attachmentRunner drives external scan/transform commands for uploads.
	// nil out-of-box → uploads get native MIME validation only (Phase 2 wires
	// the cmd: harness). See internal/attachment.PolicyProcessor.
	attachmentRunner attachment.CommandRunner

	// documents renders and caches documents. Created once in NewApp so
	// singleflight deduplication is stable across requests.
	documents *documentService

	// scriptEngine is the long-lived Lua script engine used for action
	// execution. Holding one per App (rather than per-request) means
	// every request shares the same rela.cache state, which is the
	// whole point of having a cache in a long-lived server.
	scriptEngine *script.Engine

	// schema publishes the current reloadable co-derived core (config,
	// metamodel, style map, OpenAPI generator). Readers: a.State(). Writers:
	// the watcher's reload path (reloadConfig → schema.Reload). Initial
	// snapshot is published in NewApp.
	schema schemaProvider

	// reloadMu serializes reloadConfig. schema.Reload publishes by
	// load-then-store, so two overlapping reloads could publish the older
	// file last. Readers never take it; they go through a.State().
	reloadMu sync.Mutex

	// gitOps provides git operations when git is enabled. Set once in
	// NewApp; never reloaded.
	gitOps *git.Ops

	// broker delivers SSE events to connected browsers for live-reload.
	broker *eventBroker

	// stopConfigWatch releases the data-entry.yaml subscription. Set by
	// StartWatching; nil when watching is not active.
	stopConfigWatch func()

	// stopStoreWatch cancels the store-event -> SSE bridge subscription. Set by
	// StartWatching; nil when watching is not active.
	stopStoreWatch func()

	// security holds the configured Host/Origin allowlists. Set via
	// SetSecurityConfig before NewRouter; nil disables the middlewares
	// (only sensible in unit tests where no HTTP layer is exercised).
	security *security

	// principalResolver is the per-request audit Principal resolver.
	// Set via SetPrincipalResolver before NewRouter; nil falls back
	// to defaultPrincipalResolver (Tool=data-entry, User=unknown).
	// cmd/rela-server chains an env resolver + a header resolver
	// here when --principal-header is set.
	principalResolver PrincipalResolver

	// jwtGate, when non-nil, enforces fail-closed verified-JWT identity on
	// the data API: every /api/ request must carry a valid assertion or is
	// denied 401. Set via SetJWTGate before NewRouter. Mutually exclusive
	// with a header/env principal chain — cmd/rela-server refuses to start
	// with both, so a JWT failure can never downgrade to a spoofable header.
	jwtGate *JWTGateConfig

	// mcpHandler, when non-nil, serves the remote MCP endpoint at
	// [MCPPath]. Built once by SetRemoteMCP (before NewRouter) from an
	// [MCPHandlerFactory]; nil means the route is not registered at
	// all, so an upgraded server serves no MCP until an operator opts
	// in. SetRemoteMCP refuses to enable it without jwtGate — see its
	// doc comment for why.
	//
	// It is a plain http.Handler because `internal/mcp` is the only
	// component allowed to import the MCP go-sdk (arch-lint).
	mcpHandler http.Handler

	// principalHeader is the name of the HTTP header that carries the
	// principal identity (the --principal-header flag value), or ""
	// when no header is configured. Used by noCacheMiddleware to emit
	// `Vary: <header>` on /api/ responses — under ACL those responses
	// are per-principal, and a shared cache keyed only on the URL
	// would serve principal A's filtered list to principal B
	// (TKT-VMD8 AC10, RR-VDTW). Set via SetPrincipalHeader before
	// NewRouter.
	principalHeader string

	// fieldResolver decides per-entity field, option, and
	// relation-meta affordances surfaced as `_fields` / `_relations`
	// on the wire and enforced on writes. Required (never nil) —
	// callers that don't want affordances pass NopFieldVerdictResolver{}.
	// The eventual predicate-engine ticket replaces the stub
	// implementations with a policy-driven resolver via the same
	// interface.
	fieldResolver FieldVerdictResolver

	// webhook holds the inbound-IdP webhook receiver (verifier + target action +
	// dedup). nil until SetWebhookReceiver is called, in which case the
	// /webhooks/idp route is not mounted. Optional wiring, like security and
	// principalResolver above.
	webhook *webhookReceiver

	// auditSink records short-circuit rejections (affordance gates)
	// that never reach the entitymanager. ACL denials already get a
	// `denied-write` row from the manager; affordance denials emit
	// the same op via this sink so log readers see a unified stream.
	// Required (never nil) — callers pass [audit.Nop] to opt out.
	auditSink audit.Audit
}

// StopWatching releases the data-entry.yaml subscription started by
// [App.StartWatching]. The store-level watcher (when present) has its
// own lifecycle managed by the store and is stopped during store
// close, not here — asymmetric on purpose: dataentry doesn't own the
// store, only its config subscription.
// StopWatching is lifecycle-only and must be called from a single goroutine
// (it is the StartWatching counterpart). The stop fields are not synchronized;
// concurrent Start/Stop is not supported.
func (a *App) StopWatching() {
	if a.stopConfigWatch != nil {
		a.stopConfigWatch()
		a.stopConfigWatch = nil
	}
	if a.stopStoreWatch != nil {
		a.stopStoreWatch()
		a.stopStoreWatch = nil
	}
}

// State returns the current reloadable [Schema] snapshot. Handlers should call
// State() once at entry and use the returned snapshot consistently
// throughout, instead of making multiple calls that could see different
// snapshots after a concurrent reload.
func (a *App) State() *Schema { return a.schema.Current() }

// Cfg returns the current data-entry config (convenience accessor).
// Equivalent to a.State().Cfg.
func (a *App) Cfg() *Config { return a.State().Cfg }

// Meta returns the current metamodel (convenience accessor).
func (a *App) Meta() *metamodel.Metamodel { return a.State().Meta }

// luaWriteDeps builds a lua.WriteDeps bundle using the current Schema
// metamodel. Called per action-script invocation so that metamodel reloads
// propagate to scripts without requiring app reconstruction. All other
// fields are immutable for the App's lifetime.
// Reads are ACL-BOUND to the request principal (DEC-O59WM4): an action
// script, an export_render override, or an MCP-invoked script sees exactly
// the caller's view — hidden entities absent, hidden properties redacted.
// Identity resolves per call from the ctx, so one bundle serves every
// request. Note there is no raw read handle here at all: update_entity
// patches through the manager, which does its own write-prep read
// (TKT-80EWGM), so a redacted read can no longer feed a write.
func (a *App) luaWriteDeps() lua.WriteDeps {
	redactor := appRedactor(a)
	return lua.WriteDeps{
		ReadDeps: lua.ReadDeps{
			VisibleReader: a.scriptReader(redactor),
			Tracer:        a.scriptTracer(redactor),
			Searcher:      a.searcher,
			Meta:          a.Meta(),
			ProjectRoot:   a.paths.Root,
		},
		EntityManager: a.entityManager,
	}
}

// appRedactor returns the field-redaction seam over the affordance service
// (visibility.FieldRedactor), closing over App.affordances via a closure so it
// stays valid after test builders rebind the service. It is the one source of
// the affRedactor used by the script reader, the export handler, and the view
// pipeline. A package function rather than an App method so it doesn't add to
// App's plimsoll method count (the type is at its load line — see the struct
// doc; DRY here must not cost a ratchet).
func appRedactor(a *App) visibility.FieldRedactor {
	return affRedactor{aff: func() affordanceService { return a.affordances }}
}

// scriptReader returns the ACL-bound read-out handle for script runtimes,
// or the raw store when no Declarative policy is configured (the NopACL
// path — byte-identical to pre-ACL behavior, not a bypass).
//
// When a policy IS configured but the gate cannot be built, this REFUSES
// (returns [visibility.DenyReader]) rather than degrading to the raw store
// — matching appbuild's unattended-path wiring (RR-GKCZO5, rela#1198).
//
// This wiring was fail-open on the theory that its callers are interactive,
// so a broken gate would surface as an immediate, human-visible outage.
// That does not hold: of the three consumers of App.luaWriteDeps —
// actions.go (interactive), document.go/export_render, and webhook.go — the
// IdP webhook is unattended machine-to-machine, running as `webhook:<event>`
// with nobody watching the log. An unattended job quietly reverting to
// full-graph reads is an unbounded silent disclosure into whatever it sends
// onward — the exact failure DenyReader exists to stop.
//
// Failing closed costs the interactive callers nothing they were promised:
// [visibility.ErrReaderUnavailable] is a loud, immediate, diagnosable error,
// which is what the fail-open argument actually wanted — just without the
// ungated read. A genuine DENY remains a deny, distinct from this error.
//
// DELIBERATE DIVERGENCE from appbuild: that seam substitutes
// [visibility.NopRedactor] for a nil redactor, because its callers
// (scheduler, cascades) legitimately have no affordance resolver and row
// gating alone is the best available there (RR-7408F5). Data-entry always
// has one, so a nil redactor here is a WIRING BUG, not a capability gap —
// silently swapping in NopRedactor would drop field-level redaction on a
// path that is supposed to enforce it. Do not "align" these by copying
// appbuild's guard: it would convert a caught bug into a silent downgrade,
// and delete the fault path failclosed_test.go exercises.
func (a *App) scriptReader(redactor visibility.FieldRedactor) lua.EntityReader {
	return gatedScriptReader(a.acl, a.store, redactor)
}

// lateGatedReader is a lua.EntityReader that resolves the gated reader from the
// LIVE App on every call, so a reassignment of a.acl / a.fieldResolver (tests
// rebind these after construction; a policy reload can too) is honored rather
// than frozen at wiring time. The validator and analyzeService hold their reader
// as a value, so a construction-time-captured reader would go stale — this
// wrapper is the fix. It mirrors why luaWriteDeps is a method invoked per call.
type lateGatedReader struct{ app *App }

func (r lateGatedReader) reader() lua.EntityReader {
	return r.app.scriptReader(appRedactor(r.app))
}

func (r lateGatedReader) GetEntity(ctx context.Context, id string) (*entity.Entity, error) {
	return r.reader().GetEntity(ctx, id)
}

func (r lateGatedReader) ListEntities(ctx context.Context, q store.EntityQuery) iter.Seq2[*entity.Entity, error] {
	return r.reader().ListEntities(ctx, q)
}

// ListEntityHeaders resolves the gated reader per call like the rest of this
// type, then uses its header path when it has one.
//
// The type assertion is not a gate decision: every reader gatedScriptReader
// can return (ScriptReader, UnrestrictedReader, DenyReader) implements the
// header surface, so the fallback below is unreachable in production wiring
// and exists only so a narrower test double stays valid. It degrades to
// whole-entity gating — more data read, never less gating.
func (r lateGatedReader) ListEntityHeaders(
	ctx context.Context, q store.EntityQuery,
) iter.Seq2[store.EntityHeader, error] {
	reader := r.reader()
	if hr, ok := reader.(interface {
		ListEntityHeaders(context.Context, store.EntityQuery) iter.Seq2[store.EntityHeader, error]
	}); ok {
		return hr.ListEntityHeaders(ctx, q)
	}
	return func(yield func(store.EntityHeader, error) bool) {
		for e, err := range reader.ListEntities(ctx, q) {
			if err != nil {
				yield(store.EntityHeader{}, err)
				return
			}
			if !yield(store.HeaderOf(e), nil) {
				return
			}
		}
	}
}

func (r lateGatedReader) ListRelations(ctx context.Context, q store.RelationQuery) iter.Seq2[*entity.Relation, error] {
	return r.reader().ListRelations(ctx, q)
}

// lateGatedTracer is the tracer.Tracer counterpart of lateGatedReader: it
// resolves the gated tracer (scriptTracer, which prunes hidden nodes and fails
// closed) from the LIVE App per call, so a rule's rela.trace_from/trace_to/
// find_path cannot walk into entities the requester cannot see. Wired into the
// validator's ReadDeps.Tracer so the traversal seam is gated by construction,
// not left safe only because a Lua message never reaches the wire (code review).
type lateGatedTracer struct{ app *App }

func (t lateGatedTracer) tracer() tracer.Tracer {
	return t.app.scriptTracer(appRedactor(t.app))
}

func (t lateGatedTracer) TraceFrom(ctx context.Context, id string, maxDepth int) *tracer.TraceResult {
	return t.tracer().TraceFrom(ctx, id, maxDepth)
}

func (t lateGatedTracer) TraceTo(ctx context.Context, id string, maxDepth int) *tracer.TraceResult {
	return t.tracer().TraceTo(ctx, id, maxDepth)
}

func (t lateGatedTracer) FindPath(ctx context.Context, fromID, toID string) []tracer.PathStep {
	return t.tracer().FindPath(ctx, fromID, toID)
}

func (t lateGatedTracer) FindOrphans(ctx context.Context) ([]string, error) {
	return t.tracer().FindOrphans(ctx)
}

func (t lateGatedTracer) HasCycle(ctx context.Context, startID string) bool {
	return t.tracer().HasCycle(ctx, startID)
}

// elevationRecorder adapts the audit sink onto lua.ElevationRecorder for an
// elevated document render (TKT-Y3JVFK).
//
// It exists for ONE reason: the nil conversion. audit.NewElevationRecorder
// returns *audit.ElevationRecorder, and assigning a nil one straight into
// lua's interface-typed field yields a TYPED nil, which is != nil — lua's
// `ElevationRecorder == nil` guard would pass and the first elevated read
// would nil-deref. Returning the INTERFACE type makes the nil a real nil.
// Same trap, same fix as appbuild.NewElevationAuditor (RR-5QQL1Z); duplicated
// rather than imported because dataentry must not depend on the wiring layer.
//
// auditSink is required and non-nil on a constructed App (callers pass
// audit.Nop), so the nil branch is defense against a future construction path,
// not a live case.
func elevationRecorder(sink audit.Audit) lua.ElevationRecorder {
	if sink == nil {
		return nil
	}
	return audit.NewElevationRecorder(sink)
}

// gatedScriptReader builds the ACL-bound read-out handle from an acl
// implementation directly (not App.acl), so it can be wired at CONSTRUCTION
// time — before the App receiver exists — for the validator's read deps. Under
// NopACL it degrades to the raw store (byte-identical to pre-ACL); under a
// Declarative policy it row-gates + field-redacts, resolving the principal from
// ctx per call; a construction fault REFUSES (DenyReader) rather than reading
// ungated. Same policy the per-request App.scriptReader wraps.
func gatedScriptReader(aclImpl acl.ACL, store store.Store, redactor visibility.FieldRedactor) lua.EntityReader {
	d, ok := aclImpl.(*acl.Declarative)
	if !ok || d == nil {
		// Named so the NopACL path is greppable alongside every other
		// ungated read site (TKT-1WV50C).
		return visibility.Unrestricted(store)
	}
	gate, err := visibility.NewDeclarativeGate(d)
	if err != nil {
		slog.Error("dataentry: ACL gate unavailable; script reads REFUSED", "err", err)
		return visibility.DenyReader{}
	}
	reader, err := visibility.NewPolicyReader(gate, redactor, store)
	if err != nil {
		slog.Error("dataentry: policy reader unavailable; script reads REFUSED", "err", err)
		return visibility.DenyReader{}
	}
	sr, err := visibility.NewScriptReader(reader, store, gate)
	if err != nil {
		slog.Error("dataentry: script reader unavailable; script reads REFUSED", "err", err)
		return visibility.DenyReader{}
	}
	return sr
}

// scriptTraversalGate authorizes a validation rule's traversal under the same
// tier as [gatedScriptReader], resolved at call time because a.acl is set
// after construction. Deriving both from a.acl, not from the request on ctx,
// keeps a rule's related() from seeing more than its reads: a ctx without the
// middleware's read gate is refused under a policy, not answered ungated.
func scriptTraversalGate(a *App) relresolve.Gate {
	return func(ctx context.Context, candidateType string, hop acl.TraversalHop) (*store.RelationPredicate, error) {
		d, ok := a.acl.(*acl.Declarative)
		if !ok || d == nil {
			return relresolve.Ungated(ctx, candidateType, hop)
		}
		gate, err := visibility.NewDeclarativeGate(d)
		if err != nil {
			// coverage-ignore: invariant: d is non-nil here
			return nil, fmt.Errorf("%w: %w", acl.ErrTraversalUnsupported, err)
		}
		return gate.GateTraversal(ctx, candidateType, hop)
	}
}

// scriptTracer wraps the tracer in the visibility decorator when a
// Declarative policy is configured. Trace bindings are unchanged either
// way — pruning happens inside the decorator.
//
// Construction faults REFUSE ([visibility.DenyTracer]) for the same reason
// as App.scriptReader: traversal is a read, and an unattended caller must
// not silently walk the whole graph.
func (a *App) scriptTracer(redactor visibility.FieldRedactor) tracer.Tracer {
	d, ok := a.acl.(*acl.Declarative)
	if !ok || d == nil {
		return a.tracer
	}
	gate, err := visibility.NewDeclarativeGate(d)
	if err != nil {
		slog.Error("dataentry: ACL gate unavailable; traversal REFUSED", "err", err)
		return visibility.DenyTracer{}
	}
	vt, err := visibility.NewVisibleTracer(a.tracer, gate, redactor, a.store)
	if err != nil {
		slog.Error("dataentry: visible tracer unavailable; traversal REFUSED", "err", err)
		return visibility.DenyTracer{}
	}
	return vt
}

// SetSecurityConfig configures the HTTP security middlewares applied by
// NewRouter. It must be called before NewRouter.
func (a *App) SetSecurityConfig(cfg SecurityConfig) error {
	s, err := newSecurity(cfg)
	if err != nil {
		return err
	}
	a.security = s
	return nil
}

// SetPrincipalResolver installs a custom [PrincipalResolver] used by
// the router's audit-stamp middleware. Must be called before
// [App.NewRouter]; subsequent changes have no effect on already-built
// routers.
//
// The typical wiring (in cmd/rela-server) chains
// [EnvPrincipalResolver] and [HeaderPrincipalResolver] so a
// `$RELA_DATAENTRY_USER` env var overrides any incoming header and
// the header itself overrides the default. Passing nil restores
// [defaultPrincipalResolver] behavior.
// SetCalDAVAliases installs the CalDAV alias service. Without it the CalDAV
// routes are not registered.
func (a *App) SetCalDAVAliases(s *caldavalias.Service) { a.caldavAliases = s }

func (a *App) SetPrincipalResolver(r PrincipalResolver) {
	a.principalResolver = r
}

// SetPrincipalHeader records the name of the HTTP header that carries
// the principal identity so API responses can declare `Vary` on it.
// Call alongside [App.SetPrincipalResolver] (before [App.NewRouter])
// when wiring a [HeaderPrincipalResolver]; leave unset otherwise.
func (a *App) SetPrincipalHeader(name string) {
	a.principalHeader = name

	// A webhook config may not expose the header THIS deployment trusts for
	// identity. dataentryconfig's static refusal list cannot name it — the flag
	// is an arbitrary string chosen at startup — yet it is the header most
	// certain to carry an authenticated identity, so a hook echoing it into
	// entity content would persist "who called the proxy" as if it were payload
	// data, readable by anyone who can read the entity.
	dataentryconfig.ForbidWebhookHeader(name)
}

// SetJWTGate enables fail-closed verified-JWT identity. Must be called before
// [App.NewRouter]; subsequent changes have no effect on already-built routers.
//
// When set, every [isAPIPath] request must carry an assertion that verifies, or
// it is denied 401 — see [requireVerifiedJWT]. This REPLACES the principal
// resolver for API requests rather than layering over it: JWT identity is
// exclusive, so there is no header or env source to fall back to. Callers must
// not also install a header/env chain via [App.SetPrincipalResolver];
// cmd/rela-server enforces that at startup.
//
// Returns an error when a required field is missing rather than accepting a
// config that cannot work. An empty HeaderName is fatal in a quiet way — every
// assertion would read as absent, so the server would boot clean and then 401
// every API request.
//
// Note the interface-nil caveat: a TYPED nil (e.g. a (*jwtauth.Verifier)(nil)
// stored in the interface) is not == nil and cannot be caught here without
// reflection. Callers must not construct one; cmd/rela-server checks the
// concrete pointer before it ever reaches this interface.
func (a *App) SetJWTGate(cfg JWTGateConfig) error {
	if cfg.Verifier == nil {
		return errors.New("dataentry: jwt gate requires a non-nil Verifier")
	}
	if cfg.HeaderName == "" {
		return errors.New("dataentry: jwt gate requires a non-empty HeaderName")
	}
	a.jwtGate = &cfg
	return nil
}

// NewApp creates and initializes an App. Callers pass in the
// primitives (fs, paths, meta, store) plus the services that depend
// on workspace assembly: entityManager (the production write path)
// and searcher (the live Bleve index). Everything else — state.KV,
// config.Loader, tracer, templater, validator — is constructed
// locally.
//
// The store-level file watcher (live-reload of external entity /
// relation edits) is feature-detected on `st` inside
// [App.StartWatching] via the [storeWatcher] interface; callers do
// not wire it.
//
//nolint:funlen // composition root: validates and wires many optional collaborators, each guarded independently; the branches are wiring steps, not shared logic to extract.
func NewApp(
	fs storage.FS,
	paths *project.Context,
	meta *metamodel.Metamodel,
	st store.Store,
	versions store.VersionService,
	em *entitymanager.Manager,
	searcher search.Searcher,
	visibleSearcher search.VisibleSearcher,
	aclImpl acl.ACL,
	fieldResolver FieldVerdictResolver,
	auditSink audit.Audit,
	stateKV state.KV,
	commandAuthz commandAuthorizer,
) (*App, error) {
	// Reject nil required collaborators up front rather than letting a
	// downstream handler panic on the first request that exercises them.
	// fs and paths can also be nil in tests that take a different code path
	// (newAppFromParts wires them post-construction), so they're checked
	// only when they participate in the construction below.
	if meta == nil {
		return nil, errors.New("dataentry.NewApp: meta is required")
	}
	if st == nil {
		return nil, errors.New("dataentry.NewApp: store is required")
	}
	if em == nil {
		return nil, errors.New("dataentry.NewApp: entityManager is required")
	}
	if searcher == nil {
		return nil, errors.New("dataentry.NewApp: searcher is required")
	}
	if visibleSearcher == nil {
		return nil, errors.New("dataentry.NewApp: visibleSearcher is required (wire appbuild's Services.VisibleSearcher)")
	}
	if aclImpl == nil {
		return nil, errors.New("dataentry.NewApp: acl is required (use acl.NopACL{} to opt out)")
	}
	if fieldResolver == nil {
		return nil, errors.New("dataentry.NewApp: fieldResolver is required (pass NopFieldVerdictResolver{} for permissive default)")
	}
	if auditSink == nil {
		return nil, errors.New("dataentry.NewApp: auditSink is required (pass audit.Nop{} to opt out)")
	}
	if stateKV == nil {
		return nil, errors.New("dataentry.NewApp: stateKV is required (wire appbuild's Services.State())")
	}
	if commandAuthz == nil {
		// Fail closed on a wiring omission: a missing command authorizer must
		// not default to "allow shell exec". Callers pass an explicit impl —
		// SelectCommandAuthorizer(...) in cmd/rela-server, or
		// UngatedCommandAuthorizer() for the in-process desktop/docscapture servers.
		return nil, errors.New("dataentry.NewApp: commandAuthz is required " +
			"(use SelectCommandAuthorizer, or UngatedCommandAuthorizer() for a loopback/in-process server)")
	}
	// Construct reconstructible services from the primitives.
	cfgLoader := config.NewFSLoader(fs, paths.Root)
	// The state store comes from the caller (appbuild's Services.State()) rather
	// than being rebuilt here: on the postgres build it is database-backed, so
	// the render cache, user settings and the operator logo are shared by every
	// process serving the schema. Rebuilding an FSKV here would silently pin
	// those back to this node's .rela/ — the bug where an uploaded logo is
	// visible only on whichever node served the POST (TKT-VC27L3).
	kv := stateKV
	trc := tracer.New(st)
	templater := templating.NewFSTemplater(fs, paths)
	// The validator (val) is built AFTER app.affordances below — its reader is
	// now GATED (TKT-3FL2S6, superseding DEC-O59WM4), which needs the redactor
	// that closes over app.affordances.

	// Load data-entry config from project root
	cfgData, err := cfgLoader.Load(context.Background(), ConfigFile)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", ConfigFile, err)
	}
	cfg, err := loadConfig(cfgData, meta, paths.Root)
	if err != nil {
		return nil, err
	}

	entCount, _ := st.CountEntities(context.Background(), store.EntityQuery{})
	relCount, _ := st.CountRelations(context.Background(), store.RelationQuery{})
	slog.Info("loaded project", "entities", entCount, "relations", relCount)

	// Build style map from config styles
	styleMap, styledTypes := buildStyleMap(cfg, meta)

	scriptEngine := script.NewEngine()
	app := &App{
		fs:              fs,
		paths:           paths,
		store:           st,
		versions:        versions,
		entityManager:   em,
		searcher:        searcher,
		visibleSearcher: visibleSearcher,
		visibleReader:   newVisibleReader(st),
		reader:          entityReader{store: st},
		tracer:          trc,
		templater:       templater,
		cfgLoader:       cfgLoader,
		kv:              kv,
		acl:             aclImpl,
		broker:          newEventBroker(),
		scriptEngine:    scriptEngine,
		fieldResolver:   fieldResolver,
		auditSink:       auditSink,
		// attachmentLocker must be set before the attachment handler copies
		// it below.
		attachmentLocker:  lock.For(st),
		attachmentUploads: attachment.NewLimiter(attachment.DefaultMaxUploads),
	}
	// documentService needs scriptEngine (for Lua renders) and a closure
	// that yields fresh lua.WriteDeps (so metamodel reloads propagate).
	// Constructed after app because luaWriteDeps is a method on App.
	// The elevation bundle is resolved per render and applied ONLY to a
	// document that declares allow_acl_bypass. visibility.Unrestricted names
	// the ungated path so it appears in the grep that enumerates them
	// (TKT-1WV50C), and the recorder makes elevated document reads land in
	// the audit log exactly as cascade ones do.
	app.documents = newDocumentService(st, kv, paths.Root, scriptEngine, app.luaWriteDeps,
		func() documentElevation {
			return documentElevation{
				Reader:   visibility.Unrestricted(st),
				Recorder: elevationRecorder(app.auditSink),
			}
		})

	// The copy surface (RULING 9) is built ONCE, here, for both of its
	// consumers: the `_copies` affordance below and the invoke handler.
	copyOffers, copiesHandler, cerr := wireCopies(em)
	if cerr != nil {
		return nil, fmt.Errorf("dataentry.NewApp: %w", cerr)
	}
	app.copies = copiesHandler

	// affordanceService shares the App's acl/fieldResolver/store and takes the
	// metamodel per-request via app.State(). The two relation-graph reads are
	// App methods, so it's wired after the struct literal. It MUST share the
	// same acl instance as the write path (contract-test invariant).
	app.affordances = affordanceService{
		acl:                func() acl.ACL { return app.acl },
		resolver:           func() FieldVerdictResolver { return app.fieldResolver },
		store:              st,
		meta:               func() *metamodel.Metamodel { return app.State().Meta },
		getEntity:          app.reader.getEntity,
		currentEdgesByPeer: app.currentEdgesByPeer,
		copies:             copyOffers,
		schema:             app.State,
		actionConditions:   func() ViewConditionFunc { return app.viewConditions },
	}

	app.serializer = entitySerializer{affordances: app.affordances}

	// The validator and analyze reads are GATED per requesting principal
	// (TKT-3FL2S6, superseding DEC-O59WM4): a rule cannot read data the requester
	// cannot see, so a hidden value never reaches a validation message or an
	// issue title — the leak closes by construction. gatedReader resolves the
	// principal from ctx per call (one instance serves every request); under
	// NopACL it is the raw store. The trigger entity the validator loads
	// (validator.New's first arg) and its rule bodies' cross-entity lookups
	// (ReadDeps.VisibleReader) both go through it.
	gatedReader := lateGatedReader{app: app}
	readDeps := lua.ReadDeps{
		VisibleReader: gatedReader,
		Tracer:        lateGatedTracer{app: app},
		Searcher:      searcher,
		Meta:          meta,
		ProjectRoot:   paths.Root,
	}
	val, valErr := newGatedValidator(gatedReader, scriptTraversalGate(app), meta, readDeps, st)
	if valErr != nil {
		return nil, valErr
	}
	app.validator = val

	// analyzeService entity reads route through the same gated reader; relation
	// COUNTS stay raw (structural, cannot leak).
	app.analyze = analyzeService{
		reads:     gatedReader,
		relCounts: st,
		tracer:    lateGatedTracer{app: app},
		validator: val,
	}

	// viewReader row-gates + field-redacts entities on their way out of the
	// view pipeline (DEC-ZBI39P). Wired here — after app.affordances — because
	// the redactor closes over it. Same construction as the export handler's
	// visReader: ctx-resolved gate, affordance-backed redactor, raw store.
	viewReader, viewReaderErr := visibility.NewPolicyReader(ctxRowGate{}, appRedactor(app), app.store)
	if viewReaderErr != nil {
		return nil, fmt.Errorf("dataentry: wire view reader: %w", viewReaderErr)
	}
	app.viewReader = viewReader

	app.settings = newSettingsService(kv)

	// palette owns the user override + resolved palette. Surface a broken
	// palette rather than silently falling back to defaults (which the next
	// save would then persist, destroying the user's data).
	palette, paletteErr := newPaletteService(kv, cfg.Palette)
	if paletteErr != nil {
		return nil, fmt.Errorf("load user palette: %w", paletteErr)
	}
	app.palette = palette

	// logo owns its own persistence + served cache. Same read-error policy as
	// palette: surface a corrupt .rela/theme/ rather than silently overwriting
	// it on the next save.
	logo, logoErr := newLogoStore(kv)
	if logoErr != nil {
		return nil, fmt.Errorf("load user logo: %w", logoErr)
	}
	app.logo = logo

	// syncHandler owns the /api/sync/manifest change feed (fs-client ↔ pg-server
	// replication). The record read/write channel was retired in TKT-8P1TM7 (the
	// sync client now uses /api/v1), so the handler holds only App's store (to
	// resolve a relation entry's source type for the read gate); the manifest
	// capability is resolved from the concrete store (nil on fs/memory builds,
	// where the endpoint degrades to 501). It has no write path, so the
	// unmatched_principal provision seam (TKT-ANUJDS) is wired only into the v1
	// write handler now, not here.
	app.sync = newSyncHandler(st)

	// viewsHandler owns the read-only view-assembly surface (view traversal,
	// section building, /_views, /_sidepanel, /_sidebar). Fixed service
	// handles by value — none of these are swapped by tests — plus the schema
	// snapshot / Services bundle as closures and App's shared read gate so
	// the uniform-404 behavior can't drift from the entity read path.
	app.views = newViewsHandler(app, st, logo)

	// appearanceHandler owns the theme/settings/palette routes; wired after
	// the logo/palette/settings services and viewReader it captures.
	app.appearance = newAppearanceHandler(app)

	// queryService owns the search-query pipeline. Its searcher and
	// affordance handles are closures because tests reassign both after
	// construction — see the type's doc comment.
	app.queries = newQueryService(app)

	// ganttHandler: the ACL-scoped lister and the field redactor are the two
	// seams its security pipeline hangs on — both closures over App so test
	// builders that rebind collaborators stay live.
	app.gantt = newGanttHandler(app, st)

	// commandHandler owns the user-configured command surface. Its
	// collaborators are narrow closures over App: the schema snapshot (command/
	// list/view config), the Services read bundle, the project root (exec cwd +
	// env), and the view executor for view-context commands.
	app.commands = &commandHandler{
		schema:      app.State,
		services:    app.Services,
		projectRoot: app.ProjectRoot,
		// Inline rather than an App method: its only consumer is this
		// closure, and App is at its plimsoll method load line.
		schemaFile:  func() string { return filepath.Base(app.paths.SchemaPath) },
		executeView: app.views.executeViewWhole,
		// Chosen once at the wiring site from (ACL, bind, override) — see
		// SelectCommandAuthorizer. Fixed for the process lifetime. Tests that
		// need a different verdict assign app.commands.authz directly rather
		// than reassigning app.acl (RR-CWBZVT).
		authz:    commandAuthz,
		files:    newCommandFileStore(),
		redactor: appRedactor(app),
	}

	// Build and publish the initial Schema snapshot. All reloadable
	// state lives here; there are no convenience aliases on App to keep
	// in sync.
	app.schema.Publish(&Schema{
		Cfg:         cfg,
		Meta:        meta,
		StyleMap:    styleMap,
		StyledTypes: styledTypes,
		OpenAPIGen: openapi.New(meta, openapi.Config{
			Title:       cfg.App.Name + " API",
			Description: cfg.App.Description,
			Version:     "1.0.0",
		}),
	})

	// Initialize git ops if enabled and repo is a git repository
	if cfg.Git != nil && cfg.Git.Enabled && git.IsRepo(paths.Root) {
		app.gitOps = git.NewOps(paths.Root, *cfg.Git)
		slog.Info("git sync enabled", "mode", cfg.Git.Mode)
	}

	// Wire the external-command runner for attachment scan/transform. It is
	// always available; the PolicyProcessor only invokes it when a property's
	// scan/transform config references a command. A nil runner (constructor
	// failure) leaves uploads with native MIME validation only.
	var runnerOpts []attachment.CmdRunnerOption
	if socks := metamodel.NewAttachmentPolicy(meta).ScanSockets(); len(socks) > 0 {
		runnerOpts = append(runnerOpts, attachment.WithScannerSockets(socks...))
	}
	runner, rerr := attachment.NewCmdRunner(attachmentCmdTimeout, store.MaxAttachmentBytes, runnerOpts...)
	if rerr == nil {
		app.attachmentRunner = runner
		// Tell the operator the confinement posture at boot, so an unsandboxable
		// host is discovered now rather than on the first upload that needs a
		// scan/transform (which will fail closed).
		slog.Info("external command confinement", "detail", runner.Describe())
		probeAttachmentCommands(meta, runner)
	} else {
		slog.Warn("attachments: command runner unavailable; scan/transform disabled", "err", rerr)
	}
	// Deliberately outside the branch above: a failed constructor is one of the
	// two states this warns about, so it must run on that path too.
	//
	// The nil is passed as an explicit untyped nil rather than the (*CmdRunner)
	// value, because a typed nil in an interface is NOT == nil — handing over a
	// failed runner directly would skip the constructor-failure branch and then
	// call SandboxErr on a nil pointer.
	var reporter sandboxReporter
	if rerr == nil {
		reporter = runner
	}
	warnIfScanCannotRun(meta, reporter, rerr)

	// exportHandler owns the view-export routes (transform list, entity/list
	// export). Extracted from App to keep App under its method cap. Probe the
	// transforms at startup so a missing converter (e.g. pandoc not installed)
	// surfaces as a boot warning rather than a 500 on the first export.
	export, exportErr := newExportHandler(app)
	if exportErr != nil {
		return nil, exportErr
	}
	app.export = export
	app.export.probeTransforms()

	// commentsHandler owns the commentary routes, likewise extracted to keep
	// App under its method cap. Built unconditionally: the SERVICE it wraps is
	// installed later by SetComments and stays nil when commenting is off.
	app.comments = newCommentsHandler(app, em)

	// attachmentHandler owns the entity-attachment routes. Constructed after
	// the runner wiring above so it captures the resolved runner. The acl/
	// audit/field-resolver deps are closures because tests swap those fields
	// on App after construction (same rationale as affordanceService); the
	// store/manager handles are fixed for App's lifetime.
	app.attachments = &attachmentHandler{
		schema: app.State,
		store:  st,
		// The concrete manager, not app.entityManager: each sub-handler
		// narrows to its OWN interface at its own field, so App's stays
		// exactly what App calls (TKT-IVSJV6).
		manager:    em,
		runner:     func() attachment.CommandRunner { return app.attachmentRunner },
		reader:     app.reader,
		serializer: app.serializer,
		acl:        func() acl.ACL { return app.acl },
		audit:      func() audit.Audit { return app.auditSink },
		fields:     func() FieldVerdictResolver { return app.fieldResolver },
		gateRead:   app.gateReadOrNotFound,
		locker:     app.attachmentLocker,
		uploads:    app.attachmentUploads,
		provision:  newProvisionSeam(app),
	}

	// writeHandler owns the entity/relation CRUD + clone + conflict-resolve
	// nucleus. Same collaborator rationale as attachmentHandler above: fixed
	// services by value, test-swappable deps as closures over App, and the
	// shared read/write helpers (gateRead/denyAfford/computeETag) as closures
	// so both paths stay behaviorally identical.
	app.write = &writeHandler{
		schema:  app.State,
		store:   st,
		manager: em, // concrete; writeHandler narrows to entityMutator

		reader:      app.reader,
		serializer:  app.serializer,
		affordances: app.affordances,
		acl:         func() acl.ACL { return app.acl },
		audit:       func() audit.Audit { return app.auditSink },
		gateRead:    app.gateReadOrNotFound,
		denyAfford:  app.denyAffordance,
		computeETag: app.computeEntityETag,
		faceEdges: func(ctx context.Context, e *entity.Entity) ([]*entity.Relation, map[string]bool, error) {
			return servedFaceEdges(ctx, app.reader, app.worldNeighbors, app.visibleReader, e)
		},
		currentEdgesByPeer: app.currentEdgesByPeerOnFace,
		engine:             func() *script.Engine { return app.scriptEngine },
		luaDeps:            app.luaWriteDeps,
		fullScriptDetail:   app.allowFullScriptDetail,
		paths:              paths,
		provision:          newProvisionSeam(app),
	}

	// Nudge the operator to make a conscious virus-scan choice: if the
	// metamodel has file properties with no scan command configured (and no
	// explicit `scan: off`), warn once. Configuring a scan_cmd or setting
	// `scan: off` silences this. The warning never blocks startup or uploads.
	if metamodel.NewAttachmentPolicy(meta).HasUnconfiguredScan() {
		slog.Warn("attachments: no virus scanner configured for file properties; "+
			"set attachments.scan_cmd to enable scanning, or `scan: off` on the property to silence this",
			"docs", "docs/attachment-security.md")
	}

	return app, nil
}

// loadConfig turns the raw bytes of data-entry.yaml into a validated,
// normalized Config. It is the single pipeline for both startup (NewApp) and
// hot reload (App.reloadConfig), so a config the server would refuse at boot
// is refused on reload too (TKT-IMBOK). It logs non-fatal config warnings.
//
// Nil: never returned with a nil error.
func loadConfig(cfgData []byte, meta *metamodel.Metamodel, root string) (*Config, error) {
	// Check for deprecated syntax that needs migration
	configPath := filepath.Join(root, ConfigFile)
	detections := migration.DetectBytes(cfgData, migration.FileTypeDataEntry)
	if len(detections) > 0 {
		return nil, &migration.Error{
			FilePath:   configPath,
			Detections: detections,
		}
	}

	var cfg Config
	if unmarshalErr := yaml.Unmarshal(cfgData, &cfg); unmarshalErr != nil {
		return nil, fmt.Errorf("parsing %s: %w", ConfigFile, unmarshalErr)
	}

	// Validate config against metamodel
	if validationErr := ValidateConfig(cfgData, &cfg, meta); validationErr != nil {
		return nil, fmt.Errorf("invalid %s: %w", ConfigFile, validationErr)
	}

	// Fill in calendar defaults. AFTER validation, deliberately: normalizing
	// first would replace an author's invalid default_view with "month" and
	// report nothing, turning a typo into silently different behavior.
	dataentryconfig.NormalizeCalendars(&cfg)
	dataentryconfig.NormalizeGantts(&cfg)

	// Verify action scripts exist on disk (catches typos at startup).
	// Skip set-only actions which have no script.
	for id, action := range cfg.Actions {
		if action.Script == "" {
			continue
		}
		if err := script.CheckActionScriptExists(root, action.Script); err != nil {
			return nil, fmt.Errorf("invalid %s: action %q: %w", ConfigFile, id, err)
		}
	}

	if err := checkDocumentScripts(cfg.Documents, root); err != nil {
		return nil, err
	}

	if err := checkExportRenderScripts(&cfg, root); err != nil {
		return nil, err
	}

	// Warnings come after every fatal check, so a rejected config does not
	// leave warnings in the log for a file that never took effect.
	//
	// Non-fatal configuration warnings (e.g. a relation filter control whose
	// incoming direction targets a type the relation never points to). Logged,
	// not fatal — the app still serves, the filter just returns no rows.
	for _, w := range CollectConfigWarnings(&cfg, meta) {
		slog.Warn("data-entry config warning", "detail", w)
	}

	// Warn about scripts that mail without the grant (TKT-JVHSOZ). AFTER the
	// existence checks, so a project with a missing script fails on that
	// rather than on a lint that could not read it. See mailgate.go for why
	// this is a hint rather than a check.
	warnUngatedMailActionsFromDisk(cfg.Actions, root)
	return &cfg, nil
}

// checkExportRenderScripts verifies every configured export render override
// resolves to a real script under scripts/, for the same reason and by the
// same mechanism as the documents:/actions: checks in NewApp — an operator
// should learn about a typo'd path at boot, not on someone's first export.
//
// Both override kinds are checked. The per-type one (views.<type>.export_render)
// went unverified until the per-list one was added, which was an oversight
// rather than a decision.
func checkExportRenderScripts(cfg *Config, root string) error {
	for id, list := range cfg.Lists {
		if list.ExportRender == "" {
			continue
		}
		if err := script.CheckDocumentScriptExists(root, list.ExportRender); err != nil {
			return fmt.Errorf("invalid %s: list %q: export_render: %w", ConfigFile, id, err)
		}
	}
	for id, view := range cfg.Views {
		if view.ExportRender == "" {
			continue
		}
		if err := script.CheckDocumentScriptExists(root, view.ExportRender); err != nil {
			return fmt.Errorf("invalid %s: view %q: export_render: %w", ConfigFile, id, err)
		}
	}
	return nil
}

// The server-rendered nav-enrichment path (NavItem/NavGroup/NavElement,
// enrichNavEntry, navElements) and the active-list resolvers
// (activeListForEntityType, activeListFromReferer, resolveActiveList) are
// deleted: nothing outside their own tests called them since the SPA took
// over navigation, and enrichNavEntry counted entities from the RAW store
// — leaking existence counts of ACL-hidden entities (#1043).

// coverage-ignore: requires running workspace, tested via e2e

// firstNavTarget returns the first navigable item from the navigation config,
// walking into groups as needed.
func firstNavTarget(nav []NavigationEntry) *NavigationEntry {
	for i := range nav {
		if nav[i].IsGroup() {
			if target := firstNavTarget(nav[i].Items); target != nil {
				return target
			}
			continue
		}
		return &nav[i]
	}
	return nil
}

// ProjectName returns the display name of the loaded project.
func (a *App) ProjectName() string {
	return a.State().Cfg.App.Name
}

// ProjectRoot returns the root directory of the loaded project.
func (a *App) ProjectRoot() string {
	return a.paths.Root
}

// colorToCSSClass maps a color name from config to a CSS class.
var colorToCSSClass = map[string]string{
	"blue":   "badge-blue",
	"purple": "badge-purple",
	"green":  "badge-green",
	"gray":   "badge-gray",
	"red":    "badge-red",
	"orange": "badge-orange",
	"yellow": "badge-yellow",
}

// autoColors assigns colors to enum values that have no explicit style.
var autoColors = []string{"blue", "purple", "green", "orange", "yellow", "red", "gray"}

func buildStyleMap(
	cfg *Config, meta *metamodel.Metamodel,
) (styleMap map[string]map[string]string, styledTypes map[string]bool) {
	sm := make(map[string]map[string]string)
	st := make(map[string]bool)

	// Populate from explicit config styles
	for typeName, valueColors := range cfg.Styles {
		sm[typeName] = make(map[string]string)
		st[typeName] = true
		for val, color := range valueColors {
			if cls, ok := colorToCSSClass[color]; ok {
				sm[typeName][val] = cls
			} else {
				sm[typeName][val] = "badge-gray"
			}
		}
	}

	// Auto-assign styles for custom types not already styled
	for typeName, ct := range meta.Types {
		if _, alreadyStyled := sm[typeName]; alreadyStyled {
			continue
		}
		sm[typeName] = make(map[string]string)
		st[typeName] = true
		for i, val := range ct.Values {
			sm[typeName][val] = colorToCSSClass[autoColors[i%len(autoColors)]]
		}
	}

	return sm, st
}

// newViewsHandler builds the view surface's collaborator set.
//
// A CONSTRUCTOR rather than a struct literal at the call site because there
// were two call sites — NewApp and the handler-test helper — and the helper's
// literal, which called itself a mirror of production wiring, drifted: a
// field added to production was simply absent from the copy, leaving a nil
// closure that panicked on the first request through it. One constructor
// makes the mirror unnecessary rather than merely correct today.
//
// The late-bound closures are the reason this cannot be a plain value copy.
// aclImpl exists because tests reassign app.acl after construction;
// faceEdges for the sharper case that [SetWorldNeighbors] runs after this
// point in NewApp, so capturing app.worldNeighbors eagerly would freeze the
// nil it holds now and send every view down the bare-id arm — the
// face-merging read this seam exists to close.
func newViewsHandler(app *App, st store.Store, logo *logoStore) *viewsHandler {
	return &viewsHandler{
		schema:      app.State,
		store:       st,
		reader:      app.reader,
		serializer:  app.serializer,
		affordances: app.affordances,
		viewReader:  app.viewReader,
		services:    app.Services,
		logo:        logo,
		gateRead:    app.gateReadOrNotFound,
		aclImpl:     func() acl.ACL { return app.acl },
		faceEdges: func(
			ctx context.Context, e *entity.Entity,
		) ([]*entity.Relation, error) {
			edges, _, err := servedFaceEdges(
				ctx, app.reader, app.worldNeighbors, app.visibleReader, e)
			return edges, err
		},
	}
}

// newGatedValidator builds the request-path validator. gate must answer rule
// traversals under the same tier as reader.
func newGatedValidator(
	reader validator.EntityLister, gate relresolve.Gate, meta *metamodel.Metamodel, deps lua.ReadDeps,
	st store.GraphQueryer,
) (*validator.GenericValidator, error) {
	b, err := relresolve.NewStoreBinder(meta, gate, st)
	if err != nil {
		return nil, fmt.Errorf("dataentry: validator traversals: %w", err)
	}
	val, err := validator.New(reader, meta, deps, b)
	if err != nil {
		return nil, fmt.Errorf("dataentry: validator: %w", err)
	}
	return val, nil
}

// checkDocumentScripts verifies document scripts exist on disk. Shell-command
// documents are not checkable this way (the binary may be on PATH at render
// time but unavailable now); Lua scripts live in scripts/ under the project
// root so existence can be verified upfront.
func checkDocumentScripts(docs map[string]DocumentConfig, root string) error {
	for id, doc := range docs {
		if doc.Script == "" {
			continue
		}
		if err := script.CheckDocumentScriptExists(root, doc.Script); err != nil {
			return fmt.Errorf("invalid %s: document %q: %w", ConfigFile, id, err)
		}
	}
	return nil
}
