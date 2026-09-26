---
id: TKT-5GPFZY
type: ticket
title: Converge gated search onto visibility.Searcher
kind: chore
priority: medium
effort: m
status: backlog
---

## Description

TKT-4QSZ8Y added `visibility.Searcher` for the remote MCP server. Two search
paths still use their own logic:

- `internal/dataentry` repeats the same rules in `aclReadGate.SearchScope`, `hiddenSearchFields` and `faceGatedHits`. If one copy changes, the other surface can leak. Move data-entry onto `visibility.Searcher`.
- `appbuild.luaReadDepsFor` gives scheduled and automation Lua the raw searcher, so `rela.search` there still matches on hidden properties (TKT-GGQ0JT). Gate it for identity-bearing runtimes. Keep the raw searcher only where an `AllowAllReader` is wired deliberately.

Also: `rela.search` hydrates hits with `GetEntity(hit.ID)`, which misses faced
types. Read the hit face instead, as `mcp.hydrateHits` does.
