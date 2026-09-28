package mcp

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/principal"
)

// metaWithRisk returns the standard fixture metamodel plus a `risk` entity
// type, standing in for the operator adding a type to schema.yaml mid-session.
func metaWithRisk() *metamodel.Metamodel {
	return &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"requirement": {
				Label:    "Requirement",
				IDPrefix: "REQ",
				Properties: map[string]metamodel.PropertyDef{
					"title":    {Type: "string", Required: true},
					"status":   {Type: "string"},
					"priority": {Type: "string"},
				},
			},
			"decision": {
				Label:    "Decision",
				IDPrefix: "DEC",
				Properties: map[string]metamodel.PropertyDef{
					"title":  {Type: "string", Required: true},
					"status": {Type: "string"},
				},
			},
			"risk": {
				Label:    "Risk",
				IDPrefix: "RSK",
				Properties: map[string]metamodel.PropertyDef{
					"title": {Type: "string", Required: true},
				},
			},
		},
		Relations: map[string]metamodel.RelationDef{
			"addresses": {Label: "addresses", From: []string{"decision"}, To: []string{"requirement"}},
		},
	}
}

// TestReloadDeps_NewTypeVisibleToSchemaTools is the headline behavior of
// TKT-NU247U: a type added to schema.yaml after startup shows up without a
// server restart.
func TestReloadDeps_NewTypeVisibleToSchemaTools(t *testing.T) {
	t.Parallel()
	s, st := makeTestServerWithStore(t)

	before, err := group(s, selSchemaRes).handleSchema(context.Background(), &mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("list types before reload: %v", err)
	}
	if strings.Contains(getResultText(t, before), "risk") {
		t.Fatal("fixture already has a risk type; the test would prove nothing")
	}

	next := newTestDeps(t, metaWithRisk(), st)
	if reloadErr := s.ReloadDeps(next); reloadErr != nil {
		t.Fatalf("ReloadDeps: %v", reloadErr)
	}

	after, err := group(s, selSchemaRes).handleSchema(context.Background(), &mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("list types after reload: %v", err)
	}
	if !strings.Contains(getResultText(t, after), "risk") {
		t.Errorf("reloaded schema does not list the new type:\n%s", getResultText(t, after))
	}
}

// TestReloadDeps_NewTypeIsWritable pins the reason the reload rebuilds the
// whole service stack rather than swapping Deps.Meta alone.
//
// create_entity validates through the entitymanager, which holds its OWN
// metamodel. A reload that refreshed only the read surfaces would leave this
// create failing against the old schema while the schema tool happily advertised
// the new type — a half-updated server, which is harder to diagnose than one
// that never updated at all.
func TestReloadDeps_NewTypeIsWritable(t *testing.T) {
	t.Parallel()
	s, st := makeTestServerWithStore(t)
	ctx := principal.With(context.Background(),
		principal.Principal{User: "tester", Tool: principal.ToolMCP})

	create := makeToolRequest(map[string]any{
		"type":       "risk",
		"properties": map[string]any{"title": "Supplier outage"},
	})

	// The tool reports a domain failure as a result with IsError set, not as
	// a Go error, so assert on the result.
	before, err := s.handleCreateEntity(ctx, create)
	if err != nil {
		t.Fatalf("create before reload: %v", err)
	}
	if !before.IsError {
		t.Fatal("creating an unknown type should fail before the reload")
	}

	if reloadErr := s.ReloadDeps(newTestDeps(t, metaWithRisk(), st)); reloadErr != nil {
		t.Fatalf("ReloadDeps: %v", reloadErr)
	}

	result, err := s.handleCreateEntity(ctx, create)
	if err != nil {
		t.Fatalf("create after reload: %v", err)
	}
	if result.IsError {
		t.Errorf("write path still on the old metamodel: %s", getResultText(t, result))
	}
}

// TestReloadDeps_ReachesRegisteredHandlers guards the trap this change was
// built around.
//
// Handlers used to be registered as method values (`s.trace.handleTrace`),
// which capture the handler group — and through it the metamodel — BY VALUE at
// registration time. A reloaded snapshot could never reach them, and the
// failure was silent: the tool kept working, just against the old schema. This
// asserts a handler obtained the way registerTools obtains one observes the
// reload.
func TestReloadDeps_ReachesRegisteredHandlers(t *testing.T) {
	t.Parallel()
	s, st := makeTestServerWithStore(t)

	// Bound exactly as registerTools binds it, BEFORE the reload.
	registered := bind(s, selSchemaRes, schemaResourceHandler.handleSchema)

	if err := s.ReloadDeps(newTestDeps(t, metaWithRisk(), st)); err != nil {
		t.Fatalf("ReloadDeps: %v", err)
	}

	result, err := registered(context.Background(), &mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("registered handler: %v", err)
	}
	if !strings.Contains(getResultText(t, result), "risk") {
		t.Error("a handler bound before the reload still serves the old metamodel " +
			"— registration is capturing the snapshot by value again")
	}
}

// TestReloadDeps_RejectsInvalidBundleAndKeepsPrevious pins the fail-safe
// direction: a reload driven by a file watcher must never be able to leave a
// running server without a usable metamodel.
func TestReloadDeps_RejectsInvalidBundleAndKeepsPrevious(t *testing.T) {
	t.Parallel()
	s, st := makeTestServerWithStore(t)

	broken := newTestDeps(t, metaWithRisk(), st)
	broken.Meta = nil

	if err := s.ReloadDeps(broken); err == nil {
		t.Fatal("ReloadDeps accepted a Deps with no metamodel")
	}

	// The previous bundle must still be serving.
	if s.deps().Meta == nil {
		t.Fatal("a rejected reload cleared the published metamodel")
	}
	result, err := group(s, selSchemaRes).handleSchema(context.Background(), &mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("list types after rejected reload: %v", err)
	}
	if !strings.Contains(getResultText(t, result), "requirement") {
		t.Error("rejected reload disturbed the previously published schema")
	}
}

// TestNewServer_PublishesSnapshot documents that a server is usable straight
// out of NewServer — the snapshot pointer is never nil on a running server, so
// the accessors need no nil check.
func TestNewServer_PublishesSnapshot(t *testing.T) {
	t.Parallel()
	_, st := makeTestServerWithStore(t)

	srv, err := NewServer(newTestDeps(t, metaWithRisk(), st), "0.0.0",
		WithPrincipal(principal.Principal{User: "t", Tool: principal.ToolMCP}))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.logger = slog.New(slog.DiscardHandler)

	if srv.state.current() == nil {
		t.Fatal("NewServer left the snapshot unpublished")
	}
	if srv.deps().Meta == nil {
		t.Fatal("published snapshot has no metamodel")
	}
}

// TestReloadDeps_ConcurrentWithRequests exercises the publish/read split under
// the race detector: reloads swap the snapshot while requests resolve it.
//
// The point is not the assertions — it is that `go test -race` sees a reader
// and a writer on the same state. The snapshot is published through an
// atomic.Pointer precisely so a request never observes a half-swapped
// (deps, handlers) pair.
func TestReloadDeps_ConcurrentWithRequests(t *testing.T) {
	t.Parallel()
	s, st := makeTestServerWithStore(t)

	// Bound once, up front, the way registerTools binds a real tool.
	listTypes := bind(s, selSchemaRes, schemaResourceHandler.handleSchema)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := range 50 {
			meta := metaWithRisk()
			if i%2 == 0 {
				delete(meta.Entities, "risk")
			}
			if err := s.ReloadDeps(newTestDeps(t, meta, st)); err != nil {
				t.Errorf("ReloadDeps: %v", err)
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
		for range 50 {
			result, err := listTypes(context.Background(), &mcpgo.CallToolRequest{})
			if err != nil {
				t.Errorf("list types: %v", err)
				return
			}
			// Whichever snapshot won, it is a whole one: requirement is in
			// every version of the fixture, so a torn pair would show up as a
			// missing type or a nil-deref rather than a flaky assertion.
			if !strings.Contains(getResultText(t, result), "requirement") {
				t.Error("observed a snapshot without the always-present type")
				return
			}
		}
	}()

	wg.Wait()
}
