---
id: RR-27FAH8
type: review-response
title: search_entities drops every hit of a faced type
finding: 'Hydration calls GetEntity(hit.ID), which reads the default face only. A faced type has no default row, so POL-1@published is dropped. Affects stdio too. Fix: read the hit''s own face.'
severity: significant
resolution: 'hydrateHits reads all hits in one ListEntities{IDs, AllStates} query and matches on (id, face). Faced hits carry a face field. Test: TestHydrateHits_FacedHitSurvives.'
status: addressed
---
