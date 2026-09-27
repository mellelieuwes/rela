package affordances

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/predicate"
	"github.com/Sourcehaven-BV/rela/internal/predicatefns"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
)

// FieldVerdicts carries per-entity field-level affordance decisions.
// All maps are sparse: an absent key means the permissive default
// (writable / visible / option allowed). The data-entry adapter maps
// this onto its own wire-shape verdict type.
type FieldVerdicts struct {
	Writable map[string]bool
	Visible  map[string]bool
	Options  map[string]map[string]bool
	// Attribution maps a denied field (or "field=option") to the
	// role/grant that produced the deny, for the audit Summary channel
	// (DR-C5). Sparse — only denies appear; never surfaced on the wire.
	Attribution map[string]string
}

// RelationVerdicts carries per-entity relation-level decisions, sparse
// by relation type.
type RelationVerdicts struct {
	Types map[string]RelationVerdict
}

// RelationVerdict is the decision for one relation type.
type RelationVerdict struct {
	Creatable bool
	Removable bool
	Fields    map[string]bool
	// Attribution maps a denied dimension ("create", "remove",
	// "fields.<name>") to the role/grant that denied it.
	Attribution map[string]string
}

// PolicyResolver answers field/option/relation affordance queries from
// a compiled acl.yaml policy. Construct with [New]; safe for concurrent
// use.
type PolicyResolver struct {
	policy *acl.Policy
	meta   *metamodel.Metamodel
	lookup RelationLookup

	// declarative sources role attribution via acl.Declarative —
	// groups, containment, and typed-Source attribution. Required
	// (never nil after [New] returns); [New] rejects a nil argument.
	declarative *acl.Declarative

	// machines is the compiled enum state machines (TKT-E4LW2). Used by
	// [PolicyResolver.TransitionVerdicts] to resolve which transitions the
	// current principal can perform on an entity. May be nil / empty (a
	// metamodel with no transitions) — TransitionVerdicts then returns nil.
	machines *statemachine.Set

	// envs holds the compiled predicate env per entity type, reused
	// across grants of that type.
	envs map[string]*predicate.Env

	// grants is indexed by (role, entityType) → compiled grant blocks.
	grants map[grantKey]*compiledGrants

	// typesWithVisible records entity types for which ANY role declares a
	// `visible:` block. Under the historical marker ([WithHistoricalSubject])
	// such a type gets a type-level closed-world so a reader whose live roles
	// were reduced to globals-only (see resolveViaDeclarative) fails closed
	// rather than defaulting to all-visible (TKT-73C6B2).
	typesWithVisible map[string]bool

	// traversals answers `related(...)` in `when:`; nil unless
	// [WithTraversals] is passed to [New].
	traversals TraversalBinder
	// traversalProgs lists, per entity type, every compiled `when:` of any
	// role that uses related(), so one bind answers all of a row's grants.
	traversalProgs map[string][]*predicate.Program
}

type grantKey struct {
	role       string
	entityType string
}

// compiledGrants is the per-(role, type) bundle of compiled grants. A
// nil slice value distinguishes "block declared (opt-in)" from "block
// absent"; the presence flags record which blocks the role declared
// for this type.
type compiledGrants struct {
	fields    []compiledFieldGrant
	visible   []compiledFieldGrant
	options   []compiledOptionGrant
	relations []compiledRelationGrant

	declaredFields    bool
	declaredVisible   bool
	declaredOptions   bool
	declaredRelations bool
}

// New compiles the policy's affordance grants against the metamodel
// and returns a PolicyResolver. Every grant's `when:` predicate is
// compiled up front; all compile errors are collected and joined so an
// operator sees every failure in one pass (DR-S2). All arguments must
// be non-nil.
//
// The policy is read from `declarative.Policy()` — the same object
// the resolver uses for role attribution — so the two cannot drift
// (RR-WTLD). Callers that want an all-permissive resolver wire
// NopFieldVerdictResolver at the dispatch boundary instead.
//
// The full *metamodel.Metamodel is required (not a narrower slice):
// the resolver can be asked about any entity type at runtime, and for
// each it needs that type's property defs (to build the predicate env
// and coerce values) and the relation defs (to validate relation
// grant targets). Narrowing to a per-type view would just move the
// whole-metamodel dependency to the caller.
//
// The lookup argument is consumed by predicate host functions
// (has_relation, count_relations). acl.Graph and
// affordances.RelationLookup overlap on HasEdge but the latter
// carries OutgoingCounts; consolidating them is a follow-up cleanup.
func New(
	meta *metamodel.Metamodel,
	lookup RelationLookup, declarative *acl.Declarative, opts ...Option,
) (*PolicyResolver, error) {
	if meta == nil {
		return nil, errors.New("affordances: New: meta must be non-nil")
	}
	if lookup == nil {
		return nil, errors.New("affordances: New: lookup must be non-nil")
	}
	if declarative == nil {
		return nil, errors.New("affordances: New: declarative must be non-nil")
	}
	policy := declarative.Policy()
	r := &PolicyResolver{
		policy:           policy,
		meta:             meta,
		lookup:           lookup,
		declarative:      declarative,
		envs:             map[string]*predicate.Env{},
		grants:           map[grantKey]*compiledGrants{},
		typesWithVisible: map[string]bool{},
		traversalProgs:   map[string][]*predicate.Program{},
	}
	for _, opt := range opts {
		opt(r)
	}
	if policy == nil {
		return r, nil
	}

	var errs []error
	for roleName, role := range policy.Roles {
		errs = append(errs, r.compileRole(roleName, role)...)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return r, nil
}

// WithMachines injects the compiled enum state machines so
// [PolicyResolver.TransitionVerdicts] can resolve performable transitions.
// Optional and chainable: a resolver without machines (or with an empty set)
// answers TransitionVerdicts with nil, so existing callers that don't wire
// transitions are unaffected.
//
// CONCURRENCY: this is the one mutator on an otherwise-immutable-after-[New]
// resolver, so the "safe for concurrent use" guarantee holds only if it is
// called during single-threaded wiring, BEFORE the resolver is shared — which
// is the sole production call site (ResolverFromProfile, synchronous, before
// the resolver escapes). It is a setter rather than a [New] parameter
// deliberately: machines are optional and adding a required arg would churn all
// [New] callers. Never call WithMachines concurrently with TransitionVerdicts.
func (r *PolicyResolver) WithMachines(m *statemachine.Set) *PolicyResolver {
	r.machines = m
	// A transition verdict is served to principals (`_transitions`) and its
	// when: reads the raw graph, so it carries the same bit an ACL grant does.
	for _, entityType := range r.meta.EntityTypes() {
		for _, prop := range m.MachineProps(entityType) {
			for edge, prog := range m.TraversingWhens(entityType, prop) {
				warnConditionallyVisible(r.meta, r.policy, entityType,
					fmt.Sprintf("transition %s.%s %s when:", entityType, prop, edge), prog)
			}
		}
	}
	return r
}

// EntryValues returns, for each state-machine-typed property of entityType, the
// value a create must enter at (the machine's Initial, else Default — BUG-X1C7S)
// (TKT-3G93B8). A create form uses it to lock a machine field to its initial
// value: the field is not freely editable on create. Returns an empty map when
// no machines are wired or entityType has no machine-typed property.
func (r *PolicyResolver) EntryValues(entityType string) map[string]string {
	out := map[string]string{}
	if r.machines == nil || r.machines.Empty() {
		return out
	}
	for _, prop := range r.machines.MachineProps(entityType) {
		if entry := r.machines.EntryValue(entityType, prop); entry != "" {
			out[prop] = entry
		}
	}
	return out
}

// env returns (compiling on first use) the predicate env for an entity
// type. Envs are cached so every grant of a type shares one.
func (r *PolicyResolver) env(entityType string) (*predicate.Env, error) {
	if e, ok := r.envs[entityType]; ok {
		return e, nil
	}
	e, err := buildEnv(r.meta, entityType)
	if err != nil {
		// coverage-ignore: defensive: buildEnv only errors from DeclareVar/DeclareFunc on constant non-empty names into
		// a fresh env, which
		// cannot fail; no input reaches this branch
		return nil, err
	}
	r.envs[entityType] = e
	return e, nil
}

// compileRole compiles every grant block of one role across all the
// entity types it mentions, recording per-(role, type) compiled
// grants. Returns the collected compile errors (path-prefixed).
func (r *PolicyResolver) compileRole(roleName string, role acl.RoleDef) []error {
	var errs []error
	get := func(entityType string) *compiledGrants {
		k := grantKey{roleName, entityType}
		g := r.grants[k]
		if g == nil {
			g = &compiledGrants{}
			r.grants[k] = g
		}
		return g
	}

	for et, grants := range role.Fields {
		g := get(et)
		g.declaredFields = true
		errs = append(errs, r.compileFieldBlock(roleName, et, "fields", grants, &g.fields)...)
	}
	for et, grants := range role.Visible {
		g := get(et)
		g.declaredVisible = true
		r.typesWithVisible[et] = true
		errs = append(errs, r.compileFieldBlock(roleName, et, "visible", grants, &g.visible)...)
	}
	for et, grants := range role.Options {
		g := get(et)
		g.declaredOptions = true
		errs = append(errs, r.compileOptionBlock(roleName, et, grants, &g.options)...)
	}
	for et, grants := range role.Relations {
		g := get(et)
		g.declaredRelations = true
		for i, rg := range grants {
			errs = append(errs, r.compileRelationGrant(g, roleName, et, i, rg)...)
		}
	}
	return errs
}

// compileFieldBlock validates + compiles a fields/visible block,
// appending compiled grants to out. block is the YAML key for error
// paths ("fields" or "visible").
func (r *PolicyResolver) compileFieldBlock(
	roleName, entityType, block string, grants []acl.FieldGrant, out *[]compiledFieldGrant,
) []error {
	var errs []error
	for i, fg := range grants {
		if verr := r.validateField(roleName, entityType, block, i, fg.Field); verr != nil {
			errs = append(errs, verr)
			continue
		}
		prog, err := r.compile(roleName, entityType, block, i, fg.When)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		*out = append(*out, compiledFieldGrant{field: fg.Field, program: prog})
	}
	return errs
}

func (r *PolicyResolver) compileOptionBlock(
	roleName, entityType string, grants []acl.OptionGrant, out *[]compiledOptionGrant,
) []error {
	var errs []error
	for i, og := range grants {
		if verr := r.validateOption(roleName, entityType, i, og.Field, og.Option); verr != nil {
			errs = append(errs, verr)
			continue
		}
		prog, err := r.compile(roleName, entityType, "options", i, og.When)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		*out = append(*out, compiledOptionGrant{field: og.Field, option: og.Option, program: prog})
	}
	return errs
}

func (r *PolicyResolver) compileRelationGrant(
	g *compiledGrants, roleName, entityType string, i int, rg acl.RelationGrant,
) []error {
	var errs []error
	if verr := r.validateRelation(roleName, entityType, i, rg.Relation); verr != nil {
		// Unknown relation type: report and skip the whole grant —
		// none of its dimensions can meaningfully gate anything.
		return append(errs, verr)
	}
	prog, err := r.compile(roleName, entityType, "relations", i, rg.When)
	if err != nil {
		errs = append(errs, err)
	}
	cr := compiledRelationGrant{
		relation: rg.Relation,
		create:   rg.Create,
		remove:   rg.Remove,
		program:  prog,
	}
	metaFailed := false
	for j, fg := range rg.Fields {
		fprog, ferr := r.compile(roleName, entityType,
			fmt.Sprintf("relations[%d].fields", i), j, fg.When)
		if ferr != nil {
			errs = append(errs, ferr)
			metaFailed = true
			continue
		}
		cr.fields = append(cr.fields, compiledFieldGrant{field: fg.Field, program: fprog})
	}
	for j, fg := range rg.Visible {
		fprog, ferr := r.compile(roleName, entityType,
			fmt.Sprintf("relations[%d].visible", i), j, fg.When)
		if ferr != nil {
			errs = append(errs, ferr)
			metaFailed = true
			continue
		}
		cr.visible = append(cr.visible, compiledFieldGrant{field: fg.Field, program: fprog})
	}
	// Append the grant only when it compiled cleanly end-to-end. A
	// grant that lost a meta field to a compile error must not be
	// half-installed (S4): silently dropping the field would flip a
	// closed-world meta deny into permissive if New ever relaxed to
	// warn-and-continue. New currently hard-fails on any collected
	// error, so this is belt-and-suspenders.
	if err == nil && !metaFailed {
		g.relations = append(g.relations, cr)
	}
	return errs
}

// compile compiles one grant predicate. An empty `when` yields a nil
// program, which the evaluator (passes) treats as an unconditional
// grant — nil is the intended sentinel here, not an error-absent
// invalid value.
//
//nolint:nilnil // nil program is the documented "unconditional" sentinel
func (r *PolicyResolver) compile(roleName, entityType, block string, idx int, when string) (*predicate.Program, error) {
	if when == "" {
		return nil, nil
	}
	env, err := r.env(entityType)
	if err != nil {
		// coverage-ignore: defensive: r.env only errors when buildEnv errors, which cannot happen (constant env
		// declarations); unreachable
		return nil, fmt.Errorf("roles.%s.%s.%s[%d]: %w", roleName, block, entityType, idx, err)
	}
	prog, err := predicate.Compile(env, when)
	if err == nil && len(prog.Traversals()) > 0 {
		err = predicatefns.ValidateTraversals(r.meta, entityType, prog)
		if err == nil {
			err = refuseIdentityTraversal(prog)
		}
		if err == nil && r.traversals == nil {
			err = fmt.Errorf("%s(...) is not available: no store is wired to answer it", predicate.FuncRelated)
		}
		if err == nil {
			r.traversalProgs[entityType] = append(r.traversalProgs[entityType], prog)
			warnConditionallyVisible(r.meta, r.policy, entityType,
				fmt.Sprintf("acl.yaml roles.%s.%s.%s[%d]", roleName, block, entityType, idx), prog)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("roles.%s.%s.%s[%d].when: %w", roleName, block, entityType, idx, err)
	}
	return prog, nil
}

// FieldVerdicts computes the sparse field-level verdicts for e against
// the principal carried on ctx.
func (r *PolicyResolver) FieldVerdicts(ctx context.Context, e *entity.Entity) FieldVerdicts {
	out := FieldVerdicts{}
	if e == nil || r.policy == nil {
		return out
	}

	writable := newDimension()
	visible := newDimension()
	options := newOptionDimension()

	// Historical type-level closed-world (TKT-73C6B2): when serializing a
	// snapshot of a type that ANY role gates with `visible:`, force the visible
	// dimension to opt in up front. Ordinarily the `visible:` closed-world is
	// role-scoped — it bites only for roles the reader holds that declare a
	// block, so a reader with no such role sees all fields and is protected only
	// by the row-level read gate. For history that is not enough: the reader's
	// live roles are reduced to globals-only (resolveViaDeclarative), which can
	// drop the very role that declared the block, and the reduced set would
	// otherwise default to all-visible — leaking a field that was redacted at
	// write time. Opting in here means every field not affirmatively granted
	// visible by a globally-held role is hidden. Deliberately stricter than a
	// live read; a holder of acl.PermHistoryReadRedacted bypasses redaction
	// entirely at the handler.
	if isHistoricalSubject(ctx) && r.typesWithVisible[e.Type] {
		visible.optIn("hidden")
	}

	bc, roles := r.bindingFor(ctx, e)
	if bc != nil {
		for _, role := range roles {
			g := r.grants[grantKey{role, e.Type}]
			if g == nil {
				continue
			}
			if g.declaredFields {
				r.applyFieldGrants(ctx, bc, role, "read-only", g.fields, writable)
			}
			if g.declaredVisible {
				r.applyFieldGrants(ctx, bc, role, "hidden", g.visible, visible)
			}
			if g.declaredOptions {
				r.applyOptionGrants(ctx, bc, role, g.options, options)
			}
		}
	}

	// Client attenuation, field axis (TKT-IAC8TX). Applied AFTER every role
	// grant because a ceiling only ever removes: whatever the roles allowed,
	// this can subtract from, and nothing here can add. Applied BEFORE the
	// universe is resolved so a `visible:` ceiling gets the same closed-world
	// treatment a role-declared block does — which is what makes a property
	// added to the metamodel later hidden from a restricted client by default.
	r.applyClientCeiling(ctx, e.Type, visible)

	fieldUniverse := r.declaredFields(e.Type)
	out.Writable, out.Attribution = writable.deny(fieldUniverse, out.Attribution)
	visMap, attr := visible.deny(fieldUniverse, out.Attribution)
	out.Visible = visMap
	out.Attribution = attr
	out.Options, out.Attribution = options.deny(r.enumOptions(e.Type), out.Attribution)
	return out
}

// declaredFields returns the metamodel-declared property names for an
// entity type — the closed-world universe a fields/visible block
// denies from. Unknown type yields an empty set.
func (r *PolicyResolver) declaredFields(entityType string) []string {
	def, ok := r.meta.Entities[entityType]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(def.Properties))
	for name := range def.Properties {
		out = append(out, name)
	}
	return out
}

// enumOptions returns, per enum-typed field, its declared option
// values — the closed-world universe an options block denies from.
func (r *PolicyResolver) enumOptions(entityType string) map[string][]string {
	def, ok := r.meta.Entities[entityType]
	if !ok {
		return nil
	}
	out := map[string][]string{}
	for name, prop := range def.Properties {
		values := prop.Values
		if len(values) == 0 {
			if ct, ok := r.meta.Types[prop.Type]; ok {
				values = ct.Values
			}
		}
		if len(values) > 0 {
			out[name] = values
		}
	}
	return out
}

// RelationVerdicts computes the sparse relation-level verdicts for e.
func (r *PolicyResolver) RelationVerdicts(ctx context.Context, e *entity.Entity) RelationVerdicts {
	out := RelationVerdicts{}
	if e == nil || r.policy == nil {
		return out
	}
	bc, roles := r.bindingFor(ctx, e)
	if bc == nil {
		return out
	}

	acc := newRelationAccumulator()
	for _, role := range roles {
		g := r.grants[grantKey{role, e.Type}]
		if g == nil || !g.declaredRelations {
			continue
		}
		for _, rg := range g.relations {
			grantPassed := r.passes(ctx, bc, rg.program, role)
			metaPassed := r.metaFieldResults(ctx, bc, role, rg, grantPassed)
			acc.observe(role, rg, grantPassed, metaPassed)
		}
	}
	out.Types = acc.verdicts()
	return out
}

// RelationFieldVerdicts computes the sparse per-meta-field READ-visibility
// verdict for one relation edge: relType links FROM the entity `from`, and
// metaKeys are the property names actually present on the edge about to be
// serialized. The result is a sparse map keyed by meta field name; an absent
// key means visible (the permissive default), a `false` value means hidden.
// This is the relation-side analog of [PolicyResolver.FieldVerdicts]'s Visible
// dimension, resolved against the SAME bindings so has_relation / count_relations
// / has_role behave identically (TKT-B1F5Q1).
//
// Role grants are keyed by the FROM entity type (the source owns the relation
// grant block, matching [PolicyResolver.RelationVerdicts]). A relation
// `visible:` field is visible only when BOTH the whole relation grant's `when:`
// AND the field's own `when:` pass — a field can be more restrictive than its
// grant, never less (same rule as write-side metaFieldResults).
//
// The deny universe is metaKeys (the edge's actual property names) plus any
// grant-mentioned candidate — so redaction covers exactly what would otherwise
// reach the wire, including free-form meta keys not declared in the metamodel.
//
// This is always resolved against a LIVE source entity: the live relation GET,
// and relation history's live-source case (a deleted-source relation history
// serves no meta at all, so it never reaches here — IB-review #1). There is
// therefore no historical-subject / fail-closed-reconstruct handling on this
// path; redaction is simply "today's policy against the live source."
func (r *PolicyResolver) RelationFieldVerdicts(
	ctx context.Context, from *entity.Entity, relType string, metaKeys []string,
) map[string]bool {
	if from == nil || r.policy == nil {
		return nil
	}

	visible := newDimension()
	bc, roles := r.bindingFor(ctx, from)
	if bc != nil {
		for _, role := range roles {
			g := r.grants[grantKey{role, from.Type}]
			if g == nil || !g.declaredRelations {
				continue
			}
			for _, rg := range g.relations {
				if rg.relation != relType || len(rg.visible) == 0 {
					continue
				}
				visible.optIn("hidden")
				grantPassed := r.passes(ctx, bc, rg.program, role)
				for _, fg := range rg.visible {
					if grantPassed && r.passes(ctx, bc, fg.program, role) {
						visible.allow(fg.field)
					} else {
						visible.observeDeny(fg.field, role)
					}
				}
			}
		}
	}

	denied, _ := visible.deny(metaKeys, nil)
	return denied
}

// TransitionVerdicts resolves, per state-machine-typed property on e, the
// transitions the ctx principal can perform right now — guard held
// (subject-aware) AND `when:` precondition met — plus, for blocked edges, which
// gate blocked them (TKT-FT8J9). It is the read-side counterpart of the write
// path's transition enforcement, sharing the same evaluation via
// [statemachine.Set.Performable], so the "what can I do" answer here cannot
// disagree with what a write would accept.
//
// Returns an empty map when no machines are wired, e is nil, or e's type has no
// state-machine property. This is a bounded per-entity read (only e's machine
// fields and their out-edges are evaluated), which is why running the `when:`
// predicate here is consistent with the no-predicate-on-reads rule — see
// internal/entitymanager/CLAUDE.md.
func (r *PolicyResolver) TransitionVerdicts(
	ctx context.Context, e *entity.Entity,
) map[string][]statemachine.TransitionVerdict {
	out := map[string][]statemachine.TransitionVerdict{}
	if e == nil || r.machines == nil || r.machines.Empty() {
		return out
	}
	guard := r.transitionGuard(ctx)
	for _, prop := range r.machines.MachineProps(e.Type) {
		// Emit an entry for EVERY machine-typed property, even when there are no
		// performable out-edges (a terminal state, or all edges gated). The
		// presence of the key is how a consumer distinguishes "this field is a
		// state machine (render the status control, possibly empty)" from "not a
		// machine field (render the ordinary enum widget)". Dropping terminal
		// fields would make a done ticket's status fall back to a full enum
		// select that offers illegal moves — see TKT-3G93B8. A nil slice from
		// Performable normalizes to an empty (non-nil) slice so it serializes as
		// [] rather than null.
		vs := r.machines.Performable(ctx, e, prop, guard, r.lookup)
		if vs == nil {
			vs = []statemachine.TransitionVerdict{}
		}
		out[prop] = vs
	}
	return out
}

// transitionGuard adapts the resolver's ACL into the subject-aware
// [statemachine.Guard] the transition resolver needs, bound to the ctx
// principal. It resolves via the per-request [acl.Request] on ctx when present
// (reusing the list-handler scope), else opens one for the principal. Answers
// via [acl.Request.HoldsPermissionForEntity] so ownership-relation-conferred
// permissions are honored (the same subject-aware resolution the write-path
// guard uses).
//
// This guard fails CLOSED unconditionally: an unstamped/unresolvable principal
// holds no permission, so guarded transitions read as not-performable. Unlike
// the write-path guard (appbuild.transitionGuard), it has NO "no policy → inert
// allow" tier — because it is only ever wired under an active policy
// (ResolverFromProfile constructs the machine-backed resolver only when the
// policy declares affordance grants). Do NOT wire TransitionVerdicts from a
// no-policy/CLI path expecting inert behavior: there, an unstamped principal
// would be shown zero performable transitions even where a write would succeed
// inert. If such a caller is ever needed, add the policy-active tier here first.
func (r *PolicyResolver) transitionGuard(ctx context.Context) statemachine.Guard {
	req := acl.FromContext(ctx)
	if req == nil {
		var err error
		req, err = r.declarative.ForPrincipal(principal.From(ctx))
		if err != nil {
			req = nil // unstamped principal → holds no guarded permission
		}
	}
	return requestGuard{req: req}
}

// requestGuard evaluates a transition guard against an acl.Request. A nil
// request (unresolved/unstamped principal) holds no permission → guarded edges
// resolve as not-performable (fail closed). See PolicyResolver.transitionGuard
// for why there is no inert tier.
type requestGuard struct{ req *acl.Request }

func (g requestGuard) HoldsPermission(ctx context.Context, subjectID, permission string) bool {
	if g.req == nil {
		return false
	}
	return g.req.HoldsPermissionForEntity(ctx, subjectID, permission)
}

// bindingFor resolves the effective role set for (principal, entity)
// and builds the binding context shared across grant evaluations for
// this call. Returns nil bc when no policy roles apply.
//
// Role resolution flows through [acl.Declarative.ForPrincipal] /
// Request.ForEntity, which includes group expansion and containment
// inheritance.
func (r *PolicyResolver) bindingFor(ctx context.Context, e *entity.Entity) (bc *bindingContext, roles []string) {
	p := principal.From(ctx)
	global, roles := r.resolveViaDeclarative(ctx, p, e)
	if len(roles) == 0 {
		return nil, nil
	}
	entityRoles := make(map[string]bool, len(roles))
	for _, r := range roles {
		entityRoles[r] = true
	}
	bc = &bindingContext{
		principal:   p,
		entity:      e,
		entityRoles: entityRoles,
		globalRoles: global,
		lookup:      r.lookup,
		userID:      p.User,
		resolver:    r,
	}
	bc.userFuncs = predicatefns.CurrentUserBindings(bc.identity())
	return bc, roles
}

// applyClientCeiling subtracts the request's client-attenuation field ceiling
// (TKT-IAC8TX) from the visible dimension.
//
// It runs after every role grant and can only REMOVE — a ceiling never grants,
// so a field no role made visible cannot become visible here.
//
// The two spellings map onto the dimension's existing vocabulary rather than a
// parallel mechanism:
//
//   - `visible:` opts the dimension into CLOSED WORLD and allows exactly the
//     named fields, so everything else on the type — including a property added
//     to the metamodel tomorrow — is hidden. Same machinery, same semantics as
//     a role-declared `visible:` block.
//   - `redact:` denies the named fields only, leaving the rest alone.
//
// A denial attributed to the ceiling names the baseline rather than a role, so
// an operator debugging a hidden field can tell "no role grants this" apart
// from "this client is attenuated".
func (r *PolicyResolver) applyClientCeiling(ctx context.Context, entityType string, visible *dimension) {
	// Reuse the per-request scope when one is attached, else open a fresh
	// Request — mirroring resolveViaDeclarative below.
	//
	// The fallback is load-bearing, not defensive. Returning early here (the
	// original shape) made the ceiling depend on upstream wiring: role
	// resolution opened its own Request and the ceiling did not, so on a ctx
	// carrying a principal but no Request the roles applied and the
	// attenuation silently did NOT. That is the fail-open direction — a
	// restricted client keeping its user's full field visibility — and it is
	// invisible, because nothing errors and the roles still resolve.
	// visibility.PolicyRedactor forwards whatever ctx it is handed and binds
	// nothing itself, so the guarantee must live here.
	req := acl.FromContext(ctx)
	if req == nil {
		var err error
		req, err = r.declarative.ForPrincipal(principal.From(ctx))
		if err != nil {
			// Unstamped: no verified principal_type, so no baseline can match.
			// A ceiling only ever narrows, so there is nothing to narrow toward
			// and returning is correct — unlike the stamped-but-unbound case
			// above, which this recovers.
			return
		}
	}
	fc := req.FieldCeilingFor(entityType)
	if !fc.Constrains() {
		return
	}
	rule := "client-ceiling:" + fc.Baseline
	universe := r.declaredFields(entityType)

	if fc.Visible != nil {
		// Closed world. restrictTo INTERSECTS rather than unions — a field some
		// role already denied stays denied even though the ceiling names it,
		// because a ceiling may only remove.
		visible.restrictTo(fc.Visible, universe, rule)
		return
	}
	visible.redact(fc.Redact, universe, rule)
}

// resolveViaDeclarative resolves the effective role set for
// (principal, entity) via acl.Declarative. When ForPrincipal succeeds,
// attribution flows from the resolver (group expansion, containment
// inheritance, local roles). When it errors (the unstamped-principal
// case), the policy's `everyone` role still applies — by design
// `everyone` is held implicitly by every principal, authenticated or
// not (see [acl.EveryoneRole] doc). Without this fallback, anonymous
// read/visible grants would silently degrade to "no role at all" on
// the unstamped path.
func (r *PolicyResolver) resolveViaDeclarative(
	ctx context.Context, p principal.Principal, e *entity.Entity,
) (global map[string]bool, roles []string) {
	global = map[string]bool{}
	if r.policy == nil {
		// coverage-ignore-start: defensive: both entry points (FieldVerdicts/RelationVerdicts) short-circuit on
		// r.policy==nil before reaching
		// bindingFor, so resolveViaDeclarative is never called with a nil policy today
		// Defensive: FieldVerdicts / RelationVerdicts already short-circuit
		// when policy is nil, but bindingFor is now the sole entry point
		// and a future caller without that guard would otherwise panic on
		// r.policy.Roles below.
		return global, nil
		// coverage-ignore-end
	}
	// RR-JJYW: reuse the per-request Request scope when one is attached
	// to ctx (list handlers do this). Falls back to opening a fresh
	// Request per call when none is attached — back-compat for single-
	// entity handlers and tests.
	req := acl.FromContext(ctx)
	if req == nil {
		var err error
		req, err = r.declarative.ForPrincipal(p)
		if err != nil {
			// Unstamped principal: only `everyone` (if declared) applies.
			if _, ok := r.policy.Roles[acl.EveryoneRole]; ok {
				global[acl.EveryoneRole] = true
				roles = []string{acl.EveryoneRole}
			}
			return global, roles
		}
	}
	gr := req.Globals(ctx)
	for _, a := range gr.Attributions {
		global[a.Role] = true
	}
	// For a HISTORICAL subject ([WithHistoricalSubject]) the effective role set
	// is GLOBALS-ONLY: local roles (conferred by live `role_relations` edges)
	// and ancestor-conferred roles (via `inherit_roles_through` containment) are
	// resolved by ForEntity against the LIVE graph, which no longer describes
	// the entity as-of-version. Trusting them would let a role newly conferred
	// after capture flip a `visible:` grant OPEN in an old snapshot — both by
	// SELECTING which roles' grant blocks apply and by feeding has_role. Globals
	// are assignment-based (reader-side), so they are the safe tier. Dropping to
	// globals-only can leave a reader with FEWER roles than live; the type-level
	// historical closed-world in [PolicyResolver.FieldVerdicts] compensates so
	// the reduced role set fails closed rather than defaulting to all-visible
	// (TKT-73C6B2 / RR — the role-resolution leak).
	var attrs []acl.RoleAttribution
	if isHistoricalSubject(ctx) {
		attrs = gr.Attributions
	} else {
		attrs = req.ForEntity(ctx, e.Type, e.ID)
	}
	seen := make(map[string]bool, len(attrs))
	roles = make([]string, 0, len(attrs))
	for _, a := range attrs {
		if seen[a.Role] {
			continue
		}
		seen[a.Role] = true
		roles = append(roles, a.Role)
	}
	// Stable order so multi-role deny attribution is deterministic
	// (RR-QV18 / DR-S3). Matches the legacy effectiveRoles ordering
	// the affordance verdicts have always assumed.
	sort.Strings(roles)
	return global, roles
}

// passes reports whether a grant's predicate evaluates true. A nil
// program is unconditional (true). A predicate runtime error fails
// closed (false) with a slog.Warn for operator visibility (DR-S5). The
// caller ctx is threaded into Eval (and thus into the host-function
// calls the predicate makes) so cancellation / deadlines / request
// values propagate (TKT-9NOX / PR#825 pattern).
func (r *PolicyResolver) passes(
	ctx context.Context, bc *bindingContext, prog *predicate.Program, role string,
) bool {
	if prog == nil {
		return true
	}
	// A clause that reads the current user cannot be honored for a caller
	// who has none: an unidentified caller is not "the user with the empty
	// id", and `entity.owner == current_user.id` against an owner that is
	// itself empty would otherwise GRANT. Refuse the grant — the direction
	// every other identity gap in this codebase takes — rather than
	// evaluate it against a placeholder. Only the `everyone` role can bring
	// an unidentified caller here at all (resolveViaDeclarative), so this
	// costs an anonymous-readable workspace exactly its per-user clauses.
	if bc.identity() == "" && predicatefns.RequiresCurrentUser(prog) {
		return false
	}
	b, err := bc.newBindings(r.meta)
	if err != nil {
		// coverage-ignore-start: defensive: newBindings only errors from SetVar/SetFunc on constant non-empty names
		// with non-nil values, which
		// cannot fail; binding build never errors here
		slog.Warn("affordances: binding build failed; denying grant",
			"role", role, "entity", bc.entity.ID, "error", err)
		return false
		// coverage-ignore-end
	}
	if len(prog.Traversals()) > 0 {
		traversal, terr := r.traversalFor(ctx, bc.entity)
		if terr != nil {
			slog.Warn("affordances: related() not answered; denying grant",
				"role", role, "entity", bc.entity.ID, "error", terr)
			return false
		}
		b.SetTraversal(traversal)
	}
	v, err := prog.Eval(ctx, b)
	if err != nil {
		slog.Warn("affordances: predicate eval failed; denying grant",
			"role", role, "entity", bc.entity.ID, "error", err)
		return false
	}
	boolV, ok := v.(predicate.Bool)
	if !ok {
		// coverage-ignore-start: defensive: predicate.Compile rejects any program whose top-level expression is not
		// bool, so a compiled prog always
		// evaluates to predicate.Bool; this non-bool guard is unreachable
		slog.Warn("affordances: predicate did not return bool; denying grant",
			"role", role, "entity", bc.entity.ID)
		return false
		// coverage-ignore-end
	}
	return boolV.Bool()
}

// applyFieldGrants marks each granted field allowed when its predicate
// passes. ruleKind is the denial-rule label ("read-only" or "hidden")
// recorded in attribution for fields that end up denied.
func (r *PolicyResolver) applyFieldGrants(
	ctx context.Context, bc *bindingContext, role, ruleKind string,
	grants []compiledFieldGrant, dim *dimension,
) {
	dim.optIn(ruleKind)
	for _, fg := range grants {
		if r.passes(ctx, bc, fg.program, role) {
			dim.allow(fg.field)
		} else {
			dim.observeDeny(fg.field, role)
		}
	}
}

func (r *PolicyResolver) applyOptionGrants(
	ctx context.Context, bc *bindingContext, role string,
	grants []compiledOptionGrant, dim *optionDimension,
) {
	for _, og := range grants {
		dim.optIn(og.field)
		if r.passes(ctx, bc, og.program, role) {
			dim.allow(og.field, og.option)
		} else {
			dim.observeDeny(og.field, og.option, role)
		}
	}
}

// metaFieldResults evaluates each meta-field grant's own predicate.
// A meta field is allowed only when BOTH the whole-grant predicate
// AND the meta-field predicate pass — a meta field can be more
// restrictive than its relation grant, never less. Returns
// field → passed.
func (r *PolicyResolver) metaFieldResults(
	ctx context.Context, bc *bindingContext, role string, rg compiledRelationGrant, grantPassed bool,
) map[string]bool {
	if len(rg.fields) == 0 {
		return nil
	}
	out := make(map[string]bool, len(rg.fields))
	for _, fg := range rg.fields {
		out[fg.field] = grantPassed && r.passes(ctx, bc, fg.program, role)
	}
	return out
}
