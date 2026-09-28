<script setup lang="ts">
import { ref, watch, computed, onBeforeUnmount } from 'vue'
import { useSchemaStore, useEntitiesStore, useUIStore } from '@/stores'
import { isCancelledFetch } from '@/composables/usePageData'
import EntityTargetSelect from '@/components/common/EntityTargetSelect.vue'
import TagSelect from '@/components/ui/TagSelect.vue'
import type {
  ListConfig,
  EntityType,
  FilterControl,
  PropertyDef,
  FilterState,
  Entity,
} from '@/types'

const props = defineProps<{
  // Only the filter controls are read, so a board (KanbanConfig) passes its
  // own config as a list does.
  config: Pick<ListConfig, 'filter_controls'>
  entityType?: EntityType
  filters: FilterState
}>()

const emit = defineEmits<{
  filter: [filters: FilterState]
}>()

const schemaStore = useSchemaStore()
const entitiesStore = useEntitiesStore()
const uiStore = useUIStore()

// Debounce window for text-input filters. Select/multi-select/relation fire
// immediately because they only change on a deliberate click.
const TEXT_DEBOUNCE_MS = 250

// At or below this many resolved targets, a relation filter renders as a plain
// native <select>; above it, as a typeahead combobox (TKT-DL16XM).
const RELATION_FILTER_SELECT_MAX = 10

// Per-source-type candidate fetch cap, matching RelationPicker. Source types
// with more than this many entities silently truncate their option set — the
// documented ceiling that would trigger a server-side _filter_options endpoint.
const CANDIDATE_FETCH_LIMIT = 100

// Resolved filter control with computed widget type and options.
// `relation` widgets carry their resolved candidate entities; property widgets
// carry enum `options` strings.
interface ResolvedFilter {
  key: string
  label: string
  widget: 'select' | 'multi-select' | 'text' | 'relation'
  options: string[]
  // Display labels keyed by option value (display-only; filter value stays raw).
  optionLabels: Record<string, string>
  isRelation: boolean
  // Text widgets on string properties match case-insensitively on a substring
  // (the API's `contains`), so users need not type the exact stored value.
  substring?: boolean
  // relation widgets only:
  relationCandidates?: Entity[]
  relationMode?: 'select' | 'typeahead'
}

// Resolve the display text for an option value in a filter dropdown.
function optionText(filter: ResolvedFilter, option: string): string {
  return filter.optionLabels[option] ?? option
}

// Candidate entities per relation-filter key, fetched on mount. Keyed by the
// relation name (the control key). Empty until the fetch resolves.
const relationCandidates = ref<Record<string, Entity[]>>({})

const resolvedFilters = computed((): ResolvedFilter[] => {
  if (!props.config.filter_controls) return []
  return props.config.filter_controls.map((fc) => resolveFilter(fc))
})

function resolveFilter(fc: FilterControl): ResolvedFilter {
  const key = fc.relation || fc.property || ''
  // DEC-6C1NAA: a label is authored, never derived — an unset filter label
  // shows the raw property/relation id.
  const label = fc.label || key

  if (fc.relation) {
    const candidates = relationCandidates.value[key] ?? []
    const mode = candidates.length > RELATION_FILTER_SELECT_MAX ? 'typeahead' : 'select'
    return {
      key,
      label,
      widget: 'relation',
      options: [],
      optionLabels: {},
      isRelation: true,
      relationCandidates: candidates,
      relationMode: mode,
    }
  }

  // Property filter
  const propDef = props.entityType?.properties[fc.property || '']
  if (!propDef) {
    return { key, label, widget: 'text', options: [], optionLabels: {}, isRelation: false, substring: true }
  }

  const options = propDef.values || []
  const widget = resolveWidgetType(propDef, options)
  const optionLabels = schemaStore.resolveOptionLabels(propDef, fc.property || '', props.entityType)

  const substring = widget === 'text' && propDef.type === 'string'
  return { key, label, widget, options, optionLabels, isRelation: false, substring }
}

// Source entity types for a relation filter's option candidates. Incoming
// filters keep rows whose incoming SOURCES match, so candidates come from the
// relation's `from[*]`; outgoing (default) from `to[*]` — mirroring
// RelationPicker.targetTypes and FilterControl.Direction semantics.
function relationSourceTypes(fc: FilterControl): string[] {
  const relType = schemaStore.getRelationType(fc.relation || '')
  if (!relType) return []
  return fc.direction === 'incoming' ? relType.from : relType.to
}

// Fetch candidate entities for every relation filter control. Uses the same
// generic list endpoint (via entitiesStore.fetchList) that RelationPicker uses
// — no dedicated backend.
//
// Each control loads in its own try/catch so one control's failure never
// affects a sibling: a cancelled fetch (rapid nav) is suppressed silently; any
// other error surfaces a toast and leaves that control's option set empty. Run
// via a watcher below so a later props.config change refetches (RR-L78S8H).
async function loadRelationCandidates() {
  for (const fc of props.config.filter_controls || []) {
    if (!fc.relation) continue
    const relation = fc.relation
    const types = relationSourceTypes(fc)
    try {
      const collected: Entity[] = []
      for (const type of types) {
        const result = await entitiesStore.fetchList(type, { per_page: CANDIDATE_FETCH_LIMIT })
        collected.push(...result.data)
      }
      relationCandidates.value[relation] = collected
    } catch (err) {
      // Suppress cancellations (rapid nav); continue to the next control so a
      // sibling isn't left permanently unloaded (RR-TE8HA6).
      if (isCancelledFetch(err)) continue
      uiStore.error(`Could not load filter options for “${fc.label || relation}”.`)
      console.error(`Failed to load relation filter candidates for ${relation}:`, err)
    }
  }
}

function resolveWidgetType(
  propDef: PropertyDef,
  options: string[]
): 'select' | 'multi-select' | 'text' {
  // Multi-select for list properties with enum values
  if (propDef.list && options.length > 0) {
    return 'multi-select'
  }
  // Select for properties with defined values (enums)
  if (options.length > 0) {
    return 'select'
  }
  // Text for everything else
  return 'text'
}

// Which control keys are text widgets (vs select / multi-select). Text
// widgets debounce and may have in-progress unsent input; the props.filters
// watcher must NOT clobber them. Select widgets fire immediately on change
// so there's no in-progress state to preserve.
const textWidgetKeys = computed(() => {
  const set = new Set<string>()
  for (const filter of resolvedFilters.value) {
    if (filter.widget === 'text') set.add(filter.key)
  }
  return set
})

// Local widget state is just a string per control, but we hold onto each
// property's incoming operator separately so non-default ops (e.g. `<=` from a
// deep-linked URL) survive a user edit. Widgets don't yet expose operator
// selection — that's a future enhancement.
function initializeFilters(existingFilters: FilterState): Record<string, string> {
  const result: Record<string, string> = {}
  for (const control of props.config.filter_controls || []) {
    const key = control.property || control.relation
    if (key) {
      result[key] = existingFilters[key]?.value ?? ''
    }
  }
  return result
}

function captureOperators(existingFilters: FilterState): Record<string, string | undefined> {
  const ops: Record<string, string | undefined> = {}
  for (const control of props.config.filter_controls || []) {
    const key = control.property || control.relation
    if (key) ops[key] = existingFilters[key]?.op
  }
  return ops
}

const localFilters = ref<Record<string, string>>(initializeFilters(props.filters))
const preservedOps = ref<Record<string, string | undefined>>(captureOperators(props.filters))

// Debounce timer for text-input filters. Hoisted above the props.filters
// watcher because that watcher needs to check whether an edit is mid-flight.
let textDebounceTimer: ReturnType<typeof setTimeout> | null = null

watch(
  () => props.filters,
  (newFilters) => {
    // When an external change arrives (back/forward nav, programmatic URL
    // update, another tab) while the user is mid-type in a text widget,
    // naively reassigning localFilters would drop their keystrokes. Preserve
    // the text-widget values when a debounce is pending; the pending timer
    // will then emit what the user *actually* typed, not the externally
    // supplied value.
    const rebuilt = initializeFilters(newFilters)
    if (textDebounceTimer !== null) {
      for (const key of textWidgetKeys.value) {
        rebuilt[key] = localFilters.value[key] ?? ''
      }
    }
    localFilters.value = rebuilt
    preservedOps.value = captureOperators(newFilters)
  }
)

function buildState(): FilterState {
  const state: FilterState = {}
  const substringKeys = new Set(resolvedFilters.value.filter((f) => f.substring).map((f) => f.key))
  for (const [key, value] of Object.entries(localFilters.value)) {
    if (!value) continue
    const fv: FilterState[string] = { value }
    // A deep-linked operator wins; otherwise a string text box matches on `~`.
    const op = preservedOps.value[key] ?? (substringKeys.has(key) ? '~' : undefined)
    // Omit op when it's absent or the default '=' form — same convention
    // as buildQueryWithFilters, so the state shape is canonical throughout.
    if (op && op !== '=') fv.op = op
    state[key] = fv
  }
  return state
}

function emitFilters() {
  emit('filter', buildState())
}

function handleTextInput() {
  if (textDebounceTimer) clearTimeout(textDebounceTimer)
  textDebounceTimer = setTimeout(() => {
    textDebounceTimer = null
    emitFilters()
  }, TEXT_DEBOUNCE_MS)
}

function handleFilterChange() {
  // Select widgets fire here — flush any pending text debounce so a select
  // change doesn't get clobbered by a stale text emit.
  if (textDebounceTimer) {
    clearTimeout(textDebounceTimer)
    textDebounceTimer = null
  }
  emitFilters()
}

// Relation target selector committed a value (a bare display title, or '' to
// clear). Fires immediately like a select — no debounce, no mid-type state.
function handleRelationChange(key: string, value: string) {
  localFilters.value[key] = value
  handleFilterChange()
}

// A multi-select commits under `in` once there is more than one value:
// equality against a comma-joined string ("a,b") matches nothing, and `in` is
// the operator the API defines for multi-value (BUG-AMK38R).
//
// A SINGLE value stays on `=`. The comma-joined `in` form cannot represent a
// value that itself contains a comma ("Legal, Risk & Compliance") — the server
// would split it into two members and match the wrong rows — whereas `=`
// compares the value whole and is always correct. Keeping one selection on `=`
// means the common case is exact; only a multi-selection that includes a
// comma-bearing value is affected, and expressing that needs the repeated
// `filter[x][in][]=` form, which FilterState (one string per key) cannot carry
// today. Tracked with the wire-format work in TKT-UTJ24Z.
function handleMultiSelectChange(key: string, selected: string[]) {
  localFilters.value[key] = selected.join(',')
  preservedOps.value[key] = selected.length > 1 ? 'in' : undefined
  handleFilterChange()
}

function getMultiSelectValues(key: string): string[] {
  const val = localFilters.value[key]
  if (!val) return []
  return val.split(',').filter(Boolean)
}

function clearFilters() {
  if (textDebounceTimer) {
    clearTimeout(textDebounceTimer)
    textDebounceTimer = null
  }
  localFilters.value = {}
  preservedOps.value = {}
  emit('filter', {})
}

function hasActiveFilters(): boolean {
  return Object.values(localFilters.value).some((v) => v)
}

// Load candidates on mount and whenever the set of relation filter controls
// changes (component reuse across route params, config reload). Keyed on the
// relation names so an unrelated config edit doesn't refetch (RR-L78S8H).
watch(
  () =>
    (props.config.filter_controls || [])
      .map((fc) => `${fc.relation ?? ''}:${fc.direction ?? ''}`)
      .join('|'),
  () => {
    loadRelationCandidates()
  },
  { immediate: true }
)

onBeforeUnmount(() => {
  if (textDebounceTimer !== null) {
    clearTimeout(textDebounceTimer)
    textDebounceTimer = null
  }
})
</script>

<template>
  <div class="filter-bar">
    <div class="filters">
      <div v-for="filter in resolvedFilters" :key="filter.key" class="filter-item">
        <label :for="`filter-${filter.key}`">
          {{ filter.label }}
        </label>

        <!-- Select widget -->
        <select
          v-if="filter.widget === 'select'"
          :id="`filter-${filter.key}`"
          v-model="localFilters[filter.key]"
          @change="handleFilterChange"
        >
          <option value="">All</option>
          <option v-for="option in filter.options" :key="option" :value="option">
            {{ optionText(filter, option) }}
          </option>
        </select>

        <!-- Multi-select widget — chip/tag picker with search, the same
             TagSelect edit forms use via MultiSelectWidget. -->
        <TagSelect
          v-else-if="filter.widget === 'multi-select'"
          :model-value="getMultiSelectValues(filter.key)"
          :options="filter.options"
          :option-labels="filter.optionLabels"
          :placeholder="`Filter by ${filter.label}`"
          @update:model-value="(v: string[]) => handleMultiSelectChange(filter.key, v)"
        />

        <!-- Relation widget — select (small) or typeahead (large) target
             picker. Commits the target's bare display title as the value,
             which the backend relation filter matches on. -->
        <EntityTargetSelect
          v-else-if="filter.widget === 'relation'"
          :control-id="`filter-${filter.key}`"
          :candidates="filter.relationCandidates || []"
          :mode="filter.relationMode || 'select'"
          :model-value="localFilters[filter.key] || ''"
          :placeholder="`Filter by ${filter.label}`"
          all-label="All"
          @update:model-value="(v: string) => handleRelationChange(filter.key, v)"
        />

        <!-- Text widget (default) — debounced to avoid a fetch per keystroke -->
        <input
          v-else
          :id="`filter-${filter.key}`"
          v-model="localFilters[filter.key]"
          type="text"
          :placeholder="`Filter by ${filter.label}`"
          @input="handleTextInput"
        />
      </div>
    </div>
    <button v-if="hasActiveFilters()" class="clear-filters" @click="clearFilters">
      Clear filters
    </button>
  </div>
</template>

<style scoped>
.filter-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 16px;
  border-bottom: 1px solid var(--border-color);
}

.filters {
  display: flex;
  gap: var(--space-lg);
  flex-wrap: wrap;
  /* Top-align: the tag picker grows downward as chips wrap onto a second row,
     and the default `stretch` would drag its neighbours' controls out of line
     with it. Aligning at the top keeps every label and control on one line
     regardless of how many chips are selected. */
  align-items: flex-start;
}

.filter-item {
  display: flex;
  flex-direction: column;
  gap: var(--space-2xs);
}

.filter-item label {
  font-size: var(--font-size-xs);
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  color: var(--muted-text);
}

/* The multi-enum tag picker (TagSelect → SlimSelect) sits in the same row as
   scalar selects and text inputs, so the three have to agree on height, radius,
   font size and min-width or the filter bar looks ragged.

   TagSelect's `<style>` is GLOBAL (no `scoped`), and its DOM therefore carries
   no `data-v` of this component — `:deep()` cannot reach it from here. Height,
   radius and font-size live on `.ss-main` in TagSelect itself; only the
   filter-bar-specific min-width is set here, via a global rule nested under
   `.filter-bar` so it cannot leak to other TagSelect consumers. */
.filter-item select,
.filter-item input {
  padding: 6px 10px;
  min-height: 38px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md);
  font-size: var(--font-size-base);
  min-width: 150px;
  background: var(--input-bg);
  color: var(--text-color);
}

.filter-item select:focus,
.filter-item input:focus {
  outline: none;
  border-color: var(--accent-color);
  box-shadow:
    0 0 0 2px var(--focus-ring-gap),
    0 0 0 4px var(--focus-ring);
}

.clear-filters {
  padding: 6px 12px;
  background: none;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-sm);
  font-size: var(--font-size-dense);
  color: var(--muted-text);
  cursor: pointer;
  transition: all 0.15s;
}

.clear-filters:hover {
  background: var(--hover-bg);
  color: var(--text-color);
}

@media (max-width: 768px) {
  .filter-bar {
    border: none;
    padding: 0 0 12px 0;
    margin-bottom: 4px;
    /* Stack filters above the clear button so the button doesn't get
       stretched vertically next to a wrapping filter grid. */
    flex-direction: column;
    align-items: stretch;
    gap: var(--space-sm);
  }

  /* Filters fill the width and wrap evenly. Each item flexes from a small
     min-width so up to two enum filters fit on a row before wrapping. */
  .filters {
    width: 100%;
    gap: var(--space-sm);
  }

  .clear-filters {
    align-self: flex-end;
  }

  .filter-item {
    flex: 1 1 calc(50% - 4px);
    min-width: 0;
  }

  /* Free-text filters (assignee etc.) get a full row on mobile because
     the input target is more useful at full width. */
  .filter-item:has(input[type='text']) {
    flex: 1 1 100%;
  }

  .filter-item select,
  .filter-item input {
    width: 100%;
    min-width: 0;
  }
}
</style>

<!-- TagSelect's styles are global and live in ITS OWN route chunk, which the
     list page does not load — so on this page `.ss-main` would otherwise fall
     back to browser defaults (notably a 16px font next to the 14px <select>
     beside it). These rules put the filter-bar's copy in the list chunk.

     Nested under `.filter-bar` so they apply to the list filter row only and
     cannot reach the same widget in edit forms or Settings, and global rather
     than `scoped` because TagSelect renders no `data-v` of this component. -->
<style>
.filter-bar .filter-item .ss-main {
  min-width: 150px;
  min-height: 38px;
  font-size: var(--font-size-base);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md);
  background: var(--input-bg);
  color: var(--text-color);
}

.filter-bar .filter-item .ss-main .ss-placeholder {
  color: var(--muted-text);
}

.filter-bar .filter-item .ss-main:focus-within {
  border-color: var(--accent-color);
  box-shadow:
    0 0 0 2px var(--focus-ring-gap),
    0 0 0 4px var(--focus-ring);
}

@media (max-width: 768px) {
  .filter-bar .filter-item .ss-main {
    min-width: 0;
    width: 100%;
  }
}
</style>
