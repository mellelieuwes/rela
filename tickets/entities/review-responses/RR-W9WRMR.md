---
id: RR-W9WRMR
type: review-response
title: 'Code review: Let a conflict resolved in rela reach push_set, or change the documented fix'
finding: 'GUIDE-twins.md:607-610 says a conflict is resolved by changing the shared field in rela "and have the agent push it". The code does not support that. `pushSet` drops every field that has a conflict finding (service.go `include := pushable && !conflicts[f]`), and `Pushed` leaves the base of conflicting fields alone and keeps their findings. After a user sets the field to X in rela, `pending` lists the twin as "changed in rela" and "conflict: f", but its push_set does not contain f. An agent that pushes push_set and then calls `pushed` never writes X to the external side. The next pull sees L=X≠R, so the conflict persists. The only exit is an edit on the external side, which the agent has no way to know it should make. Fix, either: (a) include a conflict field in push_set when rela''s current value differs from the finding''s recorded `Ours` (a human edited it after the conflict), and have Pushed advance its base and drop that conflict; or (b) change the guide (and the cli-reference push_set text) to say that conflicts can only be resolved by making the external side match, or by setting rela to the external value, and that conflict fields are never in push_set.'
severity: significant
resolution: A shared field whose conflict a person decided in rela (local value differs from the finding's recorded ours) is included in push_set; `pushed` advances its base and drops the conflict. Covered by a service test; the guide documents both ways to resolve a conflict.
status: addressed
---
