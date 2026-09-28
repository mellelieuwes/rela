---
id: RR-NGIEK5
type: review-response
title: pgx Tx view cannot interleave queries
finding: A pgx.Tx connection cannot run a query while a rows iterator is open. Code that queries inside a ListRelations loop fails on a Tx view.
severity: minor
resolution: Callback code drains iterators before issuing further queries; noted in the tx helper godoc and covered by the pg tests.
status: addressed
---

## Finding

A pgx.Tx connection cannot run a query while a rows iterator is open. Code that
queries inside a ListRelations loop fails on a Tx view.
