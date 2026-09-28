package conditionlint

import (
	"context"
	"fmt"
	"sort"

	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/predicate"
	"github.com/Sourcehaven-BV/rela/internal/predicatefns"
)

// ViewConditionKind names the config surface a compiled condition came from,
// so a diagnostic can say `lists["open_tickets"]` rather than just the id —
// two surfaces may legitimately share one.
type ViewConditionKind string

const (
	ViewConditionList   ViewConditionKind = "lists"
	ViewConditionKanban ViewConditionKind = "kanbans"
	// ViewConditionAction is an action's `when:` (TKT-VVS16W). Its key ID is
	// [dataentryconfig.ActionConditionID]: one program per listed type.
	ViewConditionAction ViewConditionKind = "actions"
)

// ViewConditionKey identifies one compiled view condition.
type ViewConditionKey struct {
	Kind ViewConditionKind
	ID   string
}

// String renders the key the way the config reads, for diagnostics.
func (k ViewConditionKey) String() string { return fmt.Sprintf("%s[%q]", k.Kind, k.ID) }

// CompileViewConditions compiles the `condition:` of every list and kanban,
// and the `when:` of every detail-page action.
//
// Like [CompileNextActions] and unlike [Lint], this is AUTHORITATIVE: the
// programs returned here are the ones evaluated on the read path, so an
// expression that compiles will run and one that does not is a real error.
// Compiling at config load is what makes a bad condition fail at startup
// instead of silently emptying a view — the failure mode BUG-WHEREWIDE and
// BUG-F1LTV0 both record, in opposite directions.
//
// A view declares exactly one `entity_type`, so unlike a next-action source
// there is no multi-type question: the condition is compiled against that
// type and must succeed.
//
// The Env is the request-scoped profile (CompileWithCurrentUser), matching
// next actions: a view is rendered for one signed-in principal, so a
// condition may name `current_user` and the is_current_user /
// has_current_user sugar.
//
// Returns the programs keyed by surface, plus one message per problem. A view
// with no `condition:` is absent from the result, which the read path reads as
// "no constraint".
func CompileViewConditions(
	cfg *dataentryconfig.Config, meta *metamodel.Metamodel,
) (programs map[ViewConditionKey]*predicate.Program, problems []string) {
	if cfg == nil || meta == nil {
		return nil, nil
	}
	ev := predicatefns.NewEvaluator(meta)
	programs = make(map[ViewConditionKey]*predicate.Program)

	for id, v := range cfg.Lists {
		compileOne(ev, meta, ViewConditionKey{ViewConditionList, id},
			v.EntityType, v.Condition, programs, &problems)
	}
	for id, v := range cfg.Kanbans {
		compileOne(ev, meta, ViewConditionKey{ViewConditionKanban, id},
			v.EntityType, v.Condition, programs, &problems)
	}
	compileActionConditions(ev, meta, cfg, programs, &problems)

	sort.Strings(problems)
	if len(programs) == 0 {
		programs = nil
	}
	return programs, problems
}

// compileActionConditions compiles each detail-page action's `when:` once per
// type in its available_on.
//
// `related(...)` is refused. A detail action's when decides one button on one
// entity, and keeping it a pure check of that entity's own properties keeps
// the affordance and the POST gate a single cheap evaluation. A traversal
// would also need the page-level batching list conditions use, for a page of
// one row.
func compileActionConditions(
	ev *predicatefns.Evaluator, meta *metamodel.Metamodel, cfg *dataentryconfig.Config,
	programs map[ViewConditionKey]*predicate.Program, problems *[]string,
) {
	for id, a := range cfg.Actions {
		if a.When == "" || a.AvailableOn == nil {
			// A when without available_on is a structural error that
			// validateActions reports; there is no type to compile against.
			continue
		}
		for _, et := range a.AvailableOn.EntityTypes {
			if _, ok := meta.GetEntityDef(et); !ok {
				continue // reported by validateActions
			}
			key := ViewConditionKey{ViewConditionAction, dataentryconfig.ActionConditionID(id, et)}
			prog, err := ev.CompileWithCurrentUser(et, a.When)
			if err != nil {
				*problems = append(*problems, fmt.Sprintf(
					"actions[%q]: when does not compile against entity type %q: %v", id, et, err))
				continue
			}
			if len(prog.Traversals()) > 0 {
				*problems = append(*problems, fmt.Sprintf(
					"actions[%q]: %s(...) is not available in an action's when; it may only "+
						"test the entity's own properties", id, predicate.FuncRelated))
				continue
			}
			programs[key] = prog
		}
	}
}

// ViewConditionMatcher evaluates one view's compiled condition against a row.
//
// The entity type is fixed at compile time (a view declares exactly one), so
// unlike [NextActionMatcher] there is no per-type program map and no
// unreachable-type case to refuse.
type ViewConditionMatcher struct {
	ev         *predicatefns.Evaluator
	prog       *predicate.Program
	entityType string
}

// Matches evaluates the condition against the identity on ctx.
//
// An evaluation error is SURFACED, not swallowed as a non-match. A missing
// date property is an eval error, and treating it as "does not match" would
// silently narrow the view with no diagnostic — the inverse of BUG-WHEREWIDE
// and exactly the silent-wrong-answer class this feature exists to remove.
func (m *ViewConditionMatcher) Matches(ctx context.Context, e *entity.Entity) (bool, error) {
	if m == nil || e == nil {
		return false, nil
	}
	ok, err := m.ev.MatchesAs(ctx, m.prog, m.entityType, e.ID, e.Properties)
	if err != nil {
		return false, fmt.Errorf("conditionlint: evaluating view condition for %s: %w", e.ID, err)
	}
	return ok, nil
}

// MatchesWith is [ViewConditionMatcher.Matches] for a condition using
// `related(...)`: traversal answers each one for e. Nil is fine for a
// condition without one.
func (m *ViewConditionMatcher) MatchesWith(
	ctx context.Context, e *entity.Entity, traversal predicate.TraversalFunc,
) (bool, error) {
	if m == nil || e == nil {
		return false, nil
	}
	ok, err := m.ev.MatchesWithTraversals(ctx, m.prog, m.entityType, e.ID, e.Properties, traversal)
	if err != nil {
		return false, fmt.Errorf("conditionlint: evaluating view condition for %s: %w", e.ID, err)
	}
	return ok, nil
}

// Program returns the compiled condition, so a caller can answer its
// traversals for a batch of rows before evaluating them.
func (m *ViewConditionMatcher) Program() *predicate.Program { return m.prog }

// EntityAttributes returns the entity fields the condition reads. The
// compiler rejects dynamic attribute access, so the set is complete.
func (m *ViewConditionMatcher) EntityAttributes() []string { return m.prog.Attributes("entity") }

// EntityType returns the entity type the condition was compiled against.
func (m *ViewConditionMatcher) EntityType() string { return m.entityType }

// ViewConditionMatchers compiles every view condition and returns a lookup
// keyed by (kind, id) — the pair the config uses, since a list and a kanban
// may share an id.
//
// Returns nil when any condition fails to compile: the same config was
// already refused by validation, so a partial set would only be reachable in
// a deployment that skipped it, and evaluating SOME conditions while silently
// ignoring others is worse than evaluating none.
func ViewConditionMatchers(
	cfg *dataentryconfig.Config, meta *metamodel.Metamodel,
) (lookup func(kind, id string) (*ViewConditionMatcher, bool), problems []string) {
	programs, problems := CompileViewConditions(cfg, meta)
	if len(problems) > 0 {
		return nil, problems
	}
	if len(programs) == 0 {
		return nil, nil
	}
	ev := predicatefns.NewEvaluator(meta)
	matchers := make(map[ViewConditionKey]*ViewConditionMatcher, len(programs))
	for key, prog := range programs {
		matchers[key] = &ViewConditionMatcher{ev: ev, prog: prog, entityType: entityTypeFor(cfg, key)}
	}
	return func(kind, id string) (*ViewConditionMatcher, bool) {
		m, ok := matchers[ViewConditionKey{ViewConditionKind(kind), id}]
		return m, ok
	}, nil
}

// entityTypeFor reads back the entity type a key's view declares. Compilation
// already proved it non-empty and known, so this cannot fail for a key that
// reached the matcher map.
func entityTypeFor(cfg *dataentryconfig.Config, key ViewConditionKey) string {
	switch key.Kind {
	case ViewConditionList:
		return cfg.Lists[key.ID].EntityType
	case ViewConditionKanban:
		return cfg.Kanbans[key.ID].EntityType
	case ViewConditionAction:
		_, et, _ := dataentryconfig.SplitActionConditionID(key.ID)
		return et
	}
	return ""
}

// compileOne compiles a single surface's condition, appending a diagnostic
// rather than returning early so one bad view does not hide the next.
func compileOne(
	ev *predicatefns.Evaluator, meta *metamodel.Metamodel, key ViewConditionKey, entityType, condition string,
	programs map[ViewConditionKey]*predicate.Program, problems *[]string,
) {
	if condition == "" {
		return
	}
	if entityType == "" {
		// Structural validation reports the missing entity_type separately;
		// say why the condition specifically cannot be checked rather than
		// compiling against nothing and reporting a confusing attribute error.
		*problems = append(*problems, fmt.Sprintf(
			"%s: condition requires entity_type to be set", key))
		return
	}
	prog, err := ev.CompileWithCurrentUser(entityType, condition)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf(
			"%s: condition does not compile against entity type %q: %v", key, entityType, err))
		return
	}
	if err := predicatefns.ValidateTraversals(meta, entityType, prog); err != nil {
		*problems = append(*problems, fmt.Sprintf("%s: %v", key, err))
		return
	}
	programs[key] = prog
}
