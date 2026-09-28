---
id: BUGA-VFQCCA
type: bug-analysis-checklist
title: 'Analysis: TestQueryTracer_FromPoolEmits races the listener goroutine on its log buffer'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Reproduction

- [x] Bug reproduced locally
- [x] Minimal reproduction steps documented
- [x] Environment/conditions noted

## Root Cause

- [x] Immediate cause identified (why1)
- [x] Contributing factors found (why2-3)
- [x] Systemic cause explored (why4-5)

## Fix Planning

- [x] Fix approach determined
- [x] Regression test planned
- [x] Related areas checked for similar issues

## Notes

Not reproducible by repetition (0 of 100 local -race runs), because the log
handler's mutex orders the accesses whenever the listener logs before the test's
own query. Reproduced deterministically by delaying the listener's catch-up
300ms and the test's read 600ms (temporary, reverted): 3 of 3 runs report the
race; with the fix, 0 of 3.

Related areas: tracer_test.go also captures into a bytes.Buffer, but opens no
store, so no background goroutine writes to it.
