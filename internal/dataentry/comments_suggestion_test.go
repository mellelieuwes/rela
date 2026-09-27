package dataentry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/affordances"
	"github.com/Sourcehaven-BV/rela/internal/appbuild/appbuildtest"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/comments"
	"github.com/Sourcehaven-BV/rela/internal/comments/memcomments"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/project"
	"github.com/Sourcehaven-BV/rela/internal/storage"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

const (
	suggestQuote   = "the old id in the search index"
	suggestPath    = "/api/v1/_comments/ticket/TKT-001"
	suggestAuthor  = "alice@example.com"
	suggestReplace = "a stale id in the index"
)

// suggestionJSON is a create body for a text comment on quote suggesting repl.
func suggestionJSON(quote, repl string) string {
	r, _ := json.Marshal(repl)
	return `{"anchor":{"kind":"text","quote":"` + quote + `","replacement":` + string(r) + `},"body":"suggest"}`
}

// postSuggestion creates a suggestion and returns its id.
func postSuggestion(t *testing.T, app *App, path, quote, repl string) string {
	t.Helper()
	rec := doComments(t, app, http.MethodPost, path, suggestionJSON(quote, repl), suggestAuthor)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var c commentWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &c))
	return c.ID
}

func accept(t *testing.T, app *App, path, id, user string) *httptest.ResponseRecorder {
	t.Helper()
	return doComments(t, app, http.MethodPost, path+"/"+id+"/accept", "", user)
}

func storedBody(t *testing.T, app *App) string {
	t.Helper()
	e, err := app.store.GetEntity(t.Context(), "TKT-001")
	require.NoError(t, err)
	return e.Content
}

func TestCommentSuggestion_CreateAndList(t *testing.T) {
	for _, repl := range []string{suggestReplace, ""} {
		t.Run("replacement "+repl, func(t *testing.T) {
			app := commentsApp(t)
			postSuggestion(t, app, suggestPath, suggestQuote, repl)

			got := listComments(t, app)
			require.Len(t, got.Comments, 1)
			require.NotNil(t, got.Comments[0].Anchor.Replacement,
				"an empty replacement is a deletion and must not read back as absent")
			require.Equal(t, repl, *got.Comments[0].Anchor.Replacement)
			require.True(t, got.Comments[0].Acceptable)
		})
	}

	t.Run("plain text comment is not acceptable", func(t *testing.T) {
		app := commentsApp(t)
		rec := doComments(t, app, http.MethodPost, suggestPath,
			`{"anchor":{"kind":"text","quote":"`+suggestQuote+`"},"body":"x"}`, suggestAuthor)
		require.Equal(t, http.StatusCreated, rec.Code)

		got := listComments(t, app)
		require.Nil(t, got.Comments[0].Anchor.Replacement)
		require.False(t, got.Comments[0].Acceptable)
	})
}

func TestCommentSuggestion_CreateRefusals(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"property anchor", `{"anchor":{"kind":"property","ref":"status","replacement":"x"},"body":"x"}`},
		{"section anchor", `{"anchor":{"kind":"section","ref":"notes","replacement":"x"},"body":"x"}`},
		{"bidi override", suggestionJSON(suggestQuote, "a\u202eb")},
		{"NUL", suggestionJSON(suggestQuote, "a\x00b")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app := commentsApp(t)
			rec := doComments(t, app, http.MethodPost, suggestPath, tc.body, suggestAuthor)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Empty(t, listComments(t, app).Comments)
		})
	}
}

func TestCommentSuggestion_ResolveCheckReturnsSourceQuote(t *testing.T) {
	app := newTestAppV1(t)
	app.State().Meta.Comments = &metamodel.CommentsConfig{Enabled: true, On: []string{"ticket"}}
	svc, err := comments.NewService(memcomments.New(), nil)
	require.NoError(t, err)
	app.SetComments(svc)
	seedEntity(app, &entity.Entity{
		ID: "TKT-001", Type: "ticket",
		Properties: map[string]any{"title": "T", "status": "open"},
		Content:    "A **bold claim** stands here.\n",
	})

	rec := doComments(t, app, http.MethodPost, suggestPath+"/resolve",
		`{"quote":"bold claim stands"}`, suggestAuthor)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got resolveCheckResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.True(t, got.Anchorable)
	require.Equal(t, "bold claim** stands", got.SourceQuote,
		"the prefill must be the markdown source, or accepting drops the closing **")
	require.True(t, got.Suggestable)
}

func TestCommentSuggestion_ResolveCheckAcrossBlocksIsNotSuggestable(t *testing.T) {
	app := newTestAppV1(t)
	app.State().Meta.Comments = &metamodel.CommentsConfig{Enabled: true, On: []string{"ticket"}}
	svc, err := comments.NewService(memcomments.New(), nil)
	require.NoError(t, err)
	app.SetComments(svc)
	seedEntity(app, &entity.Entity{
		ID: "TKT-001", Type: "ticket",
		Properties: map[string]any{"title": "T", "status": "open"},
		Content:    "- first item here\n- second item here\n",
	})

	rec := doComments(t, app, http.MethodPost, suggestPath+"/resolve",
		`{"quote":"item here\nsecond item"}`, suggestAuthor)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got resolveCheckResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.True(t, got.Anchorable, "a comment across items is fine")
	require.False(t, got.Suggestable, "a replacement across items would drop the list marker")
}

func TestCommentSuggestion_Accept(t *testing.T) {
	t.Run("applies the replacement and resolves", func(t *testing.T) {
		app := commentsApp(t)
		id := postSuggestion(t, app, suggestPath, suggestQuote, suggestReplace)

		rec := accept(t, app, suggestPath, id, "bob@example.com")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		want := strings.Replace(fixtureBody, suggestQuote, suggestReplace, 1)
		require.Equal(t, want, storedBody(t, app))
		var resp acceptResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Equal(t, want, resp.Content)

		got := listComments(t, app)
		require.True(t, got.Comments[0].Resolved)
		require.False(t, got.Comments[0].Acceptable)
	})

	t.Run("empty replacement deletes the text", func(t *testing.T) {
		app := commentsApp(t)
		id := postSuggestion(t, app, suggestPath, suggestQuote, "")

		rec := accept(t, app, suggestPath, id, "bob@example.com")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NotContains(t, storedBody(t, app), suggestQuote)
	})

	t.Run("a second accept is refused even when the quote survives", func(t *testing.T) {
		// "X" → "X plus more" still contains X, so only the resolved flag
		// stops a double apply.
		app := commentsApp(t)
		id := postSuggestion(t, app, suggestPath, suggestQuote, suggestQuote+" and elsewhere")

		require.Equal(t, http.StatusOK, accept(t, app, suggestPath, id, "bob@example.com").Code)
		once := storedBody(t, app)

		rec := accept(t, app, suggestPath, id, "bob@example.com")
		require.Equal(t, http.StatusConflict, rec.Code)
		require.Contains(t, rec.Body.String(), "comment_resolved")
		require.Equal(t, once, storedBody(t, app))
	})
}

func TestCommentSuggestion_AcceptRefusals(t *testing.T) {
	t.Run("no replacement", func(t *testing.T) {
		app := commentsApp(t)
		rec := doComments(t, app, http.MethodPost, suggestPath,
			`{"anchor":{"kind":"text","quote":"`+suggestQuote+`"},"body":"x"}`, suggestAuthor)
		var c commentWire
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &c))

		rec = accept(t, app, suggestPath, c.ID, suggestAuthor)
		require.Equal(t, http.StatusConflict, rec.Code)
		require.Contains(t, rec.Body.String(), "no_suggestion")
	})

	t.Run("quote edited away", func(t *testing.T) {
		app := commentsApp(t)
		id := postSuggestion(t, app, suggestPath, suggestQuote, suggestReplace)
		rewritten := "This paragraph shares no wording whatsoever with the original text.\n"
		require.NoError(t, app.store.UpdateEntity(t.Context(), &entity.Entity{
			ID: "TKT-001", Type: "ticket",
			Properties: map[string]any{"title": "Test Ticket", "status": "open"},
			Content:    rewritten,
		}))

		rec := accept(t, app, suggestPath, id, suggestAuthor)
		require.Equal(t, http.StatusConflict, rec.Code)
		require.Contains(t, rec.Body.String(), "suggestion_stale")
		require.Equal(t, rewritten, storedBody(t, app))
		require.False(t, listComments(t, app).Comments[0].Resolved, "a refused accept must leave the comment open")
	})

	t.Run("unknown comment", func(t *testing.T) {
		app := commentsApp(t)
		require.Equal(t, http.StatusNotFound, accept(t, app, suggestPath, "nope", suggestAuthor).Code)
	})

	t.Run("unknown target", func(t *testing.T) {
		app := commentsApp(t)
		rec := accept(t, app, "/api/v1/_comments/ticket/TKT-404", "x", suggestAuthor)
		require.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("wrong method", func(t *testing.T) {
		app := commentsApp(t)
		rec := doComments(t, app, http.MethodGet, suggestPath+"/x/accept", "", suggestAuthor)
		require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("read-only instance", func(t *testing.T) {
		app := commentsApp(t)
		id := postSuggestion(t, app, suggestPath, suggestQuote, suggestReplace)
		app.acl = acl.ReadOnlyACL{}

		rec := accept(t, app, suggestPath, id, suggestAuthor)
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Equal(t, fixtureBody, storedBody(t, app))
	})
}

// failingPatcher stands in for the manager to force the write step to fail
// after the comment has been resolved.
type failingPatcher struct{ err error }

func (f failingPatcher) PatchEntity(context.Context, string, entity.Patch) (*entity.UpdateResult, error) {
	return nil, f.err
}

// TestCommentSuggestion_FailedWriteReopens pins the resolve-then-write order's
// recovery: a write that fails after the resolve must reopen the comment, so
// the user is not left with a suggestion marked done that never landed.
func TestCommentSuggestion_FailedWriteReopens(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{"version conflict", &store.VersionConflictError{}, http.StatusPreconditionFailed},
		{"other write error", errors.New("boom"), http.StatusUnprocessableEntity},
		{"deleted concurrently", store.ErrNotFound, http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app := commentsApp(t)
			id := postSuggestion(t, app, suggestPath, suggestQuote, suggestQuote+" and more")
			app.comments.patcher = failingPatcher{err: tc.err}

			rec := accept(t, app, suggestPath, id, suggestAuthor)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			require.Equal(t, fixtureBody, storedBody(t, app))
			require.False(t, listComments(t, app).Comments[0].Resolved)
		})
	}
}

// raceCommentStore simulates another server process acting between this
// request's read and its write.
type raceCommentStore struct {
	comments.Store
	staleGet   bool // Get reports the comment open whatever is stored
	failReopen bool // SetResolved(false) fails
}

func (s *raceCommentStore) Get(ctx context.Context, target comments.Target, id string) (comments.Comment, error) {
	c, err := s.Store.Get(ctx, target, id)
	if s.staleGet {
		c.Resolved = false
	}
	return c, err
}

func (s *raceCommentStore) SetResolved(
	ctx context.Context, target comments.Target, id string, resolved bool,
) (bool, error) {
	if s.failReopen && !resolved {
		return false, errors.New("store down")
	}
	return s.Store.SetResolved(ctx, target, id, resolved)
}

func raceApp(t *testing.T, rs *raceCommentStore) *App {
	t.Helper()
	app := commentsApp(t)
	rs.Store = memcomments.New()
	svc, err := comments.NewService(rs, nil)
	require.NoError(t, err)
	app.SetComments(svc)
	return app
}

// TestCommentSuggestion_LostClaimChangesNothing is the multi-process race:
// another node accepted the suggestion after this request read it as open.
// The conditional resolve is the only thing that can stop a second apply,
// since writeMu is per process and a replacement containing its quote still
// matches the already-edited body.
func TestCommentSuggestion_LostClaimChangesNothing(t *testing.T) {
	rs := &raceCommentStore{}
	app := raceApp(t, rs)
	id := postSuggestion(t, app, suggestPath, suggestQuote, suggestQuote+" and more")
	_, err := rs.Store.SetResolved(t.Context(), comments.Target{Type: "ticket", ID: "TKT-001"}, id, true)
	require.NoError(t, err)
	rs.staleGet = true

	rec := accept(t, app, suggestPath, id, suggestAuthor)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "comment_resolved")
	require.Equal(t, fixtureBody, storedBody(t, app))
}

func TestCommentSuggestion_ReopenFailureIsReported(t *testing.T) {
	rs := &raceCommentStore{failReopen: true}
	app := raceApp(t, rs)
	id := postSuggestion(t, app, suggestPath, suggestQuote, suggestQuote+" and more")
	app.comments.patcher = failingPatcher{err: &store.VersionConflictError{}}

	rec := accept(t, app, suggestPath, id, suggestAuthor)
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "reopen it")
	require.Equal(t, fixtureBody, storedBody(t, app))
	require.True(t, listComments(t, app).Comments[0].Resolved)
}

func TestCommentSuggestion_AcceptWritesOnlyItsFace(t *testing.T) {
	const draft = entity.Face("draft")
	const draftBody = "Draft body text that proposes a different wording entirely.\n"
	app := commentsApp(t)
	require.NoError(t, app.store.CreateEntity(t.Context(), &entity.Entity{
		ID: "TKT-001", Type: "ticket", Face: draft,
		Properties: map[string]any{"title": "Test Ticket", "status": "open"},
		Content:    draftBody,
	}))
	const draftPath = "/api/v1/_comments/ticket/TKT-001@draft"
	id := postSuggestion(t, app, draftPath, "proposes a different wording", "offers new wording")

	// Regression: the thread's anchors resolve against the DRAFT body. They
	// used to be re-read by bare id, i.e. against the default face, where
	// this quote does not occur, so the create failed and listing detached.
	lrec := doComments(t, app, http.MethodGet, draftPath, "", suggestAuthor)
	require.Equal(t, http.StatusOK, lrec.Code)
	var listed commentListResponse
	require.NoError(t, json.Unmarshal(lrec.Body.Bytes(), &listed))
	require.Len(t, listed.Comments, 1)
	require.False(t, listed.Comments[0].Detached)
	require.Equal(t, "proposes a different wording",
		draftBody[*listed.Comments[0].Anchor.Start:*listed.Comments[0].Anchor.End])
	require.True(t, listed.Comments[0].Acceptable)

	rec := accept(t, app, draftPath, id, suggestAuthor)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	d, err := app.store.GetEntityState(t.Context(), "TKT-001", draft)
	require.NoError(t, err)
	require.Contains(t, d.Content, "offers new wording")
	require.Equal(t, fixtureBody, storedBody(t, app), "the default face must be untouched")
}

// suggestionACLApp builds an app whose manager AND handlers run under one
// declarative policy, with an in-memory audit sink.
func suggestionACLApp(t *testing.T, policy *acl.Policy) (*App, *acl.Declarative, *audit.Memory) {
	t.Helper()
	meta := testMeta()
	meta.Comments = &metamodel.CommentsConfig{Enabled: true, On: []string{"ticket"}}
	fs := storage.NewMemFS()
	paths := &project.Context{Root: "/project", CacheDir: "/project/.rela"}
	require.NoError(t, fs.MkdirAll(paths.CacheDir, 0o755))

	st := appbuildtest.New(meta, appbuildtest.WithFS(fs, paths)).Store()
	require.NoError(t, st.CreateEntity(t.Context(), &entity.Entity{
		ID: "TKT-001", Type: "ticket",
		Properties: map[string]any{"title": "Test Ticket", "status": "open"},
		Content:    fixtureBody,
	}))

	d := mustNewACL(t, policy, st)
	sink := audit.NewMemory()
	svc := appbuildtest.New(meta, appbuildtest.WithFS(fs, paths), appbuildtest.WithStore(st),
		appbuildtest.WithDeclarative(d), appbuildtest.WithAudit(sink))
	app := newAppFromParts(testConfig(), meta, &fixture{})
	rebindApp(app, fs, paths, svc)
	app.acl = d

	csvc, err := comments.NewService(memcomments.New(), nil)
	require.NoError(t, err)
	app.SetComments(csvc)
	return app, d, sink
}

func postCommentsAs(t *testing.T, app *App, d *acl.Declarative, path, body, user string) *httptest.ResponseRecorder {
	t.Helper()
	ctx := gateCtxFor(principal.With(t.Context(),
		principal.Principal{User: user, Tool: principal.ToolDataEntry}), t, d)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	app.comments.handleV1Comments(rec, req)
	return rec
}

func TestCommentSuggestion_AcceptAuthorization(t *testing.T) {
	commenting := []string{"comment:read", "comment:add"}
	policy := &acl.Policy{
		Roles: map[string]acl.RoleDef{
			"reviewer": {Read: []string{"ticket"}, Permissions: commenting},
			"editor":   {Read: []string{"ticket"}, Update: []string{"ticket"}, Permissions: commenting},
			"blind":    {Read: []string{"ticket"}, Update: []string{"ticket"}},
		},
		Assignments: map[string]string{"alice": "reviewer", "bob": "editor", "carol": "blind"},
	}

	create := func(t *testing.T, app *App, d *acl.Declarative) string {
		t.Helper()
		rec := postCommentsAs(t, app, d, suggestPath,
			suggestionJSON(suggestQuote, suggestReplace), "alice")
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		var c commentWire
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &c))
		return c.ID
	}

	t.Run("commenter without entity update is refused", func(t *testing.T) {
		app, d, sink := suggestionACLApp(t, policy)
		store := &countingCommentStore{Store: memcomments.New()}
		csvc, err := comments.NewService(store, nil)
		require.NoError(t, err)
		app.SetComments(csvc)
		id := create(t, app, d)

		rec := postCommentsAs(t, app, d, suggestPath+"/"+id+"/accept", "", "alice")
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		require.Equal(t, fixtureBody, storedBody(t, app))

		// Refused before the resolve: a certain denial never writes the comment.
		c, err := app.comments.svc.Get(t.Context(), comments.Target{Type: "ticket", ID: "TKT-001"}, id)
		require.NoError(t, err)
		require.False(t, c.Resolved)
		require.Zero(t, store.updates)

		var denied int
		for _, r := range sink.Records() {
			if r.Op == audit.OpDeniedWrite && r.Principal.User == "alice" {
				denied++
			}
		}
		require.Equal(t, 1, denied)
	})

	t.Run("editor without comment:read is refused", func(t *testing.T) {
		app, d, _ := suggestionACLApp(t, policy)
		id := create(t, app, d)

		rec := postCommentsAs(t, app, d, suggestPath+"/"+id+"/accept", "", "carol")
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "comment:read")
		require.Equal(t, fixtureBody, storedBody(t, app))
	})

	t.Run("without comment:read an unknown id is not distinguishable", func(t *testing.T) {
		app, d, _ := suggestionACLApp(t, policy)
		create(t, app, d)

		rec := postCommentsAs(t, app, d, suggestPath+"/no-such-comment/accept", "", "carol")
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	})

	t.Run("editor accepts and is the audited author", func(t *testing.T) {
		app, d, sink := suggestionACLApp(t, policy)
		id := create(t, app, d)

		rec := postCommentsAs(t, app, d, suggestPath+"/"+id+"/accept", "", "bob")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, storedBody(t, app), suggestReplace)

		var updates []audit.Record
		for _, r := range sink.Records() {
			if r.Op == audit.OpUpdateEntity {
				updates = append(updates, r)
			}
		}
		require.Len(t, updates, 1)
		require.Equal(t, "bob", updates[0].Principal.User)
	})

	t.Run("unreadable target is a 404", func(t *testing.T) {
		app, d, _ := suggestionACLApp(t, policy)
		id := create(t, app, d)

		rec := postCommentsAs(t, app, d, suggestPath+"/"+id+"/accept", "", "mallory")
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	})
}

// TestCommentSuggestion_AcceptPreservesHiddenProperty is why the splice base
// is the RAW row: a version token hashed over a redacted entity never matches
// the store, and a redacted read-modify-write would drop the hidden field.
func TestCommentSuggestion_AcceptPreservesHiddenProperty(t *testing.T) {
	policy := &acl.Policy{
		Roles: map[string]acl.RoleDef{"editor": {
			Read:        []string{"ticket"},
			Update:      []string{"ticket"},
			Permissions: []string{"comment:read", "comment:add"},
			Visible:     map[string][]acl.FieldGrant{"ticket": {{Field: "title"}}},
		}},
		Assignments: map[string]string{"bob": "editor"},
	}
	app, d, _ := suggestionACLApp(t, policy)
	app.fieldResolver = mustFieldResolver(t, app, d)

	rec := postCommentsAs(t, app, d, suggestPath,
		suggestionJSON(suggestQuote, suggestReplace), "bob")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var c commentWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &c))

	rec = postCommentsAs(t, app, d, suggestPath+"/"+c.ID+"/accept", "", "bob")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	e, err := app.store.GetEntity(t.Context(), "TKT-001")
	require.NoError(t, err)
	require.Contains(t, e.Content, suggestReplace)
	require.Equal(t, "open", e.Properties["status"], "the hidden property must survive the accept")
}

// mustFieldResolver wires field-level redaction from d, as buildFieldPolicyApp
// does, so the visible reader actually hides fields.
func mustFieldResolver(t *testing.T, app *App, d *acl.Declarative) FieldVerdictResolver {
	t.Helper()
	resolver, err := affordances.New(app.Meta(), storeRelationLookup{st: app.store}, d)
	require.NoError(t, err)
	return &policyResolver{inner: resolver}
}

// countingCommentStore counts comment writes, to prove a path never makes one.
type countingCommentStore struct {
	comments.Store
	updates int
}

func (s *countingCommentStore) Update(
	ctx context.Context, target comments.Target, id, body string, resolved bool,
) error {
	s.updates++
	return s.Store.Update(ctx, target, id, body, resolved)
}

func (s *countingCommentStore) SetResolved(
	ctx context.Context, target comments.Target, id string, resolved bool,
) (bool, error) {
	s.updates++
	return s.Store.SetResolved(ctx, target, id, resolved)
}
