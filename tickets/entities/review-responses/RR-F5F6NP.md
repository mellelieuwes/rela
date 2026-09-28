---
id: RR-F5F6NP
type: review-response
title: Test plan gaps for the new concurrency model
finding: 'Missing: concurrent disjoint patches, patch vs renumber, patch vs cascade WriteEntity, pg plain write vs Tx read-merge-write, provisioning race both 2xx, attachment pool exhaustion, fs deadlock watchdog, sqlite busy_timeout.'
severity: significant
resolution: Added to the test plan; ID minting on pg asserted not to take RELW.
status: addressed
---

## Finding

Missing: concurrent disjoint patches, patch vs renumber, patch vs cascade
WriteEntity, pg plain write vs Tx read-merge-write, provisioning race both 2xx,
attachment pool exhaustion, fs deadlock watchdog, sqlite busy_timeout.
