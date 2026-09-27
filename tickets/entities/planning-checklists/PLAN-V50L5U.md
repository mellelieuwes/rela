---
id: PLAN-V50L5U
type: planning-checklist
title: 'Planning: Replacement suggestions on text comments'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Understanding

- [x] Problem/requirements clearly understood
- [x] Scope defined (what's in/out documented below)
- [x] Acceptance criteria documented with specific test scenarios

**Scope:**

In scope: an optional replacement on TEXT-anchored comments; an accept endpoint
that splices the replacement into the entity body through `PatchEntity` and
resolves the comment; SPA composer input, diff display and Accept button. User
decisions (2026-09-25): the server applies the change, and only body-text
anchors carry suggestions.

Out of scope: suggestions on property/section anchors; image/diagram block
anchors (`BlockCommentOverlay`); multi-range tracked changes; a distinct
"rejected" state (resolve covers it); storing "accepted by/at" on the comment
(the audit log records the write).

**Acceptance Criteria:**

1. `POST /_comments/{type}/{id}` with a text anchor and `replacement` stores
it; GET echoes it. Test: handler test round trip, all four backends via
`commentstest` round-trip with a text anchor carrying a replacement.
2. A property or section anchor with `replacement` is refused with 400.
Test: handler table test.
3. An empty replacement is a valid "delete this text" suggestion, distinct
from "no suggestion". Test: create with `""`, accept, quote removed.
4. `POST .../{commentID}/accept` replaces exactly the resolved range, marks
the comment resolved, and the write lands in the audit log under the accepting
principal. Test: handler test asserts stored body, resolved flag, audit entry.
5. Accept is refused: detached or uncertain anchor (409 `suggestion_stale`);
matched text differs from the quote beyond whitespace (409); comment already
resolved (409); comment has no replacement (400); read-only instance (403);
caller cannot read target (404); caller lacks entity update (403 from the
manager). Test: one handler subtest each.
6. Stored comments written before this change load unchanged. Test:
filecomments fixture YAML without the field; round-trip equality.
7. SPA: select text, choose "Suggest a change", edit the prefilled text, post;
the thread shows old/new; Accept updates the rendered body and resolves the
thread. Test: e2e in `comments.spec.ts`.

## Research

- [x] For larger features: run `/research` to create a structured research doc
- [x] Searched for existing libraries that solve this problem
- [x] Checked codebase for similar patterns or reusable code
- [x] Looked for reference implementations in other projects
- [x] Reviewed relevant rela concepts for prior art

**Research Doc:** N/A. RES-XRYX18 already covers text anchoring; this ticket
adds no new anchoring question, and the approach is not in doubt.

**Existing Solutions:**

- No library needed; `comments.ResolveText` (textanchor v0.2.0) already
yields the byte range to splice.
- `handleV1UpdateEntity` (`write_handler.go:657-881`) is the pattern for the
write: `enterWrite`, raw read by ref, `Patch.ExpectedVersion =
store.VersionOf(read)`, `writePatchError` mapping.
- `webhook_routes.go:500-581` documents the body read-modify-write risk that
`ExpectedVersion` closes.
- Reference UX: Google Docs "suggesting" and GitHub review "suggested
changes", both applying a single-range replacement on accept.

## Approach

- [x] Technical approach chosen and documented
- [x] Approach builds on existing patterns (not reinventing)
- [x] Alternatives considered (document why rejected)
- [x] Dependencies identified (packages, APIs, types)

**Technical Approach:**

Domain (`internal/comments`):
- `Anchor.Replacement *string` (`json/yaml:"replacement,omitempty"`), not on
`TextAnchor`, which mirrors the locator library (RR: replacement placement). A
pointer because `""` (delete the text) differs from "no suggestion". It lives in
the anchor because it is immutable after creation, and the anchor is already
JSON/YAML in every backend, so no SQL migration.
- `Anchor.Validate` checks the replacement: only on a `text` kind; at most
`MaxBodyBytes`; valid UTF-8; no control chars other than newline/tab; no bidi
overrides or zero-width characters (U+200B-200F, U+202A-202E, U+2066-2069,
U+FEFF); refused when the source quote crosses a block boundary (contains a
blank line).
- `comments.ApplyReplacement(body, anchor) (string, error)`: resolves; accepts
only when the whitespace-collapsed matched span equals the collapsed quote AND
(confidence >= `ConfidenceExact` OR the quote occurs exactly once); otherwise
`ErrSuggestionStale`. Splices `body[:Start] + repl + body[End:]`. Pure and
unit-tested. A sibling `Acceptable(body, anchor) bool` drives the wire hint.
- Accept reuses `Authorizer.CanRead` (read floor + `comment:read`). The entity
update right is enforced by `PatchEntity`. Resolving as part of accept does not
require `comment:update-*`: the accepter is usually not the author, and the
accept is authorized by the entity write.
- memcomments deep-copies pointer fields in `Get`/`List`; its doc is fixed.

HTTP (`internal/dataentry`):
- `addCommentRequest.Anchor.Replacement *string`; `anchorWire.Replacement`;
`commentWire.Acceptable` computed server-side (has replacement, unresolved,
`Acceptable` predicate true). The SPA additionally requires the entity's
`_actions.update`.
- New route `POST /_comments/{type}/{id}/{commentID}/accept` (four-segment
case in the dispatcher; `router_walk_test.go` entry). Order:
  1. `refuseIfReadOnly`, then `enterWrite` (write lock + provision re-stamp).
  2. `gateCommentTarget` (404 on unreadable/missing), `Get` comment (404),
`CanRead` (403).
  3. No replacement: 409 `no_suggestion`. Already resolved: 409
`comment_resolved`.
  4. Raw read by `target.Key()` through a raw ref reader. Locked: 422
`encrypted_inaccessible`. Raw content differs from the visible content: 409
(fails closed if body redaction ever lands).
  5. `ApplyReplacement`; stale: 409 `suggestion_stale`.
  6. Resolve the comment FIRST (`svc.Update(resolved=true)`), then
`PatchEntity(target.Key(), Patch{Content, ExpectedVersion: VersionOf(raw)})`. On
patch failure, un-resolve and return the patch error via `writePatchError` (a
not-found from the patch maps to 404). If un-resolve also fails, the response
says the comment is resolved but the text was not applied. This order makes the
failure mode "resolved, not applied" (visible, reopenable) instead of a silent
double-apply when the replacement keeps the quote.
  7. 200 with `{content, warnings}`. No ETag (it needs edge readers the
handler does not have); the SPA applies `content`.
- `commentsHandler` gains `writeMu *sync.Mutex`, `provision`, a consumer-side
`entityPatcher` (`PatchEntity` only) and a raw ref reader, all in scope at
`app.go:1242`. It gets its own `enterWrite` method, and the comments file is
added to `provision_seam_invariant_test.go`.
- Known v1 limit, documented in code: `Store.Update` rewrites body and
resolved, so a comment edit racing the accept between `Get` and `Update` is
lost.

Frontend:
- `api/comments.ts`: `replacement?: string | null` on anchors,
`acceptable: boolean`, `acceptComment()` returning `{content, warnings}`.
- `TextSelectionComment.vue`: "Suggest a change" toggle revealing a textarea
prefilled with the stored SOURCE quote. `resolveCheckResponse` gains
`source_quote` (the stored `Quote`, a substring of a body the caller may already
read) so the prefill never comes from the DOM selection.
- `TextCommentPopover.vue`, `CommentsPanel.vue`: old/new diff rendered as text
(no `v-html`), old side = `anchor.quote`; Accept shown when `acceptable` and the
entity has `_actions.update`; disabled while the body editor is open, dirty or
saving.
- `EntityDetail.vue`: on `accepted`, `applyServerContent(content)` then
reload comments and view.

Attribution: the accepter is the responsible party for the body change (like
merging a pull request); audit and history record the accepter. Documented in
`docs/comments.md`.

**Alternatives rejected:**
- Top-level `Comment.Suggestion` column: needs a pg migration and sqlite ALTER
handling for no gain, since the value is immutable like the anchor.
- Client applies the edit through the normal entity PATCH: the client works in
rendered coordinates, not source bytes, so it cannot splice reliably.
- Requiring `comment:update-any` to accept: couples a content decision to
comment moderation rights.

**Files to modify:** `internal/comments/{comments.go,textresolve.go,authz.go}` +
tests, `internal/comments/commentstest/commentstest.go`,
`internal/dataentry/{comments_handler.go,comments_wiring.go,comments_handler_test.go,router_walk_test.go}`,
`frontend/src/api/comments.ts`,
`frontend/src/components/entity/{TextSelectionComment,TextCommentPopover,CommentsPanel,EntityDetail}.vue`
+ unit tests, `e2e/tests/comments.spec.ts`, `e2e/pages/comments.page.ts`,
`docs/comments.md`.

## Security Considerations

- [x] Input sources identified (user input, config, external APIs)
- [x] Input validation approach defined (allowlist preferred over blocklist)
- [x] Security-sensitive operations identified (file access, auth, crypto)
- [x] Error handling doesn't leak sensitive information

**Input Sources & Validation:**
- `replacement` (untrusted JSON): allowlisted characters (newline/tab only),
size cap, UTF-8; 400 otherwise.
- Path `commentID`: resolved only within the gated target via `Get`.

**Security-Sensitive Operations:**
- Entity body write: through `PatchEntity` only, so ACL, field gate, lock
guard, validation and audit all apply; attributed to the accepting principal.
The accepter sees the exact diff before accepting.
- Face: the patch targets `target.Key()`, the face the gate resolved, so a
suggestion on a draft never writes the published face.
- Existence: accept on an unreadable target returns the same 404 as the other
comment routes.
- Rendering: replacement text is shown as text, never `v-html`; once in the
body it goes through the existing sanitized markdown renderer.
- Read-only instances refuse accept before any other gate.

## Test Plan

- [x] Test scenarios documented for each acceptance criterion
- [x] Edge cases identified and documented
- [x] Negative test cases defined (invalid input, error conditions)
- [x] Integration test approach defined (not just unit tests)

**Test Scenarios:** see acceptance criteria 1-7; each names its test.

**Edge Cases:**
- Empty replacement (deletion); replacement containing newlines and markdown.
- Quote spanning an fsstore 80-column reflow (resolver absorbs whitespace).
- Repeated quote: prefix/suffix select the right occurrence.
- Accepting on a faced (draft) thread writes only that face.
- Body edited between list and accept so the quote moved (still exact: apply)
or was rewritten (409).
- Concurrent write between read and patch: store version conflict, 412.
- Accept twice: second returns 409 (resolved).
- Replacement that contains the quote, with the patch step failing: comment
ends un-resolved, body unchanged.
- Quote inside emphasis; quote across a bullet (refused at create).
- Accepter cannot see a hidden property: accept succeeds, property preserved.
- Empty-string replacement round-trips on all four backends.
- Typing in the SPA after accept does not revert it (e2e).

**Negative Tests:** oversize replacement, NUL or bidi override in replacement,
replacement on property anchor, accept without replacement, locked entity (422),
ACL deny on update (403), read-only (403).

**Integration:** handler tests drive the full route against the memstore app
with a real entitymanager; e2e drives the SPA flow.

## Risk Assessment

- [x] Technical risks assessed with mitigations
- [x] Security risks assessed (see Security Considerations)
- [x] Effort estimated (xs/s/m/l/xl)

**Risks:**
- Body write and comment resolve are two stores, not atomic. Mitigation:
resolve first, patch second, un-resolve on patch failure.
- Wrong range spliced. Mitigation: collapsed quote equality plus exact band
or a unique quote.
- SPA autosave reverting an accept (autosave sends no If-Match). Mitigation:
Accept disabled while the body editor is open/dirty/saving; response content
applied locally.

Effort: l.

## Documentation Planning

- [x] User-facing docs identified (skip if internal refactor)
- [x] Docs-checklist will be created when entering implementation

**Documentation Impact:**
- [x] docs/comments.md: suggestions, accept route, permissions
- [x] ~~docs/metamodel.md~~ (N/A: no config change)

## Design Review

- [x] Run `/design-review` before starting implementation
- [x] All critical/significant findings addressed in plan

**Design Review Findings:** 15 review-responses linked to TKT-S5C0K3 (4
significant, 5 minor, 6 nit); all folded into this plan.
