---
id: RR-7JU3ON
type: review-response
title: Nil-valued branch still falls through
finding: The plan claimed the Lua pitfall disappears. It disappears for false only. c and entity.opt or 'low' returns 'low' when c is true and opt is absent. For a computed property this silently stores a value the author may not intend.
severity: significant
resolution: 'Plan updated: the docs state the rule (a branch value that is nil falls through to the next alternative) with this exact example and the guard form (c and (entity.opt or ''none'') or ''low''). A test pins the behaviour.'
status: addressed
---
