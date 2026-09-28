---
id: RR-ZRZ7QX
type: review-response
title: _position disagrees with _search under a default world
finding: _position was not world-capable, so prev/next for a search hit ran in the default world and answered not_in_scope for every faced hit.
severity: significant
resolution: _position is admitted to worldCapablePath. TestSearch_PositionAgreesUnderTheDefaultWorld pins agreement.
status: addressed
---
