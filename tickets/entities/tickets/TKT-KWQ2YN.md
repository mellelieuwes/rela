---
id: TKT-KWQ2YN
type: ticket
title: Order search results by modification time in the store
kind: enhancement
status: backlog
---

## Description

`/_search?q=type:… sort:modified:desc&limit=N` reads every visible entity of the
named types and sorts them in Go before cutting to `limit`. The limit shrinks
the response, not the work. The `@` mention menu (TKT-39TIB4) asks this for its
starting list and caches the answer per scope for a minute to bound the cost.

Push `sort:modified` and the limit into `store.GraphQuery` where the backend can
order by `updated_at` (pgstore, sqlitestore), after the read gate as today, so a
principal still only fills slots with rows it may read.
