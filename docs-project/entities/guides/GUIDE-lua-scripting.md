---
audience: advanced
id: GUIDE-lua-scripting
order: 10
status: published
summary: Programmable automation with embedded Lua
title: Lua Scripting
type: guide
---

Rela includes an embedded Lua scripting runtime for programmable access to the entity graph.
Scripts can query, mutate, analyze, and export data—enabling automation beyond what's possible
with the CLI or MCP tools alone.

## Quick Start

### Via MCP (AI Assistants)

```lua
-- lua_eval: Execute inline Lua code
local tickets = rela.list_entities("ticket", "status=open")
rela.output({count = #tickets})
```

### Via CLI

```bash
# Run a script file
rela lua scripts/report.lua

# Pass arguments
rela lua scripts/migrate.lua --from=v1 --to=v2

# Run an interactive flow (with user prompts)
rela flow scripts/create-ticket.lua
```

### Shebang Support

Scripts can include a shebang line for direct execution from the command line:

```lua
#!/usr/bin/env -S rela script
local entities = rela.list_entities("ticket", "status=open")
rela.output({count = #entities})
```

```bash
chmod +x scripts/report.lua
./scripts/report.lua
```

The shebang line is automatically stripped before execution. Line numbers in error
messages remain accurate.

### Script Location

Scripts executed via `lua_run` must be in the `scripts/` directory:

```text
project/
├── scripts/
│   ├── report.lua
│   └── utils/
│       └── helpers.lua
├── entities/
└── schema.yaml
```

## Interactive Flows

Interactive flows allow Lua scripts to present forms to users and receive input. The script
suspends at each form, waits for user input, then continues execution. This enables multi-step
wizards, guided entity creation, and interactive data collection.

### Running a Flow

```bash
rela flow scripts/create-ticket.lua
```

### Executable Scripts

Flow scripts can be made directly executable with a shebang:

```lua
#!/usr/bin/env -S rela flow
-- Your flow script here

local event = rela.flow.emit({
    type = "form",
    -- ...
})
```

Then run directly:

```bash
chmod +x scripts/create-ticket.lua
./scripts/create-ticket.lua
```

### The emit() Function

Use `rela.flow.emit()` to present a form and wait for user response:

```lua
local event = rela.flow.emit({
    type = "form",
    title = "Create Ticket",
    fields = {
        {name = "title", type = "text", required = true},
        {name = "priority", type = "select",
         options = {{"high", "High"}, {"medium", "Medium"}, {"low", "Low"}}},
    },
    actions = {
        {"submit", "Create"},
        {"cancel", "Cancel"},
    },
})

if event.action == "cancel" then
    return
end

rela.create_entity("ticket", {
    title = event.data.title,
    priority = event.data.priority,
})
```

### Form Specification

| Property | Type | Required | Description |
|----------|------|----------|-------------|
| `type` | string | yes | Must be `"form"` |
| `title` | string | no | Form title |
| `description` | string | no | Help text shown below title |
| `fields` | array | yes | Field definitions (see below) |
| `actions` | array | yes | Action buttons (see below) |

### Field Types

| Type | Options | Returns |
|------|---------|---------|
| `text` | `required`, `default`, `placeholder`, `lines` | string |
| `select` | `options`, `required`, `default` | selected value |
| `multi-select` | `options`, `required`, `default` | array of values |
| `boolean` | `default` | true/false |
| `number` | `required`, `default`, `min`, `max`, `step` | number |
| `date` | `required`, `default`, `min`, `max` | "YYYY-MM-DD" |
| `markdown` | `content`, `label` | (display only, no data) |

### Field Properties

| Property | Type | Description |
|----------|------|-------------|
| `name` | string | Field identifier (required, except for markdown) |
| `type` | string | Field type (required) |
| `label` | string | Display label (defaults to the raw field name) |
| `content` | string | Markdown content (required for markdown fields) |
| `required` | boolean | Whether field is required |
| `default` | varies | Default value |
| `placeholder` | string | Placeholder text (text fields only) |
| `lines` | number | Number of lines for textarea (text fields only) |
| `options` | array | Options for select/multi-select: `{{"value", "Label"}, ...}` |
| `min`, `max` | number/string | Bounds for number or date fields |
| `step` | number | Step increment for number fields |

### Markdown Fields

Markdown fields display formatted text within a form. They don't collect user input—use them to
provide instructions, context, or visual separators between input fields:

```lua
fields = {
    {type = "markdown", content = "## Instructions\nPlease fill out the form below."},
    {name = "title", type = "text", required = true},
    {type = "markdown", content = "---\n*Additional options:*"},
    {name = "priority", type = "select", options = {{"high", "High"}, {"low", "Low"}}},
    {type = "markdown", label = "Note", content = "Fields marked with * are required."},
}
```

Markdown fields support:

- `content` (required): The markdown text to display
- `label` (optional): A title shown above the content

### Actions

Actions are defined as tuples: `{id, label}` or `{id, label, style}`:

```lua
actions = {
    {"submit", "Create"},           -- Default style
    {"cancel", "Cancel", "warning"}, -- Warning style
}
```

Styles: `primary`, `warning`, `danger`

### Event Response

When a form is submitted, `emit()` returns an event table:

```lua
{
    action = "submit",  -- The action ID the user chose
    data = {            -- Field values (for submit-like actions)
        title = "My Ticket",
        priority = "high",
    },
}
```

### Multi-Step Flows

Scripts can present multiple forms in sequence:

```lua
local data = {}

-- Step 1: Basic info
local e1 = rela.flow.emit({
    type = "form",
    title = "Step 1: Basic Info",
    fields = {
        {name = "title", type = "text", required = true},
        {name = "kind", type = "select",
         options = {{"bug", "Bug"}, {"feature", "Feature"}}},
    },
    actions = {{"next", "Next"}, {"cancel", "Cancel"}},
})
if e1.action == "cancel" then return end
data.title = e1.data.title
data.kind = e1.data.kind

-- Step 2: Details
local e2 = rela.flow.emit({
    type = "form",
    title = "Step 2: Details",
    fields = {
        {name = "description", type = "text", lines = 5},
        {name = "priority", type = "select",
         options = {{"high", "High"}, {"medium", "Medium"}, {"low", "Low"}}},
    },
    actions = {{"back", "Back"}, {"submit", "Create"}, {"cancel", "Cancel"}},
})
if e2.action == "cancel" then return end
if e2.action == "back" then
    -- Handle back navigation (use goto or restructure as loop)
end
data.description = e2.data.description
data.priority = e2.data.priority

rela.create_entity("ticket", data)
```

### Error Handling

Validation errors (invalid form spec) raise Lua errors. User cancellation is handled via
actions, not errors:

```lua
-- Handle cancel action explicitly
if event.action == "cancel" then
    print("User cancelled")
    return
end
```

Transport errors (e.g., terminal not interactive) also raise Lua errors.

## API Reference

### Query Functions

| Function | Description | Returns |
|----------|-------------|---------|
| `rela.get_entity(id)` | Get entity by ID | table or nil |
| `rela.list_entities(type, opts?)` | List entities of a type (bounded) | table (array) |
| `rela.search(query, limit?)` | Full-text search | table (array) |
| `rela.get_relations(opts?)` | Get relations with filters | table (array) |
| `rela.trace_from(id, depth?)` | Trace outgoing dependencies | table (tree) |
| `rela.trace_to(id, depth?)` | Trace incoming dependencies | table (tree) |
| `rela.find_path(from, to)` | Find shortest path | table (array) or nil |

#### Reads are bounded

**`rela.list_entities` never returns more than 2000 rows.** There is no
spelling for "give me everything" — an unbounded read of a large type is the
failure this bound exists to prevent, and leaving it as the default would make
the dangerous path the shortest one.

```lua
local tickets = rela.list_entities("ticket")                      -- up to 2000
local recent  = rela.list_entities("ticket", { limit = 50 })      -- up to 50
local open    = rela.list_entities("ticket", "status=open")       -- filter shorthand
local both    = rela.list_entities("ticket",
                  { filter = "status=open", limit = 50 })
```

`limit` may lower the bound but never raise it: asking for more than 2000
clamps rather than erroring, so a script can say "as much as possible" without
hard-coding the number. `limit = 0` is **an error, not "unbounded"** — it means
unbounded on `store.ListEntitiesPage`, and silently inheriting the opposite
meaning here would be the worst kind of near-miss.

The bound stops the read at the source. Rows past the limit are never loaded,
gated, or converted — this is a real saving, not a slice after the fact.

**Iterating past the bound is not yet possible.** Cursor paging is planned;
until it lands, `{cursor = ...}` is *rejected* rather than accepted-and-ignored,
so a script written against the future API fails loudly here instead of
silently re-reading the first page forever. The same applies to any unknown
option: a typo'd key raises rather than being skipped.

One caveat worth knowing: with a `filter`, the bound applies to rows
**examined**, so a `limit` of 50 can return fewer than 50 matches. Without a
filter it is exact.

#### Relation filters

`rela.get_relations` takes an **options table** — `{from = ..., type = ...,
to = ...}` — where each key is optional and must be a **string**. Omitting a
key means "no constraint on that field"; omitting the table entirely returns
every relation.

```lua
local rels = rela.get_relations({ from = e.id, type = "implements" })
```

A non-string option raises rather than being ignored, because silently
dropping it would widen the query to the whole graph while the script reads
the answer as filtered:

```lua
rela.get_relations({ from = 12345 })   -- error: option "from" must be a string
rela.get_relations(e.id)               -- NOT a filter: a bare id is not a
                                       -- table, so this returns EVERY relation
```

### Mutation Functions

| Function | Description | Returns |
|----------|-------------|---------|
| `rela.create_entity(type, props, content?, id?, opts?)` | Create entity | table, warnings? |
| `rela.update_entity(id, props, content?)` | Update entity | table, warnings? |
| `rela.delete_entity(id, cascade?)` | Delete entity | boolean |
| `rela.create_relation(from, type, to, opts?)` | Create relation | table |
| `rela.delete_relation(from, type, to)` | Delete relation | boolean |
| `rela.refresh()` | Reload graph from disk | boolean |

#### Writing to a content state (face)

When an entity type declares [content states](content-states.md), a create
must name the one it writes. Such a type stores no row at a default
coordinate, so there is nothing to fall back to. Pass the face in the
trailing options table:

```lua
-- An entity type declaring `faces: {draft, published}`
local policy = rela.create_entity("policy", {title = "Access control"},
                                  "body", nil, { face = "draft" })
policy.face  --> "draft"

-- A `scope: content` relation belongs to one face of its SOURCE
rela.create_relation(policy.id, "cites", "SRC-1", { face = "draft" })
```

Options accepted:

| Binding | Keys |
|---------|------|
| `rela.create_entity` | `face` |
| `rela.create_relation` | `face`, `content` |

The name must be one the type declares. A face on a type that declares none
is refused, as is a `scope: identity` relation given a face — such an edge
attaches to the entity rather than to one of its states.

The two creates differ in whether a face is **required**. An entity create on
a faced type must name one: the type stores no row at a default coordinate, so
there is nothing to fall back to. A relation create may omit it — the edge
then attaches at the identity coordinate, which is a real and readable edge
rather than a missing row. Omitting it on a `scope: content` type usually
means the edge is not where you want it, but it is addressable, and it is what
every caller that cannot yet name a face (`rela link`, the MCP tool) relies
on. Unknown keys and
non-string values raise rather than being ignored, so a typo cannot quietly
become "no face". A script names a face **directly**; it is never derived
from a world, because a world resolves through a chain that can answer with
a fallback and a write must name the row it changes.

Both read-side tables carry the state they were read at — `entity.face` and
`relation.from_face`, empty on the default face — so a script can create a
sibling on the same face as something it just read. Relations have no
`to_face`: targets are faceless by construction.

#### Addressing a face on the other write bindings

The three id-addressed bindings differ, and the differences matter:

| Binding | A `ID@face` address |
|---------|---------------------|
| `rela.update_entity` | Selects that face — intended behaviour |
| `rela.delete_entity` | **Deletes the whole entity**, every face — intended behaviour |
| `rela.delete_relation` | Ignored; removes the **default** face's edge and reports success — **known defect, BUG-YVU8CP** |

The first two rows describe the design. The third is a bug awaiting a fix, not
a contract: do not build on it. A per-face delete is not currently expressible
from Lua.

#### Validation warnings (multi-return)

`rela.create_entity` and `rela.update_entity` return TWO values per
[DEC-HWZHA](../tickets/entities/decisions/DEC-HWZHA.md): the
entity table (always present on success) and an optional warnings
table (`nil` when there are no warnings).

The contract follows **`string.gsub`** semantics — both returns can
be non-nil simultaneously, and the second is *additional success
information*, NOT an error indicator. This is the opposite of
`io.open`'s `(file, err)` shape, where the two are mutually
exclusive.

```lua
-- Existing scripts (single return) keep working:
local e = rela.update_entity("TKT-001", {title = "x"})
print(e.id)

-- Read warnings when you care:
local e, warnings = rela.update_entity("TKT-001", {title = ""})
-- e.id is set; the entity was persisted with title cleared.
-- warnings is a table when validation surfaced soft conditions:
for _, w in ipairs(warnings or {}) do
    print(w.code, w.path, w.detail)
    -- e.g. required_property_unset  /properties/title  This field is required
end
```

Each warning is `{code = string, path = string, detail = string}`.
Warning codes match those in
[`docs/data-entry/api-reference.md`](data-entry/api-reference.md).

**Hard errors still raise**. An entity with an unknown type or a
malformed ID prefix raises a Lua error — `pcall` catches it. Soft
validation conditions (missing required field, invalid enum value,
bad date) no longer raise; they show up in the warnings return so
the script can decide what to do.

```lua
-- Hard errors raise:
local ok, err = pcall(rela.update_entity, "BAD-PREFIX", {})
-- ok=false, err="entity not found: BAD-PREFIX"

-- Soft conditions don't raise:
local ok, e, warnings = pcall(rela.update_entity, "TKT-001", {title = ""})
-- ok=true, e is the entity, warnings is the validation findings.
```

### Elevated access — `rela.bypass_acl`

By default an automation script's reads **and** writes are **ACL-checked
against the triggering principal** (the user whose action fired the
automation). That is correct for most automations. But some scripts enforce a
*system invariant* on behalf of a user who isn't authorized to act directly —
e.g. stamping `submitter --created-by--> ticket` on ticket create so the
submitter (and only they) can later read their own ticket, without the
submitter being able to write `person` relations themselves.

For those, `rela.bypass_acl(fn)` runs `fn` with a single argument `admin`: a
handle whose reads and writes **skip the ACL**. Elevation is scoped to the
closure and carried by the `admin` object — the ordinary `rela.*` bindings are
never elevated.

```lua
-- on ticket create: stamp the submitter as author, server-side, unforgeable
local me = rela.principal.user
rela.bypass_acl(function(admin)
  admin.create_relation(me, "created-by", entity.id)
end)
-- back to normal (gated) authority here
```

`admin` exposes:

| Method | Purpose |
|--------|---------|
| `admin.create_relation(from, type, to, opts?)` | Link, skipping the ACL deny |
| `admin.delete_relation(from, type, to)` | Unlink, skipping the ACL deny |
| `admin.delete_entity(id, cascade?)` | Remove, skipping the ACL deny |
| `admin.get_entity(id)` | Read **raw** — full properties, no redaction |
| `admin.list_entities(type)` | Every entity of `type`, ungated |
| `admin.get_relations(opts?)` | Every matching edge, not peer-gated |

**Elevated reads return raw data.** `admin.get_entity` returns the entity with
all properties, including ones the triggering user cannot see; `list_entities`
returns rows the gated `rela.list_entities` would drop; `get_relations` returns
edges even when neither endpoint is visible to the caller. A half-elevated read
would be a confusing contract — the closure is the boundary.

Use them when an automation genuinely needs a whole-graph view, e.g. checking a
uniqueness invariant across entities the submitter cannot see:

```lua
rela.bypass_acl(function(admin)
  for _, t in ipairs(admin.list_entities("ticket")) do
    if t.properties.external_ref == entity.properties.external_ref
       and t.id ~= entity.id then
      error("duplicate external_ref: " .. t.id)
    end
  end
end)
```

Note the two reads differ deliberately in what a `nil` means:
`rela.get_entity` returns `nil` for "missing **or** hidden" (it stays
oracle-free), while `admin.get_entity` returns `nil` only for a genuine
miss. A store failure (backend down, driver error) **raises** rather than
returning `nil` — otherwise the uniqueness check above would read a
transient outage as "no duplicate" and admit one, silently violating the
invariant it exists to enforce. All three `admin` read methods raise on
store errors.

Mistyped filter options raise too: `admin.get_relations({from = 12345})` is
an error, not an unfiltered whole-graph dump. The gated `rela.get_relations`
applies the same rule, from the same code — the two cannot drift on what a
filter means.

**Operator opt-in is required.** `rela.bypass_acl` only exists when the
automation action sets `allow_acl_bypass:` in `schema.yaml` (an operator-only
file). Without it the function is absent and a script cannot elevate:

```yaml
automations:
  - name: stamp-ticket-author
    on: { entity: [ticket], created: true }
    do:
      - lua_file: stamp-author.lua
        allow_acl_bypass: read+write
```

The value selects which capabilities the `admin` handle carries:

| Value | `admin` methods | Typical use |
|---|---|---|
| `read` | `get_entity`, `list_entities`, `get_relations` | aggregate over rows the caller cannot see |
| `write` | `create_relation`, `delete_relation`, `delete_entity` | enforce a system invariant |
| `read+write` | both | the general case |

Methods a value does not grant are **absent from the handle**, not present and
failing — so `admin.delete_entity` under `allow_acl_bypass: read` is `nil`, and
"this script cannot mutate" is structural rather than a promise.

The flag is a **rough guard for review, not a permission model**. Its job is to
tell whoever deploys the script whether its bypass block needs reading
carefully: no `allow_acl_bypass:` means the script can only do what the
invoking principal can, so the ACL already bounds it; a value means someone
should read that closure before it ships. The value says which *kind* of
scrutiny — is this reading data the principal cannot see, or writing past their
permissions? That is why it is not sliced by verb.

> **Migrating from the boolean.** `allow_acl_bypass: true` used to mean reads
> and writes. It is now **refused at load** with an error naming the
> replacement; run `rela migrate` to rewrite it to `read+write`. The boolean is
> not silently accepted on purpose: this field grants ACL bypass, and quietly
> mapping a legacy value to the broadest setting is the wrong default for a
> privilege field.

### Elevated reads in a document render

A `documents:` entry may declare `allow_acl_bypass: read`, which lets its
script read entities the requesting principal cannot see. This exists for
reports that must compute over hidden rows — benchmarking a sales manager
against peers whose clients are invisible to them, say — which no `acl.yaml`
role can express, because granting enough to *compute* the benchmark grants
enough to *enumerate* the competitors.

```yaml
documents:
  sales_review:
    title: "Verkooprapportage"
    script: docs/sales_review.lua
    allow_acl_bypass: read
    permission: report:sales   # REQUIRED when elevated
```

Three rules, all enforced at config load:

- **Only `read`.** A render is served on a `GET`; `write` and `read+write` are
  a config error. Elevated writes there would not be idempotent (browsers
  prefetch, users refresh, the SPA retries) and would foreclose caching a
  principal-independent render, so they belong in an automation action or a
  schedule. This prevents a render mutating *beyond* the caller's permissions;
  the ordinary gated `rela.*` write bindings remain available to a document
  script.
- **`permission:` is required.** Without it the render would serve whatever the
  script reads to every principal. This is the one place a document's
  `permission:` is mandatory — see `docs/data-entry.md`.
- **`script:` only.** A `command:` renderer is an external process that never
  receives the Lua bindings elevation unlocks.

**The script is trusted code.** `bypass_acl` hands it a raw reader and nothing
stops it printing what it reads, so `permission: report:sales` grants "may read
whatever this script reads", not "may view this report". Review the bypass
block before deploying — that review *is* the mitigation.

**Safety properties:**

- **Audited, not anonymous.** Every elevated write records an `acl-bypass`
  audit row carrying the **real** triggering principal (not a system user) plus
  `acl_bypass=true`, so "who caused this elevated write" is always answerable.
- **Elevated reads are audited too.** A closure that used any of the `admin`
  read methods emits one `acl-bypass-read` row naming the principal, the
  automation, and which bindings were used. It is **one row per closure**, not
  per read — a single `admin.list_entities` can span the graph — and it
  deliberately records **no subject and no entity data**, since logging the
  read set would copy the very data the ACL protects into the audit log. The
  row is written even when the closure then raises, so failing is not a way to
  erase the evidence. Isolate them with `op == "acl-bypass-read"`.
- **No leak into nested cascades.** A cascade that an elevated write triggers
  runs with normal (gated) ACL authority — elevation does not propagate to
  descendant writes.
- **Closure-scoped.** After `fn` returns, `admin` is invalidated; stashing it in
  a global and calling it later raises — for reads as well as writes, so
  elevation cannot become a durable ungated handle.
- **Cannot forge identity.** `rela.principal` is read-only and the write
  attribution derives from the request context, never from the script.
- **Fails loudly, never silently degraded.** If a deployment grants elevated
  writes but no elevated read capability, the `admin` read methods **raise**
  rather than falling back to the caller's gated view. A closure that looked
  elevated but silently returned a partial graph would be worse than one that
  refuses.

### Schema Functions

| Function | Description | Returns |
|----------|-------------|---------|
| `rela.get_entity_types()` | Get all entity type definitions | table |
| `rela.get_relation_types()` | Get all relation type definitions | table |

### Output Functions

| Function | Description |
|----------|-------------|
| `rela.output(data)` | Output data as JSON to stdout |
| `rela.write_file(path, content)` | Write file to `output/` directory |

### Flow Functions

| Function | Description | Returns |
|----------|-------------|---------|
| `rela.flow.emit(screen)` | Present form, wait for user input | event table |

See [Interactive Flows](#interactive-flows) for detailed documentation.

### Utility Functions

| Function | Description | Returns |
|----------|-------------|---------|
| `rela.days_since(date)` | Days between date and today | number (-1 if invalid) |
| `rela.sort_entities(list, prop, dir?)` | Sort entities by property | sorted table |

### Date Functions

| Function | Description | Returns |
|----------|-------------|---------|
| `rela.date_add(date, offset)` | Add offset to date | date string |
| `rela.date_weekday(date)` | Get weekday name | lowercase string |
| `rela.date_next_weekday(date, day)` | Next occurrence of weekday after date | date string |
| `rela.rrule_next(rrule, after?)` | Next RRULE occurrence (default: after today) | date string or nil |

#### date_add

Add a duration offset to a date. Offsets use `Nd`, `Nw`, `Nm`, `Ny` format. Negative values
are supported.

```lua
rela.date_add("2025-01-15", "7d")   -- "2025-01-22"
rela.date_add("2025-01-15", "2w")   -- "2025-01-29"
rela.date_add("2025-01-15", "1m")   -- "2025-02-15"
rela.date_add("2025-01-15", "1y")   -- "2026-01-15"
rela.date_add("2025-01-15", "-3d")  -- "2025-01-12"
```

#### date_weekday

Returns the lowercase weekday name for a date.

```lua
rela.date_weekday("2025-01-06")  -- "monday"
```

#### date_next_weekday

Returns the next occurrence of the given weekday strictly after the date. If the date is
already that weekday, it advances to the following week.

```lua
rela.date_next_weekday("2025-01-06", "friday")  -- "2025-01-10"
rela.date_next_weekday("2025-01-06", "monday")  -- "2025-01-13" (not same day)
```

#### rrule_next

Computes the next occurrence of an iCal RRULE (RFC 5545) after a given date. If no `after`
date is provided, uses today. Returns `nil` if the rule has no more occurrences.

Accepts both `RRULE:FREQ=...` and bare `FREQ=...` formats.

**Important:** Rules with `INTERVAL` > 1 must include `DTSTART` to anchor the interval
cadence. Without it, the function raises an error.

```lua
-- Next Saturday
rela.rrule_next("FREQ=WEEKLY;BYDAY=SA;DTSTART=20250101T000000Z", "2025-01-06")
-- "2025-01-11"

-- 1st of each month
rela.rrule_next("FREQ=MONTHLY;BYMONTHDAY=1;DTSTART=20250101T000000Z", "2025-01-15")
-- "2025-02-01"

-- Last day of each month
rela.rrule_next("FREQ=MONTHLY;BYMONTHDAY=-1;DTSTART=20250101T000000Z", "2025-01-15")
-- "2025-01-31"

-- Every 2 weeks (INTERVAL > 1 requires DTSTART)
rela.rrule_next("FREQ=WEEKLY;INTERVAL=2;DTSTART=20250106T000000Z", "2025-01-06")
-- "2025-01-20"

-- 1st Saturday every 3 months (INTERVAL > 1 requires DTSTART)
rela.rrule_next("FREQ=MONTHLY;INTERVAL=3;BYDAY=1SA;DTSTART=20250101T000000Z", "2025-01-06")
-- "2025-04-05"

-- Uses today if no after date
rela.rrule_next("FREQ=DAILY")  -- tomorrow's date
```

### AI Functions

The `ai` module provides access to LLM providers (OpenAI, ollama, etc.) configured
via `.rela/ai.yaml`. These functions are available when AI is configured; otherwise
they return a `not_configured` error.

| Function | Description | Returns |
|----------|-------------|---------|
| `ai.chat(opts)` | Chat completion | (result_table, nil) or (nil, err_table) |
| `ai.complete(prompt)` | Single-message chat (convenience) | (string, nil) or (nil, err_table) |
| `ai.embed(input, opts?)` | Compute vector embeddings | (array_of_arrays, nil) or (nil, err_table) |

#### Configuration

Create `.rela/ai.yaml` (gitignored, per-user):

```yaml
base_url: http://127.0.0.1:11434/v1   # required
model: gemma3:12b                      # required, default chat model
embedding_model: nomic-embed-text      # optional, falls back to model
api_key_env: OPENAI_API_KEY            # optional, absent = no auth
timeout_seconds: 60                    # optional, default 30
```

#### ai.chat

Send a chat completion request with full control over messages, model, and parameters.

```lua
local result, err = ai.chat({
  messages = {
    {role = "system", content = "You are concise."},
    {role = "user",   content = "What is 2+2?"},
  },
  model = "gemma3:12b",   -- optional, falls back to config
  temperature = 0,        -- optional; 0 is distinct from unset
  max_tokens = 50,        -- optional
})
if err then
  print("Error: " .. err.kind .. ": " .. err.message)
else
  print(result.content)
  -- result also has: model, finish_reason, usage (sub-table)
end
```

#### ai.complete

Convenience wrapper: sends a single user message and returns just the content string.

```lua
local text, err = ai.complete("Summarize: " .. entity.content)
```

#### ai.embed

Compute vector embeddings for one or more texts. Always returns an array of arrays
(one vector per input), even for a single string input.

```lua
-- Single text
local vecs, err = ai.embed("hello world")
-- vecs[1] is the embedding vector (array of numbers)

-- Batch (one HTTP call for many texts, more efficient)
local vecs, err = ai.embed({"first", "second", "third"})
-- vecs[1], vecs[2], vecs[3] are the embedding vectors

-- Model override
local vecs, err = ai.embed("text", {model = "nomic-embed-text"})
```

**Limits:** Batch input is capped at 2048 texts. Empty strings and empty tables
raise a programming error.

#### Error Handling

AI functions return `(nil, err_table)` for expected runtime failures (network errors,
rate limits, missing config) instead of raising. This is a **deliberate deviation** from
other rela bindings — AI calls are network-bound and scripts should handle failure inline.

Programming errors (wrong argument types, empty messages) still raise via `error()`.

The error table has stable fields for branching:

| Field | Type | Description |
|-------|------|-------------|
| `kind` | string | Error category (see below) |
| `status` | number | HTTP status code (0 for non-HTTP errors) |
| `message` | string | Human-readable description |
| `retry_after` | number | Seconds to wait (for rate limits) |
| `details` | string | Underlying transport error (if any) |

| `err.kind` | When |
|---|---|
| `not_configured` | No `.rela/ai.yaml` or it failed to load |
| `auth` | API key missing/invalid; HTTP 401/403 |
| `bad_request` | HTTP 400/4xx; unknown model |
| `rate_limited` | HTTP 429; check `err.retry_after` |
| `server_error` | HTTP 5xx |
| `timeout` | Request exceeded deadline |
| `network` | DNS, connection refused, TLS |
| `bad_response` | Non-JSON, malformed response |

```lua
local result, err = ai.chat({messages = {{role="user", content="hi"}}})
if err then
  if err.kind == "rate_limited" then
    print("Rate limited, retry after " .. err.retry_after .. "s")
  elseif err.kind == "not_configured" then
    print("AI not configured — create .rela/ai.yaml")
  else
    print("AI error: " .. err.message)
  end
end
```

### HTTP Functions

The `http` module provides an HTTP client for calling external APIs. No
configuration is needed — unlike `ai.*`, the module works out of the box.

| Function | Description | Returns |
|----------|-------------|---------|
| `http.request(opts)` | Full request: url/method/headers/body/timeout | (response, nil) or (nil, err_table) |
| `http.get(url, opts?)` | GET convenience | (response, nil) or (nil, err_table) |
| `http.post(url, body, opts?)` | POST convenience | (response, nil) or (nil, err_table) |
| `http.put(url, body, opts?)` | PUT convenience | (response, nil) or (nil, err_table) |
| `http.patch(url, body, opts?)` | PATCH convenience | (response, nil) or (nil, err_table) |
| `http.delete(url, opts?)` | DELETE convenience | (response, nil) or (nil, err_table) |

For request/response bodies, use `rela.json.encode` / `rela.json.decode` —
see [JSON Functions](#json-functions).

#### http.request

```lua
local resp, err = http.request({
  url     = "https://api.example.com/data",
  method  = "POST",                          -- optional, default GET
  headers = {["Content-Type"] = "application/json"},
  body    = rela.json.encode({key = "value"}),
  timeout = 10,                              -- optional, seconds
})
```

#### Form Encoding and Basic Auth

Not every API takes JSON. `form` builds a `multipart/form-data` body and
`basic_auth` sets an `Authorization: Basic` header:

```lua
local resp, err = http.request({
  url        = "https://api.mailgun.net/v3/mg.example.com/messages",
  method     = "POST",
  form       = {subject = "Hello", text = "hi"},
  basic_auth = {user = "api", pass = rela.secrets.mailgun_key},
})
```

`form` accepts two shapes. A map is what you usually want; parts are sorted by
name so the body is deterministic:

```lua
form = {subject = "Hello", text = "hi"}
```

An array of `{name, value}` pairs when you need a **repeated field name**,
which form encoding permits and a Lua map cannot express:

```lua
form = {
  {"to", "a@example.com"},
  {"to", "b@example.com"},
  {"subject", "Hello"},
}
```

Notes:

- `form` and `body` are **mutually exclusive** — setting both raises, rather
  than silently picking one and sending half of what you meant.
- The generated `Content-Type` (with its boundary) always wins over one you set
  in `headers`; a declared boundary that does not match the body is unparseable
  at the far end.
- Form values must be strings. Numbers raise — use `tostring()`, so which
  rendering you meant is explicit rather than a float-formatting accident.
- `basic_auth.user` is required; `basic_auth.pass` may be empty, for APIs that
  authenticate with a token as the username.
- Both are available on `http.request` and on every convenience method.

#### Response Shape

`resp` is a table:

| Field | Type | Description |
|-------|------|-------------|
| `status_code` | number | HTTP status (200, 404, etc.) |
| `status` | string | Status line ("200 OK") |
| `headers` | table | Lowercase keys, first-value-wins for multi-value headers |
| `body` | string | Response body (capped at 10 MiB) |

#### Convenience Methods

```lua
local resp, err = http.get("https://api.example.com/users")
local resp, err = http.post(
    "https://api.example.com/items",
    rela.json.encode({name = "new item"}),
    {headers = {["Content-Type"] = "application/json"}}
)
```

#### Error Handling

HTTP functions follow the same `(nil, err_table)` convention as `ai.chat`.
The error table mirrors `ai.chat`'s shape (minus `status`, which always
absent on http errors — non-2xx responses come back as a normal response
table with `status_code` populated):

| Field | Type | Notes |
|-------|------|-------|
| `kind` | string | One of `timeout`, `canceled`, `network`, `bad_response` |
| `message` | string | Human-readable summary |
| `retry_after` | number | Always `0` for HTTP errors (populated by `ai` on rate limits) |
| `details` | string | Unwrapped transport error (TLS cert, DNS record, etc.) when present |

| `err.kind` | When |
|---|---|
| `timeout` | Request or body read exceeded the deadline |
| `canceled` | Request was canceled (runtime shutting down) |
| `network` | DNS failure, connection refused, TLS error, generic body read error |
| `bad_response` | Response body exceeded the 10 MiB cap |

Programming errors (missing URL, URL with userinfo, wrong argument types,
invalid HTTP method, non-positive timeout) raise Lua errors.

#### Redirects

HTTP redirects are **not** followed automatically. The 3xx response is
returned directly so scripts can inspect the `Location` header and
follow redirects explicitly if needed.

#### Security

`http.*` is a new threat surface: a Lua script can make arbitrary
outbound requests using the user's machine. There is no SSRF protection
(the localhost / private-IP range is reachable). URLs containing
embedded credentials (`http://user:pass@host/`) are rejected — set the
`Authorization` header explicitly. Treat Lua scripts as trusted code.

### Crypto Functions

The `crypto` module provides hashing and encoding primitives. Always available,
no capability needed — nothing here reaches outside the process.

| Function | Description | Returns |
|----------|-------------|---------|
| `crypto.sha256_hex(data)` | SHA-256, lowercase hex | string |
| `crypto.hmac_sha256_base64(key, msg)` | HMAC-SHA256, standard base64 | string |
| `crypto.base64_encode(data)` | Standard base64, padded | string |
| `crypto.base64_decode(s)` | Decode base64 | (string, nil) or (nil, err_table) |

```lua
local encoded = crypto.base64_encode("foobar")     -- "Zm9vYmFy"
local decoded, err = crypto.base64_decode(encoded) -- "foobar", nil
```

Lua strings are byte strings, so binary data (an image, a signature) round-trips
intact.

`base64_decode` is the one function here that returns an **error pair** rather
than raising, because it is the one that takes untrusted input — an API
response, a header, a file. Malformed base64 from a remote is an expected
runtime condition a script must be able to branch on, not a programming error.
It accepts the padded, unpadded and URL-safe alphabets, since a script decoding
someone else's output does not get to choose which it receives. `base64_encode`
emits the padded standard alphabet only.

The other three raise on a wrong-type argument, as `rela.json.encode` does.

### Mail Functions

`mail.send` delivers a message through whichever transport the project
configured in `.rela/mail.yaml`. A script cannot reach a destination the
operator did not set up.

**`mail.send` requires the `mail` capability.** Like `http` and `ai`, it is not
granted by default — see [Capabilities](#capabilities--http-ai-mail-secrets-write_file)
below.

```yaml
# data-entry.yaml
actions:
  send_digest:
    script: digest.lua
    capabilities:
      mail: true
```

```lua
local ok, err = mail.send{
  to      = "alice@example.com",      -- or {"a@example.com", "b@example.com"}
  subject = "Report ready",
  html    = "<p>Done.</p>",
  text    = "Done.",
}
if not ok then
  print("mail failed: " .. err.kind .. ": " .. err.message)
end
```

Returns `(true, nil)` on success, `(nil, err_table)` on failure. The error
table has the same shape as `ai.*` and `http.*`:

| `err.kind` | When |
|---|---|
| `denied` | The script has no `mail` capability |
| `not_configured` | The project has no `.rela/mail.yaml` |
| `timeout` | The send exceeded its deadline or was canceled |
| `delivery_failed` | The transport rejected it or could not reach the server |

**The binding is always registered**, even without the capability and even when
mail is not configured. This is where `mail.send` differs from `http` and `ai`:
those are gated by *absence* — no grant, no global — whereas `mail` is always
present and refuses.

That is a difference in how the denial is *delivered*, not in whether one
happens. Two separate questions are at work:

- **May the script call it?** Always yes, so that `mail.send` can report what is
  actually wrong. "Attempt to call a nil value" cannot distinguish "mail is not
  configured" from "you were not granted mail", and both are things an operator
  needs told precisely. It also lets a script feature-detect:

  ```lua
  local ok, err = mail.send{...}
  if not ok and err.kind == "not_configured" then
    -- fall back to writing the report to disk
  end
  ```

- **May the script send?** Only with the `mail` capability. Without it the call
  returns `err.kind == "denied"` and nothing is handed to the transport.

`denied` takes precedence over `not_configured`: a script that was never
authorized to send is not told whether the deployment has mail configured, and
the operator debugging it is pointed at the `capabilities:` block rather than at
a `mail.yaml` that is perfectly fine.

A delivery failure **never raises**: a script that mails a summary at the end of
a run must not lose the run because the mail server was rebooting. Argument
errors do raise — a missing recipient is a bug in the script that no retry fixes.

`mail.render` builds the body parts for you, so you do not have to write email
HTML by hand:

```lua
local html, text = mail.render{
  subject  = "Wekelijks MT",
  lang     = "nl",                     -- optional BCP-47 tag
  intro    = "Automatisch samengesteld.",
  sections = {
    { title = "Open acties", columns = {"Taak", "Deadline"},
      rows = {{"Leveranciersbeoordeling", "2026-09-01"}},
      links = {"/entity/taak/TASK-DEMO"} },
    { title = "Toelichting", body = "Een **korte** toelichting." },
  },
  footer = "Automatisch verzonden.",
}
mail.send{to = "maaike@example.nl", subject = "Wekelijks MT", html = html, text = text}
```

It returns both parts, rendered through the same branded, client-tested template
the declarative digests use — table-based layout, inlined CSS, Outlook
fallbacks. `intro`/`body`/`footer` are markdown and are **sanitized**; `rows`
cells are escaped values, not markdown; row links are limited to `http(s)` or a
`/`-relative path resolved against `base_url`.

There is deliberately no `html` or `css` field: the sanitizing is the point.
`mail.render` needs no mail configuration (it only formats), and a malformed
call **raises**, since nothing here depends on the network.

See the [Outbound Mail](mail.md) guide for transports, including
`transport: script`, which lets you supply the provider mapping yourself.

### JSON Functions

The `rela.json` module provides JSON encode/decode helpers, usable in any
script (HTTP request bodies, flow scripts, document renderers, ad-hoc
transforms).

| Function | Description | Returns |
|----------|-------------|---------|
| `rela.json.encode(value)` | Lua value → JSON string | string (raises on bad input) |
| `rela.json.decode(string)` | JSON string → Lua value | (value, nil) or (nil, err_table) |

```lua
local json_str = rela.json.encode({name = "test", items = {1, 2, 3}})
local data, err = rela.json.decode('{"name":"test","count":42}')
```

**Array vs object encoding:** Tables with only consecutive integer keys
starting at 1 encode as JSON arrays. Tables with any string keys encode
as JSON objects.

`rela.json.decode` returns `(nil, err_table)` with `kind="bad_response"`
for invalid JSON — the same shape `http.*` produces — so existing
error-handling branches work unchanged.

#### JSON Conversion Limits

- **Empty tables encode as objects (`{}`), not arrays (`[]`).** Lua has
  no way to mark an empty table as array-shaped. If you need a JSON empty
  array, build the body server-side or include a placeholder element.
- **Cyclic tables** are encoded with the offending branch replaced by the
  string `"<cyclic reference>"` rather than crashing the runtime.
- **Decoded JSON deeper than 64 levels** is truncated with the sentinel
  string `"<max-depth>"` to prevent stack overflow from a hostile
  response.

### Context

| Variable | Description |
|----------|-------------|
| `rela.project_root` | Absolute path to project root |
| `rela.args` | Script arguments (table) |
| `rela.today` | Current date as "YYYY-MM-DD" |
| `rela.params` | Action script parameters (table, static string-valued map from data-entry config) |
| `rela.secrets` | Per-script secrets (table, from `.rela/secrets.yaml`) |
| `rela.request` | Present **only** for a request-scoped action (an action with a `request:` block). Read-only `{method, path, content_type, raw, body, query, headers}` — see [Request-scoped actions](data-entry.md#request-scoped-actions). `headers` holds only the names the action allowlisted; credentials and proxy-asserted identity headers are refused at config load. `nil` everywhere else, so `if rela.request then` is the test. |
| `rela.principal` | Read-only `{user, tool}` — the identity this runtime runs as (the request principal, e.g. from `X-Rela-User`). Use it in write-path automations to attribute relations to the acting user, e.g. stamping a `created-by` edge from the submitter. Unstamped/CLI contexts read `{user="unknown", tool="unknown"}`. The table is frozen: assigning to it raises. It only *reads* identity — audit attribution is always derived from the request context inside the write bindings, never from this table, so it is not a spoofing vector. |
| `rela.cache` | Process-wide memoization cache namespaced per script (see [Cache](#cache)) |
| `rela.mode` | Set to `"document"` when rendering a data-entry document; `nil` otherwise (see [Document Mode](#document-mode)) |
| `rela.document` | Present only in document mode; holds `{id, entry_id}` (see [Document Mode](#document-mode)) |

### Document Mode

When a data-entry document is configured with `script:` (see
[Documents in the data-entry guide](GUIDE-data-entry.md#documents)), the
script runs in a specialized mode with extra context exposed:

| Variable                 | Meaning |
|--------------------------|---------|
| `rela.mode`              | `"document"` in this context; absent elsewhere |
| `rela.document.id`       | The key under `documents:` in `data-entry.yaml` |
| `rela.document.entry_id` | The ID of the entity being rendered |

**List renders** (a `lists.<id>.export_render` view export, see
[Transforms](../../../docs/transforms.md#custom-per-list-rendering)) run in the
same document mode — `rela.mode` is `"document"` there too — with the row set
added and `entry_id` **absent**, since a list has no entry entity:

| Variable                    | Meaning |
|-----------------------------|---------|
| `rela.document.list_id`     | The key under `lists:` being exported |
| `rela.document.entity_type` | The list's entity type |
| `rela.document.rows()`      | Iterator over the resolved rows |
| `rela.document.row(i)`      | One row by 1-based index, or `nil` |
| `rela.document.count`       | Rows this render can see |
| `rela.document.total`       | Rows before the export cap |
| `rela.document.truncated`   | Whether the cap applied |
| `rela.document.query`       | Read-only `{q, filters, sort}` |

Branch on `rela.document.list_id` (or `rows`) to tell the two apart. The rows
are handed in already resolved and gated; don't re-derive them with
`rela.list_entities`, or the export stops matching the list the user was
looking at.

The script's stdout is captured and used as the rendered document's
markdown (which is then converted to HTML). Use `print()` to produce
output.

```lua
-- scripts/docs/release_notes.lua
local entry = rela.get_entity(rela.document.entry_id)
print("# " .. (entry.properties.title or entry.id))
```

**`rela.output` in document mode** emits a warning line into the
captured stdout (and thus into the rendered document) instead of
writing JSON — captured stdout is the document body, so a raw JSON
payload in the middle of markdown is almost always a mistake.

**Cache policy**: `rela.cache.memoize` is namespaced by the script path,
not by the document config. Two `documents:` entries that share one
`.lua` file share a cache namespace — usually what you want (a helper
script caches work across all its callers). If you need doc-scoped
keys, include `rela.document.id` in your cache key explicitly.

### Capabilities — `http`, `ai`, `mail`, `secrets`, `write_file`

Five things a script can reach are **not** granted by default: outbound HTTP,
the AI provider, outbound mail, named secrets, and `rela.write_file`.

For `http`, `ai`, `secrets` and `write_file`, a script that has not been granted
one does not merely fail the call — the binding is **absent**, so you get
`attempt to index a nil value (global 'http')`.

`mail` is the exception: the binding stays present and `mail.send` returns
`err.kind == "denied"`. The denial is just as complete — nothing reaches the
transport — but the error can name the missing grant, which "attempt to call a
nil value" cannot. See [Mail Functions](#mail-functions) above.

Declare what a script needs with a `capabilities:` block next to its `script:`
reference:

```yaml
# data-entry.yaml
actions:
  notify_slack:
    script: notify.lua
    capabilities:
      http: true
      secrets: [slack_webhook_url]
```

The same block works on `documents:` entries, automation actions in
`schema.yaml`, and tasks in `schedules.yaml`.

**`secrets:` is a list of key names, not a boolean.** This is the point of the
feature: an action that needs one Slack webhook does not also receive your
database DSN and every other API key in `.rela/secrets.yaml`. A key you did not
name is absent from `rela.secrets`, so a typo shows up as a `nil` at the use
site rather than as a silently-empty credential sent to an upstream.

Why closed by default: a script holding `secrets` **and** an outbound channel
can read a credential and send it anywhere in two calls. That combination used
to be granted to *every* script on every surface — including read-only document
renders and validation rules, which cannot even write to the graph. Requiring
the grant means the capability appears only where an operator wrote it down.

`mail` is gated for the same reason and joined this list later: `mail.send` is
an outbound channel too, so pairing it with `secrets` exfiltrates a credential
just as `http` does, and for a while it was the one such channel that needed no
grant at all.

The asymmetry that makes this worth a config key: an unexpected `mail: true` on
a 500-line script **stands out** in a file an operator reviews. The same reach,
written as one line in the middle of that script, does not.

Two surfaces differ, both deliberately:

- **`rela script` / `rela flow` / the docs build** grant everything. These run
  from your shell, where you already have the project directory and can read
  `.rela/secrets.yaml` yourself, so withholding anything protects nothing.
- **MCP `lua_eval` / `lua_run`** grant nothing, and there is no way to change
  that from config. The code (or the choice of file) comes from an MCP client,
  which is the least appropriate place to pair arbitrary code with your secrets.

### Secrets

Scripts can access secrets (API keys, tokens, passwords) via the `rela.secrets` table.
Secrets are loaded from `.rela/secrets.yaml`, which lives inside the gitignored `.rela/`
directory.

A script only sees the keys its `capabilities.secrets` list names (see
[Capabilities](#capabilities--http-ai-mail-secrets-write_file) above); everything
below describes how the *values* are resolved once a key has been granted.

#### Configuration

Create `.rela/secrets.yaml` in your project:

```yaml
# Global secrets — available to all scripts
jira_api_key: sk-abc123
deploy_endpoint: https://deploy.example.com

# Per-script overrides (merged on top of global)
overrides:
  reports/sync.lua:
    jira_api_key: sk-different-key
    extra_token: tok-xyz
```

- Top-level keys are **global** and available to every script
- Keys under `overrides.<script-path>` apply only to that script and override globals
- All values are plain strings

**Keep the file readable only by its owner:**

```bash
chmod 700 .rela
chmod 600 .rela/secrets.yaml
```

Both matter. `rela init` creates `.rela/` as `0755`, so tightening the file
alone still leaves the directory listable — and a group-writable parent would
let another user replace `secrets.yaml` outright.

rela does not create the secrets file, so its permissions are whatever your
editor and umask produced. When it is group- or world-readable, rela logs a
warning naming the path and mode and loads it anyway — failing closed would
break a working deployment on upgrade. The warning repeats only if the mode
changes, so a fixed-then-regressed file is reported again rather than silently
passing. It never echoes a key name or value.

The check covers the file, not the directory: `chmod 700 .rela` is on you.

#### systemd credentials

On a server, prefer systemd's credential mechanism over a file in the project.
rela reads `$CREDENTIALS_DIRECTORY` when systemd passes a credential whose name
matches the project. Ask rela for the name rather than deriving it:

```bash
$ rela secrets credential-name
rela-secrets-acme-6e0bff4c
```

The name is `rela-secrets-<project>-<hash>`: the readable directory name so you
can tell which unit-file line belongs to which project, plus a short hash of the
project's absolute path so two projects that happen to share a directory name
never resolve to the same credential.

```ini
[Service]
LoadCredentialEncrypted=rela-secrets-acme-6e0bff4c:/etc/rela/acme-secrets.cred
```

Encrypt the file with `systemd-creds` before deploying it:

```bash
systemd-creds encrypt --name=rela-secrets-acme-6e0bff4c secrets.yaml /etc/rela/acme-secrets.cred
```

The credential has the same YAML format as `secrets.yaml`. Why this is better
than a plaintext file or an environment variable:

- It is decrypted into a per-service tmpfs at mode `0400`, owned by the service
  user — never written to persistent storage in the clear.
- It is **not inherited by child processes**, unlike an environment variable.
  That matters because rela runs external converters for attachments and view
  exports; those inherit rela's environment but cannot see a credential.
- `systemd-creds` can bind the encryption key to the host TPM, so the file on
  disk is useless if copied elsewhere.

The credential name is per-project deliberately: `$CREDENTIALS_DIRECTORY` is
process-global, so a single shared name would hand one project's secrets to
every other project served by the same process.

The name changes if you move the project directory, since it includes a hash of
the absolute path. Re-run `rela secrets credential-name` and update the unit
file after a move.

When systemd passes a credential for the project it wins over
`.rela/secrets.yaml`. Otherwise rela falls back to the project file — including
when the unit loads other, unrelated credentials.

#### Usage in Lua

```lua
-- Access a secret
local key = rela.secrets.api_key

-- Check if a secret is configured
if rela.secrets.api_key then
    -- use it
else
    error("api_key not configured in .rela/secrets.yaml")
end
```

#### Resolution order

When a script runs, its `rela.secrets` table is built by:

1. Starting with all global (top-level) values
2. Merging any per-script overrides on top

If `.rela/secrets.yaml` does not exist, `rela.secrets` is an empty table (no error).

#### Security notes

- `.rela/` is gitignored by convention — secrets are not committed to version control
- Treat Lua scripts as trusted code: any script can read all secrets available to it
- For shared projects, each contributor maintains their own `.rela/secrets.yaml`

### Cache

Scripts can memoize expensive computations (AI calls, graph traversals, property
lookups) via the `rela.cache` module. The cache is in-memory, process-wide, and
**namespaced per script path** so two scripts that happen to use the same key
do not collide.

#### API

```lua
-- Fetch a cached value, or nil on miss/expiry.
local v = rela.cache.get("my-key")

-- Store a value. nil deletes the entry. ttl defaults to 1 hour.
rela.cache.set("my-key", value, {ttl = 3600})

-- Get-or-compute: on miss, call fn(), store and return all its
-- return values; on hit, return the cached values without calling fn.
local result = rela.cache.memoize("expensive-" .. entity.id, function()
    return do_expensive_work(entity)
end, {ttl = 86400})
```

#### Options

| Option | Applies to | Default | Meaning |
|--------|------------|---------|---------|
| `ttl` | `set`, `memoize` | 3600 (1h) | Seconds until expiry. 0 or negative = no expiry |
| `bypass` | `memoize` only | false | Skip the read, always call fn and write |

`get` takes no options. Unknown option keys raise a Lua error, so typos like
`{refersh = true}` surface loudly instead of being silently dropped.

#### Multi-return values

`memoize`'s `fn` may return multiple values; `rela.cache` stores and re-emits
all of them. This matches the `(result, err)` convention used by `ai.chat`:

```lua
local resp, err = rela.cache.memoize("summary:" .. id, function()
    return ai.complete("Summarize: " .. content)
end)
if err then error(err.message) end
```

#### Caveat: nested `set` on the same key

If `fn` inside `memoize("k", fn)` itself writes to the same key via
`rela.cache.set("k", ...)`, that write is silently overwritten when
`memoize` stores `fn`'s return values. Use a different key for
side-channel writes:

```lua
rela.cache.memoize("result:" .. id, function()
    local aux = compute_aux()
    rela.cache.set("result:" .. id .. ":aux", aux)  -- different key; survives
    return compute_main(aux)
end)
```

#### Limits

- Keys are Lua strings, ≤ 512 bytes
- Values must be plain data (strings, numbers, booleans, nested tables);
  functions, userdata, and coroutines are rejected at `set` time with an
  error naming the offending type
- The cache is capped globally at 10,000 entries across all namespaces;
  when the cap is reached, the least-recently-accessed entry is evicted
  on the next write

#### Scope

- The cache is in-memory and shared across all Lua runtimes within a single
  `rela` process. Scheduled tasks running in the same `rela scheduler` process
  reuse cache state across their repeat invocations; HTTP action scripts on the
  same `rela-server` share state across requests
- Each new `rela` invocation starts with an empty cache — nothing is persisted
  to disk. A one-shot `rela script` run gets no cross-invocation reuse
- `rela.cache.*` is **not available** in inline/eval contexts (`rela mcp`'s
  `lua_eval`) — calling it raises `cache: not available in inline/eval contexts`.
  This prevents accidental cross-session bleed where two sessions would share a
  nameless namespace

#### Numeric precision

Lua numbers are 64-bit floats. Integer IDs larger than 2^53 cannot round-trip
through any Lua value — this is a Lua limitation, not a cache one, but worth
keeping in mind if you cache things keyed by raw integer IDs.

### Markdown AST

The `rela.md` module exposes a markdown AST API for parsing and rendering
content. The AST is structure-preserving: links, code spans, raw HTML,
images, autolinks, line breaks, multi-paragraph blockquotes, and
multi-block list items all keep their structure on the Lua side.
Flattening to plain strings happens at render time and in helper APIs
(`headers`, `first_paragraph`, the public `flatten`), never at parse
time.

Use `rela.md.parse(content)` to turn a markdown string into an AST table
and `rela.md.render(ast)` to serialize it back. The pair is a fixed point
on a representative corpus of GFM input.

#### Block node shapes

```lua
-- Inline-bearing leaves carry an `inlines` array.
{ type = "paragraph", inlines = { ... } }
{ type = "heading",   level = 2, inlines = { ... } }

-- Block containers carry `children` (an array of block nodes).
{ type = "blockquote", children = { ... } }

-- Lists hold items; each item is one of:
--   * a string (a plain item containing only text)
--   * a table with `inlines` (single-paragraph item with formatting)
--   * a table with `children` (multi-block item; e.g. an item
--     containing a fenced code block or nested list)
--   * a task table with `task=true`, `checked=<bool>`, and either
--     `inlines` or `children` per the same rules above
{ type = "list", ordered = false, items = { ... } }

-- Tables: each cell is an `inlines` array (link/code/raw HTML inside
-- a cell round-trips correctly).
{ type = "table",
  header = { cell, cell, ... },              -- each cell is an inlines array
  rows   = { { cell, cell }, ... },
  alignments = { "left", "center", "right" } }

-- Leaves that don't carry inline content keep a `content: string`.
{ type = "code_block", language = "go", content = "fmt.Println()" }
{ type = "thematic_break" }
{ type = "raw", content = "<unknown block>" }
```

#### Inline node shapes

```lua
-- Leaves with a `text` field:
{ type = "text",         text = "hello" }
{ type = "code_span",    text = "x = 1" }
{ type = "raw_html",     text = "<br>" }
{ type = "soft_break" }                   -- a soft line break in the source
{ type = "hard_break" }                   -- two-trailing-space line break

-- Autolinks have a `url` (no inner text):
{ type = "autolink",     url  = "https://example.com" }

-- Containers wrap further inlines:
{ type = "emphasis",     inlines = { ... } }   -- *text*
{ type = "strong",       inlines = { ... } }   -- **text**
{ type = "strikethrough",inlines = { ... } }   -- ~~text~~
{ type = "link",         url  = "/x", title = nil, inlines = { ... } }

-- Images store alt content as a separate inlines array (preserves
-- formatting inside `![**bold**](url)`):
{ type = "image",        url, title = nil, alt_inlines = { ... } }
```

#### Constructors

Block constructors accept either a string (auto-wrapped to a single text
leaf) or an inlines/children table:

```lua
rela.md.paragraph("hello")
rela.md.paragraph({rela.md.text("see "), rela.md.link_inline("docs", "/x")})
rela.md.heading(2, "Section")
rela.md.heading(2, {rela.md.text("Section "), rela.md.code_span("X")})
rela.md.blockquote("a single paragraph")
rela.md.blockquote({rela.md.paragraph("p1"), rela.md.paragraph("p2")})
```

Inline constructors:

```lua
rela.md.text(s)                              -- {type="text", text=s}
rela.md.code_span(s)                         -- {type="code_span", text=s}
rela.md.raw_html(s)                          -- {type="raw_html", text=s}
rela.md.link_inline(text_or_inlines, url, title?)
                                             -- {type="link", url, [title], inlines={...}}
```

`rela.md.link_inline` is distinct from `rela.md.link(text, url) → string`,
which predates the inline AST and is still available for scripts that
build markdown by string concatenation.

#### Flattening to text

When you want a plain string instead of an inline tree, call
`rela.md.flatten(inlines)`. It applies a conservative policy:

- **Strikethrough** (`~~...~~`) — preserved as `~~text~~`. The
  checklist-validation layer treats strikethrough as the "skip" marker.
- **Code spans** (`` `...` ``) — preserved with their backticks.
- **Raw HTML** — preserved verbatim.
- **Autolinks** — render as the URL.
- **Soft and hard breaks** — render as a single space (matching the
  pre-refactor flat-text behaviour).
- **Emphasis, strong, link** — wrapper dropped, only the inner content
  is kept (so `[See docs](url)` flattens to `See docs`).
- **Images** — render as the flattened alt content (no URL).

`rela.md.headers(ast)` and `rela.md.first_paragraph(ast)` use this
policy automatically, so existing scripts that read `header.title` or
the first-paragraph string see no behaviour change for typical content.

#### Worked example: reading and mutating a checklist

```lua
local ticket = rela.get_entity("TKT-001")
local ast = rela.md.parse(ticket.content)

for _, node in ipairs(ast) do
    if node.type == "list" then
        for _, item in ipairs(node.items) do
            if type(item) == "table" and item.task then
                local label = rela.md.flatten(item.inlines)
                if label == "implement" then
                    item.checked = true
                end
            end
        end
    end
end

rela.update_entity("TKT-001", {}, rela.md.render(ast))
```

#### Limitations

- **Mixed lists.** A list mixing task and plain items may not be
  symmetrically re-parseable: only items that carry their own
  checkbox in the source are classified as task items on parse, but
  the renderer always emits checkbox syntax for items with
  `task = true`.
- **Sparse item tables.** Scripts that delete items via
  `items[i] = nil` will get a compact rendering (the renderer skips
  `nil` holes), but they should compact the table explicitly if they
  rely on `#items` afterwards — Lua's length operator returns a
  "border", not a count.
- **Performance.** Parsing now allocates a Lua table per inline node.
  On a kitchen-sink fixture this is ~2.5–3× the allocations of the
  pre-refactor flat-string representation. For real-world content
  (mostly plain prose) the gap is smaller. If a hot-path script feels
  slow, consider parsing once and reusing the AST.

### Resolving Entity-ID References

When a Lua script composes a document from one or more entities, the
body text often mentions other entities by ID. Two helpers turn those
mentions into proper markdown links:

- `rela.md.resolve_refs(ast, replacements) → ast` — walks the AST and
  rewrites entity-ID code spans to caller-supplied markdown.
- `rela.md.entity_refs(opts?) → table` — builds the `replacements` map
  for the common case of "link every entity (or every entity of these
  types)".

#### The matching rule: code spans only

The matcher targets **code spans** whose entire content is an entity
ID. A code span is the GFM inline form `` `TKT-1` `` (backticks). Bare
prose mentions like `see TKT-1 here` are left untouched — the
ambiguous-boundary problem in prose is hard, and authors already use
backticks to flag identifiers in entity bodies. Opt in to a link by
writing the ID as a code span; leave the backticks off and it stays
text.

This rule means:

- `` see `TKT-1` here `` → `see [Title](#anchor) here`
- `see TKT-1 here` → unchanged (no opt-in)
- ` ```fenced\n`TKT-1`\n``` ` → unchanged (code blocks are skipped)
- `` see [`TKT-1`](url) `` → the code span inside the link is
  rewritten; the surrounding link wrapper stays
- `` see [TKT-1](url) `` → unchanged (only code spans are matched)
- `` `TKT-99` `` (not in map) → unchanged

#### `rela.md.entity_refs(opts?)`

Returns a `{[id] = "<markdown>"}` table covering some or all entities
in the project. Pass the result to `resolve_refs`.

Options (all optional):

- `types`: list of entity-type names to include. Unknown names raise
  an error. Default: every entity type in the metamodel.
- `style`: `"title-slug"` (default) or `"id"`. Controls the anchor in
  the default replacement value.
  - `"title-slug"` produces `[Title](#title-slug)` where the slug is
    the title lowercased (Unicode-aware), with non-alphanumeric runs
    collapsed to `-` and trailing `-` trimmed. Matches the
    auto-heading-id rules used by Pandoc, goldmark's
    auto-heading-id extension, and markdown-it-anchor.
  - `"id"` produces `[Title](#tkt-123)` (lowercased ID). For docs
    that emit their own ID-based anchors.
- `format`: `function(entity) → string`. Overrides `style` entirely.
  The callback runs once per entity and must return a string with no
  newlines. The entity table has the usual fields plus a top-level
  `title` convenience.

Default-form replacement values escape `[`, `]`, and `\` in entity
titles so a malicious or unusual title cannot break out of the
link-text slot. Custom format callbacks remain responsible for their
own escaping.

```lua
-- Common case: link every entity-ID code span to its title-slug anchor.
local refs = rela.md.entity_refs()
local ast  = rela.md.parse(entity.content)
ast = rela.md.resolve_refs(ast, refs)
print(rela.md.render(ast))

-- Restrict to tickets and bugs, ID-based anchors:
local refs = rela.md.entity_refs({
  types = {"ticket", "bug"},
  style = "id",
})

-- Fully custom format:
local refs = rela.md.entity_refs({
  format = function(e)
    return "[" .. e.title .. " (" .. e.id .. ")](/x/" .. e.id .. ")"
  end,
})
```

#### `rela.md.resolve_refs(ast, replacements)`

Walks a deep-copied AST and rewrites code-span entity-ID tokens in
block text using the provided map. Returns the new AST; the input is
not modified.

The `replacements` argument is a `{[id] = "<markdown>"}` table:

- Keys are exact code-span content. Values are inline markdown
  fragments to splice in. The fragments are parsed as inline content,
  so `[Title](url)` becomes a structured `link` inline (not opaque
  text) — link URLs survive a subsequent `render` round-trip cleanly.
- Values must not contain `\n` or `\r` (would break paragraph
  structure).
- Hand-built tables can carry any string keys — only the ones whose
  content matches a code span will rewrite.

What the walker visits and what it skips:

| inline kind | behavior |
| --- | --- |
| `code_span` | matched against keys; replaced if a hit |
| `link`, `emphasis`, `strong`, `strikethrough` | recurse into `inlines` |
| `image` | recurse into `alt_inlines` |
| `text`, `raw_html`, `autolink`, breaks | left untouched |
| inside `code_block` or `raw` block | left untouched (parser doesn't emit code-span inlines there) |

## Entity Structure

Entities returned by query functions have this structure:

```lua
{
    id = "TKT-001",
    type = "ticket",
    content = "Markdown body...",
    mod_time = "2024-01-15T10:30:00Z",  -- RFC3339 timestamp
    properties = {
        title = "Fix login bug",
        status = "open",
        priority = "high"
    }
}
```

Entities also have helper methods:

- `entity:prop(name, default)` - Get property with fallback default
- `entity:strip_prefix()` - Get ID without type prefix (e.g., "001" from "TKT-001")
- `entity:is_redacted(name)` - Whether ACL withheld the property (see below)

### Redacted properties

When an ACL policy hides a property from the reading principal, the value is
stripped before the script sees it — `properties.salary` is `nil`. That is
indistinguishable from a property nobody ever filled in, so scripts that render
reports get a blank where "[redacted]" would be more honest.

The `redacted` table names the withheld properties, and `entity:is_redacted(name)`
tests one:

```lua
local p = rela.get_entity("P-1")

if p:is_redacted("salary") then
    rela.output("salary: [redacted]")
else
    rela.output("salary: " .. p:prop("salary", "(not set)"))
end

-- Or iterate everything that was withheld:
for name in pairs(p.redacted) do
    rela.output("withheld: " .. name)
end
```

Two things to know:

- **Names only, never values.** The withheld value is not reachable from
  `redacted`, from `properties`, or via `prop()`. Disclosing the *name* is
  intentional and matches the HTTP API's `_redacted` field: field-level ACL
  hides property values, and the metamodel that declares property names is
  already served over `/api/v1/_schema`.
- **`false` means "not withheld here", not "you are allowed to see it".**
  Whether field policy is evaluated at all depends on the runtime:

  | Runtime | Entity gating | Field redaction | `is_redacted` |
  |---|---|---|---|
  | Data-entry (documents, views, actions) | yes | yes | meaningful |
  | Scheduler (`run_as:`) | yes | **no** | always `false` |
  | CLI, MCP, docs | no | no | always `false` |

  The scheduler row is the one to watch: scheduled tasks are gated on *which
  entities* they may read, but field-level `visible:` redaction is not applied
  there, so a job sees every property of an entity its identity can read.

## Filter Expressions

The `list_entities` function accepts filter expressions:

```lua
-- Single value
rela.list_entities("ticket", "status=open")

-- Multiple values (OR)
rela.list_entities("ticket", "status=open,in-progress")

-- Not equal
rela.list_entities("ticket", "priority!=low")
```

## Examples

### Generate a Report

```lua
-- Export open tickets as markdown
local tickets = rela.list_entities("ticket", "status=open")

local lines = {"# Open Tickets", ""}
for _, t in ipairs(tickets) do
    table.insert(lines, string.format("- **%s**: %s", t.id, t.properties.title))
end

rela.write_file("open-tickets.md", table.concat(lines, "\n"))
rela.output({exported = #tickets})
```

### Bulk Update

```lua
-- Close all tickets in a completed sprint
local sprint = rela.args[1] or "2024-Q1"
local tickets = rela.list_entities("ticket", "sprint=" .. sprint)

local updated = 0
for _, t in ipairs(tickets) do
    if t.properties.status ~= "done" then
        rela.update_entity(t.id, {status = "cancelled"})
        updated = updated + 1
    end
end

rela.output({sprint = sprint, cancelled = updated})
```

### Find Orphans

```lua
-- Find entities with no relations
local orphans = {}
local types = rela.get_entity_types()

for name, _ in pairs(types) do
    local entities = rela.list_entities(name)
    for _, e in ipairs(entities) do
        local from = rela.trace_from(e.id, 1)
        local to = rela.trace_to(e.id, 1)
        if #from.children == 0 and #to.children == 0 then
            table.insert(orphans, {
                id = e.id,
                type = e.type,
                title = e.properties.title
            })
        end
    end
end

rela.output(orphans)
```

### Create Related Entities

```lua
-- Create test cases from feature acceptance criteria
local feature_id = rela.args[1]
local feature = rela.get_entity(feature_id)

if not feature then
    error("Feature not found: " .. feature_id)
end

local created = {}
for line in feature.content:gmatch("[^\n]+") do
    if line:match("^%s*[-*]") then
        local criterion = line:gsub("^%s*[-*]%s*", "")
        local test = rela.create_entity("test-case", {
            title = "Verify: " .. criterion,
            status = "pending"
        })
        rela.create_relation(test.id, "verifies", feature_id)
        table.insert(created, test.id)
    end
end

rela.output({feature = feature_id, tests_created = created})
```

### Traceability Matrix

```lua
-- Generate requirement-to-test coverage matrix
local reqs = rela.list_entities("requirement")
local matrix = {}

for _, req in ipairs(reqs) do
    local entry = {
        id = req.id,
        title = req.properties.title,
        tests = {},
        covered = false
    }
    
    local trace = rela.trace_from(req.id, 2)
    for _, child in ipairs(trace.children) do
        if child.type == "test-case" then
            table.insert(entry.tests, child.id)
        end
    end
    
    entry.covered = #entry.tests > 0
    table.insert(matrix, entry)
end

-- Summary
local total = #matrix
local covered = 0
for _, entry in ipairs(matrix) do
    if entry.covered then covered = covered + 1 end
end

rela.output({
    matrix = matrix,
    summary = {
        total = total,
        covered = covered,
        uncovered = total - covered,
        coverage_pct = math.floor(covered / total * 100)
    }
})
```

## Security

The Lua runtime is sandboxed for security:

### What a script can read (access control)

When a project has an `acl.yaml`, **read bindings return the acting
identity's view, not the whole graph**. `rela.get_entity`,
`rela.list_entities`, `rela.search`, `rela.get_relations`,
`rela.trace_from`/`trace_to`/`find_path` and `rela.md.entity_refs` all
resolve against whoever the script is running as:

| Where the script runs | Reads as |
|---|---|
| Data-entry action, `export_render`, MCP-invoked | the requesting user |
| Automation / cascade | the user whose write triggered it |
| Scheduled task | the task's identity (see `run_as` in [Scheduled Tasks](scheduled-tasks.md)) |
| CLI, docs build | unrestricted (operator trust boundary) |

Consequences worth knowing when writing scripts:

- An entity the identity cannot read is simply **absent** — `get_entity`
  returns `nil`, list/search omit it. That is indistinguishable from "does
  not exist", by design.
- `get_relations` is **peer-gated**: a relation appears only when *both*
  endpoints are visible. An empty result means "none you may see", not "no
  such edges".
- Traversals **prune** hidden nodes together with everything beneath them,
  and withhold a path that would run through one.
- `rela.update_entity` reads the *unredacted* entity before writing, so
  updating one property never erases others the caller cannot see.
- Without an `acl.yaml`, everything behaves exactly as it always has.

A script that genuinely needs to read beyond its caller's view has no
implicit escape hatch — that is deliberate. The sanctioned mechanism is the
write-side `rela.bypass_acl` closure, whose read counterpart is tracked
separately.

### Available Libraries

- `string` - String manipulation
- `table` - Table operations
- `math` - Mathematical functions
- `coroutine` - Coroutine support
- Base functions: `print`, `pairs`, `ipairs`, `type`, `tostring`, `tonumber`, `error`, `pcall`,
  `assert`, `select`, `next`, `unpack`

### Unavailable (Security)

- `io` - No direct file system access
- `os` - No system commands
- `debug` - No runtime introspection
- `load`, `loadfile`, `dofile`, `loadstring` - No dynamic code loading
- `rawget`, `rawset`, `getmetatable`, `setmetatable` - No metatable manipulation

### AI / Network Access

When `.rela/ai.yaml` is configured, scripts can make HTTP requests to the configured
AI provider via `ai.chat`, `ai.complete`, and `ai.embed`. This means scripts can send
entity content to an external service.

The `http.*` module additionally lets scripts make arbitrary HTTP(S)
requests to any reachable URL. There is no SSRF filter — localhost and
private-network IPs are reachable. **Treat Lua scripts as trusted code** —
don't run scripts from untrusted sources.

API keys are never logged or included in error messages.

### File Writing

Files can only be written to the `output/` directory:

```lua
-- OK: writes to output/report.txt
rela.write_file("report.txt", "content")

-- OK: writes to output/reports/2024/summary.md  
rela.write_file("reports/2024/summary.md", "content")

-- ERROR: path traversal blocked
rela.write_file("../secret.txt", "content")

-- ERROR: absolute paths blocked
rela.write_file("/etc/passwd", "content")
```

## MCP Tools

Lua scripting is available via MCP tools:

| Tool | Description |
|------|-------------|
| `lua_eval` | Execute inline Lua code |
| `lua_run` | Execute a script from `scripts/`, or list them when `path` is omitted |

### lua_eval

Execute inline Lua code:

```text
lua_eval(code: "rela.output(rela.list_entities('ticket'))")
```

### lua_run

Execute a script file with arguments:

```text
lua_run(path: "report.lua", args: ["2024-Q1"])
```

## Validation Rules

Lua can be used in metamodel validation rules for complex business logic. See the
[Lua Validation](metamodel.md#lua-validation) section in the Metamodel Reference for details.

```yaml
validations:
  - name: check-coverage
    description: "Components need 80% test coverage"
    entity_type: component
    lua: |
      local cov = entity.properties.coverage
      if cov == nil then return nil end
      local value = tonumber(cov)
      if value and value < 80 then
        return { message = "Coverage is " .. value .. "%, need 80%" }
      end
      return nil
    severity: error
```

Key differences from script execution:

| Feature | Scripts (lua_eval/lua_run) | Validation Rules |
|---------|---------------------------|------------------|
| Workspace access | Full read/write | Read-only |
| Timeout | None | 5 seconds |
| Output | `rela.output()` | Return `nil` (pass) or `{message=...}` (violation) |
| File I/O | `rela.write_file()` | Not available |
