---
id: RR-Y93AJ3
type: review-response
title: Tx callback must take the view explicitly, not a Deps copy
finding: Deps fields (TransitionGraph, TransitionGuard/ACL, CopyVisibility, VersionRecorder, Cascade) close over the outer store, and Manager methods (assignManagedOrder, collectSiblingOrders, maybeRenumberSide) read m.deps.Store. An outer-store write inside the callback deadlocks on fs/mem/sqlite.
severity: significant
resolution: checkUniqueProperties, the order helpers and the relation merge take an explicit st store.Store. Candidates are built outside the callback. Callbacks touch only the view.
status: addressed
---
