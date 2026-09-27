---
id: RR-MVVTHG
type: review-response
title: 'S3: tail rule deleted prose after the @'
finding: Typing @ before an existing word or colon and pressing Enter replaced that word too.
severity: significant
resolution: ArmState tracks the end of the typed token (mapped with assoc 1); insertion stops there. Tests for a following word and colon in mentionArm.test.ts.
status: addressed
---
