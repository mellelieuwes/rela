package dataentry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/metamodel"
)

// schemaBody fetches path through the real router and returns the raw body.
func schemaBody(t *testing.T, app *App, path string) []byte {
	t.Helper()
	rec := httptest.NewRecorder()
	app.NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return rec.Body.Bytes()
}

// TestSchemaPacts_ServedPerType pins the wire shape of a type's pacts on
// both schema surfaces: sorted by system, empty lists as [], and the agent
// brief (instructions) kept off the wire.
func TestSchemaPacts_ServedPerType(t *testing.T) {
	app := newTestAppV1(t)
	td := app.State().Meta.Entities["ticket"]
	td.Pacts = map[string]metamodel.PactDef{
		"gh": {Scope: "https://github.com/o/r", Theirs: []string{metamodel.PactAllFields}},
		"basecamp": {
			Scope:        "https://app.basecamp.com/1/buckets/2/todolists/3",
			Theirs:       []string{"status", "body"},
			Shared:       []string{"title"},
			Propose:      []string{"status"},
			Instructions: "secret sauce for the agent",
		},
	}
	app.State().Meta.Entities["ticket"] = td

	want := []map[string]any{
		{
			"system":  "basecamp",
			"scope":   "https://app.basecamp.com/1/buckets/2/todolists/3",
			"theirs":  []any{"body", "status"},
			"shared":  []any{"title"},
			"propose": []any{"status"},
		},
		{
			"system":  "gh",
			"scope":   "https://github.com/o/r",
			"theirs":  []any{"*"},
			"shared":  []any{},
			"propose": []any{},
		},
	}

	var schema struct {
		Entities map[string]map[string]json.RawMessage `json:"entities"`
	}
	require.NoError(t, json.Unmarshal(schemaBody(t, app, "/api/v1/_schema"), &schema))
	var got []map[string]any
	require.NoError(t, json.Unmarshal(schema.Entities["ticket"]["pacts"], &got))
	require.Equal(t, want, got)
	_, present := schema.Entities["feature"]["pacts"]
	require.False(t, present, "a type without pacts must not carry the key")

	var single map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(schemaBody(t, app, "/api/v1/_schema/types/ticket"), &single))
	require.JSONEq(t, string(schema.Entities["ticket"]["pacts"]), string(single["pacts"]),
		"both schema surfaces render pacts through toV1EntityType")
}

// TestSchemaPacts_AbsentWithoutPacts is the backward-compatibility floor: a
// project declaring no pacts serves no `pacts` key on any type.
func TestSchemaPacts_AbsentWithoutPacts(t *testing.T) {
	app := newTestAppV1(t)

	var schema struct {
		Entities map[string]map[string]json.RawMessage `json:"entities"`
	}
	require.NoError(t, json.Unmarshal(schemaBody(t, app, "/api/v1/_schema"), &schema))
	require.NotEmpty(t, schema.Entities)
	for name, et := range schema.Entities {
		_, present := et["pacts"]
		require.False(t, present, "type %q leaked a pacts key with no pacts declared", name)
	}
}
