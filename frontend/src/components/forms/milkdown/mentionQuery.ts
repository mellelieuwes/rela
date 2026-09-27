/**
 * Parsing the `@` mention query out of the text before the cursor.
 *
 * The completion menu is triggered by `@` and stays open while the user types
 * a query. `SlashProvider.getContent` hands back the paragraph text up to the
 * cursor; this module turns that into either an active query or nothing.
 *
 * The old backtick trigger inspected CodeMirror token types to decide whether
 * the cursor sat somewhere a reference made sense. ProseMirror has no token
 * stream, and it does not need one: `SlashProvider.getContent` already returns
 * undefined outside a paragraph, on a non-empty selection, on a non-text
 * selection, when the view is read-only, and when the editor lacks focus. All
 * that is left is deciding what counts as a query.
 */

/** The longest query accepted before the menu gives up and closes. */
export const MAX_QUERY_LENGTH = 64

/**
 * Characters that end a mention query.
 *
 * A space ends it: a query is one token, with `-` as the word separator
 * (TKT-39TIB4), and leaving the menu open across a space would make it fire on
 * ordinary prose containing an `@`. Backticks end it because they open a code
 * span, where a mention is not what the user means. U+FFFC is what
 * ProseMirror's `textBetween` writes for an inline atom such as an existing
 * reference, which is not part of a query either.
 */
export const TERMINATORS = /[\s`\uFFFC]/

export interface MentionQuery {
  /** The text typed after the `@`, possibly empty right after the trigger. */
  query: string
  /**
   * How many characters back from the cursor the trigger sits, counting the
   * `@` itself. Insertion uses this to replace the trigger and query together.
   */
  matchLength: number
}

/**
 * Extracts the active mention query from the text before the cursor.
 *
 * Returns null when there is no active query, which is the signal to close the
 * menu.
 *
 * Nil: accepts undefined for `textBefore`, since `getContent` returns undefined
 * when the cursor is not somewhere a mention makes sense.
 */

/**
 * A character after which an `@` is not a trigger: a word character, as in an
 * email address or a handle, or a backtick that opens a code span.
 */
const NO_TRIGGER_AFTER = /[\p{L}\p{N}_`]/u

/** True when an `@` typed right after `prev` starts a mention. */
export function triggersAfter(prev: string): boolean {
  return !NO_TRIGGER_AFTER.test(prev)
}

export function parseMentionQuery(textBefore: string | undefined): MentionQuery | null {
  if (!textBefore) return null

  const at = textBefore.lastIndexOf('@')
  if (at === -1) return null

  const query = textBefore.slice(at + 1)
  if (query.length > MAX_QUERY_LENGTH) return null
  if (TERMINATORS.test(query)) return null

  // A denylist, not an allowlist, so `“@`, `/@` or `(@` still open the menu.
  if (at > 0 && !triggersAfter(textBefore[at - 1] ?? '')) return null

  return { query, matchLength: query.length + 1 }
}
