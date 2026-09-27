---
id: RR-QC30IH
type: review-response
title: Ref evaluation order follows map order
finding: evalTraversal evaluated n.refs in map order; so the reported error could vary with several failing refs.
severity: nit
resolution: evalTraversal iterates sortedKeys(n.refs).
status: addressed
---
