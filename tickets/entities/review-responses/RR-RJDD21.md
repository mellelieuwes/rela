---
id: RR-RJDD21
type: review-response
title: Comment id probe before the comment:read check
finding: svc.Get ran before CanRead, so a principal without comment:read could tell existing comment ids (403) from unknown ones (404).
severity: minor
resolution: CanRead now runs before the lookup. Subtest 'without comment:read an unknown id is not distinguishable' pins the 403.
status: addressed
---
