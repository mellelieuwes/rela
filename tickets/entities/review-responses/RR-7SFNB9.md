---
id: RR-7SFNB9
type: review-response
title: Comments handler validates the id segment twice
finding: isSafeStateRefSegment is redundant beside parseEntityRef.
severity: nit
reason: The two answer different questions (path safety vs grammar); keeping the path-safety check is cheap defence in depth and matches export_document.go.
status: wont-fix
---
