import { describe, it, expect, vi } from 'vitest'
import type { Entity } from '@/types'
import {
  createStartingList,
  RECENTLY_MODIFIED_TTL_MS,
  STARTING_LIST_SIZE,
  type StartingListSources,
} from './mentionStartingList'

function ent(id: string, type = 'ticket'): Entity {
  return { id, type, _title: id, properties: {} }
}

function sources(over: Partial<StartingListSources> = {}): StartingListSources {
  return {
    self: () => ({ id: 'TKT-SELF', type: 'ticket', face: '' }),
    related: vi.fn(async () => []),
    recent: () => [],
    load: vi.fn(async (r) => ent(r.id, r.type)),
    recentlyModified: vi.fn(async () => []),
    allTypes: () => ['ticket', 'bug'],
    ...over,
  }
}

const ids = (list: Entity[]) => list.map((e) => e.id)

describe('createStartingList', () => {
  it('orders related, then recently viewed, then recently modified', async () => {
    const list = createStartingList(
      sources({
        related: async () => [ent('TKT-R')],
        recent: () => [{ id: 'TKT-V', type: 'ticket' }],
        recentlyModified: async () => [ent('TKT-M')],
      })
    )
    expect(ids(await list.load(null))).toEqual(['TKT-R', 'TKT-V', 'TKT-M'])
  })

  it('removes duplicates and the entity being edited', async () => {
    const list = createStartingList(
      sources({
        related: async () => [ent('TKT-A'), ent('TKT-SELF')],
        recent: () => [
          { id: 'TKT-A', type: 'ticket' },
          { id: 'TKT-SELF', type: 'ticket' },
          { id: 'TKT-B', type: 'ticket' },
        ],
        recentlyModified: async () => [ent('TKT-B'), ent('TKT-SELF'), ent('TKT-C')],
      })
    )
    expect(ids(await list.load(null))).toEqual(['TKT-A', 'TKT-B', 'TKT-C'])
  })

  it('caps the list', async () => {
    const many = Array.from({ length: 20 }, (_, i) => ent(`TKT-${i}`))
    const list = createStartingList(sources({ related: async () => many }))
    expect(await list.load(null)).toHaveLength(STARTING_LIST_SIZE)
  })

  it('keeps only the scoped type and asks for recently modified of that type', async () => {
    const recentlyModified = vi.fn(async () => [ent('BUG-M', 'bug')])
    const list = createStartingList(
      sources({
        related: async () => [ent('TKT-R'), ent('BUG-R', 'bug')],
        recent: () => [
          { id: 'TKT-V', type: 'ticket' },
          { id: 'BUG-V', type: 'bug' },
        ],
        recentlyModified,
      })
    )
    expect(ids(await list.load('bug'))).toEqual(['BUG-R', 'BUG-V', 'BUG-M'])
    expect(recentlyModified).toHaveBeenCalledWith(['bug'], STARTING_LIST_SIZE + 1)
  })

  it('asks for recently modified across every type when unscoped', async () => {
    const recentlyModified = vi.fn(async () => [])
    await createStartingList(sources({ recentlyModified })).load(null)
    expect(recentlyModified).toHaveBeenCalledWith(['ticket', 'bug'], STARTING_LIST_SIZE + 1)
  })

  it('drops a recent entity the user can no longer read', async () => {
    const list = createStartingList(
      sources({
        recent: () => [
          { id: 'TKT-HIDDEN', type: 'ticket' },
          { id: 'TKT-OK', type: 'ticket' },
        ],
        load: async (r) => (r.id === 'TKT-HIDDEN' ? null : ent(r.id)),
      })
    )
    expect(ids(await list.load(null))).toEqual(['TKT-OK'])
  })

  it('does not ask for related entities of a new entity', async () => {
    const related = vi.fn(async () => [])
    await createStartingList(sources({ self: () => null, related })).load(null)
    expect(related).not.toHaveBeenCalled()
  })

  it('loads related entities and each recent id once per editor', async () => {
    const related = vi.fn(async () => [])
    const load = vi.fn(async (r: { id: string; type: string }) => ent(r.id, r.type))
    const list = createStartingList(
      sources({ related, load, recent: () => [{ id: 'TKT-V', type: 'ticket' }] })
    )
    await list.load(null)
    await list.load(null)
    expect(related).toHaveBeenCalledTimes(1)
    expect(load).toHaveBeenCalledTimes(1)
  })

  it('reuses recently modified entities within the TTL, per scope', async () => {
    let t = 0
    const recentlyModified = vi.fn(async () => [ent('TKT-M')])
    const list = createStartingList(sources({ recentlyModified, now: () => t }))
    await list.load(null)
    await list.load(null)
    expect(recentlyModified).toHaveBeenCalledTimes(1)
    await list.load('bug')
    expect(recentlyModified).toHaveBeenCalledTimes(2)
    t = RECENTLY_MODIFIED_TTL_MS
    await list.load(null)
    expect(recentlyModified).toHaveBeenCalledTimes(3)
  })

  it('keeps the related entities of each face apart', async () => {
    let face = 'draft'
    const related = vi.fn(async () => [ent('TKT-R')])
    const list = createStartingList(
      sources({ self: () => ({ id: 'TKT-SELF', type: 'ticket', face }), related })
    )
    await list.load(null)
    face = 'published'
    await list.load(null)
    expect(related).toHaveBeenCalledTimes(2)
    expect(related).toHaveBeenLastCalledWith({ id: 'TKT-SELF', type: 'ticket', face: 'published' })
  })

  it('retries a source that failed', async () => {
    const related = vi
      .fn<StartingListSources['related']>()
      .mockRejectedValueOnce(new Error('down'))
      .mockResolvedValue([ent('TKT-R')])
    const list = createStartingList(sources({ related }))
    expect(await list.load(null)).toEqual([])
    expect(ids(await list.load(null))).toEqual(['TKT-R'])
  })

  it('still shows the other sources when one fails', async () => {
    const list = createStartingList(
      sources({
        related: async () => {
          throw new Error('down')
        },
        recentlyModified: async () => {
          throw new Error('down')
        },
        recent: () => [{ id: 'TKT-V', type: 'ticket' }],
      })
    )
    expect(ids(await list.load(null))).toEqual(['TKT-V'])
  })
})
