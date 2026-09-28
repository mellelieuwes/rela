import { describe, it, expect, vi, beforeEach, afterEach, type MockInstance } from 'vitest'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import EntityDetail from './EntityDetail.vue'
import { useSchemaStore } from '@/stores/schema'
import { useUIStore } from '@/stores/ui'
import type { ActionConfig, Entity } from '@/types'
import type { ViewResponse } from '@/api'

// Lua actions on the detail page (TKT-VVS16W). The server decides which
// actions apply and publishes each as `_actions['action:<id>']`; the page
// renders those keys, confirms when asked, runs the action against the served
// face address, toasts the script's message and reloads the entity.

const fetchViewMock = vi.fn()
const runActionMock = vi.fn()

vi.mock('@/api', async (orig) => ({
  ...(await orig<typeof import('@/api')>()),
  fetchView: (...a: unknown[]) => fetchViewMock(...a),
  getCommands: async () => [],
}))
vi.mock('@/api/actions', () => ({
  runAction: (...a: unknown[]) => runActionMock(...a),
}))
type ConfirmOpts = { title: string; message: string; confirmLabel?: string }
const confirmMock = vi.fn<(opts: ConfirmOpts) => Promise<boolean>>(async () => true)
vi.mock('@/composables/useConfirm', () => ({
  useConfirm: () => ({ confirm: (opts: ConfirmOpts) => confirmMock(opts) }),
  withConfirmError: (fn: unknown) => fn,
}))
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useRoute: () => ({ query: {}, path: '/entity/document/DOC-1', name: 'entity' }),
  RouterLink: { props: ['to'], template: '<a><slot /></a>' },
}))

const entityType = 'document'
const entityId = 'DOC-1'

function viewResponse(actions: Record<string, boolean>): ViewResponse {
  const entry: Entity = {
    id: entityId,
    type: entityType,
    _title: 'Statement of applicability',
    _self: '/api/v1/documents/DOC-1@concept',
    properties: { title: 'Statement of applicability' },
    content: 'old',
    _actions: { update: true, ...actions },
  }
  return { entry, sections: [] }
}

describe('EntityDetail detail actions', () => {
  let pinia: ReturnType<typeof createPinia>
  let successMock: MockInstance

  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    const schema = useSchemaStore()
    schema.entityTypes.set(entityType, {
      name: entityType,
      label: 'Document',
      properties: { title: { type: 'string', values: null } },
    } as never)
    schema.actions = new Map<string, ActionConfig>([
      [
        'regenerate-soa',
        { label: 'Regenerate', script: 'r.lua', confirm: 'Overwrite the concept?' },
      ],
      ['notify', { label: 'Notify owner', script: 'n.lua' }],
      ['archive', { label: 'Archive', script: 'a.lua', confirm: true }],
    ])
    fetchViewMock.mockReset()
    runActionMock.mockReset().mockResolvedValue({ message: 'Regenerated' })
    confirmMock.mockReset().mockResolvedValue(true)
    successMock = vi.spyOn(useUIStore(), 'success')
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  async function mountDetail(view: ViewResponse) {
    fetchViewMock.mockResolvedValue(view)
    const wrapper = mount(EntityDetail, {
      props: { entityType, entityId },
      attachTo: document.body,
      global: { plugins: [pinia, PiniaColada] },
    })
    await flushPromises()
    expect(wrapper.text()).toContain('Statement of applicability')
    return wrapper
  }

  function desktopButton(wrapper: VueWrapper, label: string) {
    return wrapper.findAll('.desktop-actions button').find((b) => b.text().trim() === label)
  }

  it('renders only the action keys the server sent as true', async () => {
    const w = await mountDetail(
      viewResponse({ 'action:regenerate-soa': true, 'action:notify': false })
    )
    expect(desktopButton(w, 'Regenerate')).toBeDefined()
    expect(desktopButton(w, 'Notify owner')).toBeUndefined()
    // A key for an action the config does not declare renders nothing.
    expect(desktopButton(w, 'Archive')).toBeUndefined()
  })

  it('offers the actions in the mobile overflow menu too', async () => {
    const w = await mountDetail(viewResponse({ 'action:regenerate-soa': true }))
    await w.find('.mobile-overflow-btn').trigger('click')
    const items = w.findAll('.overflow-menu-item').map((b) => b.text().trim())
    expect(items).toContain('Regenerate')
  })

  it('shows the confirm text, runs against the served face, toasts and reloads', async () => {
    const w = await mountDetail(viewResponse({ 'action:regenerate-soa': true }))
    expect(fetchViewMock).toHaveBeenCalledTimes(1)

    await desktopButton(w, 'Regenerate')!.trigger('click')
    await flushPromises()

    expect(confirmMock).toHaveBeenCalledWith(
      expect.objectContaining({ title: 'Regenerate?', message: 'Overwrite the concept?' })
    )
    expect(runActionMock).toHaveBeenCalledWith('regenerate-soa', 'DOC-1@concept')
    expect(successMock).toHaveBeenCalledWith('Regenerated')
    expect(fetchViewMock).toHaveBeenCalledTimes(2)
  })

  it('uses a default message for confirm: true', async () => {
    const w = await mountDetail(viewResponse({ 'action:archive': true }))
    await desktopButton(w, 'Archive')!.trigger('click')
    await flushPromises()
    expect(confirmMock).toHaveBeenCalledWith(
      expect.objectContaining({ message: 'Run Archive on this entity?' })
    )
  })

  it('does not run when the confirm is declined', async () => {
    confirmMock.mockResolvedValue(false)
    const w = await mountDetail(viewResponse({ 'action:regenerate-soa': true }))
    await desktopButton(w, 'Regenerate')!.trigger('click')
    await flushPromises()
    expect(runActionMock).not.toHaveBeenCalled()
    expect(fetchViewMock).toHaveBeenCalledTimes(1)
  })

  it('runs without a prompt when confirm is not set', async () => {
    const w = await mountDetail(viewResponse({ 'action:notify': true }))
    await desktopButton(w, 'Notify owner')!.trigger('click')
    await flushPromises()
    expect(confirmMock).not.toHaveBeenCalled()
    expect(runActionMock).toHaveBeenCalledWith('notify', 'DOC-1@concept')
  })
})
