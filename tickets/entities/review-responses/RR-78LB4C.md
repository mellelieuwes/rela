---
id: RR-78LB4C
type: review-response
title: parseAddress fallback and duplication undocumented
finding: The lenient fallback's safety and the reason it duplicates GetEntityAt were not stated.
severity: nit
resolution: Doc comment now states why the fallback cannot pair a gate on one id with a load of another, and why it splits rather than delegating.
status: addressed
---
