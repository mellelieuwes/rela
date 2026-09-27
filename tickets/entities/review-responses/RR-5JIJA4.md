---
id: RR-5JIJA4
type: review-response
title: 'Design review: Changes to ours fields made by the sync write''s automations are never pushed'
finding: 'Pull computes State and NewBase from the Plan, which is the state before automations run (design:207-208). updateCore then runs on-update automations that mutate the entity before persisting it (manager.go:1136-1150). This is common: a theirs `status` change sets an ours `closed_at`, or creates a checklist. Once finding 1 is fixed, BaseVersion is the post-automation version, so LocalChanged is false. The Plan said in_sync, so the automation-set ours value is never listed as pending and never reaches the external side. Fix: after the write, derive local := snapshot(result.Entity), recompute ''ours/shared field with L≠NewBase'' against it, and set the state to pending when anything differs. Add a table case with an on-update `set` automation.'
severity: significant
resolution: 'After the sync write the service snapshots the post-automation entity and derives state from it: an automation that moves an ours or shared field leaves the twin pending with that field in the push set. Covered by a service test with a writer that mutates an ours field.'
status: addressed
---
