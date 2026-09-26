package mcp

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"strings"
	"testing"

	mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/appbuild/appbuildtest"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/lua"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// These tests pin the read-gating contract MCP must satisfy before it can be
// served over a network (TKT-UIR41P, AC #3).
//
// `rela mcp` itself wires acl.NopACL — the filesystem is the trust boundary
// for a local stdio transport, so a gate there would defend nothing. What
// these prove is the property that makes a *networked* wiring safe: handlers
// read exclusively through Deps.Store / Deps.Tracer, so supplying gated
// handles at the wiring site gates every read surface at once, with no
// per-handler opt-in. Reintroduce a raw store handle in a handler and these
// fail.
//
// Deliberately built through the PRODUCTION seam — appbuild.Services.GatedReads()
// — rather than a hand-composed visibility stack, so the test exercises the
// wiring a real deployment would use.
//
// Covered: tools (show/list), the relations block show_entity embeds, the
// rela:// resource path, and the trace pre-flight probe — the surfaces the
// design review found ungated (RR-CFFL52, RR-NSUN49, RR-FTJUUE).

const (
	visibleID   = "TKT-001"
	hiddenID    = "FEAT-SECRET"
	hiddenTitle = "classified feature title"
)

// gatedServer builds an MCP server whose read handles come from
// Services.GatedReads() under a policy where `alice` (viewer) may read
// tickets but not features. The returned ctx carries that principal; the
// gate resolves identity from ctx per call, exactly as in production.
func gatedServer(t *testing.T) (*Server, context.Context) {
	t.Helper()

	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket": {
				Label: "Ticket", IDPrefix: "TKT",
				Properties: map[string]metamodel.PropertyDef{"title": {Type: "string"}},
			},
			"feature": {
				Label: "Feature", IDPrefix: "FEAT",
				Properties: map[string]metamodel.PropertyDef{"title": {Type: "string"}},
			},
		},
		Relations: map[string]metamodel.RelationDef{
			"relates-to": {Label: "relates to", From: []string{"ticket"}, To: []string{"feature"}},
		},
	}

	st := memstore.New()
	ctx := context.Background()
	for _, e := range []*entity.Entity{
		newEntity(visibleID, "ticket", "visible ticket"),
		newEntity(hiddenID, "feature", hiddenTitle),
	} {
		if err := st.CreateEntity(ctx, e); err != nil {
			t.Fatalf("seed %s: %v", e.ID, err)
		}
	}
	if _, err := st.CreateRelation(ctx, visibleID, "relates-to", hiddenID, nil); err != nil {
		t.Fatalf("seed relation: %v", err)
	}

	d, err := acl.NewDeclarative(&acl.Policy{
		Roles: map[string]acl.RoleDef{
			"viewer": {Read: []string{"ticket"}},
			// editor may change tickets but, like viewer, not read features.
			"editor": {Read: []string{"ticket"}, Update: []string{"ticket"}, Delete: []string{"ticket"}},
		},
		Assignments: map[string]string{"alice": "viewer", "carol": "editor"},
	}, acl.NewStoreGraph(st), st)
	if err != nil {
		t.Fatalf("acl.NewDeclarative: %v", err)
	}

	svc := appbuildtest.New(meta,
		appbuildtest.WithStore(st),
		appbuildtest.WithDeclarative(d),
	)
	t.Cleanup(func() { _ = svc.Close() })

	reads := svc.GatedReads()
	deps := Deps{
		Store:         reads.Reader,
		Meta:          meta,
		Tracer:        reads.Tracer,
		Searcher:      reads.Searcher,
		Validator:     reads.Validator,
		EntityManager: svc.EntityManager(),
		Config:        svc.Config(),
		LuaWriteDeps:  lua.WriteDeps{ReadDeps: reads.LuaReads, EntityManager: svc.EntityManager()},
		Watcher:       nopWatcher{},
		ProjectRoot:   t.TempDir(),
		Attachments:   testAttachmentDeps(t, svc, meta, audit.Nop{}),
	}

	srv := &Server{logger: slog.New(slog.DiscardHandler)}
	setDeps(srv, deps)
	return srv, principal.With(ctx, principal.Principal{User: "alice", Tool: principal.ToolMCP})
}

func newEntity(id, typ, title string) *entity.Entity {
	e := entity.New(id, typ)
	e.SetString("title", title)
	return e
}

// readResourceReq builds a resource-read request for uri.
func readResourceReq(uri string) *mcpgo.ReadResourceRequest {
	return &mcpgo.ReadResourceRequest{Params: &mcpgo.ReadResourceParams{URI: uri}}
}

// TestACL_ShowEntity_HiddenIsIndistinguishableFromAbsent is the core
// row-level guarantee: a denied entity and a nonexistent one must produce the
// SAME response, so the tool is not an existence oracle.
func TestACL_ShowEntity_HiddenIsIndistinguishableFromAbsent(t *testing.T) {
	t.Parallel()
	s, ctx := gatedServer(t)

	hidden, err := s.handleShowEntity(ctx, makeToolRequest(map[string]any{"id": hiddenID}))
	if err != nil {
		t.Fatalf("show_entity(hidden): %v", err)
	}
	absent, err := s.handleShowEntity(ctx, makeToolRequest(map[string]any{"id": "NO-SUCH-ID"}))
	if err != nil {
		t.Fatalf("show_entity(absent): %v", err)
	}

	if !isErrorResult(hidden) {
		t.Fatalf("hidden entity returned success: %s", getResultText(t, hidden))
	}
	hiddenText, absentText := getResultText(t, hidden), getResultText(t, absent)
	if strings.Contains(hiddenText, hiddenTitle) {
		t.Errorf("LEAK: hidden title in show_entity response: %s", hiddenText)
	}
	// The messages differ only by the id the caller supplied; normalise that
	// away and they must be identical, or the difference IS the oracle.
	if norm(hiddenText, hiddenID) != norm(absentText, "NO-SUCH-ID") {
		t.Errorf("hidden and absent distinguishable:\n hidden: %s\n absent: %s", hiddenText, absentText)
	}
}

// TestACL_TwoPrincipals_SeeDifferentRows is AC #3 for the remote transport
// (TKT-BDG8U9): ONE server, two callers, each seeing only what their own
// grants permit.
//
// This is the property that makes a shared HTTP endpoint safe, and it is
// stronger than the single-principal tests around it: those would all still
// pass if the server resolved the ACL once at construction and reused that
// verdict for everyone. Here the same *Server answers both callers, so the
// gate must resolve per call from the ctx principal — which is exactly what
// the HTTP transport relies on, since it hands handlers the request ctx.
//
// bob has no assignment in the policy, so he is not merely differently
// privileged — he is unprivileged, and must not see alice's ticket.
func TestACL_TwoPrincipals_SeeDifferentRows(t *testing.T) {
	t.Parallel()
	s, aliceCtx := gatedServer(t)

	// Same server, a different caller on the ctx.
	bobCtx := principal.With(context.Background(),
		principal.Principal{User: "bob", Tool: principal.ToolMCP})

	aliceRes, err := s.handleListEntities(aliceCtx, makeToolRequest(map[string]any{}))
	if err != nil {
		t.Fatalf("list_entities(alice): %v", err)
	}
	bobRes, err := s.handleListEntities(bobCtx, makeToolRequest(map[string]any{}))
	if err != nil {
		t.Fatalf("list_entities(bob): %v", err)
	}

	aliceText, bobText := getResultText(t, aliceRes), getResultText(t, bobRes)

	// alice is a viewer of tickets, so she sees the visible one.
	if !strings.Contains(aliceText, visibleID) {
		t.Errorf("alice cannot see %s, but her role grants read on tickets: %s",
			visibleID, aliceText)
	}
	// bob has no grants at all.
	if strings.Contains(bobText, visibleID) {
		t.Errorf("LEAK: bob has no policy assignment yet sees %s — the ACL was "+
			"resolved once for the server rather than per caller: %s", visibleID, bobText)
	}
	// Neither may see the hidden feature; alice's role covers tickets only.
	if strings.Contains(aliceText, hiddenTitle) || strings.Contains(bobText, hiddenTitle) {
		t.Errorf("LEAK: hidden title visible.\n alice: %s\n bob: %s", aliceText, bobText)
	}
	// The two responses must actually differ, or the test proves nothing
	// about per-caller resolution.
	if aliceText == bobText {
		t.Errorf("both callers got identical results (%s) — identities are not "+
			"being distinguished", aliceText)
	}
}

// TestACL_ListEntities_OmitsHidden pins the list surface.
func TestACL_ListEntities_OmitsHidden(t *testing.T) {
	t.Parallel()
	s, ctx := gatedServer(t)

	result, err := s.handleListEntities(ctx, makeToolRequest(map[string]any{}))
	if err != nil {
		t.Fatalf("list_entities: %v", err)
	}
	text := getResultText(t, result)
	if strings.Contains(text, hiddenID) || strings.Contains(text, hiddenTitle) {
		t.Errorf("LEAK: hidden entity in list_entities: %s", text)
	}
	if !strings.Contains(text, visibleID) {
		t.Errorf("expected the readable ticket to survive, got: %s", text)
	}
}

// TestACL_ShowEntity_WithholdsHiddenNeighbor is RR-CFFL52: show_entity on a
// READABLE entity must not disclose an unreadable neighbor through its
// embedded relations block — neither the title nor, crucially, the id, since
// an id alone answers "does this exist?".
//
// Note there are TWO layers that can drop such an edge: visibility's gated
// ListRelations withholds edges whose far end is hidden, and
// buildStoreRelations independently re-checks the neighbor. This test asserts
// the OUTCOME (nothing about the hidden feature reaches the wire) rather than
// which layer acted — see TestACL_BuildStoreRelations_WithholdsUnreadableEdge
// for one that isolates the second layer.
func TestACL_ShowEntity_WithholdsHiddenNeighbor(t *testing.T) {
	t.Parallel()
	s, ctx := gatedServer(t)

	result, err := s.handleShowEntity(ctx, makeToolRequest(map[string]any{"id": visibleID}))
	if err != nil {
		t.Fatalf("show_entity: %v", err)
	}
	text := getResultText(t, result)
	if !strings.Contains(text, visibleID) {
		t.Fatalf("expected the readable entity itself, got: %s", text)
	}
	if strings.Contains(text, hiddenTitle) {
		t.Errorf("LEAK: hidden neighbor title in relations block: %s", text)
	}
	if strings.Contains(text, hiddenID) {
		t.Errorf("LEAK: hidden neighbor id in relations block: %s", text)
	}
}

// TestACL_BuildStoreRelations_WithholdsUnreadableEdge isolates the second
// layer. It feeds buildStoreRelations a reader whose ListRelations DOES yield
// an edge to an unreadable entity — the situation a future refactor, a
// different backend, or a bug in the upstream gate could produce — and
// asserts the edge is still withheld.
//
// Without this, TestACL_ShowEntity_WithholdsHiddenNeighbor passes vacuously:
// visibility's ListRelations already drops the edge, so reverting
// buildStoreRelations to the leaky "emit id, drop title" form does not fail
// it. Verified by reverting the fix and watching this test — and only this
// test — go red.
func TestACL_BuildStoreRelations_WithholdsUnreadableEdge(t *testing.T) {
	t.Parallel()

	st := memstore.New()
	ctx := context.Background()
	if err := st.CreateEntity(ctx, newEntity(visibleID, "ticket", "visible ticket")); err != nil {
		t.Fatalf("seed visible: %v", err)
	}
	if err := st.CreateEntity(ctx, newEntity(hiddenID, "feature", hiddenTitle)); err != nil {
		t.Fatalf("seed hidden: %v", err)
	}
	if _, err := st.CreateRelation(ctx, visibleID, "relates-to", hiddenID, nil); err != nil {
		t.Fatalf("seed relation: %v", err)
	}

	rels := buildStoreRelations(ctx, visibleID, denyEntityReader{raw: st, deny: hiddenID})
	if rels == nil {
		return // withheld entirely — correct
	}
	for relType, targets := range rels.Outgoing {
		for _, target := range targets {
			if target.ID == hiddenID || strings.Contains(target.Title, hiddenTitle) {
				t.Errorf("LEAK: unreadable neighbor survived in %q: %+v", relType, target)
			}
		}
	}
}

// denyEntityReader lists every relation (as an ungated backend would) but
// refuses GetEntity for one id, isolating buildStoreRelations' own check.
type denyEntityReader struct {
	raw  *memstore.MemStore
	deny string
}

func (d denyEntityReader) GetEntity(ctx context.Context, id string) (*entity.Entity, error) {
	if id == d.deny {
		return nil, errDenied
	}
	return d.raw.GetEntity(ctx, id)
}

func (d denyEntityReader) ListEntities(
	ctx context.Context, q store.EntityQuery,
) iter.Seq2[*entity.Entity, error] {
	return d.raw.ListEntities(ctx, q)
}

func (d denyEntityReader) ListRelations(
	ctx context.Context, q store.RelationQuery,
) iter.Seq2[*entity.Relation, error] {
	return d.raw.ListRelations(ctx, q)
}

func (d denyEntityReader) GetRelation(
	ctx context.Context, from, relType, to string,
) (*entity.Relation, error) {
	return d.raw.GetRelation(ctx, from, relType, to)
}

func (d denyEntityReader) CountEntities(ctx context.Context, q store.EntityQuery) (int, error) {
	return d.raw.CountEntities(ctx, q)
}

func (d denyEntityReader) CountRelations(ctx context.Context, q store.RelationQuery) (int, error) {
	return d.raw.CountRelations(ctx, q)
}

var errDenied = errors.New("denied")

// TestACL_Resources_AreGated is RR-CFFL52's other half: the rela:// resource
// URIs are a parallel read path to the same rows and must gate identically.
func TestACL_Resources_AreGated(t *testing.T) {
	t.Parallel()
	s, ctx := gatedServer(t)

	_, err := group(s, selSchemaRes).handleReadEntity(ctx, readResourceReq("rela://entity/feature/"+hiddenID))
	if err == nil {
		t.Error("LEAK: resource read of a hidden entity succeeded")
	} else if strings.Contains(err.Error(), hiddenTitle) {
		t.Errorf("LEAK: hidden title in resource error: %v", err)
	}

	// A readable entity still works, so the gate is not simply refusing all.
	if _, err := group(s, selSchemaRes).handleReadEntity(ctx, readResourceReq("rela://entity/ticket/"+visibleID)); err != nil {
		t.Errorf("readable entity denied through resource path: %v", err)
	}
}

// TestACL_Trace_HiddenRootIsNotAnOracle is RR-FTJUUE: the pre-flight
// existence probe in trace_from must run through the gated reader, so a
// hidden root reports what an absent one does.
func TestACL_Trace_HiddenRootIsNotAnOracle(t *testing.T) {
	t.Parallel()
	s, ctx := gatedServer(t)

	hidden, err := group(s, selTrace).handleTraceFrom(ctx, makeToolRequest(map[string]any{"id": hiddenID}))
	if err != nil {
		t.Fatalf("trace_from(hidden): %v", err)
	}
	absent, err := group(s, selTrace).handleTraceFrom(ctx, makeToolRequest(map[string]any{"id": "NO-SUCH-ID"}))
	if err != nil {
		t.Fatalf("trace_from(absent): %v", err)
	}
	if !isErrorResult(hidden) {
		t.Fatalf("trace_from on a hidden root succeeded: %s", getResultText(t, hidden))
	}
	if norm(getResultText(t, hidden), hiddenID) != norm(getResultText(t, absent), "NO-SUCH-ID") {
		t.Errorf("hidden and absent roots distinguishable:\n hidden: %s\n absent: %s",
			getResultText(t, hidden), getResultText(t, absent))
	}
}

// norm replaces the caller-supplied id with a placeholder so two responses
// that differ only by that id compare equal.
func norm(s, id string) string { return strings.ReplaceAll(s, id, "<ID>") }

// TestACL_SearchEntities_OmitsHidden pins the search tool. The raw index holds
// the hidden feature, so only the gated searcher keeps it out.
func TestACL_SearchEntities_OmitsHidden(t *testing.T) {
	t.Parallel()
	s, ctx := gatedServer(t)

	hidden, err := s.handleSearchEntities(ctx, makeToolRequest(map[string]any{"query": "classified"}))
	if err != nil {
		t.Fatalf("search_entities(hidden): %v", err)
	}
	if text := getResultText(t, hidden); strings.Contains(text, hiddenID) || strings.Contains(text, hiddenTitle) {
		t.Errorf("LEAK: hidden entity in search_entities: %s", text)
	}

	visible, err := s.handleSearchEntities(ctx, makeToolRequest(map[string]any{"query": "visible"}))
	if err != nil {
		t.Fatalf("search_entities(visible): %v", err)
	}
	text := getResultText(t, visible)
	if !strings.Contains(text, visibleID) || !strings.Contains(text, "visible ticket") {
		t.Errorf("expected the readable ticket with its title, got: %s", text)
	}
}

// TestACL_RelationReads_HiddenEndpointIsNotFound pins the relation resource
// and delete_relation. Both address an edge by its endpoint ids, and a hidden
// endpoint must answer like a missing edge.
func TestACL_RelationReads_HiddenEndpointIsNotFound(t *testing.T) {
	t.Parallel()
	s, ctx := gatedServer(t)

	_, err := group(s, selSchemaRes).handleReadRelation(ctx,
		readResourceReq("rela://relation/"+visibleID+"/relates-to/"+hiddenID))
	if err == nil {
		t.Error("LEAK: relation resource with a hidden endpoint succeeded")
	}

	hidden, err := s.handleDeleteRelation(ctx, makeToolRequest(map[string]any{
		"from": visibleID, "type": "relates-to", "to": hiddenID,
	}))
	if err != nil {
		t.Fatalf("delete_relation(hidden): %v", err)
	}
	absent, err := s.handleDeleteRelation(ctx, makeToolRequest(map[string]any{
		"from": visibleID, "type": "relates-to", "to": "NO-SUCH-ID",
	}))
	if err != nil {
		t.Fatalf("delete_relation(absent): %v", err)
	}
	if !isErrorResult(hidden) {
		t.Fatalf("delete_relation with a hidden endpoint succeeded: %s", getResultText(t, hidden))
	}
	if norm(getResultText(t, hidden), hiddenID) != norm(getResultText(t, absent), "NO-SUCH-ID") {
		t.Errorf("hidden and absent endpoints distinguishable:\n hidden: %s\n absent: %s",
			getResultText(t, hidden), getResultText(t, absent))
	}
}

// TestACL_Writes_HiddenIdIsIndistinguishableFromAbsent pins the write tools
// that name an id without reading it first. The write path answers
// "forbidden" for a hidden entity and "not found" for a missing one.
func TestACL_Writes_HiddenIdIsIndistinguishableFromAbsent(t *testing.T) {
	t.Parallel()
	s, ctx := gatedServer(t)

	tests := []struct {
		name string
		call func(id string) (*mcpgo.CallToolResult, error)
	}{
		{"rename_entity dry run", func(id string) (*mcpgo.CallToolResult, error) {
			return s.handleRenameEntity(ctx, makeToolRequest(map[string]any{
				"id": id, "new_id": "FEAT-NEW", "dry_run": true,
			}))
		}},
		{"create_relation target", func(id string) (*mcpgo.CallToolResult, error) {
			return s.handleCreateRelation(ctx, makeToolRequest(map[string]any{
				"from": visibleID, "type": "relates-to", "to": id,
			}))
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hidden, err := tc.call(hiddenID)
			if err != nil {
				t.Fatalf("hidden: %v", err)
			}
			absent, err := tc.call("NO-SUCH-ID")
			if err != nil {
				t.Fatalf("absent: %v", err)
			}
			if !isErrorResult(hidden) {
				t.Fatalf("write naming a hidden id succeeded: %s", getResultText(t, hidden))
			}
			if norm(getResultText(t, hidden), hiddenID) != norm(getResultText(t, absent), "NO-SUCH-ID") {
				t.Errorf("hidden and absent distinguishable:\n hidden: %s\n absent: %s",
					getResultText(t, hidden), getResultText(t, absent))
			}
		})
	}
}

// TestACL_LuaEval_ReadsAreGated pins the Lua tools. Before TKT-4QSZ8Y the
// runtime read through visibility.Unrestricted, so a remote caller could read
// the whole graph from lua_eval.
func TestACL_LuaEval_ReadsAreGated(t *testing.T) {
	t.Parallel()
	s, ctx := gatedServer(t)

	code := `rela.output({
		hidden = rela.get_entity("` + hiddenID + `"),
		visible = rela.get_entity("` + visibleID + `"),
		features = rela.list_entities("feature"),
		found = rela.search("classified"),
		trace = rela.trace_from("` + visibleID + `"),
	})`
	res, err := group(s, selLua).handleLuaEval(ctx, makeToolRequest(map[string]any{"code": code}))
	if err != nil {
		t.Fatalf("lua_eval: %v", err)
	}
	text := getResultText(t, res)
	if isErrorResult(res) {
		t.Fatalf("lua_eval failed: %s", text)
	}
	if strings.Contains(text, hiddenID) || strings.Contains(text, hiddenTitle) {
		t.Errorf("LEAK: hidden entity reached lua_eval: %s", text)
	}
	if !strings.Contains(text, "visible ticket") {
		t.Errorf("readable ticket missing, so the test proves nothing: %s", text)
	}
}

// TestACL_LuaEval_WritesNamingHiddenIds pins the Lua write bindings, which
// otherwise answer "forbidden" for a hidden id and "not found" for a missing
// one.
func TestACL_LuaEval_WritesNamingHiddenIds(t *testing.T) {
	t.Parallel()
	s, ctx := gatedServer(t)

	for _, call := range []string{
		`rela.update_entity("%s", {title = "x"})`,
		`rela.delete_entity("%s")`,
		`rela.create_relation("` + visibleID + `", "relates-to", "%s")`,
	} {
		run := func(id string) string {
			code := strings.ReplaceAll(call, "%s", id)
			res, err := group(s, selLua).handleLuaEval(ctx, makeToolRequest(map[string]any{"code": code}))
			if err != nil {
				t.Fatalf("lua_eval(%s): %v", code, err)
			}
			if !isErrorResult(res) {
				t.Fatalf("lua_eval(%s) succeeded: %s", code, getResultText(t, res))
			}
			return getResultText(t, res)
		}
		hidden, absent := run(hiddenID), run("NO-SUCH-ID")
		if norm(hidden, hiddenID) != norm(absent, "NO-SUCH-ID") {
			t.Errorf("%s: hidden and absent distinguishable:\n hidden: %s\n absent: %s", call, hidden, absent)
		}
	}
}

// TestACL_WriteCounts_OmitHiddenEdges (RR-MEXNEZ): the relation counts that
// rename and delete report cover visible edges only. The fixture's visible
// ticket has one edge, to the hidden feature, so a raw count would say 1.
func TestACL_WriteCounts_OmitHiddenEdges(t *testing.T) {
	t.Parallel()
	s, _ := gatedServer(t)
	ctx := principal.With(context.Background(), principal.Principal{User: "carol", Tool: principal.ToolMCP})

	res, err := s.handleRenameEntity(ctx, makeToolRequest(map[string]any{
		"id": visibleID, "new_id": "TKT-NEW", "dry_run": true,
	}))
	if err != nil || isErrorResult(res) {
		t.Fatalf("rename dry run: %v %s", err, getResultText(t, res))
	}
	if got := getResultText(t, res); !strings.Contains(got, "(0 relations updated)") {
		t.Errorf("rename reports the hidden edge: %s", got)
	}

	res, err = s.handleDeleteEntity(ctx, makeToolRequest(map[string]any{"id": visibleID, "cascade": true}))
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := getResultText(t, res); got != "Deleted "+visibleID {
		t.Errorf("delete reply = %q, want no relation count", got)
	}
}
