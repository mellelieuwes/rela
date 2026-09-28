---
id: RR-VKXB03
type: review-response
title: limit 0 or negative returns everything
finding: The default cap could be bypassed with limit 0 or -1.
severity: minor
resolution: limitArg treats a non-positive limit as the default. Pinned by TestHandleListEntities_NonPositiveLimitUsesDefault.
status: addressed
---
