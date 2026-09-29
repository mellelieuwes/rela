/**
 * GFM task lists in the editor (BUG-KHQXHH).
 *
 * The editing surface wears `md-body` and inherits the shared markdown
 * stylesheet, whose task-list rules key on `input[type='checkbox']`. Milkdown's
 * GFM preset does not emit one — it puts the state in a `data-checked`
 * attribute — so before the task-list node view every selector missed and a
 * checklist rendered as indistinguishable bullets.
 *
 * These tests assert on `input[type='checkbox']`, the SAME selector
 * `styles/markdown-content.css` uses, rather than on the node view's internals.
 * That is the point: it is what couples the editor's DOM to the read-only
 * `marked` render, so the two surfaces cannot drift apart again silently. The
 * serializer corpus test cannot catch this class of bug — task lists round-trip
 * perfectly whether or not anything is drawn on screen.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { TextSelection } from '@milkdown/kit/prose/state'
import type { EditorState } from '@milkdown/kit/prose/state'
import MilkdownEditor from './MilkdownEditor.vue'

const searchEntities = vi.fn()
vi.mock('@/api', () => ({
  searchEntities: (...args: unknown[]) => searchEntities(...args),
  listEntities: vi.fn(),
}))

async function mountEditor(props: Record<string, unknown> = {}) {
  const wrapper = mount(MilkdownEditor, {
    props: { modelValue: '', ...props },
    attachTo: document.body,
  })
  await flushPromises()
  await flushPromises()
  return wrapper
}

/**
 * The markdown the editor would hand the form, after the write-back guard.
 *
 * Trimmed, like the existing block-command tests: the `trailing` plugin keeps
 * an empty paragraph at the end of the document so there is always somewhere to
 * click below the last block, and it surfaces as a trailing newline after any
 * structural edit. That is editor-wide behaviour, not task-list behaviour, and
 * `guardWriteBack` is what stops it reaching a save.
 */
function emitted(w: ReturnType<typeof mount>): string {
  return (w.vm as unknown as { guardedValue: () => { value: string } }).guardedValue().value.trim()
}

function checkboxes(w: ReturnType<typeof mount>): HTMLInputElement[] {
  const root = w.element as unknown as HTMLElement
  return Array.from(
    root.querySelectorAll('.ProseMirror input[type="checkbox"]')
  ) as HTMLInputElement[]
}

/**
 * A pointer click on a checkbox, as a browser delivers it: `mousedown` first
 * (which the view suppresses to keep the caret out of the input), then `click`
 * (which performs the toggle). Keyboard activation fires only the second.
 */
function clickBox(box: HTMLInputElement) {
  box.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true }))
  box.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
}

// Top-level hooks apply to every suite below; the editor is remounted per test.
beforeEach(() => {
  searchEntities.mockReset()
  searchEntities.mockResolvedValue({ data: [] })
})
afterEach(() => {
  document.body.innerHTML = ''
})

describe('task list rendering', () => {
  it('renders a checkbox per task item, reflecting its checked state', async () => {
    const w = await mountEditor({ modelValue: '- [ ] todo\n- [x] done\n' })
    const boxes = checkboxes(w)
    expect(boxes.length).toBe(2)
    expect(boxes[0].checked).toBe(false)
    expect(boxes[1].checked).toBe(true)
    w.unmount()
  })

  // The stylesheet's `li:has(> input[type='checkbox']:first-child)` rule drops
  // the bullet and pulls the row back. It only fires when the input is a DIRECT
  // first child of the `li`, so nesting it any deeper silently loses the
  // styling while this file's other assertions would still pass.
  it('places the checkbox as the list item first child, where the shared CSS looks', async () => {
    const w = await mountEditor({ modelValue: '- [ ] todo\n' })
    const li = (w.element as unknown as HTMLElement).querySelector('.ProseMirror li')
    expect(li).not.toBeNull()
    expect(li!.firstElementChild?.matches('input[type="checkbox"]')).toBe(true)
    w.unmount()
  })

  it('leaves a plain bullet list without checkboxes', async () => {
    const w = await mountEditor({ modelValue: '- plain\n- items\n' })
    expect(checkboxes(w).length).toBe(0)
    w.unmount()
  })

  it('round-trips a task list without emitting a change', async () => {
    const src = '- [ ] todo\n- [x] done\n'
    const w = await mountEditor({ modelValue: src })
    expect(w.emitted('update:modelValue')).toBeUndefined()
    expect(emitted(w)).toBe(src.trim())
    w.unmount()
  })

  // Shapes a checklist body actually takes. The node view reads `checked` off
  // whatever `list_item` it is handed, so an ordered or nested list is not a
  // special case for it — but the serializer has to put the marker back in the
  // right place, and merely opening an entity must still emit nothing. The
  // last two cover a task item that contains OTHER decorated content, where a
  // node view that mishandled `contentDOM` would corrupt the item's children.
  it.each([
    ['nested task lists', '- [ ] parent\n  - [x] child\n', 2],
    ['an ordered task list', '1. [ ] first\n2. [x] second\n', 2],
    ['a multi-paragraph item', '- [ ] one\n\n  second para\n', 1],
    ['a plain child under a task', '- [ ] parent\n  - plain child\n', 1],
    ['task and plain items mixed', '- [ ] task\n- plain\n', 1],
    ['inline emphasis', '- [ ] **bold** text\n', 1],
    ['an entity reference', '- [ ] see `TKT-ABC`\n', 1],
    ['a task inside a blockquote', '> - [ ] quoted task\n', 1],
    ['a loose task list', '- [ ] a\n\n- [x] b\n', 2],
  ])('preserves %s', async (_label, src, boxes) => {
    const w = await mountEditor({ modelValue: src })
    expect(checkboxes(w).length).toBe(boxes)
    expect(w.emitted('update:modelValue'), 'opening the entity emitted a change').toBeUndefined()
    expect(emitted(w)).toBe(src.trim())
    w.unmount()
  })

  // Spellings the serializer normalizes: `*` becomes `-`, and `[X]` becomes
  // `[x]`. The write-back guard must classify that as churn and hand back the
  // ORIGINAL bytes, or opening one of these entities and saving it would
  // rewrite markers the author chose. This is the guard's job rather than the
  // node view's, but a task list is a case it has to cover.
  it.each([
    ['a star bullet', '* [ ] star\n'],
    ['an uppercase marker', '- [X] upper\n'],
  ])('suppresses churn from %s', async (_label, src) => {
    const w = await mountEditor({ modelValue: src })
    expect(checkboxes(w).length).toBe(1)
    expect(w.emitted('update:modelValue')).toBeUndefined()
    const guarded = (
      w.vm as unknown as { guardedValue: () => { value: string; verdict: string } }
    ).guardedValue()
    expect(guarded.verdict).toBe('churn-suppressed')
    expect(guarded.value).toBe(src)
    w.unmount()
  })
})

describe('task list toggling', () => {
  it('checks an open item when its checkbox is clicked', async () => {
    const w = await mountEditor({ modelValue: '- [ ] todo\n' })
    clickBox(checkboxes(w)[0])
    await flushPromises()
    expect(emitted(w)).toBe('- [x] todo')
    expect(checkboxes(w)[0].checked).toBe(true)
    w.unmount()
  })

  it('unchecks a done item when its checkbox is clicked', async () => {
    const w = await mountEditor({ modelValue: '- [x] done\n' })
    clickBox(checkboxes(w)[0])
    await flushPromises()
    expect(emitted(w)).toBe('- [ ] done')
    w.unmount()
  })

  it('toggles only the clicked item', async () => {
    const w = await mountEditor({ modelValue: '- [ ] first\n- [ ] second\n' })
    clickBox(checkboxes(w)[1])
    await flushPromises()
    expect(emitted(w)).toBe('- [ ] first\n- [x] second')
    w.unmount()
  })

  // Each node view captures its own `getPos`, so a toggle that shifted document
  // positions would leave the other items' views pointing at stale offsets and
  // the next click would mark the wrong row. Going last → first → middle is the
  // order that exposes it: a naive implementation gets the first click right.
  it('keeps hitting the right item across repeated toggles', async () => {
    const w = await mountEditor({ modelValue: '- [ ] a\n- [ ] b\n- [ ] c\n' })
    const click = (i: number) => clickBox(checkboxes(w)[i])

    click(2)
    await flushPromises()
    expect(emitted(w)).toBe('- [ ] a\n- [ ] b\n- [x] c')

    click(0)
    await flushPromises()
    expect(emitted(w)).toBe('- [x] a\n- [ ] b\n- [x] c')

    click(1)
    await flushPromises()
    expect(emitted(w)).toBe('- [x] a\n- [x] b\n- [x] c')
    w.unmount()
  })

  // Space on a focused checkbox fires `click` with NO preceding `mousedown`.
  // Binding the toggle to `mousedown` alone meant the browser ticked the input
  // natively while the document kept the old value — the tick then vanished on
  // the next redraw, and `guardWriteBack` could not see the loss because as far
  // as the document was concerned nothing had happened. Asserting the DOCUMENT
  // (not `input.checked`) is what makes this test catch it.
  it('toggles from the keyboard', async () => {
    const w = await mountEditor({ modelValue: '- [ ] todo\n' })
    const box = checkboxes(w)[0]
    box.focus()
    box.click()
    await flushPromises()
    expect(emitted(w)).toBe('- [x] todo')
    expect(checkboxes(w)[0].checked).toBe(true)
    w.unmount()
  })

  // A checkbox announced as a bare "checkbox, not checked" is unusable on a
  // list of a dozen items. The name has to track the item's text, which
  // ProseMirror rewrites, so it is mirrored on every update.
  it('names the checkbox after its item text', async () => {
    const w = await mountEditor({ modelValue: '- [ ] write the docs\n' })
    expect(checkboxes(w)[0].getAttribute('aria-label')).toBe('write the docs')
    w.unmount()
  })

  // The toggle is a document transaction, not DOM state on the input, so it
  // joins the undo stack like any other edit. Handling it as a native checkbox
  // change instead would leave the input ticked and the document unaware, and
  // undo would appear to do nothing. `docs/data-entry.md` promises this.
  it('reverts a toggle on undo', async () => {
    const w = await mountEditor({ modelValue: '- [ ] todo\n' })
    clickBox(checkboxes(w)[0])
    await flushPromises()
    expect(emitted(w)).toBe('- [x] todo')

    const surface = (w.element as unknown as HTMLElement).querySelector('.ProseMirror')!
    surface.dispatchEvent(new KeyboardEvent('keydown', { key: 'z', ctrlKey: true, bubbles: true }))
    await flushPromises()
    expect(emitted(w)).toBe('- [ ] todo')
    w.unmount()
  })
})

// A read-only body (an external system owns it through a twin) still renders
// its checkboxes. `editable: false` gates only ProseMirror's own handlers, and
// the toggle is this module's listener, so it went straight through: the box
// ticked, the document changed, and autosave sent a body PATCH the server then
// refused. Nothing may change, and nothing may reach the form.
describe('task list in a read-only editor', () => {
  it.each([
    ['read-only from mount', true],
    ['made read-only after mount', false],
  ])('ignores a checkbox click when %s', async (_name, atMount) => {
    const src = '- [ ] todo\n'
    const w = await mountEditor({ modelValue: src, readonly: atMount })
    if (!atMount) {
      await w.setProps({ readonly: true })
      await flushPromises()
    }
    const box = checkboxes(w)[0]
    // A native activation, which flips `checked` the way a browser does
    // before the listener runs, so the assertion covers the restore too.
    box.click()
    await flushPromises()

    expect(checkboxes(w)[0].checked).toBe(false)
    expect(emitted(w)).toBe(src.trim())
    // The form flushes before it saves; that path must be silent as well.
    // `flush` is on the component's defineExpose, which `w.vm` does not type.
    const exposed = w.vm as unknown as { flush: () => void }
    exposed.flush()
    await flushPromises()
    expect(w.emitted('update:modelValue')).toBeUndefined()
    w.unmount()
  })
})

describe('task list command', () => {
  function runTaskList(w: ReturnType<typeof mount>) {
    const buttons = w.findAll('.toolbar-button')
    const button = buttons.find((b) => b.attributes('aria-label') === 'Task list')
    expect(button, 'no Task list toolbar button').toBeTruthy()
    return button!
  }

  it('offers a task-list button in the toolbar', async () => {
    const w = await mountEditor()
    expect(runTaskList(w).exists()).toBe(true)
    w.unmount()
  })

  it('turns a bullet item into an unchecked task item', async () => {
    const w = await mountEditor({ modelValue: '- plain\n' })
    await runTaskList(w).trigger('click')
    await flushPromises()
    expect(emitted(w)).toBe('- [ ] plain')
    expect(checkboxes(w).length).toBe(1)
    w.unmount()
  })

  // Pressing an already-active button must undo, not no-op: the button renders
  // pressed, and a pressed button that ignores a click is a dead control.
  it('turns a task item back into a plain bullet', async () => {
    const w = await mountEditor({ modelValue: '- [ ] todo\n' })
    await runTaskList(w).trigger('click')
    await flushPromises()
    expect(emitted(w)).toBe('- todo')
    expect(checkboxes(w).length).toBe(0)
    w.unmount()
  })

  it('keeps a checked item pressed, and reverting it drops the checkbox', async () => {
    const w = await mountEditor({ modelValue: '- [x] done\n' })
    expect(runTaskList(w).attributes('aria-pressed')).toBe('true')
    await runTaskList(w).trigger('click')
    await flushPromises()
    expect(emitted(w)).toBe('- done')
    w.unmount()
  })

  it('wraps a bare paragraph into a task list', async () => {
    const w = await mountEditor({ modelValue: 'loose text\n' })
    await runTaskList(w).trigger('click')
    await flushPromises()
    expect(emitted(w)).toBe('- [ ] loose text')
    w.unmount()
  })

  // A cursor takes the INNERMOST enclosing item, matching what the toolbar's
  // pressed state reports — so the button always acts on the item it claims to
  // describe. (A ranged selection resolves the other way: shallowest, see
  // below.) Worth pinning because the opposite reading is just as plausible.
  it('toggles the nested item the cursor is in, not its parent', async () => {
    const w = await mountEditor({ modelValue: '- parent\n  - child\n' })
    const view = (
      w.vm as unknown as {
        editorViewForTest: { state: EditorState; dispatch: (tr: unknown) => void }
      }
    ).editorViewForTest
    let childPos = -1
    view.state.doc.descendants((node, pos) => {
      if (node.isText && node.text === 'child') childPos = pos
      return true
    })
    view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, childPos + 1)))
    await flushPromises()

    await runTaskList(w).trigger('click')
    await flushPromises()
    expect(emitted(w)).toBe('- parent\n  - [ ] child')
    w.unmount()
  })

  // Every neighbouring block command (Bullet list, Numbered list, Quote)
  // applies across a selection. This one walked up from the selection HEAD
  // only, so it converted the first item and silently left the rest — with
  // `aria-disabled: false` there was no signal the operation was partial.
  describe('across a selection', () => {
    /**
     * Selects from inside the first block to inside the last block that holds
     * text, as dragging across the list would.
     *
     * Deliberately NOT `TextSelection.create(doc, 1, doc.content.size - 1)`:
     * the `trailing` plugin keeps an empty paragraph at the end of every
     * document, so that range ends in a node with no text positions and
     * ProseMirror collapses the whole selection into it. The command then sees
     * a cursor in an empty paragraph rather than a range over the list.
     */
    async function selectBlocks(w: ReturnType<typeof mount>) {
      const view = (
        w.vm as unknown as {
          editorViewForTest: { state: EditorState; dispatch: (tr: unknown) => void }
        }
      ).editorViewForTest
      const { doc } = view.state
      const textBlocks: number[] = []
      doc.descendants((node, pos) => {
        if (node.isTextblock && node.content.size > 0) textBlocks.push(pos)
        return true
      })
      const first = textBlocks[0] + 1
      const lastPos = textBlocks[textBlocks.length - 1]
      const last = lastPos + doc.nodeAt(lastPos)!.content.size + 1
      view.dispatch(view.state.tr.setSelection(TextSelection.create(doc, first, last)))
      await flushPromises()
    }

    it('converts every selected bullet item', async () => {
      const w = await mountEditor({ modelValue: '- alpha\n- beta\n' })
      await selectBlocks(w)
      await runTaskList(w).trigger('click')
      await flushPromises()
      expect(emitted(w)).toBe('- [ ] alpha\n- [ ] beta')
      expect(checkboxes(w).length).toBe(2)
      w.unmount()
    })

    it('converts every selected paragraph, wrapping them first', async () => {
      const w = await mountEditor({ modelValue: 'para one\n\npara two\n' })
      await selectBlocks(w)
      await runTaskList(w).trigger('click')
      await flushPromises()
      expect(emitted(w)).toBe('- [ ] para one\n- [ ] para two')
      expect(checkboxes(w).length).toBe(2)
      w.unmount()
    })

    it('reverts every selected task item', async () => {
      const w = await mountEditor({ modelValue: '- [ ] alpha\n- [x] beta\n' })
      await selectBlocks(w)
      await runTaskList(w).trigger('click')
      await flushPromises()
      expect(emitted(w)).toBe('- alpha\n- beta')
      expect(checkboxes(w).length).toBe(0)
      w.unmount()
    })

    // Pressing once on a mixed selection must not convert some and revert
    // others, which would leave the list exactly as inconsistent as before.
    // Turning ON wins, and a ticked item keeps its tick.
    it('turns a mixed selection fully on, preserving ticks', async () => {
      const w = await mountEditor({ modelValue: '- [x] done\n- plain\n' })
      await selectBlocks(w)
      await runTaskList(w).trigger('click')
      await flushPromises()
      expect(emitted(w)).toBe('- [x] done\n- [ ] plain')
      w.unmount()
    })
  })

  // Wrapping a paragraph takes two dispatches (wrap in a list, then mark the
  // item), so undo could plausibly strand the user on the intermediate
  // `- loose text`. ProseMirror's history groups them into one step; if that
  // ever stops holding, one press of the button would need two of Ctrl-Z.
  it('undoes a wrap-and-mark in a single step', async () => {
    const w = await mountEditor({ modelValue: 'loose text\n' })
    await runTaskList(w).trigger('click')
    await flushPromises()
    expect(emitted(w)).toBe('- [ ] loose text')

    const surface = (w.element as unknown as HTMLElement).querySelector('.ProseMirror')!
    surface.dispatchEvent(new KeyboardEvent('keydown', { key: 'z', ctrlKey: true, bubbles: true }))
    await flushPromises()
    expect(emitted(w)).toBe('loose text')
    w.unmount()
  })
})

describe('task list toolbar state', () => {
  function pressed(w: ReturnType<typeof mount>, label: string): string | undefined {
    return w
      .findAll('.toolbar-button')
      .find((b) => b.attributes('aria-label') === label)
      ?.attributes('aria-pressed')
  }

  // `checked` is three-valued (null = not a task, false = open, true = done),
  // but the active-state probe compares attributes for exact equality. A naive
  // `{ checked: false }` probe would report INACTIVE for a done item, so ticking
  // a box would pop the toolbar button out. Both states must read as pressed.
  it('presses the button for an open and a done item alike', async () => {
    const open = await mountEditor({ modelValue: '- [ ] todo\n' })
    expect(pressed(open, 'Task list')).toBe('true')
    open.unmount()

    const done = await mountEditor({ modelValue: '- [x] done\n' })
    expect(pressed(done, 'Task list')).toBe('true')
    done.unmount()
  })

  it('does not press the button in a plain bullet list', async () => {
    const w = await mountEditor({ modelValue: '- plain\n' })
    expect(pressed(w, 'Task list')).toBe('false')
    w.unmount()
  })

  // A task item is still a bullet list in the document, so both buttons light
  // up. That is honest rather than a bug: lifting the list is a real action.
  it('still reports the enclosing bullet list', async () => {
    const w = await mountEditor({ modelValue: '- [ ] todo\n' })
    expect(pressed(w, 'Bullet list')).toBe('true')
    w.unmount()
  })

  // Availability is a DRY RUN: `commandAvailability.ts` calls the command with
  // no `dispatch`, and ProseMirror's contract is that it then reports without
  // changing anything. The command wraps a bare paragraph in a list before
  // marking it, so a dry run that leaked a dispatch would silently rewrite the
  // document just by rendering the toolbar — hence the emit assertion.
  it.each([
    ['a paragraph', 'plain para\n', false],
    ['a bullet item', '- bullet\n', false],
    ['a heading', '# heading\n', true],
    ['a code block', '```\ncode\n```\n', true],
  ])('reports availability in %s without mutating the document', async (_l, src, disabled) => {
    const w = await mountEditor({ modelValue: src })
    const button = w
      .findAll('.toolbar-button')
      .find((b) => b.attributes('aria-label') === 'Task list')
    expect(button!.classes().includes('is-unavailable')).toBe(disabled)
    expect(w.emitted('update:modelValue'), 'the dry run dispatched').toBeUndefined()
    w.unmount()
  })
})
