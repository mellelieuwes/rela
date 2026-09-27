import { describe, it, expect } from 'vitest'
import {
  matchTypes,
  planMention,
  resolveScope,
  scopeText,
  type MentionTypeInfo,
} from './mentionPlan'

const TYPES: MentionTypeInfo[] = [
  { name: 'ticket', prefixes: ['TKT-'] },
  { name: 'test-case', prefixes: ['TC-'] },
  { name: 'test-suite', prefixes: ['TS-'] },
  { name: 'bug', prefixes: ['BUG-'] },
  { name: 'concept', prefixes: [] },
  { name: 'feature', prefixes: ['FEAT-'] },
]

describe('matchTypes', () => {
  it.each([
    ['t', ['ticket', 'test-case', 'test-suite']],
    ['ti', ['ticket']],
    ['tkt', ['ticket']],
    ['tc', ['test-case']],
    ['bug', ['bug']],
    ['con', ['concept']],
    ['xq', []],
  ])('%s → %j', (query, want) => {
    expect(matchTypes(TYPES, query)).toEqual(want)
  })

  it('ranks an exact name or prefix match first', () => {
    const types = [
      { name: 'tests', prefixes: [] },
      { name: 'test', prefixes: [] },
    ]
    expect(matchTypes(types, 'test')).toEqual(['test', 'tests'])
  })

  it('is case-insensitive', () => {
    expect(matchTypes(TYPES, 'TI')).toEqual(['ticket'])
  })
})

describe('resolveScope', () => {
  it.each([
    ['ticket:', 'ticket', ''],
    ['ticket:fa', 'ticket', 'fa'],
    ['Ticket:fa', 'ticket', 'fa'],
    ['TKT-', 'ticket', ''],
    ['tkt-', 'ticket', ''],
    ['TKT-6MZ', 'ticket', 'TKT-6MZ'],
    ['BUG-fix', 'bug', 'BUG-fix'],
    ['tkt', null, 'tkt'],
    ['foo:bar', null, 'foo:bar'],
    ['fancy-rep', null, 'fancy-rep'],
    ['', null, ''],
  ])('%s → scope %s, search %s', (query, scopeType, search) => {
    expect(resolveScope(TYPES, query)).toEqual({ scopeType, search })
  })

  it('prefers the longest matching prefix', () => {
    const types = [
      { name: 'short', prefixes: ['T-'] },
      { name: 'long', prefixes: ['T-X-'] },
    ]
    expect(resolveScope(types, 'T-X-1').scopeType).toBe('long')
    expect(resolveScope(types, 'T-1').scopeType).toBe('short')
  })
})

describe('planMention', () => {
  it('shows the starting list for a bare @ (spec 1.1)', () => {
    expect(planMention(TYPES, '')).toEqual({
      scopeType: null,
      typeRows: [],
      search: null,
      starting: true,
      typesFirst: false,
    })
  })

  it('shows only matching types for one letter (spec 3.1)', () => {
    const plan = planMention(TYPES, 't')
    expect(plan.typeRows).toEqual(['ticket', 'test-case', 'test-suite'])
    expect(plan.search).toBeNull()
    expect(plan.typesFirst).toBe(true)
  })

  it('shows only matching types for two letters (spec 3.2)', () => {
    const plan = planMention(TYPES, 'ti')
    expect(plan.typeRows).toEqual(['ticket'])
    expect(plan.search).toBeNull()
  })

  it('searches two letters that match no type (spec 3.3)', () => {
    const plan = planMention(TYPES, 'xq')
    expect(plan.typeRows).toEqual([])
    expect(plan.search).toBe('xq')
  })

  it('offers nothing for one letter that matches no type', () => {
    const plan = planMention(TYPES, 'x')
    expect(plan.typeRows).toEqual([])
    expect(plan.search).toBeNull()
    expect(plan.starting).toBe(false)
  })

  it('shows types then search results at three letters (spec 3.4)', () => {
    const plan = planMention(TYPES, 'tic')
    expect(plan.typeRows).toEqual(['ticket'])
    expect(plan.search).toBe('tic')
    expect(plan.typesFirst).toBe(true)
  })

  it('searches only at three letters with no type match (spec 3.5)', () => {
    const plan = planMention(TYPES, 'sso')
    expect(plan.typeRows).toEqual([])
    expect(plan.search).toBe('sso')
    expect(plan.typesFirst).toBe(false)
  })

  it('puts search results first from four letters (spec 3.6)', () => {
    const plan = planMention(TYPES, 'tick')
    expect(plan.typeRows).toEqual(['ticket'])
    expect(plan.search).toBe('tick')
    expect(plan.typesFirst).toBe(false)
  })

  it('caps type rows beside a search', () => {
    const many = Array.from({ length: 10 }, (_, i) => ({ name: `abc${i}`, prefixes: [] }))
    expect(planMention(many, 'abc').typeRows).toHaveLength(3)
  })

  it('shows the type starting list for an empty scope (spec 4.3)', () => {
    expect(planMention(TYPES, 'ticket:')).toEqual({
      scopeType: 'ticket',
      typeRows: [],
      search: null,
      starting: true,
      typesFirst: false,
    })
  })

  it('searches within the scope from one character (spec 4.4)', () => {
    const plan = planMention(TYPES, 'ticket:f')
    expect(plan.scopeType).toBe('ticket')
    expect(plan.search).toBe('f')
    expect(plan.starting).toBe(false)
  })

  it('scopes by a typed ID prefix (spec 4.5, 4.6)', () => {
    expect(planMention(TYPES, 'TKT-').starting).toBe(true)
    const plan = planMention(TYPES, 'TKT-6MZ')
    expect(plan.scopeType).toBe('ticket')
    expect(plan.search).toBe('TKT-6MZ')
  })

  it('treats an unknown type name as an ordinary query (spec 4.8)', () => {
    const plan = planMention(TYPES, 'foo:')
    expect(plan.scopeType).toBeNull()
    expect(plan.search).toBe('foo:')
  })

  it('unscopes when the colon is deleted (spec 4.7)', () => {
    const plan = planMention(TYPES, 'ticket')
    expect(plan.scopeType).toBeNull()
    expect(plan.typeRows).toEqual(['ticket'])
  })
})

describe('scopeText', () => {
  it('writes the type as a name scope', () => {
    expect(scopeText('ticket')).toBe('ticket:')
    expect(resolveScope(TYPES, scopeText('ticket'))).toEqual({ scopeType: 'ticket', search: '' })
  })
})
