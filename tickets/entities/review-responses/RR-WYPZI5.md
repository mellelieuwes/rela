---
id: RR-WYPZI5
type: review-response
title: 'M5: scoped search filtered after the free-text cap'
finding: ?type= was applied after the 1000-hit relevance cap across all types.
severity: minor
resolution: 'handleV1Search splices a declared type into the query as a type: clause before executeQuery. TestSearchTypeParam_ScopesFreeText.'
status: addressed
---
