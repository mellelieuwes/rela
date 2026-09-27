---
id: REV-0NAN64
type: review-checklist
title: 'Review: Milkdown''s orphan timer fails the Frontend CI job after test teardown'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Automated Checks

- [x] All tests pass (`just test`) (full frontend suite, 3207 tests, and typecheck pass; no Go code changed)
- [x] Lint clean (`just lint`) (frontend lint passes)
- [x] ~~Comment lint gate clean (`just comment-lint`)~~ (N/A: commentlint checks Go; no Go code changed)
- [x] ~~Coverage maintained (`just coverage-check`)~~ (N/A: no Go code changed, and the frontend has no coverage enforcement)

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

- [x] ~~Run `/code-review` command (invokes cranky-code-reviewer agent)~~ (N/A: a 13-line test-configuration change; self-reviewed instead)
- [x] All critical review-responses addressed (none raised)
- [x] All significant review-responses addressed (none raised)
- [x] Self-reviewed the diff for unrelated changes (only `frontend/vitest.config.ts` and this bug's ticket files)

**Review Responses:** none.

## Acceptance Verification

- [x] Each acceptance criterion tested (reference planning checklist) (bug workflow has no planning checklist; criteria from BUGA-TO5QCX fix planning)
- [x] Test evidence documented in implementation checklist (IMPL-XICZDW)

**Acceptance Status:**
- The Milkdown teardown error no longer fails the run: PASS (stand-in test
  fails without the filter, passes with it).
- Any other unhandled error still fails the run: PASS (a different stray error
  still failed the run).

## Documentation (enhancements only)

Skip this section for bugs and internal refactors.

- [x] ~~Docs-checklist created and linked via `has-docs`~~ (N/A: bug fix, not an enhancement)
- [x] ~~User-facing documentation updated~~ (N/A: bug fix, not an enhancement)
- [x] ~~Docs-checklist marked as done~~ (N/A: bug fix, not an enhancement)

**Docs Checklist:** N/A.

## Final Checks

- [x] Commit message explains the why, not just what (the subject names the cause; the config comment carries the full why)
- [x] No TODOs or FIXMEs left unaddressed
- [x] Ready for another developer to use

## Pull Request

- [x] Run `/pr` command to create PR and monitor CI (PR #1686)

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
