---
id: BUG-GEMNW6
type: bug
title: Kanban relation filter controls are silent no-ops
description: 'A kanban''s relation: filter_controls validate and render a dropdown that only offers All; the board filters client-side on entity.properties and never applies them. The same controls work on a list.'
priority: medium
effort: s
why1: KanbanView filters client-side on entity.properties[control.property]; a relation control has no property, so it gets no options and can never match.
why2: The board has its own filter bar instead of the list's FilterBar, which fetches relation candidates and hands filters to the server.
why3: The board fetches through the same list endpoint as a list but never sent filter params; and the endpoint only recognized relation controls declared on lists, so even a sent board filter would match nothing.
why4: Board filtering was built as a separate client-side copy of list filtering; kanban tests only covered property controls, so the relation path was never exercised.
why5: Views over the same entity set (list, board) each implement filtering themselves, so a control type added to one silently does nothing on the other.
prevention: The board now reuses FilterBar, useUrlFilterSync and filterStateToApiParams, so list and board share one filter path and the server applies both. A component test pins that a relation filter reaches the list request; the e2e board spec filters by property and by relation.
status: backlog
---

## Summary

A kanban's `filter_controls:` accept `relation:` controls — the config validator
allows them and the board renders a labelled dropdown — but the board ignores
them. The dropdown only ever offers "All", and even a filter set through the URL
is never applied. `property:` controls work; `relation:` controls are silent
no-ops. The same controls work on a list.

## Reproduce

1. On any kanban, add a relation filter control:
   ```yaml
   filter_controls:
     - relation: assigned_to
       direction: outgoing
       label: Assignee
   ```
2. `rela validate` passes; the server starts.
3. Open the board. The "Assignee" dropdown lists only "All".
4. Put the same control on a list of the same type: its dropdown lists every
person, and choosing one filters the rows.

## Evidence

`frontend/src/views/KanbanView.vue` has its own filter bar instead of the list's
`FilterBar`:

- The options come from `filterOptions`, which only collects
`entity.properties[control.property]` — a relation control has no `property`, so
it gets no options.
- `filterValues[control.property || '']` binds every relation control to the
same empty key.
- `filteredEntities` filters client-side on
`String(entity.properties[prop] || '')`, which a relation value can never match.

`EntityList` uses `FilterBar` (relation candidates fetched from the relation's
target types), `useUrlFilterSync`, and `filterStateToApiParams`, so its filters
are applied server-side by the list endpoint, relation filters included. The
board already fetches through the same list endpoint (`listAllEntities`), it
just never sends the filters.

Server side, a second gap: the list endpoint only treats `filter[<rel>]` as a
relation filter when a relation control configures it
(`Config.RelationFilterDirection`, RR-B0JPPL), and that resolver scanned **lists
only**. A relation control that only a board configures would fall through to
the property pass and match nothing, even once the board sends it.

## Fix

- Frontend: the board uses the list's machinery — `FilterBar`,
`useUrlFilterSync`, and `filterStateToApiParams` merged into the board's list
params — so the server applies property and relation filters exactly as for a
list. The client-side filter-control loop and `filterOptions` are removed. Board
filters are now also in the URL (shareable, back/forward), as on a list.
`FilterBar`'s `config` prop is narrowed to `Pick<ListConfig,
'filter_controls'>`, the only field it reads.
- Backend: `Config.filterControlSources()` returns every list, then every
kanban (each by sorted ID). `RelationFilterDirection`,
`HasPropertyFilterControl` and the three filter-control warnings in
`CollectConfigWarnings` use it, so a board's controls are honored and a board
disagreeing with a list on a relation's direction is reported (lists win, then
lowest ID).

Out of scope: the board's static `filters:` stay client-side (BUG-MYN56J).

## Tests

- `KanbanView.filters.test.ts`: a relation filter in the URL reaches
`listAllEntities` and the returned card is shown.
- `TestV1ListRelationFilter_ControlOnKanbanOnly`: the list endpoint applies a
relation filter that only a kanban configures.
- `TestConfigRelationFilterDirection` (kanban-only control resolves; a list
wins a direction conflict) and
`TestCollectConfigWarnings_ConflictingRelationDirections` (a kanban conflicting
with a list is reported).
- e2e `kanban.spec.ts`: filtering the fixture board by priority and by a
`blocks` relation leaves exactly the matching card.

The relation tests fail before the fix; the priority e2e test pins that property
controls keep working through the new path.
