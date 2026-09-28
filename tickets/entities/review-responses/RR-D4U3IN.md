---
id: RR-D4U3IN
type: review-response
title: IsStateRef guard does not cover attachment lookups
finding: memstore/fsstore attachment existence checks index entities[entityID] directly, so X@draft still resolves there by key coincidence; the IsStateRef doc claimed every backend answers the same.
severity: minor
resolution: Doc narrowed to the entity getters and names the attachment lookups as not covered. Attachment routes take a bare id from the handler; extending the guard is outside this bug.
status: addressed
---
