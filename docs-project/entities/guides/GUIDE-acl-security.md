---
id: GUIDE-acl-security
type: guide
title: "ACL: Security Hardening"
status: published
order: 21
audience: advanced
summary: "Operator's hardening guide for rela's ACL system: group membership trust, fail-loud boot, audit-isolation invariants"
---

This guide covers the security properties an operator running
rela with an `acl.yaml` needs to understand. Read [GUIDE-acl-overview]
first; this assumes you know the resolver vocabulary.

## Hardening the membership relation

rela's v1 ACL confers group roles by walking the **membership
relation** from the principal — the relation type named by
`membership_relation:` in `acl.yaml`, default `member-of`. By default
that relation is a regular relation type — there is no built-in
restriction on who can create one. **If you use groups in
`assignments`, you must gate writes to the membership relation**, or
any user who can create such a relation can grant themselves any role.

The simplest hardening is to require a `delegate-membership` permission
on the relation and grant it only to administrative roles:

```yaml
role_relations:
  member-of:
    requires_permission: delegate-membership

roles:
  admin:
    permissions: [delegate-membership]
    create: ["*"]
    update: ["*"]
    delete: ["*"]
    read: ["*"]
```

With that in place, only principals who hold `delegate-membership`
(directly or via inherited role) can add someone to a group.
Operators of single-user instances who don't use groups can ignore
this; the moment you add an `assignments` mapping for a group, this
is mandatory.

### The gate covers every verb

`requires_permission:` takes one permission name, and it gates **all**
write verbs on the relation — create, update and delete alike. There is
no `create:`/`update:`/`delete:` form here, and that is deliberate: the
escalation this gate stops is a *create*
(`alice --member-of--> admins`), so a policy gating only some verbs
would read as hardened while stopping nothing. Removing a conferred
edge is an availability attack in its own right, so `delete` is not
safely left open either. Naming the permission after a verb
(`member-of:create`) is therefore misleading — prefer
`delegate-membership`, which says what holding it lets you do.

Writing a verb key under `role_relations:` is refused at load rather
than ignored, so a policy that reaches for one fails loudly instead of
booting un-gated:

```console
$ rela acl audit
appbuild: load acl.yaml: acl: parse /srv/rela/acl.yaml: role_relations:
"create" is not supported — `requires_permission:` gates ALL write verbs
on a role-conferring relation, by design. Per-verb permissions are
`relation_grants:`, which is refused on a type that confers a role
```

The policy fails to load at all, so this stops the server rather than
only the linter — which is the point: an un-gated membership relation
should not boot.

Per-verb relation permissions do exist — as `relation_grants:`, covered
in [GUIDE-acl-overview]. They apply only to relation types that do
**not** confer a role; naming a gated `role_relations:` type there is
refused, so the two can never disagree about one relation.

If you point `membership_relation:` at a domain-specific relation
(e.g. `heeft_rol` in a Dutch-language ISMS), the same hardening
applies to *that* relation — the `requires_permission` gate must name
the relation you actually configured:

```yaml
membership_relation: heeft_rol
role_relations:
  heeft_rol:
    requires_permission: delegate-membership
```

The `rela acl audit` linter (below) flags an un-gated membership
relation — default or configured — as a high-severity finding, so run
it after editing `acl.yaml`.

You do not have to remember to run it, though: whenever rela loads a
policy whose `assignments` confer a **privileged** role while the
membership relation is un-gated — at server startup, and equally when a
CLI command builds the full service bundle — it logs a prominent
warning naming the relation and the fix. (Surfaces that inject their
own ACL instead of loading the policy, such as the server's read-only
mode, do not evaluate it and stay silent.) The server still boots — the
gap is pre-existing, and in a single-team deployment where everyone who
can write to the project is already trusted, it may be an acceptable
risk you have consciously taken. The warning exists so it is a choice
rather than an oversight.

A group assigned a **read-only** role does not trigger the warning (or
the audit finding): granting yourself read access to something you were
going to be shown anyway is a visibility decision, not an escalation
path. Both the warning and the linter evaluate the same predicate, so
they never disagree.

### This is a hard requirement with content states

The tolerance above holds only while the worst case is over-granting inside a
trusted group. It stops holding when the same policy also grants read on a
**non-default world**, which is the content-states feature described in the
[Metamodel Reference](metamodel.md#content-states-and-worlds). There, an
ungated membership relation is no longer a role-management weakness. It is a
working mechanism for reading unpublished content, because a principal who can
write one membership edge can assign themselves a role that reads the draft
world.

A policy that grants read on a non-default world while leaving a
self-promotion path open is therefore **refused at load**, with an error that
names the fix. It does not boot with a warning. Two shapes trigger the refusal:

1. The membership relation is ungated, and an assignment confers a role that
   can escalate.
2. **Any** role relation is ungated while conferring a role that can escalate,
   even when the membership relation itself is gated. Without this second arm,
   an attacker writes one ungated edge to gain a role holding the gating
   permission, then writes the membership edge.

"Can escalate" means the role grants a write verb, holds a permission, **or
holds a non-default world read grant**. The last term is why a read-only role
counts here but not in the warning above. Normally, granting yourself read
access is a visibility choice rather than an escalation, but a role that can
read `world:editorial` is exactly the thing worth stealing once worlds exist.

The second arm is deliberately broader than strictly necessary. It refuses some
policies whose ungated relation could not actually reach the membership gate.
Those policies already carry a high-severity `rela acl audit` finding, and the
fix is the same single `requires_permission` line. For a check whose failure
mode is leaked unpublished content, refusing too much is the right direction.

Deployments that do not use worlds are unaffected and keep the warn-and-boot
behaviour. Gating the membership relation now costs one `requires_permission`
line and makes the change a no-op for you when you adopt worlds later.

The companion section in the [Security model for rela-server](server-security.md)
guide carries the same guidance with more context on the broader threat model.

### Scoping a grant to a content state

Both reads and writes may name a face:

```yaml
roles:
  reader:
    read: [policy@published]     # the published face, and no other
  publisher:
    update: [policy@published]   # may write the published face
```

A face-scoped read grant gates **every** read path: the entity list, the
single-entity read, `?include=` neighbours, views, and search alike. A denied
face produces the same not-found response as a face that does not exist, so a
grant cannot be used to discover which faces an entity has.

**The default differs between reads and writes, deliberately.**

| Grant | Covers |
| --- | --- |
| `read: [policy]` | **every** face |
| `read: [policy@published]` | that face only |
| `read: ["*"]` | every type, every face |
| `update: [policy]` | on a faced type, **nothing** — see below |
| `update: [policy@published]` | that face only |
| `update: ["*"]` | every type, unnamed state only |

A bare read grant covering every face is not laxness. A world never serves the
unnamed state when its chain names a face, so a bare read grant narrowed to
that state would read **nothing** under any world. That is a total outage
rather than a narrowing. Writes have no such interaction: they address a face
by id and never pass through a world.

The practical consequence is that adding `faces:` to a live type does not
silently tighten existing read grants. If you need a role kept away from
drafts, name the face it may read.

#### A write grant on a faced type must name the face

A write grant matches the face **as stored**, and a type that declares `faces:`
stores every row under a face name. Nothing is stored at the bare coordinate.
Given this type:

```yaml
policy:
  faces: {draft: {}, published: {}}
```

`POL-1@draft` and `POL-1@published` are the only rows that exist. The grants
therefore behave as follows:

| Grant | Reaches the draft face? |
| --- | --- |
| `update: [policy]` | **no**, it matches nothing and denies everything |
| `update: [policy@draft]` | **yes**, this is the correct grant |

Name every face the role may write:

```yaml
editor:
  update: [policy@draft]
```

The bare form is the dangerous spelling because it reads like a permission. It
fails closed, denying rather than over-permitting, so the symptom is an editor
who cannot save with no error naming a face. `rela acl audit` reports it as
`B12-bare-grant-on-faced-type` at severity High, with the fix spelled out.

Note that `*` is a wildcard over **types**, never over faces. `update: ["*"]`
grants each type's unnamed state, so it reaches faceless types only. A role
that must write a faced type needs that type's faces listed explicitly, even if
it already holds the wildcard. This is deliberate: the alternative would mean
every existing admin grant silently acquiring authority over `published` the
moment a type declared its first face.

#### World grants select a lens

A `world:` grant does **not** keep a role away from a face. It scopes which
worlds a caller may select with `?world=`, not which faces they may read. A
caller who omits `?world=` reads the default world, which applies no
resolution — but every face remains reachable there by addressing it directly
as `POL-1@draft`, so only a face grant stands between that caller and a
draft.

A caller who omits `?world=` may still be in a world. `app.default_world` in
`data-entry.yaml` sets the world a bare request lands in, and the server
applies it to the API and the web app alike. It is still not a gate. It selects
a face, and the `world:` grant is re-checked for it exactly as for an explicit
`?world=`, so pointing the default at a world a role may not read yields that
world's ordinary empty result, never an escalation.

## Auditing your policy with `rela acl audit`

To keep your policy correct and hardened, run `rela acl audit` after
every change to `acl.yaml`. It catches the mistakes that don't fail
boot: a typo'd entity type that silently grants nothing, an un-gated
membership relation that opens a self-promotion path, a write grant on
the `everyone` role that hands capabilities to anonymous callers.
Boot-time validation (`Policy.Validate`) deliberately stays a narrow
*structural* gate — it rejects malformed policies but does not hunt for
these foot-guns; the audit is the tool that does.

The audit reports severity-ranked findings across two tiers:

- **Pure-policy** — escalation foot-guns (un-gated membership relation
  or role-relation conferring a privileged role; a privileged
  `everyone` role) and dead/inert config (assignments or `confers`
  naming an undeclared role, a `requires_permission` no role grants,
  dead permissions, wildcard write sprawl).
- **Metamodel cross-check** — grants, `membership_relation`,
  `role_relations`, `inherit_roles_through`, `user_entity_type`, field,
  and option references that the schema doesn't declare (silent drift).
  This includes the content-state grant forms: a `read: [world:X]` grant
  naming a world no `worlds:` block declares, and a `type@face` write
  grant naming a content state the type doesn't declare. Both fail
  *closed* at runtime — the grant simply matches nothing — so without
  the audit the only symptom is a denial nobody can explain.

```console
$ rela acl audit
⚠ ACL audit: 2 finding(s)
  [critical] role "everyone" ... grants write or permissions ...
      fix: remove write/permission grants from the everyone role ...
  [high] membership relation "member-of" confers group roles ... not gated ...
      fix: add role_relations.member-of.requires_permission ...
```

The audit is **advisory** — with no flag it prints findings and always
exits zero. For CI, gate the exit code on a severity threshold with
`--fail-on`:

```bash
rela acl audit --fail-on=high     # non-zero if any critical/high finding
rela acl audit --fail-on=medium   # also fail on medium
rela acl audit --fail-on=any      # fail on any finding at all
rela acl audit --exit-code        # alias for --fail-on=high
```

A finding **below** the threshold never changes the exit code — so
`--fail-on=high` lets a medium "wildcard write" nudge through without
breaking the build, while `--fail-on=any` is the strictest gate.
Findings are always printed regardless of the threshold.

**For a production deployment, gate CI on `--fail-on=any`** and keep
the policy clean — the medium/low findings (wildcard sprawl, dead
permissions, drift) are exactly the slow rot you want to catch before
it hides a real hole. Loosen to `--fail-on=high` only if you have a
deliberate, reviewed reason to tolerate the lower-severity findings.

Use `-o json` for machine-readable output. A clean, well-gated policy
reports no findings and exits zero.

## Resolving the principal by property (`principal_property`)

When `user_entity_type` + `principal_property` are set, the resolver
maps the raw authenticated identifier to a user entity by looking it up
against that property (see GUIDE-acl-overview). Because the property is
used as an **identity key**, a duplicate value is a security ambiguity,
not just a data-quality issue — two `persoon` records with the same
`email` would make "who is this principal?" unanswerable.

Two layers keep that from happening:

1. **The property must be declared `unique: true`** on the
   `user_entity_type` in the metamodel. Boot fails if
   `principal_property` names a property that does not exist or is not
   unique. `unique: true` is a general metamodel constraint: no two
   entities of the same type may carry the same non-empty value for that
   property. It is enforced at write time — a create or update that would
   introduce a duplicate is rejected as a validation error (422). Empty
   values are exempt (a property is unique among the entities that set
   it).

2. **The resolver refuses to guess through a duplicate.** If, despite the
   constraint, more than one entity matches (e.g. data predating the
   constraint), the resolver keeps the raw principal, logs a warning, and
   grants only what the raw string is assigned — it never silently picks
   one of the duplicates.

**Enforcement rigor by backend.** The application-level uniqueness check
reads the existing entities of the type and then writes — two separate
operations with no lock held across them. On the single-writer backends
(fsstore, memstore) that is race-free enough. Under concurrent writers,
though, two racing writes with the same value could both pass the check
and both commit, leaving a duplicate.

On **PostgreSQL** this window is closed automatically: rela maintains a
partial unique index for every `unique: true` property (see
[Derived schema](postgres-backend.md#derived-schema-unique-constraints)
in the PostgreSQL backend guide), so a colliding write fails atomically in
the database and surfaces as the same conflict the write path already
reports. You do not add this index by hand — rela creates and maintains it
from the metamodel. (If existing rows already contain duplicate values when
you add `unique: true`, the index cannot be built; rela warns and leaves it
unenforced until you clean up the duplicates — run `rela db reconcile
--dry-run` to see what is blocking it.)

This matters most for `unmatched_principal: provision`
([below](#unknown-verified-identities-unmatched_principal)), which creates a
stub user keyed on `principal_property`: the database index is what
guarantees two servers provisioning the same subject at once produce exactly
one user rather than a permanently ambiguous pair. On fsstore/memstore, which
have no such index, keep provisioning to a single writer.

**Two operator notes for `principal_property`:**

- **Case sensitivity.** The lookup and the uniqueness check are exact
  byte comparisons. If your auth proxy emits `JV@x.com` but the persoon
  stores `jv@x.com`, the lookup finds no match and the request silently
  falls back to the raw principal (losing grants, never gaining them).
  Normalise the property value on write (or configure the proxy to emit
  a canonical form) — rela does not case-fold.

- **Resolution is data-entry-only.** The `principal_property` lookup is
  wired into the `/api/` data-entry path only. Writes via the CLI, MCP,
  scheduler, or desktop entry points authorize against the raw stamped
  principal and are NOT resolved to a `persoon`. A grant keyed on the
  resolved entity ID therefore applies to data-entry writes but not to
  the same person's CLI/MCP writes (which fall back to whatever the raw
  identifier is assigned — typically `everyone` only). This is
  fail-closed. Don't assume one `acl.yaml` grants the same authority on
  every transport.

## Unknown verified identities (`unmatched_principal`)

When a **verified** JWT principal's subject resolves to no
`user_entity_type` entity (the `principal_property` lookup found no
match), rela must decide what an identity it trusts but does not *know*
may do. The `unmatched_principal` key chooses:

```yaml
user_entity_type: persoon
principal_property: sub
unmatched_principal: reject   # anonymous (default) | reject | provision
```

- **`anonymous`** (default, and the behavior when the key is absent): the
  request proceeds. The principal keeps any roles its verified assertion
  grants (see `asserted_role_assignments`), but has no resolved entity, so
  local roles, ancestry, and `principal_property` grants do not apply. This
  is the SSO-newcomer case — a user provisioned in the IdP but not yet in
  the graph can still act on their asserted roles.
- **`reject`**: a graph-is-authority posture. The entity graph is the
  authority on who may mutate, so an unknown verified identity's **writes**
  are denied (403). Its **reads** are unaffected — it still sees what its
  asserted roles allow. Blocking reads too is a larger, separate choice and
  is not what this does.
- **`provision`**: lazy creation of a stub user entity. On the unmatched
  principal's **first authorized write** (never a GET), rela creates a
  minimal `user_entity_type` entity keyed on `principal_property = sub`,
  then the triggering write proceeds as that newly-created identity. The
  stub carries the verified `email`, `org_id`, and `org_slug` claims *only
  when the user type declares those properties* (otherwise they are
  dropped). The create is performed and audited under the built-in
  `system:provisioner` identity — see the operator-responsibility notes
  below.

Load-bearing details:

- **Scope is the verified-JWT data-entry write path only.** `reject`
  keys on the fact that identity came from the fail-closed JWT gate AND
  resolved to no entity. A `--principal-header` deployment, and the
  CLI/MCP/scheduler entry points, are never subject to it — their
  principals legitimately don't resolve to a `persoon` either, and a
  blanket rule would wrongly deny them. It gates writes (CRUD, sync, and
  Lua-action writes all funnel through the one write-authorization point);
  reads and non-`/api/` paths are untouched.
- **`reject` and `provision` require the lookup.** Both need
  `user_entity_type` + `principal_property` set — otherwise "unmatched"
  has no meaning (with the lookup disabled the resolver returns no-match
  for *every* request, so every principal would look unmatched and be
  rejected). Setting `reject` without them is a **load error**, not a
  runtime foot-gun.
- **`reject` also needs a wired JWT gate.** It keys on identity having
  come from the fail-closed JWT gate, so a deployment that sets `reject`
  without wiring one gets a key with **no effect** — unmatched principals
  are treated as `anonymous`, exactly as before the key was added. This
  cannot be a load error like the lookup requirement above, because
  `acl.yaml` cannot see server wiring; rela logs a warning at startup
  instead, naming which of the two conditions is missing. If you
  configured `reject` and writes are still going through, check that
  warning first.
- **`provision` additionally requires the `system:provisioner` grant.**
  The stub create is ACL-authorized like any other write, so the policy
  must grant `system:provisioner` `create` on the `user_entity_type`. The
  `acl-provisioner-grant` migration injects exactly this grant (a
  `create`-only role bound to `system:provisioner`) the same way the
  scheduler grant works — run your migrations (`rela db migrate`, or the
  migration runs on first store open) or add the grant by hand. A
  `provision` policy **without** that grant is a **load error**, so a
  misconfiguration fails at startup, not at the first unmatched request.
- **`system:` identities cannot be asserted from a request.** Both
  `system:provisioner` and `system:scheduler` are grantable assignment
  keys, and the ACL matches a principal by raw username with no notion of
  where it came from. The boundary check lives at the API instead: a
  `system:`-prefixed username arriving via `--principal-header`,
  `$RELA_DATAENTRY_USER`, or a *validly signed* JWT `sub` is refused and
  logged, so neither grant can be borrowed by a web or MCP caller. See
  "Reserved `system:` identities" in [GUIDE-server-security]. This is also
  why a forged subject can never be provisioned into a stub's
  `principal_property`.
- **The provisioned stub is bare — identity, not authority.** The
  `system:provisioner` role is `create`-only on the user type and nothing
  else, so provisioning gives the principal a graph *identity* but **no
  group membership or local roles**. This is deliberate: the on-create
  cascade authorizes as `system:provisioner`, which cannot author
  relations, so an on-create automation that adds a `member-of` edge would
  fail. An on-create automation that only sets *properties* runs fine.
  Group/local-role assignment for a newly-provisioned user is your
  responsibility — via the IdP webhook, a reconciliation job, or an admin.
  The stub is not inert in the meantime: the principal's **asserted roles
  still apply** (see `asserted_role_assignments`), so it is not powerless
  while it waits for group assignment.
- **On a filesystem/memory backend, provisioning is best-effort unique.**
  `principal_property` uniqueness is enforced check-then-write on fs/mem,
  not atomically, so two processes (or the IdP webhook racing a lazy
  provision) can in principle create two stubs for one subject. In one
  process the write mutex serializes them and the loser re-resolves the
  existing stub. A PostgreSQL backend closes the cross-process gap with a
  real unique constraint. If two stubs ever do collide, resolution for that
  subject becomes ambiguous until an operator merges them.
- **The 403 discloses only that the identity is unmatched.** Like every
  ACL denial on the data-entry write path, the body carries a
  `rule_kind`/`reason` (here, `unmatched-principal` — "verified principal
  resolves to no user entity") so the SPA's affordance contract stays
  uniform. It does **not** reveal anything the caller doesn't already
  know: an unmatched principal knows it has no entity. It does not
  distinguish "no IdP account" from "no graph entity," and it exposes no
  policy detail beyond the rule that fired.

## Roles from a verified assertion (`asserted_role_assignments`)

`asserted_role_assignments` maps a `roles` claim from a signed identity
assertion to a policy role (see GUIDE-acl-overview for the mechanics).
Three properties are load-bearing.

**The trust boundary is the signature, and it is enforced structurally.**
`internal/acl` verifies nothing — it trusts the `Principal` absolutely.
A role reaching it from an unverified source would therefore be a
complete authorization bypass, not a degradation. The assertion claims
live in *unexported* fields on `principal.Principal`, populated only by
`principal.Verified`, which only the JWT resolver calls after signature
verification. No composite literal anywhere in the tree can forge a
role; the compiler enforces this rather than a reviewer remembering to.

Consequence for operators: **never introduce a header that is trusted as
a role source.** Trusting `X-Asserted-Roles` the way `X-Forwarded-User`
is trusted under `--principal-header` would hand role selection to
anyone who can reach the server. The header path deliberately cannot
carry roles at all.

**The claim → role indirection is the second control.** A claim value
never becomes a role name; it only ever selects an entry the operator
authored. An attacker who influences role names in the IdP still cannot
name a rela role the operator has not mapped. Keep the mapping minimal
and review it like any other grant — `rela acl audit` flags a claim that
maps to a privileged role.

**Revocation lags by one assertion lifetime.** rela re-checks nothing
between requests: an assertion stays valid until its `exp`, so a role
revoked in the IdP keeps working in rela until the token expires. With
Pratique that window is **up to 9 minutes** (`AssertionTTL`). rela has
no revocation channel — there is nothing to "push" a revocation to. If
your incident response depends on cutting access faster than the token
lifetime, you must restart or otherwise intervene at the proxy; do not
assume revoking in the IdP takes effect immediately downstream.

### `org_id` is recorded, not enforced

A verified assertion's `org_id` / `org_slug` are stamped on the
principal and appear in the audit log. **Nothing in `internal/acl`
evaluates them.**

Read that literally before designing around it: a principal in org A
holding a role sees **every entity that role grants, in every org**.
There is no tenant isolation. The org in your audit log records which
tenant a request *came from*; it does not constrain what that request
could reach.

This is a deliberate scope decision, not an oversight — ACL evaluation
is additive (any role granting the verb allows it), so a cross-cutting
"deny unless org matches" needs its own design rather than a field check
bolted onto the resolver.

Client attenuation is not a counter-example. It narrows, but it does so
by compiling restrictions into plain allowlists at LOAD time, keyed on a
claim whose value set the operator enumerates. Org enforcement cannot
work that way: the comparison is between a claim and a property of the
entity being evaluated, which is a per-row predicate — the thing the
read path deliberately does not have. A test
(`TestAssertedRoles_OrgIsNotEvaluated`) pins the absence of enforcement
so it cannot be mistaken for an omission, and fails the day a partial
org check is added.

If you need per-tenant separation today, model it in the graph:
containment edges plus `inherit_roles_through` scope roles to a subtree
positively, and read-filtering follows for free. That works only if no
role grants a broad wildcard read — a global `read: ["*"]` defeats any
org scoping, in this design or any other.

## Client attenuation (`client_baselines` / `scope_grants`)

Client attenuation restricts a client BELOW the user it acts as (see
GUIDE-acl-overview for the mechanics). Four properties are load-bearing.

**Same trust boundary as asserted roles, and for the same reason.**
`principal_type` and `scope` live in unexported fields on
`principal.Principal`, populated only through `principal.Verified` /
`VerifiedFrom` after signature verification. Never introduce a header
trusted as a `principal_type` source.

The direction of harm is inverted from a role, which is worth stating
explicitly because it is easy to reason about backwards: forging a role
*adds* access, while forging a `principal_type` selects a ceiling and
therefore *removes* it. The dangerous manipulation here is not injecting
a claim but **dropping** one — a principal whose `principal_type` is
lost matches no baseline and is unrestricted. That is why every re-stamp
of a Principal must carry the claims forward, and why the audit log
records them.

**A ceiling only ever narrows.** `effective = user_grants ∩ (baseline ∪
scopes)`. Nothing an operator writes in `client_baselines` or
`scope_grants` can grant capability the acting user lacks, so a mistake
in one of these blocks fails toward *less* access. The one exception to
that comfort is a ceiling that silently matches nothing — see the audit
rules below.

**This does not contradict the additive model.** Restrictions are
compiled at LOAD time into ordinary allowlists: `redact: {person:
[salary]}` becomes "the permitted field set for person, minus salary",
and the runtime evaluator sees a plain allowlist exactly as before. No
denial primitive exists at evaluation time, and none should be added —
`ReadQuery` compiles to a `store.GraphQuery` pushed down into SQL, so a
runtime deny would have to become a SQL predicate in every backend.

**An inert ceiling is the failure mode to watch.** A wrong *grant*
denies something and someone reports it. A wrong *restriction* just
fails to restrict, and nobody files a bug about access they still have.
`rela acl audit` therefore reports:

- `A11-inert-client-baseline` — a baseline that narrows nothing.
- `A13-baseline-matches-nothing` — an empty `applies_to`.
- `A12-scope-reopens-nothing` — a scope re-opening what no baseline
  closed (it appears to work, which is exactly the trap).
- `B8` / `B9` — a ceiling naming an entity type or field the metamodel
  does not declare. A typo in a *denial* is worse than in a grant: it
  silently fails to protect.

Run `rela acl map --principal <user> --as <type>` to see what a client
actually gets, rather than inferring it from the policy file.

### `deny_worlds` selects a lens; it does not guard content

A ceiling may restrict which worlds a client may select:

```yaml
client_baselines:
  apps:
    applies_to: [app]
    deny_worlds: [editorial]     # this client may not select ?world=editorial
```

**Read this as hiding a menu option, not as protecting data.** A world is a
lens. It decides which *face* of each entity a caller is served, and the
confidentiality decision is made one level down, on the face itself.
`read: [policy@published]` compiles to a face predicate pushed into the query,
and that predicate is what keeps a reader away from the draft. The ACL package
cannot resolve a world at all: it emits a world-free query, and the layer above
stamps the world onto it.

`deny_worlds` therefore stops a client from picking a lens that makes no sense
for it. It is not the thing standing between that client and the data, and
nothing should be built as if it were. If you need a client kept away from
content, say so with the grants: name the faces in `read:`, or use
`deny_read`. Those run on every request path.

**`deny_worlds: [default]` does nothing at all.** The request path resolves the
default world before the ceiling is consulted, so the entry loads cleanly and
is silently inert. Treat it as unimplemented rather than as a control you have
configured. `rela acl audit` will not warn you, because `B10-undeclared-world`
skips the default world and the entry produces no finding.

The default world is not a special case here. It is the lens you get when you
name none, and it applies no resolution at all. Denying it would not mean "deny
the data" but "refuse to serve a request that picked no lens", and it would not
even hide a face, since a face is addressable as `ID@face` under any lens. That
is why nothing relies on it, and why the answer is to gate the faces rather
than the lens.

### Not a substitute for gating the client itself

stdio MCP has no authentication, so no verified claim exists to key a
baseline on, and its read path does not route through the ACL read
gate: it runs with the operator's own access. The remote endpoint
(`rela-server -mcp`) is gated. Client attenuation makes the *policy*
expressible; it does not by itself restrict a locally-launched
`rela mcp`.

## Fail-loud on malformed `acl.yaml`

A malformed `acl.yaml` fails boot. This is intentional: silently
degrading to NopACL (allow-all) on a typo would invert the operator's
intent — they wrote a policy specifically *to* restrict access. The
operator sees a clear error referencing the parse failure and the
file path; they fix the file and restart.

A genuinely absent `acl.yaml` is different: no file means "no access
control intended," and the server boots with `acl.NopACL{}`.
`rela-server` will warn at startup if you also bind non-loopback in
this configuration (no auth + no ACL + reachable from the LAN is
almost never what you want).

## Why `nil Subject` panics

`AuthorizeWrite` panics when its WriteRequest carries a nil Subject.
This is not a security feature per se — it's a programmer-error
guard. A nil Subject in a request means a call site forgot to
construct one, and the safe-looking alternatives (silently allow,
silently deny) both lose information:

- Silently allow → bypasses the ACL entirely at a single buggy site.
- Silently deny → looks like a permission problem when it's a code
  problem; the operator wastes time tweaking the policy.

The panic surfaces the bug at the call site, where it can be fixed,
on the first request that hits it. The dataentry call sites are
exhaustively tested for Subject population so this never reaches
production traffic.

## SSE event stream: per-type gating + audit isolation

The data-entry SSE event stream (`/api/events`, `/api/v1/_events`) is a
**cache-invalidation signal**, not an event log: it tells a connected
browser "entities of type T changed, re-fetch your active views of T."
The re-fetch goes through the already-gated REST endpoints, so the feed
itself needs to carry almost nothing.

**Per-type ACL gating (TKT-POT9GQ).** Each entity create/update/delete
collapses to a single `entity:changed` frame carrying the entity
**type only** — no id. `handleSSE` gates the type per-connection with
`readGate.ReadQuery(type)`: a connection receives the frame only if its
principal's verdict for that type is not `DenyAll` (AllowAll or a
relation-scoped Query both deliver). The type verdict is resolved once
per connection and cached. A relation write (member-of / role-relation /
containment edge — the types that can change a principal's read scope)
re-derives a **fresh** read gate for the connection: a new `acl.Request`
whose member-of closure is walked against the current graph, so a
principal who gains or loses a group membership mid-connection starts or
stops receiving a type's nudges without reconnecting. (The connection's
original request memoizes its membership closure for its lifetime; the
re-derive is what keeps the verdict honest — without it, only the
per-entity inbound query would refresh, not the principal's own group
membership.) Both the re-derive and the nudges are coalesced into one
flush window, so a bulk relation import triggers one membership re-walk
per connection, not one per edge.

Why per-type rather than per-id: the wire never carries an entity id,
so the feed cannot be a per-entity existence oracle for entities a
principal cannot read. The only residual is **per-type activity
timing** for types the principal can already read — which they could
equally infer by polling the gated list endpoint's count. A
fully-denied principal receives nothing for that type. (The richer
per-id / opaque-cache-id and snapshot-versioned-ACL designs were
considered and rejected as over-engineered for a staleness signal —
see the TKT-POT9GQ design record and IDEA-CQMKMD.)

**Audit isolation.** The stream deliberately does NOT carry audit
records, principal identity, or attribution chains. A denied write
produces a `denied-write` audit row server-side (with full attribution)
and **zero** events on the SSE wire. This matters because SSE is a
fan-out channel — putting audit attribution on it would leak the
principal-to-entity topology to anyone connected. With per-type gating
the feed now carries even less (just a type a connection may read).

A regression test in `internal/dataentry/sse_audit_isolation_test.go`
pins the audit invariant; `internal/dataentry/sse_acl_test.go` pins the
per-type gating. Future work that adds new SSE event types must
preserve both; a new audit-aware channel needs a separate broker with
its own per-subscriber gating.

## Deny response shape

A 403 from a denied write carries `rule_kind`, `rule_id`, and a
human-readable `reason`. It does NOT carry the principal's
attribution chain. This is by design:

- The wire body is what the *requester* sees. Telling them which
  groups they're in and via what edges would reveal organisational
  topology to a possibly-attacking client.
- The audit log is what an *operator* sees. It has the full chain.

If you're debugging "why was alice denied X?" — read the audit log
on the server, not the response body in the browser.

## Read-path gating

Read-side enforcement landed in two PRs with deliberately different
deny models:

- **Per-entity responses** (TKT-VQGN): deny is shaped exactly like
  not-found — a 404 indistinguishable from a nonexistent id.
- **Aggregates** (TKT-VMD8): lists and pagination
  metadata are shaped exactly like "the hidden entities don't exist" —
  filtered sets, filtered totals, no cardinality residue.

### Per-entity responses (TKT-VQGN)

ACL v1 gates every per-entity-response code path:

| Surface | Hidden-target behaviour |
|---|---|
| `GET /api/v1/<type>/<id>` | 404 with the same `not_found` body as a nonexistent id; no `ETag` header, no `If-None-Match` honoured |
| `PATCH/DELETE/POST` on `/api/v1/<type>/<id>` | 404 with the same `not_found` body, BEFORE body parse / `If-Match` / `IsLocked`; a malformed PATCH body on a hidden id returns 404, not 400 |
| `?include=*` and `?include=<type>` neighbours on any GET | hidden neighbours silently omitted from the `included` map; one batched `store.GraphQuery` per neighbour-type (not per neighbour) |

The deny shape is "indistinguishable from not-found." An attacker who
can probe URLs sees only 404 for every hidden entity, regardless of
verb. The 403 path is reserved for **visible-but-write-denied** — a
principal who can read the type but not write to that specific
record. That 403 still carries `rule_kind` / `rule_id` for
operator debugging.

### Invariants downstream maintainers MUST preserve

- **No `ETag` on deny.** Suppressing it is what closes the
  cross-principal cache poisoning surface. A future change that
  emits an `ETag` on the 404 path turns a denied principal's
  `If-None-Match: <alice-etag>` into a 304 — confirming existence.
- **All conditional-request headers short-circuit on deny.**
  `If-None-Match`, `If-Modified-Since`, `If-Match`,
  `If-Unmodified-Since`, `If-Range` MUST be consulted only AFTER
  the visibility probe passes. Today's handler emits only `ETag`,
  but the next maintainer to add `Last-Modified` needs to land the
  deny-side suppression in the same change.
- **The method dispatcher consults URL shape only.** Routing
  `GET/PATCH/DELETE/OPTIONS` for a path MUST NOT consult entity
  existence — the per-method handler is the gate. Otherwise an
  OPTIONS response shape becomes an existence oracle.
- **`?include=` uses the consumer-side `readGate`, batched per
  neighbour type.** A hub entity with 50 neighbours triggers ≤
  `O(distinct-types)` `GraphCount` calls, not 50. Future include
  surfaces (e.g. nested includes in a list response) MUST go through
  the same gate.

### Lists and pagination (TKT-VMD8)

Anything that enumerates entities of a type returns only the visible
subset, with no leak surface revealing hidden cardinality. The list
pipeline (`scopedSortedEntities`, shared by `GET /api/v1/<plural>` and
`/api/v1/_position`) resolves the read scope **first**:

- **AllowAll** — a global role grants read on the type; the pre-ACL
  load path runs unchanged.
- **DenyAll** — no role grants any read; the pipeline returns empty
  **before** the search backend, structured filters, or sort run. A
  denied principal cannot probe backend latency (or induce index
  load) through `?q=`.
- **Query** — read is conferred via role-relations; a composed
  `store.GraphQuery` selects the visible subset, and search / filter /
  sort operate on that filtered slice only.

Every pagination surface derives from the post-filter total:
`data.length`, `meta.total`, `meta.has_more`, `X-Total-Count`,
`X-Page`, `X-Per-Page`, and the `Link` header rels — `rel="next"` is
absent when no *visible* next page exists, even when hidden pages
exist after it.

### Invariants downstream maintainers MUST preserve (aggregates)

- **The DenyAll short-circuit precedes the search backend.** A
  regression test pins the searcher at zero calls on the deny path.
  New work in the list pipeline must keep the scope resolution first.
- **Search runs after ACL, on the filtered slice.** This ordering is
  the contract the `/_search` gate (TKT-BA8BSX, below) generalized to
  mixed-type search; a mock-asserted test pins
  GraphQuery-before-search on the list pipeline.
- **No count from an unfiltered source.** Any new aggregate (badge,
  dashboard card, export count) must derive from the gated set, never
  from `Store.CountEntities` on a principal-reachable path.
- **Update/delete grants imply read grants; create does not.** The policy
  loader rejects a role with `update: [x]` (or `delete: [x]`) but no covering
  `read` entry at boot (structured error naming the role and type) — you must
  read a type to modify or remove it. **Create is exempt** (TKT-4LQMWP): a role
  may `create: [x]` with no read of `x`, reading back only what it authored via
  a role-conferring relation (e.g. `created-by`). This lets a "submitter" create
  a type yet see only its own entities of that type, with the normal Create
  button still shown (the affordance derives from the `create` grant). The
  invariant covers the `update:`/`delete:` lists; the affordance grant maps
  (`fields:` / `options:` / `relations:`) restrict surfaces within an authorized
  write and never confer writability by themselves, so they are intentionally
  outside the check.

### Global search (`/_search`, TKT-BA8BSX)

The search view runs through `search.VisibleSearcher` — a seam that
executes a free-text query restricted to a per-type visibility scope.
The dataentry layer resolves the principal's `ReadQuery` verdict for
every metamodel type into a scope map; the searcher guarantees no hit
outside that scope is ever yielded. The conformance suite
(`storetest.RunVisibleSearchTests`) pins the contract for every
implementation: any new searcher must pass it.

Key properties:

- **Scope lookup is fail-closed.** Exact type entry → reserved `"*"`
  wildcard entry → deny. With no ACL configured the gate supplies
  `{"*": allow-all}`, so entities whose type is absent from the
  metamodel stay searchable exactly as before ACL existed. Under a
  policy, no wildcard is emitted — an off-metamodel type (removed
  from `schema.yaml` while its files remain) is hidden from
  search rather than leaked.
- **The result limit applies after visibility.** `/_search` returns
  up to 1000 results; the bound counts *visible* hits. A
  pre-visibility cap would starve restricted principals — the top
  candidates may all be hidden while their own matches rank below.
  Both gate placement and limit placement are pinned by conformance
  cases on every backend.
- **Two implementations, one contract.** The fs/memory builds wrap
  the regular searcher in a generic filter (`search.NewVisible`,
  batched `MatchingIDs` probes — in-process, cheap). The postgres
  build composes visibility into the search SQL itself
  (`pgstore.SearchVisible`): hidden rows never leave the database,
  the `LIMIT` is post-visibility, and there is no hidden-ROW work to
  measure through timing.
- **That last point is about rows, not fields.** Entity-level
  filtering is pushed into the query, so a hidden row costs nothing
  observable. Property-level `visible:` redaction is different: it
  runs in Go over rows that were already fetched, so a response
  carrying redacted fields does marginally more work than one that
  does not. Redaction is **not constant-time**, and that is a
  deliberate trade rather than an oversight.

  Pushing it into the store would mean expressing per-principal
  `visible:` grants — including ones conditional on graph predicates
  like `has_relation` — as a query, reimplemented for every backend.
  That is a permanent, backend-multiplied correctness burden on the
  most security-sensitive code in the tree, bought against a signal
  measured in the microseconds a redaction loop takes over an
  already-loaded row, by an attacker who must already hold a valid
  principal and already know which field to probe.

  Worth stating plainly: this residual signal exists *because* rela
  does field-level redaction at all. Most applications have no
  central point where entity access is decided, and no field-level
  visibility to speak of — a "hidden" field is one the template
  happens not to render. Here the enforcement is structural
  (`internal/dataentry/visiblereader.go` holds the store privately
  and exposes only gated reads, precisely so gating cannot be
  forgotten), and the timing edge is what remains after the hard
  part is solved.
- **Candidate-window caveat (bleve only).** The bleve backend caps
  candidate retrieval at 10000 hits; on the default build, "true
  top-1000 of the visible corpus" holds within that window. The
  linear and postgres backends have no such window. Related load
  note: a free-text query that also carries property filters defers
  all truncation until after the filters, so the generic path may
  load up to the candidate window's worth of entity bodies for one
  request — in-process on the local backends, but worth knowing
  when diagnosing search latency on very large projects.
- **Deny short-circuit.** An all-denied principal gets `data: []`
  without the search backend being invoked at all (no latency
  probe); a recording-searcher test pins zero calls — and exactly
  one call for a granted search.
- **Visible hits don't expose hidden neighbors.** Search results
  serialize without relation maps (`includeRelations=false`, pinned
  by test): a visible hit that relates to a hidden entity exposes no
  hidden ID or title through any field of its body.
- **Errors carry constant detail.** A visibility-evaluation failure
  maps to `500 acl_query_failed` / `504` / silent-on-cancel exactly
  like the list pipeline; a plain backend failure maps to
  `500 search_failed`. Raw backend error strings go to the server
  log only. (Previously these errors were silently swallowed into
  empty results.)

### Caching: per-principal responses

Under ACL, `/api/` responses differ per principal. Two layers keep
caches from leaking one principal's view to another:

- All `/api/` responses already carry
  `Cache-Control: no-cache, no-store, must-revalidate`.
- When `--principal-header` is configured, responses also carry
  `Vary: <that header>` — defense in depth for any cache layer that
  ignores `no-store`.

### Query accounting is a Debug-only diagnostic

With `-verbose`, every API response carries a `Server-Timing` header with the
number of SQL statements the request issued and their summed database time,
and the log gets one `request` record per request (see
`docs/postgres-backend.md`, "Observing query cost"). Below Debug neither
exists. The gate is a security property, not a convenience: on a path that
still resolves neighbors one by one, the statement count varies with rows the
principal cannot see, so a machine-readable count on every response would be
an existence channel of exactly the kind the row-level rule above forbids.
Wall time is already observable by any client and is accepted as coarse
timing exposure; the statement count is not, and stays an operator tool.
Do not enable `-verbose` on a multi-principal deployment to "get metrics" —
put the numbers in the log, not on the wire.

### Sidebar menu structure is principal-independent

The sidebar's *structure* (groups, labels, links) reveals metamodel
shape, not data shape. It is served identically to every principal
**except** for entries an operator explicitly marks with a
`permission:` (see GUIDE-data-entry, "Hiding entries a user cannot act
on"). The sidebar carries no per-principal data of its own.

That exception is a **UX convenience and is not a confidentiality
control** — the distinction matters, because the reasoning that once
argued against per-principal menus still holds:

- The metamodel is not a secret; it is served by `/api/v1/_schema`.
- Neither is the navigation config: `/api/v1/_config` serves the whole
  tree, `permission:` values included, to every principal.
- A hidden entry's target enforces exactly what it enforced before. Type
  the URL and you reach it; a list still returns its ACL-scoped rows,
  which for a principal permitted to read none of them is an empty list.

So `permission:` on a nav entry buys tidiness, not protection. Never
reach for it in place of a read grant, and never assume an entry's
absence means a principal cannot get at what it points to.

**Entity lists in the sidebar keep this property** (TKT-PEKL8L). A
navigation `entities:` item lists entities as links, but the sidebar
payload carries only its definition: the type, the query scope and the
sort. The browser fetches the rows from the ordinary list endpoint,
which applies the read ACL, the world and faces and the scope, exactly as
it does for a list page. So two principals get the same sidebar and
different links, and a principal who may read none of the entities gets
no links. The one channel this adds is the one a list page already has:
whether the item shows any links at all.

**Dashboard cards work the same way** (TKT-53KICM). A `permission:` on a
`dashboard.cards[]` entry omits that card from `/api/v1/_dashboard` for
non-holders, on exactly the reasoning above: the card's query already runs
through the ACL-scoped search path, so a principal who may read none of the
matching entities already sees a card reading `0`. Hiding it removes a useless
tile and nothing more — the query remains runnable, returning precisely the
rows it always did.

The card list needs its own endpoint because `/api/v1/_config` is
principal-independent by design and must stay that way: it keeps serving the
whole `dashboard:` block, `permission:` values included, to everyone.
`/api/v1/_dashboard` is the per-principal view; `_config` is the config. Do not
collapse them.

The same holds for the rest of the app's configuration.
`/api/v1/_config` serves lists, views, kanbans, documents and
navigation in full — including a document's `permission:` value. Your
`data-entry.yaml` is an operator-authored file that lives in your
repo, so its keys, script paths and permission names are already
disclosed; withholding them from the API would conceal nothing. A
capability a principal lacks is refused at the endpoint with a **403
that names it**, not hidden behind a 404 pretending it does not
exist — an operator reading that error can act on it.

Entity ids are the opposite case, and the distinction is the whole
point: there the uniform 404 is required, because whether an entity
exists is a genuine secret.

Note this section rejects per-principal menu hiding as a *security*
measure. Hiding entries a user cannot act on is still wanted as a
**UX** improvement and is tracked separately; if that lands, the
filter is an affordance — the endpoints keep enforcing independently,
and a hidden entry stays reachable by direct URL.

## ACL fail-loud and middleware scope

The `attachACLRequest` middleware:

- **Wraps `/api/` paths only.** The SPA shell at `/` and static
  assets at `/static/` `/assets/` bypass it. A misconfigured
  principal stamper that throws `ErrUnstampedPrincipal` returns
  500 on `/api/v1/...` but lets the UI keep rendering. Otherwise
  operators would be locked out of the very surface they need to
  diagnose the misconfig from.
- **Fails loud inside `/api/`.** When ACL is configured and the
  principal stamper produces an unstamped principal, the middleware
  returns 500 with `acl_unstamped_principal` rather than silently
  proceeding with no `acl.Request` attached. Silent fall-through
  was a fail-open path — every read became AllowAll because the
  read handlers couldn't tell "no ACL" from "ACL but no
  principal."
- **Refuses script reads when the read gate cannot be built.** If an
  `acl.yaml` is configured but the visibility decorator fails to
  construct, script runtimes (data-entry actions, `export_render`,
  and the IdP webhook) receive a refusing reader rather than the raw
  store: every read returns `read gate unavailable; refusing to read
  ungated`, logged at ERROR at the wiring site. Degrading to ungated
  reads would be an unbounded silent disclosure on the unattended
  paths, where nobody is watching a warning. Note the asymmetry with
  traversals: `trace_from`, `trace_to` and `find_path` cannot report
  an error, so a refused traversal looks like an empty result to the
  script — correlate with the ERROR log by timestamp when a script
  reports an unexpectedly empty graph.

A project with **no** `acl.yaml` is unaffected: that is a deliberate
"no access control" configuration, not a fault, and scripts there read
the full graph exactly as they always have.

**When an automation legitimately needs more than its caller can see**,
the sanctioned route is `rela.bypass_acl` — not widening the policy. Its
`admin` handle carries `get_entity`, `list_entities` and `get_relations`
alongside the write methods, all reading raw. That keeps the privilege
scoped to a closure and visible at the call site, and it leaves an
`acl-bypass-read` audit row, where relaxing a role in `acl.yaml` would
widen access for every read on every path with nothing in the log to
show for it. It requires operator opt-in (`allow_acl_bypass:` on the
action, valued `read`, `write` or `read+write`) — see the Lua scripting
guide.

### An elevated document is trusted code

A `documents:` entry may also declare `allow_acl_bypass: read`, so its
render can compute over rows the reader cannot see — a benchmark against
peers whose records are invisible to them. No `acl.yaml` role expresses
that: granting enough to *compute* the benchmark grants enough to
*enumerate* the subjects.

Understand what that gate means, because it is not what it looks like:

> `permission:` on an elevated document grants **"may read whatever this
> script reads"** — not "may view this report".

Once the render elevates, the ACL no longer bounds its **output**; only
the script's own discipline does. A script that prints the rows it read,
rather than a statistic derived from them, has published them to every
holder of that permission. Nothing in the system prevents this, and
nothing can: the aggregation *is* the confidentiality boundary.

So the mitigation is procedural, and it must actually happen:

- **Review the `bypass_acl` block before deploying.** That review is the
  control. The `allow_acl_bypass:` key exists in config precisely so a
  reviewer sees which scripts need it.
- **Treat the permission as equivalent to the read grant** the script
  performs, when deciding who holds it.
- **Elevated reads are audited** (`acl-bypass-read`), so use is
  answerable after the fact — but the row records *that* raw reads
  happened, not what reached the page.

An elevated document additionally requires a configured `acl.yaml`: under
no policy the permission names a capability nothing can withhold, so the
render is refused rather than served to everyone. Writes are refused
outright — a render is a `GET`.

## Property-level redaction (`visible:`)

Entity-level filtering decides whether you see an entity at all. The
`visible:` grant is the orthogonal, finer cut: a field denied by
`visible:` is **omitted from the response `properties` map** even on an
entity you are allowed to read. This redaction is applied by the
data-entry serializer on **every** HTTP read shape — per-entity GET,
list rows, `?include=` peers, and `/_search` results — not just the
write form. When the hidden field is the entity's display property, the
`_title` falls back to the entity ID so the redacted value cannot leak
through the title. See [GUIDE-acl-overview] for how `visible:` grants
are written.

**Search cannot be a hidden-field oracle.** Redacting the response body
is not enough on its own: the full-text index still contains a hidden
property's text, so a query that matched *only* that property would
otherwise return the entity as a hit and confirm the value by its mere
presence (search a candidate postcode against a hidden address field →
guess oracle). `/_search` closes this at the `VisibleSearcher` seam: each
backend reports which fields a query matched (bleve with its own analyzer,
Postgres by re-matching the candidate rows per field, the linear backend
by substring), and a hit is **dropped** when every field it matched is one
the principal may not see. A hit that also matched a *visible* field — or
the id or content, which are never property-gated — still surfaces, with
the hidden property redacted from its body. The property-level conformance
suite (`storetest.RunVisibleFieldSearchTests`) pins this across all
backends.

**Relation meta is redacted the same way.** A relation carries its own
property map (`meta`), and a role's `relations:` grant can add a `visible:`
block beside its write-side `fields:` block to redact those per-field:

```yaml
roles:
  owner:
    relations:
      ticket:
        - relation: employed-by
          visible:
            - field: salary          # only `salary` visible; every other
                                      # meta key on the edge is closed-world hidden
            - field: approval_note
              when: "has_role('hr')"  # conditional, same predicate vocabulary
```

The block is keyed on the relation's **source** entity type (the `from`
side owns the grant), and redaction runs on every browser-reachable relation
read shape — the `/relations` map, the single-relation-type GET, and both the
outgoing and incoming direction (an incoming edge resolves its grant against
the true source, not the entity being viewed). It also runs on relation
history (see below). The machine-to-machine sync channel (`/api/sync/`) applies
the **same** redaction by reading through `/api/v1` rather than a private channel
(TKT-8P1TM7) — see "Sync is a client of the authorized API" below. The deny universe is the edge's actual
meta keys, so a free-form key never declared in the metamodel is redacted
too — a caller cannot smuggle a secret past the closed-world by using an
undeclared property name. Relations have no display-title channel, so there
is no `_title` fallback to worry about; the redaction is a straight
value-omission from `meta`. A relation type with no `visible:` block emits
its meta unchanged (permissive default).

## `related(...)` in a grant's `when:`

A grant's `when:` may use `related(...)` to depend on the entities at the
other end of a relation:

```yaml
roles:
  reviewer:
    fields:
      ticket:
        - field: status
          when: "related(entity, 'implements', { status = 'open' })"
```

The traversal reads the graph with system trust, not with the principal's
read gate. Its verdict is therefore the same for every principal, but it
reveals one bit to a principal who cannot read the related entities: whether
the grant applies. Keep such conditions to facts you are willing to disclose
that way. A traversal that compares a property some role cannot see through
`visible:` earns a startup warning naming the grant.

A state-machine transition's `when:` works the same way. Whether a transition
is performable is served to principals in `_transitions`, so it carries the
same bit and earns the same warning.

A list page or search answers each traversal once for all its rows, not once
per row.

The grant does not apply, rather than guessing, when the traversal cannot be
answered:

- the store query fails;
- the entity is on a named face, because the store walks the default face's
  edges;
- the entity is a historical version, such as one shown in the history view.

A relation or property the path names that does not exist is a startup error.
See [Where `related(...)` works](metamodel.md#where-related-works) for the
other surfaces and how each counts hidden entities.

## What still leaks (deferred)

- **`/api/v1/_position` per-id semantics** — `_position` is gated on
  both scope sources: list scopes share the gated list pipeline, and
  search scopes filter the search result through the read gate before
  computing ordinals, so totals and prev/next always come from the
  principal's visible subset. What remains scoped to the follow-up
  ticket: the per-id gate on the *requested* id and the
  neighbor-disclosure analysis (a visible neighbor's id confirms a
  visible entity, but gap analysis around hidden entities needs its
  own treatment).
- **Local stdio MCP (`rela mcp`)** applies no ACL. It has no
  authentication, so there is no principal to gate for; it runs with the
  operator's own file access, like the CLI. The **remote** MCP endpoint
  (`rela-server -mcp`) is JWT-authenticated and gated (TKT-4QSZ8Y): entity
  and relation reads, `search_entities`, trace, resources and the Lua tools
  all read through `GatedReads`. Search drops hits that matched only hidden
  properties, and the Lua tools get no `rela.bypass_acl`. These residuals
  remain on the remote endpoint:
  - relation meta values are not field-redacted (TKT-0RBFN0), the same as
    on every other read path;
  - a create or rename that picks a new id which a hidden entity already
    holds fails with a conflict, which confirms that id exists. The
    data-entry write path has the same property;
  - `delete_entity` without `cascade` on an entity whose only edges lead
    to hidden entities fails with "entity has relations". That shows some
    hidden edge exists, but not how many or to what. The counts that
    `delete_entity` and `rename_entity` report cover visible edges only;
  - `analyze_cardinality` and `analyze_orphans` use structural relation
    counts, the same rule the data-entry analyze view follows.
- **Markdown body (`content`) is not field-redacted, on any read path.**
  `visible:` is a **property-values** guard: it omits hidden *property* and
  *relation-meta* values from the wire. It makes no claim over the markdown
  **body** — there are no body-level guards anywhere in rela (the web read
  path, the sync fetch, and history all serve the full body). A deployment
  that duplicates a hidden property's value into the body (e.g. a rendered
  "summary" line) discloses it to any principal who may row-read the entity.
  Covering the body would be a new cross-path mechanism; it is deliberately
  out of scope (RR-ATFNM1).

For threat-modelling purposes today: per-entity GET, write, include,
list, pagination, global-search, the SSE event stream, and the
machine-to-machine sync channel are all read-gated (the SSE feed per-type,
see above); `visible:` property/meta redaction applies to every data-entry
HTTP read body — and **sync inherits it by reading through `/api/v1`**
(TKT-8P1TM7); and `/_search`
cannot be used as a hidden-field oracle. The remaining read-side gaps are
relation meta (TKT-0RBFN0) and the markdown body (never field-redacted,
by design — see above); within the data-entry server every property/meta
read channel a browser or a replica can reach is tight.

### Sync is a client of the authorized API (a "fancy browser")

The machine-to-machine sync channel (`/api/sync/`, FEAT-NJ9FEN) used to be a
**second content channel** into the store — its own record GET/PUT/DELETE with
its own row-level gate, bypassing `visible:` field redaction on reads and the
field-write ACL on writes. TKT-8P1TM7 retired that channel: sync now reads AND
writes through the **same authorized `/api/v1` API** the SPA uses, so there is
one content channel with one authorization decision. A replica is a client with
no authority of its own — the remote is always the authoritative primary.

- The **feed** (`/api/sync/manifest`) is the only sync-specific surface that
  remains — content-free and row-gated. It lists `{id, type, op, deleted}` per
  changed row filtered by the same read verdict, carrying no property or meta
  values. A row a principal cannot read is omitted (the cursor still advances
  past it, so the client never re-polls a hidden tail forever). A row the
  principal can read but is field-redacted on **still appears** — the feed has
  no field-level decision because it carries no fields. It stays because a plain
  GET cannot express a tombstone (absence-from-a-list is not an explicit delete).
- **Reads** go through `/api/v1` GET, inheriting row-gating AND `visible:`
  redaction — the field-redaction gap closes as a consequence of there being one
  content channel, not a bolted-on step. A single-relation `/api/v1` read
  (RR-SYNCR1) serves the relation body + a relation-level ETag, field-meta
  redacted and **fail-closed** (empty meta if the source is gone), mirroring
  relation history.
- **Reads that feed a local write are safe** because the `/api/v1` response
  carries `_redacted` — the *names* (never values) of the properties withheld by
  field ACL (DEC-T0XIWQ). A replica applying a pulled change patches only the
  fields it can see: a name present in `_redacted` is **hidden, not deleted**, so
  the replica leaves its own local copy untouched; a field in neither
  `properties` nor `_redacted` is a genuine delete. A redacted read therefore
  drives a faithful replica without ever erasing hidden values the replica isn't
  entitled to.
- **Writes** go through the `/api/v1` write path, so a push is enforced by the
  same field-write ACL (`validateFieldWrite`) as a human edit, and a redacted
  replica pushes a PATCH of visible fields only — it never names (and so never
  erases) the primary's hidden fields. Ids are the primary's to mint: a
  locally-created record is pushed under a temporary id, the primary returns the
  real id, and the replica renames its local doc to match.
- **Conflict detection** uses the primary's ETag as an opaque `If-Match` token —
  the replica stores what the primary returned and echoes it back, comparing only
  for equality to detect "the primary moved." A conflict halts the record and is
  surfaced to the operator to resolve (`--force`), not auto-reconciled.

Two residual notes for a deployment. (1) The markdown **body** is not
field-redacted on any path (see "What still leaks" above), so sync replicates
full bodies — the same as a web GET. (2) A principal who **loses** row access
keeps whatever it already replicated (the row simply stops appearing and its GET
404s — it cannot refresh, but the last copy is not recalled; "you could have made
a copy"). A manifest-listed id that 404s on fetch (access lost between feed and
fetch) means *skip and advance* — the replica must NOT mirror a delete from a
bare 404, only from an explicit feed tombstone (RR-SYNCR3). Scope: this is the
CLI replica↔remote mode; a server↔server mode is deliberately deferred (see the
ticket).

## Version history read gating (`history:read`)

The PostgreSQL build's [version history](postgres-backend.md#version-history-time-machine)
is read-gated on the same principles as everything else:

- **Live-entity history** (`GET /api/v1/_history/<type>/<id>` and a specific
  version) is gated by the *same* per-entity read verdict as a plain GET of the
  entity. A principal who cannot read the entity gets a 404 indistinguishable
  from a nonexistent id — history is never a side channel for reading a hidden
  entity's content. Every returned snapshot is run through the same serializer
  redaction as a live read, so `visible:`-denied properties never appear in a
  historical version either.
- **Deleted-entity history** has no live entity to evaluate a per-entity verdict
  against (the conferring relations are gone), so it is gated on a global named
  permission, **`history:read`**. Grant it via a role's `permissions:` list,
  exactly like the delegate-X permissions:

  ```yaml
  roles:
    auditor:
      permissions: [history:read]
  ```

  Treat `history:read` as an **audit-everything-deleted super-permission**: it
  is global (not per-entity), so a holder can read the surviving history of
  *any* deleted entity of any type — including entities they never had live
  access to. Grant it only to trusted audit/compliance roles. A **non-holder**
  requesting a deleted entity's history gets the same 404 as a nonexistent id,
  so the permission boundary does not itself leak which deleted entities exist.

### Historical field redaction fails closed (`history:read-redacted`)

Field-level `visible:` redaction of a *historical* snapshot **fails closed**.
A `visible:` grant can be conditional on the subject entity's own graph state —
`visible: has_relation(entity, 'reviewed-by')` or
`count_relations(entity, 'blocks') > 0`. Evaluating such a grant needs the
entity's edges *as they were when the version was captured*, but the live store
only holds its edges *now*: for a deleted entity there are none, and for a live
entity they may have drifted. Trusting the live store would let a grant that was
DENIED at write time flip OPEN and leak a field that was hidden when the version
was written.

So historical redaction does **not** trust the live store for subject-world
state. Two things are neutered when serializing a snapshot: `has_relation` /
`count_relations` resolve as *no edges*, and the effective role set is reduced to
**globals-only** — local roles conferred by live `role_relations` edges and
ancestor roles inherited via `inherit_roles_through` are dropped, because they
too come from the live graph and would otherwise let a role conferred *after*
capture reveal a field. To keep that reduction from silently defaulting to
all-visible, a type that any role gates with `visible:` gets a **type-level
closed-world** in history: every field not affirmatively granted visible by a
globally-held role is hidden. The rule, stated as an invariant:

> A historical version shows a field only if today's `visible:` policy
> affirmatively grants it against inputs that can be safely evaluated. Every
> grant that would need untrusted subject-world state is hidden. Reader-side
> inputs (`current_user`, the roles the reader holds) stay live — so redaction
> is still per-reader, and a reader who gains a role gains historical
> visibility.

This is deliberately conservative: it can **over-redact** (hide a field a reader
could legitimately have seen) but never **under-redact** (leak). A grant that
references a property the frozen record no longer carries (a since-renamed
field) likewise binds Nil and hides the field — a drifted schema over-redacts,
never leaks.

To see the fully un-redacted frozen snapshot, a role must hold the global
**`history:read-redacted`** permission — the field-grained sibling of
`history:read`:

```yaml
roles:
  auditor:
    permissions: [history:read, history:read-redacted]
```

Its semantics are **OVERRIDE**, all-or-nothing like `history:read`: a holder
sees *every* frozen field of a historical snapshot, bypassing field redaction
entirely (the snapshot already contains every field; the reveal just skips the
strip). Grant it only to trusted audit/compliance roles — it exposes fields the
live `visible:` policy would redact. A non-holder always sees the fail-closed
redaction described above.

**Every reveal is audited** (`history-reveal`) *under a configured policy*, so
"who saw which hidden historical values, and when" is answerable after the fact.
With no `acl.yaml` there is nothing to reveal — no field is redacted — so those
reads are not recorded; otherwise every history read in an unconfigured
deployment would log a reveal that revealed nothing. The row names the entity
and the version read, and the principal who read it — it does *not* record the
revealed values, nor which fields were revealed (that list is itself a map of
what the policy hides). Ordinary redacted history reads are **not** recorded:
they disclose nothing this permission governs, and logging them would bury the
privileged reads the row exists to surface. Isolate reveals with `op ==
"history-reveal"`.

**Relation history is governed by the current live world — deliberately unlike
entity history.** Where *entity* history reconstructs the moment of capture and
adds a `history:read-redacted` audit reveal, *relation* history takes a simpler,
stricter stance: a relation's meta is redacted against **today's** `visible:`
policy evaluated against the **live source entity**, and the version's frozen
values are never served on their own terms.

- **Source entity live** → redact per-field against the current policy and the
  live source, exactly as a live relation GET. Reader-side access is live in both
  directions: lose the role that granted a field today and you lose it in history
  too; gain the role and you gain it. (This is the point — being removed from a
  team takes effect on history, not just on live reads. A copy the reader already
  made is a separate matter; rela simply stops *serving* it.)
- **Source entity deleted** → **no meta is served, to anyone.** The thing that
  grants access to a relation's properties is the live relation; once its source
  is gone there is nothing to evaluate current ACL against (and the version row
  stores the source *id*, not its *type*, so the type is unrecoverable — the
  caller-supplied URL `{fromType}` is never trusted, or a principal could spoof a
  type their own role has a favorable grant for). There is deliberately **no
  `history:read-redacted` reveal** for a deleted relation: gone is gone. The
  timeline/attribution metadata (version, op, who, when) still serves — only the
  ACL-governed property values follow this rule.

(The version rows themselves are *retained* — history is append-only, and a
deleted source's relation-version rows persist until an operator runs the audited
`relation-history-purge`. "Not served" is an access guarantee, not an erasure
one; erasure is the separate operator tool. Relation restore reads the raw frozen
meta, never the redacted view — a redacted read-modify-write would erase the
caller's hidden meta on save.)

## Command execution gating (`command:*`)

Data-entry [commands](data-entry.md#commands) execute arbitrary shell via
`sh -c`. They have no entity subject to evaluate — a `context: global` command
acts on the project, not a row — so like `history:read` they are gated on
**global named permissions**, declared per command and granted via a role's
`permissions:` list:

```yaml
# data-entry.yaml
commands:
  nightly-export:
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

The permission name is arbitrary; `command:<id>` is a readable convention, not
a requirement.

**Authorization depends on the policy AND the bind, by design:**

- **`acl.yaml` present** → a command runs only if its `permission:` is set and
  the principal holds it. A command with no `permission:` is **denied**: under a
  configured policy, an ungoverned shell-exec surface is treated as a
  misconfiguration, not as an implicit grant. The bind is irrelevant here.
- **No `acl.yaml`, loopback bind** → every command runs. A local project that
  has not opted into access control behaves exactly as it did before this gating
  existed — the server host is the user's own machine.
- **No `acl.yaml`, non-loopback bind** → command execution is **denied by
  default**, because any client reaching the server could otherwise run an
  arbitrary shell script unauthenticated. The operator restores it either by
  adding an `acl.yaml` (and a `permission:` per command) or by opting in
  explicitly with `--allow-unauthenticated-commands`
  (`RELA_ALLOW_UNAUTHENTICATED_COMMANDS=1`) — for a single-user deployment
  isolated at another layer only. See
  [server-security.md](server-security.md#configured-commands-are-remote-code-execution-by-design).
- **`--read-only`** → every command is denied, whatever the policy or bind says.
  Command execution is not an `acl.WriteRequest`, so `ReadOnlyACL` cannot deny
  it through `AuthorizeWrite`; the wiring site installs a deny authorizer for
  read-only directly. Without that, `--read-only` would not stop a command from
  mutating the project.

The choice among these is made **once at startup** from `(acl.yaml, bind,
--allow-unauthenticated-commands)`, not per request — the bind address is a
deploy-time fact. This is why the read gate alone cannot decide: with no policy,
the per-request read gate answers "permitted" for everything under both the open
and read-only modes, so it cannot distinguish them. The startup decision is the
distinguisher.

Commands the principal cannot execute are filtered out of the command-resolution
API, so their buttons do not render. Treat that as a UI affordance only — the
exec endpoint re-authorizes and 403s independently, and the 403 body never
names the required permission or any policy content.

A running command can be cancelled only by the principal that started it; a
cancel request naming another principal's execution gets the same 404 as an
unknown one, so cancellation cannot be used to probe what else is running.

### What a command permission actually confers

**Command payloads are not read-gate scoped, in any context.** A command's
stdin JSON is assembled directly from the store, without the per-entity
`PermitsRead` verdicts that gate an ordinary API read. Granting
`command:<something>` therefore confers **read access to whatever that
command's context assembles**, not merely the right to run a script:

| context | what the script receives | scoped by |
| ------- | ------------------------ | --------- |
| `entity` | the entity at the caller-supplied `entity_id`, plus **every** incident relation | nothing — any id in the store |
| `list` | every entity in the caller-supplied `list_id`, post-filter | nothing — any configured list |
| `global` | project paths only | n/a |
| `view` | the entry entity plus the entire traversal closure | *not grantable — see below* |

Note the `entity_id` and `list_id` come from the request, not from whatever
page the user was on: `available_on` restricts where a **button appears**, not
what a command may be pointed at. Treat a command grant as "may read every
entity of this shape", and scope grants accordingly.

**`context: view` cannot be granted per-command at all.** View commands are
denied outright under any configured `acl.yaml`; `permission:` on a view
command is inert and warns at config load. The difference from the contexts
above is degree, not kind: a view's traversal closure is unbounded by the
config an operator can read, so the blast radius of the grant is not knowable
in advance. Deferred until the traversal is read-gate scoped.

Restore (`POST /api/v1/_history/<type>/<id>/<version>/restore`) is a **write**,
not a read: it is authorized as an ordinary update (or create, if the entity was
deleted), runs the per-field write gate on exactly the fields that change (so it
cannot set or clear a field the principal lacks write access to), and is audited
and re-versioned like any edit.

Not point-in-time: history read uses the *current* ACL, not the ACL as-of each
version. Reading a live entity's history exposes its **entire** history from
creation — including versions written before the principal gained access, and
content later edited out. If you redact content for compliance, understand that
older versions still hold it; a dedicated version-purge primitive is the
intended follow-up for hard removal.

**Field-visibility caveat for *conditional* `visible:` grants.** A historical
snapshot is field-redacted against the **current** ACL context (the live
relations and roles), not the context as-of the version. For an unconditional
per-type `visible:` grant this is exactly correct. But if a grant is
*conditioned* — on a relation (`visible: has_edge(...)`) or on another property
value — the verdict computed for a snapshot can differ from the entity's verdict
at write time, and for a **deleted** entity (whose conferring relations/roles no
longer resolve) it can under-redact. Until the visibility verdict is frozen at
capture time (a tracked follow-up), a policy that hides fields via *conditional*
grants should not rely on history-read redaction for those fields. Unconditional
per-type `visible:` grants are unaffected.

### Relation history: dual-endpoint gating

Relation version history (the PostgreSQL build versions relation properties and
body too) is read-gated on **both** endpoints: a caller must be able to read the
`from` AND the `to` entity. Gating on the `from` alone would make the `to`
endpoint an existence/content oracle — a principal allowed to read `from` but
denied `to` could enumerate `from`'s outgoing relation histories and learn about
the hidden `to` endpoint. A deleted relation (endpoints gone) uses the same global
`history:read`; a non-holder gets the same 404 as a nonexistent relation.

Relations DO support field-level (`visible:`) redaction (TKT-B1F5Q1) — on the
live relation GET and in history. Relation history exposes exactly what a live
relation read exposes, no more, and its history redaction is governed by the
**current live world** against the **live source** (a deleted source serves no
meta at all). See the property-level-redaction section above; the dual-endpoint
gate bounds *which relations' histories* are reachable, and the live-world rule
bounds *which meta fields* within a reachable one.

## Where to read next

- [GUIDE-acl-overview] — operator's overview of the resolver.
- [CON-authorization] — vocabulary and core concept.
- `docs/server-security.md` — the broader `rela-server` threat model
  (CSRF, DNS rebinding, loopback binding, command allowlist).
