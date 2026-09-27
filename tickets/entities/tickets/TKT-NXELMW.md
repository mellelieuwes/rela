---
id: TKT-NXELMW
type: ticket
title: related() constraints match the final entity id and current_user.id
kind: enhancement
priority: medium
effort: m
status: done
---

## Description

`related(entity, path, constraints)` constraints compare a property of the final
entity with a non-empty string literal only. A query scope therefore cannot say
"tasks I am responsible for" when ownership is a relation rather than a
property. Motivating case (atlas): relation `verantwoordelijk_voor` from
`persoon` to `taak`, inverse `heeft_verantwoordelijke`, with `user_entity_type:
persoon`, so `current_user.id` is the persoon entity id.

Target scope on `taak`:

mijn: "entity.status ~= 'gereed' and related(entity, 'heeft_verantwoordelijke',
{ id = current_user.id })"

## Scope

- Accept `id` as a constraint key: the final entity's id.
- Accept `current_user.id` as a constraint value, for `id` and for a
string-shaped property.
- The traversal stays a compile-time form; the program records that it reads
`current_user`, so the "Identity in a scope" rules apply.
- `id` lowers to `RelationPredicate.Endpoints` on the final hop. An empty or
unresolved identity fails closed; it never lowers to an empty Endpoints set.
- Keep reader-visibility gating, the 422 cases, exact-scope pushdown and
derived-index derivation (an `id` constraint derives no property index).

## Acceptance criteria

- A `mijn`-style scope returns only the current user's rows on every backend.
- An anonymous or unmapped principal gets the identity error, never rows.
- Docs describe the `id` key and `current_user.id` values.
