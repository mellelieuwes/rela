---
id: RR-K1QQL0
type: review-response
title: lock.For fallback untested on postgres
finding: Nothing proved lock.For returns a BackendLocker for pgstore, so a wiring mistake would silently fall back to the in-process locker.
severity: minor
resolution: TestKeyedLock_HoldersCannotStarveThePool asserts lock.For(pgstore) is a *lock.BackendLocker. MCP validates Uploads at startup.
status: addressed
---

## Finding

Nothing proved lock.For returns a BackendLocker for pgstore, so a wiring mistake
would silently fall back to the in-process locker.
