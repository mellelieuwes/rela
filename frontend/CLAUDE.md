# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test Commands

```bash
# Development
npm run dev                    # Start Vite dev server on :5173 (proxies /api to :8080)
npm run build                  # Build to ../internal/dataentry/static/v2/

# Lint & Format
npm run lint                   # Run ESLint
npm run lint:fix               # Auto-fix lint issues
npm run format                 # Format with Prettier
npm run format:check           # Check formatting
npm run typecheck              # Run vue-tsc type checking
npm run dupes                  # Check for code duplication (jscpd)

# Unit Tests (Vitest)
npm run test                   # Run tests in watch mode
npm run test:run               # Run tests once
npm run test:run -- src/stores/ui.test.ts  # Run single test file
```

E2E tests live in the top-level `/e2e/` directory and run against the built
`rela-server` binary (which embeds this SPA's production bundle). From the
repo root, use `just e2e` or `cd e2e && npm test`.

## Architecture Overview

Vue 3 frontend for rela data entry application. Communicates with the Go backend (`rela-server`) via REST API.

### Data Flow

```text
Backend API (/api/v1/*)
     ↓
src/api/          → Axios API client layer
     ↓
src/stores/       → Pinia stores (state management)
     ↓
src/views/        → Page components (route targets)
     ↓
src/components/   → Reusable UI components
```

### Package Structure

| Directory | Purpose |
|-----------|---------|
| `src/api/` | Typed API client functions (entities, schema, git, settings, etc.) |
| `src/stores/` | Pinia stores: `schema` (metamodel/config), `entities` (CRUD + cache), `ui` (toasts, sidebar), `git` (status) |
| `src/views/` | Route-level components: Dashboard, List, Form, Entity, Kanban, Graph, Search, Settings |
| `src/widgets/` | The property-widget library + `registry.ts` (the type→widget dispatch). One widget per property type, each rendering both `mode: 'display'` and `mode: 'edit'` |
| `src/components/forms/` | Form scaffolding: DynamicForm, FieldRenderer (resolves via the widget registry), RelationPicker, SidePanel |
| `src/components/forms/milkdown/` | The WYSIWYG markdown editor (Milkdown/ProseMirror) and everything it owns: entity-ref node, `@` completion, toolbar, write-back guard |
| `src/app-editor/` | `<rela-editor>`, the Custom Element custom apps embed. Built as a standalone IIFE (`vite.editor.config.ts`), not part of the SPA bundle. Imports the editor's shared modules; holds only the plain-DOM toolbar and `@` menu that replace the SPA's Vue ones |
| `src/components/lists/` | EntityList, FilterBar, Pagination |
| `src/components/common/` | Sidebar, StatusBar, Badge, Toast, BackButton |
| `src/composables/` | Vue composables: useKeyboardShortcuts, useEvents (SSE), useListKeyboard, useScopeNavigation, useBackTarget |
| `src/styles/` | Shared CSS loaded from `main.ts` (e.g. `back-button.css` for the `.scope-nav-btn` class reused across EntityDetail, CustomView, and standalone BackButton) |
| `src/types/` | TypeScript interfaces for entities, schema, and config |

### Widgets render property values everywhere, including read-only

A property value is rendered by a **widget** on every surface — forms, entity
detail, view sections, list/table cells, and kanban cards. Widgets take a
required `mode: 'display' | 'edit'` (no default, RR-UD1I) and own their
read-only rendering. **Do not add a per-type `v-if` to a view.** A new property
type should need a widget plus a routing entry, never a branch per surface.

There are two routing entry points, and they are not interchangeable:

- `defaultRegistry.resolve(name, propertyDef)` — form side, when you hold the
  real schema entry.
- `defaultRegistry.resolveFromHint(hint)` — view side, when you have wire-level
  field metadata. Build the hint with `viewFieldRoutingHint` (view sections) or
  `densePropertyRoutingHint` (list cells, kanban cards).

**Dense surfaces are not detail views.** `densePropertyRoutingHint` exists
because a table cell and a detail row have genuinely opposite contracts, and
reusing the detail-view routing in a cell has already caused regressions
(TKT-S9C14S):

- **Empty renders blank, never a placeholder.** MultiSelectWidget's em-dash
  (RR-UD2C) is right for a detail row and wrong for a cell — `formatCellValue`
  documents that "blank table cells stay visually quiet". `isDenseEmpty` gates
  this at the cell, before the widget sees the value.
- **`list: true` must not override the type's formatter.** `defaultWidgetFor`
  puts list first because the *edit* side needs a tag picker whatever the type;
  on the display side that erases the formatter (a list-valued rrule rendered as
  an em-dash). Non-enum lists route to preformatted text.
- **`boolean` → text and `file` → text in cells**, deliberately: booleans stay
  Cmd-F searchable, and FileWidget's `<img>` previews would be one network
  request per cell.
- **Resolve per column/field, not per cell** — `resolve()` walks a Map and can
  `console.warn`, which at 200 rows is 200 warnings per render. See
  `PropertyDisplay.vue`'s precomputed `rows` (RR-UD2A) for the pattern.

The hint carries `preformatted`: true for passthrough widgets that render
`String(value)`, false for widgets that own their display formatting. A new
widget defaults to false (raw value), which is correct — the reason to add a
widget is that it renders something text cannot.

### Key Stores

- **schemaStore**: Loads metamodel (entity/relation types) and config (forms, lists, views, navigation) on app mount
- **entitiesStore**: Entity CRUD with 1-minute TTL cache, invalidates on mutations
- **uiStore**: Toast notifications, sidebar collapse state, theme (dark/light)
- **gitStore**: Git status polling for uncommitted changes indicator

### SSE Real-time Updates

`useEvents` composable connects to `/api/v1/_events` SSE endpoint.
On entity changes, it calls `entitiesStore.invalidateAll()` to refresh cached data.

### Routing

Routes use dynamic imports for code splitting. Config-driven IDs (e.g., `/list/:id`, `/form/:id`)
resolve to `data-entry.yaml` configuration from the backend.

## Lint Configuration

ESLint flat config with:

- Vue 3 recommended + TypeScript
- `vue/no-v-html: warn` (XSS risk)
- `max-lines: 500` warning for Vue files (catches god components)
- Relaxed rules for test files (`no-explicit-any`, `no-non-null-assertion` allowed)

## Pending and loading indicators

Three indicators, one per class, with no overlap (TKT-TFSNBY). The class is
decided by **who started the operation and whether the user needs to know it
finished** — not by whether it blocks.

| Class | Use | Delay / min |
|---|---|---|
| Navigation | `<ActivityBar>` in `App.vue` (already wired) | 250 / 300ms |
| Explicit action (Save, Create, Search, Refresh) | `<PendingButton>` | 500 / 400ms |
| Ambient (autosave) | `AutoSaveIndicator` + `useAutoSave` | see below |
| Background revalidation (SSE refetch) | **nothing** | — |

**The governing rule:** if an operation completes before its indicator's delay
elapses, nothing is ever shown. rela normally talks to a local or
well-connected server, so the common case is well under 100ms — meaning the
app shows no loading UI at all most of the time. That is the intended
behaviour, not a bug.

**Delay scales with how invasive the indicator is**, not with how slow the
request is. A peripheral bar that fades in can appear at 250ms; a button
label mutating under the cursor waits 500ms. This is why published figures
like Spectrum's and Primer's 1000ms exist — they are calibrated for a
*spinner on a button*, and are wrong applied to a top bar.

Timings live in `composables/pendingTimings.ts` (JS) and `styles/scales.css`
(`--pending-fade`). They are tuned by feel; change them there, not at a call
site.

### Rules for new code

- **Never wire an indicator straight to a boolean.** Put it behind
  `useDelayedPending(source, {delay, minDuration})`. Without the gate a 40ms
  request flashes for a few frames, which is the defect this replaced.
- **Use `<PendingButton>` for any button that triggers a request.**
  `pendingLabel` is required and must never be derived from `label` by string
  munging — that breaks on the first irregular verb or translated string.
  Use `…` (U+2026), not `...`.
- **No spinners inside buttons, and no skeletons anywhere.** A label swap says
  *what* is happening, survives `prefers-reduced-motion` with no fallback
  needed, and is its own screen-reader announcement. Skeletons belong to the
  1-10s band and never earn their place at a 1-2s worst case.
- **Keep previous content rather than showing an indicator.** For pagination
  and entity-to-entity navigation the right answer is usually *no* indicator:
  hold what is on screen until the new data lands. Pinia Colada does this via
  `placeholderData: (prev) => prev`; gate the template on `isPending` ("no
  data yet"), never `isFetching` ("a request is in flight"), or SSE
  revalidation will flash the spinner on every background refresh.
  `DocumentView` / `DocumentsPanel` do the same by hand with
  `v-if="loading && !docContent"` — **preserve that guard.**
- **Held content is read-side ACL's blind spot: drop it when the refetch is
  refused.** "Keep previous content" is a rule about *loading*, and it silently
  became a rule about *authorization* the moment a refetch could be denied. A
  view holding a copy the server has just refused is showing content the
  principal may no longer read, and every existing ACL test passes — the gate
  did its job at the boundary, and nothing there can see what a client kept
  (BUG-E8XE7I, issue #1603, CONTROL-8-03). Ask `shouldDropHeldContent(err)`
  from `api/errors.ts` in the catch and clear on true; keep content on every
  other failure, which is the transient case the rule above exists for.
  **A test for one proves nothing about the other** — they reach the same
  catch block and differ only in the error carried — so any surface holding
  content across a refetch needs both, as `DocumentView.rerender.test.ts` and
  `DocumentsPanel.denial.test.ts` pair them.
- **One indicator per user act.** A save shows the button state, never the
  bar. Create-then-redirect is sequential: the button owns the save, the bar
  takes over at the route change.
- **`@keyframes spin` lives once, in `styles/pending.css`.** Do not
  re-declare it in a component; it was previously copied ten times.
- **A new spinner needs its OWN `prefers-reduced-motion` rule unless it is
  unscoped.** `pending.css` suppresses `.spinner` because that class is
  declared in App.vue's unscoped `<style>`. A class declared in a *scoped*
  component `<style>` compiles to a `[data-v-*]` selector that outranks any
  unscoped rule, so adding it to `pending.css` would silently do nothing —
  put the rule beside the declaration instead, as `.spinner-sm`,
  `.search-spinner`, `.cmdk-spinner` and `.entity-picker-spinner` now do.

### Two sanctioned exceptions

- **`ConfirmModal` keeps its own treatment.** It appends one character to a
  caller-supplied `confirmLabel` (optional, defaults to `'Confirm'`) rather
  than swapping verbs. Migrating it would push a required `pendingLabel`
  through every `useConfirm()` caller to replace behaviour that is already
  minimal-shift. This is a decision, not an oversight.
- **`useAutoSave` has no entry delay, deliberately.** Its 0ms is measured
  from *request start*, and its 800ms debounce has already filtered out the
  fast and transient cases — **the debounce IS that class's delay.** Do not
  "harmonise" it with the other two by adding one; you would make autosave
  feel broken. It keeps its own `MIN_SAVING_VISIBLE_MS` / `SAVED_INDICATOR_MS`.

### Accessibility

`aria-disabled`, not native `disabled`, on a **primary** action while
pending — native `disabled` drops focus to `<body>` mid-interaction.
Secondary/Cancel buttons keep native `disabled`, because `aria-disabled`
leaves a control activatable and "cancel an in-flight action" has no defined
meaning. Since `aria-disabled` does not prevent activation, suppression is
the component's job and must cover **keyboard as well as pointer** — on a
destructive action a second activation is a second DELETE.

### Testing

jsdom has no layout engine, so the width-reservation guarantee cannot be
asserted there. Unit tests assert the structural preconditions (both labels
in the DOM, the inactive one hidden by `visibility` rather than removed);
`e2e/tests/pending-indicators.spec.ts` measures the actual pixels. Both
halves are mutation-verified — swapping `visibility: hidden` for
`display: none` fails the e2e width tests, and replacing the navigation
tracker with a counter fails `useNavigationPending.test.ts`.

When asserting a computed style in e2e, remember the scoping rule above: a
probe element built with `document.createElement` only picks up *unscoped*
CSS, so it can verify `.spinner` but not any scoped class.

## The markdown editor (Milkdown/ProseMirror)

`src/components/forms/milkdown/` replaces the old EasyMDE editor. It is
WYSIWYG: it PARSES every body it opens and RE-SERIALIZES it on save. Four
properties follow from that and must not be broken.

There are TWO editors on it, and they share their load-bearing parts rather
than mirroring them: the data-entry form (`MilkdownEditor.vue`) and the
sandboxed custom-app element (`src/app-editor/`, see below).

**Opening an entity and saving it must emit nothing.** Serialization is pinned
by `RELA_STRINGIFY_OPTIONS` (`serializerContract.ts`) and gated by a corpus
test that runs every entity body in the repo through parse → serialize. The
test distinguishes two failures that look identical in a diff:

- *byte churn* — output differs, meaning identical (table padding, bullet
  style). Expected; **suppressed** by `guardWriteBack`, which returns the
  ORIGINAL bytes when the semantic projection is unchanged.
- *semantic drift* — output means something else. **Zero tolerance**; the
  guard refuses to write rather than guessing, and CI runs the full sweep via
  `RELA_FULL_CORPUS=1`.

Never bypass `guardedValue()` and save `modelValue` directly — that reintroduces
the churn the guard exists to absorb.

**Load-time normalization is not a user edit.** ProseMirror's table plugin
rewrites column widths while a document loads, which is indistinguishable from
typing unless guarded. The `armed` flag is set only once loading settles, and
resolution transactions are tagged so an arriving title never counts as an
edit. Getting this wrong writes a diff for an entity someone only opened.

**Entity-reference titles are ACL output, never derived.** A `` `TKT-007` ``
code span becomes an `entityRef` node whose `id` is serialized and whose
`title`/`entityType`/`inaccessible` are view-only and never reach the markdown.
Titles come SOLELY from the server's per-principal `mentions` map (BUG-R9EHKV,
`collectMentions` → `visibility.Reader.Filter`). Do not look a title up from
the graph, the entity store, or a cache: an entity the principal cannot read
must produce no mention at all, and a redacted title must fall back to the ID.

**An HTML comment is a chip, and only an exact comment is.** A comment-only
`html` mdast node is retyped to `relaComment` by a remark plugin
(`commentNode.ts`) and rendered as a muted chip showing the inner text without
delimiters — templates carry authoring guidance this way, and the rendered view
drops it, so the editor was the one place it looked like prose. Three things
hold it together:

- The **retype is what wins the match**, not `parseMarkdown`. Milkdown resolves
  against `{...schema.nodes, ...schema.marks}` taking the first match, and the
  preset is registered first, so a competing matcher on `type === 'html'` would
  lose. Retyping makes the two disjoint.
- The matcher is an **anchored allowlist**, and must stay one. Anything that is
  not exactly one comment — `<!-- note --><div>`, an unterminated comment,
  `<img>` — falls through to the preset's `html` node and stays VISIBLE as
  text. Chipping live markup would hide it behind a label reading "this is only
  a note". (`<!-- a --> text <!-- b -->` is not a counter-example: remark splits
  it into two well-formed comment nodes, so both chip correctly.)
- `toMarkdown` writes the **original bytes**, never the trimmed label.
  `serializerContract.ts` excludes `html` from the whitespace-insensitive
  leaves, so any whitespace change inside a comment is semantic drift and the
  write is refused.

The label is a ProseMirror text child (`createTextNode`), never `innerHTML` —
same property `rawHtmlPassthrough.test.ts` pins for raw markup, and for the same
reason: Milkdown has shipped that vulnerability class twice in adjacent nodes.

**The editing surface wears `.md-body`.** Typography comes from the shared
`styles/markdown-content.css`, so the editor and the rendered entity view
cannot drift apart. `milkdownEditor.css` holds only editing-specific chrome
(toolbar, gutter handle, floating menus, resize handles) — do not restate
heading or list rules there.

Two things the toolbar does that are worth preserving. Command availability is
a **dry run** (`commandAvailability.ts`): a ProseMirror command called without
`dispatch` reports whether it would apply, so the disabled state cannot drift
from what the command does. And an *active* command is probed by its INVERSE,
because that is what pressing it runs — probing forward would disable the very
button that removes the formatting.

**Link URLs are validated on the way IN, and never on load.** `linkUrl.ts`
gates the two places a user supplies a target (the dialog and the paste
handler) against an allowlist of `http`/`https`/`mailto`. Two things about it
are load-bearing:

- **Do not delegate to the preset's `sanitizeLinkHref`.** It normalizes only to
  DECIDE the scheme and then returns the ORIGINAL string, so a smuggled
  zero-width character survives into the stored markdown, and a leading one is
  not refused at all. It is also absent from the package's public `.d.ts`.
  `normalizeLinkUrl` strips those characters itself and normalizes through
  `new URL()`, storing `url.href`. The preset's sanitizer stays where it
  belongs, on the render side.
- **The gate must never run at parse time.** A body that already contains a
  `javascript:` link keeps it byte-for-byte; it is defanged at every render
  sink instead. Sanitizing on load would rewrite an author's stored content —
  `rawHtmlPassthrough.test.ts` calls that "a data-integrity bug wearing a
  security fix's clothes" and pins it.

**The link button never dispatches `ToggleLink` directly.** That command is
`toggleMark`, whose `removeWhenPresent` tests `.some()`, so over a selection
that merely TOUCHES an existing link it removes that link and discards the URL
the user just typed. `linkSelection.findLinkAt` is what routes an overlap to a
retarget instead. Note also that `@milkdown/components/link-tooltip` registers
a second, different `$command('ToggleLink')`; string lookup resolves to
whichever registered first, and `commandNamesExistInEditor` cannot catch it
because the NAME is still there. That package is deliberately not installed.

`tableCommands.ts` carries hand-written guards where the dry run is not
trustworthy: the GFM schema is `table_header_row table_row+`, and the
ProseMirror table commands do not know the header is mandatory. They report
success and then corrupt the table. Those guards (`canAddRowBefore`,
`isInHeaderRow`, `isOnlyBodyRow`) are the exception, not the pattern.

### The sandboxed app editor (`src/app-editor/`)

`<rela-editor>`, the Custom Element custom apps embed (TKT-D2JML7). It runs the
same Milkdown editor behind a six-item public contract — `value`, `placeholder`,
`readonly`, `input`, `change`, `focus()` — which is the swap seam and survived
the EasyMDE → Milkdown move unchanged. See `internal/dataentry/CLAUDE.md` for
the serving side.

**Share the editor's modules; do not mirror them.** The command catalogue, the
active and availability probes, the `entityRef` node, the serializer contract,
the write-back guard and the toolbar icon geometry are all imported from
`components/forms/milkdown/`. A second copy of any of them means the two editors
can serialize the same body differently, which is precisely the drift the
EasyMDE build carried (it hand-mirrored a slice of `markdown-content.css` and
needed `markdownContentMirror.test.ts` to catch the copies diverging; both are
gone).

**What IS local is only what needs a framework in the SPA.** The bundle is a
plain IIFE with no Vue, so the toolbar (`relaToolbar.ts`) and the `@` menu
(`relaMentionMenu.ts`) are built through the DOM API. Anything reaching for Vue
or the axios API layer cannot be imported into it — that is why the ranking
lives in its own `rankMentions.ts` rather than in `useMentionMenu.ts`. Both
editors therefore share the fuzzy scorer; the SPA's **type picker** does not
travel with it, because a sandboxed app runs under `connect-src 'none'` and has
no route to the schema's type list.

**References render as BARE IDS here.** The SPA resolves titles from the
server's per-principal `mentions` map; the app bridge has no such endpoint, and
deriving a title any other way would route around the read gate (BUG-R9EHKV).

**`input` comes off the ProseMirror view hook, not the markdown listener.** The
listener is debounced 200ms, so a change made and read inside that window would
never reach the app — exactly what an app saving on submit does. It must also
not fire from `appendTransaction`, which runs while the new state is still being
assembled: an earlier version did, and every listener read the document as it
was BEFORE the edit.

**`.value` reads through `guardWriteBack`.** The getter is the app's save path,
so the churn suppression has to sit on it; returning the raw serialization would
hand an app a reformatted body it never edited.

**The `@` mention query goes to `/_search` exactly as typed.** `rankMentions.ts`
re-ranks the response client-side; it must never rewrite the request. Splitting a
hyphenated query into words was measured to destroy ID ranking on the bleve
backend (the regression BUG-O09QUC exists to prevent) and to match nothing at all
on the linear and postgres backends, which match the query as one substring.
Keeping it a single token is also what makes `@status:open` inert rather than
filter syntax. The one thing removed is a type scope (`@ticket:fa` searches `fa`),
which travels as the `type` parameter `searchEntities` already takes: the type is
a filter, never part of the scored haystack, and the scope resolves only to a name
the schema supplied (so `?type=` stays an allowlist). An unknown `foo:` is plain
query text.

Four things in that file are load-bearing and each was a real defect first:

- **Rank, do not filter.** Only uFuzzy's own `filter` may drop a row. A "match
  quality floor" (`info.terms >= needed`) rejects every *prefix* — uFuzzy counts
  `fancy-` as zero complete terms — so the menu blanked on each keystroke until a
  word was finished. Complete-word fixtures pass happily while the feature is
  unusable, which is why `rankMentions.test.ts` asserts over **every prefix** of
  a target query. Any floor added later must pass that matrix.
- **Guard the query uFuzzy only PARTLY understands, not just the fully
  untokenizable one.** `uf.filter()` returns **`null`** (not `[]`), and
  `uf.split()` silently keeps only what its Latin-oriented splitter matches:
  `日本語-jp` → `["jp"]`, and `café` → `["caf"]`, so this hits ordinary accented
  European titles too, not only CJK and Cyrillic. Ranking on the surviving
  fragment drops rows the server matched on the discarded part — the same "No
  matches over a good response" failure, reached through a different door.
  `tokenizerSawWholeNeedle` requires the terms to cover the query's
  letter/number characters and passes the server's order through otherwise. It
  needs its explicit `terms.length === 0` branch: an all-separator query like
  `---` has zero meaningful characters, so a bare coverage comparison reads as
  "fully covered" and falls through to a filter that matches nothing.
- **Never widen the response.** Recall is the server's job and it is ACL-gated, so
  a row the ranker invented would be ungated. The haystack uses the `_title` the
  server already redacted — never `properties.title`, and never a locally derived
  title (see the ACL rule above).
- **Leave uFuzzy on its defaults; `intraIns: 1` is a tab-freeze.** It buys one
  typo (`rankng` → `ranking`) and costs exponential backtracking whenever a
  repeated-character run in the needle meets one in the haystack: measured 27 s
  at a 24-character needle and 55 s at 64, against 0 ms on the defaults. The work
  is synchronous on the main thread and the 150 ms debounce bounds how *often*
  matching starts, not how long one run takes, so a long repeated-run title (a
  plain long title no validator rejects) plus a long query stalls the editor while
  the user holds unsaved work. A length bound does not help — the blowup starts
  around 16 characters. The motivating `fancyreport` → `FancyReport` case does not
  need the option. Pinned by a **cost** assertion, because every correctness
  fixture passes either way; if that test hangs rather than fails, that is the
  regression.

**What the SPA menu shows is decided by `mentionPlan.ts`** (TKT-39TIB4): a
starting list for a bare `@` (related entities, then recently viewed ones from
`utils/recentEntities.ts`, then recently modified), types for one or two letters,
types then results at three, results then types from four. The plan is pure and
re-derived from the query on every keystroke; the controller keeps no scope of
its own. The recently viewed list stores only IDs and reloads each through the
read gate, so localStorage never holds a title.

**Every read the menu makes goes through one world** (`mentionWorld.ts`): the
world `worldForFace` names for the face being edited, else the page's
`?world=`. The reference it writes is a bare id either way, so the world only
decides what can be found and whose title a row shows. Starting lists are kept
per world, because their caches hold rows resolved in one.

**A type scope is document text, not menu state.** Choosing a type rewrites
`@ti` to `@ticket:`; `mentionArm.ts` draws a chip over it as a decoration.
Backspace over the `:` unscopes through ordinary editing, which is why the keymap
has no Backspace case. An earlier design held the scope in memory and needed a
Backspace handler that read a query mirror one tick behind the document; do not
bring either back.

**The menu opens only for a TYPED `@`.** `mentionArm.ts` records the position of
an `@` that arrives through `handleTextInput` and follows it through edits. It
lets go for good when the cursor leaves the token, a space ends it, the `@` is
deleted, or the editor blurs. Paste, undo and cursor moves into old text never
reach `handleTextInput`, so they cannot open the menu. Two consequences:

- The keymap's Escape must `stopPropagation`: the global shortcut handler blurs
  the focused input on Escape, and that blur would release the `@`.
- `MilkdownEditor.vue` calls `mention.onKeydown` for EVERY key, before looking at
  `menu.state.open`. SlashProvider updates on a debounce, so an Enter pressed
  straight after typing can arrive before the menu has opened; the handler
  re-reads the document first, then waits for the pending search (spec 6.7).

**The highlight is an identity, never an index.** `MentionMenuState.highlight`
holds `{kind:'type',name}` / `{kind:'entity',id}` / `null` and
`highlightedIndex()` resolves it against the live rows on each read. The rows
change asynchronously underneath it (type rows re-plan per keystroke, entities
arrive a debounce later), and a stored index was wrong four ways: it could point
past the end (so `current()` returned null, the keymap fell through without
`preventDefault`, and Enter put a paragraph break in the document), or slide onto
an unrelated row when the list above it shrank (so Enter inserted an entity the
user never highlighted). A changed scope or stage also clears `items` and the
highlight at once, so rows from the previous question cannot be picked. **Assert
these before the debounce resolves**: `sync` drops an entity highlight that
the arriving rows no longer contain, so a test that settles first cannot see
this class of bug at all.

The sandboxed app editor (`src/app-editor/`) deliberately stays on EasyMDE —
see TKT-D2JML7 and the CSP note in `internal/dataentry/CLAUDE.md`.

## CSS Architecture

Global styles in `App.vue` use CSS custom properties for theming:

- Light/dark mode via `:root.dark` class
- Shared utility classes: `.btn`, `.btn-primary`, `.modal`, `.page-header`
- Components use scoped styles with BEM-like naming

### Design tokens: two files, different contracts

| File | Holds | Contract |
|------|-------|----------|
| `src/styles/tokens.css` | **Colour only** | Copied byte-identically into the Go binary (`internal/dataentry/apps_tokens.css`) and served to custom apps as `_rela.css`. `TestAppTokensCSSInSyncWithFrontend` fails on drift. |
| `src/styles/scales.css` | Spacing, radius, typography, elevation | Not in `_rela.css`, so an app author cannot rely on these — except the four `--font-size-*` steps noted below, which ARE a frozen contract. The `<rela-editor>` bundle concatenates this file for its own chrome, which is why the editor may use `--space-*` and `--radius-*` while an app's own markup may not. |

Both are imported from `main.ts`. Keep them separate: `tokens.css` documents
itself as theme-tokens-only, so dimension scales do **not** belong there.

**Use the scales instead of hardcoding.** Prefer `var(--space-sm)`,
`var(--radius-md)`, `var(--font-size-base)`, `var(--shadow-sm)` over raw px.
Before this existed the SPA had 10 distinct border-radius values and 17 font
sizes chosen per-component, so nothing lined up between components.

**Focus rings are two shadows and a token, never a hand-written colour**
(TKT-FRING7). The pattern is:

```css
.thing:focus-visible {
  outline: none;
  box-shadow:
    0 0 0 2px var(--focus-ring-gap),
    0 0 0 4px var(--focus-ring);
}
```

Use `--error-ring` in place of `--focus-ring` for a validation-failed field.
Three things this encodes, each of which was a live bug before:

- **A literal cannot follow the theme.** 26 rings were written as
  `rgba(99, 102, 241, 0.1)` — an indigo that is not this app's accent, is
  wrong in dark mode, and is unreachable by an operator's `custom.css`.
- **The ring is opaque on purpose.** Every translucent value fails WCAG 2.2
  §1.4.11's 3:1 minimum for non-text UI — the old ring measured 1.13:1. Don't
  "soften" it back to a tint.
- **The inner gap band is load-bearing, not decoration.** Focus rules usually
  also set `border-color: var(--accent-color)`, which is the same colour as
  the ring, so a single shadow abuts its own border at 1:1 and reads as one
  thick band. A checked `CheckboxWidget` is the extreme case: accent ring on
  an accent fill, i.e. invisible.

`styles/focus-ring.css` restores a real `outline` under
`@media (forced-colors: active)`, where Windows High Contrast discards
`box-shadow` and would otherwise leave no focus indicator at all. It needs
`!important` — a bare `:focus-visible` (0,1,0) loses to every
`input:focus { outline: none }` (0,1,1) it must override. That is the one
sanctioned `!important` in the tree; don't copy the pattern for styling.

`styles/focusRing.test.ts` enforces all of this by reading the source, because
Vitest does not apply scoped SFC styles — a mounted-component assertion on
ring colour passes against no CSS at all.

**`--font-size-{sm,base,lg,xl}` is a frozen cross-boundary contract.** The same
four names and values are declared in Go, in `appTypographyCSS`
(`internal/dataentry/apps_css.go`), and served to every custom app — TKT-PF4E6S
froze them. Changing one side without the other makes an app's typography drift
from the host UI it renders inside.

`TestFrozenTypographyContractMatchesSPA` reads **both files off disk and
compares them to each other**, so a revalue on either side fails. Don't
"simplify" it into asserting Go against literals in the test file: that only
proves Go is self-consistent and cannot see a change to `scales.css` at all.

SPA-only sizes stay **outside** the ramp's naming. `--font-size-dense` (13px)
is a role name, not a `-md` step, precisely so nobody reads it as part of the
contract and copies it into a custom app — where it is undefined and would
silently fall back to the inherited size. `TestAppCSSSource` pins the negative
side (apps must not define it).

**Token values are chosen to be value-preserving,** not to be the shortest
ramp: `--radius-lg` is 8px because 8px is the card radius across the app, so
adopting the token doesn't quietly flatten every card. When you add a token,
prefer an existing value in the tree over a rounder number.

Off-scale sizes (9/10/15/16/17/20/21/24/48px) are deliberately left as
literals — each is rare and rounding them would change a size the author picked
on purpose.

### Property layout: one grid, one stylesheet

`src/styles/properties-list.css` owns `.properties-list` / `.property-item`,
shared by `SectionEditForm`, `PropertyDisplay` and `SidePanel`. **Do not
redefine those classes in a component `<style>` block** — they used to be
declared three times with three different `min-width` values, scoped so they
could never actually share, which is why the detail page didn't align with
itself.

Layout is a **12-column grid** (TKT-5V8704). Every item spans all 12 unless the
view/form config authors a `span:`, which arrives as a `--field-span` custom
property. The default lives in exactly one place: the `var(--field-span, 12)`
fallback. Don't emit a literal `12` from Go or a component — that creates a
second copy of the default.

`utils/fieldSpan.ts` is the only place a span becomes CSS. It clamps to 1-12
and returns `undefined` for anything else, so config never reaches a stylesheet
unvalidated. The server already rejects bad spans at load with a specific
error; the frontend clamp is defence-in-depth for hand-crafted responses.

Careful with grid children: in `DynamicForm`, `.form-fields > *` and
`.form-field` have equal specificity, so both read `var(--field-span, 12)`. A
bare `span 12` on the first rule would win on source order and silently swallow
every authored span — which is exactly what happened the first time.

### Cascade layers: all rela CSS lives in `@layer rela`

A Vite `generateBundle` plugin wraps every emitted stylesheet in `@layer rela`.
This is what lets an operator's `custom.css` win the cascade — an unlayered
declaration outranks a layered one regardless of source order **or**
specificity.

The split is deliberate: `relaCssLayer.ts` holds the pure `wrapCss` logic (no
`vite` import, unit-tested by `relaCssLayer.test.ts` beside it) and
`vite.config.ts` holds the thin plugin binding, mirroring how
`vite.editor.config.ts` defines its plugin locally.

**Both files must stay at the `frontend/` root, in the `tsconfig.node.json`
project.** They cannot move under `src/`: `vue-tsc -b` builds project
references, so a file owned by *both* projects fails `TS6305` ("output file has
not been built from source file") and one imported *across* the boundary
without being listed fails `TS6307`. Neither reproduces on a warm tree — a stale
`node_modules/.cache/tsc-node` hides `TS6305` — so verify config moves with a
real `rm -rf node_modules && npm ci`, which is what CI runs.

It is not cosmetic. The build emits ~19 stylesheets; 18 are route chunks that
Vite appends to `<head>` at RUNTIME, i.e. *after* the operator's injected
`<link>`. Before layering, operator CSS lost every equal-specificity tie against
a route view — a skin worked on the dashboard and silently died on a list view.

Rules:

- **`:root` token blocks stay OUTSIDE the layer.** `wrapCss` splits them out.
  `tokens.css` is byte-identical to `internal/dataentry/apps_tokens.css`, which
  is served to custom apps in iframes as `_rela.css` — where there is no other
  rela CSS to layer against, so layering would merely demote the tokens beneath
  every unlayered rule an app author writes. `TestTokensCSSNeverLayered` pins
  both copies; `TestBuiltCSSIsLayered` pins the split in the build output.
- **Don't hand-write `@layer` in a component `<style>` block.** The wrap is
  applied once at build time. A competing layer declared in source would
  establish its own ordering against `rela`.
- **`!important` INVERTS under layers.** rela's layered `!important` beats an
  operator's unlayered `!important`. That is a documented, permanent property
  (see `docs/customisation.md`), not a bug to "fix" by unlayering.
- **The wrap is build-only.** `generateBundle` does not run under `vite` dev
  server, so `npm run dev` has NO layer and a different cascade from production.
  Verify cascade-sensitive changes against `npm run build`. (`npm run build:e2e`
  IS a real `vite build`, so the e2e suite does exercise the layer — don't
  "optimise" e2e onto the dev server.)

### Testing `rela-` custom elements: SFC fixtures only

`isCustomElement: (tag) => tag.startsWith('rela-')` is set in **both**
`vite.config.ts` and `vitest.config.ts`, so `<rela-slot>` and `<rela-editor>`
are treated as native custom elements rather than unresolved Vue components.

⚠ A regression test for this **must use a `.vue` SFC fixture** (see
`src/components/__tests__/fixtures/RelaSlotHost.vue`). Runtime-compiled string
templates never see build-time `compilerOptions`, so a string-template test
reports "Failed to resolve component" even when the config is correct — a false
negative that already cost one debugging cycle.
