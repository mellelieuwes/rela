---
id: RR-0YPT74
type: review-response
title: 'S5: recently modified reads the whole graph per @'
finding: type:<all> sort:modified:desc loads and sorts every visible entity; limit only trims the response.
severity: significant
resolution: Client caches the answer per scope for 60 s (mentionStartingList.ts, tested). api-reference.md now says limit does not bound server work. Store-side ordering filed as TKT-KWQ2YN.
status: addressed
---
