---
id: RR-Y13XDM
type: review-response
title: 'Nit: limit not validated when q is empty'
finding: An invalid limit passed silently with an empty query.
severity: nit
resolution: limit is validated before the empty-query early return; test covers both.
status: addressed
---
