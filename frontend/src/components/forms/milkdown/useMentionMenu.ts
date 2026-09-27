/**
 * The `@` completion menu, as the SPA form consumes it.
 *
 * What the menu shows for each query is decided by `planMention`
 * (`mentionPlan.ts`, TKT-39TIB4): a starting list for a bare `@`, types for one
 * or two letters, then search results with types beside them. A type scope is
 * document text (`@ticket:`), so this controller keeps no scope of its own; it
 * re-plans from the query on every keystroke.
 *
 * The search/staleness state machine lives in `mentionMenuState.ts` because the
 * sandboxed app editor runs the same machine without Vue, and a second copy of
 * that logic is exactly the drift TKT-D2JML7 removed. What stays here is the
 * reactivity, the transports, the stages and the type rows, which are SPA-only:
 * the sandboxed app runs under `connect-src 'none'` and has no route to the
 * schema's type list. Nothing here touches ProseMirror.
 *
 * # The query reaches the server unmodified
 *
 * Nothing here rewrites, splits or sanitizes the search text. Sending
 * word-split forms was measured to destroy ID ranking on the bleve backend (the
 * regression BUG-O09QUC exists to prevent) and to return nothing at all on the
 * linear and postgres backends, which match the query as one substring. The
 * only thing removed is a `ticket:` scope, which travels as the `type`
 * parameter instead.
 */
import { reactive, readonly, watch, type DeepReadonly } from 'vue'
import { ApiError, getEntity, listRecentlyModified, searchEntities } from '@/api'
import { useWorld } from '@/composables/useWorld'
import { useSchemaStore } from '@/stores/schema'
import type { Entity } from '@/types'
import { listRecentEntities } from '@/utils/recentEntities'
import { createMentionMenuMachine, type MentionMenuSnapshot } from './mentionMenuState'
import { planMention, type MentionPlan, type MentionTypeInfo } from './mentionPlan'
import { createStartingList, type StartingList, type StartingListSelf } from './mentionStartingList'
import { mentionWorld } from './mentionWorld'
import { rankMentions } from './rankMentions'

/** What the highlight currently sits on. Enter means different things per kind. */
export type MentionChoice = { kind: 'type'; name: string } | { kind: 'entity'; entity: Entity }

/**
 * WHAT the highlight is on, rather than where it sat.
 *
 * The rows are recomputed asynchronously under the user: the type rows re-plan
 * on every keystroke, while the entities arrive a debounce later. A stored
 * INDEX addressing that is unsound, and was wrong four different ways — it
 * could point past the end (making Enter a no-op that fell through to
 * ProseMirror and inserted a paragraph break), or slide onto an unrelated row
 * when the list above it shrank, so Enter inserted an entity the user never
 * highlighted.
 *
 * Storing the identity makes those unrepresentable: it either still resolves to
 * the same row or it does not resolve at all, and `highlightedIndex()` falls
 * back to the first row rather than inventing a selection.
 */
export type MentionHighlight =
  { kind: 'type'; name: string } | { kind: 'entity'; id: string } | null

/** The SPA menu's state: the shared snapshot plus what the plan decided. */
export interface MentionMenuState extends MentionMenuSnapshot<Entity> {
  /** Type rows on offer, already ranked and capped. */
  typeItems: string[]
  /** The type the query is scoped to, or null for every type. */
  scopeType: string | null
  /** True when the type rows come before the entity rows. */
  typesFirst: boolean
  /** True when the entity rows are the starting list rather than a search. */
  starting: boolean
  /** True when this query runs a search or loads the starting list. */
  searches: boolean
  /** The highlighted row's identity, or null to mean "the first row". */
  highlight: MentionHighlight
  /**
   * True while a search is debouncing or in flight. The rows on screen do not
   * answer the current query yet, so an empty list must not read "No matches".
   */
  pending: boolean
}

export interface MentionMenuController {
  state: DeepReadonly<MentionMenuState>
  /** Opens the menu, or updates the query if it is already open. */
  setQuery: (query: string) => void
  /** Supplies the types to offer; the caller owns the schema store. */
  setAvailableTypes: (types: MentionTypeInfo[]) => void
  /** The type a `name:` prefix scopes to, or null when there is none. */
  scopeTypeFor: (name: string) => string | null
  close: () => void
  moveHighlight: (delta: number) => void
  /** Highlights the row at this position in the combined list. */
  setHighlight: (index: number) => void
  /**
   * Where the highlight currently resolves to in the combined list.
   *
   * Derived from the stored identity on every call rather than kept as state,
   * so it cannot go stale against rows that changed underneath it. Falls back
   * to 0 when the highlighted row is gone, and is -1 when there are no rows.
   */
  highlightedIndex: () => number
  /** What the highlight sits on, or null when there is nothing to pick. */
  current: () => MentionChoice | null
  /** True while a search is debouncing or in flight. */
  pending: () => boolean
  /**
   * Runs `cb` with the highlighted choice once the current search settles.
   *
   * Enter pressed before results land waits for them rather than acting on
   * the rows of an older query (spec 6.7). Dropped if the query changes or the
   * menu closes first, and not run when the search found nothing.
   */
  whenSettled: (cb: (choice: MentionChoice) => void) => void
  /**
   * True when a search settled with nothing to offer. An empty starting list
   * does not count: nothing was searched for, so nothing failed to match.
   */
  settledEmpty: () => boolean
  dispose: () => void
}

/** How the controller fetches entities. Injected so tests need no network. */
export interface MentionTransport {
  search: (text: string, type: string | undefined, signal?: AbortSignal) => Promise<Entity[]>
  startingList: StartingList
}

/** The entity being edited, which the starting list excludes and starts from. */
export type MentionSelf = StartingListSelf

/**
 * A mention menu wired to the live schema and the API.
 *
 * The type list is watched rather than read once because the schema loads on
 * app mount and an editor can mount before that resolves. Type names are not
 * confidential (root CLAUDE.md: "The configuration is not a secret"), so every
 * principal is offered the full list and no per-principal filtering applies.
 */
export function useSchemaMentionMenu(self: () => MentionSelf | null): MentionMenuController {
  const schemaStore = useSchemaStore()
  const { worldParam } = useWorld()
  const typeNames = (): string[] => schemaStore.entityTypeList.map(([name]) => name)
  // Read per request, so a world switch applies to the next keystroke.
  const world = (): string | undefined =>
    mentionWorld(self(), schemaStore.worldForFace, worldParam.value)

  // One starting list per world: its caches hold rows resolved in one world,
  // and a row from another would show the wrong face's title, or an entity
  // that world does not have.
  const startingLists = new Map<string, StartingList>()
  const startingListFor = (w: string | undefined): StartingList => {
    const key = w ?? ''
    let list = startingLists.get(key)
    if (!list) {
      list = createStartingListIn(w)
      startingLists.set(key, list)
    }
    return list
  }

  function createStartingListIn(w: string | undefined): StartingList {
    return createStartingList({
      self,
      related: async (s) => {
        // The face being edited, by its address, so its own links are the
        // ones offered; the neighbours resolve through `w`. `include=*`
        // returns only neighbours the principal may read.
        const address = s.face ? `${s.id}@${s.face}` : s.id
        const entity = await getEntity(s.type, address, {
          include: '*',
          ...(w ? { world: w } : {}),
        })
        return Object.values(entity.included ?? {})
      },
      recent: listRecentEntities,
      load: async (ref) => {
        try {
          return await getEntity(ref.type, ref.id, w ? { world: w } : undefined)
        } catch (err) {
          // Gone, renamed or hidden from this user: not offered. Any other
          // failure is rethrown so the starting list retries it next time
          // rather than caching a network blip as "hidden".
          if (err instanceof ApiError && (err.status === 404 || err.status === 403)) return null
          throw err
        }
      },
      recentlyModified: async (types, limit) =>
        (await listRecentlyModified(types, limit, undefined, w)).data,
      allTypes: typeNames,
    })
  }

  const menu = useMentionMenu({
    search: async (text, type, signal) => (await searchEntities(text, type, signal, world())).data,
    startingList: { load: (type) => startingListFor(world()).load(type) },
  })

  watch(
    () => schemaStore.entityTypeList,
    (list) =>
      menu.setAvailableTypes(
        list.map(([name, def]) => ({
          name,
          prefixes: def.id_prefixes ?? (def.id_prefix ? [def.id_prefix] : []),
        }))
      ),
    { immediate: true }
  )
  return menu
}

export function useMentionMenu(transport: MentionTransport): MentionMenuController {
  // The machine mutates its snapshot in place, so wrapping it in `reactive`
  // makes every mutation reach the template without the machine knowing Vue
  // exists. The plan fields are ours; the machine never touches them.
  const state = reactive<MentionMenuState>({
    open: false,
    query: '',
    items: [],
    highlightedIndex: 0,
    loading: false,
    errorMsg: '',
    typeItems: [],
    scopeType: null,
    typesFirst: false,
    starting: false,
    searches: false,
    highlight: null,
    pending: false,
  })

  let types: MentionTypeInfo[] = []
  let disposed = false
  let settledCallback: ((choice: MentionChoice) => void) | null = null

  const plan = (query: string): MentionPlan => planMention(types, query)

  // Project the machine's snapshot onto the reactive object. Assigning the
  // fields rather than swapping the object keeps a `readonly(state)` handle a
  // component already holds pointing at the right thing.
  //
  // Driven by the machine's own `onChange` rather than by wrapping each method:
  // the interesting transitions are ASYNCHRONOUS (a search resolving, a stale
  // response being discarded), so a wrapper that synced on the synchronous call
  // would show an empty list forever.
  const sync = (): void => {
    Object.assign(state, machine.state)
    state.pending = machine.pending()
    // A fresh result set invalidates an entity highlight that is no longer in
    // it. The machine owns `items`, so this is the point where that is known.
    if (state.highlight?.kind === 'entity') {
      const id = state.highlight.id
      if (!state.items.some((e) => e.id === id)) state.highlight = null
    }
    if (settledCallback && !machine.pending()) {
      const cb = settledCallback
      settledCallback = null
      const choice = current()
      if (choice) cb(choice)
    }
  }

  const machine = createMentionMenuMachine<Entity>({
    // axios can cancel, so the signal is passed through and an abort is not
    // reported to the user as a failed search.
    abortable: true,
    shouldSearch: (query) => {
      const p = plan(query)
      return p.starting || p.search !== null
    },
    search: async (query, signal) => {
      const p = plan(query)
      if (p.starting) return transport.startingList.load(p.scopeType)
      // `?? undefined`: the API treats undefined as "every type", and a null
      // would serialize into the query string. The scope is always a name the
      // schema supplied, so `?type=` stays an allowlist.
      return transport.search(p.search ?? '', p.scopeType ?? undefined, signal)
    },
    // The starting list keeps its own order; a search is ranked against the
    // text actually searched for, which under a scope excludes `ticket:`.
    rank: (items, query) => {
      const p = plan(query)
      return p.starting ? items : rankMentions(items, p.search ?? '')
    },
    onChange: () => sync(),
  })

  function applyPlan(p: MentionPlan): void {
    state.scopeType = p.scopeType
    state.typeItems = p.typeRows
    state.typesFirst = p.typesFirst
    state.starting = p.starting
    state.searches = p.starting || p.search !== null
  }

  function setQuery(query: string): void {
    if (disposed) return
    // SlashProvider re-asks on every update, cursor blinks and focus changes
    // included. Restarting the search for the same text would push its answer
    // back each time, and hold up an Enter that is waiting for it.
    if (state.open && query === state.query) return
    settledCallback = null
    const next = plan(query)
    // Rows answering a different question are wrong, not merely stale: the
    // previous scope's entities under a new scope, or search hits where the
    // starting list belongs. Drop them rather than let Enter insert one.
    const reset =
      state.open && (next.scopeType !== state.scopeType || next.starting !== state.starting)
    applyPlan(next)
    machine.setQuery(query, reset)
    if (reset) state.highlight = null
    // The machine's `onChange` covers the async transitions; a query that
    // does not search settles synchronously, so mirror the open state here.
    state.open = true
    state.query = query
  }

  function setAvailableTypes(next: MentionTypeInfo[]): void {
    if (disposed) return
    types = next
    // A closed menu offers nothing. Without this a schema reload repopulates
    // the rows behind a closed menu, making `close()` a liar.
    if (!state.open) return
    // Re-ask the open query from scratch: rows fetched under the old type
    // list (a search where the plan now says "types only", or a starting
    // list without its recently modified part) answer a different question.
    applyPlan(plan(state.query))
    machine.setQuery(state.query, true)
    state.highlight = null
  }

  /** Exact type names only; an ID prefix scopes without a colon, so `TKT-6:` gets no chip. */
  function scopeTypeFor(name: string): string | null {
    const lower = name.toLowerCase()
    return types.find((t) => t.name.toLowerCase() === lower)?.name ?? null
  }

  function close(): void {
    settledCallback = null
    machine.close()
    state.open = false
    state.typeItems = []
    state.scopeType = null
    state.typesFirst = false
    state.starting = false
    state.searches = false
    state.highlight = null
  }

  /** The rows in display order, which depends on the stage. */
  function rows(): MentionChoice[] {
    const typeRows: MentionChoice[] = state.typeItems.map((name) => ({ kind: 'type', name }))
    const entityRows: MentionChoice[] = state.items.map((e) => ({
      kind: 'entity',
      entity: e as Entity,
    }))
    return state.typesFirst ? [...typeRows, ...entityRows] : [...entityRows, ...typeRows]
  }

  function highlightedIndex(): number {
    const all = rows()
    if (all.length === 0) return -1
    const h = state.highlight
    if (h !== null) {
      const at = all.findIndex((r) =>
        r.kind === 'type'
          ? h.kind === 'type' && r.name === h.name
          : h.kind === 'entity' && r.entity.id === h.id
      )
      if (at !== -1) return at
    }
    return 0
  }

  function setHighlight(index: number): void {
    const choice = rows()[index]
    if (!choice) return
    state.highlight =
      choice.kind === 'type'
        ? { kind: 'type', name: choice.name }
        : { kind: 'entity', id: choice.entity.id }
  }

  function moveHighlight(delta: number): void {
    const n = rows().length
    if (n === 0) return
    setHighlight((highlightedIndex() + delta + n) % n)
  }

  function current(): MentionChoice | null {
    return rows()[highlightedIndex()] ?? null
  }

  function whenSettled(cb: (choice: MentionChoice) => void): void {
    if (!machine.pending()) {
      const choice = current()
      if (choice) cb(choice)
      return
    }
    settledCallback = cb
  }

  function settledEmpty(): boolean {
    return (
      state.open &&
      state.searches &&
      !state.starting &&
      !machine.pending() &&
      state.errorMsg === '' &&
      state.items.length === 0 &&
      state.typeItems.length === 0
    )
  }

  function dispose(): void {
    disposed = true
    settledCallback = null
    machine.dispose()
  }

  return {
    state: readonly(state) as DeepReadonly<MentionMenuState>,
    setQuery,
    setAvailableTypes,
    scopeTypeFor,
    close,
    moveHighlight,
    setHighlight,
    highlightedIndex,
    current,
    pending: () => machine.pending(),
    whenSettled,
    settledEmpty,
    dispose,
  }
}
