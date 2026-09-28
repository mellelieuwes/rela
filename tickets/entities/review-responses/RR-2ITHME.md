---
id: RR-2ITHME
type: review-response
title: Other surfaces referencing an available_on action are undefined
finding: Lists (lists.*.actions), navigation entries (action:) and next-action offers (action:) can name an action. With available_on the handler requires a readable entity at an explicit address. Sidebar calls have no entity and would always 404; list rows and offers send a bare id, which has no row on a faced type. The plan only makes nav references a load error. Make every such reference to an action with available_on a load error so a config cannot half-work.
severity: significant
resolution: Plan makes list, nav and next-action references to an action with available_on a load error.
status: addressed
---
