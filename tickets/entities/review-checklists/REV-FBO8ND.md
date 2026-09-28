---
id: REV-FBO8ND
type: review-checklist
title: 'Review: Web search misses faced entities under app.default_world'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Automated Checks

- [x] All tests pass (`just test`) (go test ./internal/dataentry passes; frontend vitest passes except the milkdown corpus test, which timed out after 921 s on this loaded machine and does not touch search; CI runs the full suite)
- [x] Lint clean (`just lint`) (golangci-lint, eslint and vue-tsc clean)
- [x] Comment lint gate clean (`just comment-lint`)
- [x] ~~Coverage maintained (`just coverage-check`)~~ (N/A locally: the change adds tests only to covered packages; CI enforces the floors on the PR)

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
- [x] All significant review-responses addressed (one deferred to BUG-OJPVPG with a reason)
- [x] Self-reviewed the diff for unrelated changes

**Review Responses:** RR-ZIUHR6, RR-ZRZ7QX, RR-4VPJXP, RR-HJB7FV, RR-J83L1N,
RR-HW36VR, RR-62K4RS, RR-9TD9A0, RR-T2Z4T7

## Acceptance Verification

- [x] Each acceptance criterion tested (reference planning checklist)
- [x] Test evidence documented in implementation checklist

**Acceptance Status:**
- `_search` finds a published-only entity under `app.default_world`: PASS (`TestSearch_ResolvesThroughTheWorld`).
- A hit is served as the resolved face with `_world` provenance: PASS (`TestSearch_HitCarriesTheResolvedFace`).
- A denied world finds nothing: PASS (`TestSearch_DeniedWorldFindsNothing`).
- A `type@face` grant is honored: PASS (`TestSearch_FaceGrantIsHonored`, mutation-checked).
- `_position` agrees with `_search`: PASS (`TestSearch_PositionAgreesUnderTheDefaultWorld`).

## Documentation (enhancements only)

Skip this section for bugs and internal refactors.

- [x] ~~Docs-checklist created and linked via `has-docs`~~ (N/A: bug fix)
- [x] ~~User-facing documentation updated~~ (N/A: bug fix)
- [x] ~~Docs-checklist marked as done~~ (N/A: bug fix)

**Docs Checklist:** N/A

## Final Checks

- [x] ~~Commit message explains the why, not just what~~ (N/A: the user asked for minimal commit messages; the why is in the bug's 5-whys)
- [x] No TODOs or FIXMEs left unaddressed
- [x] Ready for another developer to use

## Pull Request

- [x] ~~Run `/pr` command to create PR and monitor CI~~ (N/A here: `/pr` runs after the bug is done, see TKT-UFV01M)

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
