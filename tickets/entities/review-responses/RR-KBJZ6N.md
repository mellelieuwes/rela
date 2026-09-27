---
id: RR-KBJZ6N
type: review-response
title: 'S4: @ inside inline code opened the menu'
finding: inlineCode is a mark, so the backtick terminator never saw it.
severity: significant
resolution: handleTextInput refuses to arm when the parent node or the active marks are code. Test in mentionArm.test.ts.
status: addressed
---
