---
id: RR-EST0W6
type: review-response
title: Data-migration face move and raw delete strand comment threads
finding: datamigration steps delete a face (DeleteEntityState) or an entity (DeleteEntity) on the raw store, below the entitymanager hook, so the comment threads survive and a recreated face inherits them.
severity: minor
reason: Separate write path with its own trust model (operator shell, raw store); filed as a follow-up bug rather than widening this fix.
status: deferred
---
