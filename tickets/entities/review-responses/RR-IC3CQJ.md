---
id: RR-IC3CQJ
type: review-response
title: 'Design review: theirs:["*"] makes computed properties theirs, contradicting validation'
finding: 'Pact.Owner returns OwnerTheirs for every field when allTheirs is set (internal/metamodel/pacts.go:172-175). Validation, however, forbids computed properties in theirs/shared. If the agent maps a computed property under `*`, Reconcile puts it in the Patch, rejectComputedPatch fails the whole pull (manager.go:1074), and the twin is stuck. Fix: Owner(field) returns OwnerOurs for computed properties, and `*` expands to non-computed properties plus body. Either the Pact must know which fields are computed at compile time, or Reconcile skips them.'
severity: minor
resolution: 'The compiled Pact knows the type''s computed properties: Owner returns ours for them even under theirs ["*"], and Reconcile skips them. Test: TestPact_TheirsStarOwnsEveryNonComputedField.'
status: addressed
---
