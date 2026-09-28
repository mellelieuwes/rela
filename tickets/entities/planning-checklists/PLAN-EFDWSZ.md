---
id: PLAN-EFDWSZ
type: planning-checklist
title: 'Planning: Trim MCP context size: fewer tools, compact answers'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Understanding

- [x] Problem/requirements clearly understood
- [x] Scope defined (what's in/out documented below)
- [x] Acceptance criteria documented with specific test scenarios

**Scope:** In: the MCP tool set, tool descriptions, answer shapes, server
instructions, `related()` in the list filter, and the MCP docs. Out: the `rela`
CLI, the web app, and refreshing server instructions on schema reload (the
go-sdk sets them once at construction).

**Acceptance Criteria:**
1. The tool list has 16 tools and is smaller (9,620 to 6,578 chars). Test:
`tools_list.golden.json`.
2. `schema` without a type answers one summary per type; with a type it
answers that type's detail. Test: `TestHandleSchema_*`.
3. Lists show display titles and page with `total`/`has_more`. Test:
`TestHandleListEntities_DisplayTitle`, pagination tests.
4. Unknown types are errors. Test: `TestHandleListEntities_Errors`.
5. `related()` in a filter counts only visible entities. Test:
`TestACL_ListEntities_RelatedFilterSeesOnlyVisibleEdges`.

## Research

- [x] ~~For larger features: run `/research`~~ (N/A: the approach follows existing surfaces)
- [x] ~~Searched for existing libraries that solve this problem~~ (N/A: internal API reshaping)
- [x] Checked codebase for similar patterns or reusable code
- [x] ~~Looked for reference implementations in other projects~~ (N/A: internal API reshaping)
- [x] Reviewed relevant rela concepts for prior art

**Research Doc:** N/A

**Existing Solutions:** `relresolve.Binder` (as used by the gated validator in
`appbuild.GatedReads`), `predicatefns.Evaluator` (as used by `rela list
--filter`), `metamodel.DisplayTitle`.

## Approach

- [x] Technical approach chosen and documented
- [x] Approach builds on existing patterns (not reinventing)
- [x] Alternatives considered (document why rejected)
- [x] Dependencies identified (packages, APIs, types)

**Technical Approach:** Merge tools that differ by one argument; add compact
DTOs for schema output; reuse the predicate evaluator for filters; expose the
gated traversal binder from `GatedReads` as an optional consumer-side
`TraversalBinder` on `mcp.Deps`. Alternative rejected: keeping old tool names as
aliases, because each alias costs context in every session.

**Files to modify:** `internal/mcp/*`, `internal/appbuild/appbuild.go`,
`internal/cli/mcp_wiring.go`, `cmd/rela-server/mcp.go`, `.go-arch-lint.yml`, MCP
docs.

## Security Considerations

- [x] Input sources identified (user input, config, external APIs)
- [x] Input validation approach defined (allowlist preferred over blocklist)
- [x] Security-sensitive operations identified (file access, auth, crypto)
- [x] Error handling doesn't leak sensitive information

**Input Sources & Validation:** Tool arguments from the MCP client. Types are
resolved against the metamodel; filters compile against it and fail on unknown
properties.

**Security-Sensitive Operations:** `related()` traversals go through the same
ACL gate as the reader; with no binder they are refused. Titles are computed
from already-redacted entities.

## Test Plan

- [x] Test scenarios documented for each acceptance criterion
- [x] Edge cases identified and documented
- [x] Negative test cases defined (invalid input, error conditions)
- [x] Integration test approach defined (not just unit tests)

**Test Scenarios:** See acceptance criteria.

**Edge Cases:** Filter without type; negated `related()`; no binder wired; types
with a `display_property` other than `title`.

**Negative Tests:** Unknown type, unknown property, `related()` with no binder,
hidden far entity.

## Risk Assessment

- [x] Technical risks assessed with mitigations
- [x] Security risks assessed (see Security Considerations)
- [x] Effort estimated (xs/s/m/l/xl)

**Risks:** Breaking tool names for existing clients (accepted by the owner).

## Documentation Planning

- [x] User-facing docs identified (skip if internal refactor)
- [x] Docs-checklist will be created when entering implementation

**Documentation Impact:** `docs/mcp-server.md`, `GUIDE-mcp-server.md`,
`docs/metamodel.md`, `CLAUDE.md`.

## Design Review

- [x] ~~Run `/design-review` before starting implementation~~ (N/A: implemented before the ticket existed; covered by code and security review)
- [x] All critical/significant findings addressed in plan

**Design Review Findings:** N/A
