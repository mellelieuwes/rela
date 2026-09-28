---
id: RR-ZIUHR6
type: review-response
title: Type listing in _search ignores type@face grants
finding: '[security] visibleEntitiesOfType built its scopeRequest without Faces, so a type@face grant was ignored and the world ranked withheld faces, serving their titles and bodies.'
severity: critical
resolution: 'visibleEntitiesOfType takes the face allowlist from the read gate and passes it as scopeRequest.Faces. TestSearch_FaceGrantIsHonored pins type: and prop: queries under the default and a non-default world.'
status: addressed
---
