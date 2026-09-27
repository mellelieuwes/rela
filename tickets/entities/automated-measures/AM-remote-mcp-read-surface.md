---
id: AM-remote-mcp-read-surface
type: automated-measure
title: 'Integration test: remote MCP offers no Lua tools'
description: Builds the remote MCP server through newRemoteMCPServer under an acl.yaml policy, serves it over HTTP and asserts tools/list has no lua_* tool and a lua_eval call fails. Also asserts the remote Deps carry gated read handles and no Lua deps. Catches a networked wiring that reuses stdio deps (the class that produced BUG-RIJR6R).
kind: test
location: cmd/rela-server/mcp_deps_test.go
status: active
---
