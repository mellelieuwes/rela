---
id: RR-UXGUT7
type: review-response
title: delete_entity reports raw per-entity relation counts including hidden neighbours
finding: 'internal/mcp/tools_entity.go:312/326 use CountRelations, which goes raw. The count includes edges to hidden entities. Fix: count through the gated ListRelations.'
severity: minor
resolution: delete_entity counts relations through the gated ListRelations, so edges to hidden neighbours are not counted.
status: addressed
---
