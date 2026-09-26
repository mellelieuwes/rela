//go:build !postgres

package main

import (
	"os"
	"path/filepath"
	"testing"

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
// policy, every read handle the remote MCP server gets is gated, and the Lua
// tools get no elevated handle and no shared cache. The per-handle behavior is
// tested in internal/mcp and internal/appbuild; this test catches a wiring
// site that hands out a raw handle instead.
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
	lw := deps.LuaWriteDeps
	if _, ok := lw.VisibleReader.(*visibility.UnrestrictedReader); ok || lw.VisibleReader == nil {
		t.Errorf("Lua VisibleReader = %T, want a gated reader", lw.VisibleReader)
	}
	if _, ok := lw.Searcher.(*visibility.Searcher); !ok {
		t.Errorf("Lua Searcher = %T, want *visibility.Searcher", lw.Searcher)
	}
	if lw.ElevatedReader != nil || lw.ElevatedManager != nil {
		t.Error("Lua tools got an elevated handle; a remote caller could use rela.bypass_acl")
	}
	if deps.LuaCache != nil {
		t.Error("LuaCache is set; the shared cache is not keyed by principal")
	}
}
