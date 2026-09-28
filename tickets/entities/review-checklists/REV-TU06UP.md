---
id: REV-TU06UP
type: review-checklist
title: 'Review: Remove the global data-entry write lock (writeMu)'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Automated Checks

- [x] All tests pass (`just test`)
- [x] Lint clean (`just lint`)
- [x] Comment lint gate clean (`just comment-lint`)
- [x] Coverage maintained (`just coverage-check`)

**Comment findings.** `just comment-report` lists the advisory rules
(duplication, nil-contract, param-contract, restatement). They are not a merge
gate, but a finding your diff *introduces* should be fixed or suppressed — don't
grow the backlog.

Every rule is a heuristic over prose, so false positives are expected. To
suppress one, prefer the inline form on the declaration line, which travels with
the code and is reviewed in this diff:

```go
func f(p string) {} //commentlint:ignore param-contract  p is contained by Clone
```

Use `.commentlint.yml` (`ignore:` path globs, `allow-phrases:`) only when the
same prose recurs across many sites. A reason is required either way — an
unexplained suppression is a finding nobody can re-evaluate later.

## Code Review

- [x] Run `/code-review` command (invokes cranky-code-reviewer agent)
- [x] All critical review-responses addressed
- [x] All significant review-responses addressed
- [x] Self-reviewed the diff for unrelated changes

**Review Responses:** Security and cranky review: RR-NT970V (critical), RR-VZCG5S,
RR-ZQSQMY, RR-2VD1CB, RR-HOM1DZ, RR-ZYUXEH, RR-207370, RR-RXN7AY (significant, all
addressed), RR-Z30H4G, RR-OG75X3, RR-0QG9NQ, RR-70I0IJ, RR-PNQFCP, RR-ESM7AM,
RR-VY5NYW, RR-K1QQL0 (minor), RR-ZXQ65G (nit). Design review responses are
linked from the ticket as well.

Gates: `just test`, `just lint`, `just arch-lint`, `just comment-lint`,
`just plimsoll`, `just coverage-check` (80.3%), `just test-postgres` and the
sqlite `TestConcurrency_` run all pass.

## Acceptance Verification

- [x] Each acceptance criterion tested (reference planning checklist)
- [x] Test evidence documented in implementation checklist

**Acceptance Status:**

1. PASS: `TestProvisionSeam_EveryWriteHandlerUsesWithProvision`,
   `TestHandleV1Action_DoesNotBlockConcurrentWrites`.
2. PASS: `TestConcurrency_GeneratedIDsAreDistinct` (mem, fs, sqlite, pg).
3. PASS: `TestConcurrency_UniqueValueAdmitsOneCreate` on every backend.
4. PASS: `TestConcurrency_RelationUpdatesToDisjointKeysAllLand`.
5. PASS: `TestConcurrency_ManagedOrderIsDistinct`.
6. PASS: `TestConcurrency_PostAutomationRewriteKeepsInterleavedWrite`.
7. PASS: `TestService_ConcurrentSingleFileUploadsLeaveOneFile`,
   `TestService_ConcurrentAppendsRespectCap`.
8. PASS: `TestWebhookConflict_PipelineAppendsAllLand`,
   `TestWebhookConflict_CrossProcessAppendsAllLand` (postgres).
9. PASS: `TestApplyRelationsModern_ConcurrentPatchesNever500` (16 racing
   add/drop PATCHes, no 5xx; it found a double race, fixed in RR-RXN7AY).
10. PASS: `TestProvision_ConcurrentFirstWritesCreateOne`.
11. PASS: all gates listed above.

## Documentation (enhancements only)

Skip this section for bugs and internal refactors.

- [x] ~~Docs-checklist created and linked via `has-docs`~~ (N/A: refactor ticket)
- [x] User-facing documentation updated
- [x] ~~Docs-checklist marked as done~~ (N/A: refactor ticket)

**Docs Checklist:** N/A (refactor). Docs updated anyway: webhooks.md,
data-entry.md, acl-security.md, data-entry/api-reference.md.

## Final Checks

- [x] Commit message explains the why, not just what
- [x] No TODOs or FIXMEs left unaddressed
- [x] Ready for another developer to use

## Pull Request

- [x] ~~Run `/pr` command to create PR and monitor CI~~ (N/A: the user asked for a commit only; a PR follows separately)

<!--
Deliberately NOT tracked here: the PR URL and whether CI passed.

Both post-date this checklist. `/pr` requires the ticket to be `done` and
validating clean before it opens the PR, and a `done` review-checklist may have
no unchecked items — so an item asking for the PR URL can only be satisfied by a
PR that does not exist yet. Checking it early would mean asserting "CI passed"
before CI ran, which turns the checklist from evidence into a formality.

GitHub records both authoritatively, and the branch and commit messages carry
the ticket ID, so the ticket-to-PR link is recoverable without duplicating it
here. See TKT-UFV01M. -->
