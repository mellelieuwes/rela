---
id: RR-B9EQNL
type: review-response
title: 'Code review: Keep rejected and local_drift findings through pushed; today it marks the twin in_sync'
finding: '`Pushed` keeps only conflict findings and otherwise sets `StateInSync` (service.go:484-488). A twin whose last pull recorded a `rejected` finding (a theirs value rela refused) or `local_drift` becomes in_sync after a routine push of ours fields. With BaseVersion equal to the current version, `Pending` then skips it (in_sync and not LocalChanged). The refused external value is still not in rela, and the twin no longer shows that. This contradicts R6 (''the twin is not stuck silently''), GUIDE-twins.md:637-639 (''the twin stays pending'') and GUIDE-twins.md:405 / cli-reference (''foreign_edit findings are cleared''), which describe only foreign_edit being cleared. Fix: in Pushed, keep `FindingRejected` and `FindingLocalDrift` alongside the conflicts, and set the state to pending when any remain. A push settles ours and shared fields, not theirs. Add a TestPushed case: a rejected pull followed by pushed stays pending and keeps the finding.'
severity: minor
resolution: Pushed keeps rejected and local_drift findings (state pending) as well as unresolved conflicts (state conflict); only foreign edits and decided conflicts are cleared. Covered by a service test.
status: addressed
---
