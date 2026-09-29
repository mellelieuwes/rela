---
id: RR-I09OT8
type: review-response
title: 'Design review: propose on theirs fields is accepted but does nothing'
finding: 'Finding.Propose exists only on foreign_edit, i.e. for ours fields (design:133). A rela edit to a theirs field is rejected by the guard, so nothing can be proposed. With theirs:["*"], every valid propose entry is theirs and has no effect. The validation rules only require propose ∩ shared = ∅ (design:46-47). Fix: validate propose ⊆ ours (reject theirs names and `*`) with a clear message, or define what proposing a theirs edit means. Stage 1 has no such workflow.'
severity: minor
resolution: Validation now requires propose to name ours fields only (theirs names, *, shared and computed properties are rejected with specific messages). Covered in pacts_test.go.
status: addressed
---
