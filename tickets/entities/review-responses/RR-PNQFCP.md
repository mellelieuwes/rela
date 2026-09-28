---
id: RR-PNQFCP
type: review-response
title: Spurious 503 from a short lock wait
finding: lockWait was short enough that a normal large upload could make a second writer on the same property fail with 503.
severity: minor
resolution: lockWait raised to 60s; a caller deadline is reported as its own error, not ErrBusy (TestService_CallerDeadlineIsNotErrBusy).
status: addressed
---

## Finding

lockWait was short enough that a normal large upload could make a second writer
on the same property fail with 503.
