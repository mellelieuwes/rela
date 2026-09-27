package metamodel_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/metamodel"
)

const pactScope = "https://app.basecamp.com/5734045/buckets/35926565/todolists/10075319677"

// pactSchema returns a metamodel whose `scenario` type carries typeExtra (extra
// entity-level YAML, indented four spaces) and pacts (the body of its
// `pacts:` block, indented six spaces). An empty pacts omits the block.
func pactSchema(t *testing.T, typeExtra, pacts string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`version: "1"
entities:
  scenario:
    label: Scenario
    id_prefix: "SC-"
    aliases: [sc]
    properties:
      title: {type: string}
      status: {type: string}
      points: {type: integer}
      estimate: {type: integer}
      double: {type: integer, computed: entity.points * 2}
`)
	b.WriteString(typeExtra)
	if pacts != "" {
		b.WriteString("    pacts:\n")
		b.WriteString(pacts)
	}
	b.WriteString(`  person:
    label: Person
    id_prefix: "PERS-"
    properties:
      name: {type: string}
`)
	return b.String()
}

func parsePacts(t *testing.T, typeExtra, pacts string) (*metamodel.Metamodel, error) {
	t.Helper()
	return metamodel.Parse([]byte(pactSchema(t, typeExtra, pacts)))
}

func TestPacts_Load(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		typeExtra string
		pacts     string
		wantErr   string // "" = must load
	}{
		{
			name: "valid pact with every key",
			pacts: `      basecamp:
        scope: ` + pactScope + `
        theirs: [status, body]
        shared: [title]
        propose: [points, estimate]
        instructions: |
          One todo per scenario.
`,
		},
		{
			name:  "scope only: every field ours",
			pacts: "      gh:\n        scope: http://github.com/org/repo\n",
		},
		{
			name:  "theirs star alone",
			pacts: "      basecamp:\n        scope: " + pactScope + "\n        theirs: [\"*\"]\n",
		},
		{
			name:    "unknown key rejected with suggestion",
			pacts:   "      basecamp:\n        scope: " + pactScope + "\n        theris: [status]\n",
			wantErr: `pact: unknown key "theris" (did you mean "theirs"?)`,
		},
		{
			name:    "pact is not a mapping",
			pacts:   "      basecamp: [status]\n",
			wantErr: "pact: want a mapping",
		},
		{
			name:    "system id pattern",
			pacts:   "      Basecamp:\n        scope: " + pactScope + "\n",
			wantErr: `entity "scenario": pact "Basecamp": invalid system id`,
		},
		{
			name:    "system id too long",
			pacts:   "      " + strings.Repeat("a", 33) + ":\n        scope: " + pactScope + "\n",
			wantErr: "invalid system id",
		},
		{
			name:    "scope required",
			pacts:   "      basecamp:\n        theirs: [status]\n",
			wantErr: `pact "basecamp": scope is required`,
		},
		{
			name:    "scope not http",
			pacts:   "      basecamp:\n        scope: ftp://example.com/list\n",
			wantErr: `scope "ftp://example.com/list" is not an http(s) URL`,
		},
		{
			name:    "scope relative",
			pacts:   "      basecamp:\n        scope: todolists/1\n",
			wantErr: `scope "todolists/1" is not an http(s) URL`,
		},
		{
			name:    "unknown field in theirs suggests",
			pacts:   "      basecamp:\n        scope: " + pactScope + "\n        theirs: [titel]\n",
			wantErr: `theirs names unknown field "titel" (did you mean "title"?)`,
		},
		{
			name:    "unknown field in shared",
			pacts:   "      basecamp:\n        scope: " + pactScope + "\n        shared: [assignee]\n",
			wantErr: `shared names unknown field "assignee"`,
		},
		{
			name:    "unknown field in propose",
			pacts:   "      basecamp:\n        scope: " + pactScope + "\n        propose: [stat]\n",
			wantErr: `propose names unknown field "stat" (did you mean "status"?)`,
		},
		{
			name:    "computed property cannot be theirs",
			pacts:   "      basecamp:\n        scope: " + pactScope + "\n        theirs: [double]\n",
			wantErr: `theirs names computed property "double"`,
		},
		{
			name:    "computed property cannot be shared",
			pacts:   "      basecamp:\n        scope: " + pactScope + "\n        shared: [double]\n",
			wantErr: `shared names computed property "double"`,
		},
		{
			name:    "star mixed with named fields",
			pacts:   "      basecamp:\n        scope: " + pactScope + "\n        theirs: [\"*\", status]\n",
			wantErr: `theirs mixes "*" with named fields`,
		},
		{
			name:    "star in shared",
			pacts:   "      basecamp:\n        scope: " + pactScope + "\n        shared: [\"*\"]\n",
			wantErr: `"*" is only allowed in theirs, not in shared`,
		},
		{
			name:    "star in propose",
			pacts:   "      basecamp:\n        scope: " + pactScope + "\n        propose: [\"*\"]\n",
			wantErr: `"*" is only allowed in theirs, not in propose`,
		},
		{
			name: "shared beside theirs star",
			pacts: "      basecamp:\n        scope: " + pactScope +
				"\n        theirs: [\"*\"]\n        shared: [title]\n",
			wantErr: `shared must be empty when theirs is ["*"]`,
		},
		{
			name: "field in theirs and shared",
			pacts: "      basecamp:\n        scope: " + pactScope +
				"\n        theirs: [status]\n        shared: [status]\n",
			wantErr: `field "status" appears more than once across theirs and shared`,
		},
		{
			name:    "field twice in theirs",
			pacts:   "      basecamp:\n        scope: " + pactScope + "\n        theirs: [body, body]\n",
			wantErr: `field "body" appears more than once across theirs and shared`,
		},
		{
			name: "propose names a shared field",
			pacts: "      basecamp:\n        scope: " + pactScope +
				"\n        shared: [title]\n        propose: [title]\n",
			wantErr: `propose names shared field "title" — propose applies only to ours fields`,
		},
		{
			name: "propose names a theirs field",
			pacts: "      basecamp:\n        scope: " + pactScope +
				"\n        theirs: [status]\n        propose: [status]\n",
			wantErr: `propose names theirs field "status" — propose applies only to ours fields`,
		},
		{
			name: "propose names body when body is theirs",
			pacts: "      basecamp:\n        scope: " + pactScope +
				"\n        theirs: [body]\n        propose: [body]\n",
			wantErr: `propose names theirs field "body"`,
		},
		{
			name: "propose beside theirs star",
			pacts: "      basecamp:\n        scope: " + pactScope +
				"\n        theirs: [\"*\"]\n        propose: [title]\n",
			wantErr: `propose names "title", which theirs ["*"] hands to the external side`,
		},
		{
			name:    "propose names a computed property",
			pacts:   "      basecamp:\n        scope: " + pactScope + "\n        propose: [double]\n",
			wantErr: `propose names computed property "double"`,
		},
		{
			name:  "propose names body when body is ours",
			pacts: "      basecamp:\n        scope: " + pactScope + "\n        propose: [body]\n",
		},
		{
			name:    "propose lists a field twice",
			pacts:   "      basecamp:\n        scope: " + pactScope + "\n        propose: [status, status]\n",
			wantErr: `propose lists "status" more than once`,
		},
		{
			name:      "faces are not supported yet",
			typeExtra: "    faces:\n      draft: {}\n",
			pacts:     "      basecamp:\n        scope: " + pactScope + "\n",
			wantErr:   `entity "scenario": pacts are not supported yet on a type that declares faces`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parsePacts(t, tc.typeExtra, tc.pacts)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err, "a bad pact must fail the whole load")
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestPacts_BodyPropertyIsAmbiguous pins that `body` cannot mean both a
// declared property and the markdown content.
func TestPacts_BodyPropertyIsAmbiguous(t *testing.T) {
	t.Parallel()
	_, err := metamodel.Parse([]byte(`version: "1"
entities:
  note:
    label: Note
    id_prefix: "N-"
    properties:
      body: {type: string}
    pacts:
      basecamp:
        scope: ` + pactScope + `
`))
	require.Error(t, err)
	require.Contains(t, err.Error(), `entity "note": cannot declare pacts: it has a property named "body"`)
}

// TestPacts_ErrorsAreDeterministic pins sorted entity-then-system order, so
// identical loads report identical errors.
func TestPacts_ErrorsAreDeterministic(t *testing.T) {
	t.Parallel()
	doc := []byte(`version: "1"
entities:
  zeta:
    label: Zeta
    id_prefix: "Z-"
    properties:
      title: {type: string}
    pacts:
      zz: {scope: nope}
      aa: {scope: nope}
  alpha:
    label: Alpha
    id_prefix: "A-"
    properties:
      title: {type: string}
    pacts:
      mm: {scope: nope}
`)
	want := []string{
		`entity "alpha": pact "mm": scope "nope" is not an http(s) URL`,
		`entity "zeta": pact "aa": scope "nope" is not an http(s) URL`,
		`entity "zeta": pact "zz": scope "nope" is not an http(s) URL`,
	}
	for range 5 {
		_, err := metamodel.Parse(doc)
		var sve *metamodel.SchemaValidationError
		require.ErrorAs(t, err, &sve)
		require.Equal(t, want, sve.Errors)
	}
}

func TestPact_OwnerAndProposes(t *testing.T) {
	t.Parallel()
	m, err := parsePacts(t, "", `      basecamp:
        scope: `+pactScope+`
        theirs: [status, body]
        shared: [title]
        propose: [points, estimate]
        instructions: brief
`)
	require.NoError(t, err)
	pact, ok := metamodel.NewPactPolicy(m).Pact("scenario", "basecamp")
	require.True(t, ok)

	tests := []struct {
		field    string
		owner    metamodel.Owner
		proposes bool
		computed bool
	}{
		{"status", metamodel.OwnerTheirs, false, false},
		{metamodel.PactBodyField, metamodel.OwnerTheirs, false, false},
		{"title", metamodel.OwnerShared, false, false},
		{"points", metamodel.OwnerOurs, true, false},
		{"estimate", metamodel.OwnerOurs, true, false},
		{"double", metamodel.OwnerOurs, false, true},
		{"undeclared", metamodel.OwnerOurs, false, false},
	}
	for _, tc := range tests {
		require.Equal(t, tc.owner, pact.Owner(tc.field), "Owner(%q)", tc.field)
		require.Equal(t, tc.proposes, pact.Proposes(tc.field), "Proposes(%q)", tc.field)
		require.Equal(t, tc.computed, pact.Computed(tc.field), "Computed(%q)", tc.field)
	}

	require.Equal(t, "basecamp", pact.System())
	require.Equal(t, "scenario", pact.EntityType())
	require.Equal(t, pactScope, pact.Scope())
	require.Equal(t, "brief", pact.Instructions())
	require.Equal(t, []string{"body", "status"}, pact.Theirs(), "sorted")
	require.Equal(t, []string{"title"}, pact.Shared())
	require.Equal(t, []string{"estimate", "points"}, pact.Propose(), "sorted")

	// Accessors hand out copies: mutating one must not change the pact.
	pact.Theirs()[0] = "title"
	require.Equal(t, metamodel.OwnerShared, pact.Owner("title"))
}

// TestPact_TheirsStarOwnsEveryNonComputedField pins R14: "*" hands every
// field to the external side except computed properties, which rela derives
// and so always owns.
func TestPact_TheirsStarOwnsEveryNonComputedField(t *testing.T) {
	t.Parallel()
	m, err := parsePacts(t, "", "      basecamp:\n        scope: "+pactScope+
		"\n        theirs: [\"*\"]\n")
	require.NoError(t, err)
	pact, ok := metamodel.NewPactPolicy(m).Pact("scenario", "basecamp")
	require.True(t, ok)

	for _, f := range []string{"title", "status", "points", metamodel.PactBodyField, "anything"} {
		require.Equal(t, metamodel.OwnerTheirs, pact.Owner(f), "Owner(%q)", f)
		require.False(t, pact.Computed(f), "Computed(%q)", f)
	}
	require.Equal(t, metamodel.OwnerOurs, pact.Owner("double"), "a computed property is never theirs")
	require.True(t, pact.Computed("double"))
	require.False(t, pact.Proposes("title"))
	require.Equal(t, []string{metamodel.PactAllFields}, pact.Theirs(), "declared form is kept")
	require.Empty(t, pact.Shared())
}

func TestPact_ZeroValueOwnsNothing(t *testing.T) {
	t.Parallel()
	var p metamodel.Pact
	require.Equal(t, metamodel.OwnerOurs, p.Owner("title"))
	require.False(t, p.Proposes("title"))
	require.False(t, p.Computed("title"))
}

func TestOwner_String(t *testing.T) {
	t.Parallel()
	require.Equal(t, "ours", metamodel.OwnerOurs.String())
	require.Equal(t, "theirs", metamodel.OwnerTheirs.String())
	require.Equal(t, "shared", metamodel.OwnerShared.String())
}

func TestPactPolicy_ResolvesAliasesAndSortsSystems(t *testing.T) {
	t.Parallel()
	m, err := parsePacts(t, "", "      gh:\n        scope: https://github.com/o/r\n"+
		"      basecamp:\n        scope: "+pactScope+"\n")
	require.NoError(t, err)
	p := metamodel.NewPactPolicy(m)

	require.True(t, p.Enabled())
	require.Equal(t, []string{"basecamp", "gh"}, p.Systems("scenario"))
	require.Equal(t, []string{"basecamp", "gh"}, p.Systems("sc"), "alias resolves")

	pact, ok := p.Pact("sc", "gh")
	require.True(t, ok, "alias resolves like CommentPolicy.Commentable")
	require.Equal(t, "scenario", pact.EntityType(), "the compiled pact names the canonical type")

	_, ok = p.Pact("scenario", "jira")
	require.False(t, ok, "undeclared system")
	_, ok = p.Pact("person", "basecamp")
	require.False(t, ok, "type without pacts")
	require.Nil(t, p.Systems("person"))
	_, ok = p.Pact("no-such-type", "basecamp")
	require.False(t, ok)
}

func TestPactPolicy_AbsentPactsAreDisabled(t *testing.T) {
	t.Parallel()
	m, err := parsePacts(t, "", "")
	require.NoError(t, err)
	p := metamodel.NewPactPolicy(m)
	require.False(t, p.Enabled())
	require.Nil(t, p.Systems("scenario"))
	_, ok := p.Pact("scenario", "basecamp")
	require.False(t, ok)

	// A partially-wired path degrades to "no twins", not a panic.
	nilPolicy := metamodel.NewPactPolicy(nil)
	require.False(t, nilPolicy.Enabled())
	_, ok = nilPolicy.Pact("scenario", "basecamp")
	require.False(t, ok)
}
