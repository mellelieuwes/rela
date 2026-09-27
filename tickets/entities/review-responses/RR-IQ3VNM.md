---
id: RR-IQ3VNM
type: review-response
title: Stale message blames the wrong cause
finding: The raw vs visible mismatch said the suggestion was stale while the real cause is a concurrent write from outside writeMu.
severity: nit
resolution: The message now says the body changed while accepting; the comment explains the case.
status: addressed
---
