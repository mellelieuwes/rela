---
id: RR-ZYUXEH
type: review-response
title: Concurrency test did not discriminate
finding: A test passed with or without the fix, so it did not pin the behaviour.
severity: significant
resolution: Rewritten so it fails without the fix; the other new tests were checked the same way.
status: addressed
---

## Finding

A test passed with or without the fix, so it did not pin the behaviour.
