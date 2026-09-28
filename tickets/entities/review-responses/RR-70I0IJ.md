---
id: RR-70I0IJ
type: review-response
title: Cascade ErrConflict comment was wrong
finding: The comment on WriteRelation ErrConflict described only one cause.
severity: minor
resolution: Comment now names both causes (concurrent writer or re-trigger). The no-op behaviour is intended.
status: addressed
---

## Finding

The comment on WriteRelation ErrConflict described only one cause.
