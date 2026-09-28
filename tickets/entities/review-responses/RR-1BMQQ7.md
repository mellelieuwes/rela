---
id: RR-1BMQQ7
type: review-response
title: permission check fails open under NopACL and ReadOnlyACL
finding: readGateFromContext returns nopReadGate under NopACL and ReadOnlyACL, whose HoldsPermission is true (RR-CWWJGW shape). The plan calls permission an intent gate like documents; that is acceptable because writes stay ACL-bounded, but the docs and godoc must say so explicitly, and a test should pin the chosen behaviour so nobody later relies on it as a boundary.
severity: minor
resolution: Plan documents permission as an intent gate and pins the NopACL/ReadOnlyACL behaviour with a test.
status: addressed
---
