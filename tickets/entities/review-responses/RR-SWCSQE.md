---
id: RR-SWCSQE
type: review-response
title: 'Code review: Gate git conflict resolve writes, or document them, for twinned entities'
finding: '`handleV1ConflictResolve` builds the resolved entity from choices the caller makes, including arbitrary `manual_content` for the body. It checks only the ACL (`authorizeConflictResolve`) and writes the file directly with `conflict.WriteResolved` (write_handler.go:1351). Ownership is never checked, so over HTTP a user can pick the stale side or type a new body or value for a theirs field of a twinned entity. The twins.go and GUIDE-twins lists of ungated paths do not mention this endpoint. Fix: before WriteResolved, load the stored entity and call `h.affordances.validateEntityWrite(ctx, stored, changedProps, unsets, &resolvedEntity.Content)`, answering with `writeExternallyOwned` on denial. Alternatively, explicitly classify conflict resolve as an fs-level edit (like git pull and hand edits, reported as local_drift) and document it in entitymanager/twins.go:79-96, entitymanager/CLAUDE.md and GUIDE-twins "Where it is not".'
severity: significant
resolution: 'Conflict resolve validates the changed fields and the resolved body against the stored row with the same ownership rule before writing (422 externally_owned). Test: TestV1ConflictResolve_TwinOwnership.'
status: addressed
---
