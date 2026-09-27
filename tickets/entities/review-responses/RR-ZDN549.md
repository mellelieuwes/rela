---
id: RR-ZDN549
type: review-response
title: Partial config-error payload shows undefined
finding: The toast detail interpolated data.file and data.error without type checks.
severity: nit
resolution: Detail is built only when error is a string; test covers an empty object payload.
status: addressed
---
