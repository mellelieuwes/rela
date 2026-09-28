package dataentry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/comments"
	"github.com/Sourcehaven-BV/rela/internal/comments/memcomments"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/principal"
)

// commentsApp returns a test App with commenting enabled for `ticket` and one
// seeded ticket to comment on.
func commentsApp(t *testing.T) *App {
	t.Helper()
	app := newTestAppV1(t)
	app.State().Meta.Comments = &metamodel.CommentsConfig{Enabled: true, On: []string{"ticket"}}

	svc, err := comments.NewService(memcomments.New(), nil)
	require.NoError(t, err)
	app.SetComments(svc)

	seedEntity(app, &entity.Entity{
		ID:         "TKT-001",
		Type:       "ticket",
		Properties: map[string]any{"title": "Test Ticket", "status": "open"},
		// A body, so stage-2 text anchors have something to resolve against.
		Content: fixtureBody,
	})
	return app
}

// asUser stamps a principal so the service can attribute an author.
func asUser(r *http.Request, user string) *http.Request {
	return r.WithContext(principal.With(r.Context(),
		principal.Principal{User: user, Tool: "data-entry"}))
}

func doComments(t *testing.T, app *App, method, path, body, user string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, http.NoBody)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if user != "" {
		req = asUser(req, user)
	}
	rec := httptest.NewRecorder()
	app.comments.handleV1Comments(rec, req)
	return rec
}

func listComments(t *testing.T, app *App) commentListResponse {
	t.Helper()
	rec := doComments(t, app, http.MethodGet, "/api/v1/_comments/ticket/TKT-001", "", "alice@example.com")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got commentListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	return got
}

const addBody = `{"anchor":{"kind":"property","ref":"status"},"body":"looks wrong"}`

// fixtureBody is the seeded entity's markdown. Long enough that
// markdown.FormatMarkdown would reflow it, which is what the drift test needs.
const fixtureBody = "Renaming an entity leaves the old id in the search index until a restart, " +
	"which is confusing because the store itself is already correct at that point.\n"

// TestComments_DisabledRoutes404 pins AC1: with no `comments:` block the routes
// do not exist, so a project without the block is indistinguishable from one
// built before the feature.
func TestComments_DisabledRoutes404(t *testing.T) {
	app := newTestAppV1(t) // no comments config, no service

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/_comments/ticket/TKT-001", ""},
		{http.MethodPost, "/api/v1/_comments/ticket/TKT-001", addBody},
		{http.MethodDelete, "/api/v1/_comments/ticket/TKT-001/abc", ""},
	} {
		t.Run(tc.method, func(t *testing.T) {
			rec := doComments(t, app, tc.method, tc.path, tc.body, "alice@example.com")
			require.Equal(t, http.StatusNotFound, rec.Code)
		})
	}
}

// TestComments_AddAndList pins AC2.
func TestComments_AddAndList(t *testing.T) {
	app := commentsApp(t)

	rec := doComments(t, app, http.MethodPost, "/api/v1/_comments/ticket/TKT-001", addBody, "alice@example.com")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	got := listComments(t, app)
	require.Len(t, got.Comments, 1)
	require.Equal(t, "looks wrong", got.Comments[0].Body)
	require.Equal(t, "status", got.Comments[0].Anchor.Ref)
	require.Equal(t, "property", got.Comments[0].Anchor.Kind)
}

// TestComments_AuthorIsServerStamped is AC3 at the HTTP boundary: a client that
// puts an author (or an id) in the body must not have it honored.
func TestComments_AuthorIsServerStamped(t *testing.T) {
	app := commentsApp(t)

	forged := `{"anchor":{"kind":"property","ref":"status"},"body":"x",` +
		`"author":"bob@example.com","id":"forged-id"}`
	rec := doComments(t, app, http.MethodPost, "/api/v1/_comments/ticket/TKT-001", forged, "alice@example.com")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	got := listComments(t, app)
	require.Len(t, got.Comments, 1)
	require.Equal(t, "alice@example.com", got.Comments[0].Author,
		"the request principal wins over a body-supplied author")
	require.NotEqual(t, "forged-id", got.Comments[0].ID,
		"the server mints the id")
}

// TestComments_UnidentifiedAuthorRefused pins that a caller with no resolvable
// identity cannot leave a comment nobody can later own.
func TestComments_UnidentifiedAuthorRefused(t *testing.T) {
	app := commentsApp(t)

	rec := doComments(t, app, http.MethodPost, "/api/v1/_comments/ticket/TKT-001", addBody, "")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

// TestComments_TypeNotEnabled pins AC4. The refusal names the reason: which
// types accept comments is metamodel config, not a secret.
func TestComments_TypeNotEnabled(t *testing.T) {
	app := commentsApp(t)
	seedEntity(app, &entity.Entity{
		ID: "FEAT-001", Type: "feature",
		Properties: map[string]any{"title": "A feature"},
	})

	rec := doComments(t, app, http.MethodGet, "/api/v1/_comments/feature/FEAT-001", "", "alice@example.com")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "comments_not_enabled")
}

// TestComments_InvalidPaths pins the path guards, including the traversal shape
// that must never reach a file backend.
func TestComments_InvalidPaths(t *testing.T) {
	app := commentsApp(t)

	for _, path := range []string{
		"/api/v1/_comments/ticket",
		"/api/v1/_comments/",
		"/api/v1/_comments/ticket/../../etc",
		"/api/v1/_comments/ticket/.hidden",
	} {
		t.Run(path, func(t *testing.T) {
			rec := doComments(t, app, http.MethodGet, path, "", "alice@example.com")
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

// TestComments_BodyValidation pins AC12 at the boundary.
func TestComments_BodyValidation(t *testing.T) {
	app := commentsApp(t)

	tests := []struct {
		name string
		body string
	}{
		{"empty body", `{"anchor":{"kind":"property","ref":"status"},"body":""}`},
		{"whitespace body", `{"anchor":{"kind":"property","ref":"status"},"body":"   "}`},
		{"control char in body", "{\"anchor\":{\"kind\":\"property\",\"ref\":\"status\"},\"body\":\"a\\u0000b\"}"},
		{"oversized body", `{"anchor":{"kind":"property","ref":"status"},"body":"` +
			strings.Repeat("x", comments.MaxBodyBytes+1) + `"}`},
		{"unknown anchor kind", `{"anchor":{"kind":"wat","ref":"status"},"body":"x"}`},
		{"empty anchor ref", `{"anchor":{"kind":"property","ref":""},"body":"x"}`},
		{"malformed json", `{not json`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := doComments(t, app, http.MethodPost,
				"/api/v1/_comments/ticket/TKT-001", tc.body, "alice@example.com")
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

// TestComments_DetachedAnchor pins AC9: an anchor naming a property that no
// longer exists still returns, flagged — a warning, never a rejection.
func TestComments_DetachedAnchor(t *testing.T) {
	app := commentsApp(t)

	live := `{"anchor":{"kind":"property","ref":"status"},"body":"on a real property"}`
	gone := `{"anchor":{"kind":"property","ref":"no_such_property"},"body":"on a ghost"}`
	for _, body := range []string{live, gone} {
		rec := doComments(t, app, http.MethodPost, "/api/v1/_comments/ticket/TKT-001", body, "alice@example.com")
		require.Equal(t, http.StatusCreated, rec.Code, "a detached anchor must not be refused")
	}

	got := listComments(t, app)
	require.Len(t, got.Comments, 2)

	byRef := map[string]commentWire{}
	for _, c := range got.Comments {
		byRef[c.Anchor.Ref] = c
	}
	require.False(t, byRef["status"].Detached)
	require.True(t, byRef["no_such_property"].Detached, "a vanished property reads as detached")
}

// TestComments_SectionAnchorNeverDetached pins that a section ref — which
// resolves against the view config, not the entity — is not mislabelled by the
// per-entity property check.
func TestComments_SectionAnchorNeverDetached(t *testing.T) {
	app := commentsApp(t)

	body := `{"anchor":{"kind":"section","ref":"acceptance-criteria"},"body":"about this section"}`
	rec := doComments(t, app, http.MethodPost, "/api/v1/_comments/ticket/TKT-001", body, "alice@example.com")
	require.Equal(t, http.StatusCreated, rec.Code)

	got := listComments(t, app)
	require.Len(t, got.Comments, 1)
	require.False(t, got.Comments[0].Detached,
		"a section anchor is not resolvable per-entity and must not be flagged")
}

// TestComments_UpdateAndDelete covers the mutating routes end to end.
func TestComments_UpdateAndDelete(t *testing.T) {
	app := commentsApp(t)

	rec := doComments(t, app, http.MethodPost, "/api/v1/_comments/ticket/TKT-001", addBody, "alice@example.com")
	require.Equal(t, http.StatusCreated, rec.Code)
	var created commentWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))

	path := "/api/v1/_comments/ticket/TKT-001/" + created.ID

	t.Run("resolve without echoing the body", func(t *testing.T) {
		rec := doComments(t, app, http.MethodPatch, path, `{"resolved":true}`, "alice@example.com")
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

		got := listComments(t, app)
		require.True(t, got.Comments[0].Resolved)
		require.Equal(t, "looks wrong", got.Comments[0].Body,
			"an unsupplied body is preserved, not blanked")
	})

	t.Run("edit the body", func(t *testing.T) {
		rec := doComments(t, app, http.MethodPatch, path, `{"body":"actually fine"}`, "alice@example.com")
		require.Equal(t, http.StatusNoContent, rec.Code)

		got := listComments(t, app)
		require.Equal(t, "actually fine", got.Comments[0].Body)
		require.True(t, got.Comments[0].Resolved, "an unsupplied flag is preserved too")
	})

	t.Run("delete", func(t *testing.T) {
		rec := doComments(t, app, http.MethodDelete, path, "", "alice@example.com")
		require.Equal(t, http.StatusNoContent, rec.Code)
		require.Empty(t, listComments(t, app).Comments)
	})
}

func TestComments_UnknownCommentIs404(t *testing.T) {
	app := commentsApp(t)

	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			rec := doComments(t, app, method,
				"/api/v1/_comments/ticket/TKT-001/nosuchcomment", `{"body":"x"}`, "alice@example.com")
			require.Equal(t, http.StatusNotFound, rec.Code)
		})
	}
}

// TestComments_UnknownTargetIs404 pins that a comment route cannot be used to
// probe for entities: an absent target answers exactly as a denied one would.
func TestComments_UnknownTargetIs404(t *testing.T) {
	app := commentsApp(t)

	rec := doComments(t, app, http.MethodGet, "/api/v1/_comments/ticket/TKT-999", "", "alice@example.com")
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestComments_MethodNotAllowed(t *testing.T) {
	app := commentsApp(t)

	t.Run("collection", func(t *testing.T) {
		rec := doComments(t, app, http.MethodPut, "/api/v1/_comments/ticket/TKT-001", "", "alice@example.com")
		require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		require.Contains(t, rec.Header().Get("Allow"), "GET")
	})

	t.Run("item", func(t *testing.T) {
		rec := doComments(t, app, http.MethodGet, "/api/v1/_comments/ticket/TKT-001/abc", "", "alice@example.com")
		require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		require.Contains(t, rec.Header().Get("Allow"), "PATCH")
	})
}

// TestComments_ReadOnlyInstanceRefusesWrites pins that ReadOnlyACL — wired for
// "absolute confidence no writes happen" — covers comments too. They bypass the
// entitymanager, so its blanket write deny never sees them; this is where the
// guarantee is kept.
func TestComments_ReadOnlyInstanceRefusesWrites(t *testing.T) {
	app := commentsApp(t)
	app.acl = acl.ReadOnlyACL{}

	t.Run("add refused", func(t *testing.T) {
		rec := doComments(t, app, http.MethodPost,
			"/api/v1/_comments/ticket/TKT-001", addBody, "alice@example.com")
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Contains(t, rec.Body.String(), "read-only")
	})

	t.Run("update refused", func(t *testing.T) {
		rec := doComments(t, app, http.MethodPatch,
			"/api/v1/_comments/ticket/TKT-001/abc", `{"body":"x"}`, "alice@example.com")
		require.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("delete refused", func(t *testing.T) {
		rec := doComments(t, app, http.MethodDelete,
			"/api/v1/_comments/ticket/TKT-001/abc", "", "alice@example.com")
		require.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("reads still work", func(t *testing.T) {
		rec := doComments(t, app, http.MethodGet,
			"/api/v1/_comments/ticket/TKT-001", "", "alice@example.com")
		require.Equal(t, http.StatusOK, rec.Code)
	})
}

// TestComments_SchemaExposesCommentable pins the SPA's discovery signal: the
// UI must be able to tell which types accept a comment affordance without
// probing the comment routes for a 400.
//
// The flag is policy, not permission — it says commenting is possible here,
// never that the caller may do it — and it is omitted entirely when false, so
// a project with no `comments:` block serves the schema it always did.
func TestComments_SchemaExposesCommentable(t *testing.T) {
	t.Run("enabled type is flagged", func(t *testing.T) {
		app := commentsApp(t)

		rec := httptest.NewRecorder()
		app.handleV1Schema(rec, httptest.NewRequest(http.MethodGet, "/api/v1/_schema", http.NoBody))
		require.Equal(t, http.StatusOK, rec.Code)

		var got struct {
			Entities map[string]struct {
				Commentable bool `json:"commentable"`
			} `json:"entities"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.True(t, got.Entities["ticket"].Commentable)
	})

	t.Run("absent block omits the field", func(t *testing.T) {
		app := newTestAppV1(t) // no comments config

		rec := httptest.NewRecorder()
		app.handleV1Schema(rec, httptest.NewRequest(http.MethodGet, "/api/v1/_schema", http.NoBody))
		require.Equal(t, http.StatusOK, rec.Code)

		var raw map[string]map[string]map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
		for name, et := range raw["entities"] {
			_, present := et["commentable"]
			require.False(t, present, "type %q leaked a commentable key with no comments block", name)
		}
	})
}

// TestComments_TextAnchor covers the stage-2 text-range path end to end: the
// client sends only a quote, the server derives the descriptors from its own
// copy of the body, and a read resolves a fresh range.
func TestComments_TextAnchor(t *testing.T) {
	quote := "the old id in the search index"

	t.Run("create derives descriptors and read resolves a range", func(t *testing.T) {
		app := commentsApp(t)

		body := `{"anchor":{"kind":"text","quote":"` + quote + `"},"body":"is this the bleve index?"}`
		rec := doComments(t, app, http.MethodPost, "/api/v1/_comments/ticket/TKT-001", body, "alice@example.com")
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

		got := listComments(t, app)
		require.Len(t, got.Comments, 1)
		anchor := got.Comments[0].Anchor

		require.Equal(t, "text", anchor.Kind)
		require.Equal(t, quote, anchor.Quote, "the quote is echoed so a client can show what was commented on")
		require.False(t, got.Comments[0].Detached)
		require.NotNil(t, anchor.Start)
		require.NotNil(t, anchor.End)
		// The resolved range must actually locate the quote in the body.
		require.Equal(t, quote, fixtureBody[*anchor.Start:*anchor.End])
		require.GreaterOrEqual(t, anchor.Confidence, 0.8)
		require.False(t, anchor.Uncertain)
	})

	t.Run("a quote absent from the body is refused", func(t *testing.T) {
		app := commentsApp(t)

		body := `{"anchor":{"kind":"text","quote":"text that is nowhere in the body"},"body":"x"}`
		rec := doComments(t, app, http.MethodPost, "/api/v1/_comments/ticket/TKT-001", body, "alice@example.com")

		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	})

	t.Run("a too-short selection is refused", func(t *testing.T) {
		app := commentsApp(t)

		body := `{"anchor":{"kind":"text","quote":"the"},"body":"x"}`
		rec := doComments(t, app, http.MethodPost, "/api/v1/_comments/ticket/TKT-001", body, "alice@example.com")

		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	})

	t.Run("detaches when the quoted text is edited away", func(t *testing.T) {
		app := commentsApp(t)

		body := `{"anchor":{"kind":"text","quote":"` + quote + `"},"body":"still relevant?"}`
		rec := doComments(t, app, http.MethodPost, "/api/v1/_comments/ticket/TKT-001", body, "alice@example.com")
		require.Equal(t, http.StatusCreated, rec.Code)

		// Rewrite the body so the quote no longer occurs.
		require.NoError(t, app.store.UpdateEntity(t.Context(), &entity.Entity{
			ID:         "TKT-001",
			Type:       "ticket",
			Properties: map[string]any{"title": "Test Ticket", "status": "open"},
			Content:    "This paragraph shares no wording whatsoever with the original text.\n",
		}))

		got := listComments(t, app)
		require.Len(t, got.Comments, 1)
		require.True(t, got.Comments[0].Detached, "an edited-away quote must detach, not match loosely")
		require.Nil(t, got.Comments[0].Anchor.Start, "a detached anchor exposes no range")
	})
}

// TestComments_TextAnchorDisambiguation pins the fix for a comment landing on
// the WRONG occurrence of a repeated quote.
//
// Reported from the demo: selecting part of "Geordend" stored an anchor whose
// prefix read "### Ong", so the highlight snapped to "Ongeordend" earlier in
// the document. The quote alone cannot decide between them — the surrounding
// rendered text is what selects the occurrence the user actually meant.
func TestComments_TextAnchorDisambiguation(t *testing.T) {
	const repeated = "## Lijsten\n\n### Ongeordend\n\n- first list item\n\n### Geordend\n\n1. second list item\n"

	newApp := func(t *testing.T) *App {
		t.Helper()
		app := commentsApp(t)
		require.NoError(t, app.store.UpdateEntity(t.Context(), &entity.Entity{
			ID:         "TKT-001",
			Type:       "ticket",
			Properties: map[string]any{"title": "Test Ticket", "status": "open"},
			Content:    repeated,
		}))
		return app
	}

	// Byte offsets of each "eordend" in the fixture, so the assertions name a
	// position derived from the fixture rather than a hardcoded number.
	first := strings.Index(repeated, "eordend")
	second := strings.Index(repeated[first+1:], "eordend") + first + 1
	require.NotEqual(t, first, second, "fixture must contain the quote twice")

	t.Run("suffix context selects the second occurrence", func(t *testing.T) {
		app := newApp(t)

		body := `{"anchor":{"kind":"text","quote":"eordend",` +
			`"quote_prefix":"G","quote_suffix":"\n\n1. second list item"},"body":"the ordered one"}`
		rec := doComments(t, app, http.MethodPost, "/api/v1/_comments/ticket/TKT-001", body, "alice@example.com")
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

		got := listComments(t, app)
		require.Len(t, got.Comments, 1)
		require.NotNil(t, got.Comments[0].Anchor.Start)
		require.Equal(t, second, *got.Comments[0].Anchor.Start,
			"context pointed at Geordend, so the anchor must not land on Ongeordend")
	})

	t.Run("suffix context selects the first occurrence", func(t *testing.T) {
		app := newApp(t)

		body := `{"anchor":{"kind":"text","quote":"eordend",` +
			`"quote_prefix":"Ong","quote_suffix":"\n\n- first list item"},"body":"the unordered one"}`
		rec := doComments(t, app, http.MethodPost, "/api/v1/_comments/ticket/TKT-001", body, "alice@example.com")
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

		got := listComments(t, app)
		require.Len(t, got.Comments, 1)
		require.NotNil(t, got.Comments[0].Anchor.Start)
		require.Equal(t, first, *got.Comments[0].Anchor.Start)
	})
}

// TestComments_ResolveCheck covers the preflight the SPA uses to decide whether
// to OFFER commenting on a selection.
//
// Without it a user selects text that cannot anchor (one spanning table cells
// has no contiguous source range), writes a comment, and only then learns it
// cannot be saved.
func TestComments_ResolveCheck(t *testing.T) {
	const path = "/api/v1/_comments/ticket/TKT-001/resolve"

	t.Run("an anchorable selection reports true", func(t *testing.T) {
		app := commentsApp(t)

		body := `{"quote":"the old id in the search index"}`
		rec := doComments(t, app, http.MethodPost, path, body, "alice@example.com")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		var got struct {
			Anchorable bool   `json:"anchorable"`
			Reason     string `json:"reason"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.True(t, got.Anchorable)
		require.Empty(t, got.Reason)
	})

	t.Run("an unanchorable selection reports false with a reason", func(t *testing.T) {
		app := commentsApp(t)

		body := `{"quote":"text that appears nowhere in this body at all"}`
		rec := doComments(t, app, http.MethodPost, path, body, "alice@example.com")
		// The check SUCCEEDS; the answer is "no". A 4xx here would make the UI
		// unable to distinguish "cannot anchor" from "request was broken".
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		var got struct {
			Anchorable bool   `json:"anchorable"`
			Reason     string `json:"reason"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.False(t, got.Anchorable)
		require.NotEmpty(t, got.Reason, "the UI shows this to explain why it cannot comment")
	})

	t.Run("creates nothing", func(t *testing.T) {
		app := commentsApp(t)

		body := `{"quote":"the old id in the search index"}`
		require.Equal(t, http.StatusOK,
			doComments(t, app, http.MethodPost, path, body, "alice@example.com").Code)

		require.Empty(t, listComments(t, app).Comments, "a preflight must not persist anything")
	})
}

// TestComments_PerFaceThreads pins that a comment belongs to ONE content state
// (FEAT-9CD2MX).
//
// A remark on the draft ("this needs a source") is not a remark on the
// published version, and the read gate is per face too — so a shared thread
// would both misattribute feedback and leak across a boundary the entity
// itself maintains.
func TestComments_PerFaceThreads(t *testing.T) {
	const draft = entity.Face("draft")

	app := commentsApp(t)
	// A second face of the SAME entity id.
	require.NoError(t, app.store.CreateEntity(t.Context(), &entity.Entity{
		ID:         "TKT-001",
		Type:       "ticket",
		Face:       draft,
		Properties: map[string]any{"title": "Test Ticket", "status": "open"},
		Content:    "Draft body text that differs from the default face.\n",
	}))

	post := func(t *testing.T, path, body string) {
		t.Helper()
		rec := doComments(t, app, http.MethodPost, path, body, "alice@example.com")
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	}
	list := func(t *testing.T, path string) commentListResponse {
		t.Helper()
		rec := doComments(t, app, http.MethodGet, path, "", "alice@example.com")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var got commentListResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		return got
	}

	const defaultPath = "/api/v1/_comments/ticket/TKT-001"
	const draftPath = "/api/v1/_comments/ticket/TKT-001@draft"

	post(t, defaultPath, `{"anchor":{"kind":"property","ref":"status"},"body":"on the default face"}`)
	post(t, draftPath, `{"anchor":{"kind":"property","ref":"status"},"body":"on the draft face"}`)

	onDefault := list(t, defaultPath)
	require.Len(t, onDefault.Comments, 1)
	require.Equal(t, "on the default face", onDefault.Comments[0].Body,
		"the default face must not show the draft's thread")

	onDraft := list(t, draftPath)
	require.Len(t, onDraft.Comments, 1)
	require.Equal(t, "on the draft face", onDraft.Comments[0].Body)
}

// seedDraftTicket adds a draft face to the commentsApp fixture's TKT-001 whose
// body shares no wording with the default face's, so a text anchor can only
// resolve against the face it was made on.
func seedDraftTicket(t *testing.T, app *App) {
	t.Helper()
	require.NoError(t, app.store.CreateEntity(t.Context(), &entity.Entity{
		ID:         "TKT-001",
		Type:       "ticket",
		Face:       "draft",
		Properties: map[string]any{"title": "Test Ticket", "status": "open"},
		Content:    draftBody,
	}))
}

// draftBody is the draft face's markdown.
const draftBody = "A draft paragraph about migrating the bleve index to postgres full-text search.\n"

// doCommentsAs runs a comments request as user under a declarative policy,
// with the request-scoped ACL state the production middleware attaches.
func doCommentsAs(
	t *testing.T, app *App, d *acl.Declarative, method, path, body, user string,
) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, http.NoBody)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	req = req.WithContext(gateCtxFor(principalCtx(user), t, d))
	rec := httptest.NewRecorder()
	app.comments.handleV1Comments(rec, req)
	return rec
}

// TestComments_FacedThreadUnderRelationConferredGrant pins BUG-R1PQY9 for a
// query-shaped read grant.
//
// The handler used to hand the row gate `TKT-001@draft` whole. A grant
// conferred by a relation composes a query keyed on the bare id, so the
// suffixed string matched nothing and every faced thread 404'd on every
// backend, while the bare face of the same entity worked.
func TestComments_FacedThreadUnderRelationConferredGrant(t *testing.T) {
	app := commentsApp(t)
	seedDraftTicket(t, app)
	_, err := app.store.CreateRelation(t.Context(), "alice", "owned-by", "TKT-001", nil)
	require.NoError(t, err)

	const draftPath = "/api/v1/_comments/ticket/TKT-001@draft"
	const note = `{"anchor":{"kind":"property","ref":"status"},"body":"on the draft"}`

	t.Run("a conferred grant reaches the draft thread", func(t *testing.T) {
		d := mustNewACL(t, &acl.Policy{
			Roles: map[string]acl.RoleDef{"viewer": {
				Read:        []string{"ticket"},
				Permissions: []string{"comment:read", "comment:add"},
			}},
			RoleRelations: map[string]acl.RoleRelationDef{"owned-by": {Confers: "viewer"}},
		}, app.store)
		app.acl = d

		rec := doCommentsAs(t, app, d, http.MethodPost, draftPath, note, "alice")
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

		rec = doCommentsAs(t, app, d, http.MethodGet, draftPath, "", "alice")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var got commentListResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Len(t, got.Comments, 1)
		require.Equal(t, "on the draft", got.Comments[0].Body)
	})

	t.Run("a grant without the draft face still 404s it", func(t *testing.T) {
		// The negative control: parsing the address must not bypass the face
		// gate. A global assignment passes the row gate, so the face gate is
		// the only thing standing between alice and the draft thread.
		d := mustNewACL(t, &acl.Policy{
			Roles: map[string]acl.RoleDef{"viewer": {
				Read:        []string{"ticket@published"},
				Permissions: []string{"comment:read"},
			}},
			Assignments: map[string]string{"alice": "viewer"},
		}, app.store)
		app.acl = d

		rec := doCommentsAs(t, app, d, http.MethodGet, draftPath, "", "alice")
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	})
}

// TestComments_TextAnchorResolvesAgainstItsFace pins BUG-R1PQY9's anchor half.
//
// The gate resolved the draft face, but the anchor code re-read the entity by
// its bare id and got the DEFAULT face's body: a quote selected on the draft
// was refused as "not found", and a stored draft anchor was resolved against
// text it was never about.
func TestComments_TextAnchorResolvesAgainstItsFace(t *testing.T) {
	const draftPath = "/api/v1/_comments/ticket/TKT-001@draft"
	const draftQuote = "migrating the bleve index to postgres"

	t.Run("a quote from the draft body anchors on the draft", func(t *testing.T) {
		app := commentsApp(t)
		seedDraftTicket(t, app)

		body := `{"anchor":{"kind":"text","quote":"` + draftQuote + `"},"body":"which release?"}`
		rec := doComments(t, app, http.MethodPost, draftPath, body, "alice@example.com")
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

		rec = doComments(t, app, http.MethodGet, draftPath, "", "alice@example.com")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var got commentListResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Len(t, got.Comments, 1)
		anchor := got.Comments[0].Anchor
		require.False(t, got.Comments[0].Detached)
		require.NotNil(t, anchor.Start)
		require.NotNil(t, anchor.End)
		// The range must locate the quote in the DRAFT body; resolved against
		// the default face it would be detached or point elsewhere.
		require.Equal(t, draftQuote, draftBody[*anchor.Start:*anchor.End])
	})

	t.Run("a quote only in the default body is refused on the draft", func(t *testing.T) {
		app := commentsApp(t)
		seedDraftTicket(t, app)

		body := `{"anchor":{"kind":"text","quote":"the old id in the search index"},"body":"x"}`
		rec := doComments(t, app, http.MethodPost, draftPath, body, "alice@example.com")
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	})

	t.Run("the resolve preflight checks the draft body", func(t *testing.T) {
		app := commentsApp(t)
		seedDraftTicket(t, app)

		rec := doComments(t, app, http.MethodPost, draftPath+"/resolve",
			`{"quote":"`+draftQuote+`"}`, "alice@example.com")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var got resolveCheckResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.True(t, got.Anchorable, got.Reason)
	})
}
