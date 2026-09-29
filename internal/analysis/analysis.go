// Package analysis exposes the read-only analysis facades the CLI
// uses to surface graph health: orphans, duplicates, gaps, cardinality
// violations, custom Lua validations, and orphan temp files left by
// interrupted writes. The service depends only on the focused
// primitives it needs (Store, Meta, Tracer, FS, Paths, Lua deps) so it
// can be constructed at any wiring site.
package analysis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/lua"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/project"
	"github.com/Sourcehaven-BV/rela/internal/relresolve"
	"github.com/Sourcehaven-BV/rela/internal/schema"
	"github.com/Sourcehaven-BV/rela/internal/storage"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/tracer"
	"github.com/Sourcehaven-BV/rela/internal/validation"
	"github.com/Sourcehaven-BV/rela/internal/validationgraph"
)

// ValidationFilter specifies which validation rules to run. Multiple
// filters union (OR).
type ValidationFilter struct {
	RuleName   string
	EntityType string
}

// Options configures analysis scope. Scope (when non-nil)
// limits analysis to specific entity IDs; nil means all entities.
type Options struct {
	Scope map[string]bool
}

// DuplicateGroup represents entities with the same normalized title.
type DuplicateGroup struct {
	Title    string
	Entities []*entity.Entity
}

// UniqueViolation represents a group of entities of the same type that
// share a value for a property declared `unique: true` in the metamodel
// — i.e. a natural-key collision the write path would reject today, but
// which may already exist in data that predates the constraint. Reported
// by [Service.FindUniqueViolations] so an operator can find and fix
// pre-existing duplicates before (or after) enabling `unique: true`.
type UniqueViolation struct {
	EntityType string
	Property   string
	Value      string
	Entities   []*entity.Entity
}

// GapResult contains gaps in an ID sequence.
//
// Missing lists at most [maxListedGaps] IDs; Unlisted counts the rest. A gap
// is a range, so its size is known without naming every ID in it, and one
// stray large number (REQ-1000000 next to REQ-001) must not cost a million
// strings.
type GapResult struct {
	Prefix   string
	Missing  []string
	Unlisted int `json:"Unlisted,omitempty"`
}

// maxListedGaps caps how many missing IDs one sequence names.
const maxListedGaps = 100

// CardinalityViolation re-exports schema.CardinalityViolation so CLI
// consumers don't need to import internal/schema directly.
type CardinalityViolation = schema.CardinalityViolation

// ValidationViolation re-exports validation.Violation so CLI
// consumers don't need to import internal/validation directly.
type ValidationViolation = validation.Violation

// ValidationResult re-exports validation.Result.
type ValidationResult = validation.Result

// ValidationLoadError re-exports validation.LoadError.
type ValidationLoadError = validation.LoadError

// Summary contains counts from all analysis types.
type Summary struct {
	Orphans                int
	Duplicates             int
	UniqueViolations       int
	Gaps                   int
	Cardinality            int
	States                 int
	PropertyErrors         int
	ValidationErrors       int
	ValidationWarnings     int
	ValidationScriptErrors int
	ValidationLoadErrors   int
}

// Deps is the dependency bundle [New] requires.
//
// Store, Meta, Tracer, LuaReadDeps are mandatory.
// LuaCache is optional (nil disables shared rela.cache.* between
// validation rules).
// FS and Paths are optional: when nil, [Service.FindOrphanedTempFiles]
// returns (nil, nil) — analyses that don't touch the filesystem still
// work.
type Deps struct {
	Store       store.Store
	Meta        *metamodel.Metamodel
	Tracer      tracer.Tracer
	LuaReadDeps lua.ReadDeps
	LuaCache    *lua.Cache
	FS          storage.FS
	Paths       *project.Context
}

// Service is the read-only analysis facade.
type Service struct {
	deps Deps
}

// New constructs a Service. Returns an error if any required
// dependency is nil — CLAUDE.md "constructors reject nil required
// fields". FS and Paths and LuaCache are optional (see Deps).
func New(d Deps) (*Service, error) {
	switch {
	case d.Store == nil:
		return nil, errors.New("analysis: Store is required")
	case d.Meta == nil:
		return nil, errors.New("analysis: Meta is required")
	case d.Tracer == nil:
		return nil, errors.New("analysis: Tracer is required")
	}
	return &Service{deps: d}, nil
}

// --- Orphan analysis ---

// FindOrphansWithScope returns entities with no relations, filtered
// by scope.
//
// A non-nil error is an [IncompleteScanError]: either the tracer could
// not enumerate orphans, or one of the orphans it named could not be
// read. The entities that WERE read are still returned, so a caller
// rendering a summary can show them, but a caller making a pass/fail
// claim must not treat the result as complete.
//
// The warn-and-skip this replaces was documented as a known follow-up
// (BUG-4KPN2M why4); a skipped orphan silently lowered the count.
func (s *Service) FindOrphansWithScope(ctx context.Context, opts Options) ([]*entity.Entity, error) {
	ids, err := s.deps.Tracer.FindOrphans(ctx)
	if err != nil {
		return nil, &IncompleteScanError{Op: "find orphans", Err: err}
	}
	st := s.deps.Store
	out := make([]*entity.Entity, 0, len(ids))
	var scanErrs []error
	for _, id := range ids {
		if !inScope(id, opts.Scope) {
			continue
		}
		e, err := st.GetEntity(ctx, id)
		if err != nil {
			scanErrs = append(scanErrs, &IncompleteScanError{
				Op:  "read orphan " + id,
				Err: err,
			})
			continue
		}
		out = append(out, e)
	}
	return out, errors.Join(scanErrs...)
}

// --- Duplicate analysis ---

// FindDuplicates returns groups of entities with similar titles,
// filtered by scope.
// Deliberately the default query, not allStatesQuery: duplicate detection asks
// whether two ENTITIES are the same thing, and an entity's translations are not
// duplicates of each other. Widening would report every faced entity as a
// duplicate of itself.
func (s *Service) FindDuplicates(ctx context.Context, opts Options) ([]DuplicateGroup, error) {
	collected, scanErr := collectEntities(ctx, s.deps.Store, store.EntityQuery{})
	entities := filterByScope(collected, opts.Scope)

	titleGroups := make(map[string][]*entity.Entity)
	for _, e := range entities {
		title := normalizeTitle(e.Title())
		if title != "" {
			titleGroups[title] = append(titleGroups[title], e)
		}
	}

	var duplicates []DuplicateGroup
	for _, group := range titleGroups {
		if len(group) > 1 {
			duplicates = append(duplicates, DuplicateGroup{
				Title:    group[0].Title(),
				Entities: group,
			})
		}
	}
	return duplicates, scanErr
}

// FindUniqueViolations returns groups of same-type entities that share a
// non-empty value for a property declared `unique: true`, filtered by
// scope. Each returned group has at least two entities. Results are
// sorted by (entity type, property, value) for stable output.
//
// This is the read-side companion to the write-path unique constraint
// (see internal/entitymanager checkUniqueProperties): the write path
// rejects NEW duplicates, this surfaces ones that already exist — e.g.
// after an operator adds `unique: true` to a property whose data already
// contains collisions, which the constraint does not retroactively clean.
// List properties are skipped (a natural key is a scalar), matching the
// write-path check.
// Deliberately the default query, not allStatesQuery: a `unique:` natural key
// identifies an ENTITY, and its states share that identity by construction —
// they are the same entity. Widening would make every faced entity collide with
// itself.
func (s *Service) FindUniqueViolations(ctx context.Context, opts Options) ([]UniqueViolation, error) {
	// (type, property) pairs the metamodel declares unique + non-list.
	type uniqueProp struct{ entityType, property string }
	var uniqueProps []uniqueProp
	for typeName, def := range s.deps.Meta.Entities {
		for propName, pd := range def.PropertyDefs() {
			if pd.Unique && !pd.List {
				uniqueProps = append(uniqueProps, uniqueProp{typeName, propName})
			}
		}
	}
	if len(uniqueProps) == 0 {
		return nil, nil
	}

	collected, scanErr := collectEntities(ctx, s.deps.Store, store.EntityQuery{})
	entities := filterByScope(collected, opts.Scope)

	// Group by (type, property, value); a group with >1 entity is a
	// violation. valueGroups keyed on the uniqueProp then the value.
	type groupKey struct{ up uniqueProp }
	valueGroups := make(map[groupKey]map[string][]*entity.Entity)
	for _, e := range entities {
		for _, up := range uniqueProps {
			if e.Type != up.entityType {
				continue
			}
			v := e.GetString(up.property)
			if v == "" {
				continue // empty values are exempt, per the write-path check
			}
			k := groupKey{up}
			if valueGroups[k] == nil {
				valueGroups[k] = make(map[string][]*entity.Entity)
			}
			valueGroups[k][v] = append(valueGroups[k][v], e)
		}
	}

	var violations []UniqueViolation
	for k, byValue := range valueGroups {
		for value, group := range byValue {
			if len(group) > 1 {
				violations = append(violations, UniqueViolation{
					EntityType: k.up.entityType,
					Property:   k.up.property,
					Value:      value,
					Entities:   group,
				})
			}
		}
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].EntityType != violations[j].EntityType {
			return violations[i].EntityType < violations[j].EntityType
		}
		if violations[i].Property != violations[j].Property {
			return violations[i].Property < violations[j].Property
		}
		return violations[i].Value < violations[j].Value
	})
	return violations, scanErr
}

// --- Gap analysis ---

// FindGaps returns gaps in ID sequences, filtered by scope. Only entities of a
// type whose IDs rela numbers can have a gap: manual-ID types are excluded by
// the entity's TYPE. Excluding them by the declared id_prefix does not work,
// because the prefix parsed from an ID is the longest letters-and-dashes run
// before the digits: a manual id `tw-basecamp-10098661922` parses to
// `tw-basecamp-`, never to the declared `tw-`.
// Deliberately the default query, not allStatesQuery: a gap is "this entity is
// missing an expected link", asked once per entity. Reporting the same gap once
// per state would be noise, not coverage.
func (s *Service) FindGaps(ctx context.Context, opts Options) ([]GapResult, error) {
	meta := s.deps.Meta
	manualTypes := make(map[string]bool)
	for typeName, entityDef := range meta.Entities {
		if entityDef.IsManualID() {
			manualTypes[typeName] = true
		}
	}

	collected, scanErr := collectEntities(ctx, s.deps.Store, store.EntityQuery{})
	prefixGroups := make(map[string][]int)
	for _, e := range collected {
		if !inScope(e.ID, opts.Scope) || manualTypes[e.Type] {
			continue
		}
		parsed, err := entity.ParseEntityID(e.ID)
		if err != nil || parsed.Prefix == "" {
			continue
		}
		prefixGroups[parsed.Prefix] = append(prefixGroups[parsed.Prefix], parsed.Number)
	}

	var allGaps []GapResult
	for prefix, numbers := range prefixGroups {
		if gap := sequenceGaps(prefix, numbers); gap.Unlisted > 0 || len(gap.Missing) > 0 {
			allGaps = append(allGaps, gap)
		}
	}
	return allGaps, scanErr
}

// sequenceGaps names the first [maxListedGaps] numbers missing from numbers
// and counts the rest arithmetically, so its cost is bounded by the number of
// entities, not by the size of the gaps between them.
func sequenceGaps(prefix string, numbers []int) GapResult {
	sort.Ints(numbers)
	gap := GapResult{Prefix: prefix}
	for i := 1; i < len(numbers); i++ {
		for j := numbers[i-1] + 1; j < numbers[i]; j++ {
			if len(gap.Missing) == maxListedGaps {
				gap.Unlisted += numbers[i] - j
				break
			}
			gap.Missing = append(gap.Missing, fmt.Sprintf("%s%03d", prefix, j))
		}
	}
	return gap
}

// --- Cardinality analysis ---

// CheckCardinality checks all cardinality constraints, filtered by
// scope.
//
// The check itself lives in [schema.CheckCardinality]: `internal/mcp`
// serves the same analysis over a gated reader and may not import this
// package, so the one implementation sits in a package both consumers
// already depend on (TKT-CICJSN). This method stays the CLI's entry
// point — `rela analyze cardinality`, `rela validate --check
// cardinality` and `analyze all` all reach it — and re-exports the
// result type, so those surfaces need no schema import.
//
// A store error fails the run loudly: the first failing count aborts
// with a wrapped error and NO violations. Reporting around a failed
// count would fabricate violations — a backend outage reads as count 0,
// which for a min bound looks exactly like missing relations
// (TKT-RNBLAC). This deliberately diverges from the under-count logging
// of the other analyses (see [Service.FindOrphansWithScope]): those can
// only miss findings, a failed count invents them.
//
// # Scope of its incomplete-scan reporting
//
// This check scans only the entity TYPES that declare a bound, so it
// detects an unreadable file only for those types. An unreadable file
// of a type with no cardinality constraints is invisible here, and
// correctly so: every constraint this check has WAS evaluated over
// every subject it governs.
//
// That means `--check cardinality` alone is not a whole-corpus
// readability gate. `--check properties` and `--check validations` both
// scan all types and do report such a file, which is why the CI
// invocation runs all three.
func (s *Service) CheckCardinality(ctx context.Context, opts Options) ([]CardinalityViolation, error) {
	return schema.CheckCardinality(ctx, s.deps.Store, s.deps.Meta, opts.Scope)
}

// --- Custom validations ---

// newValidationService wires a validation service against the
// service's Lua deps + cache. Construction is cheap (rules come from
// Meta on every Check call); the per-call instance avoids cache
// aliasing if a future caller passes a different LuaCache.
func (s *Service) newValidationService() *validation.Service {
	svc := validation.New(s.deps.Meta, s.deps.LuaReadDeps)
	// Relation gates read through the SAME reader the rest of the rule
	// evaluation uses, so a constraint counts exactly what this caller can
	// see. internal/validator wires the identical thing; the two entry
	// points into a Service must not differ here, or a gate would mean
	// something different depending on which one ran it.
	if g, err := validationgraph.New(s.deps.LuaReadDeps.VisibleReader); err != nil {
		slog.Warn("analysis: relation-cardinality gates unavailable; they will report as unevaluable",
			"error", err)
	} else {
		svc = svc.WithGraph(g)
	}
	// Analysis runs at operator trust over the raw store (its entity reads
	// are raw too), so rule traversals are answered ungated.
	if b, err := relresolve.NewStoreBinder(s.deps.Meta, relresolve.Ungated, s.deps.Store); err == nil {
		svc = svc.WithTraversals(b)
	} else {
		// Rules using related() then report a load error when they run.
		slog.Warn("analysis: related() in validation rules unavailable", "error", err)
	}
	if s.deps.LuaCache != nil {
		return svc.WithCache(s.deps.LuaCache)
	}
	return svc
}

// RunValidations executes all custom validation rules from the
// metamodel, filtered by scope.
//
// A non-nil error is an [IncompleteScanError]: the rules ran over the
// entities that could be read, so the result is usable but does not
// cover the whole project. A rule cannot have passed on an entity it
// never saw (BUG-4KPN2M).
func (s *Service) RunValidations(ctx context.Context, opts Options) (ValidationResult, error) {
	entities, scanErr := collectEntities(ctx, s.deps.Store, allStatesQuery())
	return s.newValidationService().Check(ctx, entities, opts.Scope), scanErr
}

// RunValidationsFiltered executes custom validation rules matching
// the given filters. Multiple filters union (OR). An empty
// ValidationFilter matches all rules.
// A non-nil error is an [IncompleteScanError] — see [Service.RunValidations].
func (s *Service) RunValidationsFiltered(
	ctx context.Context,
	opts Options,
	filters []ValidationFilter,
) (ValidationResult, error) {
	svc := s.newValidationService()

	ruleNames := make(map[string]bool)
	for _, filter := range filters {
		for _, rule := range svc.Rules() {
			if matchesFilter(rule, filter) {
				ruleNames[rule.Name] = true
			}
		}
	}

	entities, scanErr := collectEntities(ctx, s.deps.Store, allStatesQuery())
	return svc.CheckRules(ctx, entities, opts.Scope, ruleNames), scanErr
}

// matchesFilter returns true if the rule matches the filter criteria.
func matchesFilter(rule metamodel.ValidationRule, filter ValidationFilter) bool {
	if filter.RuleName != "" {
		return rule.Name == filter.RuleName
	}
	if filter.EntityType != "" {
		return rule.EntityType == filter.EntityType
	}
	return true
}

// CountValidationsBySeverity returns counts of errors and warnings
// from violations.
func CountValidationsBySeverity(violations []ValidationViolation) (errors, warnings int) {
	return validation.CountBySeverity(violations)
}

// --- Summary ---

// AnalyzeAll runs all analyses and returns a summary of counts. A
// cardinality store error fails the whole run — see the
// [Service.CheckCardinality] error policy.
//
// When the returned error satisfies [IsIncompleteScan] the summary is
// non-nil and usable, but its counts are lower bounds: some input could
// not be read. Any other error means no summary at all.
func (s *Service) AnalyzeAll(ctx context.Context, opts Options) (*Summary, error) {
	cardinality, err := s.CheckCardinality(ctx, opts)
	if err != nil {
		return nil, err
	}
	states, err := s.CheckStates(ctx, opts)
	if err != nil {
		return nil, err
	}

	// Each analysis rescans, so each can independently report an
	// incomplete read; join them so the caller learns about every file it
	// could not read rather than only the first.
	var scanErrs []error
	orphans, err := s.FindOrphansWithScope(ctx, opts)
	scanErrs = append(scanErrs, err)
	duplicates, err := s.FindDuplicates(ctx, opts)
	scanErrs = append(scanErrs, err)
	uniqueViolations, err := s.FindUniqueViolations(ctx, opts)
	scanErrs = append(scanErrs, err)
	gaps, err := s.FindGaps(ctx, opts)
	scanErrs = append(scanErrs, err)

	summary := &Summary{
		Orphans:          len(orphans),
		Duplicates:       len(duplicates),
		UniqueViolations: len(uniqueViolations),
		Gaps:             len(gaps),
		Cardinality:      len(cardinality),
		States:           len(states),
	}

	propErrors, err := schema.ValidateEntityProperties(ctx, s.deps.Store, s.deps.Meta)
	if err != nil {
		scanErrs = append(scanErrs, &IncompleteScanError{Op: "validate entity properties", Err: err})
	}
	for _, pe := range propErrors {
		if !inScope(pe.EntityID, opts.Scope) {
			continue
		}
		summary.PropertyErrors += len(pe.Errors)
	}

	result, err := s.RunValidations(ctx, opts)
	scanErrs = append(scanErrs, err)
	summary.ValidationErrors, summary.ValidationWarnings = validation.CountBySeverity(result.Violations)
	summary.ValidationScriptErrors = len(result.ScriptErrors)
	summary.ValidationLoadErrors = len(result.LoadErrors)

	return summary, errors.Join(scanErrs...)
}

// --- Orphan temp files ---

// FindOrphanedTempFiles returns paths of leftover .new temp files in
// the entities/ and relations/ directories. Returns (nil, nil) when
// the service was constructed without FS + Paths.
func (s *Service) FindOrphanedTempFiles() ([]string, error) {
	if s.deps.FS == nil || s.deps.Paths == nil {
		return nil, nil
	}
	orphaned := make([]string, 0) //nolint:prealloc // capacity unknown
	orphaned = append(orphaned, findTempFilesInDir(s.deps.FS, s.deps.Paths.EntitiesDir)...)
	orphaned = append(orphaned, findTempFilesInDir(s.deps.FS, s.deps.Paths.RelationsDir)...)
	return orphaned, nil
}

// CleanupOrphanedTempFiles removes every orphaned .new temp file.
// Returns the number of files cleaned up. Returns (0, nil) when the
// service was constructed without FS + Paths — same gate as
// [Service.FindOrphanedTempFiles].
func (s *Service) CleanupOrphanedTempFiles() (int, error) {
	orphaned, err := s.FindOrphanedTempFiles()
	if err != nil {
		return 0, err
	}
	for _, path := range orphaned {
		if removeErr := s.deps.FS.Remove(path); removeErr != nil {
			return 0, fmt.Errorf("remove %s: %w", path, removeErr)
		}
	}
	return len(orphaned), nil
}

// findTempFilesInDir walks a directory (recursively) for .new temp files.
func findTempFilesInDir(fs storage.FS, dir string) []string {
	var result []string
	entries, err := fs.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		name := entry.Name()
		path := dir + "/" + name
		if entry.IsDir() {
			result = append(result, findTempFilesInDir(fs, path)...)
		} else if strings.HasSuffix(name, ".new") {
			result = append(result, path)
		}
	}
	return result
}

// --- helpers ---

// collectEntities iterates the store yielding entities. On iteration
// error it returns the entities read so far AND an
// [IncompleteScanError], so a caller can both render what it has and
// report that the scan was partial.
//
// Returning the partial slice alongside the error is deliberate:
// `rela analyze` still wants to show its summary, while `rela validate`
// must refuse to call the run a pass. Before BUG-4KPN2M this function
// logged a warning and returned only the slice, which made an
// under-count indistinguishable from a clean read.
func collectEntities(ctx context.Context, s store.Store, q store.EntityQuery) ([]*entity.Entity, error) {
	out := make([]*entity.Entity, 0)
	for e, err := range s.ListEntities(ctx, q) {
		if err != nil {
			return out, &IncompleteScanError{Op: "list entities", EntityType: q.Type, Err: err}
		}
		out = append(out, e)
	}
	return out, nil
}

// filterByScope returns only entities present in scope. nil scope is
// a pass-through.
func filterByScope(entities []*entity.Entity, scope map[string]bool) []*entity.Entity {
	if scope == nil {
		return entities
	}
	result := make([]*entity.Entity, 0, len(entities))
	for _, e := range entities {
		if scope[e.ID] {
			result = append(result, e)
		}
	}
	return result
}

// inScope returns true if entityID is in scope (or scope is nil).
func inScope(entityID string, scope map[string]bool) bool {
	if scope == nil {
		return true
	}
	_, exists := scope[entityID]
	return exists
}

// normalizeTitle normalizes a title for duplicate detection.
func normalizeTitle(s string) string {
	s = strings.ToLower(s)
	s = strings.TrimSpace(s)
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// allStatesQuery is the query for an analysis that asks about CONTENT.
//
// Each content state (face) of an entity is a separate stored row holding its
// own property values, so a check about whether content conforms must see all
// of them. Leaving AllStates false — the zero value, "default-state rows only"
// — makes such a check report a clean run over data it never loaded, which is
// worse than no check because it is a claim (TKT-4Y6CMV).
//
// NOT every analysis wants this. A question about an entity's IDENTITY — is
// this a duplicate of that one, is this natural key unique, is this entity
// orphaned — is asked once per entity, and widening it would report the same
// entity once per state. Those keep the default query deliberately; see their
// call sites.
func allStatesQuery() store.EntityQuery {
	return store.EntityQuery{AllStates: true}
}
