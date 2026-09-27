/**
 * Deciding whether the `@` completion menu should be showing.
 *
 * `SlashProvider`'s own `shouldShow` only fires while the trigger character is
 * the last thing typed, so it cannot follow a query as the user extends it.
 * This replaces it: the text before the cursor is re-read on every update and
 * the answer is derived from that, together with whether the `@` was typed
 * (`mentionArm.ts`).
 *
 * It decides visibility only. The range an insertion replaces comes from the
 * arm plugin (`armedToken`), whose positions follow every edit; a width kept
 * here could be stale by the time a waiting Enter commits.
 */
import { parseMentionQuery } from './mentionQuery'

/**
 * How many characters past a query with no matches the menu stays open.
 *
 * Other editors either hide the menu at once (GitHub, Slack) or show "No
 * results" briefly (Notion). A short grace keeps the note readable without the
 * menu covering prose the user is plainly writing (TKT-39TIB4, spec 7.3).
 */
export const NO_MATCH_GRACE = 3

export interface MentionTriggerHost {
  /** True while the menu is showing. */
  isOpen: () => boolean
  /** Hides the menu. */
  close: () => void
  /** Reports the query the menu should be searching for. */
  setQuery: (query: string) => void
}

export interface MentionTrigger {
  /**
   * Answers `SlashProvider.shouldShow`.
   *
   * `armed` is whether the `@` before the cursor was typed, per `mentionArm`.
   * An unarmed `@query` is ordinary text and never opens the menu.
   */
  shouldShow: (content: string | undefined, armed: boolean) => boolean
  /** Suppresses the menu for the query currently typed, until it changes. */
  dismiss: (query: string) => void
  /**
   * Records that `query` was searched and nothing matched.
   *
   * The menu then closes once the query grows `NO_MATCH_GRACE` characters past
   * it, and comes back if it is edited to something that no longer extends it.
   */
  markNoMatch: (query: string) => void
  /** Records that a search found rows, which ends any no-match grace. */
  markMatch: () => void
}

export function createMentionTrigger(host: MentionTriggerHost): MentionTrigger {
  /**
   * The query Escape dismissed.
   *
   * Without this the menu reopens on the next update, because the text before
   * the cursor still parses as a query. Editing it — typing or deleting —
   * produces a different query and the menu is welcome back.
   */
  let dismissedQuery: string | null = null
  /** The shortest query in the current run that matched nothing. */
  let noMatchBase: string | null = null

  function hide(): false {
    if (host.isOpen()) host.close()
    return false
  }

  return {
    shouldShow(content, armed) {
      const match = armed ? parseMentionQuery(content) : null
      if (!match) {
        dismissedQuery = null
        noMatchBase = null
        return hide()
      }
      if (dismissedQuery !== null && match.query === dismissedQuery) return false
      dismissedQuery = null
      if (noMatchBase !== null) {
        if (!match.query.startsWith(noMatchBase)) noMatchBase = null
        else if (match.query.length >= noMatchBase.length + NO_MATCH_GRACE) return hide()
      }
      host.setQuery(match.query)
      return true
    },
    dismiss(query) {
      dismissedQuery = query
    },
    markNoMatch(query) {
      if (noMatchBase === null || !query.startsWith(noMatchBase)) noMatchBase = query
    },
    markMatch() {
      noMatchBase = null
    },
  }
}
