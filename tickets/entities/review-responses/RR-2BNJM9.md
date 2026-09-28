---
id: RR-2BNJM9
type: review-response
title: Int coercion error says compare under or
finding: entity.i or 1.5 reports 'cannot compare an int field with the non-integer literal 1.5' although nothing is compared.
severity: minor
resolution: 'Int coercion errors no longer mention comparing: ''the non-integer literal 1.5 cannot be an int'' and ''too large to be an exact int''. Test added.'
status: addressed
---
