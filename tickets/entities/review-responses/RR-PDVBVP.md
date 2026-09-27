---
id: RR-PDVBVP
type: review-response
title: 'M6: word boundary was an allowlist'
finding: Quotes and slashes before @ did not open the menu.
severity: minor
resolution: Denylist of word characters and backtick (triggersAfter), shared by the parser and arming. Tests in mentionQuery.test.ts and mentionArm.test.ts.
status: addressed
---
