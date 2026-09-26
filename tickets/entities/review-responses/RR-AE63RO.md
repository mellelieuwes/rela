---
id: RR-AE63RO
type: review-response
title: visibility.Readable and fallbacks lack unit tests
finding: No tests for Readable's three branches (default, ID@face, faced any-face), gatedSearcher DenySearcher fallbacks, or NopACL GetRelation on a dangling edge.
severity: minor
resolution: TestReadable covers the default row, a faced type by bare id and by id@face, an undeclared face, a missing id and a read error. The DenySearcher fallback is unreachable after NewDeclarativeGate succeeds on a non-nil policy.
status: addressed
---
