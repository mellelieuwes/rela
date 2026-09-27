---
id: RR-WX86GF
type: review-response
title: Multi-error messages collapse in the toast
finding: ConfigValidationError joins errors with newlines but .toast-message had no pre-line, so a list of errors rendered as one long line.
severity: minor
resolution: 'Toast.vue .toast-message uses white-space: pre-line.'
status: addressed
---
