---
id: RR-W6Z14S
type: review-response
title: Nil state returns success
finding: reloadConfig returns nil when State() is nil.
severity: nit
reason: Unreachable after NewApp publishes the first snapshot; behaviour is unchanged from the old rebuildState.
status: wont-fix
---
