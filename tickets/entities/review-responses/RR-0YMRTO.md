---
id: RR-0YMRTO
type: review-response
title: 'Design review: Presence-based PatchEntity guard breaks same-value writes such as `rela sync push`'
finding: 'The guard on PatchEntity checks names that are present or unset (design:270). `rela sync push` sends every property and always sends content on update (cli/sync/push.go:238-246). The dataentry validator is also strict on read-only fields and does not exempt same-value writes (dataentry/affordances.go:375-377, 412-419). With pacts on an fs primary, every push of a twinned entity is rejected, even when no theirs value changed; with body or `*` theirs, always. Fix: make the entitymanager check change-based on PatchEntity too. Merge, then reject only fields whose normalized value differs from the stored one, the same semantics as the UpdateEntity diff. Decide explicitly whether dataentry''s verdict check lets unchanged owned values through. Add a test: PATCH with an unchanged theirs value succeeds, and a changed one fails with TheirsWriteError.'
severity: significant
resolution: 'The guard is change-based on both write paths (merge, then compare with canonical.EqualValue/EqualBody), and the data-entry validator applies the same rule, so re-sending an unchanged owned value passes. Tests: the unchanged-value rows of TestPatchEntity_TwinOwnership and the dataentry PATCH test.'
status: addressed
---
