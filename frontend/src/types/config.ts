import type { SortSpec } from './schema'
import type { ScriptError } from './scriptError'

export interface ResolvedPalette {
  light: Record<string, string>
  dark?: Record<string, string>
  darkDisabled?: boolean
}

export interface Config {
  app: AppConfig
  /** Deployment description for the global "About" help (TKT-DUQBD0). */
  about_description?: string
  styles?: Record<string, Record<string, string>>
  palette?: ResolvedPalette
  forms: Record<string, FormConfig>
  lists: Record<string, ListConfig>
  views: Record<string, ViewConfig>
  /**
   * Per-entity-TYPE UX bindings, keyed by entity type — unlike `views`, which
   * is keyed by view id. Optional: the Go side omits the key when no entry is
   * configured.
   */
  entity_views?: Record<string, EntityViewConfig>
  kanbans: Record<string, KanbanConfig>
  /** Optional: the Go side omits the key entirely when no calendar is configured. */
  calendars?: Record<string, CalendarConfig>
  gantts?: Record<string, GanttConfig>
  dashboard?: DashboardConfig
  actions?: Record<string, ActionConfig>
  navigation: NavigationEntry[]
  documents?: Record<string, DocumentConfig>
  apps?: Record<string, AppEntry>
  /**
   * Operator-declared priority tiers for next-action suggestions, so the UI
   * can label a band rather than echo a raw id. The SOURCES are deliberately
   * absent: a suggestion arrives fully resolved from /_next_action, and
   * serving the rules would invite a client-side re-implementation of the
   * engine (same reasoning as "no useACL() composable").
   */
  next_action_bands?: NextActionBand[]
}

/** A custom app surfaced in the SPA (HTML fetched from /api/v1/_apps/{id}). */
export interface AppEntry {
  title?: string
  label?: string
  description?: string
}

export interface ActionConfig {
  label?: string
  key?: string
  /**
   * `true` asks before running; a string asks with that text (TKT-VVS16W).
   * Where an action appears on a DETAIL page is decided by the server and
   * published as `_actions['action:<id>']` on the entity; the scope, `when`
   * and `permission` that decide it are deliberately not served here.
   */
  confirm?: boolean | string
  set?: Record<string, string>
  script?: string
  description?: string
  params?: Record<string, string>
}

export interface AppConfig {
  name: string
  description?: string
  /**
   * Base URL of the configured PlantUML rendering server, or absent/empty
   * when PlantUML rendering is disabled. A non-empty value is the on switch
   * for ```plantuml diagram rendering (see renderPlantUMLDiagrams). The key
   * is snake_case to match the server's JSON tag verbatim — there is no
   * client-side casing transform.
   */
  plantuml_server_url?: string
  // The world a request lands in when the URL names none. '' / absent means
  // the raw default faces. Presentation only — the read grant is re-checked
  // per request, so this changes which face a bare URL resolves to, not who
  // may see it.
  default_world?: string
  // Whether THIS DEPLOYMENT can serve version history. Content versioning is
  // a postgres-only optional store capability; on fs/mem the `/_history`
  // endpoint answers a named 501.
  //
  // Consumers render the History affordance ABSENT when false — not disabled.
  // A greyed-out button still advertises a feature the deployment does not
  // have, and an ungated one can only fail.
  //
  // Absent (a server too old to send it) is treated as FALSE by the store,
  // unlike `worldReadable`'s unknown-is-permitted default. The directions
  // differ because the mistakes differ: hiding a working History button is a
  // missing feature someone reports, while showing a broken one on every fs
  // deployment is the affordance-that-lies shape this flag exists to remove.
  history_enabled?: boolean
}

export interface FormConfig {
  entity: string
  title?: string
  description?: string
  mode?: 'edit' | string
  body?: boolean
  /**
   * Wizard layout. Mutually exclusive with fields/relations (the server
   * rejects both). When present the form renders one step at a time with
   * next/back navigation instead of a single page.
   */
  steps?: FormStep[]
  fields?: FormField[]
  relations?: FormRelation[]
}

export interface FormStep {
  title: string
  description?: string
  /** Condition expression; the step is hidden/skipped when it evaluates false. */
  visible_when?: string
  fields?: FormFieldOrRelation[]
  relations?: FormFieldOrRelation[]
}

/**
 * What happens to a field's STORED value when its `visible_when` turns false
 * (BUG-FB0LN8). Hiding is presentation; the default does not touch data.
 *
 * - `no` (default) — keep the value; hide/reveal is lossless.
 * - `yes`          — clear it when the branch hides.
 *
 * `confirm` asks before clearing and undoes the TRIGGERING change on decline —
 * it is not merely `yes` with a prompt. It requires the propose/commit seam
 * (TKT-7S5735): the decision happens before anything is applied, so a decline
 * is a true no-op rather than a rollback.
 */
export type ClearWhenHidden = 'no' | 'yes' | 'confirm'

export interface FormField {
  property?: string
  widget?: string
  label?: string
  placeholder?: string
  help?: string
  default?: unknown
  readonly?: boolean
  hidden?: boolean
  /** Condition expression; the field is hidden when it evaluates false. */
  visible_when?: string
  /** Condition expression; the field is required only when it evaluates true. */
  required_when?: string
  /** Fate of the stored value when `visible_when` turns false. Default `no`. */
  clear_when_hidden?: ClearWhenHidden
  /**
   * Carry this field's value across a "Create & add another" reset
   * (TKT-7YHKD1) instead of clearing it. The reset is clean by default.
   */
  keep_on_add_another?: boolean
  /**
   * Width on the 12-column form grid (TKT-5V8704). Absent means full width —
   * one field per row, the default. Same semantics as a view section field's
   * span, so an author learns the model once.
   */
  span?: number
}

export interface RelationProperty {
  property: string
  label?: string
  required?: boolean
}

export interface FormRelation {
  relation: string
  direction?: 'outgoing' | 'incoming'
  target_type?: string
  label?: string
  required?: boolean
  widget?: string
  properties?: RelationProperty[]
  /** Condition expression; the relation widget is hidden when it evaluates false. */
  visible_when?: string
  /**
   * Carry this field's value across a "Create & add another" reset
   * (TKT-7YHKD1) instead of clearing it. The reset is clean by default.
   */
  keep_on_add_another?: boolean
}

// Unified type for form fields that can be either property fields or relation fields
export interface FormFieldOrRelation {
  // Property field props
  property?: string
  placeholder?: string
  help?: string
  default?: unknown
  readonly?: boolean
  hidden?: boolean
  transitions?: Record<string, string[]>
  // Relation field props
  relation?: string
  direction?: 'outgoing' | 'incoming'
  target_type?: string
  required?: boolean
  properties?: RelationProperty[]
  // Common props
  label?: string
  widget?: string
  /**
   * Carry this entry's value across a "Create & add another" reset
   * (TKT-7YHKD1). Applies to BOTH halves of the union — the batch context an
   * operator most wants to keep (a project, an assignee) is usually a
   * relation. See FormField.
   */
  keep_on_add_another?: boolean
  /**
   * Width on the 12-column form grid; absent = full width. See FormField.
   *
   * Only meaningful on the FIELD half of this union. A relation entry renders
   * via RelationCards / RelationPicker, which do not read it — those widgets
   * (a card list, a searchable multi-select) have a natural minimum width that
   * a narrow column would break. The config validator rejects a span on a
   * relation so an author is told rather than left wondering.
   */
  span?: number
  // Wizard conditions (see FormField / FormRelation)
  visible_when?: string
  required_when?: string
  /**
   * Fate of the stored value when `visible_when` turns false. Default `no`.
   * Property fields only — a relation's hidden-branch handling is the separate
   * `pruneWizardHiddenRelations` mechanism, and steps have no such key (a step
   * hiding is just "all its fields hid", each honoring its own setting).
   */
  clear_when_hidden?: ClearWhenHidden
}

export interface ListConfig {
  entity: string
  title?: string
  /** Markdown rendered above the list. `description` is a fallback alias. */
  header?: string
  /** Markdown rendered below the list. */
  footer?: string
  /** Fallback for `header`; the previously-unused field, used when `header` is unset. */
  description?: string
  columns: ListColumn[]
  filters?: ListFilter[]
  filter_controls?: FilterControl[]
  default_sort?: SortSpec[]
  create_form?: string
  /**
   * World the create button opens its form in, when a new entity belongs in a
   * different face than this list shows (a published ISMS list creates a
   * draft). Empty means the list's own world. The button is gated on the
   * principal being able to read THIS world, not the ambient one.
   */
  create_world?: string
  /**
   * Named query scope from the entity type's `query_scopes:`, deciding which
   * rows are members. Absent means the type's `default` scope applies; `all`
   * withdraws it.
   *
   * Sent as `?query_scope=` because the list endpoint is keyed by entity TYPE
   * — the server cannot tell which configured list is on screen, so the client
   * has to say. Never send an empty string: a name that does not resolve is a
   * 400, and "" resolves to nothing.
   */
  query_scope?: string
  edit_form?: string
  page_size?: number
  actions?: string[]
}

/**
 * Resolve the raw markdown for a view's top info region. `header` is canonical.
 * Returns '' when unset (a blank/whitespace-only value counts as unset). The
 * caller passes the result through renderMarkdown.
 *
 * `description` is an OPT-IN legacy alias, enabled per call site via
 * `allowDescriptionAlias`. Only lists pass it: there the field predated this
 * feature and was never rendered, so adopting it as an alias avoided rewriting
 * every existing config. Kanban deliberately has no such fallback (TKT-6S331G).
 *
 * The opt-in is a flag rather than a type constraint because TypeScript erases
 * at runtime: a structural parameter would happily read `description` off any
 * object carrying one, and these configs are parsed from the /_config response.
 * Relying on `KanbanConfig` merely lacking the field would make the policy a
 * comment that a later Go `Kanban.Description` field could silently void.
 */
export function viewHeaderMarkdown(
  view: { header?: string; description?: string } | undefined,
  opts: { allowDescriptionAlias?: boolean } = {}
): string {
  const alias = opts.allowDescriptionAlias ? view?.description?.trim() : ''
  return view?.header?.trim() || alias || ''
}

/** Resolve the raw markdown for a view's bottom info region ('' when unset). */
export function viewFooterMarkdown(view: { footer?: string } | undefined): string {
  return view?.footer?.trim() || ''
}

// Helper to get edit form for an entity type
export function getEditFormId(
  schemaStore: { forms: Map<string, FormConfig> },
  entityType: string
): string | undefined {
  for (const [formId, config] of schemaStore.forms) {
    if (config.entity === entityType && config.mode === 'edit') {
      return formId
    }
  }
  // Fallback to any form for this entity type
  for (const [formId, config] of schemaStore.forms) {
    if (config.entity === entityType) {
      return formId
    }
  }
  return undefined
}

export interface FilterControl {
  property?: string
  relation?: string
  // For relation filters: which edge direction the filter follows.
  // `outgoing` (default) pulls option candidates from the relation's `to[*]`
  // types; `incoming` from `from[*]`. Mirrors ListColumn.direction.
  direction?: 'outgoing' | 'incoming'
  label?: string
}

export interface ListColumn {
  property?: string
  relation?: string
  direction?: 'outgoing' | 'incoming'
  label?: string
  sortable?: boolean
  link?: string
  width?: string
}

export interface ListFilter {
  property: string
  operator?: string
  value?: string
  label?: string
  operators?: string[]
}

// SortSpec is imported from schema.ts

/**
 * Narrows what a Duplicate carries from the source entity (TKT-Z8K2FS).
 *
 * Absent means "carry every visible property" — it is not an opt-in switch, so
 * a type with no block still duplicates. `file` and state-machine properties
 * are excluded regardless, because carrying them yields a broken copy rather
 * than a narrower one.
 */
export interface DuplicateConfig {
  /** Allowlist of property names. Absent means all visible properties. */
  properties?: string[]
}

/** UX bindings for one metamodel entity type. */
export interface EntityViewConfig {
  /** View used to render an entity of this type; falls back to /entity/:type/:id. */
  detail_view?: string
  duplicate?: DuplicateConfig
}

export interface ViewConfig {
  entity: string
  title?: string
  sections: ViewSection[]
}

export interface ViewSection {
  type: 'properties' | 'relations' | 'content' | 'custom'
  title?: string
  properties?: string[]
  relations?: string[]
}

export interface KanbanConfig {
  entity: string
  title?: string
  /** Markdown rendered above the board. */
  header?: string
  /** Markdown rendered below the board. */
  footer?: string
  // Deliberately no `description`: unlike ListConfig, kanban has no legacy field
  // to adopt as a header alias. Enforced at the call site, which does NOT pass
  // viewHeaderMarkdown's `allowDescriptionAlias` — not by this field's absence,
  // which types alone cannot guarantee at runtime.
  column_property: string
  columns?: KanbanColumn[]
  swimlane_property?: string
  swimlanes?: KanbanSwimlane[]
  card: KanbanCard
  edit_form?: string
  create_form?: string
  filters?: Array<{ property: string; operator: string; value: string }>
  filter_controls?: FilterControl[]
  /** Named query scope; see the field of the same name on ListConfig. */
  query_scope?: string
}

export interface KanbanColumn {
  value: string
  label?: string
  color?: string
  /**
   * Name of an icon from the shared registry (see utils/icons.ts), rendered
   * beside the label. A NAME, never a glyph: an emoji written into `label`
   * renders verbatim, and the SPA never parses one back out of label text.
   */
  icon?: string
}

export interface KanbanSwimlane {
  value: string
  label?: string
  /** Icon name; see KanbanColumn.icon. */
  icon?: string
}

/**
 * One line of detail on a kanban card or a calendar event chip.
 *
 * Shared by both surfaces deliberately: they render the same thing — a label
 * and a value routed through the dense widget registry — so they take the same
 * config rather than two spellings of it.
 */
export interface KanbanCardField {
  property?: string
  relation?: string
  direction?: 'outgoing' | 'incoming'
  /** Overrides the displayed name; derived from property/relation when unset. */
  label?: string
  /** Renders the label before the value. Defaults to true when unset. */
  show_label?: boolean
}

/**
 * The label to render for a field.
 *
 * Resolution order: the field's explicit `label:`, then — for a relation — the
 * label authored on the relation type in schema.yaml, then the raw name.
 *
 * The relation lookup matters because a relation id reads like a field name:
 * `belongs-to` on a chip looks like a bug, while the metamodel already carries
 * "belongs to" one lookup away. Properties have no such authored label, and
 * this deliberately does NOT invent one by title-casing `assignee` — labels are
 * authored, never derived (DEC-6C1NAA), so an operator who wants "Assignee"
 * writes it.
 *
 * Nil: `relationLabel` may be omitted; callers without schema access simply get
 * the raw relation name, as before.
 */
export function cardFieldLabel(
  field: KanbanCardField,
  relationLabel?: (relation: string) => string | undefined
): string {
  if (field.label) return field.label
  if (field.relation) return relationLabel?.(field.relation) || field.relation
  return field.property || ''
}

/** Whether a field's label renders. Unset means true: an unlabelled ambiguous
 * value is worse than a redundant label. */
export function cardFieldLabelShown(field: KanbanCardField): boolean {
  return field.show_label !== false
}

/**
 * CalendarConfig is a month/week grid over date-bearing entities.
 *
 * Unlike a kanban it takes a LIST of sources: several entity types on one grid
 * is the point of a calendar, whereas a board groups one type by one property.
 *
 * Defaults (default_view, week_start, day_start, day_end, max_events_per_day,
 * and each source's max_span) are filled in server-side at config load, so the
 * SPA never has to re-implement them and the wire value is never empty.
 */
export interface CalendarConfig {
  title?: string
  /** Markdown rendered above the grid. */
  header?: string
  /** Markdown rendered below the grid. */
  footer?: string
  default_view: CalendarViewKind
  week_start: CalendarWeekStart
  sources: CalendarSourceConfig[]
  /** Extra fields on an event chip. Absent means title only. */
  event?: CalendarEventConfig
  /** Bounds of the week-view hour axis, "HH:MM". */
  day_start: string
  day_end: string
  /** Chips per month-view day cell before collapsing into "+N more". */
  max_events_per_day: number
  edit_form?: string
  create_form?: string
  filter_controls?: FilterControl[]
}

export type CalendarViewKind = 'month' | 'week'
export type CalendarWeekStart = 'monday' | 'sunday'

/** A named palette token; the theme owns what the colour actually is. */
export type CalendarColor = 'blue' | 'green' | 'amber' | 'red' | 'violet' | 'slate'

/**
 * CalendarSourceConfig projects one entity type onto the grid.
 *
 * Field names match a feed source so a single YAML anchor can serve both a
 * calendar and an ICS feed; `entity` carries the same yaml/json asymmetry as
 * lists and kanbans (yaml `entity_type`, json `entity`).
 */
export interface CalendarSourceConfig {
  entity: string
  /** Name shown in the legend that toggles this source; defaults to `entity`. */
  label?: string
  where?: string[]
  /** Date- or datetime-typed property placing the event on the grid. */
  date: string
  /** Optional end property; always the same kind as `date`. */
  end_date?: string
  summary?: string
  description?: string
  color?: CalendarColor
  /** Days to look back for events starting before the window (with end_date). */
  max_span: number
}

/** CalendarEventConfig is the kanban `card:` analogue for an event chip. It has
 * no title field: the source's `summary` already names the title property. */
export interface CalendarEventConfig {
  fields?: KanbanCardField[]
}

/**
 * GanttConfig declares a hierarchical timeline view. Depth is navigated
 * (drill-down) rather than displayed, because the hierarchy relations may be
 * self-referential (a project containing sub-projects, unbounded).
 * Server-normalized: the policy/cap fields always arrive filled.
 */
export interface GanttConfig {
  title?: string
  /** Markdown rendered above the chart. */
  header?: string
  /** Markdown rendered below the chart. */
  footer?: string
  /** Relation types traversed parent-to-child, as one set. */
  hierarchy: string[]
  multi_parent: 'first' | 'error'
  on_cycle: 'error' | 'prune' | 'mark'
  /** Levels expanded on first load; deeper levels reachable by drill-down. */
  default_depth: number
  max_depth: number
  max_nodes: number
  /** Date-role mapping keyed by entity TYPE (one mapping per type). */
  sources: Record<string, GanttSourceConfig>
  /** Extra fields on the hover card. Property fields only (server-enforced). */
  tooltip?: { fields?: KanbanCardField[] }
  filter_controls?: FilterControl[]
}

/** GanttSourceConfig maps one entity type's properties onto the three date
 * roles; each is independently optional. */
export interface GanttSourceConfig {
  start?: string
  end?: string
  committed?: string
  where?: string[]
  label?: string
  color?: CalendarColor
}

export interface KanbanCard {
  title: string
  subtitle?: string
  fields?: KanbanCardField[]
}

export interface DashboardConfig {
  title?: string
  description?: string
  cards: DashboardCard[]
}

export interface DashboardCard {
  title: string
  query: string
  display: 'count' | 'breakdown' | 'table'
  group_by?: string
  columns?: Array<{ property?: string; relation?: string; label?: string; link?: string }>
  sort?: Array<{ property: string; direction: 'asc' | 'desc' }>
  limit?: number
  /**
   * Optional ACL permission gating this card's visibility (TKT-53KICM).
   *
   * The SPA does not act on this: the server already omits cards the principal
   * cannot use from `/_dashboard`. It appears here only because `/_config`
   * serves the `dashboard:` block verbatim to everyone, so the type has to
   * describe what arrives. Never filter on it client-side — the SPA reads
   * booleans the server computed, it does not evaluate ACL.
   */
  permission?: string
}

/**
 * The per-principal dashboard payload from `/_dashboard`.
 *
 * Distinct from {@link DashboardConfig}, which is the verbatim config block on
 * `/_config`. `cards` is always an array — a project with no dashboard, an
 * empty `cards:`, and an all-filtered dashboard all arrive as `[]`.
 */
export interface DashboardResponse {
  title?: string
  description?: string
  cards: DashboardCard[]
}

/**
 * One priority tier, declared by the operator. List order IS priority order.
 * Served by /_config so the UI can label a band rather than echo a raw id.
 */
/**
 * How much a band's suggestion interrupts. A closed vocabulary rather than
 * styling knobs: the operator declares the volume, the UI decides what that
 * looks like.
 *
 * The levels differ in what the user must do to clear it, not in decoration:
 *
 * - `banner`    must be dealt with — act, snooze or mute. Onboarding, urgent.
 * - `notice`    same place, much quieter; easy to read past on purpose.
 * - `statusbar` you must go looking: a chip that expands on click. The default.
 */
export type NextActionProminence = 'banner' | 'notice' | 'statusbar'

export interface NextActionBand {
  id: string
  label?: string
  /** Defaults to 'statusbar' when unset. */
  prominence?: NextActionProminence
}

/**
 * One affordance on a suggestion: a discriminated union where exactly one of
 * action/set/navigate/snooze/dismiss/acknowledge is set. The server validates
 * that invariant, so the UI can switch on whichever field is present.
 */
/** One choice in a pick_one affordance, resolved server-side at render time. */
export interface NextActionPickOption {
  entity_id: string
  label: string
}

export interface NextActionOffer {
  label?: string
  /**
   * A render-time option list. The offer carries only the operator's config;
   * the live options arrive in the suggestion's `pick_options`, keyed by this
   * offer's index — the server resolves them through the same ACL-gated path
   * as the suggestion itself.
   */
  pick_one?: { query: string; limit?: number; action: string }
  action?: string
  set?: Record<string, string>
  confirm?: boolean
  navigate?: string
  snooze?: string[]
  dismiss?: boolean
  acknowledge?: boolean
}

/**
 * The one suggestion to show. Arrives FULLY RESOLVED — message already
 * interpolated, affordances attached — so the UI renders it rather than
 * re-deriving anything. The rules themselves are deliberately not served.
 */
export interface NextActionSuggestion {
  source: string
  band: string
  entity_id?: string
  /**
   * Opaque key component from the source's key_props. Echo it back verbatim
   * on feedback — it is part of the suggestion key, so omitting it stores a
   * snooze under a key the server never checks.
   */
  variant?: string
  message: string
  actions?: NextActionOffer[]
  /** Live pick_one options, keyed by the offer's index in `actions`. */
  pick_options?: Record<string, NextActionPickOption[]>
}

export interface NextActionResponse {
  suggestion: NextActionSuggestion | null
}

/** How a user answered a suggestion. */
export type NextActionFeedbackKind = 'snooze' | 'dismiss' | 'mute' | 'unmute' | 'shown'

export interface AnalyzeIssue {
  entityId: string
  entityType: string
  /** Optional headline shown when the row has no entity (e.g. validation rule name). */
  title?: string
  message: string
  /**
   * Per-entity explanation a Lua validation rule returned for THIS entity:
   * which of the rule's several possible defects it has, and what to do
   * about it. Shown alongside `message`, which stays the rule's own
   * description and is identical across the rule's rows. Absent on every
   * row that is not a Lua-rule violation.
   */
  ruleMessage?: string
  severity: 'error' | 'warning'
  checkType: string
  /**
   * Optional structured specifics about why the issue fired, beyond the
   * flat message. For content required-headers violations it lists the
   * missing exact headers. Absent on rows with no structured detail;
   * the message cell reveals it in an expandable detail row.
   */
  detail?: string[]
  /**
   * Present only on validation script-error rows. Carries the same
   * envelope as the action surface so the UI can branch: rows with
   * scriptError open ScriptErrorDialog instead of navigating.
   */
  scriptError?: ScriptError
}

export interface AnalyzeResult {
  errors: number
  warnings: number
  issues: AnalyzeIssue[]
  byCheck: Record<string, number>

  /**
   * Names of checks that found MORE issues than they returned. Each check
   * is capped server-side (TKT-1ESTYJ), so `byCheck` for a listed check is
   * the cap, not the true total.
   *
   * Omitted by the server when nothing was truncated, so it is optional —
   * and older servers never send it at all.
   */
  truncatedChecks?: string[]
}

export interface NavigationEntry {
  // Direct item fields
  label?: string
  list?: string
  dashboard?: boolean
  kanban?: string
  calendar?: string
  search?: boolean
  settings?: boolean
  action?: string
  /** Names a standalone document (one configured without an entity_type). */
  document?: string
  /** Global named permission required for this entry to appear in the sidebar.
   *  The SPA does not act on it — the server already omits filtered entries
   *  from /_sidebar, which is what the menu is built from. Present here only
   *  because /_config serves the navigation tree verbatim. */
  permission?: string
  icon?: string
  /** Entity type listed by an `entities:` entry (TKT-PEKL8L). */
  entities?: string
  /** Query scope for an `entities:` entry. */
  query_scope?: string
  /** Link order for an `entities:` entry. */
  sort?: SortSpec[]
  // Group fields
  group?: string
  collapsed?: boolean
  items?: NavigationEntry[]
}

// Sidebar API types (denormalized navigation)
export interface SidebarItem {
  label: string
  href?: string
  /** Glyph name, or the reserved 'none' when the author suppressed the icon.
   * Sent as 'none' rather than empty because the field is omitempty on the
   * wire: an empty string would be dropped and become indistinguishable from
   * an entry that never had an icon. */
  icon?: string
  /** The glyph this entry's kind would have produced, sent only when `icon`
   * suppresses it. The collapsed sidebar falls back to this, since a row with
   * neither icon nor label is invisible but still clickable. */
  derivedIcon?: string
  action?: string
  /** Set on an `entities:` entry (TKT-PEKL8L): the list query whose rows
   * become one link each. `label` and `href` are empty on such an item; the
   * rows supply both. */
  entities?: SidebarEntities
}

/** The list query behind an `entities:` sidebar entry, in the list
 * endpoint's own parameter grammar. */
export interface SidebarEntities {
  type: string
  /** Sent as `?query_scope=`; absent applies the type's default scope. */
  query_scope?: string
  /** Effective order in `sort=` grammar (`title,-due`); absent = id order. */
  sort?: string
}

export interface SidebarGroup {
  group?: string
  collapsed?: boolean
  items: SidebarItem[]
}

export interface SidebarData {
  app: AppConfig
  navigation: SidebarGroup[]
  /** URL of the user-uploaded sidebar logo (with cache-busting query
   *  parameter), or null/undefined when no logo is set. */
  logoUrl?: string | null
  /**
   * Entity type → form id for inline creation from a relation field.
   * A type is present only when the principal may create it AND a create
   * form resolves for it, so presence alone is the affordance — no
   * client-side permission arithmetic, and no reimplementation of the
   * server's form-resolution ordering.
   *
   * Rides on the sidebar because it is the one boot-time payload that is
   * principal-scoped (`_config` is pinned principal-independent;
   * `_schema` is a pure metamodel projection).
   *
   * A UI hint only: the create POST re-authorizes.
   */
  inline_create?: Record<string, string>
}

// Document config, mirroring the Go `dataentryconfig.DocumentConfig` that
// /_config serves verbatim. Exactly one of `command` / `script` is set
// (enforced server-side).
export interface DocumentConfig {
  title?: string
  /** Entity type this document applies to (for frontend filtering).
   *  ABSENT means a standalone document: it renders at /document/:name with
   *  no entry entity, rather than being attached to one. */
  entity_type?: string
  command?: string
  script?: string
  timeout?: number
  /** Global named permission required to render this document, if any.
   *  Present here because the config is not secret; the SPA does not act on
   *  it — the render endpoint enforces it and returns 403. */
  permission?: string
  edit?: DocumentEdit
}

// DocumentEdit configures the Edit button on the full-page document view.
// Both fields are required when the parent block is present (validated server-side).
export interface DocumentEdit {
  form: string
  label: string
}

// Response from document render API
export interface DocumentRenderResponse {
  html: string
  cached: boolean
  entity_ids: string[] // IDs of entities involved in this document (for SSE filtering)
}

// Command available for a page context
//
// No auto_open: TKT-93FUCV removed the server-side launcher it selected, and
// the server no longer sends the field. Command output files are downloaded by
// explicit click.
export interface Command {
  id: string
  label: string
  confirm?: string
  context: 'entity' | 'list' | 'view' | 'global'
}
