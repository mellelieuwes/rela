<script setup lang="ts">
import { ref, computed, type Component } from 'vue'
import { RouterLink, useRouter, type RouteLocationRaw } from 'vue-router'
import { useQuery, useMutation, useQueryCache } from '@pinia/colada'
import { useSchemaStore, useUIStore } from '@/stores'
import { listAllEntities, updateEntity, getErrorMessage } from '@/api'
import { entityKeys } from '@/queries/entities'
import { beginOptimistic, rollbackOptimistic, settleOptimistic } from '@/queries/optimisticList'
import type {
  Entity,
  KanbanConfig,
  KanbanCardField,
  KanbanColumn,
  KanbanSwimlane,
  ListParams,
} from '@/types'
import { viewHeaderMarkdown, viewFooterMarkdown } from '@/types'
import type { FilterState } from '@/types/filters'
import FilterBar from '@/components/lists/FilterBar.vue'
import BackButton from '@/components/common/BackButton.vue'
import { useBackTarget } from '@/composables/useBackTarget'
import { useUrlFilterSync } from '@/composables/useUrlFilterSync'
import { useWorld } from '@/composables/useWorld'
import { actionAllowed } from '@/utils/affordancesWarning'
import { filterStateToApiParams } from '@/utils/filters'
import { entityRef } from '@/utils/entityRef'
import { worldText } from '@/utils/worldText'
import { entityDisplayTitle } from '@/utils/entityDisplay'
import { renderMarkdown } from '@/utils/markdown'
import { hasIcon, resolveIcon } from '@/utils/icons'
import { formatCellValue } from '@/utils/format'
import { densePropertyRoutingHint } from '@/widgets/viewRouting'
import CardFieldList, { type ResolvedCardField } from '@/components/common/CardFieldList.vue'
import WorldBadge from '@/components/entity/WorldBadge.vue'
import { defaultRegistry } from '@/widgets/registry'
import type { DenseRoutingHint } from '@/widgets/viewRouting'

const props = defineProps<{
  id: string
}>()

const router = useRouter()
const schemaStore = useSchemaStore()
const uiStore = useUIStore()
const queryCache = useQueryCache()

// Back affordance — renders when ?return_to= or ?from= is present.
const backTarget = useBackTarget()

// State
const draggedCard = ref<Entity | null>(null)

// The selected world (`?world=`). A board is a projection through a world
// exactly as a list is: it decides which face each card shows AND which
// entities are on the board at all, since an entity with no face in the world
// is omitted entirely.
//
// `worldParam` is undefined under the default world, so it spreads into the
// params object without emitting an empty `?world=` — and it is part of the
// CACHE KEY below for the same reason EntityList keys on its params: two
// worlds are two different boards, and serving one from the other's cache
// would show faces the reader did not ask for.
const { world, isWorldBound, worldParam } = useWorld()

// The operator's announcement for the world on screen, or '' to announce
// nothing — the same split the list and detail pages make. Config, not data.
const worldBanner = computed<string>(
  () => (world.value ? schemaStore.worlds.get(world.value)?.banner : '') || '',
)

// Affordance gates: `_actions` map from the server. `false` → hide;
// anything else → render. Helper keeps the contract DRY across
// components; see frontend/src/utils/affordancesWarning.ts.
//
// From `_actions` alone, under every world. The server computes the map for
// the FACE each card shows, and the drag writes to that card's ADDRESS
// (`entityRef`) rather than its bare id, so a card that accepts the gesture
// writes the row the reader is looking at. A stand-in face the principal may
// not write reports `update: false` and refuses the drag up front — the
// honest ordering for a write triggered by a gesture. An earlier revision
// ANDed in `!isWorldBound`, which made every board read-only under a
// configured `default_world` (atlas worlds issue 2).
function canCreate(): boolean {
  return actionAllowed({ _actions: collectionActions.value }, 'create')
}
function canUpdate(entity: Entity): boolean {
  return actionAllowed(entity, 'update')
}

// The world note is a fact about a FACED type only: an entity of a type
// without faces has one state, present in every world, so no card can be
// missing from the board on the world's account (atlas worlds issue 1).
const boardTypeHasFaces = computed(() => {
  const def = schemaStore.getEntityType(kanbanConfig.value?.entity ?? '')
  return Object.keys(def?.faces ?? {}).length > 0
})
// The operator's `messages.projection` for the world, or nothing.
const projectionNote = computed<string>(() => {
  if (!boardTypeHasFaces.value) return ''
  const info = world.value ? schemaStore.worlds.get(world.value) : undefined
  return worldText(info?.messages?.projection, { world: world.value })
})

// Computed
const kanbanConfig = computed(() => schemaStore.getKanban(props.id) as KanbanConfig | undefined)

// The board's filter controls are the list's: the same FilterBar, the same
// URL sync, and the same server-side `filter[...]` params, so a relation
// control (whose options are the relation's targets) works on a board exactly
// as on a list. The board used to filter client-side on
// `entity.properties[control.property]`, which silently ignored every
// `relation:` control. Properties pinned by the board's static `filters:`
// cannot be overridden from the URL, as on a list. Declared after
// kanbanConfig: useUrlFilterSync reads staticFilterProperties during setup.
const { filters, writeToQuery } = useUrlFilterSync({
  staticFilterProperties: () =>
    new Set((kanbanConfig.value?.filters ?? []).map((f) => f.property)),
})

// Cards may render relation targets by ID; when any card field references a
// relation we must ask the server to embed the related entities (?include=*)
// so we can resolve those IDs to titles. Property-only boards fetch without
// includes, exactly as before.
const hasRelationFields = computed(
  () => kanbanConfig.value?.card.fields?.some((f) => !!f.relation) ?? false
)

// The board's list query (FEAT-XY2D1L). The key derives from the
// configured entity type, so switching boards (props.id) switches cache
// entries automatically, and useEvents' targeted SSE invalidation on
// ['entities', <type>] marks it stale and triggers a *background*
// refetch while this view is mounted. The template gates its spinner on
// `isPending` (no data yet), so refetches — including echoes of this
// client's own writes — never blank the board.
// listAllEntities, not listEntities: the board partitions the COMPLETE
// set by column property, and a single list call is one page (default
// 25) — treating it as the full set silently dropped page 2+ from the
// board (BUG-5OAQUG).
const boardParams = computed<ListParams | undefined>(() => {
  const params: ListParams = {}
  if (hasRelationFields.value) params.include = '*'
  if (worldParam.value) params.world = worldParam.value
  // See the same attachment in EntityList: the board's configured scope has to
  // travel on the request, since the endpoint is keyed by type.
  if (kanbanConfig.value?.query_scope) params.query_scope = kanbanConfig.value.query_scope
  // The user's filter controls, serialized exactly as EntityList does. They
  // are part of the params, so each filter state is its own cache entry.
  Object.assign(params, filterStateToApiParams(filters.value))
  return Object.keys(params).length ? params : undefined
})

// The board whose data the query last resolved; see placeholderData below.
let heldBoardId: string | undefined

const boardQuery = useQuery({
  // listParams, not the param-free `list`: the world has to separate cache
  // entries. It still shares the `list(type)` prefix, so SSE invalidation
  // reaches every world's board in one go.
  key: () => entityKeys.listParams(kanbanConfig.value?.entity ?? '', boardParams.value),
  // The signal matters more here than on single-fetch queries: when a
  // refetch supersedes this call (drag-drop settle, SSE echo), it also
  // cancels the remaining page fetches of the superseded loop.
  query: async ({ signal }) => {
    const config = kanbanConfig.value
    if (!config) throw new Error(`unknown kanban view: ${props.id}`)
    const boardId = props.id
    const result = await listAllEntities(config.entity, boardParams.value, signal)
    heldBoardId = boardId
    return result
  },
  enabled: () => !!kanbanConfig.value,
  // A filter change is a new key, which would start out pending and blank the
  // board while every page reloads. Hold the previous cards instead, as
  // EntityList does — but only for the same board: this view is reused across
  // boards, and another board's cards must not appear under this one's columns.
  placeholderData: (prev) => (heldBoardId === props.id ? prev : undefined),
})

const entities = computed(() => boardQuery.data.value?.data ?? [])
const includedEntities = computed<Record<string, Entity>>(
  () => boardQuery.data.value?.included ?? {}
)
const collectionActions = computed(() => boardQuery.data.value?._actions)
const loading = computed(() => boardQuery.isPending.value)

// has_more on the MERGED response means listAllEntities hit its page cap
// — the one case where the board is knowingly incomplete. Should never
// occur in practice (~5,000 entities), but silent truncation is exactly
// this view's bug class, so it gets a visible banner, not a console line.
const truncated = computed(() => boardQuery.data.value?.meta.has_more === true)
const totalCount = computed(() => boardQuery.data.value?.meta.total ?? 0)
const loadError = computed(() => {
  const err = boardQuery.error.value
  if (!err) return null
  return getErrorMessage(err, 'Failed to load board')
})

// pageState mirrors DynamicForm's `form-state-*` contract: a stable signal
// that this screen has finished resolving, so a screenshot{} capture can wait
// for it rather than hanging until its timeout.
const pageState = computed<'pending' | 'loaded' | 'error'>(() => {
  if (loadError.value) return 'error'
  return loading.value ? 'pending' : 'loaded'
})

// Admin-authored info regions from data-entry.yaml, rendered as sanitized
// markdown (renderMarkdown) above and below the board. Shares its resolvers and
// its .view-info styles with EntityList so both views behave identically.
const headerHtml = computed(() => renderMarkdown(viewHeaderMarkdown(kanbanConfig.value)))
const footerHtml = computed(() => renderMarkdown(viewFooterMarkdown(kanbanConfig.value)))

const entityType = computed(() => {
  if (!kanbanConfig.value) return undefined
  return schemaStore.getEntityType(kanbanConfig.value.entity)
})

const columns = computed(() => {
  if (!kanbanConfig.value) return []

  // Use defined columns or generate from unique values
  if (kanbanConfig.value.columns?.length) {
    return kanbanConfig.value.columns
  }

  // Default to the property's DECLARED enum values, in declaration order.
  //
  // `column_property` is required to be an enum (validate.go rejects anything
  // else), so the correct, ordered list is known at config-load time. Deriving
  // it from the data instead produced a board that was a picture of the current
  // rows rather than of the workflow: a state nobody is currently in had no
  // column at all — so a card could not be dragged BACK to it — and the order
  // followed entity insertion, which put `done` left of `doing` (TKT-R7H6G1).
  const property = kanbanConfig.value.column_property
  const declared = schemaStore.enumValuesForProperty(property, kanbanConfig.value.entity)
  if (declared?.length) {
    return declared.map(
      (v): KanbanColumn => ({
        value: v,
        label: schemaStore.getEnumLabel(v, property, kanbanConfig.value?.entity) ?? v,
      }),
    )
  }

  // Last resort for a non-enum property. The server rejects that today, so
  // this is unreachable in a valid config — kept so a schema that somehow
  // slips through renders a board instead of nothing.
  const values = new Set<string>()
  for (const entity of entities.value) {
    const val = String(entity.properties[property] || '')
    if (val) values.add(val)
  }
  return Array.from(values).map((v): KanbanColumn => ({ value: v, label: v }))
})

const filteredEntities = computed(() => {
  let result = [...entities.value]

  // Apply kanban config filters
  if (kanbanConfig.value?.filters) {
    for (const filter of kanbanConfig.value.filters) {
      result = result.filter((entity) => {
        const val = String(entity.properties[filter.property] || '')
        switch (filter.operator) {
          case '=':
          case '==':
            return val === filter.value
          case '!=':
            return val !== filter.value
          default:
            return true
        }
      })
    }
  }

  return result
})

// Swimlanes (rows in 2D grid layout)
const swimlanes = computed(() => {
  if (!kanbanConfig.value?.swimlane_property) return []

  // Use defined swimlanes or generate from unique values
  if (kanbanConfig.value.swimlanes?.length) {
    return kanbanConfig.value.swimlanes
  }

  // Same rule as the columns above: prefer the declared enum order. The old
  // path sorted alphabetically, which is just as arbitrary for a workflow as
  // insertion order — and equally hid a swimlane nobody currently occupies.
  const property = kanbanConfig.value.swimlane_property
  const declared = schemaStore.enumValuesForProperty(property, kanbanConfig.value.entity)
  if (declared?.length) {
    return declared.map(
      (v): KanbanSwimlane => ({
        value: v,
        label: schemaStore.getEnumLabel(v, property, kanbanConfig.value?.entity) ?? v,
      }),
    )
  }

  // Unreachable for the same reason as the column fallback — swimlane_property
  // is enum-required too (validate.go) — but kept, and sorted, so a config that
  // slips through renders deterministically rather than not at all.
  const values = new Set<string>()
  for (const entity of entities.value) {
    const val = String(entity.properties[property] || '')
    if (val) values.add(val)
  }
  return Array.from(values).sort().map((v): KanbanSwimlane => ({ value: v, label: v }))
})

const hasSwimmlanes = computed(() => swimlanes.value.length > 0)

// Accessible name for the board region. The columns are sections inside it, so
// the group needs its own name to be distinguishable from the rest of the page.
const boardLabel = computed(() => `${kanbanConfig.value?.title || 'Kanban'} board`)

// Column header text. An explicit kanban-config column label wins; otherwise
// fall back to the enum's display label for the grouping value, then the raw
// value. Keeps headers consistent with the card badges (which resolve labels
// via Badge). `column.label` defaults to the value for auto-generated columns
// (see the columns computed), so treat label===value as "no explicit label".
function columnTitle(column: { value: string; label?: string }): string {
  if (column.label && column.label !== column.value) return column.label
  const property = kanbanConfig.value?.column_property
  const entityTypeName = kanbanConfig.value?.entity
  return (
    schemaStore.getEnumLabel(column.value, property, entityTypeName) ??
    column.label ??
    column.value
  )
}

const entitiesByColumn = computed(() => {
  const grouped: Record<string, Entity[]> = {}
  const property = kanbanConfig.value?.column_property || ''

  for (const column of columns.value) {
    grouped[column.value] = []
  }

  for (const entity of filteredEntities.value) {
    const val = String(entity.properties[property] || '')
    if (grouped[val]) {
      grouped[val].push(entity)
    }
  }

  return grouped
})

// 2D grouping for swimlane mode: entitiesByCell[column][swimlane] = entities
const entitiesByCell = computed(() => {
  if (!hasSwimmlanes.value) return {}

  const cells: Record<string, Record<string, Entity[]>> = {}
  const colProp = kanbanConfig.value?.column_property || ''
  const swimProp = kanbanConfig.value?.swimlane_property || ''

  // Initialize all cells
  for (const column of columns.value) {
    cells[column.value] = {}
    for (const swimlane of swimlanes.value) {
      cells[column.value][swimlane.value] = []
    }
  }

  // Group entities into cells
  for (const entity of filteredEntities.value) {
    const colVal = String(entity.properties[colProp] || '')
    const swimVal = String(entity.properties[swimProp] || '')
    if (cells[colVal] && cells[colVal][swimVal]) {
      cells[colVal][swimVal].push(entity)
    }
  }

  return cells
})

// CSS grid style for swimlane board
const swimlaneGridStyle = computed(() => {
  const colCount = columns.value.length
  return {
    gridTemplateColumns: `auto repeat(${colCount}, minmax(240px, 1fr))`,
  }
})

// Drag-drop write path: optimistic copy-on-write against the query
// cache, rollback + toast on failure, reconcile with server truth via
// invalidation on settle. Cached entities are never mutated in place —
// other subscribers may hold references to the same objects.
interface MoveCardVars {
  entity: Entity
  updates: Record<string, string>
}

const { mutate: moveCard } = useMutation({
  mutation: ({ entity, updates }: MoveCardVars) => {
    const config = kanbanConfig.value
    if (!config) throw new Error(`unknown kanban view: ${props.id}`)
    // To the card's ADDRESS, face included — see utils/entityRef.
    return updateEntity(config.entity, entityRef(entity), { properties: updates })
  },
  onMutate({ entity, updates }: MoveCardVars) {
    return beginOptimistic(
      queryCache,
      entityKeys.list(kanbanConfig.value?.entity ?? ''),
      entity.id,
      (e) => ({ ...e, properties: { ...e.properties, ...updates } })
    )
  },
  onError(err, _vars, context) {
    rollbackOptimistic(queryCache, context)
    console.error('Failed to update entity:', err)
    uiStore.error(getErrorMessage(err, 'Failed to move card'))
  },
  async onSettled(_data, _err, _vars, context) {
    await settleOptimistic(queryCache, context)
  },
})

function getCardTitle(entity: Entity): string {
  if (!kanbanConfig.value) return entity.id
  return String(entity.properties[kanbanConfig.value.card.title] || entity.id)
}

// relationCardKey resolves the key under which a card field's relation
// targets are serialized on the entity's `relations` map. Outgoing edges
// are keyed by the relation name itself; incoming edges are keyed by the
// relation's declared inverse (schemaStore.getInverseName), falling back to
// `<relation>_inverse` when no inverse is declared. This mirrors the wire
// contract shared with EntityList relation columns (TKT-ODHV2D).
//
// MERGE-ORDER DEPENDENCY (RR-M8IIHV): the INCOMING branch only resolves once
// TKT-ODHV2D's server change lands. The list endpoint on this branch
// serializes OUTGOING edges only (see entityserializer.forWireRelated, fed by
// entityReader.outgoingRelations) — it does NOT populate the inverse key for
// incoming edges. Until ODHV2D merges, an incoming card field computes an
// inverse key that is absent from `relations`, so getCardFieldValue below
// returns '' and the card renders the '-' placeholder (degrades visibly, not a
// silent blank). The Go contract test `TestListEndpoint_IncomingEdge_InverseKey_ODHV2DContract`
// in internal/dataentry pins the server side of this inverse-key contract and
// activates once ODHV2D is integrated.
function relationCardKey(field: KanbanCardField): string {
  const rel = field.relation || ''
  if (field.direction === 'incoming') {
    return schemaStore.getInverseName(rel) || `${rel}_inverse`
  }
  return rel
}

function getCardFieldValue(entity: Entity, field: KanbanCardField): string {
  if (field.relation) {
    const ids = entity.relations?.[relationCardKey(field)] || []
    return ids
      .map((id) => {
        const included = includedEntities.value[id]
        // Unresolved target → raw ID fallback, matching EntityList.vue's
        // getFormattedCellValue (`included ? title : id`). Intentionally NOT
        // divergent: kanban and the list share one relation-cell contract
        // (RR-XM5ZEB). ACL-hidden targets do not leak their IDs here because
        // TKT-ODHV2D's server gate (`visibleRelationIDs`) removes hidden
        // neighbour IDs from the `relations` map before it reaches this
        // fallback — the SPA never sees a hidden ID to fall back to.
        return included ? entityDisplayTitle(included) : id
      })
      .join(', ')
  }
  if (!field.property) return ''
  // Formatted via the shared cell formatter so cards agree with list cells
  // (dates render human-readably, booleans as Yes/No, rrules as text). This
  // used to be a bare String(v || ''), which showed raw ISO datetimes and
  // "true" on cards -- see TKT-S9C14S.
  return formatCellValue(
    entity.properties[field.property],
    field.property,
    entityType.value,
    uiStore.effectiveTimezone
  )
}

// The stored property value, unformatted. PROPERTY fields only -- a relation
// field has no stored property and callers must use getCardFieldValue for it.
// (An earlier version returned the joined relation string from here, which
// made a function named "raw" hand pre-formatted text to a widget the moment
// anyone added a relation widget.)
function getCardFieldStoredValue(entity: Entity, field: KanbanCardField): unknown {
  if (!field.property) return undefined
  return entity.properties[field.property]
}



// Widget resolution for property card-fields, computed once per configured
// field rather than per card (RR-UD2A). Relation fields are absent on
// purpose: they have no PropertyDef and no relation widget, so they keep the
// joined-titles string path.
const cardFieldWidgets = computed(() => {
  const byProperty = new Map<string, { component: Component; hint: DenseRoutingHint }>()
  const type = entityType.value
  if (!type) return byProperty
  for (const field of kanbanConfig.value?.card.fields ?? []) {
    if (!field.property || field.relation || byProperty.has(field.property)) continue
    const hint = densePropertyRoutingHint(type.properties[field.property], field.property)
    byProperty.set(field.property, { component: defaultRegistry.resolveFromHint(hint), hint })
  }
  return byProperty
})

// One resolved card field: the widget plus the value shaped the way it wants.
// undefined means render the plain string span (relation fields, or a
// property with no widget entry).
//
// Unlike EntityList this needs no empty-value guard: visibleCardFields has
// already dropped empty fields before the template renders, so a widget's
// "no value" placeholder (MultiSelectWidget's em-dash, RR-UD2C) is
// unreachable here.
/**
 * Every visible field for one card, resolved once.
 *
 * The template used to call `resolveCardField` three times per field per card
 * (once for `v-if`, once for the component, once for each bound prop), so a
 * board of 50 cards with 3 fields did 450 resolutions per render where 150
 * would do — and each one re-derives a value the widget map already holds.
 *
 * Empty fields are dropped here rather than in the renderer, keeping the
 * dense-surface rule (empty renders as nothing, not a placeholder) with the
 * code that knows how a value is formatted.
 */
function resolvedCardFields(entity: Entity): ResolvedCardField[] {
  const out: ResolvedCardField[] = []

  for (const field of kanbanConfig.value?.card.fields ?? []) {
    const text = getCardFieldValue(entity, field)
    if (text === '') continue

    // Relation fields have no PropertyDef and so no widget: they render as
    // joined target titles, the contract shared with list relation columns.
    const entry = field.property && !field.relation
      ? cardFieldWidgets.value.get(field.property)
      : undefined

    out.push({
      field,
      component: entry?.component,
      propertyName: entry?.hint.propertyName,
      modelValue: entry
        ? entry.hint.preformatted
          ? text
          : getCardFieldStoredValue(entity, field)
        : undefined,
      text,
    })
  }
  return out
}

function onDragStart(event: DragEvent, entity: Entity) {
  draggedCard.value = entity
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = 'move'
    event.dataTransfer.setData('text/plain', entity.id)
  }
}

function onDragOver(event: DragEvent) {
  event.preventDefault()
  if (event.dataTransfer) {
    event.dataTransfer.dropEffect = 'move'
  }
}

function onDrop(event: DragEvent, columnValue: string, swimlaneValue?: string) {
  event.preventDefault()

  if (!draggedCard.value || !kanbanConfig.value) return

  const entity = draggedCard.value
  draggedCard.value = null

  // Defence in depth: `:draggable="false"` prevents drag-from-Kanban
  // starting, but external drag sources (text drag from another tab,
  // file drag) can still trigger this handler. Early-return on a
  // denied entity so we don't fire an update the server will 403.
  if (!canUpdate(entity)) return

  const colProp = kanbanConfig.value.column_property
  const swimProp = kanbanConfig.value.swimlane_property

  const currentCol = String(entity.properties[colProp] || '')
  const currentSwim = swimProp ? String(entity.properties[swimProp] || '') : undefined

  // Build update payload; skip the write when nothing moved.
  const updates: Record<string, string> = {}
  if (currentCol !== columnValue) {
    updates[colProp] = columnValue
  }
  if (swimProp && swimlaneValue !== undefined && currentSwim !== swimlaneValue) {
    updates[swimProp] = swimlaneValue
  }
  if (Object.keys(updates).length === 0) return

  moveCard({ entity, updates })
}

function onDragEnd() {
  draggedCard.value = null
}

// cardTarget is the single source of truth for where a card goes, bound to each
// card's RouterLink. The edit-form branch must be reproduced exactly, or a
// cmd-clicked tab would land on the detail page while a plain click opens the
// form.
//
// The card is now a real <a> (TKT-3CSZRG), which supersedes the earlier
// role="button" + @keydown shim: an anchor is natively focusable and
// Enter-activatable, so the keyboard half of the contract comes for free and
// cmd/middle-click open a tab, which the shim could never do.
function cardTarget(entity: Entity): RouteLocationRaw {
  // The form opens on the card's ADDRESS, face included, so an edit from a
  // world-bound board edits the face the card showed and not its bare id.
  if (kanbanConfig.value?.edit_form) {
    return `/form/${kanbanConfig.value.edit_form}/${entityRef(entity)}`
  }
  // The world rides along so the detail resolves the face the card showed.
  const path = `/entity/${entity.type}/${entity.id}`
  return worldParam.value ? { path, query: { world: worldParam.value } } : path
}

function createNew() {
  if (kanbanConfig.value?.create_form) {
    router.push(`/form/${kanbanConfig.value.create_form}`)
  }
}

// No lifecycle plumbing: the query fetches on mount, re-keys when
// props.id switches boards, and refetches in the background when
// useEvents invalidates ['entities', <type>] on SSE entity events.
</script>

<template>
  <div class="kanban-view" :data-testid="`page-state-${pageState}`">
    <header class="page-header">
      <div class="header-left">
        <BackButton v-if="backTarget" :target="backTarget" />
        <h1>{{ kanbanConfig?.title || props.id }}</h1>
      </div>
      <div class="header-actions">
        <button v-if="kanbanConfig?.create_form && canCreate()" class="btn btn-primary" @click="createNew">
          + New
        </button>
      </div>
    </header>

    <!--
      A board under a world is a PROJECTION: each card is one entity at the
      face the world resolved, and an entity with no face here has no card.
      Cards move — a drag writes the face the card shows — so the banner no
      longer claims the board is read-only; a card the principal may not write
      simply refuses the drag through `_actions`, as in the default world.
    -->
    <div v-if="isWorldBound && !loadError && (worldBanner || projectionNote)" class="world-banner">
      <!--
        Both halves are operator config: the ANNOUNCEMENT (`banner:`) and the
        NOTE (`messages.projection`, only on a board of a faced type). Neither
        declared: no banner (TKT-5SZG2L).
      -->
      <span v-if="worldBanner" class="world-banner__label">
        {{ worldBanner }}
      </span>
      <span v-if="projectionNote" class="world-banner__note">
        {{ projectionNote }}
      </span>
    </div>

    <FilterBar
      v-if="kanbanConfig?.filter_controls?.length"
      :config="kanbanConfig"
      :entity-type="entityType"
      :filters="filters"
      @filter="(f: FilterState) => writeToQuery(f)"
    />

    <!-- eslint-disable-next-line vue/no-v-html -- sanitized by renderMarkdown -->
    <div v-if="headerHtml" class="view-info view-info--top" v-html="headerHtml"/>

    <div v-if="truncated" class="truncation-banner" role="alert">
      Showing {{ entities.length }} of {{ totalCount }} items — the board is incomplete.
    </div>

    <div v-if="loading" class="loading-state">
      <div class="spinner"/>
      <span>Loading board...</span>
    </div>

    <div v-else-if="loadError" class="error-state">
      {{ loadError }}
    </div>

    <!-- Simple board (columns only) -->
    <div v-else-if="!hasSwimmlanes" class="kanban-board" role="group" :aria-label="boardLabel">
      <section
        v-for="column in columns"
        :key="column.value"
        class="kanban-column"
        :aria-labelledby="`kanban-col-${column.value}`"
        @dragover="onDragOver"
        @drop="onDrop($event, column.value)"
      >
        <h2 :id="`kanban-col-${column.value}`" class="column-header">
          <component
            :is="resolveIcon(column.icon)"
            v-if="hasIcon(column.icon)"
            class="column-icon"
            :size="16"
            aria-hidden="true"
          />
          <span class="column-title">{{ columnTitle(column) }}</span>
          <span class="column-count">{{ entitiesByColumn[column.value]?.length || 0 }}</span>
        </h2>

        <ul class="column-cards">
          <!-- The card is BOTH the link and the drag source. Deliberately no
               draggable="false" here (unlike RelationCards, RR-NPDW9A): that
               attribute is for an anchor nested INSIDE a drag source, and
               setting it on the drag source itself would disable reordering.
               onDragStart sets dataTransfer unconditionally, so the native
               link-drag is overridden in both the draggable and non-draggable
               branches.

               The <li> wrapper is `display: contents`, so the column is a real
               list for assistive tech while the anchor stays the flex item the
               layout and drag handlers were written against. RouterLink cannot
               itself render an <li>. -->
          <li v-for="entity in entitiesByColumn[column.value]" :key="entity.id" class="kanban-card-item">
            <RouterLink
              class="kanban-card"
              :to="cardTarget(entity)"
              :aria-label="getCardTitle(entity)"
              :draggable="canUpdate(entity) ? 'true' : 'false'"
              @dragstart="onDragStart($event, entity)"
              @dragend="onDragEnd"
            >
              <div class="card-id">{{ entity.id }}</div>
              <!--
                Per-CARD face provenance (TKT-ILT1WD), beside the title, the same
                place and the same component the list uses. A board is a
                projection through a world exactly as a table is, and a card that
                resolved to a stand-in is otherwise byte-identical to one that
                got the face the world asked for.

                WorldBadge renders only for a substitute, so an ordinary board
                — and every board under the default world, where `_world` is
                absent entirely — shows nothing at all.
              -->
              <div class="card-title text-wrap-anywhere">
                {{ getCardTitle(entity) }}<WorldBadge :world="entity._world" :entity-type="entity.type" />
              </div>
              <CardFieldList
                :fields="resolvedCardFields(entity)"
                :entity-type="kanbanConfig?.entity"
              />
            </RouterLink>
          </li>

          <li v-if="!entitiesByColumn[column.value]?.length" class="empty-column">
            No items
          </li>
        </ul>
      </section>
    </div>

    <!-- Swimlane board (2D grid layout) -->
    <div
      v-else
      class="kanban-swimlane-board"
      role="group"
      :aria-label="boardLabel"
      :style="swimlaneGridStyle"
    >
      <!-- Column headers -->
      <div class="swimlane-header-row">
        <div class="swimlane-label-cell" />
        <div
          v-for="column in columns"
          :key="column.value"
          class="swimlane-column-header"
        >
          <component
            :is="resolveIcon(column.icon)"
            v-if="hasIcon(column.icon)"
            class="column-icon"
            :size="16"
            aria-hidden="true"
          />
          <span class="column-title">{{ columnTitle(column) }}</span>
        </div>
      </div>

      <!-- Swimlane rows -->
      <div
        v-for="swimlane in swimlanes"
        :key="swimlane.value"
        class="swimlane-row"
      >
        <div class="swimlane-label-cell">
          <component
            :is="resolveIcon(swimlane.icon)"
            v-if="hasIcon(swimlane.icon)"
            class="column-icon"
            :size="16"
            aria-hidden="true"
          />
          <span class="swimlane-label">{{ swimlane.label || swimlane.value }}</span>
        </div>
        <ul
          v-for="column in columns"
          :key="column.value"
          class="swimlane-cell"
          :aria-label="`${columnTitle(column)} — ${swimlane.label || swimlane.value}`"
          @dragover="onDragOver"
          @drop="onDrop($event, column.value, swimlane.value)"
        >
          <!-- Same list-item wrapper as the simple board; see there. -->
          <li
            v-for="entity in entitiesByCell[column.value]?.[swimlane.value] || []"
            :key="entity.id"
            class="kanban-card-item"
          >
            <RouterLink
              class="kanban-card"
              :to="cardTarget(entity)"
              :aria-label="getCardTitle(entity)"
              :draggable="canUpdate(entity) ? 'true' : 'false'"
              @dragstart="onDragStart($event, entity)"
              @dragend="onDragEnd"
            >
              <div class="card-id">{{ entity.id }}</div>
              <!-- Face provenance; see the simple board above. -->
              <div class="card-title text-wrap-anywhere">
                {{ getCardTitle(entity) }}<WorldBadge :world="entity._world" :entity-type="entity.type" />
              </div>
              <CardFieldList
                :fields="resolvedCardFields(entity)"
                :entity-type="kanbanConfig?.entity"
              />
            </RouterLink>
          </li>
          <li v-if="!(entitiesByCell[column.value]?.[swimlane.value]?.length)" class="empty-cell">
            —
          </li>
        </ul>
      </div>
    </div>

    <!-- Sits after every board branch (loading/error/simple/swimlane) so it
         renders once regardless of state, and outside the board's horizontal
         scroll container so it stays visible on a wide board. -->
    <!-- eslint-disable-next-line vue/no-v-html -- sanitized by renderMarkdown -->
    <div v-if="footerHtml" class="view-info view-info--bottom" v-html="footerHtml"/>
  </div>
</template>

<style scoped>
/* The horizontal scroll belongs to the board containers, NOT this page wrapper.
   With overflow-x here, a board wider than the viewport dragged the page title,
   filter bar, truncation banner, and info regions sideways along with the
   columns. Scoping it to the boards keeps page furniture fixed. */
.kanban-view {
  max-width: 100%;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 24px;
}

.page-header h1 {
  margin: 0;
}

.header-left {
  display: flex;
  align-items: center;
  gap: var(--space-md);
}

.header-actions {
  display: flex;
  gap: var(--space-md);
}

.btn {
  padding: 8px 16px;
  border-radius: var(--radius-md);
  font-size: var(--font-size-base);
  font-weight: 500;
  cursor: pointer;
  border: none;
  transition: all 0.15s;
}

.btn-primary {
  background: var(--accent-color, #6366f1);
  color: white;
}

.btn-primary:hover {
  background: #4f46e5;
}

.truncation-banner {
  padding: 10px 16px;
  margin-bottom: 16px;
  border: 1px solid #f59e0b;
  border-radius: var(--radius-lg);
  background: rgba(245, 158, 11, 0.12);
  color: var(--text-color);
  font-size: var(--font-size-base);
}

.loading-state {
  display: flex;
  align-items: center;
  gap: var(--space-md);
  padding: 48px;
  color: var(--muted-text);
}

.spinner {
  width: 24px;
  height: 24px;
  border: 3px solid var(--border-color);
  border-top-color: var(--accent-color);
  border-radius: var(--radius-circle);
  animation: spin 1s linear infinite;
}

.kanban-board {
  display: flex;
  gap: var(--space-lg);
  padding-bottom: 20px;
  /* Columns size to their own content instead of stretching to the tallest
     one (the flex default, `align-self: stretch`). Combined with dropping the
     board's old `min-height: 500px`, a board of short cards is now as tall as
     its cards rather than a fixed slab of empty panel — the defect that made
     a 3-card board ~85% whitespace in a documentation figure. Each column
     keeps its own `min-height` (see .kanban-column) so it stays a credible
     drop target when empty. */
  align-items: flex-start;
  /* Per CSS spec a non-visible overflow on one axis coerces the other from
     `visible` to `auto`, so this box now ALSO clips vertically — `overflow-y:
     visible` cannot opt out. Safe today: the card hover box-shadow (8px blur)
     is inset by .column-cards' 12px padding, and padding-bottom cushions the
     last card. Anything that must escape a card's box vertically (drag ghost,
     tooltip, popover, sticky column header) will be clipped here — that is the
     accepted cost of scoping the horizontal scroll to the board. */
  overflow-x: auto;
}

.kanban-column {
  flex: 1;
  min-width: 280px;
  max-width: 350px;
  /* Fits content, with a floor that keeps a short or empty column an obvious
     panel and a comfortable drop target rather than a bare header strip.
     `min-height` (not `height`) is the point: a column with many cards grows
     past this, a column with none still reads as a panel. Deliberately well
     below the old 500px, which was a de-facto fixed height.

     The floor is proportional to the viewport with an absolute lower bound, so
     the board neither leaves a short window mostly empty nor squeezes the
     columns to a strip on a small one. min() takes whichever is smaller, and
     max() keeps 180px as the hard floor. */
  min-height: max(180px, min(340px, 38vh));
  background: var(--hover-bg);
  border-radius: var(--radius-lg);
  display: flex;
  flex-direction: column;
}

/* Now an <h2> (it names the column section via aria-labelledby), so the UA
   heading margin and font-size are reset back to the original div rendering. */
.column-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px 16px;
  border-bottom: 1px solid var(--border-color);
  margin: 0;
  font-size: var(--font-size-base);
  font-weight: 600;
}

/* Config-authored icon beside a column or swimlane label. Inherits
 * currentColor, so it follows the theme — the emoji it replaces could not. */
.column-icon {
  flex-shrink: 0;
  margin-right: var(--space-xs);
  vertical-align: text-bottom;
}

.column-title {
  font-size: var(--font-size-base);
  font-weight: 600;
  color: var(--text-color);
}

.column-count {
  background: var(--border-color);
  color: var(--muted-text);
  padding: 2px 8px;
  border-radius: var(--radius-xl);
  font-size: var(--font-size-sm);
  font-weight: 500;
}

/* Now a <ul> of <li> cards — the cards genuinely are a list. Reset the UA
   list styling so the flex column renders exactly as it did as a div. */
.column-cards {
  flex: 1;
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: var(--space-sm);
  overflow-y: auto;
  margin: 0;
  list-style: none;
}

/* Mirrors the list and detail pages' banner so the three surfaces read as one
   affordance rather than three inventions. */
.world-banner {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: var(--space-xs);
  margin-bottom: var(--space-md);
  padding: var(--space-sm) var(--space-md);
  background: color-mix(in srgb, var(--accent-color) 10%, transparent);
  border: 1px solid color-mix(in srgb, var(--accent-color) 30%, transparent);
  border-radius: var(--radius-md);
}

.world-banner__label {
  font-size: var(--font-size-base);
  color: var(--text-color);
  font-weight: 500;
}

.world-banner__note {
  font-size: var(--font-size-sm);
  color: var(--muted-text);
}

/* The list item exists purely for <ul>/<li> semantics; `display: contents`
   removes its box so the .kanban-card anchor inside remains the flex item of
   .column-cards / .swimlane-cell, exactly as it was before the card became a
   link. RouterLink cannot render an <li> itself. */
.kanban-card-item {
  display: contents;
}

.kanban-card {
  display: block;
  background: var(--card-bg);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md);
  padding: 12px;
  cursor: grab;
  transition: all 0.15s;
  /* The card is a real link so cmd/middle-click opens a tab; it must not pick
     up link colour or underline. */
  color: inherit;
  text-decoration: none;
}

.kanban-card:hover {
  border-color: var(--accent-color, #6366f1);
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.08);
}

.kanban-card:active {
  cursor: grabbing;
}

/* The card is keyboard-reachable (tabindex="0"), so it needs a visible focus
   indicator. Two-shadow token pattern per the project focus-ring convention. */
.kanban-card:focus-visible {
  outline: none;
  box-shadow:
    0 0 0 2px var(--focus-ring-gap),
    0 0 0 4px var(--focus-ring);
}

.card-id {
  font-family: monospace;
  font-size: var(--font-size-xs);
  color: var(--muted-text);
  margin-bottom: 4px;
}

.card-title {
  font-size: var(--font-size-base);
  font-weight: 500;
  color: var(--text-color);
  margin-bottom: 8px;
}

.card-fields {
  display: flex;
  flex-direction: column;
  gap: var(--space-2xs);
}

.card-field {
  display: flex;
  gap: var(--space-2xs);
  font-size: var(--font-size-sm);
}

.field-label {
  color: var(--muted-text);
}

.field-value {
  color: var(--text-color);
}

/* An empty workflow state must stay visible and droppable (TKT-R7H6G1) — it is
   never removed. What is adjusted here is only its visual WEIGHT: an empty
   column previously rendered as a full-height filled panel, so on a board where
   most states are unoccupied the empty columns out-weighed the ones carrying
   cards. The placeholder is centred in whatever height the column has, and the
   column itself is de-emphasised below. */
.empty-column {
  color: var(--muted-text);
  font-size: var(--font-size-dense);
  text-align: center;
  padding: var(--space-xl) var(--space-md);
  margin: auto 0;
}

/* :has() lets the COLUMN respond to being empty without a JS-computed class,
   keeping the emptiness a fact about the rendered cards rather than a second
   source of truth. Where :has() is unsupported the column simply keeps the
   normal filled treatment — a graceful degradation to the previous look, not a
   broken one. The dashed outline is what keeps it legible as a drop target. */
.kanban-column:has(.empty-column) {
  background: transparent;
  border: 1px dashed var(--border-color);
}

/* Swimlane board styles (2D grid layout) */
.kanban-swimlane-board {
  display: grid;
  /* grid-template-columns set via inline style */
  gap: 1px;
  background: var(--border-color);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-lg);
  /* Two-value form: scroll horizontally when the grid is wider than the
     viewport, while keeping the vertical `hidden` that clips cells to the
     rounded border. A bare `overflow-x: auto` would drop that clipping. */
  overflow: auto hidden;
  /* Was 400px. The grid's rows already size to their cards and each cell keeps
     its own floor, so a fixed board minimum only padded a short board with
     empty grid — the same defect the simple board had. Kept low enough to hold
     the header row plus one lane. */
  min-height: 160px;
}

.swimlane-header-row {
  display: contents;
}

.swimlane-label-cell {
  background: var(--hover-bg);
  padding: 12px 16px;
  display: flex;
  align-items: center;
  min-width: 120px;
  max-width: 180px;
}

.swimlane-column-header {
  background: var(--hover-bg);
  padding: 12px 16px;
  text-align: center;
  font-weight: 600;
  font-size: var(--font-size-base);
}

.swimlane-row {
  display: contents;
}

.swimlane-label {
  font-weight: 600;
  font-size: var(--font-size-dense);
  color: var(--text-color);
  writing-mode: horizontal-tb;
}

/* Now a <ul> of <li> cards, like .column-cards. Same UA reset. */
.swimlane-cell {
  background: var(--card-bg);
  padding: 8px;
  display: flex;
  flex-direction: column;
  gap: var(--space-sm);
  min-height: 100px;
  overflow-y: auto;
  margin: 0;
  list-style: none;
}

.swimlane-cell:hover {
  background: var(--hover-bg);
}

.empty-cell {
  color: var(--muted-text);
  font-size: var(--font-size-sm);
  text-align: center;
  padding: 8px;
  opacity: 0.5;
}

@media (max-width: 768px) {
  .kanban-board {
    gap: var(--space-md);
  }

  .kanban-column {
    min-width: 220px;
    max-width: 300px;
    min-height: 140px;
  }

  .column-header {
    padding: 10px 12px;
  }

  .column-cards {
    padding: 8px;
  }

  .kanban-card {
    padding: 10px;
  }

  .swimlane-label-cell {
    min-width: 100px;
    max-width: 140px;
  }
}
</style>
