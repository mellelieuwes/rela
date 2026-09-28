---
id: IMPL-6QUQ80
type: implementation-checklist
title: 'Implementation: Predicate engine: typed value selection with and/or (c and x or y)'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Development

- [x] Unit tests written for new code
- [x] Integration tests written (test full flow, not just units)
- [x] Happy path implemented
- [x] Edge cases from planning handled
- [x] Error handling in place (errors surfaced, not swallowed)

Unit: `internal/predicate/selection_test.go` (mapping, Lua semantics, literal
coercion, compile errors, hint, dependencies), `prefilter_test.go`
(`TestPrefilter_IgnoresValueSelection`, mutation-checked: fails with the guard
removed), `frontend/src/utils/conditions.test.ts`. Integration:
`computed_test.go` through `Compile`+`Evaluate` (enum/int mapping,
computed-on-computed dependency inside a branch, `related()` in a branch
refused); `cli/list_test.go` runs the reporter's `--filter` through
`applyListFilters`.

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

**Verification Evidence:** Scratch project `/tmp/selproj` with the reporter's
schema (enum `auth_method` → enum `assurance`, plus an integer band), built from
this branch:
- `rela validate`: "All configuration files are valid." (AC1)
- Created five access points; stored values: password → low/1, password_otp → medium/1, passkey → high/2, hardware_key → high/3, no method → low/1. (AC1, AC3, AC4)
- `rela list access_point --filter "(entity.method == 'passkey' and 'high' or 'low') == 'high'"` → only AP-003 (passkey). (AC2)
- `--filter "entity.method and 'x' or 'y'"` → `'and' requires bool on left, got string`. (AC5)
- AC5-AC9 further covered by the unit tests above; `just test`, `just coverage-check`, arch-lint and comment-lint pass; golangci-lint clean on the changed packages.

## Quality

- [x] Code follows project patterns (check similar code)
- [x] Checked for DRY opportunities — repeated literals, expressions, or
patterns extracted to a helper / constant / type where it sharpens the contract
(don't extract for its own sake; CLAUDE.md "three similar lines is better than a
premature abstraction" still holds)
- [x] No security issues introduced
- [x] No silent failures (errors logged AND returned)
- [x] No debug code left behind

Selection reuses `coerceOneLiteral`/`coerceLiteralOperands`; `logicalType` is
shared by the walker and `coerceSelection` so both apply one set of type rules.
