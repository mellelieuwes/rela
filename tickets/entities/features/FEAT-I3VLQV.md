---
id: FEAT-I3VLQV
type: feature
title: 'Twins: integrate entities with external systems'
summary: Declare per entity type which fields an external system owns (a Pact) and link entities to their external counterparts (Twins) that an agent keeps in sync.
description: 'Integration as a core concept: Pacts in schema.yaml plus Twins with a sync base, deterministic reconcile and field-ownership enforcement. See body.'
priority: medium
status: in-progress
---

## Why

A business-modelling tool is only as useful as its connection to the systems
where work actually happens. The entities rela models — scenarios, issues,
risks, controls — oftentimes also live somewhere else: a Basecamp todo, a GitHub
issue, a Jira ticket. Today every integration models that "somewhere else" on
its own terms: CalDAV has `read_only:` field locks, the fs↔pg sync channel has
its own hash index, a webhook has no memory at all. Nothing in rela can say
"this entity has a counterpart elsewhere", so nothing can build on it.

## What

Two concepts, together called *integration*:

- **Pact** — declared per entity type and external system in `schema.yaml` (`pacts:`). The
agreement: which fields the external system owns (`theirs`, `*` for all), which
both sides edit (`shared`); every other field is ours. Plus the scope (the
external collection) and free-text instructions describing how both sides map.
- **Twin** — the link between one entity and its counterpart: external id, URL, sync state
(`in_sync`, `pending`, `conflict`, `gone`), and the agreed base of the last
sync.

In this proposal rela does not talk to an external system itself. The transport
is whoever runs the sync — in the design case an AI agent using the external
system's own CLI, reading the Pact as its brief. rela is the ground truth for
the integration itself: it holds the contract and the state, reconciles
deterministically against the base, and enforces ownership — a `theirs` field of
a twinned entity cannot be edited in rela except through the sync path.

## Stages

1. Stage 1 (TKT, this feature's first ticket): `pacts:` schema, the twins store with file
and memory backends, deterministic reconcile, ownership enforcement in
entitymanager and the data-entry field verdicts, `rela twin` CLI for agents, a
Twin badge in the SPA, docs.
2. Later: pg/sqlite backends, relations as pact fields, faces, MCP tools, a proposal
workflow for `propose`, and re-expressing CalDAV's `read_only:` on top of twins.
