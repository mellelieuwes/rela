---
id: TKT-4QSZ8Y
type: ticket
title: Wire read-side ACL into the MCP server
kind: enhancement
priority: critical
effort: m
status: done
---

## Description

Security gate for shipping content states (design doc §12.2).

### Current state (rescoped 2026-09-26)

The original concern was the stdio server (`rela mcp`) running under `NopACL`.
That is now a documented decision: over stdio the filesystem is the trust
boundary, so a gate defends nothing. The remote HTTP MCP endpoint (`rela-server
-mcp`) is the network-facing surface, and it already reads entities, traces and
validation through `appbuild.Services.GatedReads`.

Two handles in `cmd/rela-server/mcp.go` still bypass the gate:

1. **Search.** `Deps.Searcher` is the raw `svc.Searcher()`.
`handleSearchEntities` returns the id, type and index title of every hit, so a
remote caller can enumerate entities it may not read.
2. **Lua tools.** `Deps.LuaWriteDeps` is `svc.LuaWriteDeps()`, whose reader is
`visibility.Unrestricted(store)` and whose tracer and searcher are raw.
`lua_eval` and `lua_run` are always registered, so a remote caller can read the
whole graph.

### Approach

- Add a principal-bound search decorator in `internal/visibility` that
implements `search.Searcher` over `search.VisibleSearcher`: per-type `ReadQuery`
scope, `visible:` hidden-field match filtering, and the face gate. The same
rules the data-entry search path applies.
- Extend `GatedReads` with that searcher and an ACL-bound Lua write bundle,
and wire both into the remote MCP server.
- `handleSearchEntities` takes title and status from the gated reader and
drops hits the reader does not return.

### Out of scope

- stdio MCP stays on the operator trust boundary.
- Relation meta redaction (TKT-0RBFN0).

### Acceptance criteria

- A remote MCP caller's `search_entities` never returns an entity its
principal cannot read, nor a hit that matched only a hidden field.
- `lua_eval`/`lua_run` reads, traces and searches over remote MCP are gated
exactly like `GatedReads`.
- With no `acl.yaml`, behaviour is unchanged.
