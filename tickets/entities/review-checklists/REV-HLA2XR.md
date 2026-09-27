---
id: REV-HLA2XR
type: review-checklist
title: 'Review: Rebuild the @ mention menu against an agreed behaviour spec'
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
- [x] ~~All critical review-responses addressed~~ (N/A: the review found no critical issues)
- [x] All significant review-responses addressed
- [x] Self-reviewed the diff for unrelated changes

**Review Responses:**
- Significant, addressed: RR-EU0IHG, RR-O3W5PT, RR-MVVTHG, RR-KBJZ6N, RR-0YPT74
- Minor, addressed: RR-61Z9KJ, RR-WES6UW, RR-Y35YJV, RR-YQXXRS, RR-WYPZI5,
RR-PDVBVP, RR-BK55M0, RR-532Y4E, RR-L2UMEX
- Minor, deferred: RR-AJU7F6
- Nits: RR-00XRJN, RR-Y13XDM, RR-DTDY47 (addressed); RR-IDOET8 (deferred);
RR-NFR8KN (wont-fix)

## Acceptance Verification

- [x] Each acceptance criterion tested (reference planning checklist)
- [x] Test evidence documented in implementation checklist

**Acceptance Status:**
- Starting list, stages 1-4+, type scope as text, no-match grace, opening
rules, insertion: PASS. Evidence: `markdown-editor-mention-autocomplete.spec.ts`
80/80 over 5 repeats after the review fixes; unit tests on a real Milkdown
editor for code span, paste, blur, undo, email, punctuation and tail
(`mentionArm.test.ts`, `useEditorMention.test.ts`); 3277 frontend unit tests.
- Create-type row: out of scope, TKT-AW0I30.

## Documentation (enhancements only)

Skip this section for bugs and internal refactors.

- [x] Docs-checklist created and linked via `has-docs`
- [x] User-facing documentation updated
- [x] Docs-checklist marked as done

**Docs Checklist:** DOCS-1AITSN

## Final Checks

- [x] ~~Commit message explains the why, not just what~~ (N/A: not committed yet; the user commits on request)
- [x] No TODOs or FIXMEs left unaddressed
- [x] Ready for another developer to use

## Pull Request

- [x] ~~Run `/pr` command to create PR and monitor CI~~ (N/A: a PR is opened only on the user's request, after done)

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
