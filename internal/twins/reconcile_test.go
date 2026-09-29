package twins_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/markdown"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/twins"
)

// pactOf compiles def as scenario's pact with basecamp. Scenario declares a
// computed property, age.
func pactOf(t *testing.T, def metamodel.PactDef) metamodel.Pact {
	t.Helper()
	def.Scope = "https://app.basecamp.com/1/buckets/2/todolists/3"
	m := &metamodel.Metamodel{Entities: map[string]metamodel.EntityDef{
		"scenario": {
			Properties: map[string]metamodel.PropertyDef{"age": {Computed: "1"}},
			Pacts:      map[string]metamodel.PactDef{"basecamp": def},
		},
	}}
	p, ok := metamodel.NewPactPolicy(m).Pact("scenario", "basecamp")
	require.True(t, ok)
	return p
}

// reconcile runs Reconcile for a twin that has an agreed base.
func reconcile(p metamodel.Pact, base, local twins.Snapshot, remote twins.Remote) twins.Plan {
	return twins.Reconcile(p, &base, local, remote)
}

// testPact owns status on their side, shares priority, and leaves title and
// estimate ours — title with foreign edits proposed rather than reverted.
func testPact(t *testing.T) metamodel.Pact {
	t.Helper()
	return pactOf(t, metamodel.PactDef{
		Theirs:  []string{"status"},
		Shared:  []string{"priority"},
		Propose: []string{"title"},
	})
}

// absent marks "no value" in a table row: the key is left out of the map.
type absentT struct{}

var absent = absentT{}

func props(field string, v any) map[string]any {
	if v == absent {
		return nil
	}
	return map[string]any{field: v}
}

// TestReconcile_FirstSync pins the rules without a base (a twin never
// synced): nobody can tell which side moved, so rela's owned values stand as
// changes to push, theirs are taken, and a shared difference is a conflict.
func TestReconcile_FirstSync(t *testing.T) {
	cases := []struct {
		name      string
		field     string
		l, r      any
		wantWrite any // absent = no write
		wantBase  any // absent = the field gets no base
		finding   twins.FindingKind
		state     twins.State
	}{
		{"theirs differs: taken", "status", "open", "done", "done", "done", "", twins.StateInSync},
		{"theirs equal: agreed", "status", "open", "open", absent, "open", "", twins.StateInSync},
		{"ours differs: push, not a foreign edit", "estimate", 3, 5, absent, 5, "", twins.StatePending},
		{"ours equal: agreed", "estimate", 3, 3.0, absent, 3, "", twins.StateInSync},
		{"shared differs: conflict", "priority", "low", "high", absent, absent, twins.FindingConflict,
			twins.StateConflict},
		{"shared equal: agreed", "priority", "low", "low", absent, "low", "", twins.StateInSync},
		{"theirs cleared remotely: unset", "status", "open", nil, nil, absent, "", twins.StateInSync},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			local := twins.Snapshot{Properties: map[string]any{tc.field: tc.l}}

			plan := twins.Reconcile(testPact(t), nil, local, twins.Remote{Properties: map[string]any{tc.field: tc.r}})

			switch tc.wantWrite {
			case absent:
				require.True(t, plan.Patch.IsEmpty())
			case nil:
				require.Equal(t, []string{tc.field}, plan.Patch.MetaUnset)
			default:
				require.Equal(t, map[string]any{tc.field: tc.wantWrite}, plan.Patch.Properties)
			}
			require.Equal(t, props(tc.field, tc.wantBase), plan.NewBase.Properties)
			require.Equal(t, tc.state, plan.State)
			if tc.finding == "" {
				require.Empty(t, plan.Findings)
			} else {
				require.Equal(t, []twins.Finding{
					{Field: tc.field, Kind: tc.finding, Base: nil, Ours: tc.l, Theirs: tc.r},
				}, plan.Findings)
			}
		})
	}

	t.Run("only what the remote reported becomes base", func(t *testing.T) {
		local := twins.Snapshot{Properties: map[string]any{"status": "open", "estimate": 3}, Body: "Steps"}

		plan := twins.Reconcile(testPact(t), nil, local, twins.Remote{Properties: map[string]any{"status": "open"}})

		require.Equal(t, map[string]any{"status": "open"}, plan.NewBase.Properties)
		require.Empty(t, plan.NewBase.Body)
		require.Equal(t, twins.StatePending, plan.State, "the estimate and body were never pushed")
	})

	t.Run("a body without a base", func(t *testing.T) {
		p := pactOf(t, metamodel.PactDef{Theirs: []string{"body"}})

		plan := twins.Reconcile(p, nil, twins.Snapshot{Body: "ours"}, twins.Remote{Body: new("theirs")})

		require.Equal(t, new("theirs"), plan.Patch.Content)
		require.Equal(t, "theirs", plan.NewBase.Body)
	})
}

// TestReconcile_SkipsComputed pins that a computed property is never
// reconciled: rela derives it, so a remote value for it is neither written nor
// reported, even under theirs ["*"].
func TestReconcile_SkipsComputed(t *testing.T) {
	for name, def := range map[string]metamodel.PactDef{
		"ours":       {Theirs: []string{"status"}},
		"theirs all": {Theirs: []string{"*"}},
	} {
		t.Run(name, func(t *testing.T) {
			snap := twins.Snapshot{Properties: map[string]any{"age": 3}}

			plan := reconcile(pactOf(t, def), snap, snap, twins.Remote{Properties: map[string]any{"age": 9}})

			require.True(t, plan.Patch.IsEmpty())
			require.Empty(t, plan.Findings)
			require.Equal(t, snap.Properties, plan.NewBase.Properties)
			require.Equal(t, twins.StateInSync, plan.State)
		})
	}

	t.Run("a computed property that changed locally needs no push", func(t *testing.T) {
		base := twins.Snapshot{Properties: map[string]any{"age": 3}}
		local := twins.Snapshot{Properties: map[string]any{"age": 4}}

		plan := reconcile(testPact(t), base, local, twins.Remote{})

		require.Equal(t, twins.StateInSync, plan.State)
	})
}

// TestReconcile_OwnershipMatrix walks every owner through every relation
// between local (L), base (B) and remote (R) for one field. Values: B is "a";
// "b" and "c" are edits.
func TestReconcile_OwnershipMatrix(t *testing.T) {
	type want struct {
		write   any // value written locally; absent = no write
		base    any // the field in NewBase
		finding twins.FindingKind
		state   twins.State
	}
	cases := []struct {
		name    string
		field   string
		l, r    any
		want    want
		comment string
	}{
		// theirs: the external side wins; a rela edit is drift.
		{"theirs L=B R=B", "status", "a", "a", want{absent, "a", "", twins.StateInSync}, ""},
		{"theirs L=B R≠B", "status", "a", "b", want{"b", "b", "", twins.StateInSync}, "remote change is taken"},
		{"theirs L≠B R=B", "status", "b", "a", want{"a", "a", twins.FindingLocalDrift, twins.StatePending},
			"drift is restored to the remote value"},
		{"theirs L≠B R=L", "status", "b", "b", want{absent, "b", "", twins.StateInSync}, "sides already agree"},
		{"theirs L≠B R≠B R≠L", "status", "b", "c", want{"c", "c", twins.FindingLocalDrift, twins.StatePending},
			"drift is overwritten anyway"},

		// ours: rela wins; a remote edit is reported, never written.
		{"ours L=B R=B", "estimate", "a", "a", want{absent, "a", "", twins.StateInSync}, ""},
		{"ours L=B R≠B", "estimate", "a", "b", want{absent, "a", twins.FindingForeignEdit, twins.StatePending}, ""},
		{"ours L≠B R=B", "estimate", "b", "a", want{absent, "a", "", twins.StatePending}, "a push is needed"},
		{"ours L≠B R=L", "estimate", "b", "b", want{absent, "b", "", twins.StateInSync}, "base follows agreement"},
		{"ours L≠B R≠B R≠L", "estimate", "b", "c", want{absent, "a", twins.FindingForeignEdit, twins.StatePending}, ""},

		// shared: whichever side moved wins; both moving differently conflicts.
		{"shared L=B R=B", "priority", "a", "a", want{absent, "a", "", twins.StateInSync}, ""},
		{"shared L=B R≠B", "priority", "a", "b", want{"b", "b", "", twins.StateInSync}, "remote change is taken"},
		{"shared L≠B R=B", "priority", "b", "a", want{absent, "a", "", twins.StatePending}, "local change needs a push"},
		{"shared L≠B R=L", "priority", "b", "b", want{absent, "b", "", twins.StateInSync}, "same edit on both sides"},
		{"shared L≠B R≠B R≠L", "priority", "b", "c", want{absent, "a", twins.FindingConflict, twins.StateConflict}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := twins.Snapshot{Properties: props(tc.field, "a")}
			local := twins.Snapshot{Properties: props(tc.field, tc.l)}
			plan := reconcile(testPact(t), base, local, twins.Remote{Properties: props(tc.field, tc.r)})

			if tc.want.write == absent {
				require.Empty(t, plan.Patch.Properties, tc.comment)
			} else {
				require.Equal(t, map[string]any{tc.field: tc.want.write}, plan.Patch.Properties, tc.comment)
			}
			require.Empty(t, plan.Patch.MetaUnset)
			require.Nil(t, plan.Patch.Content, "a property reconcile never touches the body")
			require.Empty(t, plan.Patch.ExpectedVersion, "the CAS token is the service's to set")
			require.Equal(t, map[string]any{tc.field: tc.want.base}, plan.NewBase.Properties)
			require.Equal(t, tc.want.state, plan.State)

			if tc.want.finding == "" {
				require.Empty(t, plan.Findings)
				return
			}
			require.Equal(t, []twins.Finding{{
				Field: tc.field, Kind: tc.want.finding, Base: "a", Ours: tc.l, Theirs: tc.r,
			}}, plan.Findings)
		})
	}
}

// TestReconcile_ProposeFlag pins that a foreign edit of an ours field listed in
// propose is reported as a proposal, and only then.
func TestReconcile_ProposeFlag(t *testing.T) {
	base := twins.Snapshot{Properties: map[string]any{"title": "a", "estimate": "a"}}
	remote := twins.Remote{Properties: map[string]any{"title": "b", "estimate": "b"}}

	plan := reconcile(testPact(t), base, base, remote)

	require.Equal(t, []twins.Finding{
		{Field: "estimate", Kind: twins.FindingForeignEdit, Base: "a", Ours: "a", Theirs: "b", Propose: false},
		{Field: "title", Kind: twins.FindingForeignEdit, Base: "a", Ours: "a", Theirs: "b", Propose: true},
	}, plan.Findings)
	require.True(t, plan.Patch.IsEmpty(), "a proposal is reported, never written")
}

// TestReconcile_AbsentVersusNil pins the Remote vocabulary: an absent key is
// "not mapped" and leaves the field alone; a present nil is "cleared on their
// side", written as an unset.
func TestReconcile_AbsentVersusNil(t *testing.T) {
	p := testPact(t)

	t.Run("an absent theirs field is untouched, even when rela drifted", func(t *testing.T) {
		base := twins.Snapshot{Properties: map[string]any{"status": "a"}}
		local := twins.Snapshot{Properties: map[string]any{"status": "b"}}

		plan := reconcile(p, base, local, twins.Remote{Properties: map[string]any{}})

		require.True(t, plan.Patch.IsEmpty())
		require.Empty(t, plan.Findings)
		require.Equal(t, base, plan.NewBase)
		require.Equal(t, twins.StateInSync, plan.State, "a theirs field never needs a push")
	})

	t.Run("an absent ours field that changed locally still needs a push", func(t *testing.T) {
		base := twins.Snapshot{Properties: map[string]any{"estimate": "a"}}
		local := twins.Snapshot{Properties: map[string]any{"estimate": "b"}}

		plan := reconcile(p, base, local, twins.Remote{})

		require.True(t, plan.Patch.IsEmpty())
		require.Equal(t, twins.StatePending, plan.State)
	})

	t.Run("a new local ours property needs a push", func(t *testing.T) {
		local := twins.Snapshot{Properties: map[string]any{"estimate": "new"}}

		plan := reconcile(p, twins.Snapshot{}, local, twins.Remote{})

		require.Equal(t, twins.StatePending, plan.State)
	})

	t.Run("a removed local ours property needs a push", func(t *testing.T) {
		base := twins.Snapshot{Properties: map[string]any{"estimate": "a"}}

		plan := reconcile(p, base, twins.Snapshot{}, twins.Remote{})

		require.Equal(t, twins.StatePending, plan.State)
	})

	t.Run("a theirs field cleared remotely is unset locally and dropped from the base", func(t *testing.T) {
		snap := twins.Snapshot{Properties: map[string]any{"status": "a", "estimate": "x"}}

		plan := reconcile(p, snap, snap, twins.Remote{Properties: map[string]any{"status": nil}})

		require.Empty(t, plan.Patch.Properties, "nil is never written as a value")
		require.Equal(t, []string{"status"}, plan.Patch.MetaUnset)
		require.Equal(t, map[string]any{"estimate": "x"}, plan.NewBase.Properties)
		require.Equal(t, twins.StateInSync, plan.State)
	})

	t.Run("a nil remote equals an absent local and base", func(t *testing.T) {
		plan := reconcile(p, twins.Snapshot{}, twins.Snapshot{},
			twins.Remote{Properties: map[string]any{"status": nil, "priority": nil, "estimate": nil}})

		require.True(t, plan.Patch.IsEmpty())
		require.Empty(t, plan.Findings)
		require.Empty(t, plan.NewBase.Properties)
		require.Equal(t, twins.StateInSync, plan.State)
	})

	t.Run("an ours field cleared remotely is a foreign edit", func(t *testing.T) {
		snap := twins.Snapshot{Properties: map[string]any{"estimate": "a"}}

		plan := reconcile(p, snap, snap, twins.Remote{Properties: map[string]any{"estimate": nil}})

		require.True(t, plan.Patch.IsEmpty())
		require.Equal(t, []twins.Finding{
			{Field: "estimate", Kind: twins.FindingForeignEdit, Base: "a", Ours: "a", Theirs: nil},
		}, plan.Findings)
	})

	t.Run("a shared field cleared remotely is unset when rela did not move", func(t *testing.T) {
		snap := twins.Snapshot{Properties: map[string]any{"priority": "high"}}

		plan := reconcile(p, snap, snap, twins.Remote{Properties: map[string]any{"priority": nil}})

		require.Equal(t, []string{"priority"}, plan.Patch.MetaUnset)
		require.Empty(t, plan.NewBase.Properties)
	})
}

// TestReconcile_Body pins that the body follows the same rules under the
// reserved field name, and compares the way canonical does.
func TestReconcile_Body(t *testing.T) {
	long := "A long paragraph that the markdown formatter will want to wrap, because it runs well past eighty columns."

	t.Run("a theirs body changed remotely is written", func(t *testing.T) {
		p := pactOf(t, metamodel.PactDef{Theirs: []string{"body"}})
		snap := twins.Snapshot{Body: "old"}

		plan := reconcile(p, snap, snap, twins.Remote{Body: new("new")})

		require.Equal(t, new("new"), plan.Patch.Content)
		require.Equal(t, "new", plan.NewBase.Body)
		require.Equal(t, twins.StateInSync, plan.State)
	})

	t.Run("a theirs body cleared remotely writes an empty body", func(t *testing.T) {
		p := pactOf(t, metamodel.PactDef{Theirs: []string{"body"}})
		snap := twins.Snapshot{Body: "old"}

		plan := reconcile(p, snap, snap, twins.Remote{Body: new("")})

		require.Equal(t, new(""), plan.Patch.Content)
		require.Empty(t, plan.NewBase.Body)
	})

	t.Run("a nil remote body is not mapped", func(t *testing.T) {
		p := pactOf(t, metamodel.PactDef{Theirs: []string{"body"}})

		plan := reconcile(p, twins.Snapshot{Body: "a"}, twins.Snapshot{Body: "b"}, twins.Remote{})

		require.Nil(t, plan.Patch.Content)
		require.Empty(t, plan.Findings)
		require.Equal(t, "a", plan.NewBase.Body)
	})

	t.Run("a reflowed local body is not drift", func(t *testing.T) {
		p := pactOf(t, metamodel.PactDef{Theirs: []string{"body"}})
		// The stored body is fsstore's reflowed form of what was pulled.
		reflowed := markdown.FormatMarkdown(long)
		require.NotEqual(t, long, reflowed, "precondition: the formatter must change this body")

		plan := reconcile(p, twins.Snapshot{Body: long}, twins.Snapshot{Body: reflowed}, twins.Remote{
			Body: new(long),
		})
		require.Nil(t, plan.Patch.Content)
		require.Empty(t, plan.Findings)
		require.Equal(t, twins.StateInSync, plan.State)
	})

	t.Run("an ours body edited remotely is a foreign edit", func(t *testing.T) {
		p := pactOf(t, metamodel.PactDef{Theirs: []string{"status"}})
		snap := twins.Snapshot{Body: "ours"}

		plan := reconcile(p, snap, snap, twins.Remote{Body: new("theirs")})

		require.Nil(t, plan.Patch.Content)
		require.Equal(t, []twins.Finding{
			{Field: "body", Kind: twins.FindingForeignEdit, Base: "ours", Ours: "ours", Theirs: "theirs"},
		}, plan.Findings)
	})

	t.Run("a shared body conflict", func(t *testing.T) {
		p := pactOf(t, metamodel.PactDef{Shared: []string{"body"}})

		plan := reconcile(p, twins.Snapshot{Body: "a"}, twins.Snapshot{Body: "b"}, twins.Remote{
			Body: new("c"),
		})

		require.Equal(t, twins.StateConflict, plan.State)
		require.Equal(t, "a", plan.NewBase.Body)
	})
}

// TestReconcile_TheirsAll pins `theirs: ["*"]`: every field, body included,
// is the external side's.
func TestReconcile_TheirsAll(t *testing.T) {
	p := pactOf(t, metamodel.PactDef{Theirs: []string{"*"}})
	snap := twins.Snapshot{Properties: map[string]any{"title": "a", "estimate": "a"}, Body: "a"}

	plan := reconcile(p, snap, snap, twins.Remote{
		Properties: map[string]any{"title": "b", "estimate": "b"},
		Body:       new("b"),
	})

	require.Equal(t, map[string]any{"title": "b", "estimate": "b"}, plan.Patch.Properties)
	require.Equal(t, new("b"), plan.Patch.Content)
	require.Empty(t, plan.Findings)
	require.Equal(t, twins.StateInSync, plan.State)
}

// TestReconcile_ValueEquality pins that representation differences between
// the agent's JSON and the stored YAML are never changes.
func TestReconcile_ValueEquality(t *testing.T) {
	p := testPact(t)
	cases := []struct {
		name  string
		field string
		l, r  any
	}{
		{"int vs whole float", "status", 3, 3.0},
		{"string list vs any list", "status", []string{"x", "y"}, []any{"x", "y"}},
		{"ours int vs whole float", "estimate", 5, 5.0},
		{"shared list", "priority", []any{"p1"}, []string{"p1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := twins.Snapshot{Properties: map[string]any{tc.field: tc.l}}

			plan := reconcile(p, snap, snap, twins.Remote{Properties: map[string]any{tc.field: tc.r}})

			require.True(t, plan.Patch.IsEmpty())
			require.Empty(t, plan.Findings)
			require.Equal(t, twins.StateInSync, plan.State)
		})
	}
}

// TestReconcile_StatePrecedence pins that a conflict outranks every other
// finding, and that findings land in field order.
func TestReconcile_StatePrecedence(t *testing.T) {
	base := twins.Snapshot{Properties: map[string]any{"priority": "a", "estimate": "a", "status": "a"}}
	local := twins.Snapshot{Properties: map[string]any{"priority": "b", "estimate": "a", "status": "b"}}
	remote := twins.Remote{Properties: map[string]any{"priority": "c", "estimate": "c", "status": "a"}}

	plan := reconcile(testPact(t), base, local, remote)

	require.Equal(t, twins.StateConflict, plan.State)
	kinds := make([]twins.FindingKind, len(plan.Findings))
	for i, f := range plan.Findings {
		kinds[i] = f.Kind
	}
	require.Equal(t, []twins.FindingKind{
		twins.FindingForeignEdit, twins.FindingConflict, twins.FindingLocalDrift,
	}, kinds, "estimate, priority, status")
	require.Equal(t, map[string]any{"status": "a"}, plan.Patch.Properties, "the drift is still restored")
}

// TestReconcile_DoesNotMutateInputs pins purity: the caller's snapshots are
// the stored twin and the read entity.
func TestReconcile_DoesNotMutateInputs(t *testing.T) {
	base := twins.Snapshot{Properties: map[string]any{"status": "a", "priority": "a"}}
	local := twins.Snapshot{Properties: map[string]any{"status": "a", "priority": "a"}}

	reconcile(testPact(t), base, local, twins.Remote{Properties: map[string]any{"status": nil, "priority": "b"}})

	require.Equal(t, map[string]any{"status": "a", "priority": "a"}, base.Properties)
	require.Equal(t, map[string]any{"status": "a", "priority": "a"}, local.Properties)
}
