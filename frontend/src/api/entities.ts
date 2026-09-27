import { api } from './client'
import type {
  Entity,
  CreateEntity,
  ListResponse,
  ListParams,
  AnalyzeResult,
  Template,
  RelationEntry,
  ModernRelationsField,
  NextActionResponse,
  NextActionFeedbackKind,
} from '@/types'
import { warnIfMissingActions } from '@/utils/affordancesWarning'

// Type → URL-path-segment (plural) registry. The API layer is a pure HTTP
// boundary; it must not import a Pinia store (the producer/consumer
// inversion the project's CLAUDE.md warns against, BUG-style B1a). The
// schema store calls registerEntityPlurals() once on load with the
// metamodel's plurals, before any view fires an entity request.
const pluralRegistry = new Map<string, string>()

export function registerEntityPlurals(plurals: Map<string, string>): void {
  pluralRegistry.clear()
  for (const [type, plural] of plurals) pluralRegistry.set(type, plural)
}

// For tests that exercise the API layer without the schema store.
export function _setEntityPluralForTest(type: string, plural: string): void {
  pluralRegistry.set(type, plural)
}

export function _resetEntityPluralsForTest(): void {
  pluralRegistry.clear()
}

export function getPlural(type: string): string {
  const plural = pluralRegistry.get(type)
  if (!plural) {
    // Fail fast instead of fabricating a URL (`category` → `/categorys`
    // would 404 confusingly). An unknown type here means the schema
    // wasn't loaded or the caller passed a typo.
    throw new Error(`unknown entity type "${type}" — no registered plural`)
  }
  return plural
}

export async function listEntities(
  type: string,
  params?: ListParams,
  signal?: AbortSignal
): Promise<ListResponse<Entity>> {
  const path = `/${getPlural(type)}`
  const res = await api.get<ListResponse<Entity>>(path, params as Record<string, unknown>, signal)
  warnIfMissingActions(res, path)
  return res
}

// listAllEntities pages: a runaway guard, not a working limit. 50 pages at
// the server's per_page cap of 100 is ~5,000 entities — far past a usable
// board. On cap hit the merged meta keeps has_more: true so the consumer
// can tell the user the set is incomplete instead of silently truncating.
const MAX_LIST_ALL_PAGES = 50

/**
 * Fetches EVERY page of a collection and merges them into one ListResponse.
 * A single listEntities call returns one PAGE (server default 25, cap 100);
 * consumers that render the complete set client-side — the kanban board
 * partitions all entities by column property — silently drop page 2+ if
 * they treat that page as the full set (BUG-5OAQUG).
 *
 * Contract:
 * - Response-driven paging: follows meta.has_more / meta.page and assumes
 *   nothing about whether the requested per_page was honored (the server
 *   silently falls back to 25 on out-of-range values).
 * - data is deduped by entity ID (later page wins). Pages are fetched
 *   sequentially against a live store; a write landing between fetches
 *   shifts offsets, and a duplicated ID would break v-for keys and the
 *   optimistic-list helpers. Entities that slip BETWEEN pages the same way
 *   are healed by the SSE invalidation that write also triggers.
 * - included maps are merged (later page wins, same rule as data);
 *   _actions comes from the first page; meta reports the merged set
 *   (page 1, per_page = data.length) with has_more true only when the
 *   fetch stopped before the server ran out of pages.
 * - signal aborts the loop between pages as well as in-flight: Pinia
 *   Colada aborts it when a refetch supersedes this call (drag-drop
 *   settle, SSE echo), and without it a superseded loop would keep
 *   paging to the cap producing a result the cache discards.
 */
export async function listAllEntities(
  type: string,
  params?: ListParams,
  signal?: AbortSignal
): Promise<ListResponse<Entity>> {
  const byId = new Map<string, Entity>()
  const included: Record<string, Entity> = {}
  let first: ListResponse<Entity> | undefined
  let res: ListResponse<Entity>
  let page = 1

  for (let fetched = 1; ; fetched++) {
    res = await listEntities(type, { ...params, page, per_page: 100 }, signal)
    first ??= res
    for (const e of res.data) byId.set(e.id, e)
    Object.assign(included, res.included)
    if (!res.meta.has_more || fetched >= MAX_LIST_ALL_PAGES) break
    // Defensive: has_more with an empty page would otherwise spin to the
    // cap re-fetching nothing. Break and keep has_more so the anomaly is
    // visible to the consumer rather than silently reported as complete.
    if (res.data.length === 0) break
    page = res.meta.page + 1
  }

  const data = [...byId.values()]
  return {
    data,
    included,
    _actions: first._actions,
    meta: {
      total: res.meta.total,
      page: 1,
      per_page: data.length,
      has_more: res.meta.has_more,
    },
  }
}

// getEntity reads one entity. `world` selects which face is served, and it
// COMBINES with `include`: TKT-WRLDAPI item 4 made neighbor resolution
// world-scoped, so an included peer is that world's face of the neighbor and a
// neighbor with no face in the world is simply absent.
//
// The two were mutually exclusive until then (422 world_include_unsupported),
// which is why callers may still carry code shaped around avoiding the
// combination.
export async function getEntity(
  type: string,
  id: string,
  params?: { include?: string; fields?: string; world?: string }
): Promise<Entity> {
  const path = `/${getPlural(type)}/${id}`
  const res = await api.get<Entity>(path, params)
  warnIfMissingActions(res, path)
  return res
}

export async function createEntity(type: string, entity: CreateEntity): Promise<Entity> {
  const path = `/${getPlural(type)}`
  const res = await api.post<Entity>(path, entity)
  warnIfMissingActions(res, path)
  return res
}

// dryRunCreateEntity evaluates field/option/relation affordances and
// soft validation against a candidate WITHOUT persisting (TKT-3I5U).
// The create form calls it on mount and (debounced) as the user types
// to gate inputs and surface warnings before commit. The verdicts are
// ADVISORY — the real createEntity re-authorizes. `signal` lets the
// caller drop a stale in-flight request (RR-ZKL2).
//
// Relations are intentionally NOT sent: a candidate has no real ID so
// edges can't be staged; relation affordances reflect the per-type
// verdict only.
export async function dryRunCreateEntity(
  type: string,
  candidate: Pick<CreateEntity, 'id' | 'prefix' | 'world' | 'properties' | 'content'>,
  signal?: AbortSignal
): Promise<Entity> {
  const path = `/${getPlural(type)}?dry_run=true`
  return api.post<Entity>(path, candidate, { signal })
}

// EntityPatch is the body shape for the unified PATCH endpoint.
// `relations` uses the JSON:API §9 wrapper exclusively; the legacy
// IDs-only form was removed in chore/drop-legacy-relations-shape.
//
// `properties_unset` (TKT-E6094) lets callers express "user cleared
// this field" distinct from "field was untouched". Autosave uses it
// to delete keys atomically alongside property upserts.
export type EntityPatch = Omit<Partial<Entity>, 'relations'> & {
  properties_unset?: string[]
  relations?: ModernRelationsField
}

export async function updateEntity(
  type: string,
  id: string,
  patch: EntityPatch,
  etag?: string,
  signal?: AbortSignal
): Promise<Entity> {
  const path = `/${getPlural(type)}/${id}`
  const res = await api.patch<Entity>(path, patch, etag, signal)
  warnIfMissingActions(res, path)
  return res
}

export async function deleteEntity(type: string, id: string): Promise<void> {
  return api.delete(`/${getPlural(type)}/${id}`)
}

/**
 * Searches entities by query text, optionally filtered by type.
 * Pass an AbortSignal to cancel an in-flight request — the command palette
 * uses this to abort superseded searches as the user types.
 *
 * `world` selects the world the hits are resolved in. Omitted, the server
 * applies `app.default_world`; callers on a page pass `useWorld().worldParam`
 * so search agrees with the page the user is browsing.
 */
export async function searchEntities(
  query: string,
  type?: string,
  signal?: AbortSignal,
  world?: string
): Promise<ListResponse<Entity>> {
  const params: Record<string, string> = { q: query }
  if (type) {
    params.type = type
  }
  if (world) {
    params.world = world
  }
  return api.get<ListResponse<Entity>>('/_search', params, signal)
}

/**
 * The most recently modified entities of the given types, newest first.
 *
 * Uses the search query language (`type:` plus `sort:modified:desc`) with the
 * endpoint's `limit`, so only `limit` rows cross the wire. The server applies
 * the read gate before the limit. Type names come from the schema, never from
 * user input. `world` is as for `searchEntities`.
 */
export async function listRecentlyModified(
  types: string[],
  limit: number,
  signal?: AbortSignal,
  world?: string
): Promise<ListResponse<Entity>> {
  const params: Record<string, string> = {
    q: `type:${types.join(',')} sort:modified:desc`,
    limit: String(limit),
  }
  if (world) {
    params.world = world
  }
  return api.get<ListResponse<Entity>>('/_search', params, signal)
}

/**
 * ScopeDescriptor encodes the query that defines an ordered result set the
 * user is navigating — a typed list or a search result today. It is sent to
 * `/_position` as a single URL-encoded JSON `scope` param. `filters` uses the
 * same flat bracket-format keys as filterStateToApiParams ("filter[status]")
 * so the wire format has one source of truth. See backend internal/dataentry/
 * scope.go and issue #844.
 */
export interface ScopeDescriptor {
  source: 'list' | 'search'
  // Required for a list scope (single-type). Optional for a search scope,
  // where it narrows a possibly-mixed-type result to one type; omit it to
  // navigate across all matched types. The backend enforces this per-source.
  type?: string
  filters?: Record<string, string>
  sort?: string
  // Required for a search scope; optional free-text filter within a list scope.
  q?: string
  // The originating list's `query_scope:`, so prev/next walks the set the list
  // actually showed. Omitting it resolves the position against the entity
  // type's DEFAULT scope instead: on a list showing `archief`, the open entity
  // is then absent from its own scope and the endpoint answers 404.
  query_scope?: string
}

/** A neighbouring entity in a scope. `type` is needed to build the target's
 * detail route, since a search scope can span entity types. */
export interface PositionRef {
  id: string
  type: string
}

/** EntityPosition mirrors the backend V1Position payload. */
export interface EntityPosition {
  prev: PositionRef | null
  next: PositionRef | null
  current: number
  total: number
}

/**
 * Resolve an entity's position within a scope. The server runs the same
 * filter/sort pipeline as the list endpoint and returns only {prev, next,
 * current, total} — no entity bodies — so navigation is correct at any set
 * size. Replaces the old per_page=1000 fetch-and-scan that silently truncated
 * past the pagination cap (#844).
 */
export async function getEntityPosition(
  id: string,
  scope: ScopeDescriptor,
  signal?: AbortSignal
): Promise<EntityPosition> {
  return api.get<EntityPosition>('/_position', { id, scope: JSON.stringify(scope) }, signal)
}

export async function analyze(): Promise<AnalyzeResult> {
  return api.get<AnalyzeResult>('/_analyze')
}

export async function getTemplates(entityType: string): Promise<Template[]> {
  return api.get<Template[]>(`/_templates/${entityType}`)
}

export async function createRelation(
  type: string,
  entityId: string,
  relationName: string,
  targetId: string,
  meta?: Record<string, unknown>,
  direction?: string
): Promise<void> {
  const body: { id: string; meta?: Record<string, unknown>; direction?: string } = { id: targetId }
  if (meta && Object.keys(meta).length > 0) {
    body.meta = meta
  }
  if (direction === 'incoming') {
    body.direction = direction
  }
  return api.post(`/${getPlural(type)}/${entityId}/relations/${relationName}`, body)
}

/**
 * Every visible relation of an entity, both directions, grouped by wire key.
 *
 * Incoming edges are keyed by the relation's INVERSE name, not its canonical
 * one, and each entry carries its own `direction`. The create body accepts
 * those same inverse keys (`resolveDirection` server-side), so a caller
 * building a create payload passes keys through unchanged rather than mapping
 * them back.
 *
 * Every edge whose peer the caller may not read is already dropped server-side,
 * so a derived count is a total over VISIBLE peers — per-principal by
 * construction, and deliberately not the raw graph total.
 */
export async function getAllEntityRelations(
  type: string,
  entityId: string,
  world?: string
): Promise<Record<string, RelationEntry[]>> {
  // The world is carried because a content-scoped relation belongs to ONE
  // face: reading unscoped while the page shows a non-default world would
  // enumerate a different face's edges than the one on screen.
  return api.get<Record<string, RelationEntry[]>>(
    `/${getPlural(type)}/${entityId}/relations`,
    world ? { world } : undefined
  )
}

export async function getEntityRelations(
  type: string,
  entityId: string,
  relationName: string,
  direction?: string
): Promise<RelationEntry[]> {
  const params = direction === 'incoming' ? { direction } : undefined
  return api.get<RelationEntry[]>(
    `/${getPlural(type)}/${entityId}/relations/${relationName}`,
    params
  )
}

export async function updateRelationProperties(
  type: string,
  entityId: string,
  relationName: string,
  targetId: string,
  meta: Record<string, unknown>,
  direction?: string
): Promise<void> {
  return api.patch(`/${getPlural(type)}/${entityId}/relations/${relationName}/${targetId}`, {
    meta,
    ...(direction === 'incoming' ? { direction } : {}),
  })
}

export async function deleteRelation(
  type: string,
  entityId: string,
  relationName: string,
  targetId: string,
  direction?: string
): Promise<void> {
  const query = direction === 'incoming' ? `?direction=${direction}` : ''
  return api.delete(`/${getPlural(type)}/${entityId}/relations/${relationName}/${targetId}${query}`)
}

/**
 * Fetch the single advisory suggestion to show now, or null when nothing is
 * owed. A null suggestion is the NORMAL, frequent answer for a well-configured
 * system — not an error, and not an empty state worth apologising for.
 *
 * `world` is the world the reader is BROWSING, and it supplies the display
 * world only: the server consults it against each source's `visible_worlds:`
 * allow list. It scopes no read — which world a source queries is
 * `source_world:`, operator config that a caller cannot reach past.
 */
export async function getNextAction(
  world?: string,
  signal?: AbortSignal
): Promise<NextActionResponse> {
  return api.get<NextActionResponse>('/_next_action', world ? { world } : undefined, signal)
}

/**
 * Record how the user answered a suggestion.
 *
 * `shown` is sent when the suggestion is actually displayed, which is what
 * starts its cooldown — the GET deliberately does not, so a prefetch or a
 * discarded response cannot silently consume the suggestion.
 */
export async function sendNextActionFeedback(body: {
  source: string
  entity_id?: string
  variant?: string
  kind: NextActionFeedbackKind
  duration?: string
}): Promise<void> {
  await api.post('/_next_action', body)
}
