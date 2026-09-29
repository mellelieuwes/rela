---
id: BUGA-MP15JT
type: bug-analysis-checklist
title: 'Analysis: rela rename id fails with ''principal is unstamped'' in any project with an acl.yaml'
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

**Reproduction:** Reproduced on a copy of an operator project with acl.yaml:
`rela rename id SC-001 SC-100` failed with 'principal is unstamped'; after the
fix it renamed with 4 relations updated.
