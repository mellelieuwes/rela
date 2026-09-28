package entitymanager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/autocascade"
	"github.com/Sourcehaven-BV/rela/internal/automation"
	"github.com/Sourcehaven-BV/rela/internal/computed"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/templating"
)

// TemplateLoader is the narrow consumer-side surface entitymanager
// needs from a templating implementation. The full
// [templating.Templater] interface has five methods; Manager calls
// exactly two. Defining this interface here (CLAUDE.md
// "consumer-side interfaces" rule) lets tests stub two methods
// instead of five, and keeps Manager decoupled from the
// template-generation half of templating's API.
type TemplateLoader interface {
	EntityTemplate(ctx context.Context, entityType, variant string) (*templating.Template, error)
	RelationTemplate(ctx context.Context, relationType string) (*templating.Template, error)
}

// Manager is the production entity-manager implementation — the whole of this
// package's write API. It runs metamodel validation, automation rules (via
// [automation.Engine]), and dispatches automation cascades through an
// [autocascade.Runner].
//
// Manager is constructed at each per-command wiring site (cmd/rela,
// cmd/rela-server, cmd/rela-desktop, plus subcommands that need their own
// write path). Consumers depend on a scoped consumer-side interface declared
// in their OWN package, never on a wide one declared here (see CLAUDE.md and
// the package doc): appbuild hands out *Manager, and each consumer's
// interface is satisfied structurally. The wide package-level EntityManager
// interface that used to sit beside this type was removed in TKT-IVSJV6.
//
// Pipeline shapes preserved from the pre-decomposition workspace
// implementation (see PLAN-HQ5Y):
//
//   - Create: createCore (validate → write) → automation.Process →
//     apply property changes → re-write if changed → cascade.
//     Engine.Process runs here (not inside Runner) because
//     PropertiesSet must land on the entity and be persisted before
//     cascade dispatches.
//   - Update: validate → engine.Process(if oldEntity != nil) →
//     apply property changes → write → cascade.
//   - Delete: lookup → collect incident relations → delete relations
//     → delete entity. No automation, no cascade.
//   - Rename: validate → write at new ID → rewrite incident relations
//     → delete old. No automation, no cascade, no re-validation.
//   - CreateRelation: fetch endpoints → validate type → check duplicate
//     → apply template → write. No automation.
//   - UpdateRelation: fetch existing → merge properties → MetaUnset →
//     content → write. No automation.
//   - DeleteRelation: delete. No automation.
//
// +1 (BUG-64MU2Q): DeleteRelationState, the face-addressed form of
// DeleteRelation. It cannot be an option on the existing method — the tail is
// part of a relation's identity, so the two address different edges, which is
// why the store draws the same line between DeleteRelation and
// DeleteRelationState. Pinned here rather than split: the pair belongs on the
// type that owns the write path.
//
//plimsoll:max-methods=41
type Manager struct {
	deps Deps

	// bypassACL marks this Manager as an ELEVATED write handle: its writes
	// skip the ACL deny (TKT-D8T148, the `rela.bypass_acl(...)` path). It is
	// false on the normal Manager and set true ONLY on the throwaway handle
	// returned by Manager.elevated. Carrying elevation on the object (not
	// the context) is what makes it leak-proof: a nested cascade an elevated
	// write triggers re-dispatches with the gated Manager (see the cascade
	// dispatch sites, which pass `m.gated()` as the Mutator), so elevation
	// never propagates to descendant writes.
	bypassACL bool
}

// elevated returns a throwaway Manager handle whose writes skip the ACL deny.
// Shares all deps with the receiver; differs only in bypassACL. Reserved for
// the script-runner's elevated write handle (rela.bypass_acl). NOT exported as
// a general capability — callers obtain it only through the autocascade
// Mutator seam for an allow_acl_bypass action.
func (m *Manager) elevated() *Manager {
	return &Manager{deps: m.deps, bypassACL: true}
}

// Elevated satisfies [autocascade.ElevatedProvider] (TKT-D8T148): it hands the
// script runner an elevated Mutator for an `allow_acl_bypass` action. Exported
// only to cross the package boundary to the script runner; the returned handle
// bypasses the ACL deny but preserves the real principal in audit and never
// propagates elevation into nested cascades (it dispatches via gated()).
func (m *Manager) Elevated() autocascade.Mutator {
	return m.elevated()
}

// gated returns a non-elevated Manager sharing the receiver's deps. On a normal
// Manager it returns the receiver; on an elevated handle it strips the bypass.
// Used at cascade-dispatch sites so a nested cascade triggered by an elevated
// write runs with normal ACL authority — elevation does not propagate to
// descendants (the leak the ctx-marker approach would have had).
func (m *Manager) gated() *Manager {
	if !m.bypassACL {
		return m
	}
	return &Manager{deps: m.deps, bypassACL: false}
}

// Compile-time assertion: Manager must satisfy the autocascade.Mutator
// surface (the per-cascade write handle scripted actions receive), so a
// drift surfaces at this type rather than at the call site that passes
// Manager into Request.Mutator.
//
// There is deliberately no assertion against a package-local write
// interface: the wide EntityManager one was deleted in TKT-IVSJV6, and its
// replacements are declared at each CONSUMER (lua.Mutator,
// attachment.EntityPatcher, mcp.EntityWriter, and the unexported ones in
// internal/cli and internal/dataentry). Asserting against them here would
// re-import every consumer and reinstate the coupling the split removed;
// each is checked where it is used, at its own wiring site.
var _ autocascade.Mutator = (*Manager)(nil)

// Deps is the constructor input for [New]. Using a struct keeps the
// constructor signature stable as new collaborators land (audit,
// principal, policy in subsequent tickets).
type Deps struct {
	// Store is the authoritative persistence layer. Required.
	Store store.Store

	// Meta is the active metamodel. Manager uses it for
	// ValidateEntity (entity writes) and ValidateRelation (relation
	// writes). Required.
	Meta *metamodel.Metamodel

	// Templater applies entity-creation and relation-creation
	// templates. The full [templating.Templater] satisfies this
	// narrow contract structurally. Required (use a no-op in tests).
	Templater TemplateLoader

	// Automations is the rule-evaluation engine. Manager calls it
	// on EventEntityCreated / EventEntityUpdated to discover side
	// effects. Optional: nil disables automation processing.
	Automations *automation.Engine

	// Cascade is the autocascade Runner that orchestrates automation
	// side effects after a write. Required iff Automations is non-nil.
	Cascade *autocascade.Runner

	// ScriptRunner executes scripted automation actions during a
	// cascade. May be nil if no scripted automations are configured;
	// Runner records each scripted action as a per-action error when
	// no ScriptRunner is supplied. Wiring sites that need
	// transport-specific deps (e.g. Lua) construct one with the
	// static read deps at this layer; the per-cascade mutator is
	// supplied by Manager via [autocascade.Request.Mutator] (see
	// internal/script/luascriptrunner.go for the Lua adapter).
	ScriptRunner autocascade.ScriptRunner

	// Audit receives one record per successful entity / relation write.
	// Required. Production wiring passes a [audit.Filesystem]; tests use
	// [audit.NewMemory] or [audit.Nop]. Never substitute a silent nil —
	// the constructor rejects nil so missing audit fails fast at wiring
	// time, not later as silently-dropped forensic data.
	Audit audit.Audit

	// ACL gates every write entry point. Required. Production wiring
	// passes [acl.NopACL] (no acl.yaml) or [acl.Declarative] (acl.yaml
	// present); `rela-server --read-only` injects [acl.ReadOnlyACL].
	// Tests use [acl.NopACL] unless they assert on the deny path.
	// Never substitute a silent nil — the constructor rejects nil so
	// missing ACL fails fast at wiring time, not later as a silently
	// disabled authz gate.
	ACL acl.ACL

	// VersionRecorder captures a synchronous entity version for rename and
	// delete (see [VersionRecorder]). Optional: nil disables synchronous
	// version capture (fs/mem builds, and the postgres build's create/update
	// versions are handled by the store's periodic sweep, not this hook).
	VersionRecorder VersionRecorder

	// AliasRewriter is notified when an entity is renamed or deleted so a
	// subsystem holding references BY ENTITY ID (the CalDAV alias service) can
	// rewrite them (see [AliasRewriter]). Optional: nil disables the hook.
	AliasRewriter AliasRewriter

	// RelationVersionRecorder captures a synchronous relation version for
	// relation delete (explicit and entity-cascade) and endpoint rename (see
	// [RelationVersionRecorder]). Optional: nil disables it (fs/mem builds; the
	// postgres build's relation create/update versions come from the sweep).
	RelationVersionRecorder RelationVersionRecorder

	// Transitions enforces enum state machines on the write path (TKT-E4LW2):
	// transition legality, guard permissions, and preconditions. Required so
	// no write path can silently skip the machine — but a metamodel with no
	// transitions compiles to an empty enforcer whose checks are no-ops, so
	// "required" costs nothing when the feature is unused. Constructed once at
	// startup by [statemachine.Compile] and injected; the Manager never
	// re-derives it from the metamodel.
	Transitions TransitionEnforcer

	// Computed is the compiled materialized-property evaluator. If omitted,
	// New compiles it from Meta so a wiring omission can never skip
	// computation. Production appbuild injects the already-compiled set.
	Computed *computed.Set

	// TransitionGuard answers the guard question for a state-machine
	// transition (does the ctx principal hold permission P for the subject).
	// May be nil: a nil guard makes every guarded edge fail closed, which is
	// the safe default when no ACL-backed guard was wired. Production wiring
	// supplies an adapter over the ACL; the direct-CLI/no-policy case is
	// handled inside that adapter (it allows when there is no policy).
	TransitionGuard statemachine.Guard

	// TransitionGraph answers has_relation/count_relations for a transition
	// `when:` precondition. May be nil when no precondition needs the graph;
	// a `when:` that queries the graph then evaluates against an empty graph.
	TransitionGraph statemachine.GraphLookup

	// FieldGate decides whether the ctx principal may author the specific
	// property changes in a [Manager.PatchEntity] call. Required — pass
	// [AllowAllFieldGate] to opt out explicitly.
	//
	// It is a CAPABILITY injected at the wiring site, never inferred from
	// identity (the write-side analog of the read-side AllowAllReader,
	// DEC-ZBI39P). Operator-trust-boundary entry points (CLI) wire
	// AllowAllFieldGate because they have full access by design;
	// request-scoped surfaces are where a policy-backed implementation
	// belongs — none is wired yet (TKT-0XL8MF); see [FieldWriteGate].
	//
	// Required rather than optional-nil on purpose: a silently-nil authz
	// gate is the "forgotten wiring must not become an ACL bypass" failure
	// (RR-X9NVHI), and it matches how ACL and Audit are already handled.
	FieldGate FieldWriteGate

	// CopyVisibility is the CALLER'S read gate, used only by CROSS-ENTITY
	// copies (TKT-C1XUA8, design doc §9.2).
	//
	// Deliberately NOT used by same-entity copies: those run elevated,
	// because hidden fields travel with the entity and the same policy
	// governs them on the target face. Routing them through this gate would
	// be the redacted-read-feeds-a-write bug, which destroys the fields the
	// principal could not see.
	//
	// Nil: accepted without a policy — the read is then raw, which is what
	// every other read on such a deployment does, and is what the CLI passes.
	// REJECTED when ACL is an [*acl.Declarative]; pass
	// [AllowAllCopyVisibility] to opt out explicitly. See [requireCopyGates]
	// for why the tolerance is conditioned rather than unconditional.
	CopyVisibility CopyReader

	// CopyGuard evaluates a copy definition's `guard:` permission against the
	// SOURCE entity, so "the owner of THIS doc may publish it" works without
	// a global grant. Nil makes a GUARDED copy fail closed, matching the
	// statemachine's nil-guard rule; an unguarded copy is unaffected.
	CopyGuard CopyGuard

	// CopyReadGate answers "may this principal READ the copy's source at
	// all", before either half of the elevation split runs (TKT-C1XUA8).
	// Elevation decides which FIELDS travel, not whether the principal may
	// touch the entity.
	//
	// Nil: accepted without a policy — a deployment with no acl.yaml has no
	// gate to consult, and every other read on it is ungated too. REJECTED
	// when ACL is an [*acl.Declarative]; pass [AllowAllCopyReadGate] to opt
	// out explicitly. This used to say "required in spirit and nil-tolerant
	// in practice", which is exactly the gap [requireCopyGates] closes.
	CopyReadGate CopyReadGate
}

// FieldWriteGate answers whether the ctx principal may write the named
// properties of e. Defined here at the call site (CLAUDE.md consumer-side
// interfaces) so entitymanager needs no dependency on the affordance
// resolver that backs it in production.
//
// **STATUS: no production surface wires a real implementation yet.** Every
// current wiring site passes [AllowAllFieldGate], so this gate is inert
// outside tests — field-level write authz is enforced only by
// internal/dataentry's own validateFieldWrite on its HTTP path, exactly as
// before. The seam exists so the policy-backed implementation is a wiring
// change rather than a rewrite; see TKT-0XL8MF. Do not assume a write
// reaching PatchEntity has been field-gated.
//
// set holds the properties being upserted (key → new value); unset holds
// the ones being removed. An implementation returns a non-nil error to
// refuse the write; the error surfaces to the caller unwrapped, so it
// should carry whatever structured denial detail the surface needs.
//
// **Scope: caller-authored changes only.** Automation-derived properties
// ([automation.Result.PropertiesSet]) are system writes and are
// deliberately NOT passed through this gate. The gate enforces parity with
// what the affordance resolver would surface to this principal on a read —
// and automation is the system acting, not the principal. Gating it would
// mean a user who cannot author `status` could never trigger an automation
// that sets `status`, breaking ordinary workflow automations (RR-00ERM9).
type FieldWriteGate interface {
	CheckFieldWrite(ctx context.Context, e *entity.Entity, set map[string]any, unset []string) error
}

// AllowAllFieldGate permits every field write. It is the explicit opt-out
// for surfaces that sit on the operator trust boundary (the CLI, where the
// caller already has full filesystem access to the data) and for tests.
//
// Named and passed deliberately, exactly like [acl.NopACL] — never the
// result of leaving [Deps.FieldGate] nil.
type AllowAllFieldGate struct{}

// CheckFieldWrite implements [FieldWriteGate] by allowing everything.
func (AllowAllFieldGate) CheckFieldWrite(
	context.Context, *entity.Entity, map[string]any, []string,
) error {
	return nil
}

// TransitionEnforcer is the narrow contract the Manager needs from the
// compiled state machines: enforce an update (old→new) and a create's entry
// value. Defined at the call site (CLAUDE.md consumer-side interfaces);
// [*statemachine.Set] satisfies it.
// CopyReader is the caller-scoped read a CROSS-ENTITY copy sees. Narrow by
// design: the copy needs one lookup, and taking the whole visibility.Reader
// would bind this package to methods it never calls.
//
// Get returns (nil, false, nil) indistinguishably for denied, missing and
// type-mismatched — whether an entity exists is a genuine secret.
type CopyReader interface {
	// Get returns the entity's stored face as the ctx principal may see it:
	// row-gated, FACE-gated (a `type@face` grant that excludes face is a
	// miss), and field-redacted. Denied and absent are indistinguishable.
	Get(ctx context.Context, entityType, id string, face entity.Face) (*entity.Entity, bool, error)
}

// CopyGuard answers a copy definition's `guard:` permission for one subject.
//
// The same shape as statemachine.Guard, and satisfied by the same wiring —
// the point of reusing the vocabulary is that the two cannot drift into
// asking different questions. Subject is the bare entity id, matching the
// conferral model: identity-scoped roles confer on the entity in every
// world, and the face-granularity already lives in the write grant.
// CopyReadGate is the row-level read verdict for a copy's SOURCE. Narrow by
// design; satisfied by the same acl.Request the rest of the read path uses.
type CopyReadGate interface {
	// PermitsReadFace is the row verdict for the id AND the face allowlist
	// for the stored face being read. Face-blind gating here would let a
	// principal granted `read: [page@published]` promote a draft it may not
	// read; [acl.Request.PermitsReadFace] is the production implementation.
	PermitsReadFace(ctx context.Context, entityType, entityID string, face entity.Face) (bool, error)
}

// AllowAllCopyReadGate permits reading every copy source. It is the explicit
// opt-out from [requireCopyGates] for a surface that sits on the operator
// trust boundary, and for tests that build a policy-backed [Deps] without
// caring about the copy path.
//
// Named and passed deliberately, exactly like [AllowAllFieldGate] and
// [acl.NopACL] — never the result of leaving [Deps.CopyReadGate] nil, because
// that is the shape a forgotten wiring takes.
type AllowAllCopyReadGate struct{}

// PermitsReadFace implements [CopyReadGate] by permitting everything.
func (AllowAllCopyReadGate) PermitsReadFace(
	context.Context, string, string, entity.Face,
) (bool, error) {
	return true, nil
}

// AllowAllCopyVisibility reads a copy source raw — no row gate, no field
// redaction. The explicit opt-out counterpart to [AllowAllCopyReadGate];
// the same "named, never nil" rule applies.
//
// Unlike the other opt-outs it needs a store to read through, so the field is
// unexported and [NewAllowAllCopyVisibility] is the only way to build one. A
// bare `AllowAllCopyVisibility{}` would pass [requireCopyGates] and then nil-
// panic on the first cross-entity copy — the deferred-downstream-symptom
// failure this whole guard exists to prevent, reintroduced by its own opt-out.
type AllowAllCopyVisibility struct{ store store.Store }

// NewAllowAllCopyVisibility builds the ungated copy reader.
//
// Nil: st is rejected — a nil store here is broken under ANY ACL, not only a
// policy-backed one, which is why this is validated at construction rather
// than inside [requireCopyGates].
func NewAllowAllCopyVisibility(st store.Store) (AllowAllCopyVisibility, error) {
	if st == nil {
		return AllowAllCopyVisibility{}, errors.New(
			"entitymanager: NewAllowAllCopyVisibility: Store is required")
	}
	return AllowAllCopyVisibility{store: st}, nil
}

// Get implements [CopyReader] by reading the stored face ungated. entityType
// is ignored: allow-all draws no per-type distinction.
//
// A store error is reported as a MISS rather than propagated, matching
// appbuild's real copyVisibility so a caller's `!ok` handling is uniform
// across the two. Note the usual "absent and denied are indistinguishable"
// rationale does NOT apply here — this gate denies nothing, so there is no
// confidentiality property to preserve; the reason is shape-consistency
// alone. Both share the same wart: a transient store failure surfaces to the
// operator as ErrCopySourceMissing for an entity that plainly exists.
func (v AllowAllCopyVisibility) Get(
	ctx context.Context, _, id string, face entity.Face,
) (*entity.Entity, bool, error) {
	e, err := v.store.GetEntityState(ctx, id, face)
	if err != nil {
		return nil, false, nil //nolint:nilerr // a miss, to match copyVisibility's shape; see the doc comment
	}
	return e, true, nil
}

type CopyGuard interface {
	HoldsPermission(ctx context.Context, entityID, permission string) bool
}

type TransitionEnforcer interface {
	EnforceUpdate(
		ctx context.Context, old, updated *entity.Entity,
		guard statemachine.Guard, lookup statemachine.GraphLookup,
	) error
	EnforceCreate(ctx context.Context, e *entity.Entity) error
}

// New constructs a Manager and validates required collaborators.
func New(d Deps) (*Manager, error) {
	if d.Store == nil {
		return nil, errors.New("entitymanager: New: Store is required")
	}
	if d.Meta == nil {
		return nil, errors.New("entitymanager: New: Meta is required")
	}
	if d.Templater == nil {
		return nil, errors.New("entitymanager: New: Templater is required")
	}
	if d.Audit == nil {
		return nil, errors.New("entitymanager: New: Audit is required (use audit.Nop{} to opt out)")
	}
	if d.ACL == nil {
		return nil, errors.New("entitymanager: New: ACL is required (use acl.NopACL{} to opt out)")
	}
	if d.Transitions == nil {
		return nil, errors.New(
			"entitymanager: New: Transitions is required (use statemachine.Compile; an empty set is a no-op)")
	}
	if d.Computed == nil {
		compiled, err := computed.Compile(d.Meta)
		if err != nil {
			return nil, fmt.Errorf("entitymanager: New: %w", err)
		}
		d.Computed = compiled
	}
	if d.FieldGate == nil {
		return nil, errors.New(
			"entitymanager: New: FieldGate is required (use entitymanager.AllowAllFieldGate{} to opt out)")
	}
	if (d.Automations == nil) != (d.Cascade == nil) {
		return nil, errors.New(
			"entitymanager: New: Automations and Cascade must be supplied together (both non-nil or both nil)",
		)
	}
	if err := requireCopyGates(d); err != nil {
		return nil, err
	}
	return &Manager{deps: d}, nil
}

// requireCopyGates refuses a policy-backed deployment that forgot to wire the
// copy read gates.
//
// [Deps.CopyReadGate] and [Deps.CopyVisibility] are nil-tolerant on purpose:
// a deployment with no acl.yaml has no gate to consult, and every other read
// on it is raw too. But that tolerance is only safe while nil actually MEANS
// "no policy". When a real policy is present and these two are nil, the copy
// path degrades silently: [copyEngine.authorizeCopy] skips its read check
// entirely and [copyEngine.readCopySource] takes the raw-store branch for
// cross-entity copies, so a principal can read a source entity — and the
// `visible:`-hidden fields on it — that every other read path would refuse.
// There is no error and no log line; the only marker was a code comment.
//
// So the nil-tolerance is conditioned rather than removed. The test for "a
// real policy" is that the ACL is an [*acl.Declarative]: that is the only
// implementation compiled from an operator's acl.yaml, and it is the identical
// test appbuild's buildEntityManager already applies to decide whether the
// gates it wires behave actively. [acl.NopACL] and [acl.ReadOnlyACL] are
// deliberately excluded — the former IS the no-policy case, and the latter
// denies every write without expressing any per-principal read policy for a
// gate to enforce.
//
// Checking d.ACL == nil instead would be vacuous: ACL is already required
// above, so it is never nil by the time this runs.
//
// The assertion is safe only because the implementation set is CLOSED — an
// unrecognized type falls through to "no policy", i.e. open, whereas
// internal/dataentry spells the same question as a switch that fails closed.
// A decorating implementation would split those two apart. That is guarded in
// the acl package itself by TestACLImplementations_AreAClosedSet, which fails
// on a new implementation and names the sites to update.
//
// This is the same fail-fast posture as [Deps.FieldGate] (RR-X9NVHI:
// forgotten wiring must not become an ACL bypass) and the sibling
// [Deps.CopyGuard], which already fails closed on a guarded copy. Those two
// made the copy READ gates the odd ones out; this closes that asymmetry at
// the load-time moment the rest of New already uses.
//
// Nil: rejected when ACL is *acl.Declarative — pass AllowAllCopyReadGate and
// AllowAllCopyVisibility to opt out explicitly.
func requireCopyGates(d Deps) error {
	if _, policyActive := d.ACL.(*acl.Declarative); !policyActive {
		return nil
	}
	var missing, optOuts []string
	if d.CopyReadGate == nil {
		missing = append(missing, "CopyReadGate")
		optOuts = append(optOuts, "AllowAllCopyReadGate{}")
	}
	if d.CopyVisibility == nil {
		missing = append(missing, "CopyVisibility")
		optOuts = append(optOuts, "NewAllowAllCopyVisibility(store)")
	}
	if len(missing) == 0 {
		return nil
	}
	// Name only what is actually missing, in both halves of the message: a
	// half-wired Deps that is told to re-wire the gate it already supplied
	// sends the operator looking at correct code.
	return fmt.Errorf(
		"entitymanager: New: %s required when ACL is a compiled policy "+
			"(*acl.Declarative): a nil copy read gate makes the copy path read its source "+
			"ungated and unredacted. Wire the real gate, or pass %s to opt out explicitly",
		strings.Join(missing, " and "), strings.Join(optOuts, " / "))
}

// authorizeAndAudit consults the ACL and, on deny, records a
// `denied-write` audit row and returns [*acl.ForbiddenError]. On allow,
// returns nil and the caller proceeds. Called as the first
// non-validation step in every write entry point.
//
// The denied-write audit happens regardless of audit backend
// (Filesystem / Memory / Nop) — forensic posture demands recording
// what was attempted, not just what landed.
//
// When this Manager is an elevated handle (`m.bypassACL` — TKT-D8T148: a
// `rela.bypass_acl(...)` write), the ACL deny is SKIPPED: the write is allowed
// regardless of the principal's grants. Elevation is a property of WHICH
// Manager you hold, not of the context — see Manager.elevated. The real
// principal is still preserved (`principal.From(ctx)`, unchanged) and the
// bypass is recorded (recordACLBypass) so the audit trail is unambiguous about
// which writes were elevated and on whose behalf. We do NOT recordDeniedWrite
// on the bypass path — there is no denial to record.
func (m *Manager) authorizeAndAudit(ctx context.Context, req acl.WriteRequest) error {
	if m.bypassACL {
		if !isAffordanceProbe(ctx) {
			m.recordACLBypass(ctx, req)
		}
		return nil
	}
	decision := m.deps.ACL.AuthorizeWrite(ctx, req)
	if decision.Allow {
		return nil
	}
	if !isAffordanceProbe(ctx) {
		// An affordance PROBE is not an attempted write, so recording it would
		// make the audit log say something untrue. See [withAffordanceProbe].
		m.recordDeniedWrite(ctx, decision, req)
	}
	return &acl.ForbiddenError{Decision: decision}
}

// affordanceProbeKey marks a context as an AFFORDANCE QUERY: a read-only
// "could this principal do X" question, not an attempted write.
type affordanceProbeKey struct{}

// withAffordanceProbe marks ctx as an affordance query, suppressing audit
// records from the authorization path.
//
// # Why the audit log must not see these
//
// [CopiesForSource] answers "which copies may this principal invoke here" by
// running the REAL authorization path — that is what stops the hint drifting
// from the write. But that path audits its denials, and a denial recorded for
// a question nobody asked makes `op=denied-write` mean "someone looked at a
// page" rather than "someone tried to write and was refused".
//
// A SPA renders an entity view, the view lists copy affordances, and every
// page load appends N rows to an append-only log. Anyone alerting on
// denied-write volume gets paged by ordinary browsing, and the real signal
// drowns. The audit log's whole value is that it does not lie (see
// [Manager.CopyState]'s note on why audit lands after the commit); this keeps
// that true in the other direction.
//
// It suppresses ONLY the record, never the decision: the verdict is computed
// identically and returned identically, so the hint still cannot drift.
//
// The key is typed and unexported, so nothing outside this package can mark a
// real write as a probe.
func withAffordanceProbe(ctx context.Context) context.Context {
	return context.WithValue(ctx, affordanceProbeKey{}, true)
}

// isAffordanceProbe reports whether ctx was marked by [withAffordanceProbe].
func isAffordanceProbe(ctx context.Context) bool {
	v, _ := ctx.Value(affordanceProbeKey{}).(bool)
	return v
}

// mapTransitionError translates a state-machine enforcement error into the
// entitymanager's wire-facing error shape (TKT-E4LW2). A guard denial becomes
// an [*acl.ForbiddenError] (RuleKind "transition-guard") so it flows through
// the same 403 path — and audit row — as any other authorization denial;
// legality and precondition failures pass through unchanged and surface as 422
// validation-class errors at the HTTP boundary. Returns nil for a nil input.
func (m *Manager) mapTransitionError(ctx context.Context, subject acl.Subject, err error) error {
	if err == nil {
		return nil
	}
	var ge *statemachine.GuardError
	if errors.As(err, &ge) {
		decision := acl.Decision{
			Allow:    false,
			RuleKind: "transition-guard",
			RuleID:   ge.Permission, // the specific right, queryable in audit (RR-F30CZ/N1)
			Reason:   err.Error(),
		}
		m.recordDeniedWrite(ctx, decision, acl.WriteRequest{Op: acl.OpUpdate, Subject: subject})
		return &acl.ForbiddenError{Decision: decision}
	}
	return err
}

// recordACLBypass emits one audit row for an elevated (rela.bypass_acl) write.
// It preserves the real triggering principal (so "who really caused this" is
// always answerable, like ruid under sudo) and marks the row acl_bypass=true
// so forensic queries can isolate elevated writes. The op stays the genuine
// write op; the bypass marker rides the Summary alongside the existing
// triggered_by=automation:<name>.
func (m *Manager) recordACLBypass(ctx context.Context, req acl.WriteRequest) {
	var subject *audit.Subject
	switch s := req.Subject.(type) {
	case acl.RelationSubject:
		subject = &audit.Subject{Kind: "relation", RelationType: s.Type, FromID: s.FromID}
	case acl.EntitySubject:
		subject = &audit.Subject{Kind: "entity", Type: s.Type(), ID: s.ID()}
	}
	m.deps.Audit.Record(audit.Record{
		Time:        time.Now().UTC(),
		Op:          audit.OpACLBypass,
		Subject:     subject,
		Principal:   principal.From(ctx),
		TriggeredBy: audit.TriggeredByFrom(ctx),
		Summary:     fmt.Sprintf("acl_bypass=true op=%s", req.Op),
	})
}

// recordDeniedWrite emits one audit row describing the refused
// attempt. Subject names the would-be target (entity or relation);
// Summary carries the deny rule_kind / rule_id / reason and the
// attempted op so jq filters can ask "what did Alice try to do?".
func (m *Manager) recordDeniedWrite(ctx context.Context, d acl.Decision, req acl.WriteRequest) {
	// RR-79HD: surface the target ID (entity ID, or relation
	// from-ID) so forensic queries against the audit log can answer
	// "which specific entity did Alice try to mutate?" without
	// re-parsing the deny summary string. ToID is omitted because
	// RR-F9M9 removed it from RelationSubject.
	var subject *audit.Subject
	switch s := req.Subject.(type) {
	case acl.RelationSubject:
		subject = &audit.Subject{
			Kind:         "relation",
			RelationType: s.Type,
			FromID:       s.FromID,
		}
	case acl.EntitySubject:
		subject = &audit.Subject{
			Kind: "entity",
			Type: s.Type(),
			ID:   s.ID(),
		}
	}
	m.deps.Audit.Record(audit.Record{
		Time:        time.Now().UTC(),
		Op:          audit.OpDeniedWrite,
		Subject:     subject,
		Principal:   principal.From(ctx),
		TriggeredBy: audit.TriggeredByFrom(ctx),
		Summary:     formatDeniedSummary(d, req.Op),
	})
}

// formatDeniedSummary builds the audit Summary for a denied-write row.
// Appends `attribution=[role=X via source, ...]` when the Decision
// carries Attributions so operators can answer "which roles did the
// resolver consider and via which paths" without re-running the
// resolver (AC7). The wire 403 path stays opaque — only audit reads
// Attributions.
func formatDeniedSummary(d acl.Decision, op acl.Op) string {
	base := fmt.Sprintf("denied: %s (rule_kind=%s rule_id=%s) attempted op=%s",
		d.Reason, d.RuleKind, d.RuleID, op)
	if len(d.Attributions) == 0 {
		return base
	}
	parts := make([]string, 0, len(d.Attributions))
	for _, a := range d.Attributions {
		parts = append(parts, fmt.Sprintf("role=%s via %s", a.Role, a.Source.String()))
	}
	return base + " attribution=[" + strings.Join(parts, ", ") + "]"
}

// CreateEntity creates a new entity, runs on-create automations, and
// dispatches any resulting cascade.
//
// Pipeline:
//
//  1. createCore: ID generation, template application, defaults,
//     metamodel validation, persist to store.
//  2. If automation should run: engine.Process(EventEntityCreated) →
//     collect property changes → apply → re-persist (yes, two writes:
//     the first is the validated bare entity, the second carries any
//     automation-set properties; pinned by manager_test.go).
//  3. Dispatch cascade via Cascade.Process; merge outcome into the
//     entity.CreateResult.
//
// **Caller-entity mutation.** The supplied `*entity.Entity` is used as
// a property/content carrier and not retained — the freshly-built
// entity is returned via [entity.CreateResult.Entity]. Callers should consume
// the returned entity, not the one they passed in.
func (m *Manager) CreateEntity(
	ctx context.Context, e *entity.Entity, opts entity.CreateOptions,
) (*entity.CreateResult, error) {
	ctx = withStoreAttribution(ctx)
	if e == nil {
		return nil, errors.New("entitymanager: CreateEntity: entity is nil")
	}
	// The face is taken from opts, NOT from e — the same value createCore
	// writes below. Reading it off the caller's carrier entity is what made
	// the authorized face and the written face two different things
	// (BUG-HC6I2T); e.Face was structurally always zero, so the check
	// answered about a row the write never touched.
	if err := m.deps.requireCreateFaceFor(e.Type, opts.Face); err != nil {
		return nil, err
	}
	if err := m.authorizeAndAudit(ctx, acl.WriteRequest{
		Op:      acl.OpCreate,
		Subject: acl.NewEntitySubject(e.Type, opts.ID, opts.Face),
	}); err != nil {
		return nil, err
	}
	if opts.ID != "" {
		if def, ok := m.deps.Meta.GetEntityDef(e.Type); ok && !def.IsManualID() {
			return nil, customIDNotAllowedError(e.Type, def, opts.ID)
		}
		// No GetEntity pre-check: it was a TOCTOU duplicate of the
		// store's atomic uniqueness guarantee. createCore now writes
		// with a direct CreateEntity and surfaces a conflict as
		// ErrEntityAlreadyExists, so a racing create can't slip past a
		// passed pre-check and become an overwrite.
	}

	created, warnings, err := createCore(ctx, m.deps, e.Type, createCoreOpts{
		ID:              opts.ID,
		IDPrefix:        opts.Prefix,
		TemplateVariant: opts.Variant,
		Properties:      e.Properties,
		Content:         e.Content,
		Face:            opts.Face,
	})
	if err != nil {
		return nil, err
	}
	// State-machine entry is enforced INSIDE createCore, before the durable
	// write (RR-HETEE), so an illegal entry never persists. No guard applies on
	// create-entry today; if a future change adds one, route its ErrGuardDenied
	// through mapTransitionError so it surfaces as 403, not 422 (RR-F30CZ/N2).

	result := &entity.CreateResult{Entity: created, Warnings: warnings}

	// Audit the durable write now, before automation re-writes or the
	// cascade run. createCore has already persisted the entity; if a
	// later step fails (the post-automation upsert, or a cascade that
	// hard-errors) the entity is still on disk, so the audit log must
	// already reflect it. Recording after the cascade left a window
	// where a committed write produced no audit record.
	m.recordEntityAudit(ctx, audit.OpCreateEntity, created, "created")

	runAutomation := m.deps.Automations != nil && !opts.SkipAutomation
	if !runAutomation {
		return result, nil
	}

	autoResult := m.deps.Automations.Process(ctx, automation.Event{
		Type:   automation.EventEntityCreated,
		Entity: created,
	})
	if len(autoResult.PropertiesSet) > 0 {
		// A compare-and-swap onto the stored row, not a write of `created`:
		// a concurrent patch that landed after createCore must survive. The
		// unique constraint is re-enforced against the POST-automation values
		// inside the same write, so an automation that sets a `unique`
		// property cannot write a duplicate natural key (the create path must
		// not be the weaker one). It is an update, never an upsert: createCore
		// already persisted the row (BUG-ZWTDH9).
		written, err := writeAutomationProperties(ctx, m.deps, created, autoResult.PropertiesSet, nil)
		if err != nil {
			return nil, err
		}
		created = written
		result.Entity = created
		// Recompute warnings against the post-automation state
		// (DEC-HWZHA). The pre-write warnings from createCore reflect
		// the entity before automation set any properties.
		if errs := m.deps.Meta.ValidateEntity(created.ID, created.Type, created.Properties); len(errs) > 0 {
			_, result.Warnings = partitionValidationErrors(errs)
		}
	}
	result.AutomationWarnings = autoResult.Warnings
	result.AutomationErrors = autoResult.Errors

	outcome, cascadeErr := m.deps.Cascade.Process(ctx, &cascadeHost{deps: m.deps}, autocascade.Request{
		Trigger:    created,
		OldTrigger: nil,
		Result:     autoResult,
		Scripts:    m.deps.ScriptRunner,
		Mutator:    m.gated(), // gated() so elevation never propagates into a nested cascade (TKT-D8T148)
	})
	if cascadeErr != nil {
		return nil, fmt.Errorf("cascade: %w", cascadeErr)
	}
	result.RelationsCreated = outcome.RelationsCreated
	result.EntitiesCreated = outcome.EntitiesCreated
	result.AutomationErrors = append(result.AutomationErrors, outcome.Errors...)
	result.AutomationWarnings = append(result.AutomationWarnings, outcome.Warnings...)

	return result, nil
}

// ValidateCreate runs the create path's defaults + validation against a
// candidate entity WITHOUT persisting, authorizing, auditing, or
// running automation. It returns the would-be entity (post template /
// status defaults) and the DEC-HWZHA soft warnings the real create
// would surface — so a dry-run create can show as-you-type validation
// feedback that cannot drift from [Manager.CreateEntity] (both share
// [buildCandidateEntity]).
//
// Contract:
//   - No write, no audit row, no automation — it is advisory only.
//     The real CreateEntity remains the sole authorization and audit
//     point; callers MUST re-authorize at commit (e.g. the data-entry
//     create handler's affordance gate).
//   - Hard structural errors (unknown type, bad manual ID, ID-prefix
//     mismatch) return as an error; soft conditions (required-unset,
//     type / value mismatch) return as warnings on a nil error.
//   - opts.ID may be empty: an ID is generated only to satisfy
//     validation that doesn't depend on it; it is not reserved.
func (m *Manager) ValidateCreate(
	ctx context.Context, e *entity.Entity, opts entity.CreateOptions,
) (*entity.Entity, []entity.Warning, error) {
	if e == nil {
		return nil, nil, errors.New("entitymanager: ValidateCreate: entity is nil")
	}
	// The face check runs here too, or the dry-run would report a create
	// clean that the real one refuses — the drift this function exists to
	// prevent (it shares buildCandidateEntity for exactly that reason).
	if err := m.deps.requireCreateFaceFor(e.Type, opts.Face); err != nil {
		return nil, nil, err
	}
	return buildCandidateEntity(ctx, m.deps, e.Type, createCoreOpts{
		ID:              opts.ID,
		IDPrefix:        opts.Prefix,
		TemplateVariant: opts.Variant,
		Properties:      e.Properties,
		Content:         e.Content,
		Face:            opts.Face,
		// Skip the full-store scan generateID would do — dry-run runs
		// per debounced keystroke and a real ID is not needed for
		// validation. RR-8I07.
		SkipIDGeneration: true,
	})
}

// UpdateEntity validates the new state, runs on-update automation
// when an old state is available, applies property changes, persists,
// and dispatches the cascade.
//
// **Caller-entity mutation.** When automation sets properties via
// [automation.Result.PropertiesSet], UpdateEntity mutates the supplied
// `*entity.Entity` in place before writing. Callers that need to
// preserve the pre-call state should clone first.
//
// **Gate:** if the entity doesn't exist, UpdateEntity returns
// [ErrEntityNotFound] and never runs the engine. (Preserves
// pre-refactor workspace behavior.)
func (m *Manager) UpdateEntity(ctx context.Context, e *entity.Entity) (*entity.UpdateResult, error) {
	ctx = withStoreAttribution(ctx)
	if e == nil {
		return nil, errors.New("entitymanager: UpdateEntity: entity is nil")
	}
	// Face comes from the entity being written, because that is what the
	// store keys on (stateKey(e.ID, e.Face)). Omitting it would authorize
	// every faced write against the DEFAULT face — the zero Face means
	// "default state" — so a role holding only a bare `update: [policy]`
	// grant could write `policy@published`, inverting the invariant
	// GrantsVerbOnState exists to hold (BUG-Y0GNSB).
	if err := m.authorizeAndAudit(ctx, acl.WriteRequest{
		Op:      acl.OpUpdate,
		Subject: acl.NewEntitySubject(e.Type, e.ID, e.Face),
	}); err != nil {
		return nil, err
	}
	// Hard-validate BEFORE the existence probe. Order is load-bearing and
	// predates the updateCore extraction: an invalid entity reports its
	// validation error even when the id does not exist. PatchEntity cannot
	// share this half (it has no candidate entity until after the read), so
	// the pre-check lives here rather than in updateCore, which re-runs the
	// same partition on the merged result.
	preErrs := m.deps.Meta.ValidateEntity(e.ID, e.Type, e.Properties)
	if hard, _ := partitionValidationErrors(preErrs); len(hard) > 0 {
		return nil, newValidationError(hard)
	}

	// The pre-image is read at the face this write AUTHORIZED against, not
	// at the zero coordinate. GetEntity(id) is GetEntityState(id, zero), so
	// the old spelling decided against e.Face and then read a different row
	// — the same authorize-here/write-there split BUG-HC6I2T removed from
	// the create path, and on a faced type it simply never found anything.
	oldEntity, getErr := m.deps.Store.GetEntityState(ctx, e.ID, e.Face)
	if getErr != nil {
		// Fails closed: only a genuine miss is reported as missing, so a
		// transient store error cannot reach the not-found branch that
		// returns before authorizing.
		if !errors.Is(getErr, store.ErrNotFound) {
			return nil, getErr
		}
		return nil, fmt.Errorf("%w: %s", ErrEntityNotFound, e.ID)
	}
	if err := rejectComputedChanges(m.deps, oldEntity, e); err != nil {
		return nil, err
	}

	// Unconditional: UpdateEntity is the whole-entity save, whose caller owns
	// every field. A caller wanting compare-and-swap uses PatchEntity with
	// entity.Patch.ExpectedVersion.
	return m.updateCore(ctx, e, oldEntity, "")
}

// PatchEntity applies a TARGETED set of property changes to one entity:
// properties the patch does not name are preserved as-is, regardless of
// whether the caller could read them. See [entity.Patch] for the
// merge semantics (upserts, then unsets, then the body tri-state).
//
// This is the safe alternative to read-modify-write. A caller doing
// GetEntity → merge → UpdateEntity must hold the WHOLE entity, so any
// property it failed to carry across is destroyed on save — and a caller
// reading through a redacting reader cannot carry across what it cannot
// see. PatchEntity owns the read internally and merges against the RAW
// stored entity, so that failure mode is unreachable: forgetting a
// property is a no-op, not an erasure.
//
// **Ordering is load-bearing** (RR-32XA5V):
//
//	read → locked-check → authorize → field gate → merge → updateCore
//
// The read comes first because the ACL subject needs the entity's real
// type and the caller supplied only an id — the same shape, and the same
// accepted existence disclosure, as [Manager.DeleteEntity]. The field gate
// runs strictly AFTER authorization: field verdicts are value-dependent,
// so consulting them for an unauthorized caller would turn allow-vs-deny
// into an oracle for stored property values and for entity existence.
//
// An elevated Manager (rela.bypass_acl) skips the field gate as well as
// the row ACL. Elevation is total by design — a half-elevated handle that
// silently drops some property writes is the confusing contract
// lua.WriteDeps.ElevatedManager exists to avoid — and the bypass is still
// recorded by authorizeAndAudit (RR-BA1NIV).
//
// # Compare-and-swap (TKT-34XS2R)
//
// Setting [entity.Patch.ExpectedVersion] makes the write conditional: it
// applies only if the stored entity still matches that token, else it fails
// with a [store.VersionConflictError] (recoverable via errors.As, and
// errors.Is-able as [store.ErrConflict]). The precondition is evaluated by
// the STORE, atomically with the write — this method's own read is write-prep
// for the merge, not the check, so no TOCTOU window exists between them.
//
// A caller that wants a bounded retry loop re-reads, re-derives its patch,
// and retries with the conflict's Actual version. ExpectedVersion is for a
// patch derived from something the caller read — notably a Content
// replacement computed from a base read.
//
// Without ExpectedVersion the patch is STILL written as a compare-and-swap,
// against the row this method read as its merge base, and retried a bounded
// number of times (read, authorize, merge, write) when another writer lands
// in between. The merge writes the whole row, so an unconditional write
// would erase a concurrent patch to a DIFFERENT property. Nothing serializes
// writers above the store, in this process or across processes, so the store
// precondition is what keeps disjoint patches from losing each other.
// Automations only compute a result before the write, and the cascade runs
// after it, so a retried attempt has no side effects to repeat.
func (m *Manager) PatchEntity(
	ctx context.Context, id string, p entity.Patch,
) (*entity.UpdateResult, error) {
	ctx = withStoreAttribution(ctx)
	if id == "" {
		return nil, errors.New("entitymanager: PatchEntity: id is empty")
	}
	pinToRead := p.ExpectedVersion == ""
	for attempt := 1; ; attempt++ {
		res, err := m.patchEntityOnce(ctx, id, p, pinToRead)
		if pinToRead && isVersionConflict(err) && attempt < casRetryAttempts {
			if berr := casBackoff(ctx, attempt); berr != nil {
				return nil, berr
			}
			continue
		}
		return res, err
	}
}

// patchEntityOnce is one attempt of [Manager.PatchEntity]. With pinToRead the
// write is conditional on the version of the row it read, rather than on
// p.ExpectedVersion.
func (m *Manager) patchEntityOnce(
	ctx context.Context, id string, p entity.Patch, pinToRead bool,
) (*entity.UpdateResult, error) {
	// RAW read, deliberately ungated: this is write-prep, and the merge
	// base must be the complete stored entity or hidden properties would
	// be dropped from the clone and erased on save. Consolidating this
	// read here is the point of the primitive — consumers no longer hold
	// a raw store handle of their own.
	//
	// The id may be the fused boundary form ("POL-1@published"), so it is
	// PARSED rather than handed to GetEntity whole: GetEntity is
	// GetEntityState(id, zero) in every backend, and a type declaring faces
	// stores no row at the zero coordinate, so the faced form would resolve
	// nothing (BUG-HC6I2T). The authorization below already reads the face
	// off the stored row, so resolving it here is what makes that correct
	// rather than accidentally right for unfaced types only.
	stored, getErr := m.getEntityByRef(ctx, id)
	if getErr != nil {
		// Structural, not textual: consumers holding a narrow write
		// interface (the Lua bindings) must be able to tell this apart
		// from other hard errors without matching on the message.
		return nil, newEntityNotFound(id)
	}

	// A locked (git-crypt) entity reads as a shell whose real property
	// values are unavailable, so merging onto it and saving would persist
	// the shell OVER the encrypted content — the same erasure this
	// primitive exists to prevent, via encryption instead of redaction
	// (RR-0QWLRC). Matches ApplyEntity's guard.
	if stored.IsLocked() {
		return nil, fmt.Errorf("entitymanager: PatchEntity: entity %s has inaccessible fields", id)
	}

	// Face from the STORED entity, not from `id`: `id` may be the fused
	// boundary form ("POL-1@published"), which stateKey resolves onto the
	// faced row — so the face actually being written is stored.Face, and
	// authorizing without it would decide against the default face
	// (BUG-Y0GNSB).
	if err := m.authorizeAndAudit(ctx, acl.WriteRequest{
		Op: acl.OpUpdate,
		// stored.ID, not `id`: the caller may have passed the fused form
		// ("POL-4@draft"), and the subject names the id and the face in
		// SEPARATE fields — passing the fused string as the ID would make the
		// row-gate key disagree with every other write path, which names the
		// bare id (see UpdateEntity above).
		Subject: acl.NewEntitySubject(stored.Type, stored.ID, stored.Face),
	}); err != nil {
		return nil, err
	}

	// Field-level gate, after the row-level decision. Skipped under
	// elevation for the same reason the row ACL is (see the doc comment).
	if !m.bypassACL {
		if err := m.deps.FieldGate.CheckFieldWrite(ctx, stored, p.Properties, p.MetaUnset); err != nil {
			return nil, err
		}
	}
	if err := rejectComputedPatch(m.deps, stored.Type, p.Properties, p.MetaUnset); err != nil {
		return nil, err
	}

	updated := stored.Clone()
	p.Apply(updated)

	expected := p.ExpectedVersion
	if pinToRead {
		expected = string(store.VersionOf(stored))
	}
	return m.updateCore(ctx, updated, stored, expected)
}

// updateCore is the shared post-authorization update pipeline: validate,
// run on-update automation, enforce transitions and unique constraints,
// persist, audit, and dispatch the cascade.
//
// Method on Manager (not a free function over [Deps] like [createCore])
// because the cascade needs m.gated() to stop elevation propagating into
// descendants. It deliberately contains **no ACL check and no attribution**
// — both belong to the entry points ([Manager.UpdateEntity], [Manager.PatchEntity]), which
// authorize with the subject shape appropriate to how they learned the
// entity type. Putting authorize here would double-authorize PatchEntity
// (which must authorize early, before it can merge) and emit two
// denied-write audit rows for one denial.
//
// oldEntity is passed in rather than re-read: both callers already hold it
// (UpdateEntity to prove existence, PatchEntity as the merge base), so the
// manager itself reads the row once per write. Callers may still read
// separately for their own reasons — internal/mcp does, to validate
// property names against the entity type before dispatching.
// expectedVersion carries the caller's compare-and-swap precondition down to
// the durable write. Empty means unconditional; PatchEntity passes the
// version of its merge base even when its caller gave none.
func (m *Manager) updateCore(
	ctx context.Context, e, oldEntity *entity.Entity, expectedVersion string,
) (*entity.UpdateResult, error) {
	// Type is immutable on update, on EVERY path and not only ApplyEntity's:
	// the store checks a non-default face's type against its family but not
	// the bare row's, so a retype here would split the family — the bare row
	// one type, its sibling faces another, and on fsstore two files under two
	// type directories (see ErrTypeImmutable).
	if oldEntity != nil && e.Type != oldEntity.Type {
		return nil, fmt.Errorf("entitymanager: %s: %w (stored %q, body %q)",
			e.ID, ErrTypeImmutable, oldEntity.Type, e.Type)
	}
	if err := m.deps.Computed.Evaluate(ctx, e); err != nil {
		return nil, err
	}
	// DEC-HWZHA: partition validation errors once. Hard errors abort;
	// soft conditions populate Result.Warnings. If automation runs and
	// mutates properties, we recompute warnings against the post-
	// automation state.
	preErrs := m.deps.Meta.ValidateEntity(e.ID, e.Type, e.Properties)
	hard, soft := partitionValidationErrors(preErrs)
	if len(hard) > 0 {
		return nil, newValidationError(hard)
	}

	result := &entity.UpdateResult{Entity: e, Warnings: soft}

	autoResult, ranAutomation, err := m.processUpdateAutomation(ctx, e, oldEntity)
	if err != nil {
		return nil, err
	}
	if ranAutomation {
		if len(autoResult.PropertiesSet) > 0 {
			// Properties changed — recompute warnings against the
			// post-automation state (DEC-HWZHA).
			if errs := m.deps.Meta.ValidateEntity(e.ID, e.Type, e.Properties); len(errs) > 0 {
				_, result.Warnings = partitionValidationErrors(errs)
			} else {
				result.Warnings = nil
			}
		}
		result.AutomationWarnings = autoResult.Warnings
		result.AutomationErrors = autoResult.Errors
	}

	// Enforce enum state machines on the final (post-automation) state, using
	// the prior state to determine the transition (TKT-E4LW2). This is the
	// unforgettable chokepoint: the enforcer is a required collaborator run in
	// the fixed write pipeline, so no update path can skip legality/guard/
	// precondition. An empty enforcer (metamodel with no transitions) is a
	// no-op.
	if err := m.deps.Transitions.EnforceUpdate(
		ctx, oldEntity, e, m.deps.TransitionGuard, m.deps.TransitionGraph,
	); err != nil {
		return nil, m.mapTransitionError(ctx, acl.NewEntitySubject(e.Type, e.ID, e.Face), err)
	}

	// Enforce `unique: true` natural-key constraints against the final
	// (post-automation) property values, atomically with the write,
	// excluding this entity's own prior version so a re-save of an unchanged
	// value does not collide.
	//
	// UpdateEntity, not upsert: the GetEntity above already established
	// the row exists (else we returned ErrEntityNotFound), so this is
	// unambiguously an update (BUG-ZWTDH9).
	// The CAS precondition rides down to the store, which is the only layer
	// that can compare-and-write atomically. Empty expectedVersion yields the
	// zero condition: an unconditional write, for a caller that owns the
	// whole entity (UpdateEntity).
	if err := writeWithUniqueCheck(ctx, m.deps, e, e.ID, func(st store.Store) error {
		_, werr := st.UpdateEntityIf(ctx, e, store.UpdateCondition{
			ExpectedVersion: store.EntityVersion(expectedVersion),
		})
		return werr
	}); err != nil {
		var invalid *ValidationError
		if errors.As(err, &invalid) {
			return nil, err
		}
		// A derived unique-property index can reject an update whose (possibly
		// automation-set) value duplicates another entity's, even though the
		// scan above passed under a concurrent writer. Surface it as the same
		// 422 the scan produces (TKT-3Q0GP1); other errors pass through.
		if ok, mapped := mapUniquePropertyConflict(err); ok {
			return nil, mapped
		}
		// %w, and nothing more: a *store.VersionConflictError MUST stay
		// recoverable by errors.As all the way up to whatever retry loop the
		// caller wrote. RR-HI9QIU is the cautionary case — a translation that
		// dropped the cause made a retry loop unreachable and turned 8 of 8
		// concurrent losers into 500s, silently.
		return nil, fmt.Errorf("write entity: %w", err)
	}

	// Audit the durable write now, before the cascade run. The entity
	// is already persisted; gating the audit on cascade success left a
	// window where a committed write produced no audit record.
	m.recordEntityAudit(ctx, audit.OpUpdateEntity, e, updateEntitySummary(oldEntity, e))

	if !ranAutomation {
		return result, nil
	}

	outcome, cascadeErr := m.deps.Cascade.Process(ctx, &cascadeHost{deps: m.deps}, autocascade.Request{
		Trigger:    e,
		OldTrigger: oldEntity,
		Result:     autoResult,
		Scripts:    m.deps.ScriptRunner,
		Mutator:    m.gated(), // gated() so elevation never propagates into a nested cascade (TKT-D8T148)
	})
	if cascadeErr != nil {
		return nil, fmt.Errorf("cascade: %w", cascadeErr)
	}
	result.RelationsCreated = outcome.RelationsCreated
	result.EntitiesCreated = outcome.EntitiesCreated
	result.AutomationErrors = append(result.AutomationErrors, outcome.Errors...)
	result.AutomationWarnings = append(result.AutomationWarnings, outcome.Warnings...)

	return result, nil
}

func (m *Manager) processUpdateAutomation(
	ctx context.Context, e, oldEntity *entity.Entity,
) (*automation.Result, bool, error) {
	if m.deps.Automations == nil {
		return nil, false, nil
	}
	result := m.deps.Automations.Process(ctx, automation.Event{
		Type:      automation.EventEntityUpdated,
		Entity:    e,
		OldEntity: oldEntity,
	})
	if len(result.PropertiesSet) == 0 {
		return result, true, nil
	}
	if err := rejectComputedPresent(m.deps, e.Type, stringMapAny(result.PropertiesSet)); err != nil {
		return nil, true, err
	}
	for prop, val := range result.PropertiesSet {
		e.SetString(prop, val)
	}
	if err := m.deps.Computed.Evaluate(ctx, e); err != nil {
		return nil, true, err
	}
	return result, true, nil
}

// authorizeCascadeRelations checks the principal may delete every relation
// type a cascade will destroy, and returns the first denial.
//
// Deduplicated by (relation type, source entity type): the decision is a pure
// function of those two plus the op, so a hub entity with thousands of edges
// across a handful of types costs a handful of checks. Without the dedup a
// denied cascade on a 5,000-edge entity would also write 5,000 denied-write
// audit rows for ONE refused operation, burying the signal it exists to give.
//
// Note the asymmetry with the live write path: DeleteRelation authorizes
// against the SOURCE entity's type, so an incoming edge is checked against its
// own source, not against the entity being deleted. That is the same subject
// the principal would face deleting the edge directly, which is the property
// that makes this gate meaningful rather than merely stricter.
func (m *Manager) authorizeCascadeRelations(
	ctx context.Context, tx store.Store, id string, incoming, outgoing []*entity.Relation,
) error {
	// The FACE is part of the key, not just the pair (BUG-64MU2Q): a
	// content-scoped edge is authorized against the state that owns it, so
	// collapsing draft- and published-tailed edges into one check would let
	// a `policy@published` grant stand for a draft-tailed edge. The
	// cardinality stays bounded by (types × faces), which is small.
	type subject struct {
		relType, fromType string
		fromFace          entity.Face
	}
	seen := make(map[subject]bool)

	check := func(rel *entity.Relation) error {
		if rel == nil {
			return nil
		}
		// Resolve through the TX view, not the outer store. The whole point of
		// running in a transaction is that the authorized set and the deleted
		// set are derived under one serialization; reading the type that the
		// decision is MADE ON from outside it would undercut exactly that. On
		// pgstore the outer handle is the pool, so this would also take a
		// second connection while the first is held.
		//
		// Best-effort, as the live relation-write path resolves it: an
		// unresolvable source yields an empty FromType, which fails closed.
		var fromType string
		if from, err := tx.GetEntity(ctx, rel.From); err == nil {
			fromType = from.Type
		}
		key := subject{relType: rel.Type, fromType: fromType, fromFace: rel.FromFace}
		if seen[key] {
			return nil
		}
		seen[key] = true

		// FromID is deliberately EMPTY. The decision is a pure function of
		// (relation type, source type, source face, op) — FromID is never
		// read by any branch of authorizeRelationWrite — so one check stands
		// for every edge sharing that tuple. Stamping one arbitrary id into
		// the audit row would make it look like a claim about that specific
		// entity: a forensic query for another source in the same class
		// would find nothing, though it was equally refused. An empty
		// FromID says "this type-and-face class", which is what was
		// actually decided.
		return m.authorizeAndAudit(ctx, acl.WriteRequest{
			Op: acl.OpDelete,
			Subject: acl.RelationSubject{
				Type:     rel.Type,
				FromType: fromType,
				FromFace: rel.FromFace,
			},
		})
	}

	// Split by direction so the error can name the FAR endpoint. For an
	// incoming edge the entity being deleted IS rel.To, so "its X relation to
	// <To>" would say "its relation to itself" and withhold the one fact that
	// makes the error actionable: the other endpoint, whose type is what
	// actually blocked the delete.
	for _, rel := range incoming {
		if err := check(rel); err != nil {
			return fmt.Errorf("cannot delete %s: its incoming %s relation from %s: %w",
				id, rel.Type, rel.From, err)
		}
	}
	for _, rel := range outgoing {
		if err := check(rel); err != nil {
			return fmt.Errorf("cannot delete %s: its outgoing %s relation to %s: %w",
				id, rel.Type, rel.To, err)
		}
	}
	return nil
}

// cascadeCapture is what deleteEntityInTx hands back for the caller to act on
// AFTER the transaction closes: the incident relations the cascade destroyed,
// captured before the delete removed them.
type cascadeCapture struct {
	incoming []*entity.Relation
	outgoing []*entity.Relation
}

// deleteEntityInTx is DeleteEntity's critical section: collect the incident
// relations, authorize every one a cascade would destroy, then delete.
//
// It performs STORE WORK ONLY. Version capture, alias notification and audit
// all happen in the caller, after the transaction closes — see the Tx comment
// at the call site for why that separation is load-bearing rather than
// stylistic.
func (m *Manager) deleteEntityInTx(
	ctx context.Context, tx store.Store, id string, cascade bool,
) (*store.DeleteResult, *cascadeCapture, error) {
	incoming, cErr := collectIncidentRelations(ctx, tx, id, store.DirectionIncoming)
	if cErr != nil {
		return nil, nil, fmt.Errorf("collect incoming relations for %q: %w", id, cErr)
	}
	outgoing, cErr := collectIncidentRelations(ctx, tx, id, store.DirectionOutgoing)
	if cErr != nil {
		return nil, nil, fmt.Errorf("collect outgoing relations for %q: %w", id, cErr)
	}
	totalRelations := len(incoming) + len(outgoing)

	if totalRelations > 0 && !cascade {
		return nil, nil, ErrHasRelations
	}

	// A cascade destroys these edges, so the principal must be allowed to
	// delete each one — otherwise deleting an entity is a back door to
	// removing relation types you hold no delete grant on.
	//
	// This MUST run inside the transaction: both backends re-derive the
	// incident set under their own lock and delete THAT set, so authorizing
	// a set collected outside the serialization would leave a window for a
	// concurrent writer. Denials abort before any write, so nothing unwinds
	// — which matters because fs/mem do not roll back (store.Transactor).
	if cascade && totalRelations > 0 {
		if aErr := m.authorizeCascadeRelations(ctx, tx, id, incoming, outgoing); aErr != nil {
			return nil, nil, aErr
		}
	}

	// Delegate the actual deletion to the store's cascade, which removes
	// the relation files and the entity file under a single lock and aborts
	// fail-secure if any relation file cannot be removed — so the entity is
	// never deleted while a relation is left behind (issue #888).
	res, delErr := tx.DeleteEntity(ctx, id, cascade)
	if delErr != nil {
		// Propagate res AND the capture, not nil: a non-transactional backend
		// reports the relations it DID remove before aborting, and the caller
		// needs both to record them — audit AND version history (issue #929).
		// Capturing only one leaves the two logs contradicting each other.
		// res is nil on a transactional backend, which the caller handles.
		return res, &cascadeCapture{incoming: incoming, outgoing: outgoing},
			fmt.Errorf("delete entity: %w", delErr)
	}
	return res, &cascadeCapture{incoming: incoming, outgoing: outgoing}, nil
}

// DeleteEntity removes an entity and its incident relations.
// **No automation, no cascade.** When cascade is false and the
// entity has any incident relations, returns [ErrHasRelations]
// without deleting anything.
func (m *Manager) DeleteEntity(ctx context.Context, id string, cascade bool) (*entity.DeleteResult, error) {
	// anyFaceOf, not GetEntity: a delete addresses the whole FAMILY, and
	// GetEntity(id) is GetEntityState(id, zero) — a coordinate a type
	// declaring faces has no row at, so every faced entity was undeletable
	// (BUG-HC6I2T). Only the type and face are read from the result, and the
	// type is the same at every face.
	//
	// Fails closed on a non-not-found error: collapsing every failure into
	// ErrEntityNotFound would let a transient store error skip the ACL check
	// below, since that branch returns before authorizing (existence is
	// itself a secret). Only a genuine miss is reported as missing.
	current, err := anyFaceOf(ctx, m.deps.Store, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", ErrEntityNotFound, id)
	}
	// ACL check happens after the lookup so the request carries the
	// real entity type; a deny on a non-existent entity would be more
	// confusing than the ErrEntityNotFound returned above.
	if aclErr := m.authorizeAndAudit(ctx, acl.WriteRequest{
		Op:      acl.OpDelete,
		Subject: acl.NewEntitySubject(current.Type, id, current.Face),
	}); aclErr != nil {
		return nil, aclErr
	}

	// Everything from here to the store delete runs inside ONE Tx.
	//
	// The incident-relation set must be collected, authorized and deleted
	// under a single serialization: both stores RE-DERIVE the set inside
	// their own lock/tx (fsstore rebuilds from its live index, pgstore
	// re-reads inside the transaction) and delete THAT set — not the one
	// handed to them. Authorizing a snapshot taken outside the lock would
	// leave a window in which a concurrent writer adds an edge that is then
	// deleted with no authorization at all, which is exactly the "back door
	// to destroying edge types you cannot delete directly" this gate exists
	// to close.
	//
	// Reads inside the callback go through the OUTER handle (that is what
	// acl.StoreGraph holds). fsstore's readers never take txMu, so this does
	// not deadlock — pinned by TestTx_ReadsViaOuterHandleDoNotDeadlock.
	// Writes must use the tx view, per the Tx contract.
	// The critical section — collect, authorize, delete — runs inside ONE Tx.
	//
	// Both backends RE-DERIVE the incident set inside their own lock/tx
	// (fsstore rebuilds from its live index, pgstore re-reads inside the
	// transaction) and delete THAT set, not the one handed to them.
	// Authorizing a snapshot taken outside the lock would leave a window in
	// which a concurrent writer adds an edge that is then deleted with no
	// authorization at all — the "back door to destroying edge types you
	// cannot delete directly" this gate exists to close.
	//
	// Everything EXTERNAL stays out of the callback: version capture, alias
	// notification and audit all run below, after the transaction closes.
	// store.Transactor is explicit that slow external I/O inside fn makes the
	// whole deployment's writers wait (pgstore holds a global advisory lock
	// for the callback's duration), and the version recorder writes through
	// its own pool connection — so a capture emitted inside would commit even
	// when the delete rolls back, leaving history asserting a delete that
	// never happened.
	var (
		res      *store.DeleteResult
		captured *cascadeCapture
	)
	txErr := m.deps.Store.Tx(ctx, func(tx store.Store) error {
		var dErr error
		res, captured, dErr = m.deleteEntityInTx(ctx, tx, id, cascade)
		return dErr
	})
	if txErr != nil {
		// A non-transactional backend can fail partway through a cascade with
		// some relation files already off disk, and it reports those in a
		// partial result (issue #929). Audit them before propagating: the
		// deletion really happened, and a log that omits it is a log that
		// denies the system's actual state.
		//
		// Same label and same emitter as the success path below, so a partial
		// and a complete cascade are indistinguishable in the log except by
		// how many rows they produced. No delete-entity record: the entity
		// survived, and claiming otherwise would be the opposite error.
		m.recordPartialCascade(ctx, id, res, captured)
		return nil, txErr
	}

	// Version capture, after commit. The rows are written from the pre-delete
	// state captured inside the transaction, so their content is the same as
	// an in-transaction capture would have produced; only the timing differs.
	// A crash between commit and here loses the delete markers — the same
	// non-atomicity the pre-Tx code carried and documented, not a regression.
	m.recordEntityVersion(ctx, store.VersionOpDelete, current, "")

	// Tell id-keyed subscribers the entity is gone. They decide what to do:
	// the CalDAV alias service RETAINS its reference, as the tombstone that
	// stops a stale client write resurrecting this entity.
	m.notifyAliasesOfDelete(ctx, id)

	// Capture a final version for every relation this cascade destroyed. This
	// is the ONLY place cascade-deleted relations get versioned: the store's
	// DeleteEntity bulk-deletes them below the write choke-point, so without
	// this their history would silently end with no `delete` marker and no
	// restore path (RR-181AFY).
	if captured != nil {
		cascadeTB := "cascade:delete-entity:" + id
		for _, rel := range captured.incoming {
			m.recordRelationVersion(ctx, store.VersionOpDelete, rel, "", "", cascadeTB)
		}
		for _, rel := range captured.outgoing {
			m.recordRelationVersion(ctx, store.VersionOpDelete, rel, "", "", cascadeTB)
		}
	}

	// Audit exactly what the store reports deleting. Cascade-deleted
	// relations carry triggered_by so the log attributes them to this
	// delete; recordRelationAudit reads it from cascadeCtx.
	cascadeCtx := ctx
	if cascade && len(res.DeletedRelations) > 0 {
		cascadeCtx = audit.WithTriggeredBy(ctx, "cascade:delete-entity:"+id)
	}
	for _, rel := range res.DeletedRelations {
		m.recordRelationAudit(cascadeCtx, audit.OpDeleteRelation, rel, "deleted")
	}

	deleteSummary := "deleted"
	if cascade && len(res.DeletedRelations) > 0 {
		deleteSummary = fmt.Sprintf("deleted (cascade: %d relations)", len(res.DeletedRelations))
	}
	m.recordEntityAudit(ctx, audit.OpDeleteEntity, current, deleteSummary)

	return &entity.DeleteResult{
		DeletedEntities:  []*entity.Entity{current},
		DeletedRelations: res.DeletedRelations,
	}, nil
}

// recordPartialCascade records the relations a FAILED cascade delete had
// already removed from disk, so BOTH logs reflect what genuinely happened
// rather than nothing at all (TKT-A23L87 / issue #929).
//
// Audit and version history together, deliberately. RR-181AFY made
// DeleteResult.DeletedRelations the single source for ALL relation-delete
// capture precisely so the two cannot drift; recording only the audit half
// here would leave the log asserting a deletion that history denies, and the
// rows are already off disk so no sweep could backfill them.
//
// Driven by res.DeletedRelations, not by the captured incident set: only the
// relations the store actually removed may be recorded. The capture supplies
// the pre-delete snapshots those ids need.
//
// Nil-safe throughout: a transactional backend returns nil on error, and a
// failure on the first relation removes nothing. Either way this is a no-op.
func (m *Manager) recordPartialCascade(
	ctx context.Context, id string, res *store.DeleteResult, captured *cascadeCapture,
) {
	if res == nil || len(res.DeletedRelations) == 0 {
		return
	}
	cascadeTB := "cascade:delete-entity:" + id
	cascadeCtx := audit.WithTriggeredBy(ctx, cascadeTB)

	// Index the captured pre-delete snapshots so each removed relation is
	// versioned from the state it actually had, not from a reconstruction.
	snapshots := make(map[string]*entity.Relation)
	if captured != nil {
		for _, rel := range append(append([]*entity.Relation{}, captured.incoming...), captured.outgoing...) {
			snapshots[relationKey(rel)] = rel
		}
	}

	for _, rel := range res.DeletedRelations {
		m.recordRelationAudit(cascadeCtx, audit.OpDeleteRelation, rel, "deleted")

		snap := rel
		if s, ok := snapshots[relationKey(rel)]; ok {
			snap = s
		}
		m.recordRelationVersion(ctx, store.VersionOpDelete, snap, "", "", cascadeTB)
	}
}

// relationKey is the (from, type, to) identity of a relation, used to pair a
// store-reported deletion with its pre-delete snapshot.
func relationKey(r *entity.Relation) string {
	return r.From + "--" + r.Type + "--" + r.To
}

// DeleteEntityFace removes ONE non-bare content state of an entity and the
// content-scoped edges that belong to it, leaving the rest of the family
// standing — what a DELETE addressed to `ID@face` means, and the only way to
// "unpublish" short of deleting the entity (TKT-SLFURL).
//
// It is [Manager.DeleteEntity]'s sibling, not a narrower spelling of it:
// DeleteEntity sweeps the whole family and every incident edge on both sides,
// whereas a face owns only the OUTGOING edges tailed at that face — incoming
// edges point at the entity, not at one of its states, and survive (see
// [store.Store.DeleteEntityState] for the rule). The bare face is refused
// here rather than delegated, because "delete the bare face" is either the
// whole entity (when no other face exists) or undefined (when one does), and
// neither is what a caller who spelled a face meant.
//
// Authorization names the face, so a role holding the bare `delete: [policy]`
// cannot remove a published face — the same narrowing every state-shaped
// write grant applies (GrantsVerbOnState). The cascade edges are authorized
// inside the transaction for the reason DeleteEntity gives: the store
// re-derives the set under its own lock and deletes THAT set.
func (m *Manager) DeleteEntityFace(
	ctx context.Context, id string, face entity.Face,
) (*entity.DeleteResult, error) {
	if face.IsDefault() {
		return nil, fmt.Errorf("delete face: %s names the bare face; delete the entity instead", id)
	}
	current, err := m.deps.Store.GetEntityState(ctx, id, face)
	if err != nil {
		// Fails closed, as in DeleteEntity: the ACL check is below.
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", ErrEntityNotFound, entity.FormatStateRef(id, face))
	}
	if aclErr := m.authorizeAndAudit(ctx, acl.WriteRequest{
		Op:      acl.OpDelete,
		Subject: acl.NewEntitySubject(current.Type, id, current.Face),
	}); aclErr != nil {
		return nil, aclErr
	}

	var (
		res      *store.DeleteResult
		outgoing []*entity.Relation
	)
	txErr := m.deps.Store.Tx(ctx, func(tx store.Store) error {
		// Only the edges TAILED AT THIS FACE go with it; the query's FromFace
		// is an equality match on the tail, so the bare face's edges and the
		// other faces' edges are not collected and not authorized here.
		tail := face
		var cErr error
		outgoing, cErr = collectRelations(ctx, tx, store.RelationQuery{
			EntityID: id, Direction: store.DirectionOutgoing, FromFace: &tail,
		})
		if cErr != nil {
			return fmt.Errorf("collect outgoing relations for %q: %w", entity.FormatStateRef(id, face), cErr)
		}
		if len(outgoing) > 0 {
			if aErr := m.authorizeCascadeRelations(ctx, tx, id, nil, outgoing); aErr != nil {
				return aErr
			}
		}
		var dErr error
		res, dErr = tx.DeleteEntityState(ctx, id, face)
		if dErr != nil {
			return fmt.Errorf("delete face: %w", dErr)
		}
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}

	// Version capture, audit and relation attribution after commit, exactly
	// as DeleteEntity orders them and for the same reasons.
	m.recordEntityVersion(ctx, store.VersionOpDelete, current, "")
	ref := entity.FormatStateRef(id, face)
	cascadeCtx := ctx
	if len(res.DeletedRelations) > 0 {
		cascadeCtx = audit.WithTriggeredBy(ctx, "cascade:delete-face:"+ref)
	}
	for _, rel := range res.DeletedRelations {
		m.recordRelationVersion(ctx, store.VersionOpDelete, rel, "", "", "cascade:delete-face:"+ref)
		m.recordRelationAudit(cascadeCtx, audit.OpDeleteRelation, rel, "deleted")
	}
	summary := fmt.Sprintf("deleted face %s", face)
	if len(res.DeletedRelations) > 0 {
		summary = fmt.Sprintf("deleted face %s (cascade: %d relations)", face, len(res.DeletedRelations))
	}
	m.recordEntityAudit(ctx, audit.OpDeleteEntity, current, summary)

	return &entity.DeleteResult{
		DeletedEntities:  []*entity.Entity{current},
		DeletedRelations: res.DeletedRelations,
	}, nil
}

// RenameEntity changes an entity's ID and rewrites all incident
// relations. **No automation, no cascade, no metamodel re-validation
// of the post-rename state** (preserved verbatim from pre-refactor
// workspace behavior).
//
// If opts.DryRun is true, no changes are persisted (and no audit
// record is emitted — dry runs do not show up in the audit log).
func (m *Manager) RenameEntity(
	ctx context.Context, oldID, newID string, opts entity.RenameOptions,
) (*entity.RenameResult, error) {
	// ACL needs the entity type, so we fetch first. Distinguish the two
	// failure modes:
	//   - not-found: skip ACL and fall through; renameEntity below
	//     returns ErrEntityNotFound with a clearer message, and there is
	//     nothing to authorize against.
	//   - any other error (transient I/O, backend hiccup): fail closed.
	//     Proceeding would run the rename with NO authorization at all —
	//     a store read that flakes must not turn an ACL-gated operation
	//     into an ungated one.
	// anyFaceOf, not GetEntity: a rename re-keys the whole family, and
	// GetEntity asks the zero coordinate — which a faced type has no row at.
	// That put every faced rename on the not-found branch below, and that
	// branch SKIPS AUTHORIZATION by design, so the rename ran ungated
	// (BUG-HC6I2T). The fail-closed reasoning below only holds if a present
	// entity is actually found.
	current, getErr := anyFaceOf(ctx, m.deps.Store, oldID)
	switch {
	case getErr == nil:
		if aclErr := m.authorizeAndAudit(ctx, acl.WriteRequest{
			Op: acl.OpRename,
			// Faceless on purpose: a rename re-keys the WHOLE entity family
			// (fsstore.renameEntity walks stateFamily(oldID), and
			// store.RenameEntity takes no Face), so there is no single face to
			// name here. Naming one would assert a narrower scope than the
			// operation has: authorizing the rename of one face while renaming
			// all of them. The faceless constructor is the loud, greppable way
			// to say that; it authorizes against the default face.
			Subject: acl.NewFacelessEntitySubject(current.Type, oldID),
		}); aclErr != nil {
			return nil, aclErr
		}
	case !errors.Is(getErr, store.ErrNotFound):
		return nil, fmt.Errorf("rename: load entity %q: %w", oldID, getErr)
	}

	// Collect incident relations (with their content) BEFORE the rename: the
	// rename rewrites each relation as create-new-triple + delete-old-triple at
	// the store level, so afterward the old endpoints are gone. We capture the
	// pre-rename state here to emit a `rename` version per relation below, so a
	// renamed endpoint's relation history stays continuous instead of reading as
	// a mass delete+create.
	var preRenameRels []*entity.Relation
	if !opts.DryRun && m.deps.RelationVersionRecorder != nil {
		preRenameRels = collectRenameAffectedRelations(ctx, m.deps.Store, oldID)
	}

	res, err := renameEntity(ctx, m.deps.Store, oldID, newID, opts)
	if err != nil || opts.DryRun {
		return res, err
	}

	// Derive both before/after subjects from the post-rename entity:
	// type is preserved by rename, so the post entity has the type
	// for both records. A separate pre-fetch would create a window
	// where audit silently no-ops if the pre-fetch fails but
	// rename succeeds (concurrent insert / racy store).
	postEntity, getErr := m.deps.Store.GetEntity(ctx, newID)
	if getErr != nil {
		slog.Error("audit.write_failed",
			"stage", "rename-postfetch",
			"new_id", newID,
			"error", getErr)
		return res, nil
	}
	m.recordRenameAudit(ctx, oldID, postEntity)
	// Capture the rename as a version event carrying the old id (prev_id), so a
	// renamed entity's history is walkable back to its former id. Only the
	// choke-point knows old->new; a later sweep sees the renamed entity as an
	// ordinary update and cannot reconstruct this link.
	m.recordEntityVersion(ctx, store.VersionOpRename, postEntity, oldID)

	// Rewrite id-keyed references for the same reason the version above carries
	// prev_id: this is the only point that knows old->new.
	m.rewriteAliasesForRename(ctx, oldID, postEntity.ID)

	// Capture a `rename` version for each incident relation, on its NEW triple,
	// carrying the pre-rename endpoints (prev_from/prev_to). The version's key is
	// the post-rename endpoint, so WriteRelationVersion resolves the surviving
	// rel_record_id; triggered_by attributes the versions to this rename.
	//
	// Since #1127 the store renames atomically (a bulk in-place
	// `UPDATE relations SET from_id=...`), so the relation KEEPS its
	// rel_record_id across the rename — the lineage is already continuous on one
	// id and this version merely appends a rename MARKER to it (the
	// prev_from/prev_to stitch walk finds no fork; it is harmless belt-and-braces
	// for a future non-atomic path). Capture here is SYNC-ONLY BEST-EFFORT: the
	// atomic re-key does not bump relations.updated_at (see
	// TestRelationRenameDoesNotBumpUpdatedAt), so the reconciliation sweep cannot
	// back-fill a rename this hook misses. That is acceptable — a missed capture
	// loses only the rename marker, never history continuity, because the
	// underlying lineage stays intact on the surviving rel_record_id.
	if len(preRenameRels) > 0 {
		renameTB := "rename-entity:" + oldID + "->" + newID
		for _, rel := range preRenameRels {
			newFrom, newTo := rel.From, rel.To
			if newFrom == oldID {
				newFrom = newID
			}
			if newTo == oldID {
				newTo = newID
			}
			after := &entity.Relation{
				From: newFrom, Type: rel.Type, To: newTo,
				Properties: rel.Properties, Content: rel.Content,
			}
			m.recordRelationVersion(ctx, store.VersionOpRename, after, rel.From, rel.To, renameTB)
		}
	}
	return res, nil
}

// collectRenameAffectedRelations gathers the incident relations of id (both
// directions) with their content, for pre-rename version capture. Self-
// referential relations appear once (outgoing). Errors are swallowed — a
// best-effort capture must never fail the rename.
func collectRenameAffectedRelations(ctx context.Context, st store.Store, id string) []*entity.Relation {
	seen := make(map[string]struct{})
	out := make([]*entity.Relation, 0)
	for _, dir := range []store.Direction{store.DirectionOutgoing, store.DirectionIncoming} {
		for r, err := range st.ListRelations(ctx, store.RelationQuery{EntityID: id, Direction: dir}) {
			if err != nil {
				continue
			}
			key := r.From + "\x00" + r.Type + "\x00" + r.To
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, r)
		}
	}
	return out
}

// CreateRelation creates a new relation, validating endpoints and
// the relation-type tuple against the metamodel. **No automation.**
func (m *Manager) CreateRelation(
	ctx context.Context, from, relType, to string, opts entity.RelationOptions,
) (*entity.Relation, error) {
	ctx = withStoreAttribution(ctx)
	// Authorize BEFORE the peer-existence lookups (BUG-K6FEVB). A missing
	// peer must never let a write skip the ACL: if authz is deferred until
	// after GetEntity, a denied caller (e.g. --read-only / ReadOnlyACL)
	// gets a soft "entity not found" instead of a *acl.ForbiddenError,
	// and the dataentry fallback then writes directly to the store,
	// bypassing the ACL and audit. The source type feeds the type-level
	// grant check; it is best-effort (empty if the source doesn't exist
	// yet), mirroring UpdateRelation/DeleteRelation. Authorization must be
	// decided from inputs that don't depend on peer existence.
	var fromType string
	if fromEntity, ferr := anyFaceOf(ctx, m.deps.Store, from); ferr == nil {
		fromType = fromEntity.Type
	}
	// BEFORE the ACL, so an unvalidated coordinate never becomes an
	// authorization coordinate — and so the check still runs on the
	// bypassACL path, where authorizeAndAudit returns without consulting
	// any grant.
	if fErr := m.deps.requireRelationFaceFor(relType, fromType, opts.FromFace); fErr != nil {
		return nil, fErr
	}
	if aclErr := m.authorizeAndAudit(ctx, acl.WriteRequest{
		Op: acl.OpCreate,
		Subject: acl.RelationSubject{
			Type:     relType,
			FromType: fromType, FromID: from,
			FromFace: opts.FromFace,
		},
	}); aclErr != nil {
		return nil, aclErr
	}

	// anyFaceOf, not GetEntity: an endpoint is an ENTITY and only its type is
	// read here, so asking the zero coordinate would report every faced
	// entity as missing (BUG-HC6I2T).
	//
	// Both fail closed: a transient store error must not be reported as a
	// missing endpoint, which would read as an ordinary validation refusal.
	fromEntity, err := anyFaceOf(ctx, m.deps.Store, from)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("source %w: %s", ErrEntityNotFound, from)
	}
	toEntity, err := anyFaceOf(ctx, m.deps.Store, to)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("target %w: %s", ErrEntityNotFound, to)
	}
	if vErr := m.deps.Meta.ValidateRelation(relType, fromEntity.Type, toEntity.Type); vErr != nil {
		return nil, fmt.Errorf("invalid relation: %w", vErr)
	}
	// Keyed on the TAIL too: two edges on the same triple with different
	// tails are two relations, so a faced create must not be rejected by
	// the default face's edge (BUG-64MU2Q). Advisory either way — the
	// store's atomic create below is the real guard.
	if _, gErr := getRelationOnFace(ctx, m.deps.Store, from, opts.FromFace, relType, to); gErr == nil {
		return nil, fmt.Errorf("%w: %s --%s--> %s", ErrRelationAlreadyExists,
			entity.FormatStateRef(from, opts.FromFace), relType, to)
	}

	rel := entity.NewRelation(from, relType, to)
	rel.FromFace = opts.FromFace

	tmpl, err := m.deps.Templater.RelationTemplate(ctx, relType)
	if err != nil {
		return nil, fmt.Errorf("load relation template: %w", err)
	}
	if tmpl != nil {
		rel.Properties = templating.ApplyRelation(rel.Properties, tmpl)
	}

	if len(opts.Properties) > 0 && rel.Properties == nil {
		rel.Properties = make(map[string]any)
	}
	maps.Copy(rel.Properties, opts.Properties)
	if opts.Content != nil {
		rel.Content = *opts.Content
	}

	// Auto-assign managed order properties (_order_out / _order_in) when
	// the relation type declares the side orderable. Overrides any
	// non-finite caller-supplied value with AppendOrder over existing
	// siblings; keeps finite caller values as-is. The sibling scan and the
	// create share one Tx so two concurrent appends cannot both read the
	// same last value and land on the same position.
	//
	// CreateRelation, not upsert: a create must never fall through to an
	// update (that would clobber a racing create of the same triple).
	// The GetRelation pre-check above is advisory; the store's atomic
	// create is the real guard, and a conflict surfaces as
	// ErrRelationAlreadyExists (BUG-ZWTDH9).
	create := func(st store.Store) error {
		if err := m.assignManagedOrder(ctx, st, rel, relType); err != nil {
			return err
		}
		_, err := st.CreateRelation(ctx, from, relType, to, &store.RelationData{
			Properties: rel.Properties,
			Content:    rel.Content,
			FromFace:   opts.FromFace,
		})
		return err
	}
	var createErr error
	if relTypeIsOrdered(m.deps.Meta, relType) {
		createErr = m.deps.Store.Tx(ctx, create)
	} else {
		createErr = create(m.deps.Store)
	}
	if createErr != nil {
		if errors.Is(createErr, store.ErrConflict) {
			return nil, fmt.Errorf("%w: %s --%s--> %s", ErrRelationAlreadyExists,
				entity.FormatStateRef(from, opts.FromFace), relType, to)
		}
		return nil, createErr
	}
	m.recordRelationAudit(ctx, audit.OpCreateRelation, rel, "created")
	return rel, nil
}

// UpdateRelation merges new properties into an existing relation,
// applies MetaUnset, optionally replaces content, and persists.
// **No automation, no metamodel re-validation.**
func (m *Manager) UpdateRelation(
	ctx context.Context, from, relType, to string, opts entity.RelationOptions,
) (*entity.Relation, error) {
	ctx = withStoreAttribution(ctx)
	// Authorize BEFORE the relation-existence lookup (BUG-K6FEVB): a
	// missing relation must not let a denied caller skip the ACL and get
	// a soft not-found. The source type feeds the type-level grant check;
	// it is best-effort (empty if the source doesn't exist).
	var sourceType string
	if fromEntity, ferr := anyFaceOf(ctx, m.deps.Store, from); ferr == nil {
		sourceType = fromEntity.Type
	}
	// BEFORE the ACL, for the reason given in [Manager.CreateRelation].
	if fErr := m.deps.requireRelationFaceFor(relType, sourceType, opts.FromFace); fErr != nil {
		return nil, fErr
	}
	if aclErr := m.authorizeAndAudit(ctx, acl.WriteRequest{
		Op: acl.OpUpdate,
		Subject: acl.RelationSubject{
			Type:     relType,
			FromType: sourceType, FromID: from,
			FromFace: opts.FromFace,
		},
	}); aclErr != nil {
		return nil, aclErr
	}

	// Reject non-finite numeric values on managed order properties.
	// HTTP wire validators already cover the dataentry path; this is
	// the engine-level backstop for MCP/Lua/CLI write paths.
	relDef, hasDef := m.deps.Meta.Relations[relType]
	touchedOut := hasDef && relDef.OutgoingOrderProperty() != "" && touchesOrderKey(opts, relDef.OutgoingOrderProperty())
	touchedIn := hasDef && relDef.IncomingOrderProperty() != "" && touchesOrderKey(opts, relDef.IncomingOrderProperty())
	if hasDef {
		if err := validateOrderUpdate(opts, relDef); err != nil {
			return nil, err
		}
	}

	// Read, merge and write in one Tx: relations carry no version to
	// compare-and-swap on, so without it two concurrent updates naming
	// different properties would each write back the row they read and the
	// first update would be lost.
	var rel *entity.Relation
	var oldProps map[string]any
	err := m.deps.Store.Tx(ctx, func(view store.Store) error {
		// Addressed by TAIL as well as triple (BUG-64MU2Q): GetRelation reads
		// the default-tail edge, so on a faced source it would load a
		// DIFFERENT edge and the merge below would write the caller's
		// properties onto it.
		var gErr error
		rel, gErr = getRelationOnFace(ctx, view, from, opts.FromFace, relType, to)
		if gErr != nil {
			return fmt.Errorf("%w: %s --%s--> %s", ErrRelationNotFound,
				entity.FormatStateRef(from, opts.FromFace), relType, to)
		}

		// Snapshot pre-update meta keys so the audit summary names exactly
		// which keys changed (values never appear).
		oldProps = cloneProperties(rel.Properties)

		if rel.Properties == nil && (len(opts.Properties) > 0 || len(opts.MetaUnset) > 0) {
			rel.Properties = make(map[string]any)
		}
		maps.Copy(rel.Properties, opts.Properties)
		for _, k := range opts.MetaUnset {
			delete(rel.Properties, k)
		}
		if opts.Content != nil {
			rel.Content = *opts.Content
		}

		// UpdateRelation, not upsert: the read above established the triple
		// exists (else ErrRelationNotFound), so this is unambiguously an
		// update (BUG-ZWTDH9).
		_, wErr := view.UpdateRelationState(ctx, from, opts.FromFace, relType, to,
			store.RelationData{
				Properties: rel.Properties,
				Content:    rel.Content,
			})
		return wErr
	})
	if err != nil {
		return nil, err
	}
	m.recordRelationAudit(ctx, audit.OpUpdateRelation, rel, updateRelationSummary(oldProps, rel.Properties))

	// Engine-initiated renumber when an order PATCH collapsed sibling
	// spacing. Errors are operator-visible (slog.Error) but do not fail
	// the user-visible Update — the caller's write already succeeded.
	m.runRenumberAfterUpdate(ctx, from, to, relType, touchedOut, touchedIn)

	return rel, nil
}

// DeleteRelation removes the DEFAULT-tail edge of the triple.
// [Manager.DeleteRelationState] is the general form. **No automation.**
func (m *Manager) DeleteRelation(ctx context.Context, from, relType, to string) error {
	return m.DeleteRelationState(ctx, from, "", relType, to)
}

// DeleteRelationState removes the edge whose SOURCE face is exactly face
// (BUG-64MU2Q). The zero face addresses the default-tail edge.
//
// Separate from [Manager.DeleteRelation] for the reason the store draws the
// same line: the tail is part of a relation's identity, so a caller holding a
// state-tailed edge that drops the face does not delete "roughly the right
// edge" — it deletes the default face's, and reports success.
//
// **No automation.**
func (m *Manager) DeleteRelationState(
	ctx context.Context, from string, face entity.Face, relType, to string,
) error {
	// Authorize BEFORE touching the store (BUG-K6FEVB). The source type
	// feeds the type-level grant check; it is best-effort (empty if the
	// source doesn't exist).
	var sourceType string
	if fromEntity, ferr := anyFaceOf(ctx, m.deps.Store, from); ferr == nil {
		sourceType = fromEntity.Type
	}
	if aclErr := m.authorizeAndAudit(ctx, acl.WriteRequest{
		Op: acl.OpDelete,
		Subject: acl.RelationSubject{
			Type:     relType,
			FromType: sourceType, FromID: from,
			FromFace: face,
		},
	}); aclErr != nil {
		return aclErr
	}
	// Fetch pre-delete AFTER authz (BUG-K6FEVB: a denied delete must return
	// ForbiddenError regardless of whether the relation exists) so the audit
	// record and version snapshot carry the full Subject (relation type + from
	// + to). The relation may not exist — then the store delete returns an
	// error and we skip both the version capture and the audit.
	//
	// Addressed by tail: reading the default edge here would version and audit
	// a different relation than the one being deleted.
	rel, getErr := getRelationOnFace(ctx, m.deps.Store, from, face, relType, to)
	// Capture the final pre-delete version BEFORE the store delete, while the
	// live row (and its rel_record_id) still exists — the same order-before
	// rationale as entity delete. Skipped if the relation was already gone.
	if getErr == nil {
		m.recordRelationVersion(ctx, store.VersionOpDelete, rel, "", "", "")
	}
	if err := m.deps.Store.DeleteRelationState(ctx, from, face, relType, to); err != nil {
		return fmt.Errorf("delete relation: %w", err)
	}
	if getErr == nil {
		m.recordRelationAudit(ctx, audit.OpDeleteRelation, rel, "deleted")
	}
	return nil
}

// relTypeIsOrdered reports whether relType declares a managed order property
// on either side, so creating one reads its siblings.
func relTypeIsOrdered(meta *metamodel.Metamodel, relType string) bool {
	def, ok := meta.Relations[relType]
	return ok && (def.OutgoingOrderProperty() != "" || def.IncomingOrderProperty() != "")
}
