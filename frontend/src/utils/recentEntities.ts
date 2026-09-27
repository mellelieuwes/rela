/**
 * Entities this browser recently opened or mentioned, newest first.
 *
 * Feeds the `@` menu's starting list (TKT-39TIB4). Only an id and a type are
 * kept, never a title: whatever is shown is loaded again through the ACL-gated
 * API, so an entity the user can no longer read drops out rather than being
 * shown from a stale local copy.
 *
 * Browser-local on purpose. It needs no server state, and a list of what one
 * person looked at is not something to share with the other users of a
 * project.
 */
import { isValidEntityRefId } from '@/components/forms/milkdown/entityRefId'

export interface RecentEntity {
  id: string
  type: string
}

const STORAGE_KEY = 'rela.recentEntities'
/** Enough to fill the starting list after filtering by type and dropping hidden rows. */
const MAX_ENTRIES = 30

/** Reads the list, tolerating a missing, corrupt or hand-edited value. */
export function listRecentEntities(): RecentEntity[] {
  let raw: string | null
  try {
    raw = localStorage.getItem(STORAGE_KEY)
  } catch {
    return []
  }
  if (!raw) return []
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return []
  }
  if (!Array.isArray(parsed)) return []
  const out: RecentEntity[] = []
  for (const item of parsed) {
    const { id, type } = (item ?? {}) as { id?: unknown; type?: unknown }
    if (isValidEntityRefId(id) && typeof type === 'string' && type !== '') {
      out.push({ id, type })
    }
  }
  return out
}

/** Moves the entity to the front of the list, adding it when absent. */
export function recordRecentEntity(id: string, type: string): void {
  if (!isValidEntityRefId(id) || type === '') return
  const next = [{ id, type }, ...listRecentEntities().filter((e) => e.id !== id)].slice(
    0,
    MAX_ENTRIES
  )
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(next))
  } catch {
    // Storage full or disabled: the starting list simply has fewer rows.
  }
}
