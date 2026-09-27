import { describe, it, expect, vi, afterEach } from 'vitest'
import { Editor, rootCtx, defaultValueCtx, editorViewCtx } from '@milkdown/kit/core'
import { commonmark } from '@milkdown/kit/preset/commonmark'
import { history } from '@milkdown/kit/plugin/history'
import { undo } from '@milkdown/kit/prose/history'
import { TextSelection } from '@milkdown/kit/prose/state'
import type { EditorView } from '@milkdown/kit/prose/view'
import type { Entity } from '@/types'
import { entityRefNode } from './entityRefNode'
import { useEditorMention, type EditorMention } from './useEditorMention'
import type { MentionTransport } from './useMentionMenu'

const search = vi.fn<MentionTransport['search']>()

// The schema-bound menu needs pinia and the API; the wiring under test only
// needs a menu, so it gets the plain controller over a fake transport.
vi.mock('./useMentionMenu', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./useMentionMenu')>()
  return {
    ...actual,
    useSchemaMentionMenu: () => {
      const menu = actual.useMentionMenu({
        search: (...args) => search(...args),
        startingList: { load: async () => [] },
      })
      menu.setAvailableTypes([{ name: 'ticket', prefixes: ['TKT-'] }])
      return menu
    },
  }
})

const recordRecentEntity = vi.fn()
vi.mock('@/utils/recentEntities', () => ({
  recordRecentEntity: (...args: unknown[]) => recordRecentEntity(...args),
}))

function ent(id: string, title: string): Entity {
  return { id, type: 'ticket', _title: title, properties: {} } as unknown as Entity
}

let editor: Editor | null = null
let mention: EditorMention | null = null

async function mount(): Promise<EditorView> {
  let view: EditorView | null = null
  mention = useEditorMention(() => null, {
    view: () => view,
    textBefore: (v) => {
      const { $from } = v.state.selection
      return v.state.doc.textBetween($from.start(), $from.pos, undefined, '￼')
    },
    hide: () => {},
  })
  const root = document.createElement('div')
  document.body.appendChild(root)
  editor = await Editor.make()
    .config((ctx) => {
      ctx.set(rootCtx, root)
      ctx.set(defaultValueCtx, 'see')
    })
    .use(commonmark)
    .use(history)
    .use(entityRefNode)
    .use(mention.plugin)
    .create()
  view = editor.action((ctx) => ctx.get(editorViewCtx))
  const end = view.state.doc.content.size - 1
  view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, end)))
  return view
}

afterEach(async () => {
  mention?.dispose()
  await editor?.destroy()
  editor = null
  mention = null
  document.body.innerHTML = ''
  search.mockReset()
  recordRecentEntity.mockReset()
})

/** Types `text` the way a keyboard does, updating the menu as the editor would. */
function type(view: EditorView, text: string): void {
  for (const ch of text) {
    const { from, to } = view.state.selection
    const handled = view.someProp('handleTextInput', (f) =>
      f(view, from, to, ch, () => view.state.tr)
    )
    if (!handled) view.dispatch(view.state.tr.insertText(ch, from, to))
    mention!.shouldShow(view)
  }
}

function press(key: string): KeyboardEvent {
  const event = new KeyboardEvent('keydown', { key, cancelable: true })
  mention!.onKeydown(event)
  return event
}

/** Waits out the search debounce and lets the response land. */
const settle = () => new Promise((r) => setTimeout(r, 250))

describe('useEditorMention', () => {
  it('opens on a typed @', async () => {
    const view = await mount()
    type(view, ' @')
    expect(mention!.menu.state.open).toBe(true)
  })

  it('writes a chosen type as the scope', async () => {
    const view = await mount()
    type(view, ' @ti')
    press('Enter')
    expect(view.state.doc.textContent).toBe('see @ticket:')
  })

  it('makes Enter wait for the search, then inserts the top result', async () => {
    search.mockResolvedValue([ent('TKT-ABC', 'fancy thing')])
    const view = await mount()
    type(view, ' @fancy')
    const event = press('Enter')
    expect(event.defaultPrevented).toBe(true)
    await settle()
    const para = view.state.doc.firstChild!
    expect(para.child(1).type.name).toBe('entityRef')
    expect(para.child(1).attrs.id).toBe('TKT-ABC')
    expect(recordRecentEntity).toHaveBeenCalledWith('TKT-ABC', 'ticket')
    expect(mention!.menu.state.open).toBe(false)
  })

  it('undoes an insertion back to the typed query, without reopening', async () => {
    search.mockResolvedValue([ent('TKT-ABC', 'fancy thing')])
    const view = await mount()
    type(view, ' @fancy')
    press('Enter')
    await settle()
    undo(view.state, view.dispatch)
    expect(view.state.doc.textContent).toBe('see @fancy')
    mention!.shouldShow(view)
    expect(mention!.menu.state.open).toBe(false)
  })

  it('writes nothing when the query moved on while Enter waited', async () => {
    search.mockResolvedValue([ent('TKT-ABC', 'fancy thing')])
    const view = await mount()
    type(view, ' @fancy')
    press('Enter')
    // The next keystroke lands in the document before the menu re-reads it.
    view.dispatch(view.state.tr.insertText('x'))
    await settle()
    expect(view.state.doc.textContent).toBe('see @fancyx')
  })

  it('reopens after Escape once the query is edited', async () => {
    const view = await mount()
    type(view, ' @ti')
    press('Escape')
    expect(mention!.menu.state.open).toBe(false)
    type(view, 'c')
    expect(mention!.menu.state.open).toBe(true)
  })
})
