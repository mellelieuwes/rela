---
id: RR-C1ILS5
type: review-response
title: 'Design review: A pulled value the schema rejects makes the twin permanently unpullable'
finding: 'TwinSync still enforces validation, transitions, transition guards and unique constraints (design:274-276). If the external system moves `status` along a path the rela state machine forbids, sends an enum value rela doesn''t declare, clears a required property, or produces a duplicate unique value, updateCore fails (manager.go:1128-1172). Pull returns that error and leaves the twin untouched, so every later pull fails the same way. Because the field is theirs, no rela user can fix it (the guard blocks edits). The twin is stuck for good, with no finding explaining why. The design does not decide this case. Fix: choose and document one policy. Either Pull applies the acceptable subset and records a new finding kind (e.g. `rejected`, carrying the validation error) with state pending, or theirs-field transitions are exempt under TwinSync (validation and unique still apply). Add a Pull acceptance case: ''pulled status violates the type''s transitions''.'
severity: significant
resolution: 'A pull whose write fails validation, a transition or a unique constraint records a `rejected` finding with the error message and leaves the twin pending without advancing the base; errors carry a WriteRejected marker (entitymanager ValidationError and the transition wrapper). Tests: TestWriteErrors_WriteRejectedMarker, TestTwinCLI_RejectedPullSucceeds.'
status: addressed
---
