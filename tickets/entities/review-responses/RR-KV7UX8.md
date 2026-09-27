---
id: RR-KV7UX8
type: review-response
title: 'Code review: Refuse pull/pushed on a gone twin; today it revives and can write first'
finding: 'Neither `Pull` (service.go:342ff) nor `Pushed` (430ff) checks `tw.Live()`. After `rela twin gone` (the external item vanished) with the entity still present, a pull or push moves the twin back to pending, in_sync or conflict, and it owns fields again. The docs (GUIDE-twins.md gone section: ''stays on the pending list until … unlink'') do not describe this. Worse: if the entity was meanwhile linked to a new item in the same system, which the docs allow, `Pull` first writes the old item''s theirs values to the entity through the sync handle (service.go:371). Only then does `store.Update` fail with ErrDuplicateTarget (file.go checkDuplicate), so the entity was changed and the command reports an error. Fix: return a new `ErrGone` from Pull and Pushed when `!tw.Live()`, before any read or write, and map it in twinError to ''<key> is gone; unlink it first''. Alternatively, document revival and run the duplicate-target check before the entity write.'
severity: minor
resolution: Pull and Pushed return ErrGone for a gone twin before reading or writing anything; the CLI says '<system>/<id> is gone; unlink it first'.
status: addressed
---
