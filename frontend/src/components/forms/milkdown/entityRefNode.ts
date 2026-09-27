/**
 * The `entityRef` ProseMirror node: an entity reference rendered as a link.
 *
 * rela writes an entity reference into markdown as an inline code span holding
 * the entity ID, `` `TKT-XXXX` ``. That storage format does not change here.
 * What changes is how the editor shows it: instead of monospaced raw ID, the
 * user sees the entity's title as a link, matching what the rendered view
 * already produces via `rewriteEntityRefToken` in `utils/markdown.ts`.
 *
 * The node is atomic. A reference is one indivisible thing: putting a cursor
 * inside `TKT-XXXX` and deleting a character would produce a reference to an
 * entity that does not exist, and the user has no reason to want that.
 *
 * ## Why a node rather than a mark
 *
 * The built-in `inlineCode` is a mark, and Milkdown's parser resolves a
 * markdown node against `{...schema.nodes, ...schema.marks}` taking the first
 * match. Object spread puts every node ahead of every mark, so an `entityRef`
 * node sees each `inlineCode` mdast node before the `inlineCode` mark does and
 * can claim the ones that look like entity references. `NodeSchema.priority`
 * does NOT do this — it only feeds `parseDOM` rule ordering — so the node/mark
 * distinction is what the behaviour rests on.
 */
import { $node } from '@milkdown/kit/utils'
import { isValidEntityRefId } from './entityRefId'

export { isValidEntityRefId } from './entityRefId'

/**
 * Decides whether a code span is an entity reference.
 *
 * This is deliberately shape-based and does not consult the mentions map. A
 * reference to an entity the user cannot read, or to one that does not exist,
 * must still round-trip through the editor unchanged. Resolving it against the
 * graph to decide whether to keep it would mean an unreadable reference is
 * silently rewritten on save, which is both a data loss and an existence
 * oracle.
 *
 * The shape is the same one the server's `scanCodeSpanCandidates` uses: a code
 * span whose entire content is a single plausible ID.
 */
export function looksLikeEntityRef(value: unknown): value is string {
  return isValidEntityRefId(value)
}

/**
 * The `entityRef` node schema.
 *
 * `toMarkdown` and `parseMarkdown` are both required by `NodeSchema`, so the
 * markdown round-trip is enforced by the type checker rather than by
 * convention. The pair is exact: `parseMarkdown` reads an `inlineCode` mdast
 * node into the `id` attribute, and `toMarkdown` writes that same `id` back as
 * an `inlineCode`. No other attribute survives serialization, which is what
 * keeps the stored bytes identical to what a user typed by hand.
 */
export const entityRefNode = $node('entityRef', () => ({
  group: 'inline',
  inline: true,
  atom: true,
  // Selectable so the whole reference can be selected and deleted as a unit,
  // and so the slash-menu insertion can replace a selected reference.
  selectable: true,
  draggable: false,
  attrs: {
    /** The entity ID. This is the only thing that is stored. */
    id: {},
    /**
     * The resolved display title, or null when it is not known.
     *
     * Not serialized: it is a view concern, and it is per-principal. A title
     * the current user may see must never be written into the markdown, since
     * the file is shared with users who may not see it.
     */
    title: { default: null },
    /**
     * The entity type, used to build the link href. Null when unresolved.
     */
    entityType: { default: null },
    /**
     * True when the entity is readable but its display title is redacted.
     * Mirrors the server's `Mention.inaccessible`.
     */
    inaccessible: { default: false },
    /**
     * Whether the title on this node came from a server mentions map.
     *
     * Distinguishes a title the server vouched for from one the picker
     * supplied locally for a just-inserted reference. Only the former is
     * cleared when a later map omits the ID, so a revoked grant drops the
     * title instead of leaving it on screen indefinitely.
     *
     * View-only, like the rest: never serialized, never parsed from the DOM.
     */
    resolvedFromServer: { default: false },
  },
  parseDOM: [
    {
      tag: 'a[data-entity-ref]',
      getAttrs: (dom) => {
        const el = dom as HTMLElement
        return {
          id: el.getAttribute('data-entity-ref'),
          title: el.getAttribute('data-entity-title'),
          entityType: el.getAttribute('data-entity-type'),
          inaccessible: el.hasAttribute('data-entity-inaccessible'),
        }
      },
    },
  ],
  toDOM: (node) => {
    const { id, title, entityType, inaccessible } = node.attrs as {
      id: string
      title: string | null
      entityType: string | null
      inaccessible: boolean
    }
    // Falls back to the raw ID when no title is known: before the mentions map
    // resolves, for a reference the principal may not read, and for a
    // reference to something that does not exist. This is the same fallback
    // `rewriteEntityRefToken` applies, and it is why showing the ID in the
    // editor is useful even though the rendered view shows only the title.
    const label = title || id
    const attrs: Record<string, string> = {
      'data-entity-ref': id,
      class: inaccessible ? 'entity-ref entity-ref-inaccessible' : 'entity-ref',
      // A real href so the reference is a genuine link: middle-click and
      // copy-link behave as the user expects. Only set when the type is
      // known, since the href shape needs it.
      ...(entityType ? { href: `/entity/${entityType}/${id}` } : {}),
      ...(title ? { 'data-entity-title': title } : {}),
      ...(entityType ? { 'data-entity-type': entityType } : {}),
      ...(inaccessible ? { 'data-entity-inaccessible': 'true' } : {}),
    }
    return ['a', attrs, inaccessible ? `${label} \u{1F512}` : label]
  },
  parseMarkdown: {
    match: (node) => node.type === 'inlineCode' && looksLikeEntityRef(node.value),
    runner: (state, node, type) => {
      state.addNode(type, { id: node.value as string })
    },
  },
  toMarkdown: {
    match: (node) => node.type.name === 'entityRef',
    runner: (state, node) => {
      // Writes exactly the code span the reference came from. The resolved
      // title and type are deliberately dropped: they are per-principal view
      // state, not content.
      state.addNode('inlineCode', undefined, node.attrs.id as string)
    },
  },
}))
