package dataentry

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/appbuild/appbuildtest"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/project"
	"github.com/Sourcehaven-BV/rela/internal/storage"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

// newAnalyzeService builds an analyzeService directly from a fixture + meta,
// with no App. Since the extraction (TKT-N26KLB M5.1) made analyzeService
// depend only on {store, tracer, validator} plus a per-call meta, the analysis
// tests construct it in isolation rather than standing up a whole App.
func newAnalyzeService(t *testing.T, f *fixture, meta *metamodel.Metamodel) analyzeService {
	t.Helper()
	fs := storage.NewMemFS()
	ctx := &project.Context{Root: "/project", CacheDir: "/project/.rela"}
	if err := fs.MkdirAll(ctx.CacheDir, 0o755); err != nil {
		t.Fatalf("newAnalyzeService: mkdir: %v", err)
	}
	svc := appbuildtest.New(meta, appbuildtest.WithFS(fs, ctx))
	seedFromFixture(svc.Store(), f)
	return analyzeService{reads: visibility.Unrestricted(svc.Store()), relCounts: svc.Store(), tracer: svc.Tracer(), validator: svc.Validator()}
}

func TestAnalyzeOrphans(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket": {Properties: map[string]metamodel.PropertyDef{
				"title": {Type: "string", Required: true},
			}},
		},
	}

	g.AddNode(&entity.Entity{ID: "T-001", Type: "ticket", Properties: map[string]any{"title": "Orphan"}})
	g.AddNode(&entity.Entity{ID: "T-002", Type: "ticket", Properties: map[string]any{"title": "Connected A"}})
	g.AddNode(&entity.Entity{ID: "T-003", Type: "ticket", Properties: map[string]any{"title": "Connected B"}})
	g.AddEdge(&entity.Relation{From: "T-002", Type: "blocks", To: "T-003"})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeOrphans(context.Background(), meta)

	if section.Name != "Orphans" {
		t.Errorf("expected section name 'Orphans', got %q", section.Name)
	}
	if len(section.Issues) != 1 {
		t.Fatalf("expected 1 orphan, got %d", len(section.Issues))
	}
	if section.Issues[0].EntityID != "T-001" {
		t.Errorf("expected orphan T-001, got %s", section.Issues[0].EntityID)
	}
	if section.Issues[0].Severity != "warning" {
		t.Errorf("expected severity 'warning', got %q", section.Issues[0].Severity)
	}
}

func TestAnalyzeOrphans_NoOrphans(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket": {Properties: map[string]metamodel.PropertyDef{}},
		},
	}

	g.AddNode(&entity.Entity{ID: "T-001", Type: "ticket", Properties: map[string]any{}})
	g.AddNode(&entity.Entity{ID: "T-002", Type: "ticket", Properties: map[string]any{}})
	g.AddEdge(&entity.Relation{From: "T-001", Type: "blocks", To: "T-002"})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeOrphans(context.Background(), meta)

	if len(section.Issues) != 0 {
		t.Errorf("expected 0 orphans, got %d", len(section.Issues))
	}
}

func TestAnalyzeDuplicates(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket": {Properties: map[string]metamodel.PropertyDef{
				"title": {Type: "string", Required: true},
			}},
		},
	}

	g.AddNode(&entity.Entity{ID: "T-001", Type: "ticket", Properties: map[string]any{"title": "Setup CI"}})
	g.AddNode(&entity.Entity{ID: "T-002", Type: "ticket", Properties: map[string]any{"title": "setup ci"}})
	g.AddNode(&entity.Entity{ID: "T-003", Type: "ticket", Properties: map[string]any{"title": "Unique"}})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeDuplicates(context.Background(), meta)

	if len(section.Issues) != 2 {
		t.Fatalf("expected 2 duplicate issues (for the pair), got %d", len(section.Issues))
	}
	// Both should reference the same title group
	if section.Issues[0].EntityID != "T-001" || section.Issues[1].EntityID != "T-002" {
		t.Errorf("expected T-001 and T-002, got %s and %s",
			section.Issues[0].EntityID, section.Issues[1].EntityID)
	}
}

func TestAnalyzeDuplicates_NoDuplicates(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket": {Properties: map[string]metamodel.PropertyDef{
				"title": {Type: "string"},
			}},
		},
	}

	g.AddNode(&entity.Entity{ID: "T-001", Type: "ticket", Properties: map[string]any{"title": "Alpha"}})
	g.AddNode(&entity.Entity{ID: "T-002", Type: "ticket", Properties: map[string]any{"title": "Beta"}})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeDuplicates(context.Background(), meta)

	if len(section.Issues) != 0 {
		t.Errorf("expected 0 duplicate issues, got %d", len(section.Issues))
	}
}

func TestAnalyzeGaps(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket": {IDPrefix: "T-", Properties: map[string]metamodel.PropertyDef{}},
		},
	}

	g.AddNode(&entity.Entity{ID: "T-001", Type: "ticket", Properties: map[string]any{}})
	// T-002 missing
	g.AddNode(&entity.Entity{ID: "T-003", Type: "ticket", Properties: map[string]any{}})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeGaps(context.Background(), meta)

	if len(section.Issues) != 1 {
		t.Fatalf("expected 1 gap issue, got %d", len(section.Issues))
	}
	issue := section.Issues[0]
	if issue.Message != "Missing ID: T-002" {
		t.Errorf("expected 'Missing ID: T-002', got %q", issue.Message)
	}
	// EntityType is populated from the prefix → type map so the
	// data-entry SPA can render a type badge on gap rows. EntityID
	// stays empty (no entity exists at that ID), which keeps the row
	// non-clickable.
	if issue.EntityType != "ticket" {
		t.Errorf("expected EntityType 'ticket', got %q", issue.EntityType)
	}
	if issue.EntityID != "" {
		t.Errorf("expected empty EntityID for gap row, got %q", issue.EntityID)
	}
}

func TestAnalyzeGaps_ManualIDsSkipped(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"component": {IDType: "manual", IDPrefix: "C-", Properties: map[string]metamodel.PropertyDef{}},
		},
	}

	g.AddNode(&entity.Entity{ID: "C-001", Type: "component", Properties: map[string]any{}})
	g.AddNode(&entity.Entity{ID: "C-005", Type: "component", Properties: map[string]any{}})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeGaps(context.Background(), meta)

	if len(section.Issues) != 0 {
		t.Errorf("expected 0 gap issues for manual ID type, got %d", len(section.Issues))
	}
}

// A manual-ID type whose ids carry more dashes than its declared prefix is
// still skipped: `tw-` parses `tw-basecamp-10` to the prefix `tw-basecamp-`,
// which a prefix match never excluded.
func TestAnalyzeGaps_ManualIDsSkippedByType(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"twin":   {IDType: "manual", IDPrefix: "tw-", Properties: map[string]metamodel.PropertyDef{}},
			"ticket": {IDPrefix: "T-", Properties: map[string]metamodel.PropertyDef{}},
		},
	}
	g.AddNode(&entity.Entity{ID: "tw-basecamp-10", Type: "twin", Properties: map[string]any{}})
	g.AddNode(&entity.Entity{ID: "tw-basecamp-15", Type: "twin", Properties: map[string]any{}})
	g.AddNode(&entity.Entity{ID: "T-001", Type: "ticket", Properties: map[string]any{}})
	g.AddNode(&entity.Entity{ID: "T-003", Type: "ticket", Properties: map[string]any{}})

	section := newAnalyzeService(t, g, meta).analyzeGaps(context.Background(), meta)

	if len(section.Issues) != 1 || section.Issues[0].Message != "Missing ID: T-002" {
		t.Fatalf("want only Missing ID: T-002, got %+v", section.Issues)
	}
}

// A huge gap stops at the section cap instead of enumerating every number
// first and trimming afterwards.
func TestAnalyzeGaps_HugeGapStopsAtTheCap(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket": {IDPrefix: "T-", Properties: map[string]metamodel.PropertyDef{}},
		},
	}
	g.AddNode(&entity.Entity{ID: "T-001", Type: "ticket", Properties: map[string]any{}})
	g.AddNode(&entity.Entity{ID: "T-5000000", Type: "ticket", Properties: map[string]any{}})

	section := newAnalyzeService(t, g, meta).analyzeGaps(context.Background(), meta)

	if len(section.Issues) != maxSectionIssues || !section.Truncated {
		t.Fatalf("want %d issues and Truncated, got %d (truncated=%v)",
			maxSectionIssues, len(section.Issues), section.Truncated)
	}
	if section.Issues[0].Message != "Missing ID: T-002" {
		t.Errorf("first issue = %q, want Missing ID: T-002", section.Issues[0].Message)
	}
}

// Entity types with multiple ID prefixes (id_prefixes plural) must still
// resolve each prefix to the same entity type for gap-row attribution.
func TestAnalyzeGaps_MultiplePrefixes(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"feature": {
				IDPrefixes: []string{"FEAT-", "F-"},
				Properties: map[string]metamodel.PropertyDef{},
			},
		},
	}

	g.AddNode(&entity.Entity{ID: "FEAT-001", Type: "feature", Properties: map[string]any{}})
	// FEAT-002 missing
	g.AddNode(&entity.Entity{ID: "FEAT-003", Type: "feature", Properties: map[string]any{}})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeGaps(context.Background(), meta)

	if len(section.Issues) != 1 {
		t.Fatalf("expected 1 gap issue, got %d", len(section.Issues))
	}
	if section.Issues[0].EntityType != "feature" {
		t.Errorf("expected EntityType 'feature' (resolved via id_prefixes), got %q", section.Issues[0].EntityType)
	}
}

func TestAnalyzeCardinality(t *testing.T) {
	g := newFixture()
	min1 := 1
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket": {Properties: map[string]metamodel.PropertyDef{
				"title": {Type: "string"},
			}},
			"component": {Properties: map[string]metamodel.PropertyDef{
				"name": {Type: "string"},
			}},
		},
		Relations: map[string]metamodel.RelationDef{
			"implements": {
				From:        []string{"ticket"},
				To:          []string{"component"},
				MinOutgoing: &min1, // Each ticket must implement at least 1 component
			},
		},
	}

	g.AddNode(&entity.Entity{ID: "T-001", Type: "ticket", Properties: map[string]any{"title": "A"}})
	g.AddNode(&entity.Entity{ID: "T-002", Type: "ticket", Properties: map[string]any{"title": "B"}})
	g.AddNode(&entity.Entity{ID: "C-001", Type: "component", Properties: map[string]any{"name": "Auth"}})
	// Only T-001 implements C-001; T-002 has no implements relation
	g.AddEdge(&entity.Relation{From: "T-001", Type: "implements", To: "C-001"})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeCardinality(context.Background(), meta)

	if len(section.Issues) != 1 {
		t.Fatalf("expected 1 cardinality violation, got %d", len(section.Issues))
	}
	if section.Issues[0].EntityID != "T-002" {
		t.Errorf("expected violation on T-002, got %s", section.Issues[0].EntityID)
	}
	if section.Issues[0].Severity != "error" {
		t.Errorf("expected severity 'error', got %q", section.Issues[0].Severity)
	}
}

func TestAnalyzeCardinality_AllSatisfied(t *testing.T) {
	g := newFixture()
	min1 := 1
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket":    {Properties: map[string]metamodel.PropertyDef{}},
			"component": {Properties: map[string]metamodel.PropertyDef{}},
		},
		Relations: map[string]metamodel.RelationDef{
			"implements": {
				From:        []string{"ticket"},
				To:          []string{"component"},
				MinOutgoing: &min1,
			},
		},
	}

	g.AddNode(&entity.Entity{ID: "T-001", Type: "ticket", Properties: map[string]any{}})
	g.AddNode(&entity.Entity{ID: "C-001", Type: "component", Properties: map[string]any{}})
	g.AddEdge(&entity.Relation{From: "T-001", Type: "implements", To: "C-001"})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeCardinality(context.Background(), meta)

	if len(section.Issues) != 0 {
		t.Errorf("expected 0 violations, got %d", len(section.Issues))
	}
}

func TestAnalyzeProperties(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket": {
				IDPrefix: "T-",
				Properties: map[string]metamodel.PropertyDef{
					"title":  {Type: "string", Required: true},
					"status": {Type: "enum", Values: []string{"open", "closed"}},
				},
			},
		},
	}

	// Missing required title, invalid enum value
	g.AddNode(&entity.Entity{ID: "T-001", Type: "ticket", Properties: map[string]any{"status": "invalid"}})
	// Valid entity
	g.AddNode(&entity.Entity{ID: "T-002", Type: "ticket", Properties: map[string]any{"title": "Good", "status": "open"}})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeProperties(context.Background(), meta)

	if len(section.Issues) < 2 {
		t.Fatalf("expected at least 2 property errors (missing title + invalid enum), got %d", len(section.Issues))
	}
	// All issues for T-001
	for _, issue := range section.Issues {
		if issue.EntityID != "T-001" {
			t.Errorf("expected all issues for T-001, got issue for %s", issue.EntityID)
		}
		if issue.Severity != "error" {
			t.Errorf("expected severity 'error', got %q", issue.Severity)
		}
	}
}

func TestAnalyzeProperties_AllValid(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket": {
				IDPrefix: "T-",
				Properties: map[string]metamodel.PropertyDef{
					"title": {Type: "string", Required: true},
				},
			},
		},
	}

	g.AddNode(&entity.Entity{ID: "T-001", Type: "ticket", Properties: map[string]any{"title": "Valid"}})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeProperties(context.Background(), meta)

	if len(section.Issues) != 0 {
		t.Errorf("expected 0 property issues, got %d", len(section.Issues))
	}
}

func TestAnalyzeValidations(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket": {Properties: map[string]metamodel.PropertyDef{
				"status":   {Type: "string"},
				"priority": {Type: "string"},
			}},
		},
		Validations: []metamodel.ValidationRule{
			{
				Name:        "accepted-needs-priority",
				Description: "Accepted tickets must have priority",
				EntityType:  "ticket",
				When:        []string{"status=accepted"},
				Then:        []string{"priority!="},
				Severity:    "error",
			},
		},
	}

	// Accepted without priority — violation
	g.AddNode(&entity.Entity{ID: "T-001", Type: "ticket", Properties: map[string]any{"status": "accepted"}})
	// Accepted with priority — OK
	g.AddNode(&entity.Entity{ID: "T-002", Type: "ticket", Properties: map[string]any{"status": "accepted", "priority": "high"}})
	// Draft — rule doesn't apply
	g.AddNode(&entity.Entity{ID: "T-003", Type: "ticket", Properties: map[string]any{"status": "draft"}})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeValidations(context.Background(), meta)

	if len(section.Issues) != 1 {
		t.Fatalf("expected 1 validation issue, got %d", len(section.Issues))
	}
	if section.Issues[0].EntityID != "T-001" {
		t.Errorf("expected violation on T-001, got %s", section.Issues[0].EntityID)
	}
	if section.Issues[0].Severity != "error" {
		t.Errorf("expected severity 'error', got %q", section.Issues[0].Severity)
	}
}

// TestAnalyzeValidations_AttachesMissingHeaderDetail covers TKT-IL499B
// (RR-UFUX7T): a content required-headers violation must carry the
// missing exact headers through to AnalysisIssue.Detail, so the
// data-entry analyze view can reveal *which* headers are missing rather
// than only the flat rule description. Guards against the loop silently
// dropping Detail on the way from RuleResult to AnalysisIssue.
func TestAnalyzeValidations_AttachesMissingHeaderDetail(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{"ncr": {}},
		Validations: []metamodel.ValidationRule{
			{
				Name:        "ncr-standard-headers",
				Description: "NCR body moet standaardkoppen bevatten",
				EntityType:  "ncr",
				Severity:    "error",
				Content: &metamodel.ContentRule{
					RequiredHeaders: []metamodel.HeaderCheck{
						{Header: "## Beschrijving"},
						{Header: "## Oorzaak"},
					},
				},
			},
		},
	}
	g.AddNode(&entity.Entity{ID: "NCR-001", Type: "ncr", Content: "# NCR\n## Beschrijving\ntext"})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeValidations(context.Background(), meta)

	if len(section.Issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(section.Issues))
	}
	got := section.Issues[0].Detail
	if len(got) != 1 || got[0] != "## Oorzaak" {
		t.Errorf("Detail = %v, want [## Oorzaak]", got)
	}
}

// TestAnalyzeValidations_SurfacesScriptError covers RR-MG0LG: a Lua
// rule that fails to compile must appear as an error issue in the
// analyze view rather than vanishing silently. The data-entry web
// surface uses CheckRuleFull so ScriptErrors and LoadErrors no
// longer disappear on the way through GenericValidator.
func TestAnalyzeValidations_SurfacesScriptError(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket": {Properties: map[string]metamodel.PropertyDef{}},
		},
		Validations: []metamodel.ValidationRule{
			{
				Name:       "broken-rule",
				EntityType: "ticket",
				Lua:        `if oops invalid`,
			},
		},
	}
	g.AddNode(&entity.Entity{ID: "T-001", Type: "ticket", Properties: map[string]any{}})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeValidations(context.Background(), meta)

	if len(section.Issues) == 0 {
		t.Fatal("expected the script error to surface as an analysis issue, got 0 issues")
	}
	var foundScriptError bool
	for _, issue := range section.Issues {
		if issue.Severity != "error" {
			continue
		}
		if issue.Title == "broken-rule" || strings.Contains(issue.Message, "Validation script failed") {
			foundScriptError = true
			// The structured *lua.ScriptError must be attached so
			// the data-entry frontend can open ScriptErrorDialog
			// instead of just rendering the flat Message string.
			if issue.ScriptError == nil {
				t.Errorf("expected ScriptError to be populated on script-error issue; got nil")
			}
		}
	}
	if !foundScriptError {
		t.Errorf("expected an issue tagged with the broken rule; got %+v", section.Issues)
	}
}

// TestAnalyzeValidations_SurfacesLoadError covers RR-MG0LG for the
// load-error path: a `lua_file:` rule whose script does not exist
// must appear as an error issue rather than silently dropping.
func TestAnalyzeValidations_SurfacesLoadError(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket": {Properties: map[string]metamodel.PropertyDef{}},
		},
		Validations: []metamodel.ValidationRule{
			{
				Name:       "missing-script",
				EntityType: "ticket",
				LuaFile:    "no-such-file.lua",
			},
		},
	}
	g.AddNode(&entity.Entity{ID: "T-001", Type: "ticket", Properties: map[string]any{}})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeValidations(context.Background(), meta)

	if len(section.Issues) == 0 {
		t.Fatal("expected the load error to surface as an analysis issue, got 0 issues")
	}
	var foundLoadError bool
	for _, issue := range section.Issues {
		if issue.Severity == "error" && issue.Title == "missing-script" {
			foundLoadError = true
			if !strings.Contains(issue.Message, "load failed") {
				t.Errorf("expected message to mention load failed, got %q", issue.Message)
			}
		}
	}
	if !foundLoadError {
		t.Errorf("expected an issue tagged with rule name 'missing-script'; got %+v", section.Issues)
	}
	// Load failures are not Lua failures — no envelope for them.
	for _, issue := range section.Issues {
		if issue.Title == "missing-script" && issue.ScriptError != nil {
			t.Errorf("LoadError issue should not have a ScriptError envelope; got %+v", issue.ScriptError)
		}
	}
}

func TestAnalyzeValidations_NoRules(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket": {Properties: map[string]metamodel.PropertyDef{}},
		},
	}

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeValidations(context.Background(), meta)

	if len(section.Issues) != 0 {
		t.Errorf("expected 0 issues with no rules, got %d", len(section.Issues))
	}
}

func TestRunAnalysis(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket": {
				IDPrefix: "T-",
				Properties: map[string]metamodel.PropertyDef{
					"title": {Type: "string", Required: true},
				},
			},
		},
	}

	// Orphan with missing required property
	g.AddNode(&entity.Entity{ID: "T-001", Type: "ticket", Properties: map[string]any{}})

	svc := newAnalyzeService(t, g, meta)
	result := svc.runAnalysis(context.Background(), meta)

	if len(result.Sections) != 6 {
		t.Errorf("expected 6 sections, got %d", len(result.Sections))
	}
	// Should have at least 1 error (missing required property) and 1 warning (orphan)
	if result.ErrorCount < 1 {
		t.Errorf("expected at least 1 error, got %d", result.ErrorCount)
	}
	if result.WarningCount < 1 {
		t.Errorf("expected at least 1 warning, got %d", result.WarningCount)
	}
}

func TestRunAnalysis_EmptyGraph(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{},
	}

	svc := newAnalyzeService(t, g, meta)
	result := svc.runAnalysis(context.Background(), meta)

	if result.ErrorCount != 0 {
		t.Errorf("expected 0 errors for empty graph, got %d", result.ErrorCount)
	}
	if result.WarningCount != 0 {
		t.Errorf("expected 0 warnings for empty graph, got %d", result.WarningCount)
	}
}

func TestAnalysisSectionCounts(t *testing.T) {
	section := AnalysisSection{
		Issues: []AnalysisIssue{
			{Severity: "error"},
			{Severity: "warning"},
			{Severity: "error"},
			{Severity: "warning"},
			{Severity: "warning"},
		},
	}

	if section.ErrorCount() != 2 {
		t.Errorf("expected 2 errors, got %d", section.ErrorCount())
	}
	if section.WarningCount() != 3 {
		t.Errorf("expected 3 warnings, got %d", section.WarningCount())
	}
}

func TestAnalysisIssueCounts(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket": {
				IDPrefix: "T-",
				Properties: map[string]metamodel.PropertyDef{
					"title": {Type: "string", Required: true},
				},
			},
		},
	}

	// Orphan (warning) + missing required title (error)
	g.AddNode(&entity.Entity{ID: "T-001", Type: "ticket", Properties: map[string]any{}})

	svc := newAnalyzeService(t, g, meta)
	errors, warnings := svc.analysisIssueCounts(context.Background(), meta)

	if errors < 1 {
		t.Errorf("expected at least 1 error, got %d", errors)
	}
	if warnings < 1 {
		t.Errorf("expected at least 1 warning, got %d", warnings)
	}
}

func TestNormalizeTitle(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Hello World", "hello world"},
		{"  Hello   World  ", "hello world"},
		{"", ""},
		{"UPPER", "upper"},
	}
	for _, tt := range tests {
		got := normalizeTitle(tt.input)
		if got != tt.want {
			t.Errorf("normalizeTitle(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestAnalyzeValidationsWithContentRules(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"decision": {Properties: map[string]metamodel.PropertyDef{
				"title":  {Type: "string"},
				"status": {Type: "string"},
			}},
		},
		Validations: []metamodel.ValidationRule{
			{
				Name:        "decision-needs-context",
				Description: "Decisions must have Context section",
				EntityType:  "decision",
				When:        []string{"status=accepted"},
				Content: &metamodel.ContentRule{
					RequiredHeaders: []metamodel.HeaderCheck{
						{Header: "## Context"},
					},
				},
				Severity: "error",
			},
		},
	}

	// Accepted without Context section — violation
	g.AddNode(&entity.Entity{
		ID:         "DEC-001",
		Type:       "decision",
		Properties: map[string]any{"title": "Auth", "status": "accepted"},
		Content:    "# Decision\nSome text without context section",
	})
	// Accepted with Context section — OK
	g.AddNode(&entity.Entity{
		ID:         "DEC-002",
		Type:       "decision",
		Properties: map[string]any{"title": "Database", "status": "accepted"},
		Content:    "# Decision\n## Context\nWe need to decide...",
	})
	// Draft — rule doesn't apply
	g.AddNode(&entity.Entity{
		ID:         "DEC-003",
		Type:       "decision",
		Properties: map[string]any{"title": "Draft", "status": "draft"},
		Content:    "# Draft decision",
	})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeValidations(context.Background(), meta)

	if len(section.Issues) != 1 {
		t.Fatalf("expected 1 content validation issue, got %d", len(section.Issues))
	}
	if section.Issues[0].EntityID != "DEC-001" {
		t.Errorf("expected violation on DEC-001, got %s", section.Issues[0].EntityID)
	}
	if section.Issues[0].Severity != "error" {
		t.Errorf("expected severity 'error', got %q", section.Issues[0].Severity)
	}
}

func TestAnalyzeValidationsWithCombinedRules(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"decision": {Properties: map[string]metamodel.PropertyDef{
				"title":  {Type: "string"},
				"status": {Type: "string"},
				"owner":  {Type: "string"},
			}},
		},
		Validations: []metamodel.ValidationRule{
			{
				Name:        "accepted-decision-complete",
				Description: "Accepted decisions need owner and Context section",
				EntityType:  "decision",
				When:        []string{"status=accepted"},
				Then:        []string{"owner!="},
				Content: &metamodel.ContentRule{
					RequiredHeaders: []metamodel.HeaderCheck{
						{Header: "## Context"},
					},
				},
				Severity: "error",
			},
		},
	}

	// Missing owner — then fails
	g.AddNode(&entity.Entity{
		ID:         "DEC-001",
		Type:       "decision",
		Properties: map[string]any{"title": "No owner", "status": "accepted"},
		Content:    "# Decision\n## Context\nHas context",
	})
	// Has owner but missing Context — content fails
	g.AddNode(&entity.Entity{
		ID:         "DEC-002",
		Type:       "decision",
		Properties: map[string]any{"title": "No context", "status": "accepted", "owner": "Alice"},
		Content:    "# Decision\nNo context section",
	})
	// Has both — OK
	g.AddNode(&entity.Entity{
		ID:         "DEC-003",
		Type:       "decision",
		Properties: map[string]any{"title": "Complete", "status": "accepted", "owner": "Bob"},
		Content:    "# Decision\n## Context\nAll good",
	})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeValidations(context.Background(), meta)

	if len(section.Issues) != 2 {
		t.Fatalf("expected 2 violations (DEC-001 and DEC-002), got %d", len(section.Issues))
	}

	// Check both violations are present
	ids := make(map[string]bool)
	for _, issue := range section.Issues {
		ids[issue.EntityID] = true
	}
	if !ids["DEC-001"] {
		t.Error("expected violation on DEC-001")
	}
	if !ids["DEC-002"] {
		t.Error("expected violation on DEC-002")
	}
}

func TestAnalyzeValidationsWithChecklistRule(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"checklist": {Properties: map[string]metamodel.PropertyDef{
				"title":  {Type: "string"},
				"status": {Type: "string"},
			}},
		},
		Validations: []metamodel.ValidationRule{
			{
				Name:        "done-checklist-complete",
				Description: "Done checklists must have all items checked",
				EntityType:  "checklist",
				When:        []string{"status=done"},
				Content: &metamodel.ContentRule{
					Checklist: &metamodel.ChecklistRule{
						AllChecked: true,
					},
				},
				Severity: "error",
			},
		},
	}

	// Done with unchecked items — violation
	g.AddNode(&entity.Entity{
		ID:         "CHK-001",
		Type:       "checklist",
		Properties: map[string]any{"title": "Incomplete", "status": "done"},
		Content:    "- [x] Done item\n- [ ] Not done item",
	})
	// Done with all checked — OK
	g.AddNode(&entity.Entity{
		ID:         "CHK-002",
		Type:       "checklist",
		Properties: map[string]any{"title": "Complete", "status": "done"},
		Content:    "- [x] Done 1\n- [x] Done 2",
	})
	// In-progress — rule doesn't apply
	g.AddNode(&entity.Entity{
		ID:         "CHK-003",
		Type:       "checklist",
		Properties: map[string]any{"title": "WIP", "status": "in-progress"},
		Content:    "- [ ] Not started",
	})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeValidations(context.Background(), meta)

	if len(section.Issues) != 1 {
		t.Fatalf("expected 1 checklist validation issue, got %d", len(section.Issues))
	}
	if section.Issues[0].EntityID != "CHK-001" {
		t.Errorf("expected violation on CHK-001, got %s", section.Issues[0].EntityID)
	}
	if section.Issues[0].Severity != "error" {
		t.Errorf("expected severity 'error', got %q", section.Issues[0].Severity)
	}
}

func TestAnalyzeValidationsWithChecklistAllowSkipped(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"checklist": {Properties: map[string]metamodel.PropertyDef{
				"title":  {Type: "string"},
				"status": {Type: "string"},
			}},
		},
		Validations: []metamodel.ValidationRule{
			{
				Name:        "done-checklist-allow-skipped",
				Description: "Done checklists must have all items checked or skipped",
				EntityType:  "checklist",
				When:        []string{"status=done"},
				Content: &metamodel.ContentRule{
					Checklist: &metamodel.ChecklistRule{
						AllChecked:   true,
						AllowSkipped: true,
					},
				},
				Severity: "error",
			},
		},
	}

	// Done with skipped item — OK
	g.AddNode(&entity.Entity{
		ID:         "CHK-001",
		Type:       "checklist",
		Properties: map[string]any{"title": "Skipped", "status": "done"},
		Content:    "- [x] Done item\n- [x] ~~Skipped item~~ (N/A: reason)",
	})
	// Done with unchecked non-skipped — violation
	g.AddNode(&entity.Entity{
		ID:         "CHK-002",
		Type:       "checklist",
		Properties: map[string]any{"title": "Incomplete", "status": "done"},
		Content:    "- [x] Done item\n- [ ] Not done, not skipped",
	})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeValidations(context.Background(), meta)

	if len(section.Issues) != 1 {
		t.Fatalf("expected 1 checklist validation issue, got %d", len(section.Issues))
	}
	if section.Issues[0].EntityID != "CHK-002" {
		t.Errorf("expected violation on CHK-002, got %s", section.Issues[0].EntityID)
	}
}

// TestRunAnalysisSectionNames pins the ordered list of section names that
// runAnalysis() publishes. These names are load-bearing across the wire:
// the data-entry SPA (frontend/src/views/AnalyzeView.vue CHECK_TYPES) keys
// off them to render check-cards, and the e2e suite
// (e2e/tests/fixtures.ts ANALYSIS_CHECKS) asserts the same ordered list.
// Renaming a section here without updating both consumers silently
// regresses the GH#785 hidden-card bug.
func TestRunAnalysisSectionNames(t *testing.T) {
	meta := &metamodel.Metamodel{}
	svc := newAnalyzeService(t, newFixture(), meta)
	result := svc.runAnalysis(context.Background(), meta)
	got := make([]string, len(result.Sections))
	for i, s := range result.Sections {
		got[i] = s.Name
	}
	want := []string{"Properties", "Cardinality", "Validations", "Orphans", "Duplicates", "ID Gaps"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("runAnalysis section names changed: got %v, want %v.\n"+
			"If this is intentional, update frontend/src/views/AnalyzeView.vue\n"+
			"CHECK_TYPES and e2e/tests/fixtures.ts ANALYSIS_CHECKS in lockstep.",
			got, want)
	}
}

// TestAnalyzeValidations_CarriesLuaRuleMessage pins that a Lua rule's
// per-entity message reaches the browser surface as RuleMessage while
// Message keeps the rule's own description. The two answer different
// questions — "which rule fired" vs "what is wrong with THIS entity" —
// and the view shows both, so neither may overwrite the other.
func TestAnalyzeValidations_CarriesLuaRuleMessage(t *testing.T) {
	g := newFixture()
	meta := &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"procedure": {Properties: map[string]metamodel.PropertyDef{
				"task": {Type: "string"},
			}},
		},
		Validations: []metamodel.ValidationRule{
			{
				Name:        "procedure-needs-task",
				Description: "Elke procedure moet een terugkerende taak hebben",
				EntityType:  "procedure",
				Severity:    "warning",
				Lua: `
				local task = entity.properties.task
				if task == nil or task == "" then
					return { message = "geen terugkerende taak gekoppeld" }
				end
				if task == "exhausted" then
					return { message = "alleen uitgeputte taken; plan een nieuwe" }
				end
				return nil
			`,
			},
		},
	}

	g.AddNode(&entity.Entity{ID: "PROCEDURE-91XS", Type: "procedure", Properties: map[string]any{}})
	g.AddNode(&entity.Entity{ID: "PROCEDURE-MCBL", Type: "procedure", Properties: map[string]any{"task": "exhausted"}})
	g.AddNode(&entity.Entity{ID: "PROCEDURE-OK", Type: "procedure", Properties: map[string]any{"task": "monthly"}})

	svc := newAnalyzeService(t, g, meta)
	section := svc.analyzeValidations(context.Background(), meta)

	if len(section.Issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(section.Issues))
	}
	want := map[string]string{
		"PROCEDURE-91XS": "geen terugkerende taak gekoppeld",
		"PROCEDURE-MCBL": "alleen uitgeputte taken; plan een nieuwe",
	}
	for _, issue := range section.Issues {
		if got := issue.RuleMessage; got != want[issue.EntityID] {
			t.Errorf("%s RuleMessage = %q, want %q", issue.EntityID, got, want[issue.EntityID])
		}
		if issue.Message != "Elke procedure moet een terugkerende taak hebben" {
			t.Errorf("%s Message = %q, want the rule description", issue.EntityID, issue.Message)
		}
	}
}
