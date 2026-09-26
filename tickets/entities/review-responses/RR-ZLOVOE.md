---
id: RR-ZLOVOE
type: review-response
title: search_entities hydrates hits one by one and rebinds ACL per hit
finding: Up to 1000 separate gated GetEntity calls, each rebinding the acl.Request (membership walk per row). CLAUDE.md requires one bind per operation and a batched read with a Counting budget test.
severity: significant
resolution: 'Hydration is one batched read per search. The remote route already binds one acl.Request per HTTP request (attachACLRequest), so no per-row rebind happens there. Test: TestHydrateHits_ReadBudget (same reads at 10 and 50 hits).'
status: addressed
---
