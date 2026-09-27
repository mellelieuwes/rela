/**
 * Which `@` the mention menu belongs to, and the scope chip drawn over it.
 *
 * The menu opens only when the user TYPES an `@` (TKT-39TIB4, spec 1.5, 1.6,
 * 8.2, 8.3). Reading the text before the cursor, which is how the menu used to
 * decide, cannot tell a freshly typed `@foo` from an old one the cursor happens
 * to land after, so clicking into existing prose, pasting, or undoing an
 * insertion all reopened it.
 *
 * This plugin records the position of the typed `@` and follows it through
 * every edit. It lets go, for good, as soon as that `@` no longer starts the
 * token the cursor is in: the `@` is deleted, the cursor leaves the token, a
 * space ends it, or the editor loses focus. Only a newly typed `@` arms it
 * again. Escape does NOT disarm; it only hides the menu, so editing the query
 * brings the menu back (spec 8.1).
 *
 * # The chip is a decoration, not a node
 *
 * A chosen scope is document text (`@ticket:`, see `mentionPlan.ts`), and
 * styling it is purely visual: the text stays text, so Backspace deletes the
 * `:` and unscopes with no special key handling. The chip is drawn while the
 * `@` is armed, which includes after Escape, since the query can still bring
 * the menu back. Once released, `@ticket:fa` reads as the plain text it is.
 */
import { Plugin, PluginKey, type EditorState } from '@milkdown/kit/prose/state'
import { Decoration, DecorationSet, type EditorView } from '@milkdown/kit/prose/view'
import { MAX_QUERY_LENGTH, TERMINATORS, triggersAfter } from './mentionQuery'

interface ArmState {
  /** Document position of the typed `@`, or null when the menu owns none. */
  at: number | null
  /**
   * End of the typed token: the position after the last character typed into
   * it. Text that was already there before the `@` was typed lies beyond it,
   * so an insertion never swallows the word the `@` was typed in front of.
   */
  end: number
}

const DISARMED: ArmState = { at: null, end: 0 }

const mentionArmKey = new PluginKey<ArmState>('rela-mention-arm')

/** The position of the armed `@`, or null. */
export function armedAt(state: EditorState): number | null {
  return mentionArmKey.getState(state)?.at ?? null
}

/** The armed token: from the `@` to the end of what was typed into it. */
export interface ArmedToken {
  from: number
  to: number
  /** The text between the `@` and the cursor, as the menu should be showing it. */
  query: string
}

/**
 * The armed `@` token, or null when there is none.
 *
 * This is the range an insertion replaces (TKT-39TIB4, spec 6.4). It comes
 * from positions the plugin mapped through every edit, never from a width
 * remembered at the last menu update, which could be stale by the time a
 * waiting Enter commits.
 */
export function armedToken(state: EditorState): ArmedToken | null {
  const arm = mentionArmKey.getState(state)
  if (!arm || arm.at === null) return null
  const cursor = state.selection.from
  return {
    from: arm.at,
    to: Math.max(arm.end, cursor),
    query: state.doc.textBetween(arm.at + 1, cursor, undefined, '\uFFFC'),
  }
}

/** Releases the armed `@`, so the menu stays shut until one is typed again. */
export function disarmMention(view: EditorView): void {
  if (armedAt(view.state) === null) return
  view.dispatch(view.state.tr.setMeta(mentionArmKey, DISARMED))
}

/**
 * True when the cursor is in code, where an `@` is literal (spec 1.4).
 *
 * Inline code is a MARK in Milkdown, so its backticks are not in the text and
 * the query's backtick terminator cannot see it; a code block is a node whose
 * spec says `code`.
 */
function inCode(state: EditorState): boolean {
  const { $from } = state.selection
  if ($from.parent.type.spec.code) return true
  const marks = state.storedMarks ?? $from.marks()
  return marks.some((m) => m.type.spec.code)
}

/** True when the `@` at `at` still starts the token ending at the cursor. */
function ownsCursor(state: EditorState, at: number): boolean {
  const { selection, doc } = state
  if (!selection.empty) return false
  const cursor = selection.from
  if (cursor <= at || at < 0 || at >= doc.content.size) return false
  if (!doc.resolve(at).sameParent(selection.$from)) return false
  if (doc.textBetween(at, at + 1, undefined, '￼') !== '@') return false
  const query = doc.textBetween(at + 1, cursor, undefined, '￼')
  return query.length <= MAX_QUERY_LENGTH && !TERMINATORS.test(query)
}

export interface MentionArmOptions {
  /**
   * The type a `name:` prefix scopes to, or null when `name` is no type.
   * Decides whether a chip is drawn.
   */
  scopeTypeFor: (name: string) => string | null
  /**
   * Called after a blur released the `@`.
   *
   * A blur changes neither the document nor the selection, and SlashProvider
   * skips `shouldShow` for such an update, so the menu has to be closed from
   * here or it would stay on screen over a disarmed query.
   */
  onBlur?: () => void
}

/** The chip over `@ticket:`, when the armed query starts with a known type. */
function chipDecorations(state: EditorState, opts: MentionArmOptions): DecorationSet | null {
  const at = armedAt(state)
  if (at === null) return null
  const query = state.doc.textBetween(at + 1, state.selection.from, undefined, '￼')
  const colon = query.indexOf(':')
  if (colon <= 0 || opts.scopeTypeFor(query.slice(0, colon)) === null) return null
  return DecorationSet.create(state.doc, [
    Decoration.inline(at, at + colon + 2, { class: 'mention-scope-chip' }),
  ])
}

/** Builds the plugin. */
export function mentionArmPlugin(opts: MentionArmOptions): Plugin<ArmState> {
  return new Plugin<ArmState>({
    key: mentionArmKey,
    state: {
      init: () => DISARMED,
      apply(tr, prev, _oldState, newState) {
        const meta = tr.getMeta(mentionArmKey) as ArmState | undefined
        let { at, end } = meta ?? prev
        if (meta === undefined && at !== null && tr.docChanged) {
          const mapped = tr.mapping.mapResult(at, 1)
          at = mapped.deleted ? null : mapped.pos
          // assoc 1: a character typed AT the end extends the token.
          end = tr.mapping.map(end, 1)
        }
        if (at !== null && !ownsCursor(newState, at)) return prev.at === null ? prev : DISARMED
        if (at === null) return prev.at === null ? prev : DISARMED
        return at === prev.at && end === prev.end ? prev : { at, end }
      },
    },
    props: {
      // Typed text arrives here; pasted text, drops and undo do not, which is
      // exactly the distinction the menu needs. The insertion is dispatched
      // here rather than left to ProseMirror so the arm rides on the same
      // transaction as the `@` it points at.
      handleTextInput(view, from, to, text) {
        if (text !== '@' || inCode(view.state)) return false
        // An email address is not a mention, and arming it would draw a chip
        // over `name@ticket:` that the menu never opens for.
        const { $from } = view.state.selection
        const prev = from > $from.start() ? view.state.doc.textBetween(from - 1, from) : ''
        if (prev !== '' && !triggersAfter(prev)) return false
        view.dispatch(
          view.state.tr
            .insertText(text, from, to)
            .setMeta(mentionArmKey, { at: from, end: from + 1 })
        )
        return true
      },
      handleDOMEvents: {
        blur(view) {
          disarmMention(view)
          opts.onBlur?.()
          return false
        },
      },
      decorations: (state) => chipDecorations(state, opts),
    },
  })
}
