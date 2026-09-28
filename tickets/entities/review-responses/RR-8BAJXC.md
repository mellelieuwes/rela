---
id: RR-8BAJXC
type: review-response
title: Other writers already ran without writeMu
finding: MCP entity writes, scheduler, CalDAV and Lua outside dataentry never took writeMu, so the races already existed on fs. Moving correctness into the manager fixes them too.
severity: minor
resolution: Stated in ticket description and docs.
status: addressed
---

## Finding

MCP entity writes, scheduler, CalDAV and Lua outside dataentry never took
writeMu, so the races already existed on fs. Moving correctness into the manager
fixes them too.
