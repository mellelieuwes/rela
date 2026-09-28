# CLAUDE.md

## Interacting with tickets & docs

Use the `rela` CLI, not MCP. Pick the project with `--project=tickets` or
`--project=docs-project`.

```bash
rela --project=tickets list ticket --filter "entity.status == 'ready'"
rela --project=tickets show TKT-XXXX
rela --project=tickets create ticket -P title=... -P kind=chore -b '## Description ...'
rela --project=tickets update TKT-XXXX -P status=done
rela --project=tickets delete TKT-XXXX --force --cascade   # --cascade also drops its relations
rela --project=tickets link TKT-XXXX affects some-concept
rela --project=tickets unlink TKT-XXXX affects some-concept
```

- `-P/--property` takes `key=value` and is repeatable. It **splits on commas**,
  so escape any comma in a value as `\,` or the value is read as another
  property and rejected.
- `-b/--body` sets the markdown body; `-B/--body-file` reads it from a file or
  `-`. Both replace the whole body.
- `-o json` on any command when you need to parse the output.
- `--filter` takes a predicate expression; the older `--where key=value` is
  deprecated.

Before considering ticket work done, run the three gates and fix what they
report:

```bash
rela --project=tickets analyze cardinality   # missing required relations
rela --project=tickets analyze properties    # missing/invalid required properties
rela --project=tickets analyze validations   # custom rules (checklists, 5-whys, ...)
```

`analyze all` adds orphan/duplicate/gap reports; those are advisory and the
project has a standing backlog of them, so judge your entity by the three gates
above rather than by a clean `analyze all`.

## Rules for new code

- **Define interfaces at the call site, not next to the implementation.**
  Producer-side interfaces couple consumers to every method the producer
  exposes. Each consumer declares the minimum interface it needs (one to three
  methods). When a callback would create a constructor cycle, the consumer
  defines the narrow interface and the wiring site supplies it — see
  `docs/architecture/consumer-side-interfaces.md` and the godoc on
  `autocascade.Host`, `mcp.GraphReader`, `scheduler.WorkspaceProvider`.
- **Capability bundles, not service locators.** When a subsystem needs several
  collaborators, group them in a purpose-specific struct (see
  `internal/lua/deps.go` with `ReadDeps` / `WriteDeps`), split by read vs. write
  so read-only code can't accidentally mutate state. A scoped consumer-side
  `Services` interface is fine (see `internal/mcp/server.go`); a cross-subsystem
  grab-bag is not.
- **No repository or transaction abstractions.** Depend directly on
  `store.Store`, `tracer.Tracer`, `search.Searcher`, `entitymanager.Manager`.
  The old `repo` and `tx` layers are gone — do not reintroduce equivalents. The
  one sanctioned transaction seam is `store.Store.Tx` itself (DEC-8UIL0): a
  contract ON the store with per-backend meaning (fs/mem: write mutex, mutual
  exclusion only; postgres: native transaction + advisory lock, rollback, events
  at commit) — not a generic unit-of-work layer stacked above it. Don't wrap
  `Tx` in a new abstraction, and don't do slow external I/O inside a `Tx`
  callback.
- **Capture state once per operation.** Call `ws.Snapshot()` (or the equivalent
  `appState.Load()`) at the top of every handler, command, MCP tool, or
  observer; reuse the returned value for every read in that operation. Do not
  call `ws.Graph()` / `ws.Meta()` repeatedly — multiple loads against the
  underlying `atomic.Pointer` can observe different snapshots if a reload lands
  between them.
- **Don't leak storage or parsing types via return values.** A function that
  returns `*markdown.Document`, `*graph.Graph`, `interface{}`, or any type whose
  package the caller wouldn't otherwise need pulls the implementation into every
  consumer. Return value types or domain-package DTOs (`entity.Entity`,
  `entity.Relation`, `tracer.Result`). If you reach for `interface{}` plus a
  type assertion as a back-channel, define a typed dependency instead.
- **Split state-publish from write-serialize.** Use `atomic.Pointer[State]` for
  publishing a new state snapshot (no reader lock, no torn reads) and a separate
  `sync.Mutex` for serializing writers. Do not combine both responsibilities
  into a single `sync.RWMutex` — the lock-upgrade dance
  (`RUnlock → Lock → defer(Unlock → RLock)`) is the symptom, not the fix.
- **Constructors reject nil required fields.** A `New*` function with required
  collaborators returns `error` and validates them up front. Never substitute a
  no-op or sentinel implementation silently — that defers the failure to a
  downstream symptom that is much harder to diagnose.
- **Restrictions compile at LOAD time; the evaluator has no denial primitive.**
  Client attenuation (`client_baselines` / `scope_grants`, TKT-IAC8TX) restricts
  a client below the user it acts as, but it does so by compiling into plain
  allowlists when `acl.yaml` loads — `redact: {person: [salary]}` becomes
  "person's permitted fields, minus salary". `decideFromAttrs`, `readQuery`,
  `grantsPermission` and `FieldVerdicts` keep seeing allowlists, so DEC-RG878's
  additive union semantics are intact. **Do not add a runtime deny.**
  `ReadQuery` compiles to a `store.GraphQuery` pushed into SQL, so a runtime
  denial would have to become a SQL predicate in every backend, and every
  evaluation path plus all of `internal/aclmap` would need re-deriving.

  The clamp point is `Request.roleFor` — every evaluation path resolves role
  names through it, so reaching into `policy.Roles[...]` directly from a new
  path silently bypasses the ceiling. A guard test (`ceilingguard_test.go`)
  scans the package and fails on that; it uses an exemption list, so a new file
  must be clean or explicitly exempted.

  A ceiling only ever NARROWS (`effective = user_grants ∩ (baseline ∪ scopes)`),
  so a bug fails toward less access — except in the compilation step, which is
  why that has direct unit tests rather than only end-to-end ones.

  **What this rule does NOT forbid**: adding a new allowlist DIMENSION to the
  compiled result. The prohibition is on subtractive evaluation — a `deny` the
  evaluator applies per row — not on the query carrying more allowlists.
  `ReadQueryResult` already carries a type verdict and a composed `GraphQuery`;
  a per-face allowlist (TKT-FACEREAD) is the same shape: computed from grants at
  compile time, pushed down as an additional predicate, still additive. It costs
  one predicate per backend, not a re-derivation of the evaluator.
- **Read-out paths go through visibility wrappers, base readers stay ungated.**
  Read-side ACL (entity row-gating + field-level `visible:` redaction) is
  enforced by `internal/visibility` decorators (`Reader`, the tracer decorator)
  injected at the wiring site — never by per-consumer redaction calls, and never
  inside `store`/`tracer`/ `search` themselves (DEC-ZBI39P; the
  `search.VisibleSearcher` pattern generalized). **Row-level**: a hidden entity
  is nonexistent — pruned subtrees, withheld paths, a denied GET
  indistinguishable from a real 404 (whether an entity _exists_ is a genuine
  secret). **Field-level** (`visible:`): redaction hides property **values
  only** — it makes no claim to conceal _which_ properties exist, since the
  metamodel (declared property names per type) is served over the API. A
  "field-existence oracle" is not a threat this guards against; code need not
  contort to hide field names, only their values. A system job that may read
  everything gets an `AllowAllReader` capability at wiring while keeping its
  genuine `system:*` principal for audit — allow-all is never inferred from
  identity. Write-prep reads (entitymanager diffing) keep raw store access: a
  redacted read-modify-write would clobber hidden fields.
- **Aggregates computed over the graph gate BEFORE they fold.** The gantt
  endpoint (`internal/dataentry/gantt_handler.go`, TKT-MW28U5) pins the pattern:
  row-gate the node set, redact each entity ONCE, then run the roll-up fold,
  then compute caps/`truncated` — all on the filtered tree. The `_views`
  pipeline's traverse-raw-then-redact-on-the-way-out order is safe for flat
  collections but NOT for an aggregate: folding raw values launders a hidden
  entity's dates into a visible parent's rolled span (a value disclosure, not
  the accepted one-bit membership channel), and a pre-filter count or truncation
  flag is an existence oracle (`TestGantt_ACLRollupExcludesHiddenChild`,
  `TestGantt_TruncatedIsPostFilter`). Any new derived-over-subtree value (sums,
  progress %, counts) follows the same order, and the result is per-principal —
  never cache it across principals. `visibility.Redact` is non-composable (raw
  store entities in, exactly once), so the redaction point stays single.
- **Partial writes go through `entitymanager.Manager.PatchEntity`, never
  read-modify-write.** Name the properties you are changing in an `entity.Patch`
  (`Properties` upserts, `MetaUnset` removes, `Content` is a `*string`
  tri-state) and the manager merges them against the raw stored entity
  internally. Properties you do not name are preserved — **forgetting one is a
  no-op, not an erasure.**

  The alternative — `GetEntity` → clone → merge → `UpdateEntity` — requires
  holding the _whole_ entity, so anything you failed to carry across is
  destroyed on save. That is unrecoverable when the read was redacted: a caller
  who cannot see a property cannot carry it, and silently deletes it. This used
  to be guarded by prose and a raw `lua.ReadDeps.WritePrepStore` handle;
  TKT-80EWGM removed both, so the mistake is now unavailable rather than merely
  discouraged. Pinned by `TestPatchEntity_PreservesUnnamedProperties` and
  `TestScriptReads_UpdatePreservesHiddenProperties`.

  `UpdateEntity` still exists for callers that legitimately own the whole entity
  (a form save that renders every field). `ApplyEntity` is the whole-record
  replace the sync channel needs. If you are writing a _subset_, you want
  `PatchEntity`.
- **Background jobs: the queue knows nothing about schedules, and never runs
  before a transaction closes.** External side effects (mail, HTTP, AI) belong
  on `jobs.Queue` rather than inline on a write path. Two rules keep the seam
  usable:

  _Retry is a flat enum_ (`RetryNever` / `RetryBounded` / `RetryPersistent`),
  plus an optional deadline and idempotency key — nothing else. The enum names
  INTENT; mechanism (attempt counts, backoff, the `RetryPersistent` outer bound)
  lives in `internal/jobs/retry.go` and is meant to be retuned there for
  everyone. Do NOT widen it into a policy struct or add per-call knobs: a call
  site needing different mechanics is evidence for a new intent value.

  _A recurring task uses `IdempotencyKey`, never a cadence-derived `Deadline`._
  A key says "one of these pending at a time is enough", so a run that is still
  queued suppresses the next rather than stacking a second copy — a daily report
  delayed six hours must not then send twice. A deadline expresses something
  different: "this is worthless after T", which makes the job VANISH when it
  cannot start in time. Under load that drops scheduled work precisely when the
  operator most wants it done, and (before the guard existed) hung the scheduler
  on a completion that never arrived. Deadlines are for work whose value
  genuinely expires; schedules are not that.

  The scheduler itself keys each job by its RUN id, not by task name
  (BUG-TKL08E). "One run per task at a time" is enforced by the run-state
  store (`internal/schedulerstate`), which every node can query. A task-name
  key made the queue a second, invisible source of truth: a job row the queue
  could not complete held the key forever and blocked every later run. Do not
  move non-overlap back into the queue key.

  _A job enqueued inside `store.Store.Tx` must not become runnable until that
  transaction commits._ Otherwise a worker reads it on another connection that
  cannot see the uncommitted writes and acts on the pre-write world — a race
  that passes tests and fails under load. `jobs.WithDeferral` collects enqueues;
  the transaction seam calls `Flush` on commit or `Discard` on rollback,
  mirroring pgstore's `txPending`. Pinned by `jobstest`.

  The fs/desktop tier is EPHEMERAL on purpose — jobs vanish on exit, because an
  unsent mail from an ended session is not worth resurrecting. Don't "fix" it to
  persist; that is what the postgres tier is for.

  _The durable queue's tables live in the TENANT's schema, like every other
  postgres-backed table._ A schema-pinned `search_path` is how rela scopes a
  tenant, and the queue is not exempt: rela submits every kind to one queue name
  and neoq's insert trigger does `pg_notify(NEW.queue, ...)`, so tables shared
  across tenants would mean tenants consuming each other's jobs. neoq v0.72.1
  could not do this — one migration named `public.neoq_jobs_id_seq` while its
  tables follow `search_path` — which is why `go.mod` carries a `replace` onto a
  fork (BUG-YJEIFH, upstream acaloiaro/neoq#149). Drop the `replace` when that
  lands, not before: `TestPostgresQueue_SchemaPinnedDSN` is what fails if it
  goes early. **Test any new postgres-touching dependency through a
  schema-pinned DSN**, not just the bare `RELA_TEST_DATABASE_URL` — the bare DSN
  resolves to `public`, which is precisely the one case that worked.

- **The configuration is not a secret; the data is.** `schema.yaml`,
  `data-entry.yaml`, `acl.yaml`, `schedules.yaml`, `scripts/`, `actions/`,
  `templates/` are operator-authored files that live in the repo — routinely a
  public one, as in any open-source app. Their _contents are already disclosed_.
  So list names, view/kanban/document/form/action names, entity and property
  names, `permission:` values, `script:` paths, even `command:` strings are
  **not** confidential, and code must not contort to conceal them: no filtering
  config endpoints per-principal, no indistinguishable-404-vs-403 on a _config
  key_, no narrowed wire types justified as leak prevention, no tests asserting
  a config name is unenumerable. The generalization of the `visible:` rule above
  (which already says property _names_ need no concealment because the metamodel
  is served over the API) — it holds for every config surface, not just the
  metamodel.

  What IS secret is **entity and relation content, and entity existence**. The
  read-path gating in `docs/acl-security.md` and the row-level rule above are
  about _rows_, not about the schema describing them. Keep the uniform 404 for
  an **entity id**; a 403 naming the missing permission is the right answer for
  a **config-declared capability**, and is more useful to the operator debugging
  it.

  This is settled, not open: `docs/acl-security.md` § "Sidebar menu structure is
  principal-independent" already records the decision. The menu carries no
  per-principal data: its structure is the same for every principal except for
  the opt-in `permission:` UX filter, and the rows behind an `entities:` entry
  (TKT-PEKL8L) come from the ACL-gated list API, not from the menu payload. The
  reason is that the metamodel is not a secret; it is served by
  `/api/v1/_schema`. Per-principal menu filtering is **deliberately not done**
  as a security measure. Don't reintroduce it as one.

  Two things this does NOT license. (1) _Secrets_ are not config:
  `.rela/secrets.yaml`, DSNs, and tokens stay off the wire — that is why
  `RELA_DATABASE_URL` is env-only. (2) A gate may still exist for
  non-confidentiality reasons — to keep an unusable entry out of a sidebar, or
  to stop an unauthorized caller triggering an expensive render — just don't
  justify it as concealment, because the next person will build on a secrecy
  property that was never real. Write down which of the two you mean.
- **Mail: the render pipeline order is a security property, and delivery is
  best-effort.** `internal/mailrender` runs markdown → goldmark → **bluemonday
  on the untrusted CONTENT ONLY** → trusted template → **douceur inline LAST**.
  Both ends are load-bearing and verified: bluemonday strips `style` attributes
  (so sanitizing the assembled document ships unstyled mail, and also strips the
  `cellpadding`/`border`/`role` and `cid:` sources email needs), while douceur
  does **no** CSS value validation (so nothing may sanitize after it, and every
  value interpolated into CSS — palette tokens included — must be allowlisted).
  Reversing either is a silent downgrade, not a build failure.

  **`lua` may import `mailrender`, and that does NOT invert the `mail → lua`
  arrow.** `mail.render` (TKT-1GA2PG) builds a `mailrender.Message` from a
  script table so a Lua author gets the hardened template instead of
  hand-writing HTML for `mail.send`. The arch-lint rule forbidding `lua → mail`
  is untouched and still holds; `mailrender` is a _different_ component and a
  true leaf (`go list -deps` shows zero internal imports), so the two arrows
  cannot form a cycle. The binding must never grow an `html:` or `css:` field —
  that would reintroduce the sanitizer bypass it exists to give authors an
  alternative to.

  **Email CSS is not web CSS, and the template's shape encodes that.** Section
  headings and the empty-section note are single-cell tables, and vertical gaps
  are spacer rows, because Outlook Windows honors `padding` only on table cells
  and `margin` is unsupported or partial across Gmail, Outlook, Yahoo and AOL. A
  `<div>` with padding renders fine wherever you are likely to test it and
  collapses where you are not. `internal/mailrender/compat_test.go` scores the
  rendered output against a **vendored, pinned** Can I Email dataset
  (`testdata/caniemail.min.json`) and fails on a regression — treat it as a
  floor, not proof the mail looks right.

  **Dark mode is defensive, and `<meta name="color-scheme">` is deliberately
  absent.** Clients split three ways: some leave mail alone (Apple Mail, Gmail
  desktop, Yahoo, AOL), some partially invert and honor `prefers-color-scheme`
  (the Outlook family), and some fully invert and rewrite the query to
  `@media none` so they cannot be targeted at all (Gmail iOS/Android, Outlook
  Windows). The `@media` block serves the middle group; the palette (mid-tone
  borders, no pure white on pure black) serves the third. Adding the meta tag
  looks like a free win and is the trap: it opts Apple Mail _into_ inverting,
  making a currently-correct rendering worse. A test asserts its absence.

  **A message's language belongs on `Message`, never on `Options`.** `Options`
  is renderer-scoped branding and a `Renderer` is built once per deployment, so
  an `Options.Lang` would stamp one language on every mail an instance sends —
  and a Dutch digest and an English one cannot both be right. `Options` carries
  only the _default_. The tag is validated in `mailrender` (shape-only BCP-47,
  rejected not escaped) because it arrives from both operator config and
  untrusted Lua, and validating at either call site would leave the other open.

  The SMTP password lives in **`.rela/secrets.yaml`** under `smtp_password` —
  the same store Lua scripts read, because an SMTP credential is no different in
  kind from the API tokens already kept there. `password_env` in
  `.rela/mail.yaml` names an environment variable as a fallback for
  container/systemd deployments; secrets.yaml wins when both are set. Never a
  literal `password:` in mail.yaml — that is refused at load.

  Header-injection validation is **rela's**, not the SMTP library's: go-mail
  rejects CR/LF in addresses but accepts it in a subject, where it is
  neutralized only incidentally by encoded-word escaping. `internal/mail`
  rejects CR/LF/NUL in every caller-supplied header value at enqueue.

  The outbox is an in-process buffer, **not a durable queue** — in `rela-server`
  there is no signal handler, so pending mail is lost on every restart with no
  drain. Mail is notification, never a system of record. A durable queue with
  swappable backends is IDEA-WIJ2H1.
- **Collection reads are content-free, batched per page, and paged in the store
  when they can be** (TKT-1U8XYN). A list, search, kanban or scope pipeline
  reads `store.EntityHeader` rows (`ListEntityHeaders`, `GraphQueryHeaders`) and
  never a body it will not render; a body is loaded for the served rows only, on
  `include_content=true`. Per-row lookups are the defect this rule exists to
  prevent: a page loads its edges with ONE `RelationQuery.EntityIDs` query, its
  neighbours with ONE header batch, a table section its relation columns with
  one query per (column, row type). Write authorization reuses the request's
  `acl.Request` (the membership walk runs once per operation, not once per verb
  per row). When the request's shape allows it, the list handler pushes paging,
  ordering and equality filters into `store.GraphQuery` (`listpushdown.go`) and
  takes the scoped count through `store.CountMatched`, never `GraphCount`'s
  total. A query scope joins that pushdown only when `queryplan.LowerScope`
  lowers it EXACTLY (TKT-XKCNCL): nothing re-checks a pushed scope, so a
  superset pre-filter is not enough, and a scope that does not lower keeps the
  Go path. Its traversals ride in `GraphQuery.Related`, never in
  `HasInbound`/`HasOutbound`, which belong to the ACL read gate. New read
  paths pin their cost with a `storetest.Counting` budget test
  asserting the count is the same at 10 and 50 rows. Measure on the postgres
  backend with `rela-server -verbose` (`Server-Timing`, one `request` log line
  each) against `prototypes/perf/project` seeded by `rela dev seed`.
- **Boundaries are enforced.** `just arch-lint` checks package import rules; run
  it before PR.

### Don't do this

- **Don't import `internal/graph` or `internal/model`** — both deleted. Use
  `internal/entity`, `internal/store`, `internal/tracer`.
- **Don't add a cross-subsystem service locator** (à la the removed
  `lua.Services`). Use `ReadDeps` / `WriteDeps` or a scoped consumer-side
  interface.
- **Don't call `ai.LoadProvider` directly from a new entry point.** Go through
  `script.NewWriterRuntime`, which calls `lua.LoadContextOptions`.
- **Don't wire AI into the validation path** — per-entity cost blowup with no
  quota. See `internal/ai/` docs for the rationale.
- **Don't reintroduce `internal/workspace`.** It was the legacy god-object
  aggregate; deleted in the workspace-decomposition arc. New code wires services
  individually via `appbuild.Discover` / `appbuild.New` or takes focused
  interfaces at the call site.
- **Don't run user-supplied Lua on the read path.** ACL gates evaluate against
  declarative policy + the graph; Lua participates only at write time. This
  targets unbounded/hot paths (per-row predicates on list reads), NOT a bounded
  single-subject evaluation the caller explicitly requested (e.g. performable
  transitions for one field on one entity). See
  `internal/entitymanager/CLAUDE.md`.

### Subsystem-specific rules (nested CLAUDE.md / godoc)

- **Writes, audit, ACL** → `internal/entitymanager/CLAUDE.md`. All writes go
  through `entitymanager.Manager`; do not write to `store.Store` directly from a
  write path.
- **Data-entry API + `_actions` affordances + write-validation policy** →
  `internal/dataentry/CLAUDE.md`.
- **Vue SPA build/test/architecture** → `frontend/CLAUDE.md`.
- **E2E tests** → `e2e/tests/AGENTS.md`.

## Architecture

rela is a schema-driven entity-graph platform. You define the shape of your
domain in a YAML metamodel (entity types, relation types, properties, validation
rules); rela gives you typed entities, typed relations, and tools to query /
validate / analyze / present the graph. Data is stored as markdown files with
YAML frontmatter.

Traceability (requirements → decisions → components) is one common use case, not
the identity. Other in-tree uses: ISO 27001 ISMS, project management, DevOps
runbooks, issue/ticket tracking (rela dogfoods itself — see `tickets/`),
documentation mirrors (`docs-project/`). Anything with typed entities and
relations fits.

```text
schema.yaml → Metamodel (entity types, relations, properties)
                     ↓
entities/*.md  → entity.Entity  ↘
                                 store.Store → tracer.Tracer  (pure reader)
relations/*.md → entity.Relation ↗          → search.Searcher (EntityObserver)
                                            → entitymanager.EntityManager
                                              (writes + automations + validation)
```

The store is the source of truth. `search` maintains a derived index as a
`store.EntityObserver`. `tracer` is a pure reader — no subscription, no derived
state. `entitymanager` is the "human intent" write path that runs automations
and validation on top of the store.

Write-path rules — validation policy (400/422/200-with-warnings), the audit log,
and ACL — live in the nested files `internal/dataentry/CLAUDE.md` and
`internal/entitymanager/CLAUDE.md`.

### Packages

Entry points: `cmd/rela`, `cmd/rela-server`, `cmd/rela-desktop`.

Domain and storage:

| Package                  | Purpose                                                                                          |
| ------------------------ | ------------------------------------------------------------------------------------------------ |
| `internal/entity`        | Domain types: `Entity`, `Relation` (no storage metadata)                                         |
| `internal/metamodel`     | Schema: entity types, relations, properties, validation                                          |
| `internal/store`         | Storage abstraction — CRUD + events; `fsstore`/`memstore`/`pgstore`                              |
| `internal/tracer`        | Pure-reader graph traversal (trace, path, orphans, cycles)                                       |
| `internal/relresolve`    | Answers `related(...)` in predicate programs: one gated store query per traversal per batch      |
| `internal/calfeed`       | Pure calendar-feed model + iCalendar/JSON serializers (event-granular; no store/vendor)          |
| `internal/mailrender`    | Pure message model → sanitized, CSS-inlined branded HTML + text/plain (leaf; no store/metamodel) |
| `internal/mail`          | Outbound email: `Sender` seam, SMTP + memory transports, `.rela/mail.yaml`, best-effort outbox   |
| `internal/search`        | Full-text + structured search (bleve + linear)                                                   |
| `internal/visibility`    | Read-side ACL wrappers: row-gate + field-redact readers, tracer decorator (DEC-ZBI39P)           |
| `internal/entitymanager` | Write path: automations, validation, audit, policy                                               |
| `internal/audit`         | Append-only JSONL audit log of every successful write                                            |
| `internal/jobs`          | Background-job seam: ephemeral (fs/desktop) or durable (postgres)                                |
| `internal/principal`     | Identity attribution (`Principal{User, Tool}`) on ctx                                            |
| `internal/validator`     | Validation engine invoked by entitymanager                                                       |
| `internal/markdown`      | Parse/write entity and relation markdown                                                         |
| `internal/project`       | Project discovery, paths (`Context`)                                                             |
| `internal/appbuild`      | Wiring facade — constructs the focused services bundle                                           |

Subsystems (see each package's doc comment for details):

| Package                | Purpose                                                                                                    |
| ---------------------- | ---------------------------------------------------------------------------------------------------------- |
| `internal/cli`         | Cobra commands                                                                                             |
| `internal/mcp`         | MCP server over stdio — tools, resources, prompts, watcher                                                 |
| `internal/dataentry`   | Data entry web app (Go API + Vue 3 SPA in `frontend/`)                                                     |
| `internal/scheduler`   | Sequential Lua script scheduler (`rela scheduler`)                                                         |
| `internal/lua`         | Lua runtime + bindings (`ReadDeps`, `WriteDeps`)                                                           |
| `internal/script`      | Script execution helpers that wrap `lua` with project context                                              |
| `internal/automation`  | Automation engine invoked by `entitymanager`                                                               |
| `internal/autocascade` | Cascade orchestration (runs automation side-effects)                                                       |
| `internal/ai`          | OpenAI-compatible LLM provider (used from Lua)                                                             |
| `internal/migration`   | Schema migrations for project YAML files                                                                   |
| `internal/cmdexec`     | Safe external-command core (argv, no shell, `{in}`/`{out}`, timeout, cap) shared by attachment + transform |
| `internal/transform`   | View-export engine: markdown `Renderer` → external-tool format conversion (the `transforms:` registry)     |

Other packages under `internal/` are self-descriptive — ls the tree.

### Condition engine: `internal/predicate` + `internal/predicatefns`

`internal/predicate` is the shared **typed expression engine** — a sandboxed
Lua-expression subset with no I/O and fixed depth/step budgets. `Compile`
retains the boolean condition profile; `CompileValue` accepts an explicit
context profile for scalar computations. Programs expose exact static record
dependencies and conservative SQL-portability metadata. Context profiles may
enable or refuse language features, but an accepted IR node must keep identical
semantics across evaluators and future targets. `internal/predicatefns` is its
metamodel-aware glue: the `ScalarType`/`EntityRecordType` type adapter, the
host-fn stdlib (`match`/`regex`/`fuzzy`/`contains`/`len`/`today`), the
`FromFilter` transpiler, and the `Evaluator` (compile-once, metamodel-scoped
Program cache). New condition/`when:`-style code evaluates through `predicate`.

These surfaces are on predicate: ACL affordance `when:`
(`internal/affordances`), state-machine transition `When:`
(`internal/statemachine`), wizard-form condition lint
(`internal/conditionlint`), automation `on.when:`/`validate:`
(`internal/automation`), metamodel validation `When:`/`Then:`
(`internal/validation`), and the CLI `--filter` flag (`internal/cli/list.go`).

Automation `on.condition:` and validation `when_condition:`/`then_condition:`
take predicate **expressions** as written, ANDed with the filter-syntax
`when:`/`then:` keys beside them. They are separate keys because the two
syntaxes overlap without erroring: `filter.Parse` accepts
`days_between(entity.due, today()) <= 7` as a filter on a property named
`days_between(entity.due, today())`, which matches nothing, silently. Don't add
dialect sniffing — the key IS the declaration of intent. A `condition:` that
fails to compile is a **load error** (`NewEngineFromMetamodel` returns one), as
is an unparseable `when:` clause: dropping a constraint widens the automation,
so failing the load is the safe direction.

`internal/filter` is NOT frozen — it remains the **query-filtering** DSL (the
`--where` string syntax and metamodel legacy filter-strings). Legacy
`--where`/`When:`/`Then:` inputs are transpiled to predicate via
`predicatefns.FromFilter` on load (`--where` is deprecated in favor of
`--filter`). `filter.Match` still directly backs query-filtering in
`internal/dataentry` (SPA view/feed `where:`), `internal/lua` (script queries),
`internal/search/searchparser`, and `internal/cli/analyze.go` — these were
**not** migrated (they filter result sets, they don't gate conditions). Don't
describe filter as "removed" or "frozen"; it's the query-filter DSL, predicate
is the condition/policy engine.

### View export & transforms (`internal/transform`)

The `transforms:` map in the metamodel registers named `markdown → format`
external commands (see `docs/transforms.md`). A `transform.Renderer` produces
markdown; the engine runs it through a transform via `internal/cmdexec` (argv
array, no shell, temp-file `{in}`/`{out}`, timeout, output cap — the same
security-reviewed exec pattern `internal/attachment` uses). Rules for new code:

- **Export is downstream of an already-authorized view, never a new
  capability.** Entity/list export in `internal/dataentry` routes through the
  SAME ACL read path as the view (`visibleReader.getVisible` /
  `scopedSortedEntities`); a request may only choose a registered transform
  _name_, never a command/flag/path.
- **The list-table renderer lives in `internal/dataentry`, not
  `internal/transform`** — it needs the ACL neighbor-visibility gate
  (`visibleRelationIDs`) so hidden neighbor titles never leak into an export.
  `internal/transform` must NOT import `internal/dataentry`; the built-in
  single-entity renderer lives in `transform`, and `dataentry` supplies the list
  renderer as a `transform.Renderer`.
- **The per-type render override (`views.<type>.export_render`) renders through
  `documentService.RenderMarkdown`** — the same Lua document machinery, reached
  only AFTER the export has resolved the entity through the ACL read gate. Never
  call `script.ExecuteDocument` on a fresh unauthenticated surface, and keep the
  entity id path-validated (`isSafePathSegment`) before it reaches a render.
- **Export downloads are hardened** like attachment downloads (nosniff, sandbox
  CSP, `no-store`, sanitized `Content-Disposition`) — the produced bytes embed
  user content.
- **External commands are CONFINED in `internal/cmdexec`, and it fails closed.**
  Both export and attachment processing run third-party parsers over
  attacker-influenceable bytes, so the shared runner adds: a no-network,
  temp-dir-only sandbox (bubblewrap on Linux, `sandbox-exec` on macOS), rlimits
  (memory/PIDs/file size/CPU, Linux), process-group kill so a converter's helper
  cannot outlive the timeout, and a bounded pool capping concurrent runs. On a
  host with no mechanism, commands REFUSE to run — only command execution is
  blocked, never server startup. Do not add a "can I run?" predicate: call `Run`
  and handle its error; `Describe()` exists solely for the startup log.
- **The transform engine must be built ONCE and shared**, not per request — it
  owns the bounded pool, so a per-request engine gives every request its own
  pool and the concurrency cap bounds nothing.

### Storage backends & build tags

The storage + search backend is chosen at compile time by Go build tags. The
composition root has one `New` recipe per scenario over shared
`prepare()`/`assemble()` helpers — see
`internal/appbuild/appbuild_{fs,memory,postgres,sqlite}.go` and the matching
`internal/cli/mcp_wiring_{fs,memory,postgres}.go`:

| Build tag         | Store         | Search                            | Binaries                                |
| ----------------- | ------------- | --------------------------------- | --------------------------------------- |
| _(none, default)_ | `fsstore`     | in-memory bleve                   | `rela`, `rela-server`                   |
| `memorybackend`   | `memstore`    | `LinearSearch`                    | (tests / experiments; no bleve)         |
| `postgres`        | `pgstore`     | PostgreSQL (`pg_trgm` + tsvector) | `rela-postgres`, `rela-server-postgres` |
| `sqlite`          | `sqlitestore` | in-memory/on-disk bleve           | `rela-sqlite`, `rela-server-sqlite`     |

`sqlitestore` is the **single-process** backend (DEC-LFSYNY): one embedded
database file at `.rela/rela.db`, no server, and `Open` takes an exclusive
sidecar lock so a second process is refused rather than admitted. That refusal
is load-bearing — `unique:` is enforced by an untransacted scan in
entitymanager, so two writers would have no backstop and the violation would be
silent. It takes the strong `Tx` tier (rollback, post-commit-only events) and,
since TKT-4NU9ZD, content versioning too — so history comes from the database
rather than from git, which it cannot use because the markdown files are not the
source of truth. Since TKT-B51CYD its graph queries run as SQL, like pgstore's,
with `graphquerynaive` as the reference: `storetest.RunGraphDifferential`
compares the two on randomized queries for both database backends, and a name
the builder cannot render as a literal JSON path falls back to the naive
path. It also refuses to open on a filesystem where WAL cannot be
enabled (iCloud/Dropbox/SMB), because SQLite is unsafe there.

Rules when touching this:

- **The `postgres` build must not link bleve; the default build must not link
  pgx; no build but `sqlite` may link `modernc.org/sqlite`.** CI asserts each of
  these via `go list -deps` (the `postgres` job in `ci.yml`). Keep
  backend-specific imports inside the tagged recipe files.
- **`pgstore.New(db DBTX)` takes an injected pgx pool**, not a DSN. The postgres
  recipe builds one pool, runs `pgstore.Migrate`, and shares it between the
  store and the in-DB search backend. appbuild owns/closes the pool;
  `store.Close()` only tears down the watcher.
- **Build-agnostic wiring lives in `prepare`/`assemble`, never in a recipe.** A
  recipe may choose and order backend steps; if logic would be copy-pasted
  between recipes, it belongs in a shared helper. This is what keeps the three
  recipes from drifting (and where future per-backend audit/ACL variation goes).

  `prepare`'s result is the exported **`appbuild.SharedBase`** (TKT-P938T7): the
  tenant-independent half — validated config, options, parsed `acl.yaml`, loaded
  metamodel — with nothing derived from a store. Build one with `NewSharedBase`
  and call `base.Assemble(store, …)` once per store; `New`/ `Discover` are that
  path with a single store. **The split is NOT along the `Services` field
  list**: `acl.Declarative` is built FROM the store (it needs a store-backed
  `acl.Graph`), so the ACL _policy_ is shared while the _evaluator_ is per-store
  — same for `lua.ReadDeps`. Two invariants keep reuse safe, both pinned by
  tests in `sharedbase_test.go`: assembly must never mutate `meta` or
  `aclPolicy` (they are pointers handed to every assembled `Services`, so a
  write leaks across tenants), and `Services.Close` must tear down only the
  store and search closer it was assembled with — never anything shared, or
  evicting one tenant breaks its siblings.
- **The metamodel is always read from disk**, even in the postgres build —
  `schema.yaml` and `templates/` stay on the filesystem, as does
  operator-authored config generally; PostgreSQL backs
  entities/relations/attachments/search. A postgres deployment still needs a
  `--project` dir.

  The exception is **runtime-written state** (TKT-VC27L3): on the postgres build
  `state.KV` is database-backed (`pgstore.StateKV`, wired via `stateKVFor`), so
  the document render cache, user settings, the operator logo/theme and the
  CalDAV alias table live in the `state_kv` table rather than under `.rela/`.
  That is deliberate — `docs/postgres-backend.md` documents several rela-server
  processes against one database, and node-local state means an uploaded logo is
  served by exactly one of them. Rows sit in the store's schema, so
  schema-per-tenant scopes this state for free. **Key validation is the `state`
  package's job** (`state.ValidatedKV` wraps the backend at the wiring site):
  pgstore must not import `internal/state` (arch-lint forbids a store depending
  on an application package), so it stores whatever key it is handed and the
  wrapper enforces the same rules `storage.RootedFS` gives FSKV. Any new backend
  must pass `internal/state/statetest.RunAll`.
- **Comments are backend-selected, and deliberately NOT in the graph**
  (TKT-OGTVJW). `comments.Store` has three implementations — `filecomments`
  (fs/memory/desktop), `pgcomments`, `sqlitecomments` — chosen by the RECIPE,
  which passes one into `buildComments` via `backendOverrides.commentStore`; nil
  selects the file backend. Any new one must pass
  `internal/comments/commentstest.RunAll`.

  The two database backends were split along the SAME reasoning that divided
  `state.KV` from versioning, and the split is the point. **postgres**: a
  comment posted through one `rela-server` node was invisible to the others, the
  defect class TKT-VC27L3 fixed for `state.KV`. **sqlite** is single-process, so
  that argument does not apply and the decision went the other way anyway —
  commentary is content ABOUT content, so it must travel with `rela.db` the way
  versioning does (TKT-4NU9ZD), not stay node-local the way the render cache
  does (TKT-L1A3PH). "Is this about the machine or about the content?" is the
  question to ask of the next such table.

  Neither backend may import `internal/store` (arch-lint enforces it): each
  declares its own narrow `DBTX` and takes an injected handle, so comments are a
  third and fourth consumer of the one pool, never a store concern. Comments
  stay outside the graph entirely — no entity type, no audit log, no versioning,
  no search indexing (TKT-FIO205, permanently decided).
- **Multi-writer change feed** (TKT-WZYWM9). The postgres watcher delivers
  cross-process writes via PostgreSQL `LISTEN/NOTIFY`: each committed write does
  `pg_notify(rela_changed, '<origin>:<schema>:<kind>:<op>:<id>')` inside its
  transaction (so the 5 single-statement writes are wrapped in a tx); a listener
  goroutine (own connection, started in `Open`, stopped in `Close`) turns remote
  notifications into `store.Event`s on the in-process `Subscribe()` fan-out. Two
  payload fields do the routing, both filtered on receipt: a per-store random
  `originID` drops self-echoes (local writes are already emitted in-process),
  and the writing `schema` drops traffic from other schemas sharing the channel.
  NOTIFY is best-effort, so a `seq > watermark` catch-up (overlap window +
  idempotent re-snapshot; runs on connect/reconnect/safety-ticker, NOT per
  notification) recovers anything missed. **The channel is ONE constant
  (`rela_changed`), not one per schema** (TKT-9TOEBH): LISTEN is database-global
  _and_ needs a dedicated session, so a per-schema name would cost one
  permanently-held connection per schema — the term that does not shrink under
  pooling. Isolation lives in the payload instead. What is **not** shared is the
  catch-up: `rela_seq` is per-schema and the catch-up query is unqualified SQL,
  so priming/catch-up stay bound to each store's own pool — do not "simplify"
  them onto a shared connection. If the listener can't connect, the store
  degrades with a warning (local events still work). Exact ordering (xid8 +
  `pg_snapshot_xmin`) is the documented upgrade, not built. The data-entry SSE
  feed consumes this via `App.startStoreEventBridge` (entity events only).
  fsstore/memstore stay in-process single-writer by nature.
- **Content versioning** (TKT-9INY0Y; pgstore below, and sqlitestore since
  TKT-4NU9ZD — the two are held to one contract by `storetest.RunVersionTests`,
  declared via `Capabilities{Versioning}`). Two tables (`entity_versions` = one
  full snapshot per version; `schema_versions` = content-addressed render-schema
  projection, deduped) plus a dedicated `version_seq` sequence. **Use
  `version_seq`, never `rela_seq`** — `rela_seq` feeds the change-feed watermark
  (`primeWatermark`/`catchUp` scan entities/relations/deletions), and burning it
  on version rows that don't land in those tables would erode the overlap budget
  and drop real events. Capture is **hybrid**: rename+delete are captured
  synchronously at the entitymanager boundary (they carry old→new id /
  pre-delete state the sweep can't reconstruct); create/update are captured by a
  debounced reconciliation **sweep** goroutine (`sweep.go`, started/stopped like
  the listener). The sweep runs its **entire tick on ONE acquired pool
  connection** under `pg_try_advisory_lock` — the lock is session-scoped, so
  issuing the inserts via the pool (other sessions) would silently void the
  single-writer guarantee. Attribution comes from ctx only, via exactly two
  boundary-populated inputs — the store never learns the Principal by another
  route: sync captures carry it inside `store.VersionInput`, and create/update
  writes carry a `store.Attribution` on ctx (`store.WithAttribution`, set ONLY
  at the entitymanager boundary and ONLY for a real principal — never translate
  a zero/unknown principal, RR-U964M0) which pgstore stamps into
  `entities/relations.last_edited_by_user/_tool` (TKT-ZIRMGM). The sweep copies
  those columns onto swept versions; NULL columns (legacy rows, unattributed
  writes) fall back to the `version-sweep` system principal — never a guessed or
  literal-"unknown" identity. Author-boundary segmentation (flush-on-author-
  change) is TKT-0IGI4V, not built: two authors in one debounce window merge
  into one version attributed to the last of them. Lineage across a
  rename/id-reuse is fenced by `[lo,hi)` vseq ranges in a recursive-CTE walk (an
  unbounded `entity_id = ANY(...)` read would merge two entities' histories —
  see the version.go doc). `HistoryReader`/`VersionWriter` are optional store
  capabilities (type-asserted like `store.Formatter`), NOT part of
  `store.Store`.
- **Relation versioning** (TKT-92JL8P; both database backends) extends the above
  to relations, which carry their own props + body. A `relation_versions` table
  reuses `version_seq` + `schema_versions`; identity is a surrogate
  `rel_record_id` **column ON the `relations` row** (`DEFAULT nextval(...)`,
  carried through writes) — NOT reconstructed per sweep-tick, which would race
  the sync path and merge/fork lineages. Delete+recreate of the same
  `(from,type,to)` mints a fresh id (histories don't merge). Capture:
  create/update via the sweep's second `FROM relations` scan
  (entities-then-relations, same tick/lock); **delete synchronously via
  `DeleteResult.DeletedRelations`** — the single path for BOTH explicit
  `DeleteRelation` and entity **cascade** delete (the store bulk-deletes
  relations below the entitymanager, so cascade edges would otherwise lose
  history). Rename **stitches** (not forks): the entitymanager captures a
  `rename` version per incident relation on the new triple carrying
  `prev_from`/`prev_to`, and `relationLineageIDs` walks those links so history
  is continuous. Since #1127 the store renames **atomically** (bulk in-place
  `UPDATE relations SET from_id=...`), so a relation KEEPS its `rel_record_id`
  across the rename — the lineage is already continuous on one id and the
  `rename` version merely appends a marker (the `prev_from`/`prev_to` stitch walk
  finds no fork; it stays as belt-and-braces for any future non-atomic path).
  Rename capture is **sync-only best-effort**: the atomic re-key does NOT bump
  `relations.updated_at` (TKT-9TQ6I), so the sweep cannot back-fill a rename the
  synchronous hook misses — acceptable because a miss loses only the rename
  marker, never lineage continuity. Read/restore is gated on **both** endpoints
  (FROM ∧ TO) — the FROM
  entity only _owns_ the UI placement, it is not the auth boundary (a TO-side
  oracle otherwise). Relations have NO field-level redaction today; relation
  history exposes exactly what a live relation GET does. `RelationHistoryReader`/
  `RelationVersionWriter` are SEPARATE optional capabilities, type-asserted
  independently of the entity ones.
- **Version purge** (TKT-BW6UUL; both database backends) is the audited, irreversible
  exception to append-only history — hard-deletes version rows for compliance
  redaction. `VersionPurger`/`RelationVersionPurger` are SEPARATE optional
  capabilities (`purge.go`), one `PurgeVersions`/`PurgeRelationVersions` method
  each. Load-bearing guardrails (design-review, do not relax): the whole op runs
  under **`sweepAdvisoryLockKey`** (mutually exclusive with a sweep tick — a purge
  racing a capture-insert loses the erasure); it **REFUSES while a live row still
  holds the content** unless `--force-live` (else the sweep re-captures it within
  one interval — a `VersionOpPurge` no-content tombstone whose content_hash = the
  live hash suppresses that re-capture via the sweep's existing dedup); it
  **REFUSES a rename row** (purging one orphans/forks the lineage walk — v1 is
  non-rename-only); `--all` purges the **fenced lineage** (`lineageCTE` /
  `relationLineageIDs`), never `WHERE id=$1` (id-reuse would destroy unrelated
  history). CLI-only (`history-purge`/`relation-history-purge`), dry-run by
  default; trust boundary is operator shell (no ACL check — like `db migrate`),
  audited via the `audit.Audit` sink (`OpPurgeVersion`, `svc.Audit()`), never
  echoing purged content. `schema_versions` is projection-only + FK-shared, so
  purge never deletes it. Purge is necessary-not-sufficient for erasure (live
  row / PITR backups survive) — see the postgres-backend guide.
- **Data migration** (TKT-0C57FS, TKT-XCJ0Y2, `internal/datamigration`,
  `docs/data-migration.md`). Two separate questions, deliberately not
  conflated:

  _Which migrations have run_ is answered by NAME, from a per-store
  `datamigration.StateStore`. A migration file is therefore NOT an edge in a
  hash graph, and a **data-only migration** (backfill, de-dup, correcting an
  old bug's values) is an ordinary file whose two projections match. Files are
  `<14-digit timestamp>-<lowercase-slug>.yaml`, validated through
  `MigrationName` on both the directory listing and the applied list — the two
  are compared by equality, so a case-folding filesystem would otherwise make
  one file look like two entries. Don't reintroduce sequential numbering: it
  collides silently across concurrent branches (BUG-TY2XQC is that defect in
  rela's own pg ladder).

  _What shape the data conforms to_ is the `ShapeProjection` the record also
  stores, which the gate classifies against and `rela migrate gen` diffs.
  **Two schema hashes coexist on purpose**: `RenderProjection` (version
  rendering, `schema_versions` dedup — stability load-bearing, do not extend)
  vs `ShapeProjection`.

  **The projections stay embedded in every migration file.** They are what
  step `Validate(from, to)` checks targets against (a rename step is
  well-formed only where the old property exists in the FROM shape, which the
  live schema no longer has once a later migration ran) and what
  `validateDeltasResolved` recomputes to refuse a file that spans a
  needs-migration change its steps don't answer. Computing either against the
  live schema instead collapses the whole chain into one aggregate delta and
  reopens BUG-TMGWIN. Removing them is not an optimization.

  **The state store is backend-selected**, like `comments.Store`: a COMMITTED
  `migrations/applied.json` on fs (the gitignored `.rela/` would be absent from
  a clone), `migration_state` in the tenant's schema on pg (so tenants at
  different points migrate independently — a documented guarantee), the same
  table in `rela.db` on sqlite (a shipped file must carry it). New backends
  pass `migstatetest.RunAll`.

  **`Gate.Evaluate` classifies; `Gate.Persist` writes, and only the CLI calls
  it.** A server writing a git-tracked file at boot would dirty a working tree
  and need a writable project dir; it also removes the concurrent-start race
  outright. A server may serve with an unrecorded ADDITIVE change — harmless by
  construction. With no record AND migrations present the gate refuses
  (`StatusUnbaselined`) rather than baselining over files that may still need
  to run; `rela migrate baseline` is the explicit override.

  Migration/GC writes are the third sanctioned raw-store exception (after
  `db migrate` and `history-purge`): operator-shell trust, no ACL, explicit
  audit records (`data-migration`/`data-gc`), `store.WithAttribution`, and
  synchronous pre-delete version capture on pg (the sweep cannot reconstruct
  deleted rows). **Steps must stay idempotent — with the applied list as the
  only double-apply guard, re-run IS the crash recovery.** The Lua step is a
  pure transform (patch in, patch out, engine applies); never hand it a write
  handle.
- **Perf seeding** (TKT-1U8XYN, `internal/perfseed`, `rela dev seed`) is the
  fourth raw-store exception, under the same terms: operator shell, attributed
  (`perf-seed` tool), one `perf-seed` audit record, and it refuses a non-empty
  store. Because nothing above the store runs, the generator keeps the
  invariants the store cannot: ids minted by construction and validated, the
  single `unique:` property unique by construction, every edge endpoint emitted
  by the same generator. Do not route it through entitymanager to "fix" that —
  20k automations per seed is the cost it exists to avoid.
- DSN is read from the `RELA_DATABASE_URL` env var **only** — there is no
  `--database-url` flag, so the credential never lands in `ps`/shell history.
  `appbuild.Discover` reads the env into `appbuild.Config.DatabaseURL`; the `db`
  commands read the env directly. Don't add a DSN flag.
- **Derived static-query indexes are all-or-nothing desired state.** The
  PostgreSQL and SQLite reconcilers own only `rela_derived_query__*` /
  `rela_derived_list__*` and derive those indexes from validated static
  dashboard/next-action/list shapes (`appbuild.staticIndexSpecs`, shared by
  both). Never
  reconcile a partial set after a `data-entry.yaml` read/parse/validation
  failure: an absent desired object means DROP, so partial input is destructive.
  Runtime/ad-hoc queries never issue DDL. Pushdown and index inference must use
  the same `internal/queryplan` eligibility decision, and an EXPLAIN test must
  prove each newly supported SQL shape actually uses its generated index, on
  both backends (`EXPLAIN QUERY PLAN` on sqlite). SQLite matches an expression
  index only when the query spells the expression identically, so its DDL is
  built with the query builder's own helpers (`sqlitestore/derivedschema.go`). A
  next-action `condition:` participates on both sides: its store-safe scalar
  equalities (`entity.x == 'lit'`, `entity.x == current_user.id`,
  `is_current_user(entity.x)`) are pushed with the query and derive columns of
  the SAME composite index; `has_current_user(list)` is pushed as a non-scalar
  `PropEqual` (jsonb containment) and deliberately derives no index, since the
  btree over `->>` does not serve it — a GIN shape would need its own EXPLAIN
  test first.
- **Migrations** are embedded SQL (`pgstore/migrations/*.sql`), applied by
  `pgstore.Migrate` in one transaction under a `pg_advisory_xact_lock`
  (concurrent-start safe; forward-only). Auto-applied on first store open; also
  runnable explicitly via the postgres-build `rela db migrate` /
  `rela db status` commands (`pgstore.Status` is the read-only version check).
  `rela db` errors clearly in non-postgres builds.
- A new `store.Store` implementation must pass `internal/store/storetest`
  (`RunAll` + the fuzz functions). pgstore's suite is DB-gated on
  `RELA_TEST_DATABASE_URL` (skips when unset). Run it with `just test-postgres`.

## Tests

- Prefer table-driven tests with `t.Run(tc.name, ...)` subtests.
- Use `t.Helper()` on assertion helpers.
- `internal/store/storetest` provides the store conformance harness — any new
  `store.Store` implementation must pass it. Likewise any new
  `search.VisibleSearcher` implementation must pass
  `storetest.RunVisibleSearchTests` (the ACL-scoped search contract).
- Race detector is on in CI; don't add `//go:build !race` tags.

## Coverage

Go: `go-test-coverage` enforces **package floor thresholds** (no ratchet);
minimums live in `.testcoverage.yml`. Coverage within the floor is free to move
up or down — floors exist to catch "new untested package added" and "core
package silently lost its tests." The frontend has no coverage enforcement —
unit tests run plain (`npm run test:run`).

- Run locally: `just coverage-check`, `just coverage-html`.
- When a floor fails, add tests — don't lower the threshold without a reason.
- Use `// coverage-ignore: <reason>` sparingly, only for genuinely untestable
  code (main functions, external-tool dependencies, OS-specific paths). Reason
  is required.

## Lint

golangci-lint with project rules. Test files exempt from `dupl`, `funlen`, magic
numbers. Cobra `cmd`/`args` unused parameters allowed. Line length: 120.

**God-object load lines** (`just plimsoll`, CI job "God-object lint"). The
[plimsoll](https://github.com/sourcehaven-bv/plimsoll) linter caps three
independent surfaces — the metric that tracks a type accreting into a god-object
(`App`, `Runtime`, `FSStore` got there because nothing stopped them):

- **`max-methods` (40)** — total methods, exported + unexported. The backstop
  for internal sprawl: a receiver with dozens of private helpers is one struct
  whose fields they can all reach.
- **`max-exported-methods` (20)** — exported methods only. The sharper signal,
  since the public API is the coupling surface consumers bind to. Note these
  often diverge wildly from the total: `App` is 226 methods but only 13
  exported; the genuinely-wide _public_ APIs are the store implementations and
  schema value types (`FSStore`, `MemStore`, `Metamodel`).
- **`max-fields` (20)** — exported struct fields.

A new type over any line fails CI. Existing offenders are grandfathered with a
`//plimsoll:max-methods=N` / `max-exported-methods=N` / `max-fields=N` directive
at the declaration site, pinned to the current count so they can't grow; ratchet
those down as you decompose (TKT-N0IKN9). A store-implementation's exported
count is the mandated `store.Store` interface, so its directive is a documented
"required interface" exception rather than a ratchet target. Prefer splitting
the type over raising the number.

**Comment discipline** (`just comment-lint`, CI job "Comment lint").
[commentlint](https://github.com/sourcehaven-bv/commentlint) checks comments
against the scope they are attached to. `commented-code` and `doclink` are
**blocking gates** (both clean); the rest are advisory (`just comment-report`)
with a backlog being worked down:

- **`duplication`** — the same fact explained in two or more comments. The
  signal we act on: a fact stored three times gets corrected in one place and
  goes stale in two. Remedy is to hoist it to the type or package they cite.
- **`nil-contract`** — nil behaviour as ad-hoc prose. Go cannot express this in
  a type, so the convention is `Nil: rejected|accepted|never returned — <why>`.
  Fixing one removes it permanently (the rule skips tagged comments).
- **`doclink`** (gate) — a `[Bracketed.Reference]` that resolves to nothing. Go
  degrades these silently (pkg.go.dev renders the literal brackets) and no other
  linter catches them — `go vet`, `staticcheck` and `godoclint` all report zero
  on a broken link. Most are a bare `[Method]` where Go needs `[Recv.Method]`;
  the finding names the qualified form. Note Go cannot link an unexported member
  or a symbol from an unimported package at all — those references should simply
  lose their brackets.
- **`param-contract`** — a precondition asserted about a bare `string`/`int`
  parameter ("MUST already have passed containedPath"). Usually a missing type;
  this repo already does it where it matters most, e.g. `principal.Principal`
  keeps `roles` unexported so they can only enter through a verifying
  constructor.
- `too-long` and `scope-reach` are **off** — see `.commentlint.yml` for why.

False positives are expected (every rule is a heuristic over prose). Suppress
with `//commentlint:ignore <rule>  <reason>` on the declaration line, or via
`.commentlint.yml` when the same prose recurs across many sites.

**Read the finding before suppressing it.** A blocking check with an easy escape
hatch makes silencing the cheapest path to green, and a reviewer skimming a diff
cannot tell a considered suppression from a reflex one. Fixing the comment is
the outcome the gate exists for; suppression is for findings that are genuinely
_wrong_, and the reason must say why. "Suppressed to unblock CI" is not a
reason. The failure message says all this too, at the moment it matters.

## Security

`govulncheck` runs **daily** from `security.yml`, which auto-opens an
auto-merging `go get` PR when a fix exists and files a tracking issue when one
does not. It also gates releases (the `security` job in `release.yml`) — a
release must not ship a known vulnerability.

It deliberately does **not** run on the PR path. A vulnerability in an
already-merged dependency is not a defect in whatever PR happens to be in
flight, and gating there blocks unrelated work: the old `ci.yml` job scanned
only on a `go.mod`/`go.sum` diff, but the merge queue runs on `merge_group` (not
`pull_request`) and took the unconditional-scan branch — so an advisory would
pass on the PR and then dequeue it from the merge queue. Don't reintroduce a
blocking vulnerability check on PRs; raise the scan cadence instead.

Known-unfixable vulns are filtered via `scripts/govulncheck-filtered.sh` — keep
`IGNORED_OSVS` in sync with `scripts/govulncheck-fixable.sh`. Run locally:
`just govulncheck`.

## Commands

Read the `justfile` for the full set. The non-obvious ones: `just arch-lint`
(package boundary check), `just ci` (full pipeline), `just dev` (data-entry
server locally), `just coverage-check`. `go test -run TestName ./...` for a
single test.

## Project files

```text
schema.yaml                     # Entity/relation schema (was metamodel.yaml)
schedules.yaml                  # Optional: schedules for `rela scheduler`
entities/<type>/                # Markdown entity files by type
relations/                      # Markdown relation files (FROM--type--TO.md)
templates/entities/<type>.md    # Optional: entity templates for defaults
templates/relations/<type>.md   # Optional: relation templates for defaults
migrations/<stamp>-<slug>.yaml  # Optional: data migrations (committed)
migrations/applied.json         # Which migrations have run (COMMITTED, fs tier)
.rela/user-defaults.yaml        # Per-user defaults (gitignored)
.rela/scheduler-run-state.json  # Scheduler runs + last-run times, non-pg builds (gitignored)
```

## Working documents

Anything temporary — designs, tickets, QA notes, scratch — goes in `.ignored/`
(gitignored). Do not commit these.

<!-- @managed: claude-workflow start -->

## Rela for Planning & Issue Tracking

This project uses two rela instances via MCP for design and issue tracking:

- **rela-docs**: Documentation entities (concepts, features, guides, tutorials,
  scenarios)
- **rela-issues-and-design-tickets**: Issue tracking (tickets, features,
  decisions, concepts, risks, measures, tests)

### Workflow for Creating Tickets/Entities

When creating or updating entities in `rela-issues-and-design-tickets`:

1. **Create the entity** with required properties
2. **Run ALL analyze tools** to check for issues:
   - `analyze` with `check: cardinality` - check required relations
   - `analyze` with `check: orphans` - find unlinked entities
   - `analyze` with `check: properties` - validate property values
   - `analyze` with `check: validations` - run custom validation rules
3. **Fix any violations** (create missing relations, add required properties,
   etc.)
4. **Repeat analysis until ALL checks pass** - do not stop after fixing one
   issue

### Common Required Relations

| Entity Type          | Required Relations                                                                                                         |
| -------------------- | -------------------------------------------------------------------------------------------------------------------------- |
| ticket               | `affects` → concept (min 1), `implements` → feature (min 1)                                                                |
| feature              | `requires` → concept (min 1)                                                                                               |
| test-case/test-suite | `test-covers` → concept (min 1), `verifies` → feature/ticket (min 1)                                                       |
| doc-task             | `affects` → concept (min 1), `triggered-by` → ticket/feature/decision (min 1), `updates` → guide/tutorial/scenario (min 1) |
| research             | `researches` → concept (min 1)                                                                                             |

### Research Documents

For larger features, run `/research <topic>` before planning to survey
approaches and document tradeoffs. This creates a `research` entity (RES-xxxx)
with structured sections: Problem, Context, Options, Recommendation.

**Workflow:**

1. `/research` creates the entity in `in-progress` and links it to concepts
2. The agent surveys the codebase and external approaches
3. Options are documented with pros/cons/effort
4. A recommendation is made and presented for user review
5. The research is linked to the ticket/feature via `has-research`

**When to use:** Enhancements or features where the approach isn't obvious,
multiple viable options exist, or the change touches unfamiliar subsystems. The
planning checklist has a research item that can be skipped with N/A for smaller
work.

### Validation Rules

The metamodel includes validation rules that enforce:

- In-progress bugs should have `why1` and `why2` started
- Done bugs must have 5-whys analysis (`why1`-`why3` required) and `prevention`
- Ready tickets need `effort`, `priority`, and `description`
- Accepted decisions need `date`, `context`, and `consequences`

Always run `analyze` with `check: validations` to catch these issues.

### 5-Whys for Bug Analysis

Bug tickets use the 5-whys method for root cause analysis:

| Property | Purpose                          |
| -------- | -------------------------------- |
| `why1`   | What was the immediate cause?    |
| `why2`   | Why did that happen?             |
| `why3`   | Why did that happen?             |
| `why4`   | Why did that happen?             |
| `why5`   | What is the systemic root cause? |

Done bugs require at least 3 levels (why1-why3). The goal is to reach systemic
causes that can be addressed with process/tooling improvements documented in
`prevention`.

### Workflow Checklists

Tickets and bugs use workflow checklists to ensure thorough planning, execution,
and review. Each phase has a dedicated checklist entity with standard items from
templates.

**Ticket Workflow:**

```text
backlog → ready → planning → in-progress → review → done
                     │            │           │
                     ▼            ▼           ▼
              planning-      implementation-  review-checklist
              checklist      checklist        (+ docs-checklist
                 │                            for enhancements)
                 ▼
           /design-review
           (before impl)
```

**Bug Workflow:**

```text
backlog → ready → analyzing → in-progress → review → done
                     │            │           │
                     ▼            ▼           ▼
              bug-analysis-  implementation-  review-checklist
              checklist      checklist
```

**Checklist Types:**

| Checklist                  | Purpose                                                      | Required For                             |
| -------------------------- | ------------------------------------------------------------ | ---------------------------------------- |
| `planning-checklist`       | Understanding, research, approach, security, risk assessment | Tickets entering `in-progress`           |
| `bug-analysis-checklist`   | Reproduction, root cause, fix planning                       | Bugs entering `in-progress`              |
| `implementation-checklist` | Development, quality checks                                  | Tickets/bugs entering `review`           |
| `review-checklist`         | Automated checks, code review, verification                  | Tickets/bugs entering `done`             |
| `docs-checklist`           | Code docs, project docs, external docs                       | Enhancement/docs tickets entering `done` |

**Review Commands:**

| Command          | When to Use                               | Creates                                      |
| ---------------- | ----------------------------------------- | -------------------------------------------- |
| `/design-review` | After planning, before implementation     | `review-response` entities for design issues |
| `/code-review`   | During review phase, after implementation | `review-response` entities for code issues   |

**Agent Workflow for Tickets:**

Checklists are **automatically created** when tickets/bugs transition to
specific statuses. The `create_entity` automation with `if_exists: skip` ensures
no duplicates.

1. **Start Planning** (status: `planning`)
   - Planning checklist is auto-created and linked via `has-planning`
   - Work through checklist items: understanding, approach, security, test plan
   - Run `/design-review` to catch issues before implementation
   - Address all critical/significant design findings
   - Mark checklist `status=done` when complete

2. **Start Implementation** (status: `in-progress`)
   - Implementation checklist is auto-created and linked via
     `has-implementation`
   - Work through development and quality items

3. **Start Review** (status: `review`)
   - Review checklist is auto-created and linked via `has-review`
   - Run `/code-review` to perform thorough code review
   - Address all critical/significant code review findings
   - If enhancement or docs ticket, manually create `docs-checklist`
   - Complete all checks before marking done

4. **Complete** (status: `done`)
   - All linked checklists must have `status=done`
   - All checklist items must be checked or skipped with reason

5. **Create PR** (after `done`)
   - Run `/pr` to create PR and monitor CI until all checks pass
   - Fixes any CI failures (lint, test, coverage) automatically
   - The PR URL and CI status are NOT recorded in the review-checklist. They
     post-date it — `/pr` gates on the ticket already being `done` and
     validating clean, and a `done` checklist may have no unchecked items, so an
     item asking for the PR URL could only be satisfied by a PR that does not
     exist yet (TKT-UFV01M). GitHub records both; the branch and commit messages
     carry the ticket ID.

**Bug Workflow Automations:**

- `analyzing` → auto-creates `bug-analysis-checklist` via `has-bug-analysis`
- `in-progress` → auto-creates `implementation-checklist` via
  `has-implementation`
- `review` → auto-creates `review-checklist` via `has-review`

**Skipping Checklist Items:**

When an item doesn't apply, use strikethrough with a reason in parentheses:

```markdown
- [x] ~~API docs updated~~ (N/A: no API changes)
- [x] ~~Performance check~~ (N/A: documentation-only change)
```

Items without reasons will fail validation.

### Review Response Protocol

**Triggering Code Review:**

When a ticket/bug enters `review` status, run the `/code-review` command. This
invokes the cranky-code-reviewer agent to perform a thorough code review and
automatically creates `review-response` entities for each finding.

Alternatively, invoke the cranky-code-reviewer agent directly for ad-hoc
reviews.

**Creating Review Responses:**

For each finding from code review:

1. Create a `review-response` entity with:
   - `title`: Brief description of the finding
   - `finding`: Full description of the issue
   - `severity`: `critical` | `significant` | `minor` | `nit`
   - `status`: `open`
2. Link to ticket/bug via `has-review-response` relation

**Addressing Review Responses:**

| Severity    | Required Action                    |
| ----------- | ---------------------------------- |
| critical    | MUST be fixed before done          |
| significant | MUST be fixed before done          |
| minor       | Should fix, can defer with reason  |
| nit         | Optional, can wont-fix with reason |

When addressing a finding:

- Fix the issue in code
- Update status to `addressed`
- Document the `resolution` (how it was fixed)

When not addressing:

- Set status to `wont-fix` or `deferred`
- Document the `reason` (justification required)

**Validation Gates:**

Tickets/bugs cannot be marked `done` if they have:

- Open critical review responses
- Open significant review responses

Minor/nit findings may remain open with warnings.

### Automation Actions

Status transitions auto-create checklists (and similar side effects) via
automations declared in the project's `schema.yaml`. Action types (`set`,
`create_relation`, `create_entity` with `if_exists`) and interpolation patterns
(`{{new.property}}`, `{{entity.id}}`, `{{today}}`) are documented in
`docs/metamodel.md` and exemplified in the live `schema.yaml`. Read those rather
than relying on a copy here — a stale copy is worse than a pointer.

Common mistake: `{{entity.title}}` is wrong; use `{{new.title}}` for a property
of the triggering entity.

<!-- @managed: claude-workflow end -->
