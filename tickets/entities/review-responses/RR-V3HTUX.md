---
id: RR-V3HTUX
type: review-response
title: Replacement belongs on Anchor, not TextAnchor
finding: TextAnchor mirrors textanchor.Anchor exactly; a replacement is not a locator.
severity: nit
resolution: Replacement *string moved to Anchor; commentstest asserts an empty-string replacement survives on all four backends. (implemented)
status: addressed
---
