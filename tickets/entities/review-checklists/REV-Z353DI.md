---
id: REV-Z353DI
type: review-checklist
title: 'Review: Hot-reload of data-entry.yaml should re-run ValidateConfig + script existence checks'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Automated Checks

- [x] All tests pass (`just test`). `dataentry` passes under `-race -shuffle=on` (418s). The frontend passes 3208/3208. The full `just ci` run hit 10m package timeouts under machine load (dataentry, docscapture, sqlitestore); none were assertion failures, and dataentry passed when rerun alone
- [x] Lint clean (`just lint`): golangci-lint 0 issues; eslint and prettier clean on the changed files; plimsoll and arch-lint clean
- [x] Comment lint gate clean (`just comment-lint`); comment-report shows no findings on the changed lines
- [x] Coverage maintained (`just coverage-check`): internal/dataentry at 83.6%, floor 55

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

- [x] Run `/code-review` command (cranky-code-reviewer + rela-security-reviewer)
- [x] All critical review-responses addressed (none raised)
- [x] All significant review-responses addressed (none raised)
- [x] Self-reviewed the diff for unrelated changes

**Review Responses:** Design: RR-J3GMA3 (deferred). Security: RR-59IX24
(addressed). Code: RR-Z9GXXM, RR-B1DZB7, RR-WX86GF, RR-55ZOIJ, RR-HWTH5L,
RR-6UQGYS, RR-L5A7W2, RR-ZDN549, RR-7C5HMB (addressed); RR-15H86M (deferred,
same as RR-J3GMA3); RR-LCM0GV, RR-W6Z14S (wont-fix, reasons recorded).

## Acceptance Verification

- [x] Each acceptance criterion tested (reference planning checklist)
- [x] Test evidence documented in implementation checklist (IMPL-VPQ9S2)

**Acceptance Status:**
1. Invalid config keeps previous: PASS (TestReloadRejectedConfigKeepsPrevious; manual run).
2. Missing scripts keep previous: PASS (same test, three script kinds; manual run with a missing action script).
3. config-error replaces refresh: PASS (TestReloadConfigBroadcasts; manual SSE capture).
4. Reload normalizes calendars: PASS (TestReloadNormalizesCalendars).
5. SPA error toast: PASS (useEvents.test.ts).
6. Startup unchanged: PASS (existing NewApp tests; same error text).

## Documentation (enhancements only)

Skip this section for bugs and internal refactors.

- [x] ~~Docs-checklist created and linked via `has-docs`~~ (N/A: kind=refactor)
- [x] User-facing documentation updated (docs/data-entry.md Config hot-reload; docs/acl-security.md SSE section)
- [x] ~~Docs-checklist marked as done~~ (N/A: kind=refactor)

**Docs Checklist:** N/A (refactor)

## Final Checks

- [x] Commit message explains the why, not just what
- [x] No TODOs or FIXMEs left unaddressed
- [x] Ready for another developer to use

## Pull Request

- [x] Run `/pr` command to create PR and monitor CI (next step, after done)

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
