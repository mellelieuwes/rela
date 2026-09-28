package dataentry

import (
	"context"
	"fmt"
	"iter"
	"sort"
	"strings"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/lua"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/natsort"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/tracer"
	"github.com/Sourcehaven-BV/rela/internal/validator"
)

// analyzeReader is the narrow, consumer-side ENTITY-read surface the analyze
// checks need. It is satisfied structurally by store.Store AND by the ctx-gating
// visibility.ScriptReader / Unrestricted readers. Wiring a GATED reader here
// (per TKT-3FL2S6, superseding DEC-O59WM4) makes the whole-graph scans read only
// the requester's slice: a hidden entity produces no issue, and a visible
// entity comes back REDACTED, so its value cannot reach an issue title or a
// validation message. The leak closes by construction, not by filtering output.
//
// Only entity reads: analyze never mutates. Relation COUNTS (cardinality) go
// through a separate counter that stays on the raw store — a count is a
// structural fact, not a value, so it cannot leak; under-visibility only makes
// cardinality potentially false-positive, which is correctness (guarded by the
// roles annotation, arc step 2), not a disclosure.
// The whole-store scans go through ListEntityHeaders, never ListEntities:
// no analyze check reads an entity BODY (grep this file for `.Content` —
// there are none), so loading bodies to discard them made a scan's peak
// memory proportional to the project's markdown. On 20k entities with
// ~100 KB bodies that was ~2.9 GB of RSS for one request (TKT-1ESTYJ).
// The header type has no Content field, so this property is now enforced
// by the compiler rather than by remembering.
//
// The per-ID GetEntity above stays: orphans and validation violations
// resolve a bounded set of ids (never a whole-store scan), and their
// gated re-load is load-bearing for the leak TKT-3FL2S6 closed.
type analyzeReader interface {
	GetEntity(ctx context.Context, id string) (*entity.Entity, error)
	ListEntityHeaders(ctx context.Context, q store.EntityQuery) iter.Seq2[store.EntityHeader, error]
}

// relationCounter counts relations for the cardinality check. Raw (ungated) on
// purpose — see analyzeReader.
type relationCounter interface {
	CountRelations(ctx context.Context, q store.RelationQuery) (int, error)
}

// analyzeService runs the read-only graph-analysis checks (orphans,
// duplicates, gaps, cardinality, properties, validations). Extracted from App
// (TKT-N26KLB M5.1): it depends only on the stable read services, and each
// method takes the per-request metamodel snapshot explicitly (capture-once,
// per the project's snapshot rule) rather than reaching back into App.
//
// reads is GATED per the requesting principal (TKT-3FL2S6, DEC-O59WM4
// superseded); relCounts is the raw relation counter (structural, cannot leak);
// the tracer is the gated decorator. Under NopACL, reads/tracer are the raw
// store/tracer (no gating).
type analyzeService struct {
	reads     analyzeReader
	relCounts relationCounter
	tracer    tracer.Tracer
	validator validator.Validator
}

// AnalysisIssue represents a single validation issue, optionally linked to an entity.
type AnalysisIssue struct {
	EntityID   string // Empty for non-entity issues (e.g., ID gaps)
	EntityType string
	Title      string
	Message    string
	Severity   string // "error" or "warning"

	// RuleMessage is the per-entity explanation a Lua validation rule
	// returned for THIS entity — which of the rule's several possible
	// defects it has, and what to do about it. Message stays the rule's
	// own description, identical across the rule's rows, so the two are
	// shown together rather than one replacing the other.
	//
	// Empty for every issue that is not a Lua-rule violation, and for a
	// Lua rule returning no message.
	RuleMessage string

	// Detail carries optional structured specifics about why the issue
	// fired, beyond the flat Message. For content required-headers
	// violations it holds the missing exact headers. Nil for issues
	// with no structured detail.
	Detail []string

	// ScriptError carries the raw *lua.ScriptError for validation
	// rules whose Lua script failed. Non-nil only on script-error
	// rows; the HTTP handler converts it to a wire envelope using
	// the per-request loopback gate, so the structured detail
	// (path, source slice, stack) reaches the frontend's existing
	// ScriptErrorDialog rather than a flat string.
	// LoadErrors do NOT get a ScriptError — they're not Lua failures.
	ScriptError *lua.ScriptError
}

// maxSectionIssues caps how many issues one analyzer section reports.
//
// A section with more than this many findings is not actionable as a list —
// nobody works through 12,000 duplicate-title warnings — and rendering them
// costs memory and wire bytes on both sides for no benefit. The cap is HARD:
// there is no query parameter to lift it, because "let me request all
// 200,000" is the request that reintroduces the problem.
//
// Detection reads one issue PAST the cap (see [capIssues]): finding a 101st
// is how truncation is known, and that extra row is discarded rather than
// reported.
const maxSectionIssues = 100

// AnalysisSection groups issues by analysis category.
type AnalysisSection struct {
	Name        string
	Description string
	Issues      []AnalysisIssue

	// Truncated reports that the analyzer found MORE issues than it
	// returned, so the UI can say so.
	//
	// Surfaced rather than silent on purpose. An operator who fixes all
	// 100 reported issues, re-runs, and sees 100 again would otherwise
	// conclude analyze is broken. The exact total is deliberately NOT
	// reported: counting every issue means doing all the work the cap
	// exists to avoid, and "100+" is as actionable as "12,431".
	Truncated bool
}

// capIssues trims a section's issues to [maxSectionIssues], setting
// Truncated when there were more.
//
// Applied by analyzers that can only know their issue count at the end
// (grouping analyzers). Streaming analyzers stop scanning at the cap
// instead — see [sectionFull] — which is strictly better because it also
// stops the work.
func capIssues(section AnalysisSection) AnalysisSection {
	if len(section.Issues) <= maxSectionIssues {
		return section
	}
	section.Issues = section.Issues[:maxSectionIssues]
	section.Truncated = true
	return section
}

// sectionFull reports whether a streaming analyzer has collected enough
// issues to stop scanning.
//
// Returns true only once the section holds MORE than the cap, so the
// caller has positively observed an extra issue rather than inferring
// truncation from reaching the limit exactly. A section with exactly
// maxSectionIssues issues is complete, not truncated — the off-by-one that
// would mislabel it is the whole reason this is a named helper.
func sectionFull(section *AnalysisSection) bool {
	return len(section.Issues) > maxSectionIssues
}

// ErrorCount returns the number of error-severity issues in this section.
func (s AnalysisSection) ErrorCount() int {
	n := 0
	for _, issue := range s.Issues {
		if issue.Severity == "error" {
			n++
		}
	}
	return n
}

// WarningCount returns the number of warning-severity issues in this section.
func (s AnalysisSection) WarningCount() int {
	n := 0
	for _, issue := range s.Issues {
		if issue.Severity == "warning" {
			n++
		}
	}
	return n
}

// AnalysisResult is the complete output of running all analyses.
type AnalysisResult struct {
	Sections     []AnalysisSection
	ErrorCount   int
	WarningCount int
}

// runAnalysis executes all analysis checks and returns a combined result.
func (svc analyzeService) runAnalysis(ctx context.Context, meta *metamodel.Metamodel) AnalysisResult {
	sections := []AnalysisSection{
		svc.analyzeProperties(ctx, meta),
		svc.analyzeCardinality(ctx, meta),
		svc.analyzeValidations(ctx, meta),
		svc.analyzeOrphans(ctx, meta),
		svc.analyzeDuplicates(ctx, meta),
		svc.analyzeGaps(ctx, meta),
	}

	var errors, warnings int
	for _, s := range sections {
		errors += s.ErrorCount()
		warnings += s.WarningCount()
	}

	return AnalysisResult{
		Sections:     sections,
		ErrorCount:   errors,
		WarningCount: warnings,
	}
}

// analysisIssueCounts returns the error and warning counts of a full
// analysis run.
//
// COUNTS ARE CAPPED, not totals: it sums the sections runAnalysis returns,
// and each of those stops at [maxSectionIssues] (TKT-1ESTYJ). A project
// with 12,000 property errors reports at most 100 from that section. The
// name predates the cap and now overstates what it returns.
//
// It also does not avoid any work despite the "without building the full
// issue details" claim it used to carry — it calls runAnalysis and discards
// everything but two integers.
//
// No production caller today (only its own test). If a dashboard badge ever
// wants these numbers, give it a real count-only path — one that counts
// without materializing issues and without the cap — rather than reusing
// this; a silently-capped "12,431 problems" badge reading "200" is worse
// than no badge.
func (svc analyzeService) analysisIssueCounts(ctx context.Context, meta *metamodel.Metamodel) (errors, warnings int) {
	result := svc.runAnalysis(ctx, meta)
	return result.ErrorCount, result.WarningCount
}

// analyzeOrphans finds entities with no connections.
func (svc analyzeService) analyzeOrphans(ctx context.Context, meta *metamodel.Metamodel) AnalysisSection {
	section := AnalysisSection{
		Name:        "Orphans",
		Description: "Entities with no incoming or outgoing relations",
	}

	orphanIDs, _ := svc.tracer.FindOrphans(ctx)

	// Each orphan id is re-loaded through the GATED reader before it can become
	// an issue: a hidden entity's GetEntity returns not-found and is dropped, and
	// a visible one is redacted. So even if the tracer yielded a raw id (it does
	// not — svc.tracer is gated too), no hidden entity reaches the wire. Do NOT
	// emit an issue straight from an orphan id/type without this gated re-load —
	// that would reopen the leak this arc closed (TKT-3FL2S6).
	//
	// The ids are sorted BEFORE loading, and loading stops one past the cap:
	// a project where every entity is an orphan would otherwise re-load the
	// entire store as full entities — 20k orphans of ~100 KB was ~930 MB of
	// live heap, the single largest remaining contributor to the analyze OOM
	// (TKT-1ESTYJ). Sorting first keeps WHICH orphans get reported
	// deterministic and id-ordered rather than dependent on how far the load
	// got.
	natsort.Strings(orphanIDs)
	var orphans []*entity.Entity
	st := svc.reads
	for _, id := range orphanIDs {
		if len(orphans) > maxSectionIssues {
			break
		}
		if e, err := st.GetEntity(ctx, id); err == nil {
			orphans = append(orphans, e)
		}
	}

	for _, e := range orphans {
		section.Issues = append(section.Issues, AnalysisIssue{
			EntityID:   e.ID,
			EntityType: e.Type,
			Title:      safeDisplayTitle(meta, e),
			Message:    "No relations",
			Severity:   "warning",
		})
	}

	return capIssues(section)
}

// analyzeDuplicates finds entities with identical normalized titles.
func (svc analyzeService) analyzeDuplicates(ctx context.Context, meta *metamodel.Metamodel) AnalysisSection {
	section := AnalysisSection{
		Name:        "Duplicates",
		Description: "Entities with identical titles",
	}

	// The one analyzer that genuinely CANNOT stream: a title is not known
	// to be duplicated until its second occurrence, which may be the last
	// row scanned, so the grouping must see the whole set. Grouping HEADERS
	// rather than entities is what makes that affordable — the map holds
	// ids and properties, never bodies.
	titleGroups := make(map[string][]store.EntityHeader)
	for h, err := range svc.reads.ListEntityHeaders(ctx, store.EntityQuery{}) {
		if err != nil {
			break
		}
		title := normalizeTitle(safeHeaderTitle(meta, h))
		if title != "" {
			titleGroups[title] = append(titleGroups[title], h)
		}
	}

	// Collect groups with duplicates, sorted by title
	var titles []string
	for title, group := range titleGroups {
		if len(group) > 1 {
			titles = append(titles, title)
		}
	}
	natsort.Strings(titles)

	for _, title := range titles {
		group := titleGroups[title]
		sortHeadersByID(group)
		ids := make([]string, len(group))
		for i, h := range group {
			ids[i] = h.ID
		}
		for _, h := range group {
			section.Issues = append(section.Issues, AnalysisIssue{
				EntityID:   h.ID,
				EntityType: h.Type,
				Title:      safeHeaderTitle(meta, h),
				Message:    fmt.Sprintf("Duplicate title (shared by %s)", strings.Join(ids, ", ")),
				Severity:   "warning",
			})
		}
	}

	return capIssues(section)
}

// analyzeGaps finds gaps in ID sequences for auto-numbered entity types.
//
// Manual-ID types are skipped by the entity's TYPE, not by matching the
// declared id_prefix against the prefix parsed from the ID: the parsed prefix
// is the longest letters-and-dashes run before the digits, so a manual id such
// as `tw-basecamp-10098661922` parses to `tw-basecamp-` and never matches the
// declared `tw-`. Enumeration stops at the section cap ([sectionFull]), so a
// huge gap costs no more than a small one.
func (svc analyzeService) analyzeGaps(ctx context.Context, meta *metamodel.Metamodel) AnalysisSection {
	section := AnalysisSection{
		Name:        "ID Gaps",
		Description: "Missing numbers in auto-generated ID sequences",
	}

	manualTypes := make(map[string]bool)
	for typeName, entityDef := range meta.Entities {
		if entityDef.IsManualID() {
			manualTypes[typeName] = true
		}
	}

	// Group IDs by prefix, remembering the type each prefix belongs to so the
	// data-entry UI's type column renders the type badge. The row stays inert
	// (EntityID is empty), so isClickable in the SPA remains false.
	prefixGroups := make(map[string][]int)
	typeByPrefix := make(map[string]string)
	for h, err := range svc.reads.ListEntityHeaders(ctx, store.EntityQuery{}) {
		if err != nil {
			break
		}
		if manualTypes[h.Type] {
			continue
		}
		parsed, err := entity.ParseEntityID(h.ID)
		if err != nil || parsed.Prefix == "" {
			continue
		}
		prefixGroups[parsed.Prefix] = append(prefixGroups[parsed.Prefix], parsed.Number)
		if cur, ok := typeByPrefix[parsed.Prefix]; !ok || h.Type < cur {
			typeByPrefix[parsed.Prefix] = h.Type
		}
	}

	// Sort prefixes for deterministic output
	prefixes := make([]string, 0, len(prefixGroups))
	for prefix := range prefixGroups {
		prefixes = append(prefixes, prefix)
	}
	natsort.Strings(prefixes)

	for _, prefix := range prefixes {
		numbers := prefixGroups[prefix]
		sort.Ints(numbers)
		for i := 1; i < len(numbers); i++ {
			for j := numbers[i-1] + 1; j < numbers[i]; j++ {
				section.Issues = append(section.Issues, AnalysisIssue{
					EntityType: typeByPrefix[prefix],
					Message:    fmt.Sprintf("Missing ID: %s%03d", prefix, j),
					Severity:   "warning",
				})
				if sectionFull(&section) {
					return capIssues(section)
				}
			}
		}
	}

	return capIssues(section)
}

// analyzeCardinality checks relation cardinality constraints.
//
//nolint:gocognit,funlen // cardinality analysis enumerates min/max bounds across every relation def and direction; the branches are the distinct violation cases, not extractable shared logic.
func (svc analyzeService) analyzeCardinality(ctx context.Context, meta *metamodel.Metamodel) AnalysisSection {
	section := AnalysisSection{
		Name:        "Cardinality",
		Description: "Relation cardinality constraint violations",
	}

	// Sort relation names for deterministic output
	relNames := make([]string, 0, len(meta.Relations))
	for name := range meta.Relations {
		relNames = append(relNames, name)
	}
	natsort.Strings(relNames)

	// listEntities lists headers of a given type, sorted by ID. GATED: only the
	// requester's visible entities are considered, so a hidden entity's title
	// cannot reach a cardinality issue.
	//
	// Materializes per TYPE rather than per store — each entry is a body-free
	// header, and the caller iterates a type's rows several times (once per
	// bound being checked), so re-scanning would cost more than it saves.
	listEntities := func(t string) []store.EntityHeader {
		var out []store.EntityHeader
		for h, err := range svc.reads.ListEntityHeaders(ctx, store.EntityQuery{Type: t}) {
			if err != nil {
				break
			}
			out = append(out, h)
		}
		sortHeadersByID(out)
		return out
	}

	// countRelations counts relations of a specific type for an entity. RAW
	// (ungated): a count is a structural fact, not a value — it cannot leak.
	// Under partial visibility this may over/under-count and produce a false
	// cardinality violation (guarded by the roles annotation, arc step 2).
	countRelations := func(entityID, relType string, direction store.Direction) int {
		n, _ := svc.relCounts.CountRelations(ctx, store.RelationQuery{
			EntityID: entityID, Type: relType, Direction: direction,
		})
		return n
	}

	for _, relName := range relNames {
		relDef := meta.Relations[relName]

		// Check min_outgoing
		if relDef.MinOutgoing != nil && *relDef.MinOutgoing > 0 {
			for _, sourceType := range relDef.From {
				for _, e := range listEntities(sourceType) {
					count := countRelations(e.ID, relName, store.DirectionOutgoing)
					if count < *relDef.MinOutgoing {
						section.Issues = append(section.Issues, AnalysisIssue{
							EntityID:   e.ID,
							EntityType: e.Type,
							Title:      safeHeaderTitle(meta, e),
							Message:    fmt.Sprintf("Must have at least %d '%s' relation(s), has %d", *relDef.MinOutgoing, relName, count),
							Severity:   "error",
						})
					}
				}
			}
		}

		// Check max_outgoing
		if relDef.MaxOutgoing != nil {
			for _, sourceType := range relDef.From {
				for _, e := range listEntities(sourceType) {
					count := countRelations(e.ID, relName, store.DirectionOutgoing)
					if count > *relDef.MaxOutgoing {
						section.Issues = append(section.Issues, AnalysisIssue{
							EntityID:   e.ID,
							EntityType: e.Type,
							Title:      safeHeaderTitle(meta, e),
							Message:    fmt.Sprintf("Has more than %d '%s' relation(s): %d", *relDef.MaxOutgoing, relName, count),
							Severity:   "error",
						})
					}
				}
			}
		}

		// Check min_incoming
		if relDef.MinIncoming != nil && *relDef.MinIncoming > 0 {
			for _, targetType := range relDef.To {
				for _, e := range listEntities(targetType) {
					count := countRelations(e.ID, relName, store.DirectionIncoming)
					if count < *relDef.MinIncoming {
						relLabel := relName
						if relDef.Inverse != nil && relDef.Inverse.GetID() != "" {
							relLabel = relDef.Inverse.GetID()
						}
						section.Issues = append(section.Issues, AnalysisIssue{
							EntityID:   e.ID,
							EntityType: e.Type,
							Title:      safeHeaderTitle(meta, e),
							Message:    fmt.Sprintf("Must have at least %d '%s' relation(s), has %d", *relDef.MinIncoming, relLabel, count),
							Severity:   "error",
						})
					}
				}
			}
		}

		// Check max_incoming
		if relDef.MaxIncoming != nil {
			for _, targetType := range relDef.To {
				for _, e := range listEntities(targetType) {
					count := countRelations(e.ID, relName, store.DirectionIncoming)
					if count > *relDef.MaxIncoming {
						relLabel := relName
						if relDef.Inverse != nil && relDef.Inverse.GetID() != "" {
							relLabel = relDef.Inverse.GetID()
						}
						section.Issues = append(section.Issues, AnalysisIssue{
							EntityID:   e.ID,
							EntityType: e.Type,
							Title:      safeHeaderTitle(meta, e),
							Message:    fmt.Sprintf("Has more than %d '%s' relation(s): %d", *relDef.MaxIncoming, relLabel, count),
							Severity:   "error",
						})
					}
				}
			}
		}
	}

	return capIssues(section)
}

// analyzeProperties validates all entity properties against the metamodel.
func (svc analyzeService) analyzeProperties(ctx context.Context, meta *metamodel.Metamodel) AnalysisSection {
	section := AnalysisSection{
		Name:        "Properties",
		Description: "Property validation errors (required fields, invalid values, ID patterns)",
	}

	// Streams: an issue is emitted as its row is scanned, so peak memory is
	// proportional to the ISSUES found, not to the entities examined. The
	// previous shape drained every entity into a slice purely to sort it —
	// sorting the (far smaller) issue list afterwards is equivalent, since
	// each entity contributes a contiguous run of issues in ID order.
	for h, err := range svc.reads.ListEntityHeaders(ctx, store.EntityQuery{}) {
		if err != nil {
			break
		}
		for _, verr := range meta.ValidateEntity(h.ID, h.Type, h.Properties) {
			section.Issues = append(section.Issues, AnalysisIssue{
				EntityID:   h.ID,
				EntityType: h.Type,
				Title:      safeHeaderTitle(meta, h),
				Message:    verr.Error(),
				Severity:   "error",
			})
		}
		// Abandon the scan once one issue past the cap has been seen:
		// stopping here is what keeps a pathological project cheap rather
		// than merely quiet.
		//
		// WHICH issues survive is then "the first 100 in store order",
		// which the sort below renders in natural order. Store order is
		// ascending by id and natural order is not (E-10 precedes E-9
		// lexicographically but follows it naturally), so on a truncated
		// section the reported set is not necessarily the naturally-first
		// 100. That is acceptable — the set is a bounded sample of a list
		// too long to act on, flagged as truncated — but it is a real
		// difference from sorting the complete set, so do not describe
		// truncated output as "the first N issues".
		if sectionFull(&section) {
			break
		}
	}
	sortIssuesByEntityID(section.Issues)

	return capIssues(section)
}

// analyzeValidations runs custom validation rules from the metamodel.
//
// The browser surface uses CheckRuleFull so Lua-script failures
// (compile, runtime, timeout, contract) and load failures
// (lua_file: missing, traversal-rejected) appear as error issues
// alongside per-entity violations. Without this, broken Lua rules
// would vanish silently from the data-entry analyze view.
func (svc analyzeService) analyzeValidations(ctx context.Context, meta *metamodel.Metamodel) AnalysisSection {
	section := AnalysisSection{
		Name:        "Validations",
		Description: "Custom validation rules defined in the metamodel",
	}

	st := svc.reads
	validator := svc.validator

	for _, rule := range meta.Validations {
		full, err := validator.CheckRuleFull(ctx, rule)
		if err != nil {
			continue
		}
		severity := rule.GetSeverity()
		for _, v := range full.Violations {
			e, err := st.GetEntity(ctx, v.EntityID)
			if err != nil {
				continue
			}
			section.Issues = append(section.Issues, AnalysisIssue{
				EntityID:    e.ID,
				EntityType:  e.Type,
				Title:       safeDisplayTitle(meta, e),
				Message:     rule.Description,
				RuleMessage: v.Message,
				Severity:    severity,
				Detail:      v.Detail,
			})
		}
		// Surface Lua failures and load failures so the UI shows
		// "rule did not run" rather than silently dropping them.
		// These are always error severity — a broken rule is not a
		// warning condition, it's a config-level problem the
		// operator needs to see.
		for _, se := range full.ScriptErrors {
			section.Issues = append(section.Issues, AnalysisIssue{
				EntityID:    se.EntityID,
				EntityType:  "",
				Title:       rule.Name,
				Message:     "Validation script failed: " + scriptErrorSummary(se),
				Severity:    "error",
				ScriptError: se,
			})
		}
		for _, le := range full.LoadErrors {
			section.Issues = append(section.Issues, AnalysisIssue{
				Title:    le.RuleName,
				Message:  "Validation script load failed: " + le.Message,
				Severity: "error",
			})
		}
	}

	return capIssues(section)
}

// scriptErrorSummary builds a single-line summary for the AnalysisIssue
// Message field. The full structured envelope (path, line, source slice)
// is kept on the lua.ScriptError; the browser surface today displays only
// flat strings, so we collapse to a one-liner.
func scriptErrorSummary(se *lua.ScriptError) string {
	if se == nil {
		return ""
	}
	msg := se.Error()
	// Replace newlines so a multi-line wrapped error renders as a
	// single AnalysisIssue.Message rather than corrupting the JSON
	// shape consumers expect.
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", " ")
	return strings.Join(strings.Fields(msg), " ")
}

func sortStoreEntitiesByID(entities []*entity.Entity) {
	sort.Slice(entities, func(i, j int) bool {
		return natsort.Less(entities[i].ID, entities[j].ID)
	})
}

// safeDisplayTitle renders an entity's display title, falling back to the id if
// ANY property backing the title is absent from the (already gated-redacted)
// entity — including one placeholder of a templated display_property. Plain
// DisplayTitle would render a PARTIAL template ("Jeroen " when achternaam is
// hidden), which leaks the readable half and confirms a hidden half exists (the
// BUG-R9EHKV leak class). Gating strips the value; this restores the whole-title
// fallback the deleted hiddenDisplayTitleEntityIDs used to provide (RR-7GN3LV
// follow-up / tschmits review on TKT-3FL2S6). e.Properties is the redacted map,
// so a hidden display property is simply absent here.
func safeDisplayTitle(meta *metamodel.Metamodel, e *entity.Entity) string {
	if def, ok := meta.GetEntityDef(e.Type); ok {
		for _, prop := range def.DisplayProperties() {
			if _, present := e.Properties[prop]; !present {
				return e.ID // a display-title source was redacted → don't leak a partial title
			}
		}
	}
	return meta.DisplayTitle(e.ID, e.Type, e.Properties)
}

// sortHeadersByID is [sortStoreEntitiesByID] for content-free headers.
func sortHeadersByID(headers []store.EntityHeader) {
	sort.Slice(headers, func(i, j int) bool {
		return natsort.Less(headers[i].ID, headers[j].ID)
	})
}

// sortIssuesByEntityID orders issues by entity ID, naturally (so E-9 sorts
// before E-10), preserving the relative order of issues that share an ID.
//
// STABLE deliberately: a streaming analyzer emits one entity's issues as a
// contiguous run in metamodel-validation order, and a caller comparing
// output against the pre-streaming implementation must see that run intact.
// An unstable sort would shuffle the messages within an entity and turn a
// pure refactor into a visible behavior change.
func sortIssuesByEntityID(issues []AnalysisIssue) {
	sort.SliceStable(issues, func(i, j int) bool {
		return natsort.Less(issues[i].EntityID, issues[j].EntityID)
	})
}

// safeHeaderTitle is [safeDisplayTitle] for a content-free header. Display
// titles derive from ID, Type and Properties — never the body — so the two
// agree by construction; this exists because the metamodel helpers take the
// fields individually and a header is not an *entity.Entity.
func safeHeaderTitle(meta *metamodel.Metamodel, h store.EntityHeader) string {
	if def, ok := meta.GetEntityDef(h.Type); ok {
		for _, prop := range def.DisplayProperties() {
			if _, present := h.Properties[prop]; !present {
				return h.ID // a display-title source was redacted → don't leak a partial title
			}
		}
	}
	return meta.DisplayTitle(h.ID, h.Type, h.Properties)
}

// normalizeTitle normalizes a title for duplicate comparison.
func normalizeTitle(s string) string {
	s = strings.ToLower(s)
	s = strings.TrimSpace(s)
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}
