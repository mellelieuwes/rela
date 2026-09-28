---
id: BUG-SMPOZB
type: bug
title: Web search misses faced entities under app.default_world
description: /api/v1/_search ignores app.default_world and the next-action source_world for free-text queries, so a faced entity with no default-face row is never found.
priority: high
effort: s
why1: /api/v1/_search runs every read in the default world. A faced entity with no default-face row is not in that world, so search cannot find it.
why2: attachWorld applies app.default_world only on routes worldCapablePath admits, and _search was refused. The free-text branch of executeQuery also built its search.Query without a World.
why3: worldCapablePath is a deny-by-default allowlist. _search was left out because the cross-type search had not been scoped and tested end to end, although the per-type list search had been.
why4: The world arc scoped routes one at a time, and the search used by the command palette and entity picker was never scheduled. The allowlist test recorded the refusal as intended rather than as open work.
why5: A read surface that is not world-capable silently serves the default world instead of failing, so the gap looks like missing data rather than an error.
prevention: Admit _search to worldCapablePath and stamp the ctx world on every executeQuery branch, with a denied-world guard. searchworld_test.go pins the default, explicit and denied worlds through the real router.
status: done
---

## Description

On a deployment with worlds and `app.default_world`, the SPA search
(`/api/v1/_search`, used by the command palette, the search page and the entity
picker) does not find faced entities that have no default-face row. The entity
list for the same type does show them.

## Reproduction

1. A `policy` type with faces `concept` and `adopted`; a world selecting `[adopted, concept]`; `app.default_world` set to it.
2. POL-001 has only an adopted face.
3. `GET /api/v1/_search?q=<title of POL-001>` returns other entities but not POL-001.

Verified on atlas: searching a beleid title finds nothing.
