---
id: RR-LCM0GV
type: review-response
title: Gantt normalization on reload untested
finding: TestReloadNormalizesCalendars covers calendars only.
severity: nit
reason: NormalizeGantts is called on the same line of the shared loadConfig pipeline as NormalizeCalendars and has its own unit tests in dataentryconfig; a second reload test adds little.
status: wont-fix
---
