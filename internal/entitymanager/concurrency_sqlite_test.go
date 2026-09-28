//go:build sqlite

package entitymanager_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/sqlitedb"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/sqlitestore"
)

func init() {
	concBackends = append(concBackends, concBackend{name: "sqlite", open: openConcSQLiteStore})
}

func openConcSQLiteStore(t *testing.T) store.Store {
	t.Helper()
	db, err := sqlitedb.Open(context.Background(), sqlitedb.Options{
		Path: filepath.Join(t.TempDir(), "rela.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	st, err := sqlitestore.New(db)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}
