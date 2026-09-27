---
id: RR-7C5HMB
type: review-response
title: config-error in SSEEventType but never dispatched
finding: on('config-error') subscribers would be silently ignored.
severity: nit
resolution: Removed config-error from the SSEEventType handler union; the composable handles it itself.
status: addressed
---
