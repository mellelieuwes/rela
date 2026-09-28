---
id: RR-41GI2S
type: review-response
title: Detail action button is not a PendingButton
finding: frontend/CLAUDE.md asks for PendingButton on buttons that trigger a request. The detail action button is a plain button disabled while running.
severity: minor
reason: PendingButton requires a pendingLabel that must not be derived from the label, and action config has no such field. Commands and copy offers in the same header use plain buttons too. Adding a pending_label to actions is a separate change.
status: deferred
---
