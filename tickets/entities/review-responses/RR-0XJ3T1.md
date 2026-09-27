---
id: RR-0XJ3T1
type: review-response
title: Re-accept after failed resolve can double-apply
finding: Patch-then-resolve relies on the quote going stale; a replacement that keeps the quote (e.g. appends text) matches again and applies twice.
severity: significant
resolution: 'Order reversed: resolve the comment first, then PatchEntity; un-resolve if the patch fails. Test with a replacement containing the quote. (implemented)'
status: addressed
---
