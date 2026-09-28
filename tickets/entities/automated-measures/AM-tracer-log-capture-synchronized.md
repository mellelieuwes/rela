---
id: AM-tracer-log-capture-synchronized
type: automated-measure
title: Tracer log-capture test synchronizes with the store's background goroutines
description: TestQueryTracer_FromPoolEmits captures the default logger through a mutex-guarded buffer and runs under -race in the Postgres Backend CI job, so a background goroutine logging concurrently cannot race the assertion.
kind: test
location: internal/store/pgstore/tracer_pool_test.go
status: active
---
