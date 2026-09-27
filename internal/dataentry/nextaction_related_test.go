package dataentry

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/appbuild"
	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/principal"
)

// A next-action `condition:` may use related(): the suggestion is the ticket
// that implements a feature. A plain conjunct beside it still pushes to the
// store; the traversal is answered in the Go pass (TKT-205V2N).
func TestNextAction_RelatedCondition(t *testing.T) {
	app := newTestAppV1(t)
	withTicketAssignment(t, app)
	seedAssignedTicket(app, "TKT-free", "alice")
	seedAssignedTicket(app, "TKT-linked", "alice")
	seedEntity(app, &entity.Entity{ID: "FEAT-1", Type: "feature", Properties: map[string]any{"title": "f"}})
	seedRelation(app, &entity.Relation{From: "TKT-linked", Type: "implements", To: "FEAT-1"})
	require.NoError(t, app.SetNextActionMatchers(appbuild.NextActionMatchers))

	for _, tc := range []struct{ name, condition, want string }{
		{"related", "related(entity, 'implements')", "TKT-linked"},
		{"not related", "not related(entity, 'implements')", "TKT-free"},
		{"with a pushed conjunct", "entity.status == 'open' and related(entity, 'implements')", "TKT-linked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withNextActions(t, app, oneBand, map[string]dataentryconfig.NextActionSource{
				"s": {Band: "b", Query: "type:ticket", Condition: tc.condition, Suggest: "{id}"},
			})
			resp, status := getNextAction(principalCtx("alice"), t, app)
			require.Equal(t, http.StatusOK, status)
			require.NotNil(t, resp.Suggestion)
			require.Equal(t, tc.want, resp.Suggestion.EntityID)
		})
	}

	// current_user.id binds per request. With no user entity type it is the
	// raw principal, so a principal named like the feature matches it.
	t.Run("current user id", func(t *testing.T) {
		withNextActions(t, app, oneBand, map[string]dataentryconfig.NextActionSource{
			"s": {Band: "b", Query: "type:ticket", Suggest: "{id}",
				Condition: "related(entity, 'implements', { id = current_user.id })"},
		})
		resp, status := getNextAction(principalCtx("FEAT-1"), t, app)
		require.Equal(t, http.StatusOK, status)
		require.NotNil(t, resp.Suggestion)
		require.Equal(t, "TKT-linked", resp.Suggestion.EntityID)

		resp, status = getNextAction(principalCtx("alice"), t, app)
		require.Equal(t, http.StatusOK, status)
		require.Nil(t, resp.Suggestion)

		// No identity: the source contributes nothing rather than matching.
		// The negated form tells "skipped" apart from "matched nothing": a
		// traversal answered for a wrong or empty id would suggest a row.
		for _, cond := range []string{
			"related(entity, 'implements', { id = current_user.id })",
			"not related(entity, 'implements', { id = current_user.id })",
		} {
			withNextActions(t, app, oneBand, map[string]dataentryconfig.NextActionSource{
				"s": {Band: "b", Query: "type:ticket", Suggest: "{id}", Condition: cond},
			})
			resp, status = getNextAction(principalCtx(principal.Unknown), t, app)
			require.Equal(t, http.StatusOK, status, cond)
			require.Nil(t, resp.Suggestion, cond)
		}
	})

	// Under a reader who cannot read features, the feature does not exist for
	// them: no ticket implements one, so the suggestion must not rest on it.
	t.Run("reader gate", func(t *testing.T) {
		d := mustNewACL(t, &acl.Policy{
			Roles:       map[string]acl.RoleDef{"reader": {Read: []string{"ticket"}}},
			Assignments: map[string]string{"alice": "reader"},
		}, app.store)
		app.acl = d
		withNextActions(t, app, oneBand, map[string]dataentryconfig.NextActionSource{
			"s": {Band: "b", Query: "type:ticket", Condition: "related(entity, 'implements')", Suggest: "{id}"},
		})
		resp, status := getNextAction(gateCtxFor(principalCtx("alice"), t, d), t, app)
		require.Equal(t, http.StatusOK, status)
		require.Nil(t, resp.Suggestion)
	})
}
