---
id: RR-0APPL4
type: review-response
title: 'Design review: Pushed has no compare-and-swap, so a user edit is silently reverted'
finding: 'Pushed sets Base := the current local values and BaseVersion := the current version (design:244-249), with no precondition. Take this sequence: the agent reads the entity at T0 and pushes those values to the external system; a user edits a shared field at T1; the agent then calls `pushed` at T2. The twin now records T1''s value as agreed. On the next pull, L = B and R (still T0) ≠ B, so the shared rule takes R (design:204). That silently reverts the user''s edit, and the audit shows it as tool=twin. For an ours field, the same race produces a false foreign_edit. Nothing gives the agent a token to prevent this: PendingItem carries only Reasons (design:232-236). Fix: have Pending/show return the entity version plus the field-level push set (field, base, local value for every ours/shared field where L≠B). `rela twin pushed` must take `--version` and fail with a conflict error when the entity''s current version differs. Store BaseVersion as that version and Base as the values at that version. Add an acceptance criterion for this race.'
severity: critical
resolution: 'Pending items carry the entity version and the push set; `Pushed` requires that version and fails with ErrVersionConflict when the entity moved (CLI `rela twin pushed --version` is required). Test: TestPushed_VersionConflict and the CLI stale-version case.'
status: addressed
---
