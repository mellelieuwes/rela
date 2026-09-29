# entitymanager — rules for new code

`entitymanager.Manager` is the "human intent" write path: it runs
automations and validation on top of `store.Store`, emits the audit log,
and consults the ACL. All entity/relation writes go through here — do not
write directly to `store.Store` from a write path, or the audit record
won't be emitted.

## Which write method

| Situation | Method |
|---|---|
| Changing a SUBSET of properties | `PatchEntity` |
| Caller legitimately owns the whole entity (form save rendering every field) | `UpdateEntity` |
| Whole-record replace from a trusted replica (sync channel) | `ApplyEntity` |

**Default to `PatchEntity`.** It takes an `entity.Patch` (`Properties`
upserts, `MetaUnset` removes, `Content` is a `*string` tri-state), does its
own write-prep read, and merges against the raw stored entity. Properties
the patch does not name are preserved regardless of whether the caller
could read them — so a caller holding a redacted view cannot erase what it
cannot see. `UpdateEntity` gives you no such protection: it saves exactly
the entity you hand it, so every property you failed to carry across is
gone.

`UpdateEntity` and `PatchEntity` share `updateCore`, so they cannot drift
on validation, automation, transitions, unique checks, audit, or cascade.
Attribution and authorization deliberately stay in the entry points —
`updateCore` has no ACL check, mirroring `createCore`. Do not move them in:
`PatchEntity` must authorize early (it needs the stored type for the ACL
subject), so an authorize inside the core would double-authorize and emit
two `denied-write` rows for one denial.

### PatchEntity ordering is load-bearing

```text
read (raw) → IsLocked guard → authorize → field gate → merge → twin guard → updateCore
```

- The **read comes first** because `acl.EntitySubject` needs the entity's
  real type and the caller supplied only an id. Same shape, and the same
  accepted existence disclosure, as `DeleteEntity`.
- The **field gate runs strictly after authorization**. Field verdicts are
  value-dependent, so consulting them for an unauthorized caller would leak
  both entity existence and stored property values through the
  allow-vs-deny difference. (Naming the *rule* in a denial is fine — config
  is not a secret. The leak is about data.)
- `Deps.FieldGate` is **required**, with `AllowAllFieldGate{}` as the named
  opt-out for operator-trust-boundary surfaces like the CLI. A nil gate is
  rejected at `New` rather than silently permitting everything.
- The gate constrains **caller-authored** changes only. Automation-derived
  properties are system writes and are deliberately not gated, or a user who
  cannot author `status` could never trigger an automation that sets it.
- **Elevation is total**: `bypass_acl` skips the field gate as well as the
  row ACL, and `recordACLBypass` still fires. A half-elevated handle that
  silently drops some property writes is the confusing contract the
  elevated-read seam exists to avoid.
- The **twin guard** ([`rejectTheirsChanges`](twins.go)) refuses a change to
  a field an external system owns through a twin (`Deps.Twins`, required;
  `NoTwinOwnership{}` opts out). It is change-based (runs after the merge,
  compares with `canonical.EqualValue`/`EqualBody`), runs on `UpdateEntity`
  and on `CopyState` into an existing target too, and — unlike the field
  gate — **applies under elevation**: bypass_acl
  lifts the ACL, not the ownership contract. Only the
  `TwinSyncWriter(m)` handle skips it, and `gated()` strips that capability
  from nested cascades like it strips `bypassACL`. Automation `set` actions
  and `ApplyEntity` are system writes and are not gated; Lua script actions
  write through `gated().PatchEntity` and are. Paths that change entity data
  outside the manager check ownership themselves, before any byte changes:
  the attachment service (`attachment.Deps.Twins`, not change-based — any
  upload or delete on an owned file property is refused) and the data-entry
  git conflict resolve (`validateEntityWrite` over the changed fields).

## Audit log

Every successful entity/relation create/update/delete/rename is audited as
a JSONL record under `.rela/audit/YYYY-MM-DD.jsonl`. See `docs/audit-log.md`
for the user-facing reference. Rules for new code:

- **New write paths inherit audit automatically.** Any code calling
  `Manager.{Create,Update,Patch,Delete,Rename}{Entity,Relation}` produces a
  record without further wiring. Do not bypass Manager. Note this is a
  property of the shared pipeline, not of the method name: `PatchEntity`
  audits because it routes through `updateCore`. A new entry point that
  reimplements the pipeline instead of calling the shared core would NOT
  inherit audit — that is the thing to check in review.
- **New entry-point binaries stamp Principal at startup**, once:
  `ctx = principal.With(ctx, principal.Principal{User: principal.SystemUser(), Tool: principal.ToolXxx})`.
  Use a `principal.ToolCLI`/`ToolMCP`/`ToolDataEntry`/`ToolScheduler`/
  `ToolDesktop` constant — string literals won't surface typos until the
  entry-point smoke test catches them.
- **Engine-initiated paths stamp `triggered_by`.** Scheduler tasks wrap the
  per-task ctx with `audit.WithTriggeredBy(ctx, "schedule:"+task.Name)`; the
  autocascade runner does the analogous thing for cascades. Direct user
  actions leave `triggered_by` empty.
- **Lua bindings do not expose audit *rewrite* primitives.** A Lua script
  must not be able to change the Principal or triggered_by a write is
  attributed as — attribution always derives from the caller's context
  inside the write bindings, never from anything the script controls.
  Do not register `rela.audit`, `rela.audit.with_principal`, or
  `rela.audit.with_triggered_by` on the runtime — those would be rewrite
  vectors. Guarded by `internal/lua/audit_spoofing_test.go`.

  `rela.principal` **is** exposed (TKT-5U6NRR) — but **read-only**: a frozen
  `{user, tool}` table (`__newindex` raises, `__metatable` locked) read from
  the request context. It only *reads* the acting identity (so write-path
  automations can attribute relations like `created-by` to the real
  submitter); it is not a rewrite hook, and the write bindings ignore it
  entirely. Reading the identity cannot forge attribution, so it does not
  weaken the spoofing defense — the test pins both the read-only contract and
  the can't-forge invariant. Do not add a *setter* or any path from a
  script-controlled value into audit attribution.
- **Constructor takes `Audit` as a required collaborator.**
  `entitymanager.Deps.Audit` and `appbuild.New` reject nil. Tests use
  `audit.Nop{}` (explicit opt-out) or `audit.NewMemory()` when asserting.

## Authorization (ACL)

The ACL is a required collaborator via `Deps.ACL`; structured 403s surface
in `internal/dataentry`. Three production implementations live in
`internal/acl`:

- `NopACL` — allow-all; default when no `acl.yaml` is present.
- `ReadOnlyACL` — deny-all; wired via `rela-server --read-only`.
- `Declarative` — policy-driven, composed with a `Policy` from `acl.yaml`.

Consumer-side interface rule: code calling into the ACL declares the
narrowest contract it needs at the call site, not `acl.ACL` in full.
`entitymanager` is the exception — it owns the constructor field so the
wiring boundary is explicit.

- **Don't run user-supplied Lua on the read path.** ACL gates and filters
  evaluate against declarative policy (`acl.yaml`) and the graph; Lua
  participates only at *write time* via the automation engine. Per-row Lua
  on reads is the perf cliff every comparable system regrets — see
  `.ignored/acl-design.md`.

  **What this rule is about: unbounded, hot paths — list/collection reads
  where a predicate runs once per row across hundreds of entities. That is
  the perf cliff.** It is NOT a blanket ban on ever evaluating a predicate
  during a read. A *bounded, on-demand* evaluation — e.g. resolving the
  performable state-machine transitions for one field on the one entity a
  user is viewing (O(out-edges), single entity) — is consistent with the
  rule's intent, not a violation of it. When in doubt, ask: does this scale
  with the size of a result set (banned) or is it a fixed, single-subject
  computation the caller explicitly requested (fine)? Don't cite this rule to
  block the latter.

See `docs/server-security.md` (schema reference), `.ignored/acl-design.md` (design
rationale, four-layer model: users → groups → roles → local roles), and
`docs/audit-log.md` (the `denied-write` audit op).
