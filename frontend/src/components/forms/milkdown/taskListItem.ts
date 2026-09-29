/**
 * GFM task lists: a real checkbox in the editor, and a command to make one.
 *
 * Milkdown's GFM preset parses and serializes `- [ ] item` correctly and adds a
 * three-valued `checked` attribute to `list_item` (`null` = not a task, `false`
 * = open, `true` = done). What it does NOT do is draw anything: its `toDOM`
 * emits `<li data-item-type="task" data-checked="false">` and leaves the
 * affordance to the host app.
 *
 * That is why task lists rendered as bare bullets (BUG-KHQXHH). The editing
 * surface wears `md-body` and inherits `styles/markdown-content.css`, whose
 * task-list rules all key on `input[type='checkbox']` — an element that was
 * never there, so every selector missed.
 *
 * The fix is a node view that injects a real `<input type="checkbox">` as the
 * item's first child, which is the shape both `marked` (client) and goldmark
 * (server) already produce. Matching their DOM rather than styling Milkdown's
 * data attributes is deliberate: it keeps ONE stylesheet governing all three
 * surfaces, so the editor cannot drift from the rendered view. The preset also
 * ships no task-list command, only an input rule, so `toggleTaskListCommand`
 * supplies the toolbar's inverse.
 */
import { $command, $view } from '@milkdown/kit/utils'
import { listItemSchema } from '@milkdown/kit/preset/commonmark'
import type { Command, Transaction } from '@milkdown/kit/prose/state'
import { wrapInList } from '@milkdown/kit/prose/schema-list'
import type { Node as ProseNode } from '@milkdown/kit/prose/model'
import type {
  EditorView,
  NodeView,
  NodeViewConstructor,
  ViewMutationRecord,
} from '@milkdown/kit/prose/view'

/** Command slice name, referenced by `editorCommands.ts`. */
export const TOGGLE_TASK_LIST_COMMAND = 'ToggleTaskList'

/**
 * Whether a `list_item` node is a task item.
 *
 * `checked` is three-valued, so this asks whether it is set at all rather than
 * comparing against a state. A probe written as `checked === false` would read
 * a ticked box as "not a task" and pop the toolbar button out mid-edit.
 */
export function isTaskItem(node: ProseNode): boolean {
  return node.type.name === 'list_item' && node.attrs.checked != null
}

/**
 * The checkbox, plus the wrapper ProseMirror fills with the item's content.
 *
 * Built with `createElement`/`textContent` only — no `innerHTML` anywhere, so
 * nothing from an entity body can reach a DOM sink. The input carries the
 * state; the item's text stays in `contentDOM` and remains fully editable.
 */
class TaskListItemView implements NodeView {
  readonly dom: HTMLLIElement
  readonly contentDOM: HTMLElement
  private readonly checkbox: HTMLInputElement
  private node: ProseNode

  constructor(node: ProseNode, view: EditorView, getPos: () => number | undefined) {
    this.node = node

    this.dom = document.createElement('li')
    this.dom.dataset.itemType = 'task'

    this.checkbox = document.createElement('input')
    this.checkbox.type = 'checkbox'
    this.checkbox.checked = node.attrs.checked === true
    // The checkbox is chrome, not text. Without this ProseMirror treats it as
    // editable content and will happily place a cursor inside it.
    this.checkbox.contentEditable = 'false'

    // `mousedown` only suppresses caret placement; it does NOT toggle.
    // ProseMirror reads mousedown to decide where to put the selection, so
    // letting it through drops a cursor inside the checkbox.
    this.checkbox.addEventListener('mousedown', (event) => event.preventDefault())

    // The toggle hangs off `click`, which fires for BOTH a pointer click and
    // Space on a focused checkbox. Binding `mousedown` instead missed the
    // keyboard path entirely: Space fires `click` with no preceding
    // `mousedown`, so the browser ticked the input while the document kept the
    // old value, and the next redraw silently reverted it — a lost edit the
    // write-back guard could not see, because as far as the document was
    // concerned nothing had happened.
    //
    // The click is deliberately NOT cancelled. Calling `preventDefault` on a
    // checkbox makes the browser RESTORE the pre-click checkedness after the
    // handler returns (the HTML spec's legacy-canceled-activation behavior),
    // which also undoes anything `update()` set during the dispatch — leaving
    // a ticked document under an unticked box. So the browser's own flip is
    // allowed to stand and the dispatch below makes the document agree with
    // it; `update()` remains authoritative on every later redraw. The early
    // returns cover the cases where no write is possible or allowed, where the
    // flip would otherwise be a tick backed by nothing.
    this.checkbox.addEventListener('click', () => {
      // A read-only editor (e.g. an external system owns the body) renders the
      // box but must not write it. `editable: false` only gates ProseMirror's
      // own input handlers; this listener is ours, so it has to ask itself.
      if (!view.editable) {
        this.checkbox.checked = this.node.attrs.checked === true
        return
      }
      const pos = getPos()
      if (pos == null) {
        // No position means no document write is possible, so the browser's
        // flip would be a tick backed by nothing. Put the input back.
        this.checkbox.checked = this.node.attrs.checked === true
        return
      }
      const target = view.state.doc.nodeAt(pos)
      if (!target) {
        this.checkbox.checked = this.node.attrs.checked === true
        return
      }
      view.dispatch(
        view.state.tr.setNodeMarkup(pos, undefined, {
          ...target.attrs,
          checked: target.attrs.checked !== true,
        })
      )
    })

    this.contentDOM = document.createElement('div')

    this.dom.appendChild(this.checkbox)
    this.dom.appendChild(this.contentDOM)
    this.syncAttrs(node)
    // contentDOM is filled by ProseMirror after construction, so the initial
    // name has to come from the node rather than from the DOM.
    const initial = node.textContent.trim()
    if (initial) this.checkbox.setAttribute('aria-label', initial)
  }

  /**
   * Mirrors the node's attributes onto the `li`.
   *
   * Values are written exactly as the preset's own `toDOM` writes them, so a
   * copy of this DOM parses back to the same attributes. In particular
   * `checked` is stringified as-is rather than collapsed to `=== true`: it is
   * three-valued, and writing `"false"` for an unset (`null`) attribute would
   * make a plain bullet parse back as an unchecked TASK item.
   */
  private syncAttrs(node: ProseNode) {
    this.dom.dataset.label = String(node.attrs.label)
    this.dom.dataset.listType = String(node.attrs.listType)
    this.dom.dataset.spread = String(node.attrs.spread)
    this.dom.dataset.checked = String(node.attrs.checked)
  }

  /**
   * Gives the checkbox an accessible name from the item's own text.
   *
   * Without it a screen reader announces a bare "checkbox, not checked" with no
   * indication of WHICH task, which on a checklist of a dozen items is unusable.
   * A `<label>` would be the conventional association, but the item's text lives
   * in `contentDOM`, which ProseMirror owns and rewrites — so the name is
   * mirrored onto `aria-label` on each update instead.
   */
  private syncLabel() {
    const text = this.contentDOM.textContent?.trim()
    if (text) this.checkbox.setAttribute('aria-label', text)
    else this.checkbox.removeAttribute('aria-label')
  }

  /**
   * Accepts an in-place update when the item is still a task item.
   *
   * Returning false for a non-task item makes ProseMirror discard this view and
   * fall back to the preset's plain `toDOM`, which is exactly what should
   * happen when the command clears `checked`.
   */
  update(node: ProseNode): boolean {
    if (node.type !== this.node.type || !isTaskItem(node)) return false
    // `checked` is reasserted unconditionally rather than only when the attrs
    // object changed. The click handler calls `preventDefault` to stop the
    // browser's native flip, but a user agent may still have moved the
    // property before the document caught up, so the input can be out of step
    // with a node whose attributes did NOT change. Re-stating it is one
    // property write and keeps the DOM honest.
    this.checkbox.checked = node.attrs.checked === true
    if (node.attrs !== this.node.attrs) this.syncAttrs(node)
    this.node = node
    this.syncLabel()
    return true
  }

  /**
   * Keeps ProseMirror from mapping mutations inside the checkbox back into the
   * document. The input is generated chrome; only `contentDOM` holds content.
   */
  ignoreMutation(mutation: ViewMutationRecord): boolean {
    return !this.contentDOM.contains(mutation.target)
  }

  /**
   * Claims only the events this view handles itself.
   *
   * Deliberately NOT every event on the checkbox: swallowing `keydown` too
   * would leave a user who has tabbed to the box unable to arrow out of it,
   * because ProseMirror never sees the key that should move the selection.
   */
  stopEvent(event: Event): boolean {
    return event.target === this.checkbox && (event.type === 'mousedown' || event.type === 'click')
  }
}

/**
 * Registers the node view for `list_item`.
 *
 * Bound to the base commonmark `list_item` schema because that is the node the
 * GFM preset extends with `checked` — there is no separate task-item node type.
 * Non-task items return no view and keep the preset's rendering.
 */
export const taskListItemView = $view(listItemSchema.node, () => {
  // ProseMirror accepts a null return to mean "no custom view, use toDOM", but
  // `NodeViewConstructor` types the result as non-nullable. The cast is on the
  // CONSTRUCTOR rather than on a fake view object, so no code downstream is
  // told a null is a NodeView.
  const construct = (node: ProseNode, view: EditorView, getPos: () => number | undefined) => {
    if (!isTaskItem(node)) return null
    return new TaskListItemView(node, view, getPos)
  }
  return construct as unknown as NodeViewConstructor
})

/**
 * Every `list_item` the selection touches, outermost-first.
 *
 * A collapsed cursor yields the one item it sits in; a selection spanning
 * several yields all of them, so the button applies across a range the way
 * Bullet list and Quote beside it do. Restricting this to the selection head
 * converted only the first item and left the rest untouched, with no signal
 * that the operation had been partial.
 *
 * The two cases resolve depth in OPPOSITE directions, deliberately:
 *
 * - A collapsed cursor takes the INNERMOST enclosing item, so putting the
 *   caret in a nested child toggles that child rather than its parent. This
 *   matches `isNodeActive` in `activeFormats.ts`, which also stops at the first
 *   match walking up — so the button's pressed state always describes the item
 *   the press will act on.
 * - A range takes the SHALLOWEST items it reaches and does not descend, so
 *   selecting a parent and its nested child toggles the parent once instead of
 *   changing two levels of structure the user did not separately select.
 */
function selectedListItems(state: EditorView['state']): Array<{ pos: number; node: ProseNode }> {
  const { $from, $to, empty } = state.selection

  if (empty || $from.pos === $to.pos) {
    for (let depth = $from.depth; depth > 0; depth--) {
      const node = $from.node(depth)
      if (node.type.name === 'list_item') return [{ pos: $from.before(depth), node }]
    }
    return []
  }

  const found: Array<{ pos: number; node: ProseNode }> = []
  state.doc.nodesBetween($from.pos, $to.pos, (node, pos) => {
    if (node.type.name !== 'list_item') return true
    found.push({ pos, node })
    // Don't descend: a nested list inside this item would otherwise be
    // toggled as well, changing structure the user did not select.
    return false
  })
  return found
}

/**
 * Toggles the items the selection touches between task items and plain ones.
 *
 * The preset offers only `wrapInTaskListInputRule` (typing `[ ] ` inside an
 * existing item), so the toolbar and `/` menu need this inverse. The three-state
 * attribute is why the toggle lives inside the command instead of being
 * expressed as a `toggleTo` slice name: from `null` it goes to `false`, and from
 * EITHER `false` or `true` back to `null`, so a ticked item reverts in one press
 * rather than cycling through unticked.
 *
 * A mixed selection turns ON: if any item is not yet a task, the press makes
 * them all tasks, and only a fully-task selection reverts. Otherwise pressing
 * once on a mixed selection would convert some items and revert others, leaving
 * the list exactly as inconsistent as before.
 *
 * On a bare paragraph it wraps in a bullet list first, so one press produces a
 * task item from anywhere. Splitting that into "make a list, then press again"
 * would be a worse button than the bullet-list one beside it.
 */
export const toggleTaskListCommand = $command(TOGGLE_TASK_LIST_COMMAND, () => (): Command => {
  return (state, dispatch) => {
    const items = selectedListItems(state)

    if (items.length > 0) {
      if (!dispatch) return true
      const turningOn = items.some((item) => item.node.attrs.checked == null)
      const tr = state.tr
      for (const item of items) {
        // Positions come from the pre-transaction doc, and `setNodeMarkup`
        // preserves node sizes, so no mapping is needed between steps.
        tr.setNodeMarkup(item.pos, undefined, {
          ...item.node.attrs,
          checked: turningOn ? (item.node.attrs.checked ?? false) : null,
        })
      }
      dispatch(tr)
      return true
    }

    // Not in a list yet. Wrap first, then mark every item the wrap produced,
    // in ONE transaction: `wrapInList` is given a capturing dispatch so its
    // steps land on a transaction this command owns. Letting it dispatch for
    // itself and then dispatching again would push a change through a channel
    // the caller never handed us, and would leave the document briefly wrapped
    // but unmarked.
    const bulletList = state.schema.nodes.bullet_list
    if (!bulletList) return false

    let wrapped: Transaction | null = null
    const capture = (tr: Transaction) => {
      wrapped = tr
    }
    if (!wrapInList(bulletList)(state, dispatch ? capture : undefined)) return false
    if (!dispatch || !wrapped) return true

    const tr = wrapped as Transaction
    const next = state.apply(tr)
    for (const item of selectedListItems(next)) {
      tr.setNodeMarkup(item.pos, undefined, { ...item.node.attrs, checked: false })
    }
    dispatch(tr)
    return true
  }
})

/** Everything this module contributes, in one `.use()`-able array. */
export const taskList = [taskListItemView, toggleTaskListCommand].flat()
