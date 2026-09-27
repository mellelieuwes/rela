import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import EntityDetail from './EntityDetail.vue'
import CommentsPanel from './CommentsPanel.vue'
import { useSchemaStore } from '@/stores/schema'
import { useUIStore } from '@/stores/ui'
import type { ViewResponse } from '@/api'
import type { Comment } from '@/api/comments'
import type { CommitResult } from '@/composables/useAutoSave'

// Accepting a suggestion from the detail page (TKT-S5C0K3). The server writes
// the body, so the page must first settle its own pending body save: one that
// lands after the accept would put the pre-accept text back.

const fetchViewMock = vi.fn()
const acceptMock = vi.fn()
const commitMock = vi.fn<() => Promise<CommitResult>>()

vi.mock('@/api', async (orig) => ({
  ...(await orig<typeof import('@/api')>()),
  fetchView: (...a: unknown[]) => fetchViewMock(...a),
  getCommands: vi.fn().mockResolvedValue([]),
}))
vi.mock('@/api/comments', async (orig) => ({
  ...(await orig<typeof import('@/api/comments')>()),
  listComments: vi.fn().mockResolvedValue([]),
  acceptComment: (...a: unknown[]) => acceptMock(...a),
}))
// The real autosave, with only the flush made controllable.
vi.mock('@/composables/useAutoSave', async (orig) => {
  const m = await orig<typeof import('@/composables/useAutoSave')>()
  return {
    ...m,
    useAutoSave: (opts: Parameters<typeof m.useAutoSave>[0]) => ({
      ...m.useAutoSave(opts),
      commitImmediately: () => commitMock(),
    }),
  }
})
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useRoute: () => ({ query: {}, path: '/entity/ticket/TKT-1', name: 'entity' }),
  RouterLink: { props: ['to'], template: '<a><slot /></a>' },
}))

const OLD = 'The old sentence stands here.'
const NEW = 'The new sentence stands here.'

function view(content: string, contentWritable?: boolean): ViewResponse {
  const v: ViewResponse = {
    entry: {
      id: 'TKT-1',
      type: 'ticket',
      _title: 'Ticket',
      properties: { title: 'Ticket' },
      content,
      _actions: { update: true },
      content_writable: contentWritable,
    },
    sections: [
      {
        sectionId: 'body',
        display: 'content',
        heading: '',
        isEmpty: false,
        isGrouped: false,
        hasContent: true,
        content,
      },
    ],
  }
  return v
}

const suggestion: Comment = {
  id: 'c1',
  author: 'alice',
  created_at: '2026-09-01T10:00:00Z',
  anchor: { kind: 'text', ref: '', quote: 'old', replacement: 'new' },
  body: 'Suggested a change.',
  resolved: false,
  editable: true,
  deletable: true,
  acceptable: true,
}

describe('EntityDetail accepting a suggestion', () => {
  let pinia: ReturnType<typeof createPinia>

  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    useSchemaStore().entityTypes.set('ticket', {
      name: 'ticket',
      label: 'Ticket',
      properties: { title: { type: 'string' } },
      commentable: true,
    } as never)
    fetchViewMock.mockReset().mockResolvedValue(view(OLD))
    acceptMock.mockReset().mockResolvedValue({ content: NEW })
    commitMock.mockReset().mockResolvedValue({ settled: true })
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  async function mountAndAccept() {
    const w = mount(EntityDetail, {
      props: { entityType: 'ticket', entityId: 'TKT-1' },
      attachTo: document.body,
      global: { plugins: [pinia, PiniaColada] },
    })
    await flushPromises()
    expect(w.find('.content-body').text()).toContain(OLD)
    // The reload after accepting never resolves here, so the body on screen
    // can only have come from the accept response.
    fetchViewMock.mockReturnValue(new Promise(() => {}))
    w.findComponent(CommentsPanel).vm.$emit('accept', suggestion)
    await flushPromises()
    return w
  }

  it('settles pending saves, accepts, and shows the new body', async () => {
    const w = await mountAndAccept()
    expect(commitMock).toHaveBeenCalled()
    expect(acceptMock).toHaveBeenCalledWith('ticket', 'TKT-1', 'c1')
    expect(commitMock.mock.invocationCallOrder[0]).toBeLessThan(
      acceptMock.mock.invocationCallOrder[0]
    )
    expect(w.find('.content-body').text()).toContain(NEW)
  })

  it.each([
    ['timed out', { settled: false }],
    ['failed', { settled: true, error: 'boom' }],
  ])('does not accept when the pending save %s', async (_name, result) => {
    commitMock.mockResolvedValue(result)
    const errorSpy = vi.spyOn(useUIStore(), 'error')
    const w = await mountAndAccept()
    expect(acceptMock).not.toHaveBeenCalled()
    expect(errorSpy).toHaveBeenCalled()
    expect(w.find('.content-body').text()).toContain(OLD)
  })

  // An external system owns the body through a twin: the server would refuse
  // the accept with 422, so the page neither offers nor performs it.
  it('neither offers nor performs an accept when the body is owned', async () => {
    fetchViewMock.mockResolvedValue(view(OLD, false))
    const w = await mountAndAccept()
    expect(w.findComponent(CommentsPanel).props('canAccept')).toBe(false)
    expect(commitMock).not.toHaveBeenCalled()
    expect(acceptMock).not.toHaveBeenCalled()
    expect(w.find('.content-body').text()).toContain(OLD)
  })
})
