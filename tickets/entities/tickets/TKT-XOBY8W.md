---
id: TKT-XOBY8W
type: ticket
title: 'Trim MCP context size: fewer tools, compact answers'
kind: enhancement
priority: medium
effort: m
status: done
---

## Description

The MCP server loads into every agent session, so its tool list and answers cost
context on every call. On atlas the old `list_entity_types` answer was about 125
KB, and 27 tools each repeated shared conventions.

Changes:

- 27 tools became 16. The `analyze_*` tools merge into `analyze` (`check`),
`trace_from`/`trace_to` into `trace` (`direction`), the four schema tools into
`schema` (optional `type`), `lua_list` into `lua_run` without a path. `export`
is removed.
- `schema` answers a compact overview, or the detail of one type with enum
values resolved.
- Results use the schema's display title, compact JSON, and paging
(`total`, `has_more`) with a default limit.
- Unknown entity or relation types are errors, not empty lists.
- `list_entities` takes a predicate `filter`, including `related(...)`,
answered under the caller's ACL gate.
- Analyze findings share one shape: `{check, count, results}`.
- Server instructions list entity types and state shared conventions once.
- Every tool rejects arguments it does not declare, so a removed argument
such as `where` fails instead of being ignored.
- `search_entities` on the remote server now searches through the ACL: hidden
entities, hidden field values and withheld faces no longer match. This leak
predates the ticket.

Breaking: old tool names and the `where` argument are gone.
