---
id: RR-H4S65K
type: review-response
title: Locked entity returns 409 instead of 422
finding: Locked entities have empty content so the anchor looks detached before PatchEntity runs.
severity: minor
resolution: Explicit IsLocked check after the raw read returns 422 encrypted_inaccessible. (implemented)
status: addressed
---
