# Data-entry API: PATCH /api/v1/{plural}/{id}

This document describes the unified PATCH endpoint for updating an entity's
properties, content, and outgoing relations atomically in a single request.

The wire format borrows the JSON:API §9 resource-identifier shape at the
relation-list level; per-edge data uses rela's existing `properties` /
`properties_unset` / `content` upsert convention. The response is rela's
flat top-level format augmented with an optional `warnings` array — we do
NOT claim full JSON:API conformance.

## Wire format

```http
PATCH /api/v1/tickets/TKT-001
Content-Type: application/json

{
  "properties": {"title": "x"},
  "content":    "...",
  "relations": {
    "tagged": {
      "data": [
        {
          "type":       "label",
          "id":         "L-001",
          "meta":       {"weight": 5},
          "meta_unset": ["added_by"],
          "content":    "edge body"
        },
        {"type": "label", "id": "L-002"}
      ]
    },
    "belongs-to": {
      "data": [{"type": "category", "id": "C-001"}]
    }
  }
}
```

All top-level fields are optional. Field absence means "leave alone".

| Field | Semantics |
|---|---|
| `properties` | Map of property names to values. **Upsert**: keys present are merged into existing properties; absent keys are unchanged. |
| `content` | Markdown body. **Upsert**: present (including empty string) replaces; absent leaves alone. |
| `relations` | Map of relation type → desired-state wrapper. See below. |

## Collection rows are content-free

`GET /api/v1/{plural}` and `GET /api/v1/_search` return each row's `id`,
`type`, `_title`, `properties`, relations and affordances, but **not** its
markdown body: `content` is absent from collection rows. A list, kanban or
dashboard renders titles and properties, and shipping every row's body made a
100-row page half a megabyte and the dashboard's count cards tens of megabytes
at 20k entities (TKT-1U8XYN). On the PostgreSQL backend the bodies never leave
the database — the server reads content-free headers for the whole pipeline
(filter, sort, paginate) and loads bodies only for the rows it serves, only
when asked.

Ask with `include_content=true` (any value `strconv.ParseBool` accepts; anything
else is `false`):

```text
GET /api/v1/tickets?page=1&per_page=25&include_content=true
GET /api/v1/_search?q=type:ticket&include_content=true
```

The per-entity `GET /api/v1/{plural}/{id}`, `_views` and `?include=` peers are
unchanged: they always carry `content`, as do ETags computed over it. A row
whose entity has an empty body looks the same with or without the flag, which
is the reason the field is omitted rather than sent empty.

## Limiting search results

`GET /api/v1/_search` accepts an optional `limit` (an integer from 1 to 100)
that caps the number of rows returned. The cap applies after the read gate, the
`type` filter and any `sort:` clause, so it counts only rows the caller may see.
Any other value is a `400 invalid_limit`.

The limit shrinks the response, not the server's work: the query still reads
and sorts every matching row before the cut.

```text
GET /api/v1/_search?q=type:ticket,feature sort:modified:desc&limit=8
```

The editor's `@` mention menu uses this form to list recently modified entities.

## Relations field

Each value of the `relations` map is one of TWO shapes:

### Modern (JSON:API §9-shaped)

```json
{"tagged": {"data": [{"type": "label", "id": "L-001", "meta": {"weight": 5}}]}}
```

The wrapper has exactly one field, `data`, which is an array of resource
identifiers. Three cases for `data`:

1. **Relation type absent from the map** → leave all edges of that type alone.
2. **`data: []`** → remove all edges of that type from this entity.
3. **`data: [{type, id, ...}, ...]`** → the array IS the new desired set. Edges
   in the list are kept (or upserted with new meta/content); edges currently in
   the graph but absent from the list are removed.

### ⚠️ Data-loss footgun

**Sending `data: []` deletes every edge of that relation type from this
entity.** This is the most dangerous shape in the wire format. If your client
builds the PATCH body via object spread on a not-yet-fetched form state —
`{relations: {tagged: {data: []}}}` is the default empty form value — the
first auto-save fire silently wipes the entity's tagged edges.

**Mitigations:**

- **Fetch before edit.** A client that auto-saves must complete its initial
  GET before issuing the first PATCH that touches `relations`.
- **Omit unsubmitted relations.** If the user hasn't touched the relation
  type, don't send it. Absent → leave alone is the safe default.
- **`data` field is required when the wrapper appears.** `{"tagged": {}}`
  returns 400, not a silent empty array. This catches the most common
  malformed-request case where a client constructed the wrapper but forgot
  to populate `data`.

### Per-edge fields

Each entry in `data` is a resource identifier with these fields:

| Field | Required? | Semantics |
|---|---|---|
| `type` | yes | Target entity type. **Soft check**: a mismatch surfaces a `target_type_mismatch` warning and writes the edge anyway (DEC-HWZHA). |
| `id` | yes | Target entity ID. **Soft check**: a missing target surfaces a `target_not_found` warning and writes the edge anyway. |
| `meta` | optional | Per-edge properties. **Upsert** — keys merge into existing meta. **Soft check**: unknown keys against the relation's closed schema produce `unknown_meta_key` warnings, but persist. |
| `meta_unset` | optional | Keys to clear after the merge. Mirrors `properties_unset` at the entity level. Non-string elements → 400 at unmarshal. |
| `content` | optional | Per-edge markdown body. **Upsert** — present (including empty string) replaces, absent leaves alone. **Hard 422**: present on a relation type without `content: true` (the file format can't hold a body). |

### Legacy (IDs-only)

```json
{"tagged": ["L-001", "L-002"]}
```

The legacy shape continues to work. Set semantics: edges in the list are kept
or created (with empty meta and empty content); edges absent from the list
are removed.

Mixing the two shapes in one PATCH body returns 400 with a stable
`shape_mixed` error code.

## Validation policy

Per [DEC-HWZHA](../../tickets/entities/decisions/DEC-HWZHA.md), validation
findings split into three classes:

### Hard 400 — request shape errors

Detectable without consulting the metamodel.

- Malformed JSON in body
- Relation wrapper missing required `data` field (`{"tagged": {}}`)
- Resource identifier missing `type` or `id`
- Mixed legacy + modern shapes in one body (`shape_mixed`)
- Non-string element in `meta_unset` array (`meta_unset_invalid`)
- `data` field has unexpected type (string, scalar, etc.)
- `data: null` on a wrapper (treated same as missing)
- Unknown sibling key in modern wrapper (only `data` allowed)

### Hard 422 — structural impossibilities

The storage layer literally cannot persist this state.

- Unknown relation type (`unknown_relation_type`) — no defined storage location
- Writing `content` on a relation type without `content: true` (`content_not_supported`) — the file format has no body slot

### 200 + warnings — soft conditions surfaced inline

Everything else previous drafts would 422 on. The API performs the requested
write and returns warnings in the response body so UIs surface them
non-blockingly. Each warning is `{code, path, detail}` where:

- `code` is stable and matches the corresponding `analyze` finding code
- `path` is an RFC 6901 JSON Pointer to the offending field
- `detail` is a human-readable explanation

Warning codes:

**Entity-level** (TKT-QETTR — soft conditions on the entity itself):

| Code | When |
|---|---|
| `required_property_unset` | Required entity property is missing or empty after the write |
| `property_type_mismatch` | Entity property value has the wrong primitive type (e.g. integer expected, string supplied) |
| `property_value_invalid` | Entity property value is the right type but outside the declared constraint (enum value not allowlisted, malformed date, bad RRULE, regex mismatch) |

**Relation-level** (TKT-6WLSW — soft conditions on a relation edge):

| Code | When |
|---|---|
| `target_not_found` | Target ID doesn't exist in the graph |
| `target_type_mismatch` | Target's actual type doesn't match `data[i].type` |
| `target_type_not_allowed` | Target type not in the relation's `to` allowlist |
| `source_type_not_allowed` | Source type not in the relation's `from` allowlist |
| `unknown_meta_key` | Meta key not declared in the relation's closed schema |
| `required_meta_unset` | Required meta property absent after merge |
| `meta_type_mismatch` | Meta value's type doesn't match the declared property type |

The `orphans` and `validations` analyze checks surface the same
findings; clients may de-duplicate by `code`.

Entity-level warnings reflect the **post-write entity state** — if the
saved entity has a missing required field even after this PATCH (because
the field was already missing on disk), the warning surfaces on every
write. This is by design: API contract is "what's wrong with this entity
right now," not "what this PATCH broke." UI surfaces (auto-save, etc.)
are responsible for warning-fatigue mitigation if that becomes a UX
problem.

## Atomicity

The handler runs in three phases:

1. **Validate** — relation validation runs FIRST. On 400/422, no writes occur.
2. **Update entity** — only if the request actually changed `properties` or `content`.
3. **Apply relations** — adds/updates/deletes per the diff. Soft-condition edges
   are written directly through the store, bypassing the workspace's
   target-existence check.

The validation-first ordering means a relation 422 leaves the entity
**untouched**. The unavoidable atomicity gap is mid-write-loop store failures
(disk full, permission denied, etc.) — these are rare, irrecoverable, and
return a 500 with `relation_write_failed`. The legacy reconciler had the same
property; this PR doesn't make it worse.

The store interface (internal/store/store.go) intentionally has no
transaction primitive — adding one would fight the pluggable-backends goal
(FEAT-CO4YP). For a local-first tool with single-writer-typical workloads,
the documented gap is acceptable.

## No-op suppression

The diff classifier compares each desired edge against the current graph
state. If the post-merge `(properties, content)` byte-equals the current
state, no write occurs and no SSE event fires. Auto-save's primary path
(re-saving an unchanged form) writes zero relation files and broadcasts
zero events.

Comparison uses `reflect.DeepEqual` after merging meta + applying meta_unset.
Both sides come from the same Go-native unmarshal path (JSON or YAML), so
type coercion (`int(5)` vs `float64(5)`) hasn't been an issue in practice. If
it surfaces, the fallback is a `valueEqual` helper in `internal/model`.

## Automation interaction

When `properties` or `content` changes trigger an `entity:updated` automation,
the automation runs during Phase 2. Phase 3 (relation reconcile) computes its
diff against the pre-automation graph state — **automation-created relations
that conflict with the desired set may be deleted and recreated**. This is
the same hazard the legacy reconciler has today.

## SSE events

A successful PATCH that performs writes broadcasts:

- One `entity:updated` event for the PATCHed entity, ONLY when entity properties or content actually changed
- One `relation:created` / `relation:updated` / `relation:deleted` event per actual relation write (none on a no-op)
- No events on 400/422 (handler returns before broadcast)

A relations-only PATCH that produces all no-ops emits zero events.

## ETag / If-Match

The handler honors `If-Match: <etag>` for optimistic concurrency control.
The ETag is computed against the entity's current `properties + content`.
Mismatch → 412 Precondition Failed.

The precondition is re-checked by the store, atomically with the write
(TKT-34XS2R). Previously the compare and the write were separate steps made
safe only by a process-local write lock, so two `rela-server` processes against
one PostgreSQL database could both pass the compare and the second could
overwrite the first. A write that loses that race now returns 412 as well — the
same status, and the same remedy for the client: re-read, re-apply, retry.

## MCP and Lua content semantics

`entitymanager.RelationOptions.Content` is `*string` — pointer-vs-string
distinguishes "leave alone" from "set to empty". Two callers have notable
boundary semantics:

- **MCP `create_relation` tool**: an empty `content` string on the request
  is treated as "leave alone" (helper `nilIfEmpty` in `internal/mcp/`). To
  clear, omit `content` or pass `null`.
- **Lua `rela.update_relation`**: an explicit empty string clears the body
  (matches the user-intuition that `content = ""` means empty). To leave
  alone, omit the field.

## Action affordances (`_actions`)

Every entity and list response carries an `_actions` map describing
which write verbs the **current principal** can apply to the resource.
The Vue SPA consults this map to render write controls (buttons, menu
items); `false` verbs are hidden, `true` verbs render.

### Per-item shape

```json
{
  "id": "TKT-001",
  "type": "ticket",
  "_actions": {
    "update": true,
    "delete": false,
    "rename": true
  }
}
```

### Per-collection shape

```json
{
  "data": [...],
  "meta": {...},
  "_actions": {
    "create": true
  }
}
```

### Phase 1 verb vocabulary

The closed set of verbs in phase 1, matching `acl.Op` exactly:

| Verb | Scope | `acl.Op` |
|---|---|---|
| `create` | per-collection | `OpCreate` |
| `update` | per-item | `OpUpdate` |
| `delete` | per-item | `OpDelete` |
| `rename` | per-item | `OpRename` |

`transition:<state>` and `relation:<type>:add/remove` will follow once
the ACL layer learns to represent them (gated on a separate ACL v0.5
work item). Until then the SPA continues to render workflow controls
unconditionally and falls back to the server's 403 on disallowed
transitions.

### Detail-page actions (`action:<id>`)

A per-item `_actions` map may also carry keys of the form `action:<id>`, one
per [action with `available_on`](../data-entry.md#actions-on-the-detail-page)
that the principal may run against this entity at its face. These keys are
only ever `true`. **Absence means "not offered"**, the opposite of the verb
keys, because only the server can list which actions apply.

```json
"_actions": { "update": true, "action:regenerate-soa": true }
```

`POST /api/v1/_action/<id>` with `{"entity_id": "<ID>@<face>"}` checks the same
rules before the script runs:

| Status | Error code | When |
|---|---|---|
| 403 | `permission_required` | The caller lacks the action's `permission:`. Applies to every action, with or without `available_on` |
| 404 | `entity_not_found` | No `entity_id`, or the entity does not exist or the caller may not read it at that face |
| 403 | `action_not_available` | Type, face or `when` does not match |
| 409 | `config_reloaded` | The configuration was reloaded while the request waited; retry |

### Always present

Every HTTP response from the data-entry server carries `_actions`.
The router unconditionally stamps a Principal on each request (a
`{User: "unknown", Tool: "data-entry"}` sentinel when no header /
environment override is configured), so there is no "anonymous"
branch in production. A principal with all verbs denied receives
`_actions: {}` — same shape, all values false. A principal with
every verb granted receives `_actions` with all values true.

### How the SPA consumes `_actions`

Phase 2 (TKT-LFT2) ships the SPA consumers. Each write affordance
(delete button, edit button, "+ New" button, drag-drop, Del-key
handler) consults `entity._actions[verb]` and renders only when the
verdict is anything other than explicit `false`. Concretely:

- `entity._actions?.delete !== false` → render the delete button.
- `entity._actions?.update !== false` → render the edit button and
  enable drag-drop in Kanban.
- `listResponse._actions?.create !== false` → render the "+ New"
  button on list / Kanban pages.
- Absent `_actions` (non-data-entry callers, pre-rollout servers) →
  defensive render; the server still 403s on the actual write.
- Direct-URL navigation to `/form/:id/:entityId` when the loaded
  entity's `_actions.update === false` → renders a "This entity is
  not editable" message in place of the form.

In **read-only mode** (`rela-server --read-only`), entity-CRUD
controls are absent across the SPA — no "+ New", no delete buttons,
no Edit buttons, drag-drop disabled. Deferred phase-2 sites (Lua
command buttons, settings / theme / git writes, relation add/remove
inside form widgets, inline-edit buttons in related-entity cards)
remain visible and 403 at the server on click; future phases gate
them as new verbs land in the ACL primitive (see TKT-XZEY).

A development-mode console warning fires once per request path when
a whitelisted API response (`listEntities`, `getEntity`,
`createEntity`, `updateEntity`) omits `_actions`. Production builds
suppress it.

### The cardinal rule

**`_actions` is a UI hint, not authorization.** The server
re-authorizes every write. A client that forges
`_actions: {delete: true}` and issues DELETE still gets the same
`403 *acl.ForbiddenError` the policy would have produced. This is
asserted as an integration test:
`internal/dataentry/affordances_contract_test.go` runs both halves
of the contract (true → 2xx, false → 403) across `NopACL`,
`ReadOnlyACL`, and `Declarative` policies. The single source of
truth for the verb→`acl.WriteRequest` translation is the
`translateVerb` function in `internal/dataentry/affordances.go`; a
grep test forbids direct `acl.WriteRequest{Op:` construction
anywhere else in the package.

### Additive evolution

New verbs are added by appending entries to `translateVerb` (and the
corresponding `acl.Op` constants in `internal/acl/`). Older SPAs
silently ignore unknown verb keys. Removing or renaming a verb is a
breaking change requiring a major API version bump.

### Scope of the invariant

The wire-vs-policy guarantee covers HTTP write endpoints reached by
the SPA. MCP write tools, Lua write paths, and scheduler-driven
writes go through the same `entitymanager.Manager` (so they are
re-authorized) but do not consult or emit `_actions`. A Lua
automation can therefore perform a write whose corresponding
`_actions[verb]` would have been `false` for the SPA principal —
that's expected and documents the scope.

## Out of scope

- **Symmetric / inverse propagation of per-edge meta.** The current write
  path doesn't propagate at all; an independent ticket would address it if
  needed.
- **Cardinality enforcement.** `min_outgoing` / `max_outgoing` are advisory
  (surfaced via `analyze`), never enforced at write time.
- **Granular relations diff verbs** (à la GraphQL `connect`/`disconnect`).
  v1 is replacement-only at the list level + upsert at the per-edge level.
- **Cross-entity atomic transactions.** A single PATCH targets one entity;
  there is no batch / transaction surface across entities.

## Affordances: `_fields` and `_relations` (TKT-G7N5)

Per-entity GET responses carry two affordance maps alongside the entity
payload: `_fields` (field-level write / option verdicts) and `_relations`
(relation-type-level create / remove / meta-field verdicts). The SPA reads
these to render disabled inputs, filtered enum selects, and hidden + Add /
x buttons. Writes that contradict a verdict return 403.

### Verdict source

Verdicts come from one of three sources, selected at server startup by the
`RELA_AFFORDANCE_PROFILE` env var (tests pass the resolver directly):

| Value | Behavior |
|---|---|
| unset / empty | **Policy-backed** when `acl.yaml` declares any affordance block (`fields:` / `visible:` / `options:` / `relations:`); otherwise permissive (both wire maps emit as `{}`). |
| `none` | Permissive — explicit opt-out even when a policy has affordance blocks. |
| `demo` | Hardcoded fixture against the `ticket` entity type — exercises every affordance code path for manual UI testing. Overrides the policy. |
| any other | Unknown — logs a warning and falls back to policy / permissive. Never panics. |

The policy-backed resolver compiles `when:` predicates from `acl.yaml` at
startup and evaluates them per entity. See the [ACL security
guide](../acl-security.md) and the [ACL overview](../acl-overview.md)
for the `acl.yaml` schema, the predicate language, and the closed-world /
cross-role semantics. The wire shape and SPA rendering below are identical
regardless of source.

### Wire shape

```json
{
  "id": "TKT-001",
  "type": "ticket",
  "properties": { /* hidden fields are OMITTED entirely */ },
  "_fields": {
    "kind": { "writable": false },
    "status": { "options": { "done": false } }
  },
  "_relations": {
    "affects":    { "creatable": false },
    "implements": { "removable": false },
    "has-planning": { "fields": { "note": { "writable": false } } }
  },
  "_redacted": ["salary"]
}
```

Both `_fields` and `_relations` are **sparse**: only entries that deviate
from the permissive default appear. An absent key in either map means
"default" — writable, all options allowed, creatable / removable. Under the
`none` profile both maps emit as `{}` (present, empty) — a closed-world
signal letting the SPA distinguish "evaluated, no deviations" from
"affordances not available" (anonymous fallback / pre-rollout server).

### Hidden fields and `_redacted`

Hidden fields are doubly invisible in the payload: their **values** are
omitted from `properties` AND they are absent from `_fields`.

Their **names**, however, are stated explicitly in `_redacted` — a list of the
properties this response withheld by field-level ACL:

```json
{
  "properties": { "status": "open" },
  "_fields": {},
  "_redacted": ["salary"]
}
```

**Never infer redaction from absence.** A property missing from `properties`
may have been redacted *or* may simply have never been set — the two are
byte-identical in the payload, and `_fields` is sparse so a plainly-writable
field is absent from it by design. Consult `_redacted`; it is the only sound
signal. (Reading absence as redaction is what made every unset property
permanently unreachable in the edit form — BUG-MLT9DE. The SPA now renders a
configured field unless `_redacted` names it.)

`_redacted` carries the same closed-world present/absent semantics as `_fields` /
`_relations`: present (possibly empty — "evaluated, nothing hidden") on
per-entity responses, absent on list rows, which carry no write affordances.
Names are sorted.

Disclosure boundary: `_redacted` reveals property **names**, never **values**.
This is not a new disclosure — the metamodel endpoint already serves the
declared property names per type, and `visible:` redaction is defined as
hiding values only, making no claim to conceal which properties exist. Row-level
ACL is unaffected: whether an *entity* exists remains a genuine secret, and
`_redacted` only ever rides a response the caller was already authorized to
read. See DEC-T0XIWQ.

### Write-side parity

Every relevant PATCH / POST / DELETE handler consults the same resolver as
the GET. A stale SPA client that submits a write contradicting a verdict
receives 403:

| Operation | Verdict checked | 403 `rule_id` shape |
|---|---|---|
| `PATCH /entities/<type>/<id>` with hidden field set or unset | `_fields.<name>` visibility | `field-affordance:hidden:<name>` |
| Same, with read-only field set or unset | `_fields.<name>.writable` | `field-affordance:read-only:<name>` |
| Same, with disallowed enum value | `_fields.<name>.options.<value>` | `field-affordance:enum-filtered:<name>=<value>` |
| `POST /entities/<type>/<id>/relations/<rel>` | `_relations.<rel>.creatable` | `relation-affordance:not-creatable:<rel>` |
| `DELETE /entities/<type>/<id>/relations/<rel>/<peer>` | `_relations.<rel>.removable` | `relation-affordance:not-removable:<rel>` |
| `PATCH /entities/<type>/<id>/relations/<rel>/<peer>` with non-writable meta | `_relations.<rel>.fields.<meta>.writable` | `relation-affordance:meta-read-only:<rel>.<meta>` |
| Unified `PATCH /entities/<type>/<id>` adding / removing edges or writing meta | as above | as above |

Unknown fields (declared neither in the metamodel nor by the resolver)
return 403 with the SAME `field-affordance:hidden:<name>` shape as
genuinely-hidden fields. This closes the side channel where an attacker
could otherwise distinguish "hidden from me" from "doesn't exist."

The 403 body shape is the same as for ACL denials:

```json
{
  "error": "forbidden",
  "rule_kind": "affordance",
  "rule_id": "field-affordance:read-only:kind",
  "reason": "field \"kind\" is not writable"
}
```

Audit log entries for these 403s use the existing `denied-write` op with
the `reason` field prefixed `affordance:<rule>:<path>` (vs `acl:` for true
ACL denials), so log readers can distinguish the two sources.

Read-only fields use strict 403 semantics: any presence of a read-only
field in `properties` or `properties_unset` rejects the whole PATCH,
including any writable fields in the same body. The SPA's `useAutoSave`
suppresses no-op PATCHes client-side via its `lastSeenServer` snapshot,
so no real SPA path produces a same-value PATCH; the rejection exists
for defense against hand-crafted clients.

Schema-declared `computed` properties always participate in this contract as
read-only fields, independently of ACL policy. Their materialized value is
returned for display with `_fields.<name>.writable: false`; the server derives
the value after writable input has been accepted.

### Create-mode affordances: dry-run (`?dry_run=true`) (TKT-3I5U)

The create form has no persisted entity to derive `_fields` from, and
verdicts can depend on the candidate's field *values* (value-dependent
`when:` predicates), so a static per-type verdict won't do. Instead the
create path is evaluated as a **dry run**:

```http
POST /api/v1/<plural>?dry_run=true
Content-Type: application/json

{ "properties": { ... }, "content": "..." }
```

The handler runs the same defaults + affordance evaluation the real
create would, against the candidate (type + supplied properties, no
persisted ID), and returns a normal per-entity response shape —
`_fields`, `_relations`, stripped hidden `properties`, and DEC-HWZHA
soft `warnings` — **without persisting anything, without an audit row,
and without taking the write lock** (it is a read-shaped operation).
The response is `Cache-Control: no-store` with no ETag.

Properties:

- **Advisory only.** The verdicts are a UI hint. The real create
  (`POST` without `?dry_run`) re-runs the affordance gate and is the
  sole authorization point — a client that POSTs a denied field still
  gets the 403 from the [write-side parity](#write-side-parity) table.
- **Value-dependent.** Because verdicts are computed against the
  supplied values, the SPA re-requests (debounced) as the user types so
  a field whose writability depends on another field updates live.
- **Relations not staged.** A candidate has no real ID, so edges can't
  be staged; `_relations` reflects the per-type verdict only.
- **Fail-open client.** If the dry-run request fails, the SPA renders
  the form unrestricted — the commit gate, not the hint, is the
  boundary.

The SPA create form (`DynamicForm`) consumes this to disable read-only
fields, omit hidden fields, and filter enum options, then commits only
the visible + writable keys; the server fills hidden / read-only
defaults itself (downstream of the gate).

## Transition affordances: `_transitions` (TKT-3G93B8)

A per-entity GET response also carries `_transitions` for every property whose
type is an **enum state machine** (a `CustomType` with declared `transitions:` —
see [the metamodel guide](../metamodel.md)). It is the resolved answer to "which
moves can *this* principal make on *this* field of *this* entity right now",
computed from the same evaluation the write path enforces (guard + `when:`
precondition). The SPA renders a machine field as a status control offering only
the performable moves, rather than a plain enum select listing every value.

### Wire shape

```json
{
  "id": "TKT-001",
  "type": "ticket",
  "properties": { "status": "todo" },
  "_transitions": {
    "status": [
      { "to": "doing", "label": "Start progress", "allowed": true },
      { "to": "done",  "label": "Complete", "guard": "close",
        "allowed": false, "reason": "guard" }
    ]
  }
}
```

Each entry is one declared out-edge from the field's current value:

| Field | Meaning |
|---|---|
| `to` | Target value the move lands on. |
| `label` | Optional display text for the **move** (the action, e.g. "Start progress"). Absent → the SPA falls back to the target value's display label, then the raw value. Authored via `label:` on the transition in the metamodel. |
| `guard` | ACL permission the move requires; absent when unguarded. |
| `allowed` | `true` iff the principal holds the guard AND the `when:` precondition holds. |
| `reason` | Why `allowed` is false: `guard` or `precondition`. Absent when allowed. Advisory (the control shows only allowed moves, so this feeds a tooltip/CLI, not the render gate). |

`_transitions` is **keyed by every machine-typed property** — a plain enum field
has no entry, and the SPA falls back to the ordinary enum control. A machine
field in a **terminal state** (no performable out-edges) still carries its key
with an **empty list** (`"status": []`): the key's presence is what tells the SPA
"this is a state machine — render the status control (here, with no moves)"
rather than a full enum select that would offer illegal targets. The map is
**absent entirely** when the server wires no state machines (no policy /
non-machine metamodel / older server), the same "feature not available" signal
`_fields` uses via its own absence. Like every affordance map it is a **UI hint,
never authorization**: the write path re-enforces the transition (guard 403,
legality / precondition 422), so a stale verdict simply surfaces the existing
structured error (attempt-and-recover).

### Create is an entry, not a transition

On the create form there is no prior state to move *from*, and a machine may only
be entered at its initial value (see BUG-X1C7S). The create dry-run
(`?dry_run=true`) therefore **locks** every machine field: it pins the property to
the machine's entry value, marks it `writable: false` in `_fields`, and omits it
from `_transitions`. The SPA renders it read-only at the initial state; the
create-commit filter drops the (non-writable) key so the server applies the
initial itself. A client cannot enter a machine at a non-initial state — the real
create rejects it regardless of the hint.

### View-section row entities (TKT-IHC7D)

Cards/list view sections (`display: properties | list | content | cards`)
serve as a per-entity surface from the user's POV: each row is the
inline-edit target of the next interaction. Their row entities
(`V1ViewEntity`) carry **typed `_props`** (the entity's property values,
typed not display-stringified) and **per-row `_fields`** with the same
sparse semantics as `V1Entity._fields`:

```json
{
  "id": "TKT-001",
  "title": "Add login",
  "type": "ticket",
  "fields": [{"property": "status", "label": "Status", "values": ["open"]}],
  "_props": {"status": "open", "title": "Add login"},
  "_fields": {"status": {"writable": false}}
}
```

`_props` and `_fields` are hidden-property-stripped at the source: a
property hidden by the metamodel + ACL never appears in either map. The
two maps may diverge in one direction only — `_fields` can carry a key
absent from `_props` when the property has no stored value but a
non-default verdict (e.g. `writable: false` on an unset field). Table
sections (`display: table`) and the entry-source section keep their
existing shape; the entry section's properties + affordances ride on
the parent `entry` (`V1Entity`).

### Out of scope (this ticket)

- **List-query affordances.** `_fields` and `_relations` ride only on
  per-entity GET, the dry-run create, and view-section row entities;
  list / collection responses (`GET /entities/<type>`) keep their
  existing shape.
- **Per-link verdicts** (different verdicts for different links of the
  same relation type). Deferred — requires per-link state-dependent
  gates.
- **Staged-relation validation.** The create form's relation pickers are
  gated only at commit, not during the dry-run (no candidate ID to hang
  edges on).
- **Inline dry-run warning display.** The dry-run returns soft
  `warnings`, but the create form does not yet render them per-field;
  they still surface on the commit response.

---

## Data-entry API: GET /api/v1/_apps/{id}/{path}

Serves a custom app's files for embedding in a sandboxed iframe. See the
"Custom apps" section of `docs/data-entry.md` for the authoring model and the
`rela.*` bridge surface.

```http
GET /api/v1/_apps/ticket-counter/            → the app's index.html
GET /api/v1/_apps/ticket-counter/app.js      → a sibling file from apps/ticket-counter/
GET /api/v1/_apps/ticket-counter/_rela.js    → the bridge SDK (reserved path)
```

- `{id}` must match `^[a-z0-9_-]{1,64}$` (else `400`).
- An app is the folder `apps/{id}/` containing `index.html` (folder-discovered,
  no config registry). A bare `/_apps/{id}` (no trailing slash) `301`-redirects
  to `/_apps/{id}/` so relative asset URLs resolve. An unknown app, or an entry
  that doesn't exist / escapes the app directory, returns `404`.
- `{path}` is resolved within `apps/{id}/` traversal-resistant (`..`,
  absolute, symlinks rejected). Content-Type is inferred from the extension.
  Reserved: `_rela.js` is served from the server (the bridge SDK), and the app
  cannot shadow it or serve any other `_`-prefixed entry.
- Every response carries a **path-scoped CSP header** — resource directives
  scoped to `/api/v1/_apps/{id}/` (not `'self'`), `connect-src 'none'`,
  `form-action 'none'`, `frame-ancestors 'self'` — plus
  `X-Content-Type-Options: nosniff`. This (not origin isolation) is what
  confines the app to its own files + the `MessageChannel` bridge.

There is **no new ACL verb** for apps: an app inherits the logged-in user's
permissions, so its reads/writes are gated exactly as the SPA's own.

---

## Attachments (`_attachments`)

A `file`-type property holds a binary attachment (image, PDF, …). The
bytes are stored by the backend keyed to `(entity id, property)`; the
property value in `properties` is the stored path string, which clients
should treat as opaque — use `_attachments` and the download endpoint
instead.

A `file` property can hold one attachment (the default) or several, when
its metamodel `max` is set above 1 (see `docs/metamodel.md`).

### Metadata on per-entity GET

Per-entity responses carry an `_attachments` map keyed by property name.
The value is **always a list** — even a single-attachment property reports
a one-element array, matching how `list:` properties and `_relations` are
always arrays. Only `file` properties that actually carry a file appear.
Same closed-world present/absent semantics as `_fields` / `_relations`: present
(possibly empty) on every per-entity response (GET, PATCH, POST create,
clone), absent on list rows.

```json
{
  "id": "TKT-001",
  "type": "ticket",
  "properties": {
    "screenshot": "attachments/TKT-001/screenshot/shot.png",
    "docs": [
      "attachments/TKT-001/docs/a.pdf",
      "attachments/TKT-001/docs/b.pdf"
    ]
  },
  "_attachments": {
    "screenshot": [
      { "id": "shot.png", "filename": "shot.png", "size": 20480,
        "contentType": "image/png",
        "href": "/api/v1/tickets/TKT-001/_attachments/screenshot/shot.png" }
    ],
    "docs": [
      { "id": "a.pdf", "filename": "a.pdf", "size": 1024,
        "contentType": "application/pdf",
        "href": "/api/v1/tickets/TKT-001/_attachments/docs/a.pdf" },
      { "id": "b.pdf", "filename": "b.pdf", "size": 2048,
        "contentType": "application/pdf",
        "href": "/api/v1/tickets/TKT-001/_attachments/docs/b.pdf" }
    ]
  }
}
```

`id` is the file's identifier within the property (its normalized file
name) — used to build the per-file download/delete URL. `contentType` is
inferred from the filename extension (the store does not persist it on
every backend). `size` is in bytes. The `properties` value is a scalar
path for a single-cap property and a list of paths for a multi-cap one;
treat both as opaque and use `_attachments` instead.

### Download endpoint

Clients fetch the bytes via the `href` from each `_attachments` entry —
treat it as opaque. The URL shape below documents what the server emits;
do **not** hand-build it from the property path string or `(id, property,
fileName)`.

```text
GET <_attachments[property][i].href>
  == GET /api/v1/{plural}/{id}/_attachments/{property}/{fileName}
```

Streams one attachment's bytes. **Access inherits the owning entity's read
permission** — a caller who cannot read the entity gets `404` (never
`403`, and byte-identical to a genuinely missing attachment, so existence
is not leaked). The bytes resolve from `(id, property, fileName)` only; no
caller-supplied path reaches the filesystem (the store rejects separators
in the file name), so there is no path-traversal surface and a renamed
entity resolves by its current id.

Response headers: `Content-Type` (inferred from the filename),
`Content-Disposition: inline; filename="…"` (sanitized), plus
`X-Content-Type-Options: nosniff` and a `Content-Security-Policy: sandbox`
so user-supplied bytes (e.g. an SVG/HTML payload) cannot execute as stored
XSS in the app origin.

### Upload endpoint

```text
PUT  /api/v1/{plural}/{id}/_attachments/{property}
POST /api/v1/{plural}/{id}/_attachments/{property}
```

`multipart/form-data` with a single `file` field. The response is the
updated entity (its `_attachments` reflects the new file). Behavior depends
on the property's `max`:

- **`max == 1`** (default): the upload **replaces** the existing file.
- **`max > 1`**: the upload **appends**, up to `max`. A file whose
  (normalized) name already exists is **auto-suffixed** (`report.pdf` →
  `report (1).pdf`) so it never overwrites a sibling. Uploading past the cap
  returns `409 attachment_limit`.

**Access inherits the owning entity's `update` permission** — an
attachment write mutates the entity, so it is authorized as an `update`,
re-checked server-side **before any bytes are written** (a deny never
orphans a file). A caller who cannot read the entity gets the same uniform
`404` as the read path; a caller who can read but not update gets `403`.

Size limits: the request is capped at ingress (`413 attachment_too_large`,
`application/problem+json`) by a default of 64 MiB, overridable per
deployment via the data-entry config key `app.max_attachment_bytes`. Every
store backend also enforces `store.MaxAttachmentBytes` as a backstop, so no
storage path is ever unbounded. Other failures: `400` for a malformed
multipart body or a missing `file` field; `422` for a validation failure
when persisting the property.

### Delete endpoint

```text
DELETE /api/v1/{plural}/{id}/_attachments/{property}/{fileName}
```

Removes one file and re-stamps the property from the remaining files;
returns `204`. Idempotent (deleting a missing file still re-stamps and
succeeds). Same `update`-permission inheritance as upload. The bytes are
removed, then the property is persisted, so a persist failure leaves
orphaned bytes rather than a property pointing at a missing file.
