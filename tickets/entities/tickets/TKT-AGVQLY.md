---
id: TKT-AGVQLY
type: ticket
title: 'Twins stage 1: pacts, twins store, reconcile and ownership enforcement'
kind: enhancement
priority: medium
effort: l
status: done
---

## Description

Stage 1 of Twins (FEAT-I3VLQV): an entity type declares a **Pact** per external
system in `schema.yaml`, and an entity can be linked to its counterpart there —
a **Twin**. An agent (or any script) that talks to the external system keeps the
pair in sync through `rela twin`; rela holds the Pact, the Twin and the agreed
base of the last sync, reconciles deterministically, and enforces who owns which
field.

**Twins are not graph content**, for the same reason comments are not
(TKT-FIO205): a Twin is bookkeeping *about* an entity, not a fact in the
operator's model. They get their own package, their own `twins.Store` interface
with a conformance kit, and their own service — outside `store.Store`, the audit
log, `/_schema` entity lists, search and analysis. The writes a sync makes *to
the entity* do go through `entitymanager` and are validated, gated and audited
like any other write.

rela never calls an external system. That keeps credentials, rate limits and the
lossy translation between two data models with the party that owns them, and
leaves rela the part only it can do: the contract, the state and the
enforcement.

## Scope

### Schema — `pacts:` per entity type

```yaml
entities:
  scenario:
    pacts:
      basecamp:
        scope: https://app.basecamp.com/…/todolists/…
        theirs: ["*"]
        shared: []
        propose: []
        instructions: |
          A Scenario is one todo in the scope todolist. …
```

A field is a declared property or `body`; every field not in `theirs`/`shared`
is ours. Validated at load: strict keys, system id pattern, http(s) scope, real
field names (not computed), `*` only alone and only in `theirs`, one owner per
field, `propose` disjoint from `shared`, no pacts on types with `faces:` (stage
1). Exposed read-only on `/_schema` (without instructions). Policy view
`metamodel.NewPactPolicy`.

### Twins store and service

`internal/twins`: `Twin{System, ExternalID, URL, Target, State, Base,
BaseVersion, SyncedAt, RemoteUpdatedAt, Findings}`, keyed by (system, external
id), at most one twin per entity per system. Backends `filetwins`
(`.rela/twins/<system>/<id>.yaml`, atomic writes) and `memtwins`, both passing
`twinstest.RunAll`.

`Reconcile(pact, base, local, remote)` is pure: per field, the owner decides —
`theirs` takes the remote value, `ours` reports a remote change as a
`foreign_edit` (flagged `propose` when the pact says so), `shared` takes
whichever side changed and reports a `conflict` when both did. The resulting
state is `conflict`, `pending` (something still has to reach the other side) or
`in_sync`.

`Pull` applies the plan through a compare-and-swap patch (`store.VersionOf` as
the token) and then advances the base; a lost race leaves the twin untouched.
`Pushed` records that the external side caught up. `Pending` is the agent's work
list: never-synced, changed in rela since the base version, pending, conflict,
gone. Rename re-targets twins; delete marks them `gone` rather than dropping
them, so the agent can carry the delete across.

### Ownership enforcement

`entitymanager` rejects caller-authored writes to a field an external system
owns on a twinned entity (`TheirsWriteError`), on the same paths computed
properties are rejected (patch and update, incl. the body). Only the handle
returned by `Manager.TwinSync()` — handed by `appbuild` to the twins service
alone — may write them; it skips nothing else (ACL, validation, transitions,
unique and audit still apply). `ApplyEntity` (the fs↔pg replica channel) and
automation writes stay ungated, as they are for the field gate. The data-entry
field verdicts mark owned fields read-only, so the SPA renders them disabled.

### Surfaces

- `rela twin link|unlink|show|list|pending|pull|pushed|gone|pact`, all with `-o json`;
principal tool `twin`.
- `GET /api/v1/_twins/{type}/{id}`, read-gated by the entity's read verdict.
- A Twin badge (system, state, link) on the entity page.
- Guide `docs/twins.md`, `pacts:` in the metamodel reference, `rela twin` in the CLI
reference.

## Out of scope

pg/sqlite twin backends (a postgres or sqlite recipe with pacts fails fast at
startup); relations as pact fields; faces; MCP tools; a proposal workflow beyond
reporting `propose` on findings; editing twins in the SPA; any transport
(webhooks, polling) — that belongs to the agent.

## Acceptance criteria

1. A schema without `pacts:` behaves exactly as before: no twin routes, no `.rela/twins`
directory, `/_schema` unchanged.
2. Every pact validation rule rejects its invalid case with a clear message at load.
3. `rela twin link` requires a pact for the entity's type and system; a second twin for
the same entity and system is refused.
4. `rela twin pull` applies `theirs` fields, reports foreign edits on ours fields and
conflicts on shared fields, and leaves the entity untouched on a version
conflict.
5. A `theirs` field of a twinned entity cannot be changed through the CLI, the data-entry
API or MCP; the same field on an entity without a twin can.
6. Owned fields are read-only in the data-entry forms.
7. `rela twin pending` lists never-synced, locally changed, conflicting and gone twins.
8. Renaming an entity keeps its twins; deleting it marks them `gone`.
9. Both backends pass `twinstest.RunAll`.
