package dataentry

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"html/template"
	"iter"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/filter"
	"github.com/Sourcehaven-BV/rela/internal/htmlutil"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/queryplan"
	"github.com/Sourcehaven-BV/rela/internal/search"
	"github.com/Sourcehaven-BV/rela/internal/search/searchparser"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// nowFunc is the clock used for filter variable substitution. Tests can
// override this to pin a deterministic "now". Default returns UTC time
// to keep $today consistent regardless of server timezone.
var nowFunc = func() time.Time { return time.Now().UTC() }

// resolveFilterVariable substitutes special variable references in filter
// values. Currently supports:
//
//	$today          today's date in YYYY-MM-DD format (UTC)
//	$tomorrow       tomorrow's date (UTC)
//	$yesterday      yesterday's date (UTC)
//
// Times are evaluated in UTC for predictability across server timezones.
// Other values are returned unchanged.
func resolveFilterVariable(value string) string {
	switch value {
	case "$today":
		return nowFunc().Format("2006-01-02")
	case "$tomorrow":
		return nowFunc().AddDate(0, 0, 1).Format("2006-01-02")
	case "$yesterday":
		return nowFunc().AddDate(0, 0, -1).Format("2006-01-02")
	}
	return value
}

// resolveFilterVariablesInList applies resolveFilterVariable to each
// comma-separated token in value. Used by the in/ne operators.
func resolveFilterVariablesInList(value string) string {
	if !strings.Contains(value, ",") {
		return resolveFilterVariable(value)
	}
	parts := strings.Split(value, ",")
	for i, p := range parts {
		parts[i] = resolveFilterVariable(strings.TrimSpace(p))
	}
	return strings.Join(parts, ",")
}

// TODO(TKT-HFEKVN): this list is the THIRD copy of the same layout set —
// metamodel.ParseDateValue and predicate.parseDateLiteral hold the other two,
// and their comments say they mirror each other by hand. They already disagree:
// this one accepts minute precision and ignores a property's declared format:
// (compareValues gets two bare strings and has no PropertyDef in scope), so a
// custom-format date compares correctly under --filter and incorrectly here.
//
// temporalLayouts are the layouts a filter value may use, tried in order.
// A bare date is listed first so it keeps winning for date-typed properties;
// the datetime layouts exist because a `datetime` property stores RFC3339 and
// comparing it against a window bound previously fell through to lexicographic
// string comparison (or errored against a bare-date bound), which silently
// excluded every entity — see TKT-IG54YO.
var temporalLayouts = []string{
	"2006-01-02",
	time.RFC3339,
	"2006-01-02T15:04:05Z",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
}

// parseTemporal parses value as a date or datetime, returning the instant it
// denotes. A bare date denotes midnight UTC, so a bare-date bound compared
// against a datetime is a start-of-day bound — callers wanting an inclusive
// upper bound should use a half-open range against the next day rather than
// relying on end-of-day semantics.
//
// Nil: never returns a nil time — ok=false means the value is not temporal.
func parseTemporal(value string) (t time.Time, ok bool) {
	for _, layout := range temporalLayouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// compareValues compares two values using the given comparison operator
// (lt, lte, gt, gte). It uses strict same-type comparison: if both sides
// parse as temporal values (date or datetime), they are compared as instants;
// if both parse as numbers, numbers are compared; otherwise strings are
// compared lexicographically.
//
// Date and datetime interoperate deliberately: a bare-date bound against an
// RFC3339 property value is a legitimate comparison (midnight on that day), and
// treating it as a type mismatch is what made date-windowed queries over
// datetime properties return nothing. Mixed offsets compare correctly because
// both sides are normalized to an instant rather than compared as strings.
//
// On a type mismatch (e.g. left is a temporal string, right is not), the
// comparison returns false and a non-nil error so callers can decide
// whether to log/reject. This prevents the silent lexicographic-fallback
// trap where "2026-04-07" < "tomorrow" returned true.
func compareValues(left, right, operator string) (match bool, err error) {
	// Both sides parse as dates/datetimes → compare as instants
	lt, lIsTime := parseTemporal(left)
	rt, rIsTime := parseTemporal(right)
	switch {
	case lIsTime && rIsTime:
		// time.Compare, not UnixNano: nanoseconds since the epoch overflow an
		// int64 outside roughly 1678-2262, and a far-future date would wrap
		// negative and sort before everything.
		return compareOrdered(lt.Compare(rt), 0, operator), nil
	case lIsTime || rIsTime:
		// One side is temporal, the other isn't — refuse to guess.
		return false, fmt.Errorf("cannot compare date %q with non-date %q",
			pickDate(left, right, lIsTime), pickNonDate(left, right, lIsTime))
	}

	// Both sides parse as numbers → compare as numbers
	lf, lNumErr := strconv.ParseFloat(left, 64)
	rf, rNumErr := strconv.ParseFloat(right, 64)
	switch {
	case lNumErr == nil && rNumErr == nil:
		return compareOrdered(lf, rf, operator), nil
	case lNumErr == nil || rNumErr == nil:
		// One side is numeric, the other isn't — refuse to guess.
		return false, fmt.Errorf("cannot compare number %q with non-number %q",
			pickNumber(left, right, lNumErr == nil), pickNonNumber(left, right, lNumErr == nil))
	}

	// Neither parses as date or number → string comparison
	return compareOrdered(left, right, operator), nil
}

// pickDate returns the side that successfully parsed as a date.
func pickDate(left, right string, leftIsDate bool) string {
	if leftIsDate {
		return left
	}
	return right
}

// pickNonDate returns the side that did NOT parse as a date.
func pickNonDate(left, right string, leftIsDate bool) string {
	if leftIsDate {
		return right
	}
	return left
}

// pickNumber / pickNonNumber are the numeric equivalents.
func pickNumber(left, right string, leftIsNum bool) string {
	if leftIsNum {
		return left
	}
	return right
}

func pickNonNumber(left, right string, leftIsNum bool) string {
	if leftIsNum {
		return right
	}
	return left
}

// compareOrdered applies an ordering operator to two ordered values.
// Returns false for unknown operators (caller is expected to validate).
func compareOrdered[T cmp.Ordered](left, right T, operator string) bool {
	c := cmp.Compare(left, right)
	switch operator {
	case "lt":
		return c < 0
	case "lte":
		return c <= 0
	case "gt":
		return c > 0
	case "gte":
		return c >= 0
	}
	return false
}

// propertyContains checks if a property value contains the given string.
// Handles string, []string, and []interface{} property types.
func propertyContains(prop any, value string) bool {
	return anyElement(prop, func(el string) bool { return el == value })
}

// propertyIsEmpty checks if a property value is empty/nil.
func propertyIsEmpty(prop any) bool {
	if prop == nil {
		return true
	}
	switch v := prop.(type) {
	case string:
		return v == ""
	case []string:
		return len(v) == 0
	case []any:
		return len(v) == 0
	default:
		return false
	}
}

// applyFilters filters entities by a set of filter conditions.
//
//nolint:gocognit // applies each filter operator against each entity property; the branches are per-operator match semantics, not shared logic to extract.
func applyFilters(entities []*entity.Entity, filters []FilterConfig) []*entity.Entity {
	if len(filters) == 0 {
		return entities
	}
	var result []*entity.Entity
	for _, e := range entities {
		match := true
		for _, f := range filters {
			if strings.HasPrefix(f.Value, "$") {
				continue // skip variable substitution
			}
			prop := e.Properties[f.Property]
			switch f.Operator {
			case "=":
				if f.Value == "" {
					if !propertyIsEmpty(prop) {
						match = false
					}
				} else if !propertyContains(prop, f.Value) {
					match = false
				}
			case "!=":
				if f.Value == "" {
					if propertyIsEmpty(prop) {
						match = false
					}
				} else if propertyContains(prop, f.Value) {
					match = false
				}
			}
		}
		if match {
			result = append(result, e)
		}
	}
	return result
}

// resolvePropertyValues returns allowed values for a property from its definition or custom type.
func resolvePropertyValues(prop metamodel.PropertyDef, meta *metamodel.Metamodel) []string {
	if len(prop.Values) > 0 {
		return prop.Values
	}
	if ct, ok := meta.Types[prop.Type]; ok {
		return ct.Values
	}
	return nil
}

// resolveWidget returns the appropriate widget type for a property.
func resolveWidget(prop metamodel.PropertyDef, meta *metamodel.Metamodel) string {
	// Check if property is a list (multi-select) - only applies to enum types
	_, isCustomType := meta.Types[prop.Type]
	isEnum := prop.Type == metamodel.PropertyTypeEnum || isCustomType
	if prop.List && isEnum {
		return WidgetMultiSelect
	}

	return meta.ResolveWidgetFromType(prop.Type)
}

// coalesce returns the first non-empty string.
func coalesce(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// containsString returns true if slice contains the given string.
func containsString(slice []string, s string) bool {
	return slices.Contains(slice, s)
}

// slugify converts a string to a URL-safe slug (lowercase, hyphens, no special chars).
func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prev := byte('-')
	for i := range len(s) {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteByte(c)
			prev = c
		} else if prev != '-' {
			b.WriteByte('-')
			prev = '-'
		}
	}
	return strings.Trim(b.String(), "-")
}

// resolvePropertyType returns the metamodel type name for a property on an entity type.
func resolvePropertyType(prop, entityType string, meta *metamodel.Metamodel) string {
	entDef, ok := meta.GetEntityDef(entityType)
	if !ok {
		return ""
	}
	propDef, ok := entDef.Properties[prop]
	if !ok {
		return ""
	}
	return propDef.Type
}

// mdConverter is the goldmark instance with GFM extensions (tables, task lists, etc.).
var mdConverter = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(html.WithUnsafe()),
)

// simpleMarkdownToHTML converts markdown to HTML using goldmark with GFM extensions.
func simpleMarkdownToHTML(md string) template.HTML {
	if md == "" {
		return ""
	}

	var buf bytes.Buffer
	if err := mdConverter.Convert([]byte(md), &buf); err != nil {
		//nolint:gosec // fallback to escaped input on conversion error
		return template.HTML(template.HTMLEscapeString(md))
	}

	result := buf.String()

	// Post-process: add md-table class to tables
	result = strings.ReplaceAll(result, "<table>", `<table class="md-table">`)

	// Post-process: convert diagram code blocks (mermaid, plantuml)
	result = htmlutil.ConvertDiagramBlocks(result)

	// Post-process: add checkbox indices for interactive toggling
	result = addCheckboxIndices(result)

	//nolint:gosec // HTML is generated by goldmark from user markdown
	return template.HTML(result)
}

var checkboxRe = regexp.MustCompile(`<input[^>]*type="checkbox"[^>]*>`)

func addCheckboxIndices(s string) string {
	idx := 0
	return checkboxRe.ReplaceAllStringFunc(s, func(match string) string {
		// Add data-cb-idx attribute
		result := strings.Replace(match, "<input", fmt.Sprintf(`<input data-cb-idx="%d"`, idx), 1)
		idx++
		return result
	})
}

// searchVisibleHits runs the ACL-scoped search with property-level redaction
// when the wired searcher supports it ([search.FieldVisibleSearcher]), so a hit
// that matched only a `visible:`-hidden property is dropped rather than
// confirming that property's value by its presence (the match-on-hidden-field
// oracle, TKT-GGQ0JT). If the searcher is entity-level only, it degrades to
// SearchVisible — no field redaction, matching pre-existing behavior.
//
// Free function (not an App method) to keep App's method surface under the
// god-object load line; it takes only the two collaborators it needs.
//
// The field filter is engaged only when the resolver can actually hide a
// property (a policy-backed resolver); under the Nop resolver redaction is a
// provable no-op, so the plain entity-level path runs — and a searcher that
// isn't a FieldVisibleSearcher is fine there.
//
// When redaction IS in play but the searcher cannot do it, this FAILS CLOSED:
// it yields search.ErrScope rather than falling back to the un-redacted
// SearchVisible. A searcher that reaches here without implementing
// FieldVisibleSearcher is a wiring bug — a decorator (cache, metrics, tracing)
// wrapped around the searcher without forwarding the method — and the failure
// it would otherwise produce is silent: search stops redacting while the policy
// still hides, with no error and no log.
//
// This mirrors search.Visible.SearchVisibleFields one layer down, whose godoc
// settles the principle: "silently skipping redaction is exactly the oracle
// this closes" (RR-8W40EW). The two decision points are the same shape; the
// outer one was the one still failing open (TKT-NCLA67).
func searchVisibleHits(
	ctx context.Context, vs search.VisibleSearcher, aff affordanceService,
	q search.Query, scope map[string]search.TypeScope,
) iter.Seq2[search.Hit, error] {
	return faceGatedHits(ctx, searchScopedHits(ctx, vs, aff, q, scope))
}

// faceGatedHits drops hits whose matched face a `type@face` grant withholds.
// The search scope carries the row verdict only (search.TypeScope has no face
// set), so a `ticket@published` principal otherwise gets draft-face hits with
// draft titles from a search the entity GET would 404. Applied at the edge
// rather than pushed into every searcher: it is one predicate over a field the
// hit already carries.
func faceGatedHits(ctx context.Context, hits iter.Seq2[search.Hit, error]) iter.Seq2[search.Hit, error] {
	return func(yield func(search.Hit, error) bool) {
		for h, err := range hits {
			if err == nil && !faceReadable(ctx, h.Type, h.Face) {
				continue
			}
			if !yield(h, err) {
				return
			}
		}
	}
}

func searchScopedHits(
	ctx context.Context, vs search.VisibleSearcher, aff affordanceService,
	q search.Query, scope map[string]search.TypeScope,
) iter.Seq2[search.Hit, error] {
	// Nothing to redact: the plain entity-level path is correct, and a searcher
	// that isn't a FieldVisibleSearcher is fine here.
	if !aff.hidesAnyField() {
		return vs.SearchVisible(ctx, q, scope)
	}
	fvs, ok := vs.(search.FieldVisibleSearcher)
	if !ok {
		// Redaction is in play but this searcher cannot do it. Refuse rather
		// than serve un-redacted hits.
		return func(yield func(search.Hit, error) bool) {
			yield(search.Hit{}, fmt.Errorf(
				"%w: policy hides fields but the wired searcher (%T) cannot redact them",
				search.ErrScope, vs))
		}
	}
	return fvs.SearchVisibleFields(ctx, q, scope, hiddenSearchFields(aff))
}

// hiddenSearchFields builds the [search.HiddenFieldsFunc] that reports, per
// hit, the property fields the request principal may not see — resolved through
// the same affordance verdict source the serializer uses, then qualified into
// the search field vocabulary (`prop:<name>`). The seam passes the already-
// loaded entity, so the resolver's `when:` predicates evaluate against the same
// snapshot the provenance check uses (no second load, no cross-snapshot race).
func hiddenSearchFields(aff affordanceService) search.HiddenFieldsFunc {
	return func(ctx context.Context, _ search.Hit, e *entity.Entity) (map[string]struct{}, error) {
		hidden := aff.hiddenProperties(ctx, e)
		if len(hidden) == 0 {
			return nil, nil
		}
		out := make(map[string]struct{}, len(hidden))
		for name := range hidden {
			out[search.PropFieldPrefix+name] = struct{}{}
		}
		return out, nil
	}
}

// visibleListByTypes is executeQuery's no-free-text branch: load
// entities by type under the per-type scope verdict. Unlike
// listFromStoreByTypes (which other, ungated consumers still use and
// which swallows iterator errors), this fails loud on both verdict
// paths — same rationale as scopedSortedEntities.
// visibleListByTypes loads the visible entities of the given types, applying
// `props` in the STORE rather than after the fact.
//
// The predicates compose with the ACL scope rather than replacing it: an
// AllowAll type gets a plain type+props query, and a scope-restricted type
// gets its verdict query with the props appended. Both are ANDed by the
// store, so narrowing by property can never widen what the gate allows.
func visibleListByTypes(
	ctx context.Context, svc Services, types []string, scope map[string]search.TypeScope,
	props []store.PropPredicate,
) ([]*entity.Entity, error) {
	if len(types) == 0 {
		if _, wildcard := scope[search.WildcardType]; wildcard {
			// Wildcard-allow (no ACL): every entity, any type — the
			// pre-ACL listAll shape, with iterator errors surfaced.
			// No type named and no ACL: every entity, any type. The
			// property predicates cannot be pushed here — GraphQuery is
			// per-type — so this one path keeps the Go-side filter, which
			// still runs over the result below.
			out := make([]*entity.Entity, 0)
			for e, err := range svc.Store.ListEntities(ctx, store.EntityQuery{World: worldScopeFrom(ctx)}) {
				if err != nil {
					return nil, fmt.Errorf("%w: %w", errListLoad, err)
				}
				out = append(out, e)
			}
			return out, nil
		}
		// Under ACL, "all types" means "all granted types":
		// deterministic order over the scope's entries.
		types = make([]string, 0, len(scope))
		for typ := range scope {
			types = append(types, typ)
		}
		slices.Sort(types)
	}

	var out []*entity.Entity
	for _, typ := range types {
		ts, ok := search.ResolveTypeScope(scope, typ)
		if !ok {
			continue // denied type
		}
		got, err := visibleEntitiesOfType(ctx, svc, typ, ts, props)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	return out, nil
}

// visibleEntitiesOfType loads one type's visible entities, applying the
// property predicates in the store.
//
// Three shapes, differing only in how the query is composed:
//
//   - AllowAll with no props: a plain type listing, byte-identical to the
//     pre-pushdown path.
//   - AllowAll with props: a type+props query rather than a full scan. The
//     error class stays errListLoad — no gate is involved here.
//   - Scope-restricted: the gate's own verdict query with the props appended,
//     ANDed by the store, so narrowing by property can never widen what the
//     gate allows. errACLListQuery, because a failure here IS a gate failure.
func visibleEntitiesOfType(
	ctx context.Context, svc Services, typ string,
	ts search.TypeScope, props []store.PropPredicate,
) ([]*entity.Entity, error) {
	// A search TypeScope is an ACL read verdict in a different shape: same
	// AllowAll/Query pair, minus DenyAll (ResolveTypeScope has already
	// denied by omission) and minus Faces. Restating it lets this path share
	// the one verdict switch (scopedread.go) rather than reimplementing it —
	// which is how the RR-GQWRLD world bug reached two sites.
	rqr := acl.ReadQueryResult{AllowAll: ts.AllowAll, Query: ts.Query}
	// The face allowlist comes from the gate directly, because TypeScope
	// cannot carry it. Without it a `type@face` grant is ignored here and the
	// world ranks withheld faces too, serving their titles (BUG-SMPOZB
	// review). It narrows the candidates before the world ranks them, as on
	// the list path.
	faces := readGateFromContext(ctx).ReadQuery(ctx, typ).Faces
	entities, _, err := scopedEntities(ctx, svc, rqr, scopeRequest{Type: typ, Props: props, Faces: faces})
	return entities, err
}

// freeTextIDsForTypeResult is what freeTextIDsForType returns: the set of
// matching ids when the searcher succeeded, plus a flag distinguishing
// "empty / non-free-text query, skip intersection" from "real result."
type freeTextIDsForTypeResult struct {
	IDs       map[string]struct{}
	HasFilter bool
}

// maxFreeTextSearchResults caps the number of hits the searcher is asked to
// return. Hoisted to a package constant so executeQuery and the list-search
// path stay in lockstep on the bound.
const maxFreeTextSearchResults = 1000

// freeTextIDs issues a Searcher query from a parsed SearchQuery under world
// scope w, and returns the matching entity IDs. Phrases are re-quoted so the
// searcher's text layer can rebuild the same fuzzy-words + exact-phrases
// compound query the dataentry UI used to build upstream. Backend failures
// surface to the caller.
//
// # IDs, not entities
//
// The one caller intersects these ids with a list it has ALREADY loaded
// (world-scoped and ACL-gated). Loading the entities here would be both wasted
// I/O and a live correctness hazard: the obvious load is `GetEntity(hit.ID)`,
// which addresses the entity's DEFAULT face and so hands back default-face
// bytes for a hit scored against the world's prime — the wrong-face serve this
// whole arc exists to prevent. Returning ids removes the opportunity rather
// than documenting it. (search.Service already loads the right face internally
// via GetEntityState; a consumer re-read here would only undo that.)
//
// Free function taking svc rather than an App method: it needs one
// collaborator, and App sits at its plimsoll method cap.
func freeTextIDs(
	ctx context.Context, svc Services, sq *searchparser.SearchQuery,
	w store.WorldScope, limit int,
) (map[string]struct{}, error) {
	parts := make([]string, 0, len(sq.FreeTextWords)+len(sq.FreeTextPhrases))
	parts = append(parts, sq.FreeTextWords...)
	for _, p := range sq.FreeTextPhrases {
		parts = append(parts, `"`+p+`"`)
	}
	q := search.Query{
		Text:  strings.Join(parts, " "),
		Types: sq.EntityTypes,
		Limit: limit,
		World: w,
	}
	ids := make(map[string]struct{})
	for hit, err := range svc.Searcher.Search(ctx, q) {
		if err != nil {
			return nil, fmt.Errorf("free-text search: %w", err)
		}
		ids[hit.ID] = struct{}{}
	}
	return ids, nil
}

// listFromStoreByTypes loads all entities matching the given types (or every
// entity when types is empty) from the store.
func listFromStoreByTypes(ctx context.Context, svc Services, types []string) []*entity.Entity {
	if len(types) == 0 {
		return listAllFromStore(ctx, svc)
	}
	var out []*entity.Entity
	for _, t := range types {
		for e, err := range svc.Store.ListEntities(ctx, store.EntityQuery{Type: t}) {
			if err != nil {
				return out
			}
			out = append(out, e)
		}
	}
	return out
}

// listAllFromStore drains every entity from the store.
//
// UNBOUNDED, and the same shape that made GET /api/v1/_analyze an OOM
// (TKT-1ESTYJ): whole entities, bodies included, retained in one slice. It
// is currently safe only because nothing reaches it with an empty type
// list — both listFromStoreByTypes callers pass exactly one type, and a
// list's entity_type is required and validated at load.
//
// So this is left as-is rather than migrated: the analyze fix removed the
// live O(store) retention, and rewriting a path with no reachable caller
// would be churn. If a caller ever DOES pass an empty type list, this
// becomes a live whole-store drain — at that point it wants
// store.ListEntityHeaders (if the caller reads no bodies) or paging, not a
// bigger slice.
func listAllFromStore(ctx context.Context, svc Services) []*entity.Entity {
	out := make([]*entity.Entity, 0)
	for e, err := range svc.Store.ListEntities(ctx, store.EntityQuery{}) {
		if err != nil {
			return out
		}
		out = append(out, e)
	}
	return out
}

// relationDirection maps the data-entry config direction type to the
// store's direction enum.
func relationDirection(d dataentryconfig.Direction) store.Direction {
	if d.IsIncoming() {
		return store.DirectionIncoming
	}
	return store.DirectionOutgoing
}

// pushdownPrefilters returns the store-evaluable subset of the property
// filters, as a PRE-FILTER only — the caller must still run every filter
// through the metamodel-aware Go pass.
//
// Only plain equality pushes down. Everything else is either unsupported by
// store.PropPredicate (ordered comparison, regex, fuzzy) or means something
// different there: a glob rides on OpEqual but would become a literal string
// comparison.
//
// NOT-equal is excluded even though PropPredicate supports it: on a typed
// property a string-form disagreement WIDENS (`flag!=yes` on a boolean errors
// in Go, excluding the row, and matches in the store), so it would admit rows
// the Go pass must then reject — no saving, and a bug in the pairing leaks them.
//
// TYPED properties are excluded entirely, in either direction. A pre-filter is
// only sound if it can never remove a row the authoritative pass would keep,
// and equality on a typed property breaks exactly that: `count=03` against an
// integer 3 matches numerically but not as strings, so pushing it would drop a
// row that belongs in the result. Only `string` and untyped-unknown properties
// compare identically both ways.
//
// Multi-type queries are handled by requiring EVERY named type to declare the
// property as string-comparable: one property name can be `string` on one type
// and `integer` on another, and the pushdown is applied per query, not per type.
// An unnamed-type query (no `type:`) pushes nothing, since any type could match.
//
// Emptiness agrees across both paths because internal/propmatch is the single
// definition backing store.PropPredicate AND internal/filter's empty handling;
// storetest's Props_value_shapes pins that agreement per backend.
func pushdownPrefilters(
	filters []*filter.Filter, meta *metamodel.Metamodel, types []string,
) []store.PropPredicate {
	return queryplan.PushdownPrefilters(filters, meta, types)
}

// propertyElements returns the property value as the list of elements a filter
// operator compares against: a single-element slice for a scalar, one entry per
// item for a list-typed property.
//
// This exists because a list property has no meaningful single string form. The
// v1 list filter used to compare `fmt.Sprintf("%v", prop)` against the filter
// value, which renders []any{"a","b"} as the Go slice literal "[a b]" — so a
// list property could never equal any selected value, and `ne` returned rows it
// was asked to exclude (BUG-AMK38R). Comparing per element is the same rule
// [propertyContains] already applies to static config-authored `filters:`, so
// the dynamic `filter[...]` path now agrees with the static one.
//
// Non-string elements render with `%v`, matching what internal/propmatch's
// Stringify does for a scalar, so a []any{1,2} behaves like the scalars 1 and 2
// rather than being dropped. (This does not CALL propmatch: arch-lint forbids
// dataentry depending on it. Keeping the rendering identical is what matters,
// and TKT-UTJ24Z is where the two converge for real.)
//
// Nil: a nil property yields a single empty-string element, so it compares
// equal to an empty filter value and unequal to any other. Falling through to
// `%v` instead would produce the Go literal "<nil>", which is a MATCHABLE
// string: `filter[x][contains]=nil` would then select every entity whose
// property is explicitly null.
func propertyElements(prop any) []string {
	if prop == nil {
		return []string{""}
	}
	switch v := prop.(type) {
	case string:
		return []string{v}
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
				continue
			}
			out = append(out, fmt.Sprintf("%v", item))
		}
		return out
	default:
		return []string{fmt.Sprintf("%v", prop)}
	}
}

// anyElement reports whether any element of the property satisfies match.
// Used by the ANY-element operators (eq, in, contains); `ne` is its negation,
// so "no element matches" stays the exact complement of "some element matches"
// and the two cannot drift apart.
func anyElement(prop any, match func(string) bool) bool {
	return slices.ContainsFunc(propertyElements(prop), match)
}

// filterSetValues expands the query params of an `in`/`ne` filter into the set
// of values it tests membership against.
//
// The two wire forms are read differently, on purpose:
//
//   - Without the `[]` suffix, each param splits on commas — the documented
//     `filter[x][in]=a,b` form.
//   - WITH the `[]` suffix (`filter[x][in][]=a&filter[x][in][]=b`), each param
//     is one member, taken verbatim.
//
// The suffix is the signal, not the number of params: a one-element array is
// indistinguishable from a comma list by count alone, and guessing from the
// count re-tears exactly the single-value case the array form exists to carry.
//
// That split is what lets the repeated form carry a value containing a comma
// ("Legal, Risk & Compliance"). Splitting it too would tear that value into two
// members matching rows that hold neither — and leave `ne` unable to exclude
// the row it names, the same wrong-answer class as BUG-AMK38R. The single-param
// comma form inherently cannot express such a value; callers with commas in
// their data must use the repeated form.
//
// Each member is trimmed and variable-resolved ($today and friends) exactly as
// the joined form is, so both representations of a filter agree.
func filterSetValues(values []string, isArrayForm bool) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if isArrayForm {
			out = append(out, resolveFilterVariable(strings.TrimSpace(v)))
			continue
		}
		for part := range strings.SplitSeq(v, ",") {
			out = append(out, resolveFilterVariable(strings.TrimSpace(part)))
		}
	}
	return out
}

// isListValue reports whether a property value is list-typed. Used to decline
// operators that have no defined meaning over a list rather than answering
// with a comparison against the rendered slice.
func isListValue(prop any) bool {
	switch prop.(type) {
	case []string, []any:
		return true
	}
	return false
}
