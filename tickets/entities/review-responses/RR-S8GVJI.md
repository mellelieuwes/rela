---
id: RR-S8GVJI
type: review-response
title: Nil condition vs SQL NULL
finding: A nil bool condition is an eval error here, while SQL CASE WHEN NULL takes the ELSE branch. A future lowering could silently change behaviour.
severity: minor
resolution: 'Plan updated: the logicalNode doc comment records this so a future SQL lowering must preserve the error or guard it.'
status: addressed
---
