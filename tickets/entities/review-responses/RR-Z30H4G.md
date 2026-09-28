---
id: RR-Z30H4G
type: review-response
title: Actions now run concurrently with the default Lua timeout
finding: Actions previously ran under writeMu. They now run concurrently and use the default 30s Lua timeout, so more CPU can be spent at once.
severity: minor
reason: The user chose the default Lua timeout. Concurrency is the goal of the ticket; docs/data-entry.md states the new behaviour.
status: wont-fix
---

## Finding

Actions previously ran under writeMu. They now run concurrently and use the
default 30s Lua timeout, so more CPU can be spent at once.
