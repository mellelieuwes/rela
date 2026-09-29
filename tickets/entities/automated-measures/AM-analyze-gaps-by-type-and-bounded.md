---
id: AM-analyze-gaps-by-type-and-bounded
type: automated-measure
title: ID gap analysis skips manual-ID types by type and bounds its enumeration
description: 'Tests seed a manual-ID type whose ids carry more dashes than its declared prefix plus a sequential type with a huge gap, and assert that only the sequential type is reported, that the CLI analyzer lists at most 100 ids and counts the rest, and that the data-entry analyzer stops at its section cap. Mutation-checked: restoring the prefix match or the unbounded loop fails them.'
kind: test
location: internal/analysis (TestFindGaps_ManualIDsAreSkippedByType, TestFindGaps_LargeGapIsCountedNotListed); internal/dataentry (TestAnalyzeGaps_ManualIDsSkippedByType, TestAnalyzeGaps_HugeGapStopsAtTheCap)
status: active
---
