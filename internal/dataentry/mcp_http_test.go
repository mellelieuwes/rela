package dataentry

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/principal"
)

// stubMCPFactory stands in for the real wiring's MCP handler. It records the
// principal present on each request ctx, which is what the middleware chain
// has to deliver — the whole point of mounting under `/api/`.
//
// It is a plain http.Handler, not an SDK server: `internal/mcp` is the only
// component permitted to import the MCP go-sdk (arch-lint), and that applies
// to this package's tests too. What is under test here is the MOUNT — routing,
// identity, attribution, CSRF — not the SDK's protocol handling, which the SDK
// tests itself. `internal/mcp` covers the server behavior.
type stubMCPFactory struct {
	mu   sync.Mutex
	seen []principal.Principal
	err  error
	host MCPHost
}

func (f *stubMCPFactory) build(host MCPHost) (http.Handler, error) {
	f.host = host
	if f.err != nil {
		return nil, f.err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen = append(f.seen, principal.From(r.Context()))
		f.mu.Unlock()

		// Mirror the stateless-transport contract the real handler has, so
		// the method-rejection test exercises the mount rather than a stub
		// that accepts everything.
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	}), nil
}

func (f *stubMCPFactory) principals() []principal.Principal {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]principal.Principal(nil), f.seen...)
}

// AC #2: with MCP not enabled the endpoint is ABSENT — an upgraded server
// serves no MCP until an operator opts in. Absent, not 403: a 403 would mean
// the route exists and merely refused, which is a different (and worse)
// upgrade story.
func TestRemoteMCP_AbsentUnlessEnabled(t *testing.T) {
	app := newTestAppV1(t)

	req := httptest.NewRequest(http.MethodPost, MCPPath, strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	app.NewRouter().ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("status = 200, want a non-OK: the MCP endpoint must not be "+
			"served when SetRemoteMCP was never called; body=%s", rec.Body)
	}
}

// AC #6, and the load-bearing one: enabling remote MCP without verified JWT
// identity is REFUSED AT STARTUP.
//
// The endpoint needs a CSRF exemption (an MCP client sends no Origin), and
// that exemption is only sound while rela verifies a bearer token itself. In
// header-identity mode the terminal resolver yields User="unknown", so
// exempting the path without a JWT gate would publish an unauthenticated
// remote write surface.
func TestSetRemoteMCP_RefusesWithoutJWTGate(t *testing.T) {
	f := &stubMCPFactory{}

	t.Run("no jwt gate is refused", func(t *testing.T) {
		app := newTestAppV1(t)
		err := app.SetRemoteMCP(f.build)
		if err == nil {
			t.Fatal("SetRemoteMCP() error = nil, want refusal: without a JWT gate " +
				"the CSRF-exempt endpoint is unauthenticated")
		}
		if !strings.Contains(err.Error(), "verified JWT identity") {
			t.Errorf("error = %q, want it to name the missing requirement", err)
		}
		if app.mcpHandler != nil {
			t.Error("mcpHandler was built despite the refusal — the endpoint would serve")
		}
	})

	t.Run("nil factory is refused", func(t *testing.T) {
		app := newTestAppV1(t)
		mustSetJWTGate(t, app)
		if err := app.SetRemoteMCP(nil); err == nil {
			t.Fatal("SetRemoteMCP(nil) error = nil, want refusal: a nil factory " +
				"would panic per request")
		}
	})

	t.Run("with a jwt gate it is accepted", func(t *testing.T) {
		// The positive half — proves the refusals above are about the missing
		// gate specifically, not a blanket rejection that would pass vacuously.
		app := newTestAppV1(t)
		mustSetJWTGate(t, app)
		if err := app.SetRemoteMCP(f.build); err != nil {
			t.Fatalf("SetRemoteMCP() error = %v, want nil once the JWT gate is set", err)
		}
		if app.mcpHandler == nil {
			t.Error("mcpHandler is nil after a successful SetRemoteMCP")
		}
	})
}

// AC #5: an unauthenticated request is refused, with no fall-through to a
// spoofable header. The route is registered, so this proves the JWT gate — not
// route absence — is what denies it.
func TestRemoteMCP_UnauthenticatedIsRefused(t *testing.T) {
	app := newTestAppV1(t)
	mustSetJWTGate(t, app)
	f := &stubMCPFactory{}
	if err := app.SetRemoteMCP(f.build); err != nil {
		t.Fatalf("SetRemoteMCP: %v", err)
	}
	h := app.NewRouter()

	t.Run("no assertion", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, MCPPath, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
		if len(f.principals()) != 0 {
			t.Errorf("the MCP server was built for an unauthenticated request "+
				"(principals seen: %v) — the gate must deny before the factory runs", f.seen)
		}
	})

	t.Run("a spoofed principal header does not authenticate", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, MCPPath, strings.NewReader("{}"))
		req.Header.Set("X-Forwarded-User", "attacker")
		req.Header.Set("X-Remote-User", "attacker")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 — a header must not stand in for a "+
				"verified assertion", rec.Code)
		}
	})
}

// AC #4 (RR-H8S10M): a request arriving on the MCP endpoint is attributed to
// Tool=mcp, not Tool=data-entry — while KEEPING the asserted roles, which is
// the part a naive `principal.Principal{Tool: ToolMCP}` composite literal
// would silently drop (VerifiedFrom is the only constructor that populates
// the unexported role/org/scope fields).
func TestRemoteMCP_AuditAttributionIsMCP(t *testing.T) {
	app := newTestAppV1(t)
	if err := app.SetJWTGate(JWTGateConfig{
		Verifier: gateVerifier{
			validToken: "good.jwt.token", subject: "usr_alice",
			orgID: "org_acme", roles: []string{"admin"},
		},
		HeaderName: "X-Auth-Assertion",
	}); err != nil {
		t.Fatalf("SetJWTGate: %v", err)
	}
	f := &stubMCPFactory{}
	if err := app.SetRemoteMCP(f.build); err != nil {
		t.Fatalf("SetRemoteMCP: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, MCPPath, strings.NewReader("{}"))
	req.Header.Set("X-Auth-Assertion", "good.jwt.token")
	rec := httptest.NewRecorder()
	app.NewRouter().ServeHTTP(rec, req)

	if len(f.principals()) != 1 {
		t.Fatalf("factory called %d times, want 1 (status=%d body=%s)",
			len(f.principals()), rec.Code, rec.Body)
	}
	got := f.principals()[0]

	if got.Tool != principal.ToolMCP {
		t.Errorf("Tool = %q, want %q — a remote MCP write must not be audited "+
			"as data-entry (RR-H8S10M)", got.Tool, principal.ToolMCP)
	}
	if got.User != "usr_alice" {
		t.Errorf("User = %q, want %q", got.User, "usr_alice")
	}
	// The roles are the regression net: they survive only because the Tool is
	// threaded INTO VerifiedFrom rather than patched on afterwards.
	if roles := got.Roles(); len(roles) != 1 || roles[0] != "admin" {
		t.Errorf("Roles() = %v, want [admin] — swapping the Tool must not drop "+
			"asserted claims", roles)
	}
	if got.OrgID() != "org_acme" {
		t.Errorf("OrgID() = %q, want %q", got.OrgID(), "org_acme")
	}
}

// The same request on a non-MCP API path must still be attributed to
// data-entry. Without this, TestRemoteMCP_AuditAttributionIsMCP would pass
// against an implementation that stamped Tool=mcp on every request.
func TestRemoteMCP_NonMCPPathKeepsDataEntryTool(t *testing.T) {
	if got := toolForPath("/api/v1/tickets/TKT-001"); got != principal.ToolDataEntry {
		t.Errorf("toolForPath(non-MCP) = %q, want %q", got, principal.ToolDataEntry)
	}
	if got := toolForPath(MCPPath); got != principal.ToolMCP {
		t.Errorf("toolForPath(MCPPath) = %q, want %q", got, principal.ToolMCP)
	}
	// A sub-path must not fall back to data-entry attribution.
	if got := toolForPath(MCPPath + "/sub"); got != principal.ToolMCP {
		t.Errorf("toolForPath(sub-path) = %q, want %q", got, principal.ToolMCP)
	}
	// A path that merely shares the prefix string is NOT the MCP endpoint.
	if got := toolForPath(MCPPath + "-other"); got != principal.ToolDataEntry {
		t.Errorf("toolForPath(%q) = %q, want %q — prefix matching must not "+
			"capture a sibling route", MCPPath+"-other", got, principal.ToolDataEntry)
	}
}

// The CSRF exemption must be conditioned on the request being provably
// non-browser, exactly like the sync/feeds/caldav entries. A browser-shaped
// request to the MCP endpoint must still face the same-origin check.
func TestRemoteMCP_CSRFExemptionOnlyForNonBrowser(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(*http.Request)
		wantExempt bool
	}{
		{
			name:       "bare MCP client request is exempt",
			setup:      func(*http.Request) {},
			wantExempt: true,
		},
		{
			name: "browser-issued (Sec-Fetch-Site) is NOT exempt",
			setup: func(r *http.Request) {
				r.Header.Set("Sec-Fetch-Site", "cross-site")
			},
			wantExempt: false,
		},
		{
			name: "credentialed (Cookie) is NOT exempt",
			setup: func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: "session", Value: "x"})
			},
			wantExempt: false,
		},
		{
			name: "Origin present is NOT exempt",
			setup: func(r *http.Request) {
				r.Header.Set("Origin", "https://evil.example")
			},
			wantExempt: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, MCPPath, strings.NewReader("{}"))
			tt.setup(req)
			if got := isCSRFExempt(req); got != tt.wantExempt {
				t.Errorf("isCSRFExempt() = %v, want %v", got, tt.wantExempt)
			}
		})
	}
}

// A broken MCP wiring is a STARTUP failure, not a per-request 500 discovered
// in production. The factory runs once during SetRemoteMCP, so the operator
// finds out while reading the error that stopped the boot.
func TestSetRemoteMCP_FactoryErrorRefusesAtStartup(t *testing.T) {
	t.Run("a factory error is surfaced", func(t *testing.T) {
		app := newTestAppV1(t)
		mustSetJWTGate(t, app)
		f := &stubMCPFactory{err: errFactoryTest}

		err := app.SetRemoteMCP(f.build)
		if err == nil {
			t.Fatal("SetRemoteMCP() error = nil, want the factory's error " +
				"surfaced at startup")
		}
		if !strings.Contains(err.Error(), errFactoryTest.Error()) {
			t.Errorf("error = %q, want it to wrap the underlying cause %q",
				err, errFactoryTest)
		}
		if app.mcpHandler != nil {
			t.Error("mcpHandler was set despite the factory failing")
		}
	})

	t.Run("a nil handler with no error is refused", func(t *testing.T) {
		// Otherwise the route would be registered with a nil handler and
		// panic on the first request.
		app := newTestAppV1(t)
		mustSetJWTGate(t, app)

		// nilnil is the exact shape under test: a factory that reports
		// success but hands back nothing to serve.
		nilFactory := func(MCPHost) (http.Handler, error) {
			return nil, nil //nolint:nilnil // the invalid input under test
		}
		err := app.SetRemoteMCP(nilFactory)
		if err == nil {
			t.Fatal("SetRemoteMCP() error = nil, want refusal of a nil handler")
		}
	})
}

// Reachability through the REAL router, in the shape TestRouterWalk cannot
// cover: that walk builds a default router, where the MCP route is
// deliberately absent, so a probe there would assert the opposite of AC #2.
// This is the same exception the IdP webhook takes
// (TestWebhook_ReachableThroughRouter).
//
// The oracle is "not 404": the route is registered and the request reached the
// MCP handler. A 404 would mean registration silently did not happen.
func TestRemoteMCP_ReachableThroughRouter(t *testing.T) {
	app := newTestAppV1(t)
	mustSetJWTGate(t, app)
	f := &stubMCPFactory{}
	if err := app.SetRemoteMCP(f.build); err != nil {
		t.Fatalf("SetRemoteMCP: %v", err)
	}

	// A well-formed JSON-RPC initialize, so the SDK handler answers rather
	// than rejecting the body shape.
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
		`"protocolVersion":"2025-11-25","capabilities":{},` +
		`"clientInfo":{"name":"test","version":"0"}}}`
	req := httptest.NewRequest(http.MethodPost, MCPPath, strings.NewReader(body))
	req.Header.Set("X-Auth-Assertion", "good.jwt.token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	app.NewRouter().ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Fatalf("status = 404: the MCP route was not registered on the router")
	}
	if len(f.principals()) == 0 {
		t.Fatalf("the per-request factory was never called (status=%d body=%s)",
			rec.Code, rec.Body)
	}
}

// A non-POST request still passes through the identity gate before reaching
// the MCP handler. The METHOD semantics themselves (stateless mode's 405 on
// GET/DELETE) belong to the SDK and are pinned in `internal/mcp`, where the
// real handler is built — asserting them against this package's stub would
// only be testing the stub.
//
// What matters here is that a GET is not accidentally exempted from
// authentication on the way in.
func TestRemoteMCP_NonPOSTStillAuthenticated(t *testing.T) {
	app := newTestAppV1(t)
	mustSetJWTGate(t, app)
	f := &stubMCPFactory{}
	if err := app.SetRemoteMCP(f.build); err != nil {
		t.Fatalf("SetRemoteMCP: %v", err)
	}
	h := app.NewRouter()

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		t.Run(method+" unauthenticated is 401", func(t *testing.T) {
			req := httptest.NewRequest(method, MCPPath, http.NoBody)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401 — %s must not bypass the identity "+
					"gate", rec.Code, method)
			}
		})
	}
}

func mustSetJWTGate(t *testing.T, app *App) {
	t.Helper()
	if err := app.SetJWTGate(JWTGateConfig{
		Verifier:   gateVerifier{validToken: "good.jwt.token", subject: "usr_alice"},
		HeaderName: "X-Auth-Assertion",
	}); err != nil {
		t.Fatalf("SetJWTGate: %v", err)
	}
}

var errFactoryTest = &factoryTestError{}

type factoryTestError struct{}

func (*factoryTestError) Error() string { return "test factory failure" }

// TestRemoteMCP_HostSharesUploadPolicy pins what the MCP attachment tools get
// from the App (TKT-R6U15C): the App's own attachment locker, its command runner,
// and the LIVE schema, so a policy reload reaches MCP uploads.
func TestRemoteMCP_HostSharesUploadPolicy(t *testing.T) {
	app := newTestAppV1(t)
	mustSetJWTGate(t, app)
	f := &stubMCPFactory{}
	if err := app.SetRemoteMCP(f.build); err != nil {
		t.Fatalf("SetRemoteMCP: %v", err)
	}

	if f.host.AttachmentLocker != app.attachmentLocker || app.attachments.locker != app.attachmentLocker {
		t.Error("MCP and web uploads do not share the App's attachment locker; their writes would not serialize")
	}
	if f.host.AttachmentRunner != app.attachmentRunner {
		t.Error("AttachmentRunner is not the App's runner")
	}

	next := *app.schema.Current()
	next.Meta = &metamodel.Metamodel{}
	cfg := *next.Cfg
	cfg.App.MaxAttachmentBytes = 1234
	next.Cfg = &cfg
	app.schema.Publish(&next)

	meta, limit := f.host.AttachmentPolicy()
	if meta != next.Meta || limit != 1234 {
		t.Errorf("AttachmentPolicy() = (%p, %d), want the reloaded schema (%p, 1234)", meta, limit, next.Meta)
	}
}
