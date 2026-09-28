package dataentry

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// stubWorlds is a WorldLookup over a fixed name set. The scopes are
// non-default only in the sense that matters here — IsDefaultWorld() is
// false — which is what the refusal and stamping logic branch on.
type stubWorlds struct {
	names map[string]bool
	// resolveDefault makes the world resolve to each entity's default state
	// rather than excluding everything — see Lookup.
	resolveDefault bool
}

func (s stubWorlds) Lookup(name string) (store.WorldScope, bool) {
	if !s.names[name] {
		return store.WorldScope{}, false
	}
	if s.resolveDefault {
		// A world that resolves `ticket` to its DEFAULT state, so an entity
		// seeded without any content-state row is still visible in it. That
		// is what lets a test tell "the grant denied me" (empty) apart from
		// "the world excluded everything" (also empty) — without it, both
		// look identical and a grant-check test passes even when the check
		// never ran.
		return store.NewWorldScope(map[string]store.TypeResolution{
			"ticket": {Fallback: store.FallbackDefaultState},
		}), true
	}
	// A resolution for one type is enough to make IsDefaultWorld() false,
	// which is what every branch under test keys on. FallbackExclude is the
	// public-world shape: an entity with no matching state contributes
	// nothing.
	return store.NewWorldScope(map[string]store.TypeResolution{
		"ticket": {
			Chain:    []entity.Face{entity.Face("published")},
			Fallback: store.FallbackExclude,
		},
	}), true
}

func TestWorldCapablePath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path string
		want bool
		why  string
	}{
		{"/api/v1/tickets", true, "collection list is world-scoped"},
		{"/api/v1/tickets/TKT-1", true, "single-entity GET is world-scoped"},
		{"/api/v1/tickets/TKT-1/relations", false, "sub-resource reads through the ungated reader"},
		{"/api/v1/tickets/TKT-1/_export", false, "export reads through the ungated reader"},
		{"/api/v1/_search", true,
			"cross-type search takes the world on every executeQuery branch (BUG-SMPOZB)"},
		{"/api/v1/_search/x", false, "the match is exact, not a prefix"},
		{"/api/v1/_views/board", false,
			"a two-segment _views path is the standalone-view surface, which is NOT scoped"},
		{"/api/v1/_views/policy/POL-1", true,
			"the ENTITY view is world-scoped end to end (TKT-WRLDAPI item 4b)"},
		{"/api/v1/_views/policy/POL-1/extra", false,
			"a fourth segment is not the entity view; the match is exact, not a prefix"},
		{"/api/v1/_views//POL-1", false, "an empty type segment is not a view path"},
		{"/api/v1/_views/policy/", false, "an empty id segment is not the entity view"},
		{"/api/v1/_sidepanel/policy/POL-1", false,
			"the side panel shares executeView but was never scoped; it passes defaultViewWorld()"},
		{"/api/v1/_documents/report", false, "document render and its cache key are world-blind"},
		{"/api/v1/_position", true, "position recomputes a search or list page's set in its world (BUG-SMPOZB)"},
		{"/api/v1/_analyze", false, "whole-graph, tracer-backed"},
		// BUG-2: history WAS refused as "an orthogonal version axis". It is not
		// orthogonal — `entity_versions` is keyed by content state (TKT-C1XUA8),
		// so a draft and its published face have different histories, and
		// refusing the world made a world-bound page show the DEFAULT face's
		// history: the wrong record presented as the right one.
		{"/api/v1/_history/ticket/TKT-1", true,
			"entity history is per-FACE, so it must be able to name the world"},
		{"/api/v1/_history/ticket/TKT-1/3", true,
			"one version's snapshot is the same per-face record"},
		{"/api/v1/_history/ticket/TKT-1/3/restore", false,
			"a fourth segment past the version is restore, a WRITE; " +
				"attachWorld refuses `?world=` on a non-GET regardless"},
		{"/api/v1/_history/ticket", false, "no id segment is not an entity's history"},
		{"/api/v1/_history/ticket/", false, "an empty id segment is not an entity's history"},
		{"/api/v1/_history//TKT-1", false, "an empty type segment is not an entity's history"},
		{"/api/v1/_relation_history/decision/DEC-1/addresses/REQ-1", false,
			"relation history is a separate, unscoped surface — gated on BOTH " +
				"endpoints with its own lineage rules"},
		{"/api/sync/manifest", false, "outside the versioned API"},
		{"/api/v1/", false, "the bare mount carries no data"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			if got := worldCapablePath(tc.path); got != tc.want {
				t.Errorf("worldCapablePath(%q) = %v, want %v (%s)",
					tc.path, got, tc.want, tc.why)
			}
		})
	}
}

// TestWorldDefaultDenyIsTheDefault is the structural claim behind the
// allowlist: a path nobody has thought about is REFUSED, so shipping a leak
// requires someone to widen the predicate — a visible, reviewable act —
// rather than to forget a store call site.
func TestWorldDefaultDenyIsTheDefault(t *testing.T) {
	t.Parallel()
	for _, p := range []string{
		"/api/v1/_something_invented_tomorrow",
		"/api/v1/tickets/TKT-1/some/deep/subresource",
		"/api/v2/tickets",
		"/api/v1/tickets/TKT-1/_attachments",
	} {
		if worldCapablePath(p) {
			t.Errorf("a path with no explicit decision must be refused a "+
				"non-default world; %q was permitted", p)
		}
	}
}

// TestAttachWorld_UnknownWorldIsNamed pins the CONFIG half of the
// deliberately-asymmetric denial semantics: a world name is operator-authored
// config, not a secret, so naming it beats a uniform silence.
func TestAttachWorld_UnknownWorldIsNamed(t *testing.T) {
	t.Parallel()
	app := &App{worlds: stubWorlds{names: map[string]bool{"published": true}}}
	rec := serveWorldRequest(t, app, "/api/v1/tickets?world=nosuchworld")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown world: got %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "nosuchworld") {
		t.Errorf("the response must NAME the missing world — it is config, "+
			"not a secret; got: %s", rec.Body.String())
	}
}

// TestDeniedWorldIsByteIdenticalToAnEmptyWorld is the CONTENT half of the
// denial semantics, and it compares the actual responses rather than
// grepping the body for the word "denied".
//
// The first version of this test did the latter, and passed against a
// hand-built `{"data":[]}` that omitted `meta`, `_actions` and five
// pagination headers the real list response carries. That bare body
// announced the denial on the first byte — turning the mechanism designed
// to close an existence oracle into one. The lesson generalizes: assert the
// property, not a proxy for it.
func TestDeniedWorldIsByteIdenticalToAnEmptyWorld(t *testing.T) {
	app := newTestAppV1(t)
	d := mustNewACL(t, &acl.Policy{
		Roles:       map[string]acl.RoleDef{"viewer": {Read: []string{"ticket"}}},
		Assignments: map[string]string{"alice": "viewer"},
	}, app.store)
	app.acl = d

	// (a) A world the principal MAY read, which happens to hold nothing.
	empty := listUnderWorld(t, app, worldHandle{name: "published",
		scope: excludeEverythingScope()})

	// (b) A world the principal may NOT read.
	denied := listUnderWorld(t, app, worldHandle{name: "published", denied: true})

	if empty.Body.String() != denied.Body.String() {
		t.Errorf("a denied world must be indistinguishable from an empty one.\n"+
			"  empty-world body:  %s\n  denied-world body: %s",
			empty.Body.String(), denied.Body.String())
	}
	for _, h := range []string{"X-Total-Count", "X-Page", "X-Per-Page", "Link", "Content-Type"} {
		if empty.Header().Get(h) != denied.Header().Get(h) {
			t.Errorf("header %s differs: empty=%q denied=%q — a client can tell "+
				"the denial from a genuinely empty world", h,
				empty.Header().Get(h), denied.Header().Get(h))
		}
	}
	if empty.Code != denied.Code {
		t.Errorf("status differs: empty=%d denied=%d", empty.Code, denied.Code)
	}
}

// excludeEverythingScope is a world that resolves the fixture's type to no
// face, so a principal who MAY read it still sees nothing.
func excludeEverythingScope() store.WorldScope {
	return store.NewWorldScope(map[string]store.TypeResolution{
		"ticket": {
			Chain:    []entity.Face{entity.Face("published")},
			Fallback: store.FallbackExclude,
		},
	})
}

// listUnderWorld runs the real list handler under a world handle.
func listUnderWorld(t *testing.T, app *App, h worldHandle) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tickets", http.NoBody)
	ctx := withWorld(withReadGate(aliceCtx(), nopReadGate{}), h)
	rec := httptest.NewRecorder()
	app.handleV1ListEntities(rec, req.WithContext(ctx), "ticket", "tickets")
	return rec
}

// TestAttachWorld_GateErrorIsNotADenial pins that an infrastructure failure
// stays distinguishable from a denial. Rendering it as an empty result would
// hide an outage behind a page that looks like a correctly-empty world.
func TestAttachWorld_GateErrorIsNotADenial(t *testing.T) {
	t.Parallel()
	app := &App{worlds: stubWorlds{names: map[string]bool{"published": true}}}

	ctx := withReadGate(context.Background(),
		fakeGate{worldErr: errors.New("store is down")})
	rec := serveWorldRequestCtx(ctx, t, app, "/api/v1/tickets?world=published")

	if rec.Code == http.StatusOK {
		t.Fatal("a gate failure must not render as an empty result — that hides " +
			"an outage behind a page that looks like a correctly-empty world")
	}
	if rec.Code != http.StatusInternalServerError && rec.Code != http.StatusGatewayTimeout {
		t.Errorf("a gate failure should surface as 5xx; got %d", rec.Code)
	}
}

// TestAttachWorld_SearchIsAdmitted pins the removal of the
// `world_search_unsupported` refusal at the HTTP boundary.
//
// It asserts only ADMISSION — that `?q=` beside `?world=` is no longer
// rejected out of hand. Whether the hits are correctly world-scoped is a
// different claim, made against a real searcher and real faces in
// listworld_search_test.go; this app has neither, so it could not make it.
// Kept separate deliberately: the middleware and the resolution are two
// distinct failure modes, and a single test covering both would leave the
// boundary untested whenever the resolution test is the one that breaks.
func TestAttachWorld_SearchIsAdmitted(t *testing.T) {
	t.Parallel()
	app := &App{worlds: stubWorlds{names: map[string]bool{"published": true}}}
	rec := serveWorldRequest(t, app, "/api/v1/tickets?world=published&q=onboarding")

	if rec.Code == http.StatusUnprocessableEntity {
		t.Fatalf("free-text search under a world is world-scoped now (a world IS "+
			"the search scope), so the middleware must not refuse it; got %d %s",
			rec.Code, rec.Body)
	}
}

// TestAttachWorld_UnsupportedRouteRefuses pins the allowlist at the HTTP
// boundary.
func TestAttachWorld_UnsupportedRouteRefuses(t *testing.T) {
	t.Parallel()
	app := &App{worlds: stubWorlds{names: map[string]bool{"published": true}}}
	rec := serveWorldRequest(t, app, "/api/v1/tickets/TKT-1/relations?world=published")

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a route that cannot honor a world must refuse; got %d %s",
			rec.Code, rec.Body)
	}
}

// TestAttachWorld_DefaultWorldIsUnaffected is the backward-compatibility
// guarantee: no `?world=`, or an explicit `?world=default`, behaves exactly
// as it did before worlds existed — including on routes the allowlist
// refuses, since those refuse only NON-default worlds.
func TestAttachWorld_DefaultWorldIsUnaffected(t *testing.T) {
	t.Parallel()
	app := &App{worlds: stubWorlds{names: map[string]bool{"published": true}}}
	for _, path := range []string{
		"/api/v1/tickets",
		"/api/v1/tickets?world=default",
		"/api/v1/_search?q=x",
		"/api/v1/_search?q=x&world=default",
		"/api/v1/tickets/TKT-1/relations?world=default",
	} {
		rec := serveWorldRequest(t, app, path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: the default world must pass through untouched; got %d %s",
				path, rec.Code, rec.Body)
		}
	}
}

// TestAttachWorld_NoWorldsWiredRefuses pins that a deployment whose wiring
// never called SetWorlds cannot acquire the parameter by accident.
func TestAttachWorld_NoWorldsWiredRefuses(t *testing.T) {
	t.Parallel()
	app := &App{} // SetWorlds never called
	rec := serveWorldRequest(t, app, "/api/v1/tickets?world=published")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("without SetWorlds every named world is unknown; got %d", rec.Code)
	}
}

// TestWorldHandleIsConstructedOnce pins §4.4's handle discipline: what
// reaches a handler is a fully-constructed handle, not a name it re-resolves.
// A handle cannot be reinterpreted; a name can, and a trace that flips worlds
// mid-walk is incoherent.
func TestWorldHandleIsConstructedOnce(t *testing.T) {
	t.Parallel()
	w := worldHandle{name: "published"}
	if !w.isDefault() {
		t.Error("a handle carrying only a name has the zero scope and IS the " +
			"default world — the scope is what reads resolve against")
	}
	if got := worldFromContext(context.Background()); !got.isDefault() {
		t.Error("an unstamped context must be the default world")
	}
}

// --- structural guards -------------------------------------------------

// TestGrantCheckPrecedesResolverConstruction is the STRUCTURAL pin for the
// ordering the whole design turns on: the per-world read grant is checked
// before any world-resolved read can happen.
//
// Behavioral tests alone would stay green for their own fixtures if someone
// later moved the check into a handler, or reordered the middleware so the
// world gate ran before the ACL gate was on the context — in which case
// resolveWorld would consult nopReadGate, whose PermitsWorld returns true,
// and every world would be permitted regardless of policy.
//
// This scans the router source for the two wrap calls and asserts
// attachWorld is wrapped FIRST. Per the wrap-order note in router.go, the
// LAST wrap is the OUTERMOST, so wrapping attachWorld first makes it the
// INNERMOST of the pair and therefore the LATER to run.
func TestGrantCheckPrecedesResolverConstruction(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	body := string(src)
	world := strings.Index(body, "handler = attachWorld(")
	aclWrap := strings.Index(body, "handler = attachACLRequest(")
	if world < 0 || aclWrap < 0 {
		t.Fatalf("could not find both wrap sites (attachWorld=%d attachACL=%d) — "+
			"if these were renamed, update this guard rather than deleting it",
			world, aclWrap)
	}
	if world > aclWrap {
		t.Error("attachWorld must be wrapped BEFORE attachACLRequest so it runs " +
			"AFTER it: the world grant check consults readGateFromContext, and " +
			"reversing this makes it see nopReadGate — which permits every " +
			"world, silently, regardless of policy")
	}
}

// TestWorldCapableRoutesDoNotUseUngatedReader is the guard entityReader's
// doc comment promises.
//
// entityReader is default-world-only by decision. That is safe only while the
// routes it serves are refused a non-default world. This asserts the two
// world-capable handlers do not reach it — a world-bound response assembled
// partly from world-resolved rows and partly from default-state ones is the
// mixed-face bug that would be hardest to see, because the entity would look
// right and its neighbors would not.
func TestWorldCapableRoutesDoNotUseUngatedReader(t *testing.T) {
	t.Parallel()

	// Every function on a world-capable request's path, not just the two
	// store helpers. Scanning only the helpers is how the first version of
	// this guard passed while both world-capable HANDLERS wrapped their
	// world-resolved rows in default-world relations and neighbors: it
	// checked a proxy for the design instead of the design.
	//
	// A reader reached here must be world-aware, or the call must be
	// conditional on the default world (which is how the relation reads on
	// these two handlers are now written — see the worldBound branches).
	worldCapableFuncs := map[string]bool{
		"scopedSortedEntities": true,
		"getWorldEntity":       true,
		"handleV1ListEntities": true,
		"handleV1GetEntity":    true,
		"resolveV1Includes":    true,
		"computeEntityETag":    true,
		// TKT-WRLDAPI item 4 moved neighbor collection out of
		// resolveV1Includes and down into includeCandidates, which dispatches
		// to one of two collectors. Without these entries the guard would
		// keep scanning resolveV1Includes — which no longer reaches the
		// reader at all — and pass while the reader call it was written to
		// catch sat two frames down. The `scanned` assertion below does not
		// protect against that: it counts what was found, not what was moved.
		//
		// defaultWorldCandidates is the one that USES the reader, and it is
		// listed precisely so the guard has something to check: it is
		// reader-using and world-consulting, which is the shape the guard
		// permits. worldCandidates must never appear in the reader-using set
		// at all.
		"includeCandidates":      true,
		"defaultWorldCandidates": true,
		"worldCandidates":        true,
		// TKT-WRLDAPI item 4b made `_views/{type}/{id}` world-capable, so its
		// handler joins the scanned set. It reached the ungated reader for the
		// entry's relations, which under a world would pair a resolved entry
		// with default-world edges — the mixed-face bug. Adding the route to
		// worldCapablePath without adding the handler here would have shipped
		// that silently, which is exactly what this guard is for.
		"handleV1Views": true,
		// The catch-all dispatcher for `/api/v1/{plural}[/{id}]` — the
		// world-capable family. It only parses the path and delegates, so it
		// reaches no reader today; it is listed because the DERIVED check below
		// requires every world-capable route's handler to be scanned, and
		// because a dispatcher that grows a reader call is exactly the change
		// that should fail loudly.
		"handleV1DynamicRoutes": true,
		// `_next_action` became world-capable when the next-action surface
		// gained its two world axes. The method dispatcher reaches no reader
		// — it splits GET from POST — but the derived check below requires
		// every world-capable route's handler to be scanned, and a dispatcher
		// that grows a reader call is exactly the change that should fail.
		//
		// Worth stating why this route is different from the others here:
		// its `?world=` is the DISPLAY world for each source's
		// `visible_worlds` allow list, and does NOT scope any read. Candidate
		// queries run in the world the OPERATOR declared per source
		// (`source_world:`), bound by nextActionSourceWorld, which replaces
		// the request's handle rather than inheriting it. So the mixed-face
		// bug this guard exists to catch cannot arrive by the request here —
		// but the reads still go through executeQuery / scopedSortedEntities,
		// which are scanned in their own right.
		"handleV1NextAction": true,
		// `_search` (BUG-SMPOZB). It reads through executeQuery and loads
		// bodies by (id, face) through loadRowContent; neither reaches the
		// entity reader.
		"handleV1Search": true,
		// `_position` (BUG-SMPOZB). Reads through resolveScope and the list
		// pushdown, both world-scoped.
		"handleV1EntityPosition": true,
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	var scanned int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || !worldCapableFuncs[fn.Name.Name] {
				return true
			}
			scanned++
			// A reader call is acceptable only inside a function that also
			// consults the world — i.e. it is guarded by an
			// IsDefaultWorld()/blocksAllReads() branch, or the whole
			// endpoint is refused for a non-default world. Requiring the
			// world to be MENTIONED is a coarse check, but it is the one
			// that fails loudly when someone adds an unguarded read.
			var usesReader, consultsWorld bool
			ast.Inspect(fn, func(inner ast.Node) bool {
				switch v := inner.(type) {
				case *ast.SelectorExpr:
					if x, ok := v.X.(*ast.SelectorExpr); ok && x.Sel != nil && x.Sel.Name == "reader" {
						usesReader = true
					}
					// The reader is also passed BY VALUE into package functions
					// now (item 4 moved these off App to stay under the
					// plimsoll cap), so `a.reader.X` is no longer the only
					// shape a reader call takes. A bare `reader.X` selector
					// counts too — without this, extracting a reader-using
					// loop into a helper silently leaves the guard's view,
					// which is exactly what happened once during item 4.
					if ident, ok := v.X.(*ast.Ident); ok && ident.Name == "reader" {
						usesReader = true
					}
				case *ast.Ident:
					// worldBoundRelations is the NAMED predicate the relation
					// branches share (it wraps worldFromContext so a denied
					// handle answers consistently). It counts as consulting
					// the world — otherwise introducing one shared predicate,
					// which is strictly better than three open-coded copies,
					// would fail this guard and pressure the next author back
					// into copying.
					//
					// servedFaceEdges counts for a different and STRONGER
					// reason. The face-scoping fix replaced the per-request
					// "is this world-bound" branch with an unconditional
					// "read the face being served", whose only surviving arm
					// is the wiring-shaped `worldNeighbors == nil` — a
					// deployment with no content states at all, where the
					// bare-id reader cannot merge faces because there is only
					// ever one. Such a function need not mention the world,
					// and demanding it did would pressure the next author
					// back into the per-request branch this fix removed.
					//
					// The bare identifier `worldNeighbors` is deliberately
					// NOT in this list, though the guarded handlers do test
					// `a.worldNeighbors == nil`. It appears in the
					// world-scoped arm of every one of these branches too, so
					// accepting it would mark the function world-consulting
					// no matter what the DEFAULT arm did — verified by
					// mutation: with it listed, replacing the nil check with
					// `if true` (an unconditionally ungated read) still
					// passed. The condition must be recognized by something
					// the unguarded shape cannot also contain.
					switch v.Name {
					case "worldScopeFrom", "worldFromContext", "worldBoundRelations",
						"servedFaceEdges":
						consultsWorld = true
					}
				}
				return true
			})
			if usesReader && !consultsWorld {
				t.Errorf("%s (%s) reaches the ungated entityReader without "+
					"consulting the world. That reader is DEFAULT-WORLD-ONLY, so "+
					"a world-bound response would pair a resolved entity with "+
					"draft relations and draft neighbors — the mixed-face bug "+
					"that reads as correct. Guard the call on "+
					"worldScopeFrom(ctx).IsDefaultWorld(), or refuse the route.",
					fn.Name.Name, name)
			}
			return false
		})
	}
	// A guard that scanned nothing is not a guard: a rename would otherwise
	// make this pass silently forever.
	if scanned != len(worldCapableFuncs) {
		t.Fatalf("scanned %d of %d world-capable functions — if these were "+
			"renamed, update this guard rather than letting it go quiet",
			scanned, len(worldCapableFuncs))
	}

	// And the enumeration itself must be COMPLETE — see
	// TestWorldCapableRoutesAreAllScanned.
	assertEveryWorldCapableRouteIsScanned(t, worldCapableFuncs)
}

// assertEveryWorldCapableRouteIsScanned derives the set of world-capable
// ROUTE HANDLERS from the route table and fails on any that
// TestWorldCapableRoutesDoNotUseUngatedReader does not scan.
//
// # Why this exists (RULING 15, applied to a test)
//
// The scanned set above is a hand-written literal. That makes the guard a
// check whose FAILURE MODE IS SILENCE: when a route becomes world-capable and
// its handler is not in the list, the guard does not fail — it passes while
// checking nothing.
//
// That is not hypothetical. It happened TWICE in this epic:
//
//   - item 4 extracted a reader-using loop into a helper, which left the
//     guard's view entirely; and
//   - item 4b admitted `_views/{type}/{id}` to worldCapablePath while
//     handleV1Views stayed unlisted — hiding a real leak (the entry's
//     relations were still read through the ungated, default-world reader)
//     that was found by READING the code, not by testing it.
//
// The third instance is the one that ships, because by then the guard is
// trusted. So the enumeration is now derived rather than asserted: a new
// world-capable route fails HERE, loudly, naming the route and the handler.
func assertEveryWorldCapableRouteIsScanned(t *testing.T, scanned map[string]bool) {
	t.Helper()

	for _, route := range registeredAPIRoutes(t) {
		if !worldCapablePath(probePathFor(route.pattern)) {
			continue
		}
		if !scanned[route.handler] {
			t.Errorf("route %q is WORLD-CAPABLE but its handler %q is not in "+
				"the scanned set of TestWorldCapableRoutesDoNotUseUngatedReader.\n"+
				"A world-capable handler that nobody scans can reach the "+
				"ungated, DEFAULT-WORLD entityReader and pair a resolved entity "+
				"with draft relations — the mixed-face bug — with no test "+
				"failing. Add %q to worldCapableFuncs (and make sure it "+
				"consults the world), or narrow worldCapablePath.",
				route.pattern, route.handler, route.handler)
		}
	}
}

// apiRoute is one `mux.HandleFunc(pattern, handler)` registration.
type apiRoute struct {
	pattern string // e.g. "/api/v1/_views/"
	handler string // the final selector, e.g. "handleV1Views"
}

// registeredAPIRoutes parses the route table out of the package source.
//
// Parsing rather than calling the registration function: building a real App
// needs a project on disk and a store, and this assertion is about the STATIC
// shape of the route table, which is exactly what the source states.
func registeredAPIRoutes(t *testing.T) []apiRoute {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	var routes []apiRoute
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "HandleFunc" {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			pattern := strings.Trim(lit.Value, `"`)
			if !strings.HasPrefix(pattern, "/api/v1/") {
				return true
			}
			if h := handlerName(call.Args[1]); h != "" {
				routes = append(routes, apiRoute{pattern: pattern, handler: h})
			}
			return true
		})
	}
	if len(routes) == 0 {
		t.Fatal("parsed no /api/v1/ routes — the registration shape changed; " +
			"update this derivation rather than letting the guard go quiet")
	}
	return routes
}

// handlerName renders the final selector of a handler expression
// (`a.views.handleV1Views` -> "handleV1Views").
func handlerName(arg ast.Expr) string {
	switch fn := arg.(type) {
	case *ast.SelectorExpr:
		if fn.Sel != nil {
			return fn.Sel.Name
		}
	case *ast.Ident:
		return fn.Name
	}
	return ""
}

// probePathFor turns a mux pattern into a concrete path to ask
// worldCapablePath about.
//
// A trailing slash means a prefix route, so it is probed with plausible
// segments; `/api/v1/` (the dynamic catch-all) is probed as an entity path,
// since that is what it actually serves.
func probePathFor(pattern string) string {
	switch {
	case pattern == "/api/v1/":
		return "/api/v1/tickets/TKT-1"
	case strings.HasSuffix(pattern, "/"):
		return pattern + "sometype/SOME-1"
	default:
		return pattern
	}
}

// --- helpers -----------------------------------------------------------

func serveWorldRequest(t *testing.T, a *App, target string) *httptest.ResponseRecorder {
	t.Helper()
	return serveWorldRequestCtx(context.Background(), t, a, target)
}

// serveWorldRequestCtx runs a request through attachWorld alone, with a
// terminal handler that reports success. That isolates the middleware's
// decision from every handler's own behavior, which is what these tests are
// about.
func serveWorldRequestCtx(
	ctx context.Context, t *testing.T, a *App, target string,
) *httptest.ResponseRecorder {
	t.Helper()
	h := attachWorld(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}), a)
	req := httptest.NewRequest(http.MethodGet, target, http.NoBody).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestWorldListGetParity_ACLGatedPrincipal is the RR-GQWRLD shape.
//
// Under a world, the LIST path and the single-entity GET must agree about
// which face exists. They reach the store by different routes — the list
// composes a query the ACL layer built, the GET goes through visibleReader —
// so a world stamped on one and not the other diverges silently.
//
// The principal is deliberately ACL-GATED (a policy read grant, so the list
// takes its GraphQuery branch rather than AllowAll). In Step 2 the equivalent
// bug was live for an entire review window because the parity test covered
// only the unrestricted principal, whose reads take the other branch
// entirely. Testing AllowAll here would pass against the broken code.
func TestWorldListGetParity_ACLGatedPrincipal(t *testing.T) {
	app := newTestAppV1(t)
	ctx := context.Background()

	// TKT-100 has a published face; TKT-200 has only its default (draft)
	// face, so a world selecting `published` with otherwise:exclude must
	// drop it entirely.
	seedEntity(app, &entity.Entity{
		ID: "TKT-100", Type: "ticket",
		Properties: map[string]any{"title": "draft face"},
	})
	seedEntity(app, &entity.Entity{
		ID: "TKT-200", Type: "ticket",
		Properties: map[string]any{"title": "draft only"},
	})
	published := entity.Face("published")
	if err := app.store.CreateEntity(ctx, &entity.Entity{
		ID: "TKT-100", Type: "ticket", Face: published,
		Properties: map[string]any{"title": "published face"},
	}); err != nil {
		t.Fatalf("seed published state: %v", err)
	}

	// An ACL-GATED principal, and the gating must be REAL: the read grant
	// is conferred by a RELATION, so ReadQuery composes a store.GraphQuery
	// instead of returning AllowAll. A global assignment would take the
	// AllowAll branch, which reads the EntityQuery — the wrong half of the
	// two this test exists to compare (and the blind spot that let the
	// equivalent Step 2 bug ship: a parity test on the unrestricted
	// principal passes against the broken code).
	seedEntity(app, &entity.Entity{
		ID: "PERSON-1", Type: "feature",
		Properties: map[string]any{"title": "alice"},
	})
	d := mustNewACL(t, &acl.Policy{
		Roles: map[string]acl.RoleDef{"viewer": {Read: []string{"ticket"}}},
		RoleRelations: map[string]acl.RoleRelationDef{
			"owned-by": {Confers: "viewer"},
		},
	}, app.store)
	app.acl = d

	// The conferring edges run FROM alice (readQuery composes HasInbound with
	// the principal as the endpoint): alice owns both tickets, so both are in her
	// read scope and the world — not the ACL — is what removes TKT-200.
	for _, id := range []string{"TKT-100", "TKT-200"} {
		if _, err := app.store.CreateRelation(ctx, "alice", "owned-by", id, nil); err != nil {
			t.Fatalf("seed owned-by for %s: %v", id, err)
		}
	}

	scope := store.NewWorldScope(map[string]store.TypeResolution{
		"ticket": {Chain: []entity.Face{published}, Fallback: store.FallbackExclude},
	})
	req, rerr := d.ForPrincipal(principal.Principal{User: "alice", Tool: principal.ToolDataEntry})
	if rerr != nil {
		t.Fatalf("ForPrincipal: %v", rerr)
	}
	gate, gerr := newACLReadGate(req)
	if gerr != nil {
		t.Fatalf("newACLReadGate: %v", gerr)
	}
	// Precondition: the gate must take the QUERY branch, not AllowAll —
	// otherwise this test silently exercises the wrong path.
	if rqr := gate.ReadQuery(aliceCtx(), "ticket"); rqr.AllowAll || rqr.Query == nil {
		t.Fatalf("this test requires an ACL-gated principal whose ReadQuery "+
			"composes a GraphQuery; got AllowAll=%v Query=%v", rqr.AllowAll, rqr.Query)
	}
	wctx := withWorld(withReadGate(aliceCtx(), gate),
		worldHandle{name: "published", scope: scope})

	// LIST under the world.
	listed, err := app.scopedSortedEntities(wctx, "ticket", nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	inList := map[string]string{}
	for _, e := range listed {
		inList[e.ID], _ = e.Properties["title"].(string)
	}

	// GET under the same world, for each id.
	for _, id := range []string{"TKT-100", "TKT-200"} {
		got, found, gerr := app.visibleReader.getVisible(wctx, "ticket", id)
		if gerr != nil {
			t.Fatalf("get %s: %v", id, gerr)
		}
		listTitle, inListed := inList[id]
		if found != inListed {
			t.Errorf("%s: GET found=%v but list membership=%v — the list and the "+
				"single-entity path disagree about which faces exist in this world",
				id, found, inListed)
			continue
		}
		if !found {
			continue
		}
		if title, _ := got.Properties["title"].(string); title != listTitle {
			t.Errorf("%s: GET served %q but the list served %q — the two paths "+
				"resolved DIFFERENT faces of the same entity", id, title, listTitle)
		}
	}

	// The world's actual content, asserted directly so a parity test that
	// agreed on the WRONG answer (both serving drafts) still fails.
	if title := inList["TKT-100"]; title != "published face" {
		t.Errorf("the world must serve the published face; list gave %q", title)
	}
	if _, present := inList["TKT-200"]; present {
		t.Error("TKT-200 has no published face and `otherwise: exclude` must " +
			"drop it — existence in a world IS the publication bit")
	}
}

// TestAttachWorld_WritesRefuseAWorld pins the rule at the HTTP boundary:
// a world is a READ-side decorator, and no write on this API honors one.
// Writes stay face-aware — they name their target by id — they just do
// not take a `?world=`.
//
// Without this, `PATCH /api/v1/tickets/TKT-1?world=published` returns 200
// having edited the DRAFT, and `DELETE ...?world=published` deletes the
// entity outright while the caller believed they were unpublishing. Both
// look successful, which is what makes them dangerous — the parameter is
// accepted, so the caller has no reason to doubt it was honored.
func TestAttachWorld_WritesRefuseAWorld(t *testing.T) {
	t.Parallel()
	app := &App{worlds: stubWorlds{names: map[string]bool{"published": true}}}
	for _, method := range []string{
		http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete,
	} {
		h := attachWorld(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}), app)
		req := httptest.NewRequest(method, "/api/v1/tickets/TKT-1?world=published", http.NoBody)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s with ?world= must be refused (worlds are read-only); got %d",
				method, rec.Code)
		}
	}
	// The same writes without ?world= are untouched.
	h := attachWorld(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), app)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/tickets/TKT-1", http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("a write without ?world= must pass through untouched; got %d", rec.Code)
	}
}

// TestAttachWorld_IncludeIsAcceptedUnderAWorld pins the REMOVAL of the
// `?include=` refusal (TKT-WRLDAPI item 4, RULING 12).
//
// The refusal existed because neighbor resolution went through the ungated,
// default-world reader, so a world-bound response with includes would have
// been a published entity wrapped in draft neighbors. Neighbor resolution is
// world-scoped now, so the combination is served rather than refused.
//
// This asserts the middleware LETS IT THROUGH; that what comes back is
// actually this world's faces is the job of the end-to-end tests in
// worldneighbors_test.go. Both halves are needed: a middleware that accepts
// the parameter while the handler ignores it would pass this test and serve
// the wrong faces, which is why this one is deliberately not the only
// coverage of the removal.
func TestAttachWorld_IncludeIsAcceptedUnderAWorld(t *testing.T) {
	t.Parallel()
	app := &App{worlds: stubWorlds{names: map[string]bool{"published": true}}}
	rec := serveWorldRequest(t, app, "/api/v1/tickets/TKT-1?world=published&include=*")
	if rec.Code == http.StatusUnprocessableEntity {
		t.Fatalf("?include= under a world is no longer refused — neighbor "+
			"resolution is world-scoped (RULING 12); got %d %s", rec.Code, rec.Body)
	}
}

// TestAttachWorld_DuplicateParamRejected pins that `?world=a&world=b` is an
// error rather than silently resolving to the first value — a client-side
// param-append bug must not become a silent wrong-face serve.
func TestAttachWorld_DuplicateParamRejected(t *testing.T) {
	t.Parallel()
	app := &App{worlds: stubWorlds{names: map[string]bool{"published": true}}}
	rec := serveWorldRequest(t, app, "/api/v1/tickets?world=default&world=published")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a duplicate ?world= must be rejected, not resolved by a "+
			"precedence rule nobody would remember; got %d %s", rec.Code, rec.Body)
	}
}

// TestWorldGrantCheckThroughTheRealRouter is the BEHAVIORAL half of the
// ordering guarantee, complementing the source scan above.
//
// The source scan asserts wrap order and would be fooled by a refactor that
// moved either call into a helper. This one goes through the real router
// with a real Declarative ACL that denies the world, and asserts the denial
// took effect — which fails if the world gate ever runs before the read gate
// is on the context, because it would then see nopReadGate and permit
// everything.
func TestWorldGrantCheckThroughTheRealRouter(t *testing.T) {
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{
		ID: "TKT-900", Type: "ticket", Properties: map[string]any{"title": "draft"},
	})
	// A policy granting read on ticket but NO world grant, so `published`
	// is denied.
	app.acl = mustNewACL(t, &acl.Policy{
		Roles:       map[string]acl.RoleDef{"viewer": {Read: []string{"ticket"}}},
		Assignments: map[string]string{"alice": "viewer"},
	}, app.store)
	app.SetWorlds(stubWorlds{
		names:          map[string]bool{"published": true},
		resolveDefault: true,
	})
	// The router's stamper replaces the ctx principal, so identity has to
	// arrive the way production supplies it.
	app.SetPrincipalResolver(func(*http.Request) principal.Principal {
		return principal.Principal{User: "alice", Tool: principal.ToolDataEntry}
	})

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/tickets?world=published", http.NoBody)
	rec := httptest.NewRecorder()
	app.NewRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("a denied world renders as an ordinary empty result; got %d %s",
			rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "TKT-900") {
		t.Error("the principal holds no grant for world:published, so the world " +
			"gate must have denied it — seeing the entity means the check ran " +
			"before the read gate was on the context and saw nopReadGate")
	}
}

// --- app.default_world, applied server-side -----------------------------

// appWithDefaultWorld builds an App whose config names a browsing default,
// with `published` declared.
func appWithDefaultWorld(t *testing.T, name string) *App {
	t.Helper()
	a := &App{worlds: stubWorlds{names: map[string]bool{"published": true}}}
	cfg := &Config{}
	cfg.App.DefaultWorld = name
	a.schema.Publish(&Schema{Cfg: cfg})
	return a
}

// boundWorld runs a request through attachWorld and reports the handle the
// middleware bound, which is the thing these tests are actually about — a
// status code cannot distinguish "resolved to published" from "resolved to
// the default world".
func boundWorld(
	ctx context.Context, t *testing.T, a *App, method, target string,
) (bound worldHandle, status int) {
	t.Helper()
	var got worldHandle
	h := attachWorld(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = worldFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}), a)
	req := httptest.NewRequest(method, target, http.NoBody).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return got, rec.Code
}

// TestDefaultWorld_AppliesToABareRequest is QA finding 1.
//
// `app.default_world` used to be serialized to `/_schema` and applied only by
// the SPA (useWorld.ts), so a bare `curl` and the browser disagreed about the
// same URL: a face-scoped role read NOTHING from the raw API despite holding a
// valid grant, which reads as a broken ACL rather than a missing parameter.
func TestDefaultWorld_AppliesToABareRequest(t *testing.T) {
	t.Parallel()
	a := appWithDefaultWorld(t, "published")
	got, code := boundWorld(context.Background(), t, a, http.MethodGet, "/api/v1/tickets")
	if code != http.StatusOK {
		t.Fatalf("bare request must succeed; got %d", code)
	}
	if got.name != "published" {
		t.Errorf("a bare request must land in the operator's default world; got %q", got.name)
	}
	if got.isDefault() {
		t.Error("the handle must carry the configured world's scope, not the default world's")
	}
}

// TestDefaultWorld_ExplicitDefaultReachesRawFaces pins the escape hatch: with
// a default configured, `?world=default` is how a caller asks for the raw
// stored faces.
//
// This calls resolveWorld DIRECTLY rather than going through attachWorld,
// because attachWorld only computes `configured` when the parameter is absent
// — so from outside, `?world=default` and "no default configured" look
// identical and the test would pass against the bug it is meant to catch.
// (It did: mutating the absent-check from `len(values) == 0` to `name == ""`
// left the middleware-level version green.)
//
// `len(values) == 0` vs `name == ""` is the whole point. Once a default
// exists, "no parameter" and "the parameter names the default world" are
// different questions with different answers.
func TestDefaultWorld_ExplicitDefaultReachesRawFaces(t *testing.T) {
	t.Parallel()
	lookup := stubWorlds{names: map[string]bool{"published": true}}
	for _, tc := range []struct {
		name   string
		target string
		want   string // "" means the default world
	}{
		{"absent takes the configured default", "/api/v1/tickets", "published"},
		{"explicit default reaches raw faces", "/api/v1/tickets?world=default", ""},
		{"explicit world still wins", "/api/v1/tickets?world=published", "published"},
		// `?world=` PRESENT but empty is the case that makes `len(values) == 0`
		// and `name == ""` different spellings rather than the same one. A
		// client that builds the query string from an empty variable sent the
		// parameter and named the default world with it; honoring the
		// operator's default there would override an explicit request.
		{"present but empty is explicit", "/api/v1/tickets?world=", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, tc.target, http.NoBody)
			got, err := resolveWorld(req, lookup, "published")
			if err != nil {
				t.Fatalf("resolveWorld: %v", err)
			}
			if got.name != tc.want {
				t.Errorf("world = %q, want %q", got.name, tc.want)
			}
			if got.isDefault() != (tc.want == "") {
				t.Errorf("isDefault() = %v for world %q", got.isDefault(), got.name)
			}
		})
	}
}

// TestDefaultWorld_UnconfiguredIsUnchanged pins that a deployment which sets
// nothing behaves exactly as before.
func TestDefaultWorld_UnconfiguredIsUnchanged(t *testing.T) {
	t.Parallel()
	a := appWithDefaultWorld(t, "")
	got, code := boundWorld(context.Background(), t, a, http.MethodGet, "/api/v1/tickets")
	if code != http.StatusOK || !got.isDefault() {
		t.Errorf("no configured default must leave the request in the default world; got %q (%d)",
			got.name, code)
	}
}

// TestDefaultWorld_DoesNotBreakNonWorldCapableRoutes is the guard against the
// obvious way to implement this wrong.
//
// `worldCapablePath` is a deny-by-default allowlist: most routes refuse a
// non-default world with a 422. Blanket-applying the configured default would
// therefore turn every bare request to relations/attachments/exports/sync into
// a 422 the moment an operator set `app.default_world` — breaking the
// deployment wholesale rather than fixing the cliff it was set to fix.
//
// This also mirrors the SPA, which attaches `worldParam` at specific call
// sites rather than to every fetch.
func TestDefaultWorld_DoesNotBreakNonWorldCapableRoutes(t *testing.T) {
	t.Parallel()
	a := appWithDefaultWorld(t, "published")
	for _, path := range []string{
		"/api/v1/tickets/TKT-1/relations",
		"/api/v1/_analyze",
	} {
		got, code := boundWorld(context.Background(), t, a, http.MethodGet, path)
		if code != http.StatusOK {
			t.Errorf("%s: a bare request must not become an error merely because a "+
				"default world is configured; got %d", path, code)
		}
		if !got.isDefault() {
			t.Errorf("%s: a route that cannot serve a non-default world must stay in "+
				"the default world; got %q", path, got.name)
		}
	}
}

// TestDefaultWorld_ExplicitStillRefusedOnUnsupportedRoute pins the asymmetry
// the test above depends on: a caller who NAMED a world the route cannot serve
// is still told so. Only the caller who named nothing is quietly left in the
// default world.
func TestDefaultWorld_ExplicitStillRefusedOnUnsupportedRoute(t *testing.T) {
	t.Parallel()
	a := appWithDefaultWorld(t, "published")
	_, code := boundWorld(context.Background(), t, a, http.MethodGet,
		"/api/v1/tickets/TKT-1/relations?world=published")
	if code != http.StatusUnprocessableEntity {
		t.Errorf("an explicit ?world= on a route that cannot serve it must still "+
			"refuse; got %d", code)
	}
}

// TestDefaultWorld_DoesNotBreakWrites pins that a browsing default stays
// read-side. Applying it to a PATCH would trip the world_read_only refusal and
// make `app.default_world` break every write on the deployment.
func TestDefaultWorld_DoesNotBreakWrites(t *testing.T) {
	t.Parallel()
	a := appWithDefaultWorld(t, "published")
	for _, method := range []string{http.MethodPatch, http.MethodPost, http.MethodDelete} {
		got, code := boundWorld(context.Background(), t, a, method, "/api/v1/tickets")
		if code != http.StatusOK {
			t.Errorf("%s: a bare write must not be refused because a default world is "+
				"configured; got %d", method, code)
		}
		if !got.isDefault() {
			t.Errorf("%s: a write must address a face by id, never ride a world; got %q",
				method, got.name)
		}
	}
}

// TestDefaultWorld_GrantIsRecheckedAndDenialIsEmpty is the security-relevant
// half of applying the operator's default server-side.
//
// `app.default_world` is presentation, not policy: it changes which face a
// bare URL resolves to and grants nothing. So the per-world read grant must be
// checked for a defaulted world exactly as for an explicit `?world=` — the
// pre-change code returned before ever reaching PermitsWorld, and keeping that
// early return while sourcing the name from config would have turned the
// browsing default into a way to read a world the principal was denied.
//
// A denial must also render as the ORDINARY empty result, never a 403 and
// never a silent fall back to the default world: what a world CONTAINS is the
// secret this feature keeps, and falling back would serve the raw faces to
// someone the operator pointed at `published`.
func TestDefaultWorld_GrantIsRecheckedAndDenialIsEmpty(t *testing.T) {
	t.Parallel()
	a := appWithDefaultWorld(t, "published")

	t.Run("granted", func(t *testing.T) {
		t.Parallel()
		ctx := withReadGate(context.Background(),
			worldGate{permit: map[string]bool{"published": true}})
		got, code := boundWorld(ctx, t, a, http.MethodGet, "/api/v1/tickets")
		if code != http.StatusOK || got.name != "published" || got.blocksAllReads() {
			t.Errorf("a granted default world must resolve normally; got %q denied=%v (%d)",
				got.name, got.blocksAllReads(), code)
		}
	})

	t.Run("denied", func(t *testing.T) {
		t.Parallel()
		ctx := withReadGate(context.Background(),
			worldGate{permit: map[string]bool{}}) // published denied
		got, code := boundWorld(ctx, t, a, http.MethodGet, "/api/v1/tickets")
		if code == http.StatusForbidden {
			t.Fatal("a denied world must not answer 403: that confirms a world exists " +
				"holding things the caller may not see")
		}
		if !got.blocksAllReads() {
			t.Errorf("a denied default world must block all reads; got %q denied=%v",
				got.name, got.blocksAllReads())
		}
		if got.isDefault() {
			t.Error("a denied world must NOT fall back to the default world — that would " +
				"serve the raw faces to a caller the operator pointed at `published`")
		}
		if got.name != "published" {
			t.Errorf("the denied handle must name the world that was actually resolved, "+
				"not the empty query parameter; got %q", got.name)
		}
	})
}

// --- BUG-CV8L3B: a denied world must not outrun the route allowlist ------

// appForWorldGrant builds an App serving one ticket, with `published`
// declared and the principal holding the world grant or not.
//
// The seeded title is what a leak looks like: a route that answers a denied
// `?world=published` with default-world content spells it out in the body.
func appForWorldGrant(t *testing.T, granted bool) *App {
	t.Helper()
	app := newTestAppV1(t)
	seedEntity(app, &entity.Entity{
		ID: "TKT-900", Type: "ticket",
		Properties: map[string]any{"title": "secretdraft"},
	})
	role := acl.RoleDef{Read: []string{"ticket"}}
	if granted {
		role.Worlds = []string{"published"}
	}
	app.acl = mustNewACL(t, &acl.Policy{
		Roles:       map[string]acl.RoleDef{"viewer": role},
		Assignments: map[string]string{"alice": "viewer"},
	}, app.store)
	app.SetWorlds(stubWorlds{
		names:          map[string]bool{"published": true},
		resolveDefault: true,
	})
	app.SetPrincipalResolver(func(*http.Request) principal.Principal {
		return principal.Principal{User: "alice", Tool: principal.ToolDataEntry}
	})
	return app
}

// TestAttachWorld_DeniedWorldRefusedLikePermitted realizes
// AM-world-denied-matches-permitted-refusal, pinning BUG-CV8L3B.
//
// On a route outside worldCapablePath's allowlist, an explicit `?world=` must
// produce the SAME response whether or not the principal holds the world
// grant. Before the fix the denied case short-circuited past the refusal — a
// principal WITHOUT the grant got 200 and the default world's content, while
// one WITH it got 422.
//
// Asserting the two are IDENTICAL rather than merely "the denied one leaks
// nothing" is the point. It catches the disclosure and the grant oracle with
// one assertion, and it keeps failing for any future divergence between the
// paths — not only for this one.
func TestAttachWorld_DeniedWorldRefusedLikePermitted(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		// The confirmed leak: world-blind, and it renders entity titles.
		"/api/v1/_analyze?world=published",
		// Underscore routes refused wholesale by the allowlist.
		"/api/v1/_documents/report?world=published",
		"/api/v1/_sidepanel/ticket/TKT-900?world=published",
		// A sub-resource of an entity — the third-segment refusal.
		"/api/v1/tickets/TKT-900/relations?world=published",
		"/api/v1/tickets/TKT-900/_export?world=published",
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			serve := func(granted bool) *httptest.ResponseRecorder {
				app := appForWorldGrant(t, granted)
				rec := httptest.NewRecorder()
				app.NewRouter().ServeHTTP(rec,
					httptest.NewRequest(http.MethodGet, path, http.NoBody))
				return rec
			}
			permitted, denied := serve(true), serve(false)

			if denied.Code != permitted.Code {
				t.Errorf("status differs: permitted=%d denied=%d — the response "+
					"tells the caller whether they hold the world grant\n"+
					"  permitted: %s\n  denied:    %s",
					permitted.Code, denied.Code,
					permitted.Body.String(), denied.Body.String())
			}
			if denied.Body.String() != permitted.Body.String() {
				t.Errorf("body differs — a denied world must be indistinguishable "+
					"from a permitted one on a route that refuses both\n"+
					"  permitted: %s\n  denied:    %s",
					permitted.Body.String(), denied.Body.String())
			}
			if denied.Code != http.StatusUnprocessableEntity {
				t.Errorf("a non-world-capable route must refuse ?world= with 422 "+
					"world_unsupported; got %d %s", denied.Code, denied.Body)
			}
			if strings.Contains(denied.Body.String(), "secretdraft") {
				t.Error("LEAK: a denied world was served DEFAULT-world content — " +
					"the draft the world exists to withhold")
			}
		})
	}
}

// TestAttachWorld_DeniedWorldStillReachesCapableRoutes is the other half of
// the fix, and the reason it is scoped to the allowlist rather than made a
// blanket refusal.
//
// A denied world on a route that CAN serve one must still reach the handler,
// so the ordinary empty result renders with its real meta/_actions/headers.
// Refusing here instead would announce the denial on the first byte — the
// existence oracle the denied handle exists to close.
func TestAttachWorld_DeniedWorldStillReachesCapableRoutes(t *testing.T) {
	t.Parallel()
	app := appForWorldGrant(t, false)
	rec := httptest.NewRecorder()
	app.NewRouter().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/v1/tickets?world=published", http.NoBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("a denied world on a world-capable route renders as an ordinary "+
			"empty result, not a refusal; got %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "secretdraft") {
		t.Error("the principal holds no grant for world:published, so the list " +
			"must be empty")
	}
}

// TestRefuseWorldIncapablePath_EmptyWorldIsTheDefaultWorld pins the one
// spelling of the default world that [TestAttachWorld_DefaultWorldIsUnaffected]
// does not cover: an EXPLICIT but empty `?world=` on a route the allowlist
// refuses.
//
// It looks like a dead branch and is not. `?world=` makes `explicit` true, so
// the request survives attachWorld's `!explicit && configured == ""` early
// return and reaches the refusal with `requested == ""` — which
// [resolveWorld] treats as the default world. Without the empty check,
// `?world=` would 422 a request that means "the world I already had", and a
// client appending an unset parameter would break every refused route.
//
// The sibling spelling `?world=default` is already covered on
// `/tickets/TKT-1/relations` by that test; this is the gap beside it.
func TestRefuseWorldIncapablePath_EmptyWorldIsTheDefaultWorld(t *testing.T) {
	t.Parallel()
	app := &App{worlds: stubWorlds{names: map[string]bool{"published": true}}}
	for _, path := range []string{
		"/api/v1/_analyze?world=",
		"/api/v1/tickets/TKT-1/relations?world=",
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			rec := serveWorldRequest(t, app, path)
			if rec.Code != http.StatusOK {
				t.Errorf("an empty ?world= names the default world, which every "+
					"route serves; got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestRefuseWorldIncapablePath_DoesNotShadowConfigErrors pins the refusal's
// PLACE in attachWorld's ladder, which the fix for BUG-CV8L3B chose
// deliberately.
//
// A duplicate or unknown `?world=` is a config error true of every route, so
// it must answer identically on a world-capable and a world-incapable one.
// Hoisting the 422 above them (the first attempt) traded `no world named
// "bogus" is declared` — the diagnostic that tells an operator they typoed a
// name — for a vaguer refusal, on exactly the routes where they are most
// likely to be experimenting.
//
// The security property does not need that hoist: both 400s are reached
// without consulting the grant, so neither can distinguish the two principals.
func TestRefuseWorldIncapablePath_DoesNotShadowConfigErrors(t *testing.T) {
	t.Parallel()
	app := &App{worlds: stubWorlds{names: map[string]bool{"published": true}}}
	for _, tc := range []struct {
		name  string
		query string
		want  string
	}{
		{"duplicate", "?world=a&world=b", "duplicate_world"},
		{"unknown", "?world=bogus", "unknown_world"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			capable := serveWorldRequest(t, app, "/api/v1/tickets"+tc.query)
			incapable := serveWorldRequest(t, app, "/api/v1/_analyze"+tc.query)

			if capable.Code != http.StatusBadRequest || incapable.Code != http.StatusBadRequest {
				t.Fatalf("a config error is a 400 on every route; capable=%d incapable=%d",
					capable.Code, incapable.Code)
			}
			if !strings.Contains(incapable.Body.String(), tc.want) {
				t.Errorf("the world-incapable route must still report %s rather than "+
					"shadowing it with world_unsupported; got %s",
					tc.want, incapable.Body)
			}
			// `instance` legitimately echoes the request path, so the two
			// bodies differ there and only there — compare the diagnosis.
			if !strings.Contains(capable.Body.String(), tc.want) {
				t.Errorf("a config error must not depend on the route's world "+
					"capability: the capable route reported something else\n"+
					"  capable:   %s\n  incapable: %s",
					capable.Body.String(), incapable.Body.String())
			}
		})
	}
}
