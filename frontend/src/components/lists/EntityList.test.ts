import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import EntityList from './EntityList.vue'
import Pagination from './Pagination.vue'
import ConfirmModal from '@/components/ui/ConfirmModal.vue'
import { useSchemaStore } from '@/stores/schema'
import { useUIStore } from '@/stores/ui'
import { _setEntityPluralForTest } from '@/api/entities'
import { _resetModalStack } from '@/composables/modalStack'
import { useConfirmHost, _resetConfirmForTest } from '@/composables/useConfirm'
import type { Entity, ListResponse } from '@/types'

// EntityList fetches via the api layer (useQuery) and deletes via it
// (useMutation), so mock the api functions, not the entities store.
const listEntitiesMock = vi.fn()
const deleteEntityMock = vi.fn()
vi.mock('@/api', async (orig) => ({
  ...(await orig<typeof import('@/api')>()),
  listEntities: (...args: unknown[]) => listEntitiesMock(...args),
  deleteEntity: (...args: unknown[]) => deleteEntityMock(...args),
}))

// Router stubs — EntityList reads both `useRouter` (for open/edit/create
// navigation) and `useRoute` (for URL-filter sync). The delete flow uses a
// minimal stub; the search-flow tests below mutate `mockRoute.query` to
// simulate `?q=` deep-links and assert that the composable reflects them.
const routerPush = vi.fn()
const routerReplace = vi.fn()
const mockRoute = { query: {} as Record<string, string>, path: '/list/tickets-list', name: 'list' }
vi.mock('vue-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-router')>()),
  useRouter: () => ({ push: routerPush, replace: routerReplace }),
  useRoute: () => mockRoute,
}))

// Integration test for the delete-flow wiring:
//   delete-button click / Delete keydown / Backspace keydown
//   → useConfirm.confirm() with onConfirm: entitiesStore.remove
//   → singleton ConfirmModal renders
//   → user confirms → remove fires; user cancels → it does not.
// Mounted alongside a useConfirmHost-bound ConfirmModal to mirror App.vue.

describe('EntityList delete integration', () => {
  const listId = 'tickets-list'
  const entityType = 'ticket'

  function makeEntity(id: string): Entity {
    return {
      id,
      type: entityType,
      properties: { title: `Title ${id}` },
    }
  }

  function seedSchema() {
    const schemaStore = useSchemaStore()
    // Minimal list config: one text column, no filters, default page size.
    schemaStore.lists.set(listId, {
      id: listId,
      title: 'Tickets',
      entity: entityType,
      columns: [{ property: 'title', label: 'Title' }],
    } as never)
    // Minimal entity type so entityType computed resolves.
    schemaStore.entityTypes.set(entityType, {
      name: entityType,
      label: 'Ticket',
      properties: {
        title: { type: 'string', values: null },
      },
    } as never)
  }

  function seedEntities(entities: Entity[]): ListResponse<Entity> {
    const response: ListResponse<Entity> = {
      data: entities,
      meta: { total: entities.length, page: 1, per_page: 25, has_more: false },
      included: {},
    }
    listEntitiesMock.mockResolvedValue(response)
    return response
  }

  let pinia: ReturnType<typeof createPinia>
  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    _setEntityPluralForTest(entityType, 'tickets')
    listEntitiesMock.mockReset()
    deleteEntityMock.mockReset().mockResolvedValue(undefined)
    _resetModalStack()
    _resetConfirmForTest()
    routerPush.mockClear()
    routerReplace.mockClear()
    mockRoute.query = {}
  })

  afterEach(() => {
    document.body.innerHTML = ''
    _resetModalStack()
    _resetConfirmForTest()
  })

  // Mount EntityList alongside the global ConfirmModal host so the singleton
  // confirm composable resolves to a rendered modal — the way App.vue wires it.
  const Host = defineComponent({
    props: { listId: { type: String, required: true } },
    setup(props) {
      const { state, onConfirmEvent, onCancelEvent } = useConfirmHost()
      return () => [
        h(EntityList, { listId: props.listId }),
        h(ConfirmModal, {
          open: state.open,
          title: state.title,
          message: state.message,
          confirmLabel: state.confirmLabel,
          cancelLabel: state.cancelLabel,
          busy: state.busy,
          danger: state.danger,
          // Swallow rethrown onConfirm errors here — the composable
          // signals "stay open" by throwing; the modal callback isn't a
          // place to surface them. The original caller has already toasted.
          onConfirm: () => { onConfirmEvent().catch(() => {}) },
          onCancel: () => { onCancelEvent() },
        }),
      ]
    },
  })

  async function mountList(entities: Entity[]) {
    seedSchema()
    seedEntities(entities)
    const wrapper = mount(Host, {
      props: { listId },
      attachTo: document.body,
      global: { plugins: [pinia, PiniaColada] },
    })
    await flushPromises()
    return wrapper
  }

  function overlay(): HTMLElement | null {
    return document.querySelector<HTMLElement>('.modal-overlay')
  }

  function modalButtons(): HTMLButtonElement[] {
    return Array.from(
      document.querySelectorAll<HTMLButtonElement>('.modal-actions button')
    )
  }

  it('does not show delete modal by default', async () => {
    const wrapper = await mountList([makeEntity('T-1'), makeEntity('T-2')])
    expect(overlay()).toBeNull()
    wrapper.unmount()
  })

  it('clicking delete button opens confirm modal for that entity', async () => {
    const entities = [makeEntity('T-1'), makeEntity('T-2')]
    const wrapper = await mountList(entities)

    const deleteButtons = wrapper.findAll('.delete-btn')
    expect(deleteButtons).toHaveLength(2)
    await deleteButtons[1].trigger('click')
    await flushPromises()

    expect(overlay()).not.toBeNull()
    // The modal's slot references the pending entity id.
    expect(overlay()?.textContent).toContain(entities[1].id)
    wrapper.unmount()
  })

  it('Delete keydown on selected row opens confirm modal for that row', async () => {
    const entities = [makeEntity('T-1'), makeEntity('T-2')]
    const wrapper = await mountList(entities)

    // Select first row via j, then open delete modal via Delete.
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'j', bubbles: true }))
    document.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Delete', bubbles: true })
    )
    await flushPromises()

    expect(overlay()).not.toBeNull()
    expect(overlay()?.textContent).toContain(entities[0].id)
    wrapper.unmount()
  })

  it('Backspace on selected row opens confirm modal', async () => {
    const entities = [makeEntity('T-1')]
    const wrapper = await mountList(entities)

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'j', bubbles: true }))
    document.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Backspace', bubbles: true })
    )
    await flushPromises()

    expect(overlay()).not.toBeNull()
    wrapper.unmount()
  })

  it('Cancel button closes modal without deleting', async () => {
    const entities = [makeEntity('T-1')]
    const wrapper = await mountList(entities)

    await wrapper.find('.delete-btn').trigger('click')
    await flushPromises()

    modalButtons()[0].click()
    await flushPromises()

    expect(overlay()).toBeNull()
    expect(deleteEntityMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('Confirm button calls deleteEntity with the pending entity', async () => {
    const entities = [makeEntity('T-1'), makeEntity('T-2')]
    const wrapper = await mountList(entities)

    // Click delete on the SECOND row — the mutation should receive that entity.
    const deleteButtons = wrapper.findAll('.delete-btn')
    await deleteButtons[1].trigger('click')
    await flushPromises()

    modalButtons()[1].click()
    await flushPromises()

    expect(deleteEntityMock).toHaveBeenCalledTimes(1)
    expect(deleteEntityMock).toHaveBeenCalledWith(entities[1].type, entities[1].id)
    wrapper.unmount()
  })

  it('rolls back the row and toasts when delete fails (onError wiring)', async () => {
    const entities = [makeEntity('T-1'), makeEntity('T-2')]
    const wrapper = await mountList(entities)
    deleteEntityMock.mockRejectedValue(new Error('boom'))
    const uiStore = useUIStore()
    const errorSpy = vi.spyOn(uiStore, 'error')
    const consoleSpy = vi.spyOn(console, 'error').mockImplementation(() => {})

    // Confirm deletion of the second row.
    const deleteButtons = wrapper.findAll('.delete-btn')
    await deleteButtons[1].trigger('click')
    await flushPromises()
    modalButtons()[1].click()
    await flushPromises()

    // After the mutation rejects, onError rolls the optimistic removal back
    // (the row is present again) and toasts the failure. This fails if the
    // onError handler is dropped — pinning the optimistic+rollback wiring.
    expect(deleteEntityMock).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('Title T-2')
    expect(errorSpy).toHaveBeenCalledTimes(1)
    errorSpy.mockRestore()
    consoleSpy.mockRestore()
    wrapper.unmount()
  })

  it('closes the confirm modal after a failed delete', async () => {
    const entities = [makeEntity('T-1')]
    const wrapper = await mountList(entities)
    deleteEntityMock.mockRejectedValue(new Error('boom'))
    const consoleSpy = vi.spyOn(console, 'error').mockImplementation(() => {})

    await wrapper.find('.delete-btn').trigger('click')
    await flushPromises()

    modalButtons()[1].click()
    await flushPromises()

    // The confirm modal closes (the mutation owns failure handling via
    // rollback + toast, unlike the old withConfirmError stay-open path).
    expect(deleteEntityMock).toHaveBeenCalledTimes(1)
    consoleSpy.mockRestore()
    wrapper.unmount()
  })

  // A string `confirm:` is the dialog text; `true` keeps the default wording
  // (TKT-VVS16W).
  it.each([
    ['string', 'Close these tickets for good?', 'Close these tickets for good?'],
    ['true', true, 'Apply Close to 1 selected entities?'],
  ])('bulk action confirm: %s', async (_name, confirm, want) => {
    const schemaStore = useSchemaStore()
    const wrapper = await mountList([
      { ...makeEntity('T-1'), _actions: { update: true } },
    ])
    schemaStore.lists.set(listId, { ...schemaStore.getList(listId)!, actions: ['close'] } as never)
    schemaStore.actions.set('close', { label: 'Close', key: 'c', script: 'c.lua', confirm })
    await flushPromises()

    await wrapper.find('.select-cell input').setValue(true)
    const btn = wrapper.findAll('.action-header-btn').find((b) => b.text().includes('Close'))
    expect(btn).toBeDefined()
    await btn!.trigger('click')
    await flushPromises()

    expect(overlay()).not.toBeNull()
    expect(overlay()!.textContent).toContain(want)
    wrapper.unmount()
  })
})

describe('EntityList search integration', () => {
  const listId = 'tickets-list'
  const entityType = 'ticket'

  function seedSchema() {
    const schemaStore = useSchemaStore()
    schemaStore.lists.set(listId, {
      id: listId,
      title: 'Tickets',
      entity: entityType,
      columns: [{ property: 'title', label: 'Title' }],
    } as never)
    schemaStore.entityTypes.set(entityType, {
      name: entityType,
      label: 'Ticket',
      properties: { title: { type: 'string', values: null } },
    } as never)
  }

  function fakeFetchList() {
    listEntitiesMock.mockReset().mockResolvedValue({
      data: [],
      meta: { total: 0, page: 1, per_page: 25, has_more: false },
      included: {},
    })
    return listEntitiesMock
  }

  let pinia: ReturnType<typeof createPinia>
  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    _setEntityPluralForTest(entityType, 'tickets')
    deleteEntityMock.mockReset().mockResolvedValue(undefined)
    _resetModalStack()
    routerPush.mockClear()
    routerReplace.mockClear()
    mockRoute.query = {}
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    document.body.innerHTML = ''
    _resetModalStack()
  })

  it('AC3: hydrates the search box from ?q= in the URL', async () => {
    seedSchema()
    fakeFetchList()
    mockRoute.query = { q: 'foo' }

    const wrapper = mount(EntityList, { props: { listId }, attachTo: document.body, global: { plugins: [pinia, PiniaColada] } })
    await flushPromises()

    const input = wrapper.find<HTMLInputElement>('.search-box input[type="search"]')
    expect(input.element.value).toBe('foo')
    wrapper.unmount()
  })

  it('AC2: typing fires exactly one fetch after the debounce window', async () => {
    seedSchema()
    const fetchList = fakeFetchList()
    const wrapper = mount(EntityList, { props: { listId }, attachTo: document.body, global: { plugins: [pinia, PiniaColada] } })
    await flushPromises()

    // Initial mount fetch already happened.
    const initialCallCount = fetchList.mock.calls.length

    const input = wrapper.find<HTMLInputElement>('.search-box input[type="search"]')
    // Type three characters in quick succession.
    for (const ch of ['T', 'TK', 'TKT']) {
      input.element.value = ch
      await input.trigger('input')
    }

    // No fetch yet — still inside the debounce window.
    expect(fetchList.mock.calls.length).toBe(initialCallCount)

    // Advance past the debounce.
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()

    // Exactly one extra fetch fired, with q="TKT" in the params.
    expect(fetchList.mock.calls.length).toBe(initialCallCount + 1)
    const lastCall = fetchList.mock.calls[fetchList.mock.calls.length - 1]
    expect(lastCall[1]).toMatchObject({ q: 'TKT' })

    wrapper.unmount()
  })

  it('AC4: clearing the search restores the unfiltered list and removes q from URL', async () => {
    seedSchema()
    const fetchList = fakeFetchList()
    mockRoute.query = { q: 'foo' }

    const wrapper = mount(EntityList, { props: { listId }, attachTo: document.body, global: { plugins: [pinia, PiniaColada] } })
    await flushPromises()

    // Sanity: initial fetch carries q=foo.
    const before = fetchList.mock.calls[fetchList.mock.calls.length - 1]
    expect(before[1]).toMatchObject({ q: 'foo' })

    // Click the SearchBox clear button.
    await wrapper.find('.search-box .clear-btn').trigger('click')
    await flushPromises()

    // Router was asked to drop `q` from the query.
    expect(routerReplace).toHaveBeenCalled()
    const lastReplace = routerReplace.mock.calls[routerReplace.mock.calls.length - 1][0]
    expect(lastReplace.query.q).toBeUndefined()

    // Subsequent fetch goes out without q.
    await flushPromises()
    const after = fetchList.mock.calls[fetchList.mock.calls.length - 1]
    expect(after[1].q).toBeUndefined()

    wrapper.unmount()
  })
})

// TKT-ODHV2D: incoming relation columns. Verifies getFormattedCellValue is
// direction-aware — an `direction: incoming` column reads the relation's
// inverse key from entity.relations and resolves the source IDs via the
// included map (exactly the same client-side ID→title path as outgoing
// columns). Rendered through the DOM since the function is component-local.
describe('EntityList incoming relation columns', () => {
  const listId = 'taaks'
  const entityType = 'taak'

  // A taak list with an incoming relation column (verantwoordelijk_voor points
  // persoon → taak; its inverse is verantwoordelijken).
  function seedSchema() {
    const schemaStore = useSchemaStore()
    schemaStore.lists.set(listId, {
      id: listId,
      title: 'Taken',
      entity: entityType,
      columns: [
        { property: 'title', label: 'Title' },
        { relation: 'verantwoordelijk_voor', direction: 'incoming', label: 'Verantwoordelijken' },
      ],
    } as never)
    schemaStore.entityTypes.set(entityType, {
      name: entityType,
      label: 'Taak',
      properties: { title: { type: 'string', values: null } },
    } as never)
    // The relation type declares an explicit inverse, so getInverseName
    // returns "verantwoordelijken" — the wire key the backend uses.
    schemaStore.relationTypes.set('verantwoordelijk_voor', {
      label: 'verantwoordelijk voor',
      from: ['persoon'],
      to: ['taak'],
      inverse: { id: 'verantwoordelijken' },
    } as never)
  }

  // Build a list response where the taak row carries incoming sources under
  // the inverse key, and the included map holds those source persoons.
  function seedIncomingResponse(sourceIds: string[]): ListResponse<Entity> {
    const included: Record<string, Entity> = {}
    for (const id of sourceIds) {
      included[id] = { id, type: 'persoon', properties: { title: `Name ${id}` }, _title: `Name ${id}` }
    }
    const response: ListResponse<Entity> = {
      data: [
        {
          id: 'TASK-1',
          type: entityType,
          properties: { title: 'Ship it' },
          relations: { verantwoordelijken: sourceIds },
        },
      ],
      meta: { total: 1, page: 1, per_page: 25, has_more: false },
      included,
    }
    listEntitiesMock.mockResolvedValue(response)
    return response
  }

  let pinia: ReturnType<typeof createPinia>
  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    _setEntityPluralForTest(entityType, 'taaks')
    listEntitiesMock.mockReset()
    deleteEntityMock.mockReset().mockResolvedValue(undefined)
    _resetModalStack()
    routerPush.mockClear()
    routerReplace.mockClear()
    mockRoute.query = {}
  })

  afterEach(() => {
    document.body.innerHTML = ''
    _resetModalStack()
  })

  it('resolves a single incoming source to its title', async () => {
    seedSchema()
    seedIncomingResponse(['PERS-JV'])
    const wrapper = mount(EntityList, {
      props: { listId },
      attachTo: document.body,
      global: { plugins: [pinia, PiniaColada] },
    })
    await flushPromises()

    // The second column cell renders the resolved persoon title.
    const dataCells = wrapper.findAll('tbody tr td:not(.actions-cell):not(.select-cell)')
    const relCell = dataCells[dataCells.length - 1]
    expect(relCell.text()).toBe('Name PERS-JV')
    wrapper.unmount()
  })

  it('resolves multiple incoming sources comma-separated', async () => {
    seedSchema()
    seedIncomingResponse(['PERS-JV', 'PERS-AB'])
    const wrapper = mount(EntityList, {
      props: { listId },
      attachTo: document.body,
      global: { plugins: [pinia, PiniaColada] },
    })
    await flushPromises()

    const dataCells = wrapper.findAll('tbody tr td:not(.actions-cell):not(.select-cell)')
    const relCell = dataCells[dataCells.length - 1]
    expect(relCell.text()).toBe('Name PERS-JV, Name PERS-AB')
    wrapper.unmount()
  })

  it('outgoing lookup key stays empty for an incoming column (no collision)', async () => {
    seedSchema()
    // Row carries edges ONLY under the outgoing relation type, none under the
    // inverse. An incoming column must therefore render empty.
    listEntitiesMock.mockResolvedValue({
      data: [
        {
          id: 'TASK-1',
          type: entityType,
          properties: { title: 'Ship it' },
          relations: { verantwoordelijk_voor: ['PERS-JV'] },
        },
      ],
      meta: { total: 1, page: 1, per_page: 25, has_more: false },
      included: { 'PERS-JV': { id: 'PERS-JV', type: 'persoon', properties: {}, _title: 'Name' } },
    } as ListResponse<Entity>)
    const wrapper = mount(EntityList, {
      props: { listId },
      attachTo: document.body,
      global: { plugins: [pinia, PiniaColada] },
    })
    await flushPromises()

    const dataCells = wrapper.findAll('tbody tr td:not(.actions-cell):not(.select-cell)')
    const relCell = dataCells[dataCells.length - 1]
    expect(relCell.text()).toBe('')
    wrapper.unmount()
  })
})

// Pins the anti-flash contract that TKT-TFSNBY's design depends on: a page
// change must NOT blank the table back to the `.loading-state` spinner.
//
// `loading` is computed from `isPending`, and a page change swaps to a NEW
// query key whose entry starts out `pending` — so the guard is Colada's
// `placeholderData: (prev) => prev`. When placeholder data is present Colada
// overrides the exposed state to `{status: 'success', data: placeholderData}`,
// which keeps `isPending` false and holds the previous page's rows on screen
// until the next page resolves.
//
// Without this test the behaviour is unpinned, and EntityList carried two
// contradictory comments about it (one claiming the spinner does show on a
// param change). Removing `placeholderData`, or gating the template on
// `asyncStatus`/`isLoading` instead of `isPending`, must fail here.
describe('EntityList pagination keeps previous rows (no spinner flash)', () => {
  const listId = 'tickets-list'
  const entityType = 'ticket'

  let pinia: ReturnType<typeof createPinia>
  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    _setEntityPluralForTest(entityType, 'tickets')
    listEntitiesMock.mockReset()
    mockRoute.query = {}
    const schemaStore = useSchemaStore()
    schemaStore.lists.set(listId, {
      id: listId,
      title: 'Tickets',
      entity: entityType,
      page_size: 2,
      columns: [{ property: 'title', label: 'Title' }],
    } as never)
    schemaStore.entityTypes.set(entityType, {
      name: entityType,
      label: 'Ticket',
      properties: { title: { type: 'string', values: null } },
    } as never)
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  function page(ids: string[], pageNo: number, hasMore: boolean): ListResponse<Entity> {
    return {
      data: ids.map((id) => ({ id, type: entityType, properties: { title: `Title ${id}` } })),
      meta: { total: 4, page: pageNo, per_page: 2, has_more: hasMore },
      included: {},
    }
  }

  it('holds page 1 rows on screen while page 2 is in flight', async () => {
    listEntitiesMock.mockResolvedValueOnce(page(['T-1', 'T-2'], 1, true))
    const wrapper = mount(EntityList, {
      props: { listId },
      attachTo: document.body,
      global: { plugins: [pinia, PiniaColada] },
    })
    await flushPromises()
    expect(wrapper.findAll('tbody tr')).toHaveLength(2)
    expect(wrapper.find('.loading-state').exists()).toBe(false)

    // Page 2 resolves only when we say so, so we can observe the in-flight gap.
    let resolvePage2: (r: ListResponse<Entity>) => void = () => {}
    listEntitiesMock.mockImplementationOnce(
      () => new Promise<ListResponse<Entity>>((res) => { resolvePage2 = res })
    )

    await wrapper.findComponent(Pagination).vm.$emit('page-change', 2)
    await flushPromises()

    // THE ASSERTION: mid-flight the old rows are still mounted and no
    // spinner replaced the table.
    expect(wrapper.find('.loading-state').exists()).toBe(false)
    expect(wrapper.findAll('tbody tr')).toHaveLength(2)
    expect(wrapper.text()).toContain('Title T-1')

    resolvePage2(page(['T-3', 'T-4'], 2, false))
    await flushPromises()

    // Page 2's rows are now the real (non-placeholder) data.
    expect(wrapper.find('.loading-state').exists()).toBe(false)
    expect(wrapper.text()).toContain('Title T-3')
    wrapper.unmount()
  })

  it('does show the spinner on the very first load (no previous data to hold)', async () => {
    let resolveFirst: (r: ListResponse<Entity>) => void = () => {}
    listEntitiesMock.mockImplementationOnce(
      () => new Promise<ListResponse<Entity>>((res) => { resolveFirst = res })
    )
    const wrapper = mount(EntityList, {
      props: { listId },
      attachTo: document.body,
      global: { plugins: [pinia, PiniaColada] },
    })
    await flushPromises()

    // Cold load is the one case where the block spinner is correct.
    expect(wrapper.find('.loading-state').exists()).toBe(true)

    resolveFirst(page(['T-1', 'T-2'], 1, true))
    await flushPromises()
    expect(wrapper.find('.loading-state').exists()).toBe(false)
    wrapper.unmount()
  })
})
