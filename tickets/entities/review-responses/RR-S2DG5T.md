---
id: RR-S2DG5T
type: review-response
title: lockedBuffer doc understates writers
finding: Doc said one writer goroutine; there are two (test and listener).
severity: nit
resolution: Reworded to 'safe for concurrent writes and reads'.
status: addressed
---
