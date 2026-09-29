---
id: BUGA-0GAFTC
type: bug-analysis-checklist
title: 'Analysis: ID gap analysis enumerates manual ids with dashes and lists gaps without bound'
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

**Reproduction:** Reproduced with 31 manual Basecamp-style ids
(`tw-basecamp-<id>`): `rela analyze all` took 74 s enumerating ~20 million ids.
After the fix a sequence with 30 million missing numbers reports in 0.25 s (100
listed, 29,999,960 counted).
