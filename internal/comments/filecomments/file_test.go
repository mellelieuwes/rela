package filecomments_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/comments"
	"github.com/Sourcehaven-BV/rela/internal/comments/commentstest"
	"github.com/Sourcehaven-BV/rela/internal/comments/filecomments"
	"github.com/Sourcehaven-BV/rela/internal/storage"
)

func newStore(t *testing.T) (store *filecomments.Store, root string) {
	t.Helper()
	root = t.TempDir()
	s, err := filecomments.New(storage.NewOsFS(), root)
	require.NoError(t, err)
	return s, root
}

func TestConformance(t *testing.T) {
	commentstest.RunAll(t, func(t *testing.T) comments.Store {
		t.Helper()
		s, _ := newStore(t)
		return s
	})
}

func TestNew_RejectsMissingArgs(t *testing.T) {
	t.Run("nil filesystem", func(t *testing.T) {
		_, err := filecomments.New(nil, t.TempDir())
		require.Error(t, err)
	})

	t.Run("empty root", func(t *testing.T) {
		_, err := filecomments.New(storage.NewOsFS(), "  ")
		require.Error(t, err)
	})
}

// TestNew_CreatesNothingUntilFirstWrite pins AC1's storage half: an operator
// who never comments must not find a directory appear in their project.
func TestNew_CreatesNothingUntilFirstWrite(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "comments")

	_, err := filecomments.New(storage.NewOsFS(), root)
	require.NoError(t, err)

	_, err = os.Stat(root)
	require.ErrorIs(t, err, os.ErrNotExist, "no directory until something is stored")
}

// TestUnsafeTargetID_Refused pins that a traversal-shaped id cannot escape the
// root. RootedFS would also refuse it; this asserts the store's own guard so a
// non-HTTP caller cannot reach the filesystem with an unchecked id.
func TestUnsafeTargetID_Refused(t *testing.T) {
	s, root := newStore(t)
	ctx := context.Background()

	for _, id := range []string{"../escape", "..", ".hidden", "a/b", ""} {
		t.Run(id, func(t *testing.T) {
			_, err := s.List(ctx, comments.Target{Type: "ticket", ID: id})
			require.Error(t, err, "unsafe id must be refused")
		})
	}

	// Nothing leaked outside the root.
	entries, err := os.ReadDir(filepath.Dir(root))
	require.NoError(t, err)
	for _, e := range entries {
		require.NotContains(t, e.Name(), "escape")
	}
}

// TestThreadFileIsReadableYAML pins the file-first promise: a stored thread is
// a plain, diffable document, not an opaque blob.
func TestThreadFileIsReadableYAML(t *testing.T) {
	s, root := newStore(t)
	ctx := context.Background()
	tgt := comments.Target{Type: "ticket", ID: "TKT-1"}

	require.NoError(t, s.Add(ctx, tgt, comments.Comment{
		ID:     "c1",
		Author: "alice@example.com",
		Anchor: comments.Anchor{Kind: comments.AnchorProperty, Ref: "status"},
		Body:   "Looks wrong to me",
	}))

	data, err := os.ReadFile(filepath.Join(root, "TKT-1.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(data), "alice@example.com")
	require.Contains(t, string(data), "Looks wrong to me")
	require.Contains(t, string(data), "status")
}

// TestEmptiedThreadRemovesFile pins that deleting the last comment leaves no
// residue, so an operator's tree does not accumulate empty documents.
func TestEmptiedThreadRemovesFile(t *testing.T) {
	s, root := newStore(t)
	ctx := context.Background()
	tgt := comments.Target{Type: "ticket", ID: "TKT-1"}

	require.NoError(t, s.Add(ctx, tgt, comments.Comment{ID: "c1", Author: "alice", Body: "x"}))
	require.NoError(t, s.Delete(ctx, tgt, "c1"))

	_, err := os.Stat(filepath.Join(root, "TKT-1.yaml"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

// TestCorruptThreadSurfacesError pins that a hand-broken file reports rather
// than silently reading as an empty thread — losing comments quietly is worse
// than refusing to serve them.
func TestCorruptThreadSurfacesError(t *testing.T) {
	s, root := newStore(t)
	ctx := context.Background()

	require.NoError(t, os.WriteFile(filepath.Join(root, "TKT-1.yaml"), []byte("comments: [oh dear\n"), 0o644))

	_, err := s.List(ctx, comments.Target{Type: "ticket", ID: "TKT-1"})
	require.Error(t, err)
}

// TestPreSuggestionThreadLoadsUnchanged pins that suggestions (TKT-S5C0K3)
// needed no data migration: a thread written before the replacement field
// existed reads back with no suggestion, and a new one writes it in place.
func TestPreSuggestionThreadLoadsUnchanged(t *testing.T) {
	s, root := newStore(t)
	ctx := context.Background()
	tgt := comments.Target{Type: "ticket", ID: "TKT-1"}

	legacy := `comments:
    - id: c1
      author: alice@example.com
      created_at: 2026-01-02T03:04:05Z
      anchor:
        kind: text
        ref: ""
        text:
          quote: the quoted text
          paragraph_index: 0
      body: an older comment
      resolved: false
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "TKT-1.yaml"), []byte(legacy), 0o644))

	got, err := s.Get(ctx, tgt, "c1")
	require.NoError(t, err)
	require.Nil(t, got.Anchor.Replacement)
	require.Equal(t, "the quoted text", got.Anchor.Text.Quote)

	repl := ""
	require.NoError(t, s.Add(ctx, tgt, comments.Comment{
		ID: "c2", Author: "bob", Body: "delete it",
		Anchor: comments.Anchor{Kind: comments.AnchorText,
			Text: &comments.TextAnchor{Quote: "the quoted text"}, Replacement: &repl},
	}))
	data, err := os.ReadFile(filepath.Join(root, "TKT-1.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(data), `replacement: ""`)
}
