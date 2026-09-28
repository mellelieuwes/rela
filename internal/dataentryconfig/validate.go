package dataentryconfig

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/natsort"
)

// Valid top-level keys in data-entry.yaml
var validTopLevelKeys = map[string]bool{
	"version":      true,
	"app":          true,
	"git":          true,
	"styles":       true,
	"forms":        true,
	"lists":        true,
	"views":        true,
	"entity_views": true,
	"kanbans":      true,
	"calendars":    true,
	"gantts":       true,
	"documents":    true,
	"feeds":        true,
	"caldav":       true,
	"dashboard":    true,
	"commands":     true,
	"actions":      true,
	"webhooks":     true,
	"navigation":   true,
	"palette":      true,

	"next_action_bands": true,
	"next_actions":      true,
}

// Known typos with suggestions
var knownTypos = map[string]string{
	"form":        "forms",
	"list":        "lists",
	"view":        "views",
	"kanban":      "kanbans",
	"calendar":    "calendars",
	"gantt":       "gantts",
	"command":     "commands",
	"style":       "styles",
	"nav":         "navigation",
	"navaigation": "navigation",
}

// Valid filter operators for list/kanban static filters. This is the UI
// operator set the SPA can translate to a wire operator the API evaluates
// (utils/filters.ts OPERATOR_MAP → eq/ne/contains/in/lt/lte/gt/gte) — the
// docs table in docs/data-entry.md "Static Filters" is the same set. `=~`
// (regex, from search/calfeed/CLI filter syntax) was wrongly allowed here
// for months while no layer below could evaluate it: the SPA degraded it
// to `eq` and the config's list silently showed zero rows.
var validFilterOperators = map[string]bool{
	"=":  true,
	"==": true, // alias for "="
	"!=": true,
	"~":  true, // substring, case-insensitive
	"<":  true,
	"<=": true,
	">":  true,
	">=": true,
	"in": true, // comma-separated list, matches any
}

var validSortDirections = map[string]bool{
	"":     true, // default (asc)
	"asc":  true,
	"desc": true,
}

// DisplayNested is the section display mode that renders a two-level
// parent→child tree.
//
// Exported because internal/dataentry matches on it when building the section,
// and one wire value should not be spelled twice across a package boundary.
const DisplayNested = "nested"

// Valid display modes for view sections
var validSectionDisplayModes = map[string]bool{
	"properties":  true,
	"content":     true,
	"table":       true,
	"list":        true,
	"cards":       true,
	"breakdown":   true,
	DisplayNested: true,
}

// Valid render modes for a view section and its fields (TKT-HOIX1). Empty is
// also accepted everywhere and means "inherit", resolving to RenderDisplay.
var validSectionRenderModes = map[string]bool{
	RenderDisplay: true,
	RenderInput:   true,
}

// Section display modes that actually render `fields`. `render` on any other
// mode is inert — warned about, not rejected (RR-675AA0), so switching a
// section's display mode mid-edit isn't a hard config-load failure.
//
// `content` is deliberately absent even though the server BUILDS fields for it:
// a traverse-sourced content section shares a builder arm with `cards`
// (`sections.go`, `case "content", "cards":`), so its rows carry resolved
// SectionFieldData on the wire. The SPA's content-card template renders only
// the markdown body and ignores them, so those fields are inert payload. This
// map tracks what is RENDERED, which is what an operator setting `render:`
// cares about — not what the builder happens to populate. Do not "fix" the
// mismatch by adding `content` here; that would suppress a warning the
// operator needs.
var sectionDisplayModesRenderingFields = map[string]bool{
	"properties": true,
	"list":       true,
	"cards":      true,
}

// sectionFieldWidgetTypes is the widget → accepted-property-types table for a
// view section field's `widget:` override (TKT-3R7RF3).
//
// This is an INDEPENDENT literal, deliberately not derived from
// Metamodel.ResolveWidgetFromType (RR-Z0GGTO). That function is the table-cell
// resolver and has no `file` case — deriving from it would reject
// `widget: file` on a `file` property, which is legal here. It also predates
// the list/values precedence the SPA applies. Its godoc calls itself the single
// source of truth for type→widget; on the section path that is not accurate,
// and the two have already drifted.
//
// The authority this DOES mirror is the SPA registry's supportedPropertyTypes
// (frontend/src/widgets/registry.ts). The two are kept honest by a paired
// fixture asserted from both languages — see widgetTableFixture in
// widget_table_test.go and the matching Vitest test.
//
// This map is only the TYPE half of the rule. It is NOT sufficient on its own:
// the SPA's defaultWidgetFor dispatches on (list, values, type) IN THAT ORDER
// (registry.ts:19-28, an order its own comment marks load-bearing per
// RR-0Z1P6), so a table keyed on type alone is narrower than the dispatch it
// claims to mirror. widgetAcceptsProperty applies the higher-precedence rules
// first; see its godoc.
var sectionFieldWidgetTypes = map[string][]string{
	WidgetText:        {metamodel.PropertyTypeString},
	WidgetTextarea:    {metamodel.PropertyTypeString},
	WidgetNumber:      {metamodel.PropertyTypeInteger},
	WidgetCheckbox:    {metamodel.PropertyTypeBoolean},
	WidgetDate:        {metamodel.PropertyTypeDate},
	WidgetDatetime:    {metamodel.PropertyTypeDatetime},
	WidgetSelect:      {metamodel.PropertyTypeEnum, metamodel.PropertyTypeString},
	WidgetMultiSelect: {metamodel.PropertyTypeEnum, metamodel.PropertyTypeString},
	WidgetRrule:       {metamodel.PropertyTypeRrule},
	WidgetFile:        {metamodel.PropertyTypeFile},
}

// widgetAcceptsProperty reports whether a widget can render a property, and if
// not, why — the reason becomes the operator's error message.
//
// The order of the checks mirrors the SPA's defaultWidgetFor dispatch
// (registry.ts:19-28), because a validator that models a narrower key than the
// renderer will accept config the renderer then handles differently:
//
//  1. `list: true` → only multi-select. This is the highest-precedence rule in
//     the SPA and the one with teeth: a list property rendered through, say,
//     TextareaWidget goes through useStringValue, so the array is flattened to
//     a string and the auto-save PATCHes a SCALAR over a list. That is silent
//     data corruption authorized by a config line the server called valid.
//  2. a value set (`values:`, or a custom type with values) → only the
//     select-family. A free-text widget over a constrained set lets an operator
//     type anything; the write validator would reject it later, but the config
//     should not have promised the control in the first place.
//  3. otherwise the plain type table above.
//
// Custom types are resolved by LOOKUP in meta.Types, not by excluding a
// hardcoded list of built-ins. An earlier version negated a builtin-name list,
// which accepted any undeclared type name as enum-like and could not tell a
// value-less custom type from a real enum.
func widgetAcceptsProperty(
	widget string, pd metamodel.PropertyDef, meta *metamodel.Metamodel,
) (ok bool, reason string) {
	if pd.List {
		if widget == WidgetMultiSelect {
			return true, ""
		}
		return false, fmt.Sprintf(
			"%q is a list property, which only %q can render", widget, WidgetMultiSelect)
	}
	if widgetPropertyHasValues(pd, meta) {
		if widget == WidgetSelect || widget == WidgetMultiSelect {
			return true, ""
		}
		return false, fmt.Sprintf(
			"the property has a fixed value set, which only %q or %q can render",
			WidgetSelect, WidgetMultiSelect)
	}
	accepted := sectionFieldWidgetTypes[widget]
	if slices.Contains(accepted, pd.Type) {
		return true, ""
	}
	return false, fmt.Sprintf("widget %q accepts: %s", widget, strings.Join(accepted, ", "))
}

// widgetPropertyHasValues reports whether a property is constrained to a fixed
// value set — either inline (`values:`) or via a custom type declared under
// `types:` that carries values. Mirrors ResolveWidgetFromType's own membership
// test (schema_output.go), which likewise requires len(Values) > 0 so a
// value-less custom type is not mistaken for an enum.
func widgetPropertyHasValues(pd metamodel.PropertyDef, meta *metamodel.Metamodel) bool {
	if len(pd.Values) > 0 {
		return true
	}
	if meta == nil {
		return false
	}
	ct, ok := meta.Types[pd.Type]
	return ok && len(ct.Values) > 0
}

// validateSectionFieldWidget checks each field's `widget:` override: the name
// must be registered, and it must accept the property's declared type.
//
// Called OUTSIDE the source-resolution guards for the name check, which needs
// no metamodel knowledge (the RR-4ICH8M lesson). The type-compatibility half
// necessarily needs the property's definition, so it is skipped — not failed —
// when the property is unknown; inertWidgetWarnings reports that case instead.
//
// `widget: file` is additionally rejected outside a `properties` section
// (RR-NGY84F): only the entry mount site passes `:attachments` to
// SectionEditForm, so a FileWidget forced into a cards/list row would render
// with no attachments at all. Failing at config load beats rendering a widget
// that cannot work.
func validateSectionFieldWidget(
	viewID string, i int, s ViewSection, eDef *metamodel.EntityDef,
	meta *metamodel.Metamodel,
) []string {
	var errs []string
	for j, f := range s.Fields {
		if f.Widget == "" {
			continue
		}
		if _, known := sectionFieldWidgetTypes[f.Widget]; !known {
			errs = append(errs, fmt.Sprintf(
				"view %q: section[%d] field[%d] has invalid widget %q (valid: %s)",
				viewID, i, j, f.Widget, strings.Join(sortedMapKeys(sectionFieldWidgetTypes), ", ")))
			continue
		}
		// Property compatibility BEFORE the display-mode rule: a field can
		// violate both, and reporting the surface rule first would send the
		// operator to fix the display mode only to hit a second, different
		// error on the next load.
		var typeErr string
		if eDef != nil {
			if pd, ok := eDef.Properties[f.Property]; ok {
				if accepted, reason := widgetAcceptsProperty(f.Widget, pd, meta); !accepted {
					typeErr = fmt.Sprintf(
						"view %q: section[%d] field[%d] sets widget %q on property %q of type %q: %s",
						viewID, i, j, f.Widget, f.Property, pd.Type, reason)
				}
			}
			// An unknown property is NOT an error here — inertWidgetWarnings
			// reports it, since there is no type to check against.
		}
		if typeErr != "" {
			errs = append(errs, typeErr)
			continue
		}
		if f.Widget == WidgetFile && s.Display != "properties" {
			errs = append(errs, fmt.Sprintf(
				"view %q: section[%d] field[%d] sets widget: file on display mode %q; "+
					"the file widget is only supported on display: properties",
				viewID, i, j, s.Display))
		}
	}
	return errs
}

// validateSectionCreate checks a section's opt-in `create:` block (TKT-R4BMJM).
//
// Everything here is an allowlist: a value not named is refused rather than
// tolerated. An operator who opts a section in has asserted something, so a
// block that cannot work is a load error rather than a silently missing button —
// a missing button gives them nothing to debug.
//
// Template EXISTENCE is deliberately not checked. This package has no
// filesystem (ValidateConfig takes only config + metamodel), and plumbing a
// templater through every caller to reach it would be a far wider change than
// the check is worth. A stale variant name degrades to the form's default
// template, which is the same behavior a form-level template typo already has.
func validateSectionCreate(
	viewID string, i int, s ViewSection, view ViewConfig, cfg *Config, meta *metamodel.Metamodel,
) []string {
	if s.Create == nil {
		return nil
	}
	var errs []string

	if f := s.Create.Flow; f != "" && f != SectionCreateFlowModal && f != SectionCreateFlowPage {
		errs = append(errs, fmt.Sprintf(
			"view %q: section[%d] create.flow %q is invalid (valid: %s, %s)",
			viewID, i, f, SectionCreateFlowModal, SectionCreateFlowPage))
	}
	for _, p := range s.Create.In {
		if p != SectionCreateInSection && p != SectionCreateInHeader {
			errs = append(errs, fmt.Sprintf(
				"view %q: section[%d] create.in contains %q (valid: %s, %s)",
				viewID, i, p, SectionCreateInSection, SectionCreateInHeader))
		}
	}

	// The relation is what a created entity gets linked by, so a section with
	// no single originating relation cannot offer the affordance at all.
	// Refused here rather than dropped at render time, because the operator
	// explicitly asked for a button they would otherwise never see.
	relation, linkAs, ok := SectionOriginRelation(view, s)
	if !ok {
		errs = append(errs, fmt.Sprintf(
			"view %q: section[%d] declares create but no single relation fills it — "+
				"create needs a section collected by one non-recursive traverse rule from entry",
			viewID, i))
		return errs
	}

	relDef, found := meta.GetRelationDef(relation)
	if !found {
		// The traverse validator already reported the unknown relation; adding
		// a second error for the same root cause would just be noise.
		return errs
	}
	reachable := relDef.To
	if linkAs == "from" {
		reachable = relDef.From
	}
	for typeName := range s.Create.Types {
		if !slices.Contains(reachable, typeName) {
			sort.Strings(reachable)
			errs = append(errs, fmt.Sprintf(
				"view %q: section[%d] create.types names %q, which relation %q cannot reach (valid: %s)",
				viewID, i, typeName, relation, strings.Join(reachable, ", ")))
			continue
		}
		// A type with no create form is dropped by the runtime derivation, so
		// an override naming one would load cleanly and then produce nothing.
		if !hasCreateForm(cfg, typeName) {
			errs = append(errs, fmt.Sprintf(
				"view %q: section[%d] create.types names %q, which has no form to create it",
				viewID, i, typeName))
		}
	}

	// A section whose relation reaches nothing creatable can never render a
	// button, so the whole block is inert — worth saying once, at the block
	// level, rather than leaving the operator to infer it.
	if !slices.ContainsFunc(reachable, func(t string) bool { return hasCreateForm(cfg, t) }) {
		sort.Strings(reachable)
		errs = append(errs, fmt.Sprintf(
			"view %q: section[%d] declares create but no type reachable by relation %q has a form to create it (reachable: %s)",
			viewID, i, relation, strings.Join(reachable, ", ")))
	}

	return errs
}

// hasCreateForm reports whether any form exists for the given entity type.
//
// Deliberately LOOSER than the runtime resolver, and only in the safe
// direction. `viewsHandler.createFormForType` prefers a non-edit form and falls
// back to an edit-mode one — which works for creation when no entity id is
// supplied (RR-KGCF61 settled that on TKT-OMUD56) — so "a form exists for this
// type" and "a form resolves for this type" agree on every input. If the
// resolver ever narrowed, this would accept a section the resolver renders
// empty; the runtime derivation is authoritative and drops the type, so the
// failure would be a missing button rather than a broken one.
func hasCreateForm(cfg *Config, entityType string) bool {
	for _, f := range cfg.Forms {
		if f.EntityType == entityType {
			return true
		}
	}
	return false
}

// validateSectionRender checks the section-level `render:` and each field's,
// independently of whether the section's source resolves (RR-4ICH8M).
func validateSectionRender(viewID string, i int, s ViewSection) []string {
	var errs []string
	if s.Render != "" && !validSectionRenderModes[s.Render] {
		errs = append(errs, fmt.Sprintf(
			"view %q: section[%d] has invalid render mode %q (valid: %s)",
			viewID, i, s.Render, joinMapKeys(validSectionRenderModes)))
	}
	for j, f := range s.Fields {
		if f.Render != "" && !validSectionRenderModes[f.Render] {
			errs = append(errs, fmt.Sprintf(
				"view %q: section[%d] field[%d] has invalid render mode %q (valid: %s)",
				viewID, i, j, f.Render, joinMapKeys(validSectionRenderModes)))
		}
	}
	return errs
}

// Valid display modes for dashboard cards
var validDashboardDisplayModes = map[string]bool{
	"count":     true,
	"table":     true,
	"breakdown": true,
}

var validCommandContexts = map[string]bool{
	"entity": true,
	"list":   true,
	"view":   true,
	"global": true,
}

var validRelationDirections = map[Direction]bool{
	"":                true, // default (outgoing)
	DirectionOutgoing: true,
	DirectionIncoming: true,
}

var validRelationWidgets = map[string]bool{
	"":                true, // default (auto-detect from cardinality)
	WidgetSelect:      true,
	WidgetMultiSelect: true,
	WidgetCards:       true,
}

// ValidateConfig performs comprehensive validation of a data-entry config.
// It returns a ConfigValidationError containing all validation issues,
// or nil if the config is valid.
func ValidateConfig(data []byte, cfg *Config, meta *metamodel.Metamodel) error {
	errs := make([]string, 0) //nolint:prealloc // total capacity unknown; aggregated from multiple helpers

	// Phase 1: Structural validation (unknown keys)
	errs = append(errs, checkUnknownKeys(data)...)

	// Phase 2: Semantic validation (cross-references, types, etc.)
	errs = append(errs, validateNavigation(cfg)...)
	errs = append(errs, validateForms(cfg, meta)...)
	errs = append(errs, validateLists(cfg, meta)...)
	errs = append(errs, validateViews(cfg, meta)...)
	errs = append(errs, validateEntityViews(cfg, meta)...)
	errs = append(errs, validateKanbans(cfg, meta)...)
	errs = append(errs, validateCalendars(cfg, meta)...)
	errs = append(errs, validateGantts(cfg, meta)...)
	errs = append(errs, validateDashboard(cfg, meta)...)
	errs = append(errs, validateCommands(cfg, meta)...)
	errs = append(errs, validateActions(cfg, meta)...)
	errs = append(errs, validateWebhooks(cfg, meta)...)
	errs = append(errs, validateApp(cfg, meta)...)
	errs = append(errs, validateDocuments(cfg)...)
	errs = append(errs, validateFeeds(cfg, meta)...)
	errs = append(errs, validateCalDAV(cfg, meta)...)
	errs = append(errs, validateStyles(cfg, meta)...)
	errs = append(errs, validateNextActions(cfg, meta)...)
	errs = append(errs, validateQueryScopes(cfg, meta)...)
	errs = append(errs, validateNavEntities(cfg, meta)...)
	errs = append(errs, validateCrossReferences(cfg)...)

	if len(errs) > 0 {
		natsort.Strings(errs)
		return &ConfigValidationError{Errors: errs}
	}
	return nil
}

// checkUnknownKeys detects unknown top-level keys in the config YAML.
func checkUnknownKeys(data []byte) []string {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil // struct unmarshal already caught this
	}

	var errs []string
	for key := range raw {
		if validTopLevelKeys[key] {
			continue
		}
		// An underscore-prefixed key holds a YAML anchor and is not config.
		// Strict key checking is what makes config typos loud, but it also
		// rejects the only place an author can PUT a shared anchor: YAML
		// resolves anchors at parse time, so the definition has to live
		// somewhere in the document, and every real key is already claimed.
		// The underscore marks intent explicitly rather than inferring it.
		//
		// Narrowed to keys that do NOT shadow a real section name: `_kanbans`
		// is far more likely to be a block someone commented out by prefixing
		// it than an anchor holder, and silently ignoring that would defeat
		// the check it is an exception to.
		if strings.HasPrefix(key, "_") && !validTopLevelKeys[strings.TrimPrefix(key, "_")] {
			continue
		}
		if suggestion, ok := knownTypos[key]; ok {
			errs = append(errs, fmt.Sprintf("unknown key %q (did you mean %q?)", key, suggestion))
		} else {
			keys := sortedMapKeys(validTopLevelKeys)
			errs = append(errs, fmt.Sprintf("unknown key %q (valid keys: %s)", key, strings.Join(keys, ", ")))
		}
	}
	return errs
}

// validateNavigation validates navigation entries.
func validateNavigation(cfg *Config) []string {
	var errs []string

	for _, nav := range cfg.Navigation {
		if nav.IsGroup() {
			for _, child := range nav.Items {
				if child.IsGroup() {
					errs = append(errs, fmt.Sprintf(
						"navigation: group %q contains nested group %q (nested groups are not supported)",
						nav.Group, child.Group))
				}
			}
		}
	}

	errs = append(errs, validateNavEntitiesPlacement(cfg.Navigation)...)

	// Validate list references in navigation
	for _, nav := range cfg.Navigation {
		errs = append(errs, validateNavEntry(nav, cfg)...)
	}

	return errs
}

func validateNavEntry(nav NavigationEntry, cfg *Config) []string {
	var errs []string

	// The name itself is checked for groups too, so a typo is reported even on
	// an entry that will also be rejected for having an icon at all.
	label := nav.Label
	if label == "" {
		label = nav.Group
	}
	if label == "" {
		label = nav.Entities
	}
	errs = append(errs, validateIconName(nav.Icon, fmt.Sprintf("navigation %q", label))...)
	errs = append(errs, validateNavEntitiesShape(nav)...)

	if nav.List != "" {
		if _, ok := cfg.Lists[nav.List]; !ok {
			errs = append(errs, fmt.Sprintf(
				"navigation: references unknown list %q", nav.List))
		}
	}

	if nav.Kanban != "" {
		if _, ok := cfg.Kanbans[nav.Kanban]; !ok {
			errs = append(errs, fmt.Sprintf(
				"navigation: references unknown kanban %q", nav.Kanban))
		}
	}
	if nav.Calendar != "" {
		if _, ok := cfg.Calendars[nav.Calendar]; !ok {
			errs = append(errs, fmt.Sprintf(
				"navigation: references unknown calendar %q", nav.Calendar))
		}
	}
	if nav.Gantt != "" {
		if _, ok := cfg.Gantts[nav.Gantt]; !ok {
			errs = append(errs, fmt.Sprintf(
				"navigation: references unknown gantt %q", nav.Gantt))
		}
	}

	if nav.Action != "" {
		if a, ok := cfg.Actions[nav.Action]; !ok {
			errs = append(errs, fmt.Sprintf(
				"navigation: references unknown action %q", nav.Action))
		} else if a.AvailableOn != nil {
			errs = append(errs, entityBoundActionRefError("navigation", nav.Action))
		}
	}

	if nav.Document != "" {
		doc, ok := cfg.Documents[nav.Document]
		switch {
		case !ok:
			errs = append(errs, fmt.Sprintf(
				"navigation: references unknown document %q", nav.Document))
		case !doc.IsStandalone():
			// An entity-anchored document needs an entry id in its URL, and a
			// sidebar entry has no entity to supply one. Rejecting at config
			// load beats emitting a link that always 400s.
			errs = append(errs, fmt.Sprintf(
				"navigation: document %q has entity_type %q so it cannot be a navigation entry "+
					"(only documents without entity_type can; they render at /document/%s)",
				nav.Document, doc.EntityType, nav.Document))
		}
	}

	if nav.IsGroup() {
		// A group is a container, not a destination — there is nothing for a
		// permission to gate, and a gated group would be ambiguous with the
		// empty-group rule (a group disappears on its own once every child is
		// filtered out). Rejecting is clearer than silently ignoring it.
		// Same reasoning as permission: a group renders as a bare section
		// title with no icon slot, so an icon here would be silently dropped.
		if nav.Icon != "" {
			errs = append(errs, fmt.Sprintf(
				"navigation: group %q cannot have an icon (a group renders as a section "+
					"title; set icon on its items instead)", nav.Group))
		}
		if nav.Permission != "" {
			errs = append(errs, fmt.Sprintf(
				"navigation: group %q cannot have a permission (set it on the items instead; "+
					"a group is hidden automatically when all its items are)", nav.Group))
		}
		for _, child := range nav.Items {
			errs = append(errs, validateNavEntry(child, cfg)...)
		}
	}

	return errs
}

// validateSidePanelSpans checks `span` on a form's side-panel section fields.
//
// Side panels reuse ViewSection/ViewSectionField and render through the same
// buildSections path as a view, so a span authored there reaches the wire — but
// no other validator descends into form.SidePanel, so it would otherwise go
// unchecked. Nil panel is the common case and yields nothing.
func validateSidePanelSpans(formID string, panel *SidePanelConfig) []string {
	if panel == nil {
		return nil
	}
	var errs []string
	for i, sec := range panel.Sections {
		// A side panel has no wire field for a nested tree
		// (v1.SidePanelSection carries Fields and Entities only), so
		// `display: nested` there would build a tree that is dropped on the
		// way out — a section with a heading and nothing in it, no error.
		// Refuse it instead. Plumbing Tree through the side panel is the
		// alternative, but nothing has asked for a tree in a 300px panel.
		if sec.Display == DisplayNested {
			errs = append(errs, fmt.Sprintf(
				"form %q: side_panel section[%d] uses display: %s, which a side panel cannot render",
				formID, i, DisplayNested))
		}
		for j, f := range sec.Fields {
			errs = append(errs, validateSpan(f.Span,
				fmt.Sprintf("form %q: side_panel section[%d] field[%d]", formID, i, j))...)
		}
	}
	return errs
}

// validateForms validates form definitions.
func validateForms(cfg *Config, meta *metamodel.Metamodel) []string {
	var errs []string

	for formID, form := range cfg.Forms {
		entDef, ok := meta.GetEntityDef(form.EntityType)
		if !ok {
			errs = append(errs, fmt.Sprintf("form %q: unknown entity type %q", formID, form.EntityType))
			continue
		}

		// A form is single-page OR a wizard, never both.
		hasFlat := len(form.Fields) > 0 || len(form.Relations) > 0
		if len(form.Steps) > 0 && hasFlat {
			errs = append(errs, fmt.Sprintf(
				"form %q: a form may define either top-level fields/relations OR steps, not both",
				formID))
		}

		// Flat (single-page) fields/relations.
		for i, f := range form.Fields {
			errs = append(errs, validateFormField(formID, "", i, f, form.EntityType, entDef, meta, false)...)
		}
		for i, r := range form.Relations {
			errs = append(errs, validateFormRelation(formID, "", i, r, form.EntityType, meta)...)
		}

		// Wizard steps: each step's fields/relations reuse the same checks.
		for si, step := range form.Steps {
			ctx := fmt.Sprintf("step[%d] ", si)
			if step.Title == "" {
				errs = append(errs, fmt.Sprintf("form %q: %shas no title", formID, ctx))
			}
			for i, f := range step.Fields {
				errs = append(errs, validateFormField(
					formID, ctx, i, f, form.EntityType, entDef, meta, step.VisibleWhen != "")...)
			}
			for i, r := range step.Relations {
				errs = append(errs, validateFormRelation(formID, ctx, i, r, form.EntityType, meta)...)
			}
		}

		// Side-panel sections carry ViewSectionField, the same struct used by
		// views — so they carry `span` too, and executeSidePanel renders them
		// through the same buildSections path. Nothing else in this file
		// descends into form.SidePanel, so without this a bad span there is
		// accepted in silence: exactly the failure validateSpan exists to
		// prevent, on a real config path.
		//
		// Scoped to span deliberately. Side-panel property names have never
		// been validated either, and fixing that is a wider behavior change
		// (it could start rejecting configs that load today) that belongs in
		// its own ticket rather than riding along with a layout PR.
		errs = append(errs, validateSidePanelSpans(formID, form.SidePanel)...)
	}

	return errs
}

// validateFormField checks one form field against the metamodel. ctx is an
// optional location prefix (e.g. "step[0] ") so wizard and flat forms share
// the rule set while keeping distinct error messages.
// stepConditional reports whether the field's enclosing wizard step carries a
// `visible_when`. A field on a conditional step can be hidden without a
// `visible_when` of its own, so `clear_when_hidden` still applies to it.
func validateFormField(
	formID, ctx string, i int, f FormField, entityType string,
	entDef *metamodel.EntityDef, meta *metamodel.Metamodel, stepConditional bool,
) []string {
	var errs []string
	if _, ok := entDef.Properties[f.Property]; !ok {
		errs = append(errs, fmt.Sprintf(
			"form %q: %sfield[%d] property %q not in metamodel for entity %q",
			formID, ctx, i, f.Property, entityType))
	}
	if len(f.Transitions) > 0 {
		if propDef, hasProp := entDef.Properties[f.Property]; hasProp {
			errs = append(errs, validateTransitions(formID, i, f, propDef, meta)...)
		}
	}
	// clear_when_hidden is allowlist-validated: a typo must not silently
	// resolve to a destructive default. Note YAML `no`/`yes` decode to the
	// literal strings "no"/"yes" here (the field is typed string, and yaml.v3
	// uses the YAML 1.2 core schema), so they land in the allowlist as
	// written; `off`/`false` do not and are rejected.
	if f.ClearWhenHidden != "" && !ValidClearWhenHidden[f.ClearWhenHidden] {
		errs = append(errs, fmt.Sprintf(
			"form %q: %sfield[%d] property %q has invalid clear_when_hidden %q (valid: %s)",
			formID, ctx, i, f.Property, f.ClearWhenHidden,
			strings.Join(sortedMapKeys(ValidClearWhenHidden), ", ")))
	}
	// A field can be hidden by its own `visible_when` OR by its enclosing
	// wizard step's. With neither, `clear_when_hidden` could never fire — that
	// is an author mistake worth reporting rather than a silently inert key.
	if f.ClearWhenHidden != "" && f.VisibleWhen == "" && !stepConditional {
		errs = append(errs, fmt.Sprintf(
			"form %q: %sfield[%d] property %q sets clear_when_hidden but neither it nor its step has a visible_when (it would never apply)",
			formID, ctx, i, f.Property))
	}
	// keep_on_add_another carries a value the USER entered across a
	// "Create & add another" reset. A hidden field has no user-entered value —
	// it never renders (FormFieldList.vue gates on `!field.hidden`) and is
	// excluded from the editable property set — so the key can express nothing
	// its `default` doesn't already say. Same class of inert-key mistake as
	// clear_when_hidden with nothing that can hide it.
	//
	// This is the ONLY coherence check on the key. `mode: edit` looks like a
	// second one and is not: Mode is unvalidated, has no Go consumer, and
	// getEditFormId treats it as "prefer this form when editing" with a
	// fallback to any form for the type — so a `mode: edit` form is still
	// reachable as a create form, and rejecting the key there would refuse a
	// working config. A wrong rejection breaks an operator; an inert key
	// merely does nothing.
	if f.KeepOnAddAnother && f.Hidden {
		errs = append(errs, fmt.Sprintf(
			"form %q: %sfield[%d] property %q sets keep_on_add_another on a hidden field (it has no entered value to keep; use default instead)",
			formID, ctx, i, f.Property))
	}
	errs = append(errs, validateSpan(f.Span, fmt.Sprintf("form %q: %sfield[%d]", formID, ctx, i))...)
	return errs
}

// validateFormRelation checks one form relation against the metamodel. ctx is
// an optional location prefix (see validateFormField).
func validateFormRelation(
	formID, ctx string, i int, r FormRelation, entityType string, meta *metamodel.Metamodel,
) []string {
	var errs []string

	// A span on a relation is a config mistake, not a layout instruction:
	// RelationCards / RelationPicker never read it, so it would be silently
	// discarded. Saying so beats leaving the author to conclude the feature
	// is broken. See FormRelation.Span.
	if r.Span != 0 {
		errs = append(errs, fmt.Sprintf(
			"form %q: %srelation[%d] cannot have a span (relation widgets always take the full row)",
			formID, ctx, i))
	}

	relDef, ok := meta.GetRelationDef(r.Relation)
	switch {
	case ok:
		// An absent `direction:` is inferred from the metamodel when the form's
		// entity type sits on exactly one side. When it sits on BOTH (a
		// self-referencing relation like `depends-on`), outgoing and incoming
		// are both meaningful and mean opposite things, so the author must say
		// which — there is no safe default to fall back on.
		_, res := InferDirection(entityType, r.Relation, meta)
		ambiguous := r.Direction == "" && res == DirectionAmbiguous
		if ambiguous {
			errs = append(errs, AmbiguousDirectionError(
				fmt.Sprintf("form %q: %srelation[%d]", formID, ctx, i), entityType, r.Relation))
		}
		// Canonical name resolved — check that the form's entity type is on
		// the correct side of the edge for the chosen direction. Wrong-side
		// configs are silently broken otherwise: the widget searches the wrong
		// target type and never shows existing edges. Skipped when ambiguous:
		// without a direction there is no side to check against, and the error
		// above is the actionable one.
		if !ambiguous {
			errs = append(errs, validateFormRelationSide(formID, i, entityType, r, relDef, meta)...)
		}
	default:
		if canonical, isInverse := meta.InverseOwner(r.Relation); isInverse {
			errs = append(errs, fmt.Sprintf(
				"form %q: %srelation[%d] uses inverse name %q; use `relation: %s` with `direction: incoming` to bind the inverse of %q",
				formID, ctx, i, r.Relation, canonical, canonical))
		} else {
			errs = append(errs, fmt.Sprintf(
				"form %q: %srelation[%d] references unknown relation %q",
				formID, ctx, i, r.Relation))
		}
	}

	if !validRelationDirections[r.Direction] {
		errs = append(errs, fmt.Sprintf(
			"form %q: %srelation[%d] has invalid direction %q (valid: outgoing, incoming)",
			formID, ctx, i, r.Direction))
	}

	if !validRelationWidgets[r.Widget] {
		errs = append(errs, fmt.Sprintf(
			"form %q: %srelation[%d] has invalid widget %q (valid: select, multi-select, cards)",
			formID, ctx, i, r.Widget))
	}

	return errs
}

// validateFormRelationSide checks that the form's entity type sits on
// the side of the relation that matches the chosen direction. An
// outgoing relation must be authored from a `From:` type; an incoming
// one must be authored from a `To:` type.
//
// The direction it checks against comes from InferDirection — the single
// shared rule — so an absent key resolves the same way here as it does in the
// server's config handler and the migration. Do not re-derive the side test
// locally: a second copy that drifts silently binds the wrong side, which is
// the exact bug class this file exists to prevent.
//
// When the author wrote an explicit direction and picked the wrong one, the
// message hints at flipping it. (For an ABSENT direction no hint is possible:
// inference already resolved it to whichever side the entity type is on, so a
// wrong-side error there means the type is on neither side and flipping would
// not help.)
func validateFormRelationSide(
	formID string, i int, entityType string, r FormRelation, relDef *metamodel.RelationDef, meta *metamodel.Metamodel,
) []string {
	if entityType == "" {
		return nil
	}
	dir := r.Direction
	if dir == "" {
		dir, _ = InferDirection(entityType, r.Relation, meta)
	}
	expected, opposite := relDef.From, relDef.To
	expectedSide, oppositeSide := "from", "to"
	flipDir := DirectionIncoming
	if dir.IsIncoming() {
		expected, opposite = relDef.To, relDef.From
		expectedSide, oppositeSide = "to", "from"
		flipDir = DirectionOutgoing
	}
	if slices.Contains(expected, entityType) {
		return nil
	}
	hint := ""
	if r.Direction != "" && slices.Contains(opposite, entityType) {
		hint = fmt.Sprintf(" (set `direction: %s` to bind the %s side of %q)", flipDir, oppositeSide, r.Relation)
	}
	return []string{fmt.Sprintf(
		"form %q: relation[%d] entity type %q is not a %s of relation %q (valid: %s)%s",
		formID, i, entityType, expectedSide, r.Relation, strings.Join(expected, ", "), hint)}
}

// validateTransitions checks that transition values are valid for the property's type.
func validateTransitions(
	formID string, fieldIdx int, field FormField, propDef metamodel.PropertyDef, meta *metamodel.Metamodel,
) []string {
	var errs []string

	// Get valid values for this property type
	validValues := GetValidEnumValues(propDef, meta)
	if len(validValues) == 0 {
		return errs // not an enum type, skip validation
	}

	validSet := make(map[string]bool)
	for _, v := range validValues {
		validSet[v] = true
	}

	// Check all transition keys and values
	for fromState, toStates := range field.Transitions {
		if !validSet[fromState] {
			errs = append(errs, fmt.Sprintf(
				"form %q: field[%d] transitions has invalid from-state %q",
				formID, fieldIdx, fromState))
		}
		for _, toState := range toStates {
			if !validSet[toState] {
				errs = append(errs, fmt.Sprintf(
					"form %q: field[%d] transitions has invalid to-state %q",
					formID, fieldIdx, toState))
			}
		}
	}

	return errs
}

// GetValidEnumValues returns the valid values for an enum or custom type property.
func GetValidEnumValues(propDef metamodel.PropertyDef, meta *metamodel.Metamodel) []string {
	if propDef.Type == metamodel.PropertyTypeEnum {
		return propDef.Values
	}
	// Check if it's a custom type
	if customType, ok := meta.Types[propDef.Type]; ok {
		return customType.Values
	}
	return nil
}

// validateEntityDuplicate validates one entity type's `duplicate:` block.
//
// Every named property must exist on the type, and an explicitly empty list is
// refused: absence is how a type opts out of narrowing, so an empty list would
// be a second spelling of the default — or, read the other way, a request to
// carry nothing, which the Duplicate affordance has no use for.
//
// Nil block: accepted, returns nil — absence means "carry everything visible".
func validateEntityDuplicate(
	meta *metamodel.Metamodel, entityType string, dup *DuplicateConfig,
) []string {
	if dup == nil {
		return nil
	}
	entDef, ok := meta.GetEntityDef(entityType)
	if !ok {
		// The unknown-type error is already reported by the caller; adding a
		// second error for the same cause would just be noise.
		return nil
	}
	if dup.Properties != nil && len(dup.Properties) == 0 {
		return []string{fmt.Sprintf(
			"entity_views[%q]: duplicate.properties is empty "+
				"(omit the duplicate block to carry every property)", entityType)}
	}
	var errs []string
	for _, prop := range dup.Properties {
		if _, ok := entDef.Properties[prop]; !ok {
			errs = append(errs, fmt.Sprintf(
				"entity_views[%q]: duplicate.properties names unknown property %q "+
					"for entity type %q", entityType, prop, entityType))
		}
	}
	return errs
}

// validateEntityViews validates entity_views entries: each key must be a known
// entity type, and each detail_view must reference an existing view.
func validateEntityViews(cfg *Config, meta *metamodel.Metamodel) []string {
	var errs []string
	for entityType, ev := range cfg.EntityViews {
		if _, ok := meta.GetEntityDef(entityType); !ok {
			errs = append(errs, fmt.Sprintf(
				"entity_views: unknown entity type %q", entityType))
		}
		errs = append(errs, validateEntityDuplicate(meta, entityType, ev.Duplicate)...)

		// An entry carrying only `duplicate:` is legitimate (TKT-Z8K2FS), so
		// the empty-detail_view refusal applies only when nothing else is
		// declared — otherwise the error's own advice ("omit the entry") would
		// tell an operator to delete config that is doing something.
		if ev.DetailView == "" {
			if ev.Duplicate == nil {
				errs = append(errs, fmt.Sprintf(
					"entity_views[%q]: entry declares nothing (omit the entry instead)", entityType))
			}
			continue
		}
		if _, ok := cfg.Views[ev.DetailView]; ok {
			continue
		}
		if suggestion := suggestView(ev.DetailView, cfg); suggestion != "" {
			errs = append(errs, fmt.Sprintf(
				"entity_views[%q]: references unknown view %q in detail_view (did you mean %q?)",
				entityType, ev.DetailView, suggestion))
		} else {
			errs = append(errs, fmt.Sprintf(
				"entity_views[%q]: references unknown view %q in detail_view",
				entityType, ev.DetailView))
		}
	}
	return errs
}

// validateExportRenderShape validates an `export_render:` script path's shape.
// Shape only — whether the file exists on disk is checked at app construction,
// which is where the project root is known (see dataentry.NewApp).
//
// Shared by the list and view overrides so the two cannot fail differently:
// before this, a typo'd list path failed here while the identical typo on a
// view fell through to a generic loader error at boot. kind/id name the owner
// for the message ("list"/"view").
func validateExportRenderShape(kind, id, path string) []string {
	if path == "" {
		return nil
	}
	var errs []string
	if !strings.HasSuffix(path, ".lua") {
		errs = append(errs, fmt.Sprintf(
			"%s %q: export_render must be a .lua script path, got %q", kind, id, path))
	}
	if !filepath.IsLocal(path) {
		errs = append(errs, fmt.Sprintf(
			"%s %q: export_render must be a local path under scripts/, got %q", kind, id, path))
	}
	return errs
}

// validateLists validates list definitions.
//
//nolint:gocognit // linear validation dispatcher: one independent config-vs-metamodel check per branch; splitting would scatter the rule set without lowering real complexity.
func validateLists(cfg *Config, meta *metamodel.Metamodel) []string {
	var errs []string

	for listID, list := range cfg.Lists {
		entDef, ok := meta.GetEntityDef(list.EntityType)
		if !ok {
			errs = append(errs, fmt.Sprintf("list %q: unknown entity type %q", listID, list.EntityType))
			continue
		}

		errs = append(errs, validateExportRenderShape("list", listID, list.ExportRender)...)

		// A typo here would silently open the create form in the list's own
		// world — the exact behavior the key exists to override, and
		// indistinguishable from never having set it. Same reasoning as
		// app.default_world (validateApp).
		//
		// Only the NAME is checked. Whether the target world resolves a face
		// for this type is deliberately NOT validated: linking to another
		// world always works, and a create writes the DEFAULT face regardless
		// (§9.4), so there is no combination to reject. The real gate is ACL —
		// the button renders only if the principal may read the target world,
		// which is per-principal and therefore not a load-time question.
		if w := list.CreateWorld; w != "" && w != metamodel.DefaultWorldName {
			if _, ok := meta.Worlds[w]; !ok {
				declared := make([]string, 0, len(meta.Worlds))
				for name := range meta.Worlds {
					declared = append(declared, name)
				}
				sort.Strings(declared)
				errs = append(errs, fmt.Sprintf(
					"list %q: create_world %q is not a declared world (schema.yaml declares: %s)",
					listID, w, strings.Join(declared, ", ")))
			}
		}

		// Validate columns
		for i, c := range list.Columns {
			if c.Relation != "" {
				if _, ok := meta.GetRelationDef(c.Relation); !ok {
					errs = append(errs, fmt.Sprintf(
						"list %q: column[%d] references unknown relation %q",
						listID, i, c.Relation))
				}
				errs = append(errs, CheckAmbiguousDirection(
					fmt.Sprintf("list %q: column[%d]", listID, i),
					list.EntityType, c.Relation, c.Direction, meta)...)
			} else if c.Property != "" {
				if _, ok := entDef.Properties[c.Property]; !ok {
					errs = append(errs, fmt.Sprintf(
						"list %q: column[%d] property %q not in metamodel for entity %q",
						listID, i, c.Property, list.EntityType))
				}
			}
		}

		errs = append(errs, validateSortSpecs(fmt.Sprintf("list %q", listID), entDef, list.Sort)...)

		// Validate filters
		for i, f := range list.Filters {
			if !validFilterOperators[f.Operator] {
				errs = append(errs, fmt.Sprintf(
					"list %q: filter[%d] has invalid operator %q (valid: %s)",
					listID, i, f.Operator, joinMapKeys(validFilterOperators)))
			}
			if f.HasProperty() && entity.IsEntityPropertyKey(f.Property) {
				if _, ok := entDef.Properties[f.Property]; !ok {
					errs = append(errs, fmt.Sprintf(
						"list %q: filter[%d] references unknown property %q",
						listID, i, f.Property))
				}
			}
		}

		// Validate filter_controls
		for i, fc := range list.FilterControls {
			if fc.Property == "" && fc.Relation == "" {
				errs = append(errs, fmt.Sprintf(
					"list %q: filter_controls[%d] must specify either property or relation",
					listID, i))
			}
			if fc.Property != "" {
				if _, ok := entDef.Properties[fc.Property]; !ok {
					errs = append(errs, fmt.Sprintf(
						"list %q: filter_controls[%d] references unknown property %q",
						listID, i, fc.Property))
				}
			}
			if fc.Relation != "" {
				if _, ok := meta.GetRelationDef(fc.Relation); !ok {
					errs = append(errs, fmt.Sprintf(
						"list %q: filter_controls[%d] references unknown relation %q",
						listID, i, fc.Relation))
				}
				errs = append(errs, CheckAmbiguousDirection(
					fmt.Sprintf("list %q: filter_controls[%d]", listID, i),
					list.EntityType, fc.Relation, fc.Direction, meta)...)
			}
		}
	}

	return errs
}

// CollectConfigWarnings returns non-fatal configuration issues that should be
// surfaced at load time (logged) but must NOT abort startup — unlike
// ValidateConfig, whose findings are hard errors. Kept as a pure,
// deterministically-ordered []string so the caller can log each and tests can
// assert on the set.
//
// It flags two conditions:
//
//   - A relation FilterControl declared `direction: incoming` whose list
//     entity_type is not a `to:` side of the relation. Such a filter follows
//     incoming edges into a type that is never the target, so it can only ever
//     match zero rows — a config smell, but not fatal (the filter still
//     behaves, it just filters everything out).
//   - Two lists of the same entity type configuring the same relation filter
//     with conflicting directions. The shared list pipeline is keyed by entity
//     type (not list ID), so RelationFilterDirection resolves a single winner
//     (lowest list ID); the loser's declared direction is silently ignored,
//     which the author should know about (RR-9MJRJG).
func CollectConfigWarnings(cfg *Config, meta *metamodel.Metamodel) []string {
	var warnings []string
	for _, listID := range sortedListIDs(cfg) {
		list := cfg.Lists[listID]
		for i, fc := range list.FilterControls {
			if fc.Relation == "" || !fc.Direction.IsIncoming() {
				continue
			}
			relDef, ok := meta.GetRelationDef(fc.Relation)
			if !ok {
				continue // unknown relation already caught as a hard error
			}
			if slices.Contains(relDef.To, list.EntityType) {
				continue
			}
			warnings = append(warnings, fmt.Sprintf(
				"list %q: filter_controls[%d] relation %q with direction: incoming targets entity type %q, "+
					"which is not a `to:` side of the relation (valid: %s); this filter will match no rows",
				listID, i, fc.Relation, list.EntityType, strings.Join(relDef.To, ", ")))
		}
	}
	warnings = append(warnings, conflictingRelationDirectionWarnings(cfg)...)
	warnings = append(warnings, relationPropertyNameCollisionWarnings(cfg, meta)...)
	warnings = append(warnings, viewCommandPermissionWarnings(cfg)...)
	warnings = append(warnings, inertSectionRenderWarnings(cfg)...)
	warnings = append(warnings, inertWidgetWarnings(cfg, meta)...)
	return warnings
}

// inertSectionRenderWarnings flags `render:` on a section whose display mode
// does not render fields at all (`table` uses `columns:`; `content` renders the
// body) — the key is silently inert there (RR-675AA0). Warn rather than error,
// for the same reason as viewCommandPermissionWarnings: the config is not
// wrong, and switching a section's display mode should not be a hard failure.
func inertSectionRenderWarnings(cfg *Config) []string {
	var warnings []string
	viewIDs := make([]string, 0, len(cfg.Views))
	for id := range cfg.Views {
		viewIDs = append(viewIDs, id)
	}
	sort.Strings(viewIDs)
	for _, viewID := range viewIDs {
		for i, s := range cfg.Views[viewID].Sections {
			if sectionDisplayModesRenderingFields[s.Display] {
				continue
			}
			// Name the precise origin — section-level, or the first offending
			// field index — so the operator isn't left scanning a long
			// `fields:` list. Matches the precision validateSectionRender's
			// errors already set.
			origin := ""
			if s.Render != "" {
				origin = fmt.Sprintf("section[%d]", i)
			} else {
				for j, f := range s.Fields {
					if f.Render != "" {
						origin = fmt.Sprintf("section[%d] field[%d]", i, j)
						break
					}
				}
			}
			if origin == "" {
				continue
			}
			warnings = append(warnings, fmt.Sprintf(
				"view %q: %s sets render: on display mode %q, which does not render fields; "+
					"the setting has no effect (it applies to: %s)",
				viewID, origin, s.Display, joinMapKeys(sectionDisplayModesRenderingFields)))
		}
	}
	return warnings
}

// inertWidgetWarnings flags a `widget:` override that cannot take effect
// (TKT-3R7RF3). Two inert cases, both warned rather than errored for the same
// reason as inertSectionRenderWarnings — the config is not wrong, just
// ineffective, and a display-mode switch should not be a hard load failure:
//
//   - a display mode that renders no fields at all (`table`, `content`), the
//     exact case sectionDisplayModesRenderingFields already describes;
//   - a property the metamodel does not declare. Such a field renders through
//     the SPA's routing-hint path, which resolves a widget from the value's
//     shape and takes no override name (RR-2GBB0V). Note this is NOT because
//     the field is read-only — an unschema'd field IS editable, since a
//     missing ACL verdict reads as writable. It is because there is no
//     PropertyDef to type-check the override against, so honoring it would
//     push an unvalidated widget into a live edit control.
//
// A third inert case is deliberately NOT warned: a state-machine field on
// `render: input`, where the SPA's StatusControl owns the field and ignores
// the widget. Machine-ness is a runtime, per-entity, per-principal fact
// (computeTransitions, gated on a TransitionResolver the config layer cannot
// see), so the warning is unbuildable here rather than merely unwritten
// (RR-66MT0D). It is documented in docs/data-entry.md instead. That same field
// on `render: display` DOES honor the widget, so the interaction is two-axis.
func inertWidgetWarnings(cfg *Config, meta *metamodel.Metamodel) []string {
	var warnings []string
	viewIDs := make([]string, 0, len(cfg.Views))
	for id := range cfg.Views {
		viewIDs = append(viewIDs, id)
	}
	sort.Strings(viewIDs)
	for _, viewID := range viewIDs {
		view := cfg.Views[viewID]
		collections := viewCollectionTypes(view, meta)
		for i, s := range view.Sections {
			renders := sectionDisplayModesRenderingFields[s.Display]
			eDef, sourceType := widgetSectionDef(s, collections, meta)
			for j, f := range s.Fields {
				if f.Widget == "" {
					continue
				}
				if !renders {
					warnings = append(warnings, fmt.Sprintf(
						"view %q: section[%d] field[%d] sets widget: on display mode %q, "+
							"which does not render fields; the setting has no effect "+
							"(it applies to: %s)",
						viewID, i, j, s.Display,
						joinMapKeys(sectionDisplayModesRenderingFields)))
					continue
				}
				if eDef == nil {
					// A collection whose target type cannot be determined
					// statically (a relation with several `to:` types) is
					// legal config — ValidateConfig does NOT error on it — so
					// unlike an unknown collection this case has no other
					// signal at all. The override is accepted unvalidated and
					// works or not depending on the runtime entity type.
					if ambiguousWidgetSource(s, collections) {
						warnings = append(warnings, fmt.Sprintf(
							"view %q: section[%d] field[%d] sets widget %q, but collection %q "+
								"resolves to several entity types, so the widget cannot be "+
								"checked against the property's type at load",
							viewID, i, j, f.Widget, s.Source))
					}
					continue
				}
				if _, ok := eDef.Properties[f.Property]; !ok {
					warnings = append(warnings, fmt.Sprintf(
						"view %q: section[%d] field[%d] sets widget %q on property %q, "+
							"which type %q does not declare; the override is ignored "+
							"(a widget can only be applied to a declared property)",
						viewID, i, j, f.Widget, f.Property, sourceType))
				}
			}
		}
	}
	return warnings
}

// widgetSectionDef resolves the entity def whose properties a section's fields
// name, or nil when the source does not resolve.
//
// Three distinct situations return nil, and they are NOT equivalent:
//   - no source at all — nothing to resolve;
//   - an unknown collection — ValidateConfig already errors, so staying
//     silent here avoids a duplicate report;
//   - a known collection with no statically-determinable type (a relation with
//     several `to:` types) — legal config that ValidateConfig does NOT error
//     on, so it is the one case with no other signal. ambiguousWidgetSource
//     distinguishes it and inertWidgetWarnings reports it.
func widgetSectionDef(
	s ViewSection, collections map[string]string, meta *metamodel.Metamodel,
) (def *metamodel.EntityDef, entityType string) {
	if meta == nil || s.Source == "" {
		return nil, ""
	}
	sourceType, ok := collections[s.Source]
	if !ok || sourceType == "" {
		return nil, ""
	}
	d, ok := meta.GetEntityDef(sourceType)
	if !ok {
		return nil, ""
	}
	return d, sourceType
}

// viewCollectionTypes builds the collection-name → entity-type map a view's
// `traverse:` block defines, plus the implicit "entry" collection.
//
// ValidateConfig builds the same map inline while also reporting errors, and
// an earlier version of this function was a hand-copy of that loop — which
// promptly diverged by omitting "entry", silently skipping every
// `source: entry` section. Prefer this one function; if ValidateConfig's
// building half is ever factored out, delete this and call that instead.
//
// A traversal whose relation type is unknown, or whose target cannot be
// determined statically (several `to:` types), yields an empty target type.
// Callers must treat empty as unresolvable rather than as an error — see
// ambiguousWidgetSource.
func viewCollectionTypes(view ViewConfig, meta *metamodel.Metamodel) map[string]string {
	out := make(map[string]string, len(view.Traverse)+1)
	if view.Entry.Type != "" {
		out["entry"] = view.Entry.Type
	}
	for _, t := range view.Traverse {
		if t.CollectAs == "" {
			continue
		}
		// Match ValidateConfig's precedence exactly: it assigns Follow then
		// overwrites with FollowIncoming, so the incoming name wins when an
		// author sets both (itself a separate error).
		relName := t.Follow
		if t.FollowIncoming != "" {
			relName = t.FollowIncoming
		}
		out[t.CollectAs] = determineTargetType(t, relName, meta)
	}
	return out
}

// ambiguousWidgetSource reports whether a section names a collection that
// EXISTS but whose entity type is not statically determinable. Distinguishes
// that from an unknown collection (already a hard error) and from a section
// with no source at all.
func ambiguousWidgetSource(s ViewSection, collections map[string]string) bool {
	if s.Source == "" {
		return false
	}
	t, ok := collections[s.Source]
	return ok && t == ""
}

// viewCommandPermissionWarnings flags `permission:` on a `context: view`
// command. The key is not honored for view commands — they are denied outright
// under any configured acl.yaml (TKT-MJ02AO) — so an author who sets it would
// otherwise believe they had granted access that silently does not apply.
// Warn rather than error: the config is not wrong, it is inert, and erroring
// would break projects that pre-emptively annotate their view commands.
func viewCommandPermissionWarnings(cfg *Config) []string {
	var warnings []string
	ids := make([]string, 0, len(cfg.Commands))
	for id := range cfg.Commands {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		cmd := cfg.Commands[id]
		if cmd.Context == "view" && cmd.Permission != "" {
			warnings = append(warnings, fmt.Sprintf(
				"command %q: permission %q is ignored for context: view — view commands are denied "+
					"under any configured acl.yaml and cannot yet be granted per-command",
				id, cmd.Permission))
		}
	}
	return warnings
}

// relationPropertyNameCollisionWarnings flags a relation FilterControl whose
// name also names a property of the list's entity type, with no property
// FilterControl to disambiguate. Properties (per-type) and relations (global)
// are disjoint namespaces, so nothing forbids the collision; the runtime
// resolves it in favor of the PROPERTY (safe, backward-compatible), which means
// the relation filter the author configured silently does nothing. Warn so the
// ambiguity is visible at load (RR-0HWAS0).
func relationPropertyNameCollisionWarnings(cfg *Config, meta *metamodel.Metamodel) []string {
	var warnings []string
	for _, listID := range sortedListIDs(cfg) {
		list := cfg.Lists[listID]
		entDef, ok := meta.GetEntityDef(list.EntityType)
		if !ok {
			continue
		}
		for i, fc := range list.FilterControls {
			if fc.Relation == "" {
				continue
			}
			if _, isProp := entDef.Properties[fc.Relation]; !isProp {
				continue
			}
			if cfg.HasPropertyFilterControl(list.EntityType, fc.Relation) {
				continue // an explicit property control resolves the routing
			}
			warnings = append(warnings, fmt.Sprintf(
				"list %q: filter_controls[%d] relation %q collides with a property of entity type %q; "+
					"the filter will match the PROPERTY, not the relation — rename or add a property "+
					"filter control to disambiguate",
				listID, i, fc.Relation, list.EntityType))
		}
	}
	return warnings
}

// conflictingRelationDirectionWarnings flags (entityType, relation) pairs where
// two or more lists of the same type configure the same relation filter with
// conflicting directions. RelationFilterDirection resolves to the lowest list
// ID, so any other list's declared direction is ignored at runtime.
func conflictingRelationDirectionWarnings(cfg *Config) []string {
	// Group the (listID, direction) each list declares for a given
	// (entityType, relation), in sorted-list-ID order so the winner and the
	// warning text are deterministic.
	type decl struct {
		listID    string
		direction Direction
	}
	byPair := map[string][]decl{}
	var pairOrder []string
	for _, listID := range sortedListIDs(cfg) {
		list := cfg.Lists[listID]
		for _, fc := range list.FilterControls {
			if fc.Relation == "" {
				continue
			}
			dir := DirectionOutgoing
			if fc.Direction.IsIncoming() {
				dir = DirectionIncoming
			}
			key := list.EntityType + "\x00" + fc.Relation
			if _, seen := byPair[key]; !seen {
				pairOrder = append(pairOrder, key)
			}
			byPair[key] = append(byPair[key], decl{listID: listID, direction: dir})
		}
	}

	var warnings []string
	for _, key := range pairOrder {
		decls := byPair[key]
		conflict := false
		for _, d := range decls[1:] {
			if d.direction != decls[0].direction {
				conflict = true
				break
			}
		}
		if !conflict {
			continue
		}
		parts := strings.SplitN(key, "\x00", 2)
		entityType, relation := parts[0], parts[1]
		winner := decls[0]
		var others []string
		for _, d := range decls {
			if d.listID == winner.listID {
				continue
			}
			others = append(others, fmt.Sprintf("%s=%s", d.listID, d.direction))
		}
		warnings = append(warnings, fmt.Sprintf(
			"entity type %q: relation %q filter is configured with conflicting directions across lists; "+
				"list %q (direction: %s) wins (lowest list ID), ignoring %s",
			entityType, relation, winner.listID, winner.direction, strings.Join(others, ", ")))
	}
	return warnings
}

// sortedListIDs returns the config's list IDs in deterministic order so
// warning output is stable across runs.
func sortedListIDs(cfg *Config) []string {
	ids := make([]string, 0, len(cfg.Lists))
	for id := range cfg.Lists {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// validateViews validates view definitions with their traversal rules and sections.
//
//nolint:gocognit,gocyclo,funlen // linear validation dispatcher: one independent config-vs-metamodel check per branch; splitting would scatter the rule set without lowering real complexity.
func validateViews(cfg *Config, meta *metamodel.Metamodel) []string {
	var errs []string

	// At most one view may target a given entity type. The detail screen
	// looks up views by entity type, so duplicates would be ambiguous.
	byType := map[string][]string{}
	for viewID, view := range cfg.Views {
		if view.Entry.Type == "" {
			continue
		}
		byType[view.Entry.Type] = append(byType[view.Entry.Type], viewID)
	}
	for entityType, viewIDs := range byType {
		if len(viewIDs) <= 1 {
			continue
		}
		sort.Strings(viewIDs)
		errs = append(errs, fmt.Sprintf(
			"multiple views target entity type %q: %s — at most one view per entity type is allowed",
			entityType, strings.Join(viewIDs, ", ")))
	}

	for viewID, view := range cfg.Views {
		// Before the entry-type check, which continues on failure: a bad
		// export_render path is worth reporting even on a view whose entry
		// type is also wrong.
		errs = append(errs, validateExportRenderShape("view", viewID, view.ExportRender)...)

		// Validate entry type
		entDef, ok := meta.GetEntityDef(view.Entry.Type)
		if !ok {
			errs = append(errs, fmt.Sprintf(
				"view %q: entry type %q not in metamodel",
				viewID, view.Entry.Type))
			continue
		}

		// Build collection map: tracks what collections are available at each point
		// Start with "entry" which refers to the entry entity type
		collections := map[string]string{
			"entry": view.Entry.Type, // collection name -> entity type
		}

		// Validate traverse rules and track collections
		for i, t := range view.Traverse {
			// Validate from
			if t.From != "*" {
				if _, ok := collections[t.From]; !ok {
					validCollections := sortedMapKeys(collections)
					validCollections = append(validCollections, "*")
					errs = append(errs, fmt.Sprintf(
						"view %q: traverse[%d] references unknown collection %q in from (valid: %s)",
						viewID, i, t.From, strings.Join(validCollections, ", ")))
				}
			}

			// Validate follow/follow_incoming relation
			var relType string
			if t.Follow != "" {
				relType = t.Follow
				if _, ok := meta.GetRelationDef(t.Follow); !ok {
					if suggestion := suggestRelation(t.Follow, meta); suggestion != "" {
						errs = append(errs, fmt.Sprintf(
							"view %q: traverse[%d] references unknown relation %q in follow (did you mean %q?)",
							viewID, i, t.Follow, suggestion))
					} else {
						errs = append(errs, fmt.Sprintf(
							"view %q: traverse[%d] references unknown relation %q in follow",
							viewID, i, t.Follow))
					}
				}
			}
			if t.FollowIncoming != "" {
				relType = t.FollowIncoming
				if _, ok := meta.GetRelationDef(t.FollowIncoming); !ok {
					if suggestion := suggestRelation(t.FollowIncoming, meta); suggestion != "" {
						errs = append(errs, fmt.Sprintf(
							"view %q: traverse[%d] references unknown relation %q in follow_incoming (did you mean %q?)",
							viewID, i, t.FollowIncoming, suggestion))
					} else {
						errs = append(errs, fmt.Sprintf(
							"view %q: traverse[%d] references unknown relation %q in follow_incoming",
							viewID, i, t.FollowIncoming))
					}
				}
			}
			if t.Follow == "" && t.FollowIncoming == "" {
				errs = append(errs, fmt.Sprintf(
					"view %q: traverse[%d] must specify either follow or follow_incoming",
					viewID, i))
			}
			if t.Follow != "" && t.FollowIncoming != "" {
				errs = append(errs, fmt.Sprintf(
					"view %q: traverse[%d] cannot specify both follow and follow_incoming",
					viewID, i))
			}

			// Validate collect_as is specified
			if t.CollectAs == "" {
				errs = append(errs, fmt.Sprintf(
					"view %q: traverse[%d] must specify collect_as",
					viewID, i))
			} else {
				// Determine target entity type for this collection
				targetType := determineTargetType(t, relType, meta)
				collections[t.CollectAs] = targetType
			}
		}

		// Validate sections
		for i, s := range view.Sections {
			// Validate source
			sourceType := ""
			if s.Source != "" {
				if entityType, ok := collections[s.Source]; ok {
					sourceType = entityType
				} else {
					validSources := sortedMapKeys(collections)
					errs = append(errs, fmt.Sprintf(
						"view %q: section[%d] references unknown collection %q in source (valid: %s)",
						viewID, i, s.Source, strings.Join(validSources, ", ")))
				}
			}

			// Validate display mode
			if !validSectionDisplayModes[s.Display] {
				errs = append(errs, fmt.Sprintf(
					"view %q: section[%d] has invalid display mode %q (valid: %s)",
					viewID, i, s.Display, joinMapKeys(validSectionDisplayModes)))
			}

			errs = append(errs, validateNestedSection(viewID, i, s, view, collections, meta)...)

			// Outside the source-resolution guard below for the same reason
			// render/widget are: an opted-in section with a bad `create:` is
			// wrong regardless of whether its source collection resolves.
			errs = append(errs, validateSectionCreate(viewID, i, s, view, cfg, meta)...)

			// Spans are checked unconditionally — deliberately NOT inside the
			// `sourceType != ""` guard below, which only runs when the source
			// collection resolves. A bad span is wrong regardless of whether
			// the section's source is valid, and hiding it behind an unrelated
			// error would surface it only after the first one was fixed.
			for j, f := range s.Fields {
				errs = append(errs, validateSpan(f.Span,
					fmt.Sprintf("view %q: section[%d] field[%d]", viewID, i, j))...)
			}

			// Validate render modes (TKT-HOIX1). Deliberately OUTSIDE the
			// source-resolution guards below: `render` is a closed enum needing
			// no metamodel knowledge, so a section whose source doesn't resolve
			// must still have it checked (RR-4ICH8M). The per-field loops below
			// also never see the section-level value.
			errs = append(errs, validateSectionRender(viewID, i, s)...)

			// Validate widget overrides (TKT-3R7RF3), outside the guard for the
			// same reason: an unregistered widget name is checkable without the
			// metamodel. The type-compatibility half needs the entity def, so
			// it is passed when resolvable and skipped (never failed) when not.
			var widgetDef *metamodel.EntityDef
			if sourceType != "" {
				if d, ok := meta.GetEntityDef(sourceType); ok {
					widgetDef = d
				}
			}
			errs = append(errs, validateSectionFieldWidget(viewID, i, s, widgetDef, meta)...)

			// Validate fields (if source type is known)
			if sourceType != "" { //nolint:nestif // nested guards each check a distinct optional field of the source config.
				if sourceDef, ok := meta.GetEntityDef(sourceType); ok {
					for j, f := range s.Fields {
						if f.Property != "" && f.Property != "title" && f.Property != "id" {
							if _, ok := sourceDef.Properties[f.Property]; !ok {
								errs = append(errs, fmt.Sprintf(
									"view %q: section[%d] field[%d] property %q not in entity %q",
									viewID, i, j, f.Property, sourceType))
							}
						}
					}

					// Validate columns. A nested section uses
					// parent_columns/child_columns instead; see
					// validateNestedSection.
					for j, c := range s.Columns {
						if c.Property != "" && c.Property != "title" && c.Property != "id" {
							if _, ok := sourceDef.Properties[c.Property]; !ok {
								errs = append(errs, fmt.Sprintf(
									"view %q: section[%d] column[%d] property %q not in entity %q",
									viewID, i, j, c.Property, sourceType))
							}
						}
						if c.Relation != "" {
							if _, ok := meta.GetRelationDef(c.Relation); !ok {
								errs = append(errs, fmt.Sprintf(
									"view %q: section[%d] column[%d] references unknown relation %q",
									viewID, i, j, c.Relation))
							}
						}
					}

					// Validate group_by
					if s.GroupBy != "" {
						if _, ok := sourceDef.Properties[s.GroupBy]; !ok {
							errs = append(errs, fmt.Sprintf(
								"view %q: section[%d] group_by references unknown property %q",
								viewID, i, s.GroupBy))
						}
					}
				}
			} else if s.Source == "entry" {
				// Source is entry, use entry entity def
				for j, f := range s.Fields {
					if f.Property != "" && f.Property != "title" && f.Property != "id" {
						if _, ok := entDef.Properties[f.Property]; !ok {
							errs = append(errs, fmt.Sprintf(
								"view %q: section[%d] field[%d] property %q not in entity %q",
								viewID, i, j, f.Property, view.Entry.Type))
						}
					}
				}
			}
		}
	}

	return errs
}

// collectionTypes returns every entity type a collection may hold.
//
// [determineTargetType] answers with ONE type and yields "" for a relation
// declaring several `to:` types — correct for its callers, which need a single
// type or nothing. A nested section needs the whole set instead: a relation
// reaching both `task` and `bug` is ordinary (15 of the 47 relations in rela's
// own schema declare more than one `to:` type), and each type may declare its
// own columns.
//
// Returns nil for the implicit `entry` collection, whose type comes from the
// view rather than a rule, and for a name no rule collects.
func collectionTypes(view ViewConfig, name string, meta *metamodel.Metamodel) []string {
	if name == "entry" {
		if view.Entry.Type != "" {
			return []string{view.Entry.Type}
		}
		return nil
	}
	// Later rules win, matching the executor: a second rule with the same
	// collect_as merges into the same bucket.
	var rule *ViewTraverse
	for i := range view.Traverse {
		if view.Traverse[i].CollectAs == name {
			rule = &view.Traverse[i]
		}
	}
	if rule == nil {
		return nil
	}
	relType := rule.Follow
	if relType == "" {
		relType = rule.FollowIncoming
	}
	relDef, ok := meta.GetRelationDef(relType)
	if !ok {
		return nil
	}
	if rule.Follow != "" {
		return relDef.To
	}
	return relDef.From
}

// validateLevelColumns checks one level's per-type column map for a nested
// section: every key must be a type the level can actually hold, and every
// column must name a property of the type it is declared for.
//
// A type the level CAN hold but that the map omits is not an error — it
// renders title and id only, the same "absent means default" rule
// [Gantt.Sources] uses, so adding a type to a relation never breaks a view.
// validateSectionSort checks a section's sort keys against the entity types
// the level can hold.
//
// A property is accepted when SOME allowed type declares it, not every one: a
// level reached through a multi-target relation legitimately mixes types, and
// a row whose type lacks the property simply sorts as absent (last ascending),
// which is the same answer the store gives. Requiring every type to declare it
// would refuse a reasonable config.
//
// When allowed is empty the type is not statically known — determineTargetType
// returns "" for a multi-`to:` relation — so only the shape is checked. That
// matches how `columns:` handles the same case: validate where the type is
// known, skip where it is not, rather than guessing.
//
// "id" is always valid: every entity has one, and it is the universal sort
// tiebreak.
//
// "modified" is refused EXPLICITLY. A section renders from a traversal result,
// where modification time is not carried, so a section sorted on it would
// silently not sort. It would also be refused as a side effect of the
// declared-by check below — no entity type declares a property with that name
// — but only until someone declares one, and a guard that holds by accident
// is not a guard.
func validateSectionSort(
	viewID string, i int, key string, specs []SortSpec,
	allowed []string, meta *metamodel.Metamodel,
) []string {
	var errs []string
	for n, spec := range specs {
		switch {
		case spec.Property == "":
			errs = append(errs, fmt.Sprintf(
				"view %q: section[%d] %s[%d] has no property", viewID, i, key, n))
			continue
		case spec.Direction != "" && spec.Direction != "asc" && spec.Direction != "desc":
			errs = append(errs, fmt.Sprintf(
				"view %q: section[%d] %s[%d] has invalid direction %q (use \"asc\" or \"desc\")",
				viewID, i, key, n, spec.Direction))
		}
		if spec.Property == "modified" {
			errs = append(errs, fmt.Sprintf(
				"view %q: section[%d] %s[%d] sorts on %q, which a section cannot order by "+
					"(a traversal result carries no modification time)",
				viewID, i, key, n, spec.Property))
			continue
		}
		if spec.Property == "id" || len(allowed) == 0 {
			continue
		}
		declaredBy := make([]string, 0, len(allowed))
		for _, entityType := range allowed {
			if def, ok := meta.GetEntityDef(entityType); ok {
				if _, has := def.Properties[spec.Property]; has {
					declaredBy = append(declaredBy, entityType)
				}
			}
		}
		if len(declaredBy) == 0 {
			errs = append(errs, fmt.Sprintf(
				"view %q: section[%d] %s[%d] sorts on %q, which no type at this level declares (types: %s)",
				viewID, i, key, n, spec.Property, strings.Join(allowed, ", ")))
		}
	}
	return errs
}

func validateLevelColumns(
	viewID string, i int, level string, byType map[string][]ListColumn,
	allowed []string, meta *metamodel.Metamodel,
) []string {
	var errs []string
	allowedSet := make(map[string]bool, len(allowed))
	for _, t := range allowed {
		allowedSet[t] = true
	}
	for _, entityType := range sortedMapKeys(byType) {
		if len(allowed) > 0 && !allowedSet[entityType] {
			errs = append(errs, fmt.Sprintf(
				"view %q: section[%d] %s declares columns for %q, which this level never holds (valid: %s)",
				viewID, i, level, entityType, strings.Join(allowed, ", ")))
			continue
		}
		entDef, ok := meta.GetEntityDef(entityType)
		if !ok {
			errs = append(errs, fmt.Sprintf(
				"view %q: section[%d] %s references unknown entity type %q",
				viewID, i, level, entityType))
			continue
		}
		for j, c := range byType[entityType] {
			if c.Property != "" && c.Property != "title" && c.Property != "id" {
				if _, ok := entDef.Properties[c.Property]; !ok {
					errs = append(errs, fmt.Sprintf(
						"view %q: section[%d] %s[%q] column[%d] property %q not in entity %q",
						viewID, i, level, entityType, j, c.Property, entityType))
				}
			}
			if c.Relation != "" {
				if _, ok := meta.GetRelationDef(c.Relation); !ok {
					errs = append(errs, fmt.Sprintf(
						"view %q: section[%d] %s[%q] column[%d] references unknown relation %q",
						viewID, i, level, entityType, j, c.Relation))
				}
			}
		}
	}
	return errs
}

// nestedOnlyKeyErr reports a nested-only key authored on another display mode.
func nestedOnlyKeyErr(viewID string, i int, key, display string) string {
	return fmt.Sprintf(
		"view %q: section[%d] sets %s but display is %q (it requires display: %s)",
		viewID, i, key, display, DisplayNested)
}

// validateNestedSection checks the `children:` key that `display: nested`
// requires, and refuses the two shapes that would render a silently wrong
// section rather than failing.
//
// collections is the accumulated collection→entity-type map the surrounding
// loop already built for `source:`, so a `children:` typo is reported against
// the same vocabulary and cannot drift from it.
//
// The two refusals, both of which would otherwise produce a section that looks
// fine and is not:
//
//   - `children:` naming a bucket NOT collected by a rule that walked FROM
//     `source:`. Nesting comes from the traverse shape, so an unrelated bucket
//     yields rows with no children at all — an empty tree, not an error.
//   - `recursive: true` on the rule that collected the children. The recursive
//     walk reports ids level by level without retaining which node each came
//     from (see viewResult.Parents in internal/dataentry), so the section would
//     render flat with every child under no parent.
func validateNestedSection(
	viewID string, i int, s ViewSection, view ViewConfig, collections map[string]string,
	meta *metamodel.Metamodel,
) []string {
	if s.Display != DisplayNested {
		var errs []string
		if s.Children != "" {
			errs = append(errs, fmt.Sprintf(
				"view %q: section[%d] sets children %q but display is %q (children requires display: %s)",
				viewID, i, s.Children, s.Display, DisplayNested))
		}
		if s.ParentColumns != nil {
			errs = append(errs, nestedOnlyKeyErr(viewID, i, "parent_columns", s.Display))
		}
		if s.ChildColumns != nil {
			errs = append(errs, nestedOnlyKeyErr(viewID, i, "child_columns", s.Display))
		}
		if s.ParentSort != nil {
			errs = append(errs, nestedOnlyKeyErr(viewID, i, "parent_sort", s.Display))
		}
		if s.ChildSort != nil {
			errs = append(errs, nestedOnlyKeyErr(viewID, i, "child_sort", s.Display))
		}
		errs = append(errs, validateSectionSort(viewID, i, "sort", s.Sort,
			collectionTypes(view, s.Source, meta), meta)...)
		return errs
	}

	// A nested section is per-level and per-type, so the flat list `table`
	// uses cannot apply to it — refuse rather than silently ignore.
	if len(s.Columns) > 0 {
		return []string{fmt.Sprintf(
			"view %q: section[%d] has display: %s, which takes parent_columns/child_columns "+
				"(keyed by entity type) instead of columns",
			viewID, i, DisplayNested)}
	}

	if s.Children == "" {
		return []string{fmt.Sprintf(
			"view %q: section[%d] has display: %s but no children (name the collection to nest under each row)",
			viewID, i, DisplayNested)}
	}
	if s.Children == s.Source {
		return []string{fmt.Sprintf(
			"view %q: section[%d] nests collection %q under itself (children must differ from source)",
			viewID, i, s.Children)}
	}
	if _, ok := collections[s.Children]; !ok {
		return []string{fmt.Sprintf(
			"view %q: section[%d] references unknown collection %q in children (valid: %s)",
			viewID, i, s.Children, strings.Join(sortedMapKeys(collections), ", "))}
	}

	// Find the rule that collected the children and check it nests under the
	// section's own source. Later rules win, matching the executor, where a
	// second rule with the same collect_as merges into the same bucket.
	var child *ViewTraverse
	for j := range view.Traverse {
		if view.Traverse[j].CollectAs == s.Children {
			child = &view.Traverse[j]
		}
	}
	if child == nil {
		// Reachable only for `entry`, which every view seeds without a rule.
		return []string{fmt.Sprintf(
			"view %q: section[%d] cannot nest collection %q (no traverse rule collects it)",
			viewID, i, s.Children)}
	}
	var errs []string
	if child.From != s.Source {
		errs = append(errs, fmt.Sprintf(
			"view %q: section[%d] nests %q under %q, but the rule collecting %q traverses from %q "+
				"(the children rule must start from the section's source)",
			viewID, i, s.Children, s.Source, s.Children, child.From))
	}
	if child.Recursive {
		errs = append(errs, fmt.Sprintf(
			"view %q: section[%d] nests %q, which is collected recursively; "+
				"display: %s renders exactly two levels and cannot attribute a recursive walk",
			viewID, i, s.Children, DisplayNested))
	}
	if len(s.Sort) > 0 {
		errs = append(errs, fmt.Sprintf(
			"view %q: section[%d] has display: %s, which takes parent_sort/child_sort "+
				"(one per level) instead of sort",
			viewID, i, DisplayNested))
	}
	errs = append(errs, validateLevelColumns(viewID, i, "parent_columns", s.ParentColumns,
		collectionTypes(view, s.Source, meta), meta)...)
	errs = append(errs, validateLevelColumns(viewID, i, "child_columns", s.ChildColumns,
		collectionTypes(view, s.Children, meta), meta)...)
	errs = append(errs, validateSectionSort(viewID, i, "parent_sort", s.ParentSort,
		collectionTypes(view, s.Source, meta), meta)...)
	errs = append(errs, validateSectionSort(viewID, i, "child_sort", s.ChildSort,
		collectionTypes(view, s.Children, meta), meta)...)
	return errs
}

// determineTargetType determines the entity type that a traverse rule collects.
func determineTargetType(t ViewTraverse, relType string, meta *metamodel.Metamodel) string {
	if relType == "" {
		return ""
	}
	relDef, ok := meta.GetRelationDef(relType)
	if !ok {
		return ""
	}

	// For outgoing (follow), target is relation's To types
	// For incoming (follow_incoming), target is relation's From types
	if t.Follow != "" {
		if len(relDef.To) == 1 {
			return relDef.To[0]
		}

		return "" // multiple possible types, can't determine statically
	}
	if t.FollowIncoming != "" {
		if len(relDef.From) == 1 {
			return relDef.From[0]
		}

		return "" // multiple possible types
	}

	return ""
}

// suggestRelation finds a similar relation name for typo suggestions.
func suggestRelation(name string, meta *metamodel.Metamodel) string {
	nameLower := strings.ToLower(name)
	for relName := range meta.Relations {
		if strings.EqualFold(relName, name) {
			return relName
		}
		// Simple similarity: check if one contains the other
		relNameLower := strings.ToLower(relName)
		if strings.Contains(relNameLower, nameLower) || strings.Contains(nameLower, relNameLower) {
			return relName
		}
	}

	return ""
}

// validateKanbans validates kanban board definitions.
//
//nolint:gocognit,gocyclo,funlen // linear validation dispatcher: one independent config-vs-metamodel check per branch; splitting would scatter the rule set without lowering real complexity.
func validateKanbans(cfg *Config, meta *metamodel.Metamodel) []string {
	var errs []string

	for kanbanID, kanban := range cfg.Kanbans {
		// Validate entity type
		entDef, ok := meta.GetEntityDef(kanban.EntityType)
		if !ok {
			errs = append(errs, fmt.Sprintf("kanban %q: unknown entity type %q", kanbanID, kanban.EntityType))
			continue
		}

		// Validate column_property exists and is enum type
		if kanban.ColumnProperty == "" { //nolint:nestif // nested guards each check a distinct optional field of the kanban config.
			errs = append(errs, fmt.Sprintf("kanban %q: column_property is required", kanbanID))
		} else {
			propDef, ok := entDef.Properties[kanban.ColumnProperty]
			if !ok {
				errs = append(errs, fmt.Sprintf(
					"kanban %q: column_property %q not in entity %q",
					kanbanID, kanban.ColumnProperty, kanban.EntityType))
			} else {
				// Check if it's an enum type
				validValues := GetValidEnumValues(propDef, meta)
				if len(validValues) == 0 {
					errs = append(errs, fmt.Sprintf(
						"kanban %q: column_property %q must be an enum type",
						kanbanID, kanban.ColumnProperty))
				} else {
					// Validate column values if specified
					validSet := make(map[string]bool)
					for _, v := range validValues {
						validSet[v] = true
					}
					for i, col := range kanban.Columns {
						if !validSet[col.Value] {
							errs = append(errs, fmt.Sprintf(
								"kanban %q: columns[%d] value %q is not valid for %q (valid: %s)",
								kanbanID, i, col.Value, kanban.ColumnProperty, strings.Join(validValues, ", ")))
						}
					}
				}
			}
		}

		// Icon names are checked unconditionally — deliberately NOT inside the
		// enum guards above, which only run when column_property resolves to an
		// enum. A bad icon name is wrong regardless, and burying it behind an
		// unrelated error would surface it only after the first one was fixed.
		for i, col := range kanban.Columns {
			errs = append(errs, validateIconName(col.Icon,
				fmt.Sprintf("kanban %q: columns[%d]", kanbanID, i))...)
		}
		for i, lane := range kanban.Swimlanes {
			errs = append(errs, validateIconName(lane.Icon,
				fmt.Sprintf("kanban %q: swimlanes[%d]", kanbanID, i))...)
		}

		// Validate swimlane_property if specified
		if kanban.SwimlaneProperty != "" { //nolint:nestif // nested guards each check a distinct optional field of the kanban config.
			propDef, ok := entDef.Properties[kanban.SwimlaneProperty]
			if !ok {
				errs = append(errs, fmt.Sprintf(
					"kanban %q: swimlane_property %q not in entity %q",
					kanbanID, kanban.SwimlaneProperty, kanban.EntityType))
			} else {
				// Check if it's an enum type
				validValues := GetValidEnumValues(propDef, meta)
				if len(validValues) == 0 {
					errs = append(errs, fmt.Sprintf(
						"kanban %q: swimlane_property %q must be an enum type",
						kanbanID, kanban.SwimlaneProperty))
				} else {
					// Validate swimlane values if specified
					validSet := make(map[string]bool)
					for _, v := range validValues {
						validSet[v] = true
					}
					for i, lane := range kanban.Swimlanes {
						if !validSet[lane.Value] {
							errs = append(errs, fmt.Sprintf(
								"kanban %q: swimlanes[%d] value %q is not valid for %q (valid: %s)",
								kanbanID, i, lane.Value, kanban.SwimlaneProperty, strings.Join(validValues, ", ")))
						}
					}
				}
			}
		}

		// Validate card title property
		if kanban.Card.Title != "" {
			if _, ok := entDef.Properties[kanban.Card.Title]; !ok {
				errs = append(errs, fmt.Sprintf(
					"kanban %q: card.title property %q not in entity %q",
					kanbanID, kanban.Card.Title, kanban.EntityType))
			}
		}

		// Validate card fields
		for i, f := range kanban.Card.Fields {
			if f.Relation != "" {
				if _, ok := meta.GetRelationDef(f.Relation); !ok {
					errs = append(errs, fmt.Sprintf(
						"kanban %q: card.fields[%d] references unknown relation %q",
						kanbanID, i, f.Relation))
				}
				errs = append(errs, CheckAmbiguousDirection(
					fmt.Sprintf("kanban %q: card.fields[%d]", kanbanID, i),
					kanban.EntityType, f.Relation, f.Direction, meta)...)
				continue
			}
			if f.Property != "" && f.Property != "title" && f.Property != "id" {
				if _, ok := entDef.Properties[f.Property]; !ok {
					errs = append(errs, fmt.Sprintf(
						"kanban %q: card.fields[%d] property %q not in entity %q",
						kanbanID, i, f.Property, kanban.EntityType))
				}
			}
		}

		// `condition:` is accepted on the struct (shared with lists) but the
		// board does not evaluate it yet — it still filters client-side. A
		// key that validates and then does nothing is precisely the silent
		// no-op BUG-F1LTV0 and BUG-MYN56J are both about, so refuse it here
		// until the kanban read path lands. Remove this when it does.
		if kanban.Condition != "" {
			errs = append(errs, fmt.Sprintf(
				"kanban %q: condition is not supported on a kanban yet "+
					"(the board filters client-side); use it on a list, or filters: here",
				kanbanID))
		}

		// Validate filter properties
		for i, f := range kanban.Filters {
			if !validFilterOperators[f.Operator] {
				errs = append(errs, fmt.Sprintf(
					"kanban %q: filters[%d] has invalid operator %q (valid: %s)",
					kanbanID, i, f.Operator, joinMapKeys(validFilterOperators)))
			}
			if f.HasProperty() && entity.IsEntityPropertyKey(f.Property) {
				if _, ok := entDef.Properties[f.Property]; !ok {
					errs = append(errs, fmt.Sprintf(
						"kanban %q: filters[%d] references unknown property %q",
						kanbanID, i, f.Property))
				}
			}
		}

		// Validate filter_controls
		for i, fc := range kanban.FilterControls {
			if fc.Property == "" && fc.Relation == "" {
				errs = append(errs, fmt.Sprintf(
					"kanban %q: filter_controls[%d] must specify either property or relation",
					kanbanID, i))
			}
			if fc.Property != "" {
				if _, ok := entDef.Properties[fc.Property]; !ok {
					errs = append(errs, fmt.Sprintf(
						"kanban %q: filter_controls[%d] references unknown property %q",
						kanbanID, i, fc.Property))
				}
			}
			if fc.Relation != "" {
				if _, ok := meta.GetRelationDef(fc.Relation); !ok {
					errs = append(errs, fmt.Sprintf(
						"kanban %q: filter_controls[%d] references unknown relation %q",
						kanbanID, i, fc.Relation))
				}
				errs = append(errs, CheckAmbiguousDirection(
					fmt.Sprintf("kanban %q: filter_controls[%d]", kanbanID, i),
					kanban.EntityType, fc.Relation, fc.Direction, meta)...)
			}
		}

		// Validate form references
		if kanban.EditForm != "" {
			if _, ok := cfg.Forms[kanban.EditForm]; !ok {
				errs = append(errs, fmt.Sprintf(
					"kanban %q: references unknown form %q in edit_form",
					kanbanID, kanban.EditForm))
			}
		}
		if kanban.CreateForm != "" {
			if _, ok := cfg.Forms[kanban.CreateForm]; !ok {
				errs = append(errs, fmt.Sprintf(
					"kanban %q: references unknown form %q in create_form",
					kanbanID, kanban.CreateForm))
			}
		}
	}

	return errs
}

// validateDashboard validates dashboard configuration.
func validateDashboard(cfg *Config, _ *metamodel.Metamodel) []string {
	var errs []string

	if cfg.Dashboard == nil {
		return errs
	}

	for i, card := range cfg.Dashboard.Cards {
		// Validate display mode
		if !validDashboardDisplayModes[card.Display] {
			errs = append(errs, fmt.Sprintf(
				"dashboard: card[%d] %q has invalid display mode %q (valid: %s)",
				i, card.Title, card.Display, joinMapKeys(validDashboardDisplayModes)))
		}

		// Validate sort directions
		for j, s := range card.Sort {
			if !validSortDirections[s.Direction] {
				errs = append(errs, fmt.Sprintf(
					"dashboard: card[%d] %q sort[%d] has invalid direction %q (valid: asc, desc)",
					i, card.Title, j, s.Direction))
			}
		}

		// For breakdown display, group_by should be specified
		if card.Display == "breakdown" && card.GroupBy == "" {
			errs = append(errs, fmt.Sprintf(
				"dashboard: card[%d] %q uses breakdown display but has no group_by",
				i, card.Title))
		}

		// For table display, columns should be specified
		if card.Display == "table" && len(card.Columns) == 0 {
			errs = append(errs, fmt.Sprintf(
				"dashboard: card[%d] %q uses table display but has no columns",
				i, card.Title))
		}
	}

	return errs
}

// appIDRegex defines the allowed format for app IDs — same shape as action IDs.
// The ID is used as the URL path segment for GET /api/v1/_apps/{id}, so it must
// be filesystem- and URL-safe.
var appIDRegex = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// ValidAppID reports whether id is a syntactically valid app ID. Apps are
// discovered by scanning apps/*.html; the id is the filename stem and is used as
// the URL path segment for GET /api/v1/_apps/{id}, so it must be filesystem- and
// URL-safe. Exported so the scanner and the request handler share one rule.
func ValidAppID(id string) bool { return appIDRegex.MatchString(id) }

// validateCommands validates command definitions.
// actionIDRegex defines the allowed format for action IDs.
// Lowercase letters, digits, hyphens, underscores, 1-64 characters.
var actionIDRegex = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// reservedListKeys are single-character keys already used by useListKeyboard.ts.
// Action key bindings must not conflict with these.
var reservedListKeys = map[string]bool{
	"j": true, "k": true, // navigation
	"o": true, "e": true, "n": true, // open, edit, new
	"h": true, "l": true, // pagination
}

// actionKeyRegex allows a single lowercase letter or digit as a shortcut key.
var actionKeyRegex = regexp.MustCompile(`^[a-z0-9]$`)

// validateActions checks action definitions: ID format, set/script exclusivity,
// script path safety, and key shortcut validity.
//
//nolint:gocognit // linear validation dispatcher: one independent config-vs-metamodel check per branch; splitting would scatter the rule set without lowering real complexity.
func validateActions(cfg *Config, meta *metamodel.Metamodel) []string {
	var errs []string

	for id, action := range cfg.Actions {
		if !actionIDRegex.MatchString(id) {
			errs = append(errs, fmt.Sprintf(
				"actions: invalid action ID %q (must match ^[a-z0-9_-]{1,64}$)", id))
			continue
		}

		hasScript := action.Script != ""
		hasSet := len(action.Set) > 0

		if hasScript && hasSet {
			errs = append(errs, fmt.Sprintf(
				"actions: %q has both script and set (must have one, not both)", id))
			continue
		}
		if !hasScript && !hasSet {
			errs = append(errs, fmt.Sprintf(
				"actions: %q has neither script nor set (must have one)", id))
			continue
		}

		// Script validation
		if hasScript {
			if !strings.HasSuffix(action.Script, ".lua") {
				errs = append(errs, fmt.Sprintf(
					"actions: %q script %q must have .lua extension", id, action.Script))
			}
			if !filepath.IsLocal(action.Script) {
				errs = append(errs, fmt.Sprintf(
					"actions: %q script %q must be a local path (no '..' or absolute paths)",
					id, action.Script))
			}
		}

		errs = append(errs, validateActionRequest(id, action, hasScript)...)
		errs = append(errs, validateActionScope(id, action, hasScript, meta)...)

		// Key validation (optional — only required when referenced by a list)
		if action.Key != "" {
			if !actionKeyRegex.MatchString(action.Key) {
				errs = append(errs, fmt.Sprintf(
					"actions: %q key %q must be a single lowercase letter or digit", id, action.Key))
			} else if reservedListKeys[action.Key] {
				errs = append(errs, fmt.Sprintf(
					"actions: %q key %q conflicts with reserved list shortcut", id, action.Key))
			}
		}
	}

	// Validate list action references
	for listID, list := range cfg.Lists {
		seenKeys := map[string]string{} // key → action ID
		entDef, hasEntDef := meta.GetEntityDef(list.EntityType)

		for _, actionID := range list.Actions {
			action, ok := cfg.Actions[actionID]
			if !ok {
				errs = append(errs, fmt.Sprintf(
					"list %q: references unknown action %q", listID, actionID))
				continue
			}

			if action.AvailableOn != nil {
				errs = append(errs, entityBoundActionRefError(
					fmt.Sprintf("list %q", listID), actionID))
			}

			// Actions referenced by lists must have label and key
			if action.Label == "" {
				errs = append(errs, fmt.Sprintf(
					"list %q: action %q must have a label when used in a list", listID, actionID))
			}
			if action.Key == "" {
				errs = append(errs, fmt.Sprintf(
					"list %q: action %q must have a key when used in a list", listID, actionID))
			} else if prev, dup := seenKeys[action.Key]; dup {
				errs = append(errs, fmt.Sprintf(
					"list %q: actions %q and %q have duplicate key %q", listID, prev, actionID, action.Key))
			} else {
				seenKeys[action.Key] = actionID
			}

			// Validate set properties against the list's entity type
			if len(action.Set) > 0 && hasEntDef {
				for prop := range action.Set {
					if _, ok := entDef.Properties[prop]; !ok {
						errs = append(errs, fmt.Sprintf(
							"list %q: action %q sets unknown property %q for entity type %q",
							listID, actionID, prop, list.EntityType))
					}
				}
			}
		}
	}

	return errs
}

// validateActionScope checks the detail-page keys of one action
// (TKT-VVS16W): available_on, and when, which only means something with it.
// The when expression itself is compiled by conditionlint, which knows the
// predicate language; this checks only the structure.
func validateActionScope(id string, action Action, hasScript bool, meta *metamodel.Metamodel) []string {
	var errs []string
	scope := action.AvailableOn
	if scope == nil {
		if action.When != "" {
			errs = append(errs, fmt.Sprintf(
				"actions: %q has when but no available_on (when decides which detail pages "+
					"offer the action, so it needs available_on to apply to)", id))
		}
		return errs
	}
	if !hasScript {
		errs = append(errs, fmt.Sprintf(
			"actions: %q has available_on but no script (a detail-page action runs a script)", id))
	}
	if len(scope.EntityTypes) == 0 {
		errs = append(errs, fmt.Sprintf(
			"actions: %q available_on needs at least one entity type in entity_types", id))
	}
	for _, et := range scope.EntityTypes {
		def, ok := meta.GetEntityDef(et)
		if !ok {
			errs = append(errs, fmt.Sprintf(
				"actions: %q available_on references unknown entity type %q", id, et))
			continue
		}
		for _, face := range scope.Faces {
			if _, declared := def.Faces[face]; !declared {
				errs = append(errs, fmt.Sprintf(
					"actions: %q available_on face %q is not declared on entity type %q",
					id, face, et))
			}
		}
	}
	return errs
}

// entityBoundActionRefError reports a reference to an action with
// available_on from a surface that cannot supply the entity address such an
// action requires. Without it the button would render and every click would
// fail.
func entityBoundActionRefError(where, actionID string) string {
	return fmt.Sprintf(
		"%s: references action %q, which has available_on and runs only against one entity "+
			"on its detail page; declare a separate action (it may share the script) for this surface",
		where, actionID)
}

func validateCommands(cfg *Config, meta *metamodel.Metamodel) []string {
	var errs []string

	for cmdID, cmd := range cfg.Commands {
		if cmd.Label == "" {
			errs = append(errs, fmt.Sprintf("command %q: label is required", cmdID))
		}
		if cmd.Script == "" {
			errs = append(errs, fmt.Sprintf("command %q: script is required", cmdID))
		}
		if !validCommandContexts[cmd.Context] {
			errs = append(errs, fmt.Sprintf(
				"command %q: invalid context %q (valid: %s)",
				cmdID, cmd.Context, joinMapKeys(validCommandContexts)))
		}
		if cmd.AvailableOn != nil {
			for _, v := range cmd.AvailableOn.Views {
				if _, ok := cfg.Views[v]; !ok {
					errs = append(errs, fmt.Sprintf(
						"command %q: available_on references unknown view %q", cmdID, v))
				}
			}
			for _, l := range cmd.AvailableOn.Lists {
				if _, ok := cfg.Lists[l]; !ok {
					errs = append(errs, fmt.Sprintf(
						"command %q: available_on references unknown list %q", cmdID, l))
				}
			}
			for _, et := range cmd.AvailableOn.EntityTypes {
				if _, ok := meta.GetEntityDef(et); !ok {
					errs = append(errs, fmt.Sprintf(
						"command %q: available_on references unknown entity type %q", cmdID, et))
				}
			}
		}
	}

	return errs
}

// validateDocuments validates document configurations.
//
// validateApp validates app-level settings. PlantUMLServerURL, when set,
// must be an absolute http/https URL with a host: the SPA feeds it straight
// into an <img src>, so a malformed or non-http scheme (e.g. javascript:,
// data:, protocol-relative //host, or a bare host) is rejected at load time
// rather than reaching a user's browser. Empty is valid (PlantUML disabled).
//
// http:// is additionally restricted to loopback. The SPA encodes the diagram
// source into the URL it fetches, so a cleartext request publishes that source
// to anyone on the path — and diagram source is ordinary private content. On
// loopback there is no network segment to observe, which is why the common
// "PlantUML as a local sidecar" deployment stays valid; anywhere else, the
// operator can use TLS.
func validateApp(cfg *Config, meta *metamodel.Metamodel) []string {
	var errs []string
	// A typo here would silently serve the DEFAULT world to every request and
	// look exactly like the feature was never configured — no error, no
	// warning, just the wrong faces. So an undeclared name fails the load.
	if w := cfg.App.DefaultWorld; w != "" && w != metamodel.DefaultWorldName {
		if meta == nil {
			errs = append(errs, "app.default_world: set, but no metamodel is available to validate it against")
		} else if _, ok := meta.Worlds[w]; !ok {
			declared := make([]string, 0, len(meta.Worlds))
			for name := range meta.Worlds {
				declared = append(declared, name)
			}
			sort.Strings(declared)
			errs = append(errs, fmt.Sprintf(
				"app.default_world: %q is not a declared world (schema.yaml declares: %s)",
				w, strings.Join(declared, ", ")))
		}
	}
	if raw := cfg.App.PlantUMLServerURL; raw != "" {
		u, err := url.Parse(raw)
		switch {
		case err != nil:
			errs = append(errs, fmt.Sprintf("app.plantuml_server_url: not a valid URL: %v", err))
		case u.Scheme != "http" && u.Scheme != "https":
			errs = append(errs, fmt.Sprintf(
				"app.plantuml_server_url: scheme must be http or https, got %q", u.Scheme))
		case u.Host == "":
			errs = append(errs, "app.plantuml_server_url: must include a host")
		case u.Scheme == "http" && !isLoopbackHost(u.Hostname()):
			errs = append(errs, fmt.Sprintf(
				"app.plantuml_server_url: http:// sends diagram source in cleartext; "+
					"use https:// (http is allowed only for loopback), got host %q", u.Hostname()))
		}
	}
	return errs
}

// isLoopbackHost reports whether host (a URL's Hostname(), so already stripped
// of port, userinfo and IPv6 brackets) names the local machine.
//
// A single trailing dot is stripped first: "localhost." is the fully-qualified
// form of the same name, and refusing it would reject a legitimate local setup.
//
// The IP case is delegated to net.IP.IsLoopback, which covers all of 127.0.0.0/8
// and ::1 (in any spelling, including the IPv4-mapped ::ffff:127.0.0.1) rather
// than just the two addresses people usually write. The name case matches
// "localhost" exactly and case-insensitively: a suffix or prefix test would
// accept "localhost.evil.com" and "127.0.0.1.evil.com", which resolve wherever
// their owner points them. Any other name — including one that happens to
// resolve to 127.0.0.1 today — is treated as remote, because resolution can
// change and validation runs at config-load time.
//
// Errs toward refusing: an unusual spelling of a loopback address (the decimal
// "2130706433", say) is rejected rather than parsed, which costs an operator a
// clearer URL and never permits cleartext to a remote host.
func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost")
}

// ReservedExportSegment is the final path segment that turns a document URL
// into an export request (/_documents/{document}/_export), and is therefore a
// name no document may take.
//
// It is declared HERE rather than imported from dataentry because dataentry
// imports this package, so the dependency cannot run the other way. dataentry's
// exportSegment is an alias of this constant, so renaming one is a compile
// error in the other; TestExportSegmentPremise asserts the two agree.
const ReservedExportSegment = "_export"

// Invariant: every document must have entity_type set, and exactly one of
// {command, script} must be non-empty. entity_type is enforced at the HTTP
// handler layer to reject cross-type render requests; the mutual exclusion
// prevents ambiguous configs (which renderer runs when both are set?).
func validateDocuments(cfg *Config) []string {
	var errs []string

	for docID, doc := range cfg.Documents {
		// entity_type is NOT required: omitting it declares a standalone
		// document (DocumentConfig.IsStandalone). What a standalone document
		// cannot have is an edit: block — that button navigates to a form for
		// the document's entity, and there is no entity. Checked here rather
		// than left to render time so the author learns at config load.
		//
		// The nil-vs-{} caveat on Edit (see its godoc) does not matter here:
		// both shapes mean "no button", and only a non-nil Edit is an error.
		if doc.IsStandalone() && doc.Edit != nil {
			errs = append(errs, fmt.Sprintf(
				"document %q: edit is not supported without entity_type (a standalone document has no entity to edit)",
				docID))
		}

		// "_export" is the reserved final segment of the document export routes
		// (/_documents/{doc}/_export), so no document may take it as a name.
		//
		// Be precise about why, because the obvious reason is wrong: such a
		// document is NOT unreachable today. The dispatch matches the reserved
		// segment only in FINAL position, so all four shapes still resolve —
		// /_documents/_export renders it, /_documents/_export/_export exports
		// it, and the anchored pair works too. The reservation is
		// forward-compatibility, not a repair: a name that is simultaneously a
		// route keyword is one dispatch change away from being shadowed
		// silently, and the failure would be a document that stops rendering
		// with no error anywhere. Refusing at load costs an operator one
		// rename and removes the whole class.
		if docID == ReservedExportSegment {
			errs = append(errs, fmt.Sprintf(
				"document %q: %q is reserved for the export route (/_documents/{document}/%s); rename the document",
				docID, ReservedExportSegment, ReservedExportSegment))
		}

		hasCmd := len(doc.Command) > 0
		hasScript := doc.Script != ""
		switch {
		case hasCmd && hasScript:
			errs = append(errs, fmt.Sprintf(
				"document %q: command and script are mutually exclusive", docID))
		case !hasCmd && !hasScript:
			errs = append(errs, fmt.Sprintf(
				"document %q: one of command or script must be set", docID))
		}

		errs = append(errs, validateDocumentElevation(docID, doc, hasScript)...)

		// {id} / {id_lower} were removed in TKT-QGHNVA: they spliced a
		// request-derived value into a shell string. Fail at config load with
		// the replacement named, rather than silently passing the literal
		// "{id}" through to the renderer — a silent pass would look like a
		// working config that produces a document about the wrong thing.
		for _, arg := range doc.Command {
			if strings.Contains(arg, "{id}") || strings.Contains(arg, "{id_lower}") {
				errs = append(errs, fmt.Sprintf(
					"document %q: {id}/{id_lower} are no longer supported; "+
						"use {in}, the entry entity's markdown file whose frontmatter carries `id:` "+
						"(commands now run without a shell, so the id never reaches the command line)",
					docID))
				break
			}
		}

		if doc.Edit != nil { //nolint:nestif // nested branches validate distinct doc.Edit sub-fields.
			if doc.Edit.Form == "" {
				errs = append(errs, fmt.Sprintf(
					"document %q: edit.form is required when edit is set", docID))
			} else if _, ok := cfg.Forms[doc.Edit.Form]; !ok {
				if suggestion := suggestForm(doc.Edit.Form, cfg); suggestion != "" {
					errs = append(errs, fmt.Sprintf(
						"document %q: edit.form references unknown form %q (did you mean %q?)",
						docID, doc.Edit.Form, suggestion))
				} else {
					errs = append(errs, fmt.Sprintf(
						"document %q: edit.form references unknown form %q",
						docID, doc.Edit.Form))
				}
			}
			if doc.Edit.Label == "" {
				errs = append(errs, fmt.Sprintf(
					"document %q: edit.label is required when edit is set", docID))
			}
		}
	}

	return errs
}

// validateStyles validates style definitions reference valid metamodel types.
func validateStyles(cfg *Config, meta *metamodel.Metamodel) []string {
	var errs []string

	for typeName := range cfg.Styles {
		// Check if it's a custom type in metamodel
		if _, ok := meta.Types[typeName]; ok {
			continue
		}
		// Check if it's used as a property type in any entity
		found := false
		for _, entDef := range meta.Entities {
			for _, propDef := range entDef.Properties {
				if propDef.Type == typeName {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			errs = append(errs, fmt.Sprintf(
				"styles: type %q is not defined in metamodel", typeName))
		}
	}

	return errs
}

// validateCrossReferences validates that all cross-references between config sections are valid.
//
//nolint:gocognit // linear validation dispatcher: one independent config-vs-metamodel check per branch; splitting would scatter the rule set without lowering real complexity.
func validateCrossReferences(cfg *Config) []string {
	var errs []string

	// Validate list references to forms and views
	for listID, list := range cfg.Lists {
		if list.CreateForm != "" {
			if _, ok := cfg.Forms[list.CreateForm]; !ok {
				if suggestion := suggestForm(list.CreateForm, cfg); suggestion != "" {
					errs = append(errs, fmt.Sprintf(
						"list %q: references unknown form %q in create_form (did you mean %q?)",
						listID, list.CreateForm, suggestion))
				} else {
					errs = append(errs, fmt.Sprintf(
						"list %q: references unknown form %q in create_form",
						listID, list.CreateForm))
				}
			}
		}
		if list.EditForm != "" {
			if _, ok := cfg.Forms[list.EditForm]; !ok {
				if suggestion := suggestForm(list.EditForm, cfg); suggestion != "" {
					errs = append(errs, fmt.Sprintf(
						"list %q: references unknown form %q in edit_form (did you mean %q?)",
						listID, list.EditForm, suggestion))
				} else {
					errs = append(errs, fmt.Sprintf(
						"list %q: references unknown form %q in edit_form",
						listID, list.EditForm))
				}
			}
		}
		if list.DetailView != "" {
			if _, ok := cfg.Views[list.DetailView]; !ok {
				if suggestion := suggestView(list.DetailView, cfg); suggestion != "" {
					errs = append(errs, fmt.Sprintf(
						"list %q: references unknown view %q in detail_view (did you mean %q?)",
						listID, list.DetailView, suggestion))
				} else {
					errs = append(errs, fmt.Sprintf(
						"list %q: references unknown view %q in detail_view",
						listID, list.DetailView))
				}
			}
		}
	}

	return errs
}

// suggestForm finds a similar form name for typo suggestions.
func suggestForm(name string, cfg *Config) string {
	nameLower := strings.ToLower(name)
	for formName := range cfg.Forms {
		if strings.EqualFold(formName, name) {
			return formName
		}
		formNameLower := strings.ToLower(formName)
		if strings.Contains(formNameLower, nameLower) || strings.Contains(nameLower, formNameLower) {
			return formName
		}
	}

	return ""
}

// suggestView finds a similar view name for typo suggestions.
func suggestView(name string, cfg *Config) string {
	nameLower := strings.ToLower(name)
	for viewName := range cfg.Views {
		if strings.EqualFold(viewName, name) {
			return viewName
		}
		viewNameLower := strings.ToLower(viewName)
		if strings.Contains(viewNameLower, nameLower) || strings.Contains(nameLower, viewNameLower) {
			return viewName
		}
	}

	return ""
}

// Helper functions

func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	natsort.Strings(keys)
	return keys
}

func joinMapKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k != "" { // skip empty string (default)
			keys = append(keys, k)
		}
	}
	natsort.Strings(keys)
	return strings.Join(keys, ", ")
}

// validateDocumentElevation checks the allow_acl_bypass declaration on a
// document (TKT-Y3JVFK). Three rules, each failing at config load rather than
// at render time so the author learns before deploying:
//
//  1. Only `read` is accepted. A document render is a GET; see the
//     DocumentConfig.AllowACLBypass godoc for why writes belong in an
//     automation action or a schedule instead.
//  2. Elevation REQUIRES permission:. Without it the render publishes whatever
//     the script reads to every principal. This is the one place a document's
//     permission: is mandatory — see the DocumentConfig.Permission godoc for
//     why it is optional otherwise.
//  3. Elevation only means something for a script: renderer. A command:
//     renderer is an external process that never sees the Lua bindings, so
//     allow_acl_bypass on one would be config naming a capability that cannot
//     apply — which "appears to work" and is worse than a missing field.
func validateDocumentElevation(docID string, doc DocumentConfig, hasScript bool) []string {
	if !doc.AllowACLBypass.Enabled() {
		return nil
	}

	var errs []string

	if doc.AllowACLBypass != metamodel.ACLBypassRead {
		errs = append(errs, fmt.Sprintf(
			"document %q: allow_acl_bypass must be %q on a document (got %q); a render is a GET, "+
				"so it must not mutate — use an automation action or a schedule for elevated writes",
			docID, metamodel.ACLBypassRead, doc.AllowACLBypass))
	}

	if doc.Permission == "" {
		errs = append(errs, fmt.Sprintf(
			"document %q: permission is required when allow_acl_bypass is set; an elevated render "+
				"reads past the caller's ACL, so without a permission it serves that data to every principal",
			docID))
	}

	if !hasScript {
		errs = append(errs, fmt.Sprintf(
			"document %q: allow_acl_bypass applies only to a script renderer; a command renderer "+
				"is an external process and never receives the Lua bindings it unlocks", docID))
	}

	return errs
}

// validateQueryScopes checks that every `query_scope:` on a list, kanban or
// navigation `entities:` entry names a scope its entity type declares.
//
// An unknown name is a LOAD ERROR rather than a fallback to unfiltered. The
// two failure directions are not symmetric: a refused config is fixed in
// seconds, while a silent fallback shows every archived row on a board the
// operator believed was scoped, and nothing on screen says so.
//
// The reserved `all` always resolves — it is implicit and means "no
// predicate", so a view may withdraw its type's default even when the type
// declares no scopes at all.
func validateQueryScopes(cfg *Config, meta *metamodel.Metamodel) []string {
	if cfg == nil || meta == nil {
		return nil
	}
	var errs []string
	for _, id := range sortedMapKeys(cfg.Lists) {
		list := cfg.Lists[id]
		errs = append(errs, checkQueryScopeRef(meta, "list", id, list.EntityType, list.QueryScope)...)
	}
	for _, id := range sortedMapKeys(cfg.Kanbans) {
		kanban := cfg.Kanbans[id]
		errs = append(errs, checkQueryScopeRef(meta, "kanban", id, kanban.EntityType, kanban.QueryScope)...)
	}
	for _, nav := range NavEntitiesEntries(cfg.Navigation) {
		errs = append(errs, checkQueryScopeRef(meta, "navigation entities", nav.Entities, nav.Entities, nav.QueryScope)...)
	}
	return errs
}

// checkQueryScopeRef resolves one `query_scope:` reference.
func checkQueryScopeRef(
	meta *metamodel.Metamodel, kind, id, entityType, scope string,
) []string {
	if scope == "" || scope == metamodel.AllQueryScopeName {
		return nil
	}
	def, ok := meta.GetEntityDef(entityType)
	if !ok {
		// The unknown entity type is already reported by validateLists /
		// validateKanbans / validateNavEntities; adding a second error about a scope on a type that
		// does not exist would be noise.
		return nil
	}
	if _, ok := def.QueryScopes[scope]; ok {
		return nil
	}
	declared := sortedMapKeys(def.QueryScopes)
	if len(declared) == 0 {
		return []string{fmt.Sprintf(
			"%s %q: query_scope %q is not declared — entity type %q declares no query_scopes:",
			kind, id, scope, entityType)}
	}
	return []string{fmt.Sprintf(
		"%s %q: query_scope %q is not declared on entity type %q (declared: %s, plus the implicit %q)",
		kind, id, scope, entityType, strings.Join(declared, ", "), metamodel.AllQueryScopeName)}
}
