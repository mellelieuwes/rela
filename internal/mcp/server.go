// Package mcp implements the Model Context Protocol server exposed by
// `rela mcp` over stdio.
//
// The server exposes rela's capabilities to AI assistants:
//
//   - Tools for entity/relation CRUD, graph trace/path, analysis (orphans,
//     cardinality, properties, validations, schema), schema introspection,
//     export, and Lua execution. Registered in tools.go (grep AddTool).
//   - Resources: rela://metamodel, rela://entity/{type}/{id},
//     rela://relation/{from}/{type}/{to}
//   - Prompts: analyze-traceability, review-orphans, summarize-project,
//     review-entity
//   - A file watcher over entities/, relations/, and the schema file with
//     a 200ms debounce; tests that exercise the watcher must wait past it
//     (see watcher.go).
//
// The server handles its own project init (discovery, metamodel load,
// store wiring) independently from the standard CLI PersistentPreRunE.

// coverage-ignore: MCP server - tested via integration tests
package mcp

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"net/http"

	mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Sourcehaven-BV/rela/internal/config"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/lua"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/search"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/tracer"
	"github.com/Sourcehaven-BV/rela/internal/validator"
)

// Deps is the focused bundle of backend services the MCP server needs.
// Every field is a domain type — the server holds no reference to any
// composition-root aggregate, so `internal/mcp` does not import
// `internal/appbuild` (enforced by arch-lint). The wiring site
// (`internal/cli`) constructs a Deps from focused services and supplies
// it to [NewServer]; tests build a Deps literal directly.
//
// ProjectRoot is the absolute project root, used by the lua tools to
// resolve relative script paths. It is the only piece of the project
// context MCP consumes — passing the string instead of a
// `*project.Context` keeps that type from leaking into MCP test stubs.
type Deps struct {
	// Store is the READ handle for every MCP read surface — tools,
	// resources, prompts, export and analyze alike. It is deliberately
	// the narrow [GraphReader], not `store.Store`: writes go through
	// EntityManager, so MCP never needs the wide composite, and typing
	// the field this way makes an ungated raw read *unavailable* rather
	// than merely discouraged (the TKT-80EWGM "make the mistake
	// impossible" pattern, applied to reads).
	//
	// The wiring site decides what this is. `rela mcp` (stdio) passes the
	// raw store — the filesystem is the trust boundary there, so a gate
	// would defend nothing. A networked wiring passes a
	// visibility-wrapped reader that resolves the ctx principal per call.
	// Either way the handlers are identical; gating is entirely a wiring
	// decision (DEC-ZBI39P).
	Store         GraphReader
	Meta          *metamodel.Metamodel
	Tracer        tracer.Tracer
	Searcher      search.Searcher
	Validator     validator.Validator
	EntityManager EntityWriter
	Config        config.Loader
	// LuaWriteDeps and LuaCache back the lua_* tools, which exist only
	// when the server is built [WithLuaTools]. A wiring that does not pass
	// that option leaves both zero.
	LuaWriteDeps lua.WriteDeps
	LuaCache     *lua.Cache
	Watcher      Watcher
	ProjectRoot  string
	Attachments  AttachmentDeps
}

// GraphReader is the read capability MCP requires of its store — the exact
// set the handlers call, declared here at the CALL SITE rather than reused
// from `store.Store`, which is a ten-interface composite (CRUD, attachments,
// watching, transactions) MCP has no business holding.
//
// It is split deliberately. The three ENTITY/RELATION reads are the gated
// surface: they return rows, so a wiring may substitute a decorator that
// hides some. The two COUNTS are [GraphCounter], kept separate because a
// count is structural — it discloses how many rows of a declared type exist,
// not which ones — and `internal/dataentry` already draws this exact line
// (`analyzeService.relCounts` is "raw (ungated) on purpose").
//
// `store.Store` satisfies the whole thing structurally, so the stdio wiring
// passes one unchanged; a visibility decorator satisfies the gated half,
// which is the point.
type GraphReader interface {
	GraphCounter

	GetEntity(ctx context.Context, id string) (*entity.Entity, error)
	ListEntities(ctx context.Context, q store.EntityQuery) iter.Seq2[*entity.Entity, error]
	GetRelation(ctx context.Context, from, relType, to string) (*entity.Relation, error)
	ListRelations(ctx context.Context, q store.RelationQuery) iter.Seq2[*entity.Relation, error]
}

// GraphCounter is the structural half of [GraphReader]: type-level tallies
// that name no individual row. Kept as its own interface so a wiring site can
// compose a gated row-reader with a raw counter without either pretending to
// be the other.
type GraphCounter interface {
	CountEntities(ctx context.Context, q store.EntityQuery) (int, error)
	CountRelations(ctx context.Context, q store.RelationQuery) (int, error)
}

// EntityWriter is the write capability MCP requires — the exact set its tool
// handlers call, declared here at the CALL SITE for the same reason
// [GraphReader] is: `entitymanager`'s own interface was a nine-method
// producer-side type that MCP has no business holding in full (TKT-IVSJV6).
// The wiring site supplies the project's *entitymanager.Manager, which
// satisfies this structurally.
//
// Three of the nine are absent because no MCP tool invokes them:
// UpdateEntity (the entity tool patches — it names the properties it touched
// rather than holding the whole record, TKT-80EWGM), ValidateCreate (an
// advisory dry-run the data-entry form path uses), and UpdateRelation.
//
// Every method here still routes through the manager, so ACL, audit and
// automations apply exactly as they do on any other write path — narrowing
// the interface removes methods, never gates.
type EntityWriter interface {
	CreateEntity(ctx context.Context, e *entity.Entity, opts entity.CreateOptions) (*entity.CreateResult, error)
	PatchEntity(ctx context.Context, id string, p entity.Patch) (*entity.UpdateResult, error)
	DeleteEntity(ctx context.Context, id string, cascade bool) (*entity.DeleteResult, error)
	RenameEntity(
		ctx context.Context, oldID, newID string, opts entity.RenameOptions,
	) (*entity.RenameResult, error)
	CreateRelation(
		ctx context.Context, from, relType, to string, opts entity.RelationOptions,
	) (*entity.Relation, error)
	DeleteRelation(ctx context.Context, from, relType, to string) error
}

// validate rejects a Deps missing any field whose zero value would
// defer a failure to request time — a nil collaborator panics inside a
// tool handler, and an empty ProjectRoot makes lua_list silently walk
// the process CWD instead of the project's scripts/ dir. Catching these
// at construction keeps the failure where it can be diagnosed.
//
// LuaCache is intentionally absent: a nil cache is a valid "no cache"
// signal that lua.WithCache tolerates.
func (d Deps) validate() error {
	switch {
	case d.Store == nil:
		return errors.New("mcp: Deps.Store is required")
	case d.Meta == nil:
		return errors.New("mcp: Deps.Meta is required")
	case d.Tracer == nil:
		return errors.New("mcp: Deps.Tracer is required")
	case d.Searcher == nil:
		return errors.New("mcp: Deps.Searcher is required")
	case d.Validator == nil:
		return errors.New("mcp: Deps.Validator is required")
	case d.EntityManager == nil:
		return errors.New("mcp: Deps.EntityManager is required")
	case d.Config == nil:
		return errors.New("mcp: Deps.Config is required")
	case d.Watcher == nil:
		return errors.New("mcp: Deps.Watcher is required")
	case d.ProjectRoot == "":
		return errors.New("mcp: Deps.ProjectRoot is required")
	}
	return d.Attachments.validate()
}

// validateFor is Deps.validate plus the checks that depend on how s was
// built: a server [WithLuaTools] needs the Lua write deps. A free function to
// keep Server under its plimsoll load line.
func validateFor(s *Server, d Deps) error {
	if err := d.validate(); err != nil {
		return err
	}
	if s.luaTools && d.LuaWriteDeps.EntityManager == nil {
		return errors.New("mcp: WithLuaTools requires Deps.LuaWriteDeps")
	}
	return nil
}

// Watcher is the narrow file-watching capability MCP requires from
// its wiring site. Start arms the watcher with an opaque "something
// changed" callback; Pause / Resume temporarily suppress callbacks
// while in-process writes happen (e.g. entity rename). The wiring
// site supplies an adapter that translates these calls into the
// underlying filesystem watcher.
type Watcher interface {
	Start(onChange func()) error
	Stop()
	Pause()
	Resume()
}

// Server wraps the MCP server with rela-specific state.
//
// TODO(TKT-N0IKN9): Server started this arc over the 40-method load line;
// it is under it now (25 methods). The directive stays pinned to the actual
// count so extraction gains cannot silently erode; ratchet it further as
// the remaining handler clusters move out.
//
// 48 → 49: [Server.HTTPHandler] (TKT-BDG8U9). It belongs on Server — it
// exposes THIS server over a second transport, the peer of [Server.Serve] —
// and it is the only method the remote endpoint added: the stateless-transport
// choice lives inside it, and the wiring site holds an http.Handler rather
// than reaching for the SDK.
//
// 49 → 38 (TKT-YUETL7): the type-name helpers moved to typeResolver (they
// need only the metamodel) and the trace/export tool handlers moved to
// traceHandler / exportHandler (store + tracer, and store + resolver,
// respectively). Each is a field below, wired from Deps in [NewServer];
// registerTools points the affected AddTool lines at the field's methods.
//
// 38 → 25 (TKT-MGNE5L): the lua tools moved to luaHandler (the sole user of
// LuaWriteDeps / LuaCache / ProjectRoot), the schema tools + resource reads
// to schemaResourceHandler (store + metamodel), and the prompt handlers to
// promptHandler (store + metamodel + tracer + resolver). register* stay on
// Server and point at the fields' methods. The remaining handlers genuinely
// span deps — the next TKT-N0IKN9 slice.
//
// 25 → 27 (TKT-NU247U): [Server.ReloadDeps] and the unexported deps accessor.
// Both are the reload seam itself — ReloadDeps is the capability, and deps()
// is what makes every handler read the CURRENT snapshot instead of one baked
// in at construction. The six handler-group accessors that would also have
// landed here are free functions ([group], [setDeps], [bind]) precisely to
// keep this number from moving further; ratchet it back down with the
// remaining handler clusters.
//
//plimsoll:max-methods=27
type Server struct {
	mcp       *mcpgo.Server
	logger    *slog.Logger
	principal principal.Principal

	// luaTools registers lua_eval / lua_run / lua_list. Off unless the wiring
	// asks for it with [WithLuaTools]; see that option for why.
	luaTools bool

	// state publishes the reloadable (Deps, handlerSet) pair. Read it per
	// request via [Server.deps] / the s.<group>() accessors, never by
	// caching the result across a call — a schema hot-reload (TKT-NU247U)
	// swaps the whole snapshot between requests.
	//
	// Handler groups are NOT embedded fields any more. They used to be, and
	// registerTools passed method values like `group(s, selTrace).handleTraceFrom`
	// straight to AddTool — which binds the group BY VALUE at registration
	// time, so a reloaded snapshot would never reach an already-registered
	// tool. Registration now goes through closures that resolve the current
	// snapshot per call; see registerTools.
	state snapshotProvider
}

// setDeps derives and publishes the snapshot for d. Used by [NewServer], by
// [Server.ReloadDeps], and by test helpers building a Server literal — one
// call, so the deps and their handler groups cannot be set inconsistently.
//
// A free function rather than a method to keep Server under its plimsoll load
// line; it is package-internal wiring, not part of the type's surface.
func setDeps(s *Server, d Deps) { s.state.publish(newSnapshot(d)) }

// deps returns the currently published dependency bundle.
func (s *Server) deps() Deps { return s.state.current().deps }

// ReloadDeps atomically republishes the server against a freshly built
// dependency bundle, so subsequent requests observe the new metamodel and the
// services derived from it.
//
// This is how `rela mcp` picks up a `schema.yaml` edit without a restart
// (TKT-NU247U): the wiring site rebuilds the metamodel-derived service stack
// against the SAME store and searcher, then hands the new Deps here. The
// registered tool/resource/prompt SET is unchanged — only what the handlers
// read through — so no capability renegotiation is involved.
//
// An invalid bundle is refused and the previous one stays published: a reload
// driven by a file watcher must never be able to leave a running server
// without a usable metamodel.
//
// Safe to call while requests are in flight. A request that has already
// resolved the snapshot completes against it; the next one sees the new
// bundle.
func (s *Server) ReloadDeps(d Deps) error {
	if err := validateFor(s, d); err != nil {
		return err
	}
	setDeps(s, d)
	return nil
}

// group resolves one handler group out of the CURRENT snapshot. Callers pass a
// selector over [handlerSet]; see [bind] for why the resolution has to happen
// per request rather than once at registration.
//
// A free function rather than six accessor methods on Server: Server sits near
// its plimsoll load line, and six one-line getters would spend that budget on
// indirection rather than on capability.
func group[G any](s *Server, sel func(handlerSet) G) G {
	return sel(s.state.current().handlers)
}

// Selectors for the handler groups, used with [group] and [bind].
func selTypes(h handlerSet) typeResolver              { return h.types }
func selTrace(h handlerSet) traceHandler              { return h.trace }
func selExport(h handlerSet) exportHandler            { return h.export }
func selLua(h handlerSet) luaHandler                  { return h.lua }
func selSchemaRes(h handlerSet) schemaResourceHandler { return h.schemaRes }
func selPrompts(h handlerSet) promptHandler           { return h.prompts }

// handlerSet is the extracted handler groups a [Server] carries.
//
// Grouping them in one struct is load-bearing, not cosmetic: every
// construction site wires them with a SINGLE whole-struct assignment
// (s.handlerSet = deps.handlers()), so a partially-wired Server cannot be
// built. Adding a seventh group adds a field here and every site picks it
// up — with per-field assignment, a site that forgot one would compile and
// then nil-panic at request time, which is how this was structured before
// and what the grouping exists to prevent. Keep it one value; do not
// spread these back out into separate returns.
type handlerSet struct {
	types     typeResolver
	trace     traceHandler
	export    exportHandler
	lua       luaHandler
	schemaRes schemaResourceHandler
	prompts   promptHandler
	attach    attachmentHandler
}

// handlers builds the extracted handler groups a [Server] carries. One
// derivation shared by [NewServer] and the test helpers that construct
// Server literals, so the wiring cannot drift between them.
func (d Deps) handlers() handlerSet {
	types := typeResolver{meta: d.Meta}
	return handlerSet{
		types:     types,
		trace:     traceHandler{store: d.Store, tracer: d.Tracer},
		export:    exportHandler{store: d.Store, types: types},
		lua:       luaHandler{writeDeps: d.LuaWriteDeps, cache: d.LuaCache, projectRoot: d.ProjectRoot},
		schemaRes: schemaResourceHandler{store: d.Store, meta: d.Meta},
		prompts:   promptHandler{store: d.Store, meta: d.Meta, tracer: d.Tracer, types: types},
		attach:    attachmentHandler{store: d.Store, deps: d.Attachments},
	}
}

// Option configures a [Server] at construction.
type Option func(*Server)

// WithPrincipal stamps p onto every tool-handler ctx via a server
// middleware so downstream audit records are correctly attributed.
// Applies to every registered tool — including lua_eval / lua_run /
// any future write tool — because the middleware runs ahead of all
// handlers (registration-time wrapping, not per-handler opt-in).
func WithPrincipal(p principal.Principal) Option {
	return func(s *Server) { s.principal = p }
}

// WithLuaTools registers the Lua scripting tools (lua_eval, lua_run,
// lua_list). Only the stdio wiring passes it.
//
// Opt-in because a script is caller-supplied code that runs in the server
// process. Over stdio the caller already controls the machine. Over HTTP it
// would let any remote caller run arbitrary scripts, which is a
// denial-of-service surface the remote API does not need. Making the default
// "absent" means a new networked wiring cannot expose them by forgetting to
// opt out.
func WithLuaTools() Option {
	return func(s *Server) { s.luaTools = true }
}

// principalMiddleware stamps the server's Principal on every inbound
// request ctx. Registered once in NewServer via AddReceivingMiddleware
// so no per-handler opt-in is required (CLAUDE.md: "make the wrong thing
// impossible to write" — a new write tool added to the server inherits
// the stamp automatically).
//
// The go-sdk's middleware is method-level rather than tool-level, so
// unlike the previous ToolHandlerMiddleware this also covers resource
// and prompt handlers. That is a strict improvement: those surfaces
// read the graph too (see RR-CFFL52 / RR-NSUN49) and previously ran
// with no principal on the ctx at all.
//
// **An identity already on the ctx WINS.** Under stdio there is never
// one, so this is the stdio server's own principal in practice. Over
// HTTP (TKT-BDG8U9) the transport hands the SDK the *http.Request ctx,
// which the middleware chain has already stamped with the JWT-verified
// caller — and overwriting that with a process-wide identity would
// attribute every remote caller's writes to one principal AND hand the
// ACL the wrong subject to gate reads against. The construction-time
// principal is the fallback for a transport that carries no identity,
// not an override of one that does.
//
// NewServer guarantees s.principal is non-zero, so the fallback is
// never the zero Principal.
func (s *Server) principalMiddleware(next mcpgo.MethodHandler) mcpgo.MethodHandler {
	return func(ctx context.Context, method string, req mcpgo.Request) (mcpgo.Result, error) {
		if _, stamped := principal.Stamped(ctx); stamped {
			return next(ctx, method, req)
		}
		return next(principal.With(ctx, s.principal), method, req)
	}
}

// NewServer creates a new MCP server for a rela project. Returns an
// error if [WithPrincipal] was not supplied — silently degrading to
// `unknown/unknown` audit attribution would be an invisible
// production bug (CLAUDE.md "constructors reject nil required
// fields"). Tests must pass a non-zero Principal too — use any
// non-empty `principal.Principal{User: ..., Tool: ...}`.
func NewServer(deps Deps, version string, opts ...Option) (*Server, error) {
	s := &Server{
		logger: slog.Default().With("component", "mcp"),
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.principal.IsZero() {
		return nil, errors.New("mcp.NewServer: Principal is required (use WithPrincipal)")
	}
	if err := validateFor(s, deps); err != nil {
		return nil, err
	}
	setDeps(s, deps)

	// Capabilities are inferred by the go-sdk from the features actually
	// registered below (tools/resources/prompts each gain listChanged when
	// the first one is added), so there is no explicit With*Capabilities
	// equivalent to carry over.
	mcpServer := mcpgo.NewServer(
		&mcpgo.Implementation{Name: "rela", Version: version},
		&mcpgo.ServerOptions{
			Instructions: "rela is a schema-driven entity-graph platform. The domain is defined by a " +
				"YAML metamodel (entity types, relation types, properties, validation rules); " +
				"entities and relations are stored as markdown files with YAML frontmatter. " +
				"Traceability is one common use case, not the only one — the graph can model " +
				"requirements, compliance controls, project plans, issue trackers, " +
				"knowledge bases, or any typed-entity-and-relation domain. " +
				"Use tools to query, create, update, and delete entities and relations. " +
				"Use resources to read entity and metamodel data directly.",
		},
	)

	s.mcp = mcpServer
	s.mcp.AddReceivingMiddleware(s.principalMiddleware)

	s.registerTools()
	s.registerResources()
	s.registerPrompts()

	return s, nil
}

// HTTPHandler returns an http.Handler serving this server over Streamable
// HTTP, for mounting inside an existing router (TKT-BDG8U9). The caller owns
// authentication, ACL and routing; this method owns only the MCP transport.
//
// **Stateless is required, not a tuning choice.** Protocol revision
// 2026-07-28 is reachable ONLY on a stateless server in the go-sdk — a
// session-bearing one negotiates down to 2025-11-25, because the newer
// revision removes sessions entirely. Consequences the caller inherits:
//
//   - GET and DELETE get 405; only POST carries messages.
//   - Server→client requests are rejected (there is no channel to answer on).
//   - Notifications reach the client only within an in-flight request.
//
// That last point is why the file watcher is pointless on this transport and
// a caller should pass a no-op [Watcher]: `resources/list_changed` has no
// stateless equivalent. Remote clients re-read on demand and see fresh data,
// because every read goes to the store.
//
// The returned handler serves THIS server for every request, so per-request
// state must travel on the ctx rather than be baked in here. That is exactly
// how identity works: the transport passes the *http.Request ctx through to
// handlers, and Server.principalMiddleware preserves a principal already
// stamped there in preference to the construction-time one.
//
// The request body limit is raised from the go-sdk's 4 MiB default to fit an
// attach_file call at [MaxUploadBytes]; see maxRequestBodyBytes.
//
// The go-sdk's DNS-rebinding guard is disabled. It rejects any non-loopback
// Host on a connection accepted over loopback, which is every request in
// production: rela-server binds 0.0.0.0 and the proxy in front of it connects
// over 127.0.0.1, forwarding the public Host. The guard protects
// unauthenticated local servers. This endpoint is only mounted behind the
// verified-JWT gate (see dataentry.App.SetRemoteMCP), and a rebinding page
// cannot produce a signed assertion.
func (s *Server) HTTPHandler() http.Handler {
	h := mcpgo.NewStreamableHTTPHandler(
		func(*http.Request) *mcpgo.Server { return s.mcp },
		&mcpgo.StreamableHTTPOptions{
			Stateless:                  true,
			DisableLocalhostProtection: true,
			MaxRequestBodyBytes:        maxRequestBodyBytes,
		},
	)
	return newLargeRequestGate(largeRequestSlots).wrap(h)
}

// Serve starts the MCP server on stdio and blocks until the peer
// disconnects or ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	// coverage-ignore-start: main-or-wiring: Serve() is the process-level stdio entry point — it arms the real
	// filesystem watcher and blocks in
	// server.ServeStdio reading os.Stdin/writing os.Stdout, unreachable by a unit test.
	s.logger.Info("starting rela MCP server on stdio")

	// Start the file watcher; MCP only cares "something changed."
	//
	// Behavior change vs mark3labs (TKT-UIR41P, documented delta): the
	// previous library exposed SendNotificationToAllClients, which this
	// callback used to push notifications/resources/list_changed on every
	// file change. The go-sdk has no equivalent — it emits list_changed
	// automatically when the resource SET changes (AddResource /
	// RemoveResources), which is a different event from "the contents
	// behind a resource template changed", and offers no exported way to
	// send an ad-hoc one.
	//
	// Resources here are a static list plus two URI templates, so the set
	// never changes at runtime; only contents do. Rather than fake a
	// set-change to trigger the notification, the callback now just logs.
	// Clients re-read on demand and see fresh data, because every read
	// goes to the store. The practical loss is that a client caching a
	// resource list is not proactively invalidated — acceptable, and
	// aligned with the direction of the 2026-07-28 spec, which requires an
	// explicit subscriptions/listen opt-in for these notifications anyway.
	if err := s.deps().Watcher.Start(func() {
		s.logger.Info("graph re-synced from file changes")
	}); err != nil {
		s.logger.Warn("file watcher not started", "error", err)
	}

	defer s.deps().Watcher.Stop()

	return s.mcp.Run(ctx, stdioTransport())
	// coverage-ignore-end
}
