import { describe, it, expect, vi, afterEach } from 'vitest'
import { Editor, rootCtx, defaultValueCtx, editorViewCtx } from '@milkdown/kit/core'
import { commonmark } from '@milkdown/kit/preset/commonmark'
import { $prose } from '@milkdown/kit/utils'
import { TextSelection } from '@milkdown/kit/prose/state'
import type { EditorView } from '@milkdown/kit/prose/view'
import { entityRefNode } from './entityRefNode'
import { armedAt, armedToken, disarmMention, mentionArmPlugin } from './mentionArm'
import { replaceMentionQueryWithRef, replaceMentionQueryWithText } from './insertEntityRef'

let editor: Editor | null = null

/** Mounts a real editor over `initial` with the arm plugin, cursor at the end. */
async function mount(initial = 'see', onBlur = vi.fn()): Promise<EditorView> {
  const root = document.createElement('div')
  document.body.appendChild(root)
  editor = await Editor.make()
    .config((ctx) => {
      ctx.set(rootCtx, root)
      ctx.set(defaultValueCtx, initial)
    })
    .use(commonmark)
    .use(entityRefNode)
    .use(
      $prose(() =>
        mentionArmPlugin({
          scopeTypeFor: (name) => (name === 'ticket' ? 'ticket' : null),
          onBlur,
        })
      )
    )
    .create()
  const view = editor.action((ctx) => ctx.get(editorViewCtx))
  const end = view.state.doc.content.size - 1
  view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, end)))
  return view
}

afterEach(async () => {
  await editor?.destroy()
  editor = null
  document.body.innerHTML = ''
})

/** Types `text` the way a keyboard does: through `handleTextInput` first. */
function type(view: EditorView, text: string): void {
  for (const ch of text) {
    const { from, to } = view.state.selection
    const handled = view.someProp('handleTextInput', (f) =>
      f(view, from, to, ch, () => view.state.tr)
    )
    if (!handled) view.dispatch(view.state.tr.insertText(ch, from, to))
  }
}

/** Inserts text without `handleTextInput`, as a paste or an undo does. */
function paste(view: EditorView, text: string): void {
  const { from, to } = view.state.selection
  view.dispatch(view.state.tr.insertText(text, from, to))
}

function paragraphText(view: EditorView): string {
  return view.state.doc.firstChild?.textContent ?? ''
}

describe('mentionArmPlugin', () => {
  it('arms on a typed @ and follows the query', async () => {
    const view = await mount()
    type(view, ' @')
    const at = armedAt(view.state)
    expect(at).not.toBeNull()
    type(view, 'tk')
    expect(armedAt(view.state)).toBe(at)
  })

  it('does not arm after a word character, as in an email address', async () => {
    const view = await mount('see')
    type(view, ' mail@ticket:')
    expect(armedAt(view.state)).toBeNull()
    expect(view.dom.querySelector('.mention-scope-chip')).toBeNull()
  })

  it('does not arm on a pasted @', async () => {
    const view = await mount()
    paste(view, '@tkt')
    expect(armedAt(view.state)).toBeNull()
  })

  it('lets go at a space', async () => {
    const view = await mount()
    type(view, ' @tk ')
    expect(armedAt(view.state)).toBeNull()
  })

  it('lets go when the cursor leaves the token, and stays released on return', async () => {
    const view = await mount()
    type(view, ' @tk')
    const end = view.state.selection.from
    view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, 1)))
    expect(armedAt(view.state)).toBeNull()
    view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, end)))
    expect(armedAt(view.state)).toBeNull()
  })

  it('lets go when the @ is deleted', async () => {
    const view = await mount()
    type(view, ' @')
    const { from } = view.state.selection
    view.dispatch(view.state.tr.delete(from - 1, from))
    expect(armedAt(view.state)).toBeNull()
  })

  it('stays armed when text is inserted before the @', async () => {
    const view = await mount()
    type(view, ' @tk')
    const at = armedAt(view.state)!
    const tr = view.state.tr.insertText('xx', 1)
    view.dispatch(tr.setSelection(TextSelection.create(tr.doc, view.state.selection.from + 2)))
    expect(armedAt(view.state)).toBe(at + 2)
  })

  it('lets go and reports on blur', async () => {
    const onBlur = vi.fn()
    const view = await mount('see', onBlur)
    type(view, ' @')
    view.dom.dispatchEvent(new FocusEvent('blur'))
    expect(armedAt(view.state)).toBeNull()
    expect(onBlur).toHaveBeenCalled()
  })

  it('draws a chip over a known `name:` scope only', async () => {
    const view = await mount()
    type(view, ' @ticket:fa')
    const chip = view.dom.querySelector('.mention-scope-chip')
    expect(chip?.textContent).toBe('@ticket:')
    disarmMention(view)
    expect(view.dom.querySelector('.mention-scope-chip')).toBeNull()
  })

  it('does not arm inside inline code', async () => {
    // A code span holding an id would parse as an entity reference.
    const view = await mount('see `a b`')
    // Position 6 sits inside the code mark, between `a` and ` b`.
    view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, 6)))
    type(view, ' @')
    expect(armedAt(view.state)).toBeNull()
  })

  it('draws no chip for an unknown name', async () => {
    const view = await mount()
    type(view, ' @foo:ba')
    expect(view.dom.querySelector('.mention-scope-chip')).toBeNull()
  })
})

describe('mention insertion', () => {
  const one = { id: 'TKT-1', title: 'One', entityType: 'ticket' }

  it('writes a chosen type over what was typed to find it', async () => {
    const view = await mount('see')
    type(view, ' @ti')
    replaceMentionQueryWithText(view, 'ticket:', armedToken(view.state)!)
    expect(paragraphText(view)).toBe('see @ticket:')
    expect(armedAt(view.state)).not.toBeNull()
    expect(view.state.selection.from).toBe(view.state.doc.firstChild!.content.size + 1)
  })

  it('replaces the whole token, including characters after the cursor', async () => {
    const view = await mount('see')
    type(view, ' @fancy')
    const end = view.state.selection.from
    view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, end - 3)))
    const token = armedToken(view.state)!
    expect(token.query).toBe('fa')
    expect(replaceMentionQueryWithRef(view, one, token)).toBe(true)
    const para = view.state.doc.firstChild!
    expect(para.textContent).toBe('see  ')
    expect(para.child(1).type.name).toBe('entityRef')
  })

  it('keeps an existing word the @ was typed in front of', async () => {
    const view = await mount('see word')
    // Paragraph content starts at 1; position 5 sits just before `word`.
    view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, 5)))
    type(view, '@tk')
    replaceMentionQueryWithRef(view, one, armedToken(view.state)!)
    const para = view.state.doc.firstChild!
    expect(para.child(1).type.name).toBe('entityRef')
    expect(para.textContent.endsWith('word')).toBe(true)
  })

  it('keeps a colon typed after the token', async () => {
    const view = await mount('see :')
    view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, 5)))
    type(view, '@tk')
    replaceMentionQueryWithRef(view, one, armedToken(view.state)!)
    expect(view.state.doc.firstChild!.textContent).toBe('see :')
  })

  it('adds no space before punctuation', async () => {
    const view = await mount('see .')
    view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, 5)))
    type(view, '@tk')
    replaceMentionQueryWithRef(view, one, armedToken(view.state)!)
    expect(view.state.doc.firstChild!.textContent).toBe('see .')
  })

  it('has no token when nothing is armed', async () => {
    const view = await mount()
    expect(armedToken(view.state)).toBeNull()
  })
})
