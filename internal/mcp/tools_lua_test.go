package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Sourcehaven-BV/rela/internal/lua"
)

// luaCallToolReq builds a minimal MCP CallToolRequest carrying named
// string args, mirroring how the real MCP harness deserialises a client
// invocation. Used to drive handleLuaEval / handleLuaRun in tests.
func luaCallToolReq(args map[string]any) *mcpgo.CallToolRequest {
	return makeToolRequest(args)
}

// decodeScriptError extracts the JSON envelope returned by the lua tools
// on failure. Errors out the test if anything about the shape is off.
func decodeScriptError(t *testing.T, result *mcpgo.CallToolResult) *lua.ScriptError {
	t.Helper()
	if !result.IsError {
		t.Fatalf("expected IsError=true, got false; content=%v", result.Content)
	}
	if len(result.Content) == 0 {
		t.Fatal("result has no content")
	}
	text, ok := result.Content[0].(*mcpgo.TextContent)
	if !ok {
		t.Fatalf("content[0] is not TextContent: %T", result.Content[0])
	}
	var se lua.ScriptError
	if err := json.Unmarshal([]byte(text.Text), &se); err != nil {
		t.Fatalf("envelope is not valid JSON: %v\nbody: %s", err, text.Text)
	}
	return &se
}

func TestHandleLuaEval_ReturnsScriptErrorEnvelope(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)

	result, err := group(s, selLua).handleLuaEval(context.Background(),
		luaCallToolReq(map[string]any{"code": "print('hello')\nerror('kaboom')"}))
	if err != nil {
		t.Fatalf("handler returned Go error: %v", err)
	}
	se := decodeScriptError(t, result)

	if se.Surface != lua.SurfaceLuaEval {
		t.Errorf("Surface=%q, want %q", se.Surface, lua.SurfaceLuaEval)
	}
	if se.Path != "<inline>" {
		t.Errorf("Path=%q, want <inline>", se.Path)
	}
	if !strings.Contains(se.LuaMessage, "kaboom") {
		t.Errorf("LuaMessage=%q, want contains kaboom", se.LuaMessage)
	}
	if se.LuaLine != 2 {
		t.Errorf("LuaLine=%d, want 2", se.LuaLine)
	}
	// CapturedOutput is intentionally empty for lua_eval/lua_run: see
	// runtime.go:256 — print() routes to os.Stdout outside document/
	// action modes so MCP / scheduler / CLI behave like a normal terminal.
	if se.CapturedOutput != "" {
		t.Errorf("lua_eval CapturedOutput=%q, want empty (print goes to terminal)", se.CapturedOutput)
	}
	// lua_eval has no on-disk source — Source slice must be empty.
	if len(se.Source) != 0 {
		t.Errorf("lua_eval Source non-empty: %+v", se.Source)
	}
}

func TestHandleLuaEval_PreservesIsErrorFlag(t *testing.T) {
	t.Parallel()
	s := makeTestServer(t)
	result, _ := group(s, selLua).handleLuaEval(context.Background(),
		luaCallToolReq(map[string]any{"code": "error('x')"}))
	if !result.IsError {
		t.Error("IsError flag must remain true on Lua failure for MCP clients to branch")
	}
}

// TestHandleLuaRun_ListsScriptsWithoutPath pins that lua_run with no path
// lists the scripts, which replaced the lua_list tool.
func TestHandleLuaRun_ListsScriptsWithoutPath(t *testing.T) {
	t.Parallel()
	meta, st := makeTestFixture(t)
	deps := newTestDeps(t, meta, st)
	if err := os.MkdirAll(filepath.Join(deps.ProjectRoot, "scripts", "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"reports/weekly.lua", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(deps.ProjectRoot, "scripts", name), []byte("return 1"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{}
	setDeps(s, deps)

	result, err := group(s, selLua).handleLuaRun(context.Background(), luaCallToolReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := getResultText(t, result)
	if result.IsError || !strings.Contains(text, filepath.Join("reports", "weekly.lua")) || strings.Contains(text, "notes.txt") {
		t.Errorf("want only the .lua script listed, got: %s", text)
	}
}
