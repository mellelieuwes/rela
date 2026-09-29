---
id: RR-Z61MF3
type: review-response
title: 'Design review: Theirs body (incl. theirs:["*"]) never converges on fsstore'
finding: 'Reconcile sets base := R for a theirs field it writes (design:200), but fsstore does not store R. It stores markdown.FormatMarkdown(R), because it reflows every body on write (fsstore/markdown.go:175-183). On the next pull, R = B but L = fmt(R) ≠ B, so the rule at design:201 fires: it adds a local_drift finding and writes R back. The store reflows it again, and this repeats on every pull: a write, an audit row and a version bump each time, with the twin never reaching in_sync. This hits the design''s own headline example (`theirs: ["*"]`). The design also says to ''reuse canonical''s normalization'', but canonical exports only HashEntity/HashRelation, and normalize is unexported (internal/canonical/canonical.go:63,85,257). Fix: export a value/body equality from canonical (e.g. canonical.EqualValue plus a body comparison that applies the same FormatMarkdown canonical''s hash already uses) and use it in Reconcile. Alternatively, take NewBase for written fields from the persisted entity. Add an acceptance criterion: on the fs backend, a second pull with an identical remote (body, numbers, lists) writes nothing, emits no audit row and leaves the twin in_sync.'
severity: critical
resolution: 'canonical exports EqualValue and EqualBody (same normalization as the content hash, body compared after FormatMarkdown); Reconcile and the entitymanager guard both use them. Tests: TestEqualValue_AgreesWithHash, TestEqualBody_IgnoresReflow, TestTwinCLI_SyncLoop (second pull: nothing to write, version unchanged).'
status: addressed
---
