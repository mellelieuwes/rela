---
id: RR-ZQSQMY
type: review-response
title: Uploads unbounded after the write lock went
finding: writeMu used to serialize uploads, bounding temp storage and converter load. Without it any number of uploads can spool and wait at once.
severity: significant
resolution: attachment.Limiter admits DefaultMaxUploads (4) per process on HTTP and MCP; a full limiter answers 503 attachment_busy with Retry-After. Pinned by TestLimiter_AdmitsUpToCapacity and TestAttachmentUpload_FullUploadBudgetIs503.
status: addressed
---

## Finding

writeMu used to serialize uploads, bounding temp storage and converter load.
Without it any number of uploads can spool and wait at once.
