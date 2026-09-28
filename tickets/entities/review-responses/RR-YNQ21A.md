---
id: RR-YNQ21A
type: review-response
title: Store events inside Tx are safe
finding: fs emit is non-blocking and observers do not call back into the store; pg/sqlite buffer events until commit.
severity: nit
reason: No change needed.
status: wont-fix
---

## Finding

fs emit is non-blocking and observers do not call back into the store; pg/sqlite
buffer events until commit.
