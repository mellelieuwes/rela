// coverage-ignore: MCP tool handlers - tested via integration tests
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Sourcehaven-BV/rela/internal/lua"
	"github.com/Sourcehaven-BV/rela/internal/script"
)

// scriptsDir is the directory where Lua scripts must be located for lua_run.
const scriptsDir = "scripts"

func toolLuaEval() *mcpgo.Tool {
	return newTool("lua_eval",
		withDescription(
			"Execute Lua code against the rela graph. "+
				"Use rela.output(data) to return results as JSON. "+
				"Available functions: get_entity, list_entities, search, create_entity, update_entity, "+
				"delete_entity, get_relations, create_relation, delete_relation, trace_from, trace_to, "+
				"find_path, refresh, write_file, get_entity_types, get_relation_types. "+
				"Context: rela.project_root, rela.args."),
		withString("code", required(),
			description("Lua code to execute")),
	)
}

func toolLuaRun() *mcpgo.Tool {
	return newTool("lua_run",
		withDescription(
			"Run a Lua script from the project's scripts/ directory. "+
				"Omit path to list the available scripts. "+
				"Use rela.output(data) to return results as JSON."),
		withString("path", description("Script path within scripts/, e.g. reports/summary.lua")),
		withArray("args", description("Arguments, available as rela.args")),
	)
}

// luaHandler serves the lua_eval / lua_run tools. A type of its
// own rather than more methods on [Server] (the urlHelpers pattern,
// TKT-MGNE5L): the lua tools are the ONLY consumers of the write-capable
// runtime deps, the script cache, and the project root — holding them here
// means no other handler in the package can reach a Lua write path. Identity
// still arrives on the ctx via Server.principalMiddleware; the handler holds
// no principal.
type luaHandler struct {
	writeDeps   lua.WriteDeps
	cache       *lua.Cache
	projectRoot string
}

func (h luaHandler) handleLuaEval(ctx context.Context, req *mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	in := newToolRequest(req)
	code, err := in.RequireString("code")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	// Capture output
	var output bytes.Buffer

	// No capability grant (TKT-YH52OM): lua_eval executes code chosen by an
	// MCP client, so it is the LEAST appropriate surface to hold http, ai or
	// secrets — arbitrary attacker-influenceable code paired with the whole
	// secrets file is precisely the exfiltration chain the ticket closes.
	// Since TKT-BDG8U9 the MCP endpoint can also be mounted over HTTP, so this
	// is reachable off-host rather than only over stdio — which raises the
	// stakes rather than changing the answer.
	// LuaWriteDeps.Capabilities is the zero value here and must stay that way;
	// do not "fix" a script that fails with "attempt to index a nil value
	// (global http)" by granting it here.
	runtime, err := script.NewWriterRuntime(h.writeDeps, "",
		&output, lua.WithContext(ctx), lua.WithCache(h.cache))
	if err != nil {
		return errorResult("config error: " + err.Error()), nil
	}
	defer runtime.Close()

	// scriptPath is intentionally left empty: rela.cache.* in lua_eval
	// raises "not available in inline/eval contexts" so sessions can't
	// accidentally share a nameless namespace.
	//
	// ctx is threaded via lua.WithContext(ctx) above; the runtime caches it and
	// RunString applies it to the LState. contextcheck can't see that flow
	// because it crosses the gopher-lua SetContext boundary.
	//nolint:contextcheck // ctx threaded via WithContext; see comment above
	if err := runtime.RunString(code); err != nil {
		// output stays empty here: print() in non-document/non-action
		// runtimes writes to os.Stdout (see lua/runtime.go:256), which
		// is the right thing for MCP — operators want their print()
		// landing in the terminal where they invoked the tool.
		return luaScriptErrorResult(lua.SurfaceLuaEval, "<inline>", "",
			runtime.ErrorFrames(), nil, err), nil
	}

	result := output.String()
	if result == "" {
		result = "Script executed successfully (no output)"
	}

	return textResult(result), nil
}

func (h luaHandler) handleLuaRun(ctx context.Context, req *mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	in := newToolRequest(req)
	path := strings.TrimSpace(in.GetString("path", ""))
	if path == "" {
		return h.listScripts(), nil
	}

	// Security: Validate path is local (no "..", no absolute paths)
	if !filepath.IsLocal(path) {
		return errorResult("script path must be a local path (no '..' or absolute paths allowed)"), nil
	}

	// Security: Must have .lua extension
	if !strings.HasSuffix(path, ".lua") {
		return errorResult("script must have .lua extension"), nil
	}

	// Parse args if provided
	args := in.GetStringSlice("args", nil)

	projectRoot := h.projectRoot

	// Security: Scripts must be in the scripts/ directory
	// Use os.Root for traversal-resistant path access
	root, err := os.OpenRoot(projectRoot)
	if err != nil {
		return errorResult("cannot open project root: " + err.Error()), nil
	}
	defer root.Close()

	// Verify script exists using traversal-resistant API
	scriptsRoot, err := root.OpenRoot(scriptsDir)
	if err != nil {
		return errorResult("scripts directory not found: " + err.Error()), nil
	}
	defer scriptsRoot.Close()

	// Read script content using traversal-resistant API to prevent symlink escapes
	scriptFile, err := scriptsRoot.Open(path)
	if err != nil {
		return errorResult(fmt.Sprintf("script not found: %s (scripts must be in the scripts/ directory)", path)), nil
	}
	defer scriptFile.Close()

	// Read script content
	scriptContent, err := io.ReadAll(scriptFile)
	if err != nil {
		return errorResult("cannot read script: " + err.Error()), nil
	}

	// Capture output
	var output bytes.Buffer

	// No capability grant — see the note in lua_eval above (TKT-YH52OM).
	// lua_run names a file under scripts/, but the CHOICE of file is the MCP
	// client's, so this inherits the same posture rather than the operator-shell
	// default `rela script` uses for the very same files.
	runtime, err := script.NewWriterRuntime(h.writeDeps, path,
		&output, lua.WithContext(ctx), lua.WithCache(h.cache))
	if err != nil {
		return errorResult("config error: " + err.Error()), nil
	}
	defer runtime.Close()

	// Use RunFileContent rather than RunString so the runtime wires
	// up chunk name, rela.args, and cache namespace identically to a
	// normal RunFile call. We read the bytes ourselves (via
	// os.OpenRoot above) specifically for traversal resistance; passing
	// them through RunFileContent keeps that while sharing all the
	// downstream invariants.
	//
	//nolint:contextcheck // ctx threaded via WithContext; see handleLuaEval note
	if err := runtime.RunFileContent(path, scriptContent, args); err != nil {
		// See note above: print() bypasses `output` for MCP runs.
		return luaScriptErrorResult(lua.SurfaceLuaRun,
			filepath.ToSlash(filepath.Join(scriptsDir, path)), projectRoot,
			runtime.ErrorFrames(), nil, err), nil
	}

	result := output.String()
	if result == "" {
		result = "Script executed successfully (no output)"
	}

	return textResult(result), nil
}

// luaScriptErrorResult builds an MCP CallToolResult carrying a JSON-
// encoded ScriptError envelope as the error message. We use
// NewToolResultError (not NewToolResultText) so the result's IsError
// flag stays true — clients keying off that flag still see the failure.
//
// projectRoot is "" for lua_eval (no script source on disk to slice);
// otherwise the source FS is rooted there so the envelope can include
// ±N lines around the failing line.
func luaScriptErrorResult(surface lua.Surface, envelopePath, projectRoot string,
	frames []lua.StackFrame, capturedOutput []byte, runErr error) *mcpgo.CallToolResult {
	in := lua.BuildInput{
		Surface:        surface,
		Path:           envelopePath,
		Frames:         frames,
		CapturedOutput: capturedOutput,
		Err:            runErr,
	}
	if projectRoot != "" {
		in.SourceFS = os.DirFS(projectRoot)
		// Frames coming from gopher-lua use the bare filename as Source;
		// re-prefix matching frames so they line up with envelopePath.
		bareName := strings.TrimPrefix(envelopePath, scriptsDir+"/")
		for i := range in.Frames {
			if in.Frames[i].Path == bareName {
				in.Frames[i].Path = envelopePath
			}
		}
	}
	se := lua.BuildScriptError(in)
	body, err := json.Marshal(se)
	if err != nil { // coverage-ignore: defensive: ScriptError holds only strings/ints/string-slices built from this
		// function's frame/err inputs (Args is unset here), so json.Marshal cannot fail.
		return errorResult(fmt.Sprintf("Lua error: %v (also failed to marshal envelope: %v)", se.Error(), err))
	}
	return errorResult(string(body))
}

// listScripts answers lua_run without a path: the .lua files under scripts/.
// It is part of lua_run rather than a tool of its own because a tool costs
// context in every session, and the list is only ever wanted right before a
// run.
func (h luaHandler) listScripts() *mcpgo.CallToolResult {
	projectRoot := h.projectRoot

	// Only search the scripts/ directory (security restriction)
	scriptsPath := filepath.Join(projectRoot, scriptsDir)

	var scripts []string

	// Walk the scripts directory recursively to find all .lua files
	_ = filepath.WalkDir(scriptsPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return filepath.SkipDir // Skip directories with errors
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".lua") {
			return nil
		}

		// Get relative path from scripts directory
		relPath, _ := filepath.Rel(scriptsPath, path)
		if relPath != "" {
			scripts = append(scripts, relPath)
		}
		return nil
	})

	if len(scripts) == 0 {
		return textResult("No Lua scripts found in scripts/ directory")
	}

	var result strings.Builder
	result.WriteString("Available Lua scripts (in scripts/):\n")
	for _, script := range scripts {
		result.WriteString("  ")
		result.WriteString(script)
		result.WriteString("\n")
	}
	result.WriteString("\nPass one as path to run it.")

	return textResult(result.String())
}
