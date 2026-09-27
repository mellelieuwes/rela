---
id: IMPL-XICZDW
type: implementation-checklist
title: 'Implementation: Milkdown''s orphan timer fails the Frontend CI job after test teardown'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Development

- [x] ~~Unit tests written for new code~~ (N/A: the change is test-runner configuration; it was verified with an uncommitted stand-in test, because the symptom depends on CI worker shutdown timing)
- [x] ~~Integration tests written (test full flow, not just units)~~ (N/A: configuration-only change; the full frontend suite is the integration run)
- [x] Happy path implemented (the Milkdown teardown ReferenceError is dropped)
- [x] Edge cases from planning handled (any other unhandled error, including another ReferenceError or the same message without a `@milkdown/ctx` frame, still fails the run)
- [x] Error handling in place (errors surfaced, not swallowed) (the callback returns `false` only on an exact three-way match; otherwise it returns `undefined` and vitest reports the error)

## Test Quality

- [x] ~~Using fixture builders or factories for test data~~ (N/A: no test code added)
- [x] ~~No hardcoded values in assertions when object is in scope~~ (N/A: no test code added)
- [x] ~~Only specifying values that matter for the test~~ (N/A: no test code added)
- [x] ~~Interpolated values constructed from objects, not hardcoded~~ (N/A: no test code added)
- [x] ~~Property comparisons use original object, not hardcoded strings~~ (N/A: no test code added)

## Manual Verification

- [x] Feature manually tested end-to-end (stand-in test reproduced the CI error without the filter and passed with it)
- [x] Each acceptance criterion verified with test scenario from planning (Milkdown error dropped; a different stray error still fails the run)
- [x] Edge cases manually verified (a different stray error still fails the run with the filter in place)

**Verification Evidence:**
Stand-in test with the real `@milkdown/ctx` `Timer`, a 10 ms timeout and
`globalThis.removeEventListener` deleted: fails with the exact CI error without
the filter, passes with it. A different stray error still fails the run. Full
frontend suite (3207 tests), typecheck and lint pass.

## Quality

- [x] Code follows project patterns (check similar code) (the config already carries commented test-environment workarounds, such as the `__E2E_TEST_HOOKS__` define)
- [x] Checked for DRY opportunities — repeated literals, expressions, or
patterns extracted to a helper / constant / type where it sharpens the contract
(don't extract for its own sake; CLAUDE.md "three similar lines is better than a
premature abstraction" still holds)
- [x] No security issues introduced (test configuration only; not shipped)
- [x] No silent failures (errors logged AND returned) (only the one known error is dropped; the filter is narrow by name, message and stack)
- [x] No debug code left behind (the stand-in test was not committed)
