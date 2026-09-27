---
id: IMPL-IK16SL
type: implementation-checklist
title: 'Implementation: Replacement suggestions on text comments'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Development

- [x] Unit tests written for new code
- [x] Integration tests written (test full flow, not just units)
- [x] Happy path implemented
- [x] Edge cases from planning handled
- [x] Error handling in place (errors surfaced, not swallowed)

## Test Quality

- [x] Using fixture builders or factories for test data
- [x] No hardcoded values in assertions when object is in scope
- [x] Only specifying values that matter for the test
- [x] Interpolated values constructed from objects, not hardcoded
- [x] Property comparisons use original object, not hardcoded strings

## Manual Verification

- [x] Feature manually tested end-to-end
- [x] Each acceptance criterion verified with test scenario from planning
- [x] Edge cases manually verified

**Verification Evidence:**

- Go: `internal/comments/suggestion_test.go` (validation table, splice incl.
reflow, unique quote with edited context, refusals), the `commentstest` contract
on all four backends (pgcomments against a scratch database), and
`internal/dataentry/comments_suggestion_test.go` (create, refusals, accept,
failed-write reopen, face isolation, ACL matrix, hidden-property preservation).
`just test` passes.
- Frontend: `SuggestionComments.test.ts` and `CommentsPanel.test.ts` (source-quote
prefill, no-op refusal, diff as text, Accept gating). 200 files / 3218 tests
pass; typecheck clean.
- End to end in Chromium (`e2e/tests/comments.spec.ts`, "suggests a replacement
and accepts it into the body"): select text, suggest, diff shows old and new,
Accept rewrites the body in place, the comment is resolved, and the change
survives a reload. Ran three times, all green.
- Edge cases verified by tests: empty replacement deletes; stale quote refuses
with 409 and leaves the comment open; resolved comment refuses; reviewer without
update gets 403 and the comment reopens; read-only instance refuses.

## Quality

- [x] Code follows project patterns (check similar code)
- [x] Checked for DRY opportunities — repeated literals, expressions, or
patterns extracted to a helper / constant / type where it sharpens the contract
(don't extract for its own sake; CLAUDE.md "three similar lines is better than a
premature abstraction" still holds)
- [x] No security issues introduced
- [x] No silent failures (errors logged AND returned)
- [x] No debug code left behind
