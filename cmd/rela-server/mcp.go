package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/Sourcehaven-BV/rela/internal/appbuild"
	"github.com/Sourcehaven-BV/rela/internal/dataentry"
	"github.com/Sourcehaven-BV/rela/internal/lua"
	relamcp "github.com/Sourcehaven-BV/rela/internal/mcp"
	"github.com/Sourcehaven-BV/rela/internal/principal"
)

// mcpServerVersion is the version rela reports in the MCP `initialize` /
// `server/discover` response. It is a distinct var from the CLI's Version
// (which rela-server does not import — it has no cobra/kong surface) and is
// overridable at build time via
// -ldflags "-X main.mcpServerVersion=$(git describe --tags)".
//
// It is informational: MCP clients log it, they do not gate on it. "dev" in
// an unstamped build is honest rather than a wrong number.
var mcpServerVersion = "dev"

// wireIdentityAndMCP installs the identity source and, when -mcp is set, the
// remote MCP endpoint. Extracted from main so the two stay in their required
// order and main stays readable.
//
// **The order is load-bearing.** [wireRemoteMCP] refuses to enable MCP unless
// a JWT gate is installed, so it must run AFTER wirePrincipalResolvers. Both
// exit on failure rather than degrading: an invalid identity configuration
// must not boot, and an operator who asked for MCP must not get a server
// silently missing it.
func wireIdentityAndMCP(app *dataentry.App, svc *appbuild.Services, f *serverFlags) {
	mode, modeErr := validateIdentityFlags(f, os.Getenv(dataentry.EnvDataEntryUserVar))
	if modeErr != nil {
		slog.Error("invalid identity configuration", "error", modeErr)
		os.Exit(1)
	}

	// Build the signed-JWT verifier once (nil when JWT identity is disabled) and
	// share it between the identity gate and the webhook receiver so the JWKS
	// is fetched a single time.
	idv := buildIdentityVerifier(context.Background(), f)
	wirePrincipalResolvers(app, f, idv, mode)
	wireWebhookReceiver(app, f, idv)

	if err := wireRemoteMCP(app, svc, f); err != nil {
		slog.Error("failed to enable remote MCP", "error", err)
		os.Exit(1)
	}
}

// wireRemoteMCP enables the HTTP MCP endpoint when -mcp is set.
//
// It returns an error rather than exiting so the caller owns the exit, the
// same shape as validateIdentityFlags. A failure here MUST stop startup: the
// operator asked for MCP, and booting without it would serve a server that
// silently lacks the feature they enabled.
//
// **Identity comes from the request, not from here.** Unlike `rela mcp`
// (stdio), which stamps one process-wide system principal, the remote server
// serves many callers. The MCP server's principal middleware preserves an
// identity already on the ctx, and the transport hands it the *http.Request
// ctx that `requireVerifiedJWT` has stamped — so `WithPrincipal` below is
// only the fallback for a request that somehow carries none. It is a real,
// non-zero system principal (NewServer requires one) rather than a
// placeholder, so an unattributed write is recorded as the server itself
// rather than as a guessed user.
//
// **Reads are ACL-gated.** Every read handle comes from
// [appbuild.Services.GatedReads] via [remoteMCPDeps], which resolves the ctx
// principal per call.
// This is the opposite of the stdio wiring's deliberate NopACL: there the
// filesystem is the trust boundary (anyone who can run `rela mcp` can edit
// the files directly), so a gate would defend nothing. A remote caller has no
// filesystem access, so the ACL is the ONLY boundary.
func wireRemoteMCP(app *dataentry.App, svc *appbuild.Services, f *serverFlags) error {
	if !f.remoteMCP {
		return nil
	}

	factory := func(host dataentry.MCPHost) (http.Handler, error) {
		srv, err := relamcp.NewServer(remoteMCPDeps(svc, host), mcpServerVersion,
			relamcp.WithPrincipal(principal.Principal{
				User: principal.SystemUser(),
				Tool: principal.ToolMCP,
			}))
		if err != nil {
			return nil, err
		}

		return srv.HTTPHandler(), nil
	}

	return app.SetRemoteMCP(factory)
}

// remoteAttachmentDeps wires the MCP attachment tools onto the web upload
// path's policy: the App's live schema (so an operator's edit to `accept:`,
// `scan:` or `max_attachment_bytes` applies to MCP uploads immediately), its
// command runner, and its write mutex. The snapshot is rebuilt per tool call,
// which costs one struct allocation.
func remoteAttachmentDeps(svc *appbuild.Services, host dataentry.MCPHost) relamcp.AttachmentDeps {
	return relamcp.AttachmentDeps{
		Snapshot: func() (relamcp.AttachmentSnapshot, error) {
			meta, limit := host.AttachmentPolicy()
			return relamcp.NewAttachmentSnapshot(
				svc.Store(), svc.EntityManager(), meta, host.AttachmentRunner, limit)
		},
		Authorizer: svc.ACL(),
		Audit:      svc.Audit(),
		WriteLock:  host.WriteLock,
	}
}

// remoteMCPDeps builds the MCP dependencies for the remote endpoint. Every
// read handle, including search and the Lua tools' reads, comes from
// [appbuild.Services.GatedReads]. The Lua tools get no elevated handle, so a
// remote caller cannot use rela.bypass_acl.
//
// LuaCache is nil on purpose. The script engine's cache is shared with
// data-entry and keyed by script path, not by principal, so a memoized gated
// read by one caller would be served to the next.
func remoteMCPDeps(svc *appbuild.Services, host dataentry.MCPHost) relamcp.Deps {
	reads := svc.GatedReads()
	return relamcp.Deps{
		Store:         reads.Reader,
		Meta:          svc.Meta(),
		Tracer:        reads.Tracer,
		Searcher:      reads.Searcher,
		Validator:     reads.Validator,
		EntityManager: svc.EntityManager(),
		Config:        svc.Config(),
		LuaWriteDeps: lua.WriteDeps{
			ReadDeps:      reads.LuaReads,
			EntityManager: svc.EntityManager(),
		},
		Watcher:     noopWatcher{},
		ProjectRoot: svc.Paths().Root,
		Attachments: remoteAttachmentDeps(svc, host),
	}
}

// noopWatcher satisfies [relamcp.Watcher] for the HTTP transport, which has
// no use for file-change callbacks.
//
// Stateless streamable HTTP cannot deliver `resources/list_changed`: there is
// no session to push to, and server→client requests are rejected outright.
// Remote clients re-read on demand instead, which is correct because every
// read goes to the store. Starting a real filesystem watcher here would burn
// an inotify handle to feed a callback whose notification can never be sent.
//
// This is a no-op, NOT a silent degradation of a working feature — the
// notification does not exist on this transport in the first place.
type noopWatcher struct{}

func (noopWatcher) Start(func()) error { return nil }
func (noopWatcher) Stop()              {}
func (noopWatcher) Pause()             {}
func (noopWatcher) Resume()            {}
