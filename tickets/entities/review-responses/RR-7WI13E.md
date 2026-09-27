---
id: RR-7WI13E
type: review-response
title: Update rights checked only after the comment is resolved
finding: A caller the manager will refuse still resolves and reopens the comment, racing concurrent comment edits and leaving it resolved if the reopen fails.
severity: minor
resolution: commentAccept pre-checks the update write via translateVerb before resolving and audits the denial like the attachment handler. The manager stays the authority. Pinned by a counting comment store in TestCommentSuggestion_AcceptAuthorization (mutation-checked).
status: addressed
---
