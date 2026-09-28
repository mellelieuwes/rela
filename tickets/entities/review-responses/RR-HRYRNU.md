---
id: RR-HRYRNU
type: review-response
title: Per-GET cost grows with every action
finding: computeDetailActions recompiled every view and action condition and recomputed field verdicts per action on every entity response.
severity: minor
resolution: One detailActionCheck per call computes the hidden set, the redacted entity and the condition lookup at most once.
status: addressed
---
