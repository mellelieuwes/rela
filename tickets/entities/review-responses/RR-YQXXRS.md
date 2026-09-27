---
id: RR-YQXXRS
type: review-response
title: 'M4: transient failures cached as hidden'
finding: The SPA load swallowed every error, so the starting list cached a network blip as null.
severity: minor
resolution: load returns null only for ApiError 404/403 and rethrows others.
status: addressed
---
