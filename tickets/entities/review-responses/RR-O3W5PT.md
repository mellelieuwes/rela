---
id: RR-O3W5PT
type: review-response
title: 'S2: insertion range came from a stale match width'
finding: commit used activeMatchLength against the current cursor; a keystroke landing while Enter waited could misplace the reference.
severity: significant
resolution: Range now comes from armedToken(view.state); commit refuses when the token query differs from the menu query. activeMatchLength removed. Pinned by useEditorMention.test.ts.
status: addressed
---
