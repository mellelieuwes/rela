---
id: IMPL-10TK9T
type: implementation-checklist
title: 'Implementation: Twins stage 1: pacts, twins store, reconcile and ownership enforcement'
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

End-to-end against a real external system: an operator project (the Eyra product
model) declares `pacts: basecamp: theirs: ["*"]` on its Scenario type. Its 31
Basecamp todos were linked and pulled with `rela twin link` / `pull`
(translation done outside rela, as intended): every twin ended `in_sync`, a
second identical pull reported "nothing to write" with the entity unchanged,
`rela twin pending` reported nothing pending, and `rela update SC-015 -s final`
was refused with "field status of SC-015 is owned by basecamp (Twin); change it
there" while relations stayed editable. A stale `--remote-updated-at` was
refused; deleting a twinned entity listed its twin as `gone`.

Automated evidence per acceptance criterion is listed in PLAN-5CJNK8; every
regression test added for a review finding was checked to fail with its fix
disabled.

## Quality

- [x] Code follows project patterns (check similar code)
- [x] Checked for DRY opportunities — repeated literals, expressions, or
patterns extracted to a helper / constant / type where it sharpens the contract
(don't extract for its own sake; CLAUDE.md "three similar lines is better than a
premature abstraction" still holds)
- [x] No security issues introduced
- [x] No silent failures (errors logged AND returned)
- [x] No debug code left behind
