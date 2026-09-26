---
id: RR-NN6UPT
type: review-response
title: writtenEntityTable swallows read errors
finding: Any readFace error returns a stub; only ErrNotFound should. create_entity with nil VisibleReader silently returns a stub.
severity: minor
resolution: writtenEntityTable returns the id/type stub only on store.ErrNotFound; any other read error and a missing reader raise.
status: addressed
---
