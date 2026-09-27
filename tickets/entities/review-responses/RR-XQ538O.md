---
id: RR-XQ538O
type: review-response
title: Routing and naming of accept
finding: No four-segment route case; a CanAccept identical to CanRead is redundant.
severity: nit
resolution: Four-segment case added plus router_walk_test entry; accept reuses Authorizer.CanRead. (implemented)
status: addressed
---
