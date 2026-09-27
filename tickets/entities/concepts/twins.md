---
id: twins
type: concept
title: Twins
summary: Counterparts of entities in external systems, kept in sync under a per-type Pact
description: A Twin links an entity to its counterpart in an external system (Basecamp, GitHub, …). A Pact, declared per entity type and system in schema.yaml, states which fields the external system owns. rela holds the Pact, every Twin and the agreed base of the last sync, reconciles deterministically and enforces field ownership; the transport (an AI agent, a script) lives outside rela.
package: internal/twins
layer: core
status: draft
---
