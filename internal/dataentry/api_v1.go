package dataentry

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	v1 "github.com/Sourcehaven-BV/rela/internal/apiwire/v1"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/conflict"
	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	entityPkg "github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/filter"
	"github.com/Sourcehaven-BV/rela/internal/lua"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/project"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// --- API v1 Types ---

// toV1EntityType renders one entity type for the wire.
//
// Shared by BOTH schema surfaces — the `entities` map of `/api/v1/_schema`
// and the single-type `/api/v1/_schema/types/{name}` — which previously each
// built the shape inline. Two copies of a serializer is two places to add a
// field and one place to forget it: the `faces` key (TKT-WRLDAPI item 3)
// would have been exactly such a field, since a client that discovers a
// type's content states from one endpoint and not the other has no way to
// tell which answer is authoritative. Extracted so they cannot drift.
//
// A free function, like its sibling toV1CustomType: it is a pure transform
// over its arguments and touches no App state. toV1PropertyDef was converted
// alongside it for the same reason — it never used its receiver either, and
// App sits at its plimsoll method cap precisely because methods accrete there
// by habit rather than by need.
func toV1EntityType(
	meta *metamodel.Metamodel, name string, def metamodel.EntityDef,
) v1.EntityType {
	et := v1.EntityType{
		Label:       def.Label,
		Plural:      def.GetPlural(name),
		Description: def.Description,
		Primary:     def.GetPrimaryProperty(),
		IDType:      def.GetIDType(),
		Properties:  make(map[string]v1.PropertyDef, len(def.Properties)),
		Faces:       schemaFaceDefs(def),
		// Whether this type accepts comments (TKT-FIO205). Policy, not
		// permission: it tells the SPA whether to offer a comment affordance
		// at all, never that the caller may use it.
		Commentable: metamodel.NewCommentPolicy(meta).Commentable(name),
		Pacts:       schemaPacts(meta, name, def),
	}
	if prefixes := def.GetIDPrefixes(); len(prefixes) > 0 {
		et.IDPrefix = prefixes[0]
		et.IDPrefixes = prefixes
	}
	for propName, propDef := range def.Properties {
		et.Properties[propName] = toV1PropertyDef(meta, propDef)
	}
	return et
}

// schemaPacts renders the pacts of entity type name, sorted by system, or nil
// when it has none (so the `pacts` key is omitted). Instructions — the agent
// brief — stay off the wire.
//
// def short-circuits the common no-pacts type before the policy is built.
func schemaPacts(meta *metamodel.Metamodel, name string, def metamodel.EntityDef) []v1.Pact {
	if len(def.Pacts) == 0 {
		return nil
	}
	policy := metamodel.NewPactPolicy(meta)
	systems := policy.Systems(name)
	out := make([]v1.Pact, 0, len(systems))
	for _, system := range systems {
		p, _ := policy.Pact(name, system)
		out = append(out, v1.Pact{
			System:  system,
			Scope:   p.Scope(),
			Theirs:  nonNilStrings(p.Theirs()),
			Shared:  nonNilStrings(p.Shared()),
			Propose: nonNilStrings(p.Propose()),
		})
	}
	return out
}

// nonNilStrings serves an empty list as [] rather than null.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func toV1PropertyDef(meta *metamodel.Metamodel, propDef metamodel.PropertyDef) v1.PropertyDef {
	pd := v1.PropertyDef{
		Type:        propDef.Type,
		Required:    propDef.Required,
		Default:     propDef.Default,
		Description: propDef.Description,
		List:        propDef.List,
		Max:         propDef.Max,
	}
	// Labels mirror Values: a property naming a custom type inherits that
	// type's values and labels; an inline `labels` map on a custom-typed
	// property is ignored, exactly as inline `values` is.
	if ct, ok := meta.Types[propDef.Type]; ok {
		pd.Values = ct.Values
		pd.Labels = ct.Labels
	} else if len(propDef.Values) > 0 {
		pd.Values = propDef.Values
		pd.Labels = propDef.Labels
	}
	return pd
}

// toV1CustomType is the single serialization site for a metamodel custom type
// onto the wire, so the _schema types map and any other consumer stay in sync.
//
// It intentionally projects only Values/Labels/Default. The documentation-only
// fields — CustomType.Descriptions (per-value meaning), CustomType.Transitions,
// and TransitionDef.Help (TKT-0YBFT8) — are NOT serialized: their consumer is the
// offline `rela docs` generator (FEAT-G4VO53), not the SPA. Wiring any of them to
// the wire is a deliberate frontend-contract change (see internal/dataentry/
// CLAUDE.md), not a "helpful" one-liner here. TestToV1CustomType_OmitsDocFields
// pins this boundary.
func toV1CustomType(ct metamodel.CustomType) v1.CustomType {
	return v1.CustomType{
		Values:  ct.Values,
		Labels:  ct.Labels,
		Default: ct.Default,
	}
}

// --- API v1 Router ---

// registerAPIV1Routes registers all /api/v1/ routes.
// Note: /api/v1/_events is registered separately in NewRouter as it needs to be
// outside the reload-lock middleware (SSE long-lived connection).
//
// When adding a route, add a probe to the route table in
// router_walk_test.go so registration stays covered.
func (a *App) registerAPIV1Routes(mux *http.ServeMux) {
	// System endpoints (underscore prefix)
	mux.HandleFunc("/api/v1/_schema", a.handleV1Schema)
	mux.HandleFunc("/api/v1/_schema/", a.handleV1SchemaRoutes)
	mux.HandleFunc("/api/v1/_config", a.handleV1Config)
	mux.HandleFunc("/api/v1/_feeds/", a.handleV1Feed)
	if routes := newCalDAVRoutes(a); routes != nil {
		routes.register(mux)
	}
	mux.HandleFunc("/api/v1/_search", a.handleV1Search)
	mux.HandleFunc("/api/v1/_position", a.handleV1EntityPosition)
	mux.HandleFunc("/api/v1/_analyze", a.handleV1Analyze)
	mux.HandleFunc("/api/v1/_next_action", a.handleV1NextAction)
	mux.HandleFunc("/api/v1/_git/status", a.handleGitStatus)
	mux.HandleFunc("/api/v1/_git/sync", a.handleGitSync)
	mux.HandleFunc("/api/v1/_settings", a.appearance.handleAPISettingsCRUD)
	mux.HandleFunc("/api/v1/_palette", a.appearance.handleAPIPaletteCRUD)
	mux.HandleFunc("/api/v1/_theme/logo", a.appearance.handleAPIThemeLogo)
	mux.HandleFunc("/api/v1/_theme/export", a.appearance.handleAPIThemeExport)
	mux.HandleFunc("/api/v1/_theme/import", a.appearance.handleAPIThemeImport)
	mux.HandleFunc("/api/v1/_sidepanel/", a.views.handleV1SidePanel)
	mux.HandleFunc("/api/v1/_sidebar", a.views.handleV1Sidebar)
	mux.HandleFunc("/api/v1/_dashboard", a.views.handleV1Dashboard)
	mux.HandleFunc("/api/v1/_conflicts", a.handleV1Conflicts)
	mux.HandleFunc("/api/v1/_conflicts/", a.handleV1ConflictRoutes)
	mux.HandleFunc("/api/v1/_documents/", a.handleV1Documents)
	mux.HandleFunc("/api/v1/_history/", func(w http.ResponseWriter, r *http.Request) { handleV1History(a, w, r) })
	mux.HandleFunc("/api/v1/_relation_history/",
		func(w http.ResponseWriter, r *http.Request) { handleV1RelationHistory(a, w, r) })
	mux.HandleFunc("/api/v1/_openapi.json", a.handleV1OpenAPI)
	mux.HandleFunc("/api/v1/_commands", a.handleV1Commands)
	mux.HandleFunc("/api/v1/_transforms", a.export.handleV1Transforms)
	mux.HandleFunc("/api/v1/_comments/", a.comments.handleV1Comments)
	mux.HandleFunc(twinsPathPrefix, a.twins.handleV1Twins)
	mux.HandleFunc("/api/v1/_templates/", a.handleV1Templates)
	mux.HandleFunc("/api/v1/_views/", a.views.handleV1Views)
	a.registerCopyRoutes(mux)
	mux.HandleFunc("/api/v1/_gantts/", a.gantt.handleV1Gantt)
	mux.HandleFunc("/api/v1/_action/", a.write.handleV1Action)
	mux.HandleFunc("/api/v1/_apps/", a.handleV1App)

	// Dynamic entity routes are handled by a catch-all
	mux.HandleFunc("/api/v1/", a.handleV1DynamicRoutes)
}

// Path-segment counts for the dynamic route dispatcher below. A
// four-segment path selects a sub-resource collection
// (relations/_actions/_attachments); a five-segment path targets a
// single member within one of those.
const (
	segmentsSubResource   = 4 // /{plural}/{id}/{sub}/{key}
	segmentsSubResourceID = 5 // /{plural}/{id}/{sub}/{key}/{member}
)

// handleV1DynamicRoutes routes requests to the appropriate entity handler
// based on URL. Read operations work against the snapshot returned by
// a.State() with no locking.
func (a *App) handleV1DynamicRoutes(w http.ResponseWriter, r *http.Request) {
	// Skip system routes (already handled)
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	if strings.HasPrefix(path, "_") {
		http.NotFound(w, r)
		return
	}

	// Parse path: {plural}[/{id}[/relations[/{relType}[/{targetId}]]]]
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeV1Error(w, r, http.StatusNotFound, "not_found", "Resource not found", "")
		return
	}

	plural := parts[0]

	// Find entity type by plural
	var typeName string
	for name, def := range a.State().Meta.Entities {
		if def.GetPlural(name) == plural {
			typeName = name
			break
		}
	}

	if typeName == "" {
		writeV1Error(w, r, http.StatusNotFound, "unknown_type", "Unknown entity type", "")
		return
	}

	switch len(parts) {
	case 1:
		// /{plural} - collection
		a.handleV1EntityCollection(w, r, typeName, plural)
	case 2:
		// /{plural}/_export (collection export) or /{plural}/{id} (single entity).
		// _export is a reserved segment; no entity id may collide (ids never
		// start with the reserved underscore).
		if parts[1] == "_export" {
			a.export.handleV1ExportList(w, r, typeName)
		} else {
			a.handleV1SingleEntity(w, r, typeName, plural, parts[1])
		}
	case 3:
		// /{plural}/{id}/relations or /{plural}/{id}/_export
		switch parts[2] {
		case "relations":
			a.handleV1EntityRelations(w, r, typeName, parts[1])
		case "_export":
			a.export.handleV1ExportEntity(w, r, typeName, parts[1])
		default:
			writeV1Error(w, r, http.StatusNotFound, "not_found", "Resource not found", "")
		}
	case segmentsSubResource:
		// /{plural}/{id}/relations/{relType}, /{plural}/{id}/_actions/{action},
		// or /{plural}/{id}/_attachments/{property}
		switch parts[2] {
		case "relations":
			a.handleV1EntityRelationType(w, r, typeName, parts[1], parts[3])
		case "_actions":
			a.handleV1EntityAction(w, r, typeName, parts[1], parts[3])
		case "_attachments":
			// Attachments are per ENTITY (the store keys them by bare id), so
			// a faced address names the same files as the bare one.
			id, ok := bareEntityID(parts[1])
			if !ok {
				writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
				return
			}
			a.attachments.handleV1AttachmentRoute(w, r, typeName, plural, id, parts[3])
		default:
			writeV1Error(w, r, http.StatusNotFound, "not_found", "Resource not found", "")
		}
	case segmentsSubResourceID:
		// /{plural}/{id}/relations/{relType}/{targetId} or
		// /{plural}/{id}/_attachments/{property}/{fileName}
		switch parts[2] {
		case "relations":
			a.handleV1RelationTarget(w, r, typeName, parts[1], parts[3], parts[4])
		case "_attachments":
			id, ok := bareEntityID(parts[1])
			if !ok {
				writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
				return
			}
			a.attachments.handleV1AttachmentFileRoute(w, r, typeName, id, parts[3], parts[4])
		default:
			writeV1Error(w, r, http.StatusNotFound, "not_found", "Resource not found", "")
		}
	default:
		writeV1Error(w, r, http.StatusNotFound, "not_found", "Resource not found", "")
	}
}

// --- Collection Handlers ---

func (a *App) handleV1EntityCollection(w http.ResponseWriter, r *http.Request, typeName, plural string) {
	switch r.Method {
	case http.MethodGet:
		a.handleV1ListEntities(w, r, typeName, plural)
	case http.MethodPost:
		// TKT-3I5U: ?dry_run=true evaluates affordances + soft validation
		// against the candidate WITHOUT persisting, so the create form can
		// gate fields / options / hidden as the user types. Read-shaped:
		// dispatched before handleV1CreateEntity, and never persists.
		if r.URL.Query().Get("dry_run") == "true" {
			a.write.handleV1DryRunCreate(w, r, typeName, plural)
			return
		}
		a.write.handleV1CreateEntity(w, r, typeName, plural)
	case http.MethodOptions:
		w.Header().Set("Allow", "GET, POST, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
	default:
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
	}
}

// errACLListQuery wraps a store.GraphQuery failure during ACL list
// filtering so call sites can route it through writeGateError (500
// acl_query_failed / 504 / silent-on-cancel) instead of mislabeling
// it as a free-text search failure.
var errACLListQuery = errors.New("acl list query failed")

// errBadFilter marks a filter[...] query param the pipeline refuses to
// evaluate: malformed key shape or an operator outside the supported set.
// Mapped to HTTP 400 by writeListPipelineError. A rejected request beats
// the old behavior of dropping the clause with only a server-side log —
// that silently returned the UNFILTERED superset, which let a config typo
// (`=~`, BUG: active_tickets) masquerade as an empty/wrong result set for
// months. Malformed wire format is a hard 400 per DEC-HWZHA.
var errBadFilter = errors.New("invalid filter parameter")

// errListLoad wraps a store iterator failure while loading the
// unfiltered (AllowAll) list. Surfaced as 500 list_load_failed at the
// call sites: before TKT-VMD8 a mid-stream error silently truncated
// the result, which under ACL would make the AllowAll and Query paths
// observably asymmetric (truncated-200 vs 500) for the same backend
// fault — and a truncated list with authoritative-looking pagination
// is worse than an error either way.
var errListLoad = errors.New("list load failed")

// scopedSortedEntities runs the shared list pipeline — ACL read scope,
// load, free-text intersection (?q=), structured filters (filter[...]),
// then the configured sort — and returns the fully ordered result set
// *before* pagination. Both handleV1ListEntities and
// handleV1EntityPosition call this, so a list and its scope navigator
// are guaranteed to observe identical ordering (under ACL both operate
// on the same visible subset — hidden entities don't occupy ordinals).
//
// ACL ordering contract (TKT-VMD8, RR-X56H + RR-3IO2): the read-scope
// verdict resolves FIRST. DenyAll returns before the search backend,
// filters, or sort run — a denied principal must not be able to probe
// backend latency through ?q=. A composed Query loads the visible
// subset via store.GraphQuery; search/filter/sort then operate on that
// filtered slice only (search-after-ACL). AllowAll keeps the pre-ACL
// load path byte-identical.
//
// Errors: free-text search failures surface verbatim (HTTP 500
// search_failed at the call site); ACL query failures are wrapped in
// errACLListQuery so call sites map them via writeGateError. Everything
// else degrades to an empty/whole set as the list endpoint always did.
// listPage returns one page of the list plus the scoped total. A request the
// store can page by itself (listpushdown.go) costs one count and one bounded
// read; everything else takes the whole-type Go pipeline and slices it.
func (a *App) listPage(
	ctx context.Context, typeName string, query map[string][]string, page, perPage int,
) (rows []*entityPkg.Entity, total int, err error) {
	ctx, n, err := resolveListNarrowing(ctx, a, typeName, query)
	if err != nil {
		return nil, 0, err
	}
	if plan, empty, ok := n.pushdownPlan(ctx, a, typeName, query, page, perPage); ok {
		if empty {
			return nil, 0, nil
		}
		return plan.run(ctx, a.Services().Store)
	}
	cond, scope := n.cond, n.scope
	all, err := scopedSortedEntitiesScoped(ctx, a, typeName, query, scope)
	if err != nil {
		return nil, 0, err
	}
	// AFTER the ACL scope and every filter, BEFORE paging and the count: the
	// condition narrows the population the page and total describe, so
	// applying it later would page one set and count another.
	ctx = primeVerdicts(ctx, a.fieldResolver, all)
	if all, err = applyViewCondition(ctx, all, cond, a.redactedForSuggestion, a.Services().Store); err != nil {
		return nil, 0, err
	}
	total = len(all)
	start := min((page-1)*perPage, total)
	end := min(start+perPage, total)
	return all[start:end], total, nil
}

func (a *App) scopedSortedEntities(
	ctx context.Context,
	typeName string,
	query map[string][]string,
) ([]*entityPkg.Entity, error) {
	// Resolves the scope from the request's ?query_scope= (absent = the
	// type's default), so every caller of this function is scoped too:
	// export mirrors the list it names, and _position navigates the set the
	// list actually showed.
	scopeName, err := queryScopeParam(query)
	if err != nil {
		return nil, err
	}
	scope, err := viewQueryScope(a.queryScopes, a.Cfg(), a.Meta(), typeName, scopeName)
	if err != nil {
		return nil, err
	}
	return scopedSortedEntitiesScoped(ctx, a, typeName, query, scope)
}

// scopedSortedEntitiesScoped is App.scopedSortedEntities with the query
// scope already resolved, for callers that resolved it to decide something
// else first (listPage uses it to gate the pushdown).
//
// A package function taking the App rather than a method, to keep App under
// its plimsoll load line — the discipline the directive at [App] records:
// a feature pays for its setter, not for its internals.
func scopedSortedEntitiesScoped(
	ctx context.Context,
	a *App,
	typeName string,
	query map[string][]string,
	scope resolvedQueryScope,
) ([]*entityPkg.Entity, error) {
	// Stamp the scope's request-scoped state ONCE, before any row is
	// evaluated — a scope reading current_user resolves it here, not per row.
	ctx, err := scope.bind(ctx)
	if err != nil {
		return nil, err
	}
	// The verdict switch, the world scope, the face allowlist and the query
	// scope all live in scopedHeaders (scopedread.go) — see its doc for why
	// they must not be re-implemented per handler.
	rqr := readGateFromContext(ctx).ReadQuery(ctx, typeName)
	entities, withheld, err := scopedEntities(ctx, a.Services(), rqr, scopeRequest{
		Type:        typeName,
		Faces:       rqr.Faces,
		Scope:       scope.Scope,
		ScopeProps:  scope.Props,
		ScopeFilter: scope.Filter,
	})
	if err != nil {
		return nil, err
	}
	if withheld {
		// Return BEFORE the free-text search: a principal who may read
		// nothing must not be able to probe the search backend's latency or
		// induce load through ?q= (RR-X56H).
		return []*entityPkg.Entity{}, nil
	}

	// Free-text search: intersect with hits from the searcher when ?q=... is
	// present. Bleve scores are discarded — the list's configured sort wins
	// over relevance ranking, same approach SearchView uses for filtering.
	// Backend errors surface as HTTP 500 rather than rendering an empty list
	// and pretending the search succeeded.
	searchResult, err := a.queries.freeTextIDsForType(ctx, queryGet(query, "q"), typeName)
	if err != nil {
		return nil, err
	}
	if searchResult.HasFilter {
		filtered := entities[:0]
		for _, e := range entities {
			if _, hit := searchResult.IDs[e.ID]; hit {
				filtered = append(filtered, e)
			}
		}
		entities = filtered
	}

	// Classify each filter[<key>] param as property vs relation ONCE, up
	// front, so both passes agree on routing. The config's FilterControls are
	// authoritative (RR-0HWAS0 / RR-B0JPPL): a relation filter applies only
	// when a list or kanban control of this type configures it. Name-based
	// GetRelationDef is only a fallback and only ever routes AWAY from
	// properties, never toward relations without a control.
	isRelationKey := relationFilterClassifier(a.Meta(), a.Cfg(), typeName)
	entities, err = applyV1Filters(entities, query, typeName, isRelationKey)
	if err != nil {
		return nil, err
	}
	entities, err = a.applyRelationFilters(ctx, entities, query, typeName, isRelationKey)
	if err != nil {
		return nil, err
	}
	entities = applyV1Sorting(entities, query, a.Meta())
	return entities, nil
}

// relationFilterClassifier returns a predicate that decides, for a bare filter
// param name (the property/relation segment, no operator), whether it should be
// handled by the direction-aware relation pass rather than applyV1Filters.
//
// Relation filtering is restricted to configured filter_controls (RR-B0JPPL):
// a `filter[<rel>]` param routes to the relation pass ONLY when a relation
// FilterControl on a list or kanban of this entity type configures it. An arbitrary
// metamodel relation with no control is NOT filterable and falls through to
// applyV1Filters (where, absent a matching property, it fails closed rather
// than silently widening the set).
//
// The config discriminator also resolves the property/relation NAME COLLISION
// (RR-0HWAS0): properties (per-type) and relations (global) are disjoint
// namespaces, so a property can share a name with a relation. An explicit
// property FilterControl for the name forces the property pass; a name that is
// only ever a relation control routes to the relation pass. When a name is both
// a property of this type and a relation control with no disambiguating
// property control, the property wins (safe, backward-compatible) and
// CollectConfigWarnings flags the ambiguity at load.
//
// A free function (not an App method) taking the already-loaded meta/cfg
// snapshot: it needs no other App state, and keeping it off the receiver keeps
// App under its plimsoll method cap.
func relationFilterClassifier(
	meta *metamodel.Metamodel,
	cfg *dataentryconfig.Config,
	typeName string,
) func(string) bool {
	return func(key string) bool {
		// Explicit property control → property pass.
		if cfg.HasPropertyFilterControl(typeName, key) {
			return false
		}
		// A relation control is required for relation filtering.
		if _, ok := cfg.RelationFilterDirection(typeName, key); !ok {
			return false
		}
		// Relation control exists, but the collision rule: if the name is
		// also a property of this type (and no property control disambiguated
		// above), the property wins.
		if entDef, ok := meta.GetEntityDef(typeName); ok {
			if _, isProp := entDef.Properties[key]; isProp {
				return false
			}
		}
		return true
	}
}

// applyRelationFilters keeps only the entities that match every relation
// filter present in the query. isRelationKey (the config-backed classifier
// built in scopedSortedEntities) decides which `filter[<key>]` params are
// relation filters; property filters are handled by applyV1Filters. Matching is
// direction-aware: the FilterControl config on a list or kanban of this
// entity type supplies the direction (default outgoing; precedence in
// dataentryconfig.Config.RelationFilterDirection), and an entity matches iff
// it has an edge of that (relation, direction) to a READABLE neighbor whose
// display title equals the requested value.
//
// Operators: only `eq` (the bare `filter[<rel>]` form) and `ne`
// (`filter[<rel>][ne]`) are supported. Any other operator segment is rejected
// with errBadFilter → HTTP 400, matching applyV1Filters. This supersedes the
// RR-6RF60V drop-to-zero-rows behavior: both were fail-closed, but zero rows
// reads as "no data" while a 400 names the broken operator to the caller.
// Never fail open.
//
// ACL (RR-HK1XNO): neighbor titles are resolved through the read gate, so a
// filter value matching a neighbor the caller cannot read does NOT include the
// edged rows (no title-match inference channel). Neighbors are gated in one
// batch per relation param via matchRelationFilter → visibleNeighborTitles.
// NOTE: when the sibling helper App.visibleRelationIDs (TKT-ODHV2D) merges,
// this should converge on it; today it uses the same readGate.PermitsReadMany
// batching pattern inline.
//
// Cost (RR-38K7K9): this runs over the entire type's visible set BEFORE
// pagination, doing ListRelations + a batched GetEntity per row. It is
// therefore O(N·edges) per request, uncached, where N is the unpaginated
// visible row count — acceptable for the current dataset sizes but NOT a
// store-side pushdown. A future optimization is to push the relation-title
// predicate into the store query. Each row short-circuits: matchRelationFilter
// stops scanning a row's edges as soon as one matches.
//
// This runs in scopedSortedEntities so BOTH the list handler and scope nav
// (_position, via resolveScope) observe identical relation filtering — they
// share this pipeline. Property filters are untouched (TKT-5U7QBR).
func (a *App) applyRelationFilters(
	ctx context.Context, entities []*entityPkg.Entity, query map[string][]string, typeName string,
	isRelationKey func(string) bool,
) ([]*entityPkg.Entity, error) {
	cfg := a.Cfg()
	for key, values := range query {
		if !strings.HasPrefix(key, "filter[") || len(values) == 0 {
			continue
		}
		relation, operator, ok := parseRelationFilterKey(key)
		if !ok || !isRelationKey(relation) {
			continue // malformed, or a property filter handled elsewhere
		}
		want := values[len(values)-1]
		if want == "" {
			continue // empty control value = no constraint
		}

		// Reject any operator we don't support for relations. Never fall
		// through and leave the set unfiltered (that would be the fail-open
		// bug RR-6RF60V); see the doc comment on the 400-over-zero-rows choice.
		switch operator {
		case "eq", "ne":
			// supported below
		default:
			return nil, fmt.Errorf("%w: unknown operator %q in relation filter key %q (supported: eq, ne)",
				errBadFilter, operator, key)
		}

		direction, _ := cfg.RelationFilterDirection(typeName, relation)
		matched, merr := matchRelationFilterMany(ctx, a.Services(), entities, relation, direction, want)
		if merr != nil {
			return nil, fmt.Errorf("%w: relation filter %q: %w", errListLoad, relation, merr)
		}
		filtered := entities[:0]
		for _, e := range entities {
			if (operator == "eq" && matched[e.ID]) || (operator == "ne" && !matched[e.ID]) {
				filtered = append(filtered, e)
			}
		}
		entities = filtered
	}
	return entities, nil
}

// matchRelationFilterMany evaluates a relation filter for every row at once
// (TKT-1U8XYN): one relation query for all rows, one content-free header
// read for every distinct neighbor, one gate probe per neighbor type. It
// replaced a per-row helper that ran one relation query plus one entity load
// per neighbor for each row. The verdict per row is unchanged — the row
// matches when at least one READABLE neighbor over the relation carries the
// wanted display title (RR-HK1XNO) — but the cost no longer grows with the
// type's row count times its fan-out. A free function taking its read seam,
// not an App method — App sits at its load line (see worldneighbors.go).
//
// A store failure is returned, never folded into an empty verdict map: an
// empty map reads as "no row matches", which for a `ne` filter admits EVERY
// row — a backend fault would silently widen a filter whose job is to narrow.
func matchRelationFilterMany(
	ctx context.Context, svc Services, rows []*entityPkg.Entity,
	relation string, direction dataentryconfig.Direction, want string,
) (map[string]bool, error) {
	matched := make(map[string]bool, len(rows))
	if len(rows) == 0 {
		return matched, nil
	}
	ids := make([]string, 0, len(rows))
	for _, e := range rows {
		ids = append(ids, e.ID)
	}
	q := store.RelationQuery{EntityIDs: ids, Type: relation, Direction: relationDirection(direction)}
	neighborsOf := make(map[string][]string, len(rows)) // row id → neighbor ids
	var neighborIDs []string
	seen := make(map[string]struct{})
	for r, err := range svc.Store.ListRelations(ctx, q) {
		if err != nil {
			return nil, err
		}
		rowID, targetID := r.From, r.To
		if direction.IsIncoming() {
			rowID, targetID = r.To, r.From
		}
		neighborsOf[rowID] = append(neighborsOf[rowID], targetID)
		if _, dup := seen[targetID]; !dup {
			seen[targetID] = struct{}{}
			neighborIDs = append(neighborIDs, targetID)
		}
	}
	if len(neighborIDs) == 0 {
		return matched, nil
	}

	// Which neighbors carry the wanted title, by type — then gate per type.
	candidatesByType := map[string][]string{}
	for h, err := range store.ListEntityHeaders(ctx, svc.Store, store.EntityQuery{IDs: neighborIDs}) {
		if err != nil {
			return nil, err
		}
		if svc.Meta.DisplayTitle(h.ID, h.Type, h.Properties) == want {
			candidatesByType[h.Type] = append(candidatesByType[h.Type], h.ID)
		}
	}
	readable := make(map[string]bool)
	gate := readGateFromContext(ctx)
	for typ, cids := range candidatesByType {
		perm, err := gate.PermitsReadMany(ctx, typ, cids)
		if err != nil {
			continue
		}
		for _, id := range cids {
			if perm[id] {
				readable[id] = true
			}
		}
	}
	for rowID, targets := range neighborsOf {
		for _, t := range targets {
			if readable[t] {
				matched[rowID] = true
				break
			}
		}
	}
	return matched, nil
}

// parseRelationFilterKey parses a `filter[<rel>]` or `filter[<rel>][<op>]` key
// using the SAME `][`-split logic as applyV1Filters, so an operator segment is
// recognized rather than being swallowed into the relation name (RR-6RF60V,
// the old TrimPrefix/TrimSuffix bug). Returns (relation, operator, ok) with
// operator defaulting to "eq". ok is false for a malformed key (empty relation,
// too many segments).
func parseRelationFilterKey(key string) (relation, operator string, ok bool) {
	filterKey := strings.TrimPrefix(key, "filter[")
	filterKey = strings.TrimSuffix(filterKey, "]")
	filterKey = strings.TrimSuffix(filterKey, "][") // was "...[]"
	parts := strings.Split(filterKey, "][")
	if len(parts) > 2 || parts[0] == "" {
		return "", "", false
	}
	operator = "eq"
	if len(parts) == 2 {
		if parts[1] == "" {
			return "", "", false
		}
		operator = parts[1]
	}
	return parts[0], operator, true
}

// queryGet returns the first value for key from a raw query map, or "".
// url.Values.Get over a plain map[string][]string without allocating.
func queryGet(query map[string][]string, key string) string {
	if vals, ok := query[key]; ok && len(vals) > 0 {
		return vals[0]
	}
	return ""
}

func (a *App) handleV1ListEntities(w http.ResponseWriter, r *http.Request, typeName, plural string) {
	query := r.URL.Query()
	page, perPage := parseV1Pagination(query)

	entities, total, err := a.listPage(r.Context(), typeName, query, page, perPage)
	if err != nil {
		writeListPipelineError(w, r, err)
		return
	}
	end := (page-1)*perPage + len(entities)

	// Bodies are opt-in for a collection (rowcontent.go): one read per
	// distinct face on the page, never for the rows that were paged out.
	if wantContent(query) {
		if err := loadRowContent(r.Context(), a.Services().Store, entities); err != nil {
			writeListPipelineError(w, r, fmt.Errorf("%w: %w", errListLoad, err))
			return
		}
	}

	// Check if includes are requested (for relation columns)
	includes := query.Get("include")
	wantIncludes := includes != ""

	// Load each row's edges once. List rows carry BOTH outgoing edges (keyed
	// by relation type) and incoming edges (keyed by the relation's inverse
	// name) so that `direction: incoming` relation columns have a wire key to
	// resolve against. The SPA computes the same inverse key and reads the
	// source entities from the ?include=* peers. See MECHANISM.md.
	//
	// Each row's links come from THAT ROW'S OWN FACE, in every world
	// including the default one — see [servedFacePageEdges].
	outgoingByRow, incomingByRow, visibleNeighbors, werr := servedFacePageEdges(
		r.Context(), a.reader, a.worldNeighbors, a.visibleReader, entities)
	if werr != nil {
		// Infrastructure failure, not an empty page. See the same arm on the
		// single-entity GET.
		writeGateError(w, r, werr)
		return
	}

	// Build response - always include relations for relation column support
	data := make([]v1.Entity, 0, len(entities))
	included := make(map[string]v1.Entity)
	pageCtx := primeVerdicts(r.Context(), a.fieldResolver, entities)
	for i, e := range entities {
		v1Entity := a.serializer.forWireRelated(
			pageCtx, e,
			outgoingByRow[i],
			incomingByRow[i],
			visibleNeighbors,
			a.Meta(), plural,
		)
		// Face provenance per ROW (BUG-3). Under a chain a row that fell back
		// to a later candidate is byte-identical to a first-choice hit — same
		// id, same title — and only `_world` separates them. Without it a
		// picker or list cannot say which face a row is, which is the whole
		// distinction content states exist to make visible.
		//
		// nil under the default world, so an ordinary list response is
		// unchanged: every row resolves to its default face there, and a block
		// on every row would be noise that also implies a world was applied.
		// Same choice loadViewEntities' provenanceFor makes, for the same
		// reason (see [viewWorld.provenanceFor]).
		if !worldScopeFrom(r.Context()).IsDefaultWorld() {
			v1Entity.World = worldProvenance(r.Context(), e)
		}
		data = append(data, v1Entity)

		// Resolve includes if requested
		if wantIncludes {
			maps.Copy(included, a.resolveV1Includes(r.Context(), e, includes))
		}
	}

	resp := v1.ListResponse{
		Data: data,
		Meta: v1.ListMeta{
			Total:   total,
			Page:    page,
			PerPage: perPage,
			HasMore: end < total,
		},
		Actions: a.affordances.computeCollectionActions(r.Context(), typeName),
	}

	// Add Link header for pagination (RFC 5988)
	addPaginationLinks(w, r, page, perPage, total, plural)

	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	w.Header().Set("X-Page", strconv.Itoa(page))
	w.Header().Set("X-Per-Page", strconv.Itoa(perPage))

	// If includes were requested, add them to response
	if len(included) > 0 {
		// For list responses with includes, we need a different response structure
		// Encode as JSON with additional "included" field
		type listWithIncludes struct {
			Data     []v1.Entity          `json:"data"`
			Meta     v1.ListMeta          `json:"meta"`
			Included map[string]v1.Entity `json:"included,omitempty"`
			Actions  map[string]bool      `json:"_actions,omitempty"`
		}
		writeV1JSON(w, http.StatusOK, listWithIncludes{
			Data:     resp.Data,
			Meta:     resp.Meta,
			Included: included,
			Actions:  resp.Actions,
		})
		return
	}

	writeV1JSON(w, http.StatusOK, resp)
}

// --- Single Entity Handlers ---

func (a *App) handleV1SingleEntity(w http.ResponseWriter, r *http.Request, typeName, plural, entityID string) {
	switch r.Method {
	case http.MethodGet:
		a.handleV1GetEntity(w, r, typeName, plural, entityID)
	case http.MethodPatch:
		a.write.handleV1UpdateEntity(w, r, typeName, plural, entityID)
	case http.MethodDelete:
		a.write.handleV1DeleteEntity(w, r, typeName, plural, entityID)
	case http.MethodOptions:
		w.Header().Set("Allow", "GET, PATCH, DELETE, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
	default:
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
	}
}

// gateReadOrNotFound runs the per-entity ACL read gate (PermitsRead)
// and writes the right response on deny: 404 with the same body shape
// as not-found (indistinguishable-from-not-found invariant) when the
// principal cannot read, or the relevant 5xx via writeGateError on
// store/timeout failure. Returns true if the handler should continue
// (gate allowed), false if a response was already written.
//
// Centralizing this means every read chokepoint (GET, PATCH, DELETE,
// clone, relations CRUD) handles the deny branch the same way — a
// future divergence in error code, body shape, or ETag suppression
// would otherwise be a per-handler oracle leak (RR-NGMI).
// entityNotFoundTitle is the title for EVERY read-path 404 — the ACL
// deny path (gateReadOrNotFound), a genuinely missing entity, and a
// missing attachment all share it so a denied read is byte-identical to
// a nonexistent one (the RR-NGMI indistinguishability invariant). Any
// handler that 404s on the read path MUST use this const, not a fresh
// literal, or the bodies drift and existence leaks.
const entityNotFoundTitle = "Entity not found"

func (a *App) gateReadOrNotFound(w http.ResponseWriter, r *http.Request, typeName, entityID string) bool {
	ok, err := readGateFromContext(r.Context()).PermitsRead(r.Context(), typeName, entityID)
	if err != nil {
		writeGateError(w, r, err)
		return false
	}
	if !ok {
		writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
		return false
	}
	return true
}

func (a *App) handleV1GetEntity(w http.ResponseWriter, r *http.Request, typeName, plural, entityID string) {
	ctx := r.Context()

	// The path segment is an ADDRESS — `ID` or `ID@face` — parsed here and
	// nowhere downstream (TKT-SLFURL). An address the grammar rejects cannot
	// name a row, so it gets the same not-found a missing row does.
	ref, ok := parseEntityRef(entityID)
	if !ok {
		writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
		return
	}

	// ACL gate (TKT-VQGN). visibleReader.getVisible applies PermitsRead
	// BEFORE the store read so a hidden id and a nonexistent id spend the
	// same MatchingIDs roundtrip — otherwise the timing difference
	// (in-memory lookup ~1µs vs. DB roundtrip ~1ms) is an id-enumeration
	// side channel that defeats the indistinguishable-404-body invariant
	// (RR-NGMI). A gate error surfaces via writeGateError; a deny is
	// returned as (nil,false,nil), indistinguishable from a real miss.
	entity, found, err := a.visibleReader.getVisibleRef(ctx, typeName, ref)
	if err != nil {
		writeGateError(w, r, err)
		return
	}
	if !found || entity.Type != typeName {
		writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
		return
	}
	// Serializing evaluates the grant traversals several times (strip,
	// `_fields`, `_relations`); priming answers them once.
	ctx = primeVerdicts(ctx, a.fieldResolver, []*entityPkg.Entity{entity})

	query := r.URL.Query()

	// Single per-entity serialization: strips hidden + attaches
	// `_fields` / `_relations` per docs/data-entry/api-reference.md.
	//
	// The edges OF THE FACE BEING SERVED, in every world including the
	// default one — see [servedFaceEdges]. Each link resolves through the
	// SAME world as the entry, per neighbor, independently (RULING 12): a
	// published view links to published faces, and a link whose head has no
	// face in this world is ABSENT rather than pointing at a 404.
	//
	// There is deliberately no default-world arm reaching the bare-id
	// reader. That arm returned the UNION of every face's content-scoped
	// edges, so a draft carried the published face's links too — the
	// mixed-face response that reads as correct because the entity looks
	// right and only its links are wrong.
	outgoing, visibleNeighbors, werr := servedFaceEdges(
		ctx, a.reader, a.worldNeighbors, a.visibleReader, entity)
	if werr != nil {
		// A neighbor-resolution fault is an infrastructure failure, not an
		// empty link set. Rendering it as "this world links to nothing"
		// would hide an outage behind a page that looks like a
		// correctly-sparse published face (RR-4TFZNL).
		writeGateError(w, r, werr)
		return
	}
	result := a.serializer.forWireScoped(ctx, entity, outgoing, visibleNeighbors, a.Meta(), plural)

	// Face provenance (TKT-WRLDAPI item 2). Attached HERE rather than inside
	// forWire, even though forWire is the shared per-entity serializer,
	// because this is the only path whose Face is the STORE's answer to a
	// world query. Its other callers would produce a label the code cannot
	// back: the history handler serializes a snapshot built by
	// entityPkg.New(), whose Face is always zero, so a historical version
	// of a PUBLISHED face would report the default coordinate. An affordance
	// map that lies is a trap for every future consumer (Ruling 11); so is a
	// provenance block.
	//
	// An EXPLICIT address was not resolved by the world at all, so it is
	// labeled by [addressedProvenance] rather than by the chain position it
	// happens to hold — see that function for why.
	if ref.Explicit {
		result.World = addressedProvenance(ctx, entity)
	} else {
		result.World = worldProvenance(ctx, entity)
	}

	// Handle includes for related entities
	if includes := query.Get("include"); includes != "" {
		result.Included = a.resolveV1Includes(ctx, entity, includes)
	}

	// Resolve the entity-ID code spans in the body so an edit form can render
	// them as titled links, the way the read surface already does
	// (`rewriteEntityRefToken`). Same call the view handler makes, so the two
	// surfaces cannot disagree about a title.
	//
	// It runs on the SERVED content, after the read gate and the redactor, so
	// a reference inside a body the caller could not read never gets scanned.
	// collectMentions then applies the gate a second time to each REFERENCED
	// entity, which is what keeps a title the caller may not see out of the
	// response (BUG-R9EHKV).
	//
	// Not folded into the ETag: mentions turn on OTHER entities' titles and on
	// the caller's own grants, neither of which this validator ever covered.
	// The response is `no-store` anyway, so a stale shared copy is not
	// reachable.
	result.Mentions = collectMentions(ctx, a.store, a.viewReader, a.Meta(), entity.Content)

	// ETag for caching (visible-only path; deny-path above emits no ETag).
	//
	// The edges computed for the BODY above are reused rather than re-read.
	// Under a world, re-deriving them costs a second head-resolution query,
	// two more relation queries and a second ACL pass per request — the
	// RR-FRK1 duplication this PR is careful to avoid one function over — and
	// it also opens a window in which the validator could describe a
	// different edge set than the body it is validating.
	etag := entityETagWithEdges(entity, outgoing)
	w.Header().Set("ETag", etag)

	// Check If-None-Match
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	writeV1JSON(w, http.StatusOK, result)
}

// writeListPipelineError maps a scopedSortedEntities / resolveScope
// error to the right HTTP shape: ACL query failures route through
// writeGateError, store-load failures surface as list_load_failed,
// and anything else is the free-text search failure the pipeline
// always surfaced. Shared by the list and _position handlers so the
// two consumers of the pipeline can't drift.
//
// All branches log the raw error server-side and keep it out of the
// response body (RR-372L / IB-review PR939 #1): a store or search
// backend error string can name tables, columns, or index paths.
func writeListPipelineError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errBadFilter):
		// Server-side log stays alongside the 400 (RR-6RF60V's logging
		// intent, CONTROL-8-15): the response informs the caller, the log
		// informs operators — a broken config or a caller probing filter
		// params is an anomalous event worth a trace even when no one
		// watches the client.
		slog.Warn("dataentry: rejected invalid filter parameter",
			"err", err, "path", r.URL.Path, "method", r.Method)
		// Client-caused: echo the detail — it only restates the caller's own
		// filter key/operator, never store internals.
		writeV1Error(w, r, http.StatusBadRequest, "invalid_filter",
			"Invalid filter parameter", err.Error())
	case errors.Is(err, errBadQueryScope):
		// Client-caused, like errBadFilter above: the caller named a scope
		// that does not resolve, or repeated the parameter. Echo the detail
		// — it names only the caller's own scope name and the entity type,
		// both of which are operator-authored config rather than secrets.
		slog.Warn("dataentry: rejected invalid query_scope",
			"err", err, "path", r.URL.Path, "method", r.Method)
		writeV1Error(w, r, http.StatusBadRequest, "invalid_query_scope",
			"Invalid query_scope parameter", err.Error())
	case errors.Is(err, errACLListQuery):
		writeGateError(w, r, err)
	case errors.Is(err, acl.ErrTraversalUnsupported):
		// The scope's related() cannot be evaluated under this principal's
		// grants (a named-face or inherited read, a field the client ceiling
		// hides, a chain the gate cannot place). The cause is the policy and
		// the schema, never a row, so the detail names config only, which is
		// not confidential (see CLAUDE.md), and helps the operator fix it.
		slog.Warn("dataentry: query scope traversal unsupported for principal",
			"err", err, "path", r.URL.Path, "method", r.Method)
		writeV1Error(w, r, http.StatusUnprocessableEntity, "query_scope_unsupported",
			"Query scope cannot be evaluated for this principal", err.Error())
	case errors.Is(err, errListLoad):
		slog.Warn("dataentry: list load failed",
			"err", err, "path", r.URL.Path, "method", r.Method)
		writeV1Error(w, r, http.StatusInternalServerError, "list_load_failed",
			"Loading entities failed", "check server logs")
	default:
		slog.Warn("dataentry: list free-text search failed",
			"err", err, "path", r.URL.Path, "method", r.Method)
		writeV1Error(w, r, http.StatusInternalServerError, "search_failed",
			"Free-text search failed", "check server logs")
	}
}

// writeGateError maps a readGate.PermitsRead / PermitsReadMany error
// to the right HTTP shape: client-disconnect emits nothing,
// deadline-exceeded is 504, everything else is 500 with the
// acl_query_failed code (RR-89XK). Centralized so every gate call
// site handles the error shape the same way.
//
// The raw error is logged server-side and never echoed in the
// response body (same RR-372L rationale as attachACLRequest): a
// backend error string can carry table/column names or other
// internals an API client has no business seeing.
func writeGateError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	slog.Warn("acl: read-gate query failed",
		"err", err, "path", r.URL.Path, "method", r.Method)
	if errors.Is(err, context.DeadlineExceeded) {
		writeV1Error(w, r, http.StatusGatewayTimeout, "acl_query_timeout",
			"ACL read-permission check timed out", "check server logs")
		return
	}
	writeV1Error(w, r, http.StatusInternalServerError, "acl_query_failed",
		"ACL read-permission check failed", "check server logs")
}

// --- Relation Handlers ---

func (a *App) handleV1EntityRelations(w http.ResponseWriter, r *http.Request, typeName, entityID string) {
	// The path segment is an ADDRESS — `ID` or `ID@face` (BUG-VFHUWO). The
	// bare id feeds the ACL row gate, which is face-blind by design; the face
	// selects which tail's edges are served.
	ref, refOK := parseEntityRef(entityID)
	if !refOK {
		writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
		return
	}

	// ACL gate (TKT-VQGN CRIT-2): /relations on a hidden entity 404s
	// indistinguishably. Without the gate the endpoint confirms
	// existence (200 vs 404) AND leaks the full neighbor-id set —
	// closing one channel via /include filter while leaving this open
	// would defeat the per-entity-response invariant.
	if !a.gateReadOrNotFound(w, r, typeName, ref.ID) {
		return
	}

	s := a.State()
	entity, found := a.reader.getEntityRef(r.Context(), ref)
	// The gate above authorized by (type, id), which a `type@face` grant is
	// invisible to, and this reader is the raw store — so the face half is owed
	// here (TKT-O7R2A1). It matters even though the response carries no body:
	// a content-scoped relation belongs to ONE face, so serving the default
	// face's edges discloses the structure of a face the grant withholds.
	if !found || entity.Type != typeName ||
		!faceReadable(r.Context(), entity.Type, entity.Face) {

		writeV1Error(w, r, http.StatusNotFound, "not_found", "Entity not found", "")
		return
	}

	// Outgoing edges at the ADDRESSED TAIL: a content-scoped edge belongs to
	// one face of its source, so an unfiltered read returns the union of every
	// face's edges and presents another face's links as this one's. Incoming
	// edges stay entity-level — heads are faceless, so an inbound edge points
	// at the entity and is shared by its faces.
	outgoing := a.reader.outgoingRelationsOnFace(r.Context(), ref)
	incoming := a.reader.incomingRelations(r.Context(), ref.ID)

	// Gate hidden neighbors (BUG-ABXMAV / RR-HJV8CP). Without this, a hidden
	// peer's `type` (via the ungated entityType read) and edge `meta` leak past
	// the source-only read gate. Mirror the list path (handleV1ListEntities):
	// compute the visible neighbor set in one type-batched pass and drop any
	// edge whose peer is not visible BEFORE reading its type. A visible neighbor
	// stays; a hidden one is dropped (no over-filtering).
	// TODO(BUG-ABXMAV): the "gate a set of neighbor edges" step is now
	// replicated here, in handleV1GetRelationType, and in the list path — a
	// shared chokepoint (P3) would collapse the three; deferred to avoid
	// churning the list path in a security fix.
	visibleNeighbors := visibleRelationIDs(r.Context(), a.reader, a.visibleReader,
		neighborIDsOf(outgoing, incoming))

	relations := make(map[string][]map[string]any)

	// Track the sort property per group, derived from the relation type's
	// Orderable mode. Empty string disables sorting for that group.
	groupSortProp := make(map[string]string)

	// pendingStrips defers per-edge relation-meta redaction (TKT-B1F5Q1) until
	// AFTER sortRelationGroup runs (see relationMetaStrip). Each strip also carries
	// the relation type so the redaction resolves the right grant block.
	type typedStrip struct {
		relationMetaStrip
		relType string
	}
	var pendingStrips []typedStrip

	for _, edge := range outgoing {
		if !visibleNeighbors[edge.To] {
			continue
		}
		rel := map[string]any{
			"id":        edge.To,
			"type":      a.reader.entityType(r.Context(), edge.To),
			"direction": "outgoing",
		}
		if len(edge.Properties) > 0 {
			rel["meta"] = edge.Properties
			pendingStrips = append(pendingStrips, typedStrip{
				relationMetaStrip: relationMetaStrip{rel: rel}, relType: edge.Type,
			})
		}
		relations[edge.Type] = append(relations[edge.Type], rel)
		if relDef, ok := s.Meta.Relations[edge.Type]; ok {
			if p := relDef.OutgoingOrderProperty(); p != "" {
				groupSortProp[edge.Type] = p
			}
		}
	}

	for _, edge := range incoming {
		if !visibleNeighbors[edge.From] {
			continue
		}
		relDef, ok := s.Meta.Relations[edge.Type]
		if !ok {
			continue
		}
		inverseName := inverseRelationKey(edge.Type, relDef)
		rel := map[string]any{
			"id":        edge.From,
			"type":      a.reader.entityType(r.Context(), edge.From),
			"direction": "incoming",
		}
		if len(edge.Properties) > 0 {
			rel["meta"] = edge.Properties
			// The relation grant lives on the SOURCE entity type (edge.From for an
			// incoming edge — the peer). Resolved fail-closed at strip time.
			pendingStrips = append(pendingStrips, typedStrip{
				relationMetaStrip: relationMetaStrip{rel: rel, incoming: true, peerID: edge.From},
				relType:           edge.Type,
			})
		}
		relations[inverseName] = append(relations[inverseName], rel)
		if p := relDef.IncomingOrderProperty(); p != "" {
			groupSortProp[inverseName] = p
		}
	}

	// Sort each group by its managed order property; missing values last;
	// ties stable on insertion order.
	for groupName, prop := range groupSortProp {
		if prop == "" {
			continue
		}
		sortRelationGroup(relations[groupName], prop)
	}

	// Redact hidden relation meta AFTER sorting, through the shared chokepoint.
	for _, ps := range pendingStrips {
		a.affordances.redactRelationMetaStrip(r.Context(), ps.relationMetaStrip, entity, ps.relType)
	}

	writeV1JSON(w, http.StatusOK, relations)
}

// relationMetaStrip is a deferred relation-meta redaction (TKT-B1F5Q1): the built
// wire `rel` map plus what is needed to resolve the source's `visible:` grants.
// The strip runs AFTER sortRelationGroup because the sort reads the managed order
// property out of `meta`, so redacting a (possibly hidden) order key first would
// break ordering. An outgoing edge's source is the path entity; an incoming edge's
// source is the peer (peerID), resolved fail-closed at strip time.
type relationMetaStrip struct {
	rel      map[string]any
	incoming bool
	peerID   string // incoming only: the source (from) entity id
}

// buildRelationTypeRows builds the single-relation-type wire rows (id/type[/meta])
// for the visible edges of relType in the given direction, plus the deferred meta
// strips. It is the shared build step for handleV1GetRelationType; the caller
// sorts then applies App.redactRelationMetaStrip.
func buildRelationTypeRows(
	ctx context.Context, reader entityReader, edges []*entityPkg.Relation,
	relType string, incoming bool, visibleNeighbors map[string]bool,
) (rows []map[string]any, strips []relationMetaStrip) {
	rows = make([]map[string]any, 0, len(edges))
	for _, edge := range edges {
		if edge.Type != relType {
			continue
		}
		peerID := edge.To
		if incoming {
			peerID = edge.From
		}
		if !visibleNeighbors[peerID] {
			continue
		}
		rel := map[string]any{"id": peerID, "type": reader.entityType(ctx, peerID)}
		if len(edge.Properties) > 0 {
			rel["meta"] = edge.Properties
			s := relationMetaStrip{rel: rel, incoming: incoming}
			if incoming {
				s.peerID = peerID
			}
			strips = append(strips, s)
		}
		rows = append(rows, rel)
	}
	return rows, strips
}

// sortRelationGroup sorts a relation group in place by a numeric meta key.
// Entries without a finite numeric value at prop sort last; ties stable.
func sortRelationGroup(group []map[string]any, prop string) {
	if len(group) < 2 || prop == "" {
		return
	}
	value := func(m map[string]any) (float64, bool) {
		meta, ok := m["meta"].(map[string]any)
		if !ok {
			return 0, false
		}
		return entitymanager.FiniteOrder(meta[prop])
	}
	sort.SliceStable(group, func(i, j int) bool {
		vi, oki := value(group[i])
		vj, okj := value(group[j])
		switch {
		case oki && !okj:
			return true
		case !oki && okj:
			return false
		case !oki && !okj:
			return false
		}
		return vi < vj
	})
}

func (a *App) handleV1EntityRelationType(w http.ResponseWriter, r *http.Request, typeName, entityID, relType string) {
	switch r.Method {
	case http.MethodGet:
		a.handleV1GetRelationType(w, r, typeName, entityID, relType)
	case http.MethodPost:
		ref, refOK := parseEntityRef(entityID)
		if !refOK {
			writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
			return
		}
		a.write.handleV1CreateRelation(w, r, typeName, ref, relType)
	default:
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
	}
}

// resolveRelationEndpoints returns the from/to entity IDs for a relation operation,
// swapping them when direction is incoming.
func resolveRelationEndpoints(entityID, peerID, direction string) (from, to string) {
	if direction == string(DirectionIncoming) {
		return peerID, entityID
	}
	return entityID, peerID
}

func (a *App) handleV1GetRelationType(w http.ResponseWriter, r *http.Request, typeName, entityID, relType string) {
	// An ADDRESS, as in handleV1EntityRelations (BUG-VFHUWO).
	ref, refOK := parseEntityRef(entityID)
	if !refOK {
		writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
		return
	}

	// ACL gate (TKT-VQGN CRIT-2): see handleV1EntityRelations. BARE id.
	if !a.gateReadOrNotFound(w, r, typeName, ref.ID) {
		return
	}

	entity, found := a.reader.getEntityRef(r.Context(), ref)
	// Face half of the grant, as in handleV1EntityRelations.
	if !found || entity.Type != typeName ||
		!faceReadable(r.Context(), entity.Type, entity.Face) {

		writeV1Error(w, r, http.StatusNotFound, "not_found", "Entity not found", "")
		return
	}

	incoming := r.URL.Query().Get("direction") == string(DirectionIncoming)

	// Outgoing at the addressed tail, incoming at the entity: see
	// handleV1EntityRelations.
	var edges []*entityPkg.Relation
	if incoming {
		edges = a.reader.incomingRelations(r.Context(), ref.ID)
	} else {
		edges = a.reader.outgoingRelationsOnFace(r.Context(), ref)
	}

	// Gate hidden neighbors (BUG-ABXMAV / RR-HJV8CP): same rationale as
	// handleV1EntityRelations. Collect this relation-type's peers and compute
	// the visible set in one batched pass, then skip any hidden peer BEFORE
	// reading its type (so its type/meta never reach the wire). See the
	// shared-chokepoint TODO in handleV1EntityRelations.
	var peerIDs []string
	for _, edge := range edges {
		if edge.Type != relType {
			continue
		}
		peerID := edge.To
		if incoming {
			peerID = edge.From
		}
		peerIDs = append(peerIDs, peerID)
	}
	visibleNeighbors := visibleRelationIDs(r.Context(), a.reader, a.visibleReader, peerIDs)

	relations, pendingStrips := buildRelationTypeRows(r.Context(), a.reader, edges, relType, incoming, visibleNeighbors)

	// Apply orderable sort when the type declares the relevant side.
	if relDef, ok := a.State().Meta.Relations[relType]; ok {
		var prop string
		if incoming {
			prop = relDef.IncomingOrderProperty()
		} else {
			prop = relDef.OutgoingOrderProperty()
		}
		if prop != "" {
			sortRelationGroup(relations, prop)
		}
	}

	// Redact hidden relation meta after sorting (see relationMetaStrip).
	for _, ps := range pendingStrips {
		a.affordances.redactRelationMetaStrip(r.Context(), ps, entity, relType)
	}

	writeV1JSON(w, http.StatusOK, relations)
}

func (a *App) handleV1RelationTarget(
	w http.ResponseWriter, r *http.Request, typeName, entityID, relType, targetID string,
) {
	// The path segment is an ADDRESS (BUG-VFHUWO); every arm below consumes
	// the parsed form.
	ref, refOK := parseEntityRef(entityID)
	if !refOK {
		writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
		return
	}
	switch r.Method {
	case http.MethodGet:
		// Single-relation body read for sync (RR-SYNCR1): meta + content +
		// _redacted + a relation-level ETag, dual-endpoint gated.
		handleV1GetRelationTarget(a, w, r, typeName, ref, relType, targetID)
	case http.MethodPatch:
		a.write.handleV1UpdateRelation(w, r, typeName, ref, relType, targetID)
	case http.MethodDelete:
		a.write.handleV1DeleteRelation(w, r, typeName, ref, relType, targetID)
	default:
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
	}
}

// --- Action Handlers ---

func (a *App) handleV1EntityAction(w http.ResponseWriter, r *http.Request, typeName, entityID, action string) {
	if r.Method != http.MethodPost {
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}

	switch action {
	case "clone":
		ref, refOK := parseEntityRef(entityID)
		if !refOK {
			writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
			return
		}
		a.write.handleV1CloneEntity(w, r, typeName, ref)
	default:
		writeV1Error(w, r, http.StatusNotFound, "unknown_action", "Unknown action", "")
	}
}

// --- System Handlers ---

func (a *App) handleV1Schema(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}

	s := a.State()
	schema := v1.Schema{
		Entities:  make(map[string]v1.EntityType),
		Relations: make(map[string]v1.RelationType),
		Types:     make(map[string]v1.CustomType),
		Worlds:    schemaWorlds(r.Context(), s.Meta),
	}

	for name, def := range s.Meta.Entities {
		schema.Entities[name] = toV1EntityType(s.Meta, name, def)
	}

	for name, def := range s.Meta.Relations {
		rt := v1.RelationType{
			Label:       def.Label,
			Description: def.Description,
			From:        def.From,
			To:          def.To,
			Symmetric:   def.Symmetric,
			MinOutgoing: def.MinOutgoing,
			MaxOutgoing: def.MaxOutgoing,
			MinIncoming: def.MinIncoming,
			MaxIncoming: def.MaxIncoming,
		}
		if def.Inverse != nil && def.Inverse.ID != "" {
			rt.Inverse = &v1.InverseDef{ID: def.Inverse.ID, Label: def.Inverse.Label}
		}
		if len(def.Properties) > 0 {
			rt.Properties = make(map[string]v1.PropertyDef, len(def.Properties))
			for propName, propDef := range def.Properties {
				rt.Properties[propName] = toV1PropertyDef(s.Meta, propDef)
			}
		}
		if def.OutgoingOrderProperty() != "" || def.IncomingOrderProperty() != "" {
			rt.Orderable = &v1.RelationOrderable{
				Outgoing: def.OutgoingOrderProperty() != "",
				Incoming: def.IncomingOrderProperty() != "",
			}
		}
		schema.Relations[name] = rt
	}

	for name, def := range s.Meta.Types {
		schema.Types[name] = toV1CustomType(def)
	}

	writeV1JSON(w, http.StatusOK, schema)
}

func (a *App) handleV1SchemaRoutes(w http.ResponseWriter, r *http.Request) {
	s := a.State()
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/_schema/")

	switch {
	case path == "types":
		// List entity type names
		names := make([]string, 0, len(s.Meta.Entities))
		for name := range s.Meta.Entities {
			names = append(names, name)
		}
		sort.Strings(names)
		writeV1JSON(w, http.StatusOK, names)

	case strings.HasPrefix(path, "types/"):
		// Get specific entity type
		typeName := strings.TrimPrefix(path, "types/")
		def, ok := s.Meta.Entities[typeName]
		if !ok {
			writeV1Error(w, r, http.StatusNotFound, "not_found", "Entity type not found", "")
			return
		}
		writeV1JSON(w, http.StatusOK, toV1EntityType(s.Meta, typeName, def))

	case path == "relations":
		writeV1JSON(w, http.StatusOK, s.Meta.Relations)

	default:
		writeV1Error(w, r, http.StatusNotFound, "not_found", "Resource not found", "")
	}
}

// resolveFormRelations returns a copy of rels with server-derived defaults
// filled in, for both the flat and wizard-step relation lists in handleV1Config.
//
// Widget: an empty Widget becomes "cards" when the relation type has edge
// properties/content.
//
// Direction: an absent `direction:` is inferred from the metamodel — the SPA
// widgets test `direction === 'incoming'` literally (RelationCards.vue,
// RelationPicker.vue), so the inference MUST happen here rather than in the
// browser, or a to-side binding would validate clean on the server and still
// render the wrong side. Resolving once on the way out keeps the two consumers
// agreeing by construction. The ambiguous case (entity type on both sides) is
// rejected by ValidateConfig at load, so it cannot reach this point.
func resolveFormRelations(
	s *Schema, entityType string, rels []dataentryconfig.FormRelation,
) []dataentryconfig.FormRelation {
	resolved := make([]dataentryconfig.FormRelation, len(rels))
	copy(resolved, rels)
	for i := range resolved {
		if resolved[i].Widget == "" {
			if def, ok := s.Meta.GetRelationDef(resolved[i].Relation); ok && def.HasAdvancedFeatures() {
				resolved[i].Widget = WidgetCards
			}
		}
		resolved[i].Direction = resolveConfigDirection(
			s, entityType, resolved[i].Relation, resolved[i].Direction)
	}
	return resolved
}

// resolveConfigDirection fills in an absent `direction:` from the metamodel,
// for any relation binding anchored to entityType.
//
// Unrelated to [resolveDirection], which maps a PATCH wire key to a canonical
// relation name; this one is about config bindings served to the SPA.
//
// Every SPA consumer of a direction tests the literal string (`direction ===
// 'incoming'` in RelationCards, RelationPicker, FilterBar, EntityList and
// KanbanView), so an inferred direction has to be materialized here or the
// browser silently falls back to outgoing. Validation rejects the ambiguous
// case at load, so only Resolved changes anything; everything else keeps the
// historical reading, which is also what the SPA would have assumed.
func resolveConfigDirection(
	s *Schema, entityType, relation string, dir dataentryconfig.Direction,
) dataentryconfig.Direction {
	if dir != "" || relation == "" {
		return dir
	}
	inferred, res := dataentryconfig.InferDirection(entityType, relation, s.Meta)
	switch res {
	case dataentryconfig.DirectionResolved:
		return inferred
	case dataentryconfig.DirectionAmbiguous:
		// ValidateConfig rejects an ambiguous binding at load, so the app
		// should not have started. Reaching here means that gate was bypassed;
		// say so rather than silently picking a side.
		slog.Warn("relation binding direction is ambiguous but reached the config handler; serving outgoing",
			"entity_type", entityType, "relation", relation)
	case dataentryconfig.DirectionNoSide, dataentryconfig.DirectionUnknown:
		// A wrong-side or unreadable binding — both already reported by
		// validation. Serve the historical reading rather than inventing one;
		// the SPA needs a non-empty value either way.
	}
	return dataentryconfig.DirectionOutgoing
}

// resolveListDirections returns a copy of lists with relation-column and
// filter-control directions materialized. Copy-on-serve, like
// resolveFormRelations: the in-memory config keeps what the operator wrote.
func resolveListDirections(s *Schema, lists map[string]dataentryconfig.List) map[string]dataentryconfig.List {
	out := make(map[string]dataentryconfig.List, len(lists))
	for id, list := range lists {
		l := list
		if len(l.Columns) > 0 {
			cols := make([]dataentryconfig.ListColumn, len(l.Columns))
			copy(cols, l.Columns)
			for i := range cols {
				cols[i].Direction = resolveConfigDirection(s, l.EntityType, cols[i].Relation, cols[i].Direction)
			}
			l.Columns = cols
		}
		l.FilterControls = resolveFilterControlDirections(s, l.EntityType, l.FilterControls)
		out[id] = l
	}
	return out
}

// resolveFilterControlDirections materializes directions on a filter-control
// list. Shared by lists and kanbans, which carry the same control type.
func resolveFilterControlDirections(
	s *Schema, entityType string, controls []dataentryconfig.FilterControl,
) []dataentryconfig.FilterControl {
	if len(controls) == 0 {
		return controls
	}
	out := make([]dataentryconfig.FilterControl, len(controls))
	copy(out, controls)
	for i := range out {
		out[i].Direction = resolveConfigDirection(s, entityType, out[i].Relation, out[i].Direction)
	}
	return out
}

// resolveKanbanDirections returns a copy of kanbans with card-field and
// filter-control directions materialized.
func resolveKanbanDirections(s *Schema, kanbans map[string]dataentryconfig.Kanban) map[string]dataentryconfig.Kanban {
	out := make(map[string]dataentryconfig.Kanban, len(kanbans))
	for id, kanban := range kanbans {
		k := kanban
		if len(k.Card.Fields) > 0 {
			fields := make([]dataentryconfig.KanbanCardField, len(k.Card.Fields))
			copy(fields, k.Card.Fields)
			for i := range fields {
				fields[i].Direction = resolveConfigDirection(s, k.EntityType, fields[i].Relation, fields[i].Direction)
			}
			k.Card.Fields = fields
		}
		k.FilterControls = resolveFilterControlDirections(s, k.EntityType, k.FilterControls)
		out[id] = k
	}
	return out
}

// resolveCalendarDirections fills in each calendar's relation directions, the
// same treatment resolveKanbanDirections gives a board (TKT-QXY8CQ).
//
// Calendars need it for the same reason: an event chip's relation field is
// resolved client-side against the relation's inverse key, and an unresolved
// direction leaves the SPA computing the wrong key and rendering nothing. The
// per-source entity type is what a field resolves against, so a chip field is
// resolved once per source rather than once per calendar — a calendar may
// project several types, and a relation's direction is only meaningful
// relative to one of them.
func resolveCalendarDirections(
	s *Schema, calendars map[string]dataentryconfig.Calendar,
) map[string]dataentryconfig.Calendar {
	out := make(map[string]dataentryconfig.Calendar, len(calendars))
	for id, calendar := range calendars {
		c := calendar
		// A field's direction depends on the type it hangs off, so resolve
		// against the first source that actually declares the relation.
		if len(c.Event.Fields) > 0 {
			fields := make([]dataentryconfig.KanbanCardField, len(c.Event.Fields))
			copy(fields, c.Event.Fields)
			for i := range fields {
				fields[i].Direction = resolveCalendarFieldDirection(s, c.Sources, fields[i])
			}
			c.Event = dataentryconfig.CalendarEvent{Fields: fields}
		}
		if len(c.Sources) > 0 && len(c.FilterControls) > 0 {
			c.FilterControls = resolveFilterControlDirections(s, c.Sources[0].EntityType, c.FilterControls)
		}
		out[id] = c
	}
	return out
}

// resolveCalendarFieldDirection resolves one chip field's direction against the
// calendar's sources, taking the first that yields a direction.
//
// Nil: an empty result is returned unchanged when no source declares the
// relation — the field simply renders nothing for those sources, which is the
// documented best-effort behavior for a heterogeneous calendar.
func resolveCalendarFieldDirection(
	s *Schema, sources []dataentryconfig.CalendarSource, field dataentryconfig.KanbanCardField,
) dataentryconfig.Direction {
	if field.Relation == "" {
		return field.Direction
	}
	for _, src := range sources {
		if d := resolveConfigDirection(s, src.EntityType, field.Relation, field.Direction); d != "" {
			return d
		}
	}
	return field.Direction
}

// aboutDescription is the deployment description shown by the SPA's global
// "About" help (TKT-DUQBD0). The data-entry.yaml `app.description` wins when set
// (it is the UI-app-specific text); otherwise it falls back to the metamodel's
// top-level `description` (the schema-level prose added in TKT-0YBFT8). Empty
// when neither is set — the SPA hides the About button.
//
// This is a SEPARATE field from AppConfig.Description on the wire: that field is
// also rendered by SettingsView as a plain one-line value, and pushing the
// (possibly multi-paragraph markdown) metamodel description into it would
// regress that view. The About surface gets its own field, `about_description`.
func aboutDescription(s *Schema) string {
	if s.Cfg != nil && s.Cfg.App.Description != "" {
		return s.Cfg.App.Description
	}
	if s.Meta != nil {
		return s.Meta.Description
	}
	return ""
}

func (a *App) handleV1Config(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}
	s := a.State()
	// Resolve relation widgets: auto-detect "cards" for relations with
	// properties/content, for both single-page (flat) and wizard (step) forms.
	forms := make(map[string]dataentryconfig.Form, len(s.Cfg.Forms))
	for id, form := range s.Cfg.Forms {
		f := form
		f.Relations = resolveFormRelations(s, form.EntityType, f.Relations)
		if len(f.Steps) > 0 {
			steps := make([]dataentryconfig.FormStep, len(f.Steps))
			copy(steps, f.Steps)
			for i := range steps {
				steps[i].Relations = resolveFormRelations(s, form.EntityType, steps[i].Relations)
			}
			f.Steps = steps
		}
		forms[id] = f
	}

	config := v1.Config{
		NextActionBands: s.Cfg.NextActionBands,
		App: v1.AppConfig{
			Name:              s.Cfg.App.Name,
			Description:       s.Cfg.App.Description,
			PlantUMLServerURL: s.Cfg.App.PlantUMLServerURL,
			DefaultWorld:      s.Cfg.App.DefaultWorld,
			// The same nil check the history handler gates on, so the
			// affordance and the endpoint cannot disagree.
			HistoryEnabled: a.versions != nil,
		},
		AboutDescription: aboutDescription(s),
		Styles:           s.StyleMap,
		Forms:            forms,
		Lists:            resolveListDirections(s, s.Cfg.Lists),
		Views:            s.Cfg.Views,
		EntityViews:      s.Cfg.EntityViews,
		Kanbans:          resolveKanbanDirections(s, s.Cfg.Kanbans),
		Calendars:        resolveCalendarDirections(s, s.Cfg.Calendars),
		Gantts:           s.Cfg.Gantts,
		Dashboard:        s.Cfg.Dashboard,
		Actions:          s.Cfg.Actions,
		Navigation:       s.Cfg.Navigation,
		Documents:        s.Cfg.Documents,
		Apps:             appsToV1(a.scanAppsOrLog()),
		Palette:          a.palette.Resolved(),
	}

	writeV1JSON(w, http.StatusOK, config)
}

// maxSearchLimit bounds the `limit` parameter of /_search. It matches the
// list endpoint's per_page ceiling.
const maxSearchLimit = 100

// parseSearchLimit reads the optional `limit` parameter of /_search. An empty
// value means no limit and returns 0. The second result is false for a value
// that is not an integer in [1, maxSearchLimit].
//
// The editor's mention menu is the motivating caller: `sort:modified:desc`
// with a small limit lists recently modified entities without shipping every
// visible row to the browser.
func parseSearchLimit(raw string) (int, bool) {
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxSearchLimit {
		return 0, false
	}
	return n, true
}

func (a *App) handleV1Search(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}

	limit, ok := parseSearchLimit(r.URL.Query().Get("limit"))
	if !ok {
		writeV1Error(w, r, http.StatusBadRequest, "invalid_limit",
			fmt.Sprintf("limit must be an integer between 1 and %d", maxSearchLimit), "")
		return
	}
	query := r.URL.Query().Get("q")
	if query == "" {
		writeV1JSON(w, http.StatusOK, v1.ListResponse{Data: []v1.Entity{}, Meta: v1.ListMeta{}})
		return
	}

	// A `type` parameter also joins the query as a `type:` clause, so a
	// free-text search is scoped BEFORE its relevance cap rather than
	// filtered after it, where the cap could have spent every slot on
	// other types. Only a declared type name is spliced in; any other value
	// cannot match a row, which the exact filter below still ensures.
	typeFilter := r.URL.Query().Get("type")
	if _, declared := a.State().Meta.Entities[typeFilter]; declared {
		query = "type:" + typeFilter + " " + query
	}

	// executeQuery is read-gated (TKT-BA8BSX): only entities the
	// request principal may read come back, and gate/load/search
	// failures surface instead of silently truncating.
	entities, err := a.queries.executeQuery(r.Context(), query)
	if err != nil {
		writeListPipelineError(w, r, err)
		return
	}

	// The exact filter stays: a `type:` clause already in `q` unions with
	// the spliced one.
	if typeFilter != "" {
		filtered := make([]*entityPkg.Entity, 0)
		for _, e := range entities {
			if e.Type == typeFilter {
				filtered = append(filtered, e)
			}
		}
		entities = filtered
	}
	// Truncated after the read gate and the type filter, so the cap counts
	// only rows this principal may see and a hidden row cannot take a slot.
	if limit > 0 && len(entities) > limit {
		entities = entities[:limit]
	}
	// Search rows are content-free (rowcontent.go); bodies are opt-in and
	// bounded by the search result cap.
	if wantContent(r.URL.Query()) {
		if err := loadRowContent(r.Context(), a.Services().Store, entities); err != nil {
			writeListPipelineError(w, r, fmt.Errorf("%w: %w", errListLoad, err))
			return
		}
	}

	meta := a.State().Meta
	data := make([]v1.Entity, 0, len(entities))
	pageCtx := primeVerdicts(r.Context(), a.fieldResolver, entities)
	for _, e := range entities {
		entityDef := meta.Entities[e.Type]
		plural := entityDef.GetPlural(e.Type)
		// includeRelations stays false on search results: the read gate
		// covers root entities only, and a relation map would expose
		// {ID, Title} of related entities this principal may not read.
		// Flipping this requires per-target gating first (RR-QO01XY) —
		// TestACLSearch_VisibleHitRelatedToHidden pins the invariant.
		row := a.serializer.forWireRelated(pageCtx, e, nil, nil, nil, a.Meta(), plural)
		// Same provenance a list row carries, and nil in the default world
		// for the same reason (see handleV1ListEntities).
		if !worldScopeFrom(r.Context()).IsDefaultWorld() {
			row.World = worldProvenance(r.Context(), e)
		}
		data = append(data, row)
	}

	resp := v1.ListResponse{
		Data: data,
		Meta: v1.ListMeta{
			Total:   len(data),
			Page:    1,
			PerPage: len(data),
		},
	}

	writeV1JSON(w, http.StatusOK, resp)
}

func (a *App) handleV1Analyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}

	analysisResult := a.analyze.runAnalysis(r.Context(), a.State().Meta)

	// runAnalysis now reads GATED per the requesting principal (TKT-3FL2S6), so
	// a hidden entity never enters a check and a visible entity is redacted
	// before its value can reach an issue title or message. No post-hoc output
	// filter is needed — the issues are already the requester's visible slice.
	// Flatten sections to (issue, section) pairs for the wire builder.
	visible := flattenIssues(analysisResult.Sections)

	result := APIAnalysisResult{
		Issues:  make([]APIIssue, 0, len(visible)),
		ByCheck: make(map[string]int),
	}

	// Carry each section's truncation flag onto the flat response, so a
	// capped check is visibly incomplete rather than silently short.
	for _, section := range analysisResult.Sections {
		if section.Truncated {
			result.TruncatedChecks = append(result.TruncatedChecks, section.Name)
		}
	}

	// Loopback gate: same policy as action / document surfaces.
	// Non-loopback callers get a degraded envelope on script-error
	// issues (no source slice, no stack, no captured output).
	fullDetail := a.allowFullScriptDetail(r)

	for _, vi := range visible {
		issue, section := vi.issue, vi.section
		api := APIIssue{
			EntityID:    issue.EntityID,
			EntityType:  issue.EntityType,
			Title:       issue.Title,
			Message:     issue.Message,
			RuleMessage: issue.RuleMessage,
			Severity:    issue.Severity,
			CheckType:   section,
			Detail:      issue.Detail,
		}
		if issue.ScriptError != nil {
			env := buildScriptErrorEnvelope(issue.ScriptError, fullDetail, "")
			api.ScriptError = &env
		}
		result.Issues = append(result.Issues, api)
		result.ByCheck[section]++
		switch issue.Severity {
		case "error":
			result.Errors++
		case "warning":
			result.Warnings++
		}
	}

	writeV1JSON(w, http.StatusOK, result)
}

// visibleIssue pairs an analysis issue with its section name, carried
// through the ACL filter so the wire builder keeps the issue→check
// association without re-walking sections.
type visibleIssue struct {
	issue   AnalysisIssue
	section string
}

// flattenIssues walks the analysis sections into (issue, section) pairs for the
// wire builder. No filtering: runAnalysis already read gated, so the issues are
// the requester's visible slice — a hidden entity produced no issue and a
// visible entity was redacted before its value could reach a title or message
// (TKT-3FL2S6, replacing the former visibleAnalysisIssues output filter).
func flattenIssues(sections []AnalysisSection) []visibleIssue {
	var out []visibleIssue
	for _, section := range sections {
		for _, issue := range section.Issues {
			out = append(out, visibleIssue{issue: issue, section: section.Name})
		}
	}
	return out
}

// --- Helper Functions ---

// resolveV1Includes expands `?include=` into the `included` map.
//
// Candidate collection is world-aware (TKT-WRLDAPI item 4): under a
// non-default world the neighbors come from the world-scoped seam, so an
// included peer is THAT world's face of the neighbor and a neighbor with no
// face in this world is absent. Under the default world it is the ungated
// reader, byte-identical to before.
//
// Everything after collection — the batched ACL gate, nested recursion,
// serialization — is shared by both paths deliberately. The world decides
// WHICH entities are candidates; it does not change how they are gated or
// rendered, and forking that would be two visibility rules to keep in step.
func (a *App) resolveV1Includes(ctx context.Context, entity *entityPkg.Entity, includes string) map[string]v1.Entity {
	s := a.State()
	included := make(map[string]v1.Entity)

	// Collect candidate target entities first; filter via the ACL
	// gate per-type (batched), then serialize the survivors. The
	// two-phase shape exists to satisfy RR-M84L (the include channel
	// must respect the per-entity visibility rule) AND RR-FRK1 (a
	// hub entity with 50 neighbors must not cost 50 GraphCount
	// round-trips).
	//
	// nestedFor maps target.ID → the remaining nested-include
	// expression (e.g. "implements.requires" → "requires" stored
	// against the implements target). Recursion happens after the
	// visibility filter so hidden neighbors don't trigger hidden nested
	// probes.
	candidates, nestedFor, err := includeCandidates(ctx, a.reader, a.worldNeighbors, entity, includes)
	if err != nil {
		// The include channel is a best-effort affordance (see
		// filterVisibleIncludes): the authoritative read is the explicit GET.
		// A world-resolution fault drops the block and logs rather than
		// failing the whole entity response — but it must not be silent, or
		// an outage reads as "this entity has no neighbors in this world".
		slog.Warn("dataentry: resolving world-scoped includes failed; include block omitted",
			"entity", entity.ID, "include", includes, "err", err)
		return nil
	}

	visible := a.filterVisibleIncludes(ctx, candidates)
	ctx = primeVerdicts(ctx, a.fieldResolver, visible)
	for _, target := range visible {
		entityDef := s.Meta.Entities[target.Type]
		plural := entityDef.GetPlural(target.Type)
		wired := a.serializer.forWireRelated(ctx, target, nil, nil, nil, a.Meta(), plural)
		// Per-neighbor provenance (QA F-6b). Each included target was resolved
		// through the world INDEPENDENTLY of its source, so one entity's
		// neighbors can mix a chain hit with a stand-in — and a client cannot
		// tell which without being told. The detail view badges exactly this:
		// "the link leads somewhere English", which the payload previously had
		// no way to say.
		//
		// Same helper the entity root and list rows use, so the three surfaces
		// cannot disagree about what a face resolution was.
		wired.World = worldProvenance(ctx, target)
		included[target.ID] = wired

		if nested, ok := nestedFor[target.ID]; ok {
			maps.Copy(included, a.resolveV1Includes(ctx, target, nested))
		}
	}
	return included
}

// filterVisibleIncludes drops any candidate the principal cannot read,
// batched by entity type. For each distinct type ONE gate call
// (PermitsReadMany over every candidate of that type) — turning a
// worst case of O(N) per-id probes into O(distinct-types). RR-FRK1.
//
// On gate error: drop the whole type's candidates (fail-closed) and
// log loud so operators see the underlying failure rather than just
// "the include block is empty." The include channel is a "best
// effort" affordance; the explicit GET / list endpoints are the
// authoritative read surface.
func (a *App) filterVisibleIncludes(ctx context.Context, candidates []*entityPkg.Entity) []*entityPkg.Entity {
	return a.visibleReader.filterVisible(ctx, candidates)
}

// applyV1Filters applies `filter[...]` query params to the entity slice. Pure
// data transform — a free function, not App behavior (TKT-N26KLB M5.5).
// A malformed key or unsupported operator returns errBadFilter (HTTP 400)
// instead of silently dropping the clause, which returned the unfiltered
// superset and hid config/caller typos.
//
// isRelationKey identifies filter keys that target a metamodel relation rather
// than an entity property; those are skipped here and handled by the relation
// pass in scopedSortedEntities (which has ctx + config for direction-aware
// traversal). A relation key would otherwise match no property and nuke the
// whole result set (TKT-5U7QBR). A nil predicate treats every key as a
// property filter (the pre-TKT-5U7QBR behavior).
//
//nolint:gocognit,funlen // dispatches over every supported filter operator and value type; each branch is one operator's semantics, not extractable shared logic.
func applyV1Filters(
	entities []*entityPkg.Entity, query map[string][]string, _ string, isRelationKey func(string) bool,
) ([]*entityPkg.Entity, error) {
	filtered := entities

	for key, values := range query {
		if !strings.HasPrefix(key, "filter[") || len(values) == 0 {
			continue
		}

		// Parse filter[property] or filter[property][operator] or
		// filter[property][operator][] (multi-value array form). Strip the
		// optional `[]` array suffix before splitting so we get clean parts.
		filterKey := strings.TrimPrefix(key, "filter[")
		filterKey = strings.TrimSuffix(filterKey, "]")
		trimmed := strings.TrimSuffix(filterKey, "][") // was "...[]"
		// An explicit `[]` suffix declares the ARRAY form: each repeated param
		// is one member, verbatim. Without it, a lone param is the comma form.
		// The suffix — not the number of params — is the signal, since a
		// one-element array is indistinguishable from a comma list by count.
		isArrayForm := trimmed != filterKey
		filterKey = trimmed
		parts := strings.Split(filterKey, "][")

		// Validate parsed shape. A malformed key like `filter[prop][][weird]`
		// produces parts=["prop", "", "weird"] — more than 2 parts or an
		// empty property/operator means the URL is bogus. Reject the request:
		// the old skip-with-a-log "fail closed" still returned the unfiltered
		// superset, which is fail-open at the result level.
		if len(parts) > 2 {
			return nil, fmt.Errorf("%w: key %q has too many segments", errBadFilter, key)
		}
		property := parts[0]
		if property == "" {
			return nil, fmt.Errorf("%w: key %q has an empty property", errBadFilter, key)
		}

		// Relation filter keys are handled by the direction-aware relation
		// pass in scopedSortedEntities. Skip them here so a relation key
		// (which matches no property) doesn't nuke the result set.
		if isRelationKey != nil && isRelationKey(property) {
			continue
		}

		operator := "eq"
		if len(parts) == 2 {
			if parts[1] == "" {
				return nil, fmt.Errorf("%w: key %q has an empty operator segment", errBadFilter, key)
			}
			operator = parts[1]
		}

		// Reject unknown operators BEFORE the per-entity loop. A typo like
		// `filter[status][equals]=done` used to be skipped with only a log —
		// which returned every entity, the very silent inclusion the skip
		// claimed to prevent. A 400 makes the typo visible to the caller
		// (and to a config author whose list filter reached the wire).
		switch operator {
		case "eq", "ne", "contains", "in", "lt", "lte", "gt", "gte":
			// known
		default:
			return nil, fmt.Errorf("%w: unknown operator %q in key %q (supported: eq, ne, contains, in, lt, lte, gt, gte)",
				errBadFilter, operator, key)
		}

		// Multi-value support: `in`/`ne` collect ALL repeated values from the
		// query (e.g. `filter[tags][in][]=a&filter[tags][in][]=b`). Other
		// operators stay last-write-wins on values[len-1] for predictability.
		//
		// setValues keeps the repeated params as DISTINCT members rather than
		// joining them into one comma string: a value may legitimately contain
		// a comma ("Legal, Risk & Compliance"), and join-then-split would tear
		// it into two members that match the wrong rows — and make `ne` fail to
		// exclude the row it names, the same wrong-answer class as BUG-AMK38R.
		// A single param still splits on commas, which is the documented
		// `filter[x][in]=a,b` form; that form simply cannot express a value
		// containing a comma, so the repeated form is the one to use for those.
		var value string
		var setValues []string
		if operator == "in" || operator == "ne" {
			value = resolveFilterVariablesInList(strings.Join(values, ","))
			setValues = filterSetValues(values, isArrayForm)
		} else {
			value = resolveFilterVariable(values[len(values)-1])
		}

		var newFiltered []*entityPkg.Entity
		for _, e := range filtered {
			propVal, ok := e.Properties[property]
			if !ok {
				if operator == "eq" && value == "" {
					newFiltered = append(newFiltered, e)
				}
				continue
			}

			// A list-typed property is compared element-wise: ANY element
			// matching satisfies eq/in/contains, and ne is the exact
			// complement (NO element matches). A scalar is a one-element
			// list, so scalar behavior is unchanged. See propertyElements.
			switch operator {
			case "eq":
				if anyElement(propVal, func(el string) bool { return el == value }) {
					newFiltered = append(newFiltered, e)
				}
			case "ne":
				// Support comma-separated values as NOT IN
				excluded := anyElement(propVal, func(el string) bool {
					return slices.Contains(setValues, el)
				})
				if !excluded {
					newFiltered = append(newFiltered, e)
				}
			case "contains":
				needle := strings.ToLower(value)
				matches := anyElement(propVal, func(el string) bool {
					return strings.Contains(strings.ToLower(el), needle)
				})
				if matches {
					newFiltered = append(newFiltered, e)
				}
			case "in":
				matches := anyElement(propVal, func(el string) bool {
					return slices.Contains(setValues, el)
				})
				if matches {
					newFiltered = append(newFiltered, e)
				}
			case "lt", "lte", "gt", "gte":
				// Ordered comparison is scalar-only. What it means to order a
				// LIST against a bound is a genuine design question, so this
				// declines to answer rather than guessing: the old code
				// compared the Go slice literal, which made `tags < "zzz"`
				// true for every row (including empty and null ones) — a
				// confident wrong answer, the same class as BUG-AMK38R.
				// Excluding the row is the fail-closed direction and leaves
				// the semantics free to be defined later.
				if isListValue(propVal) {
					slog.Warn("ordered filter operator on a list property; no rows match",
						"property", property, "operator", operator)
					continue
				}
				match, err := compareValues(fmt.Sprintf("%v", propVal), value, operator)
				if err != nil {
					// Type mismatch (e.g. property is a date, filter value isn't).
					// Exclude the entity rather than silently lying via lexicographic
					// fallback. Log so users notice.
					slog.Warn("filter compare error", "property", property, "error", err)
					continue
				}
				if match {
					newFiltered = append(newFiltered, e)
				}
			}
		}
		filtered = newFiltered
	}

	return filtered, nil
}

// parseSortParam parses the `sort=` query param into ordered sort specs:
// "-created,title" means descending created, then ascending title. Returns nil
// when no sort was requested.
//
// Shared by every consumer of the grammar — the list pipeline that APPLIES the
// sort and the export context that REPORTS it — so the two cannot drift into
// describing different orderings.
func parseSortParam(query map[string][]string) []filter.SortSpec {
	sortParam := queryGet(query, "sort")
	if sortParam == "" {
		return nil
	}
	var specs []filter.SortSpec
	for part := range strings.SplitSeq(sortParam, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		spec := filter.SortSpec{Direction: "asc"}
		if after, found := strings.CutPrefix(part, "-"); found {
			spec.Direction = "desc"
			part = after
		}
		spec.Property = part
		specs = append(specs, spec)
	}
	return specs
}

// applyV1Sorting applies `sort=` query params to the entity slice. Pure data
// transform — a free function, not App behavior (TKT-N26KLB M5.5).
//
// The ordering rule lives in [filter.QuerySort] so this path, the search and
// dashboard path, and every store backend share one definition
// (TKT-9OFGH4). It is store.GraphQuery.OrderBy's contract: byte-wise on the
// text form, enum values by declared position, absent rows as the largest
// value, id ascending as the final tiebreak — so a request reads the same
// whether or not it was pushed into the database.
//
// meta may be nil; that yields byte-wise comparison for every property, which
// is the right answer when no schema is available.
func applyV1Sorting(
	entities []*entityPkg.Entity, query map[string][]string, meta *metamodel.Metamodel,
) []*entityPkg.Entity {
	sortSpecs := parseSortParam(query)
	if len(sortSpecs) == 0 {
		return entities
	}

	sorted := make([]*entityPkg.Entity, len(entities))
	copy(sorted, entities)
	filter.QuerySortApply(newEntityQuerySort(sortSpecs, sorted, meta), sorted, entityRecord, sortSpecs)
	return sorted
}

// newEntityQuerySort resolves declared value orders for the types actually
// present in rows, so a mixed-type result set ranks each property against
// every definition that declares it.
func newEntityQuerySort(
	specs []filter.SortSpec, rows []*entityPkg.Entity, meta *metamodel.Metamodel,
) *filter.QuerySort {
	if meta == nil {
		return filter.NewQuerySort(specs, nil, nil)
	}
	defs := make(map[string]*metamodel.EntityDef)
	for _, e := range rows {
		if _, seen := defs[e.Type]; seen {
			continue
		}
		if def, ok := meta.GetEntityDef(e.Type); ok {
			defs[e.Type] = def
		}
	}
	return filter.NewQuerySort(specs, defs, meta)
}

func parseV1Pagination(query map[string][]string) (page, perPage int) {
	page = 1
	perPage = 25

	if vals, ok := query["page"]; ok && len(vals) > 0 {
		if p, err := strconv.Atoi(vals[0]); err == nil && p > 0 {
			page = p
		}
	}

	if vals, ok := query["per_page"]; ok && len(vals) > 0 {
		if pp, err := strconv.Atoi(vals[0]); err == nil && pp > 0 && pp <= 100 {
			perPage = pp
		}
	}

	return page, perPage
}

// addPaginationLinks writes RFC 8288 Link headers for the collection page.
// Pure data transform over its args — a free function (TKT-N26KLB M5.5).
func addPaginationLinks(w http.ResponseWriter, _ *http.Request, page, perPage, total int, plural string) {
	totalPages := (total + perPage - 1) / perPage
	if totalPages == 0 {
		totalPages = 1
	}

	baseURL := "/api/v1/" + plural
	var links []string

	// First
	links = append(links, fmt.Sprintf("<%s?page=1&per_page=%d>; rel=\"first\"", baseURL, perPage))

	// Prev
	if page > 1 {
		links = append(links, fmt.Sprintf("<%s?page=%d&per_page=%d>; rel=\"prev\"", baseURL, page-1, perPage))
	}

	// Next
	if page < totalPages {
		links = append(links, fmt.Sprintf("<%s?page=%d&per_page=%d>; rel=\"next\"", baseURL, page+1, perPage))
	}

	// Last
	links = append(links, fmt.Sprintf("<%s?page=%d&per_page=%d>; rel=\"last\"", baseURL, totalPages, perPage))

	w.Header().Set("Link", strings.Join(links, ", "))
}

// computeEntityETag reads the entity's edges itself. Callers that ALREADY hold
// the edges they served should use [entityETagWithEdges] instead — see the
// duplication note at the single-entity GET's call site.
func (a *App) computeEntityETag(ctx context.Context, e *entityPkg.Entity) string {
	edges, err := etagEdges(ctx, a.reader, a.worldNeighbors, a.visibleReader, e)
	if err != nil {
		return etagUnresolved(ctx, e)
	}
	return entityETagWithEdges(e, edges)
}

// etagUnresolved is the validator for an entity whose edges could not be
// resolved. See the sentinel rationale on [entityETagWithEdges].
func etagUnresolved(ctx context.Context, e *entityPkg.Entity) string {
	slog.Warn("dataentry: ETag: world-scoped edge read failed; "+
		"folding a sentinel so the validator matches nothing",
		"entity", e.ID, "world", worldFromContext(ctx).name)
	return entityETagWithEdges(e, []*entityPkg.Relation{
		{Type: "!unresolved", To: "!unresolved"},
	})
}

// entityETagWithEdges hashes e together with the edges the response actually
// carried.
//
// Taking the edges as a PARAMETER rather than reading them is what keeps the
// validator and the body describing the same document: the GET computes them
// once, for both.
//
// A package function, not a method: it needs nothing from App, and App is
// pinned at its plimsoll cap. (The ETag split briefly took it to 105, which is
// the load line doing its job — see the App type doc.)
func entityETagWithEdges(e *entityPkg.Entity, edges []*entityPkg.Relation) string {
	h := sha256.New()
	_, _ = h.Write([]byte(e.ID))
	_, _ = h.Write([]byte(e.Type))
	_, _ = h.Write([]byte(e.Content))

	// Sort properties so the hash is stable across map iteration order.
	propKeys := make([]string, 0, len(e.Properties))
	for k := range e.Properties {
		propKeys = append(propKeys, k)
	}
	sort.Strings(propKeys)
	for _, k := range propKeys {
		_, _ = h.Write([]byte(k))
		_, _ = fmt.Fprintf(h, "=%v;", e.Properties[k])
	}

	// Fold the served FACE into the hash — not the world NAME. Two worlds that
	// resolve the same id to different faces get different validators, so a
	// published-world GET cannot answer 304 against a draft-derived one (the
	// cross-world cache poisoning this fold exists for). Two worlds serving
	// the SAME face share one, which is correct: the bytes are the same. And
	// a write, which addresses the stored face directly and refuses `?world=`,
	// computes the same fold — so an If-Match taken from a GET that showed
	// the bare face matches the PATCH of it, while one taken from a GET that
	// showed `published` refuses a write aimed at the draft. Folding the
	// world name instead made every world-bound If-Match a permanent 412.
	_, _ = h.Write([]byte("f:" + e.Face.String() + ";"))

	// Fold outgoing relations into the hash so PATCHes that only change
	// edges also change the ETag — otherwise If-Match / If-None-Match
	// round-trips poison client caches.
	//
	// The edges are the caller's, and must be the ones the response BODY
	// carried. Until TKT-WRLDAPI item 4 a world-bound response carried no
	// relations at all, so nothing was folded under a world; now it carries
	// world-resolved ones, and folding nothing would mean an edge change under
	// a world never moved the ETag — a stale 304 on a response whose links had
	// changed. Folding DEFAULT-world edges instead is the mirror bug: a draft
	// edit would invalidate a published validator, and publishing a face would
	// not.
	edgeKeys := make([]string, 0, len(edges))
	for _, edge := range edges {
		edgeKeys = append(edgeKeys, edge.Type+"|"+edge.To)
	}
	sort.Strings(edgeKeys)
	for _, k := range edgeKeys {
		_, _ = h.Write([]byte("r:"))
		_, _ = h.Write([]byte(k))
	}

	sum := h.Sum(nil)
	return fmt.Sprintf("\"%s\"", base64.StdEncoding.EncodeToString(sum[:8]))
}

// validateCreateIDOpts enforces that `id` is only accepted for manual-ID types
// and that `prefix` is only accepted for non-manual types with a known prefix.
// For manual-ID types that declare one or more prefixes, the `id` must start
// with one of them AND include a non-empty suffix. Surrounding whitespace on
// the inputs is trimmed at the boundary so the error message lines up with
// what the user actually typed. Returns an empty string when valid.
func validateCreateIDOpts(def *metamodel.EntityDef, id, prefix string) string {
	id = strings.TrimSpace(id)
	prefix = strings.TrimSpace(prefix)

	if id != "" && !def.IsManualID() {
		return "id not accepted for non-manual ID type; use 'prefix' instead"
	}
	if id != "" && def.IsManualID() {
		if prefixes := def.GetIDPrefixes(); len(prefixes) > 0 {
			matched := false
			for _, p := range prefixes {
				// Reject the bare prefix (id == p) — the entity needs a
				// distinguishing suffix or it conflicts with the prefix
				// concept itself. (RR-…)
				if strings.HasPrefix(id, p) && len(id) > len(p) {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Sprintf("id %q must start with one of %v and include a suffix", id, prefixes)
			}
		}
	}
	if prefix == "" {
		return ""
	}
	if def.IsManualID() {
		return "prefix not applicable to manual ID type"
	}
	if slices.Contains(def.GetIDPrefixes(), prefix) {
		return ""
	}
	return fmt.Sprintf("prefix %q is not valid; allowed: %v", prefix, def.GetIDPrefixes())
}

func writeV1JSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeV1Error(w http.ResponseWriter, r *http.Request, status int, errType, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)

	err := v1.Error{
		Type:     "https://rela.dev/errors/" + errType,
		Title:    title,
		Status:   status,
		Detail:   detail,
		Instance: r.URL.Path,
	}

	_ = json.NewEncoder(w).Encode(err)
}

// --- Side Panel API ---

// --- Sidebar API ---

// --- Conflicts API ---

// handleV1Conflicts returns the list of conflicted files as JSON.
func (a *App) handleV1Conflicts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}

	ctx := &project.Context{
		Root:         a.paths.Root,
		EntitiesDir:  a.paths.EntitiesDir,
		RelationsDir: a.paths.RelationsDir,
	}

	result, err := conflict.DetectAll(ctx)
	if err != nil {
		writeV1Error(w, r, http.StatusInternalServerError, "conflict_detection_failed", "Failed to detect conflicts", err.Error())
		return
	}

	items := make([]v1.ConflictItem, 0, len(result.Files))
	for _, cf := range result.Files {
		relPath, _ := filepath.Rel(ctx.Root, cf.Path)
		items = append(items, v1.ConflictItem{
			Path:        relPath,
			EntityType:  cf.EntityType,
			EntityID:    cf.EntityID,
			MarkerCount: len(cf.Markers),
		})
	}

	writeV1JSON(w, http.StatusOK, v1.ConflictsResponse{
		Conflicts: items,
		Count:     len(items),
	})
}

// handleV1ConflictRoutes handles GET /api/v1/_conflicts/{path} and POST /api/v1/_conflicts/resolve.
func (a *App) handleV1ConflictRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/_conflicts/")

	if path == "resolve" && r.Method == http.MethodPost {
		a.write.handleV1ConflictResolve(w, r)
		return
	}

	if r.Method != http.MethodGet {
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}

	// Get conflict details. The path is caller-supplied — contain it to
	// the project root before any filesystem access.
	absPath, ok := resolveConflictPath(a.paths, w, r, path)
	if !ok {
		return
	}

	cf, err := conflict.ParseConflictedFile(absPath, a.State().Meta)
	if err != nil {
		writeV1Error(w, r, http.StatusInternalServerError, "parse_failed", "Failed to parse conflict", err.Error())
		return
	}

	info := conflict.AnalyzeConflict(cf)

	diffs := make([]v1.PropertyDiff, 0, len(info.PropertyDiffs))
	for _, d := range info.PropertyDiffs {
		diffs = append(diffs, v1.PropertyDiff{
			Property:    d.Property,
			OursValue:   fmt.Sprintf("%v", d.OursValue),
			TheirsValue: fmt.Sprintf("%v", d.TheirsValue),
			IsSame:      d.IsSame,
		})
	}

	detail := v1.ConflictDetail{
		Path:          path,
		EntityType:    cf.EntityType,
		EntityID:      cf.EntityID,
		PropertyDiffs: diffs,
		ContentSame:   info.ContentSame,
		ContentOurs:   info.ContentDiffOurs,
		ContentTheirs: info.ContentDiffTheirs,
	}

	writeV1JSON(w, http.StatusOK, detail)
}

// resolveConflictPath contains the caller-supplied conflict-file path
// to the project root. On failure it writes the error response (404
// when the path is inside the project but missing, 403 when it escapes
// the root) and returns ok=false.
func resolveConflictPath(paths *project.Context, w http.ResponseWriter, r *http.Request, p string) (string, bool) {
	resolved, err := containedProjectPath(paths.Root, p)
	switch {
	case errors.Is(err, errPathNotFound):
		writeV1Error(w, r, http.StatusNotFound, "conflict_not_found", "Conflicted file not found", "")
		return "", false
	case err != nil:
		writeV1Error(w, r, http.StatusForbidden, "path_outside_project", "Path is outside the project root", "")
		return "", false
	}
	return resolved, true
}

// conflictAuditSubject derives the audit subject for a resolved
// conflict write. Exactly one of e / rel is non-nil ([conflict.Resolve]
// errors otherwise).
func conflictAuditSubject(e *entityPkg.Entity, rel *entityPkg.Relation) *audit.Subject {
	if rel != nil {
		return &audit.Subject{
			Kind:         "relation",
			RelationType: rel.Type,
			FromID:       rel.From,
			ToID:         rel.To,
		}
	}
	return &audit.Subject{Kind: "entity", Type: e.Type, ID: e.ID}
}

// --- Documents API ---

// handleV1Documents handles GET /api/v1/_documents/{docName}/{entityId} and
// GET /api/v1/_documents/{docName}. Returns JSON with rendered HTML content
// for Vue SPA consumption.
//
// The two path shapes serve the two document kinds
// (dataentryconfig.DocumentConfig): the two-segment form renders an
// entity-anchored document, the one-segment form a standalone one. Each shape
// REJECTS the other kind rather than inventing or ignoring an entry id — see
// handleV1StandaloneDocument.
func (a *App) handleV1Documents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}

	// Parse path: /api/v1/_documents/{docName}[/{entityId}][/_export], then
	// dispatch on the shape. The branches live in separate functions so none
	// grows another's guards by accident — they differ in their ACL story, not
	// just in which segments are present.
	//
	// The trailing `_export` segment is matched EXACTLY and only in final
	// position, so the four shapes are disjoint:
	//
	//	{doc}                     render, standalone
	//	{doc}/_export             export, standalone
	//	{doc}/{entityId}          render, anchored
	//	{doc}/{entityId}/_export  export, anchored
	//
	// The two-segment forms cannot collide: an entity id can never be
	// "_export" because entity.ValidateID rejects a leading underscore. The
	// other side of that premise — a document literally named "_export" — is
	// closed at config load by validateDocuments. See exportSegment.
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/_documents/")
	parts := strings.Split(path, "/")

	// Trailing empty segment from a trailing slash ("/_documents/foo/") is not
	// a missing entity id; drop it so the shape matches the one-segment form.
	if len(parts) > 1 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}

	// Any REMAINING empty segment is rejected outright. This is not defensive
	// tidying — without it "/_documents/sales//_export" splits to
	// ["sales", "", "_export"], matches the 3-segment export case, and calls
	// the export handler with an empty entity id, which dispatches to the
	// STANDALONE resolver. That serves a standalone document at the anchored
	// URL shape, precisely what this package's CLAUDE.md forbids ("Never let
	// one URL shape serve the other kind... do not fall back to rendering with
	// an empty or guessed entry id").
	//
	// net/http's ServeMux happens to redirect "//" before a request reaches
	// here, but that is a property of the mux, not of this function: tests and
	// any future direct mount call it without that normalization, and a
	// percent-encoded %2F survives ServeMux cleaning entirely. Reject here so
	// the guarantee belongs to the router itself.
	if slices.Contains(parts, "") {
		writeV1Error(w, r, http.StatusBadRequest, "invalid_path",
			"Path must be /_documents/{docName}/{entityId}", "")
		return
	}

	switch {
	case len(parts) == 1:
		handleV1StandaloneDocument(a, w, r, parts[0])
	case len(parts) == 2 && parts[1] == exportSegment:
		handleV1ExportDocument(a, w, r, parts[0], "")
	case len(parts) == 2:
		handleV1AnchoredDocument(a, w, r, parts[0], parts[1])
	case len(parts) == 3 && parts[2] == exportSegment:
		handleV1ExportDocument(a, w, r, parts[0], parts[1])
	default:
		writeV1Error(w, r, http.StatusNotFound, "not_found", "Resource not found", "")
	}
}

// handleV1AnchoredDocument serves GET /api/v1/_documents/{docName}/{entityId}
// — a document declared WITH an `entity_type:`, rendered about one entity.
//
// The gate chain lives in resolveAnchoredDocument, shared with the export
// route so the two cannot drift; its ordering is load-bearing and documented
// there. What stays HERE is everything specific to producing HTML: the
// return_to rewrite, the refresh flag, and the disk cache — see the note in
// resolveAnchoredDocument about why the cache must not move into it.
//
// A plain function taking *App for the same reason handleV1StandaloneDocument
// is one: App sits on its plimsoll load line.
func handleV1AnchoredDocument(a *App, w http.ResponseWriter, r *http.Request, docName, entityID string) {
	resolved, ok := resolveAnchoredDocument(a, w, r, docName, entityID)
	if !ok {
		return // the resolver already wrote the response
	}
	renderCfg := resolved.cfg

	// Check for refresh param - skip cache if present
	forceRefresh := r.URL.Query().Get("refresh") == "true"

	// return_to is the URL the caller is currently on. The rewriter uses
	// it to inject a `return_to` query param into form links so the form
	// redirects back here after submit. isSafeReturnPath enforces the
	// open-redirect guard — it rejects protocol-relative (//evil.com),
	// backslash-tricks (/\evil.com), and any absolute/schemed URLs, and
	// returns the normalised same-origin path on success.
	returnPath := isSafeReturnPath(r.URL.Query().Get("return_to"))

	// Try to get cached content (unless refresh requested). Disk cache
	// is only populated for command: renders (see doRender); skip the
	// read for script: docs so we don't serve a stale command:-era file
	// after a doc is switched to a Lua script.
	//
	// That condition is also what keeps GetCached safe: its key carries
	// neither the ConfigID nor the principal, so it may only ever serve a
	// principal-independent command: render. Never widen this to script:
	// docs, and never call GetCached from a per-principal path (RR-2QSGLU).
	if !forceRefresh && renderCfg.Script == "" {
		result := a.documents.GetCached(r.Context(), entityID)
		if result != nil {
			html := RewriteDocumentLinks(result.HTML, returnPath, nil)
			writeV1JSON(w, http.StatusOK, v1.DocumentResponse{
				HTML:      html,
				Cached:    true,
				EntityIDs: extractEntityIDs(result.Entities),
			})
			return
		}
	}

	// Render the document
	result, err := a.documents.Render(r.Context(), entityID, renderCfg)
	if err != nil {
		var se *lua.ScriptError
		if errors.As(err, &se) {
			correlationID := newCorrelationID()
			slog.Warn("document render failed",
				"document", docName, "entity", entityID,
				"correlation", correlationID, "error", err)
			writeV1ScriptError(w, se, a.allowFullScriptDetail(r), correlationID)
			return
		}
		writeV1Error(w, r, http.StatusInternalServerError, "render_failed", "Document rendering failed", err.Error())
		return
	}

	html := RewriteDocumentLinks(result.HTML, returnPath, nil)
	writeV1JSON(w, http.StatusOK, v1.DocumentResponse{
		HTML:      html,
		Cached:    false,
		EntityIDs: extractEntityIDs(result.Entities),
	})
}

// extractEntityIDs extracts IDs from a slice of entities.
func extractEntityIDs(entities []*entityPkg.Entity) []string {
	ids := make([]string, len(entities))
	for i, e := range entities {
		ids[i] = e.ID
	}
	return ids
}

// --- Commands API ---

// handleV1Commands returns available commands for a given page context.
// Query params:
//   - page_type: "entity", "list", "view", or "dashboard"
//   - qualifier: specific list ID or view ID (optional)
//   - entity_type: the entity type shown on the page (optional)
func (a *App) handleV1Commands(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}

	query := r.URL.Query()
	pageType := query.Get("page_type")
	qualifier := query.Get("qualifier")
	entityType := query.Get("entity_type")

	resolved := a.commands.resolveCommands(r.Context(), pageType, qualifier, entityType)

	commands := make([]v1.Command, 0, len(resolved))
	for _, cmd := range resolved {
		commands = append(commands, v1.Command(cmd))
	}

	writeV1JSON(w, http.StatusOK, commands)
}

// handleV1Templates returns templates for an entity type.
// GET /api/v1/_templates/{entityType}
func (a *App) handleV1Templates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	} // Extract entity type from path: /api/v1/_templates/{entityType}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/_templates/")
	entityType := strings.TrimSuffix(path, "/")

	if entityType == "" {
		writeV1Error(w, r, http.StatusBadRequest, "missing_entity_type", "Entity type is required", "")
		return
	}

	// Check if entity type exists
	if _, ok := a.State().Meta.Entities[entityType]; !ok {
		writeV1Error(w, r, http.StatusNotFound, "entity_type_not_found",
			fmt.Sprintf("Entity type '%s' not found", entityType), "")
		return
	}

	templates, _ := a.templater.EntityTemplates(r.Context(), entityType)
	result := make([]v1.Template, 0, len(templates))

	for _, t := range templates {
		relations := make([]v1.TemplateRelation, 0, len(t.Relations))
		for _, rel := range t.Relations {
			relations = append(relations, v1.TemplateRelation{
				Relation: rel.Type,
				Target:   rel.Target,
			})
		}
		result = append(result, v1.Template{
			Name:       t.Name,
			Properties: t.Properties,
			Content:    t.Content,
			Relations:  relations,
		})
	}

	writeV1JSON(w, http.StatusOK, result)
}

// --- OpenAPI Spec ---

// handleV1OpenAPI serves the OpenAPI 3.1 specification.
func (a *App) handleV1OpenAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}

	data, err := a.State().OpenAPIGen.GenerateJSON()
	if err != nil {
		writeV1Error(w, r, http.StatusInternalServerError, "generation_failed", "Failed to generate OpenAPI spec", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	_, _ = w.Write(data)
}

// --- Views API ---

// sectionEntityToV1 lifts a section's row entity (template-side data)
// onto the wire shape. Centralizes the `v1.ViewEntity` construction so
// the typed `_props` and per-row `_fields` (TKT-IHC7D) stay consistent
// across both the top-level entities path and the (currently dormant)
// grouped-card entities path.
func sectionEntityToV1(e SectionEntityData) v1.ViewEntity {
	v1Ent := v1.ViewEntity{
		ID:         e.ID,
		Title:      e.Title,
		Type:       e.Type,
		EditFormID: e.EditFormID,
		Content:    e.Content,
		HasContent: e.HasContent,
		Props:      e.Props,
	}
	for _, f := range e.Fields {
		v1Ent.Fields = append(v1Ent.Fields, v1.SectionField(f))
	}
	if e.FieldVerdicts != nil {
		fa := e.FieldVerdicts
		v1Ent.FieldAffordances = &fa
	}
	v1Ent.World = e.World
	v1Ent.Self = e.Self
	return v1Ent
}
