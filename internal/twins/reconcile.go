package twins

import (
	"maps"
	"sort"

	"github.com/Sourcehaven-BV/rela/internal/canonical"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
)

// Remote is what the agent read from the external system, already translated
// into rela field values. Only the fields the agent maps are present: an
// absent key means "not mapped" and leaves the field alone, while a present
// nil means "cleared on their side". A nil Body is likewise "not mapped".
type Remote struct {
	Properties map[string]any
	Body       *string
}

// Plan is the outcome of [Reconcile]: what to write locally, the new agreed
// base, what could not be settled, and the resulting state.
type Plan struct {
	Patch    entity.Patch // local writes (theirs fields changed remotely; shared fields taken from remote)
	NewBase  Snapshot
	Findings []Finding
	State    State
}

// baseOp is what a field's reconcile does to the agreed base.
type baseOp int

const (
	keepBase  baseOp = iota // base stays as it was (without a base: stays absent)
	takeTheir               // base := remote value
	takeOurs                // base := local value (the sides agree again)
)

// decision is the per-field outcome of the pact rules, computed from the
// equalities alone so the table is exhaustive and independent of value types.
type decision struct {
	write   bool        // write the remote value locally
	base    baseOp      // what happens to the base
	finding FindingKind // "" when the field settles on its own
}

// decide applies the pact's ownership rules to one field with an agreed base.
// eqLB, eqRB and eqRL compare local to base, remote to base, and remote to
// local.
//
//   - theirs: a remote change is written locally and becomes the base; a local
//     change (only possible through a privileged write) is drift, restored to
//     the remote value.
//   - ours: rela wins. A differing remote value is a foreign edit, reported and
//     never written; once the sides agree the base follows.
//   - shared: whichever side moved wins; both moving to different values is a
//     conflict, left untouched for a human.
func decide(owner metamodel.Owner, eqLB, eqRB, eqRL bool) decision {
	switch owner {
	case metamodel.OwnerTheirs:
		if !eqRB {
			d := decision{write: !eqRL, base: takeTheir}
			if !eqLB && !eqRL {
				d.finding = FindingLocalDrift
			}
			return d
		}
		if !eqLB {
			return decision{write: true, base: keepBase, finding: FindingLocalDrift}
		}
		return decision{base: keepBase}
	case metamodel.OwnerShared:
		switch {
		case eqLB:
			return decision{write: !eqRL, base: takeTheir}
		case eqRB:
			return decision{base: keepBase}
		case eqRL:
			return decision{base: takeOurs}
		default:
			return decision{base: keepBase, finding: FindingConflict}
		}
	default: // ours
		switch {
		case eqRL:
			return decision{base: takeOurs}
		case !eqRB:
			return decision{base: keepBase, finding: FindingForeignEdit}
		default:
			return decision{base: keepBase}
		}
	}
}

// decideFirst applies the rules for a twin with no agreed base yet, where
// "which side moved" cannot be told and only eqRL is known.
//
//   - theirs: the remote value is taken.
//   - ours: rela's value stands. The remote value becomes the base, so the
//     difference reads as a local change to push — not as a foreign edit.
//   - shared: differing values conflict; the field gets no base until the
//     sides agree.
//
// Equal values are agreed on every side.
func decideFirst(owner metamodel.Owner, eqRL bool) decision {
	switch {
	case eqRL:
		return decision{base: takeOurs}
	case owner == metamodel.OwnerTheirs:
		return decision{write: true, base: takeTheir}
	case owner == metamodel.OwnerShared:
		return decision{base: keepBase, finding: FindingConflict}
	default: // ours
		return decision{base: takeTheir}
	}
}

// Reconcile decides, field by field, how the remote state folds into rela and
// into the agreed base. It is pure: nothing is read or written.
//
// base is nil for a twin that was never synced; the first-sync rules of
// decideFirst then apply, and the new base holds only what the remote reported.
//
// Only fields present in remote are reconciled (and the body only when
// remote.Body is set); every other field is left untouched. Computed
// properties are skipped: rela derives them, so neither side can own a change
// to one. Values compare through [canonical.EqualValue] and bodies through
// [canonical.EqualBody], so a representation difference (3 vs 3.0, a reflowed
// body) is never a change. An absent property equals nil. Writing nil means
// removing the property, so it lands in Patch.MetaUnset, and the base drops the
// key.
//
// The state is conflict when any conflict finding exists; otherwise pending
// when a foreign edit or local drift was found, or when an ours or shared
// field still differs locally from the new base (a push is needed); otherwise
// in sync. Findings list properties in name order, then the body.
func Reconcile(p metamodel.Pact, base *Snapshot, local Snapshot, remote Remote) Plan {
	var plan Plan
	if base != nil {
		plan.NewBase = Snapshot{Properties: maps.Clone(base.Properties), Body: base.Body}
	}
	after := Snapshot{Properties: maps.Clone(local.Properties), Body: local.Body}

	fields := make([]string, 0, len(remote.Properties))
	for f := range remote.Properties {
		if !p.Computed(f) {
			fields = append(fields, f)
		}
	}
	sort.Strings(fields)

	for _, f := range fields {
		l, r := local.Properties[f], remote.Properties[f]
		var b any
		var d decision
		if base == nil {
			d = decideFirst(p.Owner(f), canonical.EqualValue(r, l))
		} else {
			b = base.Properties[f]
			d = decide(p.Owner(f), canonical.EqualValue(l, b), canonical.EqualValue(r, b), canonical.EqualValue(r, l))
		}
		if d.write {
			patchProperty(&plan.Patch, f, r)
			setProperty(&after, f, r)
		}
		switch d.base {
		case takeTheir:
			setProperty(&plan.NewBase, f, r)
		case takeOurs:
			setProperty(&plan.NewBase, f, l)
		case keepBase:
			// NewBase starts as a copy of base (empty without one), so it already holds it.
		}
		plan.addFinding(p, f, d.finding, b, l, r)
	}

	if remote.Body != nil {
		l, r := local.Body, *remote.Body
		var b any
		var d decision
		if base == nil {
			d = decideFirst(p.Owner(metamodel.PactBodyField), canonical.EqualBody(r, l))
		} else {
			b = base.Body
			d = decide(p.Owner(metamodel.PactBodyField),
				canonical.EqualBody(l, base.Body), canonical.EqualBody(r, base.Body), canonical.EqualBody(r, l))
		}
		if d.write {
			plan.Patch.Content = &r
			after.Body = r
		}
		switch d.base {
		case takeTheir:
			plan.NewBase.Body = r
		case takeOurs:
			plan.NewBase.Body = l
		case keepBase:
			// NewBase starts as a copy of base (empty without one), so it already holds it.
		}
		plan.addFinding(p, metamodel.PactBodyField, d.finding, b, l, r)
	}

	plan.State = plan.state(p, after)
	return plan
}

// patchProperty adds a property write to patch; nil means removal, which a
// patch spells as MetaUnset. Fields arrive sorted, so MetaUnset comes out
// sorted.
func patchProperty(patch *entity.Patch, f string, v any) {
	if v == nil {
		patch.MetaUnset = append(patch.MetaUnset, f)
		return
	}
	if patch.Properties == nil {
		patch.Properties = map[string]any{}
	}
	patch.Properties[f] = v
}

// addFinding records a finding for field f unless kind is empty.
func (plan *Plan) addFinding(p metamodel.Pact, f string, kind FindingKind, b, l, r any) {
	if kind == "" {
		return
	}
	plan.Findings = append(plan.Findings, Finding{
		Field:   f,
		Kind:    kind,
		Base:    b,
		Ours:    l,
		Theirs:  r,
		Propose: kind == FindingForeignEdit && p.Proposes(f),
	})
}

// state derives the plan's state from its findings and from after, the local
// snapshot once the patch is applied.
func (plan *Plan) state(p metamodel.Pact, after Snapshot) State {
	if hasConflict(plan.Findings) {
		return StateConflict
	}
	if len(plan.Findings) > 0 || needsPush(p, plan.NewBase, after) {
		return StatePending
	}
	return StateInSync
}

func hasConflict(findings []Finding) bool {
	for _, f := range findings {
		if f.Kind == FindingConflict {
			return true
		}
	}
	return false
}

// pushable reports whether rela's value of field is the one the external side
// should hold: ours and shared fields, never theirs, never computed.
func pushable(p metamodel.Pact, field string) bool {
	return !p.Computed(field) && p.Owner(field) != metamodel.OwnerTheirs
}

// needsPush reports whether any pushable field of local differs from base — a
// rela value the external side does not hold yet. A theirs field never needs a
// push; a difference there is drift, which only a pull can settle.
func needsPush(p metamodel.Pact, base, local Snapshot) bool {
	if pushable(p, metamodel.PactBodyField) && !canonical.EqualBody(local.Body, base.Body) {
		return true
	}
	for f, l := range local.Properties {
		if pushable(p, f) && !canonical.EqualValue(l, base.Properties[f]) {
			return true
		}
	}
	for f, b := range base.Properties {
		if _, inLocal := local.Properties[f]; !inLocal && pushable(p, f) && !canonical.EqualValue(nil, b) {
			return true
		}
	}
	return false
}

// setProperty sets f in s, dropping the key for nil so an unset property and
// an absent one are stored alike.
func setProperty(s *Snapshot, f string, v any) {
	if v == nil {
		delete(s.Properties, f)
		return
	}
	if s.Properties == nil {
		s.Properties = map[string]any{}
	}
	s.Properties[f] = v
}
