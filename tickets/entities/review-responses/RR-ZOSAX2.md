---
id: RR-ZOSAX2
type: review-response
title: Next-action no-identity test cannot tell skipped from matched nothing
finding: The no-identity next-action case used only a positive related(); so a nil suggestion could also come from a wrongly bound traversal.
severity: minor
resolution: Added the not related(...) form to the no-identity case in TestNextAction_RelatedCondition; only a skipped source gives nil there.
status: addressed
---
