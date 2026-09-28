---
id: RR-HW36VR
type: review-response
title: Search tests lack a real ACL policy
finding: No _search test used a real ACL policy; the denied-world test binds the handle by hand.
severity: minor
resolution: TestSearch_FaceGrantIsHonored runs through the router with a real policy. TestAttachWorld_DeniedWorldRefusedLikePermitted covers the middleware denial path.
status: addressed
---
