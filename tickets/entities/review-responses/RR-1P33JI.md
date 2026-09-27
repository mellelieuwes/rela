---
id: RR-1P33JI
type: review-response
title: HTTP status mapping gaps
finding: No-replacement is state, not wire format; entity deleted between gate and patch maps to 422.
severity: nit
resolution: No replacement returns 409; a not-found from the patch returns 404. (implemented)
status: addressed
---
