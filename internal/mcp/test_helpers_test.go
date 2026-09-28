package mcp

import (
	"sync"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/appbuild"
	"github.com/Sourcehaven-BV/rela/internal/appbuild/appbuildtest"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// newTestDeps assembles the [Deps] the server consumes from an
// appbuildtest services bundle, so the test wiring is single-sourced
// with the rest of the repo instead of hand-mirroring the production
// cli.mcpServices graph (TKT-R2KBG6 — the previous hand-rolled wiring
// is where the nil-templater booby trap of TKT-TLQ94B lived).
//
// The bundle wires an in-memory bleve backend and backfills it from
// the caller-supplied store, so entities seeded before construction
// are searchable; writes during the test do not reach the index
// (same semantics as the previous fixture).
func newTestDeps(t *testing.T, meta *metamodel.Metamodel, st store.Store) Deps {
	t.Helper()

	svc := appbuildtest.New(meta, appbuildtest.WithStore(st))
	t.Cleanup(func() { _ = svc.Close() })

	return Deps{
		Store:         svc.Store(),
		Traversals:    svc.GatedReads().Traversals,
		Meta:          meta,
		Tracer:        svc.Tracer(),
		Searcher:      svc.Searcher(),
		Validator:     svc.Validator(),
		EntityManager: svc.EntityManager(),
		Config:        svc.Config(),
		LuaWriteDeps:  svc.LuaWriteDeps(),
		Watcher:       nopWatcher{},
		// Note: this real empty dir (what lua_run walks) is
		// intentionally distinct from LuaWriteDeps' ProjectRoot, which
		// points at the fixture's in-memory /project. No current test
		// resolves a Lua write relative to that root; align the two if
		// one ever does.
		ProjectRoot: t.TempDir(),
		Attachments: testAttachmentDeps(t, svc, meta, audit.Nop{}),
	}
}

// testAttachmentDeps wires the attachment tools over svc the way `rela mcp`
// does (no command runner, store backstop limit), with sink as the audit log.
func testAttachmentDeps(
	t *testing.T, svc *appbuild.Services, meta *metamodel.Metamodel, sink audit.Audit,
) AttachmentDeps {
	t.Helper()
	snap, err := NewAttachmentSnapshot(svc.Store(), svc.EntityManager(), meta, nil, store.MaxAttachmentBytes)
	if err != nil {
		t.Fatalf("NewAttachmentSnapshot: %v", err)
	}
	return AttachmentDeps{
		Snapshot:   func() (AttachmentSnapshot, error) { return snap, nil },
		Authorizer: svc.ACL(),
		Audit:      sink,
		WriteLock:  &sync.Mutex{},
	}
}

// nopWatcher satisfies the narrow watcher interface MCP consumes;
// tests never exercise file watching.
type nopWatcher struct{}

func (nopWatcher) Start(func()) error { return nil }
func (nopWatcher) Stop()              {}
func (nopWatcher) Pause()             {}
func (nopWatcher) Resume()            {}
