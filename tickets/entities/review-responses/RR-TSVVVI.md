---
id: RR-TSVVVI
type: review-response
title: Accept ignores a failed autosave flush
finding: The SPA called commitImmediately but did not check the result. A pending edit that failed to save was then overwritten by the reload after accept.
severity: significant
resolution: acceptSuggestion aborts with an error toast when the flush is unsettled or errored. EntityDetail.accept.test.ts covers both cases and was mutation-checked.
status: addressed
---
