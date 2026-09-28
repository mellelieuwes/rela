---
id: RR-NFDXHQ
type: review-response
title: Unreachable permission branch in the gate
finding: resolveDetailActionEntity had a coverage-ignored detailActionNoPermission branch that duplicated the handler's permission check.
severity: minor
resolution: The verdict enum became a single Allows bool; the handler's permission check stays the only place that names the permission.
status: addressed
---
