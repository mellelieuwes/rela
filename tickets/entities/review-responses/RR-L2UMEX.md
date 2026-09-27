---
id: RR-L2UMEX
type: review-response
title: 'M10: test gaps'
finding: No useEditorMention tests; spec 1.4/1.5/8.2/8.3/6.3/6.4 uncovered; whenSettled on close/error; Go ordering tests rely on distinct timestamps.
severity: minor
resolution: Added useEditorMention.test.ts and arm tests on a real Milkdown editor for code span, paste, blur, undo, punctuation and tail, plus whenSettled close/error tests. Go timestamps are stamped by the store with time.Now and cannot be set through its API; left as is.
status: addressed
---
