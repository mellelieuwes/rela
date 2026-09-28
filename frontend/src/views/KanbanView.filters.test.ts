import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import KanbanView from './KanbanView.vue'
import FilterBar from '@/components/lists/FilterBar.vue'
import { useSchemaStore } from '@/stores/schema'
import { _setEntityPluralForTest } from '@/api/entities'
import type { Entity, ListResponse } from '@/types'

// A board's filter controls are the list's: FilterBar, URL sync, and
// server-side `filter[...]` params. The board used to filter client-side on
// `entity.properties[control.property]`, so a `relation:` control could never
// match anything — its dropdown offered only "All" and the board ignored it.

const listAllEntitiesMock = vi.fn()
vi.mock('@/api', async (orig) => ({
  ...(await orig<typeof import('@/api')>()),
  listAllEntities: (...args: unknown[]) => listAllEntitiesMock(...args),
  updateEntity: vi.fn().mockResolvedValue(undefined),
}))

const mockRoute: { query: Record<string, unknown>; path: string } = {
  query: {},
  path: '/kanban/board',
}
const routerReplace = vi.fn()
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn(), replace: routerReplace }),
  useRoute: () => mockRoute,
  RouterLink: {
    props: ['to'],
    template: '<a :data-to="JSON.stringify(to)"><slot /></a>',
  },
}))

vi.mock('@/composables/useBackTarget', () => ({
  useBackTarget: () => null,
}))

const KANBAN_ID = 'board'
const ENTITY_TYPE = 'taak'

function seedSchema() {
  const schemaStore = useSchemaStore()
  schemaStore.kanbans.set(KANBAN_ID, {
    entity: ENTITY_TYPE,
    title: 'Taken',
    column_property: 'status',
    columns: [{ value: 'todo', label: 'Te doen' }],
    card: { title: 'title' },
    filter_controls: [
      { relation: 'toegewezen_aan', direction: 'outgoing', label: 'Toegewezen aan' },
    ],
  } as never)
  schemaStore.entityTypes.set(ENTITY_TYPE, {
    name: ENTITY_TYPE,
    label: 'Taak',
    properties: {
      title: { type: 'string' },
      status: { type: 'enum', values: ['todo'] },
    },
  } as never)
  return schemaStore
}

function card(id: string, title: string): Entity {
  return {
    id,
    type: ENTITY_TYPE,
    properties: { title, status: 'todo' },
    relations: {},
    _actions: { update: true, create: true },
  } as Entity
}

async function mountBoard() {
  seedSchema()
  const response: ListResponse<Entity> = {
    data: [card('TAAK-1', 'Werk van Anna')],
    meta: { total: 1, page: 1, per_page: 100, has_more: false },
    included: {},
    _actions: { create: true },
  } as never
  listAllEntitiesMock.mockResolvedValue(response)
  const wrapper = mount(KanbanView, {
    props: { id: KANBAN_ID },
    attachTo: document.body,
    global: { plugins: [pinia, PiniaColada] },
  })
  await flushPromises()
  return wrapper
}

let pinia: Pinia
beforeEach(() => {
  pinia = createPinia()
  setActivePinia(pinia)
  _setEntityPluralForTest(ENTITY_TYPE, 'taken')
  listAllEntitiesMock.mockReset()
  routerReplace.mockReset()
  mockRoute.query = {}
})

afterEach(() => {
  document.body.innerHTML = ''
})

describe('KanbanView filter controls', () => {
  it('sends a relation filter from the URL to the server and shows what it returns', async () => {
    mockRoute.query = { 'filter[toegewezen_aan]': 'Anna' }

    const wrapper = await mountBoard()

    const params = listAllEntitiesMock.mock.calls[0][1]
    expect(params?.['filter[toegewezen_aan]']).toBe('Anna')
    // The server already filtered: the board must not drop the card again
    // client-side (the card has no `toegewezen_aan` property).
    expect(wrapper.text()).toContain('Werk van Anna')
  })

  it('keeps the cards on screen while a filter change reloads the board', async () => {
    const wrapper = await mountBoard()
    listAllEntitiesMock.mockReturnValue(new Promise(() => {}))

    wrapper.findComponent(FilterBar).vm.$emit('filter', { toegewezen_aan: { value: 'Bram' } })
    await flushPromises()

    const calls = listAllEntitiesMock.mock.calls
    expect(calls[calls.length - 1][1]?.['filter[toegewezen_aan]']).toBe('Bram')
    expect(wrapper.text()).not.toContain('Loading board')
    expect(wrapper.text()).toContain('Werk van Anna')
  })
})
