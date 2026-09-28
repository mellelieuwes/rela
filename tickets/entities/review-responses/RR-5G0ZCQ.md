---
id: RR-5G0ZCQ
type: review-response
title: Attachment keyed lock pins pg connections per waiter and may hold the lock during upload
finding: AcquireKeyedLock takes a pool connection then blocks, so N waiters pin N connections while the holder needs more. With the no-op processor the reader is still the request body, so a slow client holds the lock.
severity: significant
resolution: lock.BackendLocker takes an in-process keyed mutex before the backend lock (one pg connection per key per process). The upload is spooled to a temp file before acquiring. Acquire uses an explicit deadline.
status: addressed
---

## Finding

AcquireKeyedLock takes a pool connection then blocks, so N waiters pin N
connections while the holder needs more. With the no-op processor the reader is
still the request body, so a slow client holds the lock.
