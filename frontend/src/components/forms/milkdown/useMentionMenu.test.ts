import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { useMentionMenu, type MentionTransport } from './useMentionMenu'
import type { MentionTypeInfo } from './mentionPlan'
import type { Entity } from '@/types'

vi.mock('@/api', () => ({
  searchEntities: vi.fn(),
  getEntity: vi.fn(),
  listRecentlyModified: vi.fn(),
}))

/**
 * Builds a candidate in the shape `/_search` returns: the display name is
 * `_title` (see `entityDisplayTitle`), not `properties.title`.
 */
function ent(id: string, title?: string): Entity {
  return { id, type: 'ticket', _title: title ?? id, properties: {} } as unknown as Entity
}

const TYPES: MentionTypeInfo[] = [
  { name: 'ticket', prefixes: ['TKT-'] },
  { name: 'research', prefixes: ['RES-'] },
  { name: 'review-checklist', prefixes: ['REV-'] },
  { name: 'review-response', prefixes: ['RR-'] },
  { name: 'decision', prefixes: ['DEC-'] },
]

/** Advances past the debounce and lets pending promises settle. */
async function settle() {
  await vi.advanceTimersByTimeAsync(200)
  await Promise.resolve()
}

function setup(starting: Entity[] = [ent('TKT-START')]) {
  // The title echoes the query so the client-side ranker keeps the hit.
  const search = vi.fn<MentionTransport['search']>(async (text) => [ent('TKT-ABC', text)])
  const load = vi.fn(async (_type: string | null) => starting)
  const m = useMentionMenu({ search, startingList: { load } })
  m.setAvailableTypes(TYPES)
  return { m, search, load }
}

describe('useMentionMenu', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('starts closed', () => {
    const { m } = setup()
    expect(m.state.open).toBe(false)
    expect(m.state.items).toEqual([])
  })

  describe('stages', () => {
    it('shows the starting list for a bare @, without searching', async () => {
      const { m, search, load } = setup()
      m.setQuery('')
      await settle()
      expect(load).toHaveBeenCalledWith(null)
      expect(search).not.toHaveBeenCalled()
      expect(m.state.items.map((e) => e.id)).toEqual(['TKT-START'])
      expect(m.state.starting).toBe(true)
      expect(m.state.typeItems).toEqual([])
    })

    it('offers only types for one letter', async () => {
      const { m, search } = setup()
      m.setQuery('r')
      await settle()
      expect(search).not.toHaveBeenCalled()
      expect(m.state.items).toEqual([])
      expect(m.state.typeItems).toEqual(['research', 'review-checklist', 'review-response'])
      expect(m.state.searches).toBe(false)
    })

    it('searches two letters when no type matches', async () => {
      const { m, search } = setup()
      m.setQuery('xy')
      await settle()
      expect(search).toHaveBeenCalledWith('xy', undefined, expect.anything())
      expect(m.state.typeItems).toEqual([])
    })

    it('shows types first, then search results, at three letters', async () => {
      const { m, search } = setup()
      m.setQuery('rev')
      await settle()
      expect(search).toHaveBeenCalledWith('rev', undefined, expect.anything())
      expect(m.state.typesFirst).toBe(true)
      expect(m.current()).toEqual({ kind: 'type', name: 'review-checklist' })
    })

    it('puts search results first from four letters', async () => {
      const { m } = setup()
      m.setQuery('revi')
      await settle()
      expect(m.state.typesFirst).toBe(false)
      expect(m.current()).toMatchObject({ kind: 'entity', entity: { id: 'TKT-ABC' } })
    })

    it('sends the query unmodified, so backend ID ranking survives', async () => {
      const { m, search } = setup()
      m.setQuery('fancy-report')
      await settle()
      expect(search).toHaveBeenCalledWith('fancy-report', undefined, expect.anything())
    })

    it('debounces so typing does not fire a request per keystroke', async () => {
      const { m, search } = setup()
      m.setQuery('abc')
      m.setQuery('abcd')
      m.setQuery('abcde')
      await settle()
      expect(search).toHaveBeenCalledTimes(1)
      expect(search).toHaveBeenCalledWith('abcde', undefined, expect.anything())
    })
  })

  describe('scope', () => {
    it('scopes to `name:` and shows that type’s starting list', async () => {
      const { m, load, search } = setup()
      m.setQuery('ticket:')
      await settle()
      expect(m.state.scopeType).toBe('ticket')
      expect(load).toHaveBeenCalledWith('ticket')
      expect(search).not.toHaveBeenCalled()
    })

    it('searches the text after the colon within the type, from one character', async () => {
      const { m, search } = setup()
      m.setQuery('ticket:f')
      await settle()
      expect(search).toHaveBeenCalledWith('f', 'ticket', expect.anything())
    })

    it('scopes by a typed ID prefix and searches the whole ID', async () => {
      const { m, search } = setup()
      m.setQuery('TKT-12')
      await settle()
      expect(m.state.scopeType).toBe('ticket')
      expect(search).toHaveBeenCalledWith('TKT-12', 'ticket', expect.anything())
    })

    it('treats an unknown `foo:` as an ordinary query', async () => {
      const { m, search } = setup()
      m.setQuery('foo:bar')
      await settle()
      expect(m.state.scopeType).toBeNull()
      expect(search).toHaveBeenCalledWith('foo:bar', undefined, expect.anything())
    })

    it('unscopes when the colon is deleted', async () => {
      const { m } = setup()
      m.setQuery('ticket:')
      await settle()
      m.setQuery('ticket')
      expect(m.state.scopeType).toBeNull()
    })

    it('drops rows from the previous scope at once, before the debounce', async () => {
      const { m } = setup()
      m.setQuery('abcd')
      await settle()
      expect(m.state.items).toHaveLength(1)
      m.setQuery('ticket:')
      expect(m.state.items).toEqual([])
      expect(m.current()).toBeNull()
    })

    it('reports the scope for a known type name only', () => {
      const { m } = setup()
      expect(m.scopeTypeFor('ticket')).toBe('ticket')
      expect(m.scopeTypeFor('Ticket')).toBe('ticket')
      expect(m.scopeTypeFor('nope')).toBeNull()
    })
  })

  describe('async safety', () => {
    it('ignores a slow response for a superseded query', async () => {
      const { m, search } = setup()
      let resolveSlow: (v: Entity[]) => void = () => {}
      search.mockImplementationOnce(() => new Promise((r) => (resolveSlow = r)))
      m.setQuery('slow')
      await vi.advanceTimersByTimeAsync(200)
      search.mockResolvedValueOnce([ent('TKT-FAST', 'fast')])
      m.setQuery('fast')
      await settle()
      resolveSlow([ent('TKT-SLOW', 'slow')])
      await settle()
      expect(m.state.items.map((e) => e.id)).toEqual(['TKT-FAST'])
    })

    it('does not reopen after close when a response lands late', async () => {
      const { m, search } = setup()
      let resolve: (v: Entity[]) => void = () => {}
      search.mockImplementationOnce(() => new Promise((r) => (resolve = r)))
      m.setQuery('abcd')
      await vi.advanceTimersByTimeAsync(200)
      m.close()
      resolve([ent('TKT-LATE')])
      await settle()
      expect(m.state.open).toBe(false)
      expect(m.state.items).toEqual([])
    })

    it('reports a search failure without leaving stale results', async () => {
      const { m, search } = setup()
      m.setQuery('abcd')
      await settle()
      search.mockRejectedValueOnce(new Error('boom'))
      m.setQuery('abcde')
      await settle()
      expect(m.state.items).toEqual([])
      expect(m.state.errorMsg).not.toBe('')
    })

    it('stops updating after dispose', async () => {
      const { m } = setup()
      m.setQuery('abcd')
      m.dispose()
      await settle()
      expect(m.state.items).toEqual([])
    })
  })

  describe('pending and settling', () => {
    it('is pending while the search debounces or runs', async () => {
      const { m } = setup()
      m.setQuery('abcd')
      expect(m.pending()).toBe(true)
      await settle()
      expect(m.pending()).toBe(false)
    })

    it('runs a waiting commit with the top result once the search lands', async () => {
      const { m } = setup()
      m.setQuery('abcd')
      const cb = vi.fn()
      m.whenSettled(cb)
      expect(cb).not.toHaveBeenCalled()
      await settle()
      expect(cb).toHaveBeenCalledWith({
        kind: 'entity',
        entity: expect.objectContaining({ id: 'TKT-ABC' }),
      })
    })

    it('does not restart the search for an unchanged query', async () => {
      const { m, search } = setup()
      m.setQuery('abcd')
      await vi.advanceTimersByTimeAsync(100)
      m.setQuery('abcd')
      await vi.advanceTimersByTimeAsync(60)
      expect(search).toHaveBeenCalledTimes(1)
    })

    it('drops a waiting commit when the menu closes', async () => {
      const { m } = setup()
      m.setQuery('abcd')
      const cb = vi.fn()
      m.whenSettled(cb)
      m.close()
      await settle()
      expect(cb).not.toHaveBeenCalled()
    })

    it('commits nothing when the awaited search fails', async () => {
      const { m, search } = setup()
      search.mockRejectedValueOnce(new Error('boom'))
      m.setQuery('zzzz')
      const cb = vi.fn()
      m.whenSettled(cb)
      await settle()
      expect(cb).not.toHaveBeenCalled()
      expect(m.pending()).toBe(false)
    })

    it('drops a waiting commit when the query changes', async () => {
      const { m } = setup()
      m.setQuery('abcd')
      const cb = vi.fn()
      m.whenSettled(cb)
      m.setQuery('abcde')
      await settle()
      expect(cb).not.toHaveBeenCalled()
    })

    it('reports a search that settled with nothing to show', async () => {
      const { m, search } = setup()
      search.mockResolvedValueOnce([])
      m.setQuery('zzzz')
      expect(m.settledEmpty()).toBe(false)
      await settle()
      expect(m.settledEmpty()).toBe(true)
    })

    it('does not count a types-only query as empty', async () => {
      const { m } = setup()
      m.setQuery('r')
      await settle()
      expect(m.settledEmpty()).toBe(false)
    })
  })

  describe('the highlight is an identity', () => {
    it('keeps the same type highlighted when the rows re-rank', async () => {
      const { m } = setup()
      m.setQuery('r')
      m.setHighlight(2)
      expect(m.current()).toEqual({ kind: 'type', name: 'review-response' })
      m.setQuery('re')
      expect(m.current()).toEqual({ kind: 'type', name: 'review-response' })
    })

    it('falls back to the first row when the highlighted one is gone', async () => {
      const { m } = setup()
      m.setQuery('r')
      m.setHighlight(1)
      m.setQuery('res')
      await settle()
      expect(m.highlightedIndex()).toBe(0)
    })

    it('wraps across both sections in display order', async () => {
      const { m } = setup()
      m.setQuery('rev')
      await settle()
      // Two types, then one entity.
      m.moveHighlight(-1)
      expect(m.current()).toMatchObject({ kind: 'entity', entity: { id: 'TKT-ABC' } })
      m.moveHighlight(1)
      expect(m.current()).toEqual({ kind: 'type', name: 'review-checklist' })
    })

    it('ignores an index past the combined length', async () => {
      const { m } = setup()
      m.setQuery('r')
      m.setHighlight(1)
      m.setHighlight(99)
      expect(m.highlightedIndex()).toBe(1)
    })

    it('has nothing to pick when there are no rows', () => {
      const { m } = setup()
      m.setQuery('q')
      expect(m.highlightedIndex()).toBe(-1)
      expect(m.current()).toBeNull()
    })
  })

  it('does not repopulate the rows behind a closed menu on a schema reload', () => {
    const { m } = setup()
    m.setQuery('r')
    m.close()
    m.setAvailableTypes([...TYPES])
    expect(m.state.typeItems).toEqual([])
  })
})
