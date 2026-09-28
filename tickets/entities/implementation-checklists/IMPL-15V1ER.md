---
id: IMPL-15V1ER
type: implementation-checklist
title: 'Implementation: Remove the global data-entry write lock (writeMu)'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Development

- [x] Unit tests written for new code (GenerateSequentialID, writeAttachmentBusy, attachment ErrBusy/caller-cancel, CAS retry paths)
- [x] Integration tests written (test full flow, not just units): entitymanager TestConcurrency_* on memstore, fsstore, sqlite, postgres and postgres-two-processes; TestWebhookConflict_CrossProcessAppendsAllLand; TestHandleV1Action_DoesNotBlockConcurrentWrites; TestProvision_ConcurrentFirstWritesCreateOne; attachment concurrent-upload tests
- [x] Happy path implemented
- [x] Edge cases from planning handled (disjoint patches, unique races, ID minting bursts, managed order, relation upsert/delete races, attachment cap and replace, lock wait expiry vs caller cancel)
- [x] Error handling in place (errors surfaced, not swallowed): ErrBusy → 503 attachment_busy; exhausted CAS retries surface ErrConflict; webhook exhaustion → 409

## Test Quality

- [x] Using fixture builders or factories for test data (setupAttachmentService, concurrencyManager, newActionTestApp)
- [x] No hardcoded values in assertions when object is in scope
- [x] Only specifying values that matter for the test
- [x] Interpolated values constructed from objects, not hardcoded
- [x] Property comparisons use original object, not hardcoded strings

## Manual Verification

- [x] Feature manually tested end-to-end (driven through the HTTP router in dataentry tests and against a local PostgreSQL with two stores on one schema)
- [x] Each acceptance criterion verified with test scenario from planning
- [x] Edge cases manually verified

**Verification Evidence:**

- `just test`, `just lint` (0 issues), `just arch-lint`, `just comment-lint`, `just plimsoll`, `just coverage-check` (80.3%): all pass.
- `just test-postgres` passes, including TestConcurrency_* (postgres, postgres-two-processes) and TestWebhookConflict_* with -race.
- `go test -race -tags sqlite -run TestConcurrency_ ./internal/entitymanager/` passes.
- AC1: TestHandleV1Action_DoesNotBlockConcurrentWrites: an action spinning on a property completes once a concurrent PATCH lands.
- Cross-process webhook appends: 8 deliveries over two routers, all 200, all lines present.

## Quality

- [x] Code follows project patterns (check similar code): consumer-side attachment.Locker; lock.For only at wiring sites; store.Tx as the one transaction seam
- [x] Checked for DRY opportunities — repeated literals, expressions, or
patterns extracted to a helper / constant / type where it sharpens the contract
(don't extract for its own sake; CLAUDE.md "three similar lines is better than a
premature abstraction" still holds)
- [x] No security issues introduced (provisioning seam guard rewritten to assert withProvision on every write handler)
- [x] No silent failures (errors logged AND returned)
- [x] No debug code left behind
