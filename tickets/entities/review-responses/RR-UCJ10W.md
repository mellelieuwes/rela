---
id: RR-UCJ10W
type: review-response
title: Exact-band-only rule rejects valid accepts
finding: A unique exact quote with edited surroundings scores ~0.67 and would be refused.
severity: minor
resolution: 'Predicate: collapsed matched span equals collapsed quote AND (confidence >= ConfidenceExact OR quote occurs once). Lives in comments.ApplyReplacement; exposed as a server-computed acceptable field on commentWire. (implemented)'
status: addressed
---
