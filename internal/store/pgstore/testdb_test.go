package pgstore_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/search"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/pgstore"
	"github.com/Sourcehaven-BV/rela/internal/store/storetest"
)

// testDBEnv names the env var holding the connection string for the
// integration/conformance suite. When unset, the suite is skipped (so a plain
// `go test ./...` without a database still passes; CI sets it).
const testDBEnv = "RELA_TEST_DATABASE_URL"

// requireDBEnv makes the skip above an ERROR instead. Set it in any
// environment whose job is to prove the postgres backend actually works —
// CI's Postgres Backend job sets it, and so should anything else that
// treats "pgstore is green" as a merge gate.
//
// Why this exists (RR-0EWZQW). A skip and a pass are indistinguishable in
// `go test` exit codes and in CI summaries, so the entire pgstore suite —
// which is the ONLY mechanism enforcing the backend-parity rule in the root
// CLAUDE.md — silently becomes a no-op if the DSN is ever missing: a dropped
// env var, a renamed service container, a copied-but-not-adjusted workflow.
// Nothing goes red, and fs/mem-vs-postgres divergence ships.
//
// This is not hypothetical. TKT-F4TIS6 shipped two real defects past every
// non-DB check — a 42P18 on any-endpoint queries, and jsonb/Go disagreement
// on list-valued properties that inverted `is empty` and broke multi-select
// equality — both caught only by running against a live database.
//
// Deliberately a SEPARATE variable rather than "fail whenever CI is set":
// forks and Dependabot PRs run the same workflow, and a contributor running
// the suite locally without a database must still get a clean skip. Strictness
// is opted into by the environment that promises a database, not inferred.
const requireDBEnv = "RELA_TEST_DATABASE_REQUIRED"

// missingDSN reports how to treat an absent DSN: skip (the default, so a
// plain `go test ./...` needs no database) or fail (when requireDBEnv is
// truthy). Returns the reason string for whichever the caller should raise.
func missingDSN() (required bool, reason string) {
	if v := os.Getenv(requireDBEnv); v != "" && v != "0" && !strings.EqualFold(v, "false") {
		return true, fmt.Sprintf(
			"%s is set, so the pgstore suite must run, but %s is empty.\n"+
				"This suite is the backend-parity gate: skipping it silently would let "+
				"fs/mem-vs-postgres divergence merge unnoticed.\n"+
				"Set %s to a reachable PostgreSQL DSN, or unset %s to allow skipping.",
			requireDBEnv, testDBEnv, testDBEnv, requireDBEnv)
	}
	return false, testDBEnv + " not set; skipping pgstore integration tests"
}

var (
	adminPoolOnce sync.Once
	adminPool     *pgxpool.Pool
	errAdminPool  error
	schemaCounter atomic.Int64
)

// skipper is the subset of *testing.T / *testing.F used by the schema helpers,
// letting both the conformance factory (T) and the fuzz factory (F) share code.
type skipper interface {
	Helper()
	Skipf(format string, args ...any)
	Fatalf(format string, args ...any)
	Cleanup(func())
}

// adminConn returns a process-wide pool connected with the default search_path,
// used to CREATE/DROP per-test schemas. Skips when the env var is unset so the
// suite is a no-op without a database.
func adminConn(tb skipper) *pgxpool.Pool {
	dsn := os.Getenv(testDBEnv)
	if dsn == "" {
		skipOrFailWithoutDSN(tb)
		return nil
	}
	adminPoolOnce.Do(func() {
		adminPool, errAdminPool = pgxpool.New(context.Background(), dsn)
	})
	if errAdminPool != nil {
		tb.Fatalf("connect to %s: %v", testDBEnv, errAdminPool)
		return nil
	}
	return adminPool
}

// newScopedPool creates a fresh, isolated schema, migrates it, and returns a
// pool whose connections are pinned to that schema via search_path. The schema
// is dropped via tb.Cleanup. Each call yields a completely empty store — the
// contract storetest.RunAll relies on (it calls the factory ~100 times).
//
// Isolation is per-schema rather than per-transaction because the store opens
// its own transactions (rename, cascade delete) and emits watcher events after
// commit; a wrapping rollback would break both. Distinct schemas also keep the
// rela_seq sequence fresh per test and allow parallel subtests.
func newScopedPool(tb skipper) *pgxpool.Pool {
	tb.Helper()
	return newScopedPoolSized(tb, defaultTestMaxConns)
}

// newScopedPoolSized is [newScopedPool] with an explicit pool size, for tests
// whose subject depends on it (the keyed-lock capacity is half the pool).
func newScopedPoolSized(tb skipper, maxConns int32) *pgxpool.Pool {
	tb.Helper()
	admin := adminConn(tb)
	pool, schema, err := createScopedPoolSized(admin, maxConns)
	if err != nil {
		tb.Fatalf("scoped pool: %v", err)
	}
	tb.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgQuoteIdent(schema)+" CASCADE")
	})
	return pool
}

// createScopedPool creates an isolated, migrated schema and a pool pinned to
// it. It takes no testing handle so it is usable inside f.Fuzz (where
// f.Helper/f.Cleanup are forbidden). The caller owns dropping the schema and
// closing the pool; the returned schema name identifies it.
func createScopedPool(admin *pgxpool.Pool) (*pgxpool.Pool, string, error) {
	return createScopedPoolSized(admin, defaultTestMaxConns)
}

// defaultTestMaxConns caps each scoped pool. The conformance suite creates
// ~100 pools and active fuzzing creates many more, so each pool's footprint
// must stay small to keep the suite well under the server's max_connections.
// Production wiring uses its own pool sizing.
const defaultTestMaxConns = 2

func createScopedPoolSized(admin *pgxpool.Pool, maxConns int32) (*pgxpool.Pool, string, error) {
	ctx := context.Background()
	schema := fmt.Sprintf("relatest_%d_%d", os.Getpid(), schemaCounter.Add(1))
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgQuoteIdent(schema)); err != nil {
		return nil, "", fmt.Errorf("create schema %s: %w", schema, err)
	}

	cfg, err := pgxpool.ParseConfig(os.Getenv(testDBEnv))
	if err != nil {
		return nil, schema, fmt.Errorf("parse %s: %w", testDBEnv, err)
	}
	// Pin every connection to the test schema; keep public for pg_trgm.
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.MaxConns = maxConns
	cfg.MinConns = 0

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, schema, fmt.Errorf("open scoped pool: %w", err)
	}
	if err := pgstore.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, schema, fmt.Errorf("migrate schema %s: %w", schema, err)
	}
	return pool, schema, nil
}

func pgQuoteIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

// testDSN returns the connection string for the suite, skipping if unset —
// or failing when requireDBEnv demands the suite actually run.
func testDSN(tb skipper) string {
	dsn := os.Getenv(testDBEnv)
	if dsn == "" {
		skipOrFailWithoutDSN(tb)
	}
	return dsn
}

// skipOrFailWithoutDSN raises the absent-DSN outcome chosen by [missingDSN]:
// a skip normally, a hard failure under requireDBEnv. Every place that gives
// up for want of a database MUST route through here — a skip site that checks
// testDBEnv on its own silently opts out of the parity gate, which is the
// exact failure mode this guard exists to close.
func skipOrFailWithoutDSN(tb skipper) {
	tb.Helper()
	required, reason := missingDSN()
	if required {
		tb.Fatalf("%s", reason)
		return
	}
	tb.Skipf("%s", reason)
}

// freshEmptySchema creates an isolated schema WITHOUT migrating it (unlike
// newScopedPool), for tests that need to observe the un-migrated state. Returns
// the schema name; dropped on cleanup.
func freshEmptySchema(tb skipper, admin *pgxpool.Pool) string {
	tb.Helper()
	schema := fmt.Sprintf("relatest_%d_%d", os.Getpid(), schemaCounter.Add(1))
	if _, err := admin.Exec(context.Background(), "CREATE SCHEMA "+pgQuoteIdent(schema)); err != nil {
		tb.Fatalf("create schema %s: %v", schema, err)
	}
	tb.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgQuoteIdent(schema)+" CASCADE")
	})
	return schema
}

// factory returns a fresh, empty store backed by an isolated schema.
func factory(t *testing.T) store.Store {
	t.Helper()
	s, err := pgstore.New(newScopedPool(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// searchFactory wires the PostgreSQL search backend against the same schema.
func searchFactory(t *testing.T) (store.Store, search.Searcher) {
	t.Helper()
	pool := newScopedPool(t)
	backend := pgstore.NewSearchBackend(pool)
	s, err := pgstore.New(pool, pgstore.WithObserver(backend))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s, search.New(s, backend)
}

// visibleSearchFactory exercises the NATIVE implementation: the store
// itself is the search.VisibleSearcher (visibility composed into the
// SQL), with the generic Service as the ungated parity baseline.
func visibleSearchFactory(t *testing.T) (store.Store, search.Searcher, search.VisibleSearcher) {
	t.Helper()
	pool := newScopedPool(t)
	backend := pgstore.NewSearchBackend(pool)
	s, err := pgstore.New(pool, pgstore.WithObserver(backend))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s, search.New(s, backend), s
}

// fieldVisibleSearchFactory builds the pgstore-native FieldVisibleSearcher for
// the property-level conformance suite (TKT-GGQ0JT).
func fieldVisibleSearchFactory(t *testing.T) (store.Store, search.Searcher, search.FieldVisibleSearcher) {
	t.Helper()
	pool := newScopedPool(t)
	backend := pgstore.NewSearchBackend(pool)
	s, err := pgstore.New(pool, pgstore.WithObserver(backend))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s, search.New(s, backend), s
}

// fuzzFactory adapts the per-schema helper to storetest.FuzzFactory.
//
// The FuzzFactory signature takes no testing handle and runs inside f.Fuzz
// where f.Helper/f.Cleanup are forbidden. Active fuzzing (`go test -fuzz`)
// calls the factory thousands of times across GOMAXPROCS workers; creating a
// fresh pool+schema per call exhausts the server's connection slots
// (SQLSTATE 53300) during the startup burst. So instead each call reuses one
// shared scoped pool and TRUNCATEs all tables to deliver the fresh-empty-store
// contract. Concurrency within a single iteration still runs real concurrent
// SQL against a real pool. (The plain seed-corpus run via `go test` — what CI
// and `just test` execute — uses this same path and stays at one connection
// pool.)
func fuzzFactory(f *testing.F) storetest.FuzzFactory {
	f.Helper()
	admin := adminConn(f)
	pool, schema, err := createScopedPool(admin)
	if err != nil {
		f.Fatalf("fuzz scoped pool: %v", err)
	}
	f.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgQuoteIdent(schema)+" CASCADE")
	})

	var mu sync.Mutex
	return func() store.Store {
		// Reset to an empty store. Serialize resets so concurrent iterations
		// (corpus workers) don't truncate each other's mid-flight data — each
		// fuzz iteration is independent and uses its store before the next
		// factory call on that worker.
		mu.Lock()
		_, terr := pool.Exec(context.Background(),
			`TRUNCATE entities, relations, attachments RESTART IDENTITY`)
		mu.Unlock()
		if terr != nil {
			panic(fmt.Sprintf("fuzz reset: %v", terr))
		}
		s, err := pgstore.New(pool)
		if err != nil {
			panic(fmt.Sprintf("pgstore.New: %v", err))
		}
		return s
	}
}
