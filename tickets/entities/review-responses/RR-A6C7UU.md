---
id: RR-A6C7UU
type: review-response
title: Lua write gate applies to all writer runtimes
finding: 'gateWriteTarget runs for actions, automations and scheduled jobs too: error text changes, extra reads per write, DenyReader blocks writes. Record the scope.'
severity: minor
resolution: 'Intended: every writer runtime now matches the data-entry write path, which also pre-reads targets through the gated reader. The cost is one gated read before and one after each write. A DenyReader runtime refusing writes is fail-closed by design. Recorded here.'
status: addressed
---
