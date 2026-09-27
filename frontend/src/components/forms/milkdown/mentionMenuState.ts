/**
 * The `@` completion menu's state machine, shared by both editors.
 *
 * The SPA form and the sandboxed app editor run the same menu: same trigger
 * length, same debounce, same result cap, same staleness rule. They differ only
 * in how they FETCH (axios with an `AbortController` vs the app bridge, which
 * offers no cancellation) and in how they RENDER (Vue vs plain DOM). Both of
 * those are injected, so neither can pull a framework in here.
 *
 * It lives in one place because the first version did not. The app editor
 * started as a hand-written copy of this logic, and a copy of a state machine
 * is worse than a copy of a stylesheet: the two menus could quietly disagree
 * about when to search and how a slow response is resolved, and nothing would
 * say so. That is the same defect (RR-9PTXV0) that TKT-D2JML7 was written to
 * remove, so shipping a smaller instance of it was not an option.
 */
import { rankMentions } from './rankMentions'

/**
 * Below this length the app editor's menu prompts rather than searching.
 *
 * Searching for one character across every type returns relevance-scored
 * noise. The SPA menu replaces this rule with its own stages through
 * `MentionMenuOptions.shouldSearch`.
 */
export const MIN_SEARCH_LEN = 2
const SEARCH_DEBOUNCE_MS = 150
const MAX_RESULTS = 20

/** The menu's observable state. Plain data, so any renderer can read it. */
export interface MentionMenuSnapshot<T> {
  /** True while the menu should be on screen. */
  open: boolean
  /** The query text after the `@`. */
  query: string
  items: T[]
  highlightedIndex: number
  /** True while a search request is in flight (not during the debounce). */
  loading: boolean
  errorMsg: string
}

/**
 * Fetches candidates for a query.
 *
 * `signal` is passed when the caller's transport can cancel. The app bridge
 * cannot, so it ignores the argument and relies on the generation guard below,
 * which is why cancellation is an optimization here and never the correctness
 * mechanism.
 *
 * Nil: a rejection other than an abort is reported to the user as a failed
 * search; an abort is swallowed.
 */
export type MentionSearchFn<T> = (query: string, signal?: AbortSignal) => Promise<T[]>

export interface MentionMenuMachine<T> {
  readonly state: Readonly<MentionMenuSnapshot<T>>
  /** The shortest query that triggers a search; below it the menu prompts. */
  readonly minQueryLength: number
  /**
   * Opens the menu, or updates the query if it is already open.
   *
   * With `reset`, the rows on screen are dropped at once instead of staying
   * until the new search lands. Pass it when the new query asks a different
   * question (another scope, or the starting list), where the old rows would
   * be wrong rather than merely stale.
   */
  setQuery(query: string, reset?: boolean): void
  /** True while a search is debouncing or in flight. */
  pending(): boolean
  close(): void
  moveHighlight(delta: number): void
  setHighlight(index: number): void
  /** The item under the highlight, or null when there is nothing to pick. */
  current(): T | null
  dispose(): void
}

export interface MentionMenuOptions<T> {
  search: MentionSearchFn<T>
  /**
   * Whether a query is searched at all. Below it the menu shows no rows.
   *
   * Defaults to "at least `MIN_SEARCH_LEN` characters", which is the app
   * editor's rule. The SPA menu decides per stage (`mentionPlan.ts`): an empty
   * query loads the starting list, and one or two letters show only types.
   */
  shouldSearch?: (query: string) => boolean
  /**
   * Orders a response for display, best first.
   *
   * Defaults to `rankMentions` against the query. The SPA passes the text it
   * actually searched for, which differs from the query under a scope such
   * as `ticket:fa`, and keeps the starting list in its own order.
   */
  rank?: (items: T[], query: string) => T[]
  /** Called after every state change, so the renderer can repaint. */
  onChange?: () => void
  /**
   * Whether the transport supports cancellation.
   *
   * When true an `AbortController` is created and its signal passed to
   * `search`, and an `AbortError` rejection is ignored rather than reported.
   */
  abortable?: boolean
}

/**
 * Builds the menu's state machine.
 *
 * The returned `state` object is MUTATED in place rather than replaced, so a
 * Vue caller can wrap it in `reactive()` and a plain-DOM caller can read it
 * directly after `onChange`.
 */
export function createMentionMenuMachine<T extends { id?: string }>(
  options: MentionMenuOptions<T>
): MentionMenuMachine<T> {
  const state: MentionMenuSnapshot<T> = {
    open: false,
    query: '',
    items: [],
    highlightedIndex: 0,
    loading: false,
    errorMsg: '',
  }

  const shouldSearch = options.shouldSearch ?? ((q: string) => q.length >= MIN_SEARCH_LEN)
  const rank = options.rank ?? rankMentions

  let searchTimer: ReturnType<typeof setTimeout> | null = null
  let abort: AbortController | null = null
  let disposed = false
  // Guards against a slow response for an old query landing after a newer one.
  // This is the load-bearing staleness defence: abort is an optimization the
  // bridge transport does not have.
  let generation = 0

  const changed = (): void => options.onChange?.()

  function cancelPending(): void {
    if (searchTimer) {
      clearTimeout(searchTimer)
      searchTimer = null
    }
    abort?.abort()
    abort = null
  }

  async function runSearch(query: string, gen: number): Promise<void> {
    let signal: AbortSignal | undefined
    if (options.abortable) {
      abort = new AbortController()
      signal = abort.signal
    }
    state.loading = true
    changed()
    try {
      const items = await options.search(query, signal)
      if (disposed || gen !== generation) return
      // Rank THEN slice, in that order. `rankMentions` promotes an exact or
      // prefix ID match ahead of the fuzzy order, so slicing first would drop a
      // target sitting past the cut before the promotion could reach it.
      state.items = rank(items, query).slice(0, MAX_RESULTS)
      state.errorMsg = ''
      state.highlightedIndex = 0
    } catch (err: unknown) {
      if ((err as { name?: string })?.name === 'AbortError') return
      if (disposed || gen !== generation) return
      state.items = []
      state.errorMsg = 'Search failed'
    } finally {
      if (!disposed && gen === generation) {
        state.loading = false
        changed()
      }
    }
  }

  return {
    state,
    minQueryLength: MIN_SEARCH_LEN,

    setQuery(query: string, reset = false): void {
      if (disposed) return
      state.open = true
      state.query = query
      cancelPending()
      if (reset) {
        state.items = []
        state.highlightedIndex = 0
      }
      const gen = ++generation
      if (!shouldSearch(query)) {
        // Show the prompt rather than a stale result set from a longer query
        // the user has just backspaced away from.
        state.items = []
        state.loading = false
        state.errorMsg = ''
        state.highlightedIndex = 0
        changed()
        return
      }
      // The timer is set BEFORE notifying, so a listener asking `pending()`
      // already sees the search as due.
      searchTimer = setTimeout(() => {
        searchTimer = null
        void runSearch(query, gen)
      }, SEARCH_DEBOUNCE_MS)
      changed()
    },

    pending(): boolean {
      return searchTimer !== null || state.loading
    },

    close(): void {
      cancelPending()
      // Bump the generation so an in-flight response cannot reopen the menu.
      generation++
      state.open = false
      state.query = ''
      state.items = []
      state.highlightedIndex = 0
      state.loading = false
      state.errorMsg = ''
      changed()
    },

    moveHighlight(delta: number): void {
      if (state.items.length === 0) return
      const n = state.items.length
      state.highlightedIndex = (state.highlightedIndex + delta + n) % n
      changed()
    },

    setHighlight(index: number): void {
      if (index < 0 || index >= state.items.length) return
      state.highlightedIndex = index
      changed()
    },

    current(): T | null {
      return state.items[state.highlightedIndex] ?? null
    },

    dispose(): void {
      disposed = true
      cancelPending()
    },
  }
}
