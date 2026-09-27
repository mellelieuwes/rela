---
id: GUIDE-data-entry
type: guide
title: "Data Entry Web App"
status: published
order: 10
audience: intermediate
summary: "Config-driven web UI for entity management"
---

The data entry application provides a web-based UI for creating, editing, and browsing entities
stored in a rela project. It is configured entirely through a `data-entry.yaml` file placed
alongside your `schema.yaml`.

## Overview

A `data-entry.yaml` file defines:

- **App metadata** - Name and description shown in the UI
- **Git settings** - Protected branches that require pull requests
- **Styles** - Color mappings for enum values displayed in lists and forms
- **Forms** - Create and edit forms for entity types, with fields and relation pickers
- **Lists** - Tabular views with sorting, filtering, and pagination
- **Views** - Read-only detail pages that traverse the graph to show related entities
- **Dashboard** - An overview page with query-driven cards showing counts, breakdowns, and tables
- **Kanbans** - Visual board views with drag-and-drop cards grouped by columns and optional swimlanes
- **Navigation** - Sidebar menu entries with optional grouping
- **Actions** - Quick operations (property mutations or Lua scripts) triggered from lists or the sidebar
- **Commands** - User-defined scripts triggered from the UI with streamed results
- **Documents** - Read-only rendered markdown panels attached to entity views, composed via shell commands or Lua scripts
- **User Defaults** - Per-user default values for properties and relations, configurable via Settings page

The file drives the entire UI without writing any code. The server reads `data-entry.yaml` and
your `schema.yaml` together, validates them, and serves a fully functional CRUD application.

### Loading and saving feedback

The UI is deliberately quiet about waiting. rela usually runs against a local
or nearby server, so most requests finish in well under a tenth of a second —
fast enough that a spinner would appear and vanish before you could read it.
Rather than flash one, the UI shows **nothing at all** unless an operation is
genuinely slow:

- **Navigating** to another page shows a thin progress bar across the top of
  the window, but only once the page has taken about a quarter-second.
- **Saving, creating or searching** changes the button's own label — "Save"
  becomes "Saving…" — but only after about half a second. The button is sized
  in advance for both labels, so it never changes width or shifts the layout
  under your cursor.
- **Autosaving** a form shows a small status mark beside the section you are
  editing, which settles into a checkmark once the change is stored.
- **Background refreshes** — when someone else changes data you are looking at
  — update the page silently, without blanking what is already on screen.

If you see no indicator at all, the operation completed quickly. That is the
intended behaviour.

### Opening things in a new tab

Anything that navigates is a real link: list rows, kanban cards, search
results, side-panel entries, related-entity titles and quick-search results,
along with the Prev/Next, Edit and History controls on an entity page. So the
usual browser gestures all work as you would expect:

- **Cmd-click** (macOS) or **Ctrl-click** (Windows/Linux) opens it in a new
  background tab.
- **Middle-click** does the same.
- **Shift-click** opens it in a new window.
- **Right-click** offers "Open link in new tab", and hovering shows the target
  address in the status bar.

A plain click still navigates in place, without a full page reload. A new tab
opened this way keeps the context of the list you came from, so the
previous/next controls on the entity page still walk the same result set.

Buttons that change data — Delete in particular — stay buttons, since opening
one in a new tab would mean nothing.

Note that a list row is a link across its whole width, which means dragging to
select text inside a row is not possible; use the entity page for that.

## Quick Start

### 1. Create data-entry.yaml

Place a `data-entry.yaml` in your project root (next to `schema.yaml`):

```yaml
version: "1.0"

app:
  name: "My Project"
  description: "Project management system"

forms:
  create_task:
    entity_type: task
    title: "New Task"
    body: true
    fields:
      - property: title
        label: "Title"
        required: true
      - property: status
        label: "Status"
        default: open

lists:
  all_tasks:
    entity_type: task
    title: "All Tasks"
    columns:
      - property: title
        label: "Title"
        sortable: true
        link: true
      - property: status
        label: "Status"
        sortable: true
    create_form: create_task
    page_size: 25

navigation:
  - label: "Tasks"
    list: all_tasks
```

### 2. Start the Server

```bash
rela-server -project /path/to/project
```

Or with a custom config path:

```bash
rela-server -project /path/to/project -config /path/to/data-entry.yaml
```

The server starts on port 8080 by default. Open `http://localhost:8080` in your browser.

## File Structure

```yaml
version: "1.0"            # Config format version

app:                       # Application metadata
  name: "..."
  description: "..."

git:                       # Git sync settings
  require_pr: [main]

styles:                    # Color mappings for enum values
  status:
    open: blue
    closed: gray

forms:                     # Create/edit form definitions
  form_name:
    entity_type: task
    ...

lists:                     # List view definitions
  list_name:
    entity_type: task
    ...

views:                     # Detail view definitions
  view_name:
    entry:
      type: task
    ...

dashboard:                 # Optional overview page
  title: "Dashboard"
  cards:
    - title: "Open"
      query: "type:task status:open"
      display: count

kanbans:                   # Kanban board views
  board_name:
    entity_type: task
    column_property: status
    ...

commands:                  # User-defined scripts
  export-json:
    label: "Export JSON"
    script: "jq '.' > /tmp/export.json"
    context: entity

navigation:                # Sidebar menu (supports groups)
  - label: "Dashboard"
    dashboard: true
  - group: "Tasks"
    items:
      - label: "All Tasks"
        list: all_tasks
```

### Reloading the file while the server runs

The server watches `data-entry.yaml` and reloads it when the file changes. A
reload runs the same checks as startup. The file must parse and pass validation
against the schema. Every `script:` it names for an action, a document or an
`export_render` override must exist.

If a check fails, the server rejects the reload and keeps serving the previous
config. It logs the error and sends it to open browsers, which show it as an
error message. Fix the file and save it again. The first save that passes the
checks is applied.

Some settings are read only at startup. A reload accepts changes to them, but
they take effect only after a restart:

- the `git:` block;
- `app.name` and `app.description` where they form the OpenAPI title and
  description;
- on the PostgreSQL backend, the derived indexes for static dashboard and
  next-action queries.

## App

Display metadata shown in the header:

```yaml
app:
  name: "Support Tickets"
  description: "Internal ticket management system"
```

| Field         | Description                      |
| ------------- | -------------------------------- |
| `name`        | Application title in the header  |
| `description` | Subtitle shown below the title   |
| `default_world` | World a request lands in when the URL carries no `?world=`. See [Worlds in the web app and API](#worlds-in-the-web-app-and-api) |

### PlantUML diagrams

Set `plantuml_server_url` to render ` ```plantuml ` fenced code blocks as
diagrams. Leaving it unset (the default) disables the feature: those blocks
render as plain code and no diagram source leaves the browser.

```yaml
app:
  name: "Support Tickets"
  plantuml_server_url: "https://plantuml.internal.example.com"
```

| Field                 | Description                                          |
| --------------------- | ---------------------------------------------------- |
| `plantuml_server_url` | Base URL of a PlantUML server. Empty = disabled.     |

Rendering happens on the server you name here: the browser encodes the diagram
source into the image URL it requests. **That source is your content**, so point
this at a server you trust — it is deliberately not defaulted to the public
`plantuml.com`, which would publish private diagrams to a third party.

For the same reason `http://` is accepted **only for loopback hosts**
(`localhost`, `127.0.0.0/8`, `::1`), which covers the common case of running
PlantUML as a local sidecar. Any other host must use `https://`, because a
cleartext request exposes the diagram source to everything on the network path.
A hostname that merely looks local (`localhost.example.com`) is treated as
remote. Invalid combinations are rejected when the config loads, not silently at
render time.

The check covers the URL you configure, not where it ends up: an `https://` URL
whose server redirects to `http://` still sends the source in cleartext, and
rela cannot see that. Point this at a server you control.

## Git

Configure git synchronization behavior:

```yaml
git:
  enabled: true
  mode: direct              # "direct" or "pr"
  branch: main              # Branch to sync with (direct mode)
  base_branch: main         # Branch to rebase onto (pr mode)
  push_branch: feature/data # Branch to push to (pr mode)
  fetch_interval: 30        # Background fetch interval in seconds (0 = disabled)
  require_pr: [main, production]
```

| Field            | Description                                                           |
| ---------------- | --------------------------------------------------------------------- |
| `enabled`        | Enable git sync features (status bar, sync button)                    |
| `mode`           | `direct` pushes to the same branch; `pr` rebases onto base and pushes to a separate branch |
| `branch`         | Target branch for direct mode (default: `main`)                       |
| `base_branch`    | Branch to rebase onto in PR mode                                      |
| `push_branch`    | Branch to push to in PR mode                                          |
| `fetch_interval` | Seconds between background fetches (0 disables background fetch)      |
| `require_pr`     | List of branch names where direct push is blocked (protected branches) |

### Sync behavior

When git is enabled, the UI shows a status bar with:

- Current branch name
- Number of local changes (uncommitted files in `entities/` and `relations/`)
- Number of remote commits ahead
- Conflict indicator if a rebase conflict is in progress

The **Sync** button performs:

1. Stage all changes in `entities/` and `relations/`
2. Commit with an auto-generated message describing the changes
3. Fetch from remote
4. Rebase onto the target branch (if behind)
5. Push to the remote

If a rebase conflict occurs, the status bar shows a conflict indicator and provides options to
resolve conflicts or abort the rebase.

When editing on a protected branch, the UI shows a banner suggesting the user create a working
branch. Commits are auto-created on every entity change, but push is blocked until the user
switches to a non-protected branch.

## Styles

Map enum values to colors for visual display in lists and forms:

```yaml
styles:
  status:
    draft: gray
    review: blue
    approved: green
    active: green
    retired: gray

  priority:
    critical: red
    high: orange
    medium: yellow
    low: green
```

The key is the custom type name (as defined in `schema.yaml` under `types:`). Each value maps
to a color name. These colors are applied everywhere that enum value appears: list cells, badges,
and form select options.

**Available colors:** `red`, `orange`, `yellow`, `green`, `blue`, `purple`, `gray`.

For customisation beyond palette and theme — arbitrary CSS, JavaScript and
assets against rela's own UI, from a `custom/` directory in your project — see
[Operator customisation hooks](customisation.md). That is an explicitly
best-effort escape hatch; the palette/theme system remains the supported path
for ordinary branding.

## Display names

Every entity's display name — the human-readable string shown in
lists, cards, side-panel breadcrumbs, related-entity links, and
search results — comes from the entity type's *primary property*.
Set it with `display_property` in `schema.yaml`:

```yaml
# schema.yaml
entities:
  applicatie:
    label: Applicatie
    display_property: naam
    properties:
      naam:
        type: string
        required: true
```

Without `display_property`, rela auto-derives one from
`title` / `name` / `label` (then any required string property,
alphabetical). That's brittle for non-English schemas — pin it
explicitly. See [GUIDE-metamodel.md → Display
name](metamodel.md#display-name) for the metamodel-side rules
(allowed types, validation diagnostics).

Where the display name shows up in the data-entry app:

- **List columns**: a column with `link: detail` renders the entity's
  display name as the link text.
- **Cards**: card titles (in `display: cards` sections, kanban cards,
  related-entity widgets).
- **Breadcrumbs**: the side panel and form headers show the display
  name above the ID.
- **Related-entity links**: every relation widget that renders linked
  entities uses the display name as link text.
- **Search results**: each result row shows the display name first.

When the display value is empty, missing, or `nil`, the UI falls
back to the entity ID — never an empty string.

## Forms

Forms define the UI for creating and editing entities. Each form is a named entry under `forms:`.

### Basic Form

```yaml
forms:
  create_ticket:
    entity_type: ticket
    title: "New Ticket"
    description: "Submit a new support ticket"
    body: true

    fields:
      - property: title
        label: "Title"
        placeholder: "Brief summary..."
        required: true

      - property: priority
        label: "Priority"
        default: medium

    relations:
      - relation: belongs-to
        direction: outgoing
        target_type: category
        label: "Category"
        widget: select
```

### Form Fields

| Field            | Type   | Description                                               |
| ---------------- | ------ | --------------------------------------------------------- |
| `entity_type`    | string | Entity type this form operates on (must exist in metamodel) |
| `title`          | string | Form heading                                              |
| `description`    | string | Help text shown below the heading                         |
| `mode`           | string | `"edit"` for edit forms (omit for create forms)           |
| `body`           | bool   | Show a markdown body editor                               |
| `fields`         | list   | Property fields                                           |
| `relations`      | list   | Relation picker fields                                    |

### Field Options

Each entry in `fields:` configures one property input:

| Field         | Type              | Description                                                    |
| ------------- | ----------------- | -------------------------------------------------------------- |
| `property`    | string            | Property name from the metamodel                               |
| `label`       | string            | Display label (defaults to property name)                      |
| `placeholder` | string            | Placeholder text for empty inputs                              |
| `help`        | string            | Help text shown below the field                                |
| `required`    | bool              | Field must be filled before submission                         |
| `default`     | string            | Default value for new entities                                 |
| `hidden`      | bool              | Include in form data but hide from UI                          |
| `widget`      | string            | Input widget type (see below)                                  |
| `span`        | int (1-12)        | Width on the layout grid; omit for full width (see below)      |
| `transitions` | map[string]list   | Allowed state transitions for enum fields (edit forms only)    |

> **Labels are authored, never derived.** When `label` is omitted the raw
> property name is displayed — rela never converts `laatste_contact` into
> `Laatste Contact`. Any such conversion would encode an English orthographic
> convention (word splitting, capitalization) into a metamodel that is
> deliberately language-neutral, and it is wrong for most languages. Write the
> label you want, in your project's own language. The same rule applies to list
> column headers, relation field labels, view-section fields, and Lua flow
> fields. `rela migrate` will never remove a `label:` you have written.

### The Markdown Body Editor

When a form sets `body: true`, the entity's markdown content is edited in a
WYSIWYG editor: headings, lists, tables, quotes, links, dividers and code
blocks render as they will appear on the entity page rather than as markdown
source.

**The file on disk stays markdown.** The editor parses the body when it opens
and writes markdown back when you save. Opening an entity and saving it
without changing anything writes nothing at all — the editor compares what it
would produce against what it read, and keeps the original bytes when the
meaning is unchanged. Your formatting is not rewritten just because you looked
at a page.

**Toolbar.** Bold, italic, strikethrough, inline code and link; headings 1-3;
bullet, numbered, task and quote blocks; code block; table; divider; undo and
redo. A button is
highlighted when the cursor is already inside that formatting, and pressing it
again removes it. A button that cannot apply where the cursor is — a heading
inside a list item, for instance — is greyed out rather than silently doing
nothing.

**Task lists.** The task-list button turns the current line into a checkbox
item, and pressing it again turns it back into an ordinary one. You can also
type `[ ]` followed by a space at the start of an existing list item. Click a
checkbox to tick or untick it; the change is part of the document, so it saves
with the rest of your edits and undoes with Ctrl-Z. In markdown these are the
usual `- [ ] open` and `- [x] done` lines.

**Tables.** Put the cursor in a table and a second group of buttons appears:
insert row above/below, insert column left/right, delete row, delete column,
delete table. A GFM table must keep its header row and at least one body row,
so the operations that would break that are disabled.

**Links to the web.** Select some text and press the link button to give it a
target; with no selection, the dialog asks for the text as well. Put the cursor
in an existing link and a small panel appears below it showing the address,
with Edit and Remove. The toolbar covers the same ground without a pointer:
with the cursor in a link, the link button reopens the dialog to change the
address, and a remove-link button appears beside it. Pasting a web address over
selected text turns that text into a link rather than replacing it.

Only `http`, `https` and `mailto` addresses are accepted, and anything else is
refused with a message rather than saved. A bare host is completed for you —
`example.com` becomes `https://example.com` — but a relative path like
`/other/page` is refused, because an entity body is read on several surfaces
that do not share a base URL, so a relative target has no stable meaning.
A `mailto:` address keeps the address and drops any `?subject=` or `?bcc=`
parameters.

This applies to links you *create*. A link already in a file is left exactly as
written, even if its address uses a scheme the editor would not accept — your
stored content is never rewritten behind your back. Such a link is not
clickable anywhere in rela.

**Linking to another entity.** Type `@` followed by part of a title or ID to
open a completion menu, then Enter or click to insert. The toolbar's
connected-nodes button opens a searchable picker for the same thing.

Matching is fuzzy and spans the title and the ID together, so you do not have to
remember which one a word came from: `@fancy-rank` finds "FancyReport ranking
fix", and a partial ID still puts an exact or prefix match at the top.

**Narrowing by type.** A bare `@` lists the entity types, and they start
narrowing from the very first letter you type: `@t` offers the types beginning
with "t", and the three closest stay on offer above the results. Pressing
Enter on a type (or clicking it) does not insert anything — it scopes the search
to that type and shows it as a chip, so the next thing you type only matches
entities of that type. This is the way to reach an entity when you remember its
type and only a fragment of its title. Once the query is longer than about six
characters the types drop away, since by then you are plainly naming an entity
rather than browsing.

To drop the scope, press Backspace until the query is empty and then once more;
the chip clears and the search widens again. A space still closes the menu
entirely, so an `@` in ordinary prose leaves nothing hanging open.

A reference is stored as a plain code span — `` `TKT-007` `` — and displays as
the entity's title, so the file stays readable outside rela and the title
cannot go stale. Titles you see are the ones you are permitted to see: a
reference to an entity your ACL role cannot read stays as the bare ID.

### File Properties on Forms

A `file`-type property renders a file control: pick or drag a file, see its
name, size and (for images) a preview, and remove it. It works on both **create
and edit** forms.

On an **edit** form the pick uploads immediately — the entity exists, so the
bytes go straight to it.

On a **create** form there is nothing to upload *to* yet. An attachment is
stored against an entity id, and the id does not exist until the entity is
saved, so the form **stages** your pick and uploads it right after the create
succeeds. In practice this is invisible: you fill the form, attach a file, press
Create, and the entity is saved with its file. Two details are worth knowing:

- A staged file is marked **"Pending save"** until it is uploaded, and counts
  toward the property's `max` while staged.
- Because the upload is a second step, it can fail on its own (a file over the
  size limit, or one your scan policy rejects). If that happens the entity is
  **still created**: you land on it, and the message names the files that did
  not attach so you can re-attach them there, on the entity's own file control.
  Nothing is silently dropped — but the failed files do have to be picked
  again, since the form that held them is gone by then.

Attachments inherit the owning entity's permissions: if you may not update the
entity, you may not attach to it.

### Field Layout (`span`)

Fields lay out on a **12-column grid**. A field with no `span` takes the full
row, so a form or view section reads as one scannable column by default — you
have to author nothing to get a sensible layout.

Set `span` to put related fields side by side:

```yaml
fields:
  - property: title # full width (no span)
  - property: description
    widget: textarea # full width
  - property: priority
    span: 4 # ┐
  - property: reporter
    span: 4 # ├ three across one row (4+4+4 = 12)
  - property: assignee
    span: 4 # ┘
  - property: due_date
    span: 6 # ┐ two across the next row
  - property: estimated_hours
    span: 6 # ┘
```

`span` works the same way on form fields and on view-section fields, so the
model is worth learning once.

**Adjacency is something you declare.** Two fields share a row because you said
they belong together — not because the browser window happened to be wide
enough. That is the point: a layout that regroups itself at different window
sizes is not a layout you can reason about.

Row behaviour, all deliberate:

- **A row that doesn't add up to 12 leaves the remainder empty.** Two `span: 5`
  fields occupy 10 columns and the last 2 stay blank; fields do not stretch to
  close the gap.
- **A field that doesn't fit wraps to the next row**, leaving the remainder of
  the previous one empty. `span: 8` followed by `span: 6` gives you two rows.
- **On narrow screens every field goes full width.** A `span: 4` date input on a
  phone would be unusable, so the grid collapses to one column below 640px.
- **Long-form values ignore `span`** on entity detail pages: a paragraph
  squeezed into a third of the row is unreadable whatever the config says.

A `span` outside 1-12 is a **config error at startup**, reported with the
offending field's position — rela will not silently round it or ignore it:

```text
form "create_ticket": field[3]: span 13 is out of range (must be 1-12, or omitted for full width)
```

A `span` on a **relation** is also an error: relation widgets (card lists,
searchable pickers) always take the full row, so a narrow one would break them.

### Widget Types

| Widget     | Description                                      | Use For                        |
| ---------- | ------------------------------------------------ | ------------------------------ |
| *(default)* | Auto-detected from property type                | Strings, enums                 |
| `text`     | Single-line text input                           | Short strings                  |
| `textarea` | Multi-line text area                             | Descriptions, notes            |
| `number`   | Numeric input                                    | Integers                       |
| `date`     | Date picker                                      | Date properties                |
| `datetime` | Date + time picker                               | Datetime properties            |
| `checkbox` | Toggle checkbox                                  | Boolean properties             |

When no widget is specified, the system auto-detects from the property's type in the metamodel:
enum types render as a `<select>`, booleans as checkboxes, dates as date pickers, `datetime`
properties as date+time pickers, and everything else as text inputs.

### Datetime fields and time zones

A `datetime` property renders as a native date + time picker. Because a
datetime is a specific instant, the field must communicate which time zone the
entered wall-clock time means:

- **Values are stored as UTC** (RFC3339, e.g. `2026-07-13T12:30:00Z`). The
  widget converts between your local wall-clock time and UTC as you type.
- **The field shows the active time zone** beneath the input ("Times shown in
  `Europe/Amsterdam`"), so the interpretation is never hidden.
- **The display time zone is configurable** on the **Settings** page under
  *Display timezone*. It defaults to your browser's time zone and applies to
  every datetime field. It is a **display-only** preference stored in your
  browser — changing it re-labels existing values but never rewrites the stored
  UTC instant.
- **Editing is non-destructive.** Viewing an entity, or saving an unrelated
  field, never rewrites a datetime you didn't touch — so values authored in a
  different time zone don't produce spurious diffs.

Note: a value that is exactly midnight UTC (e.g. a bare `2026-07-13` written by
hand and interpreted as `2026-07-13T00:00:00Z`) displays on the **previous
evening** in time zones west of UTC. The picker itself always writes a full
instant, so this only affects hand-authored midnight values.

### ID Controls on Create Forms

Create forms adapt to the entity type's `id_type` and `id_prefix` / `id_prefixes`
configuration in the metamodel. The user-facing UI is generated automatically —
no extra form configuration is required.

- **Single-prefix types** (`id_prefix: "TKT-"`): no extra controls. The form
  submits and the server assigns the next ID.
- **Multi-prefix types** (`id_prefixes: ["DEC-", "ADR-"]`): the create form
  renders a **Prefix** dropdown so the user picks which prefix the new entity
  should use. The server validates the chosen prefix against the declared
  list — unknown values are rejected with a 422.
- **Manual ID types** (`id_type: manual`): the create form renders a required
  **ID** text input that is sent verbatim as the entity's ID. If the type also
  declares one or more prefixes, the supplied ID must start with one of them
  and include a non-empty suffix. The edit form shows the ID as a read-only
  display; renaming uses the dedicated rename flow.

### Create & add another

Create forms carry a secondary **Create & add another** action beside the
primary **Create**. It creates the entity exactly as Create does, then — instead
of navigating to the new entity — stays on the form and clears it for the next
record. Use it when entering a batch: ten tasks, a stack of contacts, a set of
notes.

The created entity's ID is named in a confirmation toast, since you are not
taken to it.

The reset is **clean**: every field goes back to its metamodel default, its
form-level `default`, and any `prop.*` / `rel.*` / `link_*` pre-fill in the URL
(those survive because the action does not navigate, so the query string is
unchanged). The body/content is always cleared.

To carry a value across records, mark the field or relation with
`keep_on_add_another`:

```yaml
forms:
  new-task:
    entity_type: task
    fields:
      - property: project
        keep_on_add_another: true   # batch context — survives to the next record
      - property: title             # cleared, like everything unmarked
    relations:
      - relation: assigned-to
        keep_on_add_another: true
```

Notes:

- The default is `false` — a clean reset. This is the safe direction: a field
  you forgot to mark costs one re-entry, whereas a field wrongly carried over
  writes a stale value into every subsequent record without saying so.
- It applies to **relations** as well as properties. The batch context worth
  keeping is often a relation (a project, an assignee), not a property.
- It is rejected on a `hidden: true` field at config load — a hidden field has
  no entered value to keep, so use `default` there instead.
- It has no effect on edit forms, which autosave per field and have no
  Create button.
- The action is not offered on the inline-create modal reached from a relation
  picker: that flow exists to create exactly one entity and link it.

### State Transitions

For edit forms, you can restrict which enum values are selectable based on the current value:

```yaml
fields:
  - property: status
    label: "Status"
    transitions:
      open: [in-progress, closed]
      in-progress: [open, resolved]
      resolved: [closed, in-progress]
      closed: [open]
```

Each key is a current value; its list contains the values the user can transition to. The current
value is always implicitly included. If `transitions` is omitted, all enum values are shown.

### Relation Fields

Each entry in `relations:` configures a relation picker:

| Field          | Type   | Description                                                    |
| -------------- | ------ | -------------------------------------------------------------- |
| `relation`     | string | Relation type name from the metamodel                          |
| `direction`    | string | `"outgoing"` or `"incoming"` — inferred when omitted; required for self-referencing relations (see below) |
| `target_type`  | string | Entity type of the related entity                              |
| `label`        | string | Display label                                                  |
| `required`     | bool   | At least one relation must be selected                         |
| `widget`       | string | `"select"`, `"multi-select"`, `"cards"`, or `"search"` (auto-detected) |
| `properties`   | list   | Editable properties on the relation (only with `cards` widget) |

**Relation widget types:**

| Widget         | Description                                                  |
| -------------- | ------------------------------------------------------------ |
| `select`       | Dropdown listing all entities of the target type (pick one)  |
| `multi-select` | Tag-style picker for selecting multiple entities             |
| `cards`        | Card-based UI with inline property editing (auto-selected when relation has properties or content) |
| `search`       | Type-ahead search field for large entity sets                |

Widget is auto-detected based on metamodel: if the relation type has `properties` or `content: true` defined,
the UI uses `cards`. Otherwise, cardinality determines `select` vs `multi-select`.

**Inline creation:** A `+ New <Type>` button appears in the relation widget when you need to link
something that does not exist yet. Clicking it opens the target type's create form in a modal,
and the new entity is selected for linking — all without leaving the form you are filling in, so
your in-progress input is preserved.

There is no configuration for this. The button appears for a target type when **both** hold:

1. the current user has permission to `create` that entity type, and
2. a form is registered for that type.

That means you control it the same way you control everything else: by registering (or not
registering) a form for the type, and through `acl.yaml`. This mirrors how the side panel's "Add"
buttons already resolve their form.

Because a create form is an ordinary form definition, **a deliberately small form gives a small
inline modal** — if you want inline creation to ask for a title and nothing else, register a form
with just that field. The form definition *is* the "which fields" mechanism.

Two details worth knowing:

- If a type has only an `mode: edit` form, that form is used (an edit form works for creation when
  no entity id is supplied). Register a dedicated create form if you want different fields.
- `visible_when` inside the nested form is evaluated against the **nested** entity's own values.
  A condition cannot reference the parent form you opened it from.
- Nesting stops at one level: a relation field *inside* the inline modal offers link-existing only.

> **Changed:** `allow_create` and `create_form` on a form relation are no longer read — the rule
> above replaced them. Leaving them in your YAML is harmless (they are ignored), but you can delete
> them. If a `+ New` button disappeared after upgrading, the target type has no registered form.

### Reverse (incoming) Relations

#### How `direction` is resolved

`direction` may be omitted when the binding's entity type sits on exactly
**one** side of the relation — there is only one sensible reading, so rela
infers it:

- entity type is the relation's `from` → `outgoing`
- entity type is the relation's `to` → `incoming`

It must be written explicitly when the entity type is on **both** sides — a
self-referencing relation such as `depends-on` from `ticket` to `ticket`. There
`outgoing` and `incoming` are both valid and mean opposite things, so rela
refuses to guess and names the offending binding.

The same rule applies to **every** surface that takes a `direction:`:

| Surface | Anchored to |
| --- | --- |
| form relations (including wizard steps) | the form's `entity_type` |
| list columns and `filter_controls` | the list's `entity_type` |
| kanban `card.fields` and `filter_controls` | the kanban's `entity_type` |
| `caldav.dynamic.<name>` | the collection's `entity_type` (the **member**, not `driver_type` — the edge runs member→driver) |

> **Upgrading.** `direction` used to default to `outgoing` whenever it was
> absent, which silently bound the wrong side of a `to`-side relation. Run
> `rela migrate` to write explicit directions for the unambiguous bindings; it
> deliberately leaves self-referencing ones alone, and `rela validate` lists
> those for you to decide.

Relation types are directional in the metamodel: `implements` goes from `task` to `feature`.
Often you want to show the *inbound* side on the opposite entity's form — on the feature form,
"which tasks implement me?". Use `direction: incoming` to render a reverse widget:

```yaml
forms:
  feature:
    entity_type: feature
    relations:
      # Show tasks that implement this feature (incoming 'implements' edges).
      - relation: implements
        direction: incoming
        label: "Implemented by"
```

When `direction: incoming` is set:

- The widget reads edges via `GET /api/v1/{plural}/{id}/relations/{relType}?direction=incoming`.
- The target-type list comes from the relation's `from:`, not `to:`.
- Cardinality (single vs. multi) honors the relation's `max_incoming` instead of `max_outgoing`.
- Saving a new link writes the edge as `(peer) → {relType} → (current entity)`; the backend
  swaps from/to so the on-disk relation file stays canonical.
- Grouped responses from `GET /api/v1/{plural}/{id}/relations` surface incoming edges under
  the relation's `inverse:` name (see [metamodel.md](metamodel.md#inverse-relations)), e.g.
  `blocks` → `blockedBy`.

All form widgets (`select`, `multi-select`, `search`, `cards`) honor `direction: incoming`.

**Label collision:** The widget's section heading defaults to `label || relation`. If you
put two widgets with the same relation and no `label:` next to each other (one outgoing, one
incoming), they'll both render as "blocks". Always set an explicit `label:` on reverse
widgets — e.g. `"Blocked by"`.

### Relation Properties

When a relation type has `properties` defined in the metamodel, the `cards` widget is automatically
used and you can configure which properties are editable in the form:

```yaml
relations:
  - relation: blocks
    direction: outgoing
    target_type: ticket
    label: "Blocks"
    # widget: cards  (auto-selected because 'blocks' has properties in metamodel)
    properties:
      - property: reason
        label: "Block Reason"
        widget: text
```

| Field      | Type   | Description                       |
| ---------- | ------ | --------------------------------- |
| `property` | string | Relation property name            |
| `label`    | string | Display label                     |
| `widget`   | string | Input widget (`text`, `textarea`) |
| `required` | bool   | Must be filled                    |

### Help Modal

Every form displays a help icon (?) next to the title. Clicking it opens a modal with
documentation for the entity type, pulled from the metamodel:

- **Entity description**: The `description` field from the entity definition (supports markdown)
- **Properties**: All properties with their types and descriptions
- **Outgoing relations**: Relations from this entity to others, with cardinality constraints
- **Incoming relations**: Relations from other entities to this one, with cardinality constraints

Relations with minimum cardinality >= 1 are marked as "required" in the help modal, indicating
that at least one relation of that type must be created.

To populate the help modal, add descriptions to your metamodel:

```yaml
entities:
  ticket:
    label: Ticket
    description: |
      A ticket represents a unit of work to be completed.

      Use tickets for:
      - Bug reports
      - Feature requests
      - Tasks and chores
    properties:
      title:
        type: string
        required: true
        description: "Brief summary of the ticket"
      priority:
        type: priority
        description: "How urgently this ticket needs attention"

relations:
  blocks:
    label: blocks
    description: "Indicates this ticket must be resolved before another can proceed"
    from: [ticket]
    to: [ticket]
    min_outgoing: 0
    max_outgoing: 10
```

### Multi-step (wizard) forms

A form can be split into ordered, titled **steps** instead of a single page.
The user moves next/back, per-step validation gates "Next", and steps or fields
can appear/disappear based on earlier answers. Wizard mode is opt-in: a form
declares `steps:` **instead of** top-level `fields:`/`relations:` (a form may
not set both).

```yaml
forms:
  new_processing_record:
    entity_type: processing-record
    title: "New processing record"
    steps:
      - title: "Controller"
        fields:
          - property: controller_name
          - property: has_processors
            widget: checkbox

      - title: "Processor"
        # This whole step is skipped unless the toggle is on.
        visible_when: "form.has_processors == true"
        fields:
          - property: processor_name
            # Required only while the step is shown.
            required_when: "form.has_processors == true"
        relations:
          - relation: processed-by
            target_type: organisation

      - title: "Publish"
        fields:
          - property: published
```

Each step takes a `title`, an optional `description`, an optional `visible_when`
condition, and its own `fields:` / `relations:` (identical in shape to a
single-page form).

**Navigation and the URL.** The active step is encoded in the URL query as
`?step=N` (zero-based), so a refresh or a shared deep link returns to the same
step. An out-of-range or non-numeric `?step=` falls back to the first step.

**Validation.** "Next" validates only the current step's visible fields and
blocks progression while any are invalid. The final step's Submit re-validates
every visible step.

**Hidden branches are not saved on create.** If a step or field is hidden by a
`visible_when` that is false at submit time, its values are dropped from the
**created** entity — a branch the user revealed, filled, then abandoned never
persists.

**On edit, hiding does not delete.** A field that already had a stored value
keeps it when its branch hides, and gets it back when the branch is revealed.
Hiding is a presentation decision, not a delete. Use
[`clear_when_hidden`](#clear_when_hidden) to opt a field into clearing.

#### Condition expressions (`visible_when` / `required_when`)

`visible_when` (on a step, field, or relation) hides its target when the
expression is false. `required_when` (on a field) makes the field required only
when the expression is true. Both are boolean expressions evaluated in the
browser against the form's current values. The browser cannot read
relations, so `related(...)` is refused here at startup.

| Feature | Syntax |
| --- | --- |
| Reference a field | `form.<property>` (e.g. `form.status`) |
| String / number / boolean / nil literals | `'open'`, `3`, `true`, `false`, `nil` |
| Comparisons | `==` `!=` `<` `<=` `>` `>=`, regex `=~` |
| Boolean logic | `and`, `or`, `not`, parentheses |

Examples:

```yaml
visible_when: "form.kind == 'processor'"
visible_when: "form.country == 'US' or form.country == 'CA'"
required_when: "form.has_dpia != true"
visible_when: "form.q1 == 'no' or form.q2 == 'no'"   # WP248-style decision chain
```

Notes:

- Comparisons are **permissive**: a checkbox `true` matches both the boolean
  `true` and the string `'true'`; `form.count == 3` matches the number `3` or
  the string `'3'`.
- A condition may reference **any** earlier field; referencing a field the user
  hasn't reached yet simply reads as unset (`nil`).
- Conditions are a **UX affordance only** — the server re-validates every write
  regardless of what the wizard showed or hid. Do not rely on `required_when`
  as a server-side constraint.
- Bad conditions are caught by `rela validate`: a syntax error or a reference to
  a property that doesn't exist on the entity is reported at author time. (The
  check uses a slightly stricter grammar than the browser, so it may flag a few
  conditions the runtime would tolerate — treat every reported error as real.)

#### `clear_when_hidden`

Decides what happens to a field's **stored value** when its `visible_when` turns
false while editing. Per-field; the default keeps the value.

| Value | Behavior when the branch hides |
| --- | --- |
| `no` *(default)* | Keep the value. Hiding and revealing is lossless. |
| `yes` | Clear the value. |
| `confirm` | Ask first. On decline, the change that triggered the hide is abandoned too. |

```yaml
fields:
  - property: inkooproute
  - property: inschrijfdeadline
    visible_when: "form.inkooproute == 'aanbesteding'"
    # omit clear_when_hidden (or set `no`) to keep the date when the
    # branch hides; `yes` clears it; `confirm` asks first
```

Notes:

- Under the default, a hidden field's value is held client-side, so revealing
  the branch again restores it with no server round-trip — and the value was
  never deleted server-side, so it survives a reload too.
- This is per-**field** only. When a whole step hides, each of its fields
  honors its own setting.
- Setting it without a `visible_when` is a config error — it could never apply.
  A field on a conditional *step* is fine: the step can hide it.
- `confirm` is **not** simply `yes` with a prompt. Declining also abandons the
  edit that caused the hide, leaving the form exactly as it was — the dropdown
  snaps back. Nothing is written to the server either way until the user
  answers, so declining is a true no-op rather than an undo.
- `confirm` only prompts when something is at stake: a hidden field that is
  already empty is cleared without asking, so users are not trained to dismiss
  a dialog that never matters. One dialog names every affected field, rather
  than one dialog per field.
- Approving a `confirm` sends the triggering change and the clear in a **single**
  request, so the entity is never briefly left in a state the user did not
  approve.

## Lists

Lists display entities in a sortable, filterable table with optional create/edit actions.

### Basic List

```yaml
lists:
  all_tickets:
    entity_type: ticket
    title: "All Tickets"
    description: "View all tickets"

    columns:
      - property: title
        label: "Title"
        sortable: true
        link: true
      - property: status
        label: "Status"
        sortable: true
      - property: priority
        label: "Priority"
        sortable: true

    sort:
      property: priority
      direction: asc

    create_form: create_ticket
    edit_form: edit_ticket
    page_size: 25
```

> **Where does a click on a row go?** That's configured at entity-type
> granularity in the top-level `entity_views:` block — see
> [Entity Views](#entity-views) below. Per-list `detail_view` is no longer
> used; if you have one in an existing config, run `rela migrate` and it
> will be moved automatically.

### List Fields

| Field             | Type   | Description                                                 |
| ----------------- | ------ | ----------------------------------------------------------- |
| `entity_type`     | string | Entity type to list                                         |
| `title`           | string | List heading                                                |
| `header`          | string | Markdown rendered above the list (info/help; see below)     |
| `footer`          | string | Markdown rendered below the list                            |
| `description`     | string | Fallback for `header`; used only when `header` is unset      |
| `columns`         | list   | Column definitions                                          |
| `sort`            | object | Default sort order                                          |
| `filters`         | list   | Static filters (always applied)                             |
| `condition`       | string | Predicate expression ANDed with `filters` — for `or`, grouping and date arithmetic. See [Conditions](#conditions-condition) |
| `filter_controls` | list   | Interactive filter controls shown to the user               |
| `query_scope`     | string | Named query scope from the entity type's `query_scopes:`; `all` withdraws the type's `default` (see below) |
| `create_form`     | string | Form name for the "New" button                              |
| `create_world`    | string | World the "New" button opens its form in, when a new entity belongs in another world than the list shows |
| `edit_form`       | string | Form name for the row edit action                           |
| `page_size`       | int    | Rows per page (default: 25)                                 |
| `actions`         | list   | Action IDs available as keyboard shortcuts on selected rows |
| `export_render`   | string | Lua script under `scripts/` that renders this list for export instead of the built-in column table (see View Export & Transforms) |

#### Header and footer info regions

`header` and `footer` add admin-authored context to a list — a short
description, links to relevant guides, or a process note. Both accept Markdown
(GFM: headings, lists, links, emphasis, tables) and render as sanitized HTML
above and below the list respectively. Content is authored in `data-entry.yaml`
only; there is no in-app editor.

```yaml
lists:
  risicoregister:
    entity_type: risico
    title: "Risicoregister"
    header: |
      This register is **ISO 27001** scope. See the
      [scoring guide](/entity/guide-risk-scoring) for how KANS and IMPACT map to
      a level. New risks are reviewed weekly.
    footer: |
      _Questions? Contact the security officer._
    columns:
      - property: title
        sortable: true
```

Notes:

- Output is sanitized (DOMPurify), so raw HTML/scripts in the config cannot
  inject executable markup.
- Use standard Markdown links (`[text](/entity/ID)`) to point at other entities.
- `description` is a fallback for the top region: it was previously unused, so a
  config that already sets it now renders a header without a rewrite. When both
  `header` and `description` are set, `header` wins.

### Column Options

A column shows either a property value or the comma-separated titles of an entity's related
entities — set exactly one of `property` or `relation`.

| Field       | Type   | Description                                                                 |
| ----------- | ------ | --------------------------------------------------------------------------- |
| `property`  | string | Property name to display                                                    |
| `relation`  | string | Relation type whose targets are shown comma-separated                       |
| `direction` | string | Relation columns only: `"outgoing"` or `"incoming"`; inferred when omitted   |
| `label`     | string | Column header (defaults to property / relation name)                        |
| `sortable`  | bool   | Column can be sorted by clicking the header                                 |
| `link`      | bool   | Cell value links to the entity's detail page                                |

**Reverse relation column example** — on a feature list, show which tasks implement each row:

```yaml
columns:
  - property: title
    link: true
  - relation: implements
    direction: incoming
    label: "Implemented by"
```

### Static Filters

Apply filters that are always active (the user cannot remove them):

```yaml
filters:
  - property: status
    operator: "="
    value: open
```

| Field      | Type   | Description                              |
| ---------- | ------ | ---------------------------------------- |
| `property` | string | Property to filter on                    |
| `operator` | string | See operators below                      |
| `value`    | string | Value to compare against                 |

**Operators:**

| Operator   | Type support              | Behavior                                              |
| ---------- | ------------------------- | ----------------------------------------------------- |
| `=` / `==` | string, enum              | Exact match                                           |
| `!=`       | string, enum              | Not equal; supports comma-separated values (NOT IN)   |
| `~`        | string                    | Substring match (case-insensitive)                    |
| `<`, `<=`  | date, number              | Less than / less than or equal                        |
| `>`, `>=`  | date, number              | Greater than / greater than or equal                  |
| `in`       | string, enum              | Comma-separated list; matches any                     |

The ordering operators (`<`, `<=`, `>`, `>=`) compare with type-aware
parsing: dates are tried first (`YYYY-MM-DD`), then numbers, then string
comparison. If one side parses as a date or number and the other doesn't,
the comparison is **rejected** (the entity is excluded) — there is no
silent lexicographic fallback.

**Variable substitution in filter values:**

Filter values starting with `$` are reserved for variables. The following
date variables are supported:

| Variable     | Resolves to                          |
| ------------ | ------------------------------------ |
| `$today`     | Today's date in `YYYY-MM-DD` (UTC)   |
| `$tomorrow`  | Tomorrow's date                      |
| `$yesterday` | Yesterday's date                     |

Variables are evaluated in **UTC** for predictability across server
timezones. Variables work in single-value operators and in comma-separated
lists (`in`, `!=`):

```yaml
filters:
  # Show overdue tasks
  - property: due_date
    operator: "<="
    value: $today

  # Multiple variable tokens in a list
  - property: due_date
    operator: in
    value: "$yesterday,$today,$tomorrow"
```

To filter for a literal value that starts with `$`, you currently cannot
escape it — choose property values that don't start with `$`.

### Conditions (`condition:`)

`filters:` entries are always ANDed, so they can only express a plain
conjunction. When the membership rule needs **or**, grouping, negation of a
compound, or date arithmetic, use `condition:` — a predicate expression
evaluated per row and ANDed with `filters:`.

```yaml
lists:
  actieve_taken:
    entity_type: taak
    condition: >-
      entity.status ~= 'gereed'
      or (entity.afgerond_op ~= nil
          and days_between(today(), entity.afgerond_op) <= 2)
```

That rule — "every task that is not finished, plus ones finished in the last
two days" — is a disjunction, and cannot be written as `filters:` at all. A
filter on `afgerond_op` would also apply to the unfinished tasks and hide them.

It is operator config: the expression is never accepted from a request, so a
caller can only select among views you have declared.

A condition may use `related(...)` to filter on related entities, for example
`related(entity, 'implementedBy', { status = 'open' })`. It answers each
traversal once per page, and only entities the reader may see count. See
[Where `related(...)` works](metamodel.md#where-related-works).

> **Lists only, for now.** A `condition:` on a `kanbans:` entry is **rejected
> at startup** — the board still filters client-side, and a key that validated
> and then did nothing would be exactly the silent no-op this feature exists to
> remove. Use `filters:` on a board until this note goes away.

**A condition that does not compile is a startup error**, naming the view and
the offending attribute:

```text
view condition errors:
  lists["actieve_taken"]: condition does not compile against entity type
  "taak": predicate: compile error at line 1: unknown attribute "staus" on record
```

(One line in the real output; wrapped here to fit.)

That is deliberate — a filter that silently matches nothing is far harder to
notice than a server that refuses to start.

#### It is a different language from `filters:`

`condition:` is a Lua expression subset (the same language as automation
`condition:` and next-action `condition:` — see
[metamodel.md](metamodel.md)). `filters:` is the filter DSL. They sit next to
each other in the same block and **three differences will bite you**:

| | `filters:` | `condition:` |
|---|---|---|
| not-equal | `!=` | **`~=`** (`!=` is a parse error) |
| unset property, not-equal | **excluded** | **included** |
| value quoting | bare | `'single quotes'` |

The first is caught at startup. The second is not, so read it carefully:

```yaml
filters:
  - property: status
    operator: "!="        # a task with NO status is HIDDEN
    value: gereed
condition: "entity.status ~= 'gereed'"   # a task with NO status is SHOWN
```

Both are correct for their own language. A filter names a population, and a
task with no status is not in the "status is something other than gereed"
population. The expression language follows Lua, where `nil` equals nothing,
so `nil ~= 'gereed'` is true.

#### `days_between` argument order

`days_between(a, b)` counts days **from b to a**. So an *age* is:

```lua
days_between(today(), entity.afgerond_op)    -- 3 = finished three days ago
```

Writing the arguments the other way round is the trap, because it produces no
error:

```lua
days_between(entity.afgerond_op, today()) <= 2   -- WRONG: matches everything
```

For a past date that yields a **negative** number, and `-10 <= 2` is true — so
every old row matches and the filter appears to do nothing.

#### An unset property is an error, not `false`

A date function applied to a property that is not set raises an evaluation
error, which fails the request rather than silently dropping the row. Guard it:

```lua
entity.afgerond_op ~= nil and days_between(today(), entity.afgerond_op) <= 2
```

The `and` short-circuits, so the guard is enough. Note the shorthand
`entity.afgerond_op and …` does **not** compile — the language requires a
boolean on the left of `and`.

#### Available functions

`today()`, `days_between()`, `date_add()`, `rrule_next()`, `match()`,
`regex()`, `contains()`, `len()`. Signatures are in
[metamodel.md](metamodel.md).

A condition is evaluated for the signed-in principal, so it may also use
`current_user.id`, `is_current_user(entity.owner)` and
`has_current_user(entity.watchers)` — useful for "assigned to me or
unassigned", which is likewise a disjunction:

```yaml
condition: "is_current_user(entity.assignee) or entity.assignee == nil"
```

A condition never widens a view: it is applied on top of the ACL, so it can
only remove rows the principal could already see.

### Query Scopes

A static filter is a conjunction: every entry must match. When the membership
rule needs an OR, a negation, or a comparison against today's date, declare a
**query scope** on the entity type in `schema.yaml` and name it here:

```yaml
lists:
  archief:
    entity_type: taak
    query_scope: archief
  alles:
    entity_type: taak
    query_scope: all        # withdraw the type's default
  open_werk:
    entity_type: taak       # no query_scope: the type's `default` applies
```

The scopes themselves are declared per entity type — see
[Query Scopes](metamodel.md#query-scopes) in the Metamodel Reference for the
expression language, the implicit `all`, and the rules about identity. Three
things matter from this side:

- **A type's `default` scope applies to a view that names no scope.** This is
  the point of declaring one: a rule like "hide archived tasks" is written
  once and every list and board of that type inherits it.
- **`query_scope: all` withdraws it**, and always resolves — even for a type
  that declares no scopes at all.
- **A name the type does not declare refuses at config load**, listing the
  names it does declare. There is no fallback to unfiltered.

Scopes and static filters compose: both narrow, and both apply. A scope is the
right place for a rule that belongs to the domain ("a task is active until it
is done"), a static filter for one that belongs to the screen ("this board is
for the infra team").

**A query scope is not access control.** It decides what a screen shows, not
what a principal may read — anyone who can call the API can ask for
`query_scope: all`. Use `scope_grants:` in `acl.yaml` to restrict what a role
can see.

#### Selecting a scope over the API

The list API takes `?query_scope=<name>`, the same shape as `?world=`:

```http
GET /api/v1/taken?query_scope=archief
GET /api/v1/taken?query_scope=all
```

Omitting the parameter applies the entity type's `default` scope, if it
declares one. A name the type does not declare is a `400` naming the scope, for
the same reason an undeclared world is: scope names are configuration in your
repository, not secrets. Repeating the parameter
(`?query_scope=all&query_scope=archief`) is also refused rather than silently
taking the first, since the request asked two different things.

### Filter Controls

Interactive filters shown above the table. A control filters on either a
**property** or a **relation** (set exactly one):

```yaml
filter_controls:
  - property: status
    widget: multi-select
  - property: priority
    widget: select
  - property: assignee
    widget: search
  - relation: verantwoordelijk_voor
    direction: incoming
    label: Verantwoordelijke
```

| Field       | Type   | Description                                                     |
| ----------- | ------ | -------------------------------------------------------------- |
| `property`  | string | Property to filter on                                          |
| `relation`  | string | Relation to filter on (mutually exclusive with `property`)     |
| `direction` | string | For `relation`: `"outgoing"` or `"incoming"`; inferred when omitted |
| `widget`    | string | For `property`: `"select"`, `"multi-select"`, or `"search"`    |
| `label`     | string | Optional display label override                                |

**Relation filter controls** render as a **target selector** populated with the
display titles of the relation's target entities — a plain `<select>` for a
small set, upgrading to a typeahead combobox above ~10 options. `direction:
incoming` pulls candidates from the relation's source types (`from`); `outgoing`
(default) from its target types (`to`). The selected value the filter matches on
is the target's **display title** (honoring each type's `display_property`), not
its ID.

Notes:

- Two targets that resolve to the same display title collapse to one option and
  the filter matches both (title-based matching).
- The candidate list is fetched from the target types' entities; a type with
  more than ~100 entities has its option set truncated.
- A relation whose name is not a plain identifier (e.g. contains a hyphen)
  cannot be deep-linked as a filter (the URL parser only accepts
  `[a-zA-Z_][a-zA-Z0-9_]*` filter keys).

### URL Sync for Filters

Interactive filter selections are mirrored into the page's URL query string so
lists are deep-linkable and survive browser back/forward. The format is
bracketed:

```text
/list/all_tasks?filter[status]=open
/list/all_tasks?filter[due_date][lte]=$today
/list/all_tasks?filter[tags][in][]=urgent&filter[tags][in][]=blocker
```

Rules:

- The implicit equality form (`filter[prop]=value`) is the most concise; it
  matches the API's default `eq` operator.
- Operator suffixes (`[lte]`, `[gt]`, `[contains]`, `[in]`, …) follow the same
  names as the REST API operators. The full list is `eq`, `ne`, `contains`,
  `in`, `lt`, `lte`, `gt`, `gte` — see the ["Static Filters"](#static-filters)
  section above and the `applyV1Filters` source in
  `internal/dataentry/api_v1.go` for semantics.
- Unknown operators (typos like `[equals]`) and malformed filter keys
  **reject the request with HTTP 400** (`invalid_filter`, naming the bad
  operator). The earlier skip-with-a-warning behavior still returned the
  unfiltered superset — silently bypassing the filter it claimed to
  fail-closed on — and let a config typo hide for months.
- Multi-value filters use the repeated array form (`filter[prop][in][]=a&…`).
  Only `in` and `ne` join all repeated values; other operators take
  last-write-wins if a key appears multiple times.
- Static `filters:` entries (the always-active list config above) take
  precedence: a URL filter on the same property is dropped with a console
  warning rather than silently overriding the locked scope. **Important:**
  the lock is whole-property granularity, not per-operator — a static
  `filter[date][gte]=2024-01-01` blocks *any* URL filter on `date`,
  including `filter[date][lte]`. If you need a range combined with a static
  lower bound, define both bounds in `data-entry.yaml` rather than via URL.
- Text-input filters debounce at 250ms — typing into a search filter only
  fires one backend request after you stop typing, not one per keystroke.
- Clearing all filters from the FilterBar removes every `filter[*]` param
  from the URL while preserving non-filter params (`from`, `sort`, `page`,
  `scope`).

### Sort Configuration

Sort supports multiple criteria as a list. The first entry is the primary sort key:

```yaml
sort:
  - property: priority
    direction: desc
  - property: due_date
    direction: asc   # "asc" (default) or "desc"
```

You can also sort a list by the virtual property `id` (entity ID).

**Enum properties sort in their declared order.** A `status` whose schema lists
`values: [backlog, ready, in-progress, done]` sorts in that order rather than
alphabetically, so writing the enum in workflow order gives the right sort for
free. A value the schema no longer declares sorts after every declared one.
This applies to any property whose type declares `values:`, including a custom
type shared by several properties.

Everything else compares as text, byte by byte — so sorting is case-sensitive
(`Zebra` before `apple`) and not numeric (`item10` before `item9`). A date
declaring a non-default `format:` therefore sorts lexically, not
chronologically; store dates in the default ISO form if you need chronological
order.

That rule is what every storage backend can express, which is why it is the
rule: a sort the database cannot perform would force the whole entity type to
be loaded and sorted in memory for every page.

An entity that lacks the sort property sorts as if it held the largest value:
after every entity that has one when ascending, before them when descending.
Ties break by entity ID ascending, in both directions, so paging is stable.

On the PostgreSQL backend a sorted list page is served straight from an index
when the sort keys are string-shaped properties, including enum keys (see the
PostgreSQL guide on derived list indexes).

If no sort is configured, the list falls back to the entity type's `default_sort` from the metamodel,
or sorts by ID ascending.

The search bar also supports `sort:` clauses (see [Query Syntax](#query-syntax) below).

> **Migration**: If your config uses the old single-object format (`sort: {property: ..., direction: ...}`),
> run `rela migrate` to convert it to the list format.

## Views

Views define read-only detail pages that traverse the entity graph to display related data,
adapted for rendering as HTML sections.

### Basic View

```yaml
views:
  ticket_report:
    title: "Ticket Report"
    entry:
      type: ticket

    traverse:
      - from: entry
        follow: blocks
        collect_as: blocked_tickets
      - from: entry
        follow_incoming: blocks
        collect_as: blocked_by
      - from: entry
        follow: tagged
        collect_as: labels

    sections:
      - heading: "Ticket"
        source: entry
        display: properties
        fields:
          - property: status
          - property: priority
          - property: assignee

      - source: entry
        display: content

      - heading: "Blocks"
        source: blocked_tickets
        display: table
        columns:
          - property: title
            label: "Title"
            link: true
          - property: status
            label: "Status"
        empty_message: "No blocked tickets"
```

### View Fields

| Field      | Type   | Description                                    |
| ---------- | ------ | ---------------------------------------------- |
| `title`    | string | Page heading                                   |
| `entry`    | object | Entry entity type                              |
| `traverse` | list   | Graph traversal rules                          |
| `sections` | list   | Display sections                               |

### Entry

```yaml
entry:
  type: ticket   # Entity type of the entry entity
```

When a user opens a view, the entry entity is determined by the URL. For example,
clicking a list row whose `entity_type` has `entity_views.ticket.detail_view: ticket_report`
opens the view for that specific ticket entity.

### Traverse Rules

Traverse rules collect related entities into named collections:

```yaml
traverse:
  # Follow outgoing relations
  - from: entry
    follow: blocks
    collect_as: blocked_tickets

  # Follow incoming relations
  - from: entry
    follow_incoming: tagged
    collect_as: labels

  # Chain from a previous collection
  - from: blocked_tickets
    follow: tagged
    collect_as: blocked_labels

  # Recursive traversal
  - from: entry
    follow: dependsOn
    recursive: true
    max_depth: 5
    collect_as: dependencies

  # Filter results with where clause
  - from: entry
    follow_incoming: partOf
    collect_as: functions
    where: "type = function"

  # Filter by property value
  - from: entry
    follow_incoming: partOf
    collect_as: active_items
    where: "status = active"
```

| Field             | Type   | Description                                        |
| ----------------- | ------ | -------------------------------------------------- |
| `from`            | string | Source: `"entry"` or a collection name              |
| `follow`          | string | Outgoing relation type to follow                   |
| `follow_incoming` | string | Incoming relation type to follow (reverse)         |
| `collect_as`      | string | Name for the collected entities                    |
| `recursive`       | bool   | Follow the relation transitively                   |
| `max_depth`       | int    | Maximum recursion depth                            |
| `where`           | string | Filter expression to select matching entities      |

#### Where Clause Syntax

The `where` clause filters traversed entities using simple expressions:

```text
property = value    # Equality
property != value   # Inequality
```

The special pseudo-property `type` matches the entity type:

```yaml
where: "type = function"     # Only function entities
where: "type != component"   # Everything except components
```

Regular properties use metamodel-aware matching:

```yaml
where: "status = active"     # Match status property
where: "priority != low"     # Exclude low priority
```

#### `where:` under a world

When a view is requested with `?world=<name>`, a `where:` clause is evaluated
against the face that world resolved, not against some other face. Under
a world that selects `published`, `where: "status = active"` reads the
published face's `status`. A page that rendered published content while
filtering on draft values would contradict itself, so the filter follows the
page. Filters on the `type` pseudo-property are unaffected, because an entity's
type is the same on every face.

If a where clause is invalid or a property doesn't exist, the filter is silently
skipped and all entities are returned (fail-open for robustness).

> **Note:** that fail-open behaviour is a known defect, not a design goal — a
> mistyped `where:` silently *widens* a clause whose purpose is to narrow. It is
> tracked as BUG-WHEREWIDE and will become a load-time error, so a bad `where:`
> is refused when the config parses rather than ignored at render time.

### Sections

Sections define how collected entities are rendered on the page:

```yaml
sections:
  - heading: "Properties"
    source: entry
    display: properties
    fields:
      - property: status
      - property: priority
        label: "Priority Level"

  - heading: "Description"
    source: entry
    display: content

  - heading: "Related Items"
    source: related_items
    display: table
    columns:
      - property: title
        label: "Title"
        link: true
      - property: status
        label: "Status"
    empty_message: "No related items found"
```

| Field           | Type   | Description                                             |
| --------------- | ------ | ------------------------------------------------------- |
| `heading`       | string | Section heading (optional; omit for no heading)         |
| `source`        | string | `"entry"` or a traverse collection name                 |
| `display`       | string | Display mode (see below)                                |
| `render`        | string | `display` (default) or `input` — see Field Render Modes |
| `fields`        | list   | Properties to show (`properties`, `content`, `cards`, `list` modes) |
| `columns`       | list   | Column definitions (`table` mode; `nested` uses the two keys below) |
| `children`      | string | Collection to nest under each row (`nested` mode; required) |
| `parent_columns`| map    | `nested` mode: columns for the source level, keyed by entity type |
| `child_columns` | map    | `nested` mode: columns for the child level, keyed by entity type |
| `sort`          | list   | Sort keys for a flat section (`list`, `table`, `cards`) |
| `parent_sort`   | list   | `nested` mode: sort keys for the source level            |
| `child_sort`    | list   | `nested` mode: sort keys for the child level             |
| `group_by`      | string | Property to group entities by                           |
| `empty_message` | string | Text shown when the collection is empty                 |
| `link`          | bool   | Link entity titles to their detail pages                |
| `create`        | map    | Offer a "create related entity" button (see below)      |

Each entry under `fields:` takes:

| Field      | Type   | Description                                                  |
| ---------- | ------ | ------------------------------------------------------------ |
| `property` | string | Property name                                                |
| `label`    | string | Display label (defaults to the raw property name)            |
| `span`     | int    | Width on the 12-column grid (1-12; omit for full width)      |
| `render`   | string | `display` or `input`; overrides the section's `render`        |
| `widget`   | string | Which widget renders this property (see Widget Overrides)    |

### Creating related entities from a section

By default the entity detail page is **read-only**: it shows an entity and its
neighbours, and every change happens through a form. A section can opt out of
that and offer a button that creates a new related entity, already linked to the
entity you are looking at.

```yaml
views:
  epic:
    entry:
      type: epic
    traverse:
      - from: entry
        follow: has-task
        collect_as: tasks
    sections:
      - heading: Tasks
        source: tasks
        display: table
        create:
          in: [section, header]   # default: [section]
          flow: modal             # default: modal
          types:
            task: { template: bugfix }
            bug: { template: regression }
```

| Field   | Type | Description                                                        |
| ------- | ---- | ------------------------------------------------------------------ |
| `in`    | list | Where the button appears: `section`, `header`, or both. Default `[section]` |
| `flow`  | str  | `modal` (default) keeps you on the page; `page` opens the full form |
| `types` | map  | Per-entity-type options, keyed by type (see below)                  |

Omit `create:` entirely to keep a section read-only. There is no `create: false`
— absence already means that, and a second spelling of one meaning is refused at
load.

**Which types are offered is derived, not configured.** A type appears only when
all three hold: the section's relation can reach it, a form exists that can
create it, and the user has permission to create it. So there is no list of
types to keep in step with your ACL — removing someone's `create` grant removes
their button. One reachable type renders a direct button (`+ Task`); several
render a short menu.

The button follows the grant, but it is **not** what enforces it. The server
re-authorizes the entity and the relation when the form is submitted, so a user
who edits the request by hand gets whatever their own permissions allow. That is
the same answer the create form would give them. Treat the button as a
convenience, not as the access-control boundary.

**`flow` is per section, not per type.** Which flow feels right depends on the
form, not on the entity type, and one type opening a modal while its sibling
navigates reads as a bug rather than as configuration.

- `modal` opens the create form in a dialog and refreshes the page when it
  succeeds. Good for short forms.
- `page` navigates to the full create form and returns you to the originating
  entity on submit. Good for long or multi-step forms.

**`types` is keyed by entity type** because a template variant only exists per
type — a relation reaching both `task` and `bug` has no single meaningful
template. A type you do not list still gets a button; it just opens with the
form's own default template.

| Field      | Type | Description                                            |
| ---------- | ---- | ------------------------------------------------------ |
| `template` | str  | Template variant to preselect (`templates/entities/<type>--<variant>.md`) |

When `in:` includes `header`, the section also contributes an entry to a menu at
the top of the page. That menu is the union of the sections that opted in. It is
deduped by relation, so two sections over one relation produce a single entry.

A section can only offer this if exactly one relation filled it. A section built
from a `recursive:` traverse rule, or from a rule that starts at another
collection rather than `entry`, has no single entity to link the new one to;
declaring `create:` on one is refused at config load rather than silently
producing no button.

### Field Render Modes

A view section field renders as a **view-oriented display value** by default.
Set `render: input` to opt a field into inline editing:

```yaml
sections:
  - heading: "Properties"
    source: entry
    display: properties
    render: input          # section-wide default for its fields
    fields:
      - property: status   # inherits `input`
      - property: id
        render: display    # ...but this one overrides back to display
```

| Value     | Effect                                                            |
| --------- | ----------------------------------------------------------------- |
| `display` | (default) A read-oriented value. Not a disabled input — no control |
| `input`   | An editable widget that saves on change (auto-save)                |

Resolution is field-first: a field's own `render:` wins, else the section's,
else `display`. It is resolved server-side, so the value the SPA receives is
already effective.

> **Breaking change.** Before this, inline editing was implied by write
> permission. Sections that want it must now say so with `render: input`.

`render: input` **cannot grant editability.** Effective editability is
`render: input` AND the ACL permitting the write, so `input` on a field the
caller may not edit still renders as display. Config can only narrow.

A field belonging to a state machine renders its transition control instead of
an ordinary widget when `render: input`; on `render: display` it renders the
plain value.

### Widget Overrides

By default the widget is chosen from the property's declared type — `boolean`
gets a checkbox, `date` a date picker, an enum a dropdown. Set `widget:` to
choose a different registered widget:

```yaml
fields:
  - property: done
    widget: checkbox     # the payoff case: click to tick, with render: input
  - property: notes
    widget: textarea     # a long string, not a single-line input
```

| Widget         | Accepts                       |
| -------------- | ----------------------------- |
| `text`         | string                        |
| `textarea`     | string                        |
| `number`       | integer                       |
| `checkbox`     | boolean                       |
| `date`         | date                          |
| `datetime`     | datetime                      |
| `select`       | enum, string, custom types    |
| `multi-select` | enum, string (list values)    |
| `rrule`        | rrule                         |
| `file`         | file                          |

Rules worth knowing:

- **Field-level only.** There is no section-wide `widget:`, unlike `render:`.
  A widget is inherently per-property: a section-level one would be a config
  error on every field whose type didn't match, which the author would then
  have to override back field by field.
- **Omitting it changes nothing** — the type default applies exactly as before.
- **A mismatch is a config-load error.** `widget: checkbox` on a `date`
  property fails at startup, naming the property, its type, and what the widget
  does accept.
- **`widget: file` only works on `display: properties`.** Card and list rows
  are not given attachment data, so a file widget there would have nothing to
  show.
- **It pairs with `render:`.** A checkbox you cannot click is just an icon, so
  the interactive case needs `render: input` too.
- **On a property the schema doesn't declare, the override is ignored** and a
  warning is logged at startup. Such a field has no type to validate against.
- **On a state-machine field with `render: input`**, the transition control
  takes precedence and the widget is inert; with `render: display` the widget
  is used.

### Display Modes

| Mode         | Description                                                     |
| ------------ | --------------------------------------------------------------- |
| `properties` | Key-value pairs rendered as a definition list                   |
| `content`    | Markdown body of the entity rendered as HTML                    |
| `table`      | Tabular layout with configurable columns (like a mini-list)     |
| `cards`      | Card layout showing each entity with selected property badges   |
| `list`       | Simple bulleted list of entity titles with optional fields      |
| `nested`     | Two-level parent→child tree; each row expands to its children   |

**`properties`** is best for the entry entity's metadata. **`content`** renders the markdown body.
**`table`** works well for collections with many items. **`cards`** provides a visual layout for
smaller collections. **`list`** is the most compact.

#### `nested` — parent→child trees

Renders one expandable row per entity in `source:`, with the entities from
`children:` nested underneath. Use it for containment hierarchies —
project → epic → task, or any `has-*` chain.

```yaml
views:
  project:
    entry:
      type: project
    traverse:
      - {from: entry, follow: has-epic, collect_as: epics}
      - {from: epics, follow: has-task, collect_as: work}
    sections:
      - heading: Epics
        source: epics
        display: nested
        children: work
        parent_columns:
          epic:
            - {property: status}
            - {property: owner}
        child_columns:
          task:
            - {property: status}
            - {property: due}
          bug:
            - {property: status}
            - {property: severity}
```

The nesting comes from the **traverse rules**, not from `children:` alone: the
rule that collects the child bucket must walk `from:` the section's `source:`.
That is what associates a task with its own epic, so config load rejects a
`children:` whose rule starts anywhere else — it would render rows with no
children rather than an error.

##### Columns are per level, then per type

`display: nested` does not take the flat `columns:` list the other modes use.
It takes `parent_columns` and `child_columns`, each a map keyed by entity type.
Both axes matter:

- **Per level**, because a group row and a detail row want different things —
  and with a self-referential containment (`project has-project`) the two levels
  are the *same* type, which a type-keyed map alone could not distinguish.
- **Per type**, because a relation may reach several types. `has-task: {to:
  [task, bug]}` is ordinary rela, and a bug wants `severity` where a task wants
  `due`.

A type absent from its level's map renders its title and id only — adding a
type to a relation never breaks an existing view. A column naming a property
the type does not declare is a load error, as is declaring columns for a type
the level can never hold.

##### Sorting is per level too

For the same reason, a nested section takes `parent_sort:` and `child_sort:`
rather than one `sort:` — with two collections at two levels, a single key
could only pick a level by convention. Flat sections (`list`, `table`, `cards`)
take a plain `sort:`. Using the wrong key for the display is a load error, not
a silent no-op.

```yaml
sections:
  - heading: Epics
    source: epics
    display: nested
    children: tasks
    parent_sort:
      - property: status          # declared enum order, e.g. planned before done
    child_sort:
      - property: status
      - property: due
        direction: asc
```

Both take the same shape as a list's `sort:`, and follow the same rules —
declared order for enums, byte-wise text otherwise (see
[Sort Configuration](#sort-configuration)).

**Sorting happens before the row caps.** A nested section emits at most 2000
rows in total and at most 25 children per parent, so without a sort the rows
that survive are whichever the traversal reached first. With one, they are the
top-sorted rows — which is usually the point of asking for a sort at all.

A section that declares no sort keeps the order the traversal produced, so
adding these keys changes nothing until you use them.

##### Other behaviours

- Children render in traversal order unless the section declares `child_sort:`
  (see above); parents likewise honour `parent_sort:`.
- `recursive: true` on the children's traverse rule is refused: this mode renders
  exactly two levels, and the recursive walk does not record which parent each
  node came from.
- Rows are expanded by default, and large trees are capped. A row whose children
  were withheld reports `hasMoreChildren` with the true `childCount`, and the
  section sets `truncated` — so a capped row is never mistaken for a childless
  one.
- Values render through the same widgets every other surface uses, so an enum
  arrives as a coloured badge and a date is formatted.
- Rows carry no markdown body, like `table` and unlike `cards`.
- Not available in a form's `side_panel:`, which has no wire field for a tree.

## Entity Views

`entity_views` declares the canonical detail view for each entity type — the
view that opens when a user clicks on an entity reference anywhere in the
data-entry app (a list row, a custom view's `display: list` section, a card,
a table cell). Without an entry, the SPA falls back to a generic
`/entity/<type>/<id>` page.

```yaml
entity_views:
  ticket:
    detail_view: ticket_detail
  decision:
    detail_view: decision_detail
```

### Fields

| Field         | Type    | Description                                                       |
| ------------- | ------- | ----------------------------------------------------------------- |
| `detail_view` | string  | View name (must reference a key under `views:`) used for entities of this type |
| `duplicate`   | mapping | Narrows what a duplicate of this type copies — see below |

An entry may carry either field or both. An entry that declares neither is a
load error, since it configures nothing.

### Duplicating an entity

The entity detail page offers a **Duplicate** action for every type you may
create. It opens a dialog listing the entity's relation types in both
directions with a checkbox each, then a create form prefilled from the source.
Nothing is written until you submit, so you can edit the copy — in particular a
`unique:` title that would otherwise collide — before it exists.

By default the copy carries every property you can see, plus the markdown body.
To narrow that, name the properties to carry:

```yaml
entity_views:
  invoice:
    detail_view: invoice_detail
    duplicate:
      properties: [customer, currency, line_items]
```

A property outside the list is not blanked; it takes its normal default from
the metamodel or the template. Omit the `duplicate:` block to carry everything;
an empty `properties:` list is refused at load, because omitting the block is
already how a type opts out of narrowing.

The list narrows **properties only** — the markdown body always carries. There
is no way to exclude it, so a type whose body you would not want copied should
not be narrowed but reconsidered: the user can clear the body in the create form
before submitting.

Three kinds of property never carry, whatever you configure, because copying
them produces a broken record rather than a narrower one:

- **Attachments** (`file` properties). The bytes are stored under the source
  entity's id, so the copy would point at a file that is not its own.
- **State-machine properties.** Creating an entity is an *entry*, not a
  transition, so the copy starts at the state machine's initial value rather
  than wherever the source had reached.
- **Properties hidden from you** by field-level `visible:` rules. You never
  received the value, so the copy cannot carry it.

The dialog names anything it could not copy, so an incomplete duplicate is
always visible as one.

Relations follow the checkboxes: outgoing types are selected by default, and
incoming ones ("something else points at me") are offered but not preselected.
Only relations whose other end you may read are listed, so the counts shown are
per-viewer and may be lower than the raw graph.

### How navigation resolves

For each clickable entity reference, the SPA picks the destination URL using
the following priority:

1. A column-level `link:` on a list (server-resolved, e.g. `link: detail` or
   `link: document/<name>`).
2. `entity_views.<type>.detail_view` (the canonical detail view for the
   type) → `/view/<viewId>/<id>`.
3. Fallback: `/entity/<type>/<id>` (a generic detail page).

This means you only configure the destination *once* per entity type, and
every consumer (lists, view sections, table rows) routes consistently.

### Migration from list-level `detail_view`

Earlier versions accepted `detail_view` directly on each list. That field is
now deprecated; the canonical home is `entity_views.<type>.detail_view`. Run
`rela migrate` to lift existing list-level values into the new section
automatically. If two lists for the same entity type set conflicting
`detail_view` values, the migration leaves them in place — resolve the
conflict by hand and run `rela migrate` again.

## Dashboard

The dashboard is an optional overview page that displays a grid of query-driven cards. Each card
runs a search query against your entities and renders the results as a count, a property breakdown,
or a mini-table. The query syntax is the same as the search page: `type:`, `prop:`, `status:`,
and free text.

### Basic Dashboard

```yaml
dashboard:
  title: "Dashboard"
  description: "Project overview"
  cards:
    - title: "Open Tickets"
      query: "type:ticket status:open"
      display: count

    - title: "By Priority"
      query: "type:ticket"
      display: breakdown
      group_by: priority

    - title: "Critical Issues"
      query: "type:ticket prop:priority=critical"
      display: table
      columns:
        - property: title
          label: "Title"
          link: true
        - property: status
          label: "Status"
        - property: assignee
          label: "Assignee"
      sort:
        property: status
        direction: asc
      limit: 10
```

### Dashboard Fields

| Field         | Type   | Description                            |
| ------------- | ------ | -------------------------------------- |
| `title`       | string | Page heading                           |
| `description` | string | Subtitle shown below the heading       |
| `cards`       | list   | Card definitions                       |

### Card Options

| Field     | Type   | Description                                                        |
| --------- | ------ | ------------------------------------------------------------------ |
| `title`   | string | Card heading                                                       |
| `query`   | string | Search query (same syntax as the search page)                      |
| `display` | string | Display mode: `"count"`, `"breakdown"`, or `"table"`               |
| `group_by`| string | Property to group by (`breakdown` mode only)                       |
| `columns` | list   | Column definitions (`table` mode only, same format as list columns) |
| `sort`    | object | Sort order (`table` mode only)                                     |
| `limit`   | int    | Maximum rows to display (`table` mode only)                        |
| `permission` | string | Hide this card from principals who do not hold the named ACL permission |

### Hiding cards a user cannot use (`permission`)

A card carrying a `permission:` is omitted from the dashboard for principals
who do not hold it:

```yaml
dashboard:
  cards:
    - title: "Open Tickets"        # everyone sees this
      query: "type:ticket status:open"
      display: count

    - title: "Audit Log"           # only holders of admin:read
      query: "type:audit-entry"
      display: table
      permission: admin:read
      columns:
        - property: title
```

**This is a UX filter, not an access control.** Card data already flows through
the ACL-scoped search path, so a principal who cannot read the matching
entities already sees a card reading `0` or an empty table — `permission:`
just stops rendering that useless tile. Hiding a card grants and revokes
nothing: its query typed into the search page returns exactly the rows it
always did. Nor does it conceal configuration — `/api/v1/_config` still serves
the whole `dashboard:` block, `permission:` values included, to every
principal. Only `/api/v1/_dashboard` is filtered.

With no `acl.yaml`, and under `--read-only`, gated cards are **shown**: neither
configures a permission model, so there is nothing to check. (`--read-only`
restricts writes only; hiding read surfaces there would hide them from
everyone, since the flag carries no identity.)

When every card is filtered out, the dashboard renders an empty state — the
same one shown when no cards are configured at all.

> **Gotcha: permission names are not validated.** A typo like `admin:raed`
> yields a card **nobody can see**, with no error and no warning at startup.
> If a card has vanished, check it against the `permissions:` list on your
> roles in `acl.yaml` first. This applies equally to `permission:` on
> commands, documents, and navigation entries.

### Display Modes

**`count`** shows a single large number — the count of entities matching the query. Use this for
quick status indicators like "5 open tickets" or "3 overdue items".

**`breakdown`** groups matching entities by a property and shows each value with a count and a
horizontal bar. The property should be an enum or custom type so values can be styled with badge
colors from `styles`. For example, grouping by `status` shows "open: 2, in-progress: 1, closed: 1"
with colored bars.

**`table`** shows matching entities as a compact table. It supports the same `columns` format as
list definitions (with `property`, `label`, `sortable`, `link`), plus `sort` and `limit` to control
ordering and row count.

### Query Syntax

Cards use the same search query syntax available on the search page:

| Syntax                   | Example                           | Description                      |
| ------------------------ | --------------------------------- | -------------------------------- |
| `type:<entity_type>`     | `type:ticket`                     | Filter by entity type            |
| `type:<a>,<b>`           | `type:ticket,category`            | Multiple entity types            |
| `status:<value>`         | `status:open`                     | Shortcut for `prop:status=value` |
| `prop:<name>=<value>`    | `prop:priority=critical`          | Property equals value            |
| `prop:<name>!=<value>`   | `prop:assignee!=`                 | Property not equal               |
| `prop:<name>=~<regex>`   | `prop:title=~auth.*`              | Regex match                      |
| `prop:<name><<value>`    | `prop:due_date<2025-06-01`        | Less than (dates, numbers)       |
| `sort:<property>`        | `sort:priority`                   | Sort ascending by property       |
| `sort:<property>:desc`   | `sort:priority:desc`              | Sort descending by property      |
| `sort:id` / `sort:modified` | `sort:modified:desc`           | Sort by ID or modification time  |
| free text                | `authentication`                  | Substring match across all fields|
| `"quoted phrase"`        | `"REST API"`                      | Exact phrase match               |

Multiple terms are combined with AND logic. For example,
`type:ticket status:open prop:priority=critical` matches tickets that are both open and critical.

Every card includes a link icon that opens the same query on the search page for further
exploration.

## Next actions

An optional advisory layer: operator-declared rules that derive **one**
suggested follow-up from graph state and surface it in the UI.

It is deliberately **not** a task queue:

- **Advisory.** A hint, not a demand — things a user *could* do, not *should*.
  That is what separates it from `analyze`, which has an opinion about
  correctness.
- **One at a time.** Never a list. The aim is a companion that does not
  overload, and there is no "show me all 12" surface to grow into.
- **Good, not optimal.** Surfacing one of several good next actions is the
  goal; avoiding a bad one is the bar.

### Bands

Bands are your priority vocabulary. Declare them in order — **list order is
priority order**, highest first — and every source names one:

```yaml
next_action_bands:
  - id: blocking
    label: "Someone is waiting"
    prominence: banner
  - id: stalled
    prominence: notice
  - id: ambient
    label: "Nothing owed"
    prominence: statusbar
```

Bands rather than numeric priorities because per-source numbers do not
compose: one source returning 90 and another returning 7 on a different scale
is arbitrary ordering wearing the costume of ranking. With bands, "why is this
on top?" always has a one-sentence answer.

The engine evaluates bands in order and **stops at the first one with
something to say**, so a typical page runs one or two queries rather than all
of them.

#### Prominence

How much a band interrupts. The levels differ in what the user must do to
clear it, not in decoration:

| Value | Behaviour | Use for |
|-------|-----------|---------|
| `banner` | A bordered, accented block at the top of the page. Must be dealt with. | Onboarding; work someone else is blocked on |
| `notice` | One quiet line in the same position. Easy to read past. | Worth saying once a visit, no urgency |
| `statusbar` | A chip in the status bar; click to expand. You must go looking. | Ongoing minor stuff — true most of the time, urgent none of it |

`statusbar` is the **default**: a band that has not declared a prominence has
not earned the top of the page.

### Sources

A source is one rule. Sources are independent — none knows the others exist —
so adding a rule is adding a source, and a bad source can be deleted without
perturbing the rest.

```yaml
next_actions:
  # Fires only on an empty graph: the first-run case, which no
  # entity-shaped source can express.
  first-run:
    band: blocking
    count: "client == 0"
    suggest: "Nothing here yet. Add your first client?"
    actions:
      - navigate: "/form/client"
        label: "Add a client"

  # The common shape: a query plus a message.
  stale-proposal:
    band: stalled
    query: "type:proposal prop:status=sent"
    suggest: "{title} has been out since {sent_on}. Chase it?"
    cooldown: 3d
    key_props: [status]
    actions:
      - snooze: ["1d", "7d"]
      - dismiss: true
```

| Field | Meaning |
|-------|---------|
| `band` | **Required.** Names a declared band. |
| `query` | Candidate entities, in the search syntax (`type:`, `prop:`, …). |
| `condition` | Refines `query`'s candidates with a predicate expression — the only place date arithmetic works. See below. |
| `context` | Instead of `query`: scopes the source to the entity being viewed. |
| `count` | Instead of `query`: fires on `"<entity_type> == 0"` — the first-run case. |
| `suggest` | **Required.** The message. `{property}` interpolates from the candidate; `{id}` is the entity id. |
| `actions` | The affordances offered. See below. |
| `cooldown` | How long after being *shown* to stay quiet. Defaults to 24h. |
| `key_props` | Properties that make a re-triggered condition count as **new** — see below. |
| `source_world` | The world the candidate query runs in. Declared per source, never taken from the request. |
| `visible_worlds` | Worlds in which the suggestion may be displayed. Unset matches every world. Presentational only. |
| `defer_scope` | What "not now" covers: `entity` (default) or `source` — see below. |

Exactly one of `query`, `context` or `count` must be set.

#### `condition` — dwell time and other typed comparisons

`query` selects; `condition` refines. The split exists because they are
different languages: `query` is the search syntax, pushed toward the store,
and `condition` is a predicate expression evaluated per candidate with the
same host functions automations use.

```yaml
next_actions:
  chase-proposal:
    band: blocking
    query: "type:proposal prop:status=sent"
    condition: "days_between(entity.sent_on, today()) >= 11"
    suggest: "{title} has been out {days} days with no reply. Chase it?"
```

That is the shape `query` alone cannot express: "how long has this been
sitting?" needs date arithmetic, which needs the property's declared type from
the metamodel, which the search syntax does not consult.

**Do not put an expression in `query`.** The two syntaxes overlap without
erroring, so `query: "days_between(entity.due, today()) <= 7"` is not rejected
— it is read as a filter on a property *named*
`days_between(entity.due, today())`, matches nothing, and the source goes
quiet with no diagnostic. That is why they are separate keys rather than one
key that guesses.

Rules worth knowing:

- **`query` must name at least one entity type** when a condition is present
  (`type:task …`). A condition references `entity.<property>`, and without a
  type there is nothing to check it against.
- **The condition must be valid on *every* type the query names.** A query
  spanning `task` and `note` yields candidates of either, and the engine
  cannot know which until it holds one — so a condition valid on only one of
  them is refused at load rather than silently dropping the other's
  candidates.
- **A typo fails at startup**, not at render:
  `condition does not compile against entity type "task": unknown attribute
  "dew" on record`. A condition that silently matched nothing would be
  indistinguishable from a source with nothing to say.
- **Not available on a `count` source** — there is no entity to test.
- **`related(...)` is allowed**, as in a list condition. It is answered once
  per source, and only entities the reader may see count. See
  [Where `related(...)` works](metamodel.md#where-related-works).
- **Not available with free text in `query`** (`type:task urgent`). Free-text
  results are capped by relevance before the condition runs, so a condition
  matching only a hit past the cut would silently never fire. Select with
  `prop:` filters instead.

The available functions are the ones automations use: `days_between`,
`date_add`, `rrule_next`, `today`, plus `match`, `regex`, `contains` and
`len`. See [metamodel.md](metamodel.md) for their signatures.

##### Per-user sources: `current_user`

A next action resolves for one signed-in person, so a condition may refer to
them. Three spellings, all meaning the same identity:

```yaml
next_actions:
  my-stale-tickets:
    band: attention
    query: "type:ticket prop:status=open"
    condition: "is_current_user(entity.assignee) and days_between(entity.updated, today()) > 14"
    suggest: "{title} has been yours for two weeks without movement."
  watching:
    band: ambient
    query: "type:ticket prop:status=blocked"
    condition: "has_current_user(entity.watchers)"
    suggest: "{title} — something you watch is blocked."
```

- `entity.assignee == current_user.id` — plain equality against the current
  user's id.
- `is_current_user(entity.assignee)` — the same, for a string property. Reads
  better, and is what to write when the property may be unset (an unset
  property is simply "not me", never an error).
- `has_current_user(entity.watchers)` — membership, for a **list** property:
  true when the current user is one of the values. (Lists cannot be compared
  with `==`; this is the way to ask.)

`current_user.id` is the user **entity id** when your ACL policy declares a
`user_entity_type` and the signed-in principal resolves to one of its
entities, so it compares directly against a property that holds an entity id.
Without that, it is the raw principal (the header or JWT subject), and the
property must hold that string instead. `current_user.tool` (`data-entry`,
`mcp`, …) is available for diagnostics and is never a permission input.

**Fail-closed.** A per-user condition on a request that carries no identity —
a deployment without an identity source, for example — makes that source
contribute nothing for the request, with a warning in the server log naming
the source. It is never evaluated against a placeholder identity, so it can
never match "everyone's unset rows" or another user's. Other sources are
unaffected, and a condition that does not mention the current user keeps
working unauthenticated.

**Pushed to the store.** The current-user forms (and plain string equalities)
in a top-level `and` chain are lowered into the candidate query as a
pre-filter, so "tickets assigned to me" is an indexed lookup rather than a scan
of every ticket. On PostgreSQL the scalar ones (`==`, `is_current_user`) also
join the derived static-query index for that source; membership
(`has_current_user`) is pushed but not indexed. Anything under `or`/`not`, or
a typed comparison, stays Go-side — still correct, just not pre-filtered. The
full condition is always evaluated per candidate; pushdown only narrows what
is fetched.

#### `key_props` and re-triggering

A suggestion is identified by `(source, entity, key_props values)`. Without
`key_props`, a proposal going `draft → sent → draft` keeps the same identity,
so a snooze from the first draft still suppresses the second one.

Listing `key_props: [status]` makes the identity change with the status, so a
genuinely new stall surfaces even though an old snooze exists.

#### `defer_scope` — what "not now" covers

When a user snoozes or dismisses a suggestion, what were they declining?

- **`entity`** (the default) — *this item*. An ISMS task needing attention:
  they still want the other tasks, just not this one.
- **`source`** — *the interruption*. A daily quip, one entity per quip: which
  quip was on offer is incidental, and handing them another immediately is
  precisely what they said no to. Same for a "complete your profile" nudge —
  prompting about a different field a moment later is the same nag.

All three are entity-shaped sources with interpolated messages, so neither the
message template nor the query shape separates them. Only you know which a
source is, which is why it is declared rather than inferred.

A source with a `pick_one` affordance defaults to `source` scope: its
suggestion is about the set ("one of these is small"), so keying the deferral
to whichever option happened to be picked would hand back the same suggestion
with a different entity. Override it explicitly if you want per-candidate
deferral anyway.

#### `count` and the read gate

A `count` source is evaluated **through the caller's read gate** by default:
it asks "do *I* have any clients?", not "does anyone?". A principal who can
read no clients therefore sees the first-run hint.

`count_ungated: true` asks the whole-graph question instead. Use it only for a
genuinely operator-level check ("has this deployment been set up at all?") —
it discloses that entities of a type exist to someone permitted to read none
of them.

### Actions

The affordances offered alongside a suggestion. Each entry sets exactly one:

```yaml
    actions:
      - navigate: "/entity/task/{id}"   # hand off to a destination
        label: "Open it"
      - action: mark-task-done          # run a configured action
      - set: { status: done }           # inline property mutation
        confirm: true
      - snooze: ["1d", "7d"]            # defer, offering these durations
      - dismiss: true                   # "not this one"
      - acknowledge: true               # "seen it" — for content sources
```

`snooze` and `dismiss` are not decoration. Without a way to say "not now", the
only way to clear a suggestion is to comply with it — which makes it a demand
rather than a hint. They are also the best signal available: a source
dismissed every time is a source to delete.

**Muting is always available**, whether or not you configure it. The UI offers
"stop suggesting this" on every suggestion, per source and per user, and it is
reversible.

### What is stored, and where

Snoozes, mutes and last-shown timestamps are **per user** and live in
`.rela/next-action-state.json` — never in the graph. A snooze is not a fact
about an entity; it is a fact about one person's relationship to a suggestion
at a moment, and storing it as an entity would make it visible to everyone and
audited forever.

The state is disposable: losing it costs a user a repeated suggestion, not
data.

> **Note.** With no identity source configured, every request shares one
> bucket of this state — fine for a single-user deployment, surprising for a
> shared one. Wiring any identity (JWT, header) separates it.

### A worked example

A small consultancy. Four sources, each a different shape, ordered so the
loudest is the one someone else is blocked on:

```yaml
next_action_bands:
  - id: blocking
    label: Someone is waiting
    prominence: banner
  - id: stalled
    label: In your court
    prominence: notice
  - id: tidying
    label: Spare time
    prominence: statusbar
  - id: ambient
    label: Nothing owed
    prominence: statusbar

next_actions:
  # The ball is in THEIR court. Interpolates the client so the message
  # says who you would be chasing.
  chase-proposal:
    band: blocking
    query: "type:proposal prop:status=sent"
    suggest: "The {title} proposal is out with {client}. Chase it?"
    cooldown: 3d
    key_props: [status]
    actions:
      - navigate: "/entity/{id}"
        label: "Open it"
      - snooze: ["1d", "7d"]
      - dismiss: true

  # Same shape, opposite ownership: this one is waiting on you. Quieter,
  # because nobody else is blocked.
  send-draft:
    band: stalled
    query: "type:proposal prop:status=draft"
    suggest: "{title} has been in draft a while. Send it?"
    cooldown: 3d
    key_props: [status]
    actions:
      - action: mark-sent
      - snooze: ["1d"]
      - dismiss: true

  # Nothing is wrong — there is simply an opportunity. The options come
  # from a query at render time, so they are whatever is small right now.
  spare-time:
    band: tidying
    query: "type:task prop:status=todo prop:effort=xs"
    suggest: "Got a spare moment? One of these is small."
    cooldown: 12h
    actions:
      - pick_one:
          query: "type:task prop:status=todo prop:effort=xs"
          limit: 3
          action: start-task
      - snooze: ["1d"]

  # The counterweight. A configuration made only of chores is a nag however
  # well-tuned, so the quietest band holds something that is not work.
  daily-quip:
    band: ambient
    query: "type:quip"
    suggest: "{text}"
    actions:
      - acknowledge: true
```

What this produces, as each suggestion is deferred:

1. **banner** — "The Meridian retainer proposal is out with Meridian. Chase it?"
2. **notice** — "Kessler SOW has been in draft a while. Send it?"
3. **status bar** — "Got a spare moment?", offering three small tasks
4. **status bar** — the quip
5. nothing at all

Two things worth noticing:

`chase-proposal` and `send-draft` are almost identical rules. They differ in
**band**, because who is blocked is the thing that matters — and that is a
judgement only you can make, which is why bands are operator-declared.

`spare-time` needs no `defer_scope`: a `pick_one` source defaults to `source`
scope, so declining it means "not now" for the whole idea, not just for
whichever task happened to be offered.

### Customising the look

The next-action UI emits the operator hooks documented in
[customisation.md](customisation.md): a `<rela-slot name="companion">` you can
define in `custom.js`, and `rela-na*` classes plus `data-band` /
`data-prominence` / `data-source` attributes for `custom.css`.

```js
// custom.js — a character per band
const FACES = { blocking: '🐉', stalled: '🦊', ambient: '🌙' }
customElements.define('rela-slot', class extends HTMLElement {
  static observedAttributes = ['name', 'data-band']
  connectedCallback() { this.render() }
  attributeChangedCallback() { this.render() }
  render() {
    if (this.getAttribute('name') !== 'companion') return
    this.textContent = FACES[this.getAttribute('data-band')] || '✨'
  }
})
```

## Kanbans

Kanbans provide a visual board view where entities are displayed as cards grouped into columns
(and optionally swimlanes). Cards can be dragged between columns/swimlanes to update the
underlying entity properties.

### Basic Kanban

```yaml
kanbans:
  ticket_board:
    entity_type: ticket
    title: "Ticket Board"
    column_property: status
    card:
      title: title
      fields:
        - property: priority
        - property: assignee
    edit_form: edit_ticket
    create_form: create_ticket
```

### Kanban Fields

| Field              | Type   | Description                                                |
| ------------------ | ------ | ---------------------------------------------------------- |
| `entity_type`      | string | Entity type to display on the board                        |
| `title`            | string | Board heading                                              |
| `header`           | string | Markdown rendered above the board (info/help; see below)   |
| `footer`           | string | Markdown rendered below the board                          |
| `column_property`  | string | Property to group by for columns (must be enum/custom type)|
| `columns`          | list   | Explicit column definitions (`value`, `label`, `icon`)     |
| `swimlane_property`| string | Property to group by for swimlanes (optional)              |
| `swimlanes`        | list   | Explicit swimlane definitions (`value`, `label`, `icon`)   |
| `card`             | object | Card display configuration                                 |
| `edit_form`        | string | Form name for editing cards (click to open)                |
| `create_form`      | string | Form name for the "New" button                             |
| `filters`          | list   | Static filters (same as lists)                             |
| `filter_controls`  | list   | Interactive filter controls (same as lists)                |
| `query_scope`      | string | Named query scope (same as lists)                          |

#### Column and swimlane icons

Columns and swimlanes take an optional `icon:` — a **name**, not a glyph:

```yaml
columns:
  - value: open
    label: "To Do"
    icon: inbox
  - value: in-progress
    label: "In Progress"
    icon: progress
  - value: resolved
    label: "Done"
    icon: done
```

Icons are SVG and inherit the current text colour, so they follow the light /
dark theme and any styling applied to the header.

The full set is listed under [Icon names](#icon-names) below. An unknown name is
a config error at startup, which suggests the closest valid name.

A column with no `icon:` simply has no glyph, and writing `icon: none` gives the
same result — but only because a *column* derives no glyph from its type. A
sidebar item does, so for navigation the two differ: see
[Deliberately having no icon](#deliberately-having-no-icon).

You can still put an emoji directly in `label:` — it renders verbatim, and
rela will never strip or reinterpret it. But an emoji cannot take the theme's
colour and renders differently on every operating system, so `icon:` is
preferred where one of the names above fits.

#### Header and footer info regions

Boards support the same admin-authored info regions as lists — see
[Header and footer info regions](#header-and-footer-info-regions) under Lists
for the full description. `header` and `footer` accept Markdown, render as
sanitized HTML above and below the board, and are authored in
`data-entry.yaml` only.

```yaml
kanbans:
  ticket_board:
    entity_type: ticket
    title: "Ticket Board"
    header: |
      Cards move **left to right**. Drag a card to change its status — see the
      [workflow guide](/entity/guide-ticket-workflow).
    footer: |
      _Reopening a done ticket? Talk to the maintainers first._
    column_property: status
```

The regions sit outside the board's horizontal scroll area, so they stay
visible when a wide board scrolls sideways.

Unlike lists, a kanban has **no `description` fallback** for `header`: that
alias exists on lists only because `description` predated the info regions and
was already present in configs. Set `header` directly.

### Columns

By default, columns are inferred from the enum values of `column_property` in the metamodel.
To customize column order or labels, define explicit columns:

```yaml
kanbans:
  ticket_board:
    entity_type: ticket
    column_property: status
    columns:
      - value: open
        label: "📥 To Do"
      - value: in-progress
        label: "🔧 In Progress"
      - value: resolved
        label: "✅ Done"
```

| Field   | Type   | Description                                    |
| ------- | ------ | ---------------------------------------------- |
| `value` | string | Enum value that maps to this column            |
| `label` | string | Display label (defaults to the raw enum value)  |

Entities with column property values not in the explicit list are hidden from the board.

### Swimlanes

Add a second grouping dimension with swimlanes. This creates a grid where columns are horizontal
and swimlanes are vertical rows:

```yaml
kanbans:
  priority_board:
    entity_type: ticket
    column_property: priority
    swimlane_property: status
    swimlanes:
      - value: open
      - value: in-progress
      - value: resolved
```

| Field   | Type   | Description                                      |
| ------- | ------ | ------------------------------------------------ |
| `value` | string | Enum value that maps to this swimlane            |
| `label` | string | Display label (defaults to the raw enum value)    |

Without explicit swimlanes, values are inferred from the metamodel. Entities whose swimlane
property value is not in the list are hidden.

### Card Configuration

The `card` object controls what's displayed on each card:

```yaml
card:
  title: title          # Property to use as card heading
  fields:               # Additional fields shown on the card
    - property: priority
    - property: assignee
      label: "Owner"
```

| Field    | Type   | Description                                           |
| -------- | ------ | ----------------------------------------------------- |
| `title`  | string | Property name for the card heading                    |
| `fields` | list   | Additional properties displayed as badges on the card |

Card fields use the same styling as lists — enum values are displayed with colors from `styles`.

### Drag and Drop

Cards can be dragged between columns (and swimlanes if configured). Dropping a card updates
the entity's column property (and swimlane property) and saves the change to disk. The board
re-renders to reflect the new state.

### Navigation

Add kanban boards to the sidebar using the `kanban` field in navigation entries:

```yaml
navigation:
  - group: "Boards"
    items:
      - label: "Ticket Board"
        kanban: ticket_board
      - label: "Priority Board"
        kanban: priority_board
```

### Keyboard Shortcuts

| Key | Action                              |
| --- | ----------------------------------- |
| `N` | Open the create form (if configured)|

### Complete Example

```yaml
kanbans:
  ticket_board:
    entity_type: ticket
    title: "Ticket Board"
    column_property: status
    columns:
      - value: open
        label: "📥 To Do"
      - value: in-progress
        label: "🔧 In Progress"
      - value: resolved
        label: "✅ Done"
    card:
      title: title
      fields:
        - property: priority
        - property: assignee
    edit_form: edit_ticket
    create_form: create_ticket
    filter_controls:
      - property: priority
        widget: select

  priority_board:
    entity_type: ticket
    title: "Priority Board"
    column_property: priority
    swimlane_property: status
    swimlanes:
      - value: open
      - value: in-progress
      - value: resolved
    card:
      title: title
      fields:
        - property: assignee
    edit_form: edit_ticket
    create_form: create_ticket
    filters:
      - property: status
        operator: "!="
        value: closed
```

## Navigation

The navigation section defines the sidebar menu. Each entry is either a direct item (linking to a
list, kanban, dashboard, search or settings page) or a **group** containing multiple items:

```yaml
navigation:
  - label: "Dashboard"
    dashboard: true
  - group: "Tickets"
    items:
      - label: "Open Tickets"
        list: open_tickets
      - label: "All Tickets"
        list: all_tickets
  - group: "Reference Data"
    collapsed: true
    items:
      - label: "Categories"
        list: categories
  - label: "Search"
    search: true
```

### Direct Items

| Field       | Type   | Description                                                    |
| ----------- | ------ | -------------------------------------------------------------- |
| `label`     | string | Menu item text                                                 |
| `list`      | string | List name to navigate to (mutually exclusive with other types) |
| `kanban`    | string | Kanban board name to navigate to                               |
| `dashboard` | bool   | Link to the dashboard page                                     |
| `graph`     | bool   | Link to the graph explorer                                     |
| `document`  | string | Standalone document to open (see [Standalone documents](#standalone-documents)) |
| `search`    | bool   | Link to the search page                                        |
| `settings`  | bool   | Link to the settings page                                      |
| `action`    | string | Action ID to trigger when clicked (renders as a sidebar button)|
| `entities`  | string | Entity type whose entities are listed as links; only inside a group (see [Entity lists in a group](#entity-lists-in-a-group)) |
| `icon`      | string | Icon name; overrides the icon derived from the entry type (see below) |
| `permission`| string | Hide this entry from users who lack the named ACL permission (see below) |

#### Item icons

Each entry gets an icon from its *type* — every `list:` entry the same list
glyph, every `kanban:` the same board glyph. In a sidebar with several lists
that means several identical rows, distinguishable only by their labels.

`icon:` overrides it:

```yaml
navigation:
  - group: "Tickets"
    items:
      - label: "My Tickets"
        list: my_tickets
        icon: inbox
      - label: "Open Tickets"
        list: open_tickets
        icon: status
      - label: "All Tickets"
        list: all_tickets # no icon: keeps the derived list glyph
```

Valid names are the same set kanban columns use — see
[Icon names](#icon-names). An unknown name is a config error at startup, which
suggests the closest valid name.

An `action:` entry derives no icon of its own, so `icon:` is the only way to
give one a symbol. A **group** cannot take an icon — it renders as a plain
section title with nowhere to put one — and naming one there is an error
rather than silently ignored.

##### Deliberately having no icon

Every entry gets a glyph from its *type*, so a sidebar of five lists shows five
identical list icons. That is not always what you want: Apple's interface
guidelines advise using menu icons
[sparingly and with purpose](https://developer.apple.com/design/human-interface-guidelines/menus#Icons),
and a menu where every row carries a glyph signals no more than one where none
do. Reserving icons for the entries that deserve emphasis is a real design
choice, and `icon: none` is how you express it:

```yaml
navigation:
  - group: "Tickets"
    items:
      - label: "My Tickets"
        list: my_tickets
        icon: inbox # the one entry worth calling out
      - label: "Open Tickets"
        list: open_tickets
        icon: none
      - label: "All Tickets"
        list: all_tickets
        icon: none
```

A `none` entry keeps its place in the icon column, so its label stays aligned
with the labelled siblings above it rather than jumping left.

Note the difference from leaving `icon:` off altogether: an absent `icon:` means
*"use the glyph for this entry's type"*, whereas `none` means *"draw nothing"*.

The one exception is the **collapsed** sidebar, which hides every label. A row
with neither icon nor label would be invisible but still clickable, so a `none`
entry shows its type glyph again while collapsed — there is nothing left for it
to be distinguished from.

### Icon names

Icon names are used by `icon:` on navigation items and on kanban columns and
swimlanes. They are names, not glyphs: rela renders each as an inline SVG that
inherits the surrounding text colour, so icons follow the light / dark theme
instead of fighting it the way an emoji does.

<!-- BEGIN generated: icons -->
The 218 names below are the complete set. This table is **generated** from the
same definition the server validates against and the app renders from, so it
cannot fall out of step with either — an earlier hand-written copy went stale
within a single release, which is why it is machine-written now.

Use `none` to suppress an icon entirely; it is not in the table because it
draws nothing.

#### Navigation

| Name | Glyph | Description |
| --- | --- | --- |
| `dashboard` | House | Home or landing view |
| `list` | List | List view; the default list glyph |
| `kanban` | Kanban | Board view; the default kanban glyph |
| `calendar` | CalendarDays | Calendar view |
| `gantt` | ChartGantt | Gantt view |
| `search` | Search | Search |
| `settings` | Settings | Settings or configuration |
| `apps` | Blocks | Custom apps section |
| `document` | FileText | Document view; also the fallback glyph |
| `table` | Table2 | Tabular data |
| `grid` | LayoutGrid | Grid or tile layout |
| `layout` | LayoutDashboard | Dashboard or panel layout |
| `rows` | Rows3 | Row-oriented layout |
| `columns` | Columns3 | Column-oriented layout |
| `menu` | Menu | Menu or hamburger |
| `panel` | PanelLeft | Side panel |
| `filter` | Funnel | Filter or narrow a result set |
| `sliders` | SlidersHorizontal | Adjustable settings or controls |
| `expand` | Maximize2 | Expand or maximize |
| `collapse` | Minimize2 | Collapse or minimize |

#### Status & workflow

| Name | Glyph | Description |
| --- | --- | --- |
| `inbox` | Inbox | Incoming or unstarted work |
| `status` | CircleDot | Generic status; a dot in a circle |
| `done` | CircleCheck | Completed or resolved |
| `check` | Check | A bare check mark |
| `cancelled` | CircleX | Cancelled or rejected |
| `blocked` | Ban | Blocked or forbidden |
| `paused` | CirclePause | Paused or on hold |
| `active` | CirclePlay | Started; a play triangle in a circle |
| `progress` | LoaderCircle | Work underway; a partial ring |
| `skipped` | CircleSlash | Skipped or not applicable |
| `pending` | CircleEllipsis | Awaiting input; an ellipsis in a circle |
| `warning` | TriangleAlert | Warning or caution; a TRIANGLE |
| `alert` | CircleAlert | Alert or error; a CIRCLE |
| `info` | Info | Informational note |
| `help` | CircleQuestionMark | Help or unanswered question |
| `flag` | Flag | Flagged for attention |
| `star` | Star | Favorite or highlight |
| `bookmark` | Bookmark | Saved for later |
| `pin` | Pin | Pinned to the top |
| `flame` | Flame | Hot or high priority |
| `loading` | Loader | In-flight or loading |
| `workflow` | Workflow | A process or pipeline |
| `milestone` | Milestone | A milestone or checkpoint |
| `target` | Target | A goal or target |

#### Time

| Name | Glyph | Description |
| --- | --- | --- |
| `clock` | Clock | A point in time |
| `timer` | Timer | Elapsed or remaining time |
| `hourglass` | Hourglass | Waiting or long-running |
| `history` | History | Past revisions or an audit trail |
| `schedule` | CalendarClock | A scheduled or recurring event |
| `calendar-check` | CalendarCheck | A confirmed or completed date |
| `alarm` | AlarmClock | A reminder or alarm |
| `sunrise` | Sunrise | Start of day |
| `sunset` | Sunset | End of day |

#### People & orgs

| Name | Glyph | Description |
| --- | --- | --- |
| `user` | User | A single person |
| `users` | Users | A team or group |
| `user-add` | UserPlus | Add or invite a person |
| `user-check` | UserCheck | An approved or verified person |
| `user-remove` | UserX | Remove or deactivate a person |
| `contact` | Contact | A contact record |
| `organization` | Building2 | A company or organization |
| `briefcase` | Briefcase | Work or a business matter |
| `handshake` | Handshake | An agreement or partnership |
| `award` | Award | A recognition or certification |
| `trophy` | Trophy | An achievement |
| `graduation` | GraduationCap | Training or education |

#### Communication

| Name | Glyph | Description |
| --- | --- | --- |
| `mail` | Mail | Email |
| `mail-open` | MailOpen | Read email |
| `message` | MessageSquare | A comment or single message |
| `discussion` | MessagesSquare | A thread or discussion |
| `send` | Send | Send or submit |
| `phone` | Phone | A phone call |
| `video` | Video | A video call or recording |
| `bell` | Bell | Notifications |
| `bell-active` | BellRing | An active or unread notification |
| `announce` | Megaphone | An announcement or broadcast |
| `feed` | Rss | A feed or subscription |

#### Files & storage

| Name | Glyph | Description |
| --- | --- | --- |
| `file` | File | A generic file |
| `files` | Files | Several files |
| `file-add` | FilePlus | Create a file |
| `file-check` | FileCheck | An approved or validated file |
| `file-code` | FileCode | A source or config file |
| `spreadsheet` | FileSpreadsheet | A spreadsheet or tabular export |
| `folder` | Folder | A folder |
| `folder-open` | FolderOpen | An open folder |
| `hierarchy` | FolderTree | A hierarchy or nested structure |
| `archive` | Archive | Archived or inactive items |
| `package` | Package | A package or release |
| `box` | Box | A container or unit |
| `boxes` | Boxes | Inventory or a collection |
| `database` | Database | A database |
| `server` | Server | A server or host |
| `disk` | HardDrive | Storage or a disk |
| `cloud` | Cloud | A cloud service |
| `upload` | Upload | Upload |
| `download` | Download | Download |
| `attachment` | Paperclip | An attachment |
| `printer` | Printer | Print |

#### Actions

| Name | Glyph | Description |
| --- | --- | --- |
| `add` | Plus | Create or add |
| `remove` | Minus | Remove or subtract |
| `close` | X | Close or dismiss |
| `edit` | Pencil | Edit |
| `write` | PenLine | Compose or annotate |
| `erase` | Eraser | Clear or erase |
| `delete` | Trash2 | Delete |
| `copy` | Copy | Copy |
| `clipboard` | Clipboard | Paste or clipboard |
| `checklist` | ClipboardList | A checklist or task list |
| `approve` | ClipboardCheck | Approve or sign off |
| `save` | Save | Save |
| `refresh` | RefreshCw | Refresh or re-run |
| `undo` | RotateCcw | Undo or revert |
| `play` | Play | Run or start |
| `pause` | Pause | Pause |
| `stop` | Square | Stop |
| `skip` | SkipForward | Skip ahead |
| `share` | Share2 | Share |
| `link` | Link | A link or relation |
| `external` | ExternalLink | An external link |
| `cut` | Scissors | Cut or split |

#### Security

| Name | Glyph | Description |
| --- | --- | --- |
| `lock` | Lock | Locked or private |
| `unlock` | LockOpen | Unlocked or public |
| `key` | KeyRound | A key or credential |
| `shield` | Shield | Protection or a control |
| `shield-check` | ShieldCheck | A verified or passing control |
| `shield-alert` | ShieldAlert | A failing or at-risk control |
| `visible` | Eye | Visible or watched |
| `hidden` | EyeOff | Hidden or redacted |
| `fingerprint` | FingerprintPattern | Identity or authentication |

#### Analysis

| Name | Glyph | Description |
| --- | --- | --- |
| `chart-bar` | ChartColumn | A bar chart |
| `chart-line` | ChartLine | A line chart or trend |
| `chart-pie` | ChartPie | A pie chart or breakdown |
| `trending-up` | TrendingUp | An increasing metric |
| `trending-down` | TrendingDown | A decreasing metric |
| `activity` | Activity | Activity or a live signal |
| `gauge` | Gauge | A measure against a threshold |
| `calculator` | Calculator | A calculation |
| `percent` | Percent | A proportion |
| `scale` | Scale | A balance or trade-off |
| `layers` | Layers | Layers or grouping |

#### Development

| Name | Glyph | Description |
| --- | --- | --- |
| `code` | Code | Source code |
| `terminal` | Terminal | A command or shell |
| `bug` | Bug | A defect |
| `branch` | GitBranch | A branch |
| `commit` | GitCommitHorizontal | A commit |
| `merge` | GitMerge | A merge |
| `pull-request` | GitPullRequest | A pull request |
| `component` | Component | A component or module |
| `puzzle` | Puzzle | A plugin or extension |
| `cpu` | Cpu | Compute or hardware |
| `network` | Network | A network or topology |
| `wrench` | Wrench | A tool or maintenance task |
| `hammer` | Hammer | Building or construction |
| `cog` | Cog | A mechanism or job |
| `flask` | FlaskConical | A conical flask; an experiment or trial |
| `beaker` | Beaker | A wide-mouthed beaker; research or measurement |
| `microscope` | Microscope | Close inspection |

#### Knowledge

| Name | Glyph | Description |
| --- | --- | --- |
| `book` | Book | A book or manual |
| `book-open` | BookOpen | A guide or open reference |
| `library` | Library | A collection of documents |
| `news` | Newspaper | News or an article |
| `scroll` | Scroll | A policy or long document |
| `note` | StickyNote | A short note |
| `notebook` | NotebookPen | A notebook or journal |
| `idea` | Lightbulb | An idea or proposal |
| `tag` | Tag | A single tag or label |
| `tags` | Tags | Several tags |
| `hash` | Hash | An identifier or channel |

#### Places & logistics

| Name | Glyph | Description |
| --- | --- | --- |
| `location` | MapPin | A place |
| `map` | Map | A map or overview |
| `globe` | Globe | Global or international |
| `compass` | Compass | Orientation or discovery |
| `route` | Route | A route or path |
| `truck` | Truck | Delivery or shipping |
| `plane` | Plane | Air travel |
| `car` | Car | Road travel |
| `train` | TramFront | Rail travel |
| `ship` | Ship | Sea freight or travel |
| `bed` | Bed | Accommodation |

#### Commerce

| Name | Glyph | Description |
| --- | --- | --- |
| `cart` | ShoppingCart | An order or basket |
| `card` | CreditCard | A payment method |
| `money` | Banknote | Cash or an amount |
| `invoice` | Receipt | An invoice or receipt |
| `wallet` | Wallet | A budget or account |
| `price` | CircleDollarSign | A price or cost |
| `gavel` | Gavel | A legal or judicial matter |

#### Devices & media

| Name | Glyph | Description |
| --- | --- | --- |
| `monitor` | Monitor | A desktop or display |
| `laptop` | Laptop | A laptop |
| `phone-device` | Smartphone | A mobile device |
| `tablet` | Tablet | A tablet |
| `image` | Image | An image |
| `camera` | Camera | A photo or capture |
| `mic` | Mic | Audio input |
| `music` | Music | Audio or music |
| `headphones` | Headphones | Listening or playback |
| `wifi` | Wifi | Connectivity |
| `battery` | Battery | Power level |
| `power` | Power | On or off |
| `plug` | Plug | An integration or connection |

#### Health & environment

| Name | Glyph | Description |
| --- | --- | --- |
| `heart` | Heart | A favorite or wellbeing |
| `pulse` | HeartPulse | Health or a live check |
| `stethoscope` | Stethoscope | A medical or diagnostic matter |
| `leaf` | Leaf | Sustainability |
| `tree` | TreePine | Nature or long-term growth |
| `sprout` | Sprout | Something new or growing |
| `recycle` | Recycle | Reuse or a cycle |
| `droplet` | Droplet | Liquid or a measurement |
| `temperature` | Thermometer | Temperature or intensity |
| `rain` | CloudRain | Rain or poor conditions |
| `snow` | Snowflake | Cold or frozen |
| `wind` | Wind | Wind or airflow |

#### Theme

| Name | Glyph | Description |
| --- | --- | --- |
| `sun` | Sun | Light mode |
| `moon` | Moon | Dark mode |
| `palette` | Palette | Colors or theming |
| `brush` | Brush | Styling or appearance |
| `contrast` | Contrast | Contrast or a display setting |
| `sparkles` | Sparkles | Something new, generated, or featured |
| `zap` | Zap | Fast or automated |
| `rocket` | Rocket | A launch or release |
<!-- END generated: icons -->

### Groups

| Field       | Type   | Description                                              |
| ----------- | ------ | -------------------------------------------------------- |
| `group`     | string | Group header text (displayed as uppercase label)         |
| `collapsed` | bool   | Default collapsed state (accepted and sent on the wire; the current SPA renders groups always expanded) |
| `items`     | list   | List of direct navigation items within the group         |

Groups appear as titled sections in the sidebar. The `collapsed` flag is kept in the config
schema and the sidebar API response for compatibility, but the current SPA does not render a
collapse toggle — groups are always expanded. (The old server-rendered UI persisted collapse
state in `.rela/ui-state.json`; that mechanism has been removed.)

Nested groups are not supported. If an item inside `items` has a `group` field, config validation
will reject it with a clear error message.

The first navigable entry is the default landing page — the first direct item, or the first item
inside the first group. Order matters; items appear in the sidebar in the order listed.

### Entity lists in a group

An `entities:` item lists entities as sidebar links, one link per entity. Use it
for things people open often, such as the projects that are currently active:

```yaml
navigation:
  - group: "Active projects"
    items:
      - entities: project
        query_scope: active
        sort:
          - property: title
```

| Field         | Type   | Description |
| ------------- | ------ | ----------- |
| `entities`    | string | The entity type to list. |
| `query_scope` | string | A [query scope](#query-scopes) declared on that type. Optional. |
| `sort`        | list   | Link order, in the same form as a list's `sort:`. Optional. |
| `icon`        | string | Icon drawn before every link. Optional. |
| `permission`  | string | Hides the item, as for any other entry. Optional. |

The rules:

- **It must sit inside a group.** The group title is the heading for the links.
  An `entities:` item at the top level is a config error.
- **It takes no `label:`.** Each link shows its entity's display name (see
  [Display names](#display-names)).
- **It has exactly one kind.** Combining `entities:` with `list:`, `kanban:` or
  another destination is a config error. `query_scope:` and `sort:` are only
  valid next to `entities:`.
- **Scopes work as they do for a list.** With no `query_scope:`, the type's
  `default` scope applies; `query_scope: all` withdraws it. A scope the type does
  not declare is a config error.
- **Order.** Without `sort:`, the type's `default_sort` applies, and without that
  the links are in id order.
- **Up to 100 links are shown.** When more entities match, a line such as
  "and 12 more" follows the links. It is not a link. If a set is often that
  large, point a `list:` entry at it instead.
- **A group with no matching entities is hidden**, provided every item in it is
  an `entities:` item. It reappears when an entity starts to match.
- **Entity links are hidden while the sidebar is collapsed.** Each link would
  otherwise shrink to the same icon, and one icon cannot tell entities apart.

The links are **per user**. The sidebar only carries the definition. The
browser then asks the ordinary list API for the rows, so each user sees only
the entities they may read, in the current world, and a scope that reads
`current_user` gives each person their own set. The links update when an
entity of that type changes, without a page reload.

One limit: if a user loses access to an entity, a link they already have on
screen stays until the next update of that entity type or a page reload.
Following the link then shows the ordinary "not found" page.

### Hiding entries a user cannot act on (`permission:`)

An entry with a `permission:` is omitted from the sidebar for principals who do
not hold that permission — a global named permission granted through a role's
`permissions:` list in `acl.yaml`, the same mechanism behind `history:read` and
the `delegate-*` family:

```yaml
navigation:
  - label: "Tickets"
    list: all_tickets
  - label: "Audit log"
    list: audit_log
    permission: admin:read      # only holders see this entry
  - group: "Admin"
    items:
      - label: "Settings"
        settings: true
        permission: admin:settings
```

A group whose every item is hidden disappears with them, so you never get a
heading with nothing under it. `permission:` is not valid **on** a group —
gate the items instead.

Behaviour without a policy: with no `acl.yaml`, every entry is shown — nothing
is denied when nothing is configured. The same applies under `--read-only`,
which restricts *writes* and leaves reads untouched, so there is no permission
model to consult and no reason to hide read surfaces from an observe-only
operator.

**This is a convenience, not a security control.** It keeps menu entries a user
cannot act on out of their way; it does not protect anything. The target of a
hidden entry behaves exactly as it always did — type its URL and you reach it,
and a list still returns its ACL-scoped rows, which for someone permitted to
read none of them is simply an empty list. Nor is anything concealed:
`/api/v1/_config` serves the whole navigation tree to every principal.

So do not use `permission:` here *instead of* real access control. What
protects your data is the read ACL on the entities themselves; this only
decides what appears in a menu.

One caveat worth knowing: the permission name is **not** checked against
`acl.yaml` at config load (the same is true of `commands:` and `documents:`).
A typo like `admin:raed` produces an entry nobody can see, with no error. If a
menu item has vanished, check the spelling against the `permissions:` list on
your roles first.

Direct items and groups can be freely mixed in any order.

## Actions

Actions define quick operations that can be triggered from list views or the sidebar. An action
either mutates entity properties declaratively (`set`) or runs a Lua script (`script`).

### Defining Actions

Actions are defined at the top level of `data-entry.yaml`:

```yaml
actions:
  resolve-ticket:
    label: "Resolve"
    key: "r"
    set:
      status: resolved

  close-ticket:
    label: "Close"
    key: "c"
    confirm: true
    set:
      status: closed

  run-checks:
    label: "Validate"
    key: "v"
    script: validate-ticket.lua
    params:
      strict: "true"
```

### Action Fields

| Field         | Type   | Description                                                     |
| ------------- | ------ | --------------------------------------------------------------- |
| `label`       | string | Display text (required when referenced by a list)               |
| `key`         | string | Keyboard shortcut — single lowercase letter or digit (required when referenced by a list) |
| `description` | string | Optional description                                            |
| `set`         | map    | Property key-value pairs to set on the entity (mutually exclusive with `script`) |
| `script`      | string | Lua script path, relative to the `actions/` directory (mutually exclusive with `set`) |
| `params`      | map    | Static key-value parameters from config, exposed as `rela.params` (values must be strings — quote them in YAML) |
| `confirm`     | bool   | Show a confirmation dialog before executing (default: `false`)  |
| `request`     | map    | Opt into request-scoped execution — see [Request-scoped actions](#request-scoped-actions). Requires `script` |
| `capabilities`| map    | Ambient capability grant (`http`, `ai`, `write_file`, named `secrets`). Omitting it grants none |

Each action must have either `set` or `script`, not both.

`params` is **static config**, not runtime context: the values come from
`data-entry.yaml` and are the same for every invocation. The selected
entity (for list actions) is exposed separately via the `entity` global —
see [Lua Action Scripts](#lua-action-scripts).

### Using Actions in Lists

Reference action IDs in a list's `actions` field to make them available as keyboard shortcuts
on selected rows:

```yaml
lists:
  all_tickets:
    entity_type: ticket
    title: "All Tickets"
    columns:
      - property: title
        link: true
      - property: status
    actions: [resolve-ticket, close-ticket]
```

When a list has actions, the configured keyboard shortcuts appear in the list's toolbar.
Select one or more rows, then press the shortcut key to apply the action to all selected entities.

### Using Actions in Navigation

Reference an action ID in a navigation entry to render it as a sidebar button:

```yaml
navigation:
  - label: "Run Checks"
    action: run-checks
```

When clicked, the action executes. If the action script returns a `redirect`, the UI navigates
to that path. If it returns a `message`, a toast notification is shown.

### Lua Action Scripts

Action scripts live in the `actions/` directory at the project root. They have full access
to the rela Lua API (entity CRUD, graph queries, AI). A script can optionally return a table
to control the UI response.

#### Inputs available to the script

| Source        | Where           | Populated when                                                              |
| ------------- | --------------- | --------------------------------------------------------------------------- |
| Static config | `rela.params`   | Always — values from the action's `params:` map in `data-entry.yaml`        |
| Selected row  | `entity` global | Only when invoked from a list against a selected row (one call per entity). The table has `id`, `type`, `properties`, `content`, `mod_time`, plus `prop(name, default)` and `strip_prefix()` methods |

When invoked from a navigation sidebar button, no entity is selected — the
`entity` global is `nil`. Always nil-check it.

```lua
-- actions/validate-ticket.lua
-- Selected row from the list (nil for sidebar/nav invocations).
if entity == nil then
    return { message = "Select a ticket first", message_type = "warning" }
end

-- Static parameter from data-entry.yaml — values are always strings.
local strict = rela.params.strict == "true"

-- ... perform validation against entity.id, entity.properties, ... ...

return {
    message = "Validation passed",
    message_type = "success",      -- "success", "info", "warning", or "error"
    redirect = "/list/all_tickets" -- optional: navigate after completion
}
```

Scripts have a 5-second execution timeout (tighter than the default Lua
timeout because the action handler holds a global write lock for the
duration — concurrent mutations and other actions wait). Returning
nothing (or `nil`) produces a silent success response.

### Request-scoped actions

By default an action script sees no part of the HTTP request that triggered it:
the body carries `entity_id` and nothing else reaches the script, and the return
value is projected onto the SPA's `{redirect, message, message_type}` toast
vocabulary.

A `request:` block opts one action into seeing the **whole request**, and lets it
return an **arbitrary response**. This is the escape hatch for integrations the
declarative [webhook routes](webhooks.md) cannot express — an odd payload
shape, a response a third party needs in a specific form, logic that is not
find/create/append.

```yaml
actions:
  icinga-alert:
    script: icinga-alert.lua
    request:
      body: true                    # rela.request.body / .raw
      query: true                   # rela.request.query
      headers: [X-Event-Type]       # allowlist; rela.request.headers
      max_body_bytes: 262144        # optional; default 1 MiB, ceiling 8 MiB
```

Everything is **off by default**. An action with no `request:` block behaves
exactly as it did before, including the SPA response shape.

#### `rela.request`

Present only for a request-scoped action, so `if rela.request then` is the
script-side test. The table is read-only.

| Field                    | Type   | Present when                                      |
| ------------------------ | ------ | ------------------------------------------------- |
| `rela.request.method`    | string | always                                            |
| `rela.request.path`      | string | always                                            |
| `rela.request.content_type` | string | always                                         |
| `rela.request.raw`       | string | `body: true` — the body verbatim                  |
| `rela.request.body`      | table  | `body: true` **and** the body parsed as JSON      |
| `rela.request.query`     | table  | `query: true` — first value per key; `query._all[k]` is the full list |
| `rela.request.headers`   | table  | always (empty without `headers:`), keyed lowercase |

A body that does not parse as JSON leaves `rela.request.body` nil while
`rela.request.raw` still holds the bytes — a script that expects a text or
vendor payload reads `raw`. A body over the cap is refused with **413**; it is
never truncated.

**Headers are an allowlist, never pass-through.** Request headers carry session
cookies, bearer tokens and proxy-injected identity assertions. `Authorization`,
`Cookie`, `X-Forwarded-*`, `X-Auth-Request-*`, `X-Remote-*`, `X-Authentik-*`,
`X-Pomerium-*` and the deployment's own `-principal-header` are refused at config
load however you spell them — the same floor a declarative webhook gets.

#### Returning a response

A request-scoped script may return the rich shape instead of the SPA shape:

```lua
-- actions/icinga-alert.lua
local alert = rela.request.body
if alert == nil or alert.host == nil then
    return { status = 422, body = "expected a JSON body with host", content_type = "text/plain" }
end

-- ... find or create the incident, append the notification ...

return {
    status = 202,
    body = rela.json.encode({ status = "accepted", host = alert.host }),
    content_type = "application/json",
}
```

| Field          | Type   | Default                                                    |
| -------------- | ------ | ---------------------------------------------------------- |
| `status`       | number | `200` when a `body` is set. Must be 2xx, 4xx or 5xx        |
| `body`         | string | empty. Must be a **string** — encode tables yourself       |
| `content_type` | string | `text/plain; charset=utf-8`                                |

`content_type` is an **allowlist**: `application/json`, `text/plain`,
`text/csv`, `application/xml`, `text/xml`. `text/html` is deliberately not
allowed — a rendered page belongs in a [document](#documents). The response is
served with `X-Content-Type-Options: nosniff`, a `sandbox; default-src 'none'`
CSP and `Cache-Control: no-store`, because a script-controlled body is
attacker-influenceable output served same-origin with the SPA.

A return **cannot mix** the two vocabularies. `{message = ..., status = ...}` is
a contract error, not a precedence rule: the two are read by different consumers,
so honoring one and dropping the other would be a silent half-delivery.

**A script that fails always produces rela's error envelope**, whatever status
it might have been going to return — a run that raised produced no return value,
so it chose no status. A script that wants to answer 4xx or 5xx returns one
deliberately, as above.

#### What this does not change

- `entity_id` in the body still resolves through the read-side ACL gate. An id
  the caller may not read leaves the `entity` global nil and the action still
  runs, exactly as before; a `request:` block is not a second route to an entity.
- `capabilities:` is unaffected. A request-scoped action with no
  `capabilities:` block still gets no `http`, no `ai`, no secrets and no
  `write_file`.
- Producer **authentication** is not rela's job. The proxy in front (oauth2-proxy,
  Pratique) terminates it and hands rela an ACL-bounded request.
- **Idempotency is the script's job.** rela offers no dedup key here. A producer
  that retries will invoke the script twice, and the quiet failure mode is a
  notification appended to an incident body twice — match on a stable field from
  the payload before appending.

### Reserved Keyboard Shortcuts

The following keys are reserved for built-in list navigation and cannot be used as action keys:

| Key | Built-in Function |
| --- | ----------------- |
| `j` | Move selection down |
| `k` | Move selection up |
| `o` | Open selected entity |
| `e` | Edit selected entity |
| `n` | Create new entity |
| `h` | Previous page |
| `l` | Next page |

### Validation Rules

- Action IDs must match `^[a-z0-9_-]{1,64}$`
- `set` properties must exist in the entity type's metamodel
- `script` paths must end in `.lua` and be local paths (no `..` or absolute paths)
- Keys must be unique within a list (no two actions on the same list can share a key)

## Calendars

A calendar lays date-bearing entities out in a **month** or **week** grid, with
drag-to-reschedule writing a new date back to the entity.

```yaml
calendars:
  schedule:
    title: "Schedule"
    default_view: month          # month | week (default: month)
    week_start: monday           # monday | sunday (default: monday)
    sources:
      - entity_type: task
        where: ["status != done"]
        date: due_date
        summary: title
        color: blue
    event:                       # extra detail on each chip
      fields:
        - property: assignee
        - property: priority
        - relation: belongs-to
          label: "Project"
    edit_form: edit_task
    create_form: create_task
```

Without an `event:` block a chip shows the event's title alone. Adding fields
is usually worth it — a calendar of bare titles rarely answers the question the
reader opened it with.

Reach it from the sidebar with a `calendar:` navigation entry:

```yaml
navigation:
  - label: "Schedule"
    calendar: schedule
```

### Calendar fields

| Field | Type | Description |
| --- | --- | --- |
| `title` | string | Heading above the grid (default: the config key) |
| `header` | string | Markdown rendered above the grid |
| `footer` | string | Markdown rendered below the grid |
| `default_view` | string | `month` or `week` — the period shown on first load |
| `week_start` | string | `monday` or `sunday` |
| `sources` | list | Entity-to-event projections; at least one required |
| `event` | object | Extra fields shown on an event chip |
| `day_start` / `day_end` | string | `HH:MM` bounds of the week-view hour axis (defaults `08:00` / `20:00`) |
| `max_events_per_day` | int | Chips per month-view day cell before `+N more` (default 3) |
| `edit_form` | string | Form offered when editing from an event's preview |
| `create_form` | string | Form opened by the "New" button |
| `filter_controls` | list | Interactive filters, as on lists and kanbans |

### Source fields

| Field | Required | Description |
| --- | --- | --- |
| `entity_type` | yes | The entity type to project |
| `label` | no | Name shown in the source legend (default: the entity type) |
| `where` | no | Filter clauses, all ANDed. No OR — use a second source |
| `date` | yes | A `date`- or `datetime`-typed property placing the event. A `date` yields an all-day event, a `datetime` a timed one. Entities without a value are skipped |
| `end_date` | no | Property ending a multi-day event. Must be the **same kind** as `date` |
| `summary` | no | Property used as the event title (default: the type's display property) |
| `description` | no | Property shown in the event detail |
| `color` | no | Palette token: `blue`, `green`, `amber`, `red`, `violet`, `slate` |
| `max_span` | no | Days to look back for events starting before the visible period (default 31). Only meaningful with `end_date` |

Several sources merge into one grid, so a calendar can mix `task` and `meeting`
entities, each mapped its own way. Multiple sources are also how you express
**OR**, since a single source's `where` clauses are ANDed.

### Event chips

By default a chip shows the event's title. `event.fields` adds more beneath it,
using the same shape as kanban card fields — a property, or a relation whose
target titles are shown:

```yaml
    event:
      fields:
        - property: status
        - property: assignee
        - relation: blocked-by
        - relation: owned-by
          direction: incoming
          label: "Owner"
```

Each field renders on its own line as `label: value`. The label comes from,
in order: the field's own `label:`, the **relation type's label** from
`schema.yaml` for a relation field, then the raw name. So
`- relation: belongs-to` renders "belongs to: Apollo" when the relation
declares `label: belongs to`.

Properties have no authored label in the metamodel, so `- property: assignee`
renders "assignee: Alex". The UI does not title-case identifiers — labels are
authored, never derived — so write `label: "Assignee"` if you want one.

`show_label: false` drops the label for a value that already names itself:

```yaml
    event:
      fields:
        - property: priority
          show_label: false     # the badge already reads "High"
        - property: assignee
          label: "Owner"        # two person fields need naming
        - property: reviewer
          label: "Reviewer"
```

Values render through the same widgets as list cells and kanban cards, so dates
read human-readably and enums render as badges. A field with no value is
omitted rather than showing an empty label.

**Kanban cards take the same `fields:` config** — including `show_label` and
derived labels — and render through the same component, so the two surfaces
cannot drift.

### Ordering within a day

Events in a day cell are ordered:

1. **All-day events first**, before any timed one.
2. **Then by start time**, earliest first.
3. **Then by entity id**, so two events at the same time keep a stable order
   rather than reshuffling between refreshes.

```text
  Update changelog        ← all-day
  Ship the release        ← all-day
  09:15  Early standup
  13:00  Design review    ← same time, ordered by id
  13:00  Lunch & learn
  18:30  Retro
```

Times are the event's start in your **display timezone** (Settings), which is
also the timezone that decides which day it falls on — so a chip's position
never disagrees with the time printed on it.

A multi-day event sorts by where it *started*, so on its later days it appears
above events that begin that morning. That matches how other calendar apps
render spans.

### Multi-day events

An event with an `end_date` is drawn once per day it covers, with an arrow at
the chip's edge saying how it continues:

| Marker | Meaning |
| --- | --- |
| `→` | The event starts on this day |
| `↔` | It runs through this day, having started earlier and continuing after |
| `←` | It ends on this day |

Without them, five consecutive identical chips read as five separate events
rather than one five-day event. An event whose real start falls before the
visible period still shows a continuation marker on the first visible day,
rather than appearing to begin there.

### Toggling sources

A calendar with more than one source shows a **legend** above the grid naming
each one in its colour. Clicking an entry hides that source's events; clicking
again brings them back.

Which sources are hidden is carried in the URL (`?hide=0,1`), so a filtered view
is shareable and survives a refresh, exactly as the period and view are. Hiding
filters what is already loaded rather than refetching — toggling is a
glance-level gesture and should feel like one.

A field naming a property that one source's entity type lacks is simply not
rendered for that source's events — sources are heterogeneous by design, so a
field only has to make sense for the types that have it. A field that matches
**no** source is a config error.

### Clicking an event

Clicking a chip opens a **preview** of the entity — the same detail rendering
the entity page uses, in a modal — rather than jumping straight into an edit
form. The usual sequence is "see more, then maybe edit", and a small chip is a
poor basis for deciding to change something.

The preview is a read surface: it shows the entity's properties and content
without the entity page's Edit / History / Delete toolbar. Its two actions are
**Edit** (opening the calendar's `edit_form`, when one is configured) and
**Open full page**, where the full toolbar lives. Escape or a click outside
closes it, returning you to the period you were looking at.

### Drag to reschedule

Dragging an event to another day patches its date property. Two things are
worth knowing:

- **Drag is day-granular.** It changes the day and preserves the time of day.
  Dropping a 09:00 meeting on the next day leaves it at 09:00 — including
  across a daylight-saving boundary, where the underlying instant shifts but
  the wall clock does not.
- **`end_date` moves with `date`.** Both shift by the same whole-day delta in a
  single write, so a three-day event stays three days long.
- **A multi-day event moves relative to the segment you grabbed.** Dragging the
  middle day of an 11–15 event one day right shifts it to 12–16, not "start
  moves to the drop day". Every day of the span highlights while you drag, so
  it is clear the whole event is coming with you.

A user without update permission on an entity cannot drag it.

### Timezones

A `date` property is a calendar date: `2026-03-01` is 1 March everywhere. A
`datetime` is an instant, so which day it occupies depends on the display
timezone set in Settings — `2026-03-01T00:30:00Z` sits on 1 March in UTC and on
28 February in New York. The grid, the event's printed time and a drag all use
that same display timezone, so they never disagree.

### Sharing a source list with a feed

`calendars:` and `feeds:` (see [Calendar feeds](#calendar-feeds)) use the same
source field names,
so a YAML anchor can declare a projection once and serve both an in-app
calendar and a subscribable ICS feed:

```yaml
_task_events: &task_events
  - entity_type: task
    where: ["status != done"]
    date: due_date
    summary: title

feeds:
  tasks:
    sources: *task_events
calendars:
  schedule:
    sources: *task_events
```

The anchor holder must start with an **underscore** (`_task_events`). Unknown
top-level keys are rejected so a config typo is loud, and the underscore is how
you say "this is an anchor, not a section" — YAML resolves anchors at parse
time, so the definition has to live somewhere in the document.

The two config blocks are otherwise independent: a calendar has no `alarm:` or
`rrule:` (export concerns), and a feed has no `event:` or `default_view:` (view
concerns). Extra keys on the other side are ignored, so an anchor carrying
`color:` still parses as a feed source. Keeping them separate means either can
change without the other having to follow.

### Validation

A calendar fails config load — rather than breaking on first use — when:

- a source names an unknown entity type, or a `date`/`summary`/`description`
  property the type does not have
- `date` or `end_date` is not `date`- or `datetime`-typed, or is a `list`
  property (an event needs one date, and a drag would not know which to move)
- `date` and `end_date` are different kinds (one all-day, one timed)
- `date` uses a custom `format:`. Drag writes the property back, and only the
  default `2006-01-02` layout is supported today
- `default_view`, `week_start`, or a `color` token is not a known value
- `day_start` is not before `day_end`
- `edit_form` or `create_form` names a form that does not exist
- an `event.fields` entry matches no source's entity type

### Limits

- Only **month** and **week** views exist today; day and year are not built.
- Recurrence is not expanded — an entity with an `rrule` property renders on its
  base date only. `rrule:` remains a feed-export concern.
- Dragging changes the day, not the time, and events cannot be resized.

## Gantts

A gantt lays entities out against a **continuous time axis**, nested by
containment relations — which work contains which other work, and how those
nested spans relate. It is the third member of the view family: kanban shows
*status* spatially, calendar shows *dates*, a gantt shows *structure over
time*.

```yaml
gantts:
  delivery:
    title: "Delivery plan"
    hierarchy: [contains, has-epic, has-ticket]   # traversed as ONE set
    sources:
      project: { start: planned_start, end: planned_end, committed: target_date }
      epic:    { start: start,         end: end }
      ticket:  { start: scheduled_start, end: due }
```

Reach it from the sidebar with a `gantt:` navigation entry:

```yaml
navigation:
  - label: "Delivery"
    gantt: delivery
```

### Recursive containment

The hierarchy is **not** a fixed set of levels. `hierarchy:` names relation
types traversed parent-to-child as one set, and a self-referential relation
(`contains: from: [project] to: [project]`) nests to unbounded depth — a
project may hold sub-projects holding sub-sub-projects.

Because depth is unbounded, it is **navigated rather than displayed**:
clicking a bar re-roots the chart on that entity and rescales the time axis to
its subtree, with breadcrumbs leading back out. The twisty beside a name
expands a subtree in place instead. `default_depth` picks how many levels are
open on first load; everything deeper stays one click away.

### Planned versus rolled-up spans

Each source maps up to three date roles, all independently optional:

- `start` / `end` — the entity's **own declared window**
- `committed` — an externally promised deadline

An entity with no dates of its own still renders: its span is the **rolled-up
envelope** of its descendants, computed on the server. The two spans are kept
separate on purpose, because their disagreement is the point:

- a child starting before or ending after its parent's declared window renders
  as an **overrun** (a dotted amber region with the planned window drawn as an
  inset) — the plan disagreeing with itself
- a rolled span ending after the `committed` date renders as a **past-commit
  rule** (red diagonal stripes below the bar) — a broken external promise

The two textures differ deliberately — dots versus stripes survive
colour-blindness and greyscale where amber-versus-red alone would not.

### Gantt fields

| Field | Type | Description |
| --- | --- | --- |
| `title` | string | Heading above the chart (default: the config key) |
| `header` / `footer` | string | Markdown rendered above / below the chart |
| `hierarchy` | list | Relation types traversed parent-to-child; at least one required |
| `multi_parent` | string | `first` (default) or `error` — see below |
| `on_cycle` | string | `error` (default), `prune` or `mark` — see below |
| `default_depth` | int | Levels expanded on first load (default 2) |
| `max_depth` | int | Levels per **response**, measured from the response's root (default 10). Drilling re-roots the walk, so deeper levels stay reachable; beyond-cap levels still fold into their ancestor's rolled span |
| `max_nodes` | int | Nodes per response (default 2000); exceeding it flags the response as truncated |
| `sources` | map | Date-role mapping keyed by entity **type** |
| `tooltip` | object | Extra fields on the hover card — `fields:` of `- property:` entries with optional `label:`, shared shape with kanban cards. Property fields only: values come from the redacted entity, so field-level `visible:` policy holds; relation fields are refused at load (neighbor titles would bypass row-gating) |
| `filter_controls` | list | Interactive filters, as on lists and kanbans |

### Source fields

| Field | Required | Description |
| --- | --- | --- |
| `start` / `end` | no | `date`- or `datetime`-typed properties carrying the entity's own window |
| `committed` | no | A date property holding a promised deadline |
| `where` | no | Filter clauses, all ANDed, restricting which entities join the tree |
| `label` | no | Property used as the bar label (default: the type's display property) |
| `color` | no | Palette token, as on calendar sources |

A type reached by traversal but absent from `sources:` still renders — title
only, its span purely rolled up from descendants.

### Multiple parents and cycles

The graph permits shapes a tree cannot show, so both get an explicit policy
rather than a silent default:

- `multi_parent: first` renders an entity with two parents exactly once, under
  the parent that sorts first — deterministic, and roll-ups above it stay
  honest. `error` refuses the request instead, for projects that intend a
  strict tree. There is deliberately no `duplicate`: rendering one entity
  under two ancestors double-counts every roll-up above it.
- `on_cycle` says what happens when containment loops (A contains B contains
  A). `error` (the default) refuses the request; `prune` drops the looping
  component and renders the rest; `mark` renders it in place and flags it.

  Under `mark` the loop is broken at exactly one edge: the entity the loop
  closes back onto keeps its ordinary position under its first parent, and the
  single edge that returned to it is not drawn. That node carries `in_cycle`
  on the wire and a `↻` marker in the SPA, and the entity holding the cut edge
  reports `has_more_children` — so a withheld edge never reads as a leaf. No
  entity is drawn twice, and no roll-up counts one twice.

  Which policy is right depends on the schema. A loop is a data error in a
  project plan that intends a strict tree, so `error` fails loudly; it may be
  a legitimate mutual containment elsewhere, which is what `mark` is for. The
  marker is therefore worded neutrally: it reports that an edge is not shown,
  not that something is broken.

Both `error` policies evaluate against the **requesting principal's visible
subgraph** (hidden entities must not be reportable), so under ACL the same
gantt can refuse for one principal and render for another — if a report of
the error cannot be reproduced, check whether the offending entities are
hidden from you.

### Validation

Loading fails when:

- `hierarchy` is empty or names an unknown relation type
- a source key is not an entity type, or a date role names a property the type
  does not have, is not `date`/`datetime`-typed, or is a `list` property
- a `where` clause does not parse (dropping a filter would silently widen the
  view, so it refuses instead)
- `multi_parent` or `on_cycle` is not a known value — including
  `multi_parent: duplicate`, which is named in the error
- a `label` is omitted for a type with no display property
- a `filter_controls` entry resolves on no source type

### Limits

- The gantt is **read-only** today: no drag-to-reschedule (moving a parent
  under roll-up semantics is a cascade write across all descendants).
- Dependency arrows are not drawn — dependencies are a separate graph from
  containment and deserve their own view.
- Dates render at day granularity; `datetime` values are treated as their date.

## Commands

Commands let you define shell scripts in `data-entry.yaml` that users can trigger from the UI.
Each command receives context-specific JSON on stdin and streams results back to the browser
as toast notifications using the `::rela::` line protocol.

### Configuration

Define commands under the `commands:` key:

```yaml
commands:
  export-json:
    label: "Export JSON"
    script: |
      echo '::rela::{"type":"message","text":"Exporting..."}'
      jq '.' > /tmp/export.json
      echo '::rela::{"type":"file","path":"/tmp/export.json","label":"JSON Export","action":"reveal"}'
    context: entity
    available_on:
      entity_types: [ticket]
    confirm: "Export this entity?"
    env:
      FORMAT: json
```

| Field          | Type   | Description                                            |
| -------------- | ------ | ------------------------------------------------------ |
| `label`        | string | Button text shown in the UI (required)                 |
| `script`       | string | Shell script executed via `sh -c` (required)           |
| `context`      | string | Scope: `entity`, `list`, `view`, or `global` (required)|
| `available_on` | object | Restrict where the button appears (optional)           |
| `confirm`      | string | Confirmation prompt before execution (optional)        |
| `env`          | map    | Custom environment variables (optional)                |
| `auto_open`    | bool   | Inert; removed by `rela migrate` (see File Downloads)  |
| `permission`   | string | ACL permission required to run this command (optional) |

### Authorization

Commands run arbitrary shell, so who may execute them is governed by the ACL.
Authorization is **bimodal** — it depends on whether the project has an
`acl.yaml` at all:

| Project state | Command with `permission:` | Command without `permission:` |
| ------------- | -------------------------- | ----------------------------- |
| No `acl.yaml` | runs | runs |
| `acl.yaml` present | runs only if the principal holds it | **denied** |
| `--read-only` | **denied** | **denied** |

In other words: with no policy configured nothing changes, and once you write a
policy every command must be granted explicitly. Grant via a role's
`permissions:` list, exactly like `history:read`:

```yaml
# data-entry.yaml
commands:
  nightly-export:
    label: "Nightly export"
    context: global
    script: "./scripts/export.sh"
    permission: "command:nightly-export"
```

```yaml
# acl.yaml
roles:
  operator:
    permissions: [command:nightly-export]
```

Commands the current user may not run are omitted from the API response, so
their buttons never render. That is a convenience, not the boundary — the
server re-authorizes every execution and returns 403 regardless of what the UI
showed.

> **`available_on` is not an authorization boundary.** It controls where a
> button *appears*; it is not checked at execution time, so a command scoped to
> one list or entity type can still be invoked directly against any other. Use
> `permission:` to control *who* may run a command, and note that a command
> reads whatever its context assembles from the caller-supplied `entity_id` /
> `list_id` — not from the page the button was on. See
> [what a command permission confers](acl-security.md#what-a-command-permission-actually-confers).
>
> **Adding your first `acl.yaml` is a breaking change for commands.** Every
> command needs a `permission:` and a matching grant, or it stops working. The
> failure mode is deliberate: a denied command is safer than an ungoverned one.

**`context: view` cannot be granted per-command yet.** View commands run
unchanged when no `acl.yaml` is configured, but are denied outright once one
is — setting `permission:` on them has no effect and produces a config warning.
A view command's stdin payload is the entire view traversal (every entity the
view reaches plus the relations between them), so a grant would confer read
access far wider than the entry entity, in a way that is not evident from the
config. Per-command view grants are deferred until that scoping is resolved.

### Context Scopes

Each command runs in one of four scopes, which determines the JSON it receives on stdin:

**`entity`** — runs from entity detail and view pages. Receives the entity with all properties,
content, and relations.

**`list`** — runs from list pages. Receives all entities currently visible in the list (after
filters).

**`view`** — runs from view pages only. Receives the entry entity, all traversed collections,
and relations between them.

**`global`** — runs from the dashboard. Receives only project metadata.

#### Redacted and inaccessible properties

Entities on stdin are the same ACL-filtered entities the page rendered, so a
property hidden from the current user by a `visible:` rule is **absent from
`properties`**. Absence alone is ambiguous — the property may simply never have
been set — so the payload names the withheld ones:

```json
{
  "context": "entity",
  "entity": {
    "id": "P-1",
    "type": "person",
    "properties": {"name": "Ann"},
    "redacted": ["salary"],
    "inaccessible": [{"name": "content", "reason": "git-crypt"}]
  }
}
```

- `redacted` — property names withheld by field-level ACL. Names only; the
  values are not in the payload. Absent or empty when nothing was redacted.
- `inaccessible` — fields whose stored bytes are unreadable (git-crypt
  encrypted with no local key). Distinct from `redacted`: the data is
  unavailable to *everyone* here, not just this user.

A command that writes entities back should treat both as read-only signals and
must not echo them into a write — doing so would erase the hidden values.

### Visibility Rules (`available_on`)

Without `available_on`, a command appears on every page that matches its context. Add
`available_on` to restrict it:

```yaml
available_on:
  views: [ticket_report]      # Only on specific views
  lists: [all_tickets]         # Only on specific lists
  entity_types: [ticket]       # Only for specific entity types
  dashboard: true              # Only on the dashboard (global context)
```

A command appears if **any** condition matches.

### Environment Variables

Commands always receive:

| Variable            | Description                              |
| ------------------- | ---------------------------------------- |
| `RELA_PROJECT_ROOT` | Absolute path to the project root        |
| `RELA_CONTEXT`      | Context type (`entity`/`list`/`view`/`global`) |

Context-specific variables:

| Variable            | Available In         | Description              |
| ------------------- | -------------------- | ------------------------ |
| `RELA_ENTITY_ID`    | entity, view         | Current entity ID, always bare |
| `RELA_ENTITY_TYPE`  | entity, view         | Current entity type      |
| `RELA_ENTITY_FACE`  | entity, view         | Face on screen, empty for a faceless type |
| `RELA_ENTITY_REF`   | entity, view         | Address: `ID@face`, or the bare ID when there is no face |
| `RELA_LIST_ID`      | list                 | Current list ID          |
| `RELA_VIEW_ID`      | view                 | Current view ID          |

Custom variables from `env:` are added to the process environment.

On an entity type that declares [faces](content-states.md), a command runs
against the face the reader has on screen. `RELA_ENTITY_ID` stays bare, so a
script that predates faces keeps working unchanged; `RELA_ENTITY_REF` is the
address `rela update` and the HTTP API accept, so a face-aware script writes
back to the face it read:

```bash
rela update "$RELA_ENTITY_REF" --set reviewed_at="$(date -I)"
```

The payload an entity-context command receives is scoped to the invoking
principal: a row they may not read refuses with the ordinary not-found, a face
they may not read does the same, and a property hidden by `visible:` is absent
rather than raw. A command script therefore sees what its caller sees, never
more.

### The `::rela::` Line Protocol

Commands communicate results by writing lines to stdout with a `::rela::` prefix followed by
JSON. Lines without the prefix are treated as log output.

**Message types:**

| Type       | Purpose                          | Key Fields                            |
| ---------- | -------------------------------- | ------------------------------------- |
| `message`  | Toast notification               | `text`, `level` (info/warning/error)  |
| `error`    | Error toast                      | `text`                                |
| `file`     | Offer a file for download        | `path`, `label`                       |
| `entity`   | Entity update notification       | `id`, `entity_type`, `action` (created/updated/deleted) |
| `group`    | Start a collapsible group        | `label`                               |
| `endgroup` | End the current group            | —                                     |

**Example script:**

```bash
echo '::rela::{"type":"group","label":"Generated Files"}'
echo '::rela::{"type":"file","path":"out/report.pdf","label":"PDF Report"}'
echo '::rela::{"type":"file","path":"out/data.csv","label":"CSV Data"}'
echo '::rela::{"type":"endgroup"}'
echo '::rela::{"type":"message","text":"Done!","level":"info"}'
```

### File Downloads

A `file` message renders a **Download** button. Clicking it downloads the file
through the server to the browser that ran the command.

**The file must live inside the project root.** A relative `path` is resolved
against it; an absolute path outside it is refused. A refused file is still
listed, but without a download button — so write output somewhere under the
project directory (`out/` above), not to `/tmp`.

`label` is what the user sees and the name the file is saved as. It defaults to
the basename of `path`.

The server never sends the file's path to the browser. It sends an opaque
one-time token, and the download endpoint re-checks the command's
`permission:` against the current policy on every request. A token therefore
stops working when the permission is revoked or the server is restarted with
`--read-only`, and it expires 30 minutes after its command finishes.

> **Changed:** earlier versions ran an OS "open"/"reveal" launcher on the
> machine hosting the server, and `auto_open: true` fired it automatically.
> That only ever made sense for a desktop install — on a server it silently did
> nothing, and had it worked it would have opened the file on the server rather
> than for the user. Downloads replace both. The `auto_open` key is still
> accepted so existing configs keep loading, but it has no effect and is no
> longer sent to the browser. Run `rela migrate` to remove it from your
> `data-entry.yaml`.

### Streaming and Cancellation

Command output streams in real time into stacked toast notifications. Long-running commands
can be cancelled by the user via a cancel button.

## User Defaults

The data entry app includes a **Settings** page where users can configure default values for
properties and relations. These defaults are applied automatically when creating new entities,
reducing repetitive data entry.

### Storage

User defaults are stored in `.rela/user-defaults.yaml` (gitignored, per-user). The file is
created automatically when a user saves settings for the first time.

```yaml
# .rela/user-defaults.yaml
defaults:
    assignee: alice
    priority: high
relations:
    belongs-to: backend
overrides:
    - entity_types:
        - ticket
      defaults:
          reporter: bob
      relations:
          tagged: bug
```

### Settings Page

The Settings page is accessible from the sidebar (gear icon at the bottom). It has three sections:

**Property Defaults** — Set default values for any property defined in the metamodel. The widget
type (text input, dropdown, date picker, etc.) matches the property's type. For enum/custom types,
a dropdown shows the allowed values.

**Relation Defaults** — Set a default target entity for any relation type. When creating a new
entity, the relation will be pre-filled with this target.

**Overrides** — Scope defaults to specific entity types. For example, set `priority: critical`
only when creating tickets, while leaving the global default as `medium`.

### Resolution Order

When creating a new entity, default values are resolved in this order (highest priority first):

1. **Entity-type override** from user defaults (e.g., ticket-specific override)
2. **Global user default** (e.g., `assignee: alice`)
3. **Form-level default** (from `data-entry.yaml`, e.g., `default: medium`)
4. **Metamodel default** (from `schema.yaml` type definition)

User defaults never override values explicitly set by the user in the form.

## Complete Example

A ticket management system with forms, lists, views, dashboard, and grouped navigation:

```yaml
version: "1.0"

app:
  name: "Support Tickets"
  description: "Internal ticket management"

git:
  require_pr: [main]

styles:
  ticket_status:
    open: blue
    in-progress: purple
    resolved: green
    closed: gray
  priority:
    critical: red
    high: orange
    medium: yellow
    low: green

forms:
  create_ticket:
    entity_type: ticket
    title: "New Ticket"
    body: true
    fields:
      - property: title
        label: "Title"
        required: true
      - property: priority
        label: "Priority"
        default: medium
      - property: assignee
        label: "Assignee"
      - property: due_date
        label: "Due Date"
        widget: date
      - property: status
        hidden: true
        default: open
    relations:
      - relation: belongs-to
        direction: outgoing
        target_type: category
        label: "Category"
        widget: select

  edit_ticket:
    entity_type: ticket
    title: "Edit Ticket"
    mode: edit
    body: true
    fields:
      - property: title
        label: "Title"
      - property: status
        label: "Status"
        transitions:
          open: [in-progress, closed]
          in-progress: [open, resolved]
          resolved: [closed, in-progress]
          closed: [open]
      - property: priority
        label: "Priority"
      - property: assignee
        label: "Assignee"
      - property: due_date
        label: "Due Date"
        widget: date

  create_category:
    entity_type: category
    title: "New Category"
    fields:
      - property: name
        label: "Name"
        required: true

actions:
  resolve-ticket:
    label: "Resolve"
    key: "r"
    set:
      status: resolved
  close-ticket:
    label: "Close"
    key: "c"
    confirm: true
    set:
      status: closed

lists:
  all_tickets:
    entity_type: ticket
    title: "All Tickets"
    columns:
      - property: title
        label: "Title"
        sortable: true
        link: true
      - property: status
        label: "Status"
        sortable: true
      - property: priority
        label: "Priority"
        sortable: true
      - property: assignee
        label: "Assignee"
      - property: due_date
        label: "Due"
        sortable: true
    sort:
      property: priority
      direction: asc
    filter_controls:
      - property: status
        widget: multi-select
      - property: priority
        widget: select
    create_form: create_ticket
    edit_form: edit_ticket
    actions: [resolve-ticket, close-ticket]
    page_size: 25

  open_tickets:
    entity_type: ticket
    title: "Open Tickets"
    columns:
      - property: title
        link: true
        sortable: true
      - property: priority
        sortable: true
      - property: assignee
    filters:
      - property: status
        operator: "="
        value: open
    create_form: create_ticket
    edit_form: edit_ticket
    page_size: 25

  my_tickets:
    entity_type: ticket
    title: "My Tickets"
    columns:
      - property: title
        link: true
        sortable: true
      - property: status
        sortable: true
      - property: priority
        sortable: true
    filters:
      - property: assignee
        operator: "="
        value: "$USER"
    create_form: create_ticket
    edit_form: edit_ticket
    page_size: 25

entity_views:
  ticket:
    detail_view: ticket_detail

views:
  ticket_detail:
    title: "Ticket Detail"
    entry:
      type: ticket
    traverse:
      - from: entry
        follow: blocks
        collect_as: blocks
      - from: entry
        follow_incoming: blocks
        collect_as: blocked_by
    sections:
      - heading: "Ticket"
        source: entry
        display: properties
        fields:
          - property: status
          - property: priority
          - property: assignee
          - property: due_date
            label: "Due Date"
      - source: entry
        display: content
      - heading: "Blocks"
        source: blocks
        display: cards
        fields:
          - property: status
          - property: priority
        empty_message: "Not blocking any tickets"
      - heading: "Blocked By"
        source: blocked_by
        display: cards
        fields:
          - property: status
        empty_message: "Not blocked"

dashboard:
  title: "Dashboard"
  description: "Ticket overview"
  cards:
    - title: "Open Tickets"
      query: "type:ticket status:open"
      display: count
    - title: "By Status"
      query: "type:ticket"
      display: breakdown
      group_by: ticket_status
    - title: "Critical"
      query: "type:ticket prop:priority=critical"
      display: table
      columns:
        - property: title
          label: "Title"
          link: true
        - property: assignee
          label: "Assignee"
      limit: 5

commands:
  generate-pdf:
    label: "Generate PDF"
    script: |
      PDF="out/ticket-${RELA_ENTITY_ID}.pdf"
      # ... generate PDF ...
      echo "::rela::{\"type\":\"file\",\"path\":\"$PDF\",\"label\":\"Ticket PDF\"}"
    context: entity
    available_on:
      entity_types: [ticket]

kanbans:
  ticket_board:
    entity_type: ticket
    title: "Ticket Board"
    column_property: ticket_status
    columns:
      - value: open
        label: "📥 To Do"
      - value: in-progress
        label: "🔧 In Progress"
      - value: resolved
        label: "✅ Done"
    card:
      title: title
      fields:
        - property: priority
        - property: assignee
    edit_form: edit_ticket
    create_form: create_ticket

navigation:
  - label: "Dashboard"
    dashboard: true
  - group: "Tickets"
    items:
      - label: "My Tickets"
        list: my_tickets
      - label: "Open Tickets"
        list: open_tickets
      - label: "All Tickets"
        list: all_tickets
      - label: "Ticket Board"
        kanban: ticket_board
```

## Analysis

The data entry app includes a built-in analysis page at `/analyze` that runs the same quality
checks as the CLI's `rela analyze all` command. It checks properties, cardinality constraints,
custom validations, orphans, duplicates, and ID gaps — displaying results grouped by category
with links to affected entities.

When a dashboard is configured, a validation summary card is automatically appended showing the
total error and warning counts with a link to the full analysis page.

In each issue row the entity title links to that entity, while the message is a
separate click target that reveals more about the failure. For a
`content.required-headers` validation, clicking the message expands a detail row
listing exactly which required headers the entity is missing (only exact-match
headers; regex `pattern:` checks are not listed). A validation whose Lua script
failed instead opens the script-error dialog from the same message click.

No configuration is needed — the analysis page is always available in the sidebar.

## Documents

Documents are read-only rendered markdown, produced either by a shell
`command:` that writes markdown to stdout or by a Lua `script:` that does the
same via the embedded runtime.

There are two kinds, distinguished by whether `entity_type:` is set:

| Kind | `entity_type:` | URL | Where it appears |
|------|----------------|-----|------------------|
| **Entity-anchored** | set | `/document/<name>/<entity_id>` | Panel on the entity's detail view |
| **Standalone** | omitted | `/document/<name>` | Sidebar, via a `document:` navigation entry |

Use an entity-anchored document for content *about one entity* (a release's
notes, a ticket's summary). Use a [standalone document](#standalone-documents)
for content that is company-wide — a periodic sales report aggregated across
many types — which would otherwise have to be anchored to an arbitrary entity
that does not actually drive its content.

Captured markdown is converted to HTML via goldmark. Links using app-relative
paths (e.g. `/form/<form_id>/<entity_id>`, `/entity/ticket/TKT-001`) get a
`return_to` query param appended automatically on form links so the user lands
back on the document after submitting the form. See "Links in rendered
documents" below.

Both kinds carry an **"Export ▾"** menu when the metamodel registers any
`transforms:`, converting the document's markdown to PDF/DOCX/etc. Export is
gated exactly like the render — same checks, same order — so a document you may
view is one you may export. Documents using a `command:` renderer are not
exportable. See [View Export & Transforms](transforms.md#exporting-a-document).

The frontend's `DocumentsPanel.vue` shows every entity-anchored document whose
`entity_type` matches the current entity. SSE live-reload re-renders a document
when any entity changes (see the "SSE live-reload" caveat below).

An entity-anchored document is also reachable on its own page at
`/document/<name>/<entity_id>` (used by `rela.url.document` links and direct
deep-links). On that page the header shows Back and Refresh by default; add
an `edit:` block to the doc config to also expose an Edit button that takes
the user to a configured form, with a `return_to` query param so saving
returns to the document.

### YAML schema

```yaml
documents:
  release_notes:
    title: "Release Notes"         # shown as the panel title
    entity_type: release           # renderer runs only for this type
    script: docs/release_notes.lua # OR command: — exactly one must be set
    timeout: 15                    # seconds; defaults to 30
    edit:                          # optional; renders an Edit button on the
      form: edit_release           # full-page /document/... view
      label: "Edit release"
  ticket_summary:
    title: "Ticket Summary"
    entity_type: ticket
    command: ["my-renderer", "{in}"] # argv array; {in} = entity markdown file
    timeout: 30
  sales_review:                    # standalone — no entity_type
    title: "Verkooprapportage"
    script: docs/sales_review.lua
    permission: report:sales       # optional; see "Gating a document"
```

| Field         | Type   | Description                                                       |
| ------------- | ------ | ----------------------------------------------------------------- |
| `title`       | string | Display title                                                     |
| `entity_type` | string | Entity type this document is about. Omit for a standalone document |
| `command`     | string | Shell command producing markdown on stdout (exclusive with `script`) |
| `script`      | string | Lua script under `scripts/` (exclusive with `command`)            |
| `timeout`     | int    | Render timeout in seconds; defaults to 30                         |
| `permission`  | string | Global named permission required to render (optional; REQUIRED when `allow_acl_bypass` is set) |
| `allow_acl_bypass` | string | `read` — let the script read past the caller's ACL (optional; see "Elevated documents") |
| `edit`        | map    | Edit button config; entity-anchored documents only                |

Validation is strict: exactly one of `command:` or `script:` must be non-empty.
Configs with both, or with neither, are rejected at startup. For `script:`
docs, the referenced file is checked for existence at startup too, so typos
fail loudly instead of at the first HTTP request. When an `edit:` block is
present, both `form:` and `label:` are required and `form:` must reference a
known form ID. Note that a bare `edit:` line with no subkeys is treated as
"field absent" (no button, no validation error); to catch a stub block write
`edit: {}` instead so the required-field checks fire.

`entity_type:` is optional — omitting it declares a standalone document rather
than being an error. An `edit:` block on a standalone document is rejected,
since there is no entity for the button to open.

### Standalone documents

A document with no `entity_type:` renders company-wide content — typically a
report aggregated across many entity types — at `/document/<name>`, with no
entity id in the URL. Reach it from the sidebar with a `document:` navigation
entry:

```yaml
documents:
  sales_review:
    title: "Verkooprapportage"
    script: docs/sales_review.lua

navigation:
  - label: "Dashboard"
    dashboard: true
  - label: "Verkooprapportage"
    document: sales_review
```

Rules:

- **Script-only.** A `command:` renderer is handed the entry entity as its
  `{in}` file, and a standalone document has no entry entity, so `command:` is
  rejected rather than run against an empty or guessed one.
- **`rela.document.entry_id` is `nil`** (not `""`). Scripts should already
  tolerate this — list-render mode has behaved the same way since it shipped.
- **Only standalone documents can be navigation entries.** Pointing
  `document:` at an entity-anchored document is a config error: the sidebar has
  no entity id to put in the URL.
- **No disk caching.** Command renders cache on the entry entity's hash, and
  there is no entry entity here. Use `rela.cache.memoize` inside the script.
- **SSE live-reload re-renders on any entity change**, since the document
  declares no dependencies. Until `rela.document.depends_on` lands (TKT-E1FO1),
  the Refresh button is the reliable way to update a stale report.

### Gating a document

`permission:` restricts a document to principals holding a global named
permission granted through a role's `permissions:` list in `acl.yaml` — the
same mechanism behind `history:read` and the `delegate-*` family:

```yaml
# data-entry.yaml
documents:
  sales_review:
    script: docs/sales_review.lua
    permission: report:sales

# acl.yaml
roles:
  directie:
    permissions: [report:sales]
```

A principal without the permission gets a **403** naming the document and the
required permission, and the renderer never runs.

The menu is **not** filtered: a `document:` navigation entry is shown to every
principal, and `/api/v1/_config` serves the document config in full. That is
deliberate and matches the rest of the app — see
[ACL security](acl-security.md#sidebar-menu-structure-is-principal-independent).
Your `data-entry.yaml` is an operator-authored file that lives in your repo, so
document names, script paths, and permission names are not secrets; hiding them
from the API would buy nothing and make the menu differ per user for no gain.
A user may therefore see an entry that 403s, which is the accepted trade: an
actionable error beats a silently divergent menu.

What *is* protected is the entity data behind the document, by the read ACL —
that is the boundary, and it does not depend on `permission:` at all.

Note this differs from `commands:`, which **fails closed** under a configured
`acl.yaml`: a command without `permission:` is denied. Documents fail open
because their content is already bounded by the read ACL, while a command
shells out and its side effects are not. If you are adding both to the same
config, the identical-looking `permission:` key has opposite defaults.

`permission:` is **optional, and its absence is not an oversight.** Document
*content* is already bounded by the ACL: a document's Lua reads go through the
same gated reader as every other read path, so a principal who cannot read the
underlying entities renders an empty or partial report either way. It is not
the confidentiality boundary, so requiring it everywhere would be ceremony.

What it *does* buy on an ordinary document is honesty about scope. A report
titled "company-wide revenue" rendered for someone who can see a tenth of the
rows shows a smaller number that looks just as authoritative — nothing leaked,
but the page now asserts something untrue. Use `permission:` when a report
makes a claim its reader may not be able to compute, when the *composition* is
sensitive even though the parts are readable, or simply to keep reports a user
cannot act on out of their menu.

### Elevated documents (`allow_acl_bypass: read`)

Some reports must compute over rows the reader is not allowed to see. Consider
sales managers who each own a client set and cannot see the others — not just
the figures, but the clients' *existence*. A report benchmarking a manager
company-wide, or against the top performer, cannot be built from their own
view, and no `acl.yaml` role fixes it: granting enough to **compute** the
benchmark grants enough to **enumerate** the competitors.

`allow_acl_bypass: read` lets that document's script read past the caller's
ACL:

```yaml
documents:
  sales_benchmark:
    title: "Verkoop benchmark"
    script: docs/benchmark.lua
    allow_acl_bypass: read
    permission: report:sales
```

The script uses `rela.bypass_acl(fn)` for the reads that must span the graph;
everything outside that closure stays gated as usual. Elevated reads are
audited (`acl-bypass-read`).

**`permission:` is required here**, and this is the one case where it is the
confidentiality boundary rather than an intent gate — an elevated render has
nothing downstream bounding its output. A configured `acl.yaml` is required
too: with no policy the permission names a capability nothing can withhold, so
the document is refused rather than served to everyone.

**Only `read` is accepted.** A render is served on a `GET`, so `write` and
`read+write` are a config error: elevated writes there would not be idempotent
(browsers prefetch, users refresh, the SPA retries) and would foreclose caching
an otherwise principal-independent render. Put them in an automation action or
a schedule instead.

Note what this rule does and does not do. A document script *can* already write
through the ordinary `rela.create_entity` / `update_entity` / `delete_entity`
bindings, bounded by your own permissions — refusing `write` here prevents a
render mutating **beyond** them, it does not make rendering read-only.

**The script is trusted code.** Nothing stops it printing the rows it read
instead of a statistic derived from them, so `permission: report:sales` really
grants "may read whatever this script reads". Review the `bypass_acl` block
before deploying, and treat the permission as equivalent to the read access the
script performs.

On an entity-anchored document `permission:` applies *in addition to* the
per-entity read gate — it narrows access, and can never widen it.

### External command renderer (`command:`)

`command:` is an **argument array**, executed directly. There is no shell, so
pipes, redirection, globbing, and variable expansion are not available — and no
quoting or escaping is needed. The program must write the rendered markdown to
stdout.

```yaml
command: ["my-renderer", "{in}"]
```

| Placeholder | Expands to |
|-------------|------------|
| `{in}`      | Path to a temp file holding the entry entity's markdown, frontmatter included |

The entity id is the `id:` key of that file's frontmatter, so a renderer that
needs it reads it from the file.

> **Migrating from `{id}` / `{id_lower}`**
>
> Those placeholders were removed. They spliced a request-derived value into a
> shell string, which made the entity id the one piece of user-controlled data
> reaching `sh -c` — an id beginning with `-` arrived as an option flag rather
> than an operand. A config still using them is **rejected at load** with an
> error naming `{in}`.
>
> Two other things changed with the shell:
>
> - **A string `command:` is no longer valid**; use an array.
>   `command: "my-renderer"` → `command: ["my-renderer"]`.
> - **The working directory is no longer the project root.** Name the program
>   on `PATH` or by absolute path rather than relying on a relative path such
>   as `render.sh`.
>
> - **A sandbox is now required.** Commands run confined via `internal/cmdexec`
>   (bubblewrap on Linux, `sandbox-exec` on macOS) and **fail closed**: on a host
>   with no mechanism available, the render is refused rather than run
>   unconfined. Previously `command:` ran through a bare `sh -c` with no
>   confinement.
>
>   On Linux, installing bubblewrap is necessary but **not always sufficient**:
>   bwrap also needs unprivileged user namespaces. Distributions that restrict
>   them (Ubuntu 23.10+ with `kernel.apparmor_restrict_unprivileged_userns=1`,
>   or `kernel.unprivileged_userns_clone=0`) will refuse renders even with
>   bubblewrap installed. The server logs the specific reason at startup under
>   `external command confinement`.
>
> Shell features (pipes, redirection) are unavailable by design. If you need
> them, put them in a script file and invoke that script as the program.

Command renderer output is cached to disk at
`.rela/documents/<entry>-<hash>.html` keyed by an FNV hash of the entry
entity. Subsequent requests for the same entity skip the command and serve
the cached HTML until the entity changes.

### Lua script renderer (`script:`)

The `script:` field is a path under the project's `scripts/` directory
(e.g. `docs/release_notes.lua`). The script runs with a writer runtime —
it can read the full graph, call `ai.chat`, and use `rela.cache.memoize` —
but anything it writes to stdout (via `print()`) is captured as the
document's markdown.

When invoked in document mode, the runtime exposes extra context:

| Variable                   | Meaning |
|----------------------------|---------|
| `rela.mode`                | Always `"document"` in this context; `nil` elsewhere |
| `rela.document.id`         | The key under `documents:` in `data-entry.yaml` |
| `rela.document.entry_id`   | The ID of the entity being rendered; **`nil` for a standalone document** |

A script shared between both document kinds should branch on `entry_id`
rather than assume it:

```lua
if rela.document.entry_id then
  -- entity-anchored render
else
  -- standalone render: aggregate across the graph
end
```

`entry_id` is absent (Lua `nil`), never an empty string — `""` is truthy in
Lua, so the guard above would take the wrong branch and then fail.

Example — a document that composes a markdown doc from an entity plus its
graph neighbours:

```lua
-- scripts/docs/release_notes.lua
local entry = rela.get_entity(rela.document.entry_id)
print("# " .. (entry.properties.title or entry.id))
print()
for _, child in ipairs((rela.trace_from(entry.id, 2) or {children = {}}).children) do
  local e = rela.get_entity(child.id)
  if e then
    -- rela.url.form_edit builds an edit-form URL; rela.url.detail
    -- would be an alternative that links to the canonical detail page.
    -- rela.md.link emits [text](url) so we don't hand-concatenate markdown.
    local href = rela.url.form_edit("full_ticket", e)
    print("## " .. rela.md.link(e.id, href))
    print(e.content or "")
  end
end
```

Lua renders bypass the disk cache. In-process caching is available through
`rela.cache` (see [Lua Scripting → Cache](GUIDE-lua-scripting.md#cache) for
the full API). Memoized work is shared across HTTP requests within the
lifetime of the `rela-server` process. The cache namespace is the script
path, not the document's config key — shared helper scripts intentionally
share cache state across all documents that use them; if you need
doc-scoped keys, include `rela.document.id` in your cache key explicitly.

`rela.output({...})` in document mode emits a warning line into the
rendered document (captured stdout is the document body, so raw JSON in the
middle of markdown is almost always a mistake). If you need to output
data, use `print()`; if you're debugging, a warning line is usually fine.
A script that calls `rela.output` inside a loop will produce many warning
lines in the rendered output — that is intentionally loud.

### Links in rendered documents

Documents link to anywhere in the SPA by writing app-relative paths. The
goldmark→HTML step walks every `href="/..."` attribute and appends a
`return_to` query param. Every screen reachable from a document link
(entity detail, list, kanban, view, custom view, another document,
search, analyze) surfaces a "← Back" affordance — see
[Back navigation](#back-navigation) below.

| Target                | Write this in markdown                          | Notes                               |
|-----------------------|-------------------------------------------------|-------------------------------------|
| Edit an entity        | `[Edit](/form/full_ticket/TKT-001)`             | Adds `return_to=...`; stable `id="edit-tkt-001-<n>"` on the link for scroll-back |
| Create a new entity   | `[New](/form/full_ticket)`                      | Adds `return_to=...`; stable `id="create-full_ticket-<n>"` |
| Create with defaults  | `[New](/form/full_ticket?prop.status=open)`     | Preserves query + appends `return_to` |
| Link to entity detail | `[Detail](/entity/ticket/TKT-001)`              | Adds `return_to=...` — EntityView renders a Back button |
| Link to a list        | `[All](/list/all_tasks)`                        | Adds `return_to=...` — ListView renders a Back button |
| Link to a kanban      | `[Board](/kanban/sprint)`                       | Adds `return_to=...` — KanbanView renders a Back button |
| External link         | `[Docs](https://example.com)`                   | Untouched                           |

The rewriter is the single source of truth for `return_to` on emitted
HTML: any author-supplied `return_to` on an internal link is stripped
(with a warning) and replaced with one the server controls. The legacy
`edit://` / `create://` schemes log a warning and pass through unchanged
so downstream projects notice and migrate. Cached document renders
(`.rela/documents/<entry>-<hash>.html`) are `return_to`-free; the
rewrite happens after the cache read, so two viewers of the same entry
under different `return_to` values each get their own value rewritten
in.

### Back navigation

A view rendered from a document link shows a Back button that returns
the user to the source document. The button follows a simple
precedence:

1. `?return_to=<path>` — set by the rewriter. Validated by the
   open-redirect guard both server-side and client-side; unsafe values
   (protocol-relative `//evil.com`, percent-encoded separators,
   schemed URLs) are rejected silently.
2. `?from=<list-id>` — legacy affordance used by EntityView's scope
   navigation (Prev/Next through a list). When present, the Back
   button points to `/list/<id>` and is labelled `← <list title>` if
   the list is configured.
3. Neither — no Back button renders. The user navigated in directly
   (sidebar, bookmark), not from a back-able context.

Scope navigation (Prev/Next through a list) is independent of the Back
mechanism: if a user arrives at an entity via `?from=tasks&return_to=/doc`,
Back follows `return_to` (the document) while Prev/Next still walks the
`tasks` list and preserves `return_to` across in-list navigation.

### Building links from Lua: `rela.url`

Document scripts build URLs via the `rela.url` submodule. Each helper
corresponds to one route kind the SPA exposes. Helpers are pure string
builders — a typo in a form name produces a syntactically valid URL; the
404 surfaces in the SPA when the user clicks.

| Helper | Returns | Typical use |
|--------|---------|-------------|
| `rela.url.form_edit(name, entity)` | `/form/<name>/<entity.id>` | Edit-link for an entity, using form `<name>` |
| `rela.url.form_create(name, {relations, properties, query})` | `/form/<name>?…` | Create-link with pre-filled relations/properties |
| `rela.url.form_create(name)` | `/form/<name>` | Create-link with no pre-fill |
| `rela.url.detail(entity)` | `/entity/<entity.type>/<entity.id>` | Canonical entity detail page |
| `rela.url.list(name, query?)` | `/list/<name>?…` | Link to a configured list |
| `rela.url.view(name, entity)` | `/view/<name>/<entity.id>` | Custom view for an entity |
| `rela.url.kanban(name, query?)` | `/kanban/<name>?…` | Kanban board |
| `rela.url.document(name, entity)` | `/document/<name>/<entity.id>` | Render a different document for an entity |
| `rela.url.home(query?)` | `/dashboard?…` | App home |
| `rela.url.search(query?)` | `/search?…` | Full-text search |
| `rela.url.analyze(query?)` | `/analyze?…` | Graph analysis |
| `rela.url.settings(query?)` | `/settings?…` | App settings |
| `rela.url.conflicts(query?)` | `/conflicts?…` | Git conflicts |

Every frontend route has a typed helper. The `query?` parameter on
non-form helpers is an optional flat table of `{key = value}` pairs —
no `{query = {...}}` wrapping.

`form_edit` and `form_create` are split (not one overloaded `form(...)`) so
an author who writes `rela.url.form_create("x", {id = "prefill-x"})` meaning
"create with a prefilled id property" gets a create form — not silently
routed to edit mode on the basis of a structural check of the opts table.

`form_create`'s opts table keeps the three-sub-key shape (`relations`,
`properties`, `query`) because it has three distinct semantics — the
helper adds the `rel.` and `prop.` prefixes the form expects, and
`query` is for passthrough.

Examples:

```lua
local ticket = rela.get_entity("TKT-001")

-- Edit the ticket in the "full_ticket" form.
rela.url.form_edit("full_ticket", ticket)
-- → "/form/full_ticket/TKT-001"

-- Create a new ticket pre-filled with relations and properties. Relation
-- and property names are taken from the metamodel; the helper adds the
-- "rel." / "prop." prefixes the form expects.
rela.url.form_create("create_ticket", {
  relations  = {parent = ticket.id, assignee = "actor-me"},
  properties = {status = "open", priority = "high"},
})
-- → "/form/create_ticket?prop.priority=high&prop.status=open&rel.assignee=actor-me&rel.parent=TKT-001"

-- Canonical detail page — no form choice, always safe.
rela.url.detail(ticket)
-- → "/entity/ticket/TKT-001"

-- Singleton with a query param.
rela.url.search({q = "pseudoniem"})
-- → "/search?q=pseudoniem"
```

Form links get a `return_to` query parameter injected by the document
link rewriter so submitting the form returns the user to the document.
`return_to` is reserved — setting it in any helper's query table is
rejected with a Lua error.

#### Pre-filling a create form

`form_create` accepts three kinds of defaults in its opts table; each
maps to a query-param convention the create form reads on mount:

| Opts key     | Query form       | What the form does on mount                          |
|--------------|------------------|------------------------------------------------------|
| `relations`  | `rel.<name>=<id>` | Adds `<id>` to the named relation's targets          |
| `properties` | `prop.<name>=<v>` | Sets the property's initial value                    |
| `query`      | `<k>=<v>`         | Passed through verbatim (use for custom URL params)  |

The form applies these defaults only on initial mount; the user can
still edit or clear each field before submitting. Multiple values for
the same relation accumulate (call `form_create` with a list-shaped
value only if the metamodel permits multi-target for that relation).

```lua
-- A "+ Add sub-ticket" link that pre-selects the parent and puts the new
-- ticket straight into the correct category:
rela.url.form_create("create_ticket", {
  relations  = {parent = ticket.id, ["belongs-to"] = ticket.properties.category},
  properties = {priority = "medium", reporter = "actor-me"},
})
```

Defaults set via link query string are overlaid on top of the project's
`.rela/user-defaults.yaml` and metamodel-level defaults; the order is
covered in the **User defaults** section earlier in this guide.

### Caching and live-reload

- **Command renders** are cached on disk (`.rela/documents/<entry>-<hash>.html`).
  The hash includes only the entry entity, so the cache refreshes when the
  entry entity changes.
- **Script renders** are not cached on disk. Use `rela.cache.memoize` inside
  your script to share work across requests within the same server process.
- **SSE live-reload** refreshes a document when the entry entity changes.
  Multi-entity composition (a script that walks 20 related tickets) will
  only re-render when the **entry** entity changes, not the walked ones.
  The refresh button in the UI is the escape hatch. A follow-up ticket
  (TKT-E1FO1) tracks the fix for explicit dependency tracking.
- **Standalone documents** have no entry entity, so neither the disk cache nor
  entry-scoped reload applies. They are script-only (use `rela.cache.memoize`),
  and the same TKT-E1FO1 limitation applies more broadly: a standalone report
  is refreshed on demand.

### Security notes

- Document scripts run in the same sandbox as action scripts: no `io`, no
  `os`, no `debug`; file writes are confined to `output/` via
  `rela.write_file`. Treat Lua scripts as trusted code.
- The HTTP handler enforces `entity_type:` on every request: a document
  configured for `entity_type: release` cannot be rendered against a
  ticket, even if the caller bypasses the frontend filter.
- The two URL shapes are not interchangeable: requesting an entity-anchored
  document without an id, or a standalone document with one, is a 400. Neither
  falls back to rendering against a guessed or empty entity.
- Document scripts read through the same ACL-gated reader as the rest of the
  API, so a standalone document renders only what its caller may already see —
  it is not a way to bypass read gating. Add `permission:` when the aggregate
  itself should be restricted (see "Gating a document").
- Rendered markdown uses goldmark's `html.WithUnsafe` — the frontend
  (DOMPurify) is the sanitization boundary. If you add another consumer of
  the rendered HTML (PDF export, copy-HTML button, etc.), it must add its
  own sanitization.

### Config hot-reload

Editing `data-entry.yaml` to change a document's `script:` or `command:`
takes effect on the next request; open document panels pick up the new
renderer on their next reload. A reload that fails its checks, for example
because the new `script:` does not exist, keeps the previous config. See
"Reloading the file while the server runs".

## Calendar feeds

Feeds publish your entities as subscribable calendars. A feed is served as
iCalendar (`.ics`) and JSON at `/api/v1/_feeds/<name>.<ext>`, so a calendar app
(Apple Calendar, Google Calendar, Thunderbird, …) can subscribe to a URL and see
your tasks, deadlines, or events — each linking back into the data-entry app.

Feeds are declarative: you map entity properties to calendar fields. No scripting.

```yaml
feeds:
  tasks:                          # → /api/v1/_feeds/tasks.ics and .json
    meta:
      name: "PIM tasks"           # calendar display name (default: the feed key)
      color: "#C2185B"            # optional calendar color
      description: "Open tasks"   # optional
    sources:
      - entity_type: task
        where:                    # filter clauses, ALL ANDed (see below)
          - "status != done"
          - "due_date != "        # only tasks that have a due date
        date: due_date            # date property → the event's day
        summary: title            # property → event title (optional; see below)
        description: notes        # property → event description (optional)
        alarm: "-PT9H"            # optional reminder, 9h before (RFC 5545 duration)
        rrule: "FREQ=DAILY"       # optional recurrence (see below)
```

### Sources and merging

A feed has one or more **sources**. Each source projects a single entity type
into events. All sources merge into one calendar — so a single calendar can mix
`meeting` and `party` entities, each mapped its own way:

```yaml
feeds:
  social:
    sources:
      - entity_type: meeting
        where: ["status != cancelled"]
        date: date
        summary: title
      - entity_type: party
        date: date
        summary: name
```

Multiple sources are also how you express **OR**: the filter language has no
`or`, so "high-priority tasks OR overdue tasks" is two sources over the same type.

### Source fields

| Field | Required | Meaning |
|-------|----------|---------|
| `entity_type` | yes | The entity type to project. |
| `where` | no | A list of filter clauses, all ANDed. Empty = all entities of the type. |
| `date` | yes | A **date**- or **datetime**-typed property mapped to the event start. A `date` property yields an all-day event; a `datetime` property yields a **timed** event (rendered in UTC). Entities without a value are skipped. |
| `end_date` | no | A property for the event's end. Must be the **same kind** as `date` (both `date` or both `datetime`) — a feed event is all-day or timed, not a mix. Omit for single-day / no-end events. |
| `summary` | no | A property mapped to the event title. Defaults to the entity type's display property. |
| `description` | no | A property mapped to the event description. |
| `alarm` | no | A static RFC 5545 duration (e.g. `-PT9H`, `-P1D`) for a reminder before the event. |
| `rrule` | no | A recurrence rule — see below. |

### Filters (`where`)

`where` uses the same filter language as lists. It is a **list of
`property operator value` clauses, all ANDed** — there is no OR, NOT, or
parentheses (use multiple sources for OR). Operators: `=` (glob), `!=`, `<`,
`<=`, `>`, `>=`, `=~` (regex), `~` (fuzzy). Dates compare chronologically:

```yaml
where:
  - "due_date >= 2026-01-01"   # typed date comparison (unquoted value)
  - "due_date != "             # existence: "!=" with an empty value = "has a value"
  - "status != done"
```

### Recurrence (`rrule`)

`rrule` makes a source's events repeat. Its value is interpreted by **syntax**:

- A value containing `=` is a **literal** RFC 5545 rule applied to every event:
  `rrule: "FREQ=DAILY"`, `rrule: "FREQ=WEEKLY;COUNT=10"`. Validated at load.
- A **bare property name** reads the rule from that property per entity:
  `rrule: recurrence` (where `recurrence` holds an RRULE string). An invalid
  value in the property is dropped for that event rather than breaking the feed.

A common use is keeping open items visible: an **unbounded** `rrule: "FREQ=DAILY"`
makes each event repeat from its date onward, so an overdue task stays on today's
calendar until it leaves the feed (e.g. your `status != done` filter drops it
once done). An unbounded daily rule paints an all-day block on every future day
for each matching entity — use `;COUNT=N` or `;UNTIL=…` to bound it if that's too
dense.

### The events

Each entity becomes one event — **all-day** when the `date:` source is a `date`
property, or **timed** (a UTC `DTSTART` with a time-of-day) when it is a
`datetime` property:

- **UID** is `<type>--<id>@rela` — stable across refreshes so a calendar client
  tracks the same event over time.
- **Deep link** — every event carries an absolute `URL` back to the entity in the
  data-entry app (Apple Calendar shows it in the event's Get Info panel).
- **JSON** — the same feed at `.json` returns `{ name, color, events: [...] }`
  for non-calendar consumers (a menubar plugin, a notification script).

### Serving and access

Point your calendar app at
`http://<host>:<port>/api/v1/_feeds/<name>.ics`. The endpoint applies the
server's ACL in both dimensions: an entity the request's principal may not read
is absent from the feed, and a property hidden from them by a `visible:` grant is
omitted from the event it would otherwise appear in.

Field redaction applies to what the event **renders**, not to which entities the
feed selects. A `where:` clause is evaluated against the unredacted entity on
purpose — otherwise a hidden property would read as empty inside the filter, and
the same feed would contain different events for different readers. Which
entities a feed selects is an operator-authored decision; what their fields say
is the reader's business.

One consequence worth knowing: if a feed is anchored on a date property that a
reader may not see, entities have no usable date for them and drop out of that
reader's calendar entirely. That is deliberate — an event whose date you may not
read is not one you should be shown.

On a plain localhost server there is no authentication — the feed is readable by
anything that can reach the port, which is appropriate for a single-user local
setup (bind to `127.0.0.1`). Exposing feeds on a network is a deployment concern;
see the server security guide.

## Custom apps

Custom **apps** let you extend the data-entry web app with your own HTML+JS
applications — dashboards, specialized forms, domain mini-tools — without
forking the SPA or writing Go. An app runs inside a locked-down sandboxed
iframe and talks to the existing REST API through a host-managed
`MessageChannel` bridge, so **an app can only ever do what the logged-in user
can already do**.

### Authoring

The quickest start is the scaffold command, which creates a working,
bridge-wired starter app you can edit:

```bash
rela apps new my-dashboard
# → apps/my-dashboard/index.html  (open /app/my-dashboard)
```

An app is a **folder** under the project's `apps/` directory (alongside
`actions/`, `scripts/`, `templates/`) containing an `index.html`. There is no
separate config:

```text
apps/ticket-counter/
  index.html          →  /app/ticket-counter   (id = folder name)
  app.js              (any sibling files: js, css, images, fonts…)
  style.css
```

The **id** is the folder name and must match `^[a-z0-9_-]{1,64}$`. A folder is
a live app iff it contains `index.html`. The app declares itself via `<meta>`
tags in `index.html`'s `<head>`:

```html
<head>
  <!-- REQUIRED: which bridge contract this app targets -->
  <meta name="rela-app:bridge-version" content="1">
  <meta name="rela-app:label" content="Ticket Counter">
  <meta name="rela-app:title" content="Ticket Counter">
  <meta name="rela-app:description" content="Counts tickets by status">
  <!-- the bridge SDK (window.rela); served at the app's own path -->
  <script src="_rela.js"></script>
</head>
```

`label` (falling back to `title`, then the id) is the sidebar entry; `title`
and `description` are cosmetic. **The app must include `<script
src="_rela.js"></script>`** to get the `rela.*` bridge — rela serves it at the
app's own `_rela.js` path.

**Bridge version (required).** `rela-app:bridge-version` declares the version of
the bridge/SDK contract your app was written against (currently `1`). The
server refuses to serve an app that omits it or asks for a *newer* bridge than
the server provides (a `422` with a clear message, and the app won't appear in
the sidebar) — so a breaking bridge change in a future rela can't silently make
an old app call methods that no longer exist. When the bridge gains a breaking
change the version bumps and rela keeps serving older-versioned apps against a
compatible SDK.

The app and its files are served from `/api/v1/_apps/<id>/`, so reference
sibling assets with **relative** URLs (`<script src="app.js">`, `<img
src="logo.png">`).

### Scripts and styles must be separate files

An app's Content-Security-Policy does **not** allow `unsafe-inline`, so these
are blocked and will silently do nothing:

```html
<style>body { margin: 0 }</style>          <!-- blocked -->
<script>console.log('hi')</script>         <!-- blocked -->
<div style="margin: 1rem">…</div>          <!-- blocked -->
<button onclick="save()">Save</button>     <!-- blocked -->
```

Put them in sibling files and use classes instead:

```html
<link rel="stylesheet" href="app.css" />
<script src="app.js"></script>
```

```js
// in app.js
document.getElementById('save').addEventListener('click', save)
```

`rela apps new` scaffolds exactly this layout, so a generated app works without
changes. A blocked resource shows as a CSP violation in the browser console —
if a style or handler seems to be ignored, check there first.

**Why.** An app already runs with `allow-scripts`, so this is not what stops an
app doing what it wants; the data boundary is the path-scoped CSP plus
`connect-src 'none'` plus the bridge. Barring inline code protects an app from
*its own* bugs: if your app builds DOM from entity data with `innerHTML`, a
`<script>` or `onerror=` smuggled in through that data is blocked rather than
executed. Escape your own output regardless — the CSP is the second line, not
the first.

**Publish / unpublish.** A folder with an `index.html` is live. To take an app
offline without deleting it, rename the folder (e.g. `ticket-counter` →
`_ticket-counter`, which fails the id rule) or remove its `index.html`.

### Matching rela's look (optional `_rela.css`)

To render consistently with the rest of the app, opt into rela's styling by
linking the served stylesheet:

```html
<head>
  <link rel="stylesheet" href="_rela.css">
</head>
```

(`_rela.css` is a relative URL — it resolves against the app's own base,
`/api/v1/_apps/<id>/`, same as your other sibling assets.)

`_rela.css` provides three things:

- **Theme tokens** — CSS custom properties for colors (`--text-color`,
  `--bg-color`, `--card-bg`, `--border-color`, `--accent-color`,
  `--error/success/warning/info-color`, the `--badge-*` set), surfaces, and
  borders. Use them in your own CSS (`color: var(--text-color)`) so the app
  matches the host palette.
- **Typography** — `--font-family` (the host UI font) and a small size scale
  (`--font-size-sm` / `--font-size-base` / `--font-size-lg` / `--font-size-xl`).
  Linking `_rela.css` applies `--font-family` and `--font-size-base` on your
  app's `<html>` automatically, so text matches the host instead of the
  browser's default serif — a sandboxed app iframe is a separate document with
  no inherited typography. Use `var(--font-size-lg)` etc. for headings; override
  per-element whenever you like.
- **Base controls** — three atomic classes: `.btn` / `.btn-primary` (buttons),
  `.input` (text inputs), `.card` (a bordered surface). These are deliberately
  minimal; build anything more structural (tables, selects, modals) yourself
  using the tokens.

**Dark mode follows the host automatically** — when the user switches the data-
entry theme, rela toggles the same `dark` class on your app's `<html>` element
(`document.documentElement`, matching the SPA's own `:root.dark`), and the
tokens flip. No work needed beyond linking `_rela.css` and using `var(--…)` for your
own colors. Opting in is entirely optional; an app that wants full control of
its look simply doesn't link it.

### Editing markdown (optional `<rela-editor>`)

Apps that edit entity body content can embed rela's own markdown editor rather
than building one. Opt in with a script tag, then use the element:

```html
<head>
  <script src="_rela.js"></script>
  <script src="_rela-editor.js"></script>
</head>
<body>
  <rela-editor id="body" placeholder="Write markdown"></rela-editor>
</body>
```

It is the same WYSIWYG editor the data-entry forms use: a formatting toolbar,
table controls, and `@` completion for entity references. It renders into the
page normally, so your own CSS lays it out like any other block element.

Kept separate from `_rela.js` so only apps that want an editor pay for it.

**The element's API is deliberately small**, because it is what lets rela change
the editor underneath without breaking your app:

| | |
|---|---|
| `value` | property — get/set the markdown text, exactly as written |
| `placeholder` | attribute — plain text shown while empty (read once, at mount) |
| `readonly` | attribute — present to make the editor read-only |
| `input` | event — fired on every change |
| `change` | event — fired on blur, only if the content actually changed |
| `focus()` | method — put the cursor in the editor |

Everything else is an implementation detail and may change: which editor is
underneath, what the toolbar looks like, what DOM it generates. Read and write
`value`, listen for `input` and `change`, and your app keeps working. (The
editor has already been replaced once, and apps written against this API needed
no edit.)

```js
var editor = document.getElementById('body');

rela.whenReady(async function () {
  var res = await rela.get({ type: 'ticket', id: 'TKT-001' });
  editor.value = res.data.content;

  // `change` fires on blur after a real edit — a click-away with nothing
  // changed does not fire it, so this will not save on every focus loss.
  editor.addEventListener('change', async function () {
    await rela.update({
      type: 'ticket', id: 'TKT-001', patch: { content: editor.value },
    });
  });
});
```

Three behaviours are worth knowing:

- **Setting `value` from code is silent.** It fires no `input` event, matching a
  native `<textarea>`. Loading content into the editor therefore cannot trigger
  an autosave loop.
- **An unedited body comes back untouched.** A WYSIWYG editor re-writes the
  markdown it parses, so opening a body and saving it could otherwise rewrite
  tables, headings and list markers for no reason. As long as the user has not
  edited anything, `value` returns the original bytes exactly.
- **Once the user edits, the whole body is rewritten to normal form.** Tables
  are repadded to their column widths, `Setext`-underlined headings become `##`,
  list markers are normalised — including in parts of the document the user
  never touched. The meaning never changes, only the formatting. This is what
  editing a parsed document means, and it is worth knowing if you diff what you
  store.
- **Entity references show as plain IDs.** In the data-entry form a `` `TKT-001` ``
  reference renders as the entity's title; here it stays the ID. Titles are
  per-user information that only the server can decide you may see, and the app
  bridge has no call that returns them, so showing one would mean guessing.

### The `rela` bridge

Inside the iframe, a `rela` object (from `_rela.js`) gives the app a
promise-based, closed set of methods that forward to the REST API over the
`MessageChannel`:

| Method | REST operation |
|---|---|
| `rela.schema()` / `rela.config()` | metamodel + data-entry config |
| `rela.list({type, params})` | list entities of a type |
| `rela.get({type, id, params})` | fetch one entity |
| `rela.search({query, type})` | full-text search |
| `rela.analyze()` | run the analysis checks |
| `rela.templates({type})` | entity templates |
| `rela.position({id, scope})` | prev/next within an ordered set |
| `rela.create({type, entity})` | create an entity |
| `rela.update({type, id, patch, etag})` | update an entity |
| `rela.delete({type, id})` | delete an entity |
| `rela.relationCreate({type, id, relation, targetId, meta?, direction?})` | link entities |
| `rela.relationUpdate({type, id, relation, targetId, meta, direction?})` | edit a relation's properties |
| `rela.relationDelete({type, id, relation, targetId, direction?})` | unlink entities |
| `rela.action({actionId, entityId?, entityType?})` | run a registered Lua action |

This is a **closed set** — an app cannot ask the host to fetch an arbitrary
URL. Reads are scoped to the user's read permissions; writes go through the
normal write path (re-authorized and audited). A denied call rejects with an
error the app can catch. The SDK signals readiness with a one-time
`rela:ready` event; calls made before then are queued.

Minimal app (`apps/hello/index.html`):

```html
<!doctype html>
<html>
  <head>
    <meta name="rela-app:bridge-version" content="1">
    <script src="_rela.js"></script>
  </head>
  <body>
    <div id="out">loading…</div>
    <script>
      window.addEventListener('rela:ready', async () => {
        const res = await rela.list({ type: 'ticket', params: { per_page: 200 } });
        document.getElementById('out').textContent = res.data.length + ' tickets';
      });
    </script>
  </body>
</html>
```

### Security model

Apps run untrusted code, so the data-entry server and SPA lock them down:

- **Sandboxed iframe** — `sandbox="allow-scripts allow-forms"`, never
  `allow-same-origin`. No `localStorage`/parent-DOM access. The app loads from
  its own served path (`/api/v1/_apps/<id>/`) so its files resolve, which makes
  it same-origin with the API — so its confinement is the CSP, not the origin.
- **Path-scoped Content-Security-Policy (header)** — every resource directive
  is scoped to the app's **own** absolute subpath (e.g. `script-src
  https://host/api/v1/_apps/<id>/`), **not** `'self'` (which would include
  `/api/`, letting `<img src="/api/v1/tickets/x">` pull data). `connect-src
  'none'` means the app's own JS cannot `fetch`/`XHR`/WebSocket anything — so it
  **cannot reach `/api/` directly**. `form-action 'none'` + the sandbox block
  form/navigation exfil.
- **Bridge-only data path** — with `connect-src 'none'`, the only route to the
  API is the `MessageChannel` bridge (a message post, not a network request,
  so CSP doesn't block it). The bridge exposes only the closed method set above
  and is the per-app capability chokepoint.
- **No app-specific permissions (yet)** — an app is a UI shell. Its reads and
  writes are gated exactly like the SPA's own, so it can do nothing the user
  couldn't already do. (The bridge is where a future per-app restriction —
  e.g. read-only — would be enforced.)

**Trust level.** The sandbox protects the *browser* — it does **not** limit
what an app can do to your *data*. Because an app runs as the logged-in user,
its code can perform any create/update/delete/link the user can, and can invoke
any registered Lua action via `rela.action`. Treat an app folder with the
**same review rigor as a `scripts/` Lua action**: it is code, not content. Apps
live as files in `apps/`, versioned in git, and should go through the same
review as any other code.

## Worlds in the web app and API

If your schema declares faces and worlds, every read in the web app and the
HTTP API happens in a **world**: a declared rule that picks one face of each
entity, or leaves the entity out. The
[Metamodel Reference](metamodel.md#content-states-and-worlds) lists the schema
keys, and the guide
[How To Publish Content with Faces and Worlds](content-states.md) walks through
a complete setup. This section covers what the web app and the API do with a
world once it is declared.

A project that declares no worlds is unaffected by everything below.

### Browsing default (`app.default_world`)

`default_world` in the `app:` block names the world a request lands in when the
URL carries no `?world=`:

```yaml
app:
  name: "Handbook"
  default_world: published
```

Without it, browsing lands in the default world, which applies no resolution
and so shows none of a faced type's rows at all. For a project using faces
`default_world` is effectively required, and a handbook should land readers in
the world holding the published text.

`default_world` is presentation, not policy. It grants nothing: the world's
read grant is re-checked on every request exactly as for an explicit `?world=`,
so pointing it at a world a role may not read yields that world's ordinary
empty result. The server applies it to `curl` and to the browser alike, but
only on read requests and only on the routes listed under
[Routes that serve a world](#routes-that-serve-a-world). Passing
`?world=default` explicitly still selects the unresolved default world. Naming
an undeclared world here is a startup error.

### Creating into another world (`create_world`)

When a list is shown in a world that does not select the face a new entity is
created in, the entity has no face in the world on screen and the author would
be redirected to a page saying so. `create_world` on the list names the world
the create button opens its form in:

```yaml
lists:
  policies:
    entity_type: policy
    create_form: new_policy
    create_world: editorial
```

The form carries that world onto the post-create redirect, so the author lands
on the draft they just made. It is on the list rather than on the form because
the same form can be reached from several places, and which face a new entity
starts in belongs to the workflow that opened it. Leaving it empty opens the
form in the list's own world. It must name a declared world, and the button is
shown only if the caller may read the target world.

### What a world-bound page shows

In a non-default world the web app behaves as follows. One rule governs every
sentence it shows about worlds and faces: **the words are the operator's, or
there are none.** The app has no text of its own for any of this — "face",
"world" and "default" are storage vocabulary a reader never chose — so each
note below appears only when `schema.yaml` declares it.

- The world is part of the URL as `?world=<name>`, so a world-bound page is a
  shareable link that survives a reload. Changing world resets pagination and
  adds a browser history entry.
- The top of the page shows the world's `banner:` when declared. A list or
  board of a type that declares faces adds the world's `messages.projection`
  when declared; a type without faces has one state in every world, so its
  lists carry no note.
- Every write goes to the **address** of the row on screen, face included (see
  [Addressing a face directly](#addressing-a-face-directly-idface)). The Edit
  button opens the form on that address, inline edits and checkbox toggles
  patch it, drag-and-drop on a board or calendar writes it, and Delete removes
  it. Whether a write is offered is `_actions` on the response, which the
  server computes for the face it served: an entity served at its published
  face reports `update: false` unless a grant names that face. A detail page or
  edit form showing a face the caller may not write shows that face's
  `messages.read_only` when declared, and otherwise looks like any other
  permission denial. A detail page showing a face that declares
  `messages.notice` carries that sentence regardless of `_actions` — it says
  something about the document ("this is a draft, not yet in force") rather
  than about the reader, so it also appears on a face the caller may freely
  edit. A face declaring both shows `notice` first.
- A **View Published** button, or a menu when the entity has several other
  faces, switches to another face by navigating to its address (`_faces[].ref`)
  in the same world. It appears on every screen that has faces, including the
  default world, because an author on the draft wants to see what readers see.
- A row or card served a **stand-in** (a face reached through
  `otherwise: default`, or through a later entry in the chain than the first)
  carries a badge with the world's `messages.stand_in` when declared, typically
  `{face}`. A first-choice hit shows no badge. The badge appears on list rows,
  kanban cards, and each related entity on a detail page.
- An entity that exists but has no face in the world renders one of the faces
  the caller may read, with the world's `messages.absent` when declared. With
  `on_absent: {redirect: <world>}` the app navigates to that world instead.

A detail page shows one button per copy definition whose source is the face on
screen and that the caller may invoke, for example **Publish** on a draft
policy, in whichever world the draft is being read. A caller without the guard
permission sees no button rather than a disabled one. After a successful copy,
the app shows the copy's `on_success.message` (or its label) and lands per
`on_success.landing`: the face it wrote by default, `stay` to reload in place,
or a declared world or face.

A kanban board is a projection too. Each card is one entity at the face the
world resolved, and an entity with no face in the world has no card. Cards
move: a drag writes the face the card shows, and a card whose face the caller
may not write refuses the drag.

### Next actions and worlds

A next-action source takes two world keys, and they answer different
questions:

```yaml
next_actions:
  finished-draft:
    band: blocking
    source_world: editorial
    query: "type:policy prop:status=done"
    suggest: "The draft of {title} is marked done. Ready to publish it?"

  translate-procedure:
    band: housekeeping
    source_world: site-nl
    visible_worlds: [site-nl]
    query: "type:procedure prop:readiness=drilled"
    suggest: "{title} is drilled. Is the Dutch site's copy up to date?"
```

`source_world` is the world the candidate query runs in. It is declared per
source and never taken from the request, because a suggestion is almost always
about unfinished work, which is exactly what a `published` world leaves out.
`visible_worlds` is an allow list of worlds the suggestion may be displayed in.
Unset, it matches every world. It is presentational only: candidates pass the
caller's read gate regardless, and nothing should rely on this list to hide
content.

### Selecting a world over the API

The API takes `?world=<name>` on the entity list and the single-entity read:

```http
GET /api/v1/pages?world=published
GET /api/v1/pages/PAGE-1?world=published
```

Omitting the parameter serves the configured `default_world`, or the default
world when none is configured. `?world=default` always serves the default
world.

Reading a non-default world requires a role holding `read: [world:published]`
in `acl.yaml`. An ordinary `read: [page]` grant covers the default world only.
A principal without the grant receives an **empty result**, deliberately
indistinguishable from a world that holds nothing they may read. Whether an
entity exists in a world is exactly what publication controls, so the API does
not confirm it. An undeclared world name is a `400` that names the world,
because world names are configuration in your repository rather than secrets.

### Discovering worlds and faces

`GET /api/v1/_schema` lists the declared worlds:

```json
"worlds": {
  "default":   { "readable": true, "default": true },
  "published": { "select": ["published"], "otherwise": "exclude",
                 "banner": "Published — this is what readers see", "readable": true },
  "site-nl":   { "select": ["nl", "en"], "otherwise": "default", "readable": false }
}
```

Every declared world is listed for every caller, with its `banner`, `messages`
and `on_absent` verbatim from the schema. `readable` says whether *this*
caller may select it. A world you may not read is marked rather than hidden, and
selecting it anyway returns an empty result rather than an error. The same
response reports each type's declared faces under `entities.<type>.faces`,
plus the configured `default_world`. Like the world list, this is schema: it says `policy` declares `draft` and `published`,
never which faces a particular policy has.

### Knowing which face you got (`_world`)

A single-entity read carries the provenance of the face it served:

```json
"_world": { "name": "site-nl", "face": "en", "via": "chain", "chain_position": 1 }
```

| Field | Meaning |
| --- | --- |
| `name` | The world the response was resolved in. `default` for the implicit default world. |
| `face` | The declared name of the face that was served. Empty when the served face has no declared name. |
| `via` | The rule that chose the face: `unscoped` when no resolution was applied (a type without faces, the default world, or a face the request addressed as `ID@face`), `chain` when a face the world selects exists, `fallback-default` when `otherwise: default` substituted a faceless type's single state. |
| `chain_position` | The zero-based index of the served face in the world's chain. Present only for `via: chain`. |

Position `0` means the world got its first choice. Any later position is a
stand-in, as in the example, where `site-nl` asked for Dutch and served English.
`fallback-default` is a stand-in too. The bytes alone do not tell you which one
you got, which is why the field exists. There is no `excluded` value: an entity
the world excludes produces no response to carry it.

Entities inside a view response carry the same block, so a view can badge each
related entity independently.

### Other faces and copy offers (`_faces`, `_copies`)

A single-entity read also carries two lists a client needs to render the
controls described above.

`_faces` lists the entity's **other** faces, each with its stored coordinate,
display label and address:

```json
"_faces": [
  { "face": "published", "label": "Published", "ref": "POL-1@published" }
]
```

`ref` is the path segment that reads that face literally under any world, so a
client links to a face without working out which world leads with it. Every
face of a faced type is spelled `ID@face`, the same name the schema declares.
`_faces` reports
existence only: whether the caller may read a world is a role-level grant
already answered by `_schema`. Which faces a given entity has is data, so
`_faces` appears only on a response the caller was already cleared to read.

`_copies` lists the copy definitions whose `from:` matches the face being
served:

```json
"_copies": [
  { "name": "publish", "label": "Publish", "targetFace": "policy@published",
    "sameEntity": true, "allowed": true }
]
```

`allowed` reports whether this caller may invoke the copy on this entity right
now, and `reason` names why not when it is `false`. The verdict is computed by
the same authorization path the invoke uses, so it cannot drift from what the
write does, but it is a hint for rendering and never a boundary. Only copies
between faces of the same entity are offered. Copies that create a different
entity are supported by the invoke endpoint but not listed here.

### Invoking a copy

A copy is invoked by name:

```bash
curl -s -X POST http://localhost:8080/api/v1/_copies/publish \
  -H 'Content-Type: application/json' \
  -d '{"source_id": "POL-1"}'
```

The body names the source entity, and `target_id` when the definition creates a
different entity. A request can never describe a mapping; it chooses a
registered name, which is what keeps the guard meaningful. A successful invoke
returns what was written:

```json
{ "definition": "publish", "entityId": "POL-1", "face": "published", "created": true }
```

`created` is `true` when the copy brought the target face into existence and
`false` when it overwrote one. A caller who lacks the guard permission receives
a `403` naming it. A source the caller may not read produces the same `404` as
a source that does not exist. Any other refusal, such as an unknown definition
or a cross-entity copy without a `target_id`, is a `422`.

### Responses and errors

| Situation | Response |
| --- | --- |
| `?world=` names a world the schema does not declare | `400 unknown_world`, naming the world |
| `?world=` appears more than once | `400 duplicate_world` |
| `?world=` on a `POST`, `PATCH`, `PUT`, or `DELETE` | `422 world_read_only` |
| `?world=` on a route that cannot serve a world | `422 world_unsupported` |
| A declared world the caller may not read | An empty list, or `404` for one entity, identical to a world holding nothing readable |
| An entity that has no face in the world | Omitted from lists; `404` from the single-entity read; `200` with `_world_absent: true` from the entity view; `200` with an empty timeline and `world_face_absent: true` from history |

Writes never take a world. A world can answer a read with a stand-in face, so a
write riding that indirection would save to a face the caller did not name:
a `PATCH` in the `published` world would edit the draft of an entity with no
published face, and a `DELETE` would delete the entity outright while the caller
believed they were unpublishing. Writes address a face directly, by id, as the
next section describes.

### Addressing a face directly (`ID@face`)

Every response names the row it describes in `_self`, face included:
`/api/v1/policys/POL-1@draft` for the draft, `/api/v1/policys/POL-1@published`
for the published one. A type declaring `faces:` has no bare address, so
`_self` always carries a face; a type with no faces keeps the unsuffixed
`/api/v1/controls/CTL-1`. That address is accepted wherever the id is, on the
entity route and on the entity view:

| Request | Meaning |
| --- | --- |
| `GET /policys/POL-1@published` | The published face, under any world. An explicit address is served literally: the world made no choice, so `_world.via` is `unscoped`. |
| `GET /policys/POL-1@draft` | The draft, even under a world that would resolve a bare `POL-1` to a different face. |
| `GET /_views/policy/POL-1@published` | The entity view of that face. |
| `PATCH /policys/POL-1@published` | Edits the published face. Authorized against the face: a bare `update: [policy]` grant covers no face at all, `update: [policy@published]` covers this one. |
| `DELETE /policys/POL-1@published` | Removes the published face only, with the content-scoped edges tailed at it. Incoming edges point at the entity and survive. Authorized against the face. |
| `DELETE /policys/POL-1@draft` | Removes the draft only, unless it is the entity's last remaining face — deleting that leaves no entity behind. |

A face the entity does not have, a face name the grammar rejects, or a face
the caller may not read all produce the same `404` as a missing entity. A
`PATCH` addressing a face may not carry `scope: content` relations; it is
refused with `422 face_relations_unsupported`, because such an edge attaches
to one face and the relation writers address the entity's bare tail. Move
those edges with a copy definition instead. Identity-scoped relations are
entity-level and pass through.

This is what makes a form addressable: the web app opens its edit form on the
`_self` of the row on screen and saves to the same address, so a translator
holding `update: [guide@nl]` edits Dutch where they see Dutch.

### Routes that serve a world

A route joins this table only when its whole read path is world-scoped and
tested. Every other route refuses an explicit `?world=` rather than serving
default-world data under it.

| Route | World-scoped |
| --- | --- |
| `/api/v1/{plural}` and `/api/v1/{plural}/{id}` | Yes, including `?q=` search and `?include=` neighbours |
| `/api/v1/_views/{type}/{id}` | Yes. A view's `where:` clauses evaluate against the resolved face |
| `/api/v1/_history/{type}/{id}` | Yes. Versioning is per face on PostgreSQL, so the history is the served face's own |
| `/api/v1/_next_action` | Yes, as the display world for `visible_worlds` |
| Documents, feeds, analysis, sync, attachments, export, relation sub-resources, standalone views by name | No. An explicit `?world=` is refused with `422 world_unsupported` |

Search under a world matches the text of the face the world resolves, and an
entity the world excludes has nothing to match. Neighbours reached through
`?include=` and the `relations` map are resolved through the same world as the
entity, so a published entity is never returned wrapped in draft neighbours.

Analysis is deliberately unscoped. It reports on the health of the whole graph
a caller may read, and a world that hides a broken draft would make the graph
look clean precisely where it is not.

History under a world is the history of the face the world resolves. A backend
that has history but cannot scope it per face answers `501
history_face_unsupported` rather than serving the default face's record.
Restoring a version is a write, so a world on a restore is refused like any
other write.

## Best Practices

1. **Start with navigation** - Decide which entity types users will work with most, and create
   lists for those first. Add forms as needed. Consider adding a dashboard as the landing page
   for an at-a-glance overview.

2. **Create before edit** - Define a create form with sensible defaults and hidden fields (like
   `status: open`). Then define an edit form with transitions and all fields visible.

3. **Use `link: true`** on the primary column (usually `title` or `name`) so users can click
   through to entity details.

4. **Filter strategically** - Use static filters for focused views (e.g., "Open Tickets") and
   filter controls for exploratory views (e.g., "All Tickets").

5. **Group related lists** - Use navigation groups to organize related lists under collapsible
   headers. Keep 3-5 items per group for clarity.

6. **Style all enums** - Add color mappings for every custom type to make lists scannable.

7. **Views for key entities** - Create detail views for entities that aggregate related data.
   A risk detail view showing assets, controls, and incidents is more useful than viewing the
   risk entity alone.

## Audit log

Every edit performed through the data-entry app is recorded in
`.rela/audit/YYYY-MM-DD.jsonl` with `principal.tool: "data-entry"`.
The user is currently stamped as `"unknown"` — recording the server
process's OS user for every edit would be misleading. Per-request
user attribution (read from a header / cookie / session) lands in a
follow-up. See [audit-log.md](audit-log.md) for the full schema and
operator concerns.
