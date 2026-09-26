---
id: DOCS-W1JT9Y
type: docs-checklist
title: 'Docs: Wire read-side ACL into the MCP server'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Code Documentation

- [x] Exported functions/types have godoc
- [x] Non-obvious decisions explained in comments
- [x] Package docs updated if package purpose changed

New exported API has godoc: `visibility.Searcher`, `NewSearcher`,
`SearchScoper`, `MaxSearchLimit`, `DenySearcher`, `visibility.Readable`,
`RefReader`, and the `GatedReadBundle.Searcher` / `LuaReads` fields. The
comments record the non-obvious choices: why `Hit.Title` is cleared, why filters
and sorts are refused, why hidden names carry the `prop:` prefix, why
`remoteMCPDeps` leaves `LuaCache` nil, why `GetRelation` gates both endpoints,
and why search hits are read in one query over every face.

## Project Documentation

- [x] ~~CLAUDE.md updated with new patterns~~ (N/A: the change applies the existing visibility-wrapper rule; it adds no new rule)
- [x] docs/ updated for changed behaviour
- [x] ~~Architecture docs updated~~ (N/A: the two new arch-lint edges are recorded with a reason in `.go-arch-lint.yml`)

`docs/acl-security.md` now says the remote MCP endpoint is gated, explains why
stdio is not, and lists the remaining residuals. `docs/mcp-server.md` describes
what a remote caller gets from search and the Lua tools. `docs/acl-overview.md`
no longer says the MCP read gate is unbuilt.

## External Documentation

- [x] ~~README updated~~ (N/A: no new feature or flag)
- [x] ~~CLI reference updated~~ (N/A: no CLI change)
- [x] API docs updated

The MCP tool behaviour is documented in `docs/mcp-server.md`. `search_entities`
summaries now carry a `face` field for faced entities.
