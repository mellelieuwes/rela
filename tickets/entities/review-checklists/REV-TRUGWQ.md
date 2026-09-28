---
id: REV-TRUGWQ
type: review-checklist
title: 'Review: Predicate engine: typed value selection with and/or (c and x or y)'
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

**Review Responses:** cranky-code-reviewer and rela-security-reviewer (no
security findings). Significant: RR-6KTMVI (browser comparison regression, fixed
with syntactic selection marking), RR-6N8P45 (unset bool condition, documented
and pinned). Minor/nit addressed: RR-7TSRVL, RR-2BNJM9, RR-Z2C7J5, RR-SQIH3C,
RR-T0SDKQ, RR-94OYIF, RR-RSA5EQ, RR-EM6F1L, RR-Y5OEWJ. Deferred with reason:
RR-V72KO4, RR-RWLE9X. Design review: RR-T6819Y, RR-7JU3ON, RR-Z6GAIJ, RR-M0GJP6,
RR-S8GVJI.

## Acceptance Verification

- [x] Each acceptance criterion tested (reference planning checklist)
- [x] Test evidence documented in implementation checklist

**Acceptance Status:**
1. PASS: computed enum mapping (manual /tmp/selproj, TestSelection_EnumMapping, TestCompileEvaluate_ValueSelection).
2. PASS: reporter's `--filter` returns only the passkey entity (manual; TestApplyListFilters "filter value selection").
3. PASS: `entity.opt or 'none'` (TestSelection_LuaSemantics).
4. PASS: int and date literal branches (TestSelection_LiteralCoercion; manual band values).
5. PASS: compile errors (TestSelection_CompileErrors; manual `entity.method and 'x' or 'y'`).
6. PASS: related() in a branch refused, branch attributes are dependencies (TestCompile_RejectsRelatedInsideSelection, TestSelection_DependenciesInsideBranches).
7. PASS: prefilter ignores selection (TestPrefilter_IgnoresValueSelection, mutation-checked).
8. PASS: hint (TestSelection_IfHintNamesWorkingForm); docs updated.
9. PASS: browser selection matches predicate for literal-terminated selections, and boolean conditions are unchanged (conditions.test.ts).

## Documentation (enhancements only)

Skip this section for bugs and internal refactors.

- [x] Docs-checklist created and linked via `has-docs`
- [x] User-facing documentation updated
- [x] Docs-checklist marked as done

**Docs Checklist:** DOCS-XHK90Q

## Final Checks

- [x] Commit message explains the why, not just what
- [x] No TODOs or FIXMEs left unaddressed
- [x] Ready for another developer to use

## Pull Request

- [x] ~~Run `/pr` command to create PR and monitor CI~~ (N/A here: `/pr` requires the ticket to be done first; it runs after this checklist, per TKT-UFV01M)

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
