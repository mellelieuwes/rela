---
id: PLAN-5CJNK8
type: planning-checklist
title: 'Planning: Twins stage 1: pacts, twins store, reconcile and ownership enforcement'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Understanding

- [x] Problem/requirements clearly understood
- [x] Scope defined (what's in/out documented below)
- [x] Acceptance criteria documented with specific test scenarios

**Scope:**

In: `pacts:` per entity type (theirs/shared/propose, scope, instructions) with
load-time validation; `internal/twins` (model, `twins.Store` with file and
memory backends and the `twinstest` kit, pure `Reconcile`, service); ownership
enforcement in entitymanager and every path that writes an entity (copy,
attachments, conflict resolve) plus data-entry field verdicts and
`content_writable`; `rela twin` CLI; `GET /api/v1/_twins/{type}/{id}`; Twin
badge and read-only body editor in the SPA; guide, metamodel and CLI docs.

Out: pg/sqlite twin backends (a postgres/sqlite build with pacts fails fast),
relations as pact fields, faces, MCP twin tools, a proposal workflow beyond
reporting `propose`, editing twins in the SPA, any transport (webhooks,
polling).

**Acceptance Criteria:**

1. No `pacts:` → no `.rela/twins`, `/_twins` answers a JSON 404, `/_schema`
unchanged. Test: TestAssemble_NoPactsWiresNoTwins, router walk probe.
2. Every pact rule rejects its invalid case at load. Test: pacts_test.go table.
3. `rela twin link` needs a pact; a second live twin for the same entity and
system is refused. Test: TestTwinCLI_ErrorsNameTheCause, twinstest.
4. `rela twin pull` applies theirs fields, reports foreign edits and conflicts,
leaves the entity untouched on a version conflict. Test: reconcile table,
service tests, TestTwinCLI_SyncLoop.
5. A theirs field of a twinned entity cannot be changed through CLI, API, MCP,
copies, attachments or conflict resolve. Test: TestPatchEntity/
UpdateEntity_TwinOwnership, TestCopyState_TwinOwnership,
TestService_OwnedFilePropertyIsRefusedBeforeBytesChange,
TestV1ConflictResolve_TwinOwnership, TestAttachments_OwnedPropertyRefusesWrites.
6. Owned fields and an owned body are read-only in the web app. Test: dataentry
verdict tests, MilkdownEditor/EntityDetail readonly specs.
7. `rela twin pending` lists never-synced, changed, conflicting and gone twins.
Test: service Pending tests, CLI sync loop.
8. Rename keeps twins (also across processes); delete marks them gone. Test:
TestAssemble_PactsWireGuardSyncWriterAndAliases, TestRenameDuringSync.
9. Both backends pass `twinstest.RunAll`. Test: memtwins/filetwins tests.

## Research

- [x] For larger features: run `/research` to create a structured research doc
- [x] Searched for existing libraries that solve this problem
- [x] Checked codebase for similar patterns or reusable code
- [x] Looked for reference implementations in other projects
- [x] Reviewed relevant rela concepts for prior art

**Research Doc:** N/A — the prior-art survey is recorded here and in
FEAT-I3VLQV; the design went through a design review instead (see below).

**Existing Solutions:**

- Sync tools: Exalate ("connection" + "sync pair", formerly "twin"), Unito
("flow" + "item in sync"). Both run the transport themselves; rela deliberately
does not.
- In rela: comments (TKT-FIO205) for a non-graph side store with conformance
kit, nil-service-when-disabled and AliasRewriter lifecycle; CalDAV `read_only:`
for per-field containment of a foreign client; the fs↔pg sync channel for
content hashes and compare-and-swap (`store.VersionOf`,
`Patch.ExpectedVersion`); computed properties for rejecting writes on every
path; `Manager.elevated()` for an object-carried write capability.
- No library fits: the value is the ownership contract and its enforcement
inside rela's write path.

## Approach

- [x] Technical approach chosen and documented
- [x] Approach builds on existing patterns (not reinventing)
- [x] Alternatives considered (document why rejected)
- [x] Dependencies identified (packages, APIs, types)

**Technical Approach:**

Pact = schema config on EntityDef, compiled into `metamodel.Pact`/`PactPolicy`.
Twin = record in `internal/twins`, keyed by (system, external id), with the
agreed base snapshot and `BaseVersion`. Reconcile is pure and decides per field
from equalities against the base (canonical.EqualValue/EqualBody). The service
writes the entity through `entitymanager.TwinSyncWriter` with a
compare-and-swap, then commits bookkeeping through `Store.Modify` under the
store lock. Ownership is a free-function guard next to the computed-property
checks, change-based, applied even to elevated handles.

Alternatives rejected: a project-level Pact/Twin entity type (prototyped in the
Eyra project: no enforcement, snapshot in entity bodies, relation lists of every
type by hand); webhook-driven mapping (puts integration knowledge outside rela);
rela calling external APIs (credentials and translation belong with the agent).

**Files to modify:**

internal/metamodel (pacts), internal/canonical, internal/twins/**,
internal/entity (UpdateResult.Version), internal/entitymanager (guard, copy),
internal/attachment, internal/store/fsstore (post-write version),
internal/appbuild, internal/cli (twin), internal/dataentry, internal/apiwire/v1,
internal/mcp (attachment wiring), cmd/rela-server, cmd/rela-desktop, frontend
(types, api/twins, TwinBadge, editor readonly), docs-project guides.

## Security Considerations

- [x] Input sources identified (user input, config, external APIs)
- [x] Input validation approach defined (allowlist preferred over blocklist)
- [x] Security-sensitive operations identified (file access, auth, crypto)
- [x] Error handling doesn't leak sensitive information

**Input Sources & Validation:**

- `pacts:` (operator config): strict keys, system id pattern, http(s) scope,
field names checked against the type.
- `rela twin` arguments: external id allowlist `[A-Za-z0-9._-]{1,128}`, not
`.`/`..`, case encoded in file names; remote JSON decoded strictly (unknown keys
and `properties.body` refused).
- HTTP `/_twins/{type}/{id}`: path segments validated like comments.

**Security-Sensitive Operations:**

- Writes to owned fields: refused on every caller path, including elevated
handles; only the handle appbuild gives the twins service skips the check, and
nested cascades never inherit it. Lookup errors fail closed.
- Read-out: `/_twins` gates the entity through the visibility reader and drops
finding values of hidden fields.
- File store: flock around every read-modify-write, atomic temp+rename.

## Test Plan

- [x] Test scenarios documented for each acceptance criterion
- [x] Edge cases identified and documented
- [x] Negative test cases defined (invalid input, error conditions)
- [x] Integration test approach defined (not just unit tests)

**Test Scenarios:** see the acceptance criteria above.

**Edge Cases:**

- Absent vs present-null remote values; body not mapped vs cleared.
- First sync without a base; A-B-A values; reflowed bodies and 3 vs 3.0.
- Stale and out-of-order pulls; pushed after a concurrent edit.
- Rename or gone landing between reading a twin and writing it, across
processes; ids differing only by case.
- Computed properties under `theirs: ["*"]`; automations moving ours fields.

**Negative Tests:** invalid pacts, unknown external ids, duplicate links, stale
version on pushed, stale pull, gone twin, rejected write, writes to owned fields
through each path.

## Risk Assessment

- [x] Technical risks assessed with mitigations
- [x] Security risks assessed (see Security Considerations)
- [x] Effort estimated (xs/s/m/l/xl)

**Risks:**

- Every write gains an ownership lookup: mitigated by returning before any I/O
for types without a pact.
- File-backed twin state is node-local: postgres/sqlite builds with pacts
refuse to start rather than silently diverge.
- `.rela/twins` is machine state and does not travel with a clone: documented.

Effort: l.

## Documentation Planning

For enhancements: identify what documentation needs updating.

- [x] User-facing docs identified (skip if internal refactor)
- [x] Docs-checklist will be created when entering implementation

**Documentation Impact:**

- [x] docs/metamodel.md - New metamodel features
- [x] docs/cli-reference.md - New/changed commands
- [x] ~~docs/data-entry.md - UI changes~~ (N/A: the badge and read-only fields need no configuration; described in docs/twins.md)
- [x] CLAUDE.md - New patterns or conventions (internal/entitymanager/CLAUDE.md: twin guard)
- [x] README.md - Project-level changes (generated guide table)

## Design Review

- [x] Run `/design-review` before starting implementation
- [x] All critical/significant findings addressed in plan

**Design Review Findings:** 22 review-responses titled "Design review: …" (5
critical, 10 significant, 7 minor), all addressed, e.g. RR-5F7IT8.
