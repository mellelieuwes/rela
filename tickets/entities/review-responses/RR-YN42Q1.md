---
id: RR-YN42Q1
type: review-response
title: Detail page must post the served face address
finding: The affordance is evaluated against the entity at the served face, which may come from a world fallback. The SPA must POST that explicit ID@face address, as EntityDetail already does for commands (BUG-G2BASF), or the gate evaluates a different row than the one that produced the button.
severity: minor
resolution: Plan posts the explicit ID@face address like commands.
status: addressed
---
