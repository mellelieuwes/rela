---
id: RR-M0GJP6
type: review-response
title: Date layout of a selection result
finding: For x or y with dates the result type must keep the layout-bearing date type, or later literal coercion against the selection parses with the default layout.
severity: minor
resolution: 'Plan updated: the result type takes the operand type that carries a layout; test with a custom date format.'
status: addressed
---
