---
id: RR-6UQGYS
type: review-response
title: Host-path test can hang
finding: TestReloadConfigErrorOmitsHostPath read the channel with a bare receive.
severity: nit
resolution: Uses select/default like TestReloadConfigBroadcasts.
status: addressed
---
