---
id: PLAN-INT7OG
type: planning-checklist
title: 'Planning: Remove the global data-entry write lock (writeMu)'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Understanding

- [x] Problem/requirements clearly understood
- [x] Scope defined (what's in/out documented below)
- [x] Acceptance criteria documented with specific test scenarios

**Problem:** `dataentry.App.writeMu` serializes every data-entry mutation in one
process, including Lua actions and webhook scans for their whole run. It does
nothing across processes (postgres multi-node) and causes timeouts. An audit
found the correctness it silently provides on single-process tiers: ID minting
(max+1), `unique:` scan-then-write, relation meta read-merge-write, managed
order (max+1), the post-automation rewrite in CreateEntity, attachment
cap/replace, provisioning stub create, and webhook `append_section`. All of
these are already live races on multi-process postgres.

**Scope (in):**
- Delete `writeMu`, `enterWrite`'s lock, the MCP `Attachments.WriteLock` and the CLI `attachMu`.
- Move manager check-then-write sequences into `store.Tx` (user decision: use the existing transaction concept; fs may do no more than its Tx mutex).
- Keyed `lock.Locker` per (entity, property) inside `attachment.Service`.
- Webhook `append_section` becomes a CAS (`Patch.ExpectedVersion`) with the existing retry loop.
- Relation reconciler tolerates a concurrent writer (delete not-found and create already-exists are no-ops, not 500).
- Action scripts use the default Lua timeout (user decision).

**Scope (out):**
- pgstore's `RELW` transaction advisory lock: held only for a Tx, and it is what makes the Tx approach cross-process.
- sqlitestore's internal `writeMu` (single-writer SQLite by design).
- Conflict-resolve atomic file write (fs/git only, single user; user accepted no fs locking).
- Script-level atomicity of Lua actions: each write is protected, a find-then-create across two calls is not (same as multi-process pg today); `unique:` is now atomic on every tier, which covers the upsert-by-key pattern.

**Acceptance Criteria:**
1. No process-wide mutex on the data-entry or MCP write path. Test: grep guard test replaces `provision_seam_invariant_test`; a slow action does not block a concurrent PATCH (test with a blocking script and a concurrent write that completes).
2. Concurrent creates of one type with generated IDs all succeed with distinct IDs on memstore, fsstore, sqlite and postgres. Test: N goroutines through `Manager.CreateEntity`.
3. Concurrent creates with the same `unique:` value: exactly one succeeds, the rest get the unique 422, on every backend. Test: goroutine race through the manager.
4. Concurrent relation meta updates to different keys on one edge both land. Test: goroutines through `Manager.UpdateRelation`.
5. Concurrent managed-order creates get distinct orders. Test via manager.
6. A PATCH landing between create and the post-automation rewrite is not lost. Test with an automation that sets a property plus a racing patch (hook in test).
7. Concurrent attachment uploads to a `max: 1` property leave exactly one file, and the stamp names it. Test via `attachment.Service` goroutines, memory locker.
8. Webhook `append_section`: all concurrent appends land in one process AND across two processes on postgres. `TestWebhookConflict_CrossProcessAppendsCanBeLost` becomes an all-land assertion.
9. Concurrent relation PATCHes on one entity never return 500.
10. Provisioning: two concurrent first writes for one subject create one stub on every tier.
11. `just test`, `just lint`, `just arch-lint`, `just test-postgres` pass.

## Research

- [x] ~~For larger features: run `/research`~~ (N/A: approach decided with the user; seams already exist)
- [x] ~~Searched for existing libraries~~ (N/A: uses in-tree store.Tx and lock.Locker)
- [x] Checked codebase for similar patterns or reusable code
- [x] ~~Looked for reference implementations in other projects~~ (N/A: in-tree pattern exists)
- [x] Reviewed relevant rela concepts for prior art

**Research Doc:** N/A

**Existing Solutions:**
- `store.Store.Tx` (DEC-8UIL0): fs/mem write mutex, sqlite BEGIN IMMEDIATE, pg transaction + `RELW` advisory xact lock. Pattern for manager use: `internal/entitymanager/copy.go:183` (plan outside, pure store writes through the VIEW inside) and `DeleteEntity` at `manager.go:1469`.
- `lock.Locker` (TKT-1K47YD): memory + postgres (`pgstore.AcquireKeyedLock`) backends, conformance suite; built to replace writeMu, currently unused.
- Store CAS: `UpdateEntityIf` / `Patch.ExpectedVersion`, `store.VersionConflictError` (TKT-34XS2R), already used by the entity PATCH handler.

## Approach

- [x] Technical approach chosen and documented
- [x] Approach builds on existing patterns (not reinventing)
- [x] Alternatives considered (document why rejected)
- [x] Dependencies identified (packages, APIs, types)

**Technical Approach (revised after design review):**

Rule: every check-then-write is either a store compare-and-swap with a bounded
retry, or runs inside `store.Tx` with EVERY other writer of the same object also
inside `Tx` (pgstore plain writes do not take `RELW`). Tx callbacks receive the
view explicitly and never call a public Manager method.

1. entitymanager
   - `PatchEntity`: with no caller `ExpectedVersion`, pin the write to `VersionOf(stored)` and retry the whole read-authorize-merge-`updateCore` sequence (bounded, e.g. 5) on `VersionConflictError`. Caller-supplied versions are not retried. Automations only compute a result before the write, so a retry has no side effects. Fix the godoc.
   - Generated-ID creates: mint, create, retry on `ErrConflict` (bounded). No Tx.
   - `unique:`: when the entity sets a unique property, the unique check and the store write run in one `Tx` (create, updateCore, ApplyEntity). `checkUniqueProperties` takes an explicit `store.Store`.
   - Post-create automation rewrite and `cascadeHost.WriteEntity`: CAS-and-retry (re-read, apply `PropertiesSet`, computed, unique, `UpdateEntityIf`).
   - Relations: `UpdateRelation` read-merge-write, managed order + create, renumber, `cascadeHost.WriteRelation` and `ApplyRelation` property writes run in `Tx` with the view passed to the order helpers.
2. lock: `BackendLocker` takes an in-process keyed mutex before the backend lock, so one process pins at most one pg connection per key.
3. attachment: `Deps.Locker` (required). Spool the (processed) upload to a temp file, then acquire `attachment/<entity>/<prop>` with a deadline around list, resolve name, attach, prune, stamp. Delete takes the same lock.
4. Wiring: `appbuild` exposes a `lock.Locker` (postgres: `BackendLocker` over pgstore; other tiers: `MemoryLocker`) and every `attachment.New` caller passes it. Drop `mcp` `Attachments.WriteLock`, `MCPHost.WriteLock`, CLI `attachMu`.
5. dataentry: `writeMu` removed (done); `enterWrite` becomes `withProvision`; guard test renamed. Actions use `lua.DefaultTimeout`.
6. Webhook `append_section`: `ExpectedVersion` from the re-read; existing retry loop handles the conflict.
7. Provisioning: a unique violation on the stub create is treated like already-exists and re-resolved.
8. Reconciler: delete not-found is a no-op; create already-exists applies the desired properties through `UpdateRelation`.
9. Docs and comments as listed.

**Alternatives rejected:**
- Keyed locks everywhere (per type/entity): more moving parts, pins a pg connection per held lock; Tx is the sanctioned seam and already cross-process on pg.
- Remove only and file follow-ups: regresses fs/sqlite correctness.
- Whole reconcile in Tx: each edge goes through manager authorization/automations/cascade, which must not run inside a Tx.

**Files to modify:**
- internal/entitymanager/{core.go,manager.go,manager_order.go,cascadehost.go,unique.go} (+ new tx helper, tests)
- internal/attachment/attachment.go (+ tests)
- internal/appbuild/appbuild.go, appbuild_postgres.go (+ other recipes if needed)
- internal/dataentry/{app.go,write_handler.go,attachment_handler.go,handlers_attachment.go,actions.go,webhook.go,webhook_routes.go,relations_modern.go,provision.go,mcp_http.go} and tests (provision_seam_invariant_test.go, mcp_http_test.go, webhook_conflict_postgres_test.go, write_handler_cas_test.go)
- internal/mcp/tools_attachment.go (+ test helpers), internal/cli/{mcp_wiring.go,cli_wiring.go}, cmd/rela-server/mcp.go
- internal/lock/lock.go (package doc)
- docs/webhooks.md, docs/data-entry.md, docs/data-entry/api-reference.md, docs/acl-security.md

## Security Considerations

- [x] Input sources identified (user input, config, external APIs)
- [x] Input validation approach defined (allowlist preferred over blocklist)
- [x] Security-sensitive operations identified (file access, auth, crypto)
- [x] Error handling doesn't leak sensitive information

**Input Sources & Validation:** No new inputs. Lock keys are built from an
entity ID and property name that are already validated by the attachment path,
then checked by `lock.ValidateKey`.

**Security-Sensitive Operations:**
- Unauthenticated webhook endpoint: without the global lock a flood no longer stalls other writers; the `admit` bound stays.
- Authorization stays outside and before every Tx (no change to the AuthorizeWrite choke point).
- Provisioning keeps fail-closed behavior; uniqueness becomes atomic on all tiers.
- Attachment lock acquire has a deadline so a stuck holder surfaces as an error, never an unbounded wait.

## Test Plan

- [x] Test scenarios documented for each acceptance criterion
- [x] Edge cases identified and documented
- [x] Negative test cases defined (invalid input, error conditions)
- [x] Integration test approach defined (not just unit tests)

**Test Scenarios:** One per acceptance criterion, run with `-race` on memstore
and fsstore (default build), sqlitestore (sqlite tag) and pgstore (`just
test-postgres`). Added after review:
- concurrent disjoint `PatchEntity` calls both land;
- patch racing a managed-order renumber and a cascade `WriteEntity`;
- pg: a plain write interleaving with a Tx read-merge-write;
- provisioning race: both requests 2xx, one stub;
- attachment uploads at pool `MaxConns`=4 with 8 concurrent uploads to one key;
- fs deadlock watchdog (test timeout) around every new Tx callback;
- webhook append CAS conflict retries and all appends land, in one and two processes.

**Edge Cases:**
- Manual-ID create: no retry loop, unique still enforced.
- CAS retries exhausted: the `VersionConflictError` is returned (412 on the HTTP path).
- Attachment lock deadline expiry: error, nothing written.
- Reconciler: delete of an already-deleted edge; create of an existing edge applies desired properties.

**Negative Tests:** unique violation under race is still 422; attachment `max`
exceeded still rejected; caller-supplied `ExpectedVersion` mismatch still 412
without retry.

**Integration:** HTTP tests through `App.NewRouter` for concurrent POST/PATCH;
webhook postgres conflict tests; an action that blocks while a concurrent PATCH
completes.

## Risk Assessment

- [x] Technical risks assessed with mitigations
- [x] Security risks assessed (see Security Considerations)
- [x] Effort estimated (xs/s/m/l/xl)

**Risks:**
- Deadlock from calling the outer store inside a Tx (fs mutex is not reentrant). Mitigation: Tx callbacks receive a `Deps` copy with the view; review every call inside; race tests on fsstore.
- More pg transactions taking `RELW` (writes that set a `unique:` property; relation property writes). Mitigation: the callback holds only a scan and a write; ID minting stays outside any lock.
- Something else implicitly relied on serialization. Mitigation: audit done; full test suite with `-race`; e2e.
- Lua scripts that relied on whole-script serialization. Mitigation: documented in data-entry.md; `unique:` atomic everywhere.

**Effort:** l

## Documentation Planning

- [x] User-facing docs identified (skip if internal refactor)
- [x] Docs-checklist will be created when entering implementation

**Documentation Impact:** docs/webhooks.md, docs/data-entry.md (action timeout,
concurrency note), docs/data-entry/api-reference.md, docs/acl-security.md.
CLAUDE.md unchanged.

## Design Review

- [x] Run `/design-review` before starting implementation
- [x] All critical/significant findings addressed in plan

**Design Review Findings:** 13 findings (2 critical, 5 significant, 5 minor, 1
nit), all linked via has-review-response; the plan above incorporates each
resolution.
