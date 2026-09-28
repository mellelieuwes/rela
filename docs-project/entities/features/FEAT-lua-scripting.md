---
id: FEAT-lua-scripting
status: published
summary: Programmable automation and reporting via embedded Lua scripts
title: Lua Scripting
type: feature
---

Rela includes an embedded Lua scripting runtime that provides programmable access to the
entity graph. Scripts can query, create, update, and delete entities and relations,
generate reports, perform bulk operations, and export data to custom formats.

## Key Capabilities

- **Query Operations**: Get entities, list by type with filters, search, trace dependencies
- **Mutation Operations**: Create, update, delete entities and relations
- **Schema Introspection**: Access entity types, relation types, and property definitions
- **Output**: JSON output to stdout, file writing to `output/` directory
- **MCP Integration**: Execute scripts via the `lua_eval` and `lua_run` tools

## Security Model

The Lua runtime is sandboxed:

- Only safe libraries loaded (string, table, math, coroutine)
- No `io`, `os`, or `debug` libraries
- File writes restricted to `output/` directory
- Scripts in `scripts/` directory only (for `lua_run`)
