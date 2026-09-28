import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import type { VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import EntityPickerModal from './EntityPickerModal.vue'
import { useSchemaStore } from '@/stores/schema'
import { _resetModalStack, isAnyModalOpen } from '@/composables/modalStack'
import { searchEntities } from '@/api'
import type { Entity, ListResponse } from '@/types'

vi.mock('@/api', async () => {
  const actual = await vi.importActual<typeof import('@/api')>('@/api')
  return {
    ...actual,
    searchEntities: vi.fn(),
  }
})

const searchSpy = searchEntities as unknown as ReturnType<typeof vi.fn>

function makeEntity(overrides: Partial<Entity> = {}): Entity {
  const entity: Entity = {
    id: overrides.id ?? `T-${Math.random().toString(36).slice(2, 8).toUpperCase()}`,
    type: overrides.type ?? 'ticket',
    _title: overrides._title,
    properties: overrides.properties ?? {},
  }
  return { ...entity, ...overrides }
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

function factory(props: { open?: boolean } = {}): VueWrapper {
  return mount(EntityPickerModal, {
    props: { open: true, ...props },
    attachTo: document.body,
  })
}

async function factoryClosedThenOpen(): Promise<VueWrapper> {
  const wrapper = factory({ open: false })
  await wrapper.setProps({ open: true })
  await flushPromises()
  return wrapper
}

const dom = {
  overlay: () => document.querySelector<HTMLElement>('.entity-picker-overlay'),
  modal: () => document.querySelector<HTMLElement>('.entity-picker-modal'),
  input: (): HTMLInputElement => {
    const el = document.querySelector<HTMLInputElement>('.entity-picker-input')
    if (!el) throw new Error('entity-picker-input not in DOM')
    return el
  },
  options: () => Array.from(document.querySelectorAll<HTMLLIElement>('.entity-picker-option')),
}

const input = dom.input
const options = dom.options

async function typeQuery(value: string): Promise<void> {
  input().value = value
  input().dispatchEvent(new Event('input'))
  await flushPromises()
  vi.advanceTimersByTime(150)
  await flushPromises()
  await flushPromises()
}

async function setOpen(wrapper: VueWrapper, open: boolean): Promise<void> {
  await wrapper.setProps({ open })
  await flushPromises()
}

function pressKey(key: string): KeyboardEvent {
  const event = new KeyboardEvent('keydown', {
    key,
    bubbles: true,
    cancelable: true,
  })
  input().dispatchEvent(event)
  return event
}

describe('EntityPickerModal', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    _resetModalStack()
    searchSpy.mockClear()
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
    it('renders nothing when closed', () => {
      factory({ open: false })
      expect(dom.overlay()).toBeNull()
    })

    it('renders overlay and input when open', () => {
      factory()
      expect(dom.overlay()).not.toBeNull()
      expect(input()).not.toBeNull()
    })

    it('focuses the input on open', async () => {
      const wrapper = await factoryClosedThenOpen()
      expect(document.activeElement).toBe(input())
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
  })

  describe('search', () => {
    it('does not call searchEntities below MIN_QUERY_LEN', async () => {
      factory()
      await typeQuery('a')
      expect(searchSpy).not.toHaveBeenCalled()
    })

    it('issues debounced /_search when typing >= 2 chars', async () => {
      const entity = makeEntity({ _title: 'Fix login' })
      searchSpy.mockResolvedValueOnce(listResponse([entity]))
      factory()
      await typeQuery('fix')
      expect(searchSpy).toHaveBeenCalledExactlyOnceWith('fix', undefined, expect.any(AbortSignal), undefined)
      expect(options()).toHaveLength(1)
    })
  })

  describe('selection', () => {
    it('emits select(id) and close when clicking a result', async () => {
      const entity = makeEntity({ id: 'TKT-001', _title: 'Pick me' })
      searchSpy.mockResolvedValueOnce(listResponse([entity]))
      const wrapper = factory()
      await typeQuery('pick')

      options()[0].click()
      await flushPromises()

      expect(wrapper.emitted('select')).toEqual([[entity.id]])
      expect(wrapper.emitted('close')).toHaveLength(1)
      wrapper.unmount()
    })

    it('Enter emits select(id) for the highlighted result', async () => {
      const e1 = makeEntity({ id: 'TKT-001' })
      const e2 = makeEntity({ id: 'TKT-002' })
      searchSpy.mockResolvedValueOnce(listResponse([e1, e2]))
      const wrapper = factory()
      await typeQuery('any')

      pressKey('ArrowDown')
      await flushPromises()
      pressKey('Enter')
      await flushPromises()

      expect(wrapper.emitted('select')).toEqual([[e2.id]])
      wrapper.unmount()
    })

    it('Escape emits close without select', async () => {
      const wrapper = factory()
      pressKey('Escape')
      await flushPromises()
      expect(wrapper.emitted('select')).toBeUndefined()
      expect(wrapper.emitted('close')).toHaveLength(1)
      wrapper.unmount()
    })

    it('clicking the overlay emits close without select', async () => {
      const wrapper = factory()
      const overlay = dom.overlay()!
      overlay.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await flushPromises()
      // jsdom MouseEvent target/currentTarget alignment for synthetic dispatch
      // mirrors a backdrop click — see CommandPaletteModal.test.ts for the
      // canonical pattern.
      expect(wrapper.emitted('close')).toHaveLength(1)
      expect(wrapper.emitted('select')).toBeUndefined()
      wrapper.unmount()
    })
  })

  describe('abort on close (RR-S7I8)', () => {
    it('aborts the in-flight search when the modal closes mid-request', async () => {
      // Resolve only when we explicitly choose to so we can observe the
      // controller being aborted while the request is in flight.
      let capturedSignal: AbortSignal | undefined
      searchSpy.mockImplementationOnce((_q: string, _t: unknown, signal: AbortSignal) => {
        capturedSignal = signal
        return new Promise(() => {
          // never resolves; the watcher's abort() is what we assert on
        })
      })

      const wrapper = factory()
      await typeQuery('foo')
      expect(capturedSignal).toBeDefined()
      expect(capturedSignal!.aborted).toBe(false)

      await setOpen(wrapper, false)
      expect(capturedSignal!.aborted).toBe(true)
      wrapper.unmount()
    })

    it('cancels the pending debounce timer when the modal closes before fetch', async () => {
      const wrapper = factory()
      input().value = 'foo'
      input().dispatchEvent(new Event('input'))
      await flushPromises()
      // Close BEFORE the 150ms debounce fires.
      await setOpen(wrapper, false)
      vi.advanceTimersByTime(500)
      await flushPromises()
      expect(searchSpy).not.toHaveBeenCalled()
      wrapper.unmount()
    })
  })

  describe('keyboard navigation', () => {
    it('ArrowDown / ArrowUp moves the highlighted index with wrap-around', async () => {
      const a = makeEntity({ id: 'A-1' })
      const b = makeEntity({ id: 'B-2' })
      const c = makeEntity({ id: 'C-3' })
      searchSpy.mockResolvedValueOnce(listResponse([a, b, c]))
      factory()
      await typeQuery('any')

      // Initial highlight is 0
      const opts = options()
      expect(opts[0].classList.contains('entity-picker-option-active')).toBe(true)

      pressKey('ArrowDown')
      await flushPromises()
      expect(options()[1].classList.contains('entity-picker-option-active')).toBe(true)

      pressKey('ArrowUp')
      pressKey('ArrowUp')
      await flushPromises()
      // Wrap: 1 → 0 → 2
      expect(options()[2].classList.contains('entity-picker-option-active')).toBe(true)
    })
  })
})
