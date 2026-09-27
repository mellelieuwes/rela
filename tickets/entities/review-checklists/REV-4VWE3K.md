---
id: REV-4VWE3K
type: review-checklist
title: 'Review: related() constraints match the final entity id and current_user.id'
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

**Review Responses:** cranky-code-reviewer and rela-security-reviewer found no
critical or significant issues. Minor/nit: RR-ZOSAX2, RR-ZTBBQ1, RR-XV2AAP,
RR-WJBL7E, RR-QC30IH, RR-HZ0O0N, RR-ZLRZ67 addressed; RR-1WW94M, RR-E0IUM3,
RR-H0H924 deferred with reasons.

## Acceptance Verification

- [x] Each acceptance criterion tested (reference planning checklist)
- [x] Test evidence documented in implementation checklist

**Acceptance Status:**
- PASS `id` key and `current_user.id` value compile; other variables refused
  (predicate + predicatefns table tests)
- PASS program records current_user; value bound per request (relresolve,
  queryplan LowerScope tests; TestQueryScope_RelatedToCurrentUser)
- PASS `id` lowers to RelationPredicate.Endpoints; empty/unresolved user fails
  closed on Go, pushdown and next-action paths (acl, relresolve, dataentry tests)
- PASS gating, 422 cases, exact pushdown, no bogus index for `id`
  (TestTraversalIndexSpecs_IDDerivesNoPropertyIndex, listpushdown tests)
- PASS storetest conformance on mem/fs/sqlite/postgres; EXPLAIN on sqlite and
  postgres
- PASS manual run on rela-server (see IMPL-7I6LO0)

## Documentation (enhancements only)

Skip this section for bugs and internal refactors.

- [x] Docs-checklist created and linked via `has-docs`
- [x] User-facing documentation updated
- [x] Docs-checklist marked as done

**Docs Checklist:** DOCS-UKDLOP

## Final Checks

- [x] Commit message explains the why, not just what
- [x] No TODOs or FIXMEs left unaddressed
- [x] Ready for another developer to use

## Pull Request

- [x] Run `/pr` command to create PR and monitor CI (PR opened with gh pr create after this checklist; CI monitored there)

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
