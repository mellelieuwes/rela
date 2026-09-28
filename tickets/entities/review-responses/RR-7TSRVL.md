---
id: RR-7TSRVL
type: review-response
title: Root coercion errors carry no line
finding: 'compile passes line 0 to coerceOneLiteral at the root so errors read ''compile error: ...'' without position.'
severity: minor
resolution: 'Root coercion passes the root expression''s line; the test asserts ''error at line 1: invalid date''.'
status: addressed
---
