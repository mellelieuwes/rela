---
id: IMPL-P9M2N3
type: implementation-checklist
title: 'Implementation: Rebuild the @ mention menu against an agreed behaviour spec'
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
- Screenshots of the built e2e server: bare `@` starting list, `@f` types
only, `@feat` results then a compact type row, `@feature:` chip in the document
with the menu scoped, Enter inserting a titled reference.
- `markdown-editor-mention-autocomplete.spec.ts`: 16 tests, 80/80 passes over
5 repeats. Full e2e suite: 321 passed; the 2 failures were a fixture API timeout
and `apps.spec.ts` toolbar tests that fail at the same rate on a baseline build
of develop.
- Unit: 3265 frontend tests pass; `go test ./internal/dataentry` passes.

## Quality

- [x] Code follows project patterns (check similar code)
- [x] Checked for DRY opportunities — repeated literals, expressions, or
patterns extracted to a helper / constant / type where it sharpens the contract
(don't extract for its own sake; CLAUDE.md "three similar lines is better than a
premature abstraction" still holds)
- [x] No security issues introduced
- [x] No silent failures (errors logged AND returned)
- [x] No debug code left behind
