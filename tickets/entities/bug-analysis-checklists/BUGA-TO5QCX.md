---
id: BUGA-TO5QCX
type: bug-analysis-checklist
title: 'Analysis: Milkdown''s orphan timer fails the Frontend CI job after test teardown'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Reproduction

- [x] Bug reproduced locally (with a stand-in test: the real `@milkdown/ctx` `Timer`, a 10 ms timeout and `globalThis.removeEventListener` deleted gives the exact CI error; the real editor does not reproduce locally because it depends on CI worker shutdown timing)
- [x] Minimal reproduction steps documented (Verification section of BUG-7R27T6)
- [x] Environment/conditions noted (vitest + happy-dom; a test file that mounts the editor and finishes within the 3000 ms default timeout)

## Root Cause

- [x] Immediate cause identified (why1)
- [x] Contributing factors found (why2-3)
- [x] Systemic cause explored (why4-5)

## Fix Planning

- [x] Fix approach determined (narrow `test.onUnhandledError` filter in `frontend/vitest.config.ts`; Milkdown's Timer cannot be patched from rela)
- [x] ~~Regression test planned~~ (N/A: the symptom depends on CI worker shutdown timing and does not reproduce with the real editor; the filter was verified with an uncommitted stand-in test, and the guard is the filter's own precision, AM-vitest-teardown-filter-is-narrow)
- [x] Related areas checked for similar issues (the other `setTimeout` calls in `@milkdown/core`, `plugin-block` and `preset-commonmark` use 0-50 ms delays and do not call bare globals; the filter is not tied to one test file, so it covers every file that mounts the editor)
