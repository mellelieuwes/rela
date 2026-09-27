---
id: RR-QC5GV1
type: review-response
title: 'Design review: 422 TheirsWriteError is mostly unreachable over HTTP; generic read-only denial shows first'
finding: 'Once fieldVerdicts marks owned fields Writable=false, the dataentry validator rejects the PATCH first with RuleFieldReadOnly and "field %q is not writable" (affordances.go:412-418). The design''s clear ''owned by basecamp; change it there'' message (design:268-269, 285) is never shown to SPA/API users. Also, inbound `/hooks/{id}` writes go through PatchEntity (webhook_routes.go:579) and will be refused for theirs fields; document that webhooks are not a sync path. Fix: add a dedicated affordance rule (e.g. RuleFieldExternallyOwned) whose reason names the system.'
severity: minor
resolution: New affordance rule RuleFieldExternallyOwned whose reason names the system; the API answers 422 externally_owned with the TheirsWriteError sentence ("field status of SC-015 is owned by basecamp (Twin); change it there").
status: addressed
---
