<!-- This file is auto-generated from docs-project/entities/. Do not edit directly. -->

# Security model for rela-server

`rela-server` is the HTTP data-entry app shipped with rela. It is intended to
run on a local port (`http://localhost:8080` by default) and be opened in your
normal browser.

This guide documents the threat model the server is hardened against, the
defenses it employs, the residual risks, and the configuration knobs available
to operators and developers. For the ACL system itself — the resolver
vocabulary, role configuration, and the read-side filtering guarantees — read
[GUIDE-acl-overview] and [GUIDE-acl-security]; this guide summarizes how that
coverage stands and links out rather than restating it.

## Threat model

The server runs on your machine, but your machine is not a closed system: any
website you visit can execute arbitrary JavaScript in a browser tab, and that
JavaScript can issue HTTP requests to `http://localhost:<port>` just like the
data-entry SPA does. Without active defenses, a malicious page could:

- Read live project events from the SSE endpoint (file changes, entity
  updates, git status).
- Create / update / delete entities in your project via cross-origin
  `fetch` requests.
- Trigger any configured shell command via `/api/command/`.
- Pivot via DNS rebinding to bypass loopback assumptions.

The threat model assumes:

- The attacker is a website loaded in your browser, executing JavaScript.
- The attacker can also use simple HTML primitives (`<img src=…>`,
  `<form>` POST) that bypass JavaScript-only defenses.
- The attacker controls DNS for hostnames they own (DNS rebinding).
- The attacker does **not** have local code execution on your machine.
  Local malware running with user privileges is out of scope; any program
  running as the current user can already do everything `rela-server` can.

## Defenses

`rela-server` enforces the following on every HTTP request:

### 1. Loopback binding by default

The server binds to `127.0.0.1` by default. Other machines on your LAN
cannot reach the server unless you explicitly opt in with `--bind`.

```sh
# Default: only this machine can reach it
rela-server

# Opt in to LAN access (review threat model first!)
rela-server --bind 0.0.0.0 --allowed-origin http://my-laptop.local:8080
```

When the bind address is non-loopback, the server prints a prominent warning
at startup. **You must also pass `--allowed-origin` for every hostname your
clients will use to reach the server**, otherwise their requests will be
rejected by the Origin allowlist (see §3 below). Common examples:

- `--allowed-origin http://192.168.1.5:8080` (LAN IP)
- `--allowed-origin http://my-laptop.local:8080` (mDNS / Bonjour)
- `--allowed-origin https://rela.example.com` (behind a reverse proxy with TLS)

When bound to `0.0.0.0` or `::` the **Host header check is disabled** (we
cannot enumerate the legitimate Host values ahead of time). The Origin
allowlist becomes the only CSRF gate in that mode, so make sure your
`--allowed-origin` set is accurate.

### 2. Host header allowlist (DNS rebinding defense)

Every request must carry a `Host` header matching the bound address (or one
of the loopback aliases when bound to loopback). Requests with spoofed Host
headers — the hallmark of a DNS rebinding attack — are rejected with `403`.

### 3. Origin allowlist on sensitive endpoints

Every request to `/api/...` must carry an `Origin` header (or `Referer`
fallback) matching the server's own origin. Requests from other origins are
rejected with `403`.

The check applies on **every** HTTP method, including `GET`. This is
important: some endpoints (notably `/api/command/...`) are state-changing
even on `GET`, and a method-based filter would let `<img src=...>` style
attacks through.

Static assets (`/static/`, the SPA shell) are exempt — they leak no project
data and need to remain fetchable cross-origin in some setups.

### 4. SSE endpoints are same-origin only

`/api/events` and `/api/v1/_events` no longer reflect the request `Origin`
back as `Access-Control-Allow-Origin` (which previously let any website
subscribe to your live project events). They are protected by the same
Origin allowlist as the rest of `/api`.

### 5. Command output is downloaded by token, not by path

A command that reports an output file does not hand the browser a path, and
`/api/command-file/` does not accept one. The server keeps the file's
resolved path in memory under an opaque, unguessable token and the client may
only present that token.

The path is cleaned, made absolute, and resolved through any symlinks when the
token is minted; anything outside the project root, or containing a NUL byte,
never gets a token at all.

Presenting a valid token is not sufficient. Every download re-runs the same
authorization check that command execution runs, against the **current**
policy — so a token stops working as soon as the command's `permission:` is
revoked or the server is restarted with `--read-only`. Tokens also expire 30
minutes after their command finishes.

Earlier versions instead exposed `/api/open-file`, which launched an OS file
opener on the server host, and `/api/open-url`. Both are gone.

### 6. Per-request timeouts

`http.Server.ReadHeaderTimeout`, `ReadTimeout`, and `IdleTimeout` are set
to bound resource use. `WriteTimeout` is intentionally `0` (unlimited):
Server-Sent Events and command-exec output stream long-lived responses,
and a write deadline would kill them mid-flight. Slow-write protection
is provided by `IdleTimeout` and (in the future) by per-handler context
deadlines on individual mutating handlers.

## Audit logging

Every entity / relation create / update / delete is recorded as a
JSONL row under `.rela/audit/YYYY-MM-DD.jsonl`. Records carry the
operating user (`$USER`), the entry point that initiated the write
(`cli`, `mcp`, `data-entry`, `scheduler`, `desktop`), and — for
engine-initiated writes — the originating automation or schedule.

The log is forensic, not authoritative: a process crash between the
store write and the audit append can leave a write un-audited; see
[GUIDE-audit-log] for the durability story, the JSONL schema, and `jq`
recipes for common queries.

`.rela/audit/` is gitignored by convention — audit content is
per-machine and should not be committed.

### Retention

`rela` **never deletes audit logs**. The backend rotates to a new
`YYYY-MM-DD.jsonl` file each UTC day and appends; it has no pruning,
expiry, or cleanup path. The default behaviour is therefore
retention-safe — the application cannot, on its own, drop a record
below any required retention window.

Retention is an **operational control**, owned by the deployment, not
the application. For environments subject to a security-log retention
requirement (e.g. **POLICY-017 §4 / PROCEDURE-f4cu: security logs
retained ≥ 12 months**):

- Retain everything under `.rela/audit/` for **at least 12 months**.
  Back it up or ship it off-box if the working tree is ephemeral
  (containers, CI runners, re-provisioned hosts), since the directory
  is gitignored and lives only on the local disk.
- If you prune at all, prune **only beyond** the retention window. The
  daily file naming makes this exact: delete files whose date is older
  than your window, never on a shorter `-mtime`. See [GUIDE-audit-log]
  for a compliant example.

There is no built-in enforcement of the minimum — `rela` cannot police
an operator's `rm`. The guarantee it provides is the converse: it will
not delete logs for you.

### `data-entry` user attribution

By default the data-entry server records `principal.user: "unknown"`
on every audit row — the server-process `$USER` would be misleading
for human web users. Two opt-in sources can replace the placeholder:

- **`--principal-header X-Forwarded-User`** (or any header name) on
  `rela-server`. The middleware reads the named header on every
  request and stamps its value as `principal.user`.
- **`$RELA_DATAENTRY_USER`** env var, set on the `rela-server`
  process. Useful for local development where there's no proxy.
  The env value wins over the header. It cannot be combined with
  `--jwt-*` — see *Mutually exclusive* below.

**Trust boundary**: the `--principal-header` flag is only safe
behind a reverse proxy that

1. **strips** the same header from inbound requests, and
2. **sets** it from an authenticated source (oauth2-proxy, Vouch,
   traefik forward-auth, etc.).

A direct-to-data-entry deployment must not enable this flag —
clients can spoof the header at will. If `rela-server` is bound
beyond loopback (`--bind` non-loopback) and `--principal-header`
is set, audit attribution is only as trustworthy as the network
path between the client and the proxy.

**Prefer `--jwt-*`** for anything beyond local development. The
header path fails *open* by design — an absent header yields
`unknown`, not a denial — and its trustworthiness rests entirely
on network topology. Verified JWT identity fails *closed* and
proves authenticity cryptographically. The header path is
unchanged and still supported; it is simply the weaker of the two.

Header values are sanitized at the middleware (trim, 256-rune cap,
control-char strip) as defense-in-depth against header-injection
corrupting the JSONL stream.

### Reserved `system:` identities

rela reserves the whole **`system:`** username namespace for its own
in-process entry points. `system:scheduler` (scheduled tasks) and
`system:provisioner` (lazy user provisioning) live there today.

These are ordinary *grantable* identities in `acl.yaml` — `rela migrate`
gives `system:scheduler` a `read: ["*"]` role so existing scheduled jobs
keep working — and the ACL resolves a principal by matching the raw
username against its assignments. It has no way to tell where a principal
came from, so without a boundary check any caller who could influence the
acting username would inherit the scheduler's grants.

So a `system:`-prefixed username arriving from a **request** is refused:

| Source | Result |
| --- | --- |
| `--principal-header` value | **403** on `/api/`, logged |
| `$RELA_DATAENTRY_USER` | **403** on `/api/`, logged |
| Verified JWT `sub` (incl. remote MCP) | **denied**, logged |

A valid signature proves your IdP *issued* the subject; it does not prove
the subject is not one of rela's internal names, so the verified path is
checked too. The asymmetry in that table is not about the reserved check:
the JWT gate answers **401** uniformly for every failed assertion, so a
reserved subject is indistinguishable from an expired or unsigned one. The
distinction is in the log, not the response.

Outside `/api/` (the SPA shell, static assets) the request is served with
the identity stripped to `unknown` rather than erroring — a misconfigured
proxy stamping a reserved name must not lock you out of the UI you need in
order to fix it. Nothing on those paths uses the principal for
authorization.

The match is **case-sensitive** and tests the prefix only: `system:`,
`system:scheduler`, `system:anything` are reserved; `System:Scheduler`,
`systemscheduler` and `my-system:scheduler` are ordinary usernames (none
of which matches an assignment key either).

> **Upgrade note.** If your IdP issues subjects that begin with `system:`,
> those users can no longer authenticate. Look for
> `rejected reserved principal` in the server log. Remap the affected
> subjects in your IdP or proxy.

`run_as:` in `schedules.yaml` is **not** affected — it is operator-authored
config read in-process, inside the trust boundary, and may name any
`system:` identity.

### Verified JWT identity (`--jwt-*`)

A third, **stronger** attribution source: a signed identity assertion
(an ES256 JWT) from an OIDC identity proxy — Pratique, oauth2-proxy,
Pomerium, Keycloak, and the like. Unlike `--principal-header`, this
path does not *trust* that a proxy set a header; it **cryptographically
verifies** the assertion, so a spoofed header without a valid signature
fails verification and the request is denied.

Enable it by setting all three (env fallbacks `$RELA_JWT_ISSUER` /
`_AUDIENCE` / `_JWKS_URL`):

- **`--jwt-issuer`** — the expected `iss` of the assertion.
- **`--jwt-audience`** — this server's id (the `aud` the proxy mints for
  it); enforced strictly as a confused-deputy guard.
- **`--jwt-jwks-url`** — the proxy's JWKS endpoint; the ES256 signature
  is verified against it. Keys auto-refresh, so routine rotation needs
  no restart, and the cached set survives a failed refresh — but see
  *Availability trade-off* below for the rotation-during-outage case,
  which does deny requests. A refresh failure is logged at `ERROR`.
  Must be `https` (the JWKS is the root of trust — cleartext
  would let an on-path attacker substitute a signing key; loopback URLs
  are exempted for local testing). An unreachable JWKS at startup is
  fatal — identity never silently no-ops.
- **`--jwt-header`** — the request header carrying the JWT (default
  `X-Auth-Assertion`; a leading `Bearer` prefix is stripped). Point it at
  whatever your proxy injects, e.g. `X-Pratique-Assertion` or
  `Authorization`.

The verified **subject (`sub`)** — a stable, opaque user id — becomes
`principal.user`. Keying on `sub` (not email) means a user's audit
attribution and any `acl.yaml` assignments survive an email change.
The verification rejects non-ES256 algorithms (including `alg:none`),
a wrong issuer or audience, and expired or unsigned tokens.

**Other claims.** Beyond `sub`, rela projects `org_id`, `org_slug`, and
`roles` from the verified assertion onto the principal. All are optional
— an assertion carrying only a subject is perfectly valid, so a proxy
that models neither orgs nor roles works unchanged.

- **`roles`** (array of strings) can grant `acl.yaml` roles via
  `asserted_role_assignments`, letting you maintain group membership in
  the IdP instead of restating it per-user. See
  GUIDE-acl-overview.
- **`org_id` / `org_slug`** are recorded for audit attribution.
  **Nothing evaluates them** — they do not provide tenant isolation.
  See GUIDE-acl-security.

Claims are read only from a token that passed every verification step,
and only this resolver can populate them — the `--principal-header` path
carries no roles by construction. `roles` is bounded (32 entries, 256
runes each) so a malformed or hostile IdP cannot amplify one request.

Note that revocation lags: an issued assertion is honored until its
`exp`, so a role revoked in the IdP survives in rela for up to one token
lifetime. See GUIDE-acl-security.

**Mutually exclusive — JWT identity is the only identity source.**
Setting `--jwt-*` together with `--principal-header`, or with
`$RELA_DATAENTRY_USER` in the environment, is a **startup error**.
`rela-server` refuses to boot rather than run with both.

This is deliberate. These sources used to be layered in one resolver
chain, verified-JWT ahead of plain-header. That meant a JWT
verification failure fell **through** to the spoofable header:
anyone able to disrupt JWKS reachability — network egress, DNS, an
IdP outage — could convert rela from verified identity to
trusted-header identity, and rela would keep serving as if nothing
had changed. A startup warning was not an adequate mitigation,
because the downgrade happens per-request, long after anyone is
reading startup logs. Making it a hard error forces the choice at
configuration time, when it is visible.

**Fail-closed behavior.** With JWT identity enabled, every `/api/`
request must carry an assertion that verifies. An assertion that is
absent, malformed, expired, wrongly signed, or bearing the wrong
`iss`/`aud` is answered **401** — never downgraded to a weaker
identity. The 401 body deliberately carries no explanation (the
assertion is attacker-controlled input, and saying why it failed
would make the endpoint a verification oracle); the reason is
logged server-side instead.

The SPA shell (`/`) and static assets (`/static/`) are **not**
gated, so the app still loads and can render a signed-out state.
Those routes serve no entity data — every API call the SPA makes
is gated — and keeping them reachable means a misconfiguration
does not lock operators out of the surface they need to diagnose
it. The inbound IdP webhook is likewise outside the gate: it
authenticates itself by verifying a signed body against its own
audience, and will never carry an identity assertion.

**Availability trade-off.** Because identity now fails closed,
JWKS reachability is load-bearing. Two failure modes, which differ
in severity:

- **A transient JWKS blip is invisible.** The key set is cached and
  is replaced only after a refresh fully succeeds, so a failed
  background refresh leaves the last-known-good keys in place and
  verification continues unaffected. A failed refresh is logged at
  `ERROR` with the JWKS URL — worth investigating, but not by
  itself an outage.
- **Key rotation *during* a JWKS outage is an outage.** A token
  signed with a `kid` absent from the cached set triggers a
  synchronous refresh bounded by the request deadline (and by a
  5s rate-limit wait). If the JWKS is unreachable, those requests
  are denied — after a stall of up to that bound.

Operationally: alert on the JWKS-refresh `ERROR` line, and stage
key rotations so a new signing key is published and picked up by a
refresh *before* the old one is withdrawn. Denials caused by an
unreachable JWKS are logged at `ERROR`, distinctly from denials
caused by a bad assertion (`INFO`), so the two are separable when
triaging. Both classes are independently rate-sampled with a
suppressed count, so neither can flood the log during an outage,
and the residual is reported on the first successful verification
afterwards.

Note that the two classes are told apart heuristically: a failure
that cannot be positively identified as a key-retrieval problem is
classified as a bad assertion. That biases toward a missed alert
rather than a false page — both still deny — so treat the `ERROR`
line as a strong signal and the `INFO` volume as a weak one.

**Deployment.** As with any proxy-fronted setup, run `rela-server`
bound to `0.0.0.0` behind the proxy (so it accepts the forwarded
`Host`), and give the proxy's browser origin via `--allowed-origin`
so same-origin API writes are permitted. The JWT signature is the
authentication; the Host/Origin allowlists remain the browser-CSRF
defense.

## Access control (`acl.yaml`)

rela-server enforces a declarative ACL at every write entry point, and — when
a policy is configured — filters reads. The policy lives at `acl.yaml` at the
project root (alongside `schema.yaml`). Three modes:

| Mode | How | Behavior |
|---|---|---|
| **Open** (default) | No `acl.yaml` present | Every authenticated request can write. Reads have no filtering. Suitable for single-user local projects. |
| **Read-only** | `rela-server --read-only` or `RELA_READ_ONLY=1` | Every write returns HTTP 403; reads unaffected. Useful for demos, maintenance, observe-only deployments. Wins over `acl.yaml` — explicit flag overrides policy. |
| **Policy** | `acl.yaml` present | Writes are gated by role assignments and delegate permissions. Reads are filtered on the data-entry HTTP surface: per-entity GETs 404 like not-found for hidden entities; lists / pagination / `?include=` / `/_position` / `/_search` return only the visible subset; and `visible:`-denied properties are redacted from every response body. MCP read surfaces are not yet filtered. See [GUIDE-acl-security]. |

A startup warning fires when the server binds **beyond loopback**
(`--bind` non-loopback) **without** `acl.yaml` AND **without**
`--read-only` — that combination means anyone reachable on the
network can write to the project.

**Configuring the policy** — roles, per-verb grants, delegate-X tamper
resistance, group membership (`member-of`) hardening, the trust boundary on
`principal.user`, and the field-/option-/relation-level affordances driven by
`fields:` / `visible:` / `options:` / `relations:` grants — is covered in
[GUIDE-acl-overview] (the model) and [GUIDE-acl-security] (operator
hardening). The rest of this section documents only what the ACL **covers as a
defense** in the server threat model.

### What the ACL covers

- ✅ Write authz at every `Manager.{Create,Update,Delete}{Entity,Relation}` + `RenameEntity`.
- ✅ HTTP 403 with structured `{error, rule_kind, rule_id, reason}` body.
- ✅ Audit log records every deny as `denied-write` (see [GUIDE-audit-log]).
- ✅ Data-entry SPA hides entity-CRUD write controls based on the
  per-resource `_actions` verdict map — read-only mode produces a
  UI with no "+ New", delete, or Edit buttons for entities,
  driven by the ACL with no frontend flag. Deferred phase-2 sites
  (Lua command buttons, settings / theme / git writes, relation
  add/remove inside form widgets) remain visible and 403 at the
  server on click; later phases gate them as new verbs land.
- ✅ Field-, option-, and relation-level affordances driven by
  per-role `fields:` / `visible:` / `options:` / `relations:` grants
  with optional `when:` predicates. See [GUIDE-acl-security].
- ✅ **Entity-level read filtering** on the data-entry HTTP surface.
  Per-entity GETs 404 like not-found for hidden entities; lists,
  pagination, `?include=` neighbours, `/_position`, and `/_search`
  return only the visible subset. See [GUIDE-acl-security].
- ✅ **Property-level redaction** (`visible:` grants) on every
  data-entry HTTP read. A field denied by `visible:` is omitted from
  the response `properties` map on per-entity GET, list rows,
  `?include=` peers, and `/_search` results — not just the write
  form. When the hidden field is the display property, the title
  falls back to the entity ID so the redacted value can't leak
  through `_title`.
- ✅ **`/_search` is not a hidden-field oracle.** A search whose only
  match is a `visible:`-hidden property drops the hit rather than
  confirming the value by returning the entity; a hit that also matched
  a visible field (or id/content) still surfaces, body redacted. See
  [GUIDE-acl-security].
- ✅ Group expansion (`member-of`, transitive) and inherited local
  roles (containment, via `inherit_roles_through`). Direct local
  roles are honored as well.
- ❌ **MCP read surfaces are not yet ACL-filtered.** `show_entity`,
  `list_entities`, `search_entities`, and the trace tools return
  full entity bodies with no entity-level gate and no `visible:`
  redaction. The MCP server is local-only (stdio), so this is an
  accepted gap at this stage, tracked as a follow-up. Do not expose
  the MCP transport to an untrusted caller while relying on `visible:`
  for confidentiality.
- ❌ MCP transport intersection (filtering the tool list per principal) — deferred to a follow-up.

> **`_actions` is a UI hint, not an authorization layer.** The
> data-entry server re-authorizes every write — a client that
> bypasses the affordance map (forges `delete: true` and issues
> DELETE anyway) gets the same `403` the policy would have produced
> for any other denied request. The scope of the affordance
> invariant is HTTP write endpoints reached by the SPA; MCP / Lua /
> scheduler write paths share the same enforcement but do not emit
> or consult `_actions`.

## Running the Vue dev server (Vite)

If you run the SPA via Vite on `http://localhost:5173`, requests to the Go
backend will carry `Origin: http://localhost:5173`, which is **not** in the
default allowlist. Tell `rela-server` to permit that origin:

```sh
rela-server --allowed-origin http://localhost:5173
```

The flag is repeatable. Each value must be a complete origin
(`scheme://host:port`).

## Calling the API from curl, scripts, or non-browser clients

The Origin allowlist treats requests with no `Origin` and no `Referer` header
as cross-origin and rejects them with `403 forbidden` and reason
`origin_missing`. This catches `<img src=...>` style attacks where the
attacker has set `Referrer-Policy: no-referrer` to strip both headers.

It also rejects bare `curl http://localhost:8080/api/...` calls. To use the
API from the command line, set the Origin header explicitly:

```sh
curl -H 'Origin: http://localhost:8080' http://localhost:8080/api/v1/_config
```

The same applies to any script, MCP integration, or test harness that speaks
HTTP directly to `rela-server`.

## Troubleshooting

**"403 forbidden" with reason `host_not_allowed`** — your client sent a
`Host` header that doesn't match the bound address. If you're hitting the
server from another machine, either rebind to that interface (`--bind ...`)
or check whether DNS rebinding is in play.

**"403 forbidden" with reason `origin_not_allowed`** — your client sent an
`Origin` header that isn't in the allowlist. Add it via `--allowed-origin`
or run from a same-origin context.

**"403 forbidden" with reason `origin_missing`** — neither `Origin` nor
`Referer` was present. See "Calling the API from curl" above.

**SSE / live reload not working in Vite dev mode** — check that the Vite
proxy in `frontend/vite.config.ts` forwards `/api/events` and that you
passed `--allowed-origin http://localhost:5173`.

## Residual risks and known limitations

The following risks are **not** fully mitigated by the defenses above. They
are documented here so operators can make informed decisions.

### TOCTOU window on command file downloads

There is a time-of-check / time-of-use window between the containment check
(when a download token is minted) and the file being opened (when someone
clicks Download). An attacker with local filesystem write access could swap a
contained path for a symlink during that window.

This is an accepted residual because:

- The local filesystem is the trust boundary. Anything that can write files
  into your project can also edit the `commands:` configuration that decides
  what runs in the first place.
- Closing it would mean holding an open file descriptor for every reported
  file, for as long as its token lives.

### No authentication *by default*

In the default single-user deployment there is intentionally no login. The
trust boundary is "anything running as the current user on this machine."
Per-instance session tokens (defense in depth on top of the Origin allowlist)
are tracked as a follow-up.

This is a statement about the **default**, not a limit of the server. Two
opt-in identity sources exist, and a multi-user or network-reachable
deployment should use one:

- `-jwt-issuer` / `-jwt-audience` / `-jwt-jwks-url` — verify a signed
  assertion against the IdP's JWKS. Fail-closed: an unverified request to
  `/api/` is refused, with no fall-through to a header.
- `-principal-header` — trust a header set by a fronting proxy. Only as
  trustworthy as that proxy; it is refused alongside the JWT flags precisely
  so a JWKS outage cannot silently downgrade to it.

Remote MCP (`-mcp`) requires the JWT form specifically — see above.

### Configured commands are remote-code-execution by design

The `commands` section of `data-entry.yaml` lets you wire up arbitrary
shell scripts that run with your user privileges. Be careful what you put
there. The `/api/command/` endpoint is `POST`-only and protected by the
Origin allowlist, but the scripts themselves are still trusted code.

Because a command is arbitrary shell execution, `rela-server` gates
`/api/command/` by the active access-control mode and the bind:

| Mode | Bind | Command execution |
| --- | --- | --- |
| **Policy** (`acl.yaml`) | any | Governed: a command runs only when its `permission:` is set **and** held by the caller. `context: view` commands are denied (no fine-grained control yet). |
| **Read-only** (`--read-only`) | any | Denied — read-only means no execution. |
| **Open** (no `acl.yaml`) | loopback | Ungated (the server host is your own machine). |
| **Open** (no `acl.yaml`) | non-loopback | **Denied by default.** |

The last row is the important one: on a network bind with no `acl.yaml`,
command execution is **refused**, because any client that can reach the
server could otherwise run an arbitrary shell script. To restore ungated
commands on such a bind you must either add an `acl.yaml` (and gate each
command with a `permission:`) **or** opt in explicitly with:

```sh
rela-server --bind 0.0.0.0 --port 8080 --allow-unauthenticated-commands
# or: RELA_ALLOW_UNAUTHENTICATED_COMMANDS=1
```

Only use `--allow-unauthenticated-commands` for a **single-user**
deployment that is isolated at another layer — Docker port-publishing
scoped to a trusted host, a host firewall, or a reverse proxy that
authenticates every request. It is the command-authorization analogue of
[`--unconfined-commands`](transforms.md) (which disables the *sandbox*):
the platform refuses the risky thing by default, and a host that isolates
elsewhere asserts that explicitly. When the override is active the server
logs a prominent warning at startup. Loopback binds and any deployment
with an `acl.yaml` never need this flag.

> **Upgrade note.** Earlier versions ran configured commands ungated on
> *every* bind when no `acl.yaml` was present. A non-loopback deployment
> that relied on that now refuses commands until you add an `acl.yaml` or
> pass `--allow-unauthenticated-commands`. This is deliberate — the old
> default exposed unauthenticated shell execution to the network.

### Remote MCP exposes every tool except Lua

`-mcp` (see [mcp-server.md](mcp-server.md#remote-mcp-over-http)) serves the
MCP tool set over HTTP, except the Lua tools (`lua_eval`, `lua_run`,
`lua_list`). A script is caller-supplied code that runs in the server process,
so a remote caller could use it to exhaust server resources. Lua tools are
opt-in per server (`WithLuaTools`), and only local stdio opts in. Every call is
authenticated (the flag refuses to start without verified JWT identity),
authorized by the same ACL as the web API, and audited as the requesting
principal — so a remote caller can do exactly what that person could do
through the UI, no more.

Apart from Lua there is no per-transport allowlist: a new tool added for local
stdio use becomes remotely reachable the moment `-mcp` is on.

Two related gaps, both deliberate and tracked:

- **No RFC 9728 discovery.** The 401 does not carry a `resource_metadata`
  challenge unless your assertion header is literally `Authorization`, so MCP
  clients must be pointed at the IdP by configuration rather than discovering
  it. Usability, not confidentiality.
- **`acl.Request` is not goroutine-safe.** One is attached per HTTP request
  and memoises global roles without synchronisation. Nothing in the current
  handler fans a JSON-RPC batch across goroutines, so this is latent rather
  than live — but it constrains how batch dispatch may be implemented.

### Future WebSocket endpoints need explicit Origin checks

WebSockets are not currently used by `rela-server`. If a future feature
adds them, note that the browser does **not** enforce same-origin policy
on WebSocket upgrades — the upgrade handler must explicitly check
`Origin` itself, the same way the existing `requireSameOrigin` middleware
does for HTTP requests.

## Reporting vulnerabilities

If you discover a security issue not covered here, please open an issue
on the GitHub repository or contact the maintainers privately.
