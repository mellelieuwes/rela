---
id: TKT-IMBOK
type: ticket
title: Hot-reload of data-entry.yaml should re-run ValidateConfig + script existence checks
kind: refactor
priority: low
effort: s
status: done
---

## Problem

`internal/dataentry/watcher.go:rebuildState` reloads `data-entry.yaml` after a
filesystem change but only performs YAML unmarshal. It does NOT re-run:

- `ValidateConfig` — so a mutually-exclusive `command:`+`script:` config would be accepted at runtime even though startup would have rejected it.
- `script.CheckActionScriptExists` for action scripts.
- `script.CheckDocumentScriptExists` for document scripts.

**Symptom**: a user editing `data-entry.yaml` to point at a missing or malformed
Lua file sees an HTTP 500 at first render instead of a server log entry
indicating the config is broken.

The guide text added in TKT-CGBVW says document scripts are "checked for
existence at startup" and the "Config hot-reload" note implies hot-reloads take
effect cleanly. The code doesn't match the guide.

## Scope

- Re-run `ValidateConfig` in `rebuildState`.
- Re-run existence checks for actions and documents.
- If validation fails, log the error and leave the previously-valid config in place (do NOT swap to a broken config — that would silently wedge the server).
- Emit an SSE `config-error` event so the frontend can surface the problem.

## Out of scope

- Changing the YAML schema.
- Reloading the metamodel (separate concern).

## Notes

Pre-existing issue (action scripts had the same gap before TKT-CGBVW). Fix all
three check families together.
