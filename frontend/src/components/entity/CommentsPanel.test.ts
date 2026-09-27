import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import CommentsPanel from './CommentsPanel.vue'
import { useSchemaStore } from '@/stores/schema'
import { addComment, updateComment, deleteComment, type Comment } from '@/api/comments'
import type { EntityType } from '@/types'

// The confirm dialog is a singleton composable backed by a modal mounted in
// App.vue; mocking it keeps the delete tests about the delete path rather than
// about dialog plumbing.
const mockConfirm = vi.fn<() => Promise<boolean>>()
vi.mock('@/composables/useConfirm', () => ({
  useConfirm: () => ({ confirm: mockConfirm }),
}))

vi.mock('@/api/comments', () => ({
  addComment: vi.fn(),
  updateComment: vi.fn(),
  deleteComment: vi.fn(),
}))

const mockAdd = vi.mocked(addComment)
const mockUpdate = vi.mocked(updateComment)
const mockDelete = vi.mocked(deleteComment)

const ENTITY_TYPE = 'ticket'
const ENTITY_ID = 'TKT-001'

function comment(over: Partial<Comment> = {}): Comment {
  return {
    id: 'c1',
    author: 'alice@example.com',
    created_at: '2026-09-01T10:00:00Z',
    anchor: { kind: 'property', ref: 'status' },
    body: 'looks wrong',
    resolved: false,
    editable: true,
    deletable: true,
    ...over,
  }
}

/** Registers the entity type, defaulting to commentable. */
function seedType(over: Partial<EntityType> = {}) {
  const schemaStore = useSchemaStore()
  schemaStore.entityTypes = new Map([
    [
      ENTITY_TYPE,
      {
        label: 'Ticket',
        properties: { title: { type: 'string' }, status: { type: 'string' } },
        commentable: true,
        ...over,
      },
    ],
  ]) as never
}

/**
 * Mounts the panel and expands it.
 *
 * The panel is collapsed by default — property comments now render at their own
 * field, so this surface exists for the ones with no field row. `expand: false`
 * is for the tests that assert the collapsed header itself.
 */
async function mountPanel(sectionIds: string[] = [], expand = true, comments: Comment[] = []) {
  const w = mount(CommentsPanel, {
    props: { entityType: ENTITY_TYPE, entityId: ENTITY_ID, comments, sectionIds },
  })
  await vi.waitFor(() => expect(w.vm).toBeTruthy())
  await w.vm.$nextTick()
  if (expand && w.find('.panel-header').exists()) {
    await w.find('.panel-header').trigger('click')
  }
  await w.vm.$nextTick()
  await w.vm.$nextTick()
  return w
}

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  mockConfirm.mockResolvedValue(true)
})

describe('CommentsPanel', () => {
  it('renders nothing for a type the schema does not mark commentable', async () => {
    seedType({ commentable: false })
    const w = await mountPanel()

    expect(w.find('.comments-panel').exists()).toBe(false)
  })

  it("lists a commentable type's thread", async () => {
    seedType()
    const existing = comment({ body: 'the status is stale' })

    const w = await mountPanel([], true, [existing])

    expect(w.find('.comment-body').text()).toBe(existing.body)
    expect(w.find('.comment-author').text()).toBe(existing.author)
    expect(w.find('.comment-anchor').text()).toBe(existing.anchor.ref)
  })

  it('offers both property and section anchors', async () => {
    seedType()
    const w = await mountPanel(['history'])

    const values = w.findAll('.anchor-select option').map((o) => o.attributes('value'))
    expect(values).toContain('property:status')
    expect(values).toContain('section:history')
  })

  it('posts a comment with the selected anchor and clears the box', async () => {
    seedType()
    const created = comment({ id: 'c2', body: 'needs a second look' })
    mockAdd.mockResolvedValue(created)

    const w = await mountPanel()
    await w.find('.anchor-select').setValue('property:title')
    await w.find('.comment-input').setValue(created.body)
    await w.find('.comment-form').trigger('submit')
    await vi.waitFor(() => expect(mockAdd).toHaveBeenCalled())
    await w.vm.$nextTick()

    expect(mockAdd).toHaveBeenCalledWith(ENTITY_TYPE, ENTITY_ID, {
      anchor: { kind: 'property', ref: 'title' },
      body: created.body,
    })
    // The parent owns the list, so the panel reports the change rather than
    // splicing its own copy — which is what kept it in sync with the
    // per-field indicators.
    expect(w.emitted('changed')).toHaveLength(1)
    expect((w.find('.comment-input').element as HTMLTextAreaElement).value).toBe('')
  })

  it('resolves a comment without echoing its body back', async () => {
    seedType()
    const existing = comment()
    mockUpdate.mockResolvedValue()

    const w = await mountPanel([], true, [existing])
    const resolveBtn = w.findAll('button').find((b) => b.text() === 'Resolve')
    await resolveBtn?.trigger('click')
    await vi.waitFor(() => expect(mockUpdate).toHaveBeenCalled())
    await w.vm.$nextTick()

    expect(mockUpdate).toHaveBeenCalledWith(ENTITY_TYPE, ENTITY_ID, existing.id, { resolved: true })
    expect(w.emitted('changed')).toHaveLength(1)
  })

  it('deletes a comment once confirmed', async () => {
    seedType()
    const existing = comment()
    mockDelete.mockResolvedValue()

    const w = await mountPanel([], true, [existing])
    const deleteBtn = w.findAll('button').find((b) => b.text() === 'Delete')
    await deleteBtn?.trigger('click')
    await vi.waitFor(() => expect(mockDelete).toHaveBeenCalled())
    await w.vm.$nextTick()

    expect(mockDelete).toHaveBeenCalledWith(ENTITY_TYPE, ENTITY_ID, existing.id)
    expect(w.emitted('changed')).toHaveLength(1)
  })

  it('keeps a comment when the delete confirmation is declined', async () => {
    seedType()
    mockConfirm.mockResolvedValue(false)

    const w = await mountPanel([], true, [comment()])
    await w
      .findAll('button')
      .find((b) => b.text() === 'Delete')
      ?.trigger('click')
    await vi.waitFor(() => expect(mockConfirm).toHaveBeenCalled())
    await w.vm.$nextTick()

    expect(mockDelete).not.toHaveBeenCalled()
    expect(w.find('.comment').exists()).toBe(true)
  })

  it('flags a detached anchor without hiding the comment', async () => {
    seedType()
    const orphan = comment({ detached: true, anchor: { kind: 'property', ref: 'removed_field' } })

    const w = await mountPanel([], true, [orphan])

    expect(w.find('.comment-detached').exists()).toBe(true)
    expect(w.find('.comment-body').text()).toBe(orphan.body)
  })

  it('hides controls the server did not grant', async () => {
    seedType()
    const w = await mountPanel([], true, [comment({ editable: false, deletable: false })])

    const labels = w.findAll('.comment-actions button').map((b) => b.text())
    expect(labels).toEqual([])
  })

  it('renders the empty state when there is nothing to show', async () => {
    seedType()

    const w = await mountPanel()

    expect(w.find('.comment').exists()).toBe(false)
    expect(w.text()).toContain('No comments yet')
  })
})

describe('CommentsPanel collapsing', () => {
  it('is collapsed by default and summarises what is folded away', async () => {
    seedType()
    const w = await mountPanel([], false, [
      comment({ id: 'p1', anchor: { kind: 'property', ref: 'status' } }),
      comment({ id: 's1', anchor: { kind: 'section', ref: 'details' } }),
      comment({ id: 'd1', anchor: { kind: 'property', ref: 'gone' }, detached: true }),
    ])

    // Collapsed: the header is there, the thread is not.
    expect(w.find('.panel-header').exists()).toBe(true)
    expect(w.find('.comment').exists()).toBe(false)

    // The summary counts comments with no field row of their own — the
    // section-anchored one and the detached one, not the plain property one.
    expect(w.find('.panel-summary').text()).toContain('3 total')
    expect(w.find('.panel-summary').text()).toContain('2 not on a field')
  })

  it('expands on click', async () => {
    seedType()
    const w = await mountPanel([], false, [comment()])
    expect(w.find('.comment').exists()).toBe(false)

    await w.find('.panel-header').trigger('click')
    await w.vm.$nextTick()

    expect(w.find('.comment').exists()).toBe(true)
  })
})

describe('CommentsPanel is not a second source of truth', () => {
  // Regression: the panel used to fetch its own copy of the thread. Deleting a
  // comment from a field popover refreshed the indicators but left the panel
  // rendering the deleted row until a page reload — two lists, one of them
  // stale. Comments now arrive as a prop, so there is nothing to diverge.
  it('never fetches; it renders exactly the comments it is given', async () => {
    seedType()
    const given = [comment({ id: 'only-one', body: 'from the parent' })]

    const w = await mountPanel([], true, given)

    expect(w.findAll('.comment')).toHaveLength(1)
    expect(w.find('.comment-body').text()).toBe(given[0].body)
  })

  it('re-renders when the parent replaces the list', async () => {
    seedType()
    const w = await mountPanel([], true, [comment({ id: 'a' }), comment({ id: 'b' })])
    expect(w.findAll('.comment')).toHaveLength(2)

    // What a parent reload after a delete looks like.
    await w.setProps({ comments: [comment({ id: 'a' })] })

    expect(w.findAll('.comment')).toHaveLength(1)
  })
})

describe('CommentsPanel suggestions (TKT-S5C0K3)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    seedType()
  })

  const sugg = comment({
    anchor: { kind: 'text', ref: '', quote: 'old words', replacement: 'new words' },
    acceptable: true,
  })

  it('shows the diff and emits accept', async () => {
    const w = mount(CommentsPanel, {
      props: { entityType: ENTITY_TYPE, entityId: ENTITY_ID, comments: [sugg], canAccept: true },
    })
    await w.find('.panel-header').trigger('click')
    expect(w.find('[data-testid="suggestion-diff"]').exists()).toBe(true)
    const btn = w.findAll('button').find((b) => b.text() === 'Accept')
    await btn!.trigger('click')
    expect(w.emitted('accept')?.[0]).toEqual([sugg])
  })

  it('hides Accept without canAccept', async () => {
    const w = mount(CommentsPanel, {
      props: { entityType: ENTITY_TYPE, entityId: ENTITY_ID, comments: [sugg] },
    })
    await w.find('.panel-header').trigger('click')
    expect(w.findAll('button').some((b) => b.text() === 'Accept')).toBe(false)
  })
})
