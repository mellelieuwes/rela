---
id: RR-MEXNEZ
type: review-response
title: rename/delete/analyze report raw relation counts
finding: '[security] rename dry-run RelationsUpdated, cascade delete DeletedRelations, the non-cascade ErrHasRelations bit and analyze cardinality/orphans use raw counts, disclosing how many hidden neighbours a visible entity has. The new comment in delete_entity overclaims.'
severity: significant
resolution: 'delete_entity and rename_entity report counts of visible edges (gated ListRelations), taken before the write. The one-bit ErrHasRelations signal and the structural analyze counts are listed as residuals in acl-security.md. Test: TestACL_WriteCounts_OmitHiddenEdges.'
status: addressed
---
