/**
 * The entity ID grammar, as the editor checks it.
 *
 * Its own module, free of Milkdown, so code outside the editor (the recently
 * viewed list in `utils/recentEntities.ts`) can validate an ID without pulling
 * ProseMirror into its bundle. `entityRefNode.ts` re-exports it.
 */

/**
 * Reject IDs that would break the serialized code span.
 *
 * Mirrors the backend's one ID grammar, `internal/entity.ValidateID` (which
 * `store/storeutil.ValidateID` now delegates to, TKT-IZGF7T). That is an
 * ALLOWLIST — `^[A-Za-z0-9][A-Za-z0-9_-]*$` plus no `--` and no `..` — not the
 * looser denylist an earlier version of this file carried. A denylist here
 * would accept ids the backend refuses, so a code span like `foo.bar` would
 * render as a link to an entity that cannot exist.
 *
 * `entityRefIdGrammar.test.ts` asserts this agrees with the Go rule over a
 * shared fixture, so the two cannot drift silently.
 */
const MAX_ID_BYTES = 1024
const ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_-]*$/

export function isValidEntityRefId(id: unknown): id is string {
  if (typeof id !== 'string' || id === '' || id.length > MAX_ID_BYTES) return false
  if (id.includes('--')) return false
  if (id.includes('..')) return false
  return ID_PATTERN.test(id)
}
