<script setup lang="ts">
import { ref, shallowRef, computed, watch, onMounted, onUnmounted, type Component } from 'vue'
import { RouterLink, useRoute, useRouter, type RouteLocationRaw } from 'vue-router'
import { useQuery, useMutation, useQueryCache } from '@pinia/colada'
import { useSchemaStore, useUIStore } from '@/stores'
import { useListKeyboard } from '@/composables/useListKeyboard'
import { useListSelection } from '@/composables/useListSelection'
import { useListActions } from '@/composables/useListActions'
import { useUrlFilterSync } from '@/composables/useUrlFilterSync'
import { useWorld } from '@/composables/useWorld'
import { useCreateTarget } from '@/composables/useCreateTarget'
import { listEntities, deleteEntity, getErrorMessage } from '@/api'
import { entityKeys } from '@/queries/entities'
import { beginOptimisticRemove, rollbackOptimistic } from '@/queries/optimisticList'
import { toApiOperator, filterStateToApiParams } from '@/utils/filters'
import { entityDetailHref } from '@/utils/entityRoute'
import { entityRef, refFace } from '@/utils/entityRef'
import { worldText } from '@/utils/worldText'
import { safeInternalHref, shouldDeferToBrowser } from '@/utils/openIntent'
import { entityDisplayTitle } from '@/utils/entityDisplay'
import { renderMarkdown } from '@/utils/markdown'
import { actionAllowed } from '@/utils/affordancesWarning'
import { getCellValue, formatCellValue } from '@/utils/format'
import { densePropertyRoutingHint, isDenseEmpty } from '@/widgets/viewRouting'
import { defaultRegistry } from '@/widgets/registry'
import type { DenseRoutingHint } from '@/widgets/viewRouting'
import type { Entity, ListMeta, ListParams, ListResponse, FilterState } from '@/types'
import { viewHeaderMarkdown, viewFooterMarkdown } from '@/types'
import FilterBar from './FilterBar.vue'
import Pagination from './Pagination.vue'
import SearchBox from './SearchBox.vue'
import AdHocFilterMenu from './AdHocFilterMenu.vue'
import BackButton from '@/components/common/BackButton.vue'
import ExportMenu from '@/components/entity/ExportMenu.vue'
import WorldBadge from '@/components/entity/WorldBadge.vue'
import WorldBanner from '@/components/common/WorldBanner.vue'
import { listExportUrl } from '@/api/transforms'
import { useBackTarget } from '@/composables/useBackTarget'
import { useConfirm } from '@/composables/useConfirm'

const props = defineProps<{
  listId: string
}>()

const route = useRoute()
const router = useRouter()
const schemaStore = useSchemaStore()
const uiStore = useUIStore()
const { confirm } = useConfirm()

// Back affordance — renders when ?return_to= or ?from= is present. See TKT-JIEKC.
const backTarget = useBackTarget()

// Responsive: detect mobile for card vs table layout
const mobileQuery = typeof window !== 'undefined' ? window.matchMedia('(max-width: 768px)') : null
const isMobile = ref(mobileQuery?.matches ?? false)
function onMediaChange(e: MediaQueryListEvent) { isMobile.value = e.matches }
onMounted(() => { mobileQuery?.addEventListener('change', onMediaChange) })
onUnmounted(() => { mobileQuery?.removeEventListener('change', onMediaChange) })

// State
//
// Page is the only piece of list state the client owns directly; it feeds
// the query key (input). Everything else below is derived from the query
// result (output). Splitting page-as-input from meta-as-output removes the
// read/write cycle the old single `meta.page` had.
//
// The actual useQuery() call lives lower (after queryParams is defined,
// since its `enabled` is evaluated synchronously during setup) and assigns
// into this holder. The derived computeds here read it lazily, so they can
// be declared up top where useListActions / useListKeyboard need them.
const page = ref(1)
const queryCache = useQueryCache()

type ListQuery = ReturnType<typeof useQuery<ListResponse<Entity>>>
const listQueryRef = shallowRef<ListQuery>()

const entities = computed<Entity[]>(() => listQueryRef.value?.data.value?.data ?? [])
const meta = computed<ListMeta>(
  () =>
    listQueryRef.value?.data.value?.meta ?? {
      total: 0,
      page: page.value,
      per_page: listConfig.value?.page_size || 25,
      has_more: false,
    }
)
// `isPending` is true while a query key has no resolved data. Same-key SSE
// refetches keep the entry `success` (placeholderData holds the rows, no
// spinner — the liveness win).
//
// A param change (page/filter/sort) swaps to a NEW key whose entry starts
// out pending — but `placeholderData: (prev) => prev` below seeds it from
// the previous entry, and Colada then exposes the state as
// `{status: 'success', data: placeholderData}`. So `isPending` stays false
// and the old rows are held on screen instead of flashing the spinner.
// The block spinner is therefore reached only on a cold first load.
// Pinned by the "pagination keeps previous rows" tests.
const loading = computed(() => listQueryRef.value?.isPending.value ?? true)

// pageState is a stable, test-visible signal of whether this screen has
// finished resolving — the same contract DynamicForm's `form-state-*` marker
// provides, generalized so a screenshot can wait for ANY screen rather than
// only an edit form. Without it a capture has nothing to poll and can only
// hang until its timeout, which is why screenshot{} used to refuse every view
// but the form.
const pageState = computed<'pending' | 'loaded' | 'error'>(() => {
  if (loadError.value) return 'error'
  return loading.value ? 'pending' : 'loaded'
})
const loadError = computed(() => {
  const err = listQueryRef.value?.error.value
  return err ? getErrorMessage(err, 'Failed to load entities') : null
})
const includedEntities = computed<Record<string, Entity>>(
  () => listQueryRef.value?.data.value?.included ?? {}
)
// Collection-scope verb verdicts (e.g. {create: true|false}). Always
// emitted by the data-entry server; absent only for non-data-entry
// callers, in which case affordances render defensively (the server
// still 403s on click). See `_actions` in api-reference.md.
const collectionActions = computed<Record<string, boolean> | undefined>(
  () => listQueryRef.value?.data.value?._actions
)

// Affordance gates: `_actions` map from the server. `false` → hide;
// anything else → render. Helper keeps the contract DRY across
// components; see frontend/src/utils/affordancesWarning.ts.
function canCreate(): boolean {
  return actionAllowed({ _actions: collectionActions.value }, 'create')
}
// From `_actions` alone, under every world. The server computes the map for
// the FACE it served (a stand-in published face reports `update: false`
// unless a grant names that face), and every write this list makes goes to
// that row's address (`entityRef`), so the verdict and the write agree by
// construction. An earlier revision ANDed in `!isWorldBound` here, which made
// every list read-only under a configured `default_world` — including lists
// of types that declare no faces at all (atlas worlds issue 2).
//
// canUpdate gates the bulk-action bar (via anySelectedAllowsUpdate); canDelete
// gates the row delete button AND the Delete/Backspace shortcut.
function canDelete(entity: Entity): boolean {
  return actionAllowed(entity, 'delete')
}
function canUpdate(entity: Entity): boolean {
  return actionAllowed(entity, 'update')
}
// Bulk-action visibility: an action shows iff at least one selected
// entity permits the underlying `update` write. (All bulk actions
// today reduce to `update` at the entity level; transition / relation
// verbs are deferred to phase 3.) Returns true when nothing is
// selected (the bar isn't visible anyway) or when no `_actions` data
// is loaded yet (defensive fallback).
function anySelectedAllowsUpdate(): boolean {
  if (selectedIds.value.size === 0) return true
  for (const e of entities.value) {
    if (selectedIds.value.has(e.id) && canUpdate(e)) return true
  }
  return false
}

// Selection and actions
const { selectedIds, toggle: toggleSelection, clear: clearActionSelection, isSelected, selectAll } = useListSelection()
const hasSelection = computed(() => selectedIds.value.size > 0)

const listIdRef = computed(() => props.listId)

const { resolvedActions, processing: actionProcessing, executeAction, triggerAction } = useListActions({
  listId: listIdRef,
  selectedIds,
  entities,
  // A bulk `set` writes the ROW on screen. Selection is keyed by entity id,
  // so the address is looked up per row at execution time.
  addressOf: (entityId) => {
    const row = entities.value.find((e) => e.id === entityId)
    return row ? entityRef(row) : entityId
  },
  onClearSelection: () => clearActionSelection(),
  onRequestConfirm: (action, actionId, triggerEl) => {
    void requestActionConfirm(action, actionId, triggerEl)
  },
  // Bulk actions mutate entities server-side; invalidate the list so it
  // refetches the post-action state.
  onComplete: () => {
    void queryCache.invalidateQueries({ key: entityKeys.list(listConfig.value?.entity ?? '') })
  },
})

// Bulk action confirm. We don't pass executeAction as onConfirm because it
// uses Promise.allSettled internally and never throws — partial failures
// surface via uiStore.error and the script-error dialog. Wrapping it in
// onConfirm would silently report success even when 100% of writes failed.
// Instead: confirm-then-fire-and-forget. The action toasts its own results.
async function requestActionConfirm(
  action: import('@/types').ActionConfig,
  actionId: string,
  triggerEl: HTMLElement | null,
) {
  const ok = await confirm({
    title: `${action.label}?`,
    message:
      typeof action.confirm === 'string'
        ? action.confirm
        : `Apply ${action.label} to ${selectedIds.value.size} selected entities?`,
    confirmLabel: action.label,
  })
  if (!ok) return
  void executeAction(actionId, action, triggerEl)
}

// Static (config-pinned) filter properties — used by useUrlFilterSync to
// reject URL filters that would silently override the list's intended scope.
// Computed inline (not via configuredFilters) to avoid a forward reference.
function staticFilterProperties(): Set<string> {
  const list = schemaStore.getList(props.listId)
  const set = new Set<string>()
  for (const f of list?.filters || []) {
    if (f.operator && f.value && f.property) set.add(f.property)
  }
  return set
}

// User-selected filters and free-text search synced bidirectionally with the URL.
const { filters, q: searchQuery, writeToQuery } = useUrlFilterSync({ staticFilterProperties })

// The selected world (`?world=`). A list is one of only two surfaces the API
// can serve under a non-default world — see worldCapablePath in
// internal/dataentry/world.go.
const { world, isWorldBound, worldParam } = useWorld()

// The world's projection note, in the operator's words (`messages.projection`)
// and only on a list of a type that declares faces: a type without faces has
// one state in every world, so nothing on its list is filtered by the world
// and the note would be false. Nothing declared, nothing rendered — the app
// has no sentence of its own for this (TKT-5SZG2L).
const listTypeHasFaces = computed(() => {
  const def = schemaStore.getEntityType(listConfig.value?.entity ?? '')
  return Object.keys(def?.faces ?? {}).length > 0
})
const projectionNote = computed<string>(() => {
  if (!listTypeHasFaces.value) return ''
  const info = world.value ? schemaStore.worlds.get(world.value) : undefined
  return worldText(info?.messages?.projection, { world: world.value })
})

// The create button's destination — see useCreateTarget for why a create
// button carries a world of its own. Whether it SHOWS is `_actions.create`.
const { target: createFormTarget } = useCreateTarget(
  computed(() => listConfig.value?.create_form),
  computed(() => listConfig.value?.create_world),
  worldParam,
)

// The operator's announcement for the world on screen, or '' to announce
// nothing. Config, not data — `/_schema`.worlds is served identically to every
// principal, so this discloses nothing per-caller. Same computed as
// EntityDetail's, deliberately spelled the same way in both.
const worldBanner = computed<string>(
  () => (world.value ? schemaStore.worlds.get(world.value)?.banner : '') || '',
)
const searchBoxRef = ref<InstanceType<typeof SearchBox> | null>(null)
const filterMenuRef = ref<InstanceType<typeof AdHocFilterMenu> | null>(null)

// Sort specs: array of { property, direction } for multi-field sorting
interface SortSpec {
  property: string
  direction: 'asc' | 'desc'
}
const sortSpecs = ref<SortSpec[]>([])

// Computed for keyboard navigation
const itemCount = computed(() => entities.value.length)
const hasPrevPage = computed(() => page.value > 1)
const hasNextPage = computed(() => meta.value.has_more || page.value * meta.value.per_page < meta.value.total)

// Keyboard navigation
const { selectedIndex, clearSelection } = useListKeyboard({
  itemCount,
  hasPrevPage,
  hasNextPage,
  hasSelection,
  onOpen: (index) => {
    const entity = entities.value[index]
    if (entity) navigateToEntity(entity)
  },
  onEdit: (index) => {
    // The form opens on the row's ADDRESS, face included, so an edit from a
    // world-bound list edits the face the row showed and not its bare id.
    const entity = entities.value[index]
    if (entity && listConfig.value?.edit_form) {
      router.push(`/form/${listConfig.value.edit_form}/${entityRef(entity)}`)
    }
  },
  onCreate: () => {
    if (createFormTarget.value) {
      router.push(createFormTarget.value)
    }
  },
  onDelete: (index) => {
    const entity = entities.value[index]
    if (!entity) return
    if (!canDelete(entity)) {
      uiStore.warning('Delete not permitted for this entity')
      return
    }
    void requestDelete(entity)
  },
  onSelect: (index) => {
    const entity = entities.value[index]
    if (entity) toggleSelection(entity.id)
  },
  onClearSelection: () => {
    clearActionSelection()
  },
  onPrevPage: () => {
    if (hasPrevPage.value) {
      handlePageChange(page.value - 1)
    }
  },
  onNextPage: () => {
    if (hasNextPage.value) {
      handlePageChange(page.value + 1)
    }
  },
  onFocusSearch: () => {
    searchBoxRef.value?.focus()
  },
  onOpenFilter: () => {
    filterMenuRef.value?.open()
  },
})

// Computed
const listConfig = computed(() => schemaStore.getList(props.listId))
const entityType = computed(() => {
  if (!listConfig.value) return undefined
  return schemaStore.getEntityType(listConfig.value.entity)
})

// listExportUrlFor builds the export URL for the current list view + chosen
// transform, forwarding the active filter[...] and q params from the URL so the
// export matches what the user is looking at (the backend re-applies the same
// ACL + filter pipeline).
function listExportUrlFor(transform: string): string {
  const cfg = listConfig.value
  if (!cfg) return '#'
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(route.query)) {
    if (!key.startsWith('filter[') && key !== 'q') continue
    if (typeof value === 'string') params.set(key, value)
  }
  // Export is pinned to the view it was invoked from, so it carries the same
  // scope the rows on screen were selected by. Without this an export of a
  // scoped list silently widens to the type's default — a file that looks
  // complete and is not.
  if (cfg.query_scope) params.set('query_scope', cfg.query_scope)
  return listExportUrl(cfg.entity, props.listId, transform, params)
}

// Admin-authored info regions (markdown from data-entry.yaml, sanitized by
// renderMarkdown). `header` is canonical; `description` (previously unused) is a
// fallback for the top slot, used only when `header` is unset. No refResolver
// here — the /_config endpoint carries no mentions map, so bare-ID code spans
// stay inert; standard [text](/entity/ID) links work.
// Lists opt into the legacy `description` alias; kanban deliberately does not.
const headerHtml = computed(() =>
  renderMarkdown(viewHeaderMarkdown(listConfig.value, { allowDescriptionAlias: true }))
)
const footerHtml = computed(() => renderMarkdown(viewFooterMarkdown(listConfig.value)))

// Pre-configured filters from list config
const configuredFilters = computed(() => {
  return listConfig.value?.filters?.filter(f => f.operator && f.value) || []
})

// Check if any columns reference relations (need to include related entities)
const hasRelationColumns = computed(() => {
  return listConfig.value?.columns?.some(col => col.relation) || false
})

const hasActions = computed(() => resolvedActions.value.length > 0)

// Build query params. Reads `page` (input state), never `meta` (query
// output) — otherwise the query key would depend on its own result.
const queryParams = computed((): ListParams => {
  const params: ListParams = {
    page: page.value,
    per_page: listConfig.value?.page_size || 25,
    // Names the configured list so the server can apply its `condition:`.
    // Sent unconditionally rather than only when a condition exists: the SPA
    // would otherwise have to know which lists carry one, duplicating a
    // server-side fact that changes on config reload. An id for a list with
    // no condition simply resolves to no constraint.
    list_id: props.listId,
  }

  // Add pre-configured filters from list config
  for (const filter of configuredFilters.value) {
    const apiOp = toApiOperator(filter.operator)
    const key = `filter[${filter.property}][${apiOp}]`
    const filterValue = filter.value
    // Append to existing filter or create new
    const existing = (params as Record<string, string | number | undefined>)[key]
    if (existing) {
      (params as Record<string, string | number | undefined>)[key] = `${existing},${filterValue}`
    } else {
      (params as Record<string, string | number | undefined>)[key] = filterValue as string
    }
  }

  // Add user-selected filters via the shared serializer so EntityList and
  // useScopeNavigation stay in lockstep on the wire format.
  const userParams = filterStateToApiParams(filters.value)
  const paramsRecord = params as Record<string, string | number | undefined>
  for (const [key, value] of Object.entries(userParams)) {
    paramsRecord[key] = value
  }

  // Free-text search: backend intersects ?q= results with the typed list.
  //
  // Sent under a world too. This was suppressed while the index held only
  // DEFAULT-face documents, because a hit under `?world=published` would then
  // have been a draft leaking onto a published surface. The index is keyed per
  // face now and a world IS the search scope (TKT-9KZGJO): the backend matches
  // each entity's prime in the requested world, so the hits are that world's
  // and an entity with no face there cannot appear.
  //
  // `world` is already on `params` above, so nothing extra is needed here —
  // but do not reintroduce a client-side suppression: the scoping is the
  // server's to enforce, and a UI that quietly drops `q` would look like a
  // search returning nothing rather than one that was never run.
  if (searchQuery.value) {
    paramsRecord.q = searchQuery.value
  }

  // Add sorting - supports multi-field sorting
  if (sortSpecs.value.length > 0) {
    params.sort = sortSpecs.value
      .map((s) => (s.direction === 'desc' ? `-${s.property}` : s.property))
      .join(',')
  } else if (listConfig.value?.default_sort?.length) {
    const defaultSort = listConfig.value.default_sort
      .map((s) => (s.direction === 'desc' ? `-${s.property}` : s.property))
      .join(',')
    params.sort = defaultSort
  }

  // Include related entities for relation columns — under a world too.
  //
  // This used to be suppressed under a world, because neighbor resolution went
  // through the ungated, default-world reader and a published row would have
  // arrived wrapped in DRAFT neighbors. Two facts in that reasoning are now
  // false: TKT-WRLDAPI item 4 made neighbor resolution WORLD-SCOPED (an
  // included peer is that world's face; a neighbor with no face in the world
  // is absent), and the backend no longer "omits relations entirely on
  // world-bound reads" — it resolves them per neighbour.
  //
  // So suppressing it now costs relation columns their content for no reason.
  // See the same orphan in stores/entities.ts for the general shape.
  if (hasRelationColumns.value) {
    params.include = '*'
  }

  if (worldParam.value) {
    params.world = worldParam.value
  }

  // The list's configured scope. Sent because the endpoint is keyed by entity
  // TYPE, so the server cannot tell which list is on screen — without this the
  // view falls back to the type's `default` and a list declaring
  // `query_scope: archief` renders unscoped.
  if (listConfig.value?.query_scope) {
    params.query_scope = listConfig.value.query_scope
  }

  return params
})

// The list query (FEAT-XY2D1L). Declared here — after queryParams /
// listConfig — because Pinia Colada evaluates `enabled` synchronously
// during setup; the derived computeds up in the State block read it
// through `listQueryRef`. Keyed on the canonicalized params, so
// page/filter/sort/search changes swap cache entries automatically — the
// reactive key coalesces synchronous trigger bursts and drops stale
// responses, replacing the old generation counter + scheduleFetch
// microtask. useEvents' SSE invalidation on ['entities', <type>] marks it
// stale and background-refetches while mounted, so lists go live (they
// never reacted to SSE before). placeholderData keeps the previous rows
// visible during a param change instead of flashing the spinner.
const listQuery = useQuery({
  key: () => entityKeys.listParams(listConfig.value?.entity ?? '', queryParams.value),
  query: () => {
    const config = listConfig.value
    if (!config) throw new Error(`unknown list: ${props.listId}`)
    return listEntities(config.entity, queryParams.value)
  },
  enabled: () => !!listConfig.value,
  placeholderData: (prev) => prev,
})
listQueryRef.value = listQuery

function handleSort(field: string, event: MouseEvent) {
  const existingIndex = sortSpecs.value.findIndex((s) => s.property === field)

  if (event.shiftKey) {
    // Multi-sort: add/toggle field while keeping others
    if (existingIndex >= 0) {
      // Toggle direction if already in list
      const spec = sortSpecs.value[existingIndex]
      if (spec.direction === 'asc') {
        spec.direction = 'desc'
      } else {
        // Remove from sort if clicking desc again
        sortSpecs.value.splice(existingIndex, 1)
      }
    } else {
      // Add new sort field
      sortSpecs.value.push({ property: field, direction: 'asc' })
    }
  } else {
    // Single-sort: replace all with just this field
    if (existingIndex >= 0 && sortSpecs.value.length === 1) {
      // Toggle direction if already the only sort
      sortSpecs.value[0].direction = sortSpecs.value[0].direction === 'asc' ? 'desc' : 'asc'
    } else {
      // Replace all sorts with just this field
      sortSpecs.value = [{ property: field, direction: 'asc' }]
    }
  }

  // Sort changed → reset to page 1; the query reacts to both inputs.
  page.value = 1
}

// Helper to get sort index and direction for a field
function getSortInfo(field: string): { index: number; direction: 'asc' | 'desc' | null } {
  const idx = sortSpecs.value.findIndex((s) => s.property === field)
  if (idx < 0) return { index: -1, direction: null }
  return { index: idx, direction: sortSpecs.value[idx].direction }
}

function handleFilter(newFilters: FilterState) {
  // The filters watcher reacts to this and triggers loadEntities.
  writeToQuery(newFilters)
}

function handleSearchUpdate(value: string) {
  // Watcher on searchQuery resets page and re-fetches.
  writeToQuery(filters.value, value)
}

// Properties hidden from the AdHocFilterMenu — already covered by the
// FilterBar's static widgets (filter_controls), already pinned by the list's
// `filters:` config, or already active as an ad-hoc chip. Including all three
// in one set so the menu never offers a duplicate.
const lockedAdHocProperties = computed(() => {
  const set = new Set<string>(staticFilterProperties())
  for (const fc of listConfig.value?.filter_controls || []) {
    if (fc.property) set.add(fc.property)
    if (fc.relation) set.add(fc.relation)
  }
  for (const prop of Object.keys(filters.value)) set.add(prop)
  return set
})

function handleAdHocApply(property: string, value: string) {
  writeToQuery({ ...filters.value, [property]: { value } })
}

function removeAdHocFilter(property: string) {
  const next = { ...filters.value }
  delete next[property]
  writeToQuery(next)
}

// Filters added via the ad-hoc menu are rendered as chips. We treat any
// active filter that isn't covered by FilterBar (filter_controls) and isn't
// a static-pinned config filter as ad-hoc.
const adHocFilterChips = computed(() => {
  const filterControlKeys = new Set<string>()
  for (const fc of listConfig.value?.filter_controls || []) {
    if (fc.property) filterControlKeys.add(fc.property)
    if (fc.relation) filterControlKeys.add(fc.relation)
  }
  const pinned = staticFilterProperties()
  return Object.entries(filters.value)
    .filter(([prop]) => !filterControlKeys.has(prop) && !pinned.has(prop))
    .map(([property, fv]) => ({ property, value: fv.value }))
})

function handlePageChange(newPage: number) {
  page.value = newPage
}

// Resolve a link configuration value to a path (mirrors backend resolveLinkTarget)
function resolveLinkTarget(link: string, entityType: string, entityId: string): string {
  if (!link) return ''
  if (link === 'detail') return `/entity/${entityType}/${entityId}`
  if (link.startsWith('document/')) {
    const docName = link.slice('document/'.length)
    return `/document/${docName}/${entityId}`
  }
  return ''
}

// The column whose cell carries the row's link, if the list config declares
// one. Hoisted out of entityTarget so the template can share the same
// answer: the badge below rides the row's TITLE, and "the title" and "the
// thing you click" must be the same cell or the badge would sit beside an
// unrelated value.
const linkColumn = computed(() => listConfig.value?.columns?.find((col) => col.link))

// The column the per-row WorldBadge attaches to.
//
// A world badge is a statement about the ROW, not about any one property, so
// it must appear exactly ONCE per row — one badge per cell would repeat the
// same fact across every column. It rides the title because that is what a
// reader scans and what they click through to.
//
// `link` names the title column when the list config declares one; otherwise
// the first column is the title by the same convention the mobile card layout
// already uses (`listConfig.columns[0]` is its card title).
const badgeColumn = computed(() => linkColumn.value ?? listConfig.value?.columns?.[0])

// Whether this cell is the one that carries the row's badge. Compared by
// object identity: listConfig.columns is a stable array, so the column objects
// are the same references the template iterates.
function isBadgeColumn(column: object): boolean {
  return badgeColumn.value === column
}

// entityTarget is the SINGLE source of truth for a row's destination — used both
// by the row's plain-click push AND by the title cell's RouterLink `to`.
// Building the href separately would silently drop the query below: the tab
// would open on an unscoped detail page where prev/next navigation is dead and
// the back target is wrong, while still rendering a valid-looking page.
// Nil: returns undefined when no safe internal path resolves (empty entity
// type, or a cellLink that is not a same-origin path); the row then renders no
// link and the click is inert.
function entityTarget(entity: Entity): RouteLocationRaw | undefined {
  // Build query params to preserve navigation context.
  // Filters are already in `route.query` via useUrlFilterSync — we just
  // forward all `filter[*]` entries unchanged so the bracket format is the
  // single source of truth (no legacy `filter_*` underscore form).
  const query: Record<string, string | string[]> = {
    from: props.listId,
    scope: `list:${props.listId}`,
  }

  // Include sort if active
  if (sortSpecs.value.length > 0) {
    query.sort = sortSpecs.value
      .map((s) => (s.direction === 'desc' ? `-${s.property}` : s.property))
      .join(',')
  } else if (listConfig.value?.default_sort?.length) {
    query.sort = listConfig.value.default_sort
      .map((s) => (s.direction === 'desc' ? `-${s.property}` : s.property))
      .join(',')
  }

  // Forward bracket-format filter params from the current URL. Narrow the
  // LocationQueryValue type explicitly (it's string | null | (string|null)[]).
  for (const [key, value] of Object.entries(route.query)) {
    if (!key.startsWith('filter[')) continue
    if (value === null) continue
    if (Array.isArray(value)) {
      const filtered = value.filter((v): v is string => v !== null)
      if (filtered.length > 0) query[key] = filtered
    } else {
      query[key] = value
    }
  }

  // Forward search query so the back-button to a searched list keeps the
  // search state. Scope navigation (prev/next within a search result set)
  // is a known v1 limitation — useScopeNavigation does not yet honor q.
  if (searchQuery.value) {
    query.q = searchQuery.value
  }

  // The world rides along too (TKT-6NCSSC). A row followed from a world-bound
  // list must resolve in that world; without this the reader lands on the
  // DEFAULT face of an entity whose row they just saw carrying a fallback
  // badge. Same rule the detail page's neighbour links and historyTarget follow.
  if (worldParam.value) {
    query.world = worldParam.value
  }

  // Check for column-level link first (use first column with link)
  const columnLink = linkColumn.value?.link
    ? resolveLinkTarget(linkColumn.value.link, entity.type, entity.id)
    : ''

  // entityDetailHref returns columnLink when set, otherwise the
  // entity-route path.
  const path = entityDetailHref(
    { id: entity.id, type: entity.type },
    { cellLink: columnLink },
  )
  if (!path || !safeInternalHref(path)) return undefined
  return { path, query }
}

// navigateToEntity handles a plain left-click on the row. The row is a <tr>, so
// it cannot be an anchor; the link affordance comes from the stretched
// .row-link in the first cell, which is what cmd/middle/right-click act on.
function navigateToEntity(entity: Entity) {
  const target = rowTargets.value.get(entity.id) ?? entityTarget(entity)
  if (!target) return
  router.push(target)
}

// onRowClick backs the row's plain-click navigation. It defers to the browser
// on a modifier or non-primary click so the stretched .row-link's own default
// action runs (opening a tab/window) instead of routing in place.
function onRowClick(entity: Entity, event: MouseEvent) {
  if (shouldDeferToBrowser(event)) return
  navigateToEntity(entity)
}

// Row targets, computed ONCE per entity rather than per template reference
// (RR-SYFX1B -- the same reason columnWidgets below is per-column, not
// per-cell). entityTarget walks route.query, maps sortSpecs and scans columns,
// and the template reads it twice per row (the v-else-if and the :to), so at 25
// rows that was 50 traversals per render, re-running on every reactive tick
// including hover-driven selectedIndex changes.
const rowTargets = computed(() => {
  const byId = new Map<string, RouteLocationRaw | undefined>()
  for (const entity of entities.value) {
    byId.set(entity.id, entityTarget(entity))
  }
  return byId
})

// Widget resolution for property cells, keyed by column property name and
// computed ONCE per column rather than per cell (RR-UD2A -- the same reason
// PropertyDisplay precomputes `rows`). resolve()/resolveFromHint() walk a Map
// and can console.warn; doing that per cell would be one lookup and one
// potential warning per row per render.
//
// Relation columns are absent from this map on purpose: they have no
// PropertyDef and there is no relation widget, so they stay on the string
// path in getFormattedCellValue.
const columnWidgets = computed(() => {
  const byProperty = new Map<string, { component: Component; hint: DenseRoutingHint }>()
  const type = entityType.value
  if (!type) return byProperty
  for (const column of listConfig.value?.columns ?? []) {
    if (!column.property || byProperty.has(column.property)) continue
    const hint = densePropertyRoutingHint(type.properties[column.property], column.property)
    byProperty.set(column.property, { component: defaultRegistry.resolveFromHint(hint), hint })
  }
  return byProperty
})

// The widget for a cell, or undefined when the cell should NOT render one:
// a relation column (no PropertyDef, no relation widget) or an empty value
// (widgets may render a "no value" placeholder that cells must not show --
// see isDenseEmpty). Both fall through to the plain string span.
function cellWidget(
  entity: Entity,
  column: { property?: string; relation?: string; direction?: 'outgoing' | 'incoming' }
) {
  if (!column.property) return undefined
  const entry = columnWidgets.value.get(column.property)
  if (!entry) return undefined
  return isDenseEmpty(getCellValue(entity, column)) ? undefined : entry
}

interface ResolvedCell {
  component: Component
  propertyName: string
  modelValue: unknown
}

// One resolved cell: the widget to render plus the value already shaped the
// way that widget wants. Returns undefined when the cell must fall back to the
// plain string span (relation column, or an empty value -- see cellWidget).
//
// Memoized per (entity, column). The template reads it four times per cell
// (v-else-if, :is, and two bindings); without the cache each read redoes the
// Map lookup and re-formats the value.
//
// Safe against staleness because entity objects are copy-on-write: both
// optimistic paths in queries/optimisticList.ts rebuild the changed entity
// via `data.map(e => e.id === id ? update(e) : e)`, and a refetch parses
// fresh objects. A mutated cell therefore always arrives as a NEW identity
// and misses the cache. If an entity ever starts being mutated in place,
// this cache goes stale silently -- keyed on identity, it cannot detect it.
//
// WeakMap so a dropped row's entry is collected with the row. The inner key
// is the column object, stable across renders because listConfig.columns is
// the same array.
const cellCache = new WeakMap<Entity, Map<object, ResolvedCell | undefined>>()

function resolveCell(
  entity: Entity,
  column: { property?: string; relation?: string; direction?: 'outgoing' | 'incoming' }
): ResolvedCell | undefined {
  let perEntity = cellCache.get(entity)
  if (!perEntity) {
    perEntity = new Map()
    cellCache.set(entity, perEntity)
  }
  if (perEntity.has(column)) return perEntity.get(column)

  const entry = cellWidget(entity, column)
  const resolved: ResolvedCell | undefined = entry
    ? {
        component: entry.component,
        propertyName: entry.hint.propertyName,
        // Passthrough widgets render String(value); everything else owns its
        // display formatting and wants the stored value.
        modelValue: entry.hint.preformatted
          ? getFormattedCellValue(entity, column)
          : getCellValue(entity, column),
      }
    : undefined
  perEntity.set(column, resolved)
  return resolved
}

// isCellInaccessible reports whether the cell's underlying property is
// listed in the entity's inaccessible array (e.g. git-crypt encrypted).
// Such cells render a lock indicator instead of the value.
function isCellInaccessible(entity: Entity, column: { property?: string }): boolean {
  if (!entity.inaccessible || entity.inaccessible.length === 0) return false
  if (!column.property) return false
  return entity.inaccessible.some((f) => f.name === column.property)
}

// relationCellKey returns the wire key in entity.relations that holds a
// relation column's target IDs. Outgoing columns read the relation type
// directly; incoming columns read the relation's inverse key — the declared
// inverse name if the metamodel has one, else the `<relation>_inverse`
// fallback. This mirrors the backend's inverseRelationKey (see MECHANISM.md);
// the two must stay in lockstep.
function relationCellKey(relation: string, direction?: 'outgoing' | 'incoming'): string {
  if (direction === 'incoming') {
    return schemaStore.getInverseName(relation) ?? `${relation}_inverse`
  }
  return relation
}

// Mobile cards: drop columns whose cell is empty for this entity — a
// dangling label with a blank value wastes card space. ACL-locked cells
// stay visible: the 🔒 marker is information, not emptiness.
function visibleMobileColumns(entity: Entity) {
  return (listConfig.value?.columns.slice(1) ?? []).filter(
    (column) => isCellInaccessible(entity, column) || getFormattedCellValue(entity, column) !== ''
  )
}

function getFormattedCellValue(
  entity: Entity,
  column: { property?: string; relation?: string; direction?: 'outgoing' | 'incoming' },
): string {
  // For relation columns, resolve IDs to titles using included entities.
  // Outgoing edges are serialized under the relation type; incoming edges
  // under the relation's INVERSE key (matching the backend serializer, see
  // MECHANISM.md). The included map carries both target and source entities
  // because the list is fetched with ?include=*.
  if (column.relation) {
    const key = relationCellKey(column.relation, column.direction)
    const relationIds = entity.relations?.[key] || []
    const titles = relationIds.map((id) => {
      const included = includedEntities.value[id]
      return included ? entityDisplayTitle(included) : id
    })
    return titles.join(', ')
  }

  const value = getCellValue(entity, column)
  // Pass the user's effective display zone so datetime cells honor the
  // Settings display-timezone preference, matching the form widget (RR-K3WEW2).
  return formatCellValue(value, column.property, entityType.value, uiStore.effectiveTimezone)
}

function handleDelete(entity: Entity, event: Event) {
  event.stopPropagation()
  void requestDelete(entity)
}

// Delete as an optimistic mutation: the row vanishes immediately, rolls
// back on failure, and the list prefix is invalidated on settle so every
// param variant (other pages/filters where the entity might appear)
// refetches and reconciles with the server.
const { mutate: deleteEntityMutation } = useMutation({
  // Addressed to the ROW: on a bare face this deletes the entity, on a
  // non-bare face it removes that face only (the server's rule for `ID@face`).
  mutation: ({ entity }: { entity: Entity }) => deleteEntity(entity.type, entityRef(entity)),
  onMutate({ entity }: { entity: Entity }) {
    return beginOptimisticRemove(
      queryCache,
      entityKeys.listParams(entity.type, queryParams.value),
      entity.id
    )
  },
  onError(err, _vars, context) {
    rollbackOptimistic(queryCache, context)
    uiStore.error(getErrorMessage(err, 'Failed to delete entity'))
  },
  onSuccess(_data, { entity }) {
    uiStore.success(`Deleted ${entity.id}`)
  },
  async onSettled(_data, _err, { entity }) {
    // Invalidate the whole list (all param variants), not just the
    // visible page — the deleted entity may appear under other filters.
    await queryCache.invalidateQueries({ key: entityKeys.list(entity.type) })
  },
})

async function requestDelete(entity: Entity) {
  // Addressed to the row. On a bare face that is the entity; on a non-bare
  // face it is that face only (the server's rule for `ID@face`), and the
  // confirm says so — "delete" must not read as "unpublish" or the reverse.
  const face = refFace(entityRef(entity))
  const faceLabel = face ? schemaStore.faceLabel(entity.type, face) || face : ''
  const ok = await confirm({
    title: face ? 'Delete Face?' : 'Delete Entity?',
    message: face
      ? `Are you sure you want to delete the ${faceLabel} face of '${entity.id}'? ` +
        'Its other faces are kept. This action cannot be undone.'
      : `Are you sure you want to delete '${entity.id}'? This action cannot be undone.`,
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok) return
  deleteEntityMutation({ entity })
}

// Reconcile input `page` with the server's returned page. If the backend
// clamps an out-of-range page (e.g. requested 5, only 3 exist), adopt the
// server's value so the query key, keyboard nav, and Pagination controls
// all agree on one page-of-truth.
watch(
  () => listQuery.data.value?.meta.page,
  (serverPage) => {
    if (serverPage !== undefined && serverPage !== page.value) {
      page.value = serverPage
    }
  }
)

// Reset paging/sort/selection on list switch. The query reacts to the
// input changes — no manual fetch trigger needed.
watch(() => props.listId, () => {
  sortSpecs.value = []
  page.value = 1
  clearSelection()
  clearActionSelection()
})

// Clear selection when the visible entities change (incl. background
// SSE-driven refetches).
watch(entities, () => {
  clearSelection()
  clearActionSelection()
})

// Reset to page 1 when filters or free-text search change (covers user
// edits via writeToQuery and back/forward nav the URL sync picks up). The
// query re-keys off page + filters + search together, so a simultaneous
// filter+search edit still produces one fetch.
watch(filters, () => {
  page.value = 1
}, { deep: true })

watch(searchQuery, () => {
  page.value = 1
})
</script>

<template>
  <div v-if="listConfig" class="entity-list" :data-testid="`page-state-${pageState}`">
    <header class="list-header mobile-topbar mobile-topbar--with-menu">
      <div class="header-left">
        <BackButton v-if="backTarget" :target="backTarget" />
        <h1 id="entity-list-heading">{{ listConfig.title || listConfig.entity }}</h1>
      </div>
      <div class="header-actions">
        <ExportMenu :url-for="listExportUrlFor" />
        <RouterLink
          v-if="createFormTarget && canCreate()"
          :to="createFormTarget"
          class="btn btn-primary"
        >
          + New <kbd>N</kbd>
        </RouterLink>
      </div>
    </header>

    <!-- eslint-disable-next-line vue/no-v-html -- sanitized by renderMarkdown -->
    <div v-if="headerHtml" class="view-info view-info--top" v-html="headerHtml"/>

    <div v-if="configuredFilters.length" class="configured-filters">
      <span
        v-for="filter in configuredFilters"
        :key="`${filter.property}-${filter.operator}-${filter.value}`"
        class="filter-chip"
      >
        {{ filter.property }} {{ filter.operator }} {{ filter.value }}
      </span>
    </div>

    <!--
      Gated on `!loadError`: the banner ASSERTS "you are looking at world X",
      and it must not make that claim over an error state. An unknown world is
      a 400 (unknown_world), so a banner rendered beside the error read
      "Showing the nonexistent world" above a message saying no such world is
      declared — the page contradicting itself, and in the direction that
      reads as a designed property rather than a mistake.
    -->
    <!--
      Both halves are operator config: the ANNOUNCEMENT (`banner:` on the
      world) and the NOTE (`messages.projection`, rendered only on a list of a
      faced type). Neither declared: no banner at all.
    -->
    <WorldBanner v-if="isWorldBound && !loadError && (worldBanner || projectionNote)" :label="worldBanner">
      {{ projectionNote }}
    </WorldBanner>

    <div class="search-row">
      <!--
        Rendered under a world too. It was OMITTED while search could not be
        world-scoped, because a box that returned default-world hits on a
        published page would have surfaced drafts. Search is scoped to the
        world now, so the affordance is real: it searches the same faces this
        list shows, and an entity absent from the world is absent from its
        search. The banner above says so.
      -->
      <SearchBox
        ref="searchBoxRef"
        :model-value="searchQuery"
        :placeholder="`Search ${listConfig.entity}s...`"
        @update:model-value="handleSearchUpdate"
      />
      <AdHocFilterMenu
        ref="filterMenuRef"
        mode="list"
        :entity-type="entityType"
        :locked-properties="lockedAdHocProperties"
        @apply="handleAdHocApply"
      />
    </div>

    <div v-if="adHocFilterChips.length" class="adhoc-filter-chips">
      <span
        v-for="chip in adHocFilterChips"
        :key="chip.property"
        class="filter-chip removable"
      >
        {{ chip.property }}: {{ chip.value }}
        <button
          type="button"
          class="chip-remove"
          :title="`Remove ${chip.property} filter`"
          @click="removeAdHocFilter(chip.property)"
        >
          &times;
        </button>
      </span>
    </div>

    <div class="list-content">
      <FilterBar
        v-if="listConfig.filter_controls?.length"
        :config="listConfig"
        :entity-type="entityType"
        :filters="filters"
        @filter="handleFilter"
      />
      <div v-if="loading" class="loading-state">
        <div class="spinner"/>
        <span>Loading...</span>
      </div>

      <div v-else-if="loadError" class="empty-state load-error">
        <p>{{ loadError }}</p>
        <button type="button" class="btn btn-secondary" @click="listQuery.refetch()">
          Retry
        </button>
      </div>

      <div v-else-if="entities.length === 0" class="empty-state">
        <p v-if="searchQuery">No matches for &ldquo;{{ searchQuery }}&rdquo;.</p>
        <p v-else>No {{ listConfig.entity }}s found.</p>
        <button
          v-if="searchQuery"
          type="button"
          class="btn btn-secondary"
          @click="handleSearchUpdate('')"
        >
          Clear search
        </button>
        <RouterLink
          v-else-if="createFormTarget && canCreate()"
          :to="createFormTarget"
          class="btn btn-secondary"
        >
          Create one
        </RouterLink>
      </div>

      <template v-else>
      <!-- Mobile card layout -->
      <div v-if="isMobile" class="mobile-card-list">
        <div
          v-for="(entity, index) in entities"
          :key="'card-' + entity.id"
          class="mobile-card"
          :class="{ selected: index === selectedIndex, 'action-selected': isSelected(entity.id) }"
          @click="onRowClick(entity, $event)"
        >
          <div class="mobile-card-header">
            <!-- The card holds a delete <button>, and an <a> may not contain
                 interactive content — so the link wraps the title, not the
                 card. Stretched over the card by .mobile-card-title::after. -->
            <RouterLink
              v-if="rowTargets.get(entity.id)"
              class="mobile-card-title text-wrap-anywhere text-clamp-2"
              :to="rowTargets.get(entity.id)!"
            >
              {{ getFormattedCellValue(entity, listConfig.columns[0]) }}
            </RouterLink>
            <span v-else class="mobile-card-title text-wrap-anywhere text-clamp-2">
              {{ getFormattedCellValue(entity, listConfig.columns[0]) }}
              <!-- Same per-row provenance as the table below, on the card's
                   title. A narrow screen is not a reason to drop the one
                   signal separating a real face from a stand-in. -->
              <WorldBadge :world="entity._world" :entity-type="entity.type" />
            </span>
            <button
              v-if="canDelete(entity)"
              class="delete-btn"
              title="Delete"
              @click="handleDelete(entity, $event)"
            >
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <polyline points="3 6 5 6 21 6"/>
                <path d="M19 6v14a2 2 0 01-2 2H7a2 2 0 01-2-2V6m3 0V4a2 2 0 012-2h4a2 2 0 012 2v2"/>
              </svg>
            </button>
          </div>
          <div v-if="visibleMobileColumns(entity).length" class="mobile-card-fields">
            <div
              v-for="column in visibleMobileColumns(entity)"
              :key="column.property || column.relation"
              class="mobile-card-field"
            >
              <span class="mobile-card-label">{{ column.label || column.property || column.relation }}</span>
              <span
                v-if="isCellInaccessible(entity, column)"
                class="inaccessible-cell"
                title="inaccessible"
              >🔒</span>
              <component
                :is="resolveCell(entity, column)!.component"
                v-else-if="resolveCell(entity, column)"
                class="mobile-card-value"
                :model-value="resolveCell(entity, column)!.modelValue"
                :mode="'display'"
                :property-name="resolveCell(entity, column)!.propertyName"
                :entity-type="listConfig.entity"
              />
              <span v-else class="mobile-card-value">{{ getFormattedCellValue(entity, column) }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Desktop table layout -->
      <div v-else class="table-scroll-wrapper">
      <table class="entity-table" aria-labelledby="entity-list-heading">
        <thead>
          <tr v-if="hasSelection" class="action-header-row">
            <th class="select-column">
              <input
                type="checkbox"
                :checked="selectedIds.size === entities.length"
                :indeterminate="selectedIds.size > 0 && selectedIds.size < entities.length"
                @change="selectedIds.size === entities.length ? clearActionSelection() : selectAll(entities.map(e => e.id))"
              />
            </th>
            <th :colspan="listConfig.columns.length + 1" class="action-header-cell">
              <span class="action-header-count">{{ selectedIds.size }} selected</span>
              <button
                v-for="{ id, config } in resolvedActions"
                v-show="anySelectedAllowsUpdate()"
                :key="id"
                class="action-header-btn"
                :disabled="actionProcessing"
                @click="(e) => triggerAction(id, config, e)"
              >
                <kbd>{{ config.key }}</kbd>
                {{ config.label }}
              </button>
            </th>
          </tr>
          <tr v-else>
            <th v-if="hasActions" scope="col" class="select-column">
              <input
                type="checkbox"
                :checked="false"
                @change="selectAll(entities.map(e => e.id))"
              />
            </th>
            <th
              v-for="column in listConfig.columns"
              :key="column.property || column.relation"
              scope="col"
              :class="{
                sortable: column.sortable !== false && column.property,
                sorted: getSortInfo(column.property || '').index >= 0,
                'sorted-desc': getSortInfo(column.property || '').direction === 'desc',
              }"
              @click="column.sortable !== false && column.property && handleSort(column.property, $event)"
            >
              {{ column.label || column.property || column.relation }}
              <span v-if="getSortInfo(column.property || '').index >= 0" class="sort-indicator">
                <span v-if="sortSpecs.length > 1" class="sort-order">{{ getSortInfo(column.property || '').index + 1 }}</span>
                {{ getSortInfo(column.property || '').direction === 'desc' ? '▼' : '▲' }}
              </span>
            </th>
            <th scope="col" class="actions-column"/>
          </tr>
        </thead>
        <TransitionGroup tag="tbody" name="row">
          <tr
            v-for="(entity, index) in entities"
            :key="entity.id"
            class="entity-row"
            :data-entity-id="entity.id"
            :class="{ selected: index === selectedIndex, 'action-selected': isSelected(entity.id) }"
            @click="onRowClick(entity, $event)"
          >
            <td v-if="hasActions" class="select-cell" @click.stop>
              <input
                type="checkbox"
                :checked="isSelected(entity.id)"
                @change="toggleSelection(entity.id)"
              />
            </td>
            <td
              v-for="(column, colIndex) in listConfig.columns"
              :key="column.property || column.relation"
            >
              <span
                v-if="isCellInaccessible(entity, column)"
                class="inaccessible-cell"
                title="inaccessible"
              >🔒</span>
              <!-- The first column's content is wrapped in the row's real link,
                   stretched over the whole row by .row-link::after. A <tr>
                   cannot be an anchor, so this is what gives the row
                   cmd/middle/right-click and a hover URL preview. It wraps real
                   text (not an empty box) so it has an accessible name. -->
              <RouterLink
                v-else-if="colIndex === 0 && rowTargets.get(entity.id)"
                class="row-link"
                :to="rowTargets.get(entity.id)!"
              >
                <component
                  :is="resolveCell(entity, column)!.component"
                  v-if="resolveCell(entity, column)"
                  :model-value="resolveCell(entity, column)!.modelValue"
                  :mode="'display'"
                  :property-name="resolveCell(entity, column)!.propertyName"
                  :entity-type="listConfig.entity"
                />
                <template v-else>{{ getFormattedCellValue(entity, column) }}</template>
              </RouterLink>
              <component
                :is="resolveCell(entity, column)!.component"
                v-else-if="resolveCell(entity, column)"
                :model-value="resolveCell(entity, column)!.modelValue"
                :mode="'display'"
                :property-name="resolveCell(entity, column)!.propertyName"
                :entity-type="listConfig.entity"
              />
              <span v-else>
                {{ getFormattedCellValue(entity, column) }}
              </span>
              <!--
                Per-ROW face provenance, on the title cell only (see
                `badgeColumn`). Each row resolved through the world
                independently, so one list can mix a first-choice hit and a
                stand-in — and the two are otherwise byte-identical, which is
                the whole reason the badge exists.

                Only the STAND-IN rows render anything: WorldBadge is a no-op
                for a first-choice hit and under the default world (where
                `_world` is absent entirely), so a typical list shows no badges
                at all and the ones it does show are the exceptions.
              -->
              <WorldBadge v-if="isBadgeColumn(column)" :world="entity._world" :entity-type="entity.type" />
            </td>
            <td class="actions-cell">
              <button
                v-if="canDelete(entity)"
                class="delete-btn"
                title="Delete"
                @click="handleDelete(entity, $event)"
              >
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                  <polyline points="3 6 5 6 21 6"/>
                  <path d="M19 6v14a2 2 0 01-2 2H7a2 2 0 01-2-2V6m3 0V4a2 2 0 012-2h4a2 2 0 012 2v2"/>
                </svg>
              </button>
            </td>
          </tr>
        </TransitionGroup>
      </table>
      </div>
      </template>

      <Pagination
        v-if="meta.total > meta.per_page"
        :meta="meta"
        @page-change="handlePageChange"
      />
    </div>

    <!-- eslint-disable-next-line vue/no-v-html -- sanitized by renderMarkdown -->
    <div v-if="footerHtml" class="view-info view-info--bottom" v-html="footerHtml"/>
  </div>

  <div v-else class="error-state">
    <h2>List not found</h2>
    <p>The list "{{ listId }}" does not exist in the configuration.</p>
  </div>
</template>

<style scoped>
.inaccessible-cell {
  color: var(--color-text-muted, #888);
  font-style: italic;
  cursor: help;
}

/* Info-region styles (.view-info) live in styles/view-info.css — shared with
   KanbanView so both views render admin-authored markdown identically. */

.entity-list {
  max-width: 1200px;
}

.list-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 24px;
}

.list-header h1 {
  margin: 0;
}

.header-left {
  display: flex;
  align-items: center;
  gap: var(--space-md);
}

.header-actions {
  display: flex;
  align-items: center;
  gap: var(--space-sm);
}

.btn {
  display: inline-flex;
  align-items: center;
  gap: var(--space-xs);
  padding: 8px 16px;
  border-radius: var(--radius-md);
  font-size: var(--font-size-base);
  font-weight: 500;
  text-decoration: none;
  cursor: pointer;
  border: none;
  transition: all 0.15s;
}

.btn-primary {
  background: var(--accent-color, #6366f1);
  color: white;
}

.btn-primary:hover {
  filter: brightness(0.9);
}

.btn-secondary {
  background: var(--border-color);
  color: var(--text-color);
}

.btn-secondary:hover {
  background: var(--hover-bg);
}

.configured-filters {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-sm);
  margin-top: 12px;
  margin-bottom: 12px;
}


.filter-chip {
  display: inline-flex;
  align-items: center;
  padding: 4px 10px;
  background: var(--hover-bg);
  border: 1px solid var(--border-color);
  border-radius: 16px;
  font-size: var(--font-size-sm);
  color: var(--text-color);
}

.filter-chip.removable {
  gap: var(--space-xs);
  padding-right: 4px;
  background: color-mix(in srgb, var(--accent-color) 15%, transparent);
  border-color: color-mix(in srgb, var(--accent-color) 30%, transparent);
  color: var(--accent-color);
}

.chip-remove {
  background: none;
  border: none;
  cursor: pointer;
  font-size: var(--font-size-base);
  line-height: 1;
  padding: 0 4px;
  color: inherit;
  opacity: 0.7;
}

.chip-remove:hover {
  opacity: 1;
}

.search-row {
  display: flex;
  align-items: stretch;
  gap: var(--space-sm);
  margin-bottom: 12px;
}

.adhoc-filter-chips {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-sm);
  margin-bottom: 12px;
}

.list-content {
  background: var(--card-bg);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-sm);
  overflow: hidden;
}

.loading-state,
.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 48px;
  gap: var(--space-lg);
  color: var(--muted-text);
}

.spinner {
  width: 32px;
  height: 32px;
  border: 3px solid var(--border-color);
  border-top-color: var(--accent-color);
  border-radius: var(--radius-circle);
  animation: spin 1s linear infinite;
}

.entity-table {
  width: 100%;
  border-collapse: collapse;
}

.entity-table thead {
  position: sticky;
  top: 0;
  z-index: 5;
}

.entity-table th {
  text-align: left;
  padding: 12px 16px;
  background: var(--hover-bg);
  border-bottom: 1px solid var(--border-color);
  font-size: var(--font-size-sm);
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  color: var(--muted-text);
}

.action-header-row th {
  background: var(--hover-bg);
}

.action-header-cell {
  text-transform: none;
  letter-spacing: normal;
}

.action-header-count {
  font-weight: 600;
  font-size: var(--font-size-dense);
  color: var(--text-color);
  margin-right: 0.75rem;
}

.action-header-btn {
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
  margin-right: 0.5rem;
  vertical-align: middle;
  padding: 0.2rem 0.6rem;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-sm);
  background: var(--card-bg);
  color: var(--text-color);
  font-size: var(--font-size-sm);
  cursor: pointer;
  transition: background 0.15s;
}

.action-header-btn:hover:not(:disabled) {
  background: var(--hover-bg);
}

.action-header-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.action-header-btn kbd {
  display: inline-block;
  padding: 0.05rem 0.3rem;
  border: 1px solid var(--border-color);
  border-radius: 3px;
  background: var(--hover-bg);
  font-family: monospace;
  font-size: var(--font-size-xs);
  line-height: 1;
}

.entity-table th.sortable {
  cursor: pointer;
  user-select: none;
}

.entity-table th.sortable:hover {
  filter: brightness(0.95);
}

.entity-table th.sorted {
  color: var(--accent-color);
}

.sort-indicator {
  margin-left: 4px;
  font-size: 10px;
}

.sort-order {
  font-size: 9px;
  background: var(--accent-color);
  color: white;
  padding: 1px 4px;
  border-radius: var(--radius-lg);
  margin-right: 2px;
}

.entity-table td {
  padding: 12px 16px;
  border-bottom: 1px solid var(--border-color);
  font-size: var(--font-size-base);
}

.entity-row {
  cursor: pointer;
  transition: background 0.15s;
  /* Containing block for the stretched .row-link below. */
  position: relative;
}

/* Stretched link (the Bootstrap `.stretched-link` pattern). A <tr> may not be
   or contain an <a> at the row level, so the first cell's link is expanded over
   the whole row: cmd/ctrl-click, middle-click, right-click "Open in new tab"
   and the hover URL preview all work anywhere on the row, with exactly ONE link
   per row in the accessibility tree.
   Known trade-off (accepted, TKT-3CSZRG): the overlay sits above the row's text,
   so text selection within a row is not possible. */
.entity-row .row-link {
  color: inherit;
  text-decoration: none;
}

.entity-row .row-link::after {
  content: '';
  position: absolute;
  inset: 0;
  /* Below the interactive cells lifted to z-index 1 below. */
  z-index: 0;
}

/* Nested controls must stay above the overlay or they become unclickable. */
.entity-row .select-cell,
.entity-row .actions-cell {
  position: relative;
  z-index: 1;
}

.entity-row:hover {
  background: var(--hover-bg);
}

.entity-row.selected {
  background: color-mix(in srgb, var(--accent-color) 15%, transparent);
  outline: 2px solid var(--accent-color);
  outline-offset: -2px;
}

.entity-row.selected:hover {
  background: color-mix(in srgb, var(--accent-color) 25%, transparent);
}

.entity-row.action-selected {
  background: color-mix(in srgb, var(--accent-color) 10%, transparent);
}

.entity-row.action-selected:hover {
  background: color-mix(in srgb, var(--accent-color) 20%, transparent);
}

.row-leave-active {
  transition: opacity 0.35s ease, background-color 0.35s ease;
  overflow: hidden;
}

.row-leave-active td {
  transition: padding 0.35s ease, line-height 0.35s ease, font-size 0.35s ease, border-color 0.35s ease;
  overflow: hidden;
}

.row-leave-from {
  opacity: 1;
}

.row-leave-to {
  opacity: 0;
  background-color: color-mix(in srgb, var(--accent-color) 20%, transparent);
}

.row-leave-to td {
  padding-top: 0;
  padding-bottom: 0;
  line-height: 0;
  font-size: 0;
  border-color: transparent;
}

.select-column,
.select-cell {
  width: 32px;
  text-align: center;
}

.select-cell input[type="checkbox"],
.select-column input[type="checkbox"] {
  cursor: pointer;
  accent-color: var(--accent-color, #6366f1);
}

.error-state {
  padding: 48px;
  text-align: center;
  color: var(--muted-text);
}

.error-state h2 {
  color: var(--error-color, #ef4444);
}

.actions-column {
  width: 40px;
}

.actions-cell {
  width: 40px;
  white-space: nowrap;
}

.delete-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  padding: 0;
  background: transparent;
  border: none;
  border-radius: var(--radius-sm);
  color: var(--muted-text);
  cursor: pointer;
  transition: all 0.15s;
}

.delete-btn:hover {
  background: color-mix(in srgb, var(--error-color) 15%, transparent);
  color: var(--error-color);
}

.table-scroll-wrapper {
  overflow-x: auto;
  -webkit-overflow-scrolling: touch;
}

.mobile-card {
  background: var(--card-bg);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-lg);
  padding: 12px;
  cursor: pointer;
  transition: all 0.15s;
  /* Containing block for the stretched title link. */
  position: relative;
}

/* Same stretched-link pattern as the desktop row: the title is the real link,
   expanded over the card so the whole card supports cmd/middle/right-click. */
a.mobile-card-title {
  color: inherit;
  text-decoration: none;
}

a.mobile-card-title::after {
  content: '';
  position: absolute;
  inset: 0;
  z-index: 0;
}

/* The delete button must stay above the overlay. */
.mobile-card .delete-btn {
  position: relative;
  z-index: 1;
}

.mobile-card + .mobile-card {
  margin-top: 8px;
}

.mobile-card:hover {
  border-color: var(--accent-color, #6366f1);
}

.mobile-card.selected {
  background: color-mix(in srgb, var(--accent-color) 15%, transparent);
  outline: 2px solid var(--accent-color);
  outline-offset: -2px;
}

.mobile-card.action-selected {
  background: color-mix(in srgb, var(--accent-color) 10%, transparent);
}

.mobile-card-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: var(--space-sm);
}

.mobile-card-title {
  font-size: 15px;
  font-weight: 500;
  color: var(--text-color);
  flex: 1;
  min-width: 0;
}

.mobile-card-fields {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 16px;
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--border-color);
}

.mobile-card-field {
  display: flex;
  align-items: center;
  gap: var(--space-2xs);
  font-size: var(--font-size-dense);
}

.mobile-card-label {
  color: var(--muted-text);
}

.mobile-card-label::after {
  content: ':';
}

.mobile-card-value {
  color: var(--text-color);
}

@media (max-width: 768px) {
  /* .list-header uses .mobile-topbar.mobile-topbar--with-menu from
     mobile-bars.css (sticky chrome + safe-area math + hamburger room). */
  .list-header h1 {
    font-size: var(--font-size-lg);
  }

  .list-content {
    background: none;
    box-shadow: none;
    border-radius: 0;
    overflow: visible;
  }

  .mobile-card .delete-btn {
    width: 44px;
    height: 44px;
  }
}

@media (max-width: 480px) {
  /* .main-content drops to 12px horizontal padding at this breakpoint;
     the sticky header's full-bleed negative margin must match or the
     header pokes 4px past each screen edge and triggers horizontal scroll. */
  .list-header {
    margin-left: -12px;
    margin-right: -12px;
  }
}
</style>
