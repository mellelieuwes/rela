package dataentry

import (
	"context"
	"log/slog"
	"slices"

	"github.com/Sourcehaven-BV/rela/internal/affordances"
	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	entityPkg "github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

// detailActionKeyPrefix prefixes an action id in the `_actions` map of a
// per-entity response (TKT-VVS16W). Verb keys never contain a colon, so the
// two cannot collide.
//
// The key is only ever emitted as `true`: absence means "not offered". That
// inverts the verb keys' rule, where absence means "render anyway". A verb is
// a fixed, known set the SPA renders by default; detail actions are an open
// set only the server can enumerate for an entity.
const detailActionKeyPrefix = "action:"

// detailActionCheck decides, for one entity and one principal, which actions
// with available_on apply. It is the SINGLE decision point for both the
// `_actions["action:<id>"]` affordance and the POST /_action/{id} gate, so the
// rendered button and the enforced boundary cannot drift (the
// commandAuthorizer shape). The caller has already row- and face-gated the
// entity; this does not repeat that.
//
// One check serves one operation. It holds the config snapshot the caller
// captured, and computes the hidden-property set, the redacted entity and the
// condition lookup at most once, however many actions it is asked about.
type detailActionCheck struct {
	svc    affordanceService
	schema *Schema
	e      *entityPkg.Entity

	hidden   map[string]struct{}
	redacted *entityPkg.Entity

	lookupDone bool
	lookup     ViewConditionLookup
}

// newDetailActionCheck prepares a check of e against the config snapshot s.
//
// Nil: s and e rejected — the caller has both, and a nil would only defer
// the failure into a verdict.
func (svc affordanceService) newDetailActionCheck(
	ctx context.Context, s *Schema, e *entityPkg.Entity,
) *detailActionCheck {
	hidden := svc.hiddenProperties(ctx, e)
	return &detailActionCheck{
		svc: svc, schema: s, e: e, hidden: hidden,
		redacted: visibility.Redact(ctx, fixedRedactor(hidden), e),
	}
}

// Redacted returns the entity as the principal may see it: hidden values
// removed and named in Redacted, which a script reads through
// entity:is_redacted().
func (c *detailActionCheck) Redacted() *entityPkg.Entity { return c.redacted }

// fixedRedactor is a [visibility.FieldRedactor] over an already computed
// hidden set. It lets the check redact through [visibility.Redact], the
// single redaction point, without deriving the field verdicts twice.
type fixedRedactor map[string]struct{}

// HiddenProperties implements [visibility.FieldRedactor].
func (f fixedRedactor) HiddenProperties(context.Context, *entityPkg.Entity) map[string]struct{} {
	return f
}

// Allows reports whether the action is offered on, and may run against, the
// check's entity: type and face match available_on, the principal holds the
// permission, and when holds.
//
// when is evaluated against the REDACTED entity, so a property the caller
// cannot see binds as unset. Evaluating the raw entity would make the
// button's presence a one-bit oracle on a hidden value.
//
// Fails closed: a `when` with no compiler wired, one that does not compile or
// errors on evaluation, or one that reads a hidden property, does not hold.
func (c *detailActionCheck) Allows(ctx context.Context, id string, action dataentryconfig.Action) bool {
	scope := action.AvailableOn
	if scope == nil {
		return false
	}
	if !slices.Contains(scope.EntityTypes, c.e.Type) {
		return false
	}
	if len(scope.Faces) > 0 && !slices.Contains(scope.Faces, string(c.e.Face)) {
		return false
	}
	if !holdsActionPermission(ctx, action) {
		return false
	}
	return action.When == "" || c.whenHolds(ctx, id)
}

// entityAttributeReader reports the entity fields a compiled condition reads.
type entityAttributeReader interface {
	EntityAttributes() []string
}

// whenHolds evaluates an action's compiled `when:` against the redacted
// entity.
func (c *detailActionCheck) whenHolds(ctx context.Context, id string) bool {
	m := c.matcher(id)
	if m == nil {
		return false
	}
	// A when that reads a hidden property fails closed. On the redacted
	// entity the property binds as unset, so `entity.x ~= 'y'` would pass on
	// an entity the operator meant to exclude. Field names are not secret, so
	// refusing reveals nothing about the value.
	attrs, ok := m.(entityAttributeReader)
	if !ok {
		slog.Warn("dataentry: detail action when does not report its fields; not offering it", "action", id)
		return false
	}
	for _, name := range attrs.EntityAttributes() {
		if _, h := c.hidden[name]; h {
			return false
		}
	}
	var gq store.GraphQueryer
	if q, ok := c.svc.store.(store.GraphQueryer); ok {
		gq = q
	}
	verdicts, err := m.MatchPage(ctx, []*entityPkg.Entity{c.redacted},
		traversalGateFromContext(ctx).GateTraversal, pageMatch(gq))
	if err != nil {
		slog.Warn("dataentry: evaluating detail action when failed; not offering it",
			"action", id, "entity", c.e.ID, "error", err)
		return false
	}
	return len(verdicts) == 1 && verdicts[0]
}

// matcher returns the compiled when of action id for the entity's type. It
// compiles the snapshot's conditions on first use. Nil fails closed.
func (c *detailActionCheck) matcher(id string) ViewConditionMatcher {
	if !c.lookupDone {
		c.lookupDone = true
		var compile ViewConditionFunc
		if c.svc.actionConditions != nil {
			compile = c.svc.actionConditions()
		}
		if compile == nil {
			slog.Warn("dataentry: detail action has a when but no condition compiler is wired; not offering it",
				"action", id)
			return nil
		}
		lookup, problems := compile(c.schema.Cfg, c.schema.Meta)
		if len(problems) > 0 {
			slog.Warn("dataentry: conditions did not compile; not offering actions with a when",
				"problems", problems)
			return nil
		}
		c.lookup = lookup
	}
	if c.lookup == nil {
		return nil
	}
	m := viewConditionFor(c.lookup, viewKindAction, dataentryconfig.ActionConditionID(id, c.e.Type))
	if m == nil {
		slog.Warn("dataentry: detail action when did not compile; not offering it", "action", id)
	}
	return m
}

// holdsActionPermission reports whether the caller holds the action's
// `permission:`. An empty permission holds for everyone.
//
// An intent gate, answered by the request read gate like a document's
// permission: under NopACL and ReadOnlyACL that gate holds every permission.
// The script's writes stay bounded by the caller's own ACL either way.
func holdsActionPermission(ctx context.Context, action dataentryconfig.Action) bool {
	if action.Permission == "" {
		return true
	}
	return readGateFromContext(ctx).HoldsPermission(ctx, action.Permission)
}

// computeDetailActions returns the `action:<id>` affordance keys offered on
// e's detail page, all true. Nil when none apply, and always nil for a past
// version: an action runs against the live entity, so a key on a snapshot
// would offer what the gate refuses.
func (svc affordanceService) computeDetailActions(ctx context.Context, e *entityPkg.Entity) map[string]bool {
	if svc.schema == nil || e == nil || affordances.IsHistoricalSubject(ctx) {
		return nil
	}
	s := svc.schema()
	if s == nil || s.Cfg == nil {
		return nil
	}
	var check *detailActionCheck
	var out map[string]bool
	for id, action := range s.Cfg.Actions {
		if action.AvailableOn == nil {
			continue
		}
		if check == nil {
			check = svc.newDetailActionCheck(ctx, s, e)
		}
		if !check.Allows(ctx, id, action) {
			continue
		}
		if out == nil {
			out = map[string]bool{}
		}
		out[detailActionKeyPrefix+id] = true
	}
	return out
}

// entityReadableInRequest reports whether the principal on ctx may read e at
// its face: the world does not block reads, the row gate admits it, and the
// face is readable. Every refusal is the same answer as a missing entity.
func entityReadableInRequest(ctx context.Context, e *entityPkg.Entity) bool {
	if worldFromContext(ctx).blocksAllReads() {
		return false
	}
	ok, err := readGateFromContext(ctx).PermitsRead(ctx, e.Type, e.ID)
	if err != nil || !ok {
		return false
	}
	return faceReadable(ctx, e.Type, e.Face)
}
