---
id: BUGA-VYEG2I
type: bug-analysis-checklist
title: 'Analysis: Remote MCP offers the Lua tools'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Reproduction

- [x] Bug reproduced locally (the new cmd/rela-server remote MCP test fails against the pre-fix wiring)
- [x] Minimal reproduction steps documented (BUG-RIJR6R body; assertNoLuaTools)
- [x] Environment/conditions noted (rela-server -mcp; stdio unaffected)

## Root Cause

- [x] Immediate cause identified (why1)
- [x] Contributing factors found (why2-3)
- [x] Systemic cause explored (why4-5)

## Fix Planning

- [x] Fix approach determined
- [x] Regression test planned (AM-remote-mcp-read-surface)
- [x] Related areas checked for similar issues (read handles and search gated by TKT-4QSZ8Y; no other tool runs caller-supplied code)
