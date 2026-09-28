---
id: RR-L5MYV6
type: review-response
title: Locked log buffer helper duplicated
finding: Third copy of a locked slog writer (appbuild, dataentry). A shared testutil helper could replace all three.
severity: nit
reason: Out of scope for a test race fix; a local type is sufficient.
status: deferred
---
