//go:build !postgres && !sqlite

package appbuild_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/appbuild"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/project"
	"github.com/Sourcehaven-BV/rela/internal/script"
	"github.com/Sourcehaven-BV/rela/internal/search"
	"github.com/Sourcehaven-BV/rela/internal/storage"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
	"github.com/Sourcehaven-BV/rela/internal/twins"
)

const pactMetamodelYAML = metamodelYAML + `      status:
        type: string
    pacts:
      basecamp:
        scope: https://example.com/lists/1
        theirs: [status]
`

// assembleProject builds Services for a project whose schema is schemaYAML,
// over a fresh in-memory store.
func assembleProject(t *testing.T, schemaYAML string) *appbuild.Services {
	t.Helper()
	root := writeMinimalProject(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "metamodel.yaml"), []byte(schemaYAML), 0o644))
	fs := storage.NewSafeFS(storage.NewOsFS())
	paths, err := project.Discover(root, fs)
	require.NoError(t, err)
	base, err := appbuild.NewSharedBase(appbuild.Config{
		FS: fs, Paths: paths, ScriptEngine: script.NewEngine(), Audit: audit.Nop{},
	})
	require.NoError(t, err)
	st := memstore.New()
	svc, err := base.Assemble(st, search.New(st, search.NewLinearSearch()), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func createDoc(t *testing.T, svc *appbuild.Services) string {
	t.Helper()
	e := entity.New("", "doc")
	e.SetString("title", "Plan")
	e.SetString("status", "open")
	res, err := svc.EntityManager().CreateEntity(context.Background(), e, entity.CreateOptions{})
	require.NoError(t, err)
	return res.Entity.ID
}

// TestAssemble_NoPactsWiresNoTwins: without pacts there is no twin service,
// no twins directory, and writes are unguarded.
func TestAssemble_NoPactsWiresNoTwins(t *testing.T) {
	svc := assembleProject(t, metamodelYAML)
	require.Nil(t, appbuild.Twins(svc))

	id := createDoc(t, svc)
	_, err := svc.EntityManager().PatchEntity(context.Background(), id,
		entity.Patch{Properties: map[string]any{"title": "Changed"}})
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(svc.Paths().CacheDir, "twins"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

// TestAssemble_PactsWireGuardSyncWriterAndAliases proves the three wiring
// edges end to end: the service is the manager's ownership guard, the
// service's sync writes go through the bound twin-sync handle, and the
// service follows renames through the alias fanout.
func TestAssemble_PactsWireGuardSyncWriterAndAliases(t *testing.T) {
	ctx := context.Background()
	svc := assembleProject(t, pactMetamodelYAML)
	tw := appbuild.Twins(svc)
	require.NotNil(t, tw)
	mgr := svc.EntityManager()

	id := createDoc(t, svc)
	_, err := tw.Link(ctx, id, "basecamp", "todo-1", "https://example.com/todos/1", time.Now())
	require.NoError(t, err)

	// Guard: a caller may not change the owned field...
	_, err = mgr.PatchEntity(ctx, id, entity.Patch{Properties: map[string]any{"status": "done"}})
	var theirs *entitymanager.TheirsWriteError
	require.ErrorAs(t, err, &theirs)

	// ...but the twin sync can.
	res, err := tw.Pull(ctx, "basecamp", "todo-1", twins.Remote{Properties: map[string]any{"status": "done"}}, time.Now(), false)
	require.NoError(t, err)
	require.Contains(t, res.Applied, "status")
	stored, err := svc.Store().GetEntity(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "done", stored.GetString("status"))

	// Aliases: a rename retargets the twin.
	_, err = mgr.RenameEntity(ctx, id, "DOC-99", entity.RenameOptions{})
	require.NoError(t, err)
	moved, err := tw.ForEntity(ctx, "DOC-99")
	require.NoError(t, err)
	require.Len(t, moved, 1)

	_, statErr := os.Stat(filepath.Join(svc.Paths().CacheDir, "twins"))
	require.NoError(t, statErr, "twins live under the project cache dir")
}
