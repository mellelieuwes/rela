---
id: RR-U1V9EM
type: review-response
title: Third copy of action toast and error handling
finding: EntityDetail duplicated the result toast and script-error handling from NextActionOffers and Sidebar.
severity: minor
resolution: Extracted composables/useActionFeedback (reportResult/reportError) and used it in all three components.
status: addressed
---
