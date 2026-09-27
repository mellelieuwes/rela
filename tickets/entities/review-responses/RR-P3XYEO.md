---
id: RR-P3XYEO
type: review-response
title: delete_entity/rename_entity report counts over hidden relations
finding: delete_entity uses ungated CountRelations and rename_entity reports RelationsUpdated over hidden edges, revealing how many hidden neighbours exist.
severity: minor
resolution: Fixed by TKT-4QSZ8Y (#1683), where delete_entity and rename_entity count only visible relations.
status: addressed
---
