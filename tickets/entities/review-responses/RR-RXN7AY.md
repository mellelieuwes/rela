---
id: RR-RXN7AY
type: review-response
title: Double race in relation reconciler returned 500
finding: 'upsertEdge fell back once between update and create. When a third request flipped the edge again (update: not found, then create: already exists) the PATCH answered 500 relation_write_failed. Found by the new AC9 test.'
severity: significant
resolution: upsertEdge alternates up to 8 times; persistent flipping maps to 409 conflict in writeRelationsApplyError. Pinned by TestApplyRelationsModern_ConcurrentPatchesNever500 (failed before the fix, passes 20/20 with -race).
status: addressed
---

## Finding

upsertEdge fell back once between update and create. A third concurrent request
flipping the edge again made the PATCH answer 500.
