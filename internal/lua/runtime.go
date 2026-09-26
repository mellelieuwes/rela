// Package lua provides a Lua scripting runtime for rela with bindings
// to query entities, relations, and output results.
//
// The runtime is sandboxed: only safe Lua libraries are loaded (base, table,
// string, math, utf8, coroutine). The io, os, and debug libraries are NOT
// available to prevent filesystem access and code execution. File operations
// are only possible through the provided rela.write_file() function which
// validates paths are within the project root.
package lua

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"

	"github.com/Sourcehaven-BV/rela/internal/ai"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/filter"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/search"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/tracer"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

// Default values for Lua API functions.
const (
	defaultSearchLimit = 20
	// argPosCreateEntityID is the position of the optional ID parameter in create_entity
	// (type=1, properties=2, content=3, id=4, opts=5).
	argPosCreateEntityID = 4
	// argPosCreateEntityOpts is the position of the optional options table in
	// create_entity, carrying the content state to write.
	argPosCreateEntityOpts = 5
	// argPosCreateRelationOpts is the position of the optional options table in
	// create_relation (from=1, type=2, to=3, opts=4).
	argPosCreateRelationOpts = 4
)

// stripShebang removes a shebang line from the beginning of Lua code.
// This allows scripts to be directly executable from the command line
// (e.g., #!/usr/bin/env -S rela script). If the code starts with "#!",
// the first line is replaced with a blank line to preserve line numbers
// in error messages. A leading UTF-8 BOM is also stripped if present.
// Otherwise, the code is returned unchanged.
func stripShebang(code string) string {
	code = strings.TrimPrefix(code, "\xEF\xBB\xBF")
	if !strings.HasPrefix(code, "#!") {
		return code
	}
	idx := strings.Index(code, "\n")
	if idx == -1 {
		return ""
	}
	return code[idx:]
}

// Runtime wraps gopher-lua VM with rela bindings.
//
// The runtime is constructed via NewReader (read-only) or NewWriter (read-
// write). A read-only runtime has no mutation bindings (create/update/delete
// of entities and relations) registered at all; calling those from Lua raises
// a "attempt to call a nil value" error from the VM itself.
//
// TODO(TKT-N0IKN9): Runtime is a god-object (37 methods). Under the 40-method
// load line now; keep ratcheting this number down as bindings move out.
//
// 119 → 120 (TKT-ZF2DTV): reader(), the single choke point every read binding
// resolves its ACL-bound handle through. One method that makes six call sites
// unable to skip the gate is a good trade against this line.
//
// 120 → 105 (TKT-IG54YO): the seventeen rela.url helpers moved to urlHelpers.
// They needed nothing from the runtime — no store, no deps, no context, not
// even the Lua state, since each receives the *lua.LState it is called with —
// so they were only ever methods by habit. Grouping them in a FILE never
// helped: the linter counts methods per type, and so does anyone reading this
// struct's API. The remaining bindings are not free like this; they genuinely
// use deps and callerCtx, which is what makes them TKT-N0IKN9's job.
//
// 105 → 60 (TKT-4WBLG6): the markdown AST cluster moved to mdHelpers (42
// methods that used nothing but the Lua state) and mdEntityRefs (the one
// graph-coupled binding, now holding just meta + reader + ctx closure).
//
// 60 → 61 (TKT-XWZIOB): mail.send must be a method value: contextcheck follows
// a registration CLOSURE back to every NewReader/NewWriter call site and demands
// a context be threaded into runtime construction, which is not a thing that
// exists. ai.* and http.* register method values for the same reason. See
// registerMailModule.
//
// 61 → 46 (TKT-DOPCTI): the HTTP, cache, and AI clusters moved to
// httpBindings (stateless), cacheBindings (cacheStore + a scriptPath
// closure over the runtime's mutable field), and aiBindings (just the
// provider). The register* wiring seams stay here, as do the caps.AI /
// caps.HTTP gates in registerBindings.
//
// 46 → 38 (TKT-YVREQN): the elevation cluster moved to elevationBindings
// (the three elevated deps + the callerCtx closure — semantics verbatim),
// output/write_file/print to outputBindings (stdout, dirs, mode flags —
// all fixed once options apply), and schema introspection to
// schemaBindings (just the metamodel); sort_entities became a free
// function. This puts Runtime UNDER the 40-method load line; the
// directive stays as the pin that keeps it there.
//
//plimsoll:max-methods=38
type Runtime struct {
	L             *lua.LState
	deps          WriteDeps // EntityManager is nil on a reader runtime.
	stdout        io.Writer
	outputDir     string          // Directory where write_file can write (defaults to "output")
	timeout       time.Duration   // Execution timeout (0 = no timeout)
	parentCtx     context.Context //nolint:containedctx // cached parent ctx for lua-callback child-ctx propagation
	cancelTimeout context.CancelFunc
	params        map[string]string // rela.params values (used by action scripts)
	request       *Request          // rela.request; nil leaves the field absent (TKT-EFMRQM)
	secrets       map[string]string // rela.secrets values (from .rela/secrets.yaml)
	caps          Capabilities      // ambient capability grants; zero value denies all (TKT-YH52OM)
	isAction      bool              // true when running as an action (changes rela.output behavior)
	isDocument    bool              // document mode: rela.document populated, rela.output becomes a warning
	documentID    string            // data-entry.yaml documents: key, exposed as rela.document.id
	documentEntry string            // ID of the entity being rendered, exposed as rela.document.entry_id

	// listRender is set (non-nil Rows) only for a LIST document render — the
	// lists: export_render override. It rides on document mode rather than
	// introducing a third mode: a list render wants document mode's stdout
	// capture and its rela.output warning verbatim, and a parallel mode
	// would have to be OR-ed into every one of those guards forever.
	// Entity renders leave this zero, so rela.document carries no row
	// bindings there.
	listRender ListRenderContext
	aiProvider ai.Provider // nil means AI is not configured
	cache      cacheStore  // nil means rela.cache.* is not registered
	scriptPath string      // set by RunFile; empty for RunString/inline

	// mailSender backs mail.send. Nil means mail is not configured for this
	// project, which the binding reports as a typed not_configured error
	// rather than by being absent — see mail.go for why this one differs
	// from the ai/http capability gate.
	mailSender MailSender

	// out backs the output/filesystem bindings (rela.output, rela.write_file,
	// the print override). Built in newRuntime AFTER options apply, so it
	// snapshots stdout/outputDir/mode flags once they are final.
	out *outputBindings

	// principal is the identity this runtime runs as, exposed read-only as
	// rela.principal (TKT-5U6NRR). Set by [WithPrincipal] from the caller's
	// resolved principal; the zero value renders as {user:"", tool:""} which
	// the binding maps to the documented unknown fallback. The runtime does
	// NOT read it from the context itself — keeping principal resolution at
	// the caller avoids a context-flow the linter (contextcheck) would flag
	// and keeps the lua package free of ctx-identity plumbing.
	principal principal.Principal

	// errorFrames holds typed stack frames captured by the PCall message
	// handler on the most recent failed run. Read via ErrorFrames() after
	// PCall returns an error. Reset before every PCall.
	errorFrames []StackFrame
}

// cacheStore is the minimal contract the Lua cache bindings need from
// their backend. It's defined at the consumer (Runtime) so alternative
// implementations (a future disk-backed cache, a test fake) can drop in
// without touching wiring code. Kept unexported: callers still pass
// *Cache via WithCache and we keep the flexibility internally.
type cacheStore interface {
	get(namespacedKey string) ([]any, bool)
	set(namespacedKey string, values []any, ttl time.Duration)
	delete(namespacedKey string)
}

// Option configures a Runtime.
type Option func(*Runtime)

// DefaultTimeout is the default execution timeout for scripts.
// This prevents infinite loops and resource exhaustion.
const DefaultTimeout = 30 * time.Second

// WithTimeout sets the execution timeout for scripts.
// Default is 30 seconds. Set to 0 to disable timeout (not recommended).
func WithTimeout(d time.Duration) Option {
	return func(r *Runtime) {
		r.timeout = d
	}
}

// WithContext sets a parent context for the runtime. Cancellation of this
// context propagates into in-flight Lua operations (e.g. long-running loops
// or blocking calls from bindings). When combined with WithTimeout, the
// timeout is derived from this parent so canceling the parent also cancels
// the timeout-bound context.
//
// The parent ctx also carries values (Principal, triggered_by) into
// downstream Mutator calls — Lua write bindings derive their ctx from
// the parent so audit attribution flows naturally from caller →
// engine → Lua → Manager.
//
// Typical usage: pass cmd.Context() from a cobra RunE so that Ctrl+C
// interrupts script execution.
func WithContext(ctx context.Context) Option {
	return func(r *Runtime) {
		r.parentCtx = ctx
	}
}

// WithPrincipal sets the identity exposed read-only to scripts as
// rela.principal (TKT-5U6NRR). The caller resolves the principal (e.g.
// principal.From(ctx)) ONCE and passes the value here, so the lua package
// never reaches into the context for identity. Omitting this option leaves
// rela.principal as the unknown fallback.
func WithPrincipal(p principal.Principal) Option {
	return func(r *Runtime) {
		r.principal = p
	}
}

// callerCtx returns the parent context (or context.Background() if
// none was set). Used by write bindings to derive the ctx passed to
// the Mutator — so the caller's Principal and triggered_by flow
// through Lua into Manager.
func (r *Runtime) callerCtx() context.Context {
	if r.parentCtx != nil {
		return r.parentCtx
	}
	return context.Background()
}

// WithOutputDir sets the output directory for write_file.
// If the path is absolute, files will be written there directly.
// If relative, it's relative to the project root.
func WithOutputDir(dir string) Option {
	return func(r *Runtime) {
		r.outputDir = dir
	}
}

// WithParams sets the rela.params table contents for action scripts.
// Params are static key-value strings from the data-entry config.
func WithParams(params map[string]string) Option {
	return func(r *Runtime) {
		r.params = params
	}
}

// WithActionMode marks the runtime as running in action mode, which changes
// rela.output behavior (logs a warning instead of writing to stdout).
func WithActionMode() Option {
	return func(r *Runtime) {
		r.isAction = true
	}
}

// WithDocumentMode marks the runtime as running a data-entry document
// renderer. Populates the rela.document.{id, entry_id} table and sets
// rela.mode = "document" so scripts can branch on context; also changes
// rela.output behavior to emit a warning line (the captured stdout is the
// rendered document, so JSON noise in-band is almost certainly a mistake).
// documentID is the key under documents: in data-entry.yaml; entryID is
// the ID of the entity being rendered.
func WithDocumentMode(documentID, entryID string) Option {
	return func(r *Runtime) {
		r.isDocument = true
		r.documentID = documentID
		r.documentEntry = entryID
	}
}

// WithStandaloneDocumentMode marks the runtime as rendering a STANDALONE
// document — one declared without an `entity_type:` in data-entry.yaml, whose
// content is company-wide rather than about a single entity (TKT-M1AX6P). It
// sets document mode, so stdout capture and the rela.output warning behave
// exactly as for an entity render.
//
// Like [WithListDocumentMode] this is a SEPARATE constructor from
// [WithDocumentMode] rather than the latter called with an empty entryID:
// rela.document.entry_id must be Lua nil, and passing "" to express that is
// exactly the footgun registerContextBindings documents (empty string is
// truthy in Lua). Making the absence structural means no caller can get it
// wrong.
//
// Unlike [WithListDocumentMode] there are no row/query bindings — a standalone
// document has no driving list either. rela.document carries `id` alone.
func WithStandaloneDocumentMode(documentID string) Option {
	return func(r *Runtime) {
		r.isDocument = true
		r.documentID = documentID
	}
}

// WithListDocumentMode marks the runtime as rendering a LIST export (the
// `lists.<id>.export_render` override). It sets document mode, so stdout
// capture and the rela.output warning behave exactly as for an entity
// render, and additionally populates the row/query bindings on
// rela.document.
//
// It is a SEPARATE constructor from [WithDocumentMode] rather than extra
// optional arguments on it: a caller holding only WithDocumentMode cannot
// conjure a row provider, which keeps the typed-seam property that the
// script.Engine methods are the only way to enter either mode.
//
// entryID is deliberately not a parameter — a list render has no entry
// entity, and rela.document.entry_id stays absent (Lua nil) rather than
// empty-string. See registerContextBindings for why.
func WithListDocumentMode(documentID string, lrc ListRenderContext) Option {
	return func(r *Runtime) {
		r.isDocument = true
		r.documentID = documentID
		r.listRender = lrc
	}
}

// WithCapabilities declares which ambient capabilities the runtime may reach
// (TKT-YH52OM). Omitting it denies all of them — see [Capabilities] for why
// the default is closed rather than open.
//
// A ZERO-VALUE grant is a no-op, NOT a revocation: it leaves whatever
// [ReadDeps.Capabilities] supplied in place. The two are alternative ways to
// say the same thing, and "grant nothing" is already the default, so an empty
// option carries no information worth destroying a deps-carried grant over.
//
// This is not a stylistic choice — it fixes a real defect. Engine.execute
// passes this option UNCONDITIONALLY (plain ExecuteFile supplies
// Capabilities{}), so a straight assignment made every deps-carried grant
// vanish. The scheduler sets deps.Capabilities and calls ExecuteFile, so every
// scheduled task's `capabilities:` block was silently a no-op. Pinned by
// TestDepsCapabilitiesSurviveExecuteFile in internal/script.
//
// The failure mode this avoids is the dangerous direction: a grant that
// silently disappears is a broken feature, but it fails CLOSED. The reverse —
// an empty option that could not clear a grant when the caller MEANT to revoke
// one — does not arise, because no caller revokes: a runtime is built once, per
// execution, from a config that either named a capability or did not.
func WithCapabilities(c Capabilities) Option {
	return func(r *Runtime) {
		if !c.Any() {
			return
		}
		r.caps = c
	}
}

// WithSecrets sets the rela.secrets table contents.
// Secrets are loaded from .rela/secrets.yaml by the caller.
func WithSecrets(secrets map[string]string) Option {
	return func(r *Runtime) {
		r.secrets = secrets
	}
}

// WithAIProvider wires an AI provider into the runtime so the ai.* Lua
// bindings are functional. When omitted, ai.chat and ai.complete return
// a typed not_configured error.
func WithAIProvider(p ai.Provider) Option {
	return func(r *Runtime) {
		r.aiProvider = p
	}
}

// WithMailSender wires a mail transport into the runtime so mail.send
// delivers. When omitted (or passed nil) the `mail` global is STILL present
// and mail.send returns a typed not_configured error — see mail.go for why
// this differs from the ai/http capability gate.
//
// The sender is the same seam every other mail caller uses, so a script cannot
// reach a transport the operator did not configure.
func WithMailSender(s MailSender) Option {
	return func(r *Runtime) {
		r.mailSender = s
	}
}

// WithCache wires a process-wide cache into the runtime so the
// rela.cache.* Lua bindings are registered. When omitted (or passed
// nil), rela.cache.* is absent from the rela table — calling it from
// Lua raises "attempt to call a nil value" from the VM. The cache is
// namespaced by the runtime's script path (set by RunFile); inline or
// eval contexts that call rela.cache.* receive a fixed Lua error
// rather than sharing a nameless namespace.
func WithCache(c *Cache) Option {
	return func(r *Runtime) {
		if c != nil { // a typed nil in the interface would register the bindings
			r.cache = c
		}
	}
}

// NewReader creates a read-only Runtime with query/trace/search/output bindings.
// Mutation bindings (create/update/delete for entities and relations) are not
// registered; calling them from Lua raises "attempt to call a nil value".
//
// The Lua VM is sandboxed with only safe libraries loaded (no io, os, or debug).
func NewReader(d ReadDeps, stdout io.Writer, opts ...Option) *Runtime {
	return newRuntime(WriteDeps{ReadDeps: d}, stdout, false, opts...)
}

// NewWriter creates a read-write Runtime. All read bindings plus mutation
// bindings (create_entity, update_entity, delete_entity, create_relation,
// delete_relation) are registered.
//
// The Lua VM is sandboxed with only safe libraries loaded (no io, os, or debug).
func NewWriter(d WriteDeps, stdout io.Writer, opts ...Option) *Runtime {
	return newRuntime(d, stdout, true, opts...)
}

func newRuntime(deps WriteDeps, stdout io.Writer, allowWrites bool, opts ...Option) *Runtime {
	// Fail loud at construction: a writer runtime with no EntityManager
	// would register mutation bindings that nil-deref on first call.
	// Catching this here turns a silent runtime surprise into a build-time
	// or start-time panic with a clear message.
	if allowWrites && deps.EntityManager == nil {
		panic("lua.NewWriter: WriteDeps.EntityManager is required for a writer runtime")
	}

	// Create sandboxed Lua state - skip default libraries for security
	L := lua.NewState(lua.Options{
		SkipOpenLibs:  true,
		CallStackSize: 1024,      // Limit call stack depth to prevent stack overflow
		RegistrySize:  1024 * 64, // Limit registry size
	})

	// Load only safe libraries (NOT io, os, or debug)
	openSafeLibraries(L)

	r := &Runtime{
		L:         L,
		deps:      deps,
		stdout:    stdout,
		outputDir: defaultOutputDir,
		timeout:   DefaultTimeout,
		// Ambient capabilities default to whatever the deps declared
		// (TKT-YH52OM). Seeded BEFORE options so an explicit
		// WithCapabilities wins — deps carry the wiring-site default, the
		// option is a per-execution override. Both default to the zero
		// value, which grants nothing.
		caps: deps.Capabilities,
	}

	// Apply options
	for _, opt := range opts {
		opt(r)
	}

	// Snapshot the output-binding inputs now that options have fixed them.
	r.out = &outputBindings{
		stdout:      r.stdout,
		outputDir:   r.outputDir,
		projectRoot: deps.ProjectRoot,
		isAction:    r.isAction,
		isDocument:  r.isDocument,
	}

	// In captured-stdout contexts (document mode, action mode), redirect
	// Lua's base `print` through r.stdout. gopher-lua's default writes to
	// os.Stdout, which silently drops output from runtimes whose stdout
	// is a buffer. We override only for these modes so CLI / scheduler /
	// MCP / validation / automation scripts keep the default behavior —
	// users rely on print() reaching their terminal there.
	if r.isDocument || r.isAction {
		L.SetGlobal("print", L.NewFunction(r.out.luaPrint))
	}

	r.registerBindings(allowWrites)
	return r
}

// openSafeLibraries loads only safe Lua standard libraries.
// Excluded for security: io (file access), os (system commands), debug (internals).
func openSafeLibraries(ls *lua.LState) {
	// Libraries to load - order matters, LoadLibName must come first if used
	safeLibs := []struct {
		name string
		fn   lua.LGFunction
	}{
		{lua.BaseLibName, lua.OpenBase},
		{lua.TabLibName, lua.OpenTable},
		{lua.StringLibName, lua.OpenString},
		{lua.MathLibName, lua.OpenMath},
		{lua.CoroutineLibName, lua.OpenCoroutine},
		// NOT included: lua.IoLibName, lua.OsLibName, lua.DebugLibName, lua.ChannelLibName
	}

	for _, lib := range safeLibs {
		ls.Push(ls.NewFunction(lib.fn))
		ls.Push(lua.LString(lib.name))
		ls.Call(1, 0)
	}

	// Remove dangerous base functions that could bypass sandbox
	ls.SetGlobal("loadfile", lua.LNil)
	ls.SetGlobal("dofile", lua.LNil)
	ls.SetGlobal("load", lua.LNil)
	ls.SetGlobal("loadstring", lua.LNil)

	// Remove raw access functions that could bypass metamethod protections
	// and modify the rela module internals
	ls.SetGlobal("rawget", lua.LNil)
	ls.SetGlobal("rawset", lua.LNil)
	ls.SetGlobal("rawequal", lua.LNil)
	ls.SetGlobal("rawlen", lua.LNil)
	ls.SetGlobal("getmetatable", lua.LNil)
	ls.SetGlobal("setmetatable", lua.LNil)
}

// RunFile executes a Lua script file with arguments.
// Shebang lines (starting with #!) are automatically stripped.
//
// ErrorFrames returns the typed Lua stack frames captured by the message
// handler on the most recent failed Run call. Empty for a successful run,
// or for failures (compile errors, Go-side errors) where no PCall ran.
//
// Frames are ordered innermost-first: frame[0] is where the error was
// raised; frame[len-1] is the main chunk.
func (r *Runtime) ErrorFrames() []StackFrame {
	return r.errorFrames
}

// pcallWithCapture runs r.L.PCall(0, MultRet, ...) with a message handler
// that walks the live Lua stack via GetStack/GetInfo and stores typed
// frames on the runtime. This sidesteps regex-parsing the Object/StackTrace
// strings: we get real line numbers, function names, and source paths
// straight from gopher-lua's debug API.
//
// See https://github.com/yuin/gopher-lua/issues/46 for the rationale.
func (r *Runtime) pcallWithCapture() error {
	r.errorFrames = nil
	handler := r.L.NewFunction(func(L *lua.LState) int {
		r.errorFrames = collectStackFrames(L)
		// Pass the original error object (arg #1) through unchanged so
		// gopher-lua wraps it into ApiError as usual.
		L.Push(L.Get(1))
		return 1
	})
	return r.L.PCall(0, lua.MultRet, handler)
}

// collectStackFrames walks the live Lua stack and returns user-visible
// frames (skipping built-in G frames with no source). Capped at
// maxStackFrames; safe to call from a message handler.
func collectStackFrames(ls *lua.LState) []StackFrame {
	var frames []StackFrame
	for level := range maxStackFrames {
		dbg, ok := ls.GetStack(level)
		if !ok {
			break
		}
		if _, err := ls.GetInfo("Slunf", dbg, lua.LNil); err != nil {
			break
		}
		// Skip built-in/C frames — they have no source and a CurrentLine
		// of -1. The user only cares about their own Lua code.
		if dbg.Source == "" || dbg.CurrentLine <= 0 {
			continue
		}
		frames = append(frames, StackFrame{
			Path: dbg.Source,
			Line: dbg.CurrentLine,
			Func: dbg.Name,
		})
	}
	return frames
}

// RunFile sets the runtime's scriptPath (via filepath.Clean(path)) so
// the rela.cache.* bindings can namespace entries by script. Callers
// using RunString/inline code do not get this identity and any
// rela.cache.* call raises a Lua error.
func (r *Runtime) RunFile(path string, args []string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read script file: %w", err)
	}
	return r.RunFileContent(path, content, args)
}

// RunFileContent executes Lua code loaded from `path`, using the
// already-read `content`. This exists for callers that need traversal-
// resistant file reads (e.g. MCP's lua_run, which uses os.OpenRoot).
// Effects match RunFile: chunk name for errors, scriptPath for
// rela.cache.* namespacing, rela.args for script arguments. Shebangs
// are stripped.
func (r *Runtime) RunFileContent(path string, content []byte, args []string) error {
	// Set rela.args
	argsTable := r.L.NewTable()
	for i, arg := range args {
		argsTable.RawSetInt(i+1, lua.LString(arg))
	}
	relaTable, ok := r.L.GetGlobal("rela").(*lua.LTable)
	if !ok {
		return errors.New("rela module not initialized")
	}
	relaTable.RawSetString("args", argsTable)

	// Record the cleaned script path so rela.cache.* can namespace
	// entries. Cleaning (rather than absolutising) keeps two runs with
	// different CWDs sharing the same namespace when the relative path
	// is identical — the CLI already chdirs to project root, so this is
	// the stable identity.
	r.scriptPath = filepath.Clean(path)

	// Strip shebang if present
	code := stripShebang(string(content))

	r.applyTimeout()

	fn, err := r.L.Load(strings.NewReader(code), path)
	if err != nil {
		return fmt.Errorf("cannot compile script: %w", err)
	}

	r.L.Push(fn)
	return r.pcallWithCapture()
}

// RunString executes Lua code from a string.
// Shebang lines (starting with #!) are automatically stripped.
func (r *Runtime) RunString(code string) error {
	r.applyTimeout()
	cleaned := stripShebang(code)
	fn, err := r.L.LoadString(cleaned)
	if err != nil {
		return err
	}
	r.L.Push(fn)
	return r.pcallWithCapture()
}

// ErrNoReturnValue is returned by RunActionString when the script did not
// return a value. Action handlers can use errors.Is to check for this.
var ErrNoReturnValue = errors.New("script did not return a value")

// RunActionString executes Lua code as an action, returning the script's
// top-of-stack return value as a Go interface{}. Returns ErrNoReturnValue
// if the script did not return any values.
func (r *Runtime) RunActionString(code, name string) (any, error) {
	r.applyTimeout()

	cleaned := stripShebang(code)
	fn, err := r.L.Load(strings.NewReader(cleaned), name)
	if err != nil {
		return nil, fmt.Errorf("cannot compile script: %w", err)
	}

	// Record stack depth before so we can detect if the script returned anything
	topBefore := r.L.GetTop()
	r.L.Push(fn)
	if pcallErr := r.pcallWithCapture(); pcallErr != nil {
		return nil, pcallErr
	}
	topAfter := r.L.GetTop()

	if topAfter <= topBefore {
		// Script did not return a value
		return nil, ErrNoReturnValue
	}

	// Script may have returned multiple values; the first one is the primary return.
	// We read topBefore+1 (stack is 1-indexed; Get returns LNil for invalid indices).
	ret := r.L.Get(topBefore + 1)
	// Pop all returned values to leave the stack as we found it
	r.L.SetTop(topBefore)

	return luaValueToGo(ret), nil
}

// RunValidationString compiles `code` with `chunkname` as the chunk
// identity, runs it via the message-handler PCall, and returns the
// script's first return value as a raw gopher-lua LValue (LNil when
// the script returned nothing).
//
// Differs from RunActionString in that the return value is NOT
// converted via luaValueToGo — callers (validation rules) need direct
// access to *lua.LTable to walk return shapes that mix arrays and
// keyed entries (e.g. {message = ..., severity = ...}).
//
// The chunkname becomes the Path on each captured StackFrame, so a
// caller that uses chunkname == its envelope path gets matching frames
// without rewriting them. applyTimeout is invoked first so any
// WithTimeout / WithContext options on the runtime are honored.
func (r *Runtime) RunValidationString(code, chunkname string) (lua.LValue, error) {
	r.applyTimeout()

	cleaned := stripShebang(code)
	topBefore := r.L.GetTop()
	fn, err := r.L.Load(strings.NewReader(cleaned), chunkname)
	if err != nil {
		// Compile failures don't push anything, but be defensive so a
		// long-lived per-rule runtime can't accumulate orphaned LValues
		// across N entities (gopher-lua's stack ceiling is ~256).
		r.L.SetTop(topBefore)
		return nil, err
	}

	r.L.Push(fn)
	if pcallErr := r.pcallWithCapture(); pcallErr != nil {
		// PCall leaves the error message on the stack; reset to topBefore
		// so callers can invoke RunValidationString in a tight loop without
		// leaking stack slots per failed entity.
		r.L.SetTop(topBefore)
		return nil, pcallErr
	}
	topAfter := r.L.GetTop()

	if topAfter <= topBefore {
		return lua.LNil, nil
	}

	ret := r.L.Get(topBefore + 1)
	r.L.SetTop(topBefore)
	return ret, nil
}

// applyTimeout sets the execution timeout on the Lua state.
// Must be called before executing any Lua code.
//
// The derived context is rooted at r.parentCtx (if set) so that canceling
// the caller's context (e.g. Ctrl+C via a cobra command context) interrupts
// in-flight Lua execution. When no timeout is configured but a parent context
// is set, the parent is attached directly so cancellation still propagates.
func (r *Runtime) applyTimeout() {
	r.clearTimeout()
	parent := r.parentCtx
	if parent == nil {
		parent = context.Background()
	}
	if r.timeout > 0 {
		ctx, cancel := context.WithTimeout(parent, r.timeout)
		r.cancelTimeout = cancel
		r.L.SetContext(ctx)
		return
	}
	if r.parentCtx != nil {
		r.L.SetContext(r.parentCtx)
	}
}

// clearTimeout cancels any active timeout and removes the context from the Lua state.
func (r *Runtime) clearTimeout() {
	if r.cancelTimeout != nil {
		r.cancelTimeout()
		r.cancelTimeout = nil
	}
	r.L.RemoveContext()
}

// SetScriptPath records a cache namespace identity for callers that
// run via RunString or RunActionString (not RunFile/RunFileContent,
// which set it automatically). The "path" here is used purely as the
// rela.cache.* namespace — for inline-code callers that need a
// stable cache scope it's the caller's name for the script
// (e.g. "validations/<rule-name>"); for file-backed callers
// RunFileContent is preferred because it sets chunk name, args, and
// namespace in one step. The path is cleaned (filepath.Clean).
//
// SetScriptPath persists across subsequent RunString calls on the
// same Runtime. Call with "" to revert to inline/eval mode (cache
// calls raise). RunFile / RunFileContent override it.
func (r *Runtime) SetScriptPath(path string) {
	if path == "" {
		r.scriptPath = ""
		return
	}
	r.scriptPath = filepath.Clean(path)
}

// SetArgs sets the script arguments (rela.args) before execution.
func (r *Runtime) SetArgs(args []string) {
	argsTable := r.L.NewTable()
	for i, arg := range args {
		argsTable.RawSetInt(i+1, lua.LString(arg))
	}
	relaTable, ok := r.L.GetGlobal("rela").(*lua.LTable)
	if ok {
		relaTable.RawSetString("args", argsTable)
	}
}

// Close releases Lua VM resources.
func (r *Runtime) Close() {
	r.clearTimeout()
	r.L.Close()
}

// LState returns the underlying Lua state for setting globals.
func (r *Runtime) LState() *lua.LState {
	return r.L
}

// registerBindings sets up the rela module. When allowWrites is false, only
// read bindings are registered — mutation functions are absent from the rela.*
// table and calling them from Lua raises "attempt to call a nil value".
func (r *Runtime) registerBindings(allowWrites bool) {
	rela := r.L.NewTable()

	r.registerReadBindings(rela)
	if allowWrites {
		r.registerWriteBindings(rela)
		// rela.bypass_acl is registered ONLY when an elevated handle was wired
		// (an allow_acl_bypass automation action — TKT-D8T148). Absent BOTH, the
		// binding does not exist and a script cannot elevate.
		//
		// EITHER handle suffices (TKT-Y3JVFK). A document render is granted an
		// elevated READER and no elevated Mutator, so it can aggregate over rows
		// its caller cannot see while remaining unable to mutate: newElevatedHandle
		// omits the write methods when em is nil, so `admin.delete_entity` is
		// "attempt to call a nil value" rather than a guarded call. Reads-only is
		// structural, not a promise.
		if r.deps.ElevatedManager != nil || r.deps.ElevatedReader != nil {
			eb := &elevationBindings{
				em:       r.deps.ElevatedManager,
				er:       r.deps.ElevatedReader,
				recorder: r.deps.ElevationRecorder,
				ctxFn:    r.callerCtx,
			}
			r.L.SetField(rela, "bypass_acl", r.L.NewFunction(eb.luaBypassACL))
		}
	}
	r.registerContextBindings(rela)

	r.L.SetGlobal("rela", rela)

	// ai.* and http.* are registered ONLY when the capability was granted
	// (TKT-YH52OM). Absent the grant the global does not exist at all, so a
	// script cannot reach the provider or the network however it is written —
	// the same structural absence rela.bypass_acl relies on above.
	//
	// They were previously "always registered", which meant every runtime —
	// including read-only validation rules and document renders — could pair
	// outbound HTTP with rela.secrets and exfiltrate the whole secrets file.
	if r.caps.AI {
		r.registerAIModule()
	}
	if r.caps.HTTP {
		r.registerHTTPModule()
	}

	// Top-level crypto.* module — generic hashing primitives so a Lua action
	// can sign an outbound request to an HMAC-authenticated upstream. Always
	// registered; no configuration needed. Free function (see crypto.go) to keep
	// the Runtime method count flat.
	registerCryptoModule(r)

	// Top-level mail.* module. Registered UNCONDITIONALLY — including when no
	// sender is wired and when the runtime holds no `mail` capability — so a
	// script can feature-detect and so an unconfigured or unauthorized call
	// produces a message naming the actual problem rather than "attempt to
	// call a nil value".
	//
	// That is a registration decision, NOT an authorization one, and the
	// difference is why this line is not `if r.caps.Mail`. mail.send IS gated
	// (TKT-JVHSOZ): the binding checks r.caps.Mail itself and returns a
	// `denied` error table. ai/http above achieve the same denial by structural
	// absence, which suits them because "no http global" is a complete thought;
	// "no mail global" would leave an operator guessing between a missing
	// mail.yaml and a missing grant. mail.go argues both halves in full.
	registerMailModule(r)
}

// registerReadBindings installs read-only bindings on the rela table: entity
// and relation queries, graph traversal, output, schema introspection.
func (r *Runtime) registerReadBindings(rela *lua.LTable) {
	// Entity query functions
	r.L.SetField(rela, "get_entity", r.L.NewFunction(r.luaGetEntity))
	r.L.SetField(rela, "list_entities", r.L.NewFunction(r.luaListEntities))
	r.L.SetField(rela, "search", r.L.NewFunction(r.luaSearch))

	// Relation query functions
	r.L.SetField(rela, "get_relations", r.L.NewFunction(r.luaGetRelations))

	// Graph traversal
	r.L.SetField(rela, "trace_from", r.L.NewFunction(r.luaTraceFrom))
	r.L.SetField(rela, "trace_to", r.L.NewFunction(r.luaTraceTo))
	r.L.SetField(rela, "find_path", r.L.NewFunction(r.luaFindPath))

	// Output functions
	r.L.SetField(rela, "output", r.L.NewFunction(r.out.luaOutput))

	// Schema introspection
	sb := schemaBindings{meta: r.deps.Meta}
	r.L.SetField(rela, "get_entity_types", r.L.NewFunction(sb.luaGetEntityTypes))
	r.L.SetField(rela, "get_relation_types", r.L.NewFunction(sb.luaGetRelationTypes))

	// Utility functions
	r.L.SetField(rela, "sort_entities", r.L.NewFunction(luaSortEntities))
	r.L.SetField(rela, "days_since", r.L.NewFunction(luaDaysSince))
	r.L.SetField(rela, "today", lua.LString(time.Now().Format("2006-01-02")))
	// rela.now_unix: the current time as unix seconds, stamped once at runtime
	// construction (like rela.today above). The Lua sandbox excludes the `os`
	// library, so a script has no os.time(); this is the sanctioned way to read
	// the current time — needed, e.g., to date an HMAC-signed outbound request
	// (see examples/idp-sync.lua). An action runtime is built per invocation, so
	// the value is fresh each run.
	r.L.SetField(rela, "now_unix", lua.LNumber(time.Now().Unix()))

	// Date and RRULE utility functions
	registerDateHelpers(r.L, rela)

	// Markdown AST and generation helpers module (rela.md.*)
	r.registerMarkdownModule(rela)

	// JSON encode/decode helpers module (rela.json.*)
	r.registerJSONModule(rela)
}

// registerWriteBindings installs mutation bindings on the rela table.
// Graph mutations (create/update/delete for entities and relations) and
// filesystem writes (write_file) are all restricted to writer runtimes —
// readers (validation rules, etc.) have no way to mutate state of any kind.
func (r *Runtime) registerWriteBindings(rela *lua.LTable) {
	r.L.SetField(rela, "create_entity", r.L.NewFunction(r.luaCreateEntity))
	r.L.SetField(rela, "update_entity", r.L.NewFunction(r.luaUpdateEntity))
	r.L.SetField(rela, "delete_entity", r.L.NewFunction(r.luaDeleteEntity))
	r.L.SetField(rela, "create_relation", r.L.NewFunction(r.luaCreateRelation))
	r.L.SetField(rela, "delete_relation", r.L.NewFunction(r.luaDeleteRelation))
	// write_file is additionally capability-gated (TKT-YH52OM): a writer
	// runtime may mutate the graph without being entitled to touch the disk.
	if r.caps.WriteFile {
		r.L.SetField(rela, "write_file", r.L.NewFunction(r.out.luaWriteFile))
	}
}

// freezeTable returns a read-only proxy over data: reads fall through to the
// backing table via __index, while any assignment raises a Lua error via
// __newindex. The backing table holds the actual key/values and is not exposed
// directly, so even existing keys cannot be reassigned. Used for rela.principal
// (TKT-5U6NRR) to make its read-only contract enforced rather than conventional.
func freezeTable(ls *lua.LState, data *lua.LTable) *lua.LTable {
	proxy := ls.NewTable()
	mt := ls.NewTable()
	ls.SetField(mt, "__index", data)
	ls.SetField(mt, "__newindex", ls.NewFunction(func(s *lua.LState) int {
		s.RaiseError("attempt to modify a read-only table")
		return 0
	}))
	// __metatable hides the metatable from getmetatable() and blocks
	// setmetatable() from swapping it out to bypass the guard.
	ls.SetField(mt, "__metatable", lua.LString("read-only"))
	ls.SetMetatable(proxy, mt)
	return proxy
}

// registerContextBindings installs per-runtime context tables: args, params,
// secrets. Present on both reader and writer runtimes.
func (r *Runtime) registerContextBindings(rela *lua.LTable) {
	// Context
	r.L.SetField(rela, "args", r.L.NewTable()) // Will be set before running script

	// Params table (populated from WithParams option, used by action scripts)
	paramsTable := r.L.NewTable()
	for k, v := range r.params {
		r.L.SetField(paramsTable, k, lua.LString(v))
	}
	r.L.SetField(rela, "params", paramsTable)

	// rela.request (TKT-EFMRQM): the inbound HTTP request for a request-scoped
	// action. Absent unless the caller passed one.
	registerRequestBinding(r.L, rela, r.request)

	// Secrets table (populated from WithSecrets option, loaded from
	// .rela/secrets.yaml), FILTERED to the keys this runtime was granted
	// (TKT-YH52OM). An ungranted key is absent from the table rather than
	// present-and-empty, so a missing grant surfaces as a nil index at the use
	// site instead of as a silently-wrong credential.
	secretsTable := r.L.NewTable()
	for k, v := range r.caps.filterSecrets(r.secrets) {
		r.L.SetField(secretsTable, k, lua.LString(v))
	}
	r.L.SetField(rela, "secrets", secretsTable)

	// Principal table: the identity on whose behalf this runtime executes,
	// supplied by the caller via [WithPrincipal] (TKT-5U6NRR). Write-path
	// automations use this to attribute relations to the acting user — e.g.
	// stamping a `created-by` edge from the request principal (X-Rela-User) at
	// submit time, which the client cannot forge. When WithPrincipal was not
	// passed (CLI, unstamped contexts), the zero Principal renders as the
	// documented {user="unknown", tool="unknown"} fallback so scripts can
	// always read the field safely.
	//
	// READ-ONLY by design (PLAN-XKMJ AC13 spoofing defense). This field only
	// *reads* the identity; it is NOT a write/attribution hook. Audit
	// attribution always derives from callerCtx() inside the write bindings,
	// never from this table — so even a script that mutates a local copy
	// cannot forge who a write is recorded as. To make the read-only contract
	// enforced rather than conventional, the table is frozen: assigning to it
	// raises a Lua error. (`rela.audit*` / `with_principal` / `with_triggered_by`
	// remain absent — those WOULD be rewrite vectors.)
	// "unknown" mirrors the fallback principal.From returns for an unstamped
	// context, so a script reads the same value whether identity was absent or
	// simply not passed via WithPrincipal.
	pUser, pTool := r.principal.User, r.principal.Tool
	if pUser == "" {
		pUser = "unknown"
	}
	if pTool == "" {
		pTool = "unknown"
	}
	principalTable := r.L.NewTable()
	r.L.SetField(principalTable, "user", lua.LString(pUser))
	r.L.SetField(principalTable, "tool", lua.LString(pTool))
	r.L.SetField(rela, "principal", freezeTable(r.L, principalTable))

	// rela.cache.{get,set,memoize} when a cache is wired. The binding
	// itself guards against inline/eval contexts (empty scriptPath) by
	// raising a Lua error on any call, so a runtime with a cache but
	// no script path still behaves safely.
	r.registerCacheBindings(rela)

	// rela.url submodule — typed URL builders for the SPA's routes.
	r.registerURLModule(rela)

	// Document-mode context: rela.mode + rela.document.*.
	// Only populated when WithDocumentMode, WithStandaloneDocumentMode or
	// WithListDocumentMode was applied. In every other context rela.mode and
	// rela.document are absent (Lua nil), so a script that branches on them
	// sees nil outside document renders.
	if r.isDocument {
		r.L.SetField(rela, "mode", lua.LString("document"))
		docTable := r.L.NewTable()
		r.L.SetField(docTable, "id", lua.LString(r.documentID))

		// entry_id is set ONLY when there is a real entry entity. An entity
		// render always has one (the caller path-validates it before we get
		// here), so this is byte-identical to the previous unconditional
		// set; a LIST render and a STANDALONE document render have none and
		// must see Lua nil, never "".
		// Empty-string would be the worse lie: it is truthy in Lua, so the
		// idiomatic `if rela.document.entry_id then rela.get_entity(...)`
		// guard would pass and then raise ("entity ID cannot be empty"),
		// and `entry_id or default` would yield "" instead of the default.
		if r.documentEntry != "" {
			r.L.SetField(docTable, "entry_id", lua.LString(r.documentEntry))
		}

		// List-render bindings. Kept as closures over the provider rather
		// than *Runtime methods on purpose: Runtime is pinned at the
		// plimsoll load line (max-methods=120), and these would push it
		// over. They are also genuinely per-render state, not runtime
		// behavior.
		if r.listRender.Rows != nil {
			registerListDocumentFields(r.L, docTable, r.listRender)
		}

		r.L.SetField(rela, "document", docTable)
	}
}

// registerListDocumentFields populates the LIST-render half of
// rela.document: the query context plus the lazy row accessors.
//
// A plain function rather than a *Runtime method because it needs nothing
// from the Runtime — the LState and the render context are the whole input.
// (It also keeps Runtime off its plimsoll load line, which sits at exactly
// max-methods=120. That is a consequence, not the reason: the fix for a full
// Runtime is to take fields OFF it — see TKT-N0IKN9 — not to route new
// methods around the counter.)
//
// Rows are materialized ONE AT A TIME. A 5000-row export therefore holds a
// single Lua entity table at a time rather than 5000 (each of which is a
// table plus a properties sub-table plus two closures). That is what makes
// the caller's existing row cap the only bound needed here — no second,
// separately-tuned cap for the script path.
func registerListDocumentFields(ls *lua.LState, docTable *lua.LTable, lrc ListRenderContext) {
	rows := lrc.Rows
	q := lrc.Query

	ls.SetField(docTable, "list_id", lua.LString(lrc.ListID))
	ls.SetField(docTable, "entity_type", lua.LString(q.EntityType))
	// count is ground truth (the rows the script can actually reach); total is
	// the caller's pre-cap count, and truncated is derived from the two rather
	// than stored, so those three can't drift apart.
	//
	// A caller that under-reports Total would still be incoherent, so clamp:
	// total can never be less than the rows we are about to hand over. That
	// makes "total >= count" an invariant of what the script sees rather than
	// a promise about what every caller passes.
	count := rows.Len()
	total := max(q.Total, count)
	ls.SetField(docTable, "count", lua.LNumber(count))
	ls.SetField(docTable, "total", lua.LNumber(total))
	ls.SetField(docTable, "truncated", lua.LBool(total > count))

	// The resolved request context, frozen so "read-only" is enforced
	// rather than conventional (same treatment rela.principal gets).
	queryTable := ls.NewTable()
	// Always set, even when empty. Unlike entry_id — where absence is
	// semantically right because a list HAS no entry entity — "no search
	// term" is a representable value, so the empty string is the honest
	// answer. Omitting it made `"Search: " .. rela.document.query.q` work on
	// every filtered export and hard-error on every unfiltered one.
	ls.SetField(queryTable, "q", lua.LString(q.Q))
	filters := ls.NewTable()
	for k, v := range q.Filters {
		ls.SetField(filters, k, lua.LString(v))
	}
	ls.SetField(queryTable, "filters", freezeTable(ls, filters))
	sort := ls.NewTable()
	for i, s := range q.Sort {
		spec := ls.NewTable()
		ls.SetField(spec, "property", lua.LString(s.Property))
		ls.SetField(spec, "direction", lua.LString(s.Direction))
		sort.RawSetInt(i+1, freezeTable(ls, spec))
	}
	ls.SetField(queryTable, "sort", freezeTable(ls, sort))
	ls.SetField(docTable, "query", freezeTable(ls, queryTable))

	// row(i) -> entity table | nil. 1-based, per Lua convention.
	ls.SetField(docTable, "row", ls.NewFunction(func(s *lua.LState) int {
		i := s.CheckInt(1)
		e := rows.At(i - 1)
		if e == nil {
			s.Push(lua.LNil)
			return 1
		}
		s.Push(EntityToTable(s, e))
		return 1
	}))

	// rows() -> stateful iterator, for `for _, row in rela.document.rows() do`.
	// Each CALL to rows() mints a fresh cursor, so a script may walk the set
	// more than once; each walk re-materializes its tables. That is the
	// deliberate trade for flat memory — CPU, not RAM. Do not memoize the
	// materialized tables here; that would reintroduce exactly the O(n)
	// retention the laziness exists to avoid.
	ls.SetField(docTable, "rows", ls.NewFunction(func(s *lua.LState) int {
		i := 0
		s.Push(s.NewFunction(func(inner *lua.LState) int {
			if i >= rows.Len() {
				inner.Push(lua.LNil)
				return 1
			}
			e := rows.At(i)
			i++
			inner.Push(lua.LNumber(i))
			inner.Push(EntityToTable(inner, e))
			return 2
		}))
		return 1
	}))
}

// reader returns the read-out handle, raising when it is absent.
//
// Absent means DENY, never "fall back to the raw store" (RR-X9NVHI): a
// wiring site that forgets to supply a reader must fail loudly rather than
// silently hand a script ungated access to the graph. Every read binding
// funnels through here so the check cannot be forgotten at one of them.
func (r *Runtime) reader(ls *lua.LState, binding string) (EntityReader, bool) {
	if r.deps.VisibleReader == nil {
		ls.RaiseError("%s: no reader is configured for this runtime", binding)
		return nil, false
	}
	return r.deps.VisibleReader, true
}

// luaGetEntity implements rela.get_entity(id) -> table|nil
func (r *Runtime) luaGetEntity(ls *lua.LState) int {
	id := ls.CheckString(1)
	if id == "" {
		ls.RaiseError("entity ID cannot be empty")
		return 0
	}
	rd, ok := r.reader(ls, "rela.get_entity")
	if !ok {
		return 0
	}

	e, err := rd.GetEntity(r.callerCtx(), id)
	if err != nil {
		ls.Push(lua.LNil)
		return 1
	}

	ls.Push(EntityToTable(ls, e))
	return 1
}

// luaListEntities implements rela.list_entities(type, filter?) -> table
func (r *Runtime) luaListEntities(ls *lua.LState) int {
	entityType := ls.CheckString(1)
	if entityType == "" {
		ls.RaiseError("entity type cannot be empty")
		return 0
	}
	filterExpr, opts, err := listEntitiesArgs(ls)
	if err != nil {
		ls.RaiseError("rela.list_entities: %s", err.Error())
		return 0
	}
	rd, ok := r.reader(ls, "rela.list_entities")
	if !ok {
		return 0
	}

	// The bound is applied by stopping the iterator, not by slicing a
	// materialized set: rows past the limit are never loaded, redacted, or
	// turned into Lua tables. On fsstore that is a saved file read and parse
	// per skipped row.
	//
	// It bounds rows EXAMINED. With a property filter (which the store cannot
	// express, so it runs here) a full page in can be fewer rows out, and the
	// caller cannot tell "that is everything" from "the rest was filtered".
	// Resolving that needs the cursor -- DEC-IYHLNF stage 2.
	entities := make([]*entity.Entity, 0, min(opts.limit, initialRowCapacity))
	for e, err := range rd.ListEntities(r.callerCtx(), store.EntityQuery{Type: entityType}) {
		if err != nil {
			// RAISE, never break-and-return-what-we-have (TKT-FVQ4). A short
			// list is indistinguishable from a genuinely short result, so
			// swallowing this hands the script a wrong answer that looks right.
			ls.RaiseError("rela.list_entities: %s", err.Error())
			return 0
		}
		entities = append(entities, e)
		if len(entities) >= opts.limit {
			break
		}
	}

	// Apply filter if provided
	if filterExpr != "" {
		f, err := filter.Parse(filterExpr)
		if err != nil {
			ls.RaiseError("invalid filter: %s", err.Error())
			return 0
		}

		entityDef, found := r.deps.Meta.GetEntityDef(entityType)
		if !found {
			ls.RaiseError("unknown entity type: %s", entityType)
			return 0
		}

		filters := []*filter.Filter{f}
		filtered := make([]*entity.Entity, 0)
		for _, e := range entities {
			record := filter.Record{ID: e.ID, Type: e.Type, Properties: e.Properties}
			match, err := filter.MatchAll(record, filters, entityDef, r.deps.Meta)
			if err != nil {
				ls.RaiseError("filter error: %s", err.Error())
				return 0
			}
			if match {
				filtered = append(filtered, e)
			}
		}
		entities = filtered
	}

	result := ls.NewTable()
	for i, e := range entities {
		result.RawSetInt(i+1, EntityToTable(ls, e))
	}
	ls.Push(result)
	return 1
}

// luaGetRelations implements rela.get_relations(opts?) -> table
// opts can have: from, type, to — each an optional string.
func (r *Runtime) luaGetRelations(ls *lua.LState) int {
	// Shares relationQuery with the elevated admin.get_relations so the two
	// surfaces cannot drift on what a filter means (RR-D7KXKV). A non-string
	// option raises here as it does there: silently dropping it turns a scoped
	// question into a whole-graph one that the script reads as scoped.
	q, err := relationQuery(ls)
	if err != nil {
		ls.RaiseError("rela.get_relations: %s", err.Error())
		return 0
	}

	rd, ok := r.reader(ls, "rela.get_relations")
	if !ok {
		return 0
	}

	// Peer-gated (RR-7GDT1Y): the reader drops relations whose FROM or TO
	// the caller cannot see, so an explicit opts.from can legitimately
	// return fewer rows than the graph holds. An empty result means "no
	// edges you may see", not "no edges".
	result := ls.NewTable()
	idx := 1
	for rel, err := range rd.ListRelations(r.callerCtx(), q) {
		if err != nil {
			break
		}
		result.RawSetInt(idx, relationToTable(ls, rel))
		idx++
	}
	ls.Push(result)
	return 1
}

// luaTraceFrom implements rela.trace_from(id, depth?) -> table|nil
func (r *Runtime) luaTraceFrom(ls *lua.LState) int {
	id := ls.CheckString(1)
	if id == "" {
		ls.RaiseError("entity ID cannot be empty")
		return 0
	}
	maxDepth := ls.OptInt(2, 0)

	trace := r.deps.Tracer.TraceFrom(r.callerCtx(), id, maxDepth)
	if trace == nil {
		ls.Push(lua.LNil)
		return 1
	}
	ls.Push(traceResultToTable(ls, trace))
	return 1
}

// luaTraceTo implements rela.trace_to(id, depth?) -> table|nil
func (r *Runtime) luaTraceTo(ls *lua.LState) int {
	id := ls.CheckString(1)
	if id == "" {
		ls.RaiseError("entity ID cannot be empty")
		return 0
	}
	maxDepth := ls.OptInt(2, 0)

	trace := r.deps.Tracer.TraceTo(r.callerCtx(), id, maxDepth)
	if trace == nil {
		ls.Push(lua.LNil)
		return 1
	}
	ls.Push(traceResultToTable(ls, trace))
	return 1
}

// EntityToTable converts an entity.Entity to a Lua table.
// The returned table has a prop(name, default) method.
// Exported for use by workspace automation execution.
func EntityToTable(ls *lua.LState, e *entity.Entity) *lua.LTable {
	t := ls.NewTable()
	t.RawSetString("id", lua.LString(e.ID))
	t.RawSetString("type", lua.LString(e.Type))
	t.RawSetString("content", lua.LString(e.Content))
	// The content state this record is, '' for a faceless type (BUG-G2BASF).
	// Always present, so a script tests the value rather than its existence.
	//
	// `id` stays BARE. A script that concatenates them gets the address the
	// write path accepts (`rela.update_entity("POL-1@concept", ...)` — see
	// entitymanager.PatchEntity); one that ignores the face reads and writes
	// exactly as it did before faces existed.
	t.RawSetString("face", lua.LString(string(e.Face)))

	// The content state this row was read at, empty on the default face.
	// ALWAYS present, like mod_time and redacted below, so a script never
	// needs `e.face or "..."` — the shape of the table does not depend on
	// the data in it.
	//
	// Not redacted, deliberately: Face is not a property, so visibility.Redact
	// does not cover it. The row-level gate already decided whether this
	// script may see the row at all; given that it may, the coordinate the row
	// was fetched at discloses nothing the successful read did not. Same
	// standing as `type` above.
	t.RawSetString("face", lua.LString(e.Face.String()))

	// Add modification time as ISO 8601 string (empty if zero)
	if !e.UpdatedAt.IsZero() {
		t.RawSetString("mod_time", lua.LString(e.UpdatedAt.Format(time.RFC3339)))
	} else {
		t.RawSetString("mod_time", lua.LString(""))
	}

	props := ls.NewTable()
	for k, v := range e.Properties {
		props.RawSetString(k, GoToLuaValue(ls, v))
	}
	t.RawSetString("properties", props)

	// Names withheld by field-level ACL, as a set so a script can both
	// iterate it and test membership. Always present (empty when nothing
	// was hidden) — on ungated runtimes (CLI, MCP, docs) it is always
	// empty because no policy was evaluated. Values are NOT here; they
	// were stripped from properties above.
	redacted := ls.NewTable()
	for _, name := range e.Redacted {
		redacted.RawSetString(name, lua.LTrue)
	}
	t.RawSetString("redacted", redacted)

	// Add prop(name, default) method via a function field
	t.RawSetString("prop", ls.NewFunction(luaEntityProp))

	// Add is_redacted(name) method to distinguish withheld from unset
	t.RawSetString("is_redacted", ls.NewFunction(luaEntityIsRedacted))

	// Add strip_prefix() method to get ID without type prefix
	t.RawSetString("strip_prefix", ls.NewFunction(luaEntityStripPrefix))

	return t
}

// luaEntityProp implements entity:prop(name, default) -> value
// Returns the property value or the default if not set/empty.
func luaEntityProp(ls *lua.LState) int {
	// Get self (the entity table) - first argument in method call
	self := ls.CheckTable(1)
	name := ls.CheckString(2)
	defaultVal := ls.Get(3) // optional, can be nil

	// Get properties table
	propsVal := self.RawGetString("properties")
	props, ok := propsVal.(*lua.LTable)
	if !ok {
		ls.Push(defaultVal)
		return 1
	}

	// Get the property value
	val := props.RawGetString(name)

	// Return default if nil or empty string
	if val == lua.LNil {
		ls.Push(defaultVal)
		return 1
	}
	if str, ok := val.(lua.LString); ok && string(str) == "" {
		ls.Push(defaultVal)
		return 1
	}

	ls.Push(val)
	return 1
}

// luaEntityIsRedacted implements entity:is_redacted(name) -> bool
// Reports whether field-level ACL withheld the named property from the
// reading principal, letting a script render "[redacted]" rather than a
// blank that reads as "never set".
//
// False means "not withheld", which on an ungated runtime (CLI, MCP,
// docs) is every property — those paths evaluate no policy.
func luaEntityIsRedacted(ls *lua.LState) int {
	self := ls.CheckTable(1)
	name := ls.CheckString(2)

	redactedVal := self.RawGetString("redacted")
	redacted, ok := redactedVal.(*lua.LTable)
	if !ok {
		ls.Push(lua.LFalse)
		return 1
	}

	ls.Push(lua.LBool(lua.LVAsBool(redacted.RawGetString(name))))
	return 1
}

// luaEntityStripPrefix implements entity:strip_prefix() -> string
// Returns the entity ID with the type prefix removed (e.g., "GUIDE-foo" -> "foo").
func luaEntityStripPrefix(ls *lua.LState) int {
	self := ls.CheckTable(1)
	idVal := self.RawGetString("id")

	id, ok := idVal.(lua.LString)
	if !ok {
		ls.Push(lua.LString(""))
		return 1
	}

	// Strip prefix: find first hyphen and return everything after it
	idStr := string(id)
	for i, c := range idStr {
		if c == '-' {
			ls.Push(lua.LString(idStr[i+1:]))
			return 1
		}
	}

	// No hyphen found, return as-is
	ls.Push(id)
	return 1
}

// relationToTable converts an entity.Relation to a Lua table.
func relationToTable(ls *lua.LState, rel *entity.Relation) *lua.LTable {
	t := ls.NewTable()
	t.RawSetString("from", lua.LString(rel.From))
	t.RawSetString("type", lua.LString(rel.Type))
	t.RawSetString("to", lua.LString(rel.To))
	// The SOURCE's content state; empty on the default face and on every
	// identity-scoped edge. Always present, for the reason EntityToTable's
	// `face` is. There is no to_face: targets are faceless by construction.
	t.RawSetString("from_face", lua.LString(rel.FromFace.String()))
	// The relation body. Always present so `content` is not write-only: the
	// create binding accepts it in the options table, and a key a script can
	// set but never read back is a trap.
	t.RawSetString("content", lua.LString(rel.Content))

	if len(rel.Properties) > 0 {
		props := ls.NewTable()
		for k, v := range rel.Properties {
			props.RawSetString(k, GoToLuaValue(ls, v))
		}
		t.RawSetString("properties", props)
	}

	return t
}

// traceResultToTable converts a trace result tree to a Lua table.
func traceResultToTable(ls *lua.LState, trace *tracer.TraceResult) *lua.LTable {
	t := ls.NewTable()
	t.RawSetString("id", lua.LString(trace.ID))
	t.RawSetString("type", lua.LString(trace.Type))
	t.RawSetString("title", lua.LString(trace.Title))
	t.RawSetString("depth", lua.LNumber(trace.Depth))
	t.RawSetString("relation", lua.LString(trace.Relation))
	t.RawSetString("incoming", lua.LBool(trace.Incoming))

	// Convert children recursively
	children := ls.NewTable()
	for i, child := range trace.Children {
		children.RawSetInt(i+1, traceResultToTable(ls, child))
	}
	t.RawSetString("children", children)

	return t
}

// GoToLuaValue converts a Go value to a Lua value.
// Exported for use by workspace automation execution.
func GoToLuaValue(ls *lua.LState, v any) lua.LValue {
	if v == nil {
		return lua.LNil
	}
	switch val := v.(type) {
	case string:
		return lua.LString(val)
	case int:
		return lua.LNumber(val)
	case int64:
		return lua.LNumber(val)
	case float64:
		return lua.LNumber(val)
	case bool:
		return lua.LBool(val)
	case []any:
		t := ls.NewTable()
		for i, item := range val {
			t.RawSetInt(i+1, GoToLuaValue(ls, item))
		}
		return t
	case []string:
		t := ls.NewTable()
		for i, item := range val {
			t.RawSetInt(i+1, lua.LString(item))
		}
		return t
	case map[string]any:
		t := ls.NewTable()
		for k, item := range val {
			t.RawSetString(k, GoToLuaValue(ls, item))
		}
		return t
	default:
		// Fallback: convert to string
		return lua.LString(fmt.Sprintf("%v", v))
	}
}

// luaValueToGo converts a Lua value to a Go value. Safe against cyclic
// tables: a second visit to a table already on the current ancestry
// chain yields the sentinel string "<cyclic reference>" rather than
// recursing forever. Without that guard, a self-referential Lua value
// (e.g. `t.self = t`) would overflow the Go stack and crash the entire
// process — not catchable from PCall. Every caller (rela.output,
// RunActionString, luaTableToGoMap, and the cache) inherits the guard.
func luaValueToGo(lv lua.LValue) any {
	return luaValueToGoSeen(lv, make(map[*lua.LTable]bool))
}

// luaNumberToGo converts a Lua number to the most faithful Go type.
// gopher-lua has a single float64-backed number type, but a value that
// is integral and fits in int64 round-trips better as an int64 — an
// entity ID, a ticket number, or epoch-nanos would otherwise lose its
// integer type (and re-serialize in exponential / trailing-.0 form). The
// reverse direction (GoToLuaValue) already preserves int/int64, so this
// closes the lossy leg. Non-integral or out-of-int64-range values stay
// float64. (Integers beyond 2^53 can't be held by the float64 LNumber in
// the first place, so this is type-faithful up to that ceiling.)
func luaNumberToGo(n lua.LNumber) any {
	f := float64(n)
	if f == math.Trunc(f) && f >= math.MinInt64 && f <= math.MaxInt64 {
		return int64(f)
	}
	return f
}

func luaValueToGoSeen(lv lua.LValue, seen map[*lua.LTable]bool) any {
	switch v := lv.(type) {
	case lua.LBool:
		return bool(v)
	case lua.LNumber:
		return luaNumberToGo(v)
	case lua.LString:
		return string(v)
	case *lua.LTable:
		return luaTableToGoSeen(v, seen)
	case *lua.LNilType:
		return nil
	default:
		return nil
	}
}

// maxArraySize is the maximum size for arrays converted from Lua tables.
const maxArraySize = 100000

// cyclicReferenceMarker is the string inserted in place of a table that
// appears a second time on its own ancestry chain during conversion.
// Callers that render JSON, compare structures, or use `rela.output`
// see this marker and can investigate, rather than hanging forever.
const cyclicReferenceMarker = "<cyclic reference>"

// luaTableToGoSeen converts a Lua table to a Go map or slice, using
// the ancestry set `seen` to terminate cycles. Callers wanting a fresh
// conversion should go through luaValueToGo, which bootstraps the
// seen-set.
func luaTableToGoSeen(t *lua.LTable, seen map[*lua.LTable]bool) any {
	if seen[t] {
		return cyclicReferenceMarker
	}
	seen[t] = true
	// Unmark on return so siblings that share a non-cyclic inner table
	// are not false-positive rejected. Cycle detection is per-ancestry,
	// not per-occurrence.
	defer delete(seen, t)

	// Check if it's an array (sequential positive integer keys starting at 1)
	isArray := true
	maxN := 0
	t.ForEach(func(k, _ lua.LValue) {
		if kn, ok := k.(lua.LNumber); ok {
			f := float64(kn)
			// Must be a positive integer within bounds
			if f != math.Floor(f) || f < 1 || f > maxArraySize {
				isArray = false
				return
			}
			n := int(f)
			if n > maxN {
				maxN = n
			}
		} else {
			isArray = false
		}
	})

	if isArray && maxN > 0 {
		arr := make([]any, maxN)
		t.ForEach(func(k, v lua.LValue) {
			if kn, ok := k.(lua.LNumber); ok {
				idx := int(kn) - 1
				if idx >= 0 && idx < maxN {
					arr[idx] = luaValueToGoSeen(v, seen)
				}
			}
		})
		return arr
	}

	// It's a map
	m := make(map[string]any)
	t.ForEach(func(k, v lua.LValue) {
		var key string
		switch kv := k.(type) {
		case lua.LString:
			key = string(kv)
		case lua.LNumber:
			key = fmt.Sprintf("%v", float64(kv))
		default:
			key = k.String()
		}
		m[key] = luaValueToGoSeen(v, seen)
	})
	return m
}

// luaSearch implements rela.search(query, limit?) -> table
// Performs full-text search across entity titles and properties.
func (r *Runtime) luaSearch(ls *lua.LState) int {
	query := ls.CheckString(1)
	if query == "" {
		ls.RaiseError("search query cannot be empty")
		return 0
	}

	limit := ls.OptInt(2, defaultSearchLimit)

	if r.deps.Searcher == nil {
		ls.RaiseError("search not available")
		return 0
	}
	rd, ok := r.reader(ls, "rela.search")
	if !ok {
		return 0
	}
	result := ls.NewTable()
	i := 1
	ctx := r.callerCtx()
	for hit, err := range r.deps.Searcher.Search(ctx, search.Query{Text: query, Limit: limit}) {
		if err != nil {
			ls.RaiseError("search error: %s", err.Error())
			return 0
		}
		// Fetch the full entity for the lua table (search hits are minimal).
		// This hydration is ALSO the gate: hits the caller may not read fail
		// here and are skipped, so no hidden entity or property reaches the
		// script. Whether the hit list itself is gated depends on the wiring;
		// see ReadDeps.Searcher.
		e, err := rd.GetEntity(ctx, hit.ID)
		if err != nil {
			// A denied hit arrives as ErrNotFound and is skipped silently —
			// that is the gate working. Anything else is a real fault, and
			// since redaction makes short results EXPECTED it would
			// otherwise be indistinguishable from normal policy behavior
			// (RR-QSP6X2).
			if !errors.Is(err, store.ErrNotFound) {
				slog.Warn("lua: rela.search: hit hydration failed",
					"id", hit.ID, "err", err)
			}
			continue
		}
		result.RawSetInt(i, EntityToTable(ls, e))
		i++
	}
	ls.Push(result)
	return 1
}

// luaCreateEntity implements
// rela.create_entity(type, properties, content?, id?, opts?) -> (entity, warnings).
//
// opts is an optional table; `{face = "draft"}` names the content state to
// create on a type declaring `faces:`, which such a type REQUIRES because it
// stores no row at the zero coordinate (BUG-HC6I2T). A faceless type must not
// name one. Both rules are the manager's (requireCreateFaceFor); this binding
// only makes the value expressible.
//
// Multi-return contract follows string.gsub semantics (NOT io.open):
// both return values can be non-nil simultaneously. The first value
// is the created entity; the second is a Lua array of DEC-HWZHA soft
// validation warnings, or nil when there are none. Hard errors
// (unknown entity type, bad ID prefix) still raise via RaiseError.
//
// Existing scripts that do `local e = rela.create_entity(...)`
// continue to work unchanged — the second return is silently dropped.
func (r *Runtime) luaCreateEntity(ls *lua.LState) int {
	entityType := ls.CheckString(1)
	if entityType == "" {
		ls.RaiseError("entity type cannot be empty")
		return 0
	}

	propsTable := ls.CheckTable(2)
	props := luaTableToGoMap(propsTable)

	content := ls.OptString(3, "")
	customID := ls.OptString(argPosCreateEntityID, "")

	opts, optErr := parseWriteOpts(ls, argPosCreateEntityOpts, createEntityOptKeys, createEntityOptSet)
	if optErr != nil {
		ls.RaiseError("create entity error: %s", optErr.Error())
		return 0
	}

	// The face rides CreateOptions only. createCore assigns e.Face = opts.Face,
	// and reading it off the caller's carrier entity instead is what BUG-HC6I2T
	// was.
	newE := &entity.Entity{
		ID:         customID,
		Type:       entityType,
		Properties: props,
		Content:    content,
	}
	ctx := r.callerCtx()
	result, err := r.deps.EntityManager.CreateEntity(
		ctx, newE, entity.CreateOptions{ID: customID, Face: opts.Face})
	if err != nil {
		ls.RaiseError("create entity error: %s", err.Error())
		return 0
	}

	ls.Push(writtenEntityTable(ctx, ls, r.deps.VisibleReader, result.Entity))
	ls.Push(WarningsToTable(ls, result.Warnings))
	return 2
}

// writtenEntityTable returns the entity a write produced, as the caller may
// read it. The manager's result is built from a raw read, so returning it
// as-is would hand the script properties its reader hides. The entity is read
// again through VisibleReader instead. When the caller cannot read it at all,
// only its id and type are returned: the caller named or created it, so those
// disclose nothing. Any other read error, and a missing reader, raise.
func writtenEntityTable(ctx context.Context, ls *lua.LState, rd EntityReader, written *entity.Entity) *lua.LTable {
	if rd == nil {
		ls.RaiseError("no reader is configured for this runtime")
		return nil
	}
	e, err := readFace(ctx, rd, written.ID, written.Face)
	switch {
	case err == nil:
		return EntityToTable(ls, e)
	case errors.Is(err, store.ErrNotFound):
		return EntityToTable(ls, &entity.Entity{ID: written.ID, Type: written.Type, Face: written.Face})
	default:
		ls.RaiseError("read back %s: %v", written.ID, err)
		return nil
	}
}

// readFace reads one face of id through rd.
func readFace(ctx context.Context, rd EntityReader, id string, face entity.Face) (*entity.Entity, error) {
	if face.IsDefault() {
		return rd.GetEntity(ctx, id)
	}
	for e, err := range rd.ListEntities(ctx, store.EntityQuery{IDs: []string{id}, FaceIn: []entity.Face{face}}) {
		if err != nil {
			return nil, err
		}
		if e != nil && e.ID == id {
			return e, nil
		}
	}
	return nil, store.ErrNotFound
}

// gateWriteTarget raises "entity not found" unless the caller may read every
// id a write names. The manager answers "forbidden" for an entity that exists
// but is hidden and "not found" for a missing one, so without this a script
// could probe for hidden ids. This matches the data-entry write path, which
// also reads the target through the gated reader first.
func gateWriteTarget(ctx context.Context, ls *lua.LState, rd EntityReader, ids ...string) bool {
	for _, id := range ids {
		if !visibility.Readable(ctx, rd, id) {
			ls.RaiseError("entity not found: %s", id)
			return false
		}
	}
	return true
}

// luaUpdateEntity implements rela.update_entity(id, properties, content?) -> (entity, warnings).
//
// Multi-return contract follows string.gsub semantics (NOT io.open):
// both return values can be non-nil simultaneously. The first value
// is the updated entity; the second is a Lua array of DEC-HWZHA soft
// validation warnings, or nil when there are none. Hard errors
// (entity not found, unknown type, bad ID prefix) still raise via
// RaiseError.
//
// Existing scripts that do `local e = rela.update_entity(...)`
// continue to work unchanged — the second return is silently dropped.
func (r *Runtime) luaUpdateEntity(ls *lua.LState) int {
	id := ls.CheckString(1)
	if id == "" {
		ls.RaiseError("entity ID cannot be empty")
		return 0
	}

	ctx := r.callerCtx()

	// A TARGETED write: name only what the script actually supplied and let
	// the manager merge it against the raw stored entity. The binding holds
	// no store handle at all, so the read-before-write that used to erase a
	// caller's hidden properties is now unreachable from here (TKT-80EWGM).
	var patch entity.Patch

	// Merge properties if provided
	if ls.GetTop() >= 2 && ls.Get(2).Type() == lua.LTTable {
		patch.Properties = luaTableToGoMap(ls.CheckTable(2))
	}

	// Update content if provided (nil means not provided, empty string clears content)
	if ls.GetTop() >= 3 && ls.Get(3).Type() != lua.LTNil {
		content := ls.CheckString(3)
		patch.Content = &content
	}

	if rd, ok := r.reader(ls, "rela.update_entity"); !ok || !gateWriteTarget(ctx, ls, rd, id) {
		return 0
	}

	result, err := r.deps.EntityManager.PatchEntity(ctx, id, patch)
	if err != nil {
		// Preserve the pre-TKT-80EWGM message for a missing entity: scripts
		// match on it. The check is STRUCTURAL (see [NotFoundError]) — a
		// text match would misreport an illegal-transition rejection, whose
		// message embeds the caller's own property value, as a 404.
		if isEntityNotFound(err) {
			ls.RaiseError("entity not found: %s", id)
			return 0
		}
		ls.RaiseError("update entity error: %s", err.Error())
		return 0
	}

	ls.Push(writtenEntityTable(ctx, ls, r.deps.VisibleReader, result.Entity))
	ls.Push(WarningsToTable(ls, result.Warnings))
	return 2
}

// WarningsToTable converts a slice of entity.Warning to a Lua
// table of {code, path, detail} sub-tables. Returns lua.LNil (NOT
// an empty table) when the slice is empty so scripts can use the
// `for _, w in ipairs(warnings or {})` pattern idiomatically and
// simple `if warnings then` truthiness checks work.
//
// This is the second return value of rela.update_entity and
// rela.create_entity, following string.gsub's "(value, count)"
// pattern — both returns can be non-nil simultaneously, and the
// second is additional success information, NOT an error indicator.
func WarningsToTable(ls *lua.LState, warnings []entity.Warning) lua.LValue {
	if len(warnings) == 0 {
		return lua.LNil
	}
	tbl := ls.NewTable()
	for _, w := range warnings {
		wt := ls.NewTable()
		ls.SetField(wt, "code", lua.LString(w.Code))
		ls.SetField(wt, "path", lua.LString(w.Path))
		ls.SetField(wt, "detail", lua.LString(w.Detail))
		tbl.Append(wt)
	}
	return tbl
}

// relationQuery reads the optional {from,type,to} options table off the Lua
// stack. An absent or non-table argument yields the zero query, which matches
// every relation.
//
// Shared by the gated rela.get_relations and the elevated admin.get_relations
// so the two cannot disagree about what a filter means. Callers differ only in
// what they do with the result: the gated reader additionally peer-gates the
// rows, the elevated one does not.
func relationQuery(s *lua.LState) (store.RelationQuery, error) {
	var q store.RelationQuery
	if s.GetTop() < 1 || s.Get(1).Type() != lua.LTTable {
		return q, nil
	}
	opts := s.CheckTable(1)
	fields := []struct {
		name string
		dst  *string
	}{
		{"from", &q.From}, {"type", &q.Type}, {"to", &q.To},
	}
	for _, f := range fields {
		switch v := opts.RawGetString(f.name).(type) {
		case *lua.LNilType:
			// Absent: no constraint on this field.
		case lua.LString:
			*f.dst = string(v)
		default:
			// A non-string is REJECTED rather than skipped. Silently dropping
			// it turns a mistyped filter (`{from = 12345}` when an id came
			// back as a number) into an unfiltered whole-graph edge dump that
			// the script reads as a filtered result. On the elevated path
			// nothing gates that at all; on the gated path peer-gating bounds
			// the rows to the caller's own view, so the disclosure is bounded
			// but the answer is still silently the wrong question.
			return q, fmt.Errorf(
				"option %q must be a string, got %s", f.name, v.Type())
		}
	}
	return q, nil
}

// luaDeleteEntity implements rela.delete_entity(id, cascade?) -> boolean
func (r *Runtime) luaDeleteEntity(ls *lua.LState) int {
	id := ls.CheckString(1)
	if id == "" {
		ls.RaiseError("entity ID cannot be empty")
		return 0
	}

	cascade := ls.OptBool(2, false)

	ctx := r.callerCtx()
	if rd, ok := r.reader(ls, "rela.delete_entity"); !ok || !gateWriteTarget(ctx, ls, rd, id) {
		return 0
	}
	if _, err := r.deps.EntityManager.DeleteEntity(ctx, id, cascade); err != nil {
		ls.RaiseError("delete entity error: %s", err.Error())
		return 0
	}

	ls.Push(lua.LTrue)
	return 1
}

// luaCreateRelation implements rela.create_relation(from, type, to, opts?) -> table
//
// opts is an optional table taking `face` (the SOURCE's content state, for a
// `scope: content` relation type — targets are faceless by construction) and
// `content` (the relation body).
//
// Position 4 previously accepted and silently IGNORED a content string: the
// old doc comment advertised it and the body never read it. It now carries
// the options table, so a 4th argument must be a table. That is a deliberate
// break, and a safe one — the argument was never in the documented signature,
// so no script could have relied on it doing anything.
func (r *Runtime) luaCreateRelation(ls *lua.LState) int {
	from := ls.CheckString(1)
	relType := ls.CheckString(2)
	to := ls.CheckString(3)

	if from == "" || relType == "" || to == "" {
		ls.RaiseError("from, type, and to are required")
		return 0
	}

	opts, optErr := parseWriteOpts(ls, argPosCreateRelationOpts, createRelationOptKeys, createRelationOptSet)
	if optErr != nil {
		ls.RaiseError("create relation error: %s", optErr.Error())
		return 0
	}

	ctx := r.callerCtx()
	if rd, ok := r.reader(ls, "rela.create_relation"); !ok || !gateWriteTarget(ctx, ls, rd, from, to) {
		return 0
	}
	rel, err := r.deps.EntityManager.CreateRelation(
		ctx, from, relType, to,
		entity.RelationOptions{FromFace: opts.Face, Content: opts.Content})
	if err != nil {
		ls.RaiseError("create relation error: %s", err.Error())
		return 0
	}

	ls.Push(relationToTable(ls, rel))
	return 1
}

// luaDeleteRelation implements rela.delete_relation(from, type, to) -> boolean
func (r *Runtime) luaDeleteRelation(ls *lua.LState) int {
	from := ls.CheckString(1)
	relType := ls.CheckString(2)
	to := ls.CheckString(3)

	if from == "" || relType == "" || to == "" {
		ls.RaiseError("from, type, and to are required")
		return 0
	}

	ctx := r.callerCtx()
	if rd, ok := r.reader(ls, "rela.delete_relation"); !ok || !gateWriteTarget(ctx, ls, rd, from, to) {
		return 0
	}
	if err := r.deps.EntityManager.DeleteRelation(ctx, from, relType, to); err != nil {
		ls.RaiseError("delete relation error: %s", err.Error())
		return 0
	}

	ls.Push(lua.LTrue)
	return 1
}

// luaFindPath implements rela.find_path(from, to) -> table
func (r *Runtime) luaFindPath(ls *lua.LState) int {
	from := ls.CheckString(1)
	to := ls.CheckString(2)

	if from == "" || to == "" {
		ls.RaiseError("from and to are required")
		return 0
	}

	path := r.deps.Tracer.FindPath(r.callerCtx(), from, to)
	if path == nil {
		ls.Push(lua.LNil)
		return 1
	}

	result := ls.NewTable()
	for i, step := range path {
		stepTable := ls.NewTable()
		stepTable.RawSetString("id", lua.LString(step.ID))
		stepTable.RawSetString("type", lua.LString(step.Type))
		stepTable.RawSetString("title", lua.LString(step.Title))
		stepTable.RawSetString("relation", lua.LString(step.Relation))
		result.RawSetInt(i+1, stepTable)
	}
	ls.Push(result)
	return 1
}

// luaTableToGoMap converts a Lua table to a Go map[string]interface{}.
func luaTableToGoMap(t *lua.LTable) map[string]any {
	m := make(map[string]any)
	t.ForEach(func(k, v lua.LValue) {
		var key string
		switch kv := k.(type) {
		case lua.LString:
			key = string(kv)
		case lua.LNumber:
			key = fmt.Sprintf("%v", float64(kv))
		default:
			key = k.String()
		}
		m[key] = luaValueToGo(v)
	})
	return m
}

// hoursPerDay converts the elapsed-hours difference luaDaysSince measures into
// whole days. This is absolute elapsed time, not calendar days, so a DST
// transition shifts the boundary by an hour — acceptable for a day counter.
const hoursPerDay = 24

// luaDaysSince implements rela.days_since(date_string) -> number
// Calculates the number of days between the given date and today.
// Accepts RFC3339 (2006-01-02T15:04:05Z07:00) or date-only (2006-01-02) formats.
// Returns -1 if the date cannot be parsed.
func luaDaysSince(ls *lua.LState) int {
	dateStr := ls.CheckString(1)
	if dateStr == "" {
		ls.Push(lua.LNumber(-1))
		return 1
	}

	t, err := parseDate(dateStr)
	if err != nil {
		ls.Push(lua.LNumber(-1))
		return 1
	}

	now := time.Now()
	days := int(now.Sub(t).Hours() / hoursPerDay)
	ls.Push(lua.LNumber(days))
	return 1
}
