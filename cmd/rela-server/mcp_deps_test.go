//go:build !postgres

package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Sourcehaven-BV/rela/internal/appbuild"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/dataentry"
	"github.com/Sourcehaven-BV/rela/internal/project"
	"github.com/Sourcehaven-BV/rela/internal/script"
	"github.com/Sourcehaven-BV/rela/internal/storage"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

const depsMetamodel = `version: "1.0"
entities:
  ticket:
    label: Ticket
    id_prefix: "TKT-"
    id_type: sequential
    properties:
      title:
        type: string
relations: {}
`

const depsPolicy = `roles:
  viewer:
    read: [ticket]
assignments:
  alice: viewer
`

// TestRemoteMCPDeps_UsesGatedHandles (TKT-4QSZ8Y) pins the wiring: under a
// policy, every read handle the remote MCP server gets is gated. The
// per-handle behavior is tested in internal/mcp and internal/appbuild; this
// test catches a wiring site that hands out a raw handle instead. It also
// pins that the remote server offers no Lua tools (BUG-RIJR6R).
func TestRemoteMCPDeps_UsesGatedHandles(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"entities", "relations", filepath.Join(".rela", "audit")} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"metamodel.yaml": depsMetamodel, "acl.yaml": depsPolicy} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fs := storage.NewSafeFS(storage.NewOsFS())
	paths, err := project.Discover(root, fs)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	svc, err := appbuild.New(appbuild.Config{
		FS: fs, Paths: paths, ScriptEngine: script.NewEngine(), Audit: audit.Nop{},
	})
	if err != nil {
		t.Fatalf("appbuild.New: %v", err)
	}
	defer svc.Close()

	deps := remoteMCPDeps(svc, dataentry.MCPHost{})

	if _, ok := deps.Searcher.(*visibility.Searcher); !ok {
		t.Errorf("Searcher = %T, want *visibility.Searcher", deps.Searcher)
	}
	if deps.Store == svc.Store() {
		t.Error("Store is the raw store")
	}
	if deps.Tracer == svc.Tracer() {
		t.Error("Tracer is the raw tracer")
	}
	if deps.LuaWriteDeps.EntityManager != nil || deps.LuaWriteDeps.VisibleReader != nil {
		t.Error("LuaWriteDeps is set; the remote server has no Lua tools")
	}
	if deps.LuaCache != nil {
		t.Error("LuaCache is set; the remote server has no Lua tools")
	}

	assertNoLuaTools(t, svc)
}

// assertNoLuaTools builds the server through the production constructor,
// serves it over HTTP and checks that no lua_* tool is listed or callable
// (BUG-RIJR6R).
func assertNoLuaTools(t *testing.T, svc *appbuild.Services) {
	t.Helper()
	ctx := context.Background()

	srv, err := newRemoteMCPServer(svc, dataentry.MCPHost{WriteLock: &sync.Mutex{}})
	if err != nil {
		t.Fatalf("newRemoteMCPServer: %v", err)
	}
	ts := httptest.NewServer(srv.HTTPHandler())
	t.Cleanup(ts.Close)

	client := mcpgo.NewClient(&mcpgo.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, &mcpgo.StreamableClientTransport{
		Endpoint: ts.URL, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("tools/list is empty")
	}
	for _, tool := range tools.Tools {
		if strings.HasPrefix(tool.Name, "lua_") {
			t.Errorf("remote MCP lists %s", tool.Name)
		}
	}

	res, err := cs.CallTool(ctx, &mcpgo.CallToolParams{
		Name: "lua_eval", Arguments: map[string]any{"code": "return 1"},
	})
	if err == nil && !res.IsError {
		t.Error("remote MCP ran lua_eval")
	}
}
