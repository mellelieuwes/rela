---
id: REV-IAJEIV
type: review-checklist
title: 'Review: Twins stage 1: pacts, twins store, reconcile and ownership enforcement'
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

Also green: `go-arch-lint check`, plimsoll, markdownlint, frontend typecheck,
eslint (0 errors) and the full vitest run.

## Code Review

- [x] Run `/code-review` command (invokes cranky-code-reviewer agent)
- [x] All critical review-responses addressed
- [x] All significant review-responses addressed
- [x] Self-reviewed the diff for unrelated changes

**Review Responses:** 22 from the design review ("Design review: …") and 10 from
the code review ("Code review: …"), all `addressed`, each with a regression test
checked to fail without its fix. Unrelated-looking changes are deliberate:
fsstore's post-write version (found while verifying R1, pinned by storetest
CAS/ReturnedVersionSurvivesNormalization) and the MCP golden (the new `Pacts`
key in raw schema output, like `Faces`).

## Acceptance Verification

- [x] Each acceptance criterion tested (reference planning checklist)
- [x] Test evidence documented in implementation checklist

**Acceptance Status:**

1. PASS — TestAssemble_NoPactsWiresNoTwins, router walk probe.
2. PASS — pacts_test.go.
3. PASS — TestTwinCLI_ErrorsNameTheCause, twinstest.
4. PASS — reconcile table, service tests, TestTwinCLI_SyncLoop.
5. PASS — twin guard tests on patch/update, copy, attachments (service, HTTP,
MCP) and conflict resolve.
6. PASS — dataentry verdict tests, MilkdownEditor and EntityDetail readonly specs.
7. PASS — service Pending tests, CLI sync loop.
8. PASS — TestAssemble_PactsWireGuardSyncWriterAndAliases, TestRenameDuringSync,
cross-instance twinstest.
9. PASS — memtwins and filetwins run twinstest.RunAll.

## Documentation (enhancements only)

Skip this section for bugs and internal refactors.

- [x] Docs-checklist created and linked via `has-docs`
- [x] User-facing documentation updated
- [x] Docs-checklist marked as done

**Docs Checklist:** DOCS-TZT3A7

## Final Checks

- [x] Commit message explains the why, not just what
- [x] No TODOs or FIXMEs left unaddressed
- [x] Ready for another developer to use

## Pull Request

- [x] ~~Run `/pr` command to create PR and monitor CI~~ (N/A: the PR is opened later, on the owner's go-ahead)

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
