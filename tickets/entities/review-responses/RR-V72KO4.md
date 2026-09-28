---
id: RR-V72KO4
type: review-response
title: Root coercion does not reach arithmetic or call args
finding: -(c and 1 or 2) or takeint(c and 1 or 2) under an int profile fail with got number. Pre-existing for bare literals but easier to hit now.
severity: minor
reason: Pre-existing limitation of top-level coercion (a bare literal in a call argument already fails). Documented in doc.go; widening coercion into arithmetic and call arguments is a separate change.
status: deferred
---
