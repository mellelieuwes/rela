---
id: RR-TK15DS
type: review-response
title: Reconciler already-exists drops the request's relation properties
finding: Treating create-already-exists as success silently discards this request's opts.Properties.
severity: minor
resolution: On already-exists the reconciler applies the desired properties through UpdateRelation.
status: addressed
---

## Finding

Treating create-already-exists as success silently discards this request's
opts.Properties.
