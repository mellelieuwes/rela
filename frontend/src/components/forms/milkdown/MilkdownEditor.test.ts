import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { TextSelection } from '@milkdown/kit/prose/state'
import { listenerCtx } from '@milkdown/kit/plugin/listener'
import type { EditorState } from '@milkdown/kit/prose/state'
import MilkdownEditor from './MilkdownEditor.vue'
import type { EntityRefResolver } from '@/utils/markdown'

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
 * Invokes the component's own markdownUpdated callback.
 *
 * Milkdown's listener is debounced and does not fire for a programmatic
 * dispatch under happy-dom, so every assertion about the emit channel could
 * only ever be negative — and a negative assertion cannot tell "correctly
 * suppressed" from "never ran". Calling the registered callback exercises
 * the real path: guard included.
 */
function fireMarkdownUpdated(w: ReturnType<typeof mount>, markdown: string) {
  const editor = (
    w.vm as unknown as { editorInstanceForTest: { ctx: { get: (k: unknown) => unknown } } }
  ).editorInstanceForTest
  const manager = editor.ctx.get(listenerCtx) as unknown as {
    markdownUpdatedListeners: Array<(ctx: unknown, md: string, prev: string) => void>
  }
  expect(manager.markdownUpdatedListeners.length).toBeGreaterThan(0)
  for (const fn of manager.markdownUpdatedListeners) fn(null, markdown, '')
}

describe('MilkdownEditor', () => {
  beforeEach(() => {
    searchEntities.mockReset()
    searchEntities.mockResolvedValue({ data: [] })
  })
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('mounts and renders the loaded markdown', async () => {
    const w = await mountEditor({ modelValue: '# Hello\n\nWorld\n' })
    expect(w.find('.ProseMirror').exists()).toBe(true)
    expect(w.text()).toContain('Hello')
    expect(w.text()).toContain('World')
    w.unmount()
  })

  it('round-trips a body without emitting a change', async () => {
    const src = '## Title\n\n- a\n- b\n\nSee `TKT-ABC`.\n'
    const w = await mountEditor({ modelValue: src })
    expect(w.emitted('update:modelValue')).toBeUndefined()
    w.unmount()
  })

  it('reports the loaded value as unchanged through the write-back guard', async () => {
    const src = '## Title\n\nBody with `TKT-ABC`.\n'
    const w = await mountEditor({ modelValue: src })
    const guarded = (
      w.vm as unknown as { guardedValue: () => { value: string; verdict: string } }
    ).guardedValue()
    expect(guarded.value).toBe(src)
    expect(['unchanged', 'churn-suppressed']).toContain(guarded.verdict)
    w.unmount()
  })

  it('renders an entity ref as a link showing its resolved title', async () => {
    const refResolver: EntityRefResolver = (id) =>
      id === 'TKT-ABC' ? { type: 'ticket', title: 'Fix the thing' } : null
    const w = await mountEditor({ modelValue: 'See `TKT-ABC`.\n', refResolver })
    await flushPromises()
    const link = w.find('a[data-entity-ref]')
    expect(link.exists()).toBe(true)
    expect(link.text()).toContain('Fix the thing')
    expect(link.attributes('href')).toBe('/entity/ticket/TKT-ABC')
    w.unmount()
  })

  it('renders an unresolved ref as its bare id', async () => {
    const w = await mountEditor({ modelValue: 'See `TKT-NOPE`.\n' })
    const link = w.find('a[data-entity-ref]')
    expect(link.exists()).toBe(true)
    expect(link.text()).toContain('TKT-NOPE')
    expect(link.attributes('href')).toBeUndefined()
    w.unmount()
  })

  it('does not write a resolved title into the emitted markdown', async () => {
    const refResolver: EntityRefResolver = () => ({
      type: 'ticket',
      title: 'Confidential Name',
    })
    const w = await mountEditor({ modelValue: 'See `TKT-ABC`.\n', refResolver })
    await flushPromises()
    const guarded = (w.vm as unknown as { guardedValue: () => { value: string } }).guardedValue()
    expect(guarded.value).not.toContain('Confidential Name')
    expect(guarded.value).toContain('`TKT-ABC`')
    w.unmount()
  })

  it('applies a resolver that arrives after mount', async () => {
    const w = await mountEditor({ modelValue: 'See `TKT-ABC`.\n' })
    expect(w.find('a[data-entity-ref]').text()).toContain('TKT-ABC')
    await w.setProps({
      refResolver: (() => ({ type: 'ticket', title: 'Later Title' })) as EntityRefResolver,
    })
    await flushPromises()
    expect(w.find('a[data-entity-ref]').text()).toContain('Later Title')
    w.unmount()
  })

  it('reloads when the parent replaces the value', async () => {
    const w = await mountEditor({ modelValue: 'first\n' })
    await w.setProps({ modelValue: '# second\n' })
    await flushPromises()
    expect(w.text()).toContain('second')
    expect(w.text()).not.toContain('first')
    w.unmount()
  })

  it('mounts the mention menu hidden', async () => {
    const w = await mountEditor()
    expect(w.find('.mention-menu').exists()).toBe(true)
    expect(w.find('.mention-menu-list').exists()).toBe(false)
    w.unmount()
  })

  it('exposes the insert-entity-reference toolbar button', async () => {
    const w = await mountEditor()
    const btn = w.find('button.entity-ref-button')
    expect(btn.exists()).toBe(true)
    expect(btn.attributes('title')).toBe('Insert entity reference')
    w.unmount()
  })

  it('opens the picker from the toolbar button', async () => {
    const w = await mountEditor()
    expect(w.findComponent({ name: 'EntityPickerModal' }).props('open')).toBe(false)
    await w.find('button.entity-ref-button').trigger('click')
    expect(w.findComponent({ name: 'EntityPickerModal' }).props('open')).toBe(true)
    w.unmount()
  })

  it('inserts a code span when the picker selects an entity', async () => {
    const w = await mountEditor({ modelValue: 'before\n' })
    await w.find('button.entity-ref-button').trigger('click')
    w.findComponent({ name: 'EntityPickerModal' }).vm.$emit('select', 'TKT-PICKED')
    await flushPromises()
    const guarded = (w.vm as unknown as { guardedValue: () => { value: string } }).guardedValue()
    expect(guarded.value).toContain('`TKT-PICKED`')
    w.unmount()
  })

  it('refuses an id that would break the code span', async () => {
    const w = await mountEditor({ modelValue: 'before\n' })
    await w.find('button.entity-ref-button').trigger('click')
    w.findComponent({ name: 'EntityPickerModal' }).vm.$emit('select', 'bad`id')
    await flushPromises()
    const guarded = (w.vm as unknown as { guardedValue: () => { value: string } }).guardedValue()
    expect(guarded.value).not.toContain('bad')
    w.unmount()
  })

  it('treats a picker insertion as a real edit, not round-trip churn', async () => {
    // Regression: dirty tracking used to hang off the markdown listener, so an
    // insertion dispatched straight onto the view never set it. The write-back
    // guard then read the changed body as unexplained drift and threw the
    // edit away. Dirty is now driven by `docChanged`, which every route into
    // the document passes through.
    const w = await mountEditor({ modelValue: 'before\n' })
    await w.find('button.entity-ref-button').trigger('click')
    w.findComponent({ name: 'EntityPickerModal' }).vm.$emit('select', 'TKT-KEPT')
    await flushPromises()
    const guarded = (
      w.vm as unknown as {
        guardedValue: () => { value: string; verdict: string }
      }
    ).guardedValue()
    expect(guarded.verdict).toBe('edited')
    expect(guarded.value).toContain('`TKT-KEPT`')
    w.unmount()
  })

  it('shows the placeholder only while the document is empty', async () => {
    const w = await mountEditor({ modelValue: '', placeholder: 'Say something' })
    expect(w.find('.milkdown-placeholder').exists()).toBe(true)
    expect(w.find('.milkdown-placeholder').text()).toBe('Say something')

    await w.setProps({ modelValue: '# not empty\n' })
    await flushPromises()
    expect(w.find('.milkdown-placeholder').exists()).toBe(false)
    w.unmount()
  })

  it('does not show the placeholder for a loaded body', async () => {
    const w = await mountEditor({ modelValue: '# Hello\n' })
    expect(w.find('.milkdown-placeholder').exists()).toBe(false)
    w.unmount()
  })

  it('does not report an edit for load-time normalization', async () => {
    // Regression: loading a body containing a table makes ProseMirror's table
    // plugin rewrite the column widths before the user touches anything. The
    // dirty tracker counted that as an edit, so the write-back guard returned
    // `edited` and the reformatted table would have been saved back — a diff
    // in git for an entity that was only opened.
    const withTable = '## Heading\n\n| column | second |\n| --- | --- |\n| a | b |\n'
    const w = await mountEditor({ modelValue: withTable })
    const guarded = (w.vm as unknown as { guardedValue: () => { verdict: string } }).guardedValue()
    expect(guarded.verdict).not.toBe('edited')
    // Nothing was emitted either, so the form has nothing to save.
    expect(w.emitted('update:modelValue')).toBeUndefined()
    w.unmount()
  })

  it('still reports an edit after the user types into a normalized body', async () => {
    // The complement of the test above: arming must not swallow real edits
    // that happen to follow a normalizing load.
    const withTable = '## Heading\n\n| a | b |\n| --- | --- |\n| 1 | 2 |\n'
    const w = await mountEditor({ modelValue: withTable })
    const vm = w.vm as unknown as { guardedValue: () => { verdict: string } }
    expect(vm.guardedValue().verdict).not.toBe('edited')

    // Insert through the picker, which dispatches straight onto the view.
    await w.find('button.entity-ref-button').trigger('click')
    w.findComponent({ name: 'EntityPickerModal' }).vm.$emit('select', 'TKT-AFTER')
    await flushPromises()
    expect(vm.guardedValue().verdict).toBe('edited')
    w.unmount()
  })

  it('tears down cleanly', async () => {
    const w = await mountEditor({ modelValue: '# x\n' })
    expect(() => w.unmount()).not.toThrow()
  })
})

describe('MilkdownEditor toolbar', () => {
  beforeEach(() => {
    searchEntities.mockReset()
    searchEntities.mockResolvedValue({ data: [] })
  })
  afterEach(() => {
    document.body.innerHTML = ''
  })

  // The active-state probes name schema types by string, and a miss returns
  // false rather than throwing. Checking them against the schema the editor
  // ACTUALLY builds is what catches an upstream rename; the unit test's
  // hand-built schema cannot, since it would be renamed alongside the probes.
  it('probes only mark and node names the live preset schema defines', async () => {
    const w = await mountEditor()
    const view = document.querySelector('.ProseMirror') as
      (HTMLElement & { pmViewDesc?: unknown }) | null
    expect(view, 'editor did not mount').not.toBeNull()

    const { INLINE_COMMANDS, BLOCK_COMMANDS } = await import('./editorCommands')
    const schema = (
      w.vm as unknown as {
        editorViewForTest?: { state: { schema: { marks: object; nodes: object } } }
      }
    ).editorViewForTest?.state.schema
    expect(schema, 'editor schema not exposed').toBeTruthy()

    for (const cmd of [...INLINE_COMMANDS, ...BLOCK_COMMANDS]) {
      if (cmd.probe.kind === 'mark') {
        expect(Object.keys(schema!.marks), `mark for ${cmd.id}`).toContain(cmd.probe.mark)
      }
      if (cmd.probe.kind === 'node' || cmd.probe.kind === 'nodeWhere') {
        expect(Object.keys(schema!.nodes), `node for ${cmd.id}`).toContain(cmd.probe.node)
      }
    }
    w.unmount()
  })

  it('renders a button for every command, each labelled for screen readers', async () => {
    const w = await mountEditor()
    const { INLINE_COMMANDS, BLOCK_COMMANDS, HISTORY_COMMANDS } = await import('./editorCommands')
    const buttons = w.findAll('.toolbar-button')
    // Every command, plus the entity-reference button. The table group and the
    // unlink button are absent here: both are contextual, and this fixture's
    // cursor is in neither a table nor a link.
    expect(buttons.length).toBe(
      INLINE_COMMANDS.length + BLOCK_COMMANDS.length + HISTORY_COMMANDS.length + 1
    )
    for (const b of buttons) {
      expect(b.attributes('aria-label')).toBeTruthy()
    }
    w.unmount()
  })

  it('marks a heading button pressed when the cursor is in that heading', async () => {
    const w = await mountEditor({ modelValue: '## Title\n' })
    await flushPromises()
    const h2 = w.findAll('.toolbar-button').find((b) => b.attributes('aria-label') === 'Heading 2')
    expect(h2?.attributes('aria-pressed')).toBe('true')
    const h1 = w.findAll('.toolbar-button').find((b) => b.attributes('aria-label') === 'Heading 1')
    expect(h1?.attributes('aria-pressed')).toBe('false')
    w.unmount()
  })
})

describe('MilkdownEditor toolbar commands', () => {
  beforeEach(() => {
    searchEntities.mockReset()
    searchEntities.mockResolvedValue({ data: [] })
  })
  afterEach(() => {
    document.body.innerHTML = ''
  })

  const guarded = (w: ReturnType<typeof mount>) =>
    (w.vm as unknown as { guardedValue: () => { value: string } }).guardedValue().value

  const button = (w: ReturnType<typeof mount>, label: string) =>
    w.findAll('.toolbar-button').find((b) => b.attributes('aria-label') === label)

  // Commands are named by their registered slice name. Reading `.key` off the
  // imported command object instead yields undefined (the factory assigns it
  // when the plugin runs, not at module load), and `callCommand(undefined)`
  // throws inside the ctx container — every button was dead. Applying real
  // formatting is the only assertion that catches that.
  it.each([
    ['Heading 2', 'plain\n', '## plain\n'],
    ['Bullet list', 'plain\n', '- plain\n'],
    ['Numbered list', 'plain\n', '1. plain\n'],
    ['Quote', 'plain\n', '> plain\n'],
  ])('%s applies to the block at the cursor', async (label, src, expected) => {
    const w = await mountEditor({ modelValue: src })
    await button(w, label)!.trigger('click')
    await flushPromises()
    expect(guarded(w).trim()).toBe(expected.trim())
    w.unmount()
  })

  it('bold wraps a selected range and unwraps it on a second press', async () => {
    const w = await mountEditor({ modelValue: 'hello world\n' })
    const view = (
      w.vm as unknown as {
        editorViewForTest: { state: EditorState; dispatch: (tr: unknown) => void }
      }
    ).editorViewForTest
    view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, 1, 6)))
    await flushPromises()

    const bold = button(w, 'Bold')!
    await bold.trigger('click')
    await flushPromises()
    expect(guarded(w)).toBe('**hello** world\n')
    expect(bold.attributes('aria-pressed')).toBe('true')

    await bold.trigger('click')
    await flushPromises()
    expect(guarded(w)).toBe('hello world\n')
    expect(bold.attributes('aria-pressed')).toBe('false')
    w.unmount()
  })

  // `WrapInHeading` and friends are one-way: pressing them on a block that is
  // already that type no-ops, so a lit button appeared to do nothing.
  it.each([
    ['Heading 2', '## Title\n', 'Title'],
    ['Quote', '> quoted\n', 'quoted'],
    ['Bullet list', '- item\n', 'item'],
    ['Numbered list', '1. item\n', 'item'],
  ])('%s toggles back off when already active', async (label, src, expected) => {
    const w = await mountEditor({ modelValue: src })
    const b = button(w, label)!
    expect(b.attributes('aria-pressed'), `${label} should be lit on load`).toBe('true')
    await b.trigger('click')
    await flushPromises()
    expect(guarded(w).trim()).toBe(expected)
    expect(b.attributes('aria-pressed')).toBe('false')
    w.unmount()
  })

  // `appendTransaction` does not run for the document the editor loads with,
  // so the toolbar used to show nothing active until the first edit.
  it('lights the right button for the loaded document, before any edit', async () => {
    const w = await mountEditor({ modelValue: '> quoted\n' })
    expect(button(w, 'Quote')!.attributes('aria-pressed')).toBe('true')
    expect(button(w, 'Heading 2')!.attributes('aria-pressed')).toBe('false')
    w.unmount()
  })
})

describe('MilkdownEditor toolbar availability', () => {
  beforeEach(() => {
    searchEntities.mockReset()
    searchEntities.mockResolvedValue({ data: [] })
  })
  afterEach(() => {
    document.body.innerHTML = ''
  })

  const button = (w: ReturnType<typeof mount>, label: string) =>
    w.findAll('.toolbar-button').find((b) => b.attributes('aria-label') === label)!

  const disabled = (w: ReturnType<typeof mount>, label: string) =>
    button(w, label).attributes('aria-disabled') === 'true'

  // The reported case: a heading cannot be applied inside a list item, so the
  // button must not look pressable.
  it('disables the heading buttons inside a list item', async () => {
    const w = await mountEditor({ modelValue: '- item\n' })
    expect(disabled(w, 'Heading 1')).toBe(true)
    expect(disabled(w, 'Heading 2')).toBe(true)
    expect(disabled(w, 'Heading 3')).toBe(true)
    w.unmount()
  })

  it('leaves the heading buttons enabled in a plain paragraph', async () => {
    const w = await mountEditor({ modelValue: 'plain\n' })
    expect(disabled(w, 'Heading 1')).toBe(false)
    expect(disabled(w, 'Heading 2')).toBe(false)
    w.unmount()
  })

  // The button that removes the current formatting must stay live. Judging an
  // active command by its forward direction would disable exactly the control
  // needed to get back out of a list.
  it('keeps an active block button enabled so it can toggle off', async () => {
    const w = await mountEditor({ modelValue: '- item\n' })
    const b = button(w, 'Bullet list')
    expect(b.attributes('aria-pressed')).toBe('true')
    expect(b.attributes('aria-disabled')).toBe('false')
    w.unmount()
  })

  it('disables inline marks inside a code block, where they cannot apply', async () => {
    const w = await mountEditor({ modelValue: '```\ncode\n```\n' })
    expect(disabled(w, 'Bold')).toBe(true)
    expect(disabled(w, 'Italic')).toBe(true)
    w.unmount()
  })

  // Milkdown's ToggleInlineCode needs a real range, unlike ToggleStrong which
  // can arm a stored mark at a collapsed cursor.
  it('enables inline code only once a range is selected', async () => {
    const w = await mountEditor({ modelValue: 'hello world\n' })
    expect(disabled(w, 'Inline code')).toBe(true)

    const view = (
      w.vm as unknown as {
        editorViewForTest: { state: EditorState; dispatch: (tr: unknown) => void }
      }
    ).editorViewForTest
    view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, 1, 6)))
    await flushPromises()

    expect(disabled(w, 'Inline code')).toBe(false)
    await button(w, 'Inline code').trigger('click')
    await flushPromises()
    expect(
      (w.vm as unknown as { guardedValue: () => { value: string } }).guardedValue().value
    ).toBe('`hello` world\n')
    w.unmount()
  })

  // aria-disabled does not stop activation the way native disabled does, so
  // the handler has to refuse on its own.
  it('does nothing when an unavailable button is clicked anyway', async () => {
    const w = await mountEditor({ modelValue: '- item\n' })
    const before = (w.vm as unknown as { guardedValue: () => { value: string } }).guardedValue()
      .value
    await button(w, 'Heading 2').trigger('click')
    await flushPromises()
    expect(
      (w.vm as unknown as { guardedValue: () => { value: string } }).guardedValue().value
    ).toBe(before)
    w.unmount()
  })

  // Native `disabled` would drop focus to <body> if the cursor moved into a
  // context that disables the focused button.
  it('keeps unavailable buttons focusable rather than natively disabled', async () => {
    const w = await mountEditor({ modelValue: '- item\n' })
    const el = button(w, 'Heading 2').element as HTMLButtonElement
    expect(el.disabled).toBe(false)
    expect(el.getAttribute('aria-disabled')).toBe('true')
    w.unmount()
  })
})

describe('MilkdownEditor table controls', () => {
  beforeEach(() => {
    searchEntities.mockReset()
    searchEntities.mockResolvedValue({ data: [] })
  })
  afterEach(() => {
    document.body.innerHTML = ''
  })

  const TABLE = '| a | b |\n| --- | --- |\n| c | d |\n'

  const guarded = (w: ReturnType<typeof mount>) =>
    (w.vm as unknown as { guardedValue: () => { value: string } }).guardedValue().value

  const button = (w: ReturnType<typeof mount>, label: string) =>
    w.findAll('.toolbar-button').find((b) => b.attributes('aria-label') === label)

  /** Puts the cursor inside the table cell whose text is `text`. */
  async function cursorInCell(w: ReturnType<typeof mount>, text: string) {
    const view = (
      w.vm as unknown as {
        editorViewForTest: { state: EditorState; dispatch: (tr: unknown) => void }
      }
    ).editorViewForTest
    let pos = -1
    view.state.doc.descendants((node, p) => {
      const n = node.type.name
      if ((n === 'table_cell' || n === 'table_header') && node.textContent === text && pos < 0) {
        pos = p
      }
    })
    view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, pos + 2)))
    await flushPromises()
  }

  it('hides the table group outside a table and shows it inside', async () => {
    const plain = await mountEditor({ modelValue: 'plain\n' })
    expect(button(plain, 'Insert row below')).toBeUndefined()
    plain.unmount()

    const table = await mountEditor({ modelValue: TABLE })
    expect(button(table, 'Insert row below')).toBeDefined()
    table.unmount()
  })

  it.each([
    ['Insert row below', '| a | b |\n| - | - |\n| c | d |\n|   |   |'],
    ['Insert row above', '| a | b |\n| - | - |\n|   |   |\n| c | d |'],
    ['Delete column', '| b |\n| - |\n| d |'],
  ])('%s rewrites the table', async (label, expected) => {
    const w = await mountEditor({ modelValue: TABLE })
    await cursorInCell(w, 'c')
    await button(w, label)!.trigger('click')
    await flushPromises()
    expect(guarded(w).trim()).toBe(expected)
    w.unmount()
  })

  // There is no position above the header row. AddRowBefore reports applicable
  // anyway, then inserts a body row at index 0 and leaves the header empty —
  // which serializes as a headerless table followed by the orphaned original.
  it('refuses to insert a row above the header row', async () => {
    const w = await mountEditor({ modelValue: TABLE })
    await cursorInCell(w, 'a')
    const b = button(w, 'Insert row above')!
    expect(b.attributes('aria-disabled')).toBe('true')
    const before = guarded(w)
    await b.trigger('click')
    await flushPromises()
    expect(guarded(w)).toBe(before)
    w.unmount()
  })

  // The other three are genuinely fine from a header cell, so the guard must
  // not be widened to the whole group.
  it.each([
    ['Insert row below', '| a | b |\n| - | - |\n|   |   |\n| c | d |'],
    ['Insert column left', '|    | a | b |\n| :- | - | - |\n|    | c | d |'],
    ['Insert column right', '| a |    | b |\n| - | :- | - |\n| c |    | d |'],
  ])('%s still works from a header cell', async (label, expected) => {
    const w = await mountEditor({ modelValue: TABLE })
    await cursorInCell(w, 'a')
    const b = button(w, label)!
    expect(b.attributes('aria-disabled')).toBe('false')
    await b.trigger('click')
    await flushPromises()
    expect(guarded(w).trim()).toBe(expected)
    w.unmount()
  })

  it('inserts a row above from a body cell', async () => {
    const w = await mountEditor({ modelValue: TABLE })
    await cursorInCell(w, 'c')
    expect(button(w, 'Insert row above')!.attributes('aria-disabled')).toBe('false')
    await button(w, 'Insert row above')!.trigger('click')
    await flushPromises()
    expect(guarded(w).trim()).toBe('| a | b |\n| - | - |\n|   |   |\n| c | d |')
    w.unmount()
  })

  it('deletes a body row when another remains', async () => {
    const w = await mountEditor({
      modelValue: '| a | b |\n| --- | --- |\n| c | d |\n| e | f |\n',
    })
    await cursorInCell(w, 'c')
    await button(w, 'Delete row')!.trigger('click')
    await flushPromises()
    expect(guarded(w).trim()).toBe('| a | b |\n| - | - |\n| e | f |')
    w.unmount()
  })

  // The schema is `table_header_row table_row+`, so the last body row cannot
  // go. deleteRow would blank its cells instead of removing it.
  it('refuses to delete the only body row, leaving Delete table for that', async () => {
    const w = await mountEditor({ modelValue: TABLE })
    await cursorInCell(w, 'c')
    const b = button(w, 'Delete row')!
    expect(b.attributes('aria-disabled')).toBe('true')
    const before = guarded(w)
    await b.trigger('click')
    await flushPromises()
    expect(guarded(w)).toBe(before)
    expect(button(w, 'Delete table')!.attributes('aria-disabled')).toBe('false')
    w.unmount()
  })

  it('deletes the whole table', async () => {
    const w = await mountEditor({ modelValue: TABLE })
    await cursorInCell(w, 'c')
    await button(w, 'Delete table')!.trigger('click')
    await flushPromises()
    expect(guarded(w).trim()).toBe('')
    w.unmount()
  })

  // A GFM table cannot lose its header. deleteRow reports success there and
  // empties the cells instead, leaving a header of blank placeholders.
  it('refuses to delete the header row', async () => {
    const w = await mountEditor({ modelValue: TABLE })
    await cursorInCell(w, 'a')
    const b = button(w, 'Delete row')!
    expect(b.attributes('aria-disabled')).toBe('true')
    const before = guarded(w)
    await b.trigger('click')
    await flushPromises()
    expect(guarded(w)).toBe(before)
    w.unmount()
  })

  // `remarkPreserveEmptyLinePlugin` serialized every empty paragraph as a
  // literal `<br />`, so adding a row or column wrote raw HTML into the file.
  // This also covers the plain Table button, which had the same defect.
  it.each([
    ['a fresh table', 'x\n', 'Table'],
    ['an added row', TABLE, 'Insert row below'],
    ['an added column', TABLE, 'Insert column right'],
  ])('writes no <br /> for %s', async (_name, src, label) => {
    const w = await mountEditor({ modelValue: src })
    if (src === TABLE) await cursorInCell(w, 'c')
    await button(w, label)!.trigger('click')
    await flushPromises()
    expect(guarded(w)).not.toContain('<br')
    w.unmount()
  })

  it('keeps an empty cell empty on a plain round trip', async () => {
    const w = await mountEditor({ modelValue: '| a | b |\n| --- | --- |\n|  | d |\n' })
    expect(guarded(w)).not.toContain('<br')
    w.unmount()
  })
})

describe('MilkdownEditor write-back guard, through the component', () => {
  beforeEach(() => {
    searchEntities.mockReset()
    searchEntities.mockResolvedValue({ data: [] })
  })
  afterEach(() => {
    document.body.innerHTML = ''
  })

  // These mount the real component and assert on the CHANNEL THE FORM USES,
  // rather than reaching past the wiring to call guardedValue(). An earlier
  // version of this suite only did the latter, so it passed while the guard
  // was not connected to the emit path at all and the baseline it compared
  // against had been overwritten with the editor's own output.
  it.each([
    ['a setext heading', 'Title\n=====\n\nsome text\n'],
    ['star bullets', '* one\n* two\n'],
    ['a star thematic break', 'a\n\n***\n\nb\n'],
    ['loose table padding', '| a | b |\n|---|---|\n| c | d |\n'],
  ])('emits nothing and keeps the original bytes for %s', async (_name, src) => {
    const w = await mountEditor({ modelValue: src })
    expect(w.emitted('update:modelValue')).toBeUndefined()

    // The guard must report the ORIGINAL bytes back, not the reformatted
    // ones. `unchanged` would mean the baseline was the churned form.
    const guarded = (
      w.vm as unknown as { guardedValue: () => { value: string; verdict: string } }
    ).guardedValue()
    expect(guarded.value).toBe(src)
    expect(guarded.verdict).toBe('churn-suppressed')
    w.unmount()
  })

  it('keeps the original bytes for a body that needs no reformatting', async () => {
    const src = '# Title\n\nsome text\n'
    const w = await mountEditor({ modelValue: src })
    const guarded = (
      w.vm as unknown as { guardedValue: () => { value: string; verdict: string } }
    ).guardedValue()
    expect(guarded.value).toBe(src)
    expect(guarded.verdict).toBe('unchanged')
    w.unmount()
  })

  // The guard must sit ON the emit, not beside it. It was previously exposed
  // as an optional `guardedValue()` the form could choose to prefer, while
  // `update:modelValue` carried the raw serialization — so the churn reached
  // the save path and the guard was dead code.
  it('does not emit the reformatted body when the listener reports churn', async () => {
    const src = 'Title\n=====\n\nbody\n'
    const w = await mountEditor({ modelValue: src })
    fireMarkdownUpdated(w, '# Title\n\nbody\n')
    await flushPromises()
    expect(w.emitted('update:modelValue')).toBeUndefined()
    w.unmount()
  })

  it('emits a genuine edit through the guard', async () => {
    const src = 'Title\n=====\n\nbody\n'
    const w = await mountEditor({ modelValue: src })
    const view = (
      w.vm as unknown as {
        editorViewForTest: { state: EditorState; dispatch: (tr: unknown) => void }
      }
    ).editorViewForTest
    // Mark the document dirty the way a real keystroke would.
    view.dispatch(view.state.tr.insertText('X', 1))
    await flushPromises()

    fireMarkdownUpdated(w, '# XTitle\n\nbody\n')
    await flushPromises()
    expect(w.emitted('update:modelValue')).toEqual([['# XTitle\n\nbody\n']])
    w.unmount()
  })

  // A reload must move the baseline, or the guard would keep measuring
  // against a body the editor no longer holds.
  it('rebaselines on a new value from the parent', async () => {
    const w = await mountEditor({ modelValue: 'Title\n=====\n\nbody\n' })
    await w.setProps({ modelValue: 'Other\n-----\n\nbody two\n' })
    await flushPromises()
    const guarded = (
      w.vm as unknown as { guardedValue: () => { value: string; verdict: string } }
    ).guardedValue()
    expect(guarded.value).toBe('Other\n-----\n\nbody two\n')
    expect(guarded.verdict).toBe('churn-suppressed')
    w.unmount()
  })
})

describe('MilkdownEditor flush', () => {
  beforeEach(() => {
    searchEntities.mockReset()
    searchEntities.mockResolvedValue({ data: [] })
  })
  afterEach(() => {
    document.body.innerHTML = ''
  })

  // Milkdown's markdown listener is debounced by 200ms. The toolbar picker
  // inserts by dispatching straight on the view, so submitting immediately
  // after saved a body WITHOUT the reference — the change had not reached the
  // parent yet. Caught by e2e, not by any unit test, because the listener does
  // not fire under happy-dom at all.
  it('pushes a pending change to the parent immediately', async () => {
    const w = await mountEditor({ modelValue: 'before\n' })
    const view = (
      w.vm as unknown as {
        editorViewForTest: { state: EditorState; dispatch: (tr: unknown) => void }
      }
    ).editorViewForTest
    view.dispatch(view.state.tr.insertText('X', 1))
    await flushPromises()
    expect(w.emitted('update:modelValue')).toBeUndefined()

    ;(w.vm as unknown as { flush: () => void }).flush()
    await flushPromises()
    expect(w.emitted('update:modelValue')).toEqual([['Xbefore\n']])
    w.unmount()
  })

  // The flush must not become a way around the guard.
  it('emits nothing when the only difference is round-trip churn', async () => {
    const w = await mountEditor({ modelValue: 'Title\n=====\n\nbody\n' })
    ;(w.vm as unknown as { flush: () => void }).flush()
    await flushPromises()
    expect(w.emitted('update:modelValue')).toBeUndefined()
    w.unmount()
  })

  it('is safe to call twice', async () => {
    const w = await mountEditor({ modelValue: 'before\n' })
    const view = (
      w.vm as unknown as {
        editorViewForTest: { state: EditorState; dispatch: (tr: unknown) => void }
      }
    ).editorViewForTest
    view.dispatch(view.state.tr.insertText('X', 1))
    await flushPromises()

    const vm = w.vm as unknown as { flush: () => void }
    vm.flush()
    vm.flush()
    await flushPromises()
    expect(w.emitted('update:modelValue')).toEqual([['Xbefore\n']])
    w.unmount()
  })
})

// A read-only body must not reach the form, whatever changed the document.
// The toolbar and ProseMirror's own input handlers are gated elsewhere; these
// pin the emit channel itself, so a path nobody gated (the task-list toggle
// was one) still cannot produce a save.
describe('MilkdownEditor read-only emit channel', () => {
  beforeEach(() => {
    searchEntities.mockReset()
    searchEntities.mockResolvedValue({ data: [] })
  })
  afterEach(() => {
    document.body.innerHTML = ''
  })

  // The component's defineExpose surface, which `w.vm` does not type.
  type Exposed = {
    flush: () => void
    editorViewForTest: { state: EditorState; dispatch: (tr: unknown) => void }
  }

  async function mountDirtyReadonly() {
    const w = await mountEditor({ modelValue: 'before\n', readonly: true })
    const exposed = w.vm as unknown as Exposed
    const view = exposed.editorViewForTest
    // A transaction ProseMirror's gate does not see, like the task-list
    // toggle was: the document is dirty, so only the read-only gate is left.
    view.dispatch(view.state.tr.insertText('X', 1))
    await flushPromises()
    return { w, exposed }
  }

  it('flush emits nothing', async () => {
    const { w, exposed } = await mountDirtyReadonly()
    exposed.flush()
    await flushPromises()
    expect(w.emitted('update:modelValue')).toBeUndefined()
    w.unmount()
  })

  it('the markdown listener emits nothing', async () => {
    const { w } = await mountDirtyReadonly()
    fireMarkdownUpdated(w, 'Xbefore\n')
    await flushPromises()
    expect(w.emitted('update:modelValue')).toBeUndefined()
    w.unmount()
  })
})
