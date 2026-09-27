---
id: TKT-UIR41P
type: ticket
title: "Remote MCP part 1: migrate to go-sdk v1.7.0 and route MCP reads through an ACL-gated seam"
kind: enhancement
priority: high
effort: xl
tags:
    - needs-design
    - security
status: done
---

> **Scope note (split).** This ticket originally covered the whole arc through
> to serving MCP over HTTP. It was split once the first two parts were done and
> independently valuable: **this ticket is the go-sdk migration + the ACL-gated
> read seam**, both shipped and test-pinned. Actually exposing the endpoint —
> the HTTP mount, per-request principal and RFC 9728 discovery — is
> **TKT-BDG8U9**, which also carries the three review-responses that only bite
> once a network transport exists (RR-H8S10M, RR-PQ5UN1, RR-P34E8J).
>
> Sections below that describe the remote transport are retained as the design
> record TKT-BDG8U9 builds on, not as work outstanding here.

## Description

`rela mcp` today is a **local stdio** transport only. It wires `acl.NopACL{}`
and a single `principal.SystemUser()` bound at construction. Both are deliberate
and justified *by the stdio trust boundary* — anyone who can launch `rela mcp`
already has filesystem write access to the entity files, so a gate on the tool
surface would defend nothing (`internal/cli/mcp_wiring.go`).

We want **server deploys of rela to also serve MCP**, so an AI assistant can
connect to a deployed `rela-server` over HTTP instead of spawning a local
binary. That inverts every assumption above: the caller no longer has filesystem
access, is no longer a single trusted local user, and is no longer implicitly
authorized.

Separately, MCP shipped a new spec revision (**2026-07-28**) whose
remote-transport and authorization story is the reason this is now worth doing
properly rather than bolting SSE onto the existing server.

## Why this is not just "add an HTTP listener"

Three load-bearing facts found while scoping:

1. **The read path is knowingly ungated on MCP.** `docs/acl-security.md:743`
lists the MCP transport as an accepted read-side ACL gap: read tools "apply
neither the entity-level read gate nor `visible:` redaction; they return full
entity bodies. **The MCP server is local-only (stdio), so this is an accepted
gap at this stage.**" Remote MCP deletes that justification. Concretely
`internal/cli/mcp_wiring.go` hands `mcp.Deps` the **raw** `svc.Store()` /
`svc.Tracer()` / `svc.Searcher()`, not the visibility-wrapped variants that
`appbuild` already builds.

2. **One principal is bound at construction.** `mcp.Server` holds a single
`s.principal` stamped onto every tool ctx by `principalMiddleware`
(`internal/mcp/server.go:147`). A multi-caller HTTP deploy needs the principal
derived **per request**. The good news: that middleware is already the single
choke point, and `internal/visibility` resolves identity from ctx
(`principal.From(ctx)`, `adapters.go:41`) rather than at construction — so a
visibility-wrapped `Deps` built once can serve many principals correctly.

3. **`lua_eval` / `lua_run` get UNRESTRICTED reads.** Not a sandbox-escape
concern — the runtime is genuinely sandboxed (`SkipOpenLibs: true`, only
base/table/string/math/coroutine opened; no `io`/`os`/`debug`/`package`;
`write_file` is path-validated to a project-relative output dir —
`internal/lua/runtime.go:331`, `:390-394`, `:1261`). The real problem is the
**read gate**: `mcp_wiring.go` passes `svc.LuaWriteDeps()`, documented at
`internal/appbuild/appbuild.go:308` as *"UNRESTRICTED reads"*, whose
`VisibleReader` is literally `visibility.Unrestricted(s.store)` (`:200`). The
identity-bound variant `luaReadDepsFor` sits right beside it (`:231`) and is
described as required for *"every identity-bearing path"* (DEC-O59WM4). So over
a remote transport a two-line `rela.list_entities` script would return entities
the same caller's `show_entity` refuses — an ACL-bypassing read primitive that
directly defeats AC #3. **Decision: Lua tools are excluded from the remote
transport in this ticket** (see Scope).

DEC-RG878 (accepted) already sets the required design: *"MCP integration is
transport-layer intersection: effective = intersect(user_capabilities,
agent_scope), default-deny on writes."* TKT-G3PPD covers the tool-list filtering
half of that; this ticket is the transport + per-request identity half. They
should land coherently.

## Scope

### In scope

- **Migrate `internal/mcp` to the official `modelcontextprotocol/go-sdk`
v1.7.0** (spec revision `2026-07-28`) — DECIDED. See the SDK section below for
what this entails; note 2026-07-28 activates only with `Stateless = true`, which
is the shape we want anyway.
- Serve MCP over **Streamable HTTP** at **`/api/v1/_mcp`** from `rela-server`,
mounted on the existing router (`app.NewRouter()`) so it inherits the
`stampAuditPrincipal → requireVerifiedJWT → attachACLRequest` chain — DECIDED.
- **Expose RFC 9728 Protected Resource Metadata** (`/.well-known/
oauth-protected-resource`) plus a `WWW-Authenticate: Bearer …
resource_metadata=…` challenge on 401, so spec-compliant MCP clients
auto-discover the IdP — DECIDED. Audience validation (RFC 8707), which
2026-07-28 requires of a resource server, is already satisfied by `jwtauth`
pinning `aud`.
- **Per-request principal**: derive from the same verified-JWT / header
identity that `/api/v1` already uses (`wirePrincipalResolvers` in
`cmd/rela-server/main.go`), and stamp it per request instead of once at
construction — the equivalent of the `WithHTTPContextFunc` hook, via whatever
the go-sdk's per-request context seam turns out to be.
- **Close the read-side ACL gap for MCP**: route MCP read tools through the
`internal/visibility` wrappers (row gate + `visible:` redaction), so a remote
caller sees exactly what the same principal sees via the REST API. Update
`docs/acl-security.md:743` once closed.
- **Write path**: writes already funnel through `entitymanager` and get the ACL
gate, but they must now run under the *request's* principal, not `SystemUser()`,
so audit attribution and grants are correct.
- **Lua tools (`lua_eval` / `lua_run` / `lua_list`) are NOT registered on the
remote transport** — DECIDED. stdio keeps them unchanged. Covered by a test
asserting they are absent from the remote tool list.
- **Opt-in, off by default.** A deployed server must not start serving MCP
because it was upgraded.
- Docs: `docs/mcp-server.md` (generated from `docs-project/entities/`) +
`docs/server-security.md` threat model.

### Explicitly NOT in scope

- Replacing/removing the stdio transport — `rela mcp` keeps working unchanged.
- Multi-tenant / multi-project MCP. `rela-server` is single-project
(`--project`); this ticket does not change that.
- The tool-list intersection filtering itself — that is TKT-G3PPD. This ticket
must not contradict it, and should land the seam it needs.
- **Bringing Lua to remote MCP.** Deferred to its own ticket. When it happens
the agreed approach is the **rewire of BOTH read and write deps** onto the
identity-bound bundle (`luaReadDepsFor` + the write side), not a reads-only
patch — the `LuaWriteDeps` "unrestricted reads" mismatch is the underlying
defect and should be fixed as a whole rather than papered over per transport.
- OAuth **authorization server** duties (token issuance, DCR). rela is a
resource server; an external IdP issues tokens, matching the existing JWT/JWKS
model.

## Acceptance criteria

1. A deployed `rela-server` started with the MCP flag serves a working MCP
endpoint over Streamable HTTP; an MCP client can discover the server
(`server/discover` — there is no `initialize` handshake in this revision), list
tools, and call a read tool.
2. With MCP not enabled, the endpoint is absent (404) — no behavior change on
upgrade.
3. Two different callers with different ACL grants hit the same endpoint and
each sees only their own permitted entities; a hidden entity is
indistinguishable from a missing one, and `visible:` fields are redacted.
4. A write via remote MCP is audit-logged against the **requesting** principal,
not `system`/`unknown`.
5. An unauthenticated request is refused when identity is configured
fail-closed, with no fall-through to a spoofable header (matching the existing
`validateIdentityFlags` stance).
6. `rela mcp` (stdio) is **semantically equivalent** to today after the go-sdk
migration, with a *documented* diff for tool-schema JSON and the error-result
envelope. (Literal byte-identity is impossible: the go-sdk reflects input
schemas from struct tags and uses a different error-result convention, so
`tools/list` output necessarily differs.) Verified against golden `tools/list` +
representative `tools/call` outputs captured from the CURRENT binary and
committed **before** the migration starts.
7. `lua_eval` / `lua_run` / `lua_list` are **absent from the remote tool list**,
pinned by a test; stdio still exposes them.

   _Status:_ not built when TKT-BDG8U9 shipped the HTTP endpoint. Implemented
   by BUG-RIJR6R (`mcp.WithLuaTools`, `TestRemoteMCPDeps_UsesGatedHandles`).

   Deliberately NOT extended to "no Lua executes remotely": `analyze_validations`
   runs operator-authored `lua_file:` validation rules, and that is fine. The
   CLAUDE.md "don't run user-supplied Lua on the read path" rule targets
   unbounded per-record work on hot list views — not a bounded validation run the
   caller explicitly asked for. What must be fixed there is the *candidate set*,
   which is currently ungated (RR-H7DFZ5).
8. The server implements `server/discover` and negotiates protocol revision
`2026-07-28`.
9. An unauthenticated request to `/api/v1/_mcp` returns 401 with a
`WWW-Authenticate: Bearer … resource_metadata=…` challenge (the
`resource_metadata` parameter specifically — see RR-P34E8J), and
`/.well-known/oauth-protected-resource` serves valid RFC 9728 metadata.
10. **Enabling remote MCP without verified JWT identity is refused at startup.**
The CSRF exemption the endpoint needs is only sound while the JWT gate is
active; header-identity mode fails open to `unknown`, so the combination must
fail loud rather than silently serve the graph to anyone who can reach the port
(same posture as `validateIdentityFlags`).
11. Read gating covers **all** read surfaces — tools, resources (`rela://…`),
prompts, export, analyze, and the validator's candidate set — not just tool
handlers (RR-CFFL52, RR-NSUN49, RR-H7DFZ5). The `analyze_*` tools are **gated,
not excluded**: `internal/dataentry/analyze.go` already solved this
(TKT-3FL2S6) by injecting a gated reader + gated tracer into `analyzeService`,
so hidden entities never enter a check and counts reflect only the visible
slice. MCP reuses that pattern rather than inventing one.

## PR 1 (SDK migration, stdio-only) — DONE

Landed as two commits on `tkt-uir41p-mcp-sdk-migration`:

1. `test(mcp): freeze pre-migration goldens` — captured `tools/list` (26 tools,
   full schemas + descriptions) and one representative `tools/call` result per
   tool **from the mark3labs binary**, committed before any migration code, so
   the migration diffs against a frozen artifact rather than a co-edited test.
   Determinism required pinning the fixture titles (`testutil.EntityFor` seeds
   random ones) and normalizing `create_entity`'s minted id.
2. `refactor(mcp): migrate to modelcontextprotocol/go-sdk v1.7.0`.

**AC #6 outcome: better than required.** The criterion was relaxed to
"semantically equivalent with a documented diff" because reflected schemas were
expected to change the wire format. In the event there is **no diff at all** —
both goldens pass byte-identically. The reason: rather than adopt the go-sdk's
generic `AddTool[In, Out]` (which would have reflected new schemas from struct
tags AND changed error-result semantics in the same step), the migration keeps
the builder-style specs and explicit result envelopes behind three small shims —
`toolspec.go` (emits the same `inputSchema` JSON), `result.go` (the two result
helpers, 117 call sites), `request.go` (the argument accessors, reproducing the
old error strings verbatim). Moving to typed `In` structs is a worthwhile
follow-up, but it is a separate, independently reviewable change.

**Verification:** full `go test ./...` green; `-race` green; `just lint` 0
issues; `just arch-lint` OK (vendor alias repointed); coverage for
`internal/mcp` **rose 56% → 66.4%** against a floor of 50, so risk R3 did not
materialize. Manually smoke-tested the real `rela mcp` binary over stdio
against the live `tickets/` project: `initialize` negotiates, `show_entity`
returns this ticket, `tools/list` returns 26 tools.

**Documented behavior delta (one).** The file watcher no longer emits
`notifications/resources/list_changed`. The go-sdk fires that automatically when
the resource *set* changes and exposes no ad-hoc send; rela's watcher signals
that resource *contents* changed, which is a different event. The resource set
here is static (one resource + two URI templates), so the callback now just
logs. Clients re-read on demand and see fresh data. This also pre-empts the
stateless-transport constraint documented below, and is recorded in
`internal/mcp/server.go`.

Also of note for PR 2: the go-sdk's middleware is **method-level**
(`AddReceivingMiddleware`) rather than tool-level, so the principal stamp now
also covers resource and prompt handlers — which previously ran with **no**
principal on the ctx at all. That is a strict improvement and directly relevant
to RR-CFFL52 / RR-NSUN49.

## PR 2 (read gating, stdio-only) — IN PROGRESS

Rebased onto `develop` after four merges (#1308–#1313); none touched
`internal/mcp`, but `router.go` / `middleware_security.go` / `internal/acl` all
moved, which is PR 3 territory — rebase again before starting that.

**Step 1 done — `Deps.Store` narrowed to `GraphReader`.** MCP only ever *reads*
through the store (writes go via `EntityManager`), so the field is now the
six-method interface the handlers actually call, declared at the call site. This
is the R2 mitigation made real: a raw ungated read is now **unavailable**, not
merely discouraged. The wiring site chooses the implementation — raw store for
stdio, visibility-wrapped for a networked wiring — and the handlers are
identical either way.

Two review findings fell out of it rather than needing separate work:

- **RR-CFFL52 (neighbor leak)** — `buildStoreRelations` now withholds the
  *whole edge* when the far-end entity is unreadable. Dropping only the title
  would still disclose the neighbor's id, and an id is exactly what the
  row-level rule protects.
- **RR-FTJUUE (trace oracle)** — the `trace_*` / `find_path` existence probes
  now run through the gated handle, so hidden and absent are indistinguishable.

**RR-OMB6ID resolved properly.** `schema.ValidateRelationProperties` takes a
`RelationLister` and the counter adapter takes `TypeCounts` — consumer-side
interfaces, matching what `internal/dataentry` already does. `StoreCounter`
became an unexported closure-bound `storeCounter` built by `NewStoreCounter(ctx,
st)`: a struct with an exported `Ctx` field invites a literal that forgets it,
which for an ACL-scoped counter means silently counting the whole graph while
still compiling and still returning plausible numbers.

**Verified:** full `go test ./...` green, `just lint` 0 issues on changed
packages, `just arch-lint` OK, and **the goldens pass unmodified** — so none of
this changed behavior under the stdio (`NopACL`) wiring.

**Step 2 done — the gated seam is wired and pinned.**
`appbuild.Services.GatedReads()` returns the reader / tracer / validator an
identity-bearing consumer needs, each resolving the ctx principal per call
(the `ScheduledLuaWriteDeps` precedent). `mcp_wiring` takes its three read
handles from it. Under `rela mcp`'s `NopACL` they ARE the raw handles — stdio
unchanged, goldens pass — but every read surface now reaches the graph through
one substitutable seam instead of 34 handlers holding the store.

The validator mattered as much as the reader (RR-H7DFZ5): `Services.Validator()`
is built over the raw store, which is right for its unattended callers, so
`GatedReads` builds a *second* one over the gated reader. `validator.New`
already took a narrow `EntityLister` for exactly this — its godoc cites
TKT-3FL2S6.

Row reads are gated; **counts and `GetRelation` are not**, and
`gatedGraphReader` documents why: a count is structural (how many rows of a
declared type, never which), and a relation is addressed by two endpoint ids
the caller must already hold. Same line `dataentry` draws for `relCounts`.

**Tests: five ACL cases, built through the production seam** rather than a
hand-composed visibility stack. One of them exists because of a near-miss worth
recording: the obvious neighbor-leak test **passed vacuously**. `visibility`'s
gated `ListRelations` already drops edges whose far end is hidden, so reverting
the `buildStoreRelations` fix did not fail it — the test never exercised the
layer it named. `TestACL_BuildStoreRelations_WithholdsUnreadableEdge` feeds a
reader that yields the edge anyway, and was mutation-verified: with the fix
reverted it fails with `LEAK: {ID:FEAT-SECRET Title:}` — the neighbor id
surviving with its title stripped, which is exactly the RR-CFFL52 failure mode.

Also confirmed: the go-sdk's **method-level** middleware means resource and
prompt handlers now get a stamped principal at all. Previously they ran with
none, so any ctx-resolving gate there would have errored or failed open.

**Addressed by this step:** RR-CFFL52, RR-FTJUUE, RR-NSUN49, RR-H7DFZ5
(+ RR-OMB6ID, RR-B7ZHYO earlier).

**Remaining before PR 3:** RR-H8S10M (`verifiedPrincipal` hardcodes
`ToolDataEntry`, so AC #4 fails and the naive fix drops roles) and RR-PQ5UN1
(`acl.Request` sharing across a JSON-RPC batch). Both are HTTP-transport
concerns with no stdio manifestation, so they belong with PR 3.

## Spec revision 2026-07-28 — verified findings

Researched and **verified against the module proxy and module cache**, not just
docs. This is the part that turns a config change into a real decision.

**2026-07-28 is a large revision — "the largest since launch."** Revision line:
`2024-11-05` → `2025-03-26` → `2025-06-18` → `2025-11-25` → `2026-07-28`. The
relevant removals/additions for a remote deploy:

- **Protocol-level sessions REMOVED** (SEP-2567). No `Mcp-Session-Id`. A
2026-07-28 server MUST NOT mint or echo session IDs; GET/DELETE on the endpoint
→ `405`.
- **`initialize` handshake REMOVED** (SEP-2575). Every request carries its own
`_meta` (`protocolVersion`, `clientCapabilities`, `clientInfo`).
- **New `server/discover` RPC — servers MUST implement it.**
- **SSE resumability REMOVED** — no `Last-Event-ID`, no event IDs. SSE survives
only as a per-request response stream. Closing the stream *is* cancellation.
- **New required request headers**: `MCP-Protocol-Version` (mismatch → `400` +
`-32020`), `Mcp-Method`, `Mcp-Name`.
- `ttlMs` + `cacheScope` now **required** on all `*/list` and `resources/read`
results. Resource-not-found error code `-32002` → `-32602`.
- Authorization stays OAuth 2.1 + RFC 9728, and a resource server **MUST**
validate the token audience (RFC 8707). New: **Client ID Metadata Documents**
(URL-based client IDs) are now preferred and **DCR is deprecated**.
- **Roots, Sampling and Logging are deprecated**; tasks moved to an official
extension. `ping` / `logging/setLevel` removed.

### The SDK situation is the crux (verified from source)

| Module | Version | `LATEST_PROTOCOL_VERSION` |
|---|---|---|
| `mark3labs/mcp-go` | **v0.57.0 (what we're on)** | `2025-11-25` |
| `mark3labs/mcp-go` | v0.58.0 (current stable) | `2025-11-25` |
| `mark3labs/mcp-go` | v1.0.0-beta.1 (2026-08-12) | `2026-07-28` |
| `modelcontextprotocol/go-sdk` | **v1.7.0 (stable)** | `2026-07-28` |

So: **no stable `mark3labs` release speaks 2026-07-28.** Its support landed only
in a beta tagged one day before this ticket was written. The official
`modelcontextprotocol/go-sdk` v1.7.0 is stable, shipped the same day as the
spec, and is the reference implementation — but adopting it is a **full API
migration** of `internal/mcp` (~5.4k lines incl. tests), and it activates the
2026-07-28 protocol **only when `Stateless = true`**.

Good news either way: v0.57.0 **already** ships `StreamableHTTPServer`,
`WithHTTPContextFunc` (the per-request principal hook), `WithStateLess`,
`WithProtectedResourceMetadata` and CORS. **A remote MCP endpoint at
`2025-11-25` is available today with no SDK change at all.**

### DECIDED: migrate to official `go-sdk` v1.7.0

Chosen over staying on `mark3labs`. Rationale: it is the only option that is
**both stable and current** — the `mark3labs` 2026-07-28 support exists only in
a one-day-old beta, and shipping remote MCP at `2025-11-25` would mean building
a new network surface on a revision already superseded. Taking the migration now
avoids doing the transport work twice.

Consequences to plan for:

- `internal/mcp` is a **full API migration** (~5.4k lines incl. tests): the
`mcpgo`/`server` imports, all 26 `AddTool` registrations, resources, prompts,
and `principalMiddleware` are rewritten against the new SDK.
- **`Stateless = true` is required** for the 2026-07-28 protocol in this SDK.
That suits us (no session state to keep, multi-replica friendly) and matches the
spec's own removal of sessions.
- **`server/discover` MUST be implemented** — new in this revision.
- The stdio transport must be ported too, and AC #6 (stdio byte-identical
behavior) becomes the *hardest* criterion rather than a freebie. This is the
main risk to watch: `rela mcp` regressions would hit every existing local user.
- Seven `MCPGODEBUG` migration escape hatches exist in the SDK, all slated for
removal in v1.9.0 — do not build on them.

## Open questions (for planning)

- **The file watcher.** `Serve()` starts a watcher and broadcasts
`notifications/resources/list_changed`. On a server deploy the store event
bridge already exists (`App.startStoreEventBridge`); reuse it rather than
running a second watcher. Note the SSE broker is deliberately **lossy** and
never carries entity ids (existence-oracle avoidance, TKT-POT9GQ) — the MCP
notification must honor the same rule.
- **Migration sequencing.** Does the SDK migration land as its own PR (stdio
only, no behavior change) with the remote transport + ACL work in a second PR?
That keeps AC #6 verifiable in isolation and makes a stdio regression bisectable
— strongly preferred, but confirm the split during planning.
- **Origin/CSRF posture at `/api/v1/_mcp`.** A non-browser MCP client sends no
`Origin`, so `requireSameOrigin` would reject it unless the path is treated like
the existing `nonBrowserExemptPrefixes` (`/api/sync/`, `/api/v1/_feeds/`) via
the `isCSRFExempt` shape (`middleware_security.go:238`, `:288`). Decide this
deliberately — the exemption is only sound because the JWT gate authenticates
the caller independently.
- **`server/discover` and the ACL.** Its response advertises capabilities and
instructions. Per CLAUDE.md ("the configuration is not a secret") tool *names*
need no concealment, so this should be principal-independent — confirm that
reading, and keep it consistent with whatever TKT-G3PPD does to `tools/list`.
- **Which principal for `Tool`?** Remote callers should presumably still stamp
`principal.ToolMCP` for audit, while `User` comes from the verified JWT. Confirm
no audit/ACL rule keys on `ToolMCP` in a way that assumed stdio.

## Constraints confirmed in the server (research)

- **The router is stdlib `http.ServeMux`**, assembled in `App.NewRouter()`
(`internal/dataentry/router.go:59`). Middleware order is load-bearing and
documented at `router.go:160-203`: `stampAuditPrincipal` → `requireVerifiedJWT`
→ `attachACLRequest` → host/origin gates. An MCP mount must sit **inside** that
chain, or it gets an unstamped principal and `acl.Declarative.ForPrincipal`
fails with `ErrUnstampedPrincipal`.
- **All optional wiring must precede `NewRouter()`** — `attachACLRequest`
snapshots `a.jwtGate != nil` at construction time.
- **`principal.Verified` is the only forge-proof path to roles.** Anything
deriving a principal from an MCP request must route through a completed
signature verification (`verifiedPrincipal`, `router.go:554`), never a composite
literal.
- **Org isolation does not exist.** `principal.OrgID()` is *audit attribution
only* — nothing in `internal/acl` evaluates it, so a principal in org A sees
everything their role grants in every org (TKT-RP3X3Q). Remote MCP must not be
documented or sold as tenant-isolated.
- **One project per process.** `appbuild.Services`, `App.paths`, `state.KV` and
the scheduler are process-scoped singletons; there is no per-request workspace
seam. This is why multi-tenancy is out of scope above.
- **`/api/` is the CSRF/origin-gated prefix** (`sensitivePathPrefixes`).
Mounting MCP under `/api/` inherits the same-origin gate; a non-browser MCP
client sends no Origin, so the `isCSRFExempt` shape
(`middleware_security.go:288`) is the relevant precedent — decide deliberately
rather than inheriting by accident.

## References

- `internal/cli/mcp_wiring.go` — NopACL + raw services justification
- `internal/mcp/server.go:147` — `principalMiddleware`, single bound principal
- `internal/visibility/adapters.go:41` — ctx-scoped principal resolution
- `cmd/rela-server/main.go` — identity flags, `wirePrincipalResolvers`, router mount
- `docs/acl-security.md:743` — the accepted MCP read-gap this ticket closes
- DEC-RG878 — transport-layer intersection, default-deny on writes
- TKT-G3PPD — MCP tool-list filtering (sibling ticket)
