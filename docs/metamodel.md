<!-- This file is auto-generated from docs-project/entities/. Do not edit directly. -->

# Metamodel Reference

The metamodel defines your project's entity types, properties, and relations.
It's stored in `schema.yaml` at your project root.

> **Renamed from `metamodel.yaml`.** Projects created before the rename still
> work: rela reads `metamodel.yaml` when no `schema.yaml` is present, and warns
> once at startup. Run `rela migrate` to rename the file. The legacy name will
> keep working until a future major version. If both files exist, `schema.yaml`
> is used and the `metamodel.yaml` is ignored — `rela migrate` reports it so you
> can merge and delete it.

## Structure

```yaml
version: "1.0"
namespace: "https://example.org/ontology/architecture#"
description: |
  Optional end-user prose describing what this deployment is for. Documentation
  only — surfaced by generated docs; ignored by validation and the write path.

types:
  # Custom enum types

entities:
  # Entity type definitions

relations:
  # Relation definitions
```

## Including Partial Metamodels

For larger projects, you can split your metamodel across multiple files using the
`includes:` key. This keeps each domain's definitions in a focused, manageable file.

### Syntax

```yaml
# schema.yaml
version: "1.0"
namespace: "https://example.org/ontology/architecture#"

includes:
  - compliance/controls.yaml
  - risk.yaml

types:
  status:
    values: [draft, proposed, accepted, deprecated]
    default: draft

entities:
  requirement:
    label: Requirement
    id_prefix: REQ-
    properties:
      title:
        type: string
        required: true
```

The `includes:` key is always a YAML list of file paths, resolved relative to the
project root (where `schema.yaml` lives).

### Included File Format

Each included file is a partial metamodel. It can contain any combination of
`types:`, `entities:`, `relations:`, and `validations:` — but **must not** contain
`version:`, `namespace:`, or `description:` (these are deployment-wide, allowed
only in the root `schema.yaml`).

```yaml
# compliance/controls.yaml
types:
  applicability:
    values: [applicable, not_applicable, partial]

entities:
  control:
    label: Control
    id_prefix: CTL-
    properties:
      title:
        type: string
        required: true
      applicability:
        type: applicability

relations:
  implements_control:
    label: implements
    from: [requirement]
    to: [control]
    inverse: implementedBy

validations:
  - name: controls-need-applicability
    description: "Controls must have applicability set"
    entity_type: control
    then:
      - "applicability!="
    severity: warning
```

### Nested Includes

Included files can themselves include other files:

```yaml
# compliance/controls.yaml
includes:
  - shared/audit-types.yaml

entities:
  control:
    # ...
```

Circular includes are detected and produce a clear error:

```text
circular include detected: schema.yaml → compliance/controls.yaml → shared/audit-types.yaml → compliance/controls.yaml
```

### Diamond Includes

If the same file is reachable from multiple include paths (a "diamond" pattern),
it is loaded only once. This is not an error.

```yaml
# schema.yaml
includes:
  - a.yaml # includes shared.yaml
  - b.yaml # also includes shared.yaml — loaded once, no conflict
```

### Conflict Handling

If the same type, entity, relation, or validation name is defined in more than
one file, loading fails with an error identifying both files:

```text
duplicate entity "control": defined in both compliance/controls.yaml and risk.yaml
```

To resolve conflicts, rename one of the definitions or move it to a shared file.

### Error Messages

| Situation            | Error                                                                                   |
| -------------------- | --------------------------------------------------------------------------------------- |
| Duplicate definition | `duplicate entity "control": defined in both a.yaml and b.yaml`                         |
| Circular include     | `circular include detected: a.yaml → b.yaml → a.yaml`                                   |
| File not found       | `include file not found: missing.yaml (included from schema.yaml)`                   |
| Root-only field      | `included file a.yaml must not contain "version" (only allowed in root schema.yaml)` |

## Custom Types

Define reusable types that can be used in entity properties. Custom types support
enum values, regex validations, or both.

### Enum Types

Define allowed values for a property:

```yaml
types:
  status:
    values: [draft, proposed, accepted, deprecated, rejected, retired]
    default: draft

  priority:
    values: [critical, high, medium, low]
```

**The ORDER of `values:` is load-bearing, not just the set.** Lists, views and
sections sort an enum property by each value's position in this list, so
writing the values in workflow order — `todo` before `done`, `critical` before
`low` — gives the right sort for free, instead of the alphabetical order a
plain text comparison would produce.

Two consequences worth knowing before you edit a `values:` list:

- Reordering the values **reorders every list sorted on that property.** That
  is usually what you want, but it is a visible change to existing screens.
- A value that no longer appears in `values:` still sorts — after every
  declared value, and alphabetically among other undeclared ones — so removing
  a value does not hide rows that still hold it.

On the PostgreSQL backend the declared order is compiled into a database index,
which is rebuilt automatically when you change it.

#### Display Labels

Enum values are stored as-is (typically snake_case identifiers). For a
friendlier data-entry UI you can attach an optional human-readable **label** to
any value with a `labels:` map keyed by value:

```yaml
types:
  status:
    values: [draft, in_progress, wont_fix]
    labels:
      in_progress: In Progress
      wont_fix: Won't Fix
```

Labels also work on inline enums:

```yaml
properties:
  status:
    type: enum
    values: [open, in_progress, closed]
    labels:
      in_progress: In Progress
```

Notes:

- **Labels are display-only.** The stored value, the value submitted by forms,
  validation, and badge colours all key on the raw value — only the text shown
  in the data-entry UI changes. A value with no entry in `labels` renders raw.
- Labels are surfaced in the data-entry web UI (select dropdowns, badges across
  lists / detail / kanban, and filter menus). The CLI and the OpenAPI `enum`
  output stay value-based.
- `labels` is optional and backwards compatible: existing metamodels with plain
  value lists are unchanged and need no migration.
- When a property references a custom type, labels come from the **custom
  type**; an inline `labels` map on such a property is ignored (mirroring how an
  inline `values` list is ignored there).

#### Value Descriptions

Where `labels:` gives a value its short display text, `descriptions:` gives the
**longer prose meaning** of a value — what it signifies. It is documentation
only (surfaced by generated docs), keyed by value like `labels:`:

```yaml
types:
  ticket-status:
    values: [open, in_progress, closed]
    labels:
      in_progress: In progress
    descriptions:
      open: A newly filed ticket that no one has started yet.
      in_progress: Someone is actively working on the ticket.
      closed: The ticket is finished; no further work is expected.
```

- `descriptions` is optional and independent of `labels`: a value may have
  either, both, or neither. It never affects storage, validation, or forms.
- Distinct from the type-level `description:` scalar (which documents the type as
  a whole); `descriptions:` documents each individual value.

### State Machines (transitions)

An enum custom type becomes a **state machine** when it declares `transitions:` —
the legal value→value moves. Instead of any value changing to any other, only the
declared edges are allowed; the write path rejects an undeclared move (`422`), and
each edge can additionally require an ACL permission (`guard`, `403`) and/or a data
precondition (`when`, `422`).

```yaml
types:
  ticket-status:
    values: [todo, doing, review, done]
    initial: todo # the only value a newly created entity may enter at
    transitions:
      - from: todo
        to: doing
        label: Start progress # optional: names the MOVE (an action verb)
        help: Move here once someone picks up the ticket. # optional: why/when
      - from: doing
        to: review
        label: Send to review
      - from: review
        to: done
        guard: close # requires the `close` ACL permission
        when: 'count_relations(entity, "reviewed-by") > 0' # precondition
      - from: review
        to: doing
        label: Reopen
```

| Field | Meaning |
| ----- | ------- |
| `from` / `to` | Source and target values; both must be declared in `values`. |
| `initial` | The only value a **create** may set (else `default`). New entities are pinned to it — a create cannot enter a guarded mid-lifecycle state. |
| `guard` | An ACL permission the acting principal must hold for the move. Enforced on served paths; inert on a direct CLI write with no policy. |
| `when` | A predicate (same language as validations, evaluated against the entity + graph) that must hold for the move. |
| `label` | **Optional** display text for the move (the *action*, e.g. "Start progress"), used by the data-entry status control. Display-only — the stored value is still `to`. Absent → the UI falls back to the target value's display label, then the raw value. |
| `help` | **Optional** longer prose explaining *why or when* a user would make this move, beyond the short `label`. Documentation only — surfaced by generated docs, ignored by enforcement. |

The data-entry UI reads these to render a **status control** that offers only the
moves the current user can perform right now (see the `_transitions` affordance in
the [data-entry API reference](data-entry/api-reference.md)). `transitions` is
optional and backwards compatible: an enum type without it keeps the historical
"any value may change to any other" behavior.

### Regex Validations

Define validation patterns with user-friendly error messages. Multiple patterns
can be combined—all must pass for a value to be valid:

```yaml
types:
  semver:
    description: "Semantic version number"
    validations:
      - pattern: '^\d+\.\d+\.\d+$'
        error: "Must be valid semver (e.g., 1.2.3)"

  rrule:
    description: "iCal recurrence rule (RFC 5545)"
    validations:
      - pattern: "^FREQ=(YEARLY|MONTHLY|WEEKLY|DAILY)"
        error: "Must start with valid FREQ"
      - pattern: "^(?!.*COUNT=.*UNTIL=)"
        error: "Cannot specify both COUNT and UNTIL"

  email:
    validations:
      - pattern: "^[^@]+@[^@]+\\.[^@]+$"
        error: "Must be a valid email address"
```

Each validation requires:

| Field     | Description                                             |
| --------- | ------------------------------------------------------- |
| `pattern` | Regex pattern that values must match                    |
| `error`   | User-friendly error message shown when validation fails |

**Benefits of multiple simple patterns vs one complex regex:**

- Each pattern has its own clear error message
- Users see exactly which validation failed
- Patterns are easier to write and maintain
- No mega-regex with opaque errors

### Empty Values

- **Enum types**: Empty string is not a valid value (fails validation)
- **Regex-only types**: Empty strings skip validation (let `required` handle it)
- **List properties**: Each item in the list is validated independently

### Reserved Type Names

The following names are reserved for built-in property types and cannot be used as custom type names:

- `string` - Free-form text
- `date` - Date values
- `datetime` - Time-bearing instants (date + time)
- `integer` - Whole numbers
- `boolean` - True/false values
- `enum` - Inline enumeration (use `values:` directly in property definition)

Attempting to define a custom type with a reserved name will produce an error:

```text
cannot define custom type "string": name is reserved for built-in type
```

## Entity Types

Each entity type defines:

| Field              | Description                                                                     |
| ------------------ | ------------------------------------------------------------------------------- |
| `label`            | Display name                                                                    |
| `label_plural`     | Plural display name (defaults to label + "s")                                   |
| `description`      | Documentation explaining intent and usage (optional)                            |
| `aliases`          | Alternative names for CLI (e.g., `req` for `requirement`)                       |
| `id_type`          | `short` (default), `sequential`, or `manual` - controls ID generation           |
| `id_prefix`        | Single ID prefix (e.g., `REQ-`)                                                 |
| `id_prefixes`      | Multiple ID prefixes (e.g., `["DEC-", "ADR-"]`)                                 |
| `properties`       | Property definitions                                                            |
| `default_sort`     | Default sort order for list views                                               |
| `color`            | Fill color for graph visualizations (hex or named)                              |
| `border_color`     | Border color for graph visualizations                                           |
| `display_property` | Property whose value names the entity. See [Display name](#display-name) below. |

### Display name

Every entity type has a *primary property* — the property whose value
is the entity's display name. When unset, rela picks one
automatically: it checks `title`, `name`, `label` in that order (when
each is a required string property), then falls back to any required
string property (alphabetical), then to the entity ID. That works for
English schemas but is brittle for non-English ones — the priority
list never matches Dutch `naam` or `titel`, so the fallback runs, and
the choice silently flips if a second required string property is
added later.

Set `display_property` explicitly to make the choice load-bearing:

```yaml
entities:
  applicatie:
    label: Applicatie
    display_property: naam
    properties:
      naam:
        type: string
        required: true
```

**Allowed types.** The named property must be `string`, `integer`,
`boolean`, or `enum` (custom enum-like types are accepted). `date`,
`datetime`, `file`, `rrule`, and list-typed (`list: true`) properties are
rejected at metamodel-load time — their default rendering produces
strings nobody designed as a display name (e.g. `"2026-04-25 00:00:00
+0000 UTC"`, `"[a b c]"`).

**Runtime behavior.** Non-string values (integers, booleans, enum
values) are stringified via `fmt.Sprintf("%v", val)`. The display
falls back to the entity ID when the value is empty, missing, or
`nil`.

**Templates.** When `display_property` contains a `{`, it is a
template: each `{propname}` placeholder is replaced with that
property's value, and literal text (spaces, commas) passes through.
This composes a display name from several fields:

```yaml
entities:
  persoon:
    label: Persoon
    display_property: "{voornaam} {tussenvoegsel} {achternaam}"
    properties:
      voornaam: { type: string }
      tussenvoegsel: { type: string }
      achternaam: { type: string, required: true }
```

renders `"Jeroen Vloothuis"` — and `"Jan van der Berg"` for someone
with a `tussenvoegsel`. Consecutive whitespace collapses to one space
and the result is trimmed, so an empty middle field doesn't leave a
double space. The ID fallback applies only when the rendered result is
empty after trimming — a template with literal text (e.g. `"Mr.
{achternaam}"`) always renders that text, so it never falls back to the
ID even when every placeholder is empty. Each placeholder must name a
defined property of an allowed type (same rules as above), checked at
load. A template names no single primary property, so it is
display-only — it isn't a target for writing values.

**Validation.** A typo, whitespace mistake, list-typed reference,
disallowed type, or a malformed template (unclosed `{`, empty `{}`, or
a placeholder naming an undefined property) fails metamodel-load with a
diagnostic naming the entity, the offending value, and the available
properties.

How the data-entry app surfaces the display name across lists, cards,
breadcrumbs, and related-entity links is documented in
[GUIDE-data-entry.md → Display names](data-entry.md#display-names).

### ID Types

Entity IDs can be auto-generated or manually specified:

| Type         | Description                   | Example IDs                     |
| ------------ | ----------------------------- | ------------------------------- |
| `short`      | Random base36 IDs (default)   | `REQ-a3f8`, `REQ-k2m9`          |
| `sequential` | Auto-incremented numeric IDs  | `REQ-001`, `REQ-002`, `DEC-003` |
| `manual`     | Manually specified string IDs | `auth-module`, `user-service`   |

**Short IDs** (default):

- Automatically generated random base36 strings
- Format: `PREFIX-XXXX` (e.g., `REQ-a3f8`)
- Compact and collision-resistant
- Excluded from gap analysis (no sequence to track)

**Sequential IDs**:

- Auto-incremented numeric suffix
- Format: `PREFIX-NNN` (e.g., `REQ-001`)
- Gap analysis detects missing numbers in sequences

**Manual IDs**:

- Require `--id` flag when creating entities
- No automatic generation
- Excluded from gap analysis

```yaml
entities:
  # Short IDs (default behavior)
  requirement:
    label: Requirement
    id_prefix: REQ-
    id_type: short  # State it explicitly: an entity with no id_type is
                    # treated as legacy syntax and refused at load.

  # Sequential IDs for numbered tracking
  decision:
    label: Decision
    id_prefix: ADR-
    id_type: sequential

  # Manual IDs for components/modules
  component:
    label: Component
    id_type: manual
    # id_prefix not needed for manual IDs
    properties:
      name:
        type: string
        required: true
```

Creating entities:

```bash
# Short ID (default, auto-generated)
rela create requirement -P title="User authentication"
# Creates REQ-a3f8

# Sequential ID (auto-incremented)
rela create decision -P title="Use PostgreSQL for persistence"
# Creates ADR-001

# Manual ID (requires --id)
rela create component --id auth-service -P title="Authentication Service"
# Creates auth-service
```

### Entity Descriptions

Add a `description` field to document the intent and usage of an entity type. Descriptions
support markdown and are surfaced in the data-entry UI via help modals:

```yaml
entities:
  decision:
    label: Decision
    description: |
      A decision records an important architectural choice and its rationale.

      Use decisions when:
      - Making technology choices (frameworks, databases, etc.)
      - Defining patterns or conventions
      - Resolving requirement conflicts

      Each decision should address one or more requirements.
    properties:
      # ...
```

In the data-entry UI, a help icon (?) appears next to the entity form title. Clicking it
opens a modal showing the entity description, all properties with their descriptions, and
available relations with cardinality constraints.

### Entity Styling

Customize how entity types appear in graph visualizations with `color` and `border_color`:

```yaml
entities:
  risk:
    label: Risk
    id_prefix: RISK-
    color: "#FFEBEE" # Light red fill
    border_color: "#C62828" # Dark red border
    properties:
      # ...

  control:
    label: Control
    id_prefix: CTL-
    color: "#E8F5E9" # Light green fill
    border_color: "#2E7D32" # Dark green border
    properties:
      # ...
```

Colors can be specified as:

- Hex codes: `#FF5722`, `#4CAF50`
- Named colors: `red`, `green`, `lightblue`

These colors are used in:

- `rela graph` DOT output
- `rela schema --graphviz` visualization
- Data-entry graph views

### Example Entity Type

```yaml
entities:
  requirement:
    label: Requirement
    aliases: [req]
    id_prefix: REQ-
    properties:
      title:
        type: string
        required: true
      description:
        type: string
      status:
        type: status # References custom type above
        required: true
      priority:
        type: priority
```

### Property Types

| Type       | Description                                   | Filter Operators                    |
| ---------- | --------------------------------------------- | ----------------------------------- |
| `string`   | Free-form text                                | `=`, `!=`, `=~` (regex), glob (`*`) |
| `date`     | Date value (ISO 8601 by default)              | `=`, `!=`, `<`, `<=`, `>`, `>=`     |
| `datetime` | Time-bearing instant (RFC3339, stored as UTC) | `=`, `!=`, `<`, `<=`, `>`, `>=`     |
| `integer`  | Whole number                                  | `=`, `!=`, `<`, `<=`, `>`, `>=`     |
| `boolean`  | True or false                                 | `=`, `!=`                           |
| `enum`     | Inline enum with `values`                     | `=`, `!=`                           |
| `file`     | File attachment (stored under `attachments/`) | N/A                                 |
| `<custom>` | Reference to a type defined in `types:`       | `=`, `!=`                           |

### Property Options

| Option           | Description                                                                           |
| ---------------- | ------------------------------------------------------------------------------------- |
| `required: true` | Property must be provided                                                             |
| `default`        | Default value for the property                                                        |
| `format`         | Date format (Go layout string, e.g., `2006-01-02`)                                    |
| `description`    | Documentation for the property                                                        |
| `list: true`     | Allow multiple values (multi-select for enum types)                                   |
| `computed`       | Pure entity-local expression materialized on every write; the property is read-only   |
| `unique: true`   | Natural key: no two entities of the type may share a non-empty value (write-time 422; find pre-existing dups with `rela analyze unique`). On a type declaring `faces:` the constraint holds **per face**: two entities may not share a value within one face, while two faces of the same entity never collide with each other. Only for string-valued properties (`string`, `date`, `datetime`, `enum`, custom types) — not `list`, and not `integer`/`boolean`/`file`. On the PostgreSQL backend this is additionally enforced by an automatically-maintained database index, so it holds even under concurrent writers ([details](postgres-backend.md#derived-schema-unique-constraints)). |
| `max`            | For `file` properties: max attachments (default 1)                                    |
| `accept`         | For `file` properties: narrow the MIME allowlist (e.g. `[application/pdf]`)           |
| `scan_cmd`       | For `file` properties: the scan command (array args); configuring it enables scanning |
| `scan: off`      | For `file` properties: opt out of scanning despite a global `scan_cmd`                |
| `transform`      | For `file` properties: ordered byte transforms, each `{cmd: [...]}` or `{image: {...}}` |

### Computed properties

A property may derive its value from other properties on the same entity:

```yaml
entities:
  risk:
    properties:
      impact: { type: integer }
      likelihood: { type: integer }
      score:
        type: integer
        computed: entity.impact * entity.likelihood
      label:
        type: string
        computed: entity.category .. ": " .. entity.name
```

The expression uses rela's strict, typed Lua-compatible expression language,
not the full Lua runtime. It has no statements, loops, dynamic property access,
store/relations, network or filesystem access. Supported value constructs are
scalar literals, `entity.<property>` reads, checked integer arithmetic, string
concatenation (`..`), value selection with `and`/`or` (below), and the pure
expression functions documented under
[automation conditions](#expression-conditions-condition), including `today`, `date_add`,
`days_between`, and `rrule_next`.

#### Choosing a value with `and`/`or`

There is no `if` expression. As in Lua, `c and x or y` chooses a value: `x`
when `c` is true, otherwise `y`. This maps one enum to another or a number to a
band:

```yaml
level:
  type: assurance # enum: low, medium, high
  computed: >-
    (entity.method == 'password_otp' and 'medium')
    or ((entity.method == 'passkey' or entity.method == 'hardware_key') and 'high')
    or 'low'
band:
  type: integer
  computed: entity.score >= 80 and 3 or entity.score >= 50 and 2 or 1
```

The rules:

- `c and x` gives `x` when `c` is true and nil when it is false. `c` must be a
  boolean, and it must have a value: when `entity.flag` is unset,
  `entity.flag and 'on' or 'off'` is an evaluation error, which rejects the
  write. Compare instead: `entity.flag == true and 'on' or 'off'`.
- `x or y` gives `x` unless `x` is nil. `entity.nickname or 'anonymous'`
  supplies a default for a missing property. An empty string is a value, not
  nil.
- `x` and `y` must have the same type: string, number, integer or date. With
  boolean operands `and`/`or` are ordinary logic, so a boolean cannot be chosen.
  String literals for a date property follow the property's `format:`.
- **A nil value falls through.** In `c and entity.opt or 'low'`, a missing `opt`
  gives `'low'` even when `c` is true. Write `c and (entity.opt or 'none') or
  'low'` when that matters.

The same form works in conditions and `--filter`, for example
`(entity.priority > 4 and 'high' or 'low') == 'high'`. Keep the parentheses:
`and`/`or` bind more loosely than `==`, so without them the comparison
applies to `'low'` only.

Dependencies are inferred from the compiled expression. Computed properties may
depend on other computed properties; rela evaluates them in dependency order.
A self-reference or indirect cycle is a schema-load error. The expression's
static result type must match the property's declared type. Computed list and
file properties are not supported, and `default` cannot be combined with
`computed`.

Computed values are materialized during every entity create, update, patch and
sync apply. They are stored and indexed exactly like authored properties, so
normal filtering, sorting, search and views require no special syntax. Attempts
to set or unset them through the CLI, MCP, Lua or data-entry API are rejected;
data-entry reports `_fields.<name>.writable: false` and renders the value as
read-only.

`today()` and similar clock-dependent expressions capture **write time**. A
stored value does not advance merely because time passes.

Changing `computed` changes the schema-shape hash and reports drift because
existing materialized values need recomputation. `rela migrate gen` drafts a
declarative step for the affected entity type:

```yaml
- recompute_computed: {entity: risk}
```

The step recomputes the complete computed-property graph in dependency order,
including downstream values, for every entity of that type.

Compiled expressions also report SQL portability as groundwork for future
database-side evaluation. Property reads, arithmetic and concatenation are
portable; host-only functions such as `rrule_next` keep working on writes but
mark that program non-portable. Portability never changes expression semantics.

Computed expressions run against the raw write candidate, including properties
that may be hidden from a reader by field ACL. Treat the computed property's own
visibility as a disclosure decision: a broadly visible derived value can reveal
information about a more restricted source field.

### File attachments and `max`

A `file` property holds an attachment. By default it holds **one** file
(`max` unset or `1`): uploading a new file replaces the existing one. Set
`max` above 1 to allow several files on the same property:

```yaml
supporting_docs:
  type: file
  max: 5 # up to 5 files on this property
```

With `max > 1` the property value is a **list** of attachment paths, the
data-entry UI shows a multi-file picker (add up to `max`, remove
individually), and uploading a file whose name already exists auto-suffixes
it (`report.pdf` → `report (1).pdf`). `max` must be `>= 1` and only applies
to `file` properties.

### Attachment security: scanning, allowlist & transforms

Uploaded attachments are inspected before they are stored. A native MIME
allowlist (sniffed, blocks SVG/HTML/executables) is always on; virus scanning
and byte transforms (metadata strip, resize, document disarm) are opt-in and
driven by **external commands you configure** — rela ships no scanner or image
library. Policy lives in a global `attachments:` block plus per-property
overrides:

```yaml
attachments:
  allow: default-safe # MIME allowlist preset (or a list)
  scan_cmd: [clamdscan, --no-summary, "{in}"] # configuring this enables scanning

entities:
  report:
    properties:
      evidence:
        type: file
        transform:
          - cmd: [exiftool, -all=, "{in}", -o, "{out}"] # strip metadata
```

Commands use array args (no shell — no injection) with `{in}`/`{out}`
placeholders rela substitutes with temp paths it owns; each runs under a
timeout and output-size cap. **Configuring a `scan_cmd` enables scanning**
(fail-closed: rejects on a hit **or** when the scanner can't run); a property
can opt out with `scan: off`. See the dedicated
[Attachment Security guide](attachment-security.md)
for the full configuration and vetted command recipes (ClamAV, vips, exiftool,
qpdf, ImageMagick).

### Date Formats

For `date` properties, specify the format using Go layout strings:

```yaml
properties:
  valid_until:
    type: date
    format: "2006-01-02" # YYYY-MM-DD (ISO 8601, default)
```

Common formats:

| Format   | Example      | Go Layout              |
| -------- | ------------ | ---------------------- |
| ISO 8601 | `2025-02-01` | `2006-01-02` (default) |
| European | `01/02/2025` | `02/01/2006`           |
| US       | `02/01/2025` | `01/02/2006`           |
| Long     | `1 Feb 2025` | `2 Jan 2006`           |

### Datetime Properties

Use `datetime` for a **time-bearing instant** (a specific point in time, not
just a calendar day). Unlike `date`, a `datetime` value carries a time-of-day.

```yaml
properties:
  starts_at:
    type: datetime
    description: "When the event begins"
```

Semantics:

- **Stored as UTC RFC3339** (e.g. `2026-07-13T12:30:00Z`). Values written
  through the data-entry app are always normalized to a UTC instant.
- **Bare dates are accepted as midnight UTC.** A hand-edited value like
  `2026-07-13` on a `datetime` property is interpreted as
  `2026-07-13T00:00:00Z`. (Note that such a midnight-UTC value displays on the
  previous evening in time zones west of UTC — see the data-entry docs.)
- **Values may be quoted or unquoted** in YAML frontmatter. An unquoted
  timestamp is parsed as a timestamp; both round-trip correctly.
- **Filtering and sorting compare as instants** (down to the second).
  Equality (`=`) is therefore strict-instant: `starts_at=2026-07-13` (which
  parses as midnight) does **not** match `2026-07-13T12:30:00Z`. Use `>=` and
  `<` to query a day or range.
- **Mixed `date` + `datetime` columns sort chronologically** together.

The data-entry app renders `datetime` properties with a date+time picker and a
configurable display time zone — see the data-entry guide.

A calendar-feed source can use a `datetime` property (as its `date:` or
`end_date:`) to emit a **timed** event; a `date` property emits an all-day
event. Start and end must be the same kind (all-day or timed), and timed events
are rendered in UTC.

### Property Type Examples

```yaml
properties:
  # String - free-form text
  title:
    type: string
    required: true

  # Date with explicit format
  valid_until:
    type: date
    format: "2006-01-02"
    description: "When this evidence expires"

  # Integer
  risk_score:
    type: integer
    description: "Risk score from 1-10"

  # Boolean
  archived:
    type: boolean

  # Inline enum
  severity:
    type: enum
    values: [low, medium, high, critical]

  # Reference to custom type
  status:
    type: status
    required: true

  # File attachment
  screenshot:
    type: file
    description: "Screenshot of the issue"

  # Multi-select enum (list: true)
  tags:
    type: enum
    values: [frontend, backend, api, database, security]
    list: true # Allows selecting multiple values
```

## Relations

Relations define how entity types can be connected:

| Field          | Description                                     |
| -------------- | ----------------------------------------------- |
| `label`        | Display name                                    |
| `description`  | Explanation of the relation's meaning           |
| `from`         | Source entity types (list)                      |
| `to`           | Target entity types (list)                      |
| `inverse`      | Inverse relation definition (string or object)  |
| `symmetric`    | `true` if relation is bidirectional             |
| `min_outgoing` | Minimum outgoing relations per from-side entity |
| `max_outgoing` | Maximum outgoing relations per from-side entity |
| `min_incoming` | Minimum incoming relations per to-side entity   |
| `max_incoming` | Maximum incoming relations per to-side entity   |
| `scope`        | `identity` (default) or `content` — what the relation attaches to under content states (see below) |

### Relation scope (`scope:`)

With content states (an entity holding several faces such as `draft` and
`published`), every relation type declares what its edges attach to:

```yaml
relations:
  owned-by:    { scope: identity }   # attaches to the entity; shared by all states
  references:  { scope: content }    # attaches to a specific state (its tail side)
```

- **`identity`** (the default): the edge belongs to the entity as a
  whole. Ownership, containment, and membership are identity facts — a
  draft does not get a different owner than its published face by
  accident. A project that never uses content states behaves identically
  with or without the declaration.
- **`content`**: the edge belongs to one state on its **source** side; a
  draft may reference different targets than the published face. Targets
  are always entity-level — a relation can never point *at* a specific
  state of its target.

Unknown values are a load error. The declaration is consumed by worlds, which
serve a face's own content-scoped edges and resolve each neighbour through the
same world, and by copy definitions, which may copy content-scoped edges but
never identity-scoped ones. See
[Content States and Worlds](#content-states-and-worlds).

### Example Relation

```yaml
relations:
  addresses:
    label: addresses
    description: A decision addresses a requirement
    from: [decision]
    to: [requirement]
    min_outgoing: 1 # Each decision must address at least one requirement
    inverse: addressedBy # Simple form - the ID is also the display label
```

### Inverse Relations

The `inverse` field can be specified in two forms:

**Simple form** (recommended for most cases):

```yaml
inverse: addressedBy # The ID doubles as the display label
```

Without an explicit `label`, the ID itself is displayed — `addressedBy` renders as
`addressedBy`. Labels are **authored, never derived**: rela does not convert an
identifier into prose, because any such conversion encodes an English orthographic
convention (word splitting, capitalization) that is wrong for most languages. Use the
expanded form below to control the display text.

**Expanded form** (recommended whenever the ID is not the text you want shown):

```yaml
inverse:
  id: addressedBy
  label: "is addressed by" # Custom label
```

The inverse ID is also the key under which incoming edges are grouped in the data-entry
API's `GET /api/v1/{plural}/{id}/relations` response, and it's what surfaces in the help
modal's "Incoming relations" section. See [data-entry.md → Reverse Relations](data-entry.md#reverse-incoming-relations)
for how form widgets and list columns opt into reverse direction with `direction: incoming`.

#### Inverse name uniqueness

Inverse names must be globally unique across the metamodel. Two failure modes
are rejected at load time:

- **`inverse_name_collision`** — two relations declare the same `inverse:` ID.
  rela cannot tell which canonical relation an inverse-keyed lookup refers to,
  so this is treated as a structural error. Example:

  ```yaml
  relations:
    blocks:
      inverse: blockedBy
    prevents:
      inverse: blockedBy # rejected: collides with `blocks`
  ```

- **`inverse_shadows_canonical`** — a relation declares `inverse: X` where `X`
  is also the name of a separate canonical relation. The metamodel author
  most likely didn't mean for `X` to refer to two different relation sets at
  once. Example:

  ```yaml
  relations:
    r1:
      inverse: r2
    r2: # rejected: shadows the inverse of `r1`
      from: [...]
      to: [...]
  ```

**Exception:** symmetric relations are allowed to be their own inverse:

```yaml
relations:
  related-to:
    symmetric: true
    inverse: related-to # OK — symmetric self-inverse
```

Use the symmetric form when the relation has no preferred direction (e.g. "is
related to" reads the same from either side).

### Cardinality Constraints

Use cardinality to enforce rules:

```yaml
relations:
  implements:
    label: implements
    from: [solution]
    to: [decision]
    min_outgoing: 1 # Every solution must implement at least one decision
    max_incoming: 1 # Each decision can only be implemented by one solution
```

Check violations with:

```bash
rela analyze cardinality
```

### Symmetric Relations

For relations that work in both directions:

```yaml
relations:
  conflictsWith:
    label: conflicts with
    from: [requirement, decision]
    to: [requirement, decision]
    symmetric: true
```

## Query Scopes

An entity type can declare **query scopes**: named boolean expressions that
decide which rows a screen shows. A list or kanban selects one by name, and a
scope called `default` applies to every screen of that type unless the view
names another. The expression language is the same one
[`condition:`](#expression-conditions-condition) uses.

```yaml
entities:
  taak:
    label: Task
    properties:
      status: { type: status_enum }
      toegewezen_aan: { type: string }
    query_scopes:
      default: "entity.status ~= 'gearchiveerd'"   # hide archived everywhere
      actief:  "entity.status ~= 'gereed'"
      mijn:    "is_current_user(entity.toegewezen_aan)"
      archief: "entity.status == 'gearchiveerd'"
```

Declaring the rule on the type rather than on each screen means the project has
a vocabulary: "active tasks" is a thing an operator can refer to, and the rule
behind it is written once. It also means rela needs no built-in idea of what
"archived" means — the operator declares an ordinary property and two scopes,
and a concept rela would otherwise have to carry through the store, the API,
the search index and every export path stays out of the product entirely.

| Name | Meaning |
| --- | --- |
| `default` | Applies to every list, kanban and sidebar `entities:` item of this type that names no scope. Optional; without it, screens are unfiltered. |
| `all` | **Implicit and always available.** Resolves to no predicate, so `query_scope: all` withdraws the `default`. Reserved — a type cannot declare a scope by this name. |
| anything else | Applies only where a view names it. |

Scope names must start with a letter and may contain letters, digits, and
single `-` or `_` separators between them.

### Query scopes are presentation, not access control

**A query scope never decides what a principal is allowed to see.** The ACL
read gate in `acl.yaml` runs first and independently; a scope narrows what is
already permitted, for presentation reasons. This matters because the two read
alike in YAML: a scope written as `mijn: "is_current_user(entity.owner)"` looks
like "only my rows are visible", but it is a display filter, and anyone who
can reach the API can ask for `query_scope: all`. To restrict what a role may
read, use `scope_grants:` — see
[ACL: Authorization Overview](acl-overview.md).

One consequence worth knowing about: a scope that reads a property some role
hides with `visible:` earns a startup warning naming the scope, the property
and the roles. It is a warning rather than a refusal precisely because the row
was already gated on its own terms — but the two declarations disagree about
the same property, and whichever you meant, you probably did not mean both.

### Query scopes and worlds are different operations

They look adjacent and neither replaces the other:

| | `worlds:` | `query_scopes:` |
| --- | --- | --- |
| operates on | faces (content states of one entity) | property values |
| semantics | rank the faces and pick one per entity | include or exclude the entity |
| row count | never changes it | reduces it |
| the question | which version of X do I show | is X here at all |

So "archived" cannot be a face: an archived task is not a variant of a live
task that you would rank against it, it is a task that should not appear. The
two compose — a world picks the face, a scope decides whether the row is in the
list at all.

### Identity in a scope

A scope may read `current_user`, which is what makes `mijn:` above work. Two
consequences follow:

- A scope that never mentions the identity is evaluated exactly as an ordinary
  expression, and works on a deployment with no authentication.
- A scope that **does** mention it requires a resolved principal. On an
  anonymous deployment such a screen fails with an error rather than rendering
  an empty page, because the honest answers are the right rows or a failure,
  never somebody else's rows. Putting one in `default` therefore makes every
  screen of that type identity-dependent, and earns a startup warning saying
  so.

### Filtering on related entities

A scope can filter on the entities at the other end of a relation with
`related(entity, path, constraints)`. It is true when at least one entity
reached by walking `path` from the row matches `constraints`.

```yaml
relations:
  implements:
    label: implements
    inverse: implementedBy
    from: [ticket]
    to: [feature]

entities:
  feature:
    query_scopes:
      # Features with at least one ticket in progress.
      busy: "related(entity, 'implementedBy', { status = 'in-progress' })"
      # Features with none.
      idle: "not related(entity, 'implementedBy', { status = 'in-progress' })"
```

The arguments are:

- `entity`, the row being filtered. No other subject is accepted.
- `path`, one relation name or a list of them, such as
  `{'implementedBy', 'assigned-to'}`. The canonical name walks the relation
  forwards, from its `from:` side to its `to:` side. The `inverse:` name walks
  it backwards. A chain may mix the two.
- `constraints`, an optional table. `type = 'ticket'` picks one type where the
  relation allows several. `id = 'PERS-001'` matches the final entity by its
  id. Every other key compares a property of the final entity with a string,
  for equality. A value may also be `current_user.id`, as described under
  [Matching the current user](#matching-the-current-user).

An entity the reader may not see does not count. It neither makes a row match
`related(...)` nor stops a row matching `not related(...)`. The result can
therefore differ per reader, and it is never shared between readers.

The following limits apply:

- Only the default content state takes part. An edge from a named face, or an
  endpoint's named face, is ignored.
- A relation declared `symmetric: true` is refused, because it has no
  direction to walk.
- The constraint compares for equality with a non-empty string or with
  `current_user.id`. The property must be string-shaped: a string, enum, date,
  datetime or declared custom type, and not a list. For an enum, the value
  must be one of its declared values, so `current_user.id` is refused on an
  enum.
- A property that some role cannot see unconditionally, through `visible:`, is
  refused at startup. Matching on it would reveal its value to readers who
  cannot see it. After a live reload of `schema.yaml` or `acl.yaml`, the same
  case is refused per request instead.
- A form condition refuses `related(...)` at load. Forms evaluate their
  conditions in the browser, which cannot read relations.

#### Matching the current user

When ownership is a relation rather than a property, compare the final
entity's id with `current_user.id`. With a relation `verantwoordelijk_voor`
from `persoon` to `taak`, whose inverse is `heeft_verantwoordelijke`, and
`user_entity_type: persoon` in `acl.yaml`, this scope lists the open tasks of
the signed-in user:

```yaml
entities:
  taak:
    query_scopes:
      mijn: "entity.status ~= 'gereed' and related(entity, 'heeft_verantwoordelijke', { id = current_user.id })"
```

`current_user.id` is the only variable a constraint may read. It may stand
for `id` or for a string-shaped property that is not an enum.

The scope reads the identity, so the rules under
[Identity in a scope](#identity-in-a-scope) apply. A request without a
resolved user fails; it never returns rows.

The user's own entity follows the same read rules as any other. If the
reader's role cannot read the user entity type, the walk reaches nothing:
`related(...)` matches no row, and `not related(...)` matches every row the
reader can see. Grant read access on the user entity type when a scope must
tell the user's rows apart.

An ACL `when:` in `acl.yaml` refuses `current_user.id` in `related(...)` at
load. A grant must not depend on a second derivation of who is calling. Use
`role_relations:` to grant access through a relation to the user.

#### Where `related(...)` works

Every surface below checks the path and constraints when it loads. A surface
answers each traversal once per page or batch, not once per row. Which
entities count depends on the surface:

| Surface | Which entities count |
| --- | --- |
| `query_scopes:` | Those the reader may see |
| View (list) `condition:` and next-action `condition:` | Those the reader may see |
| Validation `when_condition:` / `then_condition:` in the web app and over MCP | Those the caller may see |
| MCP `list_entities` `filter` | Those the caller may see |
| Validation in `rela validate` and `rela analyze validations` | All |
| Automation `on.condition:` | All |
| State-machine transition `when:` | All |
| ACL `when:` in `acl.yaml` | All |

The last four run with system trust, so a hidden entity still counts. An ACL
`when:` or a transition `when:` therefore reveals one bit about entities the
principal may not read: whether the grant applies, or whether the transition
is offered. Keep such conditions to facts you are willing to disclose that
way. A traversal in either that compares a property some role cannot see
through `visible:` earns a startup warning. In
a view or next-action condition the same traversal fails every request that
evaluates it.

A surface refuses rather than guesses when it cannot answer a traversal:

- An automation's condition, a transition's `when:` and an ACL grant fail
  closed. The automation does not run, the transition is refused, and the
  grant does not apply.
- A validation rule reports a load error for the rule and checks no entity
  against it.
- A view or next-action condition fails the request.

Entities on a named face cannot be answered, because the store walks the
default face's edges. An ACL `when:` also denies a historical version, such as
one read from the history view.

Some grants cannot be expressed in the traversal's store query. A request from
a principal with such a grant fails with HTTP 422 and the error code
`query_scope_unsupported`, naming the cause. The same scope still works for
other principals. The cases are:

- the principal reads the type a hop lands on only on named faces, or only
  through `inherit_roles_through`;
- the first hop walks forwards and the principal reads the scope's own type
  only on named faces;
- the principal's read of a type is a disjunction, for example from several
  `scope_grants`;
- the client ceiling hides a compared property;
- a hop lands on a type that the principal reads through a role relation,
  such as `editor-of`, and the next hop walks backwards.

On the PostgreSQL and SQLite backends each property constraint gets a derived
index on the final entity type, so the filter does not scan every row of that
type. This covers query scopes and view and next-action conditions. An `id`
constraint derives no index: the store looks the named entity up by its key.

A list page answers the whole scope in the store query when the scope is a
plain conjunction. Every part joined by `and` must be one of these:

- `related(...)`;
- an equality between a string-shaped property, as defined above, and a
  non-empty string;
- `entity.x == current_user.id` or `is_current_user(entity.x)`.

Each property may appear in only one equality. The page and its total then
cost the same at any list size.

Any other scope, for example one with `not`, `or` or an ordered comparison, is
still correct. It is evaluated after the store read, and its cost grows with
the number of rows of the type. The same slower path applies when the rest of
the request does not suit the store, for example with free-text search, a view
`condition:`, a relation filter, or a sort on a property that is not
string-shaped.

### Which surfaces apply scopes

Scopes shape **screens**. They are deliberately absent everywhere that answers
"what is actually in the graph":

| Applies | Does not apply |
| --- | --- |
| Lists, kanbans, and list search in the web app | `rela list`, `rela export`, `rela validate` |
| The list API, where `?query_scope=` selects one | `rela analyze` and the MCP `analyze` tool |
| | MCP `list_entities`, Lua `rela.list_entities` |
| | Trace and orphan reports |

The split is not an oversight. If the cardinality check honoured a `default`
scope that hides archived rows, archiving an entity with a missing required
relation would silence the violation — and `rela validate` would report clean
over data it was never shown.

#### Load-time checks on query scopes

The loader refuses the whole schema, and reports every problem it finds, when a
query scope:

- has a name outside the grammar above, or is called `all` in any
  capitalization;
- has an empty expression;
- fails to compile, or reads a property the entity type does not declare — the
  error names the type, the scope and the expression;
- uses `related(...)` with a relation that does not exist, does not start from
  the type, or is symmetric, or compares a property that the final type does
  not declare or that is not string-shaped;
- uses `related(...)` on a property that some role in `acl.yaml` cannot see
  unconditionally.

A `query_scope:` in `data-entry.yaml` naming a scope the type does not declare
is refused when that config loads, listing the names the type does declare. It
never falls back to unfiltered: the difference between a typo that shows
nothing and a typo that shows archived records to everyone is the whole point.

## Content States and Worlds

An entity type can declare **faces**: several content states of one entity,
such as `draft` and `published`, or `en` and `nl`. A **world** is a named rule
that picks one face per entity for a reader. A **copy** moves content from one
face to another under a permission guard. This section is the key reference
for all three declarations. The guide
[How To Publish Content with Faces and Worlds](content-states.md) walks through
a complete setup, and the
[Data Entry Web App guide](data-entry.md#worlds-in-the-web-app-and-api) covers
the HTTP API and the web app.

All three declarations are optional. A schema with no `faces:`, `worlds:`, or
`copies:` behaves exactly as it did before, and a project can mix types that
declare faces with types that do not.

### Declaring faces

```yaml
entities:
  policy:
    label: Policy              # every face is a named row: POL-1@draft, POL-1@published
    faces:
      draft:     { label: "Draft" }
      published: { label: "Published" }
  ticket:
    label: Ticket              # no faces: one state, present in every world
```

| Key | Meaning |
| --- | --- |
| `faces` | A map from face name to face definition. A type without it has exactly one state, and that state appears in every world. |
| `faces.<name>.label` | Display text for the web app. Falls back to the face name. It has no effect on resolution. |
| `faces.<name>.messages.read_only` | The sentence the web app shows on a page or form that reached this face while the reader may not write it. Placeholders `{face}` (this face's label), `{world}`, `{title}` (the entity's display title). Undeclared shows nothing. |
| `faces.<name>.messages.notice` | The sentence the web app shows on a detail page that reached this face, whatever the reader may do with it — "this is a draft and not yet in force". Same placeholders. Undeclared shows nothing. |

The two message keys differ in who the sentence is about, and that decides
which one you want. `read_only` is about the **reader**: it appears only when
they may not write the face, so it explains a missing Edit button. `notice` is
about the **document**: it appears whatever they may do, because a draft is not
in force even for the editor rewriting it. A face that is writable by
definition can never show a `read_only`, which is why marking a draft needs the
second key. Both may be declared on one face; the page shows `notice` first.

```yaml
faces:
  concept:
    label: Concept
    messages:
      notice: 'Let op: dit is een concept en nog niet vastgesteld.'
  vastgesteld:
    label: Vastgesteld
    # nothing declared: this face renders no note
```

A face's declared name is also the coordinate its row is stored at, so the same
spelling works in a URL, in an `acl.yaml` grant, and in a `copies:` address. A
type declaring `faces:` stores nothing under the bare entity id: `POL-1` names
no row, only `POL-1@draft` and `POL-1@published` do. A type declaring no faces
is the opposite — it has one unnamed state stored under the bare id, and is
unaffected by any of this.

Adding `faces:` to a type that already holds data is therefore a migration, not
a relabelling: the existing rows sit at a coordinate that names no declared
face. See [Data Migration](data-migration.md).

A face name is a run of lowercase letters and digits, with further runs joined
by single hyphens: `draft`, `published`, `in-review`. Uppercase letters,
underscores, a leading digit, doubled hyphens, and `+` are rejected when the
schema loads. World names use the same grammar, because both reach URLs and
`acl.yaml` grants.

### Declaring worlds

```yaml
worlds:
  published:
    select: published
    overrides:
      guide: [en]                 # guides have no published face; English is it
    otherwise: exclude
    banner: "Published — this is what readers see"
  editorial:
    select: [draft, published]    # first existing face wins
    otherwise: default
  site-nl:
    select: [nl, en]
    otherwise: default
```

| Key | Meaning |
| --- | --- |
| `select` | The face to show, or an ordered list. The first face the entity has wins. A single name and a one-element list mean the same thing. |
| `overrides` | A map from entity type to a chain that replaces `select` for that type. It replaces the chain rather than extending it. |
| `otherwise` | **Required.** What happens to an entity whose type declares faces but that has none the chain names: `exclude` leaves it out of the world, `default` shows its unnamed state — which a faced type does not have, so `default` excludes it too. |
| `banner` | Optional text the web app shows on every page in this world. Empty shows no announcement. |
| `messages` | Optional. The web app's wording for what this world changes on a screen: `absent` (a detail page for an entity with no face here; placeholders `{face}`, `{world}`, `{title}`), `projection` (a list or board note on a faced type; `{world}` only, since a list has no single entity), `stand_in` (the badge on a row served a stand-in; `{face}`, `{world}`). A placeholder a surface cannot fill is left as written. The app has no default sentence; an undeclared entry shows nothing. |
| `on_absent` | Optional. `redirect: <world>` sends a reader who opens an entity with no face in this world to that world (or `default`) instead of showing the page. |
| `primary_for` | Optional. The faces this world is the canonical home of. Needed only when two worlds lead with the same face for a type. See below. |
| `edits` | Accepted and validated as a declared face name. Not used yet. |

A world resolves each entity to **at most one** face, using three rules in
order:

1. The type declares no faces, so the entity appears with its only state.
2. The entity has a face the chain names, so the first such face in chain
   order is shown.
3. Otherwise, `otherwise:` decides: `exclude` or `default`.

Rule 2 is why publishing works. If `POL-1` has no `published` face, it does not
exist in the `published` world. Absence is the publication bit.

Rule 3's `default` substitutes an entity's single unnamed state. A type
declaring `faces:` has no such state, so for those types `default` has nothing
to substitute and behaves as `exclude`. Write chains that name a face every
entity of the type can be expected to have.

`otherwise:` has no default, and a world without it does not load. The two
values are opposites and both are reasonable: a public world wants `exclude`,
an internal one usually wants `default`. Guessing wrong would mean a
`published` world quietly serving a draft, so the schema has to say which one
it means.

Every project also has an implicit **default world**, which applies no
resolution and serves each entity's unnamed state. A faced type has none, so
its rows are reached there only by addressing a face as `ID@face`. It needs no
declaration, it always exists, and the name `default` is reserved so nothing
can shadow it. Reading any other world
requires a `world:<name>` grant in `acl.yaml`; see the
[ACL: Authorization Overview](acl-overview.md#scoping-a-grant-to-a-content-state).

#### Load-time checks on worlds

The loader refuses the whole schema, and reports every problem it finds, when
a world:

- declares neither `select:` nor `overrides:`, which would resolve every faced
  entity through `otherwise:` alone;
- omits `otherwise:`, or gives it a value other than `exclude` or `default`;
- names a face in `select:` that no entity type declares;
- names, in `overrides:`, an unknown type, a type that declares no faces, an
  empty chain, or a face that type does not declare;
- names a face in `edits:` or `primary_for:` that it does not qualify for;
- sets `on_absent.redirect` to a world that is not declared, or to a chain
  of redirects that returns to a world it has visited (itself included);
- is called `default` in any capitalization, or has a name outside the face
  grammar.

### `primary_for:` — only when two worlds lead the same face

A face switcher in the web app ("go to the Dutch version") has to name a
**world**, because `?world=` is how a face is read and a face name is not a
world. Which world serves a face is normally inferred: it is the world whose
chain **leads** with that face. `site-nl` selecting `[nl, en]` is the world
that serves `nl`. It is not the world that serves `en`, which it only falls
back to.

That inference is unambiguous until two worlds lead the same face for the same
type. Then it is a genuine tie, and `primary_for:` breaks it:

```yaml
worlds:
  site-nl:
    select: [nl, en]
    otherwise: default
    primary_for: nl        # the canonical home of the Dutch face
  editorial-nl:
    select: [nl]
    otherwise: default
```

Two rules are enforced at load:

- **An undeclared tie is an error.** If several worlds lead a face for a type
  and none claims it, the schema does not load. Picking one would depend on the
  order the configuration happened to serialize in.
- **A claim may only confirm, never contradict.** Naming a face this world does
  not lead is an error, because the resulting control would navigate to a world
  where the face is not primary.

Sharing a chain head **is** the tie, whatever the worlds' `otherwise:` says. A
published world where absence means "not published", beside a lenient sibling
that substitutes instead, still needs one of them to claim the face: a reader
asking to be taken to a face they **have** is served the same row by both, so
`otherwise:` — which decides what happens to entities *lacking* the face —
cannot say which world the switch means.

The result is that every (type, face) some world leads resolves to exactly one
world, or the schema does not load. Anything naming the world that serves a
face can rely on getting an answer.

The key sits on the **world**, not on the face, because `overrides:` makes the
answer per type and face: one world can lead `en` for `guide` while another
leads it for `policy`.

### Relation scope under faces

Every relation type declares whether its edges belong to the entity or to one
face, with the `scope:` key described under [Relations](#relation-scope-scope).
When a reader in a world looks at an entity's relations, the edges are those
of the face being served, and each neighbour is resolved through the same
world on its own.

### Declaring copies

An ordinary write addresses one face by name and changes that face alone.
Moving content BETWEEN faces is a different operation: it happens only through
a **copy definition** that names the target face and carries its own permission
guard, which is what makes publishing an authorized operation rather than a
field edit.

```yaml
copies:
  publish:
    from: policy@draft
    to: policy@published
    label: Publish
    fields: all
    relations:
      implements: replace
    guard:
      permission: publish-policy
```

| Key | Meaning |
| --- | --- |
| `from` | The source face, as `type` or `type@face`. |
| `to` | The target face. When it names a face, `guard:` is mandatory. |
| `label` | Display text for the action in the web app. Plain text, no interpolation. Falls back to the definition name. |
| `on_success.message` | The confirmation the web app shows after the copy. Placeholders as for world messages; `{face}` is the face written. Falls back to the label. |
| `on_success.landing` | Where the web app goes afterwards: `written` (the face written, the default), `stay` (reload in place), `{world: <name>}` or `{face: <name>}`. |
| `fields` | `all` to copy every declared property, or a map from target property to source expression using the `{{...}}` interpolation grammar. A copy between different types requires an explicit map. |
| `relations` | A map from relation type to `merge` (add the edges the target lacks) or `replace` (swap the target face's edges of that type). Only `scope: content` relation types can be listed. An omitted type is not copied. |
| `guard.permission` | The ACL permission a caller must hold on the source entity. **Required** whenever `to` names a face. |

A request invokes a definition by name and never supplies a mapping. See
[Invoking a copy](data-entry.md#invoking-a-copy) for the HTTP surface.

#### Load-time checks on copies

The loader refuses a copy definition that:

- targets a face without a `guard.permission`, because an unguarded
  definition would open the face to anyone who can name the copy;
- names a face or type that the schema does not declare;
- copies no fields, or declares both `fields: all` and a field map;
- uses `fields: all` on a copy into a different entity. That copy reads
  through the caller's visibility, so copying every field would write a
  redacted entity;
- lists an identity-scoped relation type, because such an edge is shared by
  every face and copying it could duplicate an edge that confers roles;
- sets `guard.when`, which is not implemented yet. A condition that is written
  but never evaluated is refused rather than ignored;
- sets `on_success.landing` to anything but `written`, `stay`, a declared
  world, or a face the target type declares — or to both a world and a face,
  to an empty mapping, or to a mapping with a key other than `world` or `face`.

A copy runs as one store transaction and is audited after it commits. The
PostgreSQL backend rolls back a failed copy. On the filesystem and in-memory
backends the transaction is a write lock only, so a failure part-way through
can leave a partially written target face.

### Checking stored states

`rela analyze states` reports state rows the schema does not account for. Three
kinds, each with its own remedy:

| Finding | Meaning |
| --- | --- |
| `undeclared-face` | A row under a face name no type declares, for example after a `faces:` entry was renamed or removed. |
| `bare-row-on-faced-type` | A row at the bare id on a type that declares `faces:`. No world's chain names the bare id, so nothing reaches the row. |
| `unknown-entity-type` | A row whose entity type the schema does not define at all. |

A face is checked against **its own entity type**: a `draft` row on a type that
declares no faces is reported even if another type declares `draft`. Each
finding names the thing you act on — the face for the first, the entity type for
the other two — because the remedy is per-type for those.

This is detection only. To move rows between faces, use the `rename_face` step
of the [data migration system](data-migration.md#renaming-a-content-state); to
adopt bare rows into a face, use `migrate_face`.

## Default Metamodel

When you run `rela init`, this default metamodel is created:

```yaml
version: "1.0"
namespace: "https://example.org/ontology/architecture#"

types:
  status:
    values: [draft, proposed, accepted, deprecated, rejected, retired]
    default: draft

  priority:
    values: [critical, high, medium, low]

entities:
  requirement:
    label: Requirement
    aliases: [req]
    id_prefix: REQ-
    properties:
      title:
        type: string
        required: true
      description:
        type: string
      status:
        type: status
        required: true
      priority:
        type: priority

  decision:
    label: Decision
    aliases: [dec, adr]
    id_prefixes: ["DEC-", "ADR-"]
    properties:
      title:
        type: string
        required: true
      rationale:
        type: string
      status:
        type: status
        required: true

  solution:
    label: Solution
    aliases: [sol]
    id_prefix: SOL-
    properties:
      title:
        type: string
        required: true
      description:
        type: string
      status:
        type: status

  component:
    label: Component
    aliases: [comp]
    id_prefixes: ["COMP-", "AC-", "TC-"]
    properties:
      title:
        type: string
        required: true

relations:
  addresses:
    label: addresses
    description: A decision addresses a requirement
    from: [decision]
    to: [requirement]
    inverse: addressedBy

  implements:
    label: implements
    description: A solution implements a decision
    from: [solution]
    to: [decision]
    inverse: implementedBy

  realizes:
    label: realizes
    description: A component realizes a solution
    from: [component]
    to: [solution]
    inverse: realizedBy

  dependsOn:
    label: depends on
    from: [component, solution, decision]
    to: [component, solution, decision]
    inverse: dependencyOf
```

## Customization Examples

### Adding a Risk Entity Type

```yaml
entities:
  risk:
    label: Risk
    id_prefix: RISK-
    properties:
      title:
        type: string
        required: true
      likelihood:
        type: enum
        values: [low, medium, high, critical]
      impact:
        type: enum
        values: [low, medium, high, critical]

relations:
  mitigates:
    label: mitigates
    from: [decision, solution]
    to: [risk]
    inverse: mitigatedBy
```

### Adding a Stakeholder Type

```yaml
entities:
  stakeholder:
    label: Stakeholder
    aliases: [stk]
    id_prefix: STK-
    properties:
      name:
        type: string
        required: true
      role:
        type: string

relations:
  ownedBy:
    label: owned by
    from: [requirement, decision, component]
    to: [stakeholder]
    inverse: owns
```

### Multiple ID Patterns

Support different ID conventions in the same project:

```yaml
entities:
  requirement:
    label: Requirement
    aliases: [req]
    id_prefixes: ["REQ-", "FR-", "NFR-"] # Functional and non-functional
```

## After Modifying the Metamodel

After editing `schema.yaml`:

```bash
# Rebuild the cache
rela sync

# Verify with
rela tui
# Press 'm' to see the updated metamodel
```

Note: Existing entities remain valid. The metamodel only affects creation and validation of new entities and relations.

**Schema evolution:** additive changes (new types, new optional properties,
new enum values) are adopted automatically. Changes that leave stored data
mismatched — a renamed property, a changed property type, remapped enum
values — are detected at startup and handled by the data-migration system:
`rela migrate status` shows where the data stands, `rela migrate gen` drafts
a migration, `rela migrate data` runs it. See the
[data-migration guide](data-migration.md).

## Filtering Entities

Filter entities by property values using the `--where` flag:

```bash
# Exact match
rela list control --where "status=accepted"

# Glob pattern (strings only, use * for wildcard)
rela list control --where "iso27001=A.9.*"

# Regex match (strings only)
rela list control --where "title=~access.*policy"

# Date comparison
rela list evidence --where "valid_until<2025-02-01"
rela list evidence --where "valid_until>=2025-01-01"

# Integer comparison
rela list risk --where "risk_score>=5"
rela list risk --where "risk_score<10"

# Boolean filter
rela list evidence --where "archived=false"

# Multiple filters (AND logic)
rela list control --where "status=implemented" --where "applicability=applicable"
```

### Filter Operators

| Operator | Description                 | Supported Types   |
| -------- | --------------------------- | ----------------- |
| `=`      | Equal (exact match or glob) | All types         |
| `!=`     | Not equal                   | All types         |
| `<`      | Less than                   | `date`, `datetime`, `integer` |
| `<=`     | Less than or equal          | `date`, `datetime`, `integer` |
| `>`      | Greater than                | `date`, `datetime`, `integer` |
| `>=`     | Greater than or equal       | `date`, `datetime`, `integer` |
| `=~`     | Regex match                 | `string`          |

### Error Handling

Invalid filters produce helpful error messages:

```bash
# Unknown property
rela list control --where "typo=value"
# Error: unknown property "typo" for entity type "control"

# Invalid enum value
rela list control --where "status=invalid"
# Error: invalid value "invalid" (allowed: draft, proposed, accepted, ...)

# Invalid date format
rela list evidence --where "valid_until=not-a-date"
# Error: invalid date "not-a-date" for property "valid_until" (expected format: 2006-01-02)

# Invalid operator for type
rela list control --where "status>draft"
# Error: operator ">" not supported for enum property
```

## Sorting Entities

Sort entities by property values using the `--sort` flag:

```bash
# Sort by property (ascending)
rela list control --sort iso27001

# Sort descending
rela list evidence --sort valid_until --desc

# Sort by ID (default)
rela list control --sort id
```

Sorting is type-aware:

- `string`: Lexicographic (alphabetical)
- `enum`/custom types: By the order defined in the type's `values` list (not alphabetical)
- `date`: Chronological
- `datetime`: Chronological, to the second (interleaves with `date`)
- `integer`: Numeric
- `boolean`: `false` before `true`

Entities with missing values for the sort property are placed at the end.

### Default Sort Order

Entity types can declare a default sort order in the metamodel. This is used when no explicit
sort is specified in a query or CLI command:

```yaml
entities:
  ticket:
    label: Ticket
    id_prefix: "TKT-"
    default_sort:
      - property: priority
      - property: due_date
        direction: asc
    properties:
      # ...
```

Each entry in `default_sort` is a sort criterion applied in order (first entry is the primary key).
The `direction` field is optional and defaults to `"asc"`. Supported values: `"asc"` or `"desc"`.

You can sort by any property defined on the entity, plus two virtual properties:

- `id` — sorts by entity ID
- `modified` — sorts by file modification time

### Sort in Search Queries

The TUI search screen and data entry search bar support a `sort:` clause:

```text
sort:priority                     # sort by priority ascending
sort:priority:desc                # sort by priority descending
sort:id:desc                      # sort by entity ID descending
sort:modified:desc                # sort by modification time (newest first)
sort:priority:desc sort:title     # multi-sort: priority desc, then title asc
```

When no `sort:` clause is present:

1. If all results are the same entity type and that type has `default_sort`, it is used
2. Otherwise, results are sorted by ID ascending

## Custom Validation Rules

Define validation rules to enforce business constraints on your entities.
Validation rules use the same filter syntax as `--where` filters.

### Validation Rule Structure

```yaml
validations:
  - name: rule-identifier # Unique name for the rule
    description: "Human-readable description shown in output"
    entity_type: requirement # Optional: limit to specific type
    faces: [published] # Optional: limit to specific content states
    when: # Optional: IF these conditions match...
      - "status=accepted"
    then: # THEN these must be true
      - "priority!="
    when_condition: "..." # Optional: expression, ANDed with `when`
    then_condition: "..." # Optional: expression, ANDed with `then`
    severity: error # Optional: "error" or "warning" (default)
```

### How Validation Rules Work

1. **Select entities**: If `entity_type` is specified, only those entities are checked
2. **Select content states**: Every state is checked by default. If `faces` is
   specified, only those states are (see below)
3. **Apply when filter**: If `when` is specified, only entities satisfying ALL when conditions are subject to the rule
4. **Check then conditions**: Matched entities must satisfy ALL `then` conditions
5. **Report violations**: Entities that match `when` but don't satisfy `then` are reported

### `faces:` — scoping a rule to content states

If a type declares `faces:`, each state is a separate row and **every one is
validated**. A rule with no `faces:` key therefore applies to all of them.

That default is deliberate: a rule is a correctness claim, and the safe
direction for a claim is to check more rather than less. A rule that silently
skipped a state would let `rela validate` report a clean run over data it never
looked at — worse than no check, because it is a claim.

Set `faces:` when the rule is genuinely about particular states:

```yaml
validations:
  - name: published-policy-needs-owner
    entity_type: policy
    faces: [published]
    then: ["owner!="]
```

Without the scope, that rule reports every unfinished draft as a violation, and
a validator that cries wolf gets ignored.

Name faces as you declared them. A face no type declares is a **load error**: the rule would match
nothing and pass forever while appearing to guard something.

Violations report which state they are about, so an entity with a valid bare
face and an invalid translation is unambiguous in the output.

### `when:` vs `when_condition:` — two dialects, on purpose

`when:`/`then:` take **filter clauses** (`status=accepted`, `priority!=`) — one
property, one operator, one value, ANDed together. `when_condition:` and
`then_condition:` take a **predicate expression**, the same language as the
CLI's `--filter` flag and ACL `when:` rules. Expressions add boolean
composition, parentheses, and host functions — including date arithmetic:

```yaml
validations:
  - name: stale-open-tasks
    description: "an open task due within a week must have an owner"
    entity_type: taak
    when:
      - "status=open"
    when_condition: "days_between(entity.due, today()) <= 7"
    then_condition: "entity.owner ~= nil and entity.owner ~= ''"
    severity: error
```

Both keys are optional and independent — mix filter clauses and expressions
freely; everything present is ANDed.

Why two keys rather than one that accepts either? Because the syntaxes overlap
**without erroring**. The filter parser reads
`days_between(entity.due, today()) <= 7` as a filter on a property literally
named `days_between(entity.due, today())`. No such property exists, so the rule
silently selects nothing — no error at load, no warning at runtime. Choosing
the key states which dialect you meant, so a mistake surfaces immediately.

Note that a date literal in an expression is a **string**:
`entity.due <= '2026-08-25'`. It is parsed against the property's declared
format when the expression compiles.

### Example Validation Rules

```yaml
validations:
  # Accepted requirements must have a priority
  - name: accepted-needs-priority
    description: "Accepted requirements must have a priority assigned"
    entity_type: requirement
    when:
      - "status=accepted"
    then:
      - "priority!="
    severity: error

  # All decisions should have a rationale (no 'when' = applies to all)
  - name: decisions-need-rationale
    description: "Decisions should have a rationale documented"
    entity_type: decision
    then:
      - "rationale!="
    severity: warning

  # High priority requirements must have a description
  - name: high-priority-needs-description
    description: "High priority requirements need detailed descriptions"
    entity_type: requirement
    when:
      - "priority=high"
    then:
      - "description!="
    severity: warning

  # ADRs should follow naming convention
  - name: adr-naming-convention
    description: "ADRs should follow the ADR-NNN naming pattern"
    entity_type: decision
    then:
      - "title=~^ADR-\\d+:"
    severity: warning
```

### Filter Operators in Validations

Validation rules support all the same operators as `--where` filters:

| Operator | Example                | Description                                       |
| -------- | ---------------------- | ------------------------------------------------- |
| `=`      | `status=accepted`      | Equals (supports glob patterns with `*`)          |
| `!=`     | `owner!=`              | Not equals (use empty value to check "has value") |
| `<`      | `risk_score<5`         | Less than (dates, integers)                       |
| `<=`     | `deadline<=2025-12-31` | Less than or equal                                |
| `>`      | `priority>low`         | Greater than                                      |
| `>=`     | `created>=2025-01-01`  | Greater than or equal                             |
| `=~`     | `title=~^ADR-\\d+`     | Regex match (strings)                             |

### Content Validation

In addition to property-based conditions, validation rules can check markdown content structure
using the `content` field. This validates the presence of required headers in entity markdown files.

```yaml
validations:
  - name: adr-structure
    description: "ADRs must have Context and Decision headers"
    entity_type: decision
    when:
      - "status=accepted"
    content:
      required-headers:
        - "## Context"
        - "## Decision"
```

#### Required Headers

The `required-headers` field accepts a list of header checks. Each check can be:

1. **Exact match** (string): The header must match exactly, including the `#` prefix

   ```yaml
   required-headers:
     - "## Context" # Requires exactly "## Context"
     - "### Details" # Requires exactly "### Details"
   ```

2. **Pattern match** (regex): Use the `pattern:` prefix for flexible matching

   ```yaml
   required-headers:
     - pattern: "## (Alternative|Alternatives)" # Matches either spelling
     - pattern: "## .+ Analysis" # Matches any "## X Analysis" header
   ```

#### Content Validation Example

```yaml
validations:
  # ADRs must follow the standard structure
  - name: adr-required-sections
    description: "Accepted ADRs must have Context, Decision, and Consequences sections"
    entity_type: decision
    when:
      - "status=accepted"
    content:
      required-headers:
        - "## Context"
        - "## Decision"
        - "## Consequences"
    severity: error

  # User stories should have acceptance criteria
  - name: story-acceptance-criteria
    description: "User stories should have acceptance criteria"
    entity_type: requirement
    when:
      - "title=~^As a"
    content:
      required-headers:
        - pattern: "## (Acceptance Criteria|AC)"
    severity: warning
```

#### How Content Validation Works

1. Headers are extracted from the entity's markdown content using a proper parser
2. Headers inside code blocks (fenced or indented) are ignored
3. Each required header is checked against the extracted headers
4. If any required header is missing, the entity violates the rule

### Relation Cardinality Validation

The `relations` field asserts how many **outgoing** relations of a given type an
entity has, optionally restricted to targets whose own properties match a filter.
This expresses workflow gates like "a done ticket must have a completed review
checklist" or "a done ticket must have no open critical review-responses" — a
constraint that spans two entities, which property/content checks cannot.

```yaml
validations:
  # A done ticket must link to at least one completed review checklist.
  - name: done-ticket-needs-review
    description: "Done tickets must have a completed review checklist"
    entity_type: ticket
    when:
      - "status=done"
    relations:
      has-review: # keyed by relation type
        where: # target must match ALL of these (same syntax as when/then)
          - "status=done"
        min: 1 # at least one matching relation
    severity: error

  # A done ticket must have no open critical review responses.
  - name: done-ticket-no-open-critical
    description: "Done tickets cannot have open critical review responses"
    entity_type: ticket
    when:
      - "status=done"
    relations:
      has-review-response:
        where:
          - "status=open"
          - "severity=critical"
        max: 0 # no matching relation allowed
    severity: error
```

Each entry under `relations` is keyed by a relation type and supports:

| Field         | Meaning                                                              |
| ------------- | -------------------------------------------------------------------- |
| `where`       | Filters on the **far** entity's properties (ANDed). Omit to count every one. Uses the same operators as `when`/`then`. |
| `direction`   | `outgoing` (default) counts edges the entity is the SOURCE of; `incoming` counts edges it is the TARGET of. |
| `target_type` | Restricts the count to far entities of this type. Omit to count every type the relation reaches. |
| `min`         | Require at least this many matching relations.                      |
| `max`         | Require at most this many matching relations.                       |

At least one of `min`/`max` must be set. The check counts relations of the keyed
type whose far entity satisfies every `where` condition, then reports a
violation when the count falls outside `[min, max]`. It runs only for entities
that match `when`.

Counting is per **edge**, not per distinct far entity: two edges to the same
entity count two.

### Direction and target type

A constraint counts outgoing edges unless you say otherwise, so every rule
written before these keys existed keeps its meaning.

`direction: incoming` is for a claim about edges that point AT the entity. If
tasks link to the procedure they cover, then from the procedure there is
nothing outgoing to count:

```yaml
- name: procedure-heeft-open-taak
  description: "An adopted procedure needs an open task"
  entity_type: procedure
  faces: [vastgesteld]
  relations:
    gaat_over:
      direction: incoming
      target_type: taak
      where:
        - "status!=gereed"
      min: 1
  severity: error
```

`target_type` matters when a relation reaches more than one type. Here
`gaat_over` accepts both a `taak` and a `terugkerend` (a recurring schedule),
so the count alone cannot say WHICH was found — "has a task" and "has a
schedule" are different claims over one relation. A `where` clause cannot
express it either: those filter an entity's *properties*, and a type is not a
property.

Setting `target_type` also lets rela check `where` against that type when the
schema loads, rather than leaving a mistyped property to misbehave at check
time.

Both keys are validated at load. These are all load errors, because each one
would otherwise produce a rule that counts nothing and therefore passes
forever:

- a `direction` that is not `outgoing` or `incoming`
- `direction` on a `symmetric: true` relation — a symmetric edge is stored
  once with no preferred direction, so honouring direction would give the two
  endpoints different counts for the same relationship
- a `target_type` that is not a declared entity type
- a `target_type` the relation cannot reach on the side `direction` selects
- a `where` property that **none** of the reachable far types declares (a
  property only some of them declare is fine — that is the normal case for a
  relation spanning several types)

> **Incoming counts are entity-level.** An edge records the face of its tail
> but has no head face, so an incoming edge cannot be attributed to one face of
> the entity it arrives at. Since each face is validated as its own row, an
> incoming constraint on a faced type reports one violation per face for a
> single underlying defect. Narrow the rule with `faces:` to avoid the noise.

### Load-time validation of a `relations:` block

> Unknown keys inside a validation rule are rejected at load time, so a
> misspelled or mis-nested block (e.g. `relationz:`) fails loudly rather than
> being silently ignored. The same applies inside the block: an undeclared
> relation type, a constraint with neither `min` nor `max`, and bounds nothing
> can satisfy (`min: 5, max: 2`) are all load errors. A constraint that cannot
> be evaluated at check time is reported too — it never counts as satisfied.

`where` is matched against the **target** entity with the same type-aware
semantics as `when`/`then`, which is worth knowing when porting hand-written
comparisons:

- `*` and `?` are wildcards, not literal characters.
- On a list-valued property, `=` means "contains this element".
- Values are coerced to the property's declared type, so `count=007` matches a
  stored `7`, and a date matches its timestamp form.

Relations and targets are read through the same visibility rules as everything
else, so a constraint counts what the acting identity can **see**. A gate is
not a global invariant: an edge is skipped when either endpoint is invisible,
so two users can legitimately get different verdicts on the same graph. The CLI
and CI read unrestricted, so the verdict that enforces the workflow sees the
whole graph.

### Lua Validation

For complex validation logic that goes beyond property filters and content checks, you can use
Lua scripts. This enables cross-entity lookups, custom calculations, and sophisticated business rules.

#### Inline Lua Code

Use the `lua` field for short validation logic:

```yaml
validations:
  - name: status-required
    description: "Status must not be empty"
    entity_type: ticket
    lua: |
      local status = entity.properties.status
      if status == nil or status == "" then
        return { message = "Status is required" }
      end
      return nil
    severity: error
```

#### External Lua Scripts

For longer scripts, use `lua_file` to reference a script in the `validations/` directory.
Use `lua_args` to pass parameters to the script (available as `rela.args`):

```yaml
validations:
  - name: component-coverage-high
    description: "Critical components need 90% coverage"
    entity_type: component
    when:
      - "criticality=high"
    lua_file: check-coverage.lua
    lua_args: ["90"]
    severity: error
  - name: component-coverage-standard
    description: "Components need 80% coverage"
    entity_type: component
    lua_file: check-coverage.lua
    lua_args: ["80"]
    severity: warning
```

```lua
-- validations/check-coverage.lua
-- Entity is available as a global variable
-- Arguments are available via rela.args

local min_coverage = tonumber(rela.args[1]) or 80

local coverage = entity.properties.test_coverage
if coverage == nil then
  return nil  -- No coverage data, pass
end

-- Parse percentage (e.g., "85%" -> 85)
local value = tonumber(string.match(coverage, "(%d+)"))
if value == nil then
  return nil  -- Can't parse, pass
end

if value < min_coverage then
  return { message = "Coverage is " .. value .. "%, minimum is " .. min_coverage .. "%" }
end
return nil
```

#### Entity Context

The `entity` global variable provides access to the entity being validated:

| Field               | Type   | Description                       |
| ------------------- | ------ | --------------------------------- |
| `entity.id`         | string | Entity ID (e.g., "REQ-001")       |
| `entity.type`       | string | Entity type (e.g., "requirement") |
| `entity.properties` | table  | Property key-value pairs          |
| `entity.content`    | string | Markdown body content             |

Access properties directly via `entity.properties.status` or `entity.properties["my-field"]`.

#### Cross-Entity Lookups

Lua validation scripts have read-only access to the workspace for cross-entity validation:

```lua
-- Get another entity by ID
local related = rela.get_entity("REQ-001")
if related and related.properties.status ~= "approved" then
  return { message = "Related requirement must be approved" }
end

-- List entities by type
local components = rela.list_entities("component")
for _, comp in ipairs(components) do
  -- Check each component...
end

-- Trace dependencies
local deps = rela.trace_from(entity.id, 2)
for _, step in ipairs(deps.path) do
  -- Check dependency chain...
end
return nil
```

#### Return Value Semantics

Lua scripts return `nil` to pass validation, or a table (or array of tables) to report violations:

```lua
-- Pass: return nil or nothing
return nil

-- Single violation with custom message
return { message = "Status is required" }

-- Single violation with custom severity (overrides rule default)
return { message = "Consider adding a description", severity = "warning" }

-- Multiple violations from one rule
return {
  { message = "Missing owner", severity = "warning" },
  { message = "Priority not set", severity = "error" }
}
```

Each violation table has:

| Field      | Type   | Description                                                      |
| ---------- | ------ | ---------------------------------------------------------------- |
| `message`  | string | Custom error message (required)                                  |
| `severity` | string | `"error"` or `"warning"` (optional, defaults to rule's severity) |

##### `message` is per-entity; `description:` is per-rule

The two are reported side by side, never one instead of the other. The
rule's `description:` names the rule and is the same for every entity
that violates it; `message` says what is wrong with *this* entity and
what to do about it. That is the point of writing a rule in Lua: when a
defect has several shapes, the remedy differs per shape.

```lua
-- rule description: "Elke procedure moet een terugkerende taak hebben"
local task = rela.trace_from(entity.id, 1)
if #task == 0 then
  return { message = "geen terugkerende taak gekoppeld" }
end
if all_exhausted(task) then
  return { message = "alleen uitgeputte taken; plan een nieuwe" }
end
return nil
```

`rela analyze validations` groups violations under the rule description
and appends each entity's message:

```text
⚠ Elke procedure moet een terugkerende taak hebben (2):
  PROCEDURE-91XS: Toegangsbeheer — geen terugkerende taak gekoppeld
  PROCEDURE-MCBL: Incidentbeheer — alleen uitgeputte taken; plan een nieuwe
```

In `-o json` the message is its own field alongside the description:

```json
{
  "RuleName": "procedure-needs-task",
  "Description": "Elke procedure moet een terugkerende taak hebben",
  "Message": "geen terugkerende taak gekoppeld",
  "EntityID": "PROCEDURE-91XS"
}
```

A rule that returns no message (every non-Lua rule) reports the
description alone.

#### Security and Sandboxing

Lua validation runs in a sandboxed environment:

- **Read-only workspace**: Scripts cannot create, update, or delete entities
- **Execution timeout**: Scripts are terminated after 5 seconds to prevent infinite loops
- **Path restrictions**: `lua_file` scripts must be in the `validations/` directory with `.lua` extension
- **No file I/O**: Scripts cannot read or write files directly

Errors in Lua scripts (syntax errors, runtime errors, timeouts) are logged and the validation
rule is skipped ("fail open") to avoid blocking the entire validation run.

### Running Validations

```bash
# Run only custom validations
rela analyze validations

# Run all analyses including validations
rela analyze all
```

### Validation Output

```text
$ rela analyze validations
✗ Accepted requirements must have a priority assigned (2):
  REQ-003: User authentication
  REQ-007: Data encryption
⚠ Decisions should have a rationale documented (1):
  DEC-002: Use PostgreSQL
Found 2 errors, 1 warnings across 2 rules
```

### Severity Levels

- **error**: Critical violations that should be fixed. Displayed with ✗
- **warning**: Recommendations that may need attention. Displayed with ⚠

### Tips

1. **Start with warnings**: Begin with `severity: warning` and promote to `error` once your data is cleaned up
2. **Use specific entity types**: Narrow rules to specific types when possible for clearer error messages
3. **Combine with cardinality**: Use cardinality constraints for relation rules, validations for property rules
4. **Check for empty values**: Use `property!=` to require that a property has any value

## Automations

Automations are trigger-action rules that execute when entities change. They enable
workflow automation, automatic property updates, and entity creation based on state
transitions.

### Automation Structure

```yaml
automations:
  - name: automation-name
    description: "Human-readable description"
    on:
      # Trigger conditions
    do:
      # Actions to perform
```

### Triggers

Automations fire based on entity changes:

| Trigger Field      | Description                               | Example                |
| ------------------ | ----------------------------------------- | ---------------------- |
| `entity`           | Entity types to watch (string or list)    | `[ticket, bug]`        |
| `property`         | Property name to monitor                  | `status`               |
| `becomes`          | Value the property changed to             | `in-progress`          |
| `from`             | Value the property changed from           | `backlog`              |
| `created`          | Fires when entity is created              | `true`                 |
| `relation_created` | Fires when this relation type is created  | `implements`           |
| `relation_removed` | Fires when this relation type is removed  | `implements`           |
| `faces`            | Content states to watch (default: all)    | `[published]`          |
| `when`             | Property conditions that must match (AND) | `["kind=enhancement"]` |
| `condition`        | Predicate expression that must hold (AND) | `"days_between(entity.due, today()) <= 7"` |

### Scoping an automation to content states

If a type declares `faces:`, each state is a separate row and an automation
fires on **every one of them** by default. That is the existing behaviour and
the honest reading of an unscoped rule — "when a ticket's status becomes done"
is a statement about tickets, and a translated ticket is still a ticket.

What it costs is multiplied side effects. An automation that creates a checklist
entity will create one per state, which is rarely what an operator means. Use
`faces:` when the automation is about particular states:

```yaml
automations:
  - name: announce-publication
    on:
      entity: policy
      property: status
      becomes: approved
      faces: [published]
    do:
      - set: announced_at
        value: "{{today}}"
```

Name faces as you declared them. A face the triggering type does not declare
is a **load error**:
the trigger would never fire, silently disabling the automation it was meant to
narrow.

An action writes back to the state that triggered it, so an automation firing on
a translation updates that translation and not its sibling.

### Conditional Triggers

Use `when` to add property conditions that must be satisfied for the automation to fire.
This uses the same filter syntax as validation rules.

```yaml
automations:
  - name: mark-enhancement-for-docs
    description: Mark enhancement tickets for documentation review
    on:
      entity: ticket
      property: status
      becomes: review
      when:
        - "kind=enhancement"
    do:
      - set: needs_docs
        value: "true"
```

Multiple conditions use AND logic (all must match):

```yaml
on:
  entity: ticket
  property: status
  becomes: review
  when:
    - "kind=enhancement"
    - "priority=high"
```

Supported operators: `=`, `!=`, `<`, `<=`, `>`, `>=`, `=~` (regex).

**Note:** Conditions are evaluated against the entity's NEW state (after the change).
For property change triggers, use `from` to filter on the old value of the changed property.

### Actions

Actions execute when triggers match:

**Set Property**:

```yaml
do:
  - set: started_at
    value: "{{today}}"
```

**Create Relation**:

```yaml
do:
  - create_relation:
      relation: implements
      to: "{{entity.parent}}"
```

**Create Entity** (with optional relation):

```yaml
do:
  - create_entity:
      type: checklist
      properties:
        title: "Planning: {{new.title}}"
        status: in-progress
      relation: has-planning
      if_exists: skip
```

### Template Variables

Automation values support template substitution:

| Variable         | Description                            |
| ---------------- | -------------------------------------- |
| `{{today}}`      | Current date in ISO 8601 format        |
| `{{new.title}}`  | Property value from the changed entity |
| `{{new.status}}` | Any property from the changed entity   |
| `{{entity.id}}`  | Entity ID                              |
| `{{user.name}}`  | Current user's name                    |

### Example: Workflow Checklists

Automatically create workflow checklists when tickets transition through stages:

```yaml
automations:
  # Create planning checklist when ticket enters planning
  - name: ticket-planning-checklist
    description: Create planning checklist when ticket enters planning
    on:
      entity: [ticket]
      property: status
      becomes: planning
    do:
      - create_entity:
          type: planning-checklist
          properties:
            title: "Planning: {{new.title}}"
            status: in-progress
          relation: has-planning
          if_exists: skip

  # Create implementation checklist when ticket enters in-progress
  - name: ticket-implementation-checklist
    description: Create implementation checklist when ticket starts
    on:
      entity: [ticket, bug]
      property: status
      becomes: in-progress
    do:
      - create_entity:
          type: implementation-checklist
          properties:
            title: "Implementation: {{new.title}}"
            status: in-progress
          relation: has-implementation
          if_exists: skip

  # Create review checklist when ticket enters review
  - name: ticket-review-checklist
    description: Create review checklist when ticket enters review
    on:
      entity: [ticket, bug]
      property: status
      becomes: review
    do:
      - create_entity:
          type: review-checklist
          properties:
            title: "Review: {{new.title}}"
            status: in-progress
          relation: has-review
          if_exists: skip
```

### Example: Status Tracking

Track when work started and by whom:

```yaml
automations:
  - name: track-started
    description: Record when work started
    on:
      entity: [ticket, bug]
      property: status
      becomes: in-progress
    do:
      - set: started_at
        value: "{{today}}"
      - set: started_by
        value: "{{user.name}}"
```

### Expression Conditions (`condition:`)

`when:` takes filter clauses. `condition:` takes a **predicate expression** —
the same language as the CLI's `--filter` flag and ACL rules — which adds
boolean composition and host functions, including date arithmetic:

```yaml
automations:
  - name: flag-due-soon
    description: Flag tasks coming due within a week
    on:
      entity: taak
      created: true
      when:
        - "status=todo" # filter clause
      condition: "days_between(entity.due, today()) <= 7" # expression
    do:
      - set: flag
        value: "due-soon"
```

Both keys are optional and AND together. An expression can choose a value with
`c and x or y`; see
[choosing a value](#choosing-a-value-with-andor). Available functions include
`today()`, `days_between(a, b)`, `date_add(d, n, unit)`,
`rrule_next(rule, after)`, plus the string matchers `match`, `regex`,
`fuzzy`, and `contains`.

`condition:` requires `entity:` naming the type(s) it applies to — the
expression is compiled against that type's properties.

**A broken `condition:` fails at load**, naming the automation. That is
deliberate: a dropped constraint would make the automation fire on *more*
entities than you wrote, which is invisible in production. The same now
applies to an unparseable `when:` clause, which earlier versions skipped
silently.

> **Upgrade note.** Making an unparseable `when:` clause fatal is a
> behaviour change on the load path. It can only affect a clause that was
> *already* broken — one that parsed to nothing and was silently dropped,
> so the automation had been firing more widely than intended. The filter
> parser rejects only three shapes: an empty string, a clause with no
> operator (`status`), and one with no property (`=todo`). A plausible
> way to hit this is a YAML-confusion typo like `- "status: todo"` inside
> a `when:` list. If your project starts failing to load after upgrading,
> the error names the automation and the offending clause — the fix is to
> write the clause you meant, e.g. `status=todo`.

Why not one key that accepts either dialect? The syntaxes overlap without
erroring — the filter parser reads
`days_between(entity.due, today()) <= 7` as a filter on a property literally
named `days_between(entity.due, today())`, which matches nothing and reports
nothing. Choosing the key states which you meant.

Date literals inside an expression are **strings**:
`entity.due <= '2026-08-25'`.

#### Guard optional date properties

A date function applied to a property the entity does not carry is an
**eval error**, not `false`. The automation does not fire and a warning is
attached to the result:

```yaml
condition: "days_between(entity.due, today()) <= 7" # skips tasks with no due date
```

An entity with no `due` is skipped — which is often the opposite of what you
want, since a task with no due date may be exactly the one to flag. Guard the
property when it is optional:

```yaml
condition: "entity.due ~= nil and days_between(entity.due, today()) <= 7"
```

Or invert the test to catch the missing case:

```yaml
condition: "entity.due == nil or days_between(entity.due, today()) <= 7"
```

The same applies to validation rules, where the two keys fail in opposite
directions from this identical cause: a `when_condition:` that errors means
"entity not selected" (the rule skips it), while a `then_condition:` that
errors means "assertion not shown to hold" (a violation). Both fail toward
not-silently-passing, but one ignores the entity and the other flags it — so
guard optional properties rather than relying on either.

### Automation Options

| Field       | Description                                         |
| ----------- | --------------------------------------------------- |
| `if_exists` | Behavior when `create_entity` target exists: `skip` |

### Best Practices

1. **Use descriptive names**: Name automations after what they accomplish
2. **Keep actions focused**: Each automation should do one logical thing
3. **Use `if_exists: skip`**: Prevent duplicate entities when re-entering states
4. **Document with description**: Explain the workflow the automation supports
