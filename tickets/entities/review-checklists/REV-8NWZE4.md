---
id: REV-8NWZE4
type: review-checklist
title: 'Review: Actions on the entity detail page: available_on, when, permission and a confirm prompt'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Automated Checks

- [x] All tests pass — Go tests for dataentry, dataentryconfig, conditionlint, appbuild, affordances and cli pass; frontend vitest 3216 passed; typecheck clean
- [x] Lint clean — golangci-lint 0 issues; eslint 0 errors; arch-lint OK; plimsoll OK
- [x] Comment lint gate clean — `just comment-lint` OK
- [x] Coverage maintained — `just coverage-check` passes (80.6% total)

## Code Review

- [x] Ran cranky-code-reviewer and rela-security-reviewer over the diff
- [x] All critical review-responses addressed (none raised)
- [x] All significant review-responses addressed — RR-1EHLXB, RR-V2C1MI, RR-GWU59O, RR-WSMO9K, RR-CR2CG5
- [x] Self-reviewed the diff for unrelated changes — the only side change is extracting `useActionFeedback` from Sidebar and NextActionOffers, which RR-U1V9EM asked for

**Review Responses:** RR-1BMQQ7, RR-1EHLXB, RR-2ITHME, RR-41GI2S (deferred:
PendingButton needs a pending label actions do not have), RR-458DWA, RR-5JIB63,
RR-7NVGPR, RR-CR2CG5, RR-FCQZSR, RR-GWU59O, RR-HRYRNU, RR-JSEBF1, RR-NFDXHQ,
RR-U1V9EM, RR-V2C1MI, RR-W0M5FP, RR-WFHPV7, RR-WSMO9K, RR-X2QAWY, RR-YN42Q1,
RR-Z4NKSY

## Acceptance Verification

- [x] Each acceptance criterion tested (reference planning checklist PLAN-OWTQD5)
- [x] Test evidence documented in implementation checklist IMPL-GB1A12

**Acceptance Status:**

1. PASS — offered only when type, face, when and permission match: `detailactions_test.go` table cases for wrong type, wrong face, when false and missing permission; manual run on a copy of prototypes/worlds showed the button only for the editor on POL-001@draft.
2. PASS — direct POST refused in each case and the script does not run: same table asserts 403 `action_not_available` / `permission_required` and no script marker; `TestDetailAction_AffordanceAndGateAgree`; manual curl got 403 for the published face, owner hr, translator and reader, and 404 for POL-999.
3. PASS — writes audited under the invoking principal: test asserts the audit record's user; manual run logged `update-entity` by edith@example.com.
4. PASS — confirm takes a string or boolean: `action_scope_test.go` (string, true, rejected list/number/unquoted yes/Off, quoted yes stays text); `EntityDetail.actions.test.ts` shows the configured text and the default; `EntityList.test.ts` covers bulk confirm for both.
5. PASS — result message shown and entity reloaded: `EntityDetail.actions.test.ts` asserts the toast and a second fetchView; the browser run showed the toast and the regenerated content.
6. PASS — load-time validation of when, faces and permission: `action_scope_test.go` and `viewcondition_test.go`; `acl_audit_permissions_test.go` covers the unknown-permission report.
7. PASS — docs/data-entry.md documents the fields under Actions with a regenerate-document example.

## Documentation (enhancements only)

- [x] Docs-checklist created and linked via `has-docs`
- [x] User-facing documentation updated — docs/data-entry.md and docs/data-entry/api-reference.md
- [x] Docs-checklist marked as done

**Docs Checklist:** DOCS-EYVORI

## Final Checks

- [x] Commit message explains the why, not just what
- [x] No TODOs or FIXMEs left unaddressed — none added by this diff
- [x] Ready for another developer to use

## Pull Request

- [x] ~~Run `/pr` command~~ (N/A: the PR is opened after the ticket is done; see TKT-UFV01M)
