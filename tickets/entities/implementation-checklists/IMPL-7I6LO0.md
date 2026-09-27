---
id: IMPL-7I6LO0
type: implementation-checklist
title: 'Implementation: related() constraints match the final entity id and current_user.id'
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
Built rela-server and ran it on a scratch fs project with the atlas shape
(taak, persoon, verantwoordelijk_voor / heeft_verantwoordelijke, acl.yaml with
user_entity_type persoon and principal_property sub), identity via
-principal-header X-User:

- sub-alice: mijn = TAAK-1, alles-van-mij = TAAK-1, TAAK-2
- sub-bob: mijn = TAAK-3, alles-van-mij = TAAK-3
- sub-nobody (no persoon): both scopes empty
- no header: HTTP 500 acl_unstamped_principal, no rows

Automated: go test ./... on default, sqlite and postgres tags (postgres
against a local scratch database) pass. The postgres run covers the storetest
conformance cases and the new EXPLAIN test; the sqlite EXPLAIN test runs in the
default build. Next-action MatchAllWith now maps ErrNoCurrentUser from the
traversal pre-pass to ErrIdentityRequired, so an unidentified caller gets 200
with no suggestion instead of a 500 (found while adding the next-action test).

## Quality

- [x] Code follows project patterns (check similar code)
- [x] Checked for DRY opportunities — repeated literals, expressions, or
patterns extracted to a helper / constant / type where it sharpens the contract
(don't extract for its own sake; CLAUDE.md "three similar lines is better than a
premature abstraction" still holds)
- [x] No security issues introduced
- [x] No silent failures (errors logged AND returned)
- [x] No debug code left behind
