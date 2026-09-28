---
id: TKT-WE0S2K
type: ticket
title: Remove the global data-entry write lock (writeMu)
kind: refactor
priority: medium
effort: l
status: done
---

## Description

Remove dataentry's process-wide `writeMu` (and the MCP attachment `WriteLock` /
`attachMu` that shares its role).

`writeMu` serializes every data-entry mutation in one process: CRUD, sync,
attachments, webhooks, and Lua actions for their whole run. It causes two
problems:

- It protects nothing in a multi-process deployment. Several `rela-server`
processes against one PostgreSQL database share no mutex.
- It causes timeouts. Unrelated writes queue behind one lock, and a slow Lua
action or webhook scan stalls every other writer.

The few places that still rely on it for correctness move to mechanisms that
work across processes: store-level compare-and-swap (`ExpectedVersion`) and the
keyed `lock.Locker` seam (TKT-1K47YD), which was built for this.
