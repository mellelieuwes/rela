---
id: RR-QLJWO7
type: review-response
title: Searcher does not guard Filters/Sort on hidden properties
finding: '[security] Field scope applies to free text only; a Filters- or Sort-only query on a hidden property is an oracle. No current caller sets them.'
severity: minor
resolution: 'visibility.Searcher refuses a query with Filters or Sort (ErrScope). Test: TestSearcher_RefusesFiltersAndSort.'
status: addressed
---
