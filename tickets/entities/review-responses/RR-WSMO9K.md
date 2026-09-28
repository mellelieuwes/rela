---
id: RR-WSMO9K
type: review-response
title: IdP webhook ignores permission and available_on
finding: dispatchWebhookAction ran the configured action with no permission or available_on check, so an entity-bound action ran with entity nil and the docs' claim that permission applies on every surface was false.
severity: significant
resolution: The webhook refuses an action with available_on (errWebhookActionEntityBound; TestWebhook_RefusesEntityBoundAction). permission does not apply there because the webhook runs as the webhook-receiver principal, not a user; the docs now say so.
status: addressed
---
