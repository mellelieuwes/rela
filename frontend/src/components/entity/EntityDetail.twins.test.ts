import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import EntityDetail from './EntityDetail.vue'
import { useSchemaStore } from '@/stores/schema'
import type { ViewResponse } from '@/api'
import type { Twin } from '@/api/twins'
import type { Entity, PactInfo } from '@/types'

// The header's twin badges (twins stage 1). The route is only asked for a
// type the schema declares pacts for — a project without twins never calls
// it — and each twin renders one badge.

const fetchViewMock = vi.fn()
const listTwinsMock = vi.fn()
// The WRITE boundary: whether a body PATCH leaves the client at all.
const updateEntityMock = vi.fn()

vi.mock('@/api', async (orig) => ({
  ...(await orig<typeof import('@/api')>()),
  fetchView: (...a: unknown[]) => fetchViewMock(...a),
  getCommands: vi.fn(async () => []),
}))
vi.mock('@/api/entities', async (orig) => ({
  ...(await orig<typeof import('@/api/entities')>()),
  updateEntity: (...a: unknown[]) => updateEntityMock(...a),
}))
vi.mock('@/api/twins', () => ({
  listTwins: (...a: unknown[]) => listTwinsMock(...a),
}))
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useRoute: () => ({ query: {}, path: '/entity/scenario/SC-015', name: 'entity' }),
  RouterLink: { props: ['to'], template: '<a><slot /></a>' },
}))

const entityType = 'scenario'
const entityId = 'SC-015'

const view: ViewResponse = {
  entry: {
    id: entityId,
    type: entityType,
    _title: 'Checkout succeeds',
    properties: { title: 'Checkout succeeds' },
    _actions: { update: true },
  },
  sections: [],
}

const basecampTwin: Twin = {
  system: 'basecamp',
  external_id: '123',
  url: 'https://app.basecamp.com/1/todos/123',
  state: 'conflict',
  has_base: true,
  owned_fields: ['*'],
  findings: [],
}

describe('EntityDetail twin badges', () => {
  let pinia: Pinia

  function seedType(pacts?: PactInfo[]) {
    useSchemaStore().entityTypes.set(entityType, {
      name: entityType,
      label: 'Scenario',
      properties: { title: { type: 'string', values: null } },
      pacts,
    } as never)
  }

  async function mountDetail() {
    fetchViewMock.mockResolvedValue(view)
    const wrapper = mount(EntityDetail, {
      props: { entityType, entityId },
      attachTo: document.body,
      global: { plugins: [pinia, PiniaColada] },
    })
    await flushPromises()
    // Anti-vacuity: the page rendered, so an absent badge is about the badge.
    expect(wrapper.text()).toContain('Checkout succeeds')
    return wrapper
  }

  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    fetchViewMock.mockReset()
    listTwinsMock.mockReset().mockResolvedValue([basecampTwin])
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('never asks for twins, and renders no badge, when the type has no pacts', async () => {
    seedType(undefined)
    const w = await mountDetail()
    expect(listTwinsMock).not.toHaveBeenCalled()
    expect(w.find('.twin-badge').exists()).toBe(false)
  })

  it('renders a badge per twin when the type declares a pact', async () => {
    seedType([{ system: 'basecamp', scope: 'https://x.test', theirs: ['*'], shared: [], propose: [] }])
    const w = await mountDetail()
    expect(listTwinsMock).toHaveBeenCalledWith(entityType, entityId)
    const badges = w.findAll('.twin-badge')
    expect(badges).toHaveLength(1)
    expect(badges[0].classes()).toContain('is-conflict')
    expect(badges[0].text()).toContain('basecamp')
  })

  it('degrades to no badge when the twins request fails', async () => {
    seedType([{ system: 'basecamp', scope: 'https://x.test', theirs: ['*'], shared: [], propose: [] }])
    listTwinsMock.mockRejectedValue(new Error('404'))
    const w = await mountDetail()
    expect(w.find('.twin-badge').exists()).toBe(false)
  })
})

// An owned body (`content_writable: false`) renders its task checkboxes, but a
// click must not write: the server answers 422, and an optimistically ticked
// box with nothing behind it reads as a saved change.
describe('EntityDetail with a body an external system owns', () => {
  let pinia: Pinia
  const TASK = '- [ ] a task'

  function bodyView(over: Partial<Entity>): ViewResponse {
    return {
      entry: { ...view.entry, content: TASK, ...over },
      sections: [
        {
          sectionId: 'body',
          display: 'content',
          heading: '',
          isEmpty: false,
          isGrouped: false,
          hasContent: true,
          content: TASK,
        },
      ],
    }
  }

  async function clickTheBox(over: Partial<Entity>) {
    fetchViewMock.mockResolvedValue(bodyView(over))
    const w = mount(EntityDetail, {
      props: { entityType, entityId },
      attachTo: document.body,
      global: { plugins: [pinia, PiniaColada] },
    })
    await flushPromises()
    const box = w.find<HTMLInputElement>('input[type="checkbox"][data-cb-idx]')
    expect(box.exists()).toBe(true)
    await box.trigger('click')
    await flushPromises()
    const ticked = box.element.checked
    // The content channel debounces; unmount flushes it the way navigation does.
    w.unmount()
    await flushPromises()
    return { ticked }
  }

  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    fetchViewMock.mockReset()
    listTwinsMock.mockReset().mockResolvedValue([])
    updateEntityMock.mockReset().mockResolvedValue({ ...view.entry, content: '- [x] a task' })
    useSchemaStore().entityTypes.set(entityType, {
      name: entityType,
      label: 'Scenario',
      properties: { title: { type: 'string', values: null } },
    } as never)
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('refuses a checkbox toggle: no PATCH, and the box stays unticked', async () => {
    const { ticked } = await clickTheBox({ content_writable: false })
    expect(updateEntityMock).not.toHaveBeenCalled()
    expect(ticked).toBe(false)
  })

  // Positive control: the same click on a writable body does write, so the
  // refusal above is about ownership rather than a harness that never saves.
  it('writes the toggle when the body is writable', async () => {
    await clickTheBox({ content_writable: true })
    expect(updateEntityMock).toHaveBeenCalled()
  })
})
