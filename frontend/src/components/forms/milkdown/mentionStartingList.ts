/**
 * The `@` menu's starting list: what it offers before the user types a query.
 *
 * Three sources, in order (TKT-39TIB4, spec §2):
 *
 * 1. Entities related to the entity being edited. Writing about a ticket, you
 *    most often mention its feature, its concepts or a sibling.
 * 2. Entities this browser recently opened or mentioned (`recentEntities`).
 * 3. Globally recently modified entities, to fill what is left.
 *
 * The list is deduplicated, capped, and never contains the entity being edited.
 * A scoped query (`@ticket:`) keeps only that type.
 *
 * Every row comes from the ACL-gated API. The browser-local list holds ids
 * only; each is loaded again before it is shown, so a row the user can no
 * longer read is dropped rather than shown from a local copy.
 *
 * Transport-free: the caller injects the three fetches, which keeps this
 * testable and keeps axios out of it.
 */
import type { Entity } from '@/types'
import type { RecentEntity } from '@/utils/recentEntities'

/** How many rows the starting list shows. */
export const STARTING_LIST_SIZE = 8

/**
 * How long a recently-modified answer is reused, per scope.
 *
 * The server answers that query by reading every visible entity of the types
 * and sorting in Go, because no store orders by modification time yet
 * (TKT-KWQ2YN). The limit trims the response, not that work, so the menu must
 * not ask on every `@`. A minute stale is invisible in a list meant as a hint.
 */
export const RECENTLY_MODIFIED_TTL_MS = 60_000

/** The entity being edited. */
export interface StartingListSelf {
  /** The bare id. */
  id: string
  type: string
  /** The face being edited, or '' for an entity without one. */
  face: string
}

export interface StartingListSources {
  /**
   * The entity being edited, or null for a new entity. Read on every load,
   * since a form can switch entities without remounting its editor.
   */
  self: () => StartingListSelf | null
  /** Entities related to `self`. Not called when there is no `self`. */
  related: (self: StartingListSelf) => Promise<Entity[]>
  /** The browser-local recently viewed list, newest first. */
  recent: () => RecentEntity[]
  /** Loads one entity, or null when it is gone or hidden from this user. */
  load: (ref: RecentEntity) => Promise<Entity | null>
  /** Recently modified entities of these types, newest first. */
  recentlyModified: (types: string[], limit: number) => Promise<Entity[]>
  /** Every type name, for an unscoped recently-modified query. */
  allTypes: () => string[]
  /** The clock, for the recently-modified cache. Defaults to `Date.now`. */
  now?: () => number
}

export interface StartingList {
  /** The starting list for `scopeType`, or for every type when null. */
  load: (scopeType: string | null) => Promise<Entity[]>
}

/**
 * Builds a loader that caches per editor.
 *
 * Related entities and each resolved recent id are fetched at most once per
 * loader, since the menu opens many times while one entity is being edited.
 * Recently modified entities are reused for `RECENTLY_MODIFIED_TTL_MS` per
 * scope. A failed fetch is not cached, so the next opening tries again.
 */
export function createStartingList(src: StartingListSources): StartingList {
  const related = new Map<string, Promise<Entity[]>>()
  const loaded = new Map<string, Promise<Entity | null>>()
  const modified = new Map<string, { at: number; rows: Promise<Entity[]> }>()
  const now = src.now ?? Date.now

  function modifiedFresh(types: string[]): Promise<Entity[]> {
    if (types.length === 0) return Promise.resolve([])
    const key = types.join(',')
    const hit = modified.get(key)
    if (hit && now() - hit.at < RECENTLY_MODIFIED_TTL_MS) return hit.rows
    const rows = src.recentlyModified(types, STARTING_LIST_SIZE + 1).catch(() => {
      modified.delete(key)
      return []
    })
    modified.set(key, { at: now(), rows })
    return rows
  }

  function relatedOnce(self: StartingListSelf | null): Promise<Entity[]> {
    if (!self) return Promise.resolve([])
    // Each face has its own links, so the face is part of the key.
    const key = `${self.id}@${self.face}`
    let p = related.get(key)
    if (!p) {
      p = src.related(self).catch(() => {
        related.delete(key)
        return []
      })
      related.set(key, p)
    }
    return p
  }

  function loadOnce(ref: RecentEntity): Promise<Entity | null> {
    let p = loaded.get(ref.id)
    if (!p) {
      p = src.load(ref).catch(() => {
        loaded.delete(ref.id)
        return null
      })
      loaded.set(ref.id, p)
    }
    return p
  }

  async function load(scopeType: string | null): Promise<Entity[]> {
    const self = src.self()
    const out: Entity[] = []
    const seen = new Set<string>()
    if (self) seen.add(self.id)

    const take = (e: Entity | null): void => {
      if (!e || out.length >= STARTING_LIST_SIZE || seen.has(e.id)) return
      if (scopeType !== null && e.type !== scopeType) return
      seen.add(e.id)
      out.push(e)
    }

    // Start the global query alongside the others; it is only needed when the
    // first two sources leave room, but waiting for them first would add a
    // round trip to every opening.
    const recentlyModified = modifiedFresh(scopeType !== null ? [scopeType] : src.allTypes())

    for (const e of await relatedOnce(self)) take(e)

    const candidates = src
      .recent()
      .filter((r) => !seen.has(r.id) && (scopeType === null || r.type === scopeType))
      .slice(0, STARTING_LIST_SIZE - out.length)
    for (const e of await Promise.all(candidates.map(loadOnce))) take(e)

    for (const e of await recentlyModified) take(e)
    return out
  }

  return { load }
}
