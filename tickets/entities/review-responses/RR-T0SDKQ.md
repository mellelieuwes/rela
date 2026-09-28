---
id: RR-T0SDKQ
type: review-response
title: Selection not gated by Profile
finding: Arithmetic and concatenation have Profile flags; selection is on in every profile without a stated reason.
severity: minor
resolution: 'doc.go explains why selection is not profile-gated: it adds no capability a boolean profile withholds, since the top level must still be bool.'
status: addressed
---
