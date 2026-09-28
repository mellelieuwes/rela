---
id: IMPL-GB1A12
type: implementation-checklist
title: 'Implementation: Actions on the entity detail page: available_on, when, permission and a confirm prompt'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Development

- [x] Unit tests written for new code (dataentryconfig/action_scope_test.go, conditionlint viewcondition tests, cli acl audit "action" case, EntityDetail.actions.test.ts, EntityList confirm test)
- [x] Integration tests written (test full flow, not just units) (dataentry/detailactions_test.go drives GET + POST /_action through the router with a declarative ACL and memory audit)
- [x] Happy path implemented
- [x] Edge cases from planning handled (wrong type/face, when false, missing permission, unreadable/absent/bare address, hidden field in when, no compiler wired)
- [x] Error handling in place (errors surfaced, not swallowed) (403 permission_required / action_not_available, 404 entity_not_found; SPA shows script-error dialog or toast with correlation id)

## Test Quality

- [x] Using fixture builders or factories for test data (newDetailActionApp, detailMeta, detailPolicy, viewResponse helpers)
- [x] No hardcoded values in assertions when object is in scope
- [x] Only specifying values that matter for the test
- [x] Interpolated values constructed from objects, not hardcoded
- [x] Property comparisons use original object, not hardcoded strings

## Manual Verification

- [x] Feature manually tested end-to-end
- [x] Each acceptance criterion verified with test scenario from planning
- [x] Edge cases manually verified

**Verification Evidence:**

rela-server built with the embedded SPA, run on a copy of prototypes/worlds with
a `regenerate-security` action (policy, face draft, when `entity.owner ==
'security'`, permission `policies:regenerate` held by the editor only, string
confirm).

- Affordance: editor on POL-001@draft gets `action:regenerate-security: true`; POL-001@published, POL-002@draft (owner hr) and the translator (no permission) do not.
- Direct POST refusals, script not run (content unchanged): published face -> 403 action_not_available; owner hr -> 403 action_not_available; translator and reader -> 403 permission_required; POL-999 -> 404 entity_not_found.
- Success: POST as editor returns `{"message":"Regenerated POL-001@draft"}`; draft content rewritten, published face untouched; audit log records `update-entity` on POL-001 with principal edith@example.com / data-entry.
- Browser: detail page of POL-001@draft shows a Regenerate button; clicking opens the dialog with the configured text; confirming runs the script and the page shows the regenerated body without a manual refresh.
- Gates: go test (via just coverage-check, 80.3% total, all floors pass), just lint 0 issues, arch-lint OK, comment-lint OK, plimsoll OK, frontend typecheck OK, vitest 3216 passed, eslint 0 errors.

## Quality

- [x] Code follows project patterns (check similar code) (single verdict function like commandAuthorizer; SPA runner mirrors NextActionOffers)
- [x] Checked for DRY opportunities (entityReadableInRequest extracted and shared with commandHandler.entityReadable)
- [x] No security issues introduced (when evaluated on redacted entity; re-check under write lock; uniform 404)
- [x] No silent failures (errors logged AND returned) (fail-closed paths log a warning)
- [x] No debug code left behind
