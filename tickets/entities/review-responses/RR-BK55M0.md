---
id: RR-BK55M0
type: review-response
title: 'M7: IME composition not handled'
finding: Enter during composition committed a row.
severity: minor
resolution: Keymap returns early on event.isComposing (tested). Arming via handleTextInput on Android composition input is not verified.
status: addressed
---
