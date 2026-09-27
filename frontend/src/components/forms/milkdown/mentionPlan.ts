/**
 * What the `@` menu shows for a given query.
 *
 * The rules come from the agreed behaviour spec (TKT-39TIB4). They are kept
 * here, as one pure function over the query and the schema's types, so every
 * stage can be tested without an editor, a network or Vue:
 *
 * | Query                  | Shows                                               |
 * | ---------------------- | --------------------------------------------------- |
 * | `@`                    | the starting list                                   |
 * | `@t`, `@ti`            | matching types; with none, `@ti` searches instead   |
 * | `@tic`                 | matching types, then search results                 |
 * | `@tick` and longer     | search results, then matching types                 |
 * | `@ticket:` / `@TKT-`   | scoped to ticket: the starting list, or a search    |
 *
 * # The scope is document text
 *
 * A chosen type is written into the document as `@ticket:`, and a known ID
 * prefix (`@TKT-`) scopes the same way. The menu keeps no scope of its own:
 * everything here is derived from the query on every keystroke, so undo,
 * Backspace and cursor moves cannot leave a scope behind that the text no
 * longer shows. Backspacing the `:` of `@ticket:` unscopes by construction.
 */

/** One entity type as the menu needs it. */
export interface MentionTypeInfo {
  name: string
  /** ID prefixes declared for the type, e.g. `TKT-`. May be empty. */
  prefixes: string[]
}

/** The menu's plan for one query. */
export interface MentionPlan {
  /** The type the query is scoped to, or null for every type. */
  scopeType: string | null
  /** Type rows to offer, best first. Always empty when scoped. */
  typeRows: string[]
  /**
   * What to search for, or null when this stage does not search.
   *
   * For a prefix scope (`@TKT-6M`) this is the whole query, since the user is
   * typing an ID. For a name scope (`@ticket:fa`) it is the text after the `:`.
   */
  search: string | null
  /** True when the starting list stands in for a search. */
  starting: boolean
  /** True when type rows come before entity rows. */
  typesFirst: boolean
}

/** Queries shorter than this show only types. */
const TYPES_ONLY_BELOW = 3
/** From this length on, entities come first. */
const ENTITIES_FIRST_FROM = 4
/** Type rows shown beside search results. */
const MAX_TYPE_ROWS_WITH_SEARCH = 3
/** Type rows shown while only types are offered. */
const MAX_TYPE_ROWS = 8

/** A prefix without its trailing separator, lowercased: `TKT-` → `tkt`. */
function bare(prefix: string): string {
  return prefix.replace(/[-_]+$/, '').toLowerCase()
}

/**
 * Types whose name or ID prefix starts with `query`, best first.
 *
 * An exact match on the name or a prefix ranks first, then the schema order.
 */
export function matchTypes(types: readonly MentionTypeInfo[], query: string): string[] {
  const q = query.toLowerCase()
  if (q === '') return types.map((t) => t.name)
  const exact: string[] = []
  const partial: string[] = []
  for (const t of types) {
    const keys = [t.name.toLowerCase(), ...t.prefixes.map(bare).filter((p) => p !== '')]
    if (keys.some((k) => k === q)) exact.push(t.name)
    else if (keys.some((k) => k.startsWith(q))) partial.push(t.name)
  }
  return [...exact, ...partial]
}

/** The scope a query names, and what remains to search for. */
export interface ResolvedScope {
  scopeType: string | null
  /** The search text: the query without a `name:` scope, or the whole query. */
  search: string
}

/**
 * Reads a scope out of the query.
 *
 * `ticket:fa` scopes by type name (case-insensitive) and searches `fa`.
 * `TKT-6M` scopes by ID prefix and searches `TKT-6M`, since the prefix is part
 * of the ID being typed. When a query matches several prefixes the longest
 * wins, so `TKT-` beats `T-`. `foo:` with no type called `foo` is an ordinary
 * query.
 */
export function resolveScope(types: readonly MentionTypeInfo[], query: string): ResolvedScope {
  const colon = query.indexOf(':')
  if (colon > 0) {
    const name = query.slice(0, colon).toLowerCase()
    const hit = types.find((t) => t.name.toLowerCase() === name)
    if (hit) return { scopeType: hit.name, search: query.slice(colon + 1) }
  }

  const upper = query.toUpperCase()
  let best: { type: string; prefix: string } | null = null
  for (const t of types) {
    for (const prefix of t.prefixes) {
      // A prefix only scopes once its separator is typed: `tkt` is still a
      // search for the word, `TKT-` is the start of an ID.
      if (!/[-_]$/.test(prefix)) continue
      if (!upper.startsWith(prefix.toUpperCase())) continue
      if (best === null || prefix.length > best.prefix.length) best = { type: t.name, prefix }
    }
  }
  if (best) {
    const rest = query.slice(best.prefix.length)
    return { scopeType: best.type, search: rest === '' ? '' : query }
  }
  return { scopeType: null, search: query }
}

/** Builds the menu's plan for `query`. */
export function planMention(types: readonly MentionTypeInfo[], query: string): MentionPlan {
  const { scopeType, search } = resolveScope(types, query)

  if (scopeType !== null) {
    return {
      scopeType,
      typeRows: [],
      search: search === '' ? null : search,
      starting: search === '',
      typesFirst: false,
    }
  }

  if (query === '') {
    return { scopeType: null, typeRows: [], search: null, starting: true, typesFirst: false }
  }

  const matched = matchTypes(types, query)

  if (query.length < TYPES_ONLY_BELOW) {
    // Two letters with no type to offer are a search after all: an empty menu
    // would leave the user nothing to act on.
    const fallBackToSearch = matched.length === 0 && query.length === TYPES_ONLY_BELOW - 1
    return {
      scopeType: null,
      typeRows: matched.slice(0, MAX_TYPE_ROWS),
      search: fallBackToSearch ? query : null,
      starting: false,
      typesFirst: true,
    }
  }

  return {
    scopeType: null,
    typeRows: matched.slice(0, MAX_TYPE_ROWS_WITH_SEARCH),
    search: query,
    starting: false,
    typesFirst: query.length < ENTITIES_FIRST_FROM && matched.length > 0,
  }
}

/**
 * The query text that stands for a chosen type: `ticket:`.
 *
 * Choosing a type REPLACES the characters typed to find it, so `@ti` becomes
 * `@ticket:` with nothing left to search.
 */
export function scopeText(typeName: string): string {
  return `${typeName}:`
}
