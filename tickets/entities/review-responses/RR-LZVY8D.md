---
id: RR-LZVY8D
type: review-response
title: schema resolves plural-looking relation names to entity types
finding: schema type=tests returned entity test even when a relation tests exists.
severity: minor
resolution: describe tries exact entity then exact relation names before the plural fallback. Pinned by TestHandleSchema_ExactNameBeforePlural.
status: addressed
---
