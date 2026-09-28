---
id: RR-FCQZSR
type: review-response
title: Multi-process TOCTOU between check and script
finding: writeMu is per process; with several rela-server nodes on one postgres database another node can change the entity between the gate and the script.
severity: minor
resolution: Documented in docs/data-entry.md as a single-process guarantee; the script's writes stay bounded by the caller's ACL. A cross-process guarantee would need the check and the script inside one store Tx.
status: addressed
---
