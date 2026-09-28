---
id: RR-GWU59O
type: review-response
title: Config read several times per operation
finding: The handler captured the Schema before enterWrite while the verdict re-read svc.schema() for the condition lookup. A reload while waiting for the lock let the gate judge a different config from the one the action came from.
severity: significant
resolution: detailActionCheck holds the snapshot the caller captured and compiles from it once. The handler refuses an entity-bound action with 409 config_reloaded when the published Schema changed while it waited for writeMu. TestDetailAction_ConfigReloadedIsRefused uses a pipe body to land the reload deterministically; mutation-verified.
status: addressed
---
