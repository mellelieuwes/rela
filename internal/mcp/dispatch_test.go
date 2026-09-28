package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Sourcehaven-BV/rela/internal/lua"
	"github.com/Sourcehaven-BV/rela/internal/principal"
)

// These tests exercise the real registration → dispatch → argument-decode
// surface of the MCP server, which the handler-level tests in
// tools_test.go bypass (they call s.handle* methods directly with
// pre-decoded argument maps). A tool registered under the wrong name, a
// handler wired to the wrong tool, or an argument schema that stops
// decoding is only visible at this level (TKT-TLQ94B).
//
// Since TKT-UIR41P these run against a REAL client↔server session over
// the go-sdk's in-memory transport pair, rather than poking the old
// library's HandleMessage entry point directly. That is strictly closer
// to production: the request crosses a genuine JSON-RPC connection, so
// argument marshaling and result marshaling are both exercised.

// newDispatchServer builds the server through the production NewServer
// constructor so tool registration, the principal middleware, and the
// SDK dispatch table are all real.
func newDispatchServer(t *testing.T) *Server {
	t.Helper()

	meta, st := makeTestFixture(t)
	srv, err := NewServer(newTestDeps(t, meta, st), "test",
		WithPrincipal(principal.Principal{User: "tester", Tool: principal.ToolMCP}), WithLuaTools())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

// rpcError mirrors the JSON-RPC error object shape on the wire.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// connect runs s over one half of an in-memory transport pair and
// returns a connected client session. Both sides are torn down when the
// test ends.
func connect(t *testing.T, s *Server) *mcpgo.ClientSession {
	t.Helper()

	ctx := context.Background()
	serverTransport, clientTransport := mcpgo.NewInMemoryTransports()

	serverSession, err := s.mcp.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcpgo.NewClient(&mcpgo.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	return clientSession
}

// dispatch drives one request over a real session and returns the
// decoded result or error. Only the two methods the tests below need
// (tools/list, tools/call) are routed; anything else is a test bug.
func dispatch(t *testing.T, s *Server, method, params string) (json.RawMessage, *rpcError) {
	t.Helper()

	cs := connect(t, s)
	ctx := context.Background()

	var (
		result any
		err    error
	)
	switch method {
	case "tools/list":
		result, err = cs.ListTools(ctx, &mcpgo.ListToolsParams{})
	case "tools/call":
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if uerr := json.Unmarshal([]byte(params), &call); uerr != nil {
			t.Fatalf("decode tools/call params: %v", uerr)
		}
		result, err = cs.CallTool(ctx, &mcpgo.CallToolParams{
			Name:      call.Name,
			Arguments: call.Arguments,
		})
	default:
		t.Fatalf("dispatch: unsupported method %q", method)
	}

	if err != nil {
		// A transport/protocol failure. The SDK returns a *jsonrpc2.WireError
		// for genuine JSON-RPC errors; anything else is a harness fault.
		var wire *jsonrpc.Error
		if errors.As(err, &wire) {
			return nil, &rpcError{Code: int(wire.Code), Message: wire.Message}
		}
		t.Fatalf("%s: %v", method, err)
	}

	// The SDK hands back the already-unwrapped result value, so the
	// marshaled payload IS the result body — there is no envelope to peel.
	data, merr := json.Marshal(result)
	if merr != nil {
		t.Fatalf("marshal response: %v", merr)
	}
	return data, nil
}

// callTool invokes a tool via a real tools/call message with raw JSON
// arguments and returns the decoded CallToolResult fields.
func callTool(t *testing.T, s *Server, name, argsJSON string) (text string, isError bool) {
	t.Helper()

	params := fmt.Sprintf(`{"name":%q,"arguments":%s}`, name, argsJSON)
	result, rpcErr := dispatch(t, s, "tools/call", params)
	if rpcErr != nil {
		t.Fatalf("tools/call %s: JSON-RPC error %d: %s", name, rpcErr.Code, rpcErr.Message)
	}
	var decoded struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("decode tools/call result %s: %v", result, err)
	}
	if len(decoded.Content) == 0 {
		t.Fatalf("tools/call %s: empty content", name)
	}
	return decoded.Content[0].Text, decoded.IsError
}

// toolCalls is one realistic happy-path invocation per registered tool.
// Arguments are raw JSON so the real schema decode runs — no hand-built
// map[string]interface{} with pre-coerced float64s.
//
// TestDispatch_ToolInventoryMatches diffs this table against tools/list,
// so registering a new tool without adding a case here fails loudly.
var toolCalls = map[string]struct {
	args string
	// wantErr marks calls whose tool-level error result is the expected
	// outcome (the dispatch and decode still succeeded).
	wantErr bool
}{
	"list_entities":   {args: `{"type":"requirement","filter":"entity.status == 'accepted'","limit":2}`},
	"show_entity":     {args: `{"id":"REQ-001"}`},
	"search_entities": {args: `{"query":"requirement","limit":5}`},
	"create_entity":   {args: `{"type":"requirement","properties":{"title":"Created via dispatch"}}`},
	"update_entity":   {args: `{"id":"REQ-001","properties":{"status":"done"}}`},
	"delete_entity":   {args: `{"id":"REQ-002","cascade":true}`},
	"rename_entity":   {args: `{"id":"REQ-003","new_id":"REQ-099"}`},
	"list_relations":  {args: `{}`},
	"create_relation": {args: `{"from":"DEC-001","type":"addresses","to":"REQ-002"}`},
	"delete_relation": {args: `{"from":"DEC-001","type":"addresses","to":"REQ-001"}`},
	"trace":           {args: `{"id":"REQ-001","max_depth":3}`},
	"find_path":       {args: `{"from":"DEC-001","to":"REQ-001"}`},
	"analyze":         {args: `{"check":"orphans"}`},
	"schema":          {args: `{"type":"requirement"}`},
	"lua_eval":        {args: `{"code":"return 1"}`},
	"lua_run":         {args: `{"path":"missing.lua"}`, wantErr: true}, // no scripts dir in fixture
	// The shared fixture declares no file property; these three decode their
	// arguments and fail the property check. tools_attachment_test.go covers
	// the behavior.
	"list_attachments":  {args: `{"id":"REQ-001"}`},
	"read_attachment":   {args: `{"id":"REQ-001","property":"title","file_name":"a.txt"}`, wantErr: true},
	"attach_file":       {args: `{"id":"REQ-001","property":"title","file_name":"a.txt","content":"aGk="}`, wantErr: true},
	"delete_attachment": {args: `{"id":"REQ-001","property":"title"}`, wantErr: true},
}

// TestDispatch_AnalyzeEveryCheck drives every analyze check through a real
// tools/call, since the inventory table holds one call per tool name.
func TestDispatch_AnalyzeEveryCheck(t *testing.T) {
	t.Parallel()
	for _, check := range analyzeChecks {
		t.Run(check, func(t *testing.T) {
			t.Parallel()
			s := newDispatchServer(t)
			text, isError := callTool(t, s, "analyze", fmt.Sprintf(`{"check":%q}`, check))
			if isError {
				t.Errorf("analyze %s: unexpected error: %s", check, text)
			}
		})
	}
	s := newDispatchServer(t)
	if _, isError := callTool(t, s, "analyze", `{"check":"bogus"}`); !isError {
		t.Error("analyze with an unknown check should fail")
	}
}

// TestDispatch_ToolInventoryMatches pins the registered tool set via a
// real tools/list round trip. A tool added to registerTools without a
// dispatch case below (or removed while still listed here) fails this
// test by name.
func TestDispatch_ToolInventoryMatches(t *testing.T) {
	t.Parallel()
	s := newDispatchServer(t)

	result, rpcErr := dispatch(t, s, "tools/list", `{}`)
	if rpcErr != nil {
		t.Fatalf("tools/list: JSON-RPC error %d: %s", rpcErr.Code, rpcErr.Message)
	}
	var decoded struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("decode tools/list result: %v", err)
	}

	registered := make(map[string]bool, len(decoded.Tools))
	for _, tool := range decoded.Tools {
		registered[tool.Name] = true
	}
	for name := range toolCalls {
		if !registered[name] {
			t.Errorf("tool %q is in the dispatch table but not registered", name)
		}
	}
	var missing []string
	for name := range registered {
		if _, ok := toolCalls[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("tool %q is registered but has no dispatch test case — add it to toolCalls", name)
	}
}

// TestNewServer_LuaToolsAreOptIn pins that a server built without
// WithLuaTools registers no lua_* tool, and that a call fails. That is the
// remote (HTTP) wiring, which must not let callers run scripts (BUG-RIJR6R).
func TestNewServer_LuaToolsAreOptIn(t *testing.T) {
	t.Parallel()

	meta, st := makeTestFixture(t)
	srv, err := NewServer(newTestDeps(t, meta, st), "test",
		WithPrincipal(principal.Principal{User: "tester", Tool: principal.ToolMCP}))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	result, rpcErr := dispatch(t, srv, "tools/list", `{}`)
	if rpcErr != nil {
		t.Fatalf("tools/list: JSON-RPC error %d: %s", rpcErr.Code, rpcErr.Message)
	}
	var decoded struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("decode tools/list result: %v", err)
	}
	if len(decoded.Tools) == 0 {
		t.Fatal("tools/list is empty")
	}
	for _, tool := range decoded.Tools {
		if strings.HasPrefix(tool.Name, "lua_") {
			t.Errorf("tool %q registered without WithLuaTools", tool.Name)
		}
	}

	_, rpcErr = dispatch(t, srv, "tools/call", `{"name":"lua_eval","arguments":{"code":"return 1"}}`)
	if rpcErr == nil {
		t.Error("lua_eval dispatched on a server built without WithLuaTools")
	}

	noLua := newTestDeps(t, meta, st)
	noLua.LuaWriteDeps = lua.WriteDeps{}
	if _, err := NewServer(noLua, "test",
		WithPrincipal(principal.Principal{User: "tester", Tool: principal.ToolMCP}), WithLuaTools()); err == nil {
		t.Error("NewServer accepted WithLuaTools without LuaWriteDeps")
	}
}

// TestDispatch_EveryToolDecodesAndRuns drives each registered tool
// through a real tools/call. Each subtest gets a fresh server because
// the write tools mutate the seeded graph.
func TestDispatch_EveryToolDecodesAndRuns(t *testing.T) {
	t.Parallel()
	names := make([]string, 0, len(toolCalls))
	for name := range toolCalls {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		tc := toolCalls[name]
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newDispatchServer(t)
			text, isError := callTool(t, s, name, tc.args)
			if isError != tc.wantErr {
				t.Errorf("isError = %v, want %v (result text: %s)", isError, tc.wantErr, text)
			}
			if text == "" {
				t.Error("empty result text")
			}
		})
	}
}

// TestDispatch_UnknownToolRejected pins that the dispatch layer (not our
// handlers) rejects calls to unregistered tool names.
func TestDispatch_UnknownToolRejected(t *testing.T) {
	t.Parallel()
	s := newDispatchServer(t)

	result, rpcErr := dispatch(t, s, "tools/call", `{"name":"no_such_tool","arguments":{}}`)
	if rpcErr == nil {
		t.Fatalf("expected JSON-RPC error for unknown tool, got result %s", result)
	}
}

// TestDispatch_MalformedArgumentsSurface pins that a required argument
// missing from the wire payload surfaces as an error to the client
// rather than silently running with a zero value. The enforcement
// lives in the handler's RequireString guard (mcp-go does not reject
// missing required arguments at the schema/dispatch layer); this test
// pins the client-visible contract regardless of which layer enforces
// it.
func TestDispatch_MalformedArgumentsSurface(t *testing.T) {
	t.Parallel()
	s := newDispatchServer(t)

	text, isError := callTool(t, s, "show_entity", `{}`)
	if !isError {
		t.Errorf("show_entity without required id should produce an error result, got: %s", text)
	}
}

// TestDispatch_UnknownArgumentRejected pins that an undeclared argument is an
// error. The removed `where` is the case that matters: dropping it silently
// would return an unfiltered list the caller takes for a filtered one.
func TestDispatch_UnknownArgumentRejected(t *testing.T) {
	t.Parallel()
	s := newDispatchServer(t)
	text, isError := callTool(t, s, "list_entities", `{"type":"requirement","where":"status=accepted"}`)
	if !isError || !strings.Contains(text, "unknown argument(s): where") || !strings.Contains(text, "filter") {
		t.Errorf("want an unknown-argument error naming where and listing filter, got isError=%v %s", isError, text)
	}
}
