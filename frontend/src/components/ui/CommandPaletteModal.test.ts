import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { createRouter, createMemoryHistory } from 'vue-router'
import { mount, flushPromises } from '@vue/test-utils'
import type { VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import CommandPaletteModal from './CommandPaletteModal.vue'
import { useSchemaStore } from '@/stores/schema'
import { _resetModalStack, isAnyModalOpen } from '@/composables/modalStack'
import { searchEntities } from '@/api'
import type { Entity, ListResponse } from '@/types'

// Router stub — palette navigates via router.push when an entity is selected.


// Mock the search endpoint so each test can stub responses and we can assert
// signal forwarding without hitting the real API client.
vi.mock('@/api', async () => {
  const actual = await vi.importActual<typeof import('@/api')>('@/api')
  return {
    ...actual,
    searchEntities: vi.fn(),
  }
})

const searchSpy = searchEntities as unknown as ReturnType<typeof vi.fn>

function makeEntity(overrides: Partial<Entity> = {}): Entity {
  return {
    id: overrides.id ?? `T-${Math.random().toString(36).slice(2, 8).toUpperCase()}`,
    type: overrides.type ?? 'ticket',
    _title: overrides._title,
    properties: overrides.properties ?? {},
    ...overrides,
  } as Entity
}

function listResponse(entities: Entity[]): ListResponse<Entity> {
  return {
    data: entities,
    meta: { total: entities.length, page: 1, per_page: 25, has_more: false },
  }
}

function seedSchema() {
  const schemaStore = useSchemaStore()
  schemaStore.entityTypes.set('ticket', {
    name: 'ticket',
    label: 'Ticket',
    properties: {},
  } as never)
}

// A real router is installed (not just the mocked useRouter) because results are
// RouterLinks now — RouterLink resolves `to` through the injected router, so
// without one the anchors render with no href and the link assertions are
// meaningless. `useRouter` stays mocked so navigation intent is still spied on.
const testRouter = createRouter({
  history: createMemoryHistory('/'),
  routes: [
    { path: '/', component: { template: '<div/>' } },
    { path: '/entity/:type/:id', component: { template: '<div/>' } },
  ],
})

// `routerPush` spies on the REAL router rather than replacing useRouter.
// RouterLink resolves `to` through the injected router, so mocking the module
// would leave every result anchor with no href — the very affordance under test.
const routerPush = vi.spyOn(testRouter, 'push')

// RouterLink renders an empty href until the router has resolved its initial
// location, so prime it once for the whole file.
await testRouter.push('/')
await testRouter.isReady()

function factory(props: { open?: boolean } = {}): VueWrapper {
  return mount(CommandPaletteModal, {
    props: { open: true, ...props },
    attachTo: document.body,
    global: { plugins: [testRouter] },
  })
}

// Mount in a closed state, await mount, then drive the open transition.
// Used by tests that need to observe the closed → open lifecycle (focus
// capture, modal-stack registration on flip).
async function factoryClosedThenOpen(): Promise<VueWrapper> {
  const wrapper = factory({ open: false })
  await wrapper.setProps({ open: true })
  await flushPromises()
  return wrapper
}

// Centralized DOM lookups so query strings live in one place.
const dom = {
  overlay: () => document.querySelector<HTMLElement>('.cmdk-overlay'),
  modal: () => document.querySelector<HTMLElement>('.cmdk-modal'),
  input: (): HTMLInputElement => {
    const el = document.querySelector<HTMLInputElement>('.cmdk-input')
    if (!el) throw new Error('cmdk-input not in DOM')
    return el
  },
  options: () => Array.from(document.querySelectorAll<HTMLLIElement>('.cmdk-option')),
  // The option's navigation lives on an inner anchor (TKT-3CSZRG) so that
  // cmd/middle-click opens a tab; plain clicks still route in place.
  optionLinks: () =>
    Array.from(document.querySelectorAll<HTMLAnchorElement>('a.cmdk-option-link')),
  hint: () => document.querySelector<HTMLElement>('.cmdk-hint')?.textContent?.trim(),
  spinner: () => document.querySelector<HTMLElement>('.cmdk-spinner'),
}

const input = dom.input
const options = dom.options
const optionLinks = dom.optionLinks

// Type into the palette and wait for the debounced search to settle.
// Vue's v-model needs a microtask to sync the input event into the bound ref;
// flushPromises before advancing the fake timer ensures the watcher schedules
// the debounced setTimeout, which we then advance past.
async function typeQuery(value: string): Promise<void> {
  input().value = value
  input().dispatchEvent(new Event('input'))
  await flushPromises()
  vi.advanceTimersByTime(150)
  await flushPromises()
  await flushPromises()
}

// Drive the open prop and let the watcher run.
async function setOpen(wrapper: VueWrapper, open: boolean): Promise<void> {
  await wrapper.setProps({ open })
  await flushPromises()
}

// Press a key on the palette input. Bubbles so the @keydown on .cmdk-overlay
// catches it just like a real keypress would.
function pressKey(
  key: string,
  init: KeyboardEventInit = {}
): KeyboardEvent {
  const event = new KeyboardEvent('keydown', {
    key,
    bubbles: true,
    cancelable: true,
    ...init,
  })
  input().dispatchEvent(event)
  return event
}

describe('CommandPaletteModal', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    _resetModalStack()
    routerPush.mockClear()
    searchSpy.mockClear()
    // Default implementation so tests that don't queue a value don't crash on
    // resp.data; individual tests can still override with mockResolvedValueOnce
    // / mockRejectedValueOnce / mockImplementationOnce.
    searchSpy.mockResolvedValue(listResponse([]))
    document.body.innerHTML = ''
    seedSchema()
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    document.body.innerHTML = ''
    _resetModalStack()
  })

  describe('rendering', () => {
    it('does not render when closed', () => {
      factory({ open: false })
      expect(dom.overlay()).toBeNull()
    })

    it('renders overlay and input when open', () => {
      factory()
      expect(dom.overlay()).not.toBeNull()
      expect(input()).not.toBeNull()
    })

    it('shows the empty hint when query is blank', () => {
      factory()
      expect(dom.hint()).toBe('Type to search entities')
    })

    it('renders title, type label and id for each result', async () => {
      const entity = makeEntity({ id: 'T-1', type: 'ticket', _title: 'Fix login' })
      searchSpy.mockResolvedValueOnce(listResponse([entity]))
      factory()
      await typeQuery('fix')

      const opts = options()
      expect(opts).toHaveLength(1)
      expect(opts[0].textContent).toContain(entity._title)
      expect(opts[0].textContent).toContain(entity.id)
      expect(opts[0].textContent).toContain('Ticket')
    })

    it('ignores properties.title and falls back to id when _title missing', async () => {
      // properties.title is NOT a display source — only the backend's
      // metamodel-aware _title is. An entity with a `title` property but no
      // _title must render its ID, not the property (BUG-1P88YM).
      const entity = makeEntity({ id: 'T-9', properties: { title: 'Legacy title' } })
      searchSpy.mockResolvedValueOnce(listResponse([entity]))
      factory()
      await typeQuery('leg')

      expect(options()[0].textContent).toContain('T-9')
      expect(options()[0].textContent).not.toContain('Legacy title')
    })

    it('falls back to id when both title fields missing', async () => {
      const entity = makeEntity()
      searchSpy.mockResolvedValueOnce(listResponse([entity]))
      factory()
      await typeQuery('any')

      const titleEl = document.querySelector('.cmdk-title')
      expect(titleEl?.textContent).toBe(entity.id)
    })
  })

  describe('focus and lifecycle', () => {
    it('focuses the input on open', async () => {
      const wrapper = await factoryClosedThenOpen()
      expect(document.activeElement).toBe(input())
      wrapper.unmount()
    })

    it('restores previously focused element on close', async () => {
      const trigger = document.createElement('button')
      document.body.appendChild(trigger)
      trigger.focus()

      const wrapper = await factoryClosedThenOpen()
      expect(document.activeElement).toBe(input())

      await setOpen(wrapper, false)
      expect(document.activeElement).toBe(trigger)
      wrapper.unmount()
    })

    it('registers with the modal stack while open', async () => {
      const wrapper = factory({ open: false })
      expect(isAnyModalOpen()).toBe(false)

      await setOpen(wrapper, true)
      expect(isAnyModalOpen()).toBe(true)

      await setOpen(wrapper, false)
      expect(isAnyModalOpen()).toBe(false)

      wrapper.unmount()
    })

    it('resets query and highlightedIndex when re-opened', async () => {
      searchSpy.mockResolvedValue(
        listResponse([makeEntity(), makeEntity()])
      )
      const wrapper = factory()
      await typeQuery('foo')
      pressKey('ArrowDown')
      await flushPromises()

      await setOpen(wrapper, false)
      await setOpen(wrapper, true)

      expect(input().value).toBe('')
      // No results yet (query is empty), so no active descendant.
      expect(input().getAttribute('aria-activedescendant')).toBeNull()
      wrapper.unmount()
    })
  })

  describe('search behavior', () => {
    it('coalesces rapid keystrokes into a single API call', async () => {
      // Verifies our debounce: type three characters in quick succession,
      // and only the final value should make it to the API.
      factory()

      input().value = 'ab'
      input().dispatchEvent(new Event('input'))
      input().value = 'abc'
      input().dispatchEvent(new Event('input'))
      input().value = 'abcd'
      input().dispatchEvent(new Event('input'))
      // Drain the debounce.
      await flushPromises()
      vi.advanceTimersByTime(150)
      await flushPromises()
      await flushPromises()

      expect(searchSpy).toHaveBeenCalledTimes(1)
      expect(searchSpy).toHaveBeenLastCalledWith('abcd', undefined, expect.any(AbortSignal), undefined)
    })

    // Queries below the minimum length, or that contain only whitespace,
    // must short-circuit without hitting the API. Otherwise the backend
    // gets pummeled on every keystroke before the user has typed anything
    // useful, and the UX is "show 8000 unrelated results for 'a'."
    it.each<[string, string, string]>([
      ['empty', '', ''],
      ['whitespace-only', '   ', ''],
      ['single character', 'a', 'a'],
    ])('does not call /_search for %s queries', async (_name, query, prefix) => {
      factory()
      // Some cases (empty) need a prior real query to clear, so search
      // doesn't no-op on the no-change path.
      if (prefix) await typeQuery(prefix + 'x')
      searchSpy.mockClear()
      await typeQuery(query)

      expect(searchSpy).not.toHaveBeenCalled()
    })

    it('shows the empty hint for a single-character query', async () => {
      factory()
      await typeQuery('a')
      expect(dom.hint()).toBe('Type to search entities')
    })

    it('caps rendered results at MAX_RESULTS (50)', async () => {
      const many = Array.from({ length: 200 }, () => makeEntity())
      searchSpy.mockResolvedValueOnce(listResponse(many))
      factory()
      await typeQuery('big')

      expect(options()).toHaveLength(50)
    })

    it('shows "No matches" when results are empty', async () => {
      searchSpy.mockResolvedValueOnce(listResponse([]))
      factory()
      await typeQuery('nothing')

      expect(dom.hint()).toBe('No matches')
    })

    it('shows error message on search failure', async () => {
      searchSpy.mockRejectedValueOnce(new Error('network down'))
      factory()
      await typeQuery('foo')

      expect(dom.hint()).toBe('Search failed')
    })

    it('keeps previous results visible while a refetch is in flight', async () => {
      const first = makeEntity()
      searchSpy.mockResolvedValueOnce(listResponse([first]))
      factory()
      await typeQuery('fi')
      expect(options()).toHaveLength(1)

      // Trigger a second search but don't resolve it.
      let resolveSecond: (value: ListResponse<Entity>) => void = () => {}
      searchSpy.mockImplementationOnce(
        () =>
          new Promise<ListResponse<Entity>>((resolve) => {
            resolveSecond = resolve
          })
      )
      input().value = 'fix'
      input().dispatchEvent(new Event('input'))
      await flushPromises()
      vi.advanceTimersByTime(150)
      await flushPromises()

      // Previous results still visible (no flicker).
      expect(options()).toHaveLength(1)
      expect(dom.spinner()).not.toBeNull()

      // Resolve the second request — results swap.
      resolveSecond(listResponse([makeEntity(), makeEntity()]))
      await flushPromises()
      expect(options()).toHaveLength(2)
    })

    it('aborts the previous request when a new one is issued', async () => {
      const seenSignals: AbortSignal[] = []
      searchSpy.mockImplementation(
        async (
          _q: string,
          _t?: string,
          signal?: AbortSignal
        ): Promise<ListResponse<Entity>> => {
          if (signal) seenSignals.push(signal)
          return listResponse([])
        }
      )
      factory()

      await typeQuery('aa')
      await typeQuery('aab')

      expect(seenSignals).toHaveLength(2)
      // First signal was aborted before the second request was issued.
      expect(seenSignals[0].aborted).toBe(true)
      expect(seenSignals[1].aborted).toBe(false)
    })

    it('cancels in-flight request and timer on unmount', async () => {
      let resolveFn: (value: ListResponse<Entity>) => void = () => {}
      const seenSignals: AbortSignal[] = []
      searchSpy.mockImplementationOnce(
        (_q: string, _t?: string, signal?: AbortSignal) =>
          new Promise<ListResponse<Entity>>((resolve) => {
            if (signal) seenSignals.push(signal)
            resolveFn = resolve
          })
      )
      const wrapper = factory()
      input().value = 'foo'
      input().dispatchEvent(new Event('input'))
      await flushPromises()
      vi.advanceTimersByTime(150)
      await flushPromises()

      expect(seenSignals[0].aborted).toBe(false)
      wrapper.unmount()
      expect(seenSignals[0].aborted).toBe(true)

      // Even if the request resolves after unmount, no error is thrown.
      resolveFn(listResponse([makeEntity()]))
      await flushPromises()
    })
  })

  describe('keyboard navigation', () => {
    async function setupWithResults(n: number) {
      const entities = Array.from({ length: n }, () => makeEntity())
      searchSpy.mockResolvedValueOnce(listResponse(entities))
      const wrapper = factory()
      await typeQuery('ee')
      return { wrapper, entities }
    }

    it('ArrowDown moves highlight forward', async () => {
      const { wrapper } = await setupWithResults(3)
      pressKey('ArrowDown')
      await flushPromises()
      expect(options()[1].classList.contains('cmdk-option-active')).toBe(true)
      wrapper.unmount()
    })

    it('ArrowDown wraps from last to first', async () => {
      const { wrapper } = await setupWithResults(3)
      pressKey('ArrowDown')
      pressKey('ArrowDown')
      pressKey('ArrowDown')
      await flushPromises()
      expect(options()[0].classList.contains('cmdk-option-active')).toBe(true)
      wrapper.unmount()
    })

    it('ArrowUp wraps from first to last', async () => {
      const { wrapper } = await setupWithResults(3)
      pressKey('ArrowUp')
      await flushPromises()
      expect(options()[2].classList.contains('cmdk-option-active')).toBe(true)
      wrapper.unmount()
    })

    it('ArrowDown does not crash with empty results', async () => {
      factory()
      pressKey('ArrowDown')
      await flushPromises()
      // No throw, no options rendered.
      expect(options()).toHaveLength(0)
    })

    it('aria-activedescendant matches highlighted option id', async () => {
      const { wrapper, entities } = await setupWithResults(2)
      pressKey('ArrowDown')
      await flushPromises()
      const expected = `cmdk-option-${entities[1].id}`
      expect(input().getAttribute('aria-activedescendant')).toBe(expected)
      expect(options()[1].id).toBe(expected)
      wrapper.unmount()
    })

    it('scrolls the highlighted option into view on arrow navigation', async () => {
      const { wrapper, entities } = await setupWithResults(20)
      const target = entities[5]
      const scrollSpy = vi.spyOn(
        document.getElementById(`cmdk-option-${target.id}`)!,
        'scrollIntoView'
      )
      // Press ArrowDown 5 times to land on `target`.
      for (let i = 0; i < 5; i++) pressKey('ArrowDown')
      await flushPromises()

      expect(scrollSpy).toHaveBeenCalled()
      expect(scrollSpy).toHaveBeenLastCalledWith({ block: 'nearest' })
      wrapper.unmount()
    })

    it('Enter navigates to the highlighted entity and emits close', async () => {
      const { wrapper, entities } = await setupWithResults(2)
      pressKey('ArrowDown')
      await flushPromises()
      pressKey('Enter')
      await flushPromises()

      expect(routerPush).toHaveBeenCalledWith(`/entity/${entities[1].type}/${entities[1].id}`)
      expect(wrapper.emitted('close')).toHaveLength(1)
      wrapper.unmount()
    })

    it('Enter is a no-op when results are empty', async () => {
      const wrapper = factory()
      pressKey('Enter')
      await flushPromises()
      expect(routerPush).not.toHaveBeenCalled()
      expect(wrapper.emitted('close')).toBeUndefined()
    })

    it('Escape emits close and stops propagation', async () => {
      const wrapper = factory()
      const event = new KeyboardEvent('keydown', {
        key: 'Escape',
        bubbles: true,
        cancelable: true,
      })
      const stopSpy = vi.spyOn(event, 'stopPropagation')
      input().dispatchEvent(event)
      await flushPromises()

      expect(stopSpy).toHaveBeenCalled()
      expect(wrapper.emitted('close')).toHaveLength(1)
      expect(routerPush).not.toHaveBeenCalled()
    })

    it('Tab is preventDefault’d so focus stays on the input', async () => {
      factory()
      await flushPromises()
      // After mount with open=true the input is auto-focused (immediate watcher).
      expect(document.activeElement).toBe(input())

      const event = pressKey('Tab')

      expect(event.defaultPrevented).toBe(true)
      expect(document.activeElement).toBe(input())
    })
  })

  describe('selection', () => {
    it('clicking a result navigates and emits close', async () => {
      const entity = makeEntity({ type: 'ticket' })
      searchSpy.mockResolvedValueOnce(listResponse([entity]))
      const wrapper = factory()
      await typeQuery('xx')

      const link = optionLinks()[0]
      expect(link.getAttribute('href')).toBe(`/entity/${entity.type}/${entity.id}`)

      link.click()
      await flushPromises()

      expect(routerPush).toHaveBeenCalledWith(`/entity/${entity.type}/${entity.id}`)
      expect(wrapper.emitted('close')).toHaveLength(1)
    })

    it('still renders the option text when there is no resolvable href', async () => {
      // Guarding only the link would leave an empty, zero-height <li
      // role="option">: invisible, still counted by the arrow-key highlight,
      // and nameless to a screen reader — worse than the inert row it replaced.
      const entity = makeEntity({ type: '' })
      searchSpy.mockResolvedValueOnce(listResponse([entity]))
      factory()
      await typeQuery('xx')

      expect(optionLinks()).toHaveLength(0)
      const option = options()[0]
      expect(option).toBeTruthy()
      expect(option.textContent).toContain(entity.id)
      expect(option.querySelector('.cmdk-option-link')).not.toBeNull()
    })

    it('does not navigate when entity has no type (empty href)', async () => {
      searchSpy.mockResolvedValueOnce(
        listResponse([makeEntity({ type: '' })])
      )
      const wrapper = factory()
      await typeQuery('xx')

      options()[0].click()
      await flushPromises()

      expect(routerPush).not.toHaveBeenCalled()
      expect(wrapper.emitted('close')).toBeUndefined()
    })
  })

  describe('overlay click', () => {
    it('emits close when backdrop is clicked', () => {
      const wrapper = factory()
      dom.overlay()!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      expect(wrapper.emitted('close')).toHaveLength(1)
    })

    it('does not emit close when clicking inside the modal', () => {
      const wrapper = factory()
      dom.modal()!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      expect(wrapper.emitted('close')).toBeUndefined()
    })
  })
})
