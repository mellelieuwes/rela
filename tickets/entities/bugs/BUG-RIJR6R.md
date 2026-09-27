---
id: BUG-RIJR6R
type: bug
title: 'Remote MCP offers the Lua tools'
description: Remote MCP registered lua_eval, lua_run and lua_list, so any authenticated remote caller could run arbitrary scripts in the server process. TKT-UIR41P AC 7 required them to be absent remotely.
priority: high
why1: registerTools registered every tool on every transport, and the remote wiring passed Lua deps into the MCP Deps.
why2: TKT-BDG8U9 reused the stdio Deps shape for the HTTP endpoint and nothing marked the Lua tools as transport-sensitive.
why3: The TKT-UIR41P decision (AC 7, TestRemoteMCP_NoLuaTools) lived only in ticket prose; the planned test was never written, so nothing failed when the exclusion was missed.
why4: 'Tool registration was opt-out by default: a tool is exposed remotely unless someone removes it, so omissions fail open.'
why5: Security acceptance criteria are tracked as ticket text rather than as named tests that must exist before a ticket can close.
prevention: Lua tools are now opt-in per server (WithLuaTools), so a new networked wiring fails closed. A test in cmd/rela-server builds the remote server through its production constructor and asserts no lua_* tool is listed or callable.
status: done
---

## Problem

Found in code review of develop fa09ecf96.

Remote MCP (`/api/v1/_mcp`) registered `lua_eval`, `lua_run` and `lua_list`.
TKT-UIR41P decided (AC 7, PLAN-O8KMBQ `TestRemoteMCP_NoLuaTools`) that the Lua
tools are not offered remotely, but that was never built.

At the time the Lua reads were also unrestricted. TKT-4QSZ8Y fixed that by
giving remote Lua gated reads, and it also gated `search_entities`. Gated reads
still let a remote caller run arbitrary scripts in the server process, which is
a denial-of-service surface, and no remote use case needs it.

## Fix

- `mcp.WithLuaTools()` makes the Lua tools opt-in. Only the stdio wiring
  (`rela mcp`) passes it.
- The remote server (`newRemoteMCPServer`) gets no Lua deps and does not
  register the tools; a call fails as an unknown tool.
- Tests: `TestRemoteMCPDeps_UsesGatedHandles` (cmd/rela-server, real MCP client
  over HTTP) and `TestNewServer_LuaToolsAreOptIn` (internal/mcp).
