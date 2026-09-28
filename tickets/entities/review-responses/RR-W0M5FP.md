---
id: RR-W0M5FP
type: review-response
title: Ticket example uses invalid when syntax
finding: 'The ticket example when: "kind = ''soa''" is not valid predicate syntax. The condition language is Lua-expression syntax: entity.kind == ''soa''. The docs example must use the correct form.'
severity: nit
resolution: Docs example uses entity.kind == 'soa'.
status: addressed
---
