---
id: RR-Z6GAIJ
type: review-response
title: Root coercion also accepts plain literals
finding: 'Coercing the root to profile.Expected in compile also makes a bare literal such as computed: 5 on an integer property compile, which fails today.'
severity: minor
resolution: 'Accepted as intended: it only affects programs that fail today. Covered by TestSelection_LiteralCoercion/bare literal root. Not called out in the docs: a constant computed value is not a use case worth documenting.'
status: addressed
---
