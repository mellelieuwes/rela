package dataentry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/appbuild"
	"github.com/Sourcehaven-BV/rela/internal/appbuild/appbuildtest"
	"github.com/Sourcehaven-BV/rela/internal/attachment"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/lua"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/openapi"
	"github.com/Sourcehaven-BV/rela/internal/project"
	"github.com/Sourcehaven-BV/rela/internal/script"
	"github.com/Sourcehaven-BV/rela/internal/search"
	"github.com/Sourcehaven-BV/rela/internal/state"
	"github.com/Sourcehaven-BV/rela/internal/storage"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

// seedEntity writes an entity directly into the app's store.
func seedEntity(app *App, e *entity.Entity) {
	if err := app.store.CreateEntity(context.Background(), e); err != nil {
		panic(err)
	}
}

// fixture is a trivial collector of entities + relations used by test
// helpers to build an App. It replaces the historical *graph.Graph
// container and has no behavior of its own — it's just a slice pair
// that store seeders can iterate.
type fixture struct {
	entities  []*entity.Entity
	relations []*entity.Relation
}

// newFixture constructs an empty fixture.
func newFixture() *fixture { return &fixture{} }

// AddNode appends an entity. Kept named AddNode for drop-in
// compatibility with tests migrating off *graph.Graph.
func (f *fixture) AddNode(e *entity.Entity) { f.entities = append(f.entities, e) }

// AddEdge appends a relation. Kept named AddEdge for drop-in compat.
func (f *fixture) AddEdge(r *entity.Relation) { f.relations = append(f.relations, r) }

// NodesByType returns all entities of the given type in fixture order.
func (f *fixture) NodesByType(entityType string) []*entity.Entity {
	var out []*entity.Entity
	for _, e := range f.entities {
		if e.Type == entityType {
			out = append(out, e)
		}
	}
	return out
}

// seedFromFixture ingests every entity and relation of a fixture into
// the given store.
func seedFromFixture(st store.Store, f *fixture) {
	if st == nil || f == nil {
		return
	}
	ctx := context.Background()
	for _, e := range f.entities {
		if err := st.CreateEntity(ctx, e); err != nil {
			panic(err)
		}
	}
	for _, r := range f.relations {
		if _, err := st.CreateRelation(ctx, r.From, r.Type, r.To, nil); err != nil {
			panic(err)
		}
	}
}

// seedRelation is the relation counterpart to seedEntity.
func seedRelation(app *App, r *entity.Relation) {
	if _, err := app.store.CreateRelation(context.Background(), r.From, r.Type, r.To, nil); err != nil {
		panic(err)
	}
}

// entitiesByType returns the entities of a given type currently held
// by the app's store.
func entitiesByType(app *App, entityType string) []*entity.Entity {
	out := make([]*entity.Entity, 0)
	for e, err := range app.store.ListEntities(
		context.Background(),
		store.EntityQuery{Type: entityType},
	) {
		if err != nil {
			continue
		}
		out = append(out, e)
	}
	return out
}

// bindRepo rewires the given app to a workspace rooted at root, preserving
// the current app's entities and relations. Uses an OS-backed SafeFS so
// handlers that touch disk (e.g., cache writes) find a real filesystem.
func bindRepo(app *App, root string) {
	bindRepoWithFS(app,
		storage.NewSafeFS(storage.NewOsFS()),
		&project.Context{Root: root},
	)
}

// bindRepoWithFS rewires the given app to project services rooted at
// the given filesystem + paths, preserving fixtures. Use when the test
// needs to share a specific filesystem (e.g., an in-memory FS across
// multiple App instances).
func bindRepoWithFS(app *App, fs storage.FS, paths *project.Context) {
	newSvc := appbuildtest.New(app.Meta(), appbuildtest.WithFS(fs, paths))
	reseedStore(newSvc.Store(), app.store)
	rebindApp(app, fs, paths, newSvc)
}

// rebindApp repoints the app's service fields at the given services bundle.
// Used by bindRepoWithFS.
func rebindApp(app *App, fs storage.FS, paths *project.Context, svc *appbuild.Services) {
	app.fs = fs
	app.paths = paths
	app.store = svc.Store()
	app.visibleReader = newVisibleReader(svc.Store())
	app.reader = entityReader{store: svc.Store()}
	app.entityManager = svc.EntityManager()
	app.searcher = svc.Searcher()
	app.visibleSearcher = svc.VisibleSearcher()
	app.tracer = svc.Tracer()
	// Gated reads mirror production (NewApp): the validator and analyze read
	// through lateGatedReader so tests exercise the real per-principal gating
	// (TKT-3FL2S6). lateGatedReader is late-bound, so it tolerates app.acl /
	// app.affordances being rebound below.
	gatedReader := lateGatedReader{app: app}
	val, err := newGatedValidator(gatedReader, scriptTraversalGate(app), svc.Meta(), lua.ReadDeps{
		VisibleReader: gatedReader,
		Tracer:        lateGatedTracer{app: app},
		Searcher:      svc.Searcher(),
		Meta:          svc.Meta(),
		ProjectRoot:   paths.Root,
	}, svc.Store())
	if err != nil {
		panic(err.Error())
	}
	app.validator = val
	app.analyze = analyzeService{reads: gatedReader, relCounts: svc.Store(), tracer: lateGatedTracer{app: app}, validator: app.validator}
	app.templater = svc.Templater()
	app.cfgLoader = svc.Config()
	app.kv = svc.State()
	// logo + palette stores over the same kv; fresh fixtures have nothing on
	// disk so the loads can't error (nil-returns match production's clean-boot
	// path). Callers that need a specific project palette resolved re-wire
	// app.palette after this with newPaletteService(kv, cfgPalette).
	app.logo, _ = newLogoStore(svc.State())
	app.palette, _ = newPaletteService(svc.State(), nil)
	app.settings = newSettingsService(svc.State())
	app.acl = svc.ACL()
	app.auditSink = svc.Audit()
	// Wire a minimal documentService for tests that hit the documents
	// handler. Script engine can be the real one (tests that use script:
	// configs will need to seed scripts on disk).
	if app.scriptEngine != nil {
		// Elevation wired exactly as production does (NewApp): an elevated
		// document test that got a nil bundle here would silently render
		// WITHOUT bypass_acl and the test would pass for the wrong reason.
		app.documents = newDocumentService(app.store, app.kv, "/", app.scriptEngine, app.luaWriteDeps,
			func() documentElevation {
				return documentElevation{
					Reader:   visibility.Unrestricted(app.store),
					Recorder: elevationRecorder(app.auditSink),
				}
			})
	}
	// The copy surface is wired through the SAME helper production uses, so
	// `_copies` rides test-app entity responses exactly as it does in NewApp.
	// The construction only errors on a nil service, which a real manager
	// cannot produce — same clean-boot swallow as the logo/palette stores.
	copyOffers, copiesHandler, _ := wireCopies(svc.EntityManager())
	app.copies = copiesHandler
	app.affordances = affordanceService{
		acl:                func() acl.ACL { return app.acl },
		resolver:           func() FieldVerdictResolver { return app.fieldResolver },
		store:              svc.Store(),
		meta:               func() *metamodel.Metamodel { return app.State().Meta },
		getEntity:          app.reader.getEntity,
		currentEdgesByPeer: app.currentEdgesByPeer,
		copies:             copyOffers,
	}
	app.serializer = entitySerializer{affordances: app.affordances}
	// viewReader mirrors the production wiring (NewApp) so view-pipeline reads
	// are row-gated + field-redacted in tests too. The construction only errors
	// on nil args, which the literals above cannot produce — same clean-boot
	// swallow as the logo/palette stores.
	app.viewReader, _ = visibility.NewPolicyReader(ctxRowGate{}, appRedactor(app), svc.Store())
	// Rebuild the sync handler (manifest-only) over the rebound store. The record
	// write path was retired in TKT-8P1TM7, so there is no writeMu/provision here.
	app.sync = newSyncHandler(svc.Store())
	// The SAME constructor production uses, not a copy of it. The literal
	// that stood here called itself a mirror of NewApp's wiring and then
	// drifted from it — a field added in production was missing here, leaving
	// a nil closure that panicked on the first request through the view
	// surface. Sharing the constructor makes that class of drift unavailable.
	app.views = newViewsHandler(app, svc.Store(), app.logo)
	// appearanceHandler mirrors production wiring (see NewApp): built after
	// the logo/palette/settings services and viewReader it captures.
	app.appearance = newAppearanceHandler(app)
	// queryService mirrors production wiring (see NewApp). Its searcher and
	// affordance handles are closures, so the rebinds tests perform after this
	// point (rebindVisibleSearcher, app.affordances = ...) are picked up.
	app.queries = newQueryService(app)
	app.gantt = newGanttHandler(app, app.store)
	// commandHandler holds closures over App methods, which read the fields
	// rebound above — so it stays valid after this rebind. (Rebuilt rather than
	// relying on a nil zero value, since newHandlerTestApp bypasses NewApp.)
	app.commands = &commandHandler{
		schema:      app.State,
		services:    app.Services,
		projectRoot: app.ProjectRoot,
		executeView: app.views.executeView,
		// Default to ungated (the loopback/desktop behavior). Command-auth tests
		// assign app.commands.authz directly to the impl under test rather than
		// reassigning app.acl — the wiring seam, not the ctx read gate, is what
		// distinguishes NopACL/ReadOnly/Declarative (RR-CWBZVT / RR-QWVG8Y).
		authz:    ungatedAuthorizer{},
		files:    newCommandFileStore(),
		redactor: appRedactor(app),
	}
	// attachmentHandler mirrors production wiring: closures for the swappable
	// acl/audit/field-resolver fields (attachment ACL tests reassign app.acl
	// after this rebind), values for the fixed store/manager handles.
	app.attachments = &attachmentHandler{
		schema:     app.State,
		store:      svc.Store(),
		manager:    svc.EntityManager(),
		runner:     func() attachment.CommandRunner { return app.attachmentRunner },
		reader:     app.reader,
		serializer: app.serializer,
		acl:        func() acl.ACL { return app.acl },
		audit:      func() audit.Audit { return app.auditSink },
		fields:     func() FieldVerdictResolver { return app.fieldResolver },
		gateRead:   app.gateReadOrNotFound,
		writeMu:    &app.writeMu,
		provision:  newProvisionSeam(app),
	}

	// Export handler over the app's current services (mirrors NewApp).
	// Constructor error is impossible with the non-nil test collaborators;
	// panic keeps the helper's no-error signature honest if that changes.
	var exportErr error
	if app.export, exportErr = newExportHandler(app); exportErr != nil {
		panic(exportErr)
	}

	// Comments handler over the app's current services (mirrors NewApp). The
	// SERVICE inside stays nil until a test calls SetComments, which is the
	// "commenting disabled" state the 404 routes depend on.
	app.comments = newCommentsHandler(app, svc.EntityManager())

	// writeHandler mirrors production wiring (see NewApp): closures for the
	// swappable acl/audit collaborators, values for the fixed service handles,
	// and App's shared read/write helpers so both paths stay identical.
	app.write = &writeHandler{
		schema:      app.State,
		store:       svc.Store(),
		manager:     svc.EntityManager(),
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
		writeMu:            &app.writeMu,
		provision:          newProvisionSeam(app),
	}
}

// rebindSyncHandler rebuilds app.sync over the app's CURRENT store/manager.
// Production resolves the sync capabilities once at construction (the store is
// fixed for App's lifetime); a test that swaps app.store after construction to
// inject a fake manifest/apply source must call this so the handler re-resolves
// against the swapped store.
func rebindSyncHandler(app *App) {
	app.sync = newSyncHandler(app.store)
}

// rebindVisibleSearcher re-derives the generic visible-search wrapper
// over the app's CURRENT searcher+store pair. Tests that inject a fake
// via `app.searcher = ...` and exercise an executeQuery consumer
// (/_search, _position search scope) must call this afterwards —
// executeQuery routes through app.visibleSearcher (TKT-BA8BSX), which
// otherwise still wraps the searcher from construction time. Tests
// that only hit the list pipeline (?q= on list endpoints) don't need
// it: that path reads app.searcher directly.
func rebindVisibleSearcher(t *testing.T, app *App) {
	t.Helper()
	v, err := search.NewVisible(app.searcher, app.store)
	if err != nil {
		t.Fatalf("rebindVisibleSearcher: %v", err)
	}
	app.visibleSearcher = v
}

// reseedStore copies every entity and relation from src into dst.
func reseedStore(dst, src store.Store) {
	if src == nil || dst == nil {
		return
	}
	ctx := context.Background()
	for e, err := range src.ListEntities(ctx, store.EntityQuery{}) {
		if err != nil {
			continue
		}
		if err := dst.CreateEntity(ctx, e); err != nil {
			panic(err)
		}
	}
	for r, err := range src.ListRelations(ctx, store.RelationQuery{}) {
		if err != nil {
			continue
		}
		if _, err := dst.CreateRelation(ctx, r.From, r.Type, r.To, nil); err != nil {
			panic(err)
		}
	}
}

// newAppFromParts builds an App with a populated Schema snapshot for
// tests that previously used the struct-literal pattern
// `&App{Cfg: cfg, meta: meta, g: g}`. The App.state face must be
// populated because handlers now read from it; a nil snapshot would
// nil-deref inside a.State().
//
// Populates the co-derived Schema fields with safe defaults (UserDefaults,
// Palette, UserPalette, OpenAPIGen) so handlers that touch the
// less-common fields don't nil-deref in tests that didn't ask for them.
func newAppFromParts(cfg *Config, meta *metamodel.Metamodel, f *fixture, opts ...appbuildtest.Option) *App {
	app := &App{
		scriptEngine:  script.NewEngine(),
		fieldResolver: NopFieldVerdictResolver{},
		auditSink:     audit.Nop{},
	}
	if meta != nil {
		// Use an in-memory FS + project context so the workspace's
		// templater has paths it can dereference. Without this,
		// CreateRelation panics inside RelationTemplate when it tries
		// to compute a path against a nil *project.Context.
		fs := storage.NewMemFS()
		ctx := &project.Context{Root: "/project", CacheDir: "/project/.rela"}
		_ = fs.MkdirAll(ctx.CacheDir, 0o755)
		svc := appbuildtest.New(meta, append([]appbuildtest.Option{appbuildtest.WithFS(fs, ctx)}, opts...)...)
		rebindApp(app, fs, ctx, svc)
		seedFromFixture(app.store, f)
	}
	if cfg == nil {
		cfg = &Config{}
	}
	var styleMap map[string]map[string]string
	var styledTypes map[string]bool
	if meta != nil {
		styleMap, styledTypes = buildStyleMap(cfg, meta)
	}
	var openAPIGen *openapi.Generator
	if meta != nil {
		openAPIGen = openapi.New(meta, openapi.Config{Title: cfg.App.Name})
	}
	// Re-resolve the palette against this fixture's project palette (rebindApp
	// wired a nil-cfg default).
	if app.palette != nil {
		_ = app.palette.Reresolve(cfg.Palette)
	}
	app.schema.Publish(&Schema{
		Cfg:         cfg,
		Meta:        meta,
		StyleMap:    styleMap,
		StyledTypes: styledTypes,
		OpenAPIGen:  openAPIGen,
	})
	return app
}

// doRequest drives a request through the production router
// (app.NewRouter().ServeHTTP), so mux registration, URL-pattern
// parsing, and middleware ordering are exercised — unlike calling
// app.handleV1* methods directly with pre-parsed route params.
//
// Convention (TKT-TLQ94B): new endpoint tests should go through this
// helper; existing handler-level tests migrate opportunistically when
// touched. TestRouterWalk_AllAPIRoutesReachHandlers covers route
// registration itself.
func doRequest(t *testing.T, app *App, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, http.NoBody)
	w := httptest.NewRecorder()
	app.NewRouter().ServeHTTP(w, r)
	return w
}

// newHandlerTestApp builds an App for handler tests.
func newHandlerTestApp(t *testing.T) *App {
	t.Helper()
	meta := testMeta()
	cfg := testConfig()
	g, _ := testGraph(meta)

	// Add a relation for testing edge display
	g.AddEdge(entity.NewRelation("TKT-001", "depends_on", "TKT-002"))

	// Add view config
	cfg.Views = map[string]ViewConfig{
		"ticket_detail": {
			Title: "Ticket Detail",
			Entry: ViewEntry{Type: "ticket"},
			Traverse: []ViewTraverse{
				{From: "entry", Follow: "belongs_to", CollectAs: "components"},
			},
			Sections: []ViewSection{
				{Heading: "Properties", Source: "entry", Display: "properties", Fields: []ViewSectionField{
					{Property: "title"}, {Property: "status"},
				}},
				{Heading: "Components", Source: "components", Display: "list"},
			},
		},
	}

	// Add dashboard config
	cfg.Dashboard = &DashboardConfig{
		Title: "Dashboard",
		Cards: []DashboardCard{
			{Title: "All Tickets", Query: "type:ticket", Display: "count"},
		},
	}

	styleMap, styledTypes := buildStyleMap(cfg, meta)

	// Set up a filesystem for tests that need to read/write cache files
	fs := storage.NewMemFS()
	ctx := &project.Context{Root: "/project", CacheDir: "/project/.rela"}
	_ = fs.MkdirAll(ctx.CacheDir, 0o755)

	svc := appbuildtest.New(meta, appbuildtest.WithFS(fs, ctx))
	seedFromFixture(svc.Store(), g)

	// fieldResolver must be set explicitly because this fixture bypasses
	// NewApp (which rejects a nil resolver). Without it, any handler that
	// serializes entities for the wire panics — caught by the router walk
	// test driving _search through the full router (TKT-TLQ94B).
	app := &App{fieldResolver: NopFieldVerdictResolver{}}
	rebindApp(app, fs, ctx, svc)
	// Make sure kv hits the real filesystem through state.KV, matching production.
	kvRoot, err := storage.NewRootedFS(fs, ctx.CacheDir)
	if err != nil {
		t.Fatalf("NewRootedFS: %v", err)
	}
	app.kv = state.NewFSKV(kvRoot)
	// Resolve the palette against this fixture's project palette (rebindApp
	// wired a nil-cfg default). The user palette stays nil: the theme tests
	// use its nil-ness as the "nothing saved yet" signal.
	if app.palette != nil {
		_ = app.palette.Reresolve(cfg.Palette)
	}
	// Populate the snapshot fields handlers deref unconditionally — the
	// router walk test hits every route, including _openapi.json, which
	// panics on a nil OpenAPIGen.
	app.schema.Publish(&Schema{
		Cfg:         cfg,
		Meta:        meta,
		StyleMap:    styleMap,
		StyledTypes: styledTypes,
		OpenAPIGen:  openapi.New(meta, openapi.Config{Title: cfg.App.Name}),
	})
	return app
}
