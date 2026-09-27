import { describe, it, expect } from 'vitest'
import { parseMentionQuery } from './mentionQuery'

describe('parseMentionQuery', () => {
  it('opens on a bare @ at the start of a paragraph', () => {
    expect(parseMentionQuery('@')).toEqual({ query: '', matchLength: 1 })
  })

  it('opens on @ after a space and tracks the query', () => {
    expect(parseMentionQuery('see @TKT')).toEqual({ query: 'TKT', matchLength: 4 })
  })

  it('reports the match length so the trigger and query are replaced together', () => {
    const m = parseMentionQuery('x @abcd')
    expect(m?.matchLength).toBe(5)
  })

  it('closes when a space is typed after the trigger', () => {
    expect(parseMentionQuery('@TKT ')).toBeNull()
    expect(parseMentionQuery('@ ')).toBeNull()
  })

  it('closes inside a code span', () => {
    expect(parseMentionQuery('`@TKT')).toBeNull()
  })

  it('fires after punctuation that is not part of a word', () => {
    for (const before of ['“', '/', '(', '-']) {
      expect(parseMentionQuery(`${before}@tk`)).toEqual({ query: 'tk', matchLength: 3 })
    }
  })

  it('does not fire on an email address', () => {
    expect(parseMentionQuery('mail jeroen@example')).toBeNull()
    expect(parseMentionQuery('a@b')).toBeNull()
  })

  it('fires after an opening bracket or paren', () => {
    expect(parseMentionQuery('(@TKT')).toEqual({ query: 'TKT', matchLength: 4 })
    expect(parseMentionQuery('[@TKT')).toEqual({ query: 'TKT', matchLength: 4 })
  })

  it('tracks the most recent @ when there are several', () => {
    expect(parseMentionQuery('@one and @two')).toEqual({ query: 'two', matchLength: 4 })
  })

  it('returns null when there is no @', () => {
    expect(parseMentionQuery('plain text')).toBeNull()
  })

  it('returns null for undefined, which is what getContent gives outside a paragraph', () => {
    expect(parseMentionQuery(undefined)).toBeNull()
    expect(parseMentionQuery('')).toBeNull()
  })

  it('gives up on an over-long query rather than searching for it', () => {
    expect(parseMentionQuery('@' + 'x'.repeat(65))).toBeNull()
    expect(parseMentionQuery('@' + 'x'.repeat(64))).not.toBeNull()
  })
})
