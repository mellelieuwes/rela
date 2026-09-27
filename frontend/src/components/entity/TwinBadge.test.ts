import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import TwinBadge from './TwinBadge.vue'
import type { Twin, TwinState } from '@/api/twins'

// TwinBadge is a read-out of one twin: which system, how the sync stands
// (the state class carries the colour), where the external item lives, and
// when it last synced.

function twin(over: Partial<Twin> = {}): Twin {
  return {
    system: 'basecamp',
    external_id: '123',
    url: 'https://app.basecamp.com/1/todos/123',
    state: 'in_sync',
    has_base: true,
    synced_at: '2026-09-01T10:00:00Z',
    owned_fields: ['status'],
    findings: [],
    ...over,
  }
}

describe('TwinBadge', () => {
  it.each<[TwinState, string]>([
    ['in_sync', 'in sync'],
    ['pending', 'pending'],
    ['conflict', 'conflict'],
    ['gone', 'gone'],
  ])('renders %s with its own state class and label', (state, label) => {
    const w = mount(TwinBadge, { props: { twin: twin({ state }) } })
    const badge = w.find('.twin-badge')
    expect(badge.classes()).toContain(`is-${state}`)
    // Exactly one state class, so two states can never share a colour rule.
    expect(badge.classes().filter((c) => c.startsWith('is-'))).toEqual([`is-${state}`])
    expect(w.find('.twin-badge__system').text()).toBe('basecamp')
    expect(w.find('.twin-badge__state').text()).toBe(label)
  })

  it('links to the external item in a new tab without an opener', () => {
    const a = mount(TwinBadge, { props: { twin: twin() } }).find('a.twin-badge')
    expect(a.attributes('href')).toBe('https://app.basecamp.com/1/todos/123')
    expect(a.attributes('target')).toBe('_blank')
    expect(a.attributes('rel')).toBe('noopener')
  })

  it('does not put a non-http url in an href', () => {
    const w = mount(TwinBadge, { props: { twin: twin({ url: 'javascript:alert(1)' }) } })
    expect(w.find('a').exists()).toBe(false)
    expect(w.find('.twin-badge').attributes('href')).toBeUndefined()
    expect(w.find('.twin-badge__system').text()).toBe('basecamp')
  })

  it('names the last sync time in the tooltip, and a never-synced twin as such', () => {
    const synced = mount(TwinBadge, { props: { twin: twin() } }).find('.twin-badge')
    expect(synced.attributes('title')).toContain(
      `last synced ${new Date('2026-09-01T10:00:00Z').toLocaleString()}`
    )
    const fresh = mount(TwinBadge, {
      props: { twin: twin({ state: 'pending', has_base: false, synced_at: undefined }) },
    }).find('.twin-badge')
    expect(fresh.attributes('title')).toContain('never synced')
  })
})
