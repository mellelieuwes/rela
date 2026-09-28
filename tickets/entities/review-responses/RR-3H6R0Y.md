---
id: RR-3H6R0Y
type: review-response
title: Assertion satisfied by listener queries
finding: 'The listener''s primeWatermark/catchUp queries run on the traced pool, so ''pgstore: query'' could match without GetEntity being traced.'
severity: minor
resolution: Test now queries a unique id and asserts that id appears in the log.
status: addressed
---
