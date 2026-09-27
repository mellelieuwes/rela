package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/principal"
)

// Golden artifacts for the go-sdk migration (TKT-UIR41P, PR 1).
//
// AC #6 asks that `rela mcp` stay semantically equivalent across the move from
// mark3labs/mcp-go to modelcontextprotocol/go-sdk. The criterion was written as
// "semantically equivalent, with a documented diff", on the assumption that the
// go-sdk's reflected input schemas and different error-result convention would
// force a wire change.
//
// OUTCOME: no diff. The migration kept the hand-built schema shape (toolspec.go)
// and the explicit result envelope (result.go) instead of adopting the reflected
// generic AddTool, so both artifacts below are byte-identical pre- and
// post-migration. That is a stronger result than AC #6 required.
//
// A test that is rewritten alongside the code it guards is not a regression
// net: both sides move together and the diff hides. So these goldens were
// captured from the PRE-migration binary and committed BEFORE the migration
// started, in their own commit.
//
// Regenerate deliberately (and only when the diff has been reviewed):
//
//	UPDATE_GOLDEN=1 go test ./internal/mcp -run TestGolden
//
// The two artifacts live in testdata/:
//
//   - tools_list.golden.json  — the full tools/list response: names, descriptions,
//     and inputSchema for every registered tool. This is where the reflected-schema
//     diff will show up.
//   - tool_calls.golden.json  — one representative tools/call result per tool,
//     driven from the shared toolCalls table, capturing isError and the result text.
//     This is where a behavioral regression shows up.
const (
	goldenToolsList = "tools_list.golden.json"
	goldenToolCalls = "tool_calls.golden.json"
)

// updateGolden is set by `-update`. Declared here (not with flag.Bool at init)
// so the flag exists only for this package's test binary.
var updateGolden = os.Getenv("UPDATE_GOLDEN") == "1"

// goldenPath resolves a testdata artifact path.
func goldenPath(name string) string { return filepath.Join("testdata", name) }

// readGolden loads a committed artifact, failing loudly when it is absent —
// an absent golden means the pre-migration capture never happened, which is
// exactly the mistake this file exists to prevent.
func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(goldenPath(name))
	if err != nil {
		t.Fatalf("read golden %s: %v\n\nIf this is the first run, capture it from the "+
			"PRE-migration binary with:\n    UPDATE_GOLDEN=1 go test ./internal/mcp -run TestGolden", name, err)
	}
	return data
}

// writeGolden persists an artifact, creating testdata/ on first capture.
func writeGolden(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatalf("mkdir testdata: %v", err)
	}
	if err := os.WriteFile(goldenPath(name), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write golden %s: %v", name, err)
	}
	t.Logf("wrote %s", goldenPath(name))
}

// marshalStable renders v as indented JSON. Go's encoder sorts map keys, so
// map-shaped payloads are stable across runs; slice order is the caller's
// responsibility (both capture sites below sort by tool name).
func marshalStable(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	return data
}

// newGoldenServer builds a dispatch server over a DETERMINISTIC fixture.
//
// The shared makeTestFixture seeds random titles via testutil.EntityFor (a
// "word-a3f8" per entity) — deliberate, because it catches handlers that
// hardcode fixture values. That randomness is fatal for a golden, so this
// helper overwrites every seeded title with a fixed one.
//
// Note this does NOT make every tool deterministic: create_entity mints a
// random id (REQ-TBVH), so that one call is normalized at capture time
// instead — see stableText.
func newGoldenServer(t *testing.T) *Server {
	t.Helper()

	meta, st := makeTestFixture(t)
	ctx := context.Background()
	for id, title := range map[string]string{
		"REQ-001": "First requirement",
		"REQ-002": "Second requirement",
		"REQ-003": "Third requirement",
		"DEC-001": "First decision",
	} {
		e, err := st.GetEntity(ctx, id)
		if err != nil {
			t.Fatalf("golden fixture: get %s: %v", id, err)
		}
		e.Properties["title"] = title
		if err := st.UpdateEntity(ctx, e); err != nil {
			t.Fatalf("golden fixture: pin title on %s: %v", id, err)
		}
	}

	srv, err := NewServer(newTestDeps(t, meta, st), "test",
		WithPrincipal(principal.Principal{User: "tester", Tool: principal.ToolMCP}), WithLuaTools())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

// mintedIDRe matches a generated entity id (prefix + 4 base32-ish chars), which
// create_entity produces fresh on every run.
var mintedIDRe = regexp.MustCompile(`\b(REQ|DEC)-[A-Z0-9]{4}\b`)

// stableText normalizes the one genuinely nondeterministic element of a tool
// result — a freshly minted entity id — so the golden pins everything else.
// The seeded ids (REQ-001 … DEC-001) are numeric and unaffected.
func stableText(s string) string {
	return mintedIDRe.ReplaceAllString(s, "$1-XXXX")
}

// goldenTool is one tool's public contract as seen by a client. Captured from
// the real tools/list response rather than from the registration structs, so
// it reflects what actually goes on the wire.
type goldenTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// captureToolsList performs a real tools/list round trip and returns the tools
// sorted by name.
func captureToolsList(t *testing.T) []goldenTool {
	t.Helper()
	s := newGoldenServer(t)

	result, rpcErr := dispatch(t, s, "tools/list", `{}`)
	if rpcErr != nil {
		t.Fatalf("tools/list: JSON-RPC error %d: %s", rpcErr.Code, rpcErr.Message)
	}
	var decoded struct {
		Tools []goldenTool `json:"tools"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	sort.Slice(decoded.Tools, func(i, j int) bool { return decoded.Tools[i].Name < decoded.Tools[j].Name })
	return decoded.Tools
}

// TestGolden_ToolsList pins the wire-visible tool contract: names,
// descriptions and input schemas.
//
// This passed unchanged across the go-sdk migration, which is the evidence
// for AC #6 on the schema side. A failure here means a tool's public contract
// moved: review the diff and record the accepted delta on the ticket before
// regenerating, never regenerate to make it green.
func TestGolden_ToolsList(t *testing.T) {
	t.Parallel()

	got := marshalStable(t, captureToolsList(t))
	if updateGolden {
		writeGolden(t, goldenToolsList, got)
		return
	}

	want := readGolden(t, goldenToolsList)
	if !bytes.Equal(got, normalizeTrailingNewline(want)) {
		t.Errorf("tools/list differs from the committed golden.\n"+
			"This is expected after the go-sdk migration (reflected schemas differ from\n"+
			"hand-built ones). Review the diff, record the accepted delta on TKT-UIR41P,\n"+
			"then regenerate with UPDATE_GOLDEN=1.\n\n--- got ---\n%s", got)
	}
}

// goldenCall is one tool invocation's observable outcome.
type goldenCall struct {
	Tool    string `json:"tool"`
	Args    string `json:"args"`
	IsError bool   `json:"isError"`
	Text    string `json:"text"`
}

// TestGolden_ToolCalls pins one representative invocation per registered tool,
// reusing the shared toolCalls table so a newly registered tool is captured
// automatically (TestDispatch_ToolInventoryMatches already fails if a tool is
// missing from that table).
//
// Each call gets a fresh server because the write tools mutate the fixture.
func TestGolden_ToolCalls(t *testing.T) {
	t.Parallel()

	names := make([]string, 0, len(toolCalls))
	for name := range toolCalls {
		names = append(names, name)
	}
	sort.Strings(names)

	calls := make([]goldenCall, 0, len(names))
	for _, name := range names {
		tc := toolCalls[name]
		s := newGoldenServer(t)
		text, isError := callTool(t, s, name, tc.args)
		calls = append(calls, goldenCall{Tool: name, Args: tc.args, IsError: isError, Text: stableText(text)})
	}

	got := marshalStable(t, calls)
	if updateGolden {
		writeGolden(t, goldenToolCalls, got)
		return
	}

	want := readGolden(t, goldenToolCalls)
	if !bytes.Equal(got, normalizeTrailingNewline(want)) {
		t.Errorf("tools/call results differ from the committed golden.\n"+
			"Unlike the schema diff, a change here is a BEHAVIORAL regression and should\n"+
			"be treated as a bug in the migration until proven otherwise.\n\n--- got ---\n%s", got)
	}
}

// normalizeTrailingNewline strips the single trailing newline writeGolden adds,
// so comparisons are against the marshaled bytes themselves.
func normalizeTrailingNewline(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		return b[:n-1]
	}
	return b
}
