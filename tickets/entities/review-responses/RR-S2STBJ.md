---
id: RR-S2STBJ
type: review-response
title: Other Lua surfaces keep the raw searcher; stale comments
finding: '[security] luaReadDepsFor passes the raw searcher to scheduled/automation Lua (TKT-GGQ0JT oracle). lua/deps.go:67 and runtime.go:1713 still say hits are ungated.'
severity: minor
reason: Stale comments in lua/deps.go and runtime.go are fixed. Gating the searcher for scheduled and automation Lua changes those runtimes and needs its own review of AllowAllReader wirings; tracked as TKT-5GPFZY.
status: deferred
---
