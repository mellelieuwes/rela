//go:build postgres

package entitymanager_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/pgstore"
)

func init() {
	concBackends = append(concBackends,
		concBackend{name: "postgres", open: func(t *testing.T) store.Store {
			return openConcPGStore(t, concPGSchema(t))
		}},
		// Two stores over one schema stand in for two rela-server processes:
		// they share no memory, so only the database can serialize them.
		concBackend{name: "postgres-two-processes", open: openConcPGTwoProcesses},
	)
}

var concSchemaCounter atomic.Int64

// concPGSchema creates and migrates a fresh schema and returns a DSN pinned
// to it. A schema-pinned DSN, not the bare RELA_TEST_DATABASE_URL, per the
// root CLAUDE.md: the bare DSN resolves to `public`, the one case that works
// by accident.
func concPGSchema(t *testing.T) string {
	t.Helper()
	base := os.Getenv("RELA_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("RELA_TEST_DATABASE_URL not set")
	}
	schema := fmt.Sprintf("relaconc_%d_%d", os.Getpid(), concSchemaCounter.Add(1))
	cfg, err := pgx.ParseConfig(base)
	require.NoError(t, err)
	kv := []string{
		"host=" + cfg.Host,
		fmt.Sprintf("port=%d", cfg.Port),
		"user=" + cfg.User,
		"dbname=" + cfg.Database,
		"search_path=" + schema + ",public",
	}
	if cfg.Password != "" {
		kv = append(kv, "password="+cfg.Password)
	}
	if cfg.TLSConfig == nil {
		kv = append(kv, "sslmode=disable")
	}
	dsn := strings.Join(kv, " ")

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
	return dsn
}

func openConcPGStore(t *testing.T, dsn string) store.Store {
	t.Helper()
	ctx := context.Background()
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

// alternatingStore sends each write and each Tx to one of two stores over
// the same schema in turn, so a race between goroutines is also a race
// between processes. Reads go to the first store; both see the same rows.
type alternatingStore struct {
	store.Store
	other store.Store
	n     atomic.Int64
}

func (s *alternatingStore) pick() store.Store {
	if s.n.Add(1)%2 == 0 {
		return s.other
	}
	return s.Store
}

func (s *alternatingStore) Tx(ctx context.Context, fn func(store.Store) error) error {
	return s.pick().Tx(ctx, fn)
}

func (s *alternatingStore) CreateEntity(ctx context.Context, e *entity.Entity) error {
	return s.pick().CreateEntity(ctx, e)
}

func (s *alternatingStore) UpdateEntityIf(
	ctx context.Context, e *entity.Entity, cond store.UpdateCondition,
) (store.EntityVersion, error) {
	return s.pick().UpdateEntityIf(ctx, e, cond)
}

func (s *alternatingStore) CreateRelation(
	ctx context.Context, from, relType, to string, data *store.RelationData,
) (*entity.Relation, error) {
	return s.pick().CreateRelation(ctx, from, relType, to, data)
}

func (s *alternatingStore) UpdateRelationState(
	ctx context.Context, from string, face entity.Face, relType, to string, data store.RelationData,
) (*entity.Relation, error) {
	return s.pick().UpdateRelationState(ctx, from, face, relType, to, data)
}

func openConcPGTwoProcesses(t *testing.T) store.Store {
	t.Helper()
	dsn := concPGSchema(t)
	return &alternatingStore{Store: openConcPGStore(t, dsn), other: openConcPGStore(t, dsn)}
}
