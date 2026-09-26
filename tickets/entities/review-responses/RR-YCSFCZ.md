---
id: RR-YCSFCZ
type: review-response
title: Shared rela.cache crosses principals once remote Lua reads are gated
finding: 'cmd/rela-server/mcp.go passes svc.ScriptEngine().LuaCache(), shared with data-entry; cache keys ignore the principal. A memoized gated read by one principal is served to another. Fix: pass a nil LuaCache in the remote wiring.'
severity: significant
resolution: remoteMCPDeps leaves LuaCache nil; the godoc says why. Pinned by TestRemoteMCPDeps_UsesGatedHandles.
status: addressed
---
