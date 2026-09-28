---
id: IMPL-O6O58F
type: implementation-checklist
title: 'Implementation: Trim MCP context size: fewer tools, compact answers'
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

- [x] Feature manually tested end-to-end
- [x] Each acceptance criterion verified with test scenario from planning
- [x] Edge cases manually verified

**Verification Evidence:** Ran `rela --project=tickets mcp` over stdio and
called `list_entities` with `related(entity, 'implements', { title = 'MCP Server
API' }) and entity.status == 'in-progress'` (returned TKT-XOBY8W), with `not
related(entity, 'affects')` (7 tickets), and with a constraint on a missing
property (refused). Tool list dropped from 9,620 to 6,578 characters; the
tickets schema overview is 5.5 KB. The ACL tests were checked by mutation: an
ungated binder and the raw searcher each make them fail.

## Quality

- [x] Code follows project patterns (check similar code)
- [x] Checked for DRY opportunities — repeated literals, expressions, or
patterns extracted to a helper / constant / type where it sharpens the contract
(don't extract for its own sake; CLAUDE.md "three similar lines is better than a
premature abstraction" still holds)
- [x] No security issues introduced
- [x] No silent failures (errors logged AND returned)
- [x] No debug code left behind
