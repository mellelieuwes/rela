package appbuild

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/storage"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

func twinsMeta(t *testing.T, pacts bool) *metamodel.Metamodel {
	t.Helper()
	src := `
version: "1"
entities:
  ticket:
    label: Ticket
    id_prefix: "TKT-"
    properties:
      title:
        type: string
`
	if pacts {
		src += `    pacts:
      basecamp:
        scope: https://example.com/lists/1
        theirs: [title]
`
	}
	m, err := metamodel.Parse([]byte(src))
	require.NoError(t, err)
	return m
}

// TestBuildTwins_NoPactsYieldsNothing: a schema without pacts gets no
// service, no writer to bind, no storage — and the manager's guard is the
// named opt-out, never a nil service boxed into the interface.
func TestBuildTwins_NoPactsYieldsNothing(t *testing.T) {
	p := paths(t)
	svc, writer, err := buildTwins(storage.NewOsFS(), p, twinsMeta(t, false), memstore.New(), "")
	require.NoError(t, err)
	require.Nil(t, svc)
	require.Nil(t, writer)
	require.Equal(t, entitymanager.NoTwinOwnership{}, twinOwnership(svc))

	_, statErr := os.Stat(filepath.Join(p.CacheDir, twinsDirName))
	require.ErrorIs(t, statErr, os.ErrNotExist, "a project without pacts gets no twins directory")
}

// TestBuildTwins_DatabaseBackendRefusesPacts: no twin store exists for the
// database builds yet, and falling back to node-local files would split the
// twins across nodes silently.
func TestBuildTwins_DatabaseBackendRefusesPacts(t *testing.T) {
	for _, backend := range []string{"postgres", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			_, _, err := buildTwins(storage.NewOsFS(), paths(t), twinsMeta(t, true), memstore.New(), backend)
			require.ErrorContains(t, err, backend)
			require.ErrorContains(t, err, "pacts")
		})
	}
	// Without pacts a database build is unaffected.
	svc, _, err := buildTwins(storage.NewOsFS(), paths(t), twinsMeta(t, false), memstore.New(), "postgres")
	require.NoError(t, err)
	require.Nil(t, svc)
}

func TestBuildTwins_PactsWithoutCacheDirFails(t *testing.T) {
	_, _, err := buildTwins(storage.NewOsFS(), nil, twinsMeta(t, true), memstore.New(), "")
	require.Error(t, err)
}

// TestTwinSyncWriter_UnboundRefuses: a sync before the manager is bound is a
// wiring bug and must fail loudly, not panic or silently no-op.
func TestTwinSyncWriter_UnboundRefuses(t *testing.T) {
	_, err := (&twinSyncWriter{}).PatchEntity(context.Background(), "TKT-1", entity.Patch{})
	require.ErrorContains(t, err, "before the entity manager was bound")
}

// TestTwinEntityReader_ClassifiesMissingEntity: the twins service tells a
// missing entity apart structurally, so the reader must mark it, while the
// version it returns for a present entity is the stored row's.
func TestTwinEntityReader_ClassifiesMissingEntity(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	require.NoError(t, st.CreateEntity(ctx, entity.New("TKT-1", "ticket")))
	r := twinEntityReader{st: st}

	_, _, err := r.GetEntityVersion(ctx, "TKT-404")
	var nf interface{ EntityNotFound() bool }
	require.True(t, errors.As(err, &nf) && nf.EntityNotFound(), "missing entity must carry EntityNotFound: %v", err)
	require.ErrorIs(t, err, store.ErrNotFound)

	e, version, err := r.GetEntityVersion(ctx, "TKT-1")
	require.NoError(t, err)
	stored, err := st.GetEntity(ctx, "TKT-1")
	require.NoError(t, err)
	require.Equal(t, "TKT-1", e.ID)
	require.Equal(t, string(store.VersionOf(stored)), version)
}
