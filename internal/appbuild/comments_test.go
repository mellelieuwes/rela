package appbuild

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/comments"
	"github.com/Sourcehaven-BV/rela/internal/comments/memcomments"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/project"
	"github.com/Sourcehaven-BV/rela/internal/storage"
)

func commentsMeta(t *testing.T, enabled bool) *metamodel.Metamodel {
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
	if enabled {
		src += "comments:\n  enabled: true\n  on: [ticket]\n"
	}
	m, err := metamodel.Parse([]byte(src))
	require.NoError(t, err)
	return m
}

func paths(t *testing.T) *project.Context {
	t.Helper()
	root := t.TempDir()
	return &project.Context{Root: root, CacheDir: filepath.Join(root, project.CacheDir)}
}

// TestBuildComments_DisabledYieldsNilService pins AC1's wiring half: with no
// `comments:` block the service is genuinely nil, which is what lets the
// data-entry app decide not to serve the routes at all.
func TestBuildComments_DisabledYieldsNilService(t *testing.T) {
	p := paths(t)
	svc, err := buildComments(storage.NewOsFS(), p, commentsMeta(t, false), nil)
	require.NoError(t, err)
	require.Nil(t, svc, "a nil service IS the disabled signal")

	_, statErr := os.Stat(filepath.Join(p.CacheDir, commentsDirName))
	require.ErrorIs(t, statErr, os.ErrNotExist, "a disabled feature creates no storage")
}

func TestBuildComments_EnabledYieldsService(t *testing.T) {
	svc, err := buildComments(storage.NewOsFS(), paths(t), commentsMeta(t, true), nil)
	require.NoError(t, err)
	require.NotNil(t, svc)
}

// TestBuildComments_EnabledWithoutPathsFails pins that a configured feature
// with nowhere to store data is a wiring error, not a silent downgrade —
// otherwise an operator who switched commenting on would find it quietly
// missing.
func TestBuildComments_EnabledWithoutPathsFails(t *testing.T) {
	t.Run("nil paths", func(t *testing.T) {
		_, err := buildComments(storage.NewOsFS(), nil, commentsMeta(t, true), nil)
		require.Error(t, err)
	})

	t.Run("nil filesystem", func(t *testing.T) {
		_, err := buildComments(nil, paths(t), commentsMeta(t, true), nil)
		require.Error(t, err)
	})
}

// TestBuildComments_BackendOverrideIsUsed pins the database recipes' half of
// TKT-OGTVJW: when a recipe supplies a backend, comments must go THERE and not
// to the filesystem.
//
// Asserting on the empty `.rela/comments/` directory is the point. A wiring
// regression that silently fell back to filecomments would still produce a
// working service and pass every other test here, while a postgres deployment
// quietly went back to node-local comments — the exact defect this ticket
// exists to fix, and one with no error message to notice.
func TestBuildComments_BackendOverrideIsUsed(t *testing.T) {
	p := paths(t)
	backend := memcomments.New()

	svc, err := buildComments(storage.NewOsFS(), p, commentsMeta(t, true), backend)
	require.NoError(t, err)
	require.NotNil(t, svc)

	ctx := commentsCtx()
	target := comments.Target{Type: "ticket", ID: "TKT-1"}
	_, err = svc.Add(ctx, target, comments.AddRequest{
		Anchor: comments.Anchor{Kind: comments.AnchorProperty, Ref: "status"},
		Body:   "in the injected backend",
	})
	require.NoError(t, err)

	stored, err := backend.List(ctx, target)
	require.NoError(t, err)
	require.Len(t, stored, 1, "the comment must land in the supplied backend")

	_, statErr := os.Stat(filepath.Join(p.CacheDir, commentsDirName))
	require.ErrorIs(t, statErr, os.ErrNotExist,
		"a supplied backend must leave no filesystem comment store behind")
}

// TestBuildComments_NilBackendUsesFilesystem is the other half: the fs, memory
// and desktop tiers pass nothing and must keep the file backend they had.
func TestBuildComments_NilBackendUsesFilesystem(t *testing.T) {
	p := paths(t)
	svc, err := buildComments(storage.NewOsFS(), p, commentsMeta(t, true), nil)
	require.NoError(t, err)
	require.NotNil(t, svc)

	// The file backend creates its directory on the first write, not at
	// construction, so the comment is what makes the storage observable.
	_, err = svc.Add(commentsCtx(), comments.Target{Type: "ticket", ID: "TKT-1"},
		comments.AddRequest{
			Anchor: comments.Anchor{Kind: comments.AnchorProperty, Ref: "status"},
			Body:   "on disk",
		})
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(p.CacheDir, commentsDirName))
	require.NoError(t, statErr, "the default tier still stores comments on disk")
}

// commentsCtx stamps an author, which the service requires before it will
// accept a comment.
func commentsCtx() context.Context {
	return principal.With(context.Background(), principal.Principal{User: "alice", Tool: "test"})
}

// recordingRewriter captures the notifications it receives.
//
// The interface assertion is the compile-time guard that a fan-out subscriber
// is substitutable for the entitymanager's single-rewriter field.
var _ entitymanager.AliasRewriter = (*recordingRewriter)(nil)

type recordingRewriter struct {
	renamed []string
	deleted []string
	err     error
}

func (r *recordingRewriter) EntityRenamed(_ context.Context, oldID, newID string) error {
	r.renamed = append(r.renamed, oldID+"->"+newID)
	return r.err
}

func (r *recordingRewriter) EntityDeleted(_ context.Context, id string) error {
	r.deleted = append(r.deleted, id)
	return r.err
}

func (r *recordingRewriter) EntityFaceDeleted(_ context.Context, id string, face entity.Face) error {
	r.deleted = append(r.deleted, entity.FormatStateRef(id, face))
	return r.err
}

// TestAliasFanout_NilWhenNothingSubscribes pins that an unused hook stays nil,
// so the Manager's nil fast path still applies.
func TestAliasFanout_NilWhenNothingSubscribes(t *testing.T) {
	require.Nil(t, newAliasFanout())
	require.Nil(t, newAliasFanout(nil, nil))
}

// TestAliasFanout_NilWhenOnlyTypedNilsSubscribe covers what the untyped-nil
// case above cannot. The nils production passes are concrete pointers from a
// disabled subsystem, and boxing one into the variadic yields an interface
// with a non-nil type word — so `s != nil` kept it and the first delete
// dereferenced it. A literal `nil` in a test is converted by the compiler to a
// true nil interface and never reproduced that.
func TestAliasFanout_NilWhenOnlyTypedNilsSubscribe(t *testing.T) {
	var disabled *comments.Service // exactly what buildComments returns when off
	require.Nil(t, newAliasFanout(disabled))
	require.Nil(t, newAliasFanout(disabled, nil))
}

// TestAliasFanout_UnwrapsSingleSubscriber keeps the common case free of an
// indirection: with one subscriber there is nothing to fan out to.
func TestAliasFanout_UnwrapsSingleSubscriber(t *testing.T) {
	only := &recordingRewriter{}
	got := newAliasFanout(nil, only)
	require.Same(t, only, got)
}

// TestAliasFanout_SkipsTypedNilBesideLiveSubscriber is the production shape:
// comments off, CalDAV aliases on. The dead subscriber must be dropped rather
// than fanned out to, and the live one must still be notified.
func TestAliasFanout_SkipsTypedNilBesideLiveSubscriber(t *testing.T) {
	var disabled *comments.Service
	live := &recordingRewriter{}

	got := newAliasFanout(disabled, live)
	require.Same(t, live, got, "a dead subscriber must not force a fanout")

	ctx := context.Background()
	require.NoError(t, got.EntityDeleted(ctx, "TKT-1"))
	require.NoError(t, got.EntityRenamed(ctx, "TKT-old", "TKT-new"))
	require.Equal(t, []string{"TKT-1"}, live.deleted)
	require.Equal(t, []string{"TKT-old->TKT-new"}, live.renamed)
}

// TestAliasFanout_DisabledCommentsSurviveDelete wires the real buildComments
// output into the fanout, which is the step the disabled-service test above
// stops short of — and the step where the panic was born. An unwired hook is
// what keeps the delete a no-op: the Manager skips a nil AliasRewriter.
func TestAliasFanout_DisabledCommentsSurviveDelete(t *testing.T) {
	svc, err := buildComments(storage.NewOsFS(), paths(t), commentsMeta(t, false), nil)
	require.NoError(t, err)
	require.Nil(t, svc)

	require.Nil(t, newAliasFanout(svc), "disabled comments must leave the hook unwired")
}

// TestAliasFanout_EnabledCommentsStillSubscribe is the other half of the
// filter: dropping a disabled service is only correct if an enabled one still
// gets through. Without this, an over-eager isNilSubscriber would silently stop
// comment cleanup on delete — and a thread stranded at a reused id is the
// hazard [comments.Service.EntityDeleted] exists to prevent, which is quieter
// than the panic this fix removed.
func TestAliasFanout_EnabledCommentsStillSubscribe(t *testing.T) {
	svc, err := buildComments(storage.NewOsFS(), paths(t), commentsMeta(t, true), nil)
	require.NoError(t, err)
	require.NotNil(t, svc)

	require.Same(t, svc, newAliasFanout(svc), "an enabled service must stay wired")
}

func TestAliasFanout_NotifiesEverySubscriber(t *testing.T) {
	a, b := &recordingRewriter{}, &recordingRewriter{}
	fanout := newAliasFanout(a, b)
	ctx := context.Background()

	require.NoError(t, fanout.EntityRenamed(ctx, "TKT-old", "TKT-new"))
	require.NoError(t, fanout.EntityDeleted(ctx, "TKT-1"))
	require.NoError(t, fanout.EntityFaceDeleted(ctx, "TKT-2", "draft"))

	require.Equal(t, []string{"TKT-old->TKT-new"}, a.renamed)
	require.Equal(t, []string{"TKT-old->TKT-new"}, b.renamed)
	require.Equal(t, []string{"TKT-1", "TKT-2@draft"}, a.deleted)
	require.Equal(t, []string{"TKT-1", "TKT-2@draft"}, b.deleted)
}

// TestAliasFanout_OneFailureDoesNotSkipOthers pins the join-don't-short-circuit
// rule. A failing CalDAV rewrite must not stop the comment store learning about
// a rename — that would strand a thread for an unrelated reason.
func TestAliasFanout_OneFailureDoesNotSkipOthers(t *testing.T) {
	boom := errors.New("boom")
	failing := &recordingRewriter{err: boom}
	healthy := &recordingRewriter{}
	fanout := newAliasFanout(failing, healthy)

	err := fanout.EntityRenamed(context.Background(), "TKT-old", "TKT-new")
	require.ErrorIs(t, err, boom)
	require.Equal(t, []string{"TKT-old->TKT-new"}, healthy.renamed,
		"a failing subscriber must not prevent the next one being notified")
}
