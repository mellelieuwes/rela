---
id: RR-T2Z4T7
type: review-response
title: Wildcard branch loads full entities
finding: visibleListByTypes wildcard branch uses ListEntities instead of headers, against the collection-read rule.
severity: nit
reason: Predates this fix and is reached only without an ACL; switching to headers changes the content-loading contract of that branch and belongs in its own change.
status: deferred
---
