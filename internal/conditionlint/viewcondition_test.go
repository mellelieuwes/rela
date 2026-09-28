package conditionlint

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
)

// vcMeta mirrors the atlas `taak` shape the feature was designed against: an
// enum-ish status plus an OPTIONAL completion date, which is what makes the
// motivating rule disjunctive.
func vcMeta() *metamodel.Metamodel {
	return &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"taak": {Properties: map[string]metamodel.PropertyDef{
				"status":      {Type: metamodel.PropertyTypeString},
				"afgerond_op": {Type: metamodel.PropertyTypeDate},
				"labels":      {Type: metamodel.PropertyTypeString, List: true},
			}},
		},
	}
}

func TestCompileViewConditions(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *dataentryconfig.Config
		wantProgs int
		wantProb  string // substring; empty means "no problems"
	}{
		{
			name: "the atlas board rule compiles",
			cfg: &dataentryconfig.Config{Kanbans: map[string]dataentryconfig.Kanban{
				"taken_bord": {
					EntityType: "taak",
					Condition: "entity.status ~= 'gereed' or (entity.afgerond_op ~= nil " +
						"and days_between(today(), entity.afgerond_op) <= 2)",
				},
			}},
			wantProgs: 1,
		},
		{
			name: "a list condition compiles",
			cfg: &dataentryconfig.Config{Lists: map[string]dataentryconfig.List{
				"open": {EntityType: "taak", Condition: "entity.status ~= 'gereed'"},
			}},
			wantProgs: 1,
		},
		{
			name: "current_user is available",
			cfg: &dataentryconfig.Config{Lists: map[string]dataentryconfig.List{
				"mine": {EntityType: "taak", Condition: "is_current_user(entity.status)"},
			}},
			wantProgs: 1,
		},
		{
			name: "no condition compiles nothing and is not an error",
			cfg: &dataentryconfig.Config{Lists: map[string]dataentryconfig.List{
				"plain": {EntityType: "taak"},
			}},
			wantProgs: 0,
		},
		{
			name: "an unknown attribute is a problem, naming the view",
			cfg: &dataentryconfig.Config{Lists: map[string]dataentryconfig.List{
				"typo": {EntityType: "taak", Condition: "entity.stats ~= 'gereed'"},
			}},
			wantProb: `lists["typo"]: condition does not compile against entity type "taak"`,
		},
		{
			// The trap an author hits first: `!=` is filter syntax, the
			// condition dialect is Lua, so this must fail loudly at load.
			name: "the filter-dialect != spelling is refused",
			cfg: &dataentryconfig.Config{Kanbans: map[string]dataentryconfig.Kanban{
				"board": {EntityType: "taak", Condition: "entity.status != 'gereed'"},
			}},
			wantProb: `kanbans["board"]: condition does not compile`,
		},
		{
			name: "a non-boolean condition is refused",
			cfg: &dataentryconfig.Config{Lists: map[string]dataentryconfig.List{
				"scalar": {EntityType: "taak", Condition: "entity.status"},
			}},
			wantProb: `lists["scalar"]: condition does not compile`,
		},
		{
			name: "an unknown entity type is reported, not panicked on",
			cfg: &dataentryconfig.Config{Lists: map[string]dataentryconfig.List{
				"ghost": {EntityType: "nope", Condition: "entity.status ~= 'x'"},
			}},
			wantProb: `lists["ghost"]: condition does not compile against entity type "nope"`,
		},
		{
			name: "a missing entity_type says so rather than erroring obscurely",
			cfg: &dataentryconfig.Config{Lists: map[string]dataentryconfig.List{
				"bare": {Condition: "entity.status ~= 'x'"},
			}},
			wantProb: `lists["bare"]: condition requires entity_type to be set`,
		},
		{
			name: "related() over an unknown relation is refused",
			cfg: &dataentryconfig.Config{Kanbans: map[string]dataentryconfig.Kanban{
				"board": {EntityType: "taak", Condition: "related(entity, 'blocks')"},
			}},
			wantProb: `kanbans["board"]: related: `,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			progs, problems := CompileViewConditions(tc.cfg, vcMeta())
			if tc.wantProb != "" {
				require.NotEmpty(t, problems, "expected a problem")
				require.Contains(t, problems[0], tc.wantProb)
				return
			}
			require.Empty(t, problems)
			require.Len(t, progs, tc.wantProgs)
		})
	}
}

// A list and a kanban may share an id; the key must keep them apart so a
// diagnostic points at the right one and neither program is overwritten.
func TestCompileViewConditions_ListAndKanbanShareAnID(t *testing.T) {
	cfg := &dataentryconfig.Config{
		Lists: map[string]dataentryconfig.List{
			"taken": {EntityType: "taak", Condition: "entity.status ~= 'gereed'"},
		},
		Kanbans: map[string]dataentryconfig.Kanban{
			"taken": {EntityType: "taak", Condition: "entity.status ~= 'todo'"},
		},
	}
	progs, problems := CompileViewConditions(cfg, vcMeta())
	require.Empty(t, problems)
	require.Len(t, progs, 2)
	require.Contains(t, progs, ViewConditionKey{ViewConditionList, "taken"})
	require.Contains(t, progs, ViewConditionKey{ViewConditionKanban, "taken"})
}

func TestCompileViewConditions_NilInputs(t *testing.T) {
	progs, problems := CompileViewConditions(nil, vcMeta())
	require.Nil(t, progs)
	require.Empty(t, problems)

	progs, problems = CompileViewConditions(&dataentryconfig.Config{}, nil)
	require.Nil(t, progs)
	require.Empty(t, problems)
}

// An action's when compiles once per type in available_on (TKT-VVS16W).
func TestCompileViewConditions_ActionWhen(t *testing.T) {
	scope := &dataentryconfig.ActionScope{EntityTypes: []string{"taak"}}
	tests := []struct {
		name     string
		action   dataentryconfig.Action
		wantProg bool
		wantProb string
	}{
		{
			name:     "compiles against the listed type",
			action:   dataentryconfig.Action{Script: "a.lua", AvailableOn: scope, When: "entity.status == 'open'"},
			wantProg: true,
		},
		{
			name:     "current_user is available",
			action:   dataentryconfig.Action{Script: "a.lua", AvailableOn: scope, When: "is_current_user(entity.status)"},
			wantProg: true,
		},
		{
			name:     "unknown property is a problem",
			action:   dataentryconfig.Action{Script: "a.lua", AvailableOn: scope, When: "entity.nope == 'x'"},
			wantProb: `actions["regen"]: when does not compile against entity type "taak"`,
		},
		{
			name:     "syntax error is a problem",
			action:   dataentryconfig.Action{Script: "a.lua", AvailableOn: scope, When: "entity.status =="},
			wantProb: "when does not compile",
		},
		{
			name:     "related() is refused",
			action:   dataentryconfig.Action{Script: "a.lua", AvailableOn: scope, When: "related(entity, 'blocks')"},
			wantProb: "is not available in an action's when",
		},
		{
			name:   "when without available_on is left to structural validation",
			action: dataentryconfig.Action{Script: "a.lua", When: "entity.status == 'open'"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &dataentryconfig.Config{Actions: map[string]dataentryconfig.Action{"regen": tc.action}}
			progs, problems := CompileViewConditions(cfg, vcMeta())
			key := ViewConditionKey{ViewConditionAction, dataentryconfig.ActionConditionID("regen", "taak")}
			if tc.wantProb != "" {
				require.Len(t, problems, 1)
				require.Contains(t, problems[0], tc.wantProb)
				return
			}
			require.Empty(t, problems)
			_, has := progs[key]
			require.Equal(t, tc.wantProg, has)
		})
	}
}

// The matcher for an action key evaluates against the type encoded in its id.
func TestViewConditionMatchers_ActionWhen(t *testing.T) {
	cfg := &dataentryconfig.Config{Actions: map[string]dataentryconfig.Action{"regen": {
		Script: "a.lua", When: "entity.status == 'open'",
		AvailableOn: &dataentryconfig.ActionScope{EntityTypes: []string{"taak"}},
	}}}
	lookup, problems := ViewConditionMatchers(cfg, vcMeta())
	require.Empty(t, problems)
	m, ok := lookup(string(ViewConditionAction), dataentryconfig.ActionConditionID("regen", "taak"))
	require.True(t, ok)
	require.Equal(t, "taak", m.EntityType())
}
