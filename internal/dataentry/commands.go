package dataentry

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/natsort"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/project"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

// Protocol prefix for structured command output messages.
const commandOutputPrefix = "::rela::"

// cancelGrace is the time a SIGINTed command gets to clean up before SIGKILL.
// Shared between explicit /api/command-cancel and context-cancel (client disconnect).
const cancelGrace = 3 * time.Second

// ResolvedCommand is a command that has been matched to a specific page context.
//
// Field-for-field convertible to [v1.Command] (handleV1Commands does exactly
// that conversion), so the two must be changed together.
type ResolvedCommand struct {
	ID      string
	Label   string
	Confirm string
	Context string
}

// commandDenyReason explains why a command may not run. It is deliberately
// coarse: the wire 403 must not echo the required permission name or any
// policy data, mirroring [acl.Decision.Reason] ("never contains raw policy
// data so 403 bodies don't leak the full effective-role set").
const commandDenyReason = "not permitted to run this command"

// commandAuthorizer reports whether the principal on ctx may execute cmd.
//
// It is the SINGLE decision point (one instance held by commandHandler), called
// by both the exec handler and resolveCommands so the rendered button set and
// the enforced boundary cannot drift (DEC-EIHQSU). The 403 is the boundary; the
// resolve filter is a UX affordance.
//
// Which implementation a handler holds is decided ONCE at the wiring site from
// (ACL, bind, override) — see SelectCommandAuthorizer. This is the seam that
// distinguishes NopACL from ReadOnlyACL and loopback from network: the ctx read
// gate cannot, because readGateFromContext hands back the permissive nopReadGate
// under BOTH NopACL and ReadOnlyACL (RR-QWVG8Y / RR-CWWJGW). A guard written
// against the read gate alone therefore fails OPEN — so gatedAuthorizer is only
// ever constructed when a real *acl.Declarative is present.
type commandAuthorizer interface {
	Authorize(ctx context.Context, cmd CommandConfig) bool
}

// ungatedAuthorizer grants every command. It is the pre-ACL / NopACL behavior,
// selected only for a loopback bind (or the desktop/in-process server, which has
// no network listener) or when an operator explicitly opts in on a network bind
// via --allow-unauthenticated-commands. Named, not an anonymous `return true`,
// so every ungated command path is greppable.
type ungatedAuthorizer struct{}

func (ungatedAuthorizer) Authorize(context.Context, CommandConfig) bool { return true }

// denyAuthorizer refuses every command. It is the fail-closed choice:
// ReadOnlyACL, a network bind with no policy and no override, a gate that was
// required but could not be built, and the nil-authorizer wiring-bug fallback.
// The DenyReader analog for command execution.
type denyAuthorizer struct{}

func (denyAuthorizer) Authorize(context.Context, CommandConfig) bool { return false }

// gatedAuthorizer enforces a configured Declarative policy: a command needs its
// `permission:` set AND held by the principal (resolved per-request from the ctx
// read gate). `context: view` commands are denied outright — they have no
// fine-grained control yet (their payload is the whole traversal closure, not
// one entity; see TKT-MJ02AO), so a granted permission must not open the gate.
//
// LOAD-BEARING (RR-QWVG8Y): a gatedAuthorizer is only valid over a real
// Declarative policy. newGatedAuthorizer rejects a nil *acl.Declarative, and the
// wiring site never selects this impl for NopACL/ReadOnly/unknown — because on
// those paths readGateFromContext returns the permissive nopReadGate and the
// per-permission check would fail OPEN.
//
// The d field is not read by Authorize (the verdict comes from the per-request
// ctx read gate, which shares the same policy) — it exists so the type cannot be
// constructed without a non-nil policy. Do not drop it: an empty gatedAuthorizer{}
// selected on a NopACL path is exactly the fail-open this guard prevents.
type gatedAuthorizer struct {
	d *acl.Declarative
}

// newGatedAuthorizer builds a gatedAuthorizer, rejecting a nil policy so a
// wiring mistake can never produce a gate that silently grants via nopReadGate.
func newGatedAuthorizer(d *acl.Declarative) (gatedAuthorizer, error) {
	if d == nil {
		return gatedAuthorizer{}, errors.New("dataentry: newGatedAuthorizer: acl.Declarative is nil")
	}
	return gatedAuthorizer{d: d}, nil
}

func (gatedAuthorizer) Authorize(ctx context.Context, cmd CommandConfig) bool {
	// View commands have no fine-grained control yet: `permission:` is not
	// honored for them, so a granted permission must NOT open the gate.
	// Deferred deliberately, not overlooked.
	if cmd.Context == "view" {
		return false
	}
	if cmd.Permission == "" {
		return false // fail closed: a policy is configured, this command is ungoverned
	}
	return readGateFromContext(ctx).HoldsPermission(ctx, cmd.Permission)
}

// UngatedCommandAuthorizer returns the pass-through authorizer for an
// in-process or strictly-loopback server (rela-desktop's Wails asset server,
// docscapture's local render harness) — hosts with no network listener where
// the server host IS the user's machine. Exported so those cmd entry points can
// wire it without the (ACL, bind) selection that only rela-server needs.
func UngatedCommandAuthorizer() commandAuthorizer { return ungatedAuthorizer{} }

// CommandAuthNotifier carries the startup-log hooks for the two command-auth
// outcomes an operator must not discover at runtime. Either field may be nil.
//
// Both are needed. Instrumenting only OnOverride (the path that grants) leaves
// the path that DENIES silent — and that is the one that changes behavior for
// an existing non-loopback deployment on upgrade.
type CommandAuthNotifier struct {
	// OnOverride fires when --allow-unauthenticated-commands actually takes
	// effect on a network bind, i.e. commands became ungated by operator choice.
	OnOverride func()
	// OnRefuse fires when a network bind with no policy refuses command
	// execution, i.e. configured commands stopped working and the operator
	// needs to know the remedy.
	OnRefuse func()
}

// SelectCommandAuthorizer chooses the command authorizer for rela-server from
// the active ACL, the concrete Declarative policy (nil when none), the bind
// host, and the operator's --allow-unauthenticated-commands override. This is
// the seam: the bind address lives here in the cmd layer, not in App, so the
// loopback-vs-network decision cannot be made inside dataentry alone.
//
// Decision matrix (fail-closed by construction — the only paths to ungated are
// loopback or an explicit override):
//
//   - ReadOnlyACL                       → deny  (read-only means no exec, ever)
//   - Declarative policy present        → gated (permission must be set AND held)
//   - NopACL + loopback bind            → ungated (pre-ACL local behavior)
//   - NopACL + network bind + override  → ungated (operator opted in; log loudly)
//   - NopACL + network bind, no override→ deny  (THE FIX: was ungated)
//   - anything else / gate unbuildable  → deny
//
// loopback reports whether the bind host is loopback (the caller passes
// isLoopbackHost(bind); an in-process server with no bind passes true).
// override is the --allow-unauthenticated-commands flag.
//
// notify carries the two startup-log hooks. BOTH branches that change what an
// operator gets are instrumented, deliberately symmetric: taking the override
// is loud because it opens a network shell, and REFUSING is loud because it
// silently breaks a previously-working deployment (buttons vanish, exec 403s)
// with nothing on screen to say why.
func SelectCommandAuthorizer(
	active acl.ACL, declarative *acl.Declarative, loopback, override bool, notify CommandAuthNotifier,
) (commandAuthorizer, error) {
	// ReadOnly is checked before anything else: it is a stronger guarantee than
	// "no policy" and must deny exec on every bind. Match both value and pointer
	// forms (AuthorizeWrite has a value receiver, so &ReadOnlyACL{} also
	// satisfies acl.ACL) — the same &-reachable bypass the old switch guarded.
	switch active.(type) {
	case acl.ReadOnlyACL, *acl.ReadOnlyACL:
		return denyAuthorizer{}, nil
	}

	// A configured Declarative policy governs commands on any bind. Build the
	// gated authorizer from the concrete policy — never fall back to the ctx
	// read gate for NopACL/ReadOnly, which answers permissive there (RR-QWVG8Y).
	if declarative != nil {
		return newGatedAuthorizer(declarative)
	}

	// No policy (NopACL / off-metamodel). Loopback stays ungated for local
	// desktop/dev use; a network bind refuses unless the operator opts in.
	if loopback {
		return ungatedAuthorizer{}, nil
	}
	if override {
		if notify.OnOverride != nil {
			notify.OnOverride()
		}
		return ungatedAuthorizer{}, nil
	}
	if notify.OnRefuse != nil {
		notify.OnRefuse()
	}
	return denyAuthorizer{}, nil
}

// resolveCommands returns commands available for a given page context.
// pageType is "entity", "list", "view", or "dashboard".
// qualifier is the specific list ID or view ID.
// entityType is the entity type shown on the page (empty for dashboard).
//
// Commands the principal may not execute are omitted, so the SPA never renders
// a button that would 403 on click. This is presentation only — the same
// authorizer is re-consulted at exec time, which is the actual boundary.
func (h *commandHandler) resolveCommands(
	ctx context.Context, pageType, qualifier, entityType string,
) []ResolvedCommand {
	s := h.schema()
	if len(s.Cfg.Commands) == 0 {
		return nil
	}

	// Sort command IDs for deterministic order.
	ids := make([]string, 0, len(s.Cfg.Commands))
	for id := range s.Cfg.Commands {
		ids = append(ids, id)
	}
	natsort.Strings(ids)

	var result []ResolvedCommand
	for _, id := range ids {
		cmd := s.Cfg.Commands[id]
		if !matchesPage(cmd, pageType, qualifier, entityType) {
			continue
		}
		if h.authorizer().Authorize(ctx, cmd) {
			result = append(result, ResolvedCommand{
				ID:      id,
				Label:   cmd.Label,
				Confirm: cmd.Confirm,
				Context: cmd.Context,
			})
		}
	}
	return result
}

// matchesPage checks if a command should appear on the given page.
func matchesPage(cmd CommandConfig, pageType, qualifier, entityType string) bool {
	scope := cmd.AvailableOn

	// No scope restriction: show on any page that matches the command's context.
	if scope == nil {
		return contextMatchesPage(cmd.Context, pageType)
	}

	// Check explicit scope matches.
	switch pageType {
	case "view":
		if contains(scope.Views, qualifier) {
			return true
		}
		if contains(scope.EntityTypes, entityType) {
			return true
		}
	case "entity":
		if contains(scope.EntityTypes, entityType) {
			return true
		}
	case "list":
		if contains(scope.Lists, qualifier) {
			return true
		}
	case "dashboard":
		if scope.Dashboard {
			return true
		}
	}
	return false
}

// contextMatchesPage returns true when a command's context type is compatible
// with the page type. Entity and view commands both appear on entity/view pages.
func contextMatchesPage(cmdContext, pageType string) bool {
	switch cmdContext {
	case "entity":
		return pageType == "entity" || pageType == "view"
	case "view":
		return pageType == "view"
	case "list":
		return pageType == "list"
	case "global":
		return pageType == "dashboard"
	}
	return false
}

// contains checks if a string slice contains a value.
func contains(slice []string, val string) bool {
	return slices.Contains(slice, val)
}

// --- Stdin JSON builders ---

// commandInput is the JSON structure passed to a command script on stdin.
type commandInput struct {
	Context     string                      `json:"context"`
	Entity      *entity.Entity              `json:"entity,omitempty"`
	Entities    []*entity.Entity            `json:"entities,omitempty"`
	Collections map[string][]*entity.Entity `json:"collections,omitempty"`
	Relations   []*entity.Relation          `json:"relations,omitempty"`
	ListID      string                      `json:"list_id,omitempty"`
	ViewID      string                      `json:"view_id,omitempty"`
	Project     commandProjectInfo          `json:"project"`
}

type commandProjectInfo struct {
	Root      string `json:"root"`
	Metamodel string `json:"metamodel"`
}

// entityReadable reports whether this principal may read the given row: the
// world block, the face-blind row gate, then the FACE gate — the same three
// checks [visibleReader.getVisibleRef] applies, in the same order.
//
// Not delegated to getVisibleRef because that resolves an address to a row and
// this caller already holds one: a command request carries no entity type, so
// the type has to come off the stored entity. Re-reading through getVisibleRef
// would mean a second store round-trip to reach the identical verdict on the
// identical row.
//
// A gate ERROR is a denial, not a pass. The caller renders every false as the
// uniform not-found, so a denied face stays indistinguishable from an absent
// one (the row-level rule).
func (h *commandHandler) entityReadable(ctx context.Context, e *entity.Entity) bool {
	return entityReadableInRequest(ctx, e)
}

// redactEntity applies field-level `visible:` redaction to an entity bound for
// a command's stdin.
//
// An unwired redactor FAILS CLOSED: the payload leaves the process, so a
// missing collaborator must withhold values rather than ship them raw. Both
// construction sites supply one (app.go and the test helper), so reaching the
// nil arm is a wiring bug, reported rather than silently tolerated.
//
// Nil: e nil returns nil.
func (h *commandHandler) redactEntity(ctx context.Context, e *entity.Entity) *entity.Entity {
	if e == nil {
		return nil
	}
	if h.redactor == nil {
		slog.Error("dataentry: command handler has no field redactor; "+
			"withholding every property from the command payload",
			"type", e.Type, "id", e.ID)
		out := *e
		out.Properties = nil
		out.Content = ""
		return &out
	}
	return visibility.Redact(ctx, h.redactor, e)
}

func (h *commandHandler) buildEntityInput(ctx context.Context, e *entity.Entity) *commandInput {
	return &commandInput{
		Context:   "entity",
		Entity:    e,
		Relations: relationsForEntity(ctx, h.services(), e.ID),
		Project:   h.projectInfo(),
	}
}

// relationsForEntity loads every relation where id is either endpoint
// and returns them as []*entity.Relation for the command-input payload.
func relationsForEntity(ctx context.Context, svc Services, id string) []*entity.Relation {
	rels := make([]*entity.Relation, 0)
	q := store.RelationQuery{EntityID: id, Direction: store.DirectionBoth}
	for r, err := range svc.Store.ListRelations(ctx, q) {
		if err != nil {
			return rels
		}
		rels = append(rels, r)
	}
	return rels
}

func (h *commandHandler) buildListInput(listID string, entities []*entity.Entity) *commandInput {
	return &commandInput{
		Context:  "list",
		ListID:   listID,
		Entities: entities,
		Project:  h.projectInfo(),
	}
}

// buildViewInput assembles the stdin JSON for a view-context command. The
// viewResult it receives is already row-gated + field-redacted (executeView,
// DEC-ZBI39P): a command script sees the same visibility the HTTP view does, so
// a property hidden from the invoking principal is absent from the entity JSON
// rather than raw. (Behavior change since BUG-9QL9XV: previously raw.)
func (h *commandHandler) buildViewInput(ctx context.Context, viewID string, vr *viewResult) *commandInput {
	// Collect all entity IDs in the result set.
	idSet := map[string]bool{vr.Entry.ID: true}
	for _, entities := range vr.Collections {
		for _, e := range entities {
			idSet[e.ID] = true
		}
	}

	// Gather relations between entities in the result set.
	svc := h.services()
	var rels []*entity.Relation
	for id := range idSet {
		q := store.RelationQuery{EntityID: id, Direction: store.DirectionOutgoing}
		for r, err := range svc.Store.ListRelations(ctx, q) {
			if err != nil {
				break
			}
			if idSet[r.To] {
				rels = append(rels, r)
			}
		}
	}

	collections := make(map[string][]*entity.Entity, len(vr.Collections))
	maps.Copy(collections, vr.Collections)

	return &commandInput{
		Context:     "view",
		ViewID:      viewID,
		Entity:      vr.Entry,
		Collections: collections,
		Relations:   rels,
		Project:     h.projectInfo(),
	}
}

func (h *commandHandler) buildGlobalInput() *commandInput {
	return &commandInput{
		Context: "global",
		Project: h.projectInfo(),
	}
}

func (h *commandHandler) projectInfo() commandProjectInfo {
	// Report the schema file this project actually has — the value is handed
	// to external commands, which would fail opening a name that isn't there.
	// Read from the path resolved at discovery rather than re-statting: that
	// keeps this off the disk on a request path and off the OS filesystem,
	// which an injected (e.g. in-memory) FS would not have been.
	schema := project.SchemaFile
	if h.schemaFile != nil {
		schema = h.schemaFile()
	}
	return commandProjectInfo{
		Root:      h.projectRoot(),
		Metamodel: schema,
	}
}

// --- Protocol parser ---

// CommandMessage is a structured message parsed from a command's stdout, and
// also the shape written back out as an SSE event.
//
// The two directions are not identical for `file` messages: a script supplies
// Path, the server replaces it with Token, and the outbound payload carries no
// Path at all (see [commandHandler.mintFileToken]). Path keeps `omitempty` so
// an emitted message simply omits it — the raw server path must not reach the
// browser, which is what the token exists to prevent.
type CommandMessage struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Level string `json:"level,omitempty"`
	// Path is INBOUND ONLY: the location a script reports for a `file`
	// message. Never serialized back to the client.
	Path string `json:"path,omitempty"`
	// Token is OUTBOUND ONLY: the opaque handle the browser presents to
	// /api/command-file/ to download the file Path named.
	Token      string `json:"token,omitempty"`
	Label      string `json:"label,omitempty"`
	Action     string `json:"action,omitempty"`
	ID         string `json:"id,omitempty"`
	EntityType string `json:"entity_type,omitempty"`
	URL        string `json:"url,omitempty"`
}

// parseCommandOutput parses a single line of command stdout.
// If the line has the ::rela:: prefix, it returns the parsed message.
// Otherwise it returns a log-type message with the raw text.
func parseCommandOutput(line string) CommandMessage {
	if after, ok := strings.CutPrefix(line, commandOutputPrefix); ok {
		payload := after
		var msg CommandMessage
		if err := json.Unmarshal([]byte(payload), &msg); err == nil {
			return msg
		}
	}
	return CommandMessage{Type: "log", Text: line}
}

// --- Process management ---

type runningCommand struct {
	cmd *exec.Cmd

	// owner is the principal that started this command. runningCommands is a
	// package-level map keyed only by execID, and execID is client-supplied
	// (see handleCommandExec), so without an owner recorded here any caller
	// who guesses or reuses an id could cancel someone else's run — a
	// cross-principal kill that the exec-side permission check does not cover
	// (RR-YZV7SY). handleCommandCancel compares against this.
	owner principal.Principal
}

var (
	runningCommands sync.Map
)

// --- HTTP Handlers ---

// handleCommandExec handles POST /api/command/{commandID} and streams results as SSE.
//
// Restricted to POST: this endpoint runs configured shell commands and a GET
// would let `<img src=/api/command/X>` invoke them cross-origin from any
// browser tab, bypassing same-origin policy entirely.
//
//nolint:funlen // dispatches over every command context and argument shape inline; each branch is one command variant, and extracting them would fragment the request lifecycle.
func (h *commandHandler) handleCommandExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	commandID := strings.TrimPrefix(r.URL.Path, "/api/command/")
	s := h.schema()
	cmd, ok := s.Cfg.Commands[commandID]
	if !ok {
		http.Error(w, "Unknown command: "+commandID, http.StatusNotFound)
		return
	}

	// Authorization boundary (TKT-MJ02AO). resolveCommands already hides
	// unauthorized commands from the UI, but that is presentation: this is the
	// check that actually holds. 403 rather than 404 — the command's existence
	// is already public via config, so there is no oracle to protect.
	if !h.authorizer().Authorize(r.Context(), cmd) {
		http.Error(w, commandDenyReason, http.StatusForbidden)
		return
	}

	execID := r.URL.Query().Get("exec_id")
	if execID == "" {
		execID = fmt.Sprintf("cmd-%d", time.Now().UnixNano())
	}

	// A SECOND, server-minted key for this run, used only to group its download
	// tokens. Deliberately not execID: that one is client-supplied (it has to
	// be, so the client can address /api/command-cancel/), and a caller who
	// reuses another run's id would otherwise reach into that run's token group
	// and start its expiry clock early. This is the same hazard runningCommands
	// records an `owner` for (RR-YZV7SY); here there is nothing to authorize
	// against, so the key is simply not the client's to name.
	runKey := newRunKey()

	// Build stdin JSON based on context.
	var input *commandInput
	switch cmd.Context {
	case "entity":
		entityID := r.URL.Query().Get("entity_id")
		svc := h.services()
		// An ADDRESS, as everywhere an id is accepted: `ID` or `ID@face`.
		// Parsed rather than handed to the store whole — on fsstore the raw
		// string happens to hit a faced row's index key, on pgstore it never
		// does, and a command that runs on one backend and 404s on the
		// other is the split this grammar exists to remove.
		id, face, perr := entity.ParseStateRef(entityID)
		if perr != nil {
			http.Error(w, "Entity not found: "+entityID, http.StatusNotFound)
			return
		}
		entityDomain, err := svc.Store.GetEntityState(r.Context(), id, face)
		if err != nil {
			http.Error(w, "Entity not found: "+entityID, http.StatusNotFound)
			return
		}
		// READ-GATE the row, then its FACE, then redact — the chain this
		// branch owed and did not have (BUG-G2BASF review). The view branch
		// gets all three from executeView; this one reads the raw store, so
		// it applies them itself.
		//
		// Ordered read-then-gate rather than gate-then-read because the row
		// gate needs the entity TYPE and a command request carries only an
		// address. That is the same order executeViewRef uses, and it
		// discloses nothing: every failure below is the one not-found a
		// missing entity produces, and nothing derived from the row is
		// written before the gates clear.
		//
		// `authorizeCommand` does NOT cover this: it decides whether this
		// COMMAND may run, not which rows it may see.
		if !h.entityReadable(r.Context(), entityDomain) {
			http.Error(w, "Entity not found: "+entityID, http.StatusNotFound)
			return
		}
		input = h.buildEntityInput(r.Context(), h.redactEntity(r.Context(), entityDomain))
	case "list":
		listID := r.URL.Query().Get("list_id")
		listCfg, found := s.Cfg.Lists[listID]
		if !found {
			http.Error(w, "List not found: "+listID, http.StatusNotFound)
			return
		}
		entities := listFromStoreByTypes(r.Context(), h.services(), []string{listCfg.EntityType})
		entities = applyFilters(entities, listCfg.Filters)
		input = h.buildListInput(listID, entities)
	case "view":
		viewID := r.URL.Query().Get("view_id")
		entityID := r.URL.Query().Get("entity_id")
		viewCfg, found := s.Cfg.Views[viewID]
		if !found {
			http.Error(w, "View not found: "+viewID, http.StatusNotFound)
			return
		}
		// DEFAULT WORLD, named explicitly — and this is the call site the
		// explicit-parameter design exists for. The viewResult below is
		// marshaled to JSON and piped to an operator shell script's stdin, so
		// a world applied here changes what an EXTERNAL PROCESS receives, past
		// any layer that could observe it. Scoping the command surface for
		// worlds is its own ticket, with its own thinking about what a
		// world-bound command even means.
		vr, err := h.executeView(r.Context(), viewCfg, entityID, defaultViewWorld())
		if err != nil {
			http.Error(w, "View error: "+err.Error(), http.StatusBadRequest)
			return
		}
		input = h.buildViewInput(r.Context(), viewID, vr)
	case "global":
		input = h.buildGlobalInput()
	default:
		http.Error(w, "Invalid command context: "+cmd.Context, http.StatusBadRequest)
		return
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		http.Error(w, "Failed to build input: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Set up SSE response. Flusher is optional — Wails' asset server on
	// macOS/Linux delivers each Write() immediately without needing Flush().
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Start the script. Use r.Context() so a client disconnect triggers cancel,
	// but mirror handleCommandCancel's SIGINT+3s-grace contract so scripts that
	// catch SIGINT (e.g., to flush output or commit a transaction) still get
	// the chance rather than being SIGKILLed outright.
	// #nosec G702 -- cmd.Script is operator-authored: it comes from the
	// `commands:` map in the project's data-entry.yaml (dataentryconfig),
	// selected by name via commandID. The request may only choose a
	// registered command key; it never supplies a command, flag, or path.
	// Request-derived values reach the script only as stdin JSON and
	// RELA_* env vars, never as shell text. A shell is intentional here —
	// `script:` is documented as a shell snippet, and executing it is the
	// feature. The trust boundary is the config file (operator/repo write
	// access), enforced upstream by authorizeCommand.
	proc := exec.CommandContext(r.Context(), "sh", "-c", cmd.Script)
	proc.Cancel = func() error { return proc.Process.Signal(syscall.SIGINT) }
	proc.WaitDelay = cancelGrace
	proc.Dir = h.projectRoot()
	proc.Env = h.buildCommandEnv(cmd, input)
	proc.Stdin = strings.NewReader(string(inputJSON))

	stdout, err := proc.StdoutPipe()
	if err != nil {
		writeSSEEvent(w, flusher, "error", `{"text":"Failed to create stdout pipe"}`)
		writeSSEDone(w, flusher, false)
		return
	}
	stderr, err := proc.StderrPipe()
	if err != nil {
		writeSSEEvent(w, flusher, "error", `{"text":"Failed to create stderr pipe"}`)
		writeSSEDone(w, flusher, false)
		return
	}

	if startErr := proc.Start(); startErr != nil {
		msg, _ := json.Marshal(map[string]string{"text": "Failed to start: " + startErr.Error()})
		writeSSEEvent(w, flusher, "error", string(msg))
		writeSSEDone(w, flusher, false)
		return
	}

	// Register for cancellation, bound to the starting principal so only they
	// can cancel it (RR-YZV7SY).
	runningCommands.Store(execID, &runningCommand{
		cmd:   proc,
		owner: principal.From(r.Context()),
	})
	defer runningCommands.Delete(execID)
	// Start the download tokens' expiry clock when the run ends. Not a delete:
	// the user clicks Download after the file is reported (TKT-93FUCV).
	defer h.files.release(runKey)

	// Capture stderr in background.
	var stderrBuf strings.Builder
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			stderrBuf.WriteString(scanner.Text())
			stderrBuf.WriteString("\n")
		}
	}()

	// Stream stdout as SSE events.
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		msg := parseCommandOutput(line)
		if msg.Type == "file" {
			msg = h.mintFileToken(runKey, commandID, cmd, msg)
		}
		data, _ := json.Marshal(msg)
		writeSSEEvent(w, flusher, msg.Type, string(data))
	}

	// Wait for stderr goroutine and process to finish.
	<-stderrDone
	waitErr := proc.Wait()

	if waitErr != nil {
		errText := "Command failed"
		if stderrBuf.Len() > 0 {
			errText = strings.TrimSpace(stderrBuf.String())
		}
		msg, _ := json.Marshal(map[string]string{"text": errText})
		writeSSEEvent(w, flusher, "error", string(msg))
		writeSSEDone(w, flusher, false)
		return
	}

	writeSSEDone(w, flusher, true)
}

// handleCommandCancel handles POST /api/command-cancel/{execID}.
func (h *commandHandler) handleCommandCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	execID := strings.TrimPrefix(r.URL.Path, "/api/command-cancel/")
	val, ok := runningCommands.Load(execID)
	if !ok {
		http.Error(w, "No running command: "+execID, http.StatusNotFound)
		return
	}
	rc, castOK := val.(*runningCommand)
	if !castOK {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Only the principal that started the command may cancel it (RR-YZV7SY).
	// execID is client-supplied and the registry is process-global, so without
	// this a caller who guessed an id could kill another user's run — including
	// a caller whose own exec attempts are being 403'd. Answer 404, identical
	// to an unknown id, so cancel cannot be used to probe which commands are
	// currently running under other principals.
	if !rc.owner.Equal(principal.From(r.Context())) {
		http.Error(w, "No running command: "+execID, http.StatusNotFound)
		return
	}

	// Send SIGINT for graceful shutdown.
	if rc.cmd.Process != nil {
		_ = rc.cmd.Process.Signal(syscall.SIGINT)
	}

	// Wait briefly, then force kill.
	go func() {
		time.Sleep(cancelGrace)
		if rc.cmd.Process != nil {
			_ = rc.cmd.Process.Kill()
		}
	}()

	w.WriteHeader(http.StatusOK)
}

// mintFileToken converts a `file` message from a script into one the browser
// can act on: the raw server path is replaced by an opaque download token.
//
// Two things happen here, both load-bearing (TKT-93FUCV):
//
//   - The path is containment-checked ONCE, now, while the project root is in
//     hand. Everything downstream works from the resolved path stored under the
//     token, so no later step re-derives a path from anything a client said.
//   - The token is bound to cmd, the command this run executed, so the download
//     can re-authorize the same way exec did.
//
// A path that fails containment (or does not exist) yields a file message with
// no token. The UI renders the label without a Download button, which is the
// honest outcome: the script named something we will not serve. It is NOT an
// error event — a script writing a file outside the project is misconfigured,
// not failed, and failing the whole run would be a worse trade.
//
// The server path is dropped either way. It is meaningless to a remote user and
// naming it in the payload is how the old launcher grew its arbitrary-path hole.
func (h *commandHandler) mintFileToken(
	runKey, commandID string, cmd CommandConfig, msg CommandMessage,
) CommandMessage {
	label := msg.Label
	if label == "" {
		label = filepath.Base(msg.Path)
	}

	resolved, err := containProjectPath(h.projectRoot(), msg.Path)
	if err != nil {
		slog.Warn("dataentry: command file not downloadable",
			"err", err, "command", commandID, "run", runKey)
		return CommandMessage{Type: msg.Type, Label: label}
	}

	// Regular files only. Containment proves WHERE the path is, not WHAT it is,
	// and a directory opens successfully — so without this a directory would
	// mint a token, commit the download headers, and then fail the copy, giving
	// the user a 200 and an empty file. Checked here rather than at download so
	// the operator gets the same warning as any other unusable path, and the
	// download path stays a straight read.
	info, err := os.Stat(resolved.abs)
	if err != nil || !info.Mode().IsRegular() {
		slog.Warn("dataentry: command file is not a regular file",
			"err", err, "command", commandID, "run", runKey)
		return CommandMessage{Type: msg.Type, Label: label}
	}

	token, err := h.files.mint(runKey, resolved, label, cmd)
	if err != nil {
		// Only reachable if crypto/rand fails, which means the process is in
		// serious trouble — Error, not Warn. The cases above are operator
		// misconfiguration; this one is not.
		slog.Error("dataentry: minting command file token failed",
			"err", err, "command", commandID, "run", runKey)
		return CommandMessage{Type: msg.Type, Label: label}
	}
	return CommandMessage{Type: msg.Type, Label: label, Token: token}
}

// handleCommandFile handles GET /api/command-file/{token}, streaming a file a
// command run produced.
//
// This replaced POST /api/open-file, which ran an OS launcher (open/xdg-open/
// explorer) on the SERVER. That was incoherent anywhere but a desktop install:
// on a headless deployment it silently no-opped, and had it worked it would
// have opened the file on the server rather than for the user who asked.
//
// The authorization model is the point of the route, not an addition to it:
//
//   - The caller presents a TOKEN, never a path. A path parameter would be the
//     same arbitrary-server-read the launcher was, in a politer wrapper.
//   - Holding the token is not enough. The wired authorizer re-runs on every
//     download, so a token stops working as soon as the grant that minted it
//     goes away — a revoked permission, a policy the authorizer now denies.
//     Validating only at mint time would let the capability outlive the policy.
//
// Unknown, expired and unauthorized all return 404, matching the attachment
// handler's convention that hidden and nonexistent are indistinguishable. A 403
// here would confirm that a token is real and names a file, to a caller who may
// not have it.
func (h *commandHandler) handleCommandFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := strings.TrimPrefix(r.URL.Path, "/api/command-file/")
	entry, ok := h.files.lookup(token)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// The re-check. Same authorizer, same config the run was authorized with.
	if !h.authorizer().Authorize(r.Context(), entry.cmd) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	f, err := os.Open(entry.path.abs)
	if err != nil {
		// The script may have cleaned up after itself, or written to a temp
		// location that has since gone. Don't echo the path or the OS error.
		slog.Warn("dataentry: opening command file failed", "err", err)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	setHardenedDownloadHeaders(w.Header(), contentTypeForFilename(entry.label), entry.label)
	// no-store is set here rather than in the shared helper because the three
	// download endpoints genuinely differ: attachments are stable stored
	// content and set no Cache-Control at all, while these bytes and an
	// export's are produced per request. Same line, same reason, as export.go.
	w.Header().Set("Cache-Control", "no-store")

	if _, err := io.Copy(w, f); err != nil {
		// Headers are already written; the status can't change now.
		slog.Warn("dataentry: streaming command file failed", "err", err)
	}
}

// --- Helpers ---

// errPathOutsideProject is returned by containedProjectPath when the input
// resolves to a location outside the project root.
var errPathOutsideProject = errors.New("path outside project")

// errPathNotFound is returned by containedProjectPath when the path is
// inside the project root structurally but does not exist on disk.
var errPathNotFound = errors.New("path not found")

// containedProjectPath cleans, resolves, and validates that filePath lives
// inside projectRoot. The returned path is absolute and symlink-resolved.
//
// A TOCTOU window remains, and TKT-93FUCV widened it: the check runs when a
// download token is minted, but the file is opened later, when someone clicks
// Download. An attacker with local write access to the project directory could
// swap a contained path for a symlink in between. The local filesystem is the
// trust boundary here — someone who can write into the project root can also
// edit the `commands:` config that decides what runs at all — so this is
// accepted rather than mitigated. Closing it would mean holding an open file
// descriptor from mint to download, which means holding one per emitted file
// for the token's whole lifetime.
func containedProjectPath(projectRoot, filePath string) (string, error) {
	if strings.ContainsRune(filePath, 0) {
		return "", errPathOutsideProject
	}

	clean := filepath.Clean(filePath)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(projectRoot, clean)
	}
	abs, err := filepath.Abs(clean)
	if err != nil {
		return "", errPathOutsideProject
	}

	rootAbs, err := filepath.Abs(filepath.Clean(projectRoot))
	if err != nil {
		return "", errPathOutsideProject
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		rootResolved = rootAbs
	}

	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// Path does not exist (or contains a broken symlink). Distinguish
		// "not found inside project" from "outside project" so the handler
		// can return 404 vs 403. Verify the unresolved abs path is at
		// least structurally inside the project root before reporting it
		// as a 404; otherwise it's a traversal attempt against a
		// non-existent file.
		insideProject := abs == rootResolved ||
			strings.HasPrefix(abs, rootResolved+string(os.PathSeparator)) ||
			abs == rootAbs ||
			strings.HasPrefix(abs, rootAbs+string(os.PathSeparator))
		if insideProject {
			return "", errPathNotFound
		}
		return "", errPathOutsideProject
	}

	if resolved == rootResolved {
		return resolved, nil
	}
	if strings.HasPrefix(resolved, rootResolved+string(os.PathSeparator)) {
		return resolved, nil
	}
	return "", errPathOutsideProject
}

func (h *commandHandler) buildCommandEnv(cmd CommandConfig, input *commandInput) []string {
	env := os.Environ()
	env = append(env,
		"RELA_PROJECT_ROOT="+h.projectRoot(),
		"RELA_CONTEXT="+cmd.Context,
	)
	if input.Entity != nil {
		// The face is a SEPARATE variable, and RELA_ENTITY_ID stays bare
		// (BUG-G2BASF). A script that does not know about faces keeps
		// working unchanged, and one that does can address the row it was
		// actually invoked on without parsing an address out of the id.
		//
		// RELA_ENTITY_REF is what `rela update` and the HTTP API accept, so
		// a face-aware script writes back to the face it read. Built with
		// [entity.FormatStateRef], the inverse of the ParseStateRef the
		// handler above uses, so the two spellings of an address cannot
		// drift apart here. Both are always SET — an unset variable and an
		// empty one are different things to a shell.
		env = append(env,
			"RELA_ENTITY_ID="+input.Entity.ID,
			"RELA_ENTITY_TYPE="+input.Entity.Type,
			"RELA_ENTITY_FACE="+string(input.Entity.Face),
			"RELA_ENTITY_REF="+entity.FormatStateRef(input.Entity.ID, input.Entity.Face),
		)
	}
	if input.ListID != "" {
		env = append(env, "RELA_LIST_ID="+input.ListID)
	}
	if input.ViewID != "" {
		env = append(env, "RELA_VIEW_ID="+input.ViewID)
	}
	for k, v := range cmd.Env {
		env = append(env, k+"="+v)
	}
	return env
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, event, data string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	if flusher != nil {
		flusher.Flush()
	}
}

func writeSSEDone(w http.ResponseWriter, flusher http.Flusher, success bool) {
	data, _ := json.Marshal(map[string]bool{"success": success})
	writeSSEEvent(w, flusher, "done", string(data))
}
