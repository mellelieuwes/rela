---
id: RR-HDXYCH
type: review-response
title: Denial audit subject has no face
finding: The OpDeniedWrite record for accept does not name the face.
severity: nit
reason: audit.Subject has no face field; the record matches the manager's recordDeniedWrite. Adding one is an audit-schema change outside this ticket.
status: wont-fix
---
