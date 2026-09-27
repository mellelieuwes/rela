import { describe, it, expect, beforeEach } from 'vitest'
import { listRecentEntities, recordRecentEntity } from './recentEntities'

describe('recentEntities', () => {
  beforeEach(() => localStorage.clear())

  it('starts empty', () => {
    expect(listRecentEntities()).toEqual([])
  })

  it('lists newest first and moves a repeat to the front', () => {
    recordRecentEntity('TKT-1', 'ticket')
    recordRecentEntity('BUG-1', 'bug')
    recordRecentEntity('TKT-1', 'ticket')
    expect(listRecentEntities()).toEqual([
      { id: 'TKT-1', type: 'ticket' },
      { id: 'BUG-1', type: 'bug' },
    ])
  })

  it('keeps at most 30 entries', () => {
    for (let i = 0; i < 40; i++) recordRecentEntity(`TKT-${i}`, 'ticket')
    const list = listRecentEntities()
    expect(list).toHaveLength(30)
    expect(list[0].id).toBe('TKT-39')
  })

  it('ignores an invalid id or an empty type', () => {
    recordRecentEntity('bad id', 'ticket')
    recordRecentEntity('TKT-1', '')
    expect(listRecentEntities()).toEqual([])
  })

  it.each([
    ['not json', '{'],
    ['not an array', '{"id":"TKT-1"}'],
    ['bad rows', '[null, 3, {"id":"x y","type":"t"}, {"id":"TKT-1","type":"ticket"}]'],
  ])('tolerates a stored value that is %s', (_name, value) => {
    localStorage.setItem('rela.recentEntities', value)
    const list = listRecentEntities()
    expect(list.every((e) => e.id === 'TKT-1')).toBe(true)
  })
})
