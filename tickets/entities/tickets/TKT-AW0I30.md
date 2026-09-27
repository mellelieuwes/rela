---
id: TKT-AW0I30
type: ticket
title: '@ menu: offer a Create type row when nothing matches'
kind: enhancement
priority: low
status: backlog
---

## Description

Follow-up to TKT-39TIB4. When an `@` query finds no matches, or when it is
scoped to a type (`@ticket:login-bug`), offer a row such as "Create ticket
'login-bug'". Choosing it creates the entity (through the inline-create form, so
required fields and ACL apply) and inserts a reference to it.

Open questions: which types to offer when unscoped, how required properties are
collected, and whether the row respects the create permission of the current
principal.
