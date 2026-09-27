---
id: RR-AJU7F6
type: review-response
title: 'M8: recent entities are per browser, not per user'
finding: localStorage is shared by every user of one browser profile.
severity: minor
reason: Only IDs are stored, and each is reloaded through the ACL-gated API before display, so another user sees no title or content. The browser profile is already a shared-trust boundary. The security review judged it no new exposure.
status: deferred
---
