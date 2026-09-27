package filetwins_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/storage"
	"github.com/Sourcehaven-BV/rela/internal/twins"
	"github.com/Sourcehaven-BV/rela/internal/twins/filetwins"
	"github.com/Sourcehaven-BV/rela/internal/twins/twinstest"
)

func newStore(t *testing.T) (store *filetwins.Store, root string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "twins")
	s, err := filetwins.New(storage.NewOsFS(), root)
	require.NoError(t, err)
	return s, root
}

func sampleTwin() twins.Twin {
	return twins.Twin{
		System:     "basecamp",
		ExternalID: "9001",
		URL:        "https://app.basecamp.com/1/buckets/2/todos/9001",
		Target:     twins.Target{Type: "scenario", ID: "SC-015"},
		State:      twins.StatePending,
		Base:       twins.Snapshot{Properties: map[string]any{"status": "open"}, Body: "Steps"},
	}
}

func TestConformance(t *testing.T) {
	twinstest.RunAll(t, func(t *testing.T) twins.Store {
		t.Helper()
		s, _ := newStore(t)
		return s
	})
}

// TestNew_RefusesInMemoryFS pins that a base the atomic writes and the lock
// cannot go through is refused up front, not discovered as writes landing on
// the real disk.
func TestNew_RefusesInMemoryFS(t *testing.T) {
	_, err := filetwins.New(storage.NewMemFS(), "/project/.rela/twins")
	require.Error(t, err)
}

// TestCrossInstance runs the shared-directory suite with two stores on one
// root, standing in for the CLI and a running server.
func TestCrossInstance(t *testing.T) {
	twinstest.RunCrossInstanceTests(t, func(t *testing.T) (a, b twins.Store) {
		t.Helper()
		root := filepath.Join(t.TempDir(), "twins")
		sa, err := filetwins.New(storage.NewOsFS(), root)
		require.NoError(t, err)
		sb, err := filetwins.New(storage.NewOsFS(), root)
		require.NoError(t, err)
		return sa, sb
	})
}

func TestNew_RejectsMissingArgs(t *testing.T) {
	t.Run("nil filesystem", func(t *testing.T) {
		_, err := filetwins.New(nil, t.TempDir())
		require.Error(t, err)
	})
	t.Run("empty root", func(t *testing.T) {
		_, err := filetwins.New(storage.NewOsFS(), "  ")
		require.Error(t, err)
	})
}

// TestNoDirectoryUntilFirstTwin pins that a project without twins gets no
// directory: reads of an absent root are empty, not errors, and create nothing.
func TestNoDirectoryUntilFirstTwin(t *testing.T) {
	s, root := newStore(t)
	ctx := context.Background()

	list, err := s.List(ctx, twins.Filter{})
	require.NoError(t, err)
	require.Empty(t, list)
	byTarget, err := s.ForTarget(ctx, "SC-015")
	require.NoError(t, err)
	require.Empty(t, byTarget)

	_, err = os.Stat(root)
	require.ErrorIs(t, err, os.ErrNotExist, "no directory until a twin is stored")
}

// TestLayout pins the on-disk shape the guide documents: one readable YAML
// document per twin, an index line per twin under its entity, lowercase names.
func TestLayout(t *testing.T) {
	s, root := newStore(t)
	ctx := context.Background()
	tw := sampleTwin()
	tw.ExternalID = "Todo-9001"
	require.NoError(t, s.Create(ctx, tw))

	data, err := os.ReadFile(filepath.Join(root, "basecamp", "^todo-9001.yaml"))
	require.NoError(t, err, "uppercase letters are spelled ^ plus the lowercase")
	for _, want := range []string{"SC-015", "state: pending", "status: open", "https://app.basecamp.com"} {
		require.Contains(t, string(data), want)
	}

	index, err := os.ReadFile(filepath.Join(root, "_targets", "^s^c-015"))
	require.NoError(t, err)
	require.Equal(t, "basecamp/Todo-9001\n", string(index))

	require.NoError(t, s.Delete(ctx, "basecamp", "Todo-9001"))
	_, err = os.Stat(filepath.Join(root, "_targets", "^s^c-015"))
	require.ErrorIs(t, err, os.ErrNotExist, "an emptied index leaves no residue")
}

// TestIndexResidueIgnored pins the crash-recovery half of the index order: a
// line whose twin is gone or moved (a write interrupted between the index and
// the twin) is skipped, not reported.
func TestIndexResidueIgnored(t *testing.T) {
	s, root := newStore(t)
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, sampleTwin()))
	index := filepath.Join(root, "_targets", "^s^c-015")
	require.NoError(t, os.WriteFile(index, []byte("basecamp/9001\nbasecamp/404\ngithub/9001\n"), 0o644))

	got, err := s.ForTarget(ctx, "SC-015")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "9001", got[0].ExternalID)
}

// TestUnsafeKeysRefused pins that a traversal-shaped system, external id or
// entity id cannot reach the filesystem, whichever caller supplies it.
func TestUnsafeKeysRefused(t *testing.T) {
	s, root := newStore(t)
	ctx := context.Background()

	cases := []struct{ system, externalID, entityID string }{
		{"../escape", "1", "SC-1"},
		{"basecamp", "../escape", "SC-1"},
		{"basecamp", "..", "SC-1"},
		{"basecamp", "a/b", "SC-1"},
		{"Basecamp", "1", "SC-1"},
		{"_targets", "1", "SC-1"},
		{"", "1", "SC-1"},
		{"basecamp", "", "SC-1"},
		{"basecamp", "1", "../escape"},
		{"basecamp", "1", ".hidden"},
	}
	for _, tc := range cases {
		t.Run(tc.system+"|"+tc.externalID+"|"+tc.entityID, func(t *testing.T) {
			tw := sampleTwin()
			tw.System, tw.ExternalID, tw.Target.ID = tc.system, tc.externalID, tc.entityID
			require.Error(t, s.Create(ctx, tw))
			_, err := s.Get(ctx, tc.system, tc.externalID)
			require.Error(t, err)
		})
	}

	entries, err := os.ReadDir(filepath.Dir(root))
	require.NoError(t, err)
	for _, e := range entries {
		require.NotContains(t, e.Name(), "escape")
	}
	list, err := s.List(ctx, twins.Filter{})
	require.NoError(t, err)
	require.Empty(t, list, "no refused create may leave a twin behind")
}

// TestCorruptTwinSurfacesError pins that a hand-broken file reports rather than
// reading as absent: a silently dropped twin silently drops the ownership it
// enforces.
func TestCorruptTwinSurfacesError(t *testing.T) {
	s, root := newStore(t)
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, sampleTwin()))
	require.NoError(t, os.WriteFile(filepath.Join(root, "basecamp", "9001.yaml"), []byte("target: [oh dear\n"), 0o644))

	_, err := s.Get(ctx, "basecamp", "9001")
	require.Error(t, err)
	require.NotErrorIs(t, err, twins.ErrNotFound)
	_, err = s.ForTarget(ctx, "SC-015")
	require.Error(t, err)
}

// TestStrayFilesIgnored pins that files the store could not have written (an
// interrupted temp file, a note, an uppercase name) do not surface as twins or
// break listing.
func TestStrayFilesIgnored(t *testing.T) {
	s, root := newStore(t)
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, sampleTwin()))
	for _, name := range []string{"basecamp/9002.yaml.tmp", "README.md", "basecamp/notes.txt", "basecamp/Upper.yaml"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("garbage: ["), 0o644))
	}

	list, err := s.List(ctx, twins.Filter{})
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "9001", list[0].ExternalID)
}
