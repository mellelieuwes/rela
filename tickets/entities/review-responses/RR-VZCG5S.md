---
id: RR-VZCG5S
type: review-response
title: Attachment write authorized before the lock (TOCTOU)
finding: The attachment handler checked write permission before waiting on the per-property lock. A permission or entity change during the wait was not re-checked.
severity: significant
resolution: attachment.Service re-authorizes through a required WriteAuthorizer after the lock is taken, for write, delete and detach. Pinned by TestService_ReauthorizesUnderLock.
status: addressed
---

## Finding

The attachment handler checked write permission before waiting on the
per-property lock. A permission or entity change during the wait was not
re-checked.
