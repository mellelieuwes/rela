---
id: AM-search-resolves-through-world
type: automated-measure
title: 'Test: /_search resolves through the request world and honors face grants'
description: Pins that /api/v1/_search and /_position use the configured or explicit world, a denied world finds nothing, and a type@face grant narrows faces before the world ranks. Catches a search route that serves the default world or ignores face grants (BUG-SMPOZB).
kind: test
location: internal/dataentry/searchworld_test.go
status: active
---
