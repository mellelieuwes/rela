---
id: RR-8IVSHW
type: review-response
title: Search decorator emits the raw indexed Title
finding: 'SearchVisibleFields keeps a hit matched on id/content even when the title property is hidden; Hit.Title is the raw indexed value. Fix: clear Title in the decorator; consumers take titles from the gated reader.'
severity: minor
resolution: 'visibility.Searcher clears Hit.Title; search_entities takes the title from the gated reader. Test: TestSearcher_Gating.'
status: addressed
---
