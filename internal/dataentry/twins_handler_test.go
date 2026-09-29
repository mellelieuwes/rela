package dataentry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	v1 "github.com/Sourcehaven-BV/rela/internal/apiwire/v1"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/twins"
	"github.com/Sourcehaven-BV/rela/internal/twins/memtwins"
)

const (
	twinTicket      = "TKT-001"
	twinSecretTitle = "Secret launch codename"
	twinRemoteTitle = "Remote rewrite of the codename"
)

// twinStoreReader is the test twin of appbuild's reader adapter: the raw
// stored entity and its version.
type twinStoreReader struct{ st store.Store }

func (r twinStoreReader) GetEntityVersion(ctx context.Context, id string) (*entity.Entity, string, error) {
	e, err := r.st.GetEntity(ctx, id)
	if err != nil {
		return nil, "", err
	}
	return e, string(store.VersionOf(e)), nil
}

func (twinStoreReader) VersionAt(e *entity.Entity, id string) string {
	at := *e
	at.ID = id
	return string(store.VersionOf(&at))
}

// refusingSyncWriter fails every sync write; the scenarios below never write.
type refusingSyncWriter struct{}

func (refusingSyncWriter) PatchEntity(context.Context, string, entity.Patch) (*entity.UpdateResult, error) {
	return nil, errors.New("no sync write expected in this test")
}

// twinsApp returns a test App whose ticket type has a pact with basecamp
// owning theirs, one seeded ticket, and the twins service wired. The ticket
// is linked to basecamp todo 123.
func twinsApp(t *testing.T, theirs ...string) (*App, *twins.Service) {
	t.Helper()
	app := newTestAppV1(t)
	meta := app.State().Meta
	def := meta.Entities["ticket"]
	def.Pacts = map[string]metamodel.PactDef{
		"basecamp": {Scope: "https://app.basecamp.com/1/buckets/2/todolists/3", Theirs: theirs},
	}
	meta.Entities["ticket"] = def
	seedEntity(app, &entity.Entity{
		ID: twinTicket, Type: "ticket",
		Properties: map[string]any{"title": twinSecretTitle, "status": "open"},
		Content:    "The body.\n",
	})
	svc, err := twins.NewService(memtwins.New(), func() *metamodel.Metamodel { return app.State().Meta },
		twinStoreReader{app.store}, refusingSyncWriter{}, nil)
	require.NoError(t, err)
	SetTwins(app, svc)
	_, err = svc.Link(t.Context(), twinTicket, "basecamp", "123", "https://app.basecamp.com/1/todos/123", time.Time{})
	require.NoError(t, err)
	return app, svc
}

// seedForeignEdit syncs the twin once, then pulls a remote whose title the
// external side rewrote: a foreign_edit finding carrying both title values.
func seedForeignEdit(t *testing.T, app *App, svc *twins.Service) {
	t.Helper()
	ctx := t.Context()
	e, err := app.store.GetEntity(ctx, twinTicket)
	require.NoError(t, err)
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	_, err = svc.Pushed(ctx, "basecamp", "123", string(store.VersionOf(e)), t0)
	require.NoError(t, err)
	res, err := svc.Pull(ctx, "basecamp", "123", twins.Remote{
		Properties: map[string]any{"title": twinRemoteTitle, "status": "open"},
	}, t0.Add(time.Hour), false)
	require.NoError(t, err)
	require.Len(t, res.Twin.Findings, 1, "fixture must produce the foreign_edit finding")
}

func getTwins(ctx context.Context, t *testing.T, app *App, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctx)
	rec := httptest.NewRecorder()
	app.twins.handleV1Twins(rec, req)
	return rec
}

// titleHidingResolver hides `title` from every principal.
type titleHidingResolver struct{}

func (titleHidingResolver) FieldVerdicts(context.Context, *entity.Entity) FieldVerdicts {
	return FieldVerdicts{Visible: map[string]bool{"title": false}}
}

func (titleHidingResolver) RelationVerdicts(context.Context, *entity.Entity) RelationVerdicts {
	return RelationVerdicts{}
}

func TestTwinsRoute_DisabledIs404(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{ID: twinTicket, Type: "ticket", Properties: map[string]any{"title": "T"}})

	rec := getTwins(t.Context(), t, app, "/api/v1/_twins/ticket/"+twinTicket)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "/errors/not_found")
}

func TestTwinsRoute_UnreadableEntityIsIndistinguishable404(t *testing.T) {
	app, _ := twinsApp(t, "status")
	d := mustNewACL(t, &acl.Policy{
		Roles:       map[string]acl.RoleDef{"viewer": {Read: []string{"feature"}}},
		Assignments: map[string]string{"alice": "viewer"},
	}, app.store)
	app.acl = d
	ctx := gateCtxFor(aliceCtx(), t, d)

	denied := getTwins(ctx, t, app, "/api/v1/_twins/ticket/"+twinTicket)
	missing := getTwins(ctx, t, app, "/api/v1/_twins/ticket/TKT-404")
	require.Equal(t, http.StatusNotFound, denied.Code, denied.Body.String())
	require.Equal(t, http.StatusNotFound, missing.Code)
	require.Equal(t, stripInstance(t, missing.Body.String()), stripInstance(t, denied.Body.String()))
	require.NotContains(t, denied.Body.String(), "basecamp")
}

func TestTwinsRoute_ListsTwins(t *testing.T) {
	app, svc := twinsApp(t, "status")
	seedForeignEdit(t, app, svc)

	rec := getTwins(t.Context(), t, app, "/api/v1/_twins/ticket/"+twinTicket)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got []v1.Twin
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
	tw := got[0]
	require.Equal(t, "basecamp", tw.System)
	require.Equal(t, "123", tw.ExternalID)
	require.Equal(t, "pending", tw.State)
	require.True(t, tw.HasBase)
	require.Equal(t, "2026-09-01T11:00:00Z", tw.RemoteUpdatedAt)
	require.NotEmpty(t, tw.SyncedAt)
	require.Equal(t, []string{"status"}, tw.OwnedFields)
	require.Equal(t, []v1.TwinFinding{{
		Field: "title", Kind: "foreign_edit",
		Base: twinSecretTitle, Ours: twinSecretTitle, Theirs: twinRemoteTitle,
	}}, tw.Findings)
}

// TestTwinsRoute_HiddenFieldValuesNeverAppear pins the read-out redaction
// (R11): a principal who may not see `title` learns that a finding is about
// title, never what either side holds.
func TestTwinsRoute_HiddenFieldValuesNeverAppear(t *testing.T) {
	app, svc := twinsApp(t, "status")
	seedForeignEdit(t, app, svc)
	app.fieldResolver = titleHidingResolver{}

	rec := getTwins(t.Context(), t, app, "/api/v1/_twins/ticket/"+twinTicket)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	require.NotContains(t, body, twinSecretTitle)
	require.NotContains(t, body, twinRemoteTitle)

	var got []v1.Twin
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
	require.Equal(t, []v1.TwinFinding{{Field: "title", Kind: "foreign_edit"}}, got[0].Findings)
}

func getTicket(t *testing.T, app *App) v1.Entity {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tickets/"+twinTicket, http.NoBody)
	app.handleV1GetEntity(rec, req, "ticket", "tickets", twinTicket)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var e v1.Entity
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e))
	return e
}

func TestTwinsVerdicts_OwnedFieldsAndBody(t *testing.T) {
	tests := []struct {
		name            string
		theirs          []string
		readOnly        []string
		writable        []string
		contentWritable bool
	}{
		{"one owned property", []string{"status"}, []string{"status"}, []string{"title"}, true},
		{"owned body", []string{"body"}, nil, []string{"title", "status"}, false},
		{"everything", []string{"*"}, []string{"title", "status"}, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app, _ := twinsApp(t, tc.theirs...)
			e := getTicket(t, app)
			require.NotNil(t, e.FieldAffordances)
			fields := *e.FieldAffordances
			for _, name := range tc.readOnly {
				fa := fields[name]
				require.NotNil(t, fa.Writable, name)
				require.False(t, *fa.Writable, name)
				require.Equal(t, "Owned by basecamp", fa.Reason, name)
			}
			for _, name := range tc.writable {
				require.Nil(t, fields[name].Writable, name)
			}
			require.NotNil(t, e.ContentWritable)
			require.Equal(t, tc.contentWritable, *e.ContentWritable)
		})
	}
}

func patchTicket(t *testing.T, app *App, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/tickets/"+twinTicket, strings.NewReader(body))
	rec := httptest.NewRecorder()
	app.write.handleV1UpdateEntity(rec, req, "ticket", "tickets", twinTicket)
	return rec
}

func TestTwinsPatch_OwnershipIsChangeBased(t *testing.T) {
	tests := []struct {
		name     string
		theirs   string
		body     string
		wantCode int
	}{
		{"changing an owned field", "status", `{"properties":{"status":"done"}}`, http.StatusUnprocessableEntity},
		{"unsetting an owned field", "status", `{"properties_unset":["status"]}`, http.StatusUnprocessableEntity},
		{"resending the owned value", "status", `{"properties":{"status":"open","title":"New"}}`, http.StatusOK},
		{"changing an owned body", "body", `{"content":"Rewritten.\n"}`, http.StatusUnprocessableEntity},
		{"resending the owned body", "body", `{"content":"The body.\n","properties":{"status":"x"}}`, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app, _ := twinsApp(t, tc.theirs)
			rec := patchTicket(t, app, tc.body)
			require.Equal(t, tc.wantCode, rec.Code, rec.Body.String())
			if tc.wantCode != http.StatusUnprocessableEntity {
				return
			}
			require.Contains(t, rec.Body.String(), "owned by basecamp (Twin)")
			stored, err := app.store.GetEntity(t.Context(), twinTicket)
			require.NoError(t, err)
			require.Equal(t, "open", stored.Properties["status"], "a refused write must persist nothing")
			require.Equal(t, "The body.\n", stored.Content)
		})
	}
}

// TestWritePatchError_TheirsWriteErrorIs422 pins the manager-side refusal: a
// write the affordance check did not see (another route, or a race with a
// link) still reaches the client as the ownership 422, not a validation one.
func TestWritePatchError_TheirsWriteErrorIs422(t *testing.T) {
	err := &entitymanager.TheirsWriteError{
		Type: "ticket", ID: twinTicket, Fields: []string{"status"}, Systems: []string{"basecamp"},
	}
	rec := httptest.NewRecorder()
	writePatchError(rec, httptest.NewRequest(http.MethodPatch, "/api/v1/tickets/"+twinTicket, http.NoBody),
		errors.Join(errors.New("wrapped"), err))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	var got v1.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "https://rela.dev/errors/externally_owned", got.Type)
	require.Equal(t, "field status of TKT-001 is owned by basecamp (Twin); change it there", got.Detail)
}

// TestTwinOwnership_LookupPolicy pins when ownership is asked and what a
// failed lookup means.
func TestTwinOwnership_LookupPolicy(t *testing.T) {
	meta := &metamodel.Metamodel{Entities: map[string]metamodel.EntityDef{
		"ticket": {
			Properties: map[string]metamodel.PropertyDef{
				"title": {Type: "string"}, "status": {Type: "string"}, "score": {Type: "integer", Computed: "1"},
			},
			Pacts: map[string]metamodel.PactDef{"basecamp": {Scope: "https://x.test", Theirs: []string{"*"}}},
		},
		"feature": {Properties: map[string]metamodel.PropertyDef{"title": {Type: "string"}}},
	}}
	stored := &entity.Entity{ID: twinTicket, Type: "ticket", Properties: map[string]any{"title": "T", "score": 1}}
	var calls int
	svcWith := func(owned map[string][]string, err error) affordanceService {
		return affordanceService{
			resolver: func() FieldVerdictResolver { return NopFieldVerdictResolver{} },
			meta:     func() *metamodel.Metamodel { return meta },
			owned: func(context.Context, string, string) (map[string][]string, error) {
				calls++
				return owned, err
			},
		}
	}
	ctx := t.Context()

	t.Run("a type without a pact is never looked up", func(t *testing.T) {
		calls = 0
		svc := svcWith(nil, errors.New("must not be called"))
		feat := &entity.Entity{ID: "FEAT-1", Type: "feature", Properties: map[string]any{"title": "F"}}
		require.Nil(t, svc.validateFieldWrite(ctx, feat, map[string]any{"title": "G"}, nil))
		_ = svc.fieldVerdicts(ctx, feat)
		require.Zero(t, calls)
	})
	t.Run("a create candidate is never looked up", func(t *testing.T) {
		calls = 0
		svc := svcWith(nil, errors.New("must not be called"))
		candidate := &entity.Entity{Type: "ticket", Properties: map[string]any{"title": "New"}}
		require.Nil(t, svc.validateFieldWrite(ctx, candidate, candidate.Properties, nil))
		require.Zero(t, calls)
	})
	t.Run("star owns every non-computed property", func(t *testing.T) {
		v := svcWith(map[string][]string{"*": {"basecamp"}}, nil).fieldVerdicts(ctx, stored)
		require.False(t, v.IsWritable("title"))
		require.False(t, v.IsWritable("status"))
		require.False(t, v.owned.ownsProperty("score"), "computed properties are never owned")
		require.True(t, v.owned.content)
	})
	t.Run("a failed lookup fails closed", func(t *testing.T) {
		svc := svcWith(nil, errors.New("disk on fire"))
		v := svc.fieldVerdicts(ctx, stored)
		require.False(t, v.IsWritable("title"))
		require.True(t, v.owned.content)
		d := svc.validateFieldWrite(ctx, stored, map[string]any{"title": "Changed"}, nil)
		require.NotNil(t, d)
		require.Equal(t, RuleFieldExternallyOwned, d.Rule)
	})
}
