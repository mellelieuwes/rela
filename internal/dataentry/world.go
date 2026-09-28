package dataentry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	v1 "github.com/Sourcehaven-BV/rela/internal/apiwire/v1"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// WorldParam is the query parameter that selects a world on the read API:
// `?world=published`. Absent or empty means the DEFAULT world, bound
// explicitly at this boundary — the interior never sees "unspecified"
// (design doc §4.4).
const WorldParam = "world"

// defaultWorldName is the reserved name of the implicit default world,
// mirroring metamodel.DefaultWorldName and acl.DefaultWorldName. Spelled
// here so `?world=default` is accepted as an explicit way to ask for what
// an absent parameter already means.
const defaultWorldName = "default"

// errWorldUnknown names a `?world=` value no declared world matches. A
// CONFIG error, rendered as a named 400 — see [resolveWorld].
var errWorldUnknown = errors.New("no such world")

// errWorldDenied means the principal holds no read grant for a world that
// DOES exist. Rendered as an EMPTY RESULT, never a 403 — see
// [resolveWorld].
var errWorldDenied = errors.New("world not readable by this principal")

// errWorldDuplicated means the request carried more than one `?world=`.
// Rejected rather than resolved by a precedence rule nobody would remember.
var errWorldDuplicated = errors.New("duplicate world parameter")

// errWorldUnsupported names a route that cannot honor a non-default world.
// Rendered as a 422: the caller asked for something coherent that this
// surface cannot serve, and saying so is better than serving default-world
// data under a published-world request (Ruling 3).
var errWorldUnsupported = errors.New("this endpoint cannot serve a non-default world")

// WorldLookup resolves a declared world NAME to its compiled scope.
//
// Consumer-side interface: internal/dataentry may not import internal/worlds
// (arch-lint), and a store.WorldScope is metamodel-free by construction, so
// the compiled map is injected from the wiring site via [App.SetWorlds].
//
// It must FAIL CLOSED on an unknown name — returning ok=false rather than
// substituting the default world, which would silently widen a request that
// asked for a narrower view.
type WorldLookup interface {
	Lookup(name string) (store.WorldScope, bool)
}

// worldHandle is the per-request world binding: a NAME and its compiled
// SCOPE, resolved once at the top of the operation and reused for every read
// in it.
//
// Both fields are needed. The scope is what the store matches on; the name is
// what the grant was checked against and what diagnostics report — a
// store.WorldScope deliberately carries no name (internal/store cannot know
// one), so it cannot be recovered later.
type worldHandle struct {
	name  string
	scope store.WorldScope

	// denied marks a world that EXISTS but that this principal holds no
	// read grant for. The request still runs, so the handler produces its
	// ordinary response shape; the read seams simply find nothing. See the
	// errWorldDenied arm in attachWorld for why the alternative — writing a
	// synthetic empty body — leaked the denial.
	denied bool
}

// isDefault reports whether this handle is the default world. The zero
// handle is the default world, which is what makes an unstamped context and
// an explicit `?world=default` behave identically.
func (w worldHandle) isDefault() bool { return !w.denied && w.scope.IsDefaultWorld() }

// blocksAllReads reports a handle that must yield nothing at all.
func (w worldHandle) blocksAllReads() bool { return w.denied }

type worldCtxKey struct{}

// withWorld binds a resolved world to ctx for the rest of the operation.
//
// The handle is CONSTRUCTED ONCE, in middleware, and carried whole — this is
// deliberately not "a world name on ctx that each read re-resolves". §4.4
// forbids the latter: a world that travels as ambient data can be
// reinterpreted per call, and a trace that flips worlds mid-walk is
// incoherent. A fully-constructed handle cannot be reinterpreted.
//
// The key is typed and unexported so nothing outside this package can inject
// one.
func withWorld(ctx context.Context, w worldHandle) context.Context {
	return context.WithValue(ctx, worldCtxKey{}, w)
}

// worldFromContext returns the request's world handle, or the zero handle
// (the default world) when none was bound.
//
// Defaulting to the DEFAULT world rather than erroring is correct here and is
// not a fail-open: the default world is today's graph, so an unstamped
// context behaves exactly as it did before worlds existed. A non-default
// world can only ever arrive by passing the grant check in
// [resolveWorld].
func worldFromContext(ctx context.Context) worldHandle {
	w, _ := ctx.Value(worldCtxKey{}).(worldHandle)
	return w
}

// worldScopeFrom returns the store scope to stamp onto a query built from
// ctx. Spelled as its own helper so query-construction sites read as
// `q.World = worldScopeFrom(ctx)` and a reviewer can grep for the ones that
// forgot.
func worldScopeFrom(ctx context.Context) store.WorldScope {
	return worldFromContext(ctx).scope
}

// SetWorlds injects the compiled world map, enabling `?world=` selection.
//
// Until this is called the App serves the default world only and REFUSES any
// `?world=` naming something else — which is the correct posture for a
// surface whose wiring never opted in, and means a deployment that does not
// use worlds cannot accidentally acquire the parameter.
func (a *App) SetWorlds(w WorldLookup) { a.worlds = w }

// resolveWorld resolves the request's `?world=` parameter into a handle,
// applying the per-world read grant BEFORE any resolver is constructed.
//
// A free function taking the lookup rather than an App method: it needs
// exactly one collaborator, and App is at its plimsoll method cap because it
// has accreted for years — adding to it is the habit that got it there.
//
// Returns (handle, nil) on success. The two failure modes are DELIBERATELY
// DIFFERENT, and the difference is not an inconsistency to tidy away:
//
//   - UNKNOWN world -> a named 400. A world name is operator-authored
//     CONFIG (schema.yaml), and CLAUDE.md is explicit that config names are
//     not secret: naming the missing world is more useful to whoever is
//     debugging it than a uniform silence, and conceals nothing that the
//     operator's own repo does not already state.
//
//     `/api/v1/_schema` now enumerates the declared worlds (TKT-WRLDAPI), so
//     this 400 names something the same caller can already list. Note the
//     ORDER of that argument: the conclusion does not rest on the
//     enumeration. Config names are not secret whether or not this API
//     happens to serve them, and the enumeration exists because a client
//     cannot build a world selector by guessing — not to make the 400 safe.
//     An earlier revision of this comment asserted the enumeration before it
//     was built; it is recorded here as fact only now that it is one.
//
//   - Known world the principal may NOT read -> (zero handle, errWorldDenied),
//     which callers render as an EMPTY RESULT, never a 403. What a world
//     CONTAINS — and whether any given entity exists in it — is exactly the
//     secret this feature exists to keep (§4.4). A 403 here would tell the
//     caller that a world exists holding things they may not see, which is
//     the existence oracle the row-level rule forbids. Indistinguishable
//     from a world with nothing in it, by design.
//
// Do NOT "unify" these two into one response shape. They protect different
// things: one is config, the other is content.
func resolveWorld(r *http.Request, lookup WorldLookup, configured string) (worldHandle, error) {
	values := r.URL.Query()[WorldParam]
	if len(values) > 1 {
		// Get() would silently take the FIRST, so `?world=default&world=published`
		// would serve the default world under a request that also asked for
		// published — a client-side param-append bug becoming a silent
		// wrong-face serve, which is the direction this arc refuses.
		return worldHandle{}, errWorldDuplicated
	}
	name := r.URL.Query().Get(WorldParam)
	if len(values) == 0 {
		// No `?world=` at all: the operator's browsing default applies
		// (`app.default_world`). Empty means the default world, so an
		// unconfigured deployment behaves exactly as before.
		//
		// This used to be a SPA-only rule, and that was the defect: the
		// config was serialized to `/_schema` and applied in useWorld.ts,
		// so a bare `curl` and the browser disagreed about the same URL.
		// A face-scoped role then read nothing from the raw API despite a
		// valid grant, which looks like a broken ACL rather than a missing
		// parameter (QA finding 1).
		//
		// An EXPLICIT `?world=default` still reaches the raw faces — that
		// is why this branch tests `len(values) == 0` and the one below
		// tests the name. The two are no longer the same question.
		name = configured
	}
	if name == "" || name == defaultWorldName {
		// The default world needs no grant beyond the ordinary read gates
		// that already run per entity: it IS today's graph.
		return worldHandle{}, nil
	}
	if lookup == nil {
		return worldHandle{}, errWorldUnknown
	}
	scope, ok := lookup.Lookup(name)
	if !ok {
		return worldHandle{}, errWorldUnknown
	}
	permitted, err := readGateFromContext(r.Context()).PermitsWorld(r.Context(), name)
	if err != nil {
		// An infrastructure failure is NOT a denial. Rendering it as an
		// empty result would hide an outage behind a page that looks like a
		// correctly-empty world, with no operator signal (RR-4TFZNL).
		return worldHandle{}, err
	}
	if !permitted {
		return worldHandle{}, errWorldDenied
	}
	return worldHandle{name: name, scope: scope}, nil
}

// # Why an allowlist rather than teaching every read path
//
// A world-bound request must never be served default-world data (Ruling 3).
// internal/dataentry reaches entity content through roughly forty store call
// sites across a dozen files — the list pipeline, the ungated entityReader
// used for relations and serialization, view traversal, document render,
// feeds, CalDAV, sync, commands. Teaching all of them in one change would be
// a large diff in which a single missed site is a silent leak, and "silence
// in the direction of serving the wrong face" is the failure this arc keeps
// hitting.
//
// So the default is DENY, and a route joins this predicate only when its
// whole read path is world-scoped and tested. That is the DEC-ZBI39P stance —
// structurally incapable rather than "defaults to safe" — applied at the
// route table: a leak requires someone to WIDEN this predicate, a visible and
// reviewable act, rather than to forget a call site.
//
// worldCapablePath reports whether path may serve a non-default world.
//
// Deliberately conservative, and the list has grown deliberately: the
// collection list (`/{plural}`), the single-entity GET (`/{plural}/{id}`) and
// exactly five underscore routes named one at a time below — `_views`,
// `_history`, `_next_action`, `_search` and `_position`. Every other
// underscore endpoint (analyze, documents, feeds, sync) is refused, along
// with every sub-resource of an entity (relations, attachments, export),
// because each reaches content through a path that is still world-blind.
//
// Each admission carries its own justification at the call site rather than a
// prefix rule, so widening this stays one reviewable edit per route.
//
// Refusing is not a permanent verdict on those routes; it is the honest one
// until each is scoped and tested.
func worldCapablePath(path string) bool {
	trimmed := strings.TrimPrefix(path, "/api/v1/")
	if trimmed == path {
		// Not under the versioned API (e.g. /api/sync/...). Refuse.
		return false
	}
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return false
	}
	// The ONE underscore route that is world-capable: the entity view
	// (TKT-WRLDAPI item 4b). It is named exactly rather than admitted by a
	// prefix rule, so widening the underscore family stays a deliberate act —
	// every other `_`-prefixed endpoint remains refused by the clause below.
	if isWorldCapableViewPath(trimmed) {
		return true
	}
	// The SECOND underscore route, admitted for the same reason and named just
	// as exactly: entity history (TKT-WRLDGAPS / BUG-2).
	//
	// Versioning is PER-FACE — `entity_versions` is keyed by the content state
	// and TKT-C1XUA8 added per-face capture — so a draft and its published face
	// have genuinely different histories. Refusing `?world=` here did not keep
	// the surface safe: it made the History button on a world-bound page show
	// the DEFAULT face's history, which is the wrong record presented as the
	// right one. That is worse than a refusal, because nothing on screen says
	// which face it belongs to.
	if isWorldCapableHistoryPath(trimmed) {
		return true
	}
	// The THIRD underscore route, named exactly like the two above: the
	// advisory next-action surface.
	//
	// It is admitted on a NARROWER claim than the others. `?world=` here does
	// not scope a read at all — it supplies the DISPLAY world for each
	// source's `visible_worlds` allow list. Which world a source READS is
	// declared per source in config (`source_world:`) and is deliberately not
	// taken from the request, so admitting this parameter cannot point an
	// operator's query at a world they did not name.
	//
	// Refusing it was actively wrong rather than merely conservative. A
	// next-action answers "what should I do now?", and the answer is almost
	// always unfinished work — exactly what a publication world excludes. So
	// a reader browsing `published` could never be told that the suggestion
	// they were being shown was computed for somewhere else, and an operator
	// had no way to say "only nag about this while in editorial".
	if trimmed == "_next_action" {
		return true
	}
	// The FOURTH and FIFTH, named exactly: cross-type search and scope
	// position (BUG-SMPOZB). Refusing search left `app.default_world`
	// unapplied there, so the command palette and entity picker could not
	// find a faced entity that has no default face.
	//
	// Every branch of executeQuery takes the world from ctx, behind a
	// denied-world guard: the free-text search, and the type listing, which
	// also applies the grant's face allowlist before the world ranks. Hits
	// load the face the searcher matched. Free-text hits are face-gated
	// AFTER the world ranks, which can drop an entity whose prime is a
	// withheld face; that narrows only, and is BUG-OJPVPG.
	//
	// `_position` is admitted with search because it recomputes the set a
	// search or list page showed; in a different world it answered
	// not_in_scope for the rows the page had just listed. Both of its paths
	// take the world: the store pushdown (pushdownPlan) and the Go path
	// through resolveScope.
	if trimmed == "_search" || trimmed == "_position" {
		return true
	}
	if strings.HasPrefix(trimmed, "_") {
		return false
	}
	// `{plural}` or `{plural}/{id}` only. A third segment is a
	// sub-resource (relations, attachments, _export) and is refused.
	return strings.Count(trimmed, "/") <= 1 &&
		!strings.Contains(trimmed, "/_")
}

// refuseWorldConfigError writes the 400 for a malformed or undeclared
// `?world=` and reports true, or reports false for every other outcome.
//
// The two it handles are CONFIG errors: they are true of a request on any
// route, for any principal, and are settled without consulting the grant. That
// is why they come first — and why they must not be shadowed by the
// route-capability 422 that follows. An operator who typos a world name is
// better served by `no world named "pubished" is declared` than by a refusal
// telling them the route cannot serve worlds at all, especially since a typo
// is likeliest on the routes they are experimenting with.
//
// Nil: `err` is accepted — a nil or unrelated error reports false, which is
// what makes this usable as a guard rather than an arm of a switch.
func refuseWorldConfigError(w http.ResponseWriter, r *http.Request, requested string, err error) bool {
	switch {
	case errors.Is(err, errWorldDuplicated):
		writeV1Error(w, r, http.StatusBadRequest, "duplicate_world",
			"more than one ?world= parameter", "pass it exactly once")
		return true
	case errors.Is(err, errWorldUnknown):
		// A config name, not a secret: name it. See resolveWorld.
		writeV1Error(w, r, http.StatusBadRequest, "unknown_world",
			fmt.Sprintf("no world named %q is declared", requested),
			"check the `worlds:` block in schema.yaml")
		return true
	}
	return false
}

// refuseWorldIncapablePath writes the `world_unsupported` 422 and reports true
// when `requested` names a non-default world on a route [worldCapablePath]
// refuses. It reports false — write nothing, carry on — for the default world
// or a capable route.
//
// # Why this is decided BEFORE the grant check's outcome
//
// Whether a route can serve a non-default world is a property of the ROUTE. It
// does not depend on the principal, and it must not be allowed to: this check
// used to live inside `if !handle.isDefault()`, downstream of [resolveWorld],
// where a DENIED world never reached it. `errWorldDenied` short-circuits
// straight to the handler so the ordinary empty result renders — so a
// principal WITHOUT the world grant sailed past a refusal that a principal
// WITH it received, and landed on a world-blind route that answered with full
// DEFAULT-world content (BUG-CV8L3B, confirmed on `_analyze`).
//
// That disclosed content the world exists to withhold, and made the two grant
// outcomes distinguishable — the oracle [resolveWorld] rules out. Deciding on
// the requested name closes both: permitted and denied reach this refusal
// identically, so it cannot itself become an oracle, and a denied handle flows
// only into routes that are world-scoped and tested.
//
// Keyed on the NAME rather than a resolved handle deliberately. A denied
// handle carries the ZERO scope, and a zero scope IS the default world, so
// `handle.isDefault()` cannot distinguish "no world asked for" from "a world
// asked for and refused" — the same trap [queryService.freeTextIDsForType]
// documents at its own seam. That is also why the empty name passes through:
// `?world=` is explicit but means the default world.
func refuseWorldIncapablePath(w http.ResponseWriter, r *http.Request, requested string) bool {
	if requested == "" || requested == defaultWorldName {
		return false
	}
	if worldCapablePath(r.URL.Path) {
		return false
	}
	writeV1Error(w, r, http.StatusUnprocessableEntity, "world_unsupported",
		errWorldUnsupported.Error(),
		"this endpoint serves the default world only; omit ?world=")
	return true
}

// isWorldCapableViewPath matches `_views/{type}/{id}` — the entity view, whose
// whole read path was world-scoped in TKT-WRLDAPI item 4b.
//
// EXACTLY three segments, and the id must be non-empty. `_views/{name}` (a
// standalone view by config name) is a DIFFERENT surface reached through a
// different handler, and it is not scoped: it is refused here rather than
// admitted by a looser prefix match.
//
// `_sidepanel` and the command runner's `kind: view` share executeView but are
// not routes this admits — and they pass defaultViewWorld() explicitly, so
// even a future routing mistake could not hand them a world. See [viewWorld].
func isWorldCapableViewPath(trimmed string) bool {
	parts := strings.Split(trimmed, "/")
	return len(parts) == 3 && parts[0] == "_views" && parts[1] != "" && parts[2] != ""
}

// isWorldCapableHistoryPath matches `_history/{type}/{id}` and
// `_history/{type}/{id}/{version}` — the entity-history reads.
//
// The RESTORE sub-path (`.../{version}/restore`) is a POST and is therefore
// already refused one layer up: attachWorld rejects `?world=` on any
// non-GET/HEAD/OPTIONS request with `world_read_only`, because a world can
// answer a read with a fallback face and writing through that answer saves the
// wrong state. So this predicate does not need to exclude it, and deliberately
// does not try — duplicating a rule that is enforced structurally elsewhere
// invites the two copies to drift.
//
// `_relation_history` is NOT admitted. Relation history is gated on BOTH
// endpoints and has its own lineage rules; it is a separate surface that has
// not been world-scoped or tested, and this arc's default is deny.
func isWorldCapableHistoryPath(trimmed string) bool {
	parts := strings.Split(trimmed, "/")
	if len(parts) < 3 || len(parts) > 4 || parts[0] != "_history" {
		return false
	}
	return parts[1] != "" && parts[2] != ""
}

// configuredDefaultWorld returns the operator's browsing default
// (`app.default_world`), or "" when none is set or no config is loaded.
//
// A free function taking the App rather than a method, for the same reason
// resolveWorld is one: App sits at its plimsoll load line, and adding a
// method to it is the habit that got it there.
//
// Tolerates an App with no schema state: middleware is exercised in tests
// against a bare App, and a nil-deref there would be a panic in a path whose
// whole job is to be transparent when unconfigured.
func configuredDefaultWorld(a *App) string {
	if a == nil {
		return ""
	}
	state := a.State()
	if state == nil || state.Cfg == nil {
		return ""
	}
	return state.Cfg.App.DefaultWorld
}

// readOnlyMethod reports whether a method only reads. Worlds are a read-side
// routing rule, so this gates BOTH the refusal of an explicit `?world=` on a
// write and the application of the operator's browsing default — a write must
// address a face by id, never by world.
func readOnlyMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead ||
		method == http.MethodOptions
}

// attachWorld resolves `?world=` once per request and binds the handle to
// the context, refusing every combination this surface cannot serve.
//
// Runs INSIDE attachACLRequest (so the read gate it needs for the grant
// check is already on the context) and before any handler, which is what
// makes "the grant check happens before a resolver is constructed"
// structural rather than a convention each handler must remember.
func attachWorld(next http.Handler, a *App) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Any occurrence counts, including an EMPTY value: resolveWorld
		// decides on the values slice (a duplicate is a 400, an empty value
		// is the default world), so this must agree with it or
		// `?world=&world=published` slips past the write refusal and the
		// duplicate check as "not explicit" while resolveWorld sees two
		// values. One question, one spelling.
		explicit := len(r.URL.Query()[WorldParam]) > 0
		// The operator's browsing default applies only where the path can
		// actually serve a non-default world. Without this clause, merely
		// setting `app.default_world` would turn every bare request to a
		// non-world-capable route — relations, attachments, exports, sync —
		// into a 422 `world_unsupported`, breaking the deployment wholesale
		// rather than fixing the cliff it was set to fix.
		//
		// This is also precisely what the SPA does: `useWorld().worldParam`
		// is attached at specific call sites (lists, entity detail, views,
		// history, kanban, next-action), never blanket-applied to every
		// fetch. Restricting the server-side default to the same set is what
		// makes the two agree.
		//
		// An EXPLICIT `?world=` on such a path still refuses, loudly, below:
		// a caller who named a world deserves to be told the route cannot
		// serve it, whereas a caller who named nothing asked for no such
		// thing and gets the default world.
		configured := ""
		if !explicit && readOnlyMethod(r.Method) && worldCapablePath(r.URL.Path) {
			// Read PER REQUEST, never captured at wiring: the config is
			// hot-reloaded by the watcher, so a value read once at startup
			// would go stale the first time an operator edits
			// data-entry.yaml.
			configured = configuredDefaultWorld(a)
		}
		if !isAPIPath(r.URL.Path) || (!explicit && configured == "") {
			next.ServeHTTP(w, r)
			return
		}
		// A write addresses the row its ID names — `POL-1@published` one
		// face of a faced type, `POL-1` the single state of a type
		// declaring none — but it never takes a
		// world. A world is a read-side routing rule that can answer with a
		// FALLBACK face, so a write riding that indirection would save the
		// wrong state's content: `PATCH ...?world=published` would silently
		// edit the DRAFT of an entity with no published face, and
		// `DELETE ...?world=published` would delete the entity outright
		// while the caller believed they were unpublishing. Refusing the
		// parameter keeps the target of every write named directly.
		//
		// Only an EXPLICIT parameter is refused. A bare write carries no
		// world to refuse — the operator's browsing default is a read-side
		// presentation rule, so applying it to a PATCH and then rejecting
		// the request would make `app.default_world` break every write on
		// the deployment.
		if explicit && !readOnlyMethod(r.Method) {
			writeV1Error(w, r, http.StatusUnprocessableEntity, "world_read_only",
				"worlds are read-only on this API",
				"omit ?world= — address the face directly by id (`ID` or `ID@face`)")
			return
		}
		handle, err := resolveWorld(r, a.worlds, configured)
		// The world this request resolved to, for diagnostics and for the
		// denied handle's name. NOT `r.URL.Query().Get(WorldParam)`, which is
		// empty when the operator's default supplied the world — reporting ""
		// there would name the wrong world in an error and, worse, hand
		// `worldHandle.name` an empty string, which `isDefault` reads as the
		// DEFAULT world and would turn a denial into a default-world serve.
		requested := r.URL.Query().Get(WorldParam)
		if !explicit {
			requested = configured
		}
		// Two guards, then one dispatch. The ORDER is the security property:
		// both guards answer without consulting the grant, so neither can
		// distinguish principals, and the arms below — which do consult it —
		// cannot reach a route a permitted world would have been refused on.
		// See [refuseWorldIncapablePath].
		if refuseWorldConfigError(w, r, requested, err) {
			return
		}
		if refuseWorldIncapablePath(w, r, requested) {
			return
		}
		switch {
		case errors.Is(err, errWorldDenied):
			// NOT a 403, and NOT a synthetic body either. The request
			// continues with a handle marked `denied`, so the ORDINARY
			// handler runs and renders its own empty result — same JSON
			// shape, same `meta`, same `_actions`, same pagination headers
			// as a world that genuinely holds nothing this caller may read.
			//
			// Writing a hand-built `{"data":[]}` here was the first attempt
			// and it was wrong: the real list response carries meta/_actions/
			// X-Total-Count, so the bare body announced the denial on the
			// first byte — turning the thing designed to close an existence
			// oracle into one.
			//
			// Continuing is only safe on a route that can honor a world at
			// all, which is what the guard above establishes.
			next.ServeHTTP(w, r.WithContext(withWorld(r.Context(),
				worldHandle{name: requested, denied: true})))
			return
		case err != nil:
			// Infrastructure failure, not a denial.
			writeGateError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(withWorld(r.Context(), handle)))
	})
}

// worldProvenance labels HOW the request's world resolved this face
// (TKT-WRLDAPI item 2).
//
// # Labeling, not re-resolving
//
// Resolution itself happens exactly once, in the store: the world scope rides
// the query ([store.EntityQuery.World]) and the backend picks the prime. This
// function reads the answer back — the coordinate the returned row was stored
// at — and names the rule that must have produced it. It never fetches, never
// walks the chain, and cannot disagree with the store about WHICH face was
// served, because it is handed that face.
//
// That distinction matters: a second chain walk here would be a second
// implementation of the semantics deciding which face a reader sees, free to
// drift from the store's. The mapping below is total over the states the
// store can return, so there is nothing left to decide:
//
//   - type absent from the scope -> rule 1, "unscoped".
//   - face present in the chain -> rule 2, "chain", plus its chain INDEX.
//   - otherwise -> rule 3 under `otherwise: default`, "fallback-default".
//
// The index is what distinguishes the world's first choice from a later
// candidate standing in for it — a distinction the rule name alone cannot
// carry. See [resolutionRuleAt].
//
// # Why the ZERO coordinate in a chain is not a problem
//
// A [store.WorldScope] chain CAN carry the zero coordinate. Nothing a schema
// compiles to does so today — a declared name IS its stored coordinate
// (BUG-HC6I2T) and `entity.ParseFace` rejects the empty name — but the scope
// is a plain store type any caller may construct, so the totality of the
// mapping must not rest on the chain being zero-free. An earlier version of
// this comment rested on the opposite claim and was wrong for a different
// reason; the argument below holds either way.
//
// Totality does not need that invariant, because a zero coordinate in the
// chain is matched BY POSITION everywhere it matters, ahead of the fallback:
//
//   - here, slices.Index finds it and returns rule 2 with its real index;
//   - storeutil.WorldPrimes compares `coord != c.Face`, so a default row
//     matches the chain entry and takes its rank;
//   - pgstore's worldSQL emits `face = ” THEN i` for the chain entry
//     BEFORE the `otherwise: default` arm's `face = ” THEN len(chain)`,
//     and CASE takes the first match.
//
// The rank ordering is the load-bearing property, not the absence of "".
// Pinned by TestZeroInChain_Provenance (here), the storeutil chain tests, and
// TestWorldSQL_ZeroInChain (pgstore) — one per mechanism, because the three
// implementations agree by construction rather than by sharing code.
//
// The third arm is reachable only when the fallback actually fired: under
// `otherwise: exclude` the store returns NOTHING, so there is no response to
// label and the handler has already rendered a 404.
//
// The wire's `face` is the coordinate the row is stored at, which is also the
// name the operator wrote in `faces:` — a face has one spelling and the two
// cannot diverge (BUG-HC6I2T). It is empty only for a type declaring no faces,
// whose single state lives at the zero coordinate and has no name.
//
// Returns nil for a nil entity, so a caller may pass a not-found result
// through without branching.
func worldProvenance(ctx context.Context, e *entity.Entity) *v1.EntityWorld {
	if e == nil {
		return nil
	}
	handle := worldFromContext(ctx)
	name := handle.name
	if name == "" {
		name = defaultWorldName
	}
	rule, position := resolutionRuleAt(handle.scope, e.Type, e.Face)
	return &v1.EntityWorld{
		Name:          name,
		Face:          e.Face.String(),
		Via:           rule,
		ChainPosition: position,
	}
}

// The wire vocabulary for a resolution rule is [store.ResolutionRule]'s own
// String(); these names exist so a handler and its tests spell it once.
var (
	ruleUnscoped        = store.ResolutionUnscoped.String()
	ruleChain           = store.ResolutionChain.String()
	ruleFallbackDefault = store.ResolutionFallbackDefault.String()
)

// resolutionRule names the rule that produced a face stored at p, for an
// entity of entityType, under scope. See [worldProvenance] for why this is a
// total mapping rather than a walk.
//
// entityType MUST be canonical: [store.WorldScope] is keyed on canonical
// names only, and an alias reaching For() reads as an unknown type, which is
// rule 1. The route resolves the type by iterating the metamodel's own keys,
// so every caller on this path is canonical by construction.
func resolutionRule(scope store.WorldScope, entityType string, p entity.Face) string {
	rule, _ := resolutionRuleAt(scope, entityType, p)
	return rule
}

// resolutionRuleAt is [resolutionRule] plus the chain POSITION of the served
// coordinate — the rank the world got, not merely that it got something.
//
// The position is non-nil only for rule 2; the other rules did not resolve
// through the chain, so there is no rank to report and a zero would read as
// "first choice".
//
// # Why the rule alone was not enough
//
// "chain" says SOME selected coordinate exists, never WHICH. Under
// `select: [published, draft]` a real published face and a draft standing in
// for a missing one both reported "chain", so a `published`-world reader
// shown draft bytes could not tell — the silent substitution content states
// exist to prevent. `fallback-default` guards only the `otherwise:` arm,
// which fires when the chain matched NOTHING; a within-chain fallback slipped
// between the two.
//
// The index closes it without redefining anything: position 0 is the world's
// first choice, and any position > 0 is a within-chain fallback. Serving `en`
// from `[nl, en]` remains rule 2 — the world was OBEYED, not overridden, and
// calling it "fallback-default" would wrongly claim the `otherwise:` arm
// fired — but it is now visibly the second choice.
//
// This stays a LOOKUP, not a chain walk, for the reason [worldProvenance]
// gives: resolution already happened in the store, and re-deriving it here
// would be a second implementation of the semantics free to drift from it.
// slices.Index is the same read-back slices.Contains was, keeping the answer
// it had already computed instead of discarding it.
func resolutionRuleAt(
	scope store.WorldScope, entityType string, p entity.Face,
) (rule string, position *int) {
	r, pos := store.ResolutionAt(scope, entityType, p)
	if r == store.ResolutionChain {
		return r.String(), &pos
	}
	return r.String(), nil
}
