---
id: RR-EU0IHG
type: review-response
title: 'S1: app editor hid rows while Enter still picked them'
finding: Scheduling a search set loading=true in the shared machine; the app editor then showed only 'Searching…' while Enter picked a stale, hidden row.
severity: significant
resolution: Reverted loading-at-schedule in mentionMenuState.ts. The SPA reads a reactive pending flag instead, so the app editor behaves as before.
status: addressed
---
