---
id: RR-NT970V
type: review-response
title: Keyed-lock holders can exhaust the pg pool
finding: Each held pg keyed lock pins a pool connection. Enough concurrent attachment writers take every connection, and their own store writes then wait for a connection that only frees on release (self-deadlock until ctx expiry).
severity: critical
resolution: pgstore.AcquireKeyedLock now takes one of max(1, MaxConns/2) slots before pinning a connection, so holders always leave connections for their writes. Pinned by the postgres TestKeyedLock_HoldersCannotStarveThePool.
status: addressed
---

## Finding

Each held pg keyed lock pins a pool connection. Enough concurrent attachment
writers take every connection, and their own store writes then wait for a
connection that only frees on release (self-deadlock until ctx expiry).
