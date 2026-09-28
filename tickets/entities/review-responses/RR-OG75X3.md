---
id: RR-OG75X3
type: review-response
title: Renumber partial failure leaves unaudited writes on fs/mem
finding: On fs/mem Tx has no rollback. If a renumber fails half way, the rewrites that landed are not audited because audit runs after commit.
severity: minor
reason: Auditing inside Tx is disallowed (no slow I/O in Tx) and fs/mem have no rollback. Renumber failures are logged; a partial renumber stays a valid order. Follow-up if it shows up in practice.
status: deferred
---

## Finding

On fs/mem Tx has no rollback. If a renumber fails half way, the rewrites that
landed are not audited because audit runs after commit.
