---
id: PLAN-OWTQD5
type: planning-checklist
title: 'Planning: Actions on the entity detail page: available_on, when, permission and a confirm prompt'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Understanding

- [x] Problem/requirements clearly understood
- [x] Scope defined (what's in/out documented below)
- [x] Acceptance criteria documented with specific test scenarios

**Scope:**

In scope:
- `actions.<id>.available_on` (`entity_types`, optional `faces`), `when`, `permission`, and `confirm` as bool or string.
- Server-side affordance: per-entity GET adds `_actions["action:<id>"] = true` for each matching action. The SPA renders only those keys; labels and confirm text come from `/_config` (config is not secret).
- Server-side enforcement in `POST /api/v1/_action/<id>`.
- Detail-page buttons in `EntityDetail.vue`, a confirm dialog, the result toast, and an entity reload after success.
- Load-time validation, `rela acl audit` permission collection, and docs.

Out of scope:
- Edit forms (autosave and CAS conflict, TKT-34XS2R).
- List rows: `_actions["action:*"]` is emitted on per-entity GET only, so list pages do not pay one condition evaluation per row.
- Changing the write lock or the 5 s timeout (see Risks; a follow-up ticket if needed).

**Acceptance Criteria:**
1. Affordance: `action:<id>` is present only when type, face, `when` and `permission` all match. Tests cover each of the four mismatches plus the positive case.
2. Enforcement: a direct POST is refused in each of the four cases, and the script does not run (the script writes a marker entity; the test asserts it is absent).
3. Audit: a successful run's write carries the invoking principal (audit sink and entity attribution checked).
4. `confirm:` parses `true`, `false` and a string; `/_config` serialises the string; the SPA dialog shows it (vitest).
5. The SPA shows the returned message and re-fetches the entity after success (vitest on EntityDetail).
6. Load errors: an unparseable `when`, a `when` naming an unknown property, `faces` naming an undeclared face, `entity_types` naming an unknown type, and `faces` on a type without faces. Action permissions are added to `dataEntryPermissions.UsedPermissions`, which is how command permissions are cross-checked today. There is no load-time check against acl.yaml for commands either.
7. Docs: `docs/data-entry.md` gets an "Actions on the detail page" section with the regenerate-document example.

## Research

- [x] For larger features: run `/research` to create a structured research doc
- [x] Searched for existing libraries that solve this problem
- [x] Checked codebase for similar patterns or reusable code
- [x] Looked for reference implementations in other projects
- [x] Reviewed relevant rela concepts for prior art

**Research Doc:** N/A. Every piece reuses an existing in-repo pattern; there is
no open choice between approaches beyond the timeout question below.

**Existing Solutions:**
- Commands: `CommandScope` / `matchesPage` (`internal/dataentry/commands.go:257`) define `available_on.entity_types`; the new block is a separate `ActionScope` type, because commands' `views`/`lists`/`dashboard` keys do not apply to detail actions.
- Entity + face gate: `commandHandler.entityReadable` + `redactEntity` (`commands.go:344`), with `entity.ParseStateRef` + `GetEntityState` for `ID@face` addresses.
- Condition compile and evaluate: `conditionlint.CompileViewConditions` / `ViewConditionMatcher` (`internal/conditionlint/viewcondition.go`), wired through the consumer-side `ViewConditionFunc` seam (`internal/dataentry/viewcondition.go`). Actions may name several types, so compilation is per (action, type), like `NextActionMatcher`.
- Permission: `gateDocumentPermission` (`standalone_document_handler.go:41`), which returns a 403 `permission_required` naming the permission.
- Permission audit: `internal/cli/acl.go` `UsedPermissions`.

## Approach

- [x] Technical approach chosen and documented
- [x] Approach builds on existing patterns (not reinventing)
- [x] Alternatives considered (document why rejected)
- [x] Dependencies identified (packages, APIs, types)

**Technical Approach:**

1. Config (`internal/dataentryconfig/config.go`): add `AvailableOn *ActionScope` (`EntityTypes`, `Faces`), `When string`, `Permission string` to `Action`. Change `Confirm bool` to a `Confirm` type that unmarshals YAML bool or string and marshals JSON as `true` or the string. `available_on` requires `script:` (a `set:` action has no script to run on the detail page).
2. Validation (`validate.go`): unknown entity types; faces declared on each listed type; `when` requires `available_on`.
3. Conditions (`internal/conditionlint`): `CompileActionConditions` returns programs keyed by (action id, entity type) using the request-scoped env (`current_user` allowed), exactly like view conditions. Compile failures are load errors. `related(...)` is refused at load, as forms do with `refuseFormTraversal`, so a `when` is a pure single-entity check (RR-7NVGPR). `current_user` is bound per request the same way list conditions bind identity. Wired through a new consumer-side `ActionConditionFunc` seam, set by the composition root the way `SetViewConditions` is.
4. One decision function in a new file `internal/dataentry/detailactions.go`:
`detailActionVerdict(ctx, id, action, e) (ok bool, reason)`. It checks type,
face, permission, then `when` against the REDACTED entity. Both the affordance
and the handler call it, so they cannot drift (the `commandAuthorizer` shape).
5. Affordance: the per-entity GET handler calls it for each action with `available_on` and adds `action:<id>` keys. Only `true` keys are emitted; absence means "not offered". `affordances_contract_test` is scoped to verb keys, and `internal/dataentry/CLAUDE.md` records the different absence semantics of `action:` keys (RR-458DWA).
6. Handler (`actions.go`): when the action has `available_on`, `entity_id` becomes REQUIRED and is parsed as an address (`ParseStateRef` + `GetEntityState`), row-gated, face-gated and redacted like the command entity path.
   - Missing or unreadable entity: uniform 404 `entity_not_found`.
   - Permission missing: 403 `permission_required`, naming action and permission (a config-declared capability, per root CLAUDE.md).
   - Type, face or `when` mismatch: 403 `action_not_available`.
Resolution and all checks run AFTER `enterWrite`, under writeMu, so no other
in-process writer can change the entity between the check and the script
(RR-CR2CG5). A refused call holds the lock only for one read. The redacted
entity is what the script receives. Actions without `available_on` keep today's
behaviour exactly, except that `permission:`, when set, is enforced on every
invocation.
7. SPA: `types/config.ts` (`confirm: boolean | string`, new fields), `EntityDetail.vue` renders buttons for `action:*` keys next to commands, uses the existing confirm modal, calls `runAction(id, address, type)` with the explicit `ID@face` address of the served row, computed the way commands already do (RR-YN42Q1), toasts the message, and reloads the entity. `useListActions` shows a confirm string too.
8. `internal/cli/acl.go`: collect action permissions.
9. Docs: `docs/data-entry.md`, `docs/data-entry/api-reference.md` (`action:<id>` keys, new errors).

Alternatives rejected:
- A separate `_detail_actions` array with label and confirm: duplicates config that `/_config` already serves.
- Emitting `action:<id>: false` for non-matching actions: lists every action on every entity, and the SPA would still need the config for labels.
- Making `permission:` bimodal like commands (denied under a policy when unset): breaks every existing action in ACL-configured projects.

**Files to modify:**
- `internal/dataentryconfig/config.go`, `validate.go`, tests
- `internal/conditionlint/actioncondition.go` (new), tests
- `internal/dataentry/actions.go`, `detailactions.go` (new), `viewcondition.go` or a sibling seam file, the entity GET handler, `app.go`, tests
- composition root(s) that call `SetViewConditions`
- `internal/cli/acl.go`
- `frontend/src/types/config.ts`, `components/entity/EntityDetail.vue`, `composables/useListActions.ts`, tests
- `docs/data-entry.md`, `docs/data-entry/api-reference.md`

## Security Considerations

- [x] Input sources identified (user input, config, external APIs)
- [x] Input validation approach defined (allowlist preferred over blocklist)
- [x] Security-sensitive operations identified (file access, auth, crypto)
- [x] Error handling doesn't leak sensitive information

**Input Sources & Validation:**
- `entity_id` from the POST body: parsed with `ParseStateRef`; any parse failure, miss, or row or face denial gives the same 404.
- `data-entry.yaml`: validated at load; `when` compiled at load.

**Security-Sensitive Operations:**
- Script execution with the caller's principal. The gate runs before the lock and before the script.
- `when` is evaluated on the redacted entity, so a hidden field reads as unset. Otherwise the button's presence would be a one-bit oracle on a hidden value.
- `permission` is an intent gate, as on documents. It is checked through the request read gate, which holds every permission under NopACL and ReadOnlyACL (RR-1BMQQ7). The godoc and docs say so, and a test pins it. The boundary for writes remains the principal's ACL in entitymanager. The same caveat applies to capabilities (http, mail, secrets) that the action declares; the docs will say so.

## Test Plan

- [x] Test scenarios documented for each acceptance criterion
- [x] Edge cases identified and documented
- [x] Negative test cases defined (invalid input, error conditions)
- [x] Integration test approach defined (not just unit tests)

**Test Scenarios:**
- AC1/AC2: one table-driven test in `internal/dataentry` over {match, wrong type, wrong face, when false, permission missing, entity unreadable, redacted field used in when}. Each case asserts both the GET `_actions` key and the POST status, and asserts the script did not run. This pins "affordance false ⇒ refused".
- AC3: POST as a named principal; assert the audit record's principal and the entity's attribution.
- AC4: config unit tests for the three YAML shapes; `/_config` JSON; vitest for the dialog text.
- AC5: vitest for EntityDetail (toast plus re-fetch).
- AC6: validate tests for each load error; `acl.go` test that action permissions are collected.
- E2E: one Playwright test on the demo project: button visible on the concept face, confirm, message shown, content updated.

**Edge Cases:**
- A faceless type with `faces:` set: load error.
- `faces` omitted: every face matches, including a bare/faceless row.
- An action with `available_on` invoked with no `entity_id` (sidebar or app): 404 `entity_not_found`.
- A `when` evaluation error (for example a date function on an unset property): refused and logged, the same way a list surfaces it, not treated as a silent match.
- A list (`lists.*.actions`), navigation entry (`action:`) or next-action offer (`action:`) that references an action with `available_on`: load error (RR-2ITHME). A sidebar call has no entity, and list rows and offers send a bare id, which has no row on a faced type. An operator who wants both surfaces declares two actions that share one script. Apps may still call it with an explicit address; the same gates apply.

**Negative Tests:** covered above.

## Risk Assessment

- [x] Technical risks assessed with mitigations
- [x] Security risks assessed (see Security Considerations)
- [x] Effort estimated (xs/s/m/l/xl)

**Risks:**
- Timeout: decided with the user to keep 5 s and document it. A follow-up for lock-free reads is filed only if a real project hits it.
- `permission:` is enforced on every invocation of an action, not only detail-page calls (decided with the user).
- `Confirm` type change: YAML stays compatible (bool still parses). The JSON wire changes from bool to bool-or-string; the SPA is updated in the same PR.
- The existing action handler resolves `entity_id` with `GetEntity(id)`, which does not parse `ID@face` on pgstore. The new path parses the address; the old path is unchanged.

Effort: l.

## Documentation Planning

For enhancements: identify what documentation needs updating.

- [x] User-facing docs identified (skip if internal refactor)
- [x] Docs-checklist will be created when entering implementation

**Documentation Impact:**
- docs/data-entry.md (Actions: detail-page section and example using `entity.kind == 'soa'`, RR-W0M5FP; `confirm` string; 5 s timeout)
- docs/data-entry/api-reference.md (`action:<id>` affordance keys, new error codes)

## Design Review

- [x] Run `/design-review` before starting implementation
- [x] All critical/significant findings addressed in plan

**Design Review Findings:** RR-CR2CG5, RR-2ITHME, RR-7NVGPR (significant);
RR-1BMQQ7, RR-458DWA, RR-YN42Q1 (minor); RR-W0M5FP (nit). All are addressed in
the approach above.
