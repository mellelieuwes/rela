---
id: IMPL-VPQ9S2
type: implementation-checklist
title: 'Implementation: Hot-reload of data-entry.yaml should re-run ValidateConfig + script existence checks'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Development

- [x] Unit tests written for new code
- [x] Integration tests written (test full flow, not just units)
- [x] Happy path implemented
- [x] Edge cases from planning handled
- [x] Error handling in place (errors surfaced, not swallowed)

## Test Quality

- [x] Using fixture builders or factories for test data
- [x] No hardcoded values in assertions when object is in scope
- [x] Only specifying values that matter for the test
- [x] Interpolated values constructed from objects, not hardcoded
- [x] Property comparisons use original object, not hardcoded strings

## Manual Verification

- [x] Feature manually tested end-to-end
- [x] Each acceptance criterion verified with test scenario from planning
- [x] Edge cases manually verified

**Verification Evidence:**

Built `rela-server` and the SPA, then ran the server against a copy of the
`tickets` project with an SSE client attached (`curl -N` with an Origin header).

1. Appended an action pointing at a missing script to `data-entry.yaml`.
Server log: `WARN config reload rejected; keeping previous config error="invalid
data-entry.yaml: action \"bogus\": cannot access actions directory"`. SSE:
`event: config-error` with `{"error":"invalid data-entry.yaml: action \"bogus\":
...","file":"data-entry.yaml"}`. No `refresh` frame. `/api/v1/_schema` kept
answering 200.
2. Restored the file. Log: `config reloaded`. SSE: `event: refresh`.

Automated coverage per acceptance criterion:
- AC1/AC2: `TestReloadRejectedConfigKeepsPrevious` (unparsable YAML,
command+script document, missing action / document / export_render script). Each
asserts the error and that `Cfg()` is the same pointer as before.
- AC2 positive: `TestReloadAcceptsExistingScripts`.
- AC3: `TestReloadConfigBroadcasts` (accepted → refresh; rejected → config-error;
unreadable file → config-error with the fixed message and no host path).
- AC4: `TestReloadNormalizesCalendars`.
- AC5: `useEvents.test.ts` (error toast, no cache invalidation; malformed payload
still shows the toast).
- AC6: existing NewApp tests pass; startup error text is unchanged.

## Quality

- [x] Code follows project patterns (check similar code)
- [x] Checked for DRY opportunities — the startup pipeline is extracted once into
`loadConfig` and shared by `NewApp` and `reloadConfig`
- [x] No security issues introduced
- [x] No silent failures (errors logged AND returned)
- [x] No debug code left behind
