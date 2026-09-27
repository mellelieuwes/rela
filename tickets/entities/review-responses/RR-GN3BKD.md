---
id: RR-GN3BKD
type: review-response
title: Accept wiring must use the enterWrite pattern
finding: A write-lock closure is invisible to provision_seam_invariant_test.go.
severity: minor
resolution: commentsHandler gets writeMu, provision and a consumer-side entityPatcher; an enterWrite method; the comments file is added to the invariant test list. (implemented)
status: addressed
---
