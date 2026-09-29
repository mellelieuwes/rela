import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import type { EditorState } from '@milkdown/kit/prose/state'
import { TextSelection } from '@milkdown/kit/prose/state'
import MilkdownEditor from './MilkdownEditor.vue'

/**
 * Acceptance tests for the link, divider and history controls.
 *
 * Kept apart from `MilkdownEditor.test.ts`, which is already 800 lines. These
 * drive the real editor rather than a schema stub, so they exercise the wiring
 * the pure tests in `linkUrl.test.ts` and `linkSelection.test.ts` cannot.
 *
 * Layout is not asserted anywhere here: happy-dom has no layout engine, so
 * `getBoundingClientRect` is all zeros and floating-ui cannot place anything.
 * These check structure and document content; position is the e2e suite's job.
 */

const searchEntities = vi.fn()
vi.mock('@/api', () => ({
  searchEntities: (...args: unknown[]) => searchEntities(...args),
  listEntities: vi.fn(),
}))

type EditorVM = {
  guardedValue: () => { value: string; verdict: string }
}

async function mountEditor(props: Record<string, unknown> = {}) {
  const wrapper = mount(MilkdownEditor, {
    props: { modelValue: '', ...props },
    attachTo: document.body,
  })
  await flushPromises()
  await flushPromises()
  return wrapper
}

function markdownOf(w: VueWrapper): string {
  return (w.vm as unknown as EditorVM).guardedValue().value
}

function button(w: VueWrapper, label: string) {
  return w.findAll('.toolbar-button').find((b) => b.attributes('aria-label') === label)
}

/** The live ProseMirror view, via the accessor the sibling editor tests use. */
function editorViewOf(w: VueWrapper) {
  const view = (
    w.vm as unknown as {
      editorViewForTest?: {
        state: EditorState
        dispatch: (tr: unknown) => void
      }
    }
  ).editorViewForTest
  if (!view) throw new Error('editor view not available')
  return view
}

/**
 * Drives the dialog the way a user does: open it, fill it, submit.
 *
 * Returns false when no dialog appeared, so a test asserting a refusal can
 * tell "refused" from "never opened".
 */
async function fillDialog(url: string, text?: string): Promise<boolean> {
  const dialog = document.querySelector('.link-dialog')
  if (!dialog) return false

  const inputs = dialog.querySelectorAll('input')
  // The text field only exists for a collapsed caret, so the URL field is the
  // last input either way.
  const urlInput = inputs[inputs.length - 1] as HTMLInputElement
  if (text !== undefined && inputs.length > 1) {
    const textInput = inputs[0] as HTMLInputElement
    textInput.value = text
    textInput.dispatchEvent(new Event('input', { bubbles: true }))
  }
  urlInput.value = url
  urlInput.dispatchEvent(new Event('input', { bubbles: true }))

  const form = dialog.querySelector('form') as HTMLFormElement
  form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await flushPromises()
  return true
}

function dialogError(): string {
  return document.querySelector('#link-dialog-error')?.textContent?.trim() ?? ''
}

describe('link insertion', () => {
  beforeEach(() => {
    searchEntities.mockReset()
    searchEntities.mockResolvedValue({ data: [] })
  })
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('offers a link button', async () => {
    const w = await mountEditor()
    expect(button(w, 'Link')).toBeTruthy()
    w.unmount()
  })

  it('wraps a selection in a link (AC 1)', async () => {
    const w = await mountEditor({ modelValue: 'documentation here\n' })
    await selectRange(w, 1, 14)

    await button(w, 'Link')!.trigger('click')
    await flushPromises()
    expect(await fillDialog('https://example.com')).toBe(true)

    expect(markdownOf(w)).toContain('[documentation](https://example.com/)')
    w.unmount()
  })

  it('promotes a bare host to https (AC 5)', async () => {
    const w = await mountEditor({ modelValue: 'docs\n' })
    await selectRange(w, 1, 5)

    await button(w, 'Link')!.trigger('click')
    await flushPromises()
    await fillDialog('example.com')

    expect(markdownOf(w)).toContain('(https://example.com/)')
    w.unmount()
  })

  it('keeps a bare host:port (AC 14)', async () => {
    const w = await mountEditor({ modelValue: 'docs\n' })
    await selectRange(w, 1, 5)

    await button(w, 'Link')!.trigger('click')
    await flushPromises()
    await fillDialog('example.com:8080/path')

    expect(markdownOf(w)).toContain('(https://example.com:8080/path)')
    w.unmount()
  })

  it.each([
    ['javascript:alert(1)'],
    ['data:text/html,<script>x</script>'],
    ['/relative/path'],
  ])('refuses %s and writes nothing (AC 4)', async (bad) => {
    const src = 'docs\n'
    const w = await mountEditor({ modelValue: src })
    await selectRange(w, 1, 5)

    await button(w, 'Link')!.trigger('click')
    await flushPromises()
    await fillDialog(bad)

    // The message is shown, the dialog stays open, and the document is
    // untouched — not merely "no link", but no write at all.
    expect(dialogError().length).toBeGreaterThan(0)
    expect(document.querySelector('.link-dialog')).toBeTruthy()
    expect(markdownOf(w)).not.toContain(bad)
    expect(markdownOf(w)).toBe(src)
    w.unmount()
  })

  it('strips mailto parameters (AC 13)', async () => {
    const w = await mountEditor({ modelValue: 'mail\n' })
    await selectRange(w, 1, 5)

    await button(w, 'Link')!.trigger('click')
    await flushPromises()
    await fillDialog('mailto:a@b.com?bcc=evil@x.com')

    const md = markdownOf(w)
    expect(md).toContain('mailto:a@b.com')
    expect(md).not.toContain('bcc')
    w.unmount()
  })

  it('inserts text and link from a collapsed caret', async () => {
    // "start " is 6 characters in a paragraph starting at 1, so the end of the
    // text is position 7 — one past the last valid position in a doc of size 7.
    const w = await mountEditor({ modelValue: 'start \n' })
    await selectRange(w, 6, 6)

    await button(w, 'Link')!.trigger('click')
    await flushPromises()
    await fillDialog('https://example.com', 'the docs')

    expect(markdownOf(w)).toContain('[the docs](https://example.com/)')
    w.unmount()
  })

  it('leaves the caret outside the link, so typing does not extend it', async () => {
    // Moving the caret past the text is not enough on its own: a position on a
    // mark's trailing boundary inherits the marks of the node before it, so
    // the next keystroke joins the link unless the stored marks are cleared.
    const w = await mountEditor({ modelValue: 'ab\n' })
    await selectRange(w, 2, 2)

    await button(w, 'Link')!.trigger('click')
    await flushPromises()
    await fillDialog('https://example.com', 'LINK')

    const view = editorViewOf(w)
    expect(view.state.storedMarks ?? []).toHaveLength(0)

    // Typing at the caret must land outside the link.
    view.dispatch(view.state.tr.insertText('TAIL', view.state.selection.from))
    await flushPromises()

    const md = markdownOf(w)
    expect(md).toContain('[LINK](https://example.com/)')
    expect(md).not.toContain('[LINKTAIL]')
    w.unmount()
  })
})

describe('link retarget and removal', () => {
  beforeEach(() => {
    searchEntities.mockReset()
    searchEntities.mockResolvedValue({ data: [] })
  })
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('retargets rather than destroying a partially overlapped link (AC 11)', async () => {
    // The data-loss case. `toggleMark` would REMOVE the link here, because
    // `removeWhenPresent` tests `.some()`, and the typed URL would vanish
    // with it.
    const w = await mountEditor({ modelValue: 'see [docs](https://old.test/) here\n' })
    // Selection running from the plain text across part of the link.
    await selectRange(w, 2, 8)

    await button(w, 'Link')!.trigger('click')
    await flushPromises()
    await fillDialog('https://new.test/')

    const md = markdownOf(w)
    expect(md).toContain('https://new.test/')
    expect(md).not.toContain('https://old.test/')
    // The link itself survived; the text was not swallowed.
    expect(md).toContain('[docs]')
    w.unmount()
  })

  it('prefills the dialog when editing an existing link', async () => {
    const w = await mountEditor({ modelValue: '[docs](https://old.test/)\n' })
    await selectRange(w, 2, 2)

    await button(w, 'Link')!.trigger('click')
    await flushPromises()

    const inputs = document.querySelectorAll('.link-dialog input')
    const urlInput = inputs[inputs.length - 1] as HTMLInputElement
    expect(urlInput.value).toBe('https://old.test/')
    w.unmount()
  })

  it('removes the link and keeps the text (AC 3)', async () => {
    const w = await mountEditor({ modelValue: '[docs](https://a.test/)\n' })
    await selectRange(w, 2, 2)
    await flushPromises()

    const unlink = button(w, 'Remove link')
    expect(unlink).toBeTruthy()
    await unlink!.trigger('click')
    await flushPromises()

    const md = markdownOf(w)
    expect(md).not.toContain('https://a.test/')
    expect(md).toContain('docs')
    w.unmount()
  })

  it('offers unlink only while the caret is in a link (AC 10)', async () => {
    const w = await mountEditor({ modelValue: 'plain [docs](https://a.test/)\n' })
    await selectRange(w, 1, 1)
    await flushPromises()
    expect(button(w, 'Remove link')).toBeUndefined()

    await selectRange(w, 9, 9)
    await flushPromises()
    expect(button(w, 'Remove link')).toBeTruthy()
    w.unmount()
  })

  // Edit and Remove both write the document. A read-only body hides the
  // toolbar, so the panel was the one link control left that could still
  // change it.
  it('shows no link panel in a read-only body', async () => {
    const src = '[docs](https://a.test/)\n'
    const editable = await mountEditor({ modelValue: src })
    await selectRange(editable, 2, 2)
    // Positive control: the same caret shows the panel when editable.
    expect(editable.find('.link-tooltip').exists()).toBe(true)
    editable.unmount()

    const w = await mountEditor({ modelValue: src, readonly: true })
    await selectRange(w, 2, 2)
    expect(w.find('.link-tooltip').exists()).toBe(false)
    w.unmount()
  })
})

describe('divider and history', () => {
  beforeEach(() => {
    searchEntities.mockReset()
    searchEntities.mockResolvedValue({ data: [] })
  })
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('inserts a divider serializing to --- (AC 7)', async () => {
    const w = await mountEditor({ modelValue: 'before\n' })
    await selectRange(w, 7, 7)

    await button(w, 'Divider')!.trigger('click')
    await flushPromises()

    expect(markdownOf(w)).toContain('---')
    w.unmount()
  })

  it('renders undo and redo as aria-disabled at the ends of the stack (AC 8)', async () => {
    const w = await mountEditor({ modelValue: 'text\n' })
    await flushPromises()

    const undo = button(w, 'Undo')
    const redo = button(w, 'Redo')
    expect(undo).toBeTruthy()
    expect(redo).toBeTruthy()
    // Nothing has been typed, so both ends are empty.
    expect(undo!.attributes('aria-disabled')).toBe('true')
    expect(redo!.attributes('aria-disabled')).toBe('true')
    // Never natively disabled: that drops focus to <body> mid-interaction,
    // and these flip on every transaction.
    expect(undo!.attributes('disabled')).toBeUndefined()
    expect(redo!.attributes('disabled')).toBeUndefined()
    w.unmount()
  })

  it('enables undo once there is something to undo', async () => {
    const w = await mountEditor({ modelValue: 'before\n' })
    await selectRange(w, 7, 7)
    await button(w, 'Divider')!.trigger('click')
    await flushPromises()

    expect(button(w, 'Undo')!.attributes('aria-disabled')).toBe('false')
    w.unmount()
  })
})

describe('pasting a URL', () => {
  beforeEach(() => {
    searchEntities.mockReset()
    searchEntities.mockResolvedValue({ data: [] })
  })
  afterEach(() => {
    document.body.innerHTML = ''
  })

  /** Fires a real paste at the editable element, the way a browser would. */
  async function paste(w: VueWrapper, text: string): Promise<void> {
    const data = new DataTransfer()
    data.setData('text/plain', text)
    const event = new ClipboardEvent('paste', {
      clipboardData: data,
      bubbles: true,
      cancelable: true,
    })
    w.find('.ProseMirror').element.dispatchEvent(event)
    await flushPromises()
  }

  it('wraps the selection rather than replacing it (AC 6)', async () => {
    const w = await mountEditor({ modelValue: 'documentation here\n' })
    await selectRange(w, 1, 14)

    await paste(w, 'https://example.com/a')

    const md = markdownOf(w)
    expect(md).toContain('[documentation](https://example.com/a)')
    // The selected words survived; the URL did not overwrite them.
    expect(md).toContain('documentation')
    w.unmount()
  })

  it('validates a pasted URL through the same gate', async () => {
    const src = 'documentation here\n'
    const w = await mountEditor({ modelValue: src })
    await selectRange(w, 1, 14)

    await paste(w, 'javascript:alert(1)')

    // Not linkified. Whether the text is replaced is ProseMirror's business —
    // what matters is that no link mark was created from a blocked scheme.
    expect(markdownOf(w)).not.toContain('](javascript:')
    w.unmount()
  })

  it('leaves an ordinary paste alone', async () => {
    const w = await mountEditor({ modelValue: 'documentation here\n' })
    await selectRange(w, 1, 14)

    await paste(w, 'replacement text')

    const md = markdownOf(w)
    expect(md).not.toContain('](')
    expect(md).toContain('replacement text')
    w.unmount()
  })

  // The three cases below were all silent data loss: `handlePaste` returned
  // true on conditions it had not actually verified, so ProseMirror's own
  // handling never ran and the clipboard went nowhere.

  it('falls through for a selection spanning two blocks', async () => {
    // Applying one mark across a block boundary yields one link PER BLOCK, so
    // a single pasted URL used to become two links in two paragraphs.
    const w = await mountEditor({ modelValue: 'alpha\n\nbeta\n' })
    await selectRange(w, 2, 10)

    await paste(w, 'https://new.test/')

    const md = markdownOf(w)
    expect(md).not.toContain('](https://new.test/)')
    w.unmount()
  })

  it('pastes as plain text inside a code block', async () => {
    // `code_block` declares `marks: ''`, so no link is possible here. The
    // property that matters is that the paste still HAPPENS: swallowing it
    // (returning true, then applying a mark the block cannot hold) would drop
    // the user's clipboard entirely.
    const w = await mountEditor({ modelValue: '```\nconst x = 1\n```\n' })
    await selectRange(w, 2, 6)

    await paste(w, 'https://new.test/')

    const md = markdownOf(w)
    expect(md).toContain('https://new.test/')
    expect(md).not.toContain('](https://new.test/)')
    w.unmount()
  })

  it('refuses a paste that would partly overwrite an existing link', async () => {
    // Refusing in `linkPasteHref` is not enough: falling through hands the
    // selection to ProseMirror's default paste, which replaces it — shredding
    // the link. Before this was guarded the body became
    // `shttps\://new\.test/[s](https://old.test/) here`: the word "docs" gone,
    // the link reduced to one character, and the URL left as escaped text.
    const src = 'see [docs](https://old.test/) here\n'
    const w = await mountEditor({ modelValue: src })
    await selectRange(w, 2, 8)

    await paste(w, 'https://new.test/')

    const md = markdownOf(w)
    // The existing link survives intact, target and text both.
    expect(md).toContain('[docs](https://old.test/)')
    // And the pasted URL did not land as escaped literal text.
    expect(md).not.toContain('\\:')
    w.unmount()
  })

  it('does not linkify a URL pasted at a collapsed caret', async () => {
    // Nothing to wrap, so this is an ordinary text paste.
    const w = await mountEditor({ modelValue: 'start \n' })
    await selectRange(w, 6, 6)

    await paste(w, 'https://example.com/a')

    expect(markdownOf(w)).not.toContain('](https://example.com/a)')
    w.unmount()
  })
})

describe('pre-existing hostile links', () => {
  beforeEach(() => {
    searchEntities.mockReset()
    searchEntities.mockResolvedValue({ data: [] })
  })
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('leaves a stored javascript: link byte-identical (AC 12)', async () => {
    // Deliberate: the gate is on NEW input only. Rewriting an author's stored
    // body on open would break the losslessness contract that
    // `rawHtmlPassthrough.test.ts` pins — a data-integrity bug wearing a
    // security fix's clothes. The URL is defanged at every render sink
    // instead, which the sibling test asserts.
    const src = '[click](javascript:alert(1))\n'
    const w = await mountEditor({ modelValue: src })

    expect(markdownOf(w)).toBe(src)
    expect(w.emitted('update:modelValue')).toBeUndefined()
    w.unmount()
  })

  it('still refuses to CREATE one', async () => {
    const w = await mountEditor({ modelValue: 'docs\n' })
    await selectRange(w, 1, 5)
    await button(w, 'Link')!.trigger('click')
    await flushPromises()
    await fillDialog('javascript:alert(1)')

    expect(markdownOf(w)).toBe('docs\n')
    w.unmount()
  })
})

/**
 * Moves the selection in the live document.
 *
 * Uses `editorViewForTest`, the accessor the sibling editor tests already use
 * to reach the ProseMirror view.
 */
async function selectRange(w: VueWrapper, from: number, to: number): Promise<void> {
  const view = (
    w.vm as unknown as {
      editorViewForTest?: {
        state: EditorState
        dispatch: (tr: unknown) => void
        focus: () => void
      }
    }
  ).editorViewForTest
  if (!view) throw new Error('editor view not available')
  view.focus()
  view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, from, to)))
  await flushPromises()
}
