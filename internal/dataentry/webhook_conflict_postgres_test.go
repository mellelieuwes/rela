//go:build postgres

package dataentry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/appbuild/appbuildtest"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/project"
	"github.com/Sourcehaven-BV/rela/internal/script"
	"github.com/Sourcehaven-BV/rela/internal/storage"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/pgstore"
)

// These tests exist because the fs/memstore tiers CANNOT exhibit the race the
// webhook pipeline guards against. `checkUniqueProperties` in entitymanager is
// a check-then-write with no lock across the two steps, and fsstore/memstore are
// single-writer by nature, so on those tiers two "concurrent" creates simply
// serialize and both pass. Only PostgreSQL, where a derived partial unique index
// (rela_derived_uniq__*, TKT-3Q0GP1) backstops the scan, can produce the
// atomic loser this pipeline's retry loop is built to handle.
//
// Everything here therefore runs against a SCHEMA-PINNED DSN rather than the
// bare RELA_TEST_DATABASE_URL: the bare DSN resolves to `public`, which is
// precisely the case that works by accident (root CLAUDE.md).

var conflictSchemaCounter atomic.Int64

// openPGStore opens a store over its own pool against dsn, closing both on
// cleanup.
//
// pgstore.Open takes an injected handle (TKT-OGTVJW), so pool ownership is the
// caller's — these tests each want an INDEPENDENT pool anyway, since they stand
// in for separate rela-server processes. The schema is already migrated by the
// helper that created it.
func openPGStore(t *testing.T, ctx context.Context, dsn string) store.Store {
	t.Helper()
	pool, poolCloser, err := pgstore.NewPool(ctx, dsn)
	require.NoError(t, err)
	st, _, err := pgstore.Open(ctx, pool, dsn)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = st.Close()
		_ = poolCloser.Close()
	})
	return st
}

func conflictTestSchema(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	base := os.Getenv("RELA_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("RELA_TEST_DATABASE_URL not set; skipping webhook conflict tests")
	}

	schema := fmt.Sprintf("relahook_%d_%d", os.Getpid(), conflictSchemaCounter.Add(1))

	// Pin every connection from this DSN to the test schema. Keeping `public`
	// on the path is what pg_trgm needs; the schema itself comes first so every
	// unqualified table resolves inside the test's own namespace.
	dsn := dsnWithSchema(t, base, schema)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS "`+schema+`"`)
	require.NoError(t, err)
	require.NoError(t, pgstore.Migrate(ctx, pool))

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`)
		pool.Close()
	})
	return pool, dsn
}

// alertMetamodel is a minimal schema with a `unique:` match property, matching
// the ticket's Identity section (the operator declares uniqueness; the webhook
// layer never assumes it).
func alertMetamodel() *metamodel.Metamodel {
	return &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"incident": {
				Label:    "Incident",
				IDPrefix: "INC-",
				Properties: map[string]metamodel.PropertyDef{
					"title":     {Type: "string", Required: true},
					"alert_key": {Type: "string", Unique: true},
					"status":    {Type: "string"},
				},
				PropertyOrder: []string{"title", "alert_key", "status"},
			},
		},
	}
}

// reconcileUnique creates the derived partial unique indexes for the metamodel,
// which is what makes the create race atomic rather than check-then-write.
//
// Reconciliation is an OPTIONAL store capability, type-asserted rather than part
// of store.Store — the same shape as HistoryReader/VersionWriter.
func reconcileUnique(t *testing.T, st store.Store, meta *metamodel.Metamodel) {
	t.Helper()
	reconciler, ok := st.(interface {
		Reconcile(context.Context, []store.DerivedObjectSpec, store.ReconcileOptions) (
			[]store.DerivedObjectOutcome, error)
	})
	require.True(t, ok, "pgstore must support derived-schema reconciliation")
	var specs []store.DerivedObjectSpec
	for _, typeName := range meta.EntityTypes() {
		def, ok := meta.GetEntityDef(typeName)
		require.True(t, ok)
		for propName, pd := range def.PropertyDefs() {
			if pd.Unique && !pd.List {
				specs = append(specs, store.DerivedObjectSpec{
					Kind: store.DerivedUnique, Type: typeName, Property: propName,
				})
			}
		}
	}
	_, err := reconciler.Reconcile(context.Background(), specs, store.ReconcileOptions{})
	require.NoError(t, err)
}

// newPostgresHookApp builds an App whose store is the given POSTGRES store, so
// the pipeline under test runs against the tier that can actually exhibit
// cross-connection races (fs/memstore are single-writer).
func newPostgresHookApp(t *testing.T, st store.Store, hooks map[string]dataentryconfig.Webhook) *App {
	t.Helper()
	meta := alertMetamodel()

	app := &App{
		scriptEngine:  script.NewEngine(),
		fieldResolver: NopFieldVerdictResolver{},
		auditSink:     audit.Nop{},
	}
	fs := storage.NewMemFS()
	paths := &project.Context{Root: "/project", CacheDir: "/project/.rela"}
	require.NoError(t, fs.MkdirAll(paths.CacheDir, 0o755))

	svc := appbuildtest.New(meta, appbuildtest.WithFS(fs, paths), appbuildtest.WithStore(st))
	rebindApp(app, fs, paths, svc)

	cfg := &dataentryconfig.Config{
		App:        dataentryconfig.AppConfig{Name: "Hook Test", Description: "x"},
		Forms:      map[string]dataentryconfig.Form{},
		Lists:      map[string]dataentryconfig.List{},
		Views:      map[string]dataentryconfig.ViewConfig{},
		Kanbans:    map[string]dataentryconfig.Kanban{},
		Navigation: []dataentryconfig.NavigationEntry{},
		Webhooks:   hooks,
	}
	app.schema.Publish(&Schema{Cfg: cfg, Meta: meta})
	require.NoError(t, app.SetSecurityConfig(SecurityConfig{BindAddress: "127.0.0.1:8080"}))
	return app
}

// TestWebhookConflict_ConcurrentCreateLosesOnUnique is the correctness core of
// the create half: two concurrent creates carrying the SAME `unique:` value must
// resolve to exactly ONE stored entity, with the loser receiving
// store.UniquePropertyError rather than silently duplicating.
//
// This is the HA-duplicate case from the ticket: two Icinga masters send
// byte-identical notifications, distinguishable only by content hash.
func TestWebhookConflict_ConcurrentCreateLosesOnUnique(t *testing.T) {
	_, dsn := conflictTestSchema(t)
	ctx := context.Background()
	meta := alertMetamodel()

	st := openPGStore(t, ctx, dsn)
	reconcileUnique(t, st, meta)

	const key = "b7a027ffdd51c19e22e7b7f00c894ef2f3d968896fb40e382df73339c8c645ac"
	const writers = 8

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		wins     int
		conflict int
		other    []error
	)
	start := make(chan struct{})

	for i := range writers {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start // release all writers together to maximize overlap
			e := entity.New(fmt.Sprintf("INC-%d", n), "incident")
			e.Properties = map[string]any{
				"title":     "web01/http",
				"alert_key": key,
				"status":    "open",
			}
			createErr := st.CreateEntity(ctx, e)

			mu.Lock()
			defer mu.Unlock()
			var unique store.UniquePropertyError
			switch {
			case createErr == nil:
				wins++
			case errors.As(createErr, &unique):
				conflict++
			default:
				other = append(other, createErr)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	require.Empty(t, other, "unexpected error kinds from concurrent create")
	require.Equal(t, 1, wins, "exactly one concurrent create must win")
	require.Equal(t, writers-1, conflict,
		"every loser must see store.UniquePropertyError (the derived unique index is the backstop)")

	// The database must hold exactly one incident for the key.
	var stored int
	for e, listErr := range st.ListEntities(ctx, store.EntityQuery{Type: "incident"}) {
		require.NoError(t, listErr)
		if e.Properties["alert_key"] == key {
			stored++
		}
	}
	require.Equal(t, 1, stored, "the unique index must permit exactly one row for the key")
}

// TestWebhookConflict_LoserRefindsAndProceeds pins the pipeline's response to
// losing: after a UniquePropertyError the executor re-finds and takes the
// UPDATE path, so the delivery still lands rather than being dropped. This is
// the behaviour that makes the no-lock design safe.
func TestWebhookConflict_LoserRefindsAndProceeds(t *testing.T) {
	_, dsn := conflictTestSchema(t)
	ctx := context.Background()
	meta := alertMetamodel()

	st := openPGStore(t, ctx, dsn)
	reconcileUnique(t, st, meta)

	hooks := map[string]dataentryconfig.Webhook{
		"alert": {
			Find: &dataentryconfig.WebhookFind{
				Type:  "incident",
				Match: []string{"alert_key"},
				// The payload field is "key", not "alert_key", so the match
				// must say so; the default would read {{body.alert_key}}, find
				// nothing, and create a duplicate on every delivery.
				Values: map[string]string{"alert_key": "{{body.key}}"},
			},
			CreateIfMissing: &dataentryconfig.WebhookCreate{
				Type: "incident",
				Properties: map[string]string{
					"title":     "{{body.title}}",
					"alert_key": "{{body.key}}",
					"status":    "open",
				},
			},
		},
	}
	app := newPostgresHookApp(t, st, hooks)
	router := app.NewRouter()

	// Two deliveries for the SAME alert_key, concurrent. One wins the create;
	// the other must LOSE on the unique constraint, re-find the winner, and
	// still answer 200 carrying the winner's id.
	//
	// Driving the real router is the point. The previous version of this test
	// called st.CreateEntity directly and asserted the store's behaviour, so it
	// could not observe that the pipeline never recognizes the conflict — which
	// is exactly the defect it was supposed to guard (RR-HI9QIU / RR-SG8P1N).
	const deliveries = 8
	var wg sync.WaitGroup
	codes := make([]int, deliveries)
	ids := make([]string, deliveries)
	start := make(chan struct{})

	for i := range deliveries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			body := `{"title":"web01/http","key":"shared-alert-key"}`
			req := httptest.NewRequest(http.MethodPost, "/hooks/alert", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Host = "127.0.0.1:8080"
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			codes[i] = rec.Code
			var resp struct {
				EntityID string `json:"entity_id"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &resp)
			ids[i] = resp.EntityID
		}()
	}
	close(start)
	wg.Wait()

	// Every delivery must succeed: the loser's whole purpose is to re-find and
	// proceed rather than surface the race to the producer. Icinga never
	// retries, so a 5xx here is a permanently lost alert.
	for i, code := range codes {
		require.Equalf(t, http.StatusOK, code,
			"delivery %d got %d; a create-conflict loser must re-find and proceed", i, code)
	}

	// Exactly one entity, and every delivery reports the same id.
	var stored []*entity.Entity
	for e, listErr := range st.ListEntities(ctx, store.EntityQuery{Type: "incident"}) {
		require.NoError(t, listErr)
		stored = append(stored, e)
	}
	require.Len(t, stored, 1, "the unique constraint must admit exactly one incident")
	for i, id := range ids {
		require.Equalf(t, stored[0].ID, id,
			"delivery %d reported %q but the stored entity is %q", i, id, stored[0].ID)
	}
}

// TestWebhookConflict_BlindUpdateLosesAppends documents WHY applyWebhookSteps
// re-reads the body before splicing, by demonstrating the failure it avoids.
//
// store.UpdateEntity is a blind write with no compare-and-swap: concurrent
// read-modify-write appends computed from a base captured earlier silently
// overwrite one another. Nothing errors — the notifications simply vanish, which
// is the worst shape of failure for an alert pipeline.
//
// This test asserts the LOSS, so it is a characterization of the store's
// contract rather than a wish. If a future change makes UpdateEntity
// conflict-detecting, this test failing is the correct signal to revisit the
// re-read in applyWebhookSteps.
func TestWebhookConflict_BlindUpdateLosesAppends(t *testing.T) {
	_, dsn := conflictTestSchema(t)
	ctx := context.Background()

	st := openPGStore(t, ctx, dsn)

	seed := entity.New("INC-APPEND", "incident")
	seed.Properties = map[string]any{"title": "web01/http", "status": "open"}
	seed.Content = "## Notifications"
	require.NoError(t, st.CreateEntity(ctx, seed))

	// Every writer captures the SAME stale base first, then writes — the
	// read-modify-write pattern applyWebhookSteps must not reproduce.
	stale, err := st.GetEntity(ctx, "INC-APPEND")
	require.NoError(t, err)

	const appenders = 6
	var wg sync.WaitGroup
	for i := range appenders {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			updated := stale.Clone()
			updated.Content = stale.Content + fmt.Sprintf("\n- alert %d", n)
			_ = st.UpdateEntity(ctx, updated)
		}(i)
	}
	wg.Wait()

	final, err := st.GetEntity(ctx, "INC-APPEND")
	require.NoError(t, err)

	landed := 0
	for i := range appenders {
		if strings.Contains(final.Content, fmt.Sprintf("- alert %d", i)) {
			landed++
		}
	}
	require.Less(t, landed, appenders,
		"expected blind concurrent updates to LOSE writes; if all %d landed, "+
			"UpdateEntity may have become conflict-detecting — revisit applyWebhookSteps", appenders)
}

// TestWebhookConflict_PipelineAppendsAllLand is the positive counterpart: the
// REAL pipeline, driven concurrently through the production router against a
// postgres store, must not lose an append. Six concurrent deliveries, six
// notifications present at the end.
//
// There is no process-wide write lock (TKT-WE0S2K), so the deliveries really
// do run concurrently: what keeps every append is the compare-and-swap in
// applyWebhookSteps and its retry. The cross-process variant below pins the
// same guarantee across two stores.
func TestWebhookConflict_PipelineAppendsAllLand(t *testing.T) {
	_, dsn := conflictTestSchema(t)
	ctx := context.Background()

	st := openPGStore(t, ctx, dsn)

	app := newPostgresHookApp(t, st, map[string]dataentryconfig.Webhook{
		"alert": {
			Find: &dataentryconfig.WebhookFind{Type: "incident", Match: []string{"title"}},
			Then: []dataentryconfig.WebhookStep{{
				AppendSection: &dataentryconfig.WebhookAppendSection{
					Section: "Notifications", Content: "- alert {{body.n}}",
				},
			}},
		},
	})

	seed := entity.New("INC-PIPE", "incident")
	seed.Properties = map[string]any{"title": "web01/http", "status": "open"}
	seed.Content = "## Notifications"
	require.NoError(t, st.CreateEntity(ctx, seed))

	const deliveries = 6
	router := app.NewRouter()
	var wg sync.WaitGroup
	start := make(chan struct{})
	codes := make([]int, deliveries)

	for i := range deliveries {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			body := fmt.Sprintf(`{"title":"web01/http","n":%d}`, n)
			req := httptest.NewRequest(http.MethodPost, "/hooks/alert", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Host = "127.0.0.1:8080"
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			codes[n] = rec.Code
		}(i)
	}
	close(start)
	wg.Wait()

	for i, code := range codes {
		require.Equal(t, http.StatusOK, code, "delivery %d did not succeed", i)
	}

	final, err := st.GetEntity(ctx, "INC-PIPE")
	require.NoError(t, err)
	for i := range deliveries {
		require.Contains(t, final.Content, fmt.Sprintf("- alert %d", i),
			"delivery %d was lost; content = %q", i, final.Content)
	}
}

// TestWebhookConflict_CrossProcessAppendsAllLand pins that concurrent appends
// from two rela-server processes against one database all land.
//
// Two Apps on SEPARATE pgstores over the SAME schema model the multi-writer
// deployment documented in docs/postgres-backend.md. They share no memory, so
// only the database can serialize them: each append is a compare-and-swap on
// the row version (applyWebhookSteps), and a lost one retries against the
// fresh body.
func TestWebhookConflict_CrossProcessAppendsAllLand(t *testing.T) {
	_, dsn := conflictTestSchema(t)
	ctx := context.Background()

	hooks := map[string]dataentryconfig.Webhook{
		"alert": {
			Find: &dataentryconfig.WebhookFind{Type: "incident", Match: []string{"title"}},
			Then: []dataentryconfig.WebhookStep{{
				AppendSection: &dataentryconfig.WebhookAppendSection{
					Section: "Notifications", Content: "- alert {{body.n}}",
				},
			}},
		},
	}

	// Two independent stores over one schema == two processes.
	stA := openPGStore(t, ctx, dsn)
	stB := openPGStore(t, ctx, dsn)

	seed := entity.New("INC-XPROC", "incident")
	seed.Properties = map[string]any{"title": "web01/http", "status": "open"}
	seed.Content = "## Notifications"
	require.NoError(t, stA.CreateEntity(ctx, seed))

	routers := []http.Handler{
		newPostgresHookApp(t, stA, hooks).NewRouter(),
		newPostgresHookApp(t, stB, hooks).NewRouter(),
	}

	deliver := func(router http.Handler, n int) int {
		body := fmt.Sprintf(`{"title":"web01/http","n":%d}`, n)
		req := httptest.NewRequest(http.MethodPost, "/hooks/alert", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Host = "127.0.0.1:8080"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}

	const deliveries = 8
	var wg sync.WaitGroup
	codes := make([]int, deliveries)
	start := make(chan struct{})
	for i := range deliveries {
		wg.Go(func() {
			<-start
			codes[i] = deliver(routers[i%2], i)
		})
	}
	close(start)
	wg.Wait()

	for i, code := range codes {
		require.Equal(t, http.StatusOK, code, "cross-process delivery %d failed", i)
	}

	final, err := stA.GetEntity(ctx, "INC-XPROC")
	require.NoError(t, err)
	for i := range deliveries {
		require.Contains(t, final.Content, fmt.Sprintf("- alert %d", i),
			"cross-process delivery %d was lost; content = %q", i, final.Content)
	}
}

// TestWebhookConflict_SchemaPinnedDSNIsIsolated proves the harness above is
// actually schema-pinned rather than silently writing to `public`. Without this
// the concurrency tests could pass against a shared namespace and prove nothing
// about tenant isolation.
func TestWebhookConflict_SchemaPinnedDSNIsIsolated(t *testing.T) {
	poolA, dsnA := conflictTestSchema(t)
	_, dsnB := conflictTestSchema(t)
	require.NotEqual(t, dsnA, dsnB, "each test schema must get its own DSN")

	ctx := context.Background()
	stA := openPGStore(t, ctx, dsnA)
	stB := openPGStore(t, ctx, dsnB)

	e := entity.New("INC-ISOLATED", "incident")
	e.Properties = map[string]any{"title": "only in A"}
	require.NoError(t, stA.CreateEntity(ctx, e))

	// B must not see A's row.
	_, err := stB.GetEntity(ctx, "INC-ISOLATED")
	require.Error(t, err, "schema B must not see schema A's entity — the DSN is not pinned")

	// And the ROW must live in A's own schema, not in public. Asserting on the
	// row rather than on the existence of a public.entities TABLE is deliberate:
	// a shared test database usually already has one from other suites, so a
	// table-existence check would fail for reasons unrelated to pinning.
	var inA int
	require.NoError(t, poolA.QueryRow(ctx,
		`SELECT count(*) FROM entities WHERE id = $1`, "INC-ISOLATED",
	).Scan(&inA))
	require.Equal(t, 1, inA, "the row must be in the pinned schema (unqualified name resolves there)")

	// A fresh database has no public.entities at all, which also means the row
	// did not leak there.
	var publicTable *string
	require.NoError(t, poolA.QueryRow(ctx, `SELECT to_regclass('public.entities')::text`).Scan(&publicTable))
	if publicTable == nil {
		return
	}
	var inPublic int
	require.NoError(t, poolA.QueryRow(ctx,
		`SELECT count(*) FROM public.entities WHERE id = $1`, "INC-ISOLATED",
	).Scan(&inPublic))
	require.Zero(t, inPublic, "the row leaked into public — the DSN is not schema-pinned")
}
