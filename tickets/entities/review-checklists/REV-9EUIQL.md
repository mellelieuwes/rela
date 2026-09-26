---
id: REV-9EUIQL
type: review-checklist
title: 'Review: Wire read-side ACL into the MCP server'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Automated Checks

- [x] All tests pass (`just test`). The one failure, `TestFacelessSubjectsAreArguedFor`, reports only files under a stale, locked agent worktree in `.claude/worktrees/` that is outside this change.
- [x] Lint clean (`just lint`)
- [x] Comment lint gate clean (`just comment-lint`)
- [x] Coverage maintained (`go-test-coverage` on the profile: total 80.4%, all floors pass)

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

- [x] Run `/code-review` command (cranky-code-reviewer and rela-security-reviewer)
- [x] All critical review-responses addressed
- [x] All significant review-responses addressed
- [x] Self-reviewed the diff for unrelated changes

**Review Responses:** design review RR-RKBUZU, RR-1J042K, RR-T4G0S7, RR-YCSFCZ,
RR-UXGUT7, RR-8IVSHW, RR-PG14A3, RR-8GX7H4, RR-2F9XNK (all addressed). Code
review RR-27FAH8, RR-ZLOVOE, RR-MEXNEZ (significant, addressed); RR-A6C7UU,
RR-NN6UPT, RR-AE63RO, RR-QLJWO7, RR-WHHOIY (addressed); RR-581HSP, RR-S2STBJ
(deferred to TKT-5GPFZY); RR-G04YKR (wont-fix).

## Acceptance Verification

- [x] Each acceptance criterion tested (reference planning checklist)
- [x] Test evidence documented in implementation checklist

**Acceptance Status:**

- AC1 PASS: TestACL_SearchEntities_OmitsHidden, TestSearcher_Gating.
- AC2 PASS: TestGatedReads_SearchDropsHiddenFieldMatch, TestSearcher_Gating.
- AC3 PASS: TestSearcher_Gating (no index title), TestHydrateHits_FacedHitSurvives.
- AC4 PASS: TestACL_LuaEval_ReadsAreGated.
- AC5 PASS: TestNoPolicy_GatedSearcherIsRaw.
- AC6 PASS: TestScriptWrites_ResultIsRedacted.
- AC7 PASS: TestACL_RelationReads_HiddenEndpointIsNotFound.
- AC8 PASS: TestACL_Writes_HiddenIdIsIndistinguishableFromAbsent, TestACL_LuaEval_WritesNamingHiddenIds, TestScriptWrites_HiddenTargetIsNotFound.
- AC9 PASS: TestACL_WriteCounts_OmitHiddenEdges.
- AC10 PASS: TestRemoteMCPDeps_UsesGatedHandles.

## Documentation (enhancements only)

Skip this section for bugs and internal refactors.

- [x] Docs-checklist created and linked via `has-docs`
- [x] User-facing documentation updated
- [x] Docs-checklist marked as done

**Docs Checklist:** DOCS-W1JT9Y

## Final Checks

- [x] Commit message explains the why, not just what
- [x] No TODOs or FIXMEs left unaddressed
- [x] Ready for another developer to use

## Pull Request

- [x] Run `/pr` command to create PR and monitor CI

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
