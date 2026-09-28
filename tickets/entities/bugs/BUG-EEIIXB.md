---
id: BUG-EEIIXB
type: bug
title: Relation-conferred type@face grant denies explicitly addressed faces at the row gate
description: A role conferred by a relation and granted only type@face fails PermitsRead for the entity, so getVisibleRef 404s an explicit ID@face address to the granted face on the entity and comment routes.
priority: medium
status: backlog
---

## Reproduction

Policy: role `viewer` with `read: [ticket@draft]`, conferred by the relation
`owned-by`. Alice owns TKT-1, which has a default face and a `draft` face.

- `PermitsRead(ctx, "ticket", "TKT-1")` returns false.
- So `GET /api/v1/entities/ticket/TKT-1@draft` and
`GET /api/v1/_comments/ticket/TKT-1@draft` both return 404, although the grant
names exactly that face.
- With `read: [ticket]` (every face), both return 200.

Found while writing the regression tests for BUG-R1PQY9.

## Suspected cause

`PermitsRead` is documented as face-blind, but for a relation-conferred role it
runs the composed `ReadQuery` through `store.MatchingIDs` in the default world.
Since the per-relation branches
(`TestReadQuery_ConferredRolesKeepTheirOwnFaces`), each branch carries its
role's faces, so the query matches the default-world row only when the role
grants the default face. `getVisibleRef` calls `PermitsRead` before it loads the
addressed face, so an explicit address to a granted non-default face is denied
at the row gate.

A global assignment is not affected: `ReadQuery` returns AllowAll and only the
face gate applies.
