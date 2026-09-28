---
id: RR-VY5NYW
type: review-response
title: Upload stored twice in temp space
finding: The upload is spooled to a temp file and then written again by the store, doubling temp use.
severity: minor
reason: Spooling is required to keep the lock short. The Limiter bounds total use. Streaming from the spool file is a separate optimization.
status: deferred
---

## Finding

The upload is spooled to a temp file and then written again by the store,
doubling temp use.
