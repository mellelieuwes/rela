import { describe, it, expect, vi } from 'vitest'
import { createMentionMenuMachine, MIN_SEARCH_LEN } from './mentionMenuState'

/** Lets the debounce and an awaited search both land. */
const settle = (): Promise<void> => new Promise((r) => setTimeout(r, 200))

function machineWith(
  search: (q: string, s?: AbortSignal) => Promise<Array<{ id: string }>>,
  abortable = false
) {
  const changes: number[] = []
  const m = createMentionMenuMachine<{ id: string }>({
    search,
    abortable,
    onChange: () => changes.push(1),
  })
  return { m, changes }
}

describe('createMentionMenuMachine', () => {
  it('prompts instead of searching below the minimum query length', async () => {
    const search = vi.fn(async () => [])
    const { m } = machineWith(search)
    m.setQuery('T')
    await settle()
    expect(search).not.toHaveBeenCalled()
    expect(m.state.open).toBe(true)
    expect(m.state.items).toEqual([])
  })

  it('searches once the query is long enough, ranking ID matches first', async () => {
    const { m } = machineWith(async () => [{ id: 'FEAT-TKX' }, { id: 'TKT-ABC' }])
    m.setQuery('TKT')
    await settle()
    // `TKT-ABC` is promoted by the ID tier, and `FEAT-TKX` is DROPPED: the
    // ranking now filters as well as orders. The tier sort it replaced kept
    // every row and only re-ordered them, so a non-matching row survived at the
    // bottom. `FEAT-TKX` has no title here, so its haystack is the bare id and
    // `TKT` does not match it.
    expect(m.state.items.map((i) => i.id)).toEqual(['TKT-ABC'])
  })

  it('keeps a row whose TITLE matches even when its id does not', async () => {
    // The counterpart to the case above: filtering is on the title AND id
    // together, so a title match is not collateral damage of the id tier.
    const { m } = machineWith(async () => [
      { id: 'FEAT-TKX', _title: 'unrelated' },
      { id: 'ZZ-1', _title: 'TKT planning notes' },
    ])
    m.setQuery('TKT')
    await settle()
    expect(m.state.items.map((i) => i.id)).toEqual(['ZZ-1'])
  })

  it('is pending while the search debounces, already when it notifies', async () => {
    let pendingAtChange = false
    const m = createMentionMenuMachine<{ id: string }>({
      search: async () => [],
      onChange: () => (pendingAtChange = m.pending()),
    })
    m.setQuery('TKT')
    expect(pendingAtChange).toBe(true)
    await settle()
    expect(m.pending()).toBe(false)
  })

  it('clears stale results when the query is backspaced below the minimum', async () => {
    const { m } = machineWith(async () => [{ id: 'TKT-ABC' }])
    m.setQuery('TKT')
    await settle()
    expect(m.state.items).toHaveLength(1)
    m.setQuery('T')
    expect(m.state.items).toEqual([])
  })

  it('discards a response for a query the user has moved on from', async () => {
    // The load-bearing staleness defence, because the app bridge cannot cancel.
    let release: (() => void) | null = null
    const gate = new Promise<void>((r) => (release = r))
    const search = vi
      .fn<(q: string) => Promise<Array<{ id: string }>>>()
      .mockImplementationOnce(async () => {
        await gate
        return [{ id: 'STALE-1' }]
      })
      .mockImplementationOnce(async () => [{ id: 'FRESH-1' }])
    const { m } = machineWith(search)
    m.setQuery('ST')
    await new Promise((r) => setTimeout(r, 160))
    m.setQuery('FR')
    release!()
    await settle()
    expect(m.state.items.map((i) => i.id)).toEqual(['FRESH-1'])
  })

  it('cannot be reopened by a response that lands after close', async () => {
    let release: (() => void) | null = null
    const gate = new Promise<void>((r) => (release = r))
    const { m } = machineWith(async () => {
      await gate
      return [{ id: 'LATE-1' }]
    })
    m.setQuery('LA')
    await new Promise((r) => setTimeout(r, 160))
    m.close()
    release!()
    await settle()
    expect(m.state.open).toBe(false)
    expect(m.state.items).toEqual([])
  })

  it('reports a failed search rather than showing an empty list', async () => {
    const { m } = machineWith(async () => {
      throw new Error('nope')
    })
    m.setQuery('TKT')
    await settle()
    expect(m.state.errorMsg).toBe('Search failed')
  })

  it('swallows an abort, which is a cancellation and not a failure', async () => {
    const { m } = machineWith(async () => {
      const err = new Error('aborted')
      err.name = 'AbortError'
      throw err
    }, true)
    m.setQuery('TKT')
    await settle()
    expect(m.state.errorMsg).toBe('')
  })

  it('passes an abort signal only when the transport can use one', async () => {
    const seen: Array<AbortSignal | undefined> = []
    const record = async (_q: string, s?: AbortSignal) => {
      seen.push(s)
      return []
    }
    machineWith(record, true).m.setQuery('AA')
    machineWith(record, false).m.setQuery('BB')
    await settle()
    expect(seen[0]).toBeInstanceOf(AbortSignal)
    expect(seen[1]).toBeUndefined()
  })

  it('wraps the highlight so holding a key never strands it at an end', async () => {
    const { m } = machineWith(async () => [{ id: 'A-1' }, { id: 'A-2' }, { id: 'A-3' }])
    m.setQuery('A-')
    await settle()
    expect(m.current()?.id).toBe('A-1')
    m.moveHighlight(1)
    expect(m.current()?.id).toBe('A-2')
    m.moveHighlight(-2)
    expect(m.current()?.id).toBe('A-3')
  })

  it('refuses a highlight outside the result set', async () => {
    const { m } = machineWith(async () => [{ id: 'A-1' }])
    m.setQuery('A-')
    await settle()
    m.setHighlight(5)
    expect(m.state.highlightedIndex).toBe(0)
  })

  it('stops answering once disposed', async () => {
    const search = vi.fn(async () => [{ id: 'A-1' }])
    const { m } = machineWith(search)
    m.dispose()
    m.setQuery('AA')
    await settle()
    expect(m.state.items).toEqual([])
  })

  it('notifies on every change, so a plain-DOM renderer can repaint', async () => {
    const { m, changes } = machineWith(async () => [{ id: 'A-1' }])
    m.setQuery('AA')
    await settle()
    // At least: the query opening, the search starting, the search resolving.
    expect(changes.length).toBeGreaterThanOrEqual(3)
  })

  it('exposes the minimum query length both editors prompt against', () => {
    expect(MIN_SEARCH_LEN).toBe(2)
  })
})
