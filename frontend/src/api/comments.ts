import { api } from './client'
import type { Warning } from '@/types/entity'

/**
 * How a comment is pinned to part of an entity.
 *
 * `property` and `section` are drift-free by construction — a property name and
 * an operator-authored section id are NAMES, not offsets, so they survive any
 * edit to the body. `text` (stage 2) anchors to body content the user CAN edit
 * away, so it stores a quote plus surrounding context and is re-located
 * server-side on every read; when that fails it reports as detached rather than
 * silently pointing somewhere wrong.
 */
export type CommentAnchorKind = 'property' | 'section' | 'text'

export interface CommentAnchor {
  kind: CommentAnchorKind
  ref: string
  /** The anchored body text, for a `text` anchor. Echoed so the UI can show
   *  WHAT was commented on even when the range no longer resolves. */
  quote?: string
  /**
   * Byte offsets into the entity body, resolved server-side on every read.
   * Present only for a text anchor that located successfully.
   *
   * Never stored — an offset is invalidated by any edit earlier in the body.
   * Slice with these, never with `quote.length`: the located range can differ
   * from the quote (it absorbs whitespace the formatter moved).
   */
  start?: number
  end?: number
  /** Resolver score, 0-1. */
  confidence?: number
  /** Located, but far enough from an exact match that the UI should say the
   *  text may have moved. */
  uncertain?: boolean
  /**
   * Suggested substitute for `quote` (text anchors only). `""` suggests
   * deleting the text; absent or null means the comment suggests nothing.
   * `quote` is the markdown SOURCE the replacement will overwrite.
   */
  replacement?: string | null
}

/** One comment as served by `/api/v1/_comments/...`. */
export interface Comment {
  id: string
  /** Server-stamped from the request principal; never client-supplied. */
  author: string
  created_at: string
  anchor: CommentAnchor
  body: string
  resolved: boolean
  /**
   * The anchor no longer names anything on the entity. Soft by design
   * (DEC-HWZHA): the comment still renders, flagged as orphaned, rather than
   * being hidden or rejected.
   */
  detached?: boolean
  /**
   * Server-computed permission hints, mirroring `_actions`. They drive which
   * controls are shown; they are not the gate. The server re-authorizes every
   * mutation, so a client that ignores them gains nothing.
   */
  editable: boolean
  deletable: boolean
  /**
   * The suggestion could be applied to the current body: it has one, the
   * comment is open, and the quote still locates unambiguously. Combine it
   * with the entity's `_actions.update`; the server re-checks both on accept.
   */
  acceptable?: boolean
}

interface CommentListResponse {
  comments: Comment[]
}

export interface AddCommentRequest {
  anchor: {
    kind: CommentAnchorKind
    ref: string
    /** For a `text` anchor: the selected body text, as RENDERED. */
    quote?: string
    /**
     * Rendered text immediately before/after the selection.
     *
     * These pick WHICH occurrence of a repeated quote was meant. They can only
     * select among real occurrences — the quote must still be found in the body
     * — so they narrow, never introduce, a location. Omitting them makes a
     * repeated quote resolve to the first match, which once put a comment on
     * "Geordend" onto "Ongeordend".
     */
    quote_prefix?: string
    quote_suffix?: string
    /** Suggested replacement for the quote's SOURCE text; `""` deletes it. */
    replacement?: string
  }
  body: string
}

/** Mutable fields. Omitting one leaves it unchanged — resolving does not require echoing the body. */
export interface UpdateCommentRequest {
  body?: string
  resolved?: boolean
}

function targetPath(entityType: string, entityId: string): string {
  return `/_comments/${encodeURIComponent(entityType)}/${encodeURIComponent(entityId)}`
}

/**
 * listComments returns a target's comments, oldest first.
 *
 * Rejects with a 404 both when the entity cannot be read and when commenting
 * is disabled — the two are deliberately indistinguishable, so callers must
 * treat a failure as "no comment thread here", never as proof of absence.
 */
export async function listComments(entityType: string, entityId: string): Promise<Comment[]> {
  const resp = await api.get<CommentListResponse>(targetPath(entityType, entityId))
  return resp.comments
}

/** addComment creates a comment. Author, id and timestamp are server-written. */
export async function addComment(
  entityType: string,
  entityId: string,
  req: AddCommentRequest
): Promise<Comment> {
  return api.post<Comment>(targetPath(entityType, entityId), req)
}

/** updateComment edits a comment's body and/or resolved flag. */
export async function updateComment(
  entityType: string,
  entityId: string,
  commentId: string,
  req: UpdateCommentRequest
): Promise<void> {
  await api.patch<void>(`${targetPath(entityType, entityId)}/${encodeURIComponent(commentId)}`, req)
}

/** deleteComment removes a comment. */
export async function deleteComment(
  entityType: string,
  entityId: string,
  commentId: string
): Promise<void> {
  await api.delete(`${targetPath(entityType, entityId)}/${encodeURIComponent(commentId)}`)
}

export interface ResolveCheck {
  anchorable: boolean
  reason?: string
  /**
   * The markdown source the selection maps to. A suggestion must start from
   * this rather than the rendered selection, or accepting it would drop any
   * markup inside the range.
   */
  source_quote?: string
  /** Whether a suggested replacement for this selection would be accepted. */
  suggestable?: boolean
}

/**
 * Asks whether a selection could be anchored, without creating anything.
 *
 * Used to decide whether to OFFER commenting: some selections have no
 * contiguous source range (one spanning table cells, for instance), and letting
 * a user write a comment that then fails on save is the worse outcome.
 *
 * A non-anchorable selection is a successful response with `anchorable: false`,
 * not an error — so a genuine network failure stays distinguishable.
 */
export async function checkAnchorable(
  entityType: string,
  entityId: string,
  quote: string,
  prefix: string,
  suffix: string
): Promise<ResolveCheck> {
  return api.post<ResolveCheck>(`${targetPath(entityType, entityId)}/resolve`, {
    quote,
    quote_prefix: prefix,
    quote_suffix: suffix,
  })
}

export interface AcceptResult {
  /** The entity body as written, to replace any local copy. */
  content: string
  warnings?: Warning[]
}

/**
 * acceptComment applies a comment's suggested replacement to the entity body
 * and resolves the comment. Requires the right to edit the entity.
 */
export async function acceptComment(
  entityType: string,
  entityId: string,
  commentId: string
): Promise<AcceptResult> {
  return api.post<AcceptResult>(
    `${targetPath(entityType, entityId)}/${encodeURIComponent(commentId)}/accept`,
    {}
  )
}
