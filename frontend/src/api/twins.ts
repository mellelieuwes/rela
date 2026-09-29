import { api } from './client'

/**
 * Where a twin stands in the sync loop. `pending`: a side changed, or it never
 * synced; `conflict`: a shared field changed on both sides; `gone`: the entity
 * or the external item no longer exists.
 */
export type TwinState = 'in_sync' | 'pending' | 'conflict' | 'gone'

/** One field the last sync could not settle on its own. */
export interface TwinFinding {
  field: string
  kind: 'conflict' | 'foreign_edit' | 'local_drift' | 'rejected'
  /**
   * The agreed, rela and external values. `null` when absent — and always
   * `null` for a field the caller may not see: the field name stays, its
   * values never reach the client.
   */
  base: unknown
  ours: unknown
  theirs: unknown
  propose: boolean
  message?: string
}

/**
 * One entity's counterpart in an external system, as served by
 * `/api/v1/_twins/{type}/{id}`. Twins are edited only through `rela twin`;
 * the web app shows them.
 */
export interface Twin {
  system: string
  external_id: string
  url: string
  state: TwinState
  has_base: boolean
  /** RFC 3339; absent until the first sync. */
  synced_at?: string
  remote_updated_at?: string
  /** Fields this system owns on the entity; `*` is every field, `body` the content. */
  owned_fields: string[]
  findings: TwinFinding[]
}

/**
 * listTwins returns an entity's twins, sorted by system.
 *
 * Rejects with a 404 both when the entity cannot be read and when no pact is
 * declared — deliberately indistinguishable, so a failure means "no twins to
 * show", never proof of absence.
 */
export async function listTwins(entityType: string, entityId: string): Promise<Twin[]> {
  return api.get<Twin[]>(`/_twins/${encodeURIComponent(entityType)}/${encodeURIComponent(entityId)}`)
}
