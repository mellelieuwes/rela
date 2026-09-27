---
id: RR-55ZOIJ
type: review-response
title: Concurrency test can pass vacuously
finding: TestConcurrentReadDuringOnReload ignored reloadConfig errors; if the fixture stopped validating, nothing would publish and the test would test nothing.
severity: minor
resolution: The reloader loop now fails the test on a non-nil error.
status: addressed
---
