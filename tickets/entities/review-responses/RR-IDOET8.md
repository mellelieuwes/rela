---
id: RR-IDOET8
type: review-response
title: 'Nit: related rows ordered by id'
finding: include=* returns neighbours in map order and loads all of them to show 8.
severity: nit
reason: Related rows are a hint and fill at most 8 slots; ranking them needs a relevance signal that does not exist yet.
status: deferred
---
