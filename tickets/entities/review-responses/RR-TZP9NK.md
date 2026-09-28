---
id: RR-TZP9NK
type: review-response
title: MCP handlers read the snapshot more than once
finding: s.deps() and group(s, selTypes) each loaded the snapshot, so a reload between them mixed metamodels.
severity: minor
resolution: Each handler loads the snapshot once and reads deps and the type resolver from it.
status: addressed
---
