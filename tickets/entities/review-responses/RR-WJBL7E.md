---
id: RR-WJBL7E
type: review-response
title: EXPLAIN coverage is inbound-only while the doc claims every id constraint
finding: The new EXPLAIN tests cover only the inbound hop; but the doc said the relation's own key serves every id constraint.
severity: minor
resolution: Narrowed the doc sentence to say an id constraint derives no index and the store looks the named entity up by its key. Outbound Endpoints plans were already pinned by the existing closure EXPLAIN tests.
status: addressed
---
