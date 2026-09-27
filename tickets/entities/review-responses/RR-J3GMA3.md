---
id: RR-J3GMA3
type: review-response
title: config-error event is lossy for late-joining tabs
finding: The config-error SSE frame is broadcast once at reload time. A tab that connects afterwards gets no signal that the running config is stale versus disk. The server log still records it.
severity: minor
reason: Matches the SSE feed contract (a hint, not a log). The server keeps serving the last valid config, so a late tab sees a consistent app. Replaying the last error on connect is a separate enhancement.
status: deferred
---
