---
id: RR-PG14A3
type: review-response
title: 'Hidden-field names must carry the search prop: prefix'
finding: 'FieldRedactor returns bare names; MatchHasVisibleField compares prefixed names. Missing the prefix silently disables the oracle filter. Fix: qualify with search.PropFieldPrefix and pin with a unit test where the value occurs only in the property.'
severity: minor
resolution: 'hiddenFields qualifies names with search.PropFieldPrefix. Tests: TestSearcher_Gating (hidden-property match dropped) and TestGatedReads_SearchDropsHiddenFieldMatch.'
status: addressed
---
