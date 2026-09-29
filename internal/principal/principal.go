// Package principal carries the identity attribution attached to each
// request: who initiated it (User) and through what entry point
// (Tool). Entry-point binaries stamp a [Principal] on the request
// context once at startup (or per-request for HTTP-style entry
// points); downstream consumers — audit logging today, access control
// in subsequent PRs — read it via [From].
//
// This package is deliberately small. It owns the [Principal] type,
// the Tool* constants, the context plumbing, and the OS-user lookup.
// It does NOT carry session lifecycle, role mapping, or
// authentication — those are separate concerns that may grow their
// own packages.
//
// **Growth gated on ACL.** The package was extracted from `audit`
// specifically so a future ACL ticket can import [Principal] without
// pulling audit. If that ticket gets de-prioritized and no second
// consumer materializes, this package should be reabsorbed into the
// consumer that uses it most — currently `audit`. Don't grow this
// package speculatively.
//
// That gate has since opened, once (TKT-RP3X3Q): [Principal] gained the
// verified-assertion claims — orgID, orgSlug, roles — because internal/acl
// needs to grant roles from a signed identity assertion, and the claims have
// nowhere else to live that both the ACL evaluator and the audit log can read.
// The unexported-fields-plus-[Verified] shape exists so the trust boundary is
// enforced by the compiler; see the [Principal] doc.
//
// The gate is not now open generally. `roles` earned its place by having an
// evaluator; `orgID`/`orgSlug` ride along for audit attribution only and are
// the outer limit of what belongs here. Session lifecycle, provisioning, and
// role *expansion* remain out — they are separate concerns with their own
// packages.
//
// It opened a second time (TKT-IAC8TX) on the same terms: `principalType` and
// `scopes` earned their place by having an evaluator — internal/acl compiles
// them into a client-attenuation ceiling. `client_id` was considered and left
// out precisely because nothing evaluates it; it would be attribution-only
// clutter, and the ceiling can key on it later if that changes.
package principal

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"unicode"
)

// Principal identifies who is making a write. User is the OS user
// captured at process startup via [SystemUser]; data-entry overrides it
// per-request from an HTTP middleware. Tool identifies the entry point —
// one of the Tool* constants below.
//
// RawUser records the pre-resolution identifier when the ACL policy's
// `principal_property` lookup substituted User with a graph entity ID
// (e.g. the `X-Forwarded-User` email before it became a `PERS-…` ID). It
// is empty when no substitution happened — the common case — and exists
// so the audit log can record BOTH the identity that authenticated and
// the entity it resolved to without a round-trip to the graph. Old audit
// consumers ignore the `raw_user` key (omitempty).
//
// # Verified assertion claims
//
// orgID, orgSlug, roles, principalType and scopes carry claims from a
// CRYPTOGRAPHICALLY VERIFIED identity assertion. They are unexported on
// purpose: [VerifiedFrom] (and its [Verified] wrapper) is the only way to
// populate them, so no composite literal anywhere in the tree can forge a role.
// That matters because internal/acl trusts a Principal absolutely — it verifies
// nothing itself — so a role reaching it from an unverified source (a spoofable
// header, say) would be a complete authorization bypass. The compiler enforces
// the trust boundary here rather than leaving it to a code reviewer's memory.
//
// Read them via [Principal.OrgID], [Principal.OrgSlug], [Principal.Roles],
// [Principal.PrincipalType], [Principal.Scopes], [Principal.Email].
//
// The two attenuation claims run the OPPOSITE direction from roles: principalType
// selects a ceiling that removes capability, and scopes re-open pieces of it
// bounded by the acting user. So a forged one could only ever narrow — but they
// share the constructor discipline anyway, because "this claim is safe to forge"
// is not a property worth asking a future reader to re-derive.
//
// email carries the verified `email` claim, when the assertion supplies one. It
// rides here for the same reason org does — so lazy provisioning (TKT-ANUJDS)
// can stamp it onto a freshly-created stub user entity — and is populated only
// via the verified constructors, never a composite literal, so it shares the
// org/roles trust boundary. Attribution/enrichment only; nothing in internal/acl
// evaluates it.
type Principal struct {
	User    string
	Tool    string
	RawUser string

	orgID         string
	orgSlug       string
	roles         []string
	principalType string
	scopes        []string
	email         string
}

// Claims carries the verified-assertion fields [VerifiedFrom] stamps onto a
// Principal. It exists so the claim set can grow without re-churning every
// call site the way a widening positional signature would: [Verified] already
// took five arguments, and client attenuation (TKT-IAC8TX) needed two more.
//
// A zero Claims is valid — it produces a Principal with no verified claims,
// which is what every non-assertion entry point wants.
type Claims struct {
	OrgID   string
	OrgSlug string
	Roles   []string

	// PrincipalType selects a client-attenuation baseline in acl.yaml. Note
	// the asymmetry with Roles: a role ADDS capability, a principal type only
	// ever REMOVES it. That is why an unrecognized value is safe to ignore
	// (no baseline matches → unrestricted) while an unrecognized role is
	// dropped — both fail toward the acting user's own grants.
	PrincipalType string

	// Scopes re-open capability a baseline closed, always bounded by what the
	// acting user holds. A scope can therefore never escalate past the user.
	Scopes []string

	// Email is the verified `email` claim, when the assertion supplies one.
	// Attribution/enrichment only (lazy provisioning stamps it on a stub user
	// entity, TKT-ANUJDS); nothing in internal/acl evaluates it.
	Email string
}

// VerifiedFrom constructs a Principal carrying claims from a verified identity
// assertion. Callers MUST have validated the assertion's signature before
// calling this — see the type doc for why that is load-bearing.
//
// Slice claims are defensively copied so a later mutation of the caller's slice
// cannot retroactively change an authorization decision.
func VerifiedFrom(user, tool string, c Claims) Principal {
	p := Principal{
		User:          user,
		Tool:          tool,
		orgID:         c.OrgID,
		orgSlug:       c.OrgSlug,
		principalType: c.PrincipalType,
		email:         c.Email,
	}
	if len(c.Roles) > 0 {
		p.roles = slices.Clone(c.Roles)
	}
	if len(c.Scopes) > 0 {
		p.scopes = slices.Clone(c.Scopes)
	}
	return p
}

// Verified is the original positional constructor, retained because it reads
// well at the many call sites that only ever carry org + roles. It delegates to
// [VerifiedFrom]; prefer that one when setting the attenuation or email claims.
func Verified(user, tool, orgID, orgSlug string, roles []string) Principal {
	return VerifiedFrom(user, tool, Claims{OrgID: orgID, OrgSlug: orgSlug, Roles: roles})
}

// WithEmail returns a copy of p carrying the verified `email` claim. Useful when
// a caller already holds a verified Principal and only needs to add the email
// (the lazy-provisioning re-stamp path, TKT-ANUJDS) rather than rebuild it
// through [VerifiedFrom]. The verified-source guarantee still holds — WithEmail
// is only reachable from a Principal a caller already built via a verified
// constructor.
func (p Principal) WithEmail(email string) Principal {
	out := p.Clone()
	out.email = email
	return out
}

// Email returns the verified `email` claim, or "" when absent. Same
// attribution-only caveat as [Principal.OrgID].
func (p Principal) Email() string { return p.email }

// OrgID returns the verified `org_id` claim, or "" when the principal did not
// arrive with a verified assertion.
//
// ATTRIBUTION ONLY. Nothing in internal/acl evaluates this — a principal in
// org A holding a role sees every entity that role grants, in EVERY org.
// Presence of an org in the audit log does NOT imply tenant isolation.
// Enforcement is deliberately deferred; see TKT-RP3X3Q.
func (p Principal) OrgID() string { return p.orgID }

// OrgSlug returns the verified `org_slug` claim, or "" when absent. The same
// attribution-only caveat as [Principal.OrgID] applies.
func (p Principal) OrgSlug() string { return p.orgSlug }

// Roles returns the verified `roles` claim — bare role names scoped to
// [Principal.OrgID]. Empty for every non-assertion entry point (CLI, MCP,
// scheduler, a proxy-trusted header, ...).
//
// These are the IdP's role names, NOT rela roles: an ACL policy maps them to
// declared roles through an operator-authored allowlist, so a claim value can
// never name a rela role the operator did not choose to grant.
//
// The returned slice is a copy; mutating it cannot affect authorization.
func (p Principal) Roles() []string {
	if len(p.roles) == 0 {
		return nil
	}
	return slices.Clone(p.roles)
}

// PrincipalType returns the verified `principal_type` claim — what KIND of
// caller this is (e.g. "user", "app", "pat", "service"). Empty for every
// non-assertion entry point, and for a proxy that does not model it.
//
// This selects a client-attenuation baseline in acl.yaml (TKT-IAC8TX). Unlike
// [Principal.Roles] it can only ever REMOVE capability, never add it, so an
// unrecognized value is not a security event: no baseline matches and the
// principal keeps exactly its acting user's grants.
func (p Principal) PrincipalType() string { return p.principalType }

// Scopes returns the verified `scope` claim, split on whitespace. Empty for
// every non-assertion entry point.
//
// A scope re-opens capability a client baseline closed, always intersected with
// what the acting user holds — so a scope can never grant past the user. Like
// [Principal.Roles], these are the IdP's names, mapped through an
// operator-authored allowlist in acl.yaml; an unknown scope grants nothing.
//
// The returned slice is a copy; mutating it cannot affect authorization.
func (p Principal) Scopes() []string {
	if len(p.scopes) == 0 {
		return nil
	}
	return slices.Clone(p.scopes)
}

// IsZero reports whether p carries no identity at all. Entry points use it to
// reject an unconfigured Principal at construction time.
//
// This exists because Principal stopped being comparable when it gained a slice
// field, so `p == Principal{}` no longer compiles. Deliberately NOT
// reflect.DeepEqual: a Principal carrying only assertion claims and no
// User/Tool is still an unusable identity and must still be rejected.
func (p Principal) IsZero() bool {
	return p.User == "" && p.Tool == "" && p.RawUser == ""
}

// Clone returns a deep copy of p, safe to hand to code that might outlive or
// mutate the original.
//
// Principal is a value type, so a plain assignment already copies the scalar
// fields — but the roles slice header is shared, and a caller resliced onto
// that backing array could mutate a role another holder is about to authorize
// against. This is the one place the compiler stops enforcing the guarantee the
// unexported fields exist to provide, so accessors that hand out a Principal by
// value use this instead.
func (p Principal) Clone() Principal {
	if len(p.roles) > 0 {
		p.roles = slices.Clone(p.roles)
	}
	if len(p.scopes) > 0 {
		p.scopes = slices.Clone(p.scopes)
	}
	return p
}

// Sanitized returns a copy of p with every string field passed through clean.
// Used at a serialization boundary (the audit JSONL writer) to bound field
// length and strip control characters.
//
// It lives here rather than in the consumer because the assertion claims are
// unexported: a consumer cannot reach them, and a consumer that only cleaned
// the exported fields would silently let unbounded role strings through. clean
// is supplied by the caller so this package stays free of a policy decision
// about what "clean" means.
func (p Principal) Sanitized(clean func(string) string) Principal {
	out := Principal{
		User:          clean(p.User),
		Tool:          clean(p.Tool),
		RawUser:       clean(p.RawUser),
		orgID:         clean(p.orgID),
		orgSlug:       clean(p.orgSlug),
		principalType: clean(p.principalType),
		email:         clean(p.email),
	}
	if len(p.roles) > 0 {
		out.roles = make([]string, 0, len(p.roles))
		for _, r := range p.roles {
			out.roles = append(out.roles, clean(r))
		}
	}
	if len(p.scopes) > 0 {
		out.scopes = make([]string, 0, len(p.scopes))
		for _, s := range p.scopes {
			out.scopes = append(out.scopes, clean(s))
		}
	}
	return out
}

// Equal reports whether p and q carry the same identity, including the
// assertion claims. Replaces `==`, which stopped compiling when Principal
// gained a slice field.
func (p Principal) Equal(q Principal) bool {
	return p.User == q.User &&
		p.Tool == q.Tool &&
		p.RawUser == q.RawUser &&
		p.orgID == q.orgID &&
		p.orgSlug == q.orgSlug &&
		p.principalType == q.principalType &&
		p.email == q.email &&
		slices.Equal(p.roles, q.roles) &&
		slices.Equal(p.scopes, q.scopes)
}

// principalJSON is the wire format. It is a separate type because the assertion
// claims live in unexported fields (see the [Principal] doc), which
// encoding/json cannot reach in either direction.
type principalJSON struct {
	User          string   `json:"user"`
	Tool          string   `json:"tool"`
	RawUser       string   `json:"raw_user,omitempty"`
	OrgID         string   `json:"org_id,omitempty"`
	OrgSlug       string   `json:"org_slug,omitempty"`
	Roles         []string `json:"roles,omitempty"`
	PrincipalType string   `json:"principal_type,omitempty"`
	Scopes        []string `json:"scopes,omitempty"`
	Email         string   `json:"email,omitempty"`
}

// MarshalJSON emits the assertion claims alongside the exported fields. Every
// new key is omitempty, so a record from a non-assertion entry point is
// byte-identical to what previous versions wrote.
func (p Principal) MarshalJSON() ([]byte, error) {
	return json.Marshal(principalJSON{
		User:          p.User,
		Tool:          p.Tool,
		RawUser:       p.RawUser,
		OrgID:         p.orgID,
		OrgSlug:       p.orgSlug,
		Roles:         p.roles,
		PrincipalType: p.principalType,
		Scopes:        p.scopes,
		Email:         p.email,
	})
}

// UnmarshalJSON restores a Principal from the audit wire format.
//
// This is the one path that populates the assertion claims without a signature
// check, and it is safe precisely because of what it is for: reading back a
// record this process already wrote and verified. It must never be pointed at
// request-path input — a Principal decoded from an untrusted payload would
// bypass the guarantee the unexported fields exist to provide.
func (p *Principal) UnmarshalJSON(data []byte) error {
	var w principalJSON
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*p = Principal{
		User:          w.User,
		Tool:          w.Tool,
		RawUser:       w.RawUser,
		orgID:         w.OrgID,
		orgSlug:       w.OrgSlug,
		roles:         w.Roles,
		principalType: w.PrincipalType,
		scopes:        w.Scopes,
		email:         w.Email,
	}
	return nil
}

// Tool constants — the values that may appear in [Principal.Tool].
// Entry-point wiring references these instead of string literals so
// typos surface at compile time.
const (
	ToolCLI       = "cli"
	ToolMCP       = "mcp"
	ToolDataEntry = "data-entry"
	ToolScheduler = "scheduler"
	ToolDesktop   = "desktop"
	// ToolSync attributes writes applied by the sync API (FEAT-NJ9FEN) so the
	// audit log distinguishes a synced write from a direct data-entry edit.
	ToolSync = "sync"
	// ToolWebhookReceiver attributes writes made by an inbound-webhook handler
	// (e.g. an IdP membership event that provisions a person entity). It is a
	// distinct entry point from data-entry: the write originates from a verified
	// server-to-server callback, not a human at the UI.
	ToolWebhookReceiver = "webhook-receiver"
	// ToolProvisioner attributes the stub-user create performed by lazy
	// provisioning (`unmatched_principal: provision`, TKT-ANUJDS) when a verified
	// principal resolves to no user entity. It is a distinct entry point so the
	// audit log distinguishes an auto-provisioned stub from a human edit; paired
	// with [UserProvisioner].
	ToolProvisioner = "provisioner"
	// ToolTwin attributes writes made by the `rela twin` commands: an agent
	// syncing an entity with its counterpart in an external system. The audit
	// log then shows a sync write as tool=twin with the real user, apart from
	// that user's direct CLI edits.
	ToolTwin = "twin"
)

// UserScheduler is the default [Principal.User] for scheduled tasks that
// declare no `run_as` — a FIXED identity, deliberately not the OS user.
//
// The scheduler used to default to [SystemUser] ($USER). That made a job's
// read scope depend on which OS account happened to run `rela scheduler`,
// so the assignment an operator needed in acl.yaml differed per host and
// could not be written down in advance. Worse, a systemd unit typically has
// no $USER, so SystemUser() returned "unknown" — which [acl.Declarative]
// rejects as an unstamped principal, failing the task outright rather than
// merely scoping it.
//
// A fixed principal makes the scheduler's identity a documented, grantable
// constant:
//
//	assignments:
//	  system:scheduler: <a-role-with-read>
//
// It grants nothing by itself (DEC-O59WM4) — privileges still come only
// from acl.yaml. `run_as` continues to override it per task.
const UserScheduler = "system:scheduler"

// UserProvisioner is the fixed [Principal.User] under which lazy provisioning
// creates a stub user entity for an unmatched verified principal
// (`unmatched_principal: provision`, TKT-ANUJDS). Like [UserScheduler] it is a
// documented, grantable constant — a migration binds it to a role granting
// `create: [<user_entity_type>]` and NOTHING else, so the create it performs is
// authorized and audited to this identity while it cannot author group edges or
// touch any other type (the bare-stub containment, RR-28SCW3).
//
// It grants nothing by itself; the create-only privilege comes solely from the
// acl.yaml role the provisioner migration injects. Paired with
// [ToolProvisioner].
const UserProvisioner = "system:provisioner"

// ReservedPrefix namespaces the [Principal.User] values that only rela's own
// in-process entry points may assert. [UserScheduler] and [UserProvisioner] are
// its current members.
//
// The whole prefix is reserved, not just the two constants above, so a future
// `system:*` identity is safe by construction rather than depending on someone
// remembering to extend a list. The cost is that a deployment whose IdP issues
// a `system:`-shaped subject cannot use it as an acting identity.
const ReservedPrefix = "system:"

// IsReserved reports whether user names an internal identity under
// [ReservedPrefix].
//
// **Request-path entry points MUST reject a reserved user.** These names are
// grantable in acl.yaml — the DEC-O59WM4 migration binds [UserScheduler] to a
// role with `read: ["*"]` — and the ACL resolves them by a plain map lookup on
// the raw string (`policy.Assignments[user]`), with no notion of where the
// principal came from. So a reserved name arriving from the wire is identity
// spoofing that inherits the scheduler's grants; the ACL cannot detect it,
// because it cannot tell a forged `system:scheduler` from the real one.
//
// The check belongs at the boundary where request input becomes a Principal,
// not in the ACL and not in [With] — the scheduler, the provisioner and the CLI
// all stamp reserved identities legitimately, in-process.
//
// The comparison is deliberately case-SENSITIVE: acl.yaml assignments are
// matched exactly, so "System:Scheduler" confers no scheduler grant and is an
// ordinary (if odd) username. Case-folding here would reject it for no security
// gain.
//
// It is correct on a RAW value, not merely a pre-sanitized one. Leading control
// characters are stripped before the prefix test, because a caller's own
// sanitizer may map them to something that IS reserved: data-entry's
// sanitizeUser turns "\x01system:scheduler" into exactly "system:scheduler". If
// this function only trimmed whitespace, whether a name counted as reserved
// would depend on whether the caller happened to sanitize first — safe by
// ordering rather than by construction, and silently wrong for any future entry
// point that passes a raw value (provisioning already does).
func IsReserved(user string) bool {
	return strings.HasPrefix(trimReservedNoise(user), ReservedPrefix)
}

// trimReservedNoise strips the leading characters that carry no identity but
// could hide a reserved prefix: ASCII whitespace and C0/DEL control codes.
//
// Only the LEADING run matters. The prefix test that follows anchors at
// position 0, so interior or trailing noise cannot manufacture a `system:`
// prefix — and stripping it would risk equating names that are genuinely
// distinct to the ACL's exact-match assignment lookup.
func trimReservedNoise(user string) string {
	return strings.TrimLeftFunc(user, func(r rune) bool {
		return unicode.IsSpace(r) || r <= 0x1f || r == 0x7f
	})
}

// principalKey is the unexported context.WithValue key so no other
// package can collide with it or read/write the value outside this
// package's API.
type principalKey struct{}

// With returns a derived context carrying p. Entry points stamp
// Principal once at startup (or per-request for data-entry);
// consumers read it via [From] on each operation.
//
// Call sites read as `principal.With(ctx, ...)` and `principal.From(ctx)` —
// the package name carries the noun so the function names don't stutter.
func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// Unknown is the placeholder user (and tool) for a ctx that carries no
// identity, and for a transport that could not determine one — the data-entry
// server without an identity source stamps it explicitly. It is an
// ATTRIBUTION default, adequate for an audit record; it is not a user, and a
// consumer comparing an identity against graph data must treat it as absent.
const Unknown = "unknown"

// From returns the Principal carried by ctx, or
// Principal{User:"unknown", Tool:"unknown"} if none was stamped.
// Returning a default rather than panicking keeps downstream consumers
// best-effort even when a new call site forgets to thread principal
// through — the misattribution is visible (in the audit log, for
// instance), not silent.
func From(ctx context.Context) Principal {
	if v, ok := ctx.Value(principalKey{}).(Principal); ok {
		// Cloned: the ctx value is shared by every reader for the life of the
		// request, so handing out the roles backing array would let one
		// consumer mutate what another is about to authorize against.
		return v.Clone()
	}
	return Principal{User: Unknown, Tool: Unknown}
}

// Stamped returns the Principal carried by ctx and whether one was actually
// stamped. It is [From] without the unknown/unknown default, for the callers
// that must distinguish "no identity on this ctx" from "an identity that
// happens to be unknown".
//
// Use it only when the two cases lead to DIFFERENT behavior — chiefly a
// transport deciding whether to supply a fallback identity (the MCP server's
// principal middleware: a request-borne identity wins, a construction-time
// one fills in when there is none). Everywhere else, [From]'s default is the
// right answer and this function would just be a way to reintroduce nil
// checks.
//
// Never use ok==false to grant access. An unstamped ctx means the identity is
// unknown, not that the caller is privileged.
func Stamped(ctx context.Context) (Principal, bool) {
	v, ok := ctx.Value(principalKey{}).(Principal)
	if !ok {
		return Principal{}, false
	}
	return v.Clone(), true
}

// SystemUser returns the OS user running this process — $USER
// trimmed, or "unknown" if $USER is unset or whitespace-only. Used by
// entry-point wiring to populate [Principal.User].
//
// The original plan had a four-tier fallback chain ($RELA_ACTOR →
// $USER → git config user.email → "system") — dropped during review
// as over-engineered for what is one bit of operator identity.
// Operators who want a different identity set $USER; data-entry will
// override per-request via HTTP middleware in a follow-up.
func SystemUser() string {
	u := strings.TrimSpace(os.Getenv("USER"))
	if u == "" {
		return Unknown
	}
	return u
}
