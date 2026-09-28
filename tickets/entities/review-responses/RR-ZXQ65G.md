---
id: RR-ZXQ65G
type: review-response
title: Duplicate backoff helpers
finding: cas.go and webhook_routes.go each have a jittered backoff helper.
severity: nit
reason: They live in separate packages with different bounds; a shared helper would add a dependency for a few lines.
status: wont-fix
---

## Finding

cas.go and webhook_routes.go each have a jittered backoff helper.
