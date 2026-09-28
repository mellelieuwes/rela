---
id: RR-55G8DT
type: review-response
title: Unconditional PatchEntity loses disjoint updates without writeMu
finding: 'PatchEntity reads the raw row, merges and writes the whole row unconditionally (manager.go:1079, updateCore UpdateEntityIf with empty condition). Two concurrent patches naming different properties: both read S0, the second write erases the first. The godoc claim that an unconditional patch only loses same-property updates held only under writeMu. Callers without ExpectedVersion: Lua update, attachment stamp, webhook set, MCP, provisioning.'
severity: critical
resolution: PatchEntity without a caller ExpectedVersion now pins the write to VersionOf(stored) and retries the whole read-authorize-merge-updateCore sequence a bounded number of times on VersionConflictError. A caller-supplied ExpectedVersion is not retried. Godoc corrected; concurrent disjoint-patch test on every backend.
status: addressed
---

## Finding

PatchEntity reads the raw row, merges and writes the whole row unconditionally
(manager.go:1079, updateCore UpdateEntityIf with empty condition). Two
concurrent patches naming different properties: both read S0, the second write
erases the first. The godoc claim that an unconditional patch only loses
same-property updates held only under writeMu. Callers without ExpectedVersion:
Lua update, attachment stamp, webhook set, MCP, provisioning.
