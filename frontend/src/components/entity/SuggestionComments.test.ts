import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import SuggestionDiff from './SuggestionDiff.vue'
import TextCommentPopover from './TextCommentPopover.vue'
import TextSelectionComment from './TextSelectionComment.vue'
import { addComment, checkAnchorable, type Comment } from '@/api/comments'

// Suggested replacements on text comments (TKT-S5C0K3): the composer that
// proposes one, the diff that shows it, and the Accept affordance.

vi.mock('@/api/comments', () => ({
  addComment: vi.fn(),
  updateComment: vi.fn(),
  deleteComment: vi.fn(),
  checkAnchorable: vi.fn(),
}))

const mockAdd = vi.mocked(addComment)
const mockCheck = vi.mocked(checkAnchorable)

function suggestion(over: Partial<Comment> = {}): Comment {
  return {
    id: 'c1',
    author: 'alice@example.com',
    created_at: '2026-09-01T10:00:00Z',
    anchor: { kind: 'text', ref: '', quote: 'old words', replacement: 'new words' },
    body: 'Suggested a change.',
    resolved: false,
    editable: true,
    deletable: true,
    acceptable: true,
    ...over,
  }
}

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
})

describe('SuggestionDiff', () => {
  it('shows both sides as text, never as markup', () => {
    const w = mount(SuggestionDiff, { props: { quote: '**old**', replacement: '<b>new</b>' } })
    expect(w.find('del').text()).toBe('**old**')
    expect(w.find('ins').text()).toBe('<b>new</b>')
    expect(w.find('ins b').exists()).toBe(false)
  })

  it('describes an empty replacement as a deletion', () => {
    const w = mount(SuggestionDiff, { props: { quote: 'old', replacement: '' } })
    expect(w.find('ins').exists()).toBe(false)
    expect(w.text()).toContain('Suggests deleting this text')
  })
})

describe('TextCommentPopover suggestions', () => {
  function mountPopover(comments: Comment[], canAccept: boolean) {
    return mount(TextCommentPopover, {
      props: {
        entityType: 'ticket',
        entityId: 'TKT-001',
        comments,
        position: { top: 0, left: 0 },
        canAccept,
      },
    })
  }

  it('renders the diff instead of the plain quote', () => {
    const w = mountPopover([suggestion()], true)
    expect(w.find('[data-testid="suggestion-diff"]').exists()).toBe(true)
    expect(w.find('.tcp-quote').exists()).toBe(false)
  })

  it('emits accept for an acceptable suggestion', async () => {
    const c = suggestion()
    const w = mountPopover([c], true)
    const btn = w.findAll('button').find((b) => b.text() === 'Accept')
    expect(btn).toBeDefined()
    await btn!.trigger('click')
    expect(w.emitted('accept')?.[0]).toEqual([c])
  })

  it.each([
    ['the user may not accept', suggestion(), false],
    ['the server says it no longer applies', suggestion({ acceptable: false }), true],
    ['it is a plain comment', suggestion({ acceptable: undefined }), true],
  ])('hides Accept when %s', (_name, c, canAccept) => {
    const w = mountPopover([c], canAccept)
    expect(w.findAll('button').some((b) => b.text() === 'Accept')).toBe(false)
  })
})

describe('TextSelectionComment suggesting', () => {
  // jsdom has selections but no layout, so Range geometry is stubbed.
  beforeEach(() => {
    Range.prototype.getBoundingClientRect = () => new DOMRect(0, 0, 10, 10)
  })

  async function composeOver(sourceQuote: string | undefined, suggestable = true) {
    const container = document.createElement('div')
    container.innerHTML = '<p>Some <strong>bold claim</strong> stands here.</p>'
    document.body.appendChild(container)
    mockCheck.mockResolvedValue({ anchorable: true, source_quote: sourceQuote, suggestable })
    mockAdd.mockResolvedValue({} as never)

    const w = mount(TextSelectionComment, {
      props: { entityType: 'ticket', entityId: 'TKT-001', container },
      attachTo: document.body,
    })
    const range = document.createRange()
    range.selectNodeContents(container.querySelector('strong')!)
    window.getSelection()!.removeAllRanges()
    window.getSelection()!.addRange(range)
    document.dispatchEvent(new Event('selectionchange'))
    await vi.waitFor(() => expect(w.find('.tsc-btn').attributes('disabled')).toBeUndefined())
    await w.find('.tsc-btn').trigger('click')
    return w
  }

  it('prefills the replacement with the SOURCE quote and posts it', async () => {
    const w = await composeOver('**bold claim**')
    await w.find('.tsc-suggest').trigger('click')
    const repl = w.find<HTMLTextAreaElement>('#tsc-replacement')
    expect(repl.element.value).toBe('**bold claim**')

    // Unchanged text would change nothing, so it cannot be submitted.
    expect(w.find('.tsc-submit').attributes('disabled')).toBeDefined()

    await repl.setValue('**strong claim**')
    await w.find('form').trigger('submit')
    expect(mockAdd).toHaveBeenCalledWith('ticket', 'TKT-001', {
      anchor: expect.objectContaining({
        kind: 'text',
        quote: 'bold claim',
        replacement: '**strong claim**',
      }),
      body: 'Suggested a change.',
    })
    w.unmount()
  })

  it('offers no suggestion where the server says it would be refused', async () => {
    const w = await composeOver('**bold claim**', false)
    expect(w.find('.tsc-suggest').exists()).toBe(false)
    w.unmount()
  })

  it('keeps a typed replacement when toggled off and on', async () => {
    const w = await composeOver('**bold claim**')
    await w.find('.tsc-suggest').trigger('click')
    await w.find('#tsc-replacement').setValue('edited')
    await w.find('.tsc-suggest').trigger('click')
    await w.find('.tsc-suggest').trigger('click')
    expect(w.find<HTMLTextAreaElement>('#tsc-replacement').element.value).toBe('edited')
    w.unmount()
  })

  it('offers no suggestion without a source quote', async () => {
    const w = await composeOver(undefined)
    expect(w.find('.tsc-suggest').exists()).toBe(false)
    w.unmount()
  })

  it('sends no replacement for a plain comment', async () => {
    const w = await composeOver('**bold claim**')
    await w.find('textarea[aria-label="Comment body"]').setValue('hm')
    await w.find('form').trigger('submit')
    expect(mockAdd.mock.calls[0][2].anchor).not.toHaveProperty('replacement')
    w.unmount()
  })
})
