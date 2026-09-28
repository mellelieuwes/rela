package dataentry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/Sourcehaven-BV/rela/internal/apiwire/v1"
	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	"github.com/Sourcehaven-BV/rela/internal/entity"
)

// callAction is a thin wrapper that invokes handleV1Action directly. It
// used to simulate the reloadLockMiddleware by holding RLock around the
// call; since App.mu was deleted this is now just a direct call, but
// keeping the helper avoids touching every test body in this file.
func callAction(app *App, req *http.Request, rec *httptest.ResponseRecorder) {
	app.write.handleV1Action(rec, req)
}

// newActionTestApp builds a test App with a real project root containing
// the given action scripts.
func newActionTestApp(t *testing.T, scripts map[string]string) *App {
	t.Helper()

	tmpDir := t.TempDir()
	actionsDir := filepath.Join(tmpDir, "actions")
	if err := os.MkdirAll(actionsDir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, content := range scripts {
		if err := os.WriteFile(filepath.Join(actionsDir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	app := newTestAppV1(t)
	bindRepo(app, tmpDir)

	// Wire security so allowFullScriptDetail works against r.RemoteAddr.
	// Tests that want loopback callers set req.RemoteAddr = "127.0.0.1:..."
	// explicitly; the default httptest "192.0.2.1:1234" is non-loopback.
	if err := app.SetSecurityConfig(SecurityConfig{BindAddress: "127.0.0.1:8080"}); err != nil {
		t.Fatalf("SetSecurityConfig: %v", err)
	}

	return app
}

func TestHandleV1Action_Success(t *testing.T) {
	app := newActionTestApp(t, map[string]string{
		"hello.lua": `return {redirect = "/done", message = "ok"}`,
	})
	app.Cfg().Actions = map[string]dataentryconfig.Action{
		"hello": {Script: "hello.lua"},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/_action/hello", http.NoBody)
	rec := httptest.NewRecorder()

	callAction(app, req, rec)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp v1.ActionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.Redirect != "/done" {
		t.Errorf("expected redirect=/done, got %q", resp.Redirect)
	}
	if resp.Message != "ok" {
		t.Errorf("expected message=ok, got %q", resp.Message)
	}
}

func TestHandleV1Action_NoReturn(t *testing.T) {
	app := newActionTestApp(t, map[string]string{
		"noop.lua": `local x = 1`,
	})
	app.Cfg().Actions = map[string]dataentryconfig.Action{
		"noop": {Script: "noop.lua"},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/_action/noop", http.NoBody)
	rec := httptest.NewRecorder()

	callAction(app, req, rec)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rec.Code)
	}
}

func TestHandleV1Action_NotFound(t *testing.T) {
	app := newActionTestApp(t, nil)
	app.Cfg().Actions = map[string]dataentryconfig.Action{}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/_action/missing", http.NoBody)
	rec := httptest.NewRecorder()

	callAction(app, req, rec)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleV1Action_InvalidID(t *testing.T) {
	app := newActionTestApp(t, nil)

	tests := []struct {
		name string
		path string
	}{
		{"uppercase", "/api/v1/_action/Foo"},
		{"empty", "/api/v1/_action/"},
		{"slash", "/api/v1/_action/foo/bar"},
		{"dot", "/api/v1/_action/foo.bar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, http.NoBody)
			rec := httptest.NewRecorder()
			callAction(app, req, rec)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("expected 400 for %q, got %d", tt.path, rec.Code)
			}
		})
	}
}

func TestHandleV1Action_MethodNotAllowed(t *testing.T) {
	app := newActionTestApp(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/_action/foo", http.NoBody)
	rec := httptest.NewRecorder()

	callAction(app, req, rec)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleV1Action_ScriptError_DegradedForNonLoopback(t *testing.T) {
	app := newActionTestApp(t, map[string]string{
		"boom.lua": `print("before")
error("kaboom")`,
	})
	app.Cfg().Actions = map[string]dataentryconfig.Action{
		"boom": {Script: "boom.lua"},
	}

	// Default httptest.NewRequest sets RemoteAddr to 192.0.2.1 (TEST-NET-1,
	// non-loopback), so the response should omit source/captured/stack.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/_action/boom", http.NoBody)
	rec := httptest.NewRecorder()

	callAction(app, req, rec)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}

	var env ScriptErrorEnvelope
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if env.Error != "script_error" {
		t.Errorf("Error=%q, want script_error", env.Error)
	}
	if env.CorrelationID == "" {
		t.Error("missing correlation_id")
	}
	if env.Script.Surface != "action" {
		t.Errorf("Script.Surface=%q, want action", env.Script.Surface)
	}
	if env.Script.Path != "actions/boom.lua" {
		t.Errorf("Script.Path=%q, want actions/boom.lua", env.Script.Path)
	}
	if env.Lua.Line != 2 {
		t.Errorf("Lua.Line=%d, want 2", env.Lua.Line)
	}
	if !strings.Contains(env.Lua.Message, "kaboom") {
		t.Errorf("Lua.Message=%q, want contains kaboom", env.Lua.Message)
	}
	// Degraded shape: gated fields must be absent.
	if len(env.Source) != 0 {
		t.Errorf("non-loopback caller got source slice: %+v", env.Source)
	}
	if len(env.Stack) != 0 {
		t.Errorf("non-loopback caller got stack: %+v", env.Stack)
	}
	if env.CapturedOutput != "" {
		t.Errorf("non-loopback caller got captured output: %q", env.CapturedOutput)
	}
}

func TestHandleV1Action_ScriptError_FullForLoopback(t *testing.T) {
	app := newActionTestApp(t, map[string]string{
		"boom.lua": `print("before")
error("kaboom")`,
	})
	app.Cfg().Actions = map[string]dataentryconfig.Action{
		"boom": {Script: "boom.lua"},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/_action/boom", http.NoBody)
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()

	callAction(app, req, rec)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}

	var env ScriptErrorEnvelope
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(env.Source) == 0 {
		t.Error("loopback caller missing source slice")
	}
	if len(env.Stack) == 0 {
		t.Error("loopback caller missing stack")
	}
	if !strings.Contains(env.CapturedOutput, "before") {
		t.Errorf("CapturedOutput=%q, want contains 'before'", env.CapturedOutput)
	}
}

func TestHandleV1Action_ParamsPassed(t *testing.T) {
	app := newActionTestApp(t, map[string]string{
		"echo.lua": `return {message = rela.params.greeting}`,
	})
	app.Cfg().Actions = map[string]dataentryconfig.Action{
		"echo": {
			Script: "echo.lua",
			Params: map[string]string{"greeting": "hello world"},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/_action/echo", http.NoBody)
	rec := httptest.NewRecorder()

	callAction(app, req, rec)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp v1.ActionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.Message != "hello world" {
		t.Errorf("expected message='hello world', got %q", resp.Message)
	}
}

func TestHandleV1Action_OpenRedirectRejected(t *testing.T) {
	app := newActionTestApp(t, map[string]string{
		"evil.lua": `return {redirect = "//evil.com"}`,
	})
	app.Cfg().Actions = map[string]dataentryconfig.Action{
		"evil": {Script: "evil.lua"},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/_action/evil", http.NoBody)
	rec := httptest.NewRecorder()

	callAction(app, req, rec)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for open redirect, got %d", rec.Code)
	}
}

func TestHandleV1Action_Concurrent(t *testing.T) {
	// Parallel POSTs to the same action run concurrently and all succeed
	// (no panics, no races under -race).
	app := newActionTestApp(t, map[string]string{
		"noop.lua": `return {message = "done"}`,
	})
	app.Cfg().Actions = map[string]dataentryconfig.Action{
		"noop": {Script: "noop.lua"},
	}

	const N = 5
	var wg sync.WaitGroup
	for range N {
		wg.Go(func() {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/_action/noop", http.NoBody)
			rec := httptest.NewRecorder()
			callAction(app, req, rec)
			if rec.Code != http.StatusOK {
				t.Errorf("expected 200, got %d", rec.Code)
			}
		})
	}
	wg.Wait()
}

func TestNewCorrelationID(t *testing.T) {
	id1 := newCorrelationID()
	id2 := newCorrelationID()
	if id1 == id2 {
		t.Errorf("correlation IDs should differ: %q == %q", id1, id2)
	}
	if id1 == "" {
		t.Error("correlation ID is empty")
	}
}

// TestHandleV1Action_DoesNotBlockConcurrentWrites pins acceptance criterion 1
// of TKT-WE0S2K: a running action holds no lock that other writes wait on.
// The action spins until a concurrent PATCH has landed. Under the removed
// process-wide write lock the PATCH would wait for the action and the action
// for the PATCH, so the test would time out instead.
func TestHandleV1Action_DoesNotBlockConcurrentWrites(t *testing.T) {
	app := newActionTestApp(t, map[string]string{
		"wait.lua": `
while true do
  local e = rela.get_entity("TKT-001")
  if e ~= nil and e.properties.status == "go" then break end
end
return {message = "released"}`,
	})
	app.Cfg().Actions = map[string]dataentryconfig.Action{
		"wait": {Script: "wait.lua"},
	}
	seedEntity(app, &entity.Entity{
		ID: "TKT-001", Type: "ticket",
		Properties: map[string]any{"title": "Blocked", "status": "open"},
	})

	actionDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/_action/wait", http.NoBody)
		rec := httptest.NewRecorder()
		callAction(app, req, rec)
		actionDone <- rec
	}()

	patchDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { patchDone <- patchBody(app, `{"properties":{"status":"go"}}`, nil) }()

	const limit = 10 * time.Second
	select {
	case rec := <-patchDone:
		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH got %d, want 200: %s", rec.Code, rec.Body)
		}
	case <-time.After(limit):
		t.Fatal("PATCH did not complete while the action was running; a write lock is back")
	}
	select {
	case rec := <-actionDone:
		if rec.Code != http.StatusOK {
			t.Fatalf("action got %d, want 200: %s", rec.Code, rec.Body)
		}
	case <-time.After(limit):
		t.Fatal("action did not observe the concurrent PATCH")
	}
}
