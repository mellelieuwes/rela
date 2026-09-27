---
id: RR-I76C4A
type: review-response
title: Invisible characters can disguise a replacement
finding: Bidi overrides and zero-width characters pass the C0 filter.
severity: nit
resolution: Replacement validation rejects U+200B-200F, U+202A-202E, U+2066-2069 and U+FEFF. (implemented)
status: addressed
---
