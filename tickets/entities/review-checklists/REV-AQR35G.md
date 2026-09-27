---
id: REV-AQR35G
type: review-checklist
title: 'Review: Replacement suggestions on text comments'
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

**Review Responses:** Design review: RR-0XJ3T1, RR-1P33JI, RR-40B8T4, RR-4CMFRF,
RR-A41Q8F, RR-BYTDFQ, RR-GN3BKD, RR-H4S65K, RR-HM67QG, RR-I76C4A, RR-TUD0XV,
RR-UCJ10W, RR-V3HTUX, RR-XQ538O. Security review: RR-2H29ZI, RR-52HZ4Z,
RR-64S70J, RR-7WI13E, RR-RJDD21. Code review: RR-4SWBO9 (critical), RR-356GGT,
RR-6JY8CT, RR-9HZA03, RR-IQ3VNM, RR-NWUZ40, RR-TSVVVI, RR-XWR82X, RR-4XEDON,
RR-3DIYN4 (all addressed); RR-I7DJR3, RR-HDXYCH, RR-81T8Y7 (wont-fix, minor or
nit, reasons recorded). No critical or significant finding is open.

## Acceptance Verification

- [x] Each acceptance criterion tested (reference planning checklist)
- [x] Test evidence documented in implementation checklist

**Acceptance Status:**

1. PASS. Handler round trip in `comments_suggestion_test.go`; the `commentstest`
contract round-trips a replacement on all four backends (pgcomments run against
a scratch database).
2. PASS. Handler table test refuses property and section anchors with 400.
3. PASS. Empty replacement accepted and the quote removed (`suggestion_test.go`,
handler test).
4. PASS. Handler test asserts stored body, resolved flag and the audit entry
under the accepting principal.
5. PASS, with one deliberate change: a comment without a replacement answers
409 `no_suggestion`, not 400, because the request is well formed and the
refusal depends on stored state (DEC-HWZHA). The other refusals have one
subtest each, plus the cross-process claim and reopen-failure tests.
6. PASS. filecomments fixture without the field loads unchanged.
7. PASS. e2e "suggests a replacement and accepts it into the body" in
`comments.spec.ts`; the whole spec passes (7 tests).

## Documentation (enhancements only)

Skip this section for bugs and internal refactors.

- [x] Docs-checklist created and linked via `has-docs`
- [x] User-facing documentation updated
- [x] Docs-checklist marked as done

**Docs Checklist:** DOCS-3QP1VW

## Final Checks

- [x] Commit message explains the why, not just what
- [x] No TODOs or FIXMEs left unaddressed
- [x] Ready for another developer to use

## Pull Request

- [x] ~~Run `/pr` command to create PR and monitor CI~~ (N/A: the user has not asked for a PR; run after done)

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
