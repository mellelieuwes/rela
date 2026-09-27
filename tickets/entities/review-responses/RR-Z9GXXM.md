---
id: RR-Z9GXXM
type: review-response
title: Docs overstate what a reload applies
finding: docs/data-entry.md said a reload runs the same checks as startup. The git block, the OpenAPI title/description and pg derived indexes are still read only at startup, so an accepted reload silently does not apply them.
severity: minor
resolution: docs/data-entry.md now lists the restart-only settings.
status: addressed
---
