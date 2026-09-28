---
id: RR-TLBHKV
type: review-response
title: Tx does not exclude plain writes on postgres
finding: pgstore takes RELW only in Tx; plain writes do not. A Tx check-then-write is only safe if every writer of that object also runs in Tx. The post-create rewrite and cascadeHost.WriteEntity are whole-row unconditional updates; maybeRenumberSide, cascadeHost.WriteRelation and ApplyRelation rewrite relation properties outside any Tx.
severity: critical
resolution: Post-create rewrite and cascadeHost.WriteEntity use CAS-and-retry (re-read, apply PropertiesSet, unique, UpdateEntityIf). All manager relation-property writes (UpdateRelation merge, managed order + create, renumber, cascade WriteRelation, ApplyRelation) run inside Tx, so the invariant holds on every backend. Invariant documented per object.
status: addressed
---

## Finding

pgstore takes RELW only in Tx; plain writes do not. A Tx check-then-write is
only safe if every writer of that object also runs in Tx. The post-create
rewrite and cascadeHost.WriteEntity are whole-row unconditional updates;
maybeRenumberSide, cascadeHost.WriteRelation and ApplyRelation rewrite relation
properties outside any Tx.
