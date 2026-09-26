---
id: RR-G04YKR
type: review-response
title: GatedReads builds DeclarativeGate twice; dead _ = e; types snapshot
finding: Build the gate once; drop the dead assignment in delete_entity; note the metamodel type snapshot.
severity: nit
reason: The dead _ = e is removed. Building DeclarativeGate is a nil check around a pointer, so sharing it adds plumbing for no gain. The type snapshot matches every other GatedReads handle, which are also built from the assembled metamodel.
status: wont-fix
---
