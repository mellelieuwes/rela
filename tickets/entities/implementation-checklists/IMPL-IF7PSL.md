---
id: IMPL-IF7PSL
type: implementation-checklist
title: 'Implementation: Comments on a non-default face 404 on database backends and resolve text anchors against the wrong face'
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

- The new handler tests (`TestComments_FacedThreadUnderRelationConferredGrant`,
`TestComments_TextAnchorResolvesAgainstItsFace`) and
`TestComments_PerFaceThreads` fail against the old handler and pass with the
fix.
- storetest `AddressStringIsNotAnID` passes on memstore, fsstore, sqlite and
postgres (`RELA_TEST_DATABASE_REQUIRED=1`).
- The face-delete hook is covered in `entitymanager` (`TestAliasHook_FiresOnFaceDelete`),
`appbuild` (fan-out) and `comments` (`TestEntityFaceDeleted_DropsOnlyThatFace`).
- `TestReaders_ResolveAnAddressToItsFace` covers the four address-taking readers.
- `go test ./...`, `just lint`, `just arch-lint`, `just plimsoll` and
`just comment-lint` pass.

## Quality

- [x] Code follows project patterns (check similar code)
- [x] Checked for DRY opportunities — repeated literals, expressions, or
patterns extracted to a helper / constant / type where it sharpens the contract
(don't extract for its own sake; CLAUDE.md "three similar lines is better than a
premature abstraction" still holds)
- [x] No security issues introduced
- [x] ~~No silent failures (errors logged AND returned)~~ (N/A as written: alias-hook failures are logged and not returned by design, because the store write already landed; matches the existing rename and delete hooks)
- [x] No debug code left behind
