---
id: BUG-1FFUSO
type: bug
title: TestQueryTracer_FromPoolEmits races the listener goroutine on its log buffer
description: The test reads an unsynchronized bytes.Buffer that the store's background listener also writes to via the Debug query tracer, so the race detector fails the Postgres Backend CI job intermittently.
priority: medium
effort: xs
why1: The test reads buf.String() on a plain bytes.Buffer while the store's listener goroutine writes to the same buffer through the Debug query tracer, with no synchronization between the two.
why2: The test swaps the process-wide default slog logger, and pgstore.Open starts a listener goroutine whose startup queries (primeWatermark, catchUp) log through that default logger.
why3: 'The handler''s internal mutex usually orders the accesses: the listener logs first and the test''s own query then locks the same mutex. The race only appears when the listener''s catch-up runs after the test''s query, which happens on a slow CI runner.'
why4: The test was written when it was the only goroutine expected to log, and passes reliably on fast machines, so the unsynchronized read was never exposed locally.
why5: Tests that capture a process-global sink (the default logger) implicitly assume no background goroutine shares it. Nothing makes that assumption visible when a store starts background work.
prevention: 'The capture buffer is now mutex-guarded (lockedBuffer), so any goroutine logging through the swapped default logger is synchronized with the test''s read. Verified by forcing the late catch-up with a temporary delay: the race reproduces on the old test and not on the fixed one. The race-enabled Postgres CI job keeps running the test (AM-tracer-log-capture-synchronized).'
status: done
---

## Description

`TestQueryTracer_FromPoolEmits` (internal/store/pgstore/tracer_pool_test.go)
fails intermittently under `-race` in the Postgres Backend CI job with "race
detected during execution of test".

The test points the default slog logger at a plain `bytes.Buffer`, opens a
store, and reads `buf.String()`. The store's change-feed listener goroutine runs
a catch-up query on start, and the query tracer logs it at Debug level into the
same buffer. The read and the write are unsynchronized.

## Evidence

CI run 36226309420 (PR #1679): write in `queryTracer.TraceQueryEnd` from
`(*listener).catchUp` (listener.go:314); previous read in
`TestQueryTracer_FromPoolEmits` (tracer_pool_test.go:33).
