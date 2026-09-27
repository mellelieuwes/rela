/**
 * Placing an `entityRef` node into the document.
 *
 * Both routes to a reference end here — the `@` completion menu and the
 * toolbar's picker — so the node's shape and the cursor handling after it are
 * written once. Extracted from the editor component, which is over the
 * god-component line and was carrying two near-identical copies of this.
 *
 * A title passed in is DISPLAY ONLY and never reaches the markdown; the node
 * serializes to its id alone. It is carried so a just-inserted reference reads
 * correctly straight away, rather than showing a bare id until the next
 * mentions refresh — which would not contain the new id anyway.
 */
import { closeHistory } from '@milkdown/kit/prose/history'
import { TextSelection } from '@milkdown/kit/prose/state'
import type { EditorView } from '@milkdown/kit/prose/view'
import { isValidEntityRefId } from './entityRefId'

export interface EntityRefInsert {
  id: string
  /** Display title, or empty to let it resolve later. */
  title: string
  entityType: string | null
}

/**
 * Replaces [from, to) with an entity reference.
 *
 * Returns false when the id is not a valid reference, so a caller cannot write
 * a node the serializer would then have to make sense of.
 */
export function replaceWithEntityRef(
  view: EditorView,
  ref: EntityRefInsert,
  from: number,
  to: number
): boolean {
  if (!isValidEntityRefId(ref.id)) return false

  const { state } = view
  const nodeType = state.schema.nodes.entityRef
  if (!nodeType) return false

  const node = nodeType.create({
    id: ref.id,
    title: ref.title || null,
    entityType: ref.entityType,
    inaccessible: false,
  })

  const tr = state.tr.replaceWith(from, to, node)
  // A space after the reference, so typing on does not get absorbed into the
  // node — unless a space or punctuation already follows, where one more would
  // leave `see [Title] .` (TKT-39TIB4, spec 6.3). The node is one position
  // wide, hence from + 1.
  const next = tr.doc.textBetween(from + 1, Math.min(from + 2, tr.doc.resolve(from + 1).end()))
  let after = from + 1
  if (!FOLLOWS_WITHOUT_SPACE.test(next)) {
    tr.insertText(' ', after)
    after += 1
  }
  tr.setSelection(TextSelection.create(tr.doc, after))
  // Its own undo step: undo brings back the typed `@query` rather than
  // removing the query together with the reference.
  closeHistory(tr)
  view.dispatch(tr)
  return true
}

/** A character after which an inserted reference needs no space of its own. */
const FOLLOWS_WITHOUT_SPACE = /^[\s.,;:!?)\]}'"]/

/** Inserts at the cursor, replacing any selection. */
export function insertEntityRefAtCursor(view: EditorView, ref: EntityRefInsert): boolean {
  const { from, to } = view.state.selection
  return replaceWithEntityRef(view, ref, from, to)
}

/** A document range: the armed `@` token, per `mentionArm.armedToken`. */
export interface TokenRange {
  from: number
  to: number
}

/** Replaces the `@query` token with an entity reference. */
export function replaceMentionQueryWithRef(
  view: EditorView,
  ref: EntityRefInsert,
  range: TokenRange
): boolean {
  return replaceWithEntityRef(view, ref, range.from, range.to)
}

/**
 * Replaces the query after the `@` with `text`, keeping the `@`.
 *
 * Used to write a chosen type scope: `@ti` becomes `@ticket:`. The characters
 * typed to find the type are consumed, not kept as a search (spec 4.1). The
 * cursor lands at the end, so the menu goes on reading the new query.
 */
export function replaceMentionQueryWithText(
  view: EditorView,
  text: string,
  range: TokenRange
): boolean {
  const start = range.from + 1
  const tr = view.state.tr.insertText(text, start, range.to)
  tr.setSelection(TextSelection.create(tr.doc, start + text.length))
  view.dispatch(tr)
  return true
}
