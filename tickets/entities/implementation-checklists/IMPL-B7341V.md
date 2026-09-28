---
id: IMPL-B7341V
type: implementation-checklist
title: 'Implementation: TestQueryTracer_FromPoolEmits races the listener goroutine on its log buffer'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Development

- [x] ~~Unit tests written for new code~~ (N/A: test-only change; lockedBuffer is a test helper)
- [x] ~~Integration tests written (test full flow, not just units)~~ (N/A: the fix is to an existing integration test)
- [x] Happy path implemented
- [x] Edge cases from planning handled
- [x] Error handling in place (errors surfaced, not swallowed)

## Test Quality

- [x] ~~Using fixture builders or factories for test data~~ (N/A: no test data involved)
- [x] No hardcoded values in assertions when object is in scope
- [x] Only specifying values that matter for the test
- [x] Interpolated values constructed from objects, not hardcoded
- [x] Property comparisons use original object, not hardcoded strings

## Manual Verification

- [x] Feature manually tested end-to-end
- [x] Each acceptance criterion verified with test scenario from planning
- [x] Edge cases manually verified

**Verification Evidence:** Forced the late listener catch-up with temporary
sleeps (listener 300ms before catchUp, test 600ms before its read): the old test
reports DATA RACE in 3 of 3 runs, the fixed one in 0 of 3. Sleeps reverted; the
final diff touches only tracer_pool_test.go. `go test -race -tags postgres -run
TestQueryTracer -count=20` passes; golangci-lint and comment-lint clean.

## Quality

- [x] Code follows project patterns (check similar code)
- [x] Checked for DRY opportunities — repeated literals, expressions, or
patterns extracted to a helper / constant / type where it sharpens the contract
(don't extract for its own sake; CLAUDE.md "three similar lines is better than a
premature abstraction" still holds)
- [x] No security issues introduced
- [x] No silent failures (errors logged AND returned)
- [x] No debug code left behind
