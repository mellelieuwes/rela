import { describe, it, expect, vi } from 'vitest'
import { createMentionTrigger, NO_MATCH_GRACE } from './mentionTrigger'

function setup() {
  let open = false
  const host = {
    isOpen: () => open,
    close: vi.fn(() => (open = false)),
    setQuery: vi.fn(() => (open = true)),
  }
  return { trigger: createMentionTrigger(host), host }
}

describe('createMentionTrigger', () => {
  it('shows for an armed query', () => {
    const { trigger, host } = setup()
    expect(trigger.shouldShow('see @tk', true)).toBe(true)
    expect(host.setQuery).toHaveBeenCalledWith('tk')
  })

  it('never shows for an @ that was not typed', () => {
    const { trigger, host } = setup()
    expect(trigger.shouldShow('see @tk', false)).toBe(false)
    expect(host.setQuery).not.toHaveBeenCalled()
  })

  it('closes when the query ends', () => {
    const { trigger, host } = setup()
    trigger.shouldShow('see @tk', true)
    expect(trigger.shouldShow('see @tk ', true)).toBe(false)
    expect(host.close).toHaveBeenCalled()
  })

  it('stays shut after Escape until the query is edited', () => {
    const { trigger } = setup()
    trigger.shouldShow('see @tk', true)
    trigger.dismiss('tk')
    expect(trigger.shouldShow('see @tk', true)).toBe(false)
    expect(trigger.shouldShow('see @tkt', true)).toBe(true)
  })

  it(`closes ${NO_MATCH_GRACE} characters past a query that matched nothing`, () => {
    const { trigger } = setup()
    trigger.shouldShow('@xyz', true)
    trigger.markNoMatch('xyz')
    expect(trigger.shouldShow('@xyza', true)).toBe(true)
    expect(trigger.shouldShow('@xyzab', true)).toBe(true)
    expect(trigger.shouldShow('@xyzabc', true)).toBe(false)
  })

  it('reopens when the query is edited back below the unmatched one', () => {
    const { trigger } = setup()
    trigger.shouldShow('@xyz', true)
    trigger.markNoMatch('xyz')
    trigger.shouldShow('@xyzabc', true)
    expect(trigger.shouldShow('@xy', true)).toBe(true)
    expect(trigger.shouldShow('@xyzabc', true)).toBe(true)
  })

  it('ends the grace when a later search finds rows', () => {
    const { trigger } = setup()
    trigger.shouldShow('@ticket:', true)
    trigger.markNoMatch('ticket:')
    trigger.markMatch()
    expect(trigger.shouldShow('@ticket:abc', true)).toBe(true)
  })

  it('keeps the shortest unmatched query as the base', () => {
    const { trigger } = setup()
    trigger.markNoMatch('xyz')
    trigger.markNoMatch('xyza')
    trigger.shouldShow('@xyza', true)
    expect(trigger.shouldShow('@xyzabc', true)).toBe(false)
  })
})
