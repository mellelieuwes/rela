package mcp

import (
	"context"
	"log/slog"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// scopedTaakMeta declares a type whose DEFAULT query scope hides archived
// rows. `~=` deliberately does not lower to a store predicate, so a surface
// that honored the scope would have to filter in Go — there is no way for the
// row to vanish by accident of the store query.
func scopedTaakMeta() *metamodel.Metamodel {
	return &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"taak": {
				Label:      "Taak",
				IDPrefixes: []string{"TAAK-"},
				Properties: map[string]metamodel.PropertyDef{
					"title":  {Type: "string"},
					"status": {Type: "string"},
				},
				QueryScopes: map[string]string{
					"default": "entity.status ~= 'gearchiveerd'",
					"archief": "entity.status == 'gearchiveerd'",
				},
			},
		},
	}
}

// TestHandleListEntities_IgnoresQueryScopes is AC6 for MCP (TKT-EVR2TU).
//
// A script author calling list_entities asks what the graph contains. If the
// default scope applied here, a tool would silently operate on a subset, and
// the author would have no way to tell from the call site — the narrowing is
// declared in schema.yaml, nowhere near the script. The user's framing when
// this was decided: applying it by default is "too surprising for script
// authors".
//
// Opting IN is AC7 and is a separate decision; this test pins only that the
// default does not arrive uninvited.
func TestHandleListEntities_IgnoresQueryScopes(t *testing.T) {
	t.Parallel()

	meta := scopedTaakMeta()
	st := memstore.New()
	for _, tc := range []struct{ id, status string }{
		{"TAAK-1", "todo"},
		{"TAAK-2", "gearchiveerd"},
	} {
		e := entity.New(tc.id, "taak")
		e.SetString("title", tc.id)
		e.SetString("status", tc.status)
		if err := st.CreateEntity(context.Background(), e); err != nil {
			t.Fatalf("seed %s: %v", tc.id, err)
		}
	}

	srv := &Server{logger: slog.New(slog.DiscardHandler)}
	setDeps(srv, newTestDeps(t, meta, st))

	result, err := srv.handleListEntities(context.Background(),
		makeToolRequest(map[string]any{"type": "taak"}))
	if err != nil {
		t.Fatalf("handleListEntities: %v", err)
	}
	ids := map[string]bool{}
	for _, e := range decodeEntityPage(t, getResultText(t, result)).Entities {
		ids[e.ID] = true
	}
	if !ids["TAAK-2"] {
		t.Fatalf("MCP list_entities dropped the archived row, so the schema's default "+
			"query scope reached a non-SPA surface. Got: %v", ids)
	}
	if !ids["TAAK-1"] {
		t.Fatalf("control row missing — the fixture, not the scope, is wrong: %v", ids)
	}
}
