---
id: PLAN-EOV13R
type: planning-checklist
title: 'Planning: Hot-reload of data-entry.yaml should re-run ValidateConfig + script existence checks'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Understanding

- [x] Problem/requirements clearly understood
- [x] Scope defined (what's in/out documented below)
- [x] Acceptance criteria documented with specific test scenarios

**Scope:**

In scope: `App.rebuildState` (internal/dataentry/watcher.go) runs the same load
pipeline as `NewApp`: deprecated-syntax detection, unmarshal, `ValidateConfig`,
calendar/gantt normalization, config warnings, action / document / export_render
script existence checks. On failure the previous config stays published, the
error is logged, and an SSE `config-error` event replaces the `refresh` event.
The SPA shows the error as a toast.

Out of scope: YAML schema changes, metamodel reload, derived-index reconcile.

**Acceptance Criteria:**
1. A reload whose config fails `ValidateConfig` (e.g. a document with both
`command:` and `script:`) keeps the previous config and logs the error.
2. A reload pointing an action, document, or export_render at a missing
script keeps the previous config and logs the error.
3. A failed reload broadcasts `config-error` (not `refresh`); a successful
reload still broadcasts `refresh`.
4. A successful reload applies calendar/gantt normalization (today it skips it).
5. The SPA shows an error toast on `config-error`.
6. Startup behaviour is unchanged (same checks, same error messages).

## Research

- [x] ~~For larger features: run `/research` to create a structured research doc~~ (N/A: small refactor)
- [x] ~~Searched for existing libraries that solve this problem~~ (N/A: internal pipeline reuse)
- [x] Checked codebase for similar patterns or reusable code
- [x] ~~Looked for reference implementations in other projects~~ (N/A: internal pipeline reuse)
- [x] Reviewed relevant rela concepts for prior art

**Research Doc:** N/A

**Existing Solutions:** The full pipeline already exists inline in `NewApp`
(internal/dataentry/app.go ~L910-L990) plus `checkExportRenderScripts`. The
palette reload in `rebuildState` already follows "keep previous on failure".

## Approach

- [x] Technical approach chosen and documented
- [x] Approach builds on existing patterns (not reinventing)
- [x] Alternatives considered (document why rejected)
- [x] Dependencies identified (packages, APIs, types)

**Technical Approach:**
1. Extract `loadConfig(data []byte, meta, root string) (*Config, error)` in
app.go holding the pipeline from `NewApp`. `NewApp` calls it after
`cfgLoader.Load`; error text unchanged.
2. `rebuildState` returns an error. On config change it calls `loadConfig`;
on error it returns without publishing (previous `Schema` stays).
3. The `StartWatching` subscriber broadcasts `refresh` on success and a
`config-error` frame (`{"file":"data-entry.yaml","error":"..."}`) on failure. A
read error sends a fixed message, since loader errors may hold host paths;
validation/script errors are config-derived (config is not a secret; script
errors already omit system paths).
4. Frontend `useEvents.ts`: add `config-error` type; listener calls
`useUIStore().showError(...)`.

Alternative rejected: validating in the subscriber and passing a parsed config
into `rebuildState` (two places would own the "keep previous" decision).

**Files to modify:** internal/dataentry/app.go, internal/dataentry/watcher.go,
internal/dataentry/watcher_test.go,
frontend/src/composables/useEvents.ts(+test), docs/data-entry.md.

## Security Considerations

- [x] Input sources identified (user input, config, external APIs)
- [x] Input validation approach defined (allowlist preferred over blocklist)
- [x] Security-sensitive operations identified (file access, auth, crypto)
- [x] Error handling doesn't leak sensitive information

**Input Sources & Validation:** Operator-authored `data-entry.yaml` and scripts;
validated by the existing startup validators, which are now applied on reload
too.

**Security-Sensitive Operations:** Script file access goes through the existing
`os.OpenRoot` loaders. The SSE `config-error` payload carries config-derived
text only (not a secret per CLAUDE.md); loader I/O errors are replaced with a
fixed message so host paths stay off the wire. The frontend renders the toast as
text, not HTML.

## Test Plan

- [x] Test scenarios documented for each acceptance criterion
- [x] Edge cases identified and documented
- [x] Negative test cases defined (invalid input, error conditions)
- [x] Integration test approach defined (not just unit tests)

**Test Scenarios:**
- AC1/AC2: table-driven Go test on `rebuildState` with invalid configs
(command+script document, missing action script, missing document script,
missing export_render, unparsable YAML, deprecated syntax): returns error,
`Cfg()` still the original.
- AC3: test the subscriber callback helper emits `config-error` vs `refresh`
via a broker subscription.
- AC4: reload a config with a calendar lacking default_view; normalized value present.
- AC5: vitest in useEvents.test.ts emitting `config-error` calls showError.
- AC6: existing NewApp tests pass unchanged.

**Edge Cases:** Empty file (valid YAML, fails validation → keep previous); file
removed (Load error → keep previous, fixed message); a fix after a failure (next
reload succeeds and broadcasts refresh).

**Negative Tests:** covered by the table above.

## Risk Assessment

- [x] Technical risks assessed with mitigations
- [x] Security risks assessed (see Security Considerations)
- [x] Effort estimated (xs/s/m/l/xl)

**Risks:** Configs accepted by today's lenient reload but rejected by validation
will no longer hot-apply. That is the intended behaviour; startup already
rejects them. Effort: s.

## Documentation Planning

- [x] User-facing docs identified (skip if internal refactor)
- [x] ~~Docs-checklist will be created when entering implementation~~ (N/A: kind=refactor)

**Documentation Impact:**
- [x] docs/data-entry.md - "Config hot-reload" section: reload re-validates;
failure keeps the previous config and shows an error.

## Design Review

- [x] Run `/design-review` before starting implementation
- [x] All critical/significant findings addressed in plan

**Design Review Findings:**  (minor, deferred). No critical or significant
findings.
