<!-- This file is auto-generated from docs-project/entities/. Do not edit directly. -->

# CLI Reference

Complete reference for all rela commands.

## Global Options

These options work with any command:

| Option          | Description                                                          |
| --------------- | -------------------------------------------------------------------- |
| `-p, --project` | Project directory (default: auto-detect from cwd, or `RELA_PROJECT`) |
| `-o, --output`  | Output format: `table` (default) or `json`                           |
| `-v, --verbose` | Enable verbose output                                                |
| `-q, --quiet`   | Suppress non-essential output                                        |
| `-h, --help`    | Show help for any command                                            |

## Environment Variables

| Variable       | Description                                                      |
| -------------- | ---------------------------------------------------------------- |
| `RELA_PROJECT` | Default project directory when `--project` flag is not specified |

## Project Configuration

Project-level settings are stored in `.rela/config.yaml`. This file is optional; defaults are used
when it doesn't exist.

```yaml
formatting:
  line_width: 80 # Maximum line width for paragraph wrapping (default: 80)
```

| Setting                 | Description                               | Default |
| ----------------------- | ----------------------------------------- | ------- |
| `formatting.line_width` | Maximum line width for paragraph wrapping | 80      |

## Commands

### rela init

Initialize a new rela project.

```bash
rela init
rela init -p /path/to/project
```

Creates:

- `schema.yaml` - Default configuration
- `entities/` - Entity storage directory
- `relations/` - Relation storage directory
- `.rela/` - Cache directory (added to `.gitignore` if present)

---

### rela apps new

Scaffold a new custom data-entry app (see the data-entry guide's "Custom apps"
section).

```bash
rela apps new my-dashboard
```

Creates `apps/<id>/index.html` — a working starter that links the bridge SDK
(`_rela.js`) and the optional theme (`_rela.css`), reads data via
`rela.list(...)`, and renders a result. Edit it, then open `/app/<id>` in the
data-entry server. The `<id>` must match `^[a-z0-9_-]{1,64}$` and becomes the
folder name and route; it errors if the app already exists.

---

### rela create

Create a new entity.

```bash
rela create <type> [flags]
```

**Arguments:**

- `type` - Entity type (e.g., `requirement`, `decision`, `solution`, `component`)

**Flags:**

| Flag              | Description                                                                    |
| ----------------- | ------------------------------------------------------------------------------ |
| `-s, --status`    | Entity status (default: `draft`)                                               |
| `-p, --priority`  | Entity priority                                                                |
| `--id`            | Custom entity ID (required for string ID types, auto-generated for sequential) |
| `-P, --property`  | Set a property (format: key=value, can be repeated)                            |
| `-b, --body`      | Markdown body content for the entity                                           |
| `-B, --body-file` | Read body content from file (use `-` for stdin)                                |

Set the title (and any other property) with `-P`, e.g. `-P title="..."`.

> **Removed:** `create` no longer has a `-t, --title` flag. It used to write
> into the entity type's _display_ property, which is not always `title` (a
> project may name it `naam`/`label` or set `display_property`). If you relied
> on `--title` writing into a non-`title` display property, set that property
> directly: `-P <yourprop>="..."`. (`rela update -t` is unaffected — it always
> wrote the literal `title` property.)

**Templates:**

If a template exists at `templates/entities/<type>.md`, its frontmatter values are used as defaults
and its content is applied to the new entity. CLI flags override template defaults.
See [rela template](#rela-template) for creating templates.

**ID Types:**

Entity types can have either sequential or string IDs (configured in `schema.yaml`):

- **Sequential** (default): IDs are auto-generated (`REQ-001`, `REQ-002`, etc.)
- **String**: IDs must be provided with `--id` flag

```bash
# Sequential ID type (auto-generated)
rela create requirement -P title="System must scale to 1000 users"
# Creates REQ-001

# String ID type (requires --id)
rela create component --id auth-service -P title="Authentication Service"
# Creates auth-service
```

**Examples:**

```bash
# Create with auto-generated ID (sequential types)
rela create requirement -P title="System must scale to 1000 users"

# Use type alias
rela create req -P title="Short form works too"

# With status and priority
rela create requirement -P title="Security audit" --status proposed --priority high

# With custom ID (works for both sequential and string types)
rela create requirement -P title="Custom ID" --id REQ-CUSTOM-001

# String ID type (--id is required)
rela create component --id user-service -P title="User Service"

# With custom properties
rela create control -P title="Access Control" -P "iso27001=A.5.15" -P "owner=Security Team"

# With body content
rela create requirement -P title="Auth feature" --body "## Description\n\nUser authentication."
```

---

### rela list

List entities with optional filtering.

```bash
rela list [type] [flags]
```

**Arguments:**

- `type` - Entity type to filter by (optional, shows all if omitted)

**Flags:**

| Flag       | Description                                                    |
| ---------- | ------------------------------------------------------------- |
| `--filter` | Filter by a predicate expression (recommended)                |
| `--where`  | Filter by property, legacy syntax (repeatable, AND). Deprecated — prefer `--filter`. |
| `--sort`   | Sort by property (or `id`, `modified`)                        |
| `--desc`   | Sort descending                                               |

**`--filter` (predicate expressions):**

`--filter` takes a boolean predicate over the entity's fields, so it can
express conditions the legacy `--where` cannot — notably `or` and grouped
logic:

```bash
rela list ticket --filter "entity.status == 'ready' and entity.priority > 3"
rela list ticket --filter "entity.status == 'done' or entity.priority > 8"
```

Comparisons are typed by the property's declared metamodel type (integers
compare numerically, dates instant-granularly). Host functions are
available for pattern matching: `match(field, glob)`, `regex(field, re)`,
`fuzzy(field, target)`, and `contains(list_field, value)`.

Date arithmetic is available too, so a condition can be relative to the
current day rather than to a hard-coded date:

| Function | Returns | Meaning |
| -------- | ------- | ------- |
| `today()` | date | The current day, at UTC midnight |
| `days_between(a, b)` | number | Whole days from `b` to `a`; positive when `a` is later |
| `date_add(d, n, unit)` | date | `d` shifted by `n` (may be negative); `unit` is `day`, `week`, `month` or `year` |
| `rrule_next(rule, after)` | date | The first occurrence of an RRULE strictly after `after` |

```bash
# due within a week (and anything already overdue)
rela list taak --filter "days_between(entity.due, today()) <= 7"

# strictly overdue
rela list taak --filter "days_between(entity.due, today()) < 0"

# a three-day grace period has lapsed
rela list taak --filter "date_add(entity.due, 3, 'day') < today()"
```

Every date value is normalized to UTC midnight, so results are whole days
and compare equal to bare `YYYY-MM-DD` literals regardless of any time
component or timezone on the stored value.

`date_add` accepts `day`, `week`, `month` and `year`. Month and year
arithmetic **clamps to the last valid day** of the target month rather than
spilling into the next one:

| Expression | Result |
| ---------- | ------ |
| `date_add('2026-01-31', 1, 'month')` | `2026-02-28` |
| `date_add('2028-01-31', 1, 'month')` | `2028-02-29` (leap year) |
| `date_add('2026-03-31', -1, 'month')` | `2026-02-28` |
| `date_add('2028-02-29', 1, 'year')` | `2029-02-28` |

Clamping is stated rather than inherited: Go's own `AddDate` would normalize
January 31 + 1 month to **March 3**, silently turning "the last of every
month" into "the 3rd of every other month".

Note that clamping is lossy — chaining month steps from a month-end date does
not recover the original day (`Jan 31 → Feb 28 → Mar 28`). For a true
"last day of every month" schedule use `rrule_next` with an RRULE, which
models that directly.

`rrule_next` errors when the rule is malformed _or_ when it has no
occurrence left (a `COUNT` reached, an `UNTIL` passed); the two messages
differ so a finished schedule is distinguishable from a typo.

`--filter` also accepts `related(...)`, which filters on the entities at the
other end of a relation. The CLI runs with operator trust, so every entity
counts. It makes one store query per traversal, whatever the number of rows.

```bash
# tickets with no owner
rela list ticket --filter "not related(entity, 'owned-by')"

# features with at least one open ticket
rela list feature --filter "related(entity, 'implementedBy', { status = 'open' })"
```

See [Where `related(...)` works](metamodel.md#where-related-works) for the
path and constraint syntax.

**`--where` (legacy, deprecated):**

`--where` is transpiled to a predicate internally; prefer `--filter`. It
supports these comparison operators:

| Operator | Description                     | Example                             |
| -------- | ------------------------------- | ----------------------------------- |
| `=`      | Exact match (supports `*` glob) | `--where "status=draft"`            |
| `!=`     | Not equal                       | `--where "status!=done"`            |
| `<`      | Less than (date/integer)        | `--where "due_date<2025-03-01"`     |
| `<=`     | Less than or equal              | `--where "priority<=2"`             |
| `>`      | Greater than                    | `--where "risk_score>5"`            |
| `>=`     | Greater than or equal           | `--where "valid_until>=2025-01-01"` |
| `=~`     | Regex match                     | `--where "title=~^Auth"`            |
| `~`      | Fuzzy match                     | `--where "title~authentcation"`     |

**Glob Patterns:**

Use `*` in equality matches for wildcard matching:

```bash
rela list control --where "iso27001=A.9.*"    # Matches A.9.1, A.9.2.1, etc.
rela list --where "title=User*"               # Titles starting with "User"
```

**Fuzzy Matching:**

The `~` operator performs fuzzy (typo-tolerant) matching:

```bash
rela list --where "title~authentcation"       # Finds "authentication" despite typo
```

**Examples:**

```bash
# List all entities
rela list

# List by type (plural or singular)
rela list requirements
rela list requirement
rela list req

# Filter by property value
rela list control --where "status=implemented"

# Multiple filters (AND logic)
rela list control --where "status=implemented" --where "applicability=applicable"

# Date comparison
rela list evidence --where "valid_until<2025-02-01"

# Integer comparison
rela list risk --where "risk_score>=5"

# Sort by property
rela list control --sort iso27001
rela list evidence --sort valid_until --desc

# JSON output
rela list -o json
```

---

### rela show

Display detailed information about an entity.

```bash
rela show <id>
```

**Arguments:**

- `id` - Entity ID to show

Shows the entity's properties plus all incoming and outgoing relations.

**Examples:**

```bash
rela show REQ-001
rela show DEC-042 -o json
```

---

### rela update

Update an entity's properties.

```bash
rela update <id> [flags]
```

**Arguments:**

- `id` - Entity ID to update

**Flags:**

| Flag                | Description                                             |
| ------------------- | ------------------------------------------------------- |
| `-t, --title`       | New title                                               |
| `-s, --status`      | New status                                              |
| `-p, --priority`    | New priority                                            |
| `-d, --description` | New description                                         |
| `-P, --property`    | Set a property (`key=value`, repeatable)                |
| `-U, --unset`       | Remove a property entirely (repeatable)                 |
| `-b, --body`        | Replace the markdown body                               |
| `-B, --body-file`   | Read the body from a file (`-` for stdin)               |
| `--clear-body`      | Remove the markdown body                                |
| `--strict`          | Exit 1 if soft validation warnings are surfaced         |

At least one flag is required.

**Updates are targeted.** Only the properties you name are changed;
everything else on the entity is left exactly as it was. There is no need
to re-supply fields you are not editing, and no risk of blanking them by
omission.

**`-P key=` and `-U key` are different.** `-P key=` _sets the property to
the empty string_ (the key remains, with an empty value). `-U key` _removes
the property altogether_. Use `-U` when you mean "this no longer applies";
use `-P key=` when you mean "this is known to be blank".

`-B/--body-file` is honored even when the file is empty — naming a source is
an explicit instruction. Use `--clear-body` when you mean to clear the body,
and note the two cannot be combined.

**Examples:**

```bash
# Update status — every other property is untouched
rela update REQ-001 --status accepted

# Update multiple fields
rela update DEC-042 --title "Revised title" --status proposed

# Set an arbitrary property
rela update REQ-001 -P owner=alice

# Set a property to the empty string (key kept)
rela update REQ-001 -P owner=

# Remove a property entirely (key gone)
rela update REQ-001 -U owner

# Replace the body, then clear it
rela update REQ-001 --body "New notes"
rela update REQ-001 --clear-body
```

---

### rela delete

Delete an entity.

```bash
rela delete <id> [flags]
```

**Arguments:**

- `id` - Entity ID to delete

**Flags:**

| Flag          | Description               |
| ------------- | ------------------------- |
| `-f, --force` | Skip confirmation prompt  |
| `--cascade`   | Also delete related links |

Without `--cascade`, deletion fails if the entity has relations.

**Examples:**

```bash
# Interactive confirmation
rela delete REQ-001

# Skip confirmation
rela delete REQ-001 --force

# Delete entity and all its relations
rela delete REQ-001 --cascade
```

---

### rela history

Show an entity's version history, or print a past version's snapshot for piping
to a diff tool. **PostgreSQL and SQLite builds** — content versioning is a
database-backend capability (filesystem projects use git for the same
purpose); on other builds this reports that the backend does not support
history.

```bash
rela history <id> [--version N]
```

**Arguments:**

- `id` - Entity ID (may name a live or an already-deleted entity)

**Flags:**

- `--version N` - Print the full snapshot for version ordinal N (as JSON)
  instead of the timeline, so it can be piped to an external diff tool

Each timeline row shows the version number, timestamp, operation
(create/update/rename/delete), and the principal (user + tool) that made the
change. Reading a deleted entity's history requires the `history:read`
permission (see the ACL guide).

**Examples:**

```bash
rela history TKT-42                       # the version timeline
rela history TKT-42 --version 3           # print version 3's snapshot

# Diff two versions with any external tool (Unix-style):
diff <(rela history TKT-42 --version 3) <(rela history TKT-42 --version 5)
```

---

### rela restore

Restore an entity's content and properties to a past version. **PostgreSQL
build only.** The restore is applied as a normal write — authorized, validated,
audited, and itself recorded as a new version (history is never rewritten). If
the entity was deleted, it is re-created.

```bash
rela restore <id> <version>
```

**Arguments:**

- `id` - Entity ID to restore
- `version` - The version ordinal to restore to (see `rela history <id>`)

Only entity content and properties are restored; the entity's relations
as-of that version are versioned separately (see `rela relation-history`).

**Examples:**

```bash
rela restore TKT-42 3
```

---

### rela relation-history

Show a relation's version history, or print a past version's snapshot for piping
to a diff tool. A relation is addressed by its three-part key. **PostgreSQL and
SQLite builds** (filesystem projects use git). Relations carry their own properties and
markdown body, versioned with the same time-machine model as entities.

```bash
rela relation-history <from> <type> <to> [--version N]
```

**Arguments:**

- `from` - Source entity ID (the relation's `from`)
- `type` - Relation type (e.g. `blocks`)
- `to` - Target entity ID (the relation's `to`)

**Flags:**

- `--version N` - Print the full snapshot for version ordinal N (as JSON) for
  piping to an external diff tool, instead of the timeline

Each row shows the version, timestamp, operation (create/update/rename/delete),
and principal. A rename row shows the pre-rename endpoints so a renamed edge's
history reads as one continuous timeline. Reading a relation's history requires
read access to **both** endpoints (a deleted relation requires `history:read`).

**Examples:**

```bash
rela relation-history TKT-42 blocks TKT-99

# Diff two versions with any external tool (Unix-style):
diff <(rela relation-history TKT-42 blocks TKT-99 --version 2) \
     <(rela relation-history TKT-42 blocks TKT-99 --version 4)
```

---

### rela relation-restore

Restore a relation's content and properties to a past version. **PostgreSQL and
SQLite builds.** Applied as a normal write (authorized, validated, audited, re-versioned).
If the relation was deleted it is re-created — which fails with a conflict if an
endpoint entity no longer exists.

```bash
rela relation-restore <from> <type> <to> <version>
```

**Arguments:**

- `from` / `type` / `to` - The relation's three-part key
- `version` - The version ordinal to restore to (see `rela relation-history`)

**Examples:**

```bash
rela relation-restore TKT-42 blocks TKT-99 2
```

---

### rela history-purge

Hard-delete an entity's version history for compliance (leaked secret, PII, GDPR
erasure). The deliberate, audited, **irreversible** exception to append-only
history. **PostgreSQL and SQLite builds.** Operator-only: the trust boundary is
shell access to the database (no ACL check), like `rela db migrate`.

```bash
rela history-purge <id> (--vseq N | --content-hash H | --all) --reason "..." [--commit] [--yes] [--force-live]
```

**Arguments / flags:**

- `id` — the entity whose history to purge
- `--vseq N` — purge the single version row with this vseq (from `rela history`)
- `--content-hash H` — purge every row in the lineage with this content hash
  (erase a value everywhere it was captured; verifiable afterward)
- `--all` — purge the entity's entire (fenced) history
- `--reason "..."` — **required**; recorded in the audit trail. Do NOT put the
  secret here (logged in cleartext)
- `--commit` — actually delete. **Without it the command is a dry-run** that only
  shows what would be purged
- `--yes` — skip the type-the-id confirmation (scripts); requires `--commit`
- `--force-live` — purge even though the live row still holds the content (writes
  a tombstone so the sweep will not re-capture it). Prefer redacting the live
  value first.

Purge **refuses** while the live row still holds the content (unless
`--force-live`) and **refuses** a rename row. It is one necessary step, not
cryptographic erasure — PITR/backup lifecycle is the operator's responsibility.

**Examples:**

```bash
rela history-purge TKT-42 --content-hash abc123 --reason "erase SSN per DPO-42"          # dry-run
rela history-purge TKT-42 --content-hash abc123 --reason "erase SSN per DPO-42" --commit  # do it
```

---

### rela relation-history-purge

The relation analog of `rela history-purge`, addressing a relation by its
three-part key. Same flags, guardrails, and irreversibility. **PostgreSQL and
SQLite builds.**

```bash
rela relation-history-purge <from> <type> <to> (--vseq N | --content-hash H | --all) --reason "..." [--commit] [--yes] [--force-live]
```

**Examples:**

```bash
rela relation-history-purge TKT-42 blocks TKT-99 --all --reason "erase per request" --commit
```

---

### rela attach

Attach file(s) to an entity.

```bash
rela attach <entity-id> <file>... [flags]
```

Each file is stored at `attachments/<entity-id>/<property>/<filename>`.
A file-type property holds one attachment by default; set `max` above 1 on
the property (see the metamodel reference) to allow several.

> **Note:** for a single-attachment property (`max: 1`), re-running `attach`
> **replaces** the existing file. For a multi-attachment property (`max > 1`),
> `attach` **appends** up to the cap, auto-suffixing a duplicate name
> (`report.pdf` → `report (1).pdf`) and erroring when full.

**Arguments:**

- `entity-id` - Target entity ID
- `file...` - One or more files to attach (supports glob patterns)

**Flags:**

| Flag             | Description                   |
| ---------------- | ----------------------------- |
| `-P, --property` | Property to attach file(s) to |

If `--property` is not specified, uses the first `file`-type property defined for the entity type.

**Examples:**

```bash
rela attach BUG-042 screenshot.png
rela attach BUG-042 screenshot.png --property screenshot
rela attach DEC-007 supporting-doc.pdf --property supporting-docs
```

---

### rela attachments

List all file attachments for an entity.

```bash
rela attachments <entity-id>
```

Shows the property name, path, and size for each attachment.

**Arguments:**

- `entity-id` - Entity ID to list attachments for

**Examples:**

```bash
rela attachments BUG-042
rela attachments DEC-007
```

---

### rela detach

Remove an attachment from an entity property.

```bash
rela detach <entity-id> <property> [--file <name>]
```

Deletes the underlying file from the attachment store and re-stamps the
property. When the property holds a single attachment, `--file` may be
omitted. When it holds several (a `file` property with `max > 1`), pass
`--file` to select which one — `rela attachments <entity-id>` lists the
names.

**Arguments:**

- `entity-id` - Entity ID
- `property` - Property name containing the attachment

**Flags:**

| Flag         | Description                                       |
| ------------ | ------------------------------------------------- |
| `-f, --file` | File name to detach (required when more than one) |

**Examples:**

```bash
rela detach BUG-042 screenshot
rela detach DEC-007 supporting-docs --file spec.pdf
```

---

### rela link

Create a relation between two entities.

```bash
rela link <from> <relation> <to>
```

**Arguments:**

- `from` - Source entity ID
- `relation` - Relation type name
- `to` - Target entity ID

Both entities must exist. The relation type is validated against the metamodel.

**Templates:**

If a template exists at `templates/relations/<type>.md`, its frontmatter values are used as defaults
for relation properties. See [rela template](#rela-template) for creating templates.

**Examples:**

```bash
rela link DEC-001 addresses REQ-001
rela link SOL-001 implements DEC-001
rela link COMP-001 realizes SOL-001
rela link COMP-001 dependsOn COMP-002
```

---

### rela unlink

Remove a relation between entities.

```bash
rela unlink <from> <relation> <to>
```

**Arguments:**

- `from` - Source entity ID
- `relation` - Relation type name
- `to` - Target entity ID

**Examples:**

```bash
rela unlink DEC-001 addresses REQ-001
```

---

### rela sync

Two-way sync between a local project (fsstore) and a remote rela-server backed
by PostgreSQL. `push` sends locally-changed records to the server; `pull` brings
remote changes into the local project. Conflicts are surfaced for manual
resolution — no automatic merge. See [Sync](sync.md) for the full design.

```bash
rela sync push [--remote <url>] [--token <token>] [--force <id>]
rela sync pull [--remote <url>] [--token <token>] [--force <id>]
```

**Flags:**

| Flag       | Description                                                      |
| ---------- | ---------------------------------------------------------------- |
| `--remote` | Remote rela-server base URL (proxy-fronted). Env: `RELA_REMOTE`. |
| `--token`  | Bearer token for the OAuth proxy. Prefer env `RELA_SYNC_TOKEN`.  |
| `--force`  | Resolve one record id: push = local wins, pull = remote wins.    |

The sync state (a per-record content-hash index and an opaque server cursor)
lives in `.rela/sync-state.json`. Dirty detection is local: a record whose
canonical hash differs from the index is pushed; the index advances only past
confirmed-applied records, so an interrupted run resumes on re-run.

**Conflicts.** A record changed on both ends halts with a clear report and is
NOT applied. Resolve it explicitly:

- `rela sync push --force <id>` — overwrite the remote with the local copy.
- `rela sync pull --force <id>` — overwrite the local copy with the remote.

**Authentication.** In production rela-server sits behind an OAuth proxy and has
no native auth — the CLI authenticates to the _proxy_ by presenting a JWT bearer
(`Authorization: Bearer $RELA_SYNC_TOKEN`). The token is read from the env/flag
and never logged. On loopback/dev with no proxy, sync works without a token. See
[Sync](sync.md) for the proxy configuration.

**Examples:**

```bash
export RELA_REMOTE=https://rela.example.com
export RELA_SYNC_TOKEN=$(my-idp-get-token)

rela sync push                 # send local changes
rela sync pull                 # bring in remote changes
rela sync push --force TKT-42  # resolve TKT-42: local wins
rela sync pull --force TKT-42  # resolve TKT-42: remote wins
```

> Note: sync requires the remote to run the PostgreSQL backend. Against a
> non-postgres server the manifest endpoint returns 501 and `pull` reports that
> the server does not support sync.

---

### rela twin

Manage twins: an entity's counterparts in external systems, kept in sync by an
agent that talks to the external system with its own tools; in this stage rela
does not call the external system itself. Requires a `pacts:` block in the metamodel. See
[Twins](twins.md) for the full design and a worked example.

```bash
rela twin link <entity-id> <system> <external-id> --url <url> [--remote-updated-at <RFC3339>]
rela twin unlink <system> <external-id>
rela twin show <entity-id>
rela twin list [--system <system>] [--state <state>]
rela twin pending [--system <system>]
rela twin pull <system> <external-id> --remote <file|-> --remote-updated-at <RFC3339> [--force]
rela twin pushed <system> <external-id> --version <version> [--remote-updated-at <RFC3339>]
rela twin gone <system> <external-id>
rela twin pact <entity-type> <system>
```

Every subcommand accepts `-o json`. Writes made by `rela twin` are audited
with `tool: twin` and the real user.

**Subcommands:**

| Subcommand | Description |
| ---------- | ----------- |
| `link`     | Record that an entity has a counterpart in `<system>`. The entity type needs a pact with that system. The twin starts `pending` with no base. Refused when the external id is already linked in that system, when the entity already has a twin there that is not gone, or when the entity is locked by git-crypt. |
| `unlink`   | Remove a twin. The entity is not touched. |
| `show`     | List the twins of one entity, with state, findings and owned fields. |
| `list`     | List twins, optionally filtered by system and state. |
| `pending`  | The agent's work list: twins not `in_sync`, twins whose entity changed in rela since the last sync, and gone twins until unlinked. Each item carries its reasons, the entity `version`, and a `push_set` of ours and shared fields to write to the external item. A shared field in conflict is in the `push_set` only once it was changed in rela after the conflict. |
| `pull`     | Reconcile what the agent read from the external item. Writes theirs fields and uncontested shared fields to the entity, records findings, and advances the twin. Refused on a gone twin. |
| `pushed`   | Confirm that the external item now holds the `push_set` values: every ours field, and every shared field not in conflict or changed in rela after its conflict. Advances the base; clears `foreign_edit` findings and the conflicts resolved in rela. Other findings stay: the twin becomes `conflict` while a conflict remains, `pending` while a `local_drift` or `rejected` finding remains, and `in_sync` otherwise. Refused on a gone twin. |
| `gone`     | Mark a twin gone because the external item no longer exists. The twin stays listed by `pending` until unlinked. |
| `pact`     | Print the pact between an entity type and a system, including its `instructions` (the agent's brief). |

**Flags:**

| Flag                  | Subcommand | Description |
| --------------------- | ---------- | ----------- |
| `--url`               | `link`     | Required. URL of the external item. |
| `--remote-updated-at` | `link`     | Optional. The external item's modification time (RFC 3339). |
| `--system`            | `list`, `pending` | Only twins in this system. |
| `--state`             | `list`     | Only twins in this state: `pending`, `in_sync`, `conflict` or `gone`. |
| `--remote`            | `pull`     | Required. JSON file with the translated external values, or `-` for stdin. |
| `--remote-updated-at` | `pull`     | Required. The external item's modification time as read (RFC 3339). A time older than the twin's stored one is refused as stale. |
| `--force`             | `pull`     | Accept a pull older than the twin's stored modification time. |
| `--version`           | `pushed`   | Required. The entity `version` from the `pending` item that was pushed. Refused when the entity has changed since. |
| `--remote-updated-at` | `pushed`   | Optional. The external item's modification time after the push (RFC 3339). Without it, the stored time is kept. |

**Remote input.** `pull` reads rela values, already translated by the agent:

```json
{"properties": {"status": "done", "due": null}, "body": "markdown"}
```

A property absent from `properties` is not mapped and left untouched; a
`null` means the external side cleared it. The body goes in the top-level
`body` key, never inside `properties`; likewise an absent `body` is not mapped
and `"body": null` clears it. Any other top-level key is refused.

**Pull outcomes.** A sync write refused by validation, a state-machine
transition or a unique property does not fail the command: the twin records a
`rejected` finding with the error message and stays `pending`. Read the
findings, not only the exit status.

**Errors.** The command exits non-zero with a clear message when the entity
type has no pact with the system, the external id is invalid or already
linked, the entity already has a twin in that system, a pull is stale, the
`--version` of `pushed` is out of date, the entity changed during a pull (run
it again), `pull` or `pushed` names a gone twin (unlink it first), or the twin
changed while the command ran, for example because its entity was renamed (run
it again). A live twin whose entity no longer exists is marked `gone`.

**`pending -o json`** items:

| Key             | Description |
| --------------- | ----------- |
| `system`, `external_id`, `url` | The twin |
| `entity`        | `{type, id}` of the twinned entity |
| `state`         | `pending`, `in_sync`, `conflict` or `gone` |
| `reasons`       | Why it is listed, e.g. `never synced`, `changed in rela`, `conflict: status` |
| `local_changed` | The entity changed in rela since the last sync |
| `version`       | The entity's current version; pass it to `pushed --version` |
| `push_set`      | `[{field, base, local}]`: ours and shared fields to write to the external item; a shared field in conflict only once it was changed in rela after the conflict |
| `findings`      | `[{field, kind, base, ours, theirs, propose, message}]`; kind is `conflict`, `foreign_edit`, `local_drift` or `rejected` |

`show`, `list`, `link`, `pull`, `pushed` and `gone` print twin objects with
`system`, `external_id`, `url`, `entity`, `state` and `findings` as above,
plus `has_base` (false until the first pull rela accepts or the first push; a
pull that rela rejects leaves it as it was), `version` (the entity
version recorded at the last link, pull or push; `pushed --version` wants the
one from `pending`), and `synced_at` and `remote_updated_at` (RFC 3339,
omitted when never set). `pull` adds `applied`, the fields it wrote to the
entity; `show` adds `owned_fields`, the fields each twin's system owns.
`unlink -o json` prints `{system, external_id, unlinked}`; `pact -o json`
prints `{entity_type, system, scope, theirs, shared, ours, propose,
instructions}`.

**Examples:**

```bash
rela twin pact scenario basecamp            # read the agent brief
rela twin link SC-015 basecamp 7654321098 \
  --url https://app.basecamp.com/5734045/buckets/35926565/todos/7654321098
translate-todo 7654321098 | rela twin pull basecamp 7654321098 \
  --remote - --remote-updated-at 2026-09-20T14:03:11Z
rela twin pending --system basecamp -o json
rela twin pushed basecamp 7654321311 --version 9b2e71c40d5a \
  --remote-updated-at 2026-09-21T09:12:40Z
rela twin gone basecamp 7654321100
rela twin unlink basecamp 7654321100
```

`translate-todo` stands for the agent's own code that reads the Basecamp todo
and prints the remote JSON.

---

### rela trace

Trace dependencies between entities.

#### rela trace from

Trace downstream dependencies (what depends on this entity).

```bash
rela trace from <id> [flags]
```

**Flags:**

| Flag      | Description                   |
| --------- | ----------------------------- |
| `--depth` | Maximum depth (0 = unlimited) |

**Examples:**

```bash
rela trace from REQ-001
rela trace from REQ-001 --depth 2
```

#### rela trace to

Trace upstream dependencies (what this entity depends on).

```bash
rela trace to <id> [flags]
```

**Flags:**

| Flag      | Description                   |
| --------- | ----------------------------- |
| `--depth` | Maximum depth (0 = unlimited) |

**Examples:**

```bash
rela trace to COMP-001
rela trace to COMP-001 --depth 3
```

#### rela trace path

Find the shortest path between two entities.

```bash
rela trace path <from> <to>
```

**Examples:**

```bash
rela trace path REQ-001 COMP-001
```

---

### rela graph

Export the entity graph to Graphviz DOT format.

```bash
rela graph [flags]
```

**Flags:**

| Flag           | Description                                             |
| -------------- | ------------------------------------------------------- |
| `-o, --output` | Output file (stdout if not specified)                   |
| `-f, --format` | Output format: `dot`, `png`, `svg`, `pdf`               |
| `--direction`  | Graph direction: `tb` (top-bottom) or `lr` (left-right) |
| `--types`      | Filter by entity types (comma-separated)                |

Rendering to PNG/SVG/PDF requires Graphviz (`dot` command).

**Examples:**

```bash
# Print DOT to stdout
rela graph

# Save DOT file
rela graph --file architecture.dot

# Render to PNG
rela graph --file architecture.png -f png

# Filter by types
rela graph --types requirement,decision

# Left-to-right layout
rela graph --direction lr
```

---

### rela export

Export entities to structured formats for external tool integration.

```bash
rela export [type] [flags]
```

**Arguments:**

- `type` - Entity type to export (required unless using `--all`)

**Flags:**

| Flag               | Description                                       |
| ------------------ | ------------------------------------------------- |
| `-f, --format`     | Output format: `json` (default), `csv`, or `yaml` |
| `--with-relations` | Include relation data in export                   |
| `--all`            | Export all entities and relations                 |

**Output Formats:**

- **JSON**: Array of objects with full property data. Best for programmatic processing with `jq`.
- **CSV**: Comma-separated values with header row. Best for spreadsheet import or `mlr` (Miller).
- **YAML**: YAML format. Best for human readability and configuration.

**Examples:**

```bash
# Export all controls as JSON
rela export control --format json

# Export controls as CSV for spreadsheet import
rela export control --format csv

# Export controls with their relations included
rela export control --with-relations

# Export all entities and relations
rela export --all --format json

# Use with jq for custom filtering
rela export control --format json | jq '.[] | select(.properties.status == "draft")'

# Use with jq to create a summary report
rela export control --format json | jq '[.[] | {id, title: .properties.title, status: .properties.status}]'

# Use with Miller for CSV filtering
rela export control --format csv | mlr --csv filter '$status == "implemented"'

# Generate a gap report (controls without evidence)
rela export control --with-relations --format json | \
  jq '.[] | select(.relations.outgoing.evidencedBy == null) | {id, title: .properties.title}'

# Export to file
rela export control --format csv > controls.csv
```

**Relation Data Structure:**

When using `--with-relations`, each entity includes:

```json
{
  "id": "CTRL-001",
  "type": "control",
  "properties": { ... },
  "relations": {
    "outgoing": {
      "mitigates": [{"id": "RISK-001", "title": "Unauthorized Access"}],
      "evidencedBy": [{"id": "EV-001", "title": "Audit Report"}]
    },
    "incoming": {
      "implements": [{"id": "PROC-001", "title": "Access Procedure"}]
    }
  }
}
```

**Full Export Structure:**

When using `--all`, the output includes both entities and relations:

```json
{
  "entities": [ ... ],
  "relations": [
    {"from": "DEC-001", "relation": "addresses", "to": "REQ-001"}
  ]
}
```

---

### rela fmt

Format entity and relation files for consistent styling.

```bash
rela fmt [type] [flags]
```

**Arguments:**

- `type` - Optional: specific entity type to format

**Flags:**

| Flag        | Description                                         |
| ----------- | --------------------------------------------------- |
| `--dry-run` | Preview changes without writing                     |
| `--check`   | Check if files need formatting (exits 1 if they do) |

This command normalizes:

- Frontmatter property ordering (id/type first for entities, from/relation/to for relations)
- Markdown content formatting (headings, lists, whitespace)
- Paragraph wrapping to configured line width (default: 80 characters)

**Configuration:**

Line width can be configured in `.rela/config.yaml`:

```yaml
formatting:
  line_width: 100 # default: 80
```

**What gets wrapped:**

- Paragraph text is wrapped to the configured line width
- Code blocks, headings, lists, and blockquotes are NOT wrapped (preserved as-is)

**Examples:**

```bash
rela fmt                # Format all entities and relations
rela fmt requirements   # Format only requirements (entities)
rela fmt --dry-run      # Preview changes without writing
rela fmt --check        # Check if files need formatting (for CI)
```

**CI Integration:**

Add to your CI pipeline to ensure consistent formatting:

```yaml
- run: rela fmt --check
```

This will exit with code 1 if any files need formatting.

---

### rela import

Import entities and relations from structured files.

```bash
rela import <file> [flags]
```

**Arguments:**

- `file` - Path to the import file (JSON, YAML, or CSV)

**Flags:**

| Flag              | Description                                                                           |
| ----------------- | ------------------------------------------------------------------------------------- |
| `-f, --format`    | Input format: `json`, `yaml`, or `csv`. Auto-detected from extension if not specified |
| `-n, --dry-run`   | Validate without creating files                                                       |
| `-u, --update`    | Replace existing entities instead of failing on duplicates                            |
| `--skip-errors`   | Continue importing on validation errors                                               |
| `-r, --relations` | Path to relations CSV file (for CSV imports)                                          |

**Input Formats:**

**JSON** - Object with `entities` and `relations` arrays, or just an array of entities:

```json
{
  "entities": [
    {
      "id": "REQ-001",
      "type": "requirement",
      "properties": { "title": "User login", "status": "draft" }
    },
    {
      "id": "DEC-001",
      "type": "decision",
      "properties": { "title": "Use JWT", "status": "accepted" }
    }
  ],
  "relations": [{ "from": "DEC-001", "relation": "addresses", "to": "REQ-001" }]
}
```

**YAML** - Same structure as JSON:

```yaml
entities:
  - id: REQ-001
    type: requirement
    properties:
      title: User login
      status: draft
relations:
  - from: DEC-001
    relation: addresses
    to: REQ-001
```

**CSV** - Columns for entity fields (id, type, and properties):

```csv
id,type,title,status,priority
REQ-001,requirement,User login,draft,high
REQ-002,requirement,User logout,draft,medium
```

For CSV, relations require a separate file with `--relations`:

```csv
from,relation,to
DEC-001,addresses,REQ-001
```

**Examples:**

```bash
# Import from JSON
rela import entities.json

# Import from YAML
rela import data.yaml

# Import from CSV
rela import entities.csv

# Import CSV with separate relations file
rela import entities.csv --relations relations.csv

# Dry-run to validate without creating files
rela import --dry-run data.json

# Update existing entities instead of failing
rela import --update data.json

# Continue on validation errors
rela import --skip-errors data.json

# Explicit format (when extension doesn't match)
rela import data.txt --format json
```

**Behavior Notes:**

- **Validation**: All entities are validated against the metamodel before import
- **Auto-generated properties**: If `status` is not provided, the entity type's default is used
- **Duplicate handling**: Without `--update`, importing an existing entity ID fails
- **Update mode**: `--update` does a full replacement, not a merge (existing properties not in the import file are removed)
- **Relations**: Relations referencing entities not in the graph (and not in the import) will fail
- **Atomic by default**: If any entity fails validation, no entities are created (unless `--skip-errors`)

**Round-trip with export:**

```bash
# Export all entities and relations
rela export --all -f json > backup.json

# Later, import to a new project
rela import backup.json
```

---

### rela analyze

Run quality analysis checks.

#### rela analyze orphans

Find entities with no connections.

```bash
rela analyze orphans
```

#### rela analyze duplicates

Find entities with similar titles.

```bash
rela analyze duplicates
```

#### rela analyze unique

Find entities that violate a `unique: true` property constraint — same-type
entities sharing a value for a unique property. The write path rejects new
duplicates; this surfaces ones that already exist (e.g. after adding
`unique: true` to a property whose data already contains collisions, which
the constraint does not clean retroactively).

```bash
rela analyze unique
```

#### rela analyze gaps

Find gaps in ID sequences for entity types with sequential IDs.

```bash
rela analyze gaps
```

Gap analysis only applies to entity types with `id_type: sequential`. Entity types with
`short` (default) or `manual` IDs are excluded since they don't follow numeric sequences.

#### rela analyze cardinality

Check relation cardinality constraints.

```bash
rela analyze cardinality
```

#### rela analyze validations

Run custom validation rules defined in the metamodel.

```bash
rela analyze validations
```

Validation rules check entity properties against custom conditions.
See [Metamodel Reference - Custom Validation Rules](metamodel.md#custom-validation-rules) for details.
A rule's `when_condition:` or `then_condition:` may use `related(...)`. Here
every entity counts, including ones a role in `acl.yaml` may not read.

**Example output:**

```text
$ rela analyze validations
✗ Accepted requirements must have a priority assigned (2):
  REQ-003: User authentication
  REQ-007: Data encryption
⚠ Decisions should have a rationale documented (1):
  DEC-002: Use PostgreSQL
Found 2 errors, 1 warnings across 2 rules
```

#### rela analyze relation-files

Find relation files whose **filename** disagrees with their **content**.

```bash
rela analyze relation-files
```

Relations are stored as `relations/FROM--TYPE--TO.md`, and rela indexes them
**by filename**. If a file is renamed or hand-edited so that its name and its
`from` / `relation` / `to` frontmatter describe different relations, the
relation the content describes does not exist as far as the graph is concerned
— while the one the filename describes points at entities that may not.

The symptom shows up somewhere else entirely: `analyze cardinality` reports the
correctly-referenced entity as having zero relations, with nothing pointing back
at the offending file. This check names the file directly.

It reports three kinds of problem:

| Finding | Meaning |
| --- | --- |
| filename and content describe different relations | The store indexed the _filename_ triple; the relation the content describes is not in the graph. |
| filename is not `FROM--TYPE--TO` | The store skips the file entirely — the relation does not exist at all. |
| legacy `type:` key | The frontmatter spells the relation type `type:` instead of `relation:`. The store reads only `relation:`, so the relation loads with an **empty type** while the index says otherwise. |

**Example output:**

```text
$ rela analyze relation-files
⚠ relations/A--doet--FN-8Q7E.md: indexed as "A--doet--FN-8Q7E"
  but content says "A--doet--FN-0Q7E"
⚠ relations/B--covers--FEAT-x.md: uses `type:` instead of `relation:`
  — loads with an EMPTY relation type
⚠ Found 2 relation file(s) with filename/content problems
```

To fix a mismatch, rename the file to match its content (or correct the content
to match the name — whichever is right for that relation). To fix a legacy
`type:` key, rename the key to `relation:`; the value is already correct.

Renaming an entity through rela keeps filename and content in step
automatically; this check exists for files that were moved, hand-edited, or
merged outside it.

#### rela analyze properties

Validate entity property values against the metamodel schema.

```bash
rela analyze properties
```

Checks for:

- Invalid enum values (not in allowed list)
- Invalid custom type values
- Invalid date formats
- Invalid integer/boolean values
- Missing required properties
- Entity IDs not matching configured patterns

This catches issues in manually-edited markdown files that bypass CLI validation.

#### rela analyze schema

Analyze metamodel schema usage to find unused or underused types.

```bash
rela analyze schema
rela analyze schema --threshold 2
rela analyze schema --cleanup --dry-run
```

Shows:

- Entity types with no instances
- Relation types with no instances
- Custom types (enums) not referenced by any property
- Types with few instances (when `--threshold` is set)

**Flags:**

| Flag          | Description                                                   |
| ------------- | ------------------------------------------------------------- |
| `--threshold` | Show types with instance count <= threshold (0 = only unused) |
| `--cleanup`   | Remove unused types from schema.yaml                       |
| `--dry-run`   | Preview cleanup changes without modifying files               |

The cleanup operation only removes types that have no instances AND no references in
configuration files (data-entry.yaml, validations, automations). Types referenced in
forms, lists, views, or validations will not be removed even if they have zero instances.

#### rela analyze all

Run all analysis checks.

```bash
rela analyze all
```

Runs orphans, duplicates, gaps, cardinality, properties, and (if defined) custom validations.

---

### rela mcp

Start the MCP (Model Context Protocol) server over stdio.

```bash
rela mcp
```

Exposes rela's capabilities to AI assistants like Claude Code, Cursor, and other MCP-compatible
clients. The server runs on stdin/stdout using JSON-RPC and provides:

- **21 tools** for entity/relation CRUD, graph tracing, analysis, and export
- **3 resources** for reading entities, relations, and the metamodel by URI
- **4 prompts** for common AI-assisted workflows
- **File watching** with automatic graph sync on changes

**Client Configuration (Claude Code):**

Setup with `claude mcp add` (recommended):

```bash
claude mcp add rela -s local -- /path/to/rela mcp
```

Setup with `.mcp.json` (for sharing via git):

```json
{
  "mcpServers": {
    "rela": {
      "command": "rela",
      "args": ["mcp"]
    }
  }
}
```

See [MCP Server Guide](mcp-server.md) for the full tool/resource/prompt reference.

---

### rela tui

Launch the interactive terminal UI.

```bash
rela tui
```

See [TUI Guide](tui.md) for details.

---

### rela completion

Generate shell completion scripts.

```bash
rela completion <shell>
```

**Arguments:**

- `shell` - Target shell: `bash`, `zsh`, `fish`, or `powershell`

**Examples:**

```bash
# Bash
rela completion bash > /etc/bash_completion.d/rela

# Zsh
rela completion zsh > "${fpath[1]}/_rela"

# Fish
rela completion fish > ~/.config/fish/completions/rela.fish
```

---

### rela template

Manage templates for creating entities and relations.

Templates provide default frontmatter values and markdown body content when creating new entities or
relations. They are stored in:

- `templates/entities/<type>.md` - Entity templates
- `templates/relations/<type>.md` - Relation templates

#### rela template init

Generate template files from the metamodel.

```bash
rela template init [type...] [flags]
```

**Arguments:**

- `type...` - Optional: specific entity or relation types to generate templates for

**Flags:**

| Flag          | Description                      |
| ------------- | -------------------------------- |
| `--entities`  | Only generate entity templates   |
| `--relations` | Only generate relation templates |
| `--force`     | Overwrite existing templates     |

Without arguments, generates templates for all entity and relation types defined in the metamodel.

**Examples:**

```bash
# Generate all templates
rela template init

# Generate template for a specific entity type
rela template init requirement

# Generate template for a specific relation type
rela template init addresses

# Generate only entity templates
rela template init --entities

# Generate only relation templates
rela template init --relations

# Overwrite existing templates
rela template init --force

# Generate specific types with force
rela template init requirement decision --force
```

**Generated Template Format:**

Entity templates include all properties from the metamodel with their default values:

```markdown
---
title: ""
status: draft
priority: medium
---

# Description

Describe your requirement here.
```

Relation templates include a placeholder for rationale:

```markdown
---
---

# Rationale

Explain why this addresses relation exists.
```

**Using Templates:**

Once templates are created, they are automatically applied when using `rela create` or `rela link`.
CLI flags override template defaults.

```bash
# Create a template
rela template init requirement

# Edit the template to customize defaults
# templates/entities/requirement.md

# New entities will use the template
rela create requirement -P title="My Requirement"
```

---

### rela migrate

Migrate project files to the current schema format.

```bash
rela migrate [flags]
```

**Flags:**

| Flag      | Description                                                   |
| --------- | ------------------------------------------------------------- |
| `--check` | Check for pending migrations without applying (useful for CI) |

This command detects deprecated syntax patterns in your project files (e.g., `schema.yaml`) and
transforms them to the current format while preserving comments and formatting.

It also renames a legacy `metamodel.yaml` to `schema.yaml`.

If a project contains **both** files, `schema.yaml` is the one rela reads and the
`metamodel.yaml` is ignored. Nothing is renamed or deleted in that case — `migrate`
reports the ignored file so you can merge anything you still need and remove it,
and `migrate --check` exits non-zero until you do.

**When to use:**

If you see an error like this when running any rela command:

```text
schema.yaml uses deprecated syntax:
  - Rename id_type values: "auto" → "sequential", "string" → "manual"

Run 'rela migrate' to update your project files.
```

Run `rela migrate` to automatically update your files.

**Examples:**

```bash
# Apply all pending migrations
rela migrate

# Check for migrations without applying (for CI pipelines)
rela migrate --check
```

**CI Integration:**

Add to your CI pipeline to ensure project files are up-to-date:

```yaml
- run: rela migrate --check
```

This will exit with code 1 if migrations are needed.

### rela migrate status / gen / data / gc / baseline

Operate the **data-migration** system: when `schema.yaml` changes shape
(property renamed, type changed, enum values remapped), these commands
detect it, draft a migration, and transform the stored content. See the
[data-migration guide](data-migration.md) for the full model.

```bash
rela migrate status              # where does the data stand vs the live schema?
rela migrate gen                 # draft migrations/<timestamp>-<slug>.yaml from the diff
rela migrate data                # dry-run the pending migrations
rela migrate data --apply        # execute them
rela migrate baseline            # dry-run recording migrations as already applied
rela migrate baseline --apply    # record them, without running them
rela migrate gc                  # dry-run the orphaned-data garbage collector
rela migrate gc --scan --apply   # full-scan for orphans, then delete expired ones
```

**Flags:**

| Command | Flag | Description |
| ------- | ---- | ----------- |
| `gen`   | `--description` | One-line description embedded in the migration file |
| `gen`   | `--stdout` | Print the draft instead of writing `migrations/<name>` |
| `data`  | `--apply` | Execute (default is a dry-run preview with per-step counts) |
| `baseline` | `--apply` | Record the baseline (default is a dry-run preview) |
| `gc`    | `--apply` | Delete expired orphaned data (default is a dry-run preview) |
| `gc`    | `--scan`  | Full-scan the store for orphans not yet in the drift ledger |
| `gc`    | `--grace` | Override the grace period orphaned data must age before deletion (default 720h) |

Unlike bare `rela migrate` (config files, service-free), these subcommands
need the project services and a store. Compatible schema changes (new types,
new optional properties, new enum values) never need any of this — they are
adopted automatically. `status` exits 1 while an incompatible change is
unmigrated, so it doubles as a CI check.

**Which migrations have run** is tracked by NAME, so a migration need not
change the schema at all: a backfill or a data correction is an ordinary
migration file you write by hand (`gen` drafts only from a schema diff).

**`baseline`** is for a store whose content already matches the current schema
but that has no record of the migrations in the project — an existing project
adopting the system, or one whose record was lost. It marks them applied
without running them, so use it only when that claim is true; `migrate data`
is the right command when they genuinely still need to run.

---

### rela normalize

Normalize markdown headers in entity files to start at level 2 (##).

```bash
rela normalize [type] [flags]
```

This command adjusts header levels so the minimum header level in each entity is `##`,
preserving the relative hierarchy. For example:

```markdown
# Overview → ## Overview

## Details → ### Details

### Subsection → #### Subsection
```

Setext-style headers (underlined with `===` or `---`) are converted to ATX style (`##`).

**Arguments:**

- `type` - Optional: specific entity type to normalize

**Flags:**

| Flag        | Description                     |
| ----------- | ------------------------------- |
| `--dry-run` | Preview changes without writing |

**Examples:**

```bash
rela normalize                # Normalize all entities
rela normalize requirements   # Normalize only requirements
rela normalize req            # Alias works too
rela normalize --dry-run      # Preview changes without writing
```

---

### rela rename

Rename entity types or entity IDs across the project.

#### rela rename entity

Rename an entity type across the entire project.

```bash
rela rename entity <old-type> <new-type> [flags]
```

**Arguments:**

- `old-type` - Current entity type name
- `new-type` - New entity type name

**Flags:**

| Flag          | Description                             |
| ------------- | --------------------------------------- |
| `--plural`    | Override plural form for directory name |
| `-f, --force` | Skip confirmation prompt                |

This updates:

- The entity key in `schema.yaml`
- All relation `from`/`to` references in `schema.yaml`
- All validation `entity_type` references in `schema.yaml`
- The entity directory (e.g., `entities/issues/` → `entities/tickets/`)
- The `type` field in all entity markdown files
- Entity templates (if they exist)

**Examples:**

```bash
rela rename entity issue ticket
rela rename entity issue ticket --plural tickets
rela rename entity requirement feature --force
```

#### rela rename id

Rename an entity's ID and update all relations that reference it.

```bash
rela rename id <old-id> <new-id> [flags]
```

**Arguments:**

- `old-id` - Current entity ID
- `new-id` - New entity ID

**Flags:**

| Flag        | Description                      |
| ----------- | -------------------------------- |
| `--dry-run` | Preview changes without applying |

This updates:

- The entity file (renamed and `id` field updated)
- All relation files where this entity is the `from` or `to` endpoint

**Examples:**

```bash
rela rename id REQ-001 REQ-100
rela rename id REQ-001 REQ-100 --dry-run
```

---

### rela schema

View the metamodel schema.

```bash
rela schema [command] [flags]
```

Displays information about the loaded metamodel including entity types, relation types,
and custom types.

**Subcommands:**

| Command     | Description                             |
| ----------- | --------------------------------------- |
| `overview`  | Show metamodel overview (default)       |
| `entities`  | List all entity types with descriptions |
| `relations` | List all relation types                 |
| `types`     | List custom types defined in metamodel  |
| `entity`    | Show details for a specific entity type |
| `relation`  | Show details for a specific relation    |

**Flags:**

| Flag            | Description                                   |
| --------------- | --------------------------------------------- |
| `--graphviz`    | Output metamodel as GraphViz DOT format       |
| `--constraints` | Include cardinality constraints in DOT output |

**Examples:**

```bash
rela schema                    # Overview
rela schema entities           # List entity types
rela schema relations          # List relation types
rela schema types              # List custom types
rela schema entity service     # Detail for one entity type
rela schema relation addresses # Detail for one relation type
rela schema --graphviz         # Output as DOT format
rela schema --graphviz --constraints  # DOT with cardinality
```

---

### rela gc

Remove orphaned files left behind by interrupted writes.

```bash
rela gc [flags]
```

Attachments are 1:1 with their owning entity — deleting an entity takes its
attachments with it, so there is no separate attachment GC pass.

**Flags:**

| Flag           | Description                                            |
| -------------- | ------------------------------------------------------ |
| `--temp-files` | Clean up orphaned `.new` files from interrupted writes |
| `--dry-run`    | Show what would be removed without actually removing   |

**Examples:**

```bash
rela gc --temp-files            # Remove orphaned temp files
rela gc --temp-files --dry-run  # Preview what would be removed
```

---

### rela dev seed

Fill an **empty** project store with generated data for performance work.

```bash
rela dev seed [flags]
```

The data comes from a deterministic generator (`internal/perfseed`) shaped for
the perf demo project in `prototypes/perf/project`: teams, people, projects,
tasks, controls, risks, policies with draft/published faces and documents with
en/nl faces, plus the relations between them. The same `--seed` and `--scale`
always produce the same graph, on every backend — on the default build it
writes markdown files, on the postgres build it writes straight into the
database named by `RELA_DATABASE_URL`.

This is a raw store write: no automations, validations or ACL run, the writes
are attributed to the operator with tool `perf-seed`, and the run leaves one
`perf-seed` audit record. It refuses a store that already holds entities so a
second run cannot sit beside real data.

**Flags:**

| Flag        | Description                                                                 |
| ----------- | --------------------------------------------------------------------------- |
| `--profile` | Which graph to generate; only `perf` exists today                           |
| `--scale`   | Size multiplier in (0, 10]; `1` is roughly 20k entities and 45k relations   |
| `--seed`    | PRNG seed (default 1)                                                       |
| `--batch`   | Writes per transaction (default 500)                                        |
| `--force`   | Seed even though the store already holds entities                           |

**Examples:**

```bash
cp -R prototypes/perf/project /tmp/perf && cd /tmp/perf
rela dev seed --scale 0.05                 # a few hundred entities as markdown files

export RELA_DATABASE_URL='postgres://rela@localhost/rela_perf?sslmode=disable'
rela-postgres dev seed --project prototypes/perf/project --scale 1
```

The filesystem backend writes one file per row and indexes each for search,
so keep the scale small there; the full graph is meant for PostgreSQL, where
scale 1 loads in well under a minute.

---

### rela validate

Validate project configuration files.

```bash
rela validate
```

Checks `schema.yaml` and `data-entry.yaml` for:

- Unknown/misspelled keys
- Invalid cross-references (forms, lists, views)
- Invalid entity types, relations, and properties
- View traversal correctness
- Dashboard and command configuration

**Examples:**

```bash
rela validate
```

Pass `--check` to also validate the entity graph:

```bash
rela validate --check cardinality --check properties --check validations
```

**Exit codes:**

| Code | Meaning |
| ---- | ------- |
| `0`  | Every rule was evaluated over the whole project, and all passed. |
| `1`  | Every rule was evaluated, and at least one violation was found. |
| `2`  | Some input could not be read, so the run is **incomplete**. The offending files are named in the output. |

Exit `2` takes precedence over exit `1`. A run that could not read all of
its input cannot support a claim about the rules, including the claim that
the violations it reported are all of them. The usual cause is an entity
whose YAML frontmatter does not parse, most often an unquoted scalar
containing a colon followed by a space — quote it:

```yaml
title: "Something: with a colon"
```

Treat exit `2` as a failure in CI. "All validations passed." is printed only
when the run actually read everything.

Note that `--check cardinality` on its own only scans the entity types that
declare a cardinality bound, so it reports an unreadable file only for those
types. `--check properties` and `--check validations` scan all types.

---

### rela version

Print version information.

```bash
rela version
```
