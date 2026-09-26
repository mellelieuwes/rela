---
id: RR-581HSP
type: review-response
title: Gated search logic duplicated with dataentry
finding: visibility.Searcher repeats aclReadGate.SearchScope, hiddenSearchFields and faceGatedHits. Converge dataentry onto visibility.Searcher.
severity: minor
reason: Moving dataentry onto visibility.Searcher touches the web search path and its tests; tracked as TKT-5GPFZY.
status: deferred
---
