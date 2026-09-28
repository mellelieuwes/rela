---
id: RR-HOM1DZ
type: review-response
title: UpdateRelation read-merge-write not in a Tx
finding: ApplyRelation's update branch and the soft-condition fallback in writeUpdateRelation read, merged and wrote a relation outside a transaction, so a concurrent writer could be lost.
severity: significant
resolution: Both now run inside store.Tx using the Tx view (persistApplyRelation, upsertEdge/edgeOnFace).
status: addressed
---

## Finding

ApplyRelation's update branch and the soft-condition fallback in
writeUpdateRelation read, merged and wrote a relation outside a transaction, so
a concurrent writer could be lost.
