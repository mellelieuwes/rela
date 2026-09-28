---
id: RR-Z4NKSY
type: review-response
title: Docs example builds ID@ on faceless types
finding: The regenerate example concatenated entity.face unconditionally; on a faceless type that writes to "ID@".
severity: minor
resolution: The example uses the bare id when entity.face is empty.
status: addressed
---
