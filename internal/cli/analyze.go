package cli

import (
	"context"
	stderrors "errors"
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Sourcehaven-BV/rela/internal/analysis"
	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/errors"
	"github.com/Sourcehaven-BV/rela/internal/filter"
	"github.com/Sourcehaven-BV/rela/internal/lua"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/output"
	"github.com/Sourcehaven-BV/rela/internal/schema"
)

// AnalyzeCmd is the parent of analyze subcommands.
type AnalyzeCmd struct {
	Orphans       AnalyzeOrphansCmd       `cmd:"" help:"Find entities with no connections."`
	Duplicates    AnalyzeDuplicatesCmd    `cmd:"" help:"Find entities with similar titles."`
	Unique        AnalyzeUniqueCmd        `cmd:"" help:"Find entities that violate a unique property constraint."`
	Gaps          AnalyzeGapsCmd          `cmd:"" help:"Find gaps in ID sequences."`
	Cardinality   AnalyzeCardinalityCmd   `cmd:"" help:"Check relation cardinality constraints."`
	RelationOrder AnalyzeRelationOrderCmd `cmd:"" name:"relation-order" help:"Find duplicate or missing values on managed relation order properties."`
	RelationFiles AnalyzeRelationFilesCmd `cmd:"" name:"relation-files" help:"Find relation files whose filename disagrees with their content."`
	Properties    AnalyzePropertiesCmd    `cmd:"" help:"Validate entity property values against metamodel."`
	Validations   AnalyzeValidationsCmd   `cmd:"" help:"Run custom validation rules from metamodel."`
	States        AnalyzeStatesCmd        `cmd:"" help:"Find content-state integrity issues (undeclared faces, rows stranded at the bare id)."`
	All           AnalyzeAllCmd           `cmd:"" help:"Run all analyses."`
	Schema        AnalyzeSchemaCmd        `cmd:"" help:"Analyze metamodel schema usage."`
}

// resolveAnalyzeOpts returns the analysis options. Kept for symmetry
// with the original implementation; today there are no flag-driven
// scope or filter overrides at the top level.
func resolveAnalyzeOpts() (*analysis.Options, error) {
	return &analysis.Options{}, nil
}

// writeAnalysisJSON writes an analysis result if JSON output is enabled.
func writeAnalysisJSON(count int, details any, successMsg, issuesFmt string) bool {
	if out.Format != "json" {
		return false
	}
	status := "success"
	message := successMsg
	if count > 0 {
		status = "warning"
		message = fmt.Sprintf(issuesFmt, count)
	}
	_ = out.WriteAnalysisResult(output.AnalysisResult{
		Status: status, Message: message, Count: count, Details: details,
	})
	return true
}

// reportPartialScan renders the incomplete-scan condition for an
// `analyze` subcommand and returns the error the command should return.
//
// analyze differs from validate: its output is a human summary, so it
// still renders the partial results above this notice. What it must not
// do is present them as complete. The exit code matches validate's
// (exitValidationPartial) so any script wrapping either command reads
// "could not read all input" the same way (BUG-4KPN2M scope item 3).
//
// Returns nil when scanErr is nil, so call sites can `return
// reportPartialScan(...)` unconditionally.
func reportPartialScan(scanErr error) error {
	if scanErr == nil {
		return nil
	}
	if out.Format == "json" {
		// The JSON body was already written by the caller; the exit code
		// is the only channel left that a consumer reliably reads.
		return errors.NewExitError(exitValidationPartial)
	}
	out.WriteError("Scan INCOMPLETE: some entities could not be read, so the counts above are lower bounds.")
	for _, f := range analysis.IncompleteScanFiles(scanErr) {
		out.WriteMessage("  ✗ %s", f)
	}
	return errors.NewExitError(exitValidationPartial)
}

// AnalyzeOrphansCmd finds entities with no connections.
type AnalyzeOrphansCmd struct{}

// Run dispatches `rela analyze orphans`.
func (c *AnalyzeOrphansCmd) Run(ctx context.Context, analyzer *analysis.Service) error {
	opts, err := resolveAnalyzeOpts()
	if err != nil {
		return err
	}
	orphans, scanErr := analyzer.FindOrphansWithScope(ctx, *opts)
	filter.SortByID(orphans, storeEntityRecord, false)

	orphansMsg := "No orphan entities found"
	if writeAnalysisJSON(len(orphans), orphans, orphansMsg, "Found %d orphan entities") {
		return reportPartialScan(scanErr)
	}

	if len(orphans) == 0 {
		if scanErr == nil {
			out.WriteSuccess("No orphan entities found")
		}
		return reportPartialScan(scanErr)
	}
	out.WriteWarning("Found %d orphan entities:", len(orphans))
	if err := out.WriteEntities(orphans); err != nil {
		return err
	}
	return reportPartialScan(scanErr)
}

// AnalyzeDuplicatesCmd finds entities with similar titles.
type AnalyzeDuplicatesCmd struct{}

// Run dispatches `rela analyze duplicates`.
func (c *AnalyzeDuplicatesCmd) Run(ctx context.Context, analyzer *analysis.Service) error {
	opts, err := resolveAnalyzeOpts()
	if err != nil {
		return err
	}
	duplicates, scanErr := analyzer.FindDuplicates(ctx, *opts)

	if out.Format == "json" {
		type duplicateGroup struct {
			Title    string           `json:"title"`
			Entities []*entity.Entity `json:"entities"`
		}
		var details []duplicateGroup
		for _, group := range duplicates {
			details = append(details, duplicateGroup{Title: group.Title, Entities: group.Entities})
		}
		writeAnalysisJSON(len(duplicates), details,
			"No duplicate titles found", "Found %d groups of potential duplicates")
		return reportPartialScan(scanErr)
	}

	if len(duplicates) == 0 {
		if scanErr == nil {
			out.WriteSuccess("No duplicate titles found")
		}
		return reportPartialScan(scanErr)
	}
	out.WriteWarning("Found %d groups of potential duplicates:", len(duplicates))
	for _, group := range duplicates {
		out.WriteMessage("")
		out.WriteMessage("  Title: %s", group.Title)
		for _, e := range group.Entities {
			out.WriteMessage("    - %s (%s)", e.ID, e.Type)
		}
	}
	return reportPartialScan(scanErr)
}

// AnalyzeUniqueCmd finds entities that violate a `unique: true` property
// constraint — same-type entities sharing a value for a unique property.
type AnalyzeUniqueCmd struct{}

// Run dispatches `rela analyze unique`.
func (c *AnalyzeUniqueCmd) Run(ctx context.Context, analyzer *analysis.Service) error {
	opts, err := resolveAnalyzeOpts()
	if err != nil {
		return err
	}
	violations, scanErr := analyzer.FindUniqueViolations(ctx, *opts)

	if out.Format == "json" {
		type uniqueViolation struct {
			EntityType string           `json:"entity_type"`
			Property   string           `json:"property"`
			Value      string           `json:"value"`
			Entities   []*entity.Entity `json:"entities"`
		}
		details := make([]uniqueViolation, 0, len(violations))
		for _, v := range violations {
			details = append(details, uniqueViolation{
				EntityType: v.EntityType, Property: v.Property, Value: v.Value, Entities: v.Entities,
			})
		}
		writeAnalysisJSON(len(violations), details,
			"No unique constraint violations found", "Found %d unique constraint violations")
		return reportPartialScan(scanErr)
	}

	if len(violations) == 0 {
		if scanErr == nil {
			out.WriteSuccess("No unique constraint violations found")
		}
		return reportPartialScan(scanErr)
	}
	out.WriteWarning("Found %d unique constraint violations:", len(violations))
	for _, v := range violations {
		out.WriteMessage("")
		out.WriteMessage("  %s.%s = %q shared by:", v.EntityType, v.Property, v.Value)
		for _, e := range v.Entities {
			out.WriteMessage("    - %s", e.ID)
		}
	}
	return reportPartialScan(scanErr)
}

// AnalyzeGapsCmd finds gaps in ID sequences.
type AnalyzeGapsCmd struct{}

// Run dispatches `rela analyze gaps`.
func (c *AnalyzeGapsCmd) Run(ctx context.Context, analyzer *analysis.Service) error {
	opts, err := resolveAnalyzeOpts()
	if err != nil {
		return err
	}
	allGaps, scanErr := analyzer.FindGaps(ctx, *opts)
	gapsMsg := "No ID sequence gaps found"
	if writeAnalysisJSON(len(allGaps), allGaps, gapsMsg, "Found gaps in %d ID sequences") {
		return reportPartialScan(scanErr)
	}

	if len(allGaps) == 0 {
		if scanErr == nil {
			out.WriteSuccess("No ID sequence gaps found")
		}
	} else {
		for _, gap := range allGaps {
			out.WriteWarning("Gaps in %s sequence:", gap.Prefix)
			out.WriteMessage("  Missing: %s", strings.Join(gap.Missing, ", "))
			if gap.Unlisted > 0 {
				out.WriteMessage("  … and %d more", gap.Unlisted)
			}
		}
	}
	return reportPartialScan(scanErr)
}

// AnalyzeCardinalityCmd checks relation cardinality constraints.
type AnalyzeCardinalityCmd struct{}

// Run dispatches `rela analyze cardinality`.
func (c *AnalyzeCardinalityCmd) Run(ctx context.Context, analyzer *analysis.Service) error {
	opts, err := resolveAnalyzeOpts()
	if err != nil {
		return err
	}
	violations, err := analyzer.CheckCardinality(ctx, *opts)
	if err != nil {
		return err
	}
	cardMsg := "All cardinality constraints satisfied"
	if writeAnalysisJSON(len(violations), violations, cardMsg, "Found %d cardinality violations") {
		return nil
	}

	for _, v := range violations {
		out.WriteWarning("%s %s", v.EntityID, v.Message())
	}
	if len(violations) == 0 {
		out.WriteSuccess("All cardinality constraints satisfied")
	} else {
		out.WriteWarning("Found %d cardinality violations", len(violations))
	}
	return nil
}

// AnalyzeStatesCmd reports content-state integrity findings
// (TKT-DOFYR1): rows stored under faces no metamodel declaration accounts
// for, rows stranded at the bare id on a type that declares `faces:`
// (BUG-UA3BK3), and type-mismatched states. The remedy is a data
// migration; this surface detects only.
//
// It no longer reports headless families. A family with no zero-coordinate
// row was corrupt while one face was privileged by storage, and is the
// ordinary shape of a faced entity now (BUG-HC6I2T).
type AnalyzeStatesCmd struct{}

// Run dispatches `rela analyze states`.
func (c *AnalyzeStatesCmd) Run(ctx context.Context, analyzer *analysis.Service) error {
	opts, err := resolveAnalyzeOpts()
	if err != nil {
		return err
	}
	findings, err := analyzer.CheckStates(ctx, *opts)
	if err != nil {
		return err
	}
	statesMsg := "No content-state findings"
	if writeAnalysisJSON(len(findings), findings, statesMsg, "Found %d content-state findings") {
		return nil
	}
	for _, f := range findings {
		examples := strings.Join(f.Examples, ", ")
		if len(f.Examples) < f.Count {
			examples += ", …"
		}
		out.WriteWarning("[%s] %s: %d row(s) — %s (e.g. %s)",
			f.Code, f.Subject, f.Count, f.Detail, examples)
	}
	if len(findings) == 0 {
		out.WriteSuccess("%s", statesMsg)
	} else {
		out.WriteWarning("Found %d content-state findings", len(findings))
	}
	return nil
}

// AnalyzeRelationOrderCmd finds duplicate or missing values on managed
// relation order properties. Soft condition: engine tolerates them
// (duplicates sort stably, missing values sort last) — use this to
// surface markdown files needing cleanup after hand-edits or imports.
type AnalyzeRelationOrderCmd struct{}

// Run dispatches `rela analyze relation-order`.
func (c *AnalyzeRelationOrderCmd) Run(ctx context.Context, analyzer *analysis.Service) error {
	opts, err := resolveAnalyzeOpts()
	if err != nil {
		return err
	}
	issues := analyzer.CheckRelationOrder(ctx, *opts)

	if writeAnalysisJSON(len(issues), issues,
		"All orderable relations have consistent order values",
		"Found %d relation-order issue(s)") {

		return nil
	}

	for _, iss := range issues {
		out.WriteWarning("%s (%s) on %s side of %q: %d %s value(s) at %s",
			iss.EntityID, iss.EntityType, iss.Side, iss.RelationType,
			iss.Count, iss.Kind, iss.Property)
	}

	if len(issues) == 0 {
		out.WriteSuccess("All orderable relations have consistent order values")
	} else {
		out.WriteWarning("Found %d relation-order issue(s)", len(issues))
	}
	return nil
}

// AnalyzeRelationFilesCmd finds relation files whose NAME disagrees with their
// CONTENT. fsstore keys relations entirely on the filename, so such a file is
// indexed as the relation its name describes and the one its content describes
// simply does not exist — surfacing elsewhere as a cardinality error against an
// innocent entity, with no route back to the file (issue #1004).
type AnalyzeRelationFilesCmd struct{}

// Run dispatches `rela analyze relation-files`.
func (c *AnalyzeRelationFilesCmd) Run(analyzer *analysis.Service) error {
	issues := analyzer.CheckRelationFilenames()

	if writeAnalysisJSON(len(issues), issues,
		"All relation files agree with their filenames",
		"Found %d relation file(s) with filename/content problems") {

		return nil
	}

	// Switch on the typed Reason, not on which fields happen to be empty: a
	// new reason must then be handled here rather than silently rendering as
	// whichever case its empty fields resemble.
	for _, iss := range issues {
		switch iss.Reason {
		case analysis.ReasonUnparseableName:
			out.WriteWarning("%s: %s — the store skips it, so this relation is not in the graph",
				iss.File, iss.Reason)
		case analysis.ReasonLegacyTypeKey:
			out.WriteWarning("%s: uses `type:` instead of `relation:` — loads with an EMPTY "+
				"relation type (filename says %q)", iss.File, iss.FromFilename)
		case analysis.ReasonMismatch:
			// Both triples, always: the filename one is what the store
			// indexed, the content one is what other checks report as
			// missing. Printing one leaves the operator to guess the mapping.
			out.WriteWarning("%s: indexed as %q but content says %q",
				iss.File, iss.FromFilename, iss.FromContent)
		default:
			out.WriteWarning("%s: %s", iss.File, iss.Reason)
		}
	}

	if len(issues) == 0 {
		out.WriteSuccess("All relation files agree with their filenames")
	} else {
		out.WriteWarning("Found %d relation file(s) with filename/content problems", len(issues))
	}
	return nil
}

// AnalyzePropertiesCmd validates entity property values against metamodel.
type AnalyzePropertiesCmd struct{}

// Run dispatches `rela analyze properties`.
func (c *AnalyzePropertiesCmd) Run(ctx context.Context, svc *readServices) error {
	opts, err := resolveAnalyzeOpts()
	if err != nil {
		return err
	}
	return runPropertyValidation(ctx, svc, *opts)
}

func runPropertyValidation(ctx context.Context, svc *readServices, opts analysis.Options) error {
	allEntityErrors, scanErr := schema.ValidateEntityProperties(ctx, svc.Store, svc.Meta)
	if scanErr != nil {
		scanErr = &analysis.IncompleteScanError{Op: "validate entity properties", Err: scanErr}
	}
	if opts.Scope != nil {
		filtered := allEntityErrors[:0]
		for _, ee := range allEntityErrors {
			if opts.Scope[ee.EntityID] {
				filtered = append(filtered, ee)
			}
		}
		allEntityErrors = filtered
	}
	allRelationErrors := schema.ValidateRelationProperties(ctx, svc.Store, svc.Meta)

	errorCount := 0
	for _, ee := range allEntityErrors {
		errorCount += len(ee.Errors)
	}
	for _, re := range allRelationErrors {
		errorCount += len(re.Errors)
	}

	if out.Format == "json" {
		if err := writePropertyValidationJSON(allEntityErrors, allRelationErrors, errorCount); err != nil {
			return err
		}
		return reportPartialScan(scanErr)
	}
	if err := writePropertyValidationText(allEntityErrors, allRelationErrors, errorCount); err != nil {
		return err
	}
	return reportPartialScan(scanErr)
}

func writePropertyValidationJSON(
	allEntityErrors []schema.PropertyError,
	allRelationErrors []schema.RelationPropertyError,
	errorCount int,
) error {
	entityResults := make([]output.PropertyValidationResult, 0, len(allEntityErrors))
	for _, ee := range allEntityErrors {
		errStrings := make([]string, len(ee.Errors))
		for i, err := range ee.Errors {
			errStrings[i] = err.Message
		}
		entityResults = append(entityResults, output.PropertyValidationResult{
			EntityID:   ee.EntityID,
			EntityType: ee.EntityType,
			Errors:     errStrings,
		})
	}
	relationResults := make([]output.RelationPropertyValidationResult, 0, len(allRelationErrors))
	for _, re := range allRelationErrors {
		errStrings := make([]string, len(re.Errors))
		for i, err := range re.Errors {
			errStrings[i] = err.Message
		}
		relationResults = append(relationResults, output.RelationPropertyValidationResult{
			RelationKey:  re.RelationKey,
			RelationType: re.RelationType,
			Errors:       errStrings,
		})
	}
	status := "success"
	message := "All entity and relation properties are valid"
	if errorCount > 0 {
		status = "error"
		message = fmt.Sprintf("Found %d property errors across %d entities and %d relations",
			errorCount, len(allEntityErrors), len(allRelationErrors))
	}
	details := make(map[string]any)
	if len(entityResults) > 0 {
		details["entities"] = entityResults
	}
	if len(relationResults) > 0 {
		details["relations"] = relationResults
	}
	return out.WriteAnalysisResult(output.AnalysisResult{
		Status: status, Message: message, Count: errorCount, Details: details,
	})
}

func writePropertyValidationText(
	allEntityErrors []schema.PropertyError,
	allRelationErrors []schema.RelationPropertyError,
	errorCount int,
) error {
	if errorCount == 0 {
		out.WriteSuccess("All entity and relation properties are valid")
		return nil
	}
	out.WriteError("Found %d property errors:", errorCount)
	if len(allEntityErrors) > 0 {
		out.WriteMessage("")
		out.WriteMessage("Entities (%d):", len(allEntityErrors))
		for _, ee := range allEntityErrors {
			out.WriteMessage("  %s (%s):", ee.EntityID, ee.EntityType)
			for _, err := range ee.Errors {
				out.WriteMessage("    - %s", err.Error())
			}
		}
	}
	if len(allRelationErrors) > 0 {
		out.WriteMessage("")
		out.WriteMessage("Relations (%d):", len(allRelationErrors))
		for _, re := range allRelationErrors {
			out.WriteMessage("  %s:", re.RelationKey)
			for _, err := range re.Errors {
				out.WriteMessage("    - %s", err.Error())
			}
		}
	}
	return nil
}

// AnalyzeValidationsCmd runs custom validation rules from metamodel.
type AnalyzeValidationsCmd struct{}

// Run dispatches `rela analyze validations`.
func (c *AnalyzeValidationsCmd) Run(ctx context.Context, svc *readServices, analyzer *analysis.Service) error {
	opts, err := resolveAnalyzeOpts()
	if err != nil {
		return err
	}
	return runValidations(ctx, svc, analyzer, *opts)
}

func runValidations(ctx context.Context, svc *readServices, analyzer *analysis.Service, opts analysis.Options) error {
	rules := svc.Meta.Validations
	if len(rules) == 0 {
		return writeNoValidationRules()
	}
	result, scanErr := analyzer.RunValidations(ctx, opts)
	errorCount, warningCount := countValidationViolationsBySeverity(result.Violations)
	var err error
	if out.Format == "json" {
		err = writeValidationsJSON(rules, result, errorCount, warningCount)
	} else {
		err = writeValidationsText(rules, result, errorCount, warningCount)
	}
	// An incomplete scan outranks a violation exit: the rules did not see
	// the whole project, so their verdict is not the finding to act on.
	if partial := reportPartialScan(scanErr); partial != nil {
		return partial
	}
	return err
}

func writeNoValidationRules() error {
	if out.Format == "json" {
		return out.WriteAnalysisResult(output.AnalysisResult{
			Status:  "success",
			Message: "No custom validation rules defined in metamodel",
			Count:   0,
			Details: []any{},
		})
	}
	out.WriteSuccess("No custom validation rules defined in metamodel")
	return nil
}

func countValidationViolationsBySeverity(violations []analysis.ValidationViolation) (errs, warns int) {
	for _, v := range violations {
		if v.Severity == "error" {
			errs++
		} else {
			warns++
		}
	}
	return errs, warns
}

func writeValidationsJSON(
	rules []metamodel.ValidationRule,
	result analysis.ValidationResult,
	errorCount, warningCount int,
) error {
	status := "success"
	message := fmt.Sprintf("All %d validation rules passed", len(rules))
	if errorCount > 0 || result.HasErrors() {
		status = "error"
		message = fmt.Sprintf("Found %d errors, %d warnings across %d rules",
			errorCount, warningCount, len(rules))
	} else if warningCount > 0 {
		status = "warning"
		message = fmt.Sprintf("Found %d warnings across %d rules", warningCount, len(rules))
	}
	if jsonErr := out.WriteAnalysisResult(output.AnalysisResult{
		Status: status, Message: message, Count: errorCount + warningCount, Details: result.Violations,
	}); jsonErr != nil {
		return jsonErr
	}
	if errorCount > 0 || result.HasErrors() {
		return errors.NewExitError(1)
	}
	return nil
}

func writeValidationsText(
	rules []metamodel.ValidationRule,
	result analysis.ValidationResult,
	errorCount, warningCount int,
) error {
	ruleViolations := make(map[string][]analysis.ValidationViolation)
	for _, v := range result.Violations {
		ruleViolations[v.RuleName] = append(ruleViolations[v.RuleName], v)
	}
	for _, rule := range rules {
		writeRuleViolations(rule, ruleViolations[rule.Name])
	}
	renderValidationErrors(result.ScriptErrors, result.LoadErrors)
	writeValidationsSummary(len(rules), errorCount, warningCount, result.HasErrors())
	if errorCount > 0 || result.HasErrors() {
		return errors.NewExitError(1)
	}
	return nil
}

func writeRuleViolations(rule metamodel.ValidationRule, vs []analysis.ValidationViolation) {
	if len(vs) == 0 {
		return
	}
	if rule.GetSeverity() == "error" {
		out.WriteError("%s (%d):", rule.Description, len(vs))
	} else {
		out.WriteWarning("%s (%d):", rule.Description, len(vs))
	}
	for _, v := range vs {
		out.WriteMessage("%s", formatValidationViolationLine(v))
	}
}

// formatValidationViolationLine renders one violation as the indented
// line under its rule heading.
//
// A Lua rule's per-entity Message is what makes the finding actionable —
// it says which of the rule's several possible defects this entity has —
// so it is appended after the title. Rules that return no message (every
// non-Lua rule) keep the bare "id: title" form: the rule description
// above is then the whole finding and a trailing separator would suggest
// text that isn't there.
func formatValidationViolationLine(v analysis.ValidationViolation) string {
	line := fmt.Sprintf("  %s: %s", v.EntityID, v.EntityTitle)
	if v.Message != "" {
		line += " — " + v.Message
	}
	return line
}

func writeValidationsSummary(ruleCount, errorCount, warningCount int, hasErrors bool) {
	if errorCount == 0 && warningCount == 0 && !hasErrors {
		out.WriteSuccess("All %d validation rules passed", ruleCount)
		return
	}
	if errorCount > 0 {
		out.WriteError("Found %d errors, %d warnings across %d rules",
			errorCount, warningCount, ruleCount)
	} else if warningCount > 0 {
		out.WriteWarning("Found %d warnings across %d rules", warningCount, ruleCount)
	}
}

func renderValidationErrors(scriptErrors []*lua.ScriptError, loadErrors []analysis.ValidationLoadError) {
	if len(scriptErrors) > 0 {
		out.WriteError("Validation script errors (%d):", len(scriptErrors))
		for _, se := range scriptErrors {
			out.WriteMessage("%s", formatScriptError(se))
		}
	}
	if len(loadErrors) > 0 {
		out.WriteError("Validation load errors (%d):", len(loadErrors))
		for _, le := range loadErrors {
			out.WriteMessage("  %s: %s", le.RuleName, le.Message)
		}
	}
}

// AnalyzeAllCmd runs all analyses.
type AnalyzeAllCmd struct{}

// Run dispatches `rela analyze all`.
func (c *AnalyzeAllCmd) Run(ctx context.Context, svc *readServices, analyzer *analysis.Service) error {
	opts, err := resolveAnalyzeOpts()
	if err != nil {
		return err
	}
	summary, err := analyzer.AnalyzeAll(ctx, *opts)
	// An incomplete scan still yields a usable summary; anything else
	// yields none.
	scanErr := err
	if err != nil && !analysis.IsIncompleteScan(err) {
		return err
	}
	if !analysis.IsIncompleteScan(scanErr) {
		scanErr = nil
	}
	if out.Format == "json" {
		if jsonErr := writeAnalyzeAllJSON(summary); jsonErr != nil {
			return jsonErr
		}
		return reportPartialScan(scanErr)
	}
	writeAnalyzeAllSummary(svc, summary)
	if sectionErr := runAnalyzeAllSections(ctx, svc, analyzer, *opts); sectionErr != nil {
		return sectionErr
	}
	return reportPartialScan(scanErr)
}

type allAnalysisSummary struct {
	Orphans                int `json:"orphans"`
	Cardinality            int `json:"cardinality"`
	States                 int `json:"states"`
	Duplicates             int `json:"duplicates"`
	UniqueViolations       int `json:"unique_violations"`
	Gaps                   int `json:"gaps"`
	Properties             int `json:"properties"`
	ValidationErrors       int `json:"validation_errors"`
	ValidationWarnings     int `json:"validation_warnings"`
	ValidationScriptErrors int `json:"validation_script_errors"`
	ValidationLoadErrors   int `json:"validation_load_errors"`
}

func writeAnalyzeAllJSON(summary *analysis.Summary) error {
	jsonSummary := allAnalysisSummary{
		Orphans:                summary.Orphans,
		Cardinality:            summary.Cardinality,
		States:                 summary.States,
		Duplicates:             summary.Duplicates,
		UniqueViolations:       summary.UniqueViolations,
		Gaps:                   summary.Gaps,
		Properties:             summary.PropertyErrors,
		ValidationErrors:       summary.ValidationErrors,
		ValidationWarnings:     summary.ValidationWarnings,
		ValidationScriptErrors: summary.ValidationScriptErrors,
		ValidationLoadErrors:   summary.ValidationLoadErrors,
	}
	validationFailures := summary.ValidationScriptErrors + summary.ValidationLoadErrors
	totalIssues := summary.Orphans + summary.Cardinality + summary.States + summary.Duplicates +
		summary.UniqueViolations + summary.Gaps + summary.PropertyErrors +
		summary.ValidationErrors + validationFailures
	status := "success"
	message := "All analyses passed"
	if summary.ValidationErrors > 0 || summary.PropertyErrors > 0 || validationFailures > 0 {
		status = "error"
		message = fmt.Sprintf("Found %d issues requiring attention", totalIssues)
	} else if totalIssues > 0 || summary.ValidationWarnings > 0 {
		status = "warning"
		message = fmt.Sprintf("Found %d issues and %d warnings", totalIssues, summary.ValidationWarnings)
	}
	return out.WriteAnalysisResult(output.AnalysisResult{
		Status:  status,
		Message: message,
		Count:   totalIssues + summary.ValidationWarnings,
		Details: jsonSummary,
	})
}

func writeAnalyzeAllSummary(svc *readServices, summary *analysis.Summary) {
	summaryItems := []string{
		fmt.Sprintf("Orphans: %d", summary.Orphans),
		fmt.Sprintf("Cardinality: %d", summary.Cardinality),
		fmt.Sprintf("States: %d", summary.States),
		fmt.Sprintf("Duplicates: %d", summary.Duplicates),
		fmt.Sprintf("Unique Violations: %d", summary.UniqueViolations),
		fmt.Sprintf("Gaps: %d", summary.Gaps),
		fmt.Sprintf("Properties: %d", summary.PropertyErrors),
	}
	if len(svc.Meta.Validations) > 0 {
		summaryItems = append(summaryItems,
			fmt.Sprintf("Validation Errors: %d", summary.ValidationErrors),
			fmt.Sprintf("Validation Warnings: %d", summary.ValidationWarnings))
		if summary.ValidationScriptErrors > 0 {
			summaryItems = append(summaryItems,
				fmt.Sprintf("Validation Script Errors: %d", summary.ValidationScriptErrors))
		}
		if summary.ValidationLoadErrors > 0 {
			summaryItems = append(summaryItems,
				fmt.Sprintf("Validation Load Errors: %d", summary.ValidationLoadErrors))
		}
	}
	out.WriteSummaryBox(summaryItems)
	out.WriteMessage("")
}

func runAnalyzeAllSections(
	ctx context.Context, svc *readServices, analyzer *analysis.Service, opts analysis.Options,
) error {
	var errs []error
	out.WriteSectionHeader("Orphan Analysis")
	if err := (&AnalyzeOrphansCmd{}).Run(ctx, analyzer); err != nil {
		errs = append(errs, fmt.Errorf("orphan analysis: %w", err))
	}
	out.WriteMessage("")
	out.WriteSectionHeader("Duplicate Analysis")
	if err := (&AnalyzeDuplicatesCmd{}).Run(ctx, analyzer); err != nil {
		errs = append(errs, fmt.Errorf("duplicate analysis: %w", err))
	}
	out.WriteMessage("")
	out.WriteSectionHeader("Unique Constraint Analysis")
	if err := (&AnalyzeUniqueCmd{}).Run(ctx, analyzer); err != nil {
		errs = append(errs, fmt.Errorf("unique analysis: %w", err))
	}
	out.WriteMessage("")
	out.WriteSectionHeader("ID Gap Analysis")
	if err := (&AnalyzeGapsCmd{}).Run(ctx, analyzer); err != nil {
		errs = append(errs, fmt.Errorf("gap analysis: %w", err))
	}
	out.WriteMessage("")
	out.WriteSectionHeader("Content States")
	// Second full states scan (the summary above already ran one) — the
	// same pre-existing compute-once follow-up as the cardinality note
	// below.
	if err := (&AnalyzeStatesCmd{}).Run(ctx, analyzer); err != nil {
		errs = append(errs, fmt.Errorf("content-state analysis: %w", err))
	}
	out.WriteMessage("")
	out.WriteSectionHeader("Cardinality Analysis")
	// Second full cardinality scan (the summary above already ran one).
	// With a flaky backend the two runs can disagree — summary success
	// followed by a section error here. Pre-existing shape; computing
	// once and passing the violations down is the follow-up fix.
	if err := (&AnalyzeCardinalityCmd{}).Run(ctx, analyzer); err != nil {
		errs = append(errs, fmt.Errorf("cardinality analysis: %w", err))
	}
	out.WriteMessage("")
	out.WriteSectionHeader("Property Validation")
	if err := runPropertyValidation(ctx, svc, opts); err != nil {
		errs = append(errs, fmt.Errorf("property validation: %w", err))
	}
	if len(svc.Meta.Validations) > 0 {
		out.WriteMessage("")
		out.WriteSectionHeader("Custom Validations")
		if err := runValidations(ctx, svc, analyzer, opts); err != nil {
			errs = append(errs, fmt.Errorf("custom validations: %w", err))
		}
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// AnalyzeSchemaCmd analyzes metamodel schema usage.
type AnalyzeSchemaCmd struct {
	Threshold int  `default:"0" help:"Show types with instance count <= threshold (0 = only unused)."`
	Cleanup   bool `help:"Remove unused types from metamodel and config files."`
	DryRun    bool `name:"dry-run" help:"Preview cleanup changes without modifying files."`
}

// Run dispatches `rela analyze schema`.
func (c *AnalyzeSchemaCmd) Run(svc *readServices) error {
	if c.Threshold < 0 {
		return stderrors.New("--threshold must be non-negative")
	}
	dataEntry := loadDataEntryConfig(svc)
	counter := schema.NewStoreCounter(context.Background(), svc.Store)
	analysisResult := schema.Analyze(svc.Meta, counter, dataEntry, c.Threshold)

	if c.Cleanup {
		return runSchemaCleanup(svc, analysisResult, c.DryRun)
	}
	return outputSchemaAnalysis(analysisResult)
}

func loadDataEntryConfig(svc *readServices) *dataentryconfig.Config {
	data, err := svc.Config.Load(context.Background(), dataentryconfig.ConfigFile)
	if err != nil {
		return nil
	}
	var cfg dataentryconfig.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	return &cfg
}

func runSchemaCleanup(svc *readServices, analysisResult *schema.Analysis, dryRun bool) error {
	plan := schema.PlanCleanup(analysisResult, filepath.Base(svc.Paths.SchemaPath))
	if plan.IsEmpty() {
		if out.Format == "json" {
			return out.WriteAnalysisResult(output.AnalysisResult{
				Status: "success", Message: "No unused types to clean up",
				Count: 0, Details: plan,
			})
		}
		out.WriteSuccess("No unused types to clean up")
		return nil
	}

	if out.Format == "json" {
		status := "success"
		message := fmt.Sprintf("Planned %d changes", plan.TotalChanges())
		if dryRun {
			message = fmt.Sprintf("Would make %d changes (dry-run)", plan.TotalChanges())
		}
		return out.WriteAnalysisResult(output.AnalysisResult{
			Status: status, Message: message,
			Count: plan.TotalChanges(), Details: plan,
		})
	}

	if dryRun {
		out.WriteMessage("Dry-run: would make the following changes:")
	} else {
		out.WriteMessage("Making the following changes:")
	}
	for _, change := range plan.MetamodelChanges {
		out.WriteMessage("  %s: %s %s", change.File, change.Action, change.Target)
	}
	for _, change := range plan.DataEntryChanges {
		out.WriteMessage("  %s: %s %s", change.File, change.Action, change.Target)
	}

	if dryRun {
		out.WriteSuccess("Would make %d changes (dry-run, no files modified)", plan.TotalChanges())
		return nil
	}

	if err := schema.ExecuteCleanup(plan, svc.Paths.SchemaPath, svc.Paths.Root, false); err != nil {
		return err
	}
	out.WriteSuccess("Made %d changes", plan.TotalChanges())
	return nil
}

func outputSchemaAnalysis(an *schema.Analysis) error {
	totalUnused := an.TotalUnused()
	totalLowUsage := an.TotalLowUsage()
	totalIssues := totalUnused + totalLowUsage
	if out.Format == "json" {
		return outputSchemaAnalysisJSON(an, totalUnused, totalLowUsage, totalIssues)
	}
	return outputSchemaAnalysisText(an, totalUnused, totalLowUsage, totalIssues)
}

func outputSchemaAnalysisJSON(an *schema.Analysis, totalUnused, totalLowUsage, totalIssues int) error {
	status := "success"
	message := "All schema types are in use"
	if totalUnused > 0 {
		status = "warning"
		message = fmt.Sprintf("Found %d unused types", totalUnused)
		if totalLowUsage > 0 {
			message = fmt.Sprintf("Found %d unused types and %d low-usage types", totalUnused, totalLowUsage)
		}
	} else if totalLowUsage > 0 {
		status = "warning"
		message = fmt.Sprintf("Found %d low-usage types", totalLowUsage)
	}
	return out.WriteAnalysisResult(output.AnalysisResult{
		Status: status, Message: message, Count: totalIssues, Details: an,
	})
}

func outputSchemaAnalysisText(an *schema.Analysis, totalUnused, totalLowUsage, totalIssues int) error {
	if totalIssues == 0 {
		out.WriteSuccess("All schema types are in use")
		return nil
	}
	outputUnusedTypes("Unused Entity Types", an.UnusedEntityTypes)
	outputUnusedTypes("Unused Relation Types", an.UnusedRelationTypes)
	outputUnusedCustomTypes(an.UnusedCustomTypes)
	outputLowUsageTypes("Low-Usage Entity Types", an.LowUsageEntityTypes)
	outputLowUsageTypes("Low-Usage Relation Types", an.LowUsageRelationTypes)

	out.WriteWarning("Found %d unused types and %d low-usage types", totalUnused, totalLowUsage)
	if totalUnused > 0 {
		out.WriteMessage("Run with --cleanup to remove unused types")
	}
	return nil
}

func outputUnusedTypes(header string, usages []schema.TypeUsage) {
	if len(usages) == 0 {
		return
	}
	out.WriteSectionHeader(header)
	for _, usage := range usages {
		if len(usage.References) == 0 {
			out.WriteWarning("  %s (0 instances, can be removed)", usage.Name)
		} else {
			out.WriteWarning("  %s (0 instances, %d references)", usage.Name, len(usage.References))
			for _, ref := range usage.References {
				out.WriteMessage("    - %s: %s (%s)", ref.File, ref.Section, ref.Kind)
			}
		}
	}
	out.WriteMessage("")
}

func outputUnusedCustomTypes(usages []schema.TypeUsage) {
	if len(usages) == 0 {
		return
	}
	out.WriteSectionHeader("Unused Custom Types")
	for _, usage := range usages {
		out.WriteWarning("  %s (not referenced, can be removed)", usage.Name)
	}
	out.WriteMessage("")
}

func outputLowUsageTypes(header string, usages []schema.TypeUsage) {
	if len(usages) == 0 {
		return
	}
	out.WriteSectionHeader(header)
	for _, usage := range usages {
		out.WriteMessage("  %s (%d instances)", usage.Name, usage.Count)
	}
	out.WriteMessage("")
}
