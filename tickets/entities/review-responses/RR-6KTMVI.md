---
id: RR-6KTMVI
type: review-response
title: Browser and/or inside a comparison changed boolean results
finding: conditions.ts returns raw operands from and/or; as an operand of == the value is often NIL where it used to be a bool. (form.a or form.b) == false with both unset flipped from true to false; (form.a or form.b) == true with a='yes' flipped to false. conditionlint accepts these bool-typed expressions.
severity: significant
resolution: 'conditions.ts now marks selecting logical nodes syntactically at parse (markSelections): a node selects when a value operand is a string/number literal or a selecting node, propagated into value operands. Non-selecting nodes use the original bool-yielding code unchanged, so every previously valid condition evaluates as before, including inside comparisons. Vitest cases for all four reported expressions. Limitation (reference-only selections need a literal default) documented in conditions.ts and data-entry.md.'
status: addressed
---
