// Package appbuildtest provides a test fixture for assembling an
// [appbuild.Services] bundle. It lives in its own package so that
// the bleve dependency it requires (in-memory search index for tests)
// is not compiled into the production [appbuild] package — keeping
// the bleve import out of the production binary when an alternative
// search backend is built.
//
// Mirrors the [internal/store/storetest] pattern: a sibling package
// next to the production code that supplies test-only constructors
// without polluting the production package's import graph.
package appbuildtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/appbuild"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/autocascade"
	"github.com/Sourcehaven-BV/rela/internal/automation"
	"github.com/Sourcehaven-BV/rela/internal/config"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/lua"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/project"
	"github.com/Sourcehaven-BV/rela/internal/relresolve"
	"github.com/Sourcehaven-BV/rela/internal/script"
	"github.com/Sourcehaven-BV/rela/internal/search"
	"github.com/Sourcehaven-BV/rela/internal/search/bleveindex"
	"github.com/Sourcehaven-BV/rela/internal/state"
	"github.com/Sourcehaven-BV/rela/internal/storage"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
	"github.com/Sourcehaven-BV/rela/internal/templating"
	"github.com/Sourcehaven-BV/rela/internal/tracer"
	"github.com/Sourcehaven-BV/rela/internal/validator"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

// Option configures a [*appbuild.Services] built via [New].
type Option func(*testConfig)

type testConfig struct {
	fs          storage.FS
	paths       *project.Context
	store       store.Store
	audit       audit.Audit
	acl         acl.ACL
	declarative *acl.Declarative
}

// WithStore replaces the default empty memstore with a caller-supplied
// store. The fixture's search index is populated from the store's
// current contents at construction time.
//
// Caveat: a caller-supplied store is NOT auto-wired with the search
// backend as an observer — observer setup must happen at store
// construction, which the fixture cannot retrofit. Initial-state
// backfill still runs, so any entities already in the store appear in
// search results; subsequent writes will not reach the index. If you
// need incremental sync, build the memstore with [memstore.WithObserver]
// yourself and pass that store here, or use the default memstore
// (omit WithStore) which wires the observer automatically.
func WithStore(s store.Store) Option {
	return func(c *testConfig) { c.store = s }
}

// WithFS overrides the default in-memory filesystem and project
// context with caller-supplied ones. Use this when a test wants to
// seed project files (schema.yaml, templates, data-entry.yaml)
// or assert on paths the fixture's default location does not match.
//
// Without this option, [New] supplies a default in-memory FS rooted
// at `/project` with a `.rela` cache subdir, so [appbuild.Services]
// always has a valid FS + Paths (matching production where they are
// required).
func WithFS(fs storage.FS, paths *project.Context) Option {
	return func(c *testConfig) {
		c.fs = fs
		c.paths = paths
	}
}

// WithAudit replaces the default [audit.Nop] sink with a
// caller-supplied audit backend. Tests that assert on audit records
// pass [audit.NewMemory]; tests that don't care can omit this option
// and rely on the default Nop.
func WithAudit(a audit.Audit) Option {
	return func(c *testConfig) { c.audit = a }
}

// WithACL replaces the default [acl.NopACL] with a caller-supplied
// ACL backend. Tests that assert on the deny path pass
// [acl.ReadOnlyACL]; tests that don't care can omit this option and
// rely on the allow-all default.
//
// For tests exercising the affordance resolver path, use
// [WithDeclarative] instead — it wires both ACL and the concrete
// resolver so [appbuild.Services.ACLDeclarative] returns non-nil.
func WithACL(a acl.ACL) Option {
	return func(c *testConfig) { c.acl = a }
}

// WithDeclarative wires the concrete *acl.Declarative as both the ACL
// (write authz path) and the resolver (affordance path). The fixture
// fails fast if d is nil — that's a setup bug, not "I want NopACL"
// (use [WithACL] for that). RR-FGJR: without this option, an
// appbuildtest fixture that injected a Declarative as ACL would
// silently get NopFieldVerdictResolver because
// Services.ACLDeclarative() returned nil.
func WithDeclarative(d *acl.Declarative) Option {
	return func(c *testConfig) {
		c.declarative = d
		c.acl = d
	}
}

// New constructs a *appbuild.Services bundle suitable for tests. By
// default the fixture has an in-memory filesystem rooted at
// `/project`, an empty memstore, and a real script engine (cheap to
// construct; only exercised when automations fire). Use [WithFS] /
// [WithStore] to customize.
//
// New takes *Metamodel directly and bypasses the loader, so test
// metamodels that use pre-migration syntax work without running
// migrations first.
//
// Panics on construction failure: tests have no recovery path, and a
// loud panic surfaces fixture-setup bugs at their source.
func New(meta *metamodel.Metamodel, opts ...Option) *appbuild.Services {
	if meta == nil {
		panic("appbuildtest.New: meta is required")
	}
	cfg := resolveConfig(opts)

	searchBackend := newSearchBackend()
	st := resolveStore(cfg.store, searchBackend)
	tr := tracer.New(st)
	searcher := resolveSearcher(st, searchBackend)
	readDeps := buildReadDeps(st, tr, searcher, meta, cfg.paths)

	autoEngine, cascadeRunner := buildAutomation(meta, st)
	templater := templating.NewFSTemplater(cfg.fs, cfg.paths)
	cfgLoader := config.NewFSLoader(cfg.fs, cfg.paths.Root)
	stateKV := mustBuildStateKV(cfg.fs, cfg.paths)
	scriptEngine := script.NewEngine()
	auditSink := cfg.audit
	if auditSink == nil {
		auditSink = audit.Nop{}
	}
	aclImpl := cfg.acl
	if aclImpl == nil {
		aclImpl = acl.NopACL{}
	}

	tw, err := appbuild.CompileTransitions(meta, st, aclImpl)
	if err != nil {
		panic(fmt.Sprintf("appbuildtest.New: compile transitions: %v", err))
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       st,
		Meta:        meta,
		Templater:   templater,
		Audit:       auditSink,
		ACL:         aclImpl,
		Automations: autoEngine,
		Cascade:     cascadeRunner,
		// NOTE: this Deps literal is a HAND-COPY of buildEntityManager's, not a
		// call to it, so the two drift silently — a dep added there and not
		// here leaves every test using this fixture running against a
		// capability real deployments have. That is how the Copy* deps came to
		// be unwired in BOTH places at once (TKT-WRLDAPI item 5). If you add a
		// dep to buildEntityManager, add it here too.
		//
		// The authorization deps are the ones where drift is dangerous rather
		// than merely confusing, so they no longer rely on this note: each is
		// either required by entitymanager.New or taken from the shared
		// appbuild.TransitionWiring below.
		//
		// Mirrors the production wiring in appbuild.assemble: elevated reads
		// are granted here so an integration test exercises the same
		// capability set a real deployment has (TKT-ACSBSA). Still inert
		// without an allow_acl_bypass action + an ElevatedProvider Mutator.
		ScriptRunner: script.NewLuaScriptRunnerWithElevatedReads(
			scriptEngine, readDeps, script.ReadElevation{
				Reader:   visibility.Unrestricted(st),
				Recorder: appbuild.NewElevationAuditor(auditSink),
			},
		),
		Transitions:     tw.Enforcer,
		FieldGate:       entitymanager.AllowAllFieldGate{},
		Twins:           entitymanager.NoTwinOwnership{},
		TransitionGuard: tw.Guard,
		TransitionGraph: tw.Graph,
		// The copy deps (TKT-WRLDAPI item 5), all three taken from the same
		// TransitionWiring production uses. CopyGuard reuses tw.Guard for the
		// reason production does: entitymanager.CopyGuard and
		// statemachine.Guard are deliberately the same shape, so the two
		// cannot drift into asking different questions.
		//
		// These were previously left nil here on the reasoning that this
		// fixture's default ACL is NopACL and its other reads are raw too.
		// That held for the default and broke for WithACL(declarative): a
		// test could get a policy-backed manager whose copy path still read
		// its source ungated — the forgotten-wiring state entitymanager.New
		// now refuses (#1437). Taking all three from tw makes the posture
		// follow the ACL, exactly as in production.
		CopyGuard:      tw.Guard,
		CopyReadGate:   tw.ReadGate,
		CopyVisibility: tw.Visibility,
	})
	if err != nil {
		panic(fmt.Sprintf("appbuildtest.New: build entitymanager: %v", err))
	}

	// Backfill the search index when a caller-supplied store is used:
	// observers are NOT invoked for entities already present at
	// construction time. (The default memstore wires the observer at
	// build time so backfill is unnecessary there.)
	if cfg.store != nil && searchBackend != nil {
		if backfillErr := backfill(context.Background(), searchBackend, cfg.store); backfillErr != nil {
			panic(fmt.Sprintf("appbuildtest.New: index entities: %v", backfillErr))
		}
	}

	valBinder, err := relresolve.NewStoreBinder(meta, relresolve.Ungated, st)
	if err != nil {
		panic(fmt.Sprintf("appbuildtest.New: build traversal binder: %v", err))
	}
	val, err := validator.New(st, meta, readDeps, valBinder)
	if err != nil {
		panic(fmt.Sprintf("appbuildtest.New: build validator: %v", err))
	}

	svc, err := appbuild.NewFromCollaborators(appbuild.Collaborators{
		FS:            cfg.fs,
		Paths:         cfg.paths,
		Meta:          meta,
		Store:         st,
		Searcher:      searcher,
		EntityManager: mgr,
		Tracer:        tr,
		Validator:     val,
		Templater:     templater,
		CfgLoader:     cfgLoader,
		StateKV:       stateKV,
		ScriptEngine:  scriptEngine,
		SearchCloser:  searchCloser(searchBackend),
		ACL:           aclImpl,
		Declarative:   cfg.declarative,
		Audit:         auditSink,
	})
	if err != nil {
		panic(fmt.Sprintf("appbuildtest.New: assemble services: %v", err))
	}
	return svc
}

// resolveConfig applies opts and fills in the filesystem defaults.
func resolveConfig(opts []Option) *testConfig {
	cfg := &testConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.fs == nil || cfg.paths == nil {
		cfg.fs, cfg.paths = defaultMemFS()
	} else if cfg.paths.CacheDir == "" {
		// Caller-supplied Paths without CacheDir is a common test
		// oversight; default it to <root>/.rela so the fixture's
		// state.KV setup still works. Production project.Discover
		// always sets this. The path doesn't have to exist on the
		// FS yet — state.NewFSKV creates it lazily on first write.
		cfg.paths.CacheDir = cfg.paths.Root + "/.rela"
	}
	return cfg
}

// searchCloser returns the bleve index as an io.Closer, or nil when
// the index is itself nil. Avoids the typed-nil-into-interface trap
// where Services.Close would otherwise invoke Close on a nil
// *bleveindex.Index.
func searchCloser(backend *bleveindex.Index) io.Closer {
	if backend == nil {
		return nil
	}
	return backend
}

// backfill re-indexes every entity currently in the caller-supplied
// store. Lives in the test fixture so the production search seam can
// stay build-tag-specific while tests always use bleve.
func backfill(ctx context.Context, backend *bleveindex.Index, s store.Store) error {
	if backend == nil || s == nil {
		return nil
	}
	entities := make([]*entity.Entity, 0)
	var listErrs []error
	for e, err := range s.ListEntities(ctx, store.EntityQuery{}) {
		if err != nil {
			listErrs = append(listErrs, err)
			continue
		}
		entities = append(entities, e)
	}
	indexed, indexErr := backend.IndexBatch(entities)
	if len(listErrs) == 0 && indexErr == nil {
		return nil
	}
	skipped := len(entities) - indexed
	return fmt.Errorf("backfill indexed %d entities, skipped %d, list errors: %v, index error: %w",
		indexed, skipped, listErrs, indexErr)
}

func newSearchBackend() *bleveindex.Index {
	idx, err := bleveindex.NewMem()
	if err != nil {
		slog.Warn("appbuildtest.New: failed to create search index", "error", err)
		return nil
	}
	return idx
}

func resolveStore(custom store.Store, backend *bleveindex.Index) store.Store {
	if custom != nil {
		return custom
	}
	if backend != nil {
		return memstore.New(memstore.WithObserver(backend))
	}
	return memstore.New()
}

func resolveSearcher(st store.Store, backend *bleveindex.Index) search.Searcher {
	if backend != nil {
		return search.New(st, backend)
	}
	return search.ErrSearcher(errors.New("search index not available"))
}

func buildReadDeps(st store.Store, tr tracer.Tracer, searcher search.Searcher,
	meta *metamodel.Metamodel, paths *project.Context) lua.ReadDeps {
	root := ""
	if paths != nil {
		root = paths.Root
	}
	// Test fixture: unrestricted reads (no ACL wiring here). Production
	// identity-bearing paths use Services.luaReadDepsFor instead.
	return lua.ReadDeps{
		VisibleReader: visibility.Unrestricted(st),
		Tracer:        tr,
		Searcher:      searcher,
		Meta:          meta,
		ProjectRoot:   root,
	}
}

func buildAutomation(meta *metamodel.Metamodel, st store.Store) (*automation.Engine, *autocascade.Runner) {
	if len(meta.Automations) == 0 {
		return nil, nil
	}
	traversals, err := relresolve.NewStoreBinder(meta, relresolve.Ungated, st)
	if err != nil {
		panic(fmt.Sprintf("appbuildtest.New: build traversal binder: %v", err))
	}
	autoEngine, err := automation.NewEngineFromMetamodel(meta, meta.Automations, automation.WithTraversals(traversals))
	if err != nil {
		panic(fmt.Sprintf("appbuildtest.New: build automation engine: %v", err))
	}
	r, err := autocascade.New(autocascade.Deps{Engine: autoEngine})
	if err != nil {
		panic(fmt.Sprintf("appbuildtest.New: build autocascade runner: %v", err))
	}
	return autoEngine, r
}

// mustBuildStateKV roots a state.KV at paths.CacheDir; panics on an
// invalid root since that's a fixture setup bug.
func mustBuildStateKV(fs storage.FS, paths *project.Context) state.KV {
	rfs, err := storage.NewRootedFS(fs, paths.CacheDir)
	if err != nil {
		panic(fmt.Sprintf("appbuildtest.New: build state KV: invalid root %q: %v", paths.CacheDir, err))
	}
	return state.NewFSKV(rfs)
}

// defaultMemFS returns an in-memory filesystem rooted at `/project`
// with the `.rela` cache subdirectory pre-created. Used when the
// caller does not supply [WithFS] — appbuild.Collaborators requires
// non-nil FS + Paths, so the fixture always produces a valid pair.
func defaultMemFS() (storage.FS, *project.Context) {
	fs := storage.NewMemFS()
	root := "/project"
	cacheDir := root + "/.rela"
	if err := fs.MkdirAll(cacheDir, 0o755); err != nil {
		panic(fmt.Sprintf("appbuildtest.New: build default mem FS: %v", err))
	}
	return fs, &project.Context{Root: root, CacheDir: cacheDir}
}
