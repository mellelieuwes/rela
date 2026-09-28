---
id: RR-0QG9NQ
type: review-response
title: Unique scan runs under the RELW lock on pg
finding: 'The unique: check scans under the RELW advisory lock, which serializes writers of types with unique properties.'
severity: minor
reason: 'Correctness first: the scan must be exclusive to be a backstop. An index-backed unique check is a separate performance ticket.'
status: deferred
---

## Finding

The unique: check scans under the RELW advisory lock, which serializes writers
of types with unique properties.
