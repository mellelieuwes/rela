package dataentry

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/filter"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/search"
	"github.com/Sourcehaven-BV/rela/internal/search/searchparser"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// queryService owns the search-query pipeline: the `/_search` and
// `_position`-scope entry point (executeQuery), its free-text branch, the
// list endpoint's `?q=` id-set helper, and the metamodel-aware sort and
// property-filter passes those share. Extracted from App (TKT-SJ0LRS, part
// of the TKT-R68TV8 decomposition arc).
//
// Consolidating them here is the point of the extraction: the read handlers,
// the next-action engine (nextaction.go) and the `_position` scope resolver
// (scope.go) now share ONE collaborator instead of each reaching back into
// App for the same closures.
//
// Collaborator rationale differs from the sibling handlers in one respect
// that is load-bearing. The ACL-scoped searcher and the affordance service
// are held as CLOSURES rather than values, because tests reassign
// `app.visibleSearcher` (see rebindVisibleSearcher) and `app.affordances`
// after construction; a captured value would silently keep the searcher from
// construction time and a test injecting a recording/denying searcher would
// exercise the wrong one.
//
// The searcher seam is deliberately [search.VisibleSearcher], never a plain
// search.Searcher: executeQuery's free-text branch must only ever see hits
// the request principal may read (TKT-BA8BSX). This type also holds NO
// store.Store FIELD — it never carries an independent store handle, so a
// read cannot bypass the request-scoped bundle. The store reads it does
// perform arrive through the Services bundle passed in per call, whose
// gating is applied by the free functions in helpers.go
// (visibleListByTypes / visibleEntitiesOfType) against the resolved scope.
type queryService struct {
	// schema is the reloadable snapshot; the sort and property-filter passes
	// resolve entity definitions against it per call so a metamodel reload
	// propagates.
	schema func() *Schema
	// services returns the read bundle (store + searcher + metamodel).
	services func() Services
	// visibleSearcher is the ACL-scoped search seam. Late-bound: tests
	// reassign App.visibleSearcher after construction.
	visibleSearcher func() search.VisibleSearcher
	// affordances supplies the per-hit field-verdict source used to drop
	// hits that matched only a `visible:`-hidden property. Late-bound for
	// the same reason as visibleSearcher.
	affordances func() affordanceService
}

// newQueryService wires the leaf over the App's current collaborators.
// Called from NewApp and from the test rebind path.
func newQueryService(app *App) *queryService {
	return &queryService{
		schema:          app.State,
		services:        app.Services,
		visibleSearcher: func() search.VisibleSearcher { return app.visibleSearcher },
		affordances:     func() affordanceService { return app.affordances },
	}
}

// executeQuery parses a search query and returns all matching entities.
// It supports the same query syntax as the search page: type:, prop:, status:,
// and free text. Free-text words use OR logic with fuzzy matching via Bleve;
// results are ranked by score.
//
// It runs the search-view pipeline under the ctx principal's
// read scope (TKT-BA8BSX). Every consumer — handleV1Search, the
// _position search scope (resolveScope), and the next-action engine —
// inherits the gate from here, so no future consumer can run an ungated
// search by accident.
//
// Ordering of the gate: the scope resolves FIRST, and an
// all-effective-DenyAll scope returns before any backend work — a
// denied principal must not be able to probe search-backend latency
// (RR-X56H pattern, pinned with a recording searcher in
// acl_search_test.go). The free-text branch then runs through
// search.VisibleSearcher so hidden hits never have their bodies
// loaded; the type-listing branch resolves the per-type verdict
// against the store directly.
//
// The maxFreeTextSearchResults bound counts entities that survived
// BOTH visibility and property filters (post-visibility truncation —
// a pre-visibility cap starves restricted principals; a pre-filter
// cap would starve filtered queries the same way).
//
// Errors: visibility-scope failures wrap errACLListQuery (mapped by
// writeGateError: cancel-silent / 504 / 500 acl_query_failed with
// constant detail), store-load failures wrap errListLoad, and plain
// search-backend failures pass through (500 search_failed). The
// pre-TKT-BA8BSX version swallowed both error classes into silently
// truncated results.
func (q *queryService) executeQuery(ctx context.Context, query string) ([]*entity.Entity, error) {
	return q.executeQueryPrefiltered(ctx, query, nil)
}

// executeQueryPrefiltered is executeQuery with extra store predicates ANDed
// into the no-free-text branch's pushdown.
//
// `extra` is for a caller that will apply a FURTHER authoritative filter of
// its own over the result — the next-action engine's `condition:` — and has
// lowered the store-safe part of it (queryplan.ConditionPrefilters). The
// contract is the same belt-and-braces one as the query's own pushdown: the
// predicates may only ever remove rows the caller's Go pass would also
// remove, so the outcome is identical to fetching everything, minus the I/O.
// Nothing here evaluates them; a caller that passes a predicate it does not
// also enforce has widened nothing but has narrowed on its own authority.
//
// They are ignored on the free-text branch, which runs through the search
// index rather than a type-scoped store list. That branch is also
// relevance-CAPPED (maxFreeTextSearchResults) before any caller's pass runs,
// so a selection predicate over it would be lossy; the only caller with a
// condition — the next-action engine — is refused a free-text query at load
// (conditionlint) for exactly that reason, and this comment is the reminder
// for the next one.
func (q *queryService) executeQueryPrefiltered(
	ctx context.Context, query string, extra []store.PropPredicate,
) ([]*entity.Entity, error) {
	sq := searchparser.ParseQuery(query)
	if sq.IsEmpty() {
		return nil, nil
	}
	// A denied handle carries the ZERO scope, which IS the default world, so
	// both branches below would otherwise search everything under a world the
	// principal may not read. Same guard as freeTextIDsForType.
	if worldFromContext(ctx).blocksAllReads() {
		return []*entity.Entity{}, nil
	}

	svc := q.services()
	typeNames := make([]string, 0, len(svc.Meta.Entities))
	for name := range svc.Meta.Entities {
		typeNames = append(typeNames, name)
	}
	slices.Sort(typeNames)
	scope := readGateFromContext(ctx).SearchScope(ctx, typeNames)
	if len(scope) == 0 {
		return []*entity.Entity{}, nil
	}

	var candidates []*entity.Entity
	var err error
	if sq.HasFreeText() {
		// Hits arrive in relevance order. Scores are dropped because
		// executeQuery never sorted by them.
		candidates, err = q.runVisibleFreeTextSearch(ctx, svc, sq, scope)
	} else {
		// Push the equality filters into the store as a PRE-FILTER, cutting
		// the rows loaded. The Go pass below still evaluates every filter,
		// including the pushed ones, and remains authoritative.
		//
		// That belt-and-braces is deliberate rather than redundant.
		// store.PropPredicate compares by STRING FORM; filter.MatchAll is
		// metamodel-aware. On a typed property they disagree — `count!=03`
		// against an integer 3 is a non-match typed and a match as strings,
		// and an enum filter naming an undeclared value ERRORS in Go
		// (surfacing the operator's typo) while the store silently returns
		// nothing. Dropping a pushed filter from the Go pass would let the
		// looser of the two decide, WIDENING results on a path /_search and
		// scope navigation share.
		//
		// Keeping both makes the outcome provably identical to the
		// pre-pushdown behavior — the store can only ever remove rows the Go
		// pass would also have removed — while still winning the I/O.
		pushed := pushdownPrefilters(sq.PropertyFilters, svc.Meta, sq.EntityTypes)
		pushed = append(pushed, extra...)
		candidates, err = visibleListByTypes(ctx, svc, sq.EntityTypes, scope, pushed)
	}
	if err != nil {
		return nil, err
	}

	results := make([]*entity.Entity, 0, len(candidates))
	for _, e := range candidates {
		if !q.matchesPropertyFilters(e, sq.PropertyFilters) {
			continue
		}
		results = append(results, e)
		if sq.HasFreeText() && len(results) >= maxFreeTextSearchResults {
			break
		}
	}

	// Apply sort from query syntax (free-text results are already ranked by relevance)
	if sq.HasSort() {
		q.sortEntitiesMulti(results, sq.SortClauses)
	}

	return results, nil
}

// runVisibleFreeTextSearch is executeQuery's free-text branch: the
// same phrase re-quoting as runFreeTextSearchE, routed through the
// ACL-scoped searcher. The backend-side limit is only set when no
// property filters remain — with Go-side filters pending, truncation
// happens in executeQuery after them, or the filter gap would re-open
// the starvation the post-visibility limit closes.
func (q *queryService) runVisibleFreeTextSearch(
	ctx context.Context, svc Services, sq *searchparser.SearchQuery, scope map[string]search.TypeScope,
) ([]*entity.Entity, error) {
	parts := make([]string, 0, len(sq.FreeTextWords)+len(sq.FreeTextPhrases))
	parts = append(parts, sq.FreeTextWords...)
	for _, p := range sq.FreeTextPhrases {
		parts = append(parts, `"`+p+`"`)
	}
	limit := 0
	if len(sq.PropertyFilters) == 0 {
		limit = maxFreeTextSearchResults
	}
	sQuery := search.Query{
		Text:  strings.Join(parts, " "),
		Types: sq.EntityTypes,
		Limit: limit,
		World: worldScopeFrom(ctx),
	}
	var hits []search.Hit
	for hit, err := range searchVisibleHits(ctx, q.visibleSearcher(), q.affordances(), sQuery, scope) {
		if err != nil {
			if errors.Is(err, search.ErrScope) {
				return nil, fmt.Errorf("%w: %w", errACLListQuery, err)
			}
			return nil, fmt.Errorf("free-text search: %w", err)
		}
		hits = append(hits, hit)
	}
	// Load the FACE each hit matched, not the bare id, in ONE read per
	// distinct face rather than one per hit (TKT-1U8XYN). Under the default
	// world every hit carries the zero face and this is one query. The
	// searcher already resolved which face matched; a bare-id re-read would
	// render default-face bytes for a hit scored against another face.
	loaded, err := loadHitHeaders(ctx, svc.Store, hits)
	if err != nil {
		return nil, fmt.Errorf("free-text search: %w", err)
	}
	// Emit in ranked hit order; a hit the store no longer has (deleted
	// between index query and read) is skipped so the result stays a
	// coherent set of currently-existing entities.
	out := make([]*entity.Entity, 0, len(hits))
	for _, hit := range hits {
		if e, ok := loaded[rowKey{hit.ID, hit.Face}]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}

// freeTextIDsForType runs a free-text search constrained to the given entity
// type and returns the matching ids. Empty / whitespace queries (and queries
// like `prop:status=open` that have no free-text words) return HasFilter=false
// so the caller skips intersection entirely. Searcher errors are surfaced —
// the list handler converts them to HTTP 500 rather than rendering an empty
// list and pretending the search succeeded.
//
// Used by the list endpoint to support `?q=` without going through the full
// executeQuery path: a list is already type-scoped, so any `type:` token from
// the query string is intentionally ignored — we always pin the type to the
// list's type to keep the surface predictable.
//
// The search is WORLD-SCOPED off ctx, and that is what makes `?q=` safe to
// combine with `?world=` at all (TKT-9KZGJO step 5). The scope decides which
// FACE of each entity the text is matched against, so a `published`-world
// search never matches on draft-only text; an entity the world excludes
// resolves to no face and cannot appear. A denied world yields nothing here
// for the same reason the list itself does — see the blocksAllReads guard.
//
// attachWorld refused `?world=` combined with `?q=` outright
// (`world_search_unsupported`) until that step landed. Do not restore that
// refusal without also reverting this threading: a bare refusal would leave the
// per-world index built and unreachable.
func (q *queryService) freeTextIDsForType(
	ctx context.Context, query, typeName string,
) (freeTextIDsForTypeResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return freeTextIDsForTypeResult{}, nil
	}
	sq := searchparser.ParseQuery(query)
	if sq.IsEmpty() || !sq.HasFreeText() {
		return freeTextIDsForTypeResult{}, nil
	}
	sq.EntityTypes = []string{typeName}

	// A denied world must find NOTHING, and saying so here rather than relying
	// on the caller is deliberate. scopedSortedEntities returns early on a
	// denied handle today, so this is unreachable through it — but a denied
	// handle carries the ZERO scope (resolveWorld never built one), and a zero
	// scope IS the default world. Any future caller reaching this without the
	// early return would therefore get a full default-world search under a
	// world the principal may not read. Fail closed at the seam that would leak.
	if worldFromContext(ctx).blocksAllReads() {
		return freeTextIDsForTypeResult{IDs: nil, HasFilter: true}, nil
	}

	ids, err := freeTextIDs(ctx, q.services(), sq, worldScopeFrom(ctx), maxFreeTextSearchResults)
	if err != nil {
		return freeTextIDsForTypeResult{}, err
	}
	return freeTextIDsForTypeResult{IDs: ids, HasFilter: true}, nil
}

// sortEntitiesMulti orders search results by the shared query-sort rule, so a
// `sort:` clause in the search bar and the same key on a list page agree
// (TKT-9OFGH4).
//
// [filter.QuerySort] rather than [filter.SortMulti]: the two differed on
// strings, dates, ids and the meaning of descending, and a search result and a
// list page showing the same rows in different orders is the defect. The rule
// is whatever the store can express, since only that can be pushed down.
func (q *queryService) sortEntitiesMulti(entities []*entity.Entity, specs []filter.SortSpec) {
	if len(specs) == 0 {
		return
	}
	s := q.schema()
	entityDefs := make(map[string]*metamodel.EntityDef)
	for _, e := range entities {
		if _, ok := entityDefs[e.Type]; !ok {
			if def, ok := s.Meta.GetEntityDef(e.Type); ok {
				entityDefs[e.Type] = def
			}
		}
	}
	filter.QuerySortApply(filter.NewQuerySort(specs, entityDefs, s.Meta), entities, entityRecord, specs)
}

// matchesPropertyFilters checks whether an entity matches the given property
// filters. Returns true if no filters are specified or all filters match.
func (q *queryService) matchesPropertyFilters(e *entity.Entity, filters []*filter.Filter) bool {
	if len(filters) == 0 {
		return true
	}
	s := q.schema()
	entDef, ok := s.Meta.GetEntityDef(e.Type)
	if !ok {
		return false
	}
	matched, err := filter.MatchAll(entityRecord(e), filters, entDef, s.Meta)
	return err == nil && matched
}

// loadHitHeaders reads the content-free rows the hits name, grouped by
// face: default-face hits in one IDs query, each other face in one
// AllStates query narrowed to that face on the way out. Returns only rows
// that exist, as content-free entities (see rowcontent.go).
func loadHitHeaders(ctx context.Context, st store.Store, hits []search.Hit) (map[rowKey]*entity.Entity, error) {
	byFace := make(map[entity.Face][]string)
	seen := make(map[rowKey]struct{}, len(hits))
	for _, h := range hits {
		k := rowKey{h.ID, h.Face}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		byFace[h.Face] = append(byFace[h.Face], h.ID)
	}
	out := make(map[rowKey]*entity.Entity, len(seen))
	for face, ids := range byFace {
		q := store.EntityQuery{IDs: ids, AllStates: !face.IsDefault()}
		for h, err := range store.ListEntityHeaders(ctx, st, q) {
			if err != nil {
				return nil, err
			}
			if h.Face != face {
				continue // AllStates over-returns the family's other faces
			}
			out[rowKey{h.ID, h.Face}] = headerEntity(h)
		}
	}
	return out, nil
}
