---
id: IMPL-6DIX09
type: implementation-checklist
title: 'Implementation: Wire read-side ACL into the MCP server'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Development

- [x] Unit tests written for new code
- [x] Integration tests written (test full flow, not just units)
- [x] Happy path implemented
- [x] Edge cases from planning handled
- [x] Error handling in place (errors surfaced, not swallowed)

## Test Quality

- [x] Using fixture builders or factories for test data
- [x] No hardcoded values in assertions when object is in scope
- [x] Only specifying values that matter for the test
- [x] Interpolated values constructed from objects, not hardcoded
- [x] Property comparisons use original object, not hardcoded strings

## Manual Verification

- [x] ~~Feature manually tested end-to-end~~ (N/A: an HTTP run needs a JWT issuer and JWKS; covered instead by handler-level tests over the real appbuild wiring and a rela-server wiring test)
- [x] Each acceptance criterion verified with test scenario from planning
- [x] Edge cases manually verified

**Verification Evidence:**

- `internal/visibility`: TestSearcher_Gating, TestSearcher_FaceGate, TestSearcher_ClampsLimit, TestSearcher_FailsClosed, TestNewSearcher_Rejects.
- `internal/mcp`: TestACL_SearchEntities_OmitsHidden, TestACL_RelationReads_HiddenEndpointIsNotFound, TestACL_Writes_HiddenIdIsIndistinguishableFromAbsent, TestACL_LuaEval_ReadsAreGated, TestACL_LuaEval_WritesNamingHiddenIds.
- `internal/lua`: TestScriptWrites_ResultIsRedacted, TestScriptWrites_HiddenTargetIsNotFound.
- `internal/appbuild`: TestGatedReads_SearchDropsHiddenFieldMatch, TestNoPolicy_GatedSearcherIsRaw.
- `cmd/rela-server`: TestRemoteMCPDeps_UsesGatedHandles.
- `go test ./...` passes except the faceless guard, which fails only on a stale `.claude/worktrees` copy outside this change. golangci-lint, arch-lint, comment-lint and plimsoll are clean.

## Quality

- [x] Code follows project patterns (check similar code)
- [x] Checked for DRY opportunities — repeated literals, expressions, or
patterns extracted to a helper / constant / type where it sharpens the contract
(don't extract for its own sake; CLAUDE.md "three similar lines is better than a
premature abstraction" still holds)
- [x] No security issues introduced
- [x] No silent failures (errors logged AND returned)
- [x] No debug code left behind
