---
id: BUG-R1PQY9
type: bug
title: Comments on a non-default face 404 on database backends and resolve text anchors against the wrong face
description: 'The comments handler hands the raw `ID@face` path segment to visibleReader.getVisible, a bare-id reader, instead of parsing it with parseEntityRef and reading through getVisibleRef. Effects: (1) on postgres and sqlite GetEntity(''TKT-1@draft'') matches no row, so every comment thread on a non-default face returns 404; (2) under a relation-conferred (query-shaped) read grant the row gate receives the suffixed id and matches nothing, so faced threads 404 on every backend; (3) gateCommentTarget rewrites the target to bare id + face, and buildTextAnchor/liveAnchors then call getVisible with the bare id, so text anchors on a non-default face are built and resolved against the DEFAULT face''s body (400 on create, wrong or detached ranges on read); (4) entitymanager.DeleteEntityFace never notifies the comment service, so a deleted face''s thread survives and reappears if that face is recreated.'
priority: high
why1: The comments handler passed the raw `ID@face` path segment to visibleReader.getVisible, which takes a bare id. On pgstore/sqlitestore the lookup is (id='TKT-1@draft', face='') and matches nothing; under a query-shaped read grant the row gate matches nothing either. After the gate, liveAnchors and buildTextAnchor re-read the entity by the now-bare id, which resolves the default face's body. DeleteEntityFace has no comment notification at all.
why2: 'The comments feature (TKT-FIO205, #1528, merged 2026-09-07) added faces to comments by accepting `@` in the path segment (isSafeStateRefSegment) and trusting getVisible to resolve it. getVisibleRef and parseEntityRef landed one day later in #1527 (TKT-SLFURL), and the follow-up sweep of unparsed routes (BUG-VFHUWO, #1658) did not include _comments.'
why3: The only faced-comments test (TestComments_PerFaceThreads) runs on memstore with no ACL. memstore keys its index on FormatStateRef, so GetEntity('TKT-001@draft') returns the draft row by accident and a 200 assertion passes. Nothing drove a faced thread under a query-shaped grant, with a text anchor, or through a face delete.
why4: The previous prevention (measure faced-route-address-parse-test) is a per-route test plus a one-time inventory on BUG-VFHUWO. A route merged in parallel with that inventory was never in it, and a per-route test only protects routes someone remembers to test.
why5: 'Systemic: two stores accept a suffixed id in GetEntity by coincidence of their index key, so the test suite (memstore-only in dataentry) cannot observe an unparsed address, while the backends that expose it are not the ones tests run on. A bare id and an `ID@face` address are both string at the boundary. Fourth occurrence of this shape (BUG-64MU2Q, BUG-OOZBBK, BUG-VFHUWO, this one).'
prevention: 'P1: memstore and fsstore GetEntityState refuse an id containing the state-ref separator with ErrNotFound, matching pgstore/sqlitestore, pinned by storetest AddressStringIsNotAnID on every backend. This removes the coincidence that hid all four occurrences. Address-taking readers (visibility PolicyReader/AllowAllReader/ScriptReader/UnrestrictedReader) parse the address themselves, and callers with a user-supplied address use store.GetEntityAt. P2: comment handler tests assert the row reached (thread contents, anchor range), including a relation-conferred grant, and a face delete drops that face''s thread.'
status: done
---

## Reproduction

The SPA addresses a faced thread as `ID@face` (`EntityDetail.vue`,
`commentEntityId`). For a type that declares faces this is every view, since
such types have no bare row (BUG-HC6I2T).

Probe tests in `internal/dataentry` (run on memstore, then deleted):

1. **Postgres/SQLite semantics.** With a store wrapper whose `GetEntity` matches the
id literally against the bare row (as pgstore/sqlitestore do: `WHERE id = $1 AND
face = ''`), `GET` and `POST /api/v1/_comments/ticket/TKT-001@draft` both return
404 "Entity not found".
2. **Relation-conferred read grant.** Role `viewer` conferred by `owned-by`,
`read: [ticket, ticket@draft]`, `comment:read`/`comment:add`. `GET .../TKT-001`
returns 200; `GET .../TKT-001@draft` returns 404.
3. **Text anchor on the draft face.** `POST .../TKT-001@draft` with a text anchor
quoting draft-only text returns 400 "the selected text was not found in the
current body" (it was matched against the default face's body).

Face delete: `entitymanager.DeleteEntityFace` has no call to
`notifyAliasesOfDelete` or any per-face equivalent, so the thread at `ID@face`
is never removed.

## Why the existing test passes

`TestComments_PerFaceThreads` runs on memstore with no ACL. memstore/fsstore key
their index on `entity.FormatStateRef`, so `GetEntity("TKT-001@draft")` finds
the draft row by coincidence (the same coincidence BUG-Y0GNSB documents).

## Fix plan

1. **Parse the address once, at the route.** `handleV1Comments` parses the id
segment with `parseEntityRef` (replacing `isSafeStateRefSegment`) and the gate
reads through `getVisibleRef`. The row gate then receives the bare id, and the
store is asked for `(id, face)`.
2. **Carry the resolved address, not a re-derivable id.** `gateCommentTarget`
returns the resolved entity alongside the target. `liveAnchors` and
`buildTextAnchor` take that entity instead of re-reading by id. This removes two
extra reads per request and makes a wrong-face body read impossible.
3. **Face delete drops that face's thread.** Add a per-face notification from
`DeleteEntityFace` (a new optional method on the alias-rewriter fan-out, or a
separate narrow consumer-side interface), implemented by `comments.Service` as
`store.DeleteTarget(Target{ID, Face})`. The CalDAV alias service ignores it.
4. **Prevention: remove the memstore/fsstore coincidence.** `GetEntity(id)` on
memstore and fsstore refuses an id containing `@` with `ErrNotFound`, as pgstore
and sqlitestore already do, and a `storetest` case pins it for every backend.
This makes the whole class fail in the memstore test suite. Before landing,
audit callers that pass raw ids (CLI `rela update`, MCP `update_entity`, Lua
`rela.update_entity`; see BUG-Y0GNSB) and route them through `ParseStateRef` +
`GetEntityState`.

## Implementation notes

- The audit found that the read surfaces which take an address were relying on
the coincidence: `visibility.PolicyReader.Get`, `AllowAllReader.Get`,
`ScriptReader.GetEntity` (Lua `rela.get_entity`) and `UnrestrictedReader`. Each
now splits the address and loads through `GetEntityState`. `EntityGetter` asks
for `GetEntityState` so a reader cannot forward an address to `GetEntity` by
accident. MCP reads go through these readers, so they are covered too.
- `store.GetEntityAt(ctx, r, addr)` is the one helper for a caller holding a
user-supplied address. The CLI `show` and `render` commands use it. `delete` and
`acl can` keep a bare-id contract, so an address is a clean not-found there
rather than a half-handled face. `show` lists relations by the row's bare id and
restricts outgoing edges to the addressed face.
- Writes were already safe: the entitymanager parses refs itself
(`getEntityByRef`, `anyFaceOf`).
- The face-delete notification is `AliasRewriter.EntityFaceDeleted`. The CalDAV
alias service ignores it.
- Out of scope, filed separately: a relation-conferred role granted only
`type@face` fails the row gate for every face except the default one. This
affects the entity route as well as comments.

## Regression tests

- Handler tests that assert the ROW reached, not the status: on a store with
database semantics (and the sqlite backend where available), list and add on
`TKT-1@draft` return the draft thread.
- Relation-conferred read grant: `TKT-1@draft` list returns 200.
- Text anchor quoting draft-only text is accepted on `@draft`, and a stored
draft text comment resolves its range within the draft body.
- `DeleteEntityFace(draft)` empties the draft thread and leaves the default
face's thread intact.
- `storetest`: `GetEntity("X@face")` is `ErrNotFound` on every backend.

## Related areas checked

- `feed_handler.go` `getEntity` also calls `getVisible`, but with ids from feed
config and scope traversal, not a path segment. To confirm during
implementation.
- Rename: `Store.Rename` re-keys every face on all backends (`commentstest`
"Rename moves every face"). Not affected.
- Per-face storage isolation is covered by `commentstest` "Faces". Not affected.
