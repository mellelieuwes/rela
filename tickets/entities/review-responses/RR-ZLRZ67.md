---
id: RR-ZLRZ67
type: review-response
title: Identity fallback comment claims narrowing only
finding: queryIdentityFor's comment says an unresolved principal narrows and never widens; which is false for not related(... current_user.id).
severity: nit
resolution: 'Reworded the comment: a negated identity comparison matches every row the reader can already see; never past the read gate.'
status: addressed
---
