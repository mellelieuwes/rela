---
id: RR-NFR8KN
type: review-response
title: 'Nit: Meta.Total reports the truncated count'
finding: A client cannot tell the list was cut.
severity: nit
reason: /_search has no paging; Total counts returned rows, and the only limit caller wants a fixed-size hint list.
status: wont-fix
---
