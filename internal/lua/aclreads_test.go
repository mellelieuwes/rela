package lua_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/affordances"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager/entitymanagertest"
	"github.com/Sourcehaven-BV/rela/internal/lua"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
	"github.com/Sourcehaven-BV/rela/internal/tracer"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

// TKT-ZF2DTV / DEC-O59WM4: script reads are ACL-bound to the acting
// identity. These tests assert on what a script OBSERVES — the values that
// would land in an LLM prompt — using the real ACL + affordances engines
// over a memstore, not stubs.

// aclWorld is the fixture: a seeded store plus the wiring a script runtime
// gets on an identity-bearing path.
//
// Policy: alice may read ticket + person, but on `person` only `name` is
// visible — `salary` is hidden. `secret` entities she cannot read at all.
const aclWorldPolicy = `
roles:
  viewer:
    read: [ticket, person]
    visible:
      person:
        - field: name
assignments:
  alice: viewer
`

func aclWorldMeta() *metamodel.Metamodel {
	str := metamodel.PropertyDef{Type: metamodel.PropertyTypeString}
	return &metamodel.Metamodel{
		Entities: map[string]metamodel.EntityDef{
			"ticket": {Properties: map[string]metamodel.PropertyDef{"title": str, "cost": str}},
			"secret": {Properties: map[string]metamodel.PropertyDef{"title": str}},
			"person": {Properties: map[string]metamodel.PropertyDef{"name": str, "salary": str}},
		},
		Relations: map[string]metamodel.RelationDef{
			"owns": {From: []string{"ticket"}, To: []string{"person", "secret"}},
		},
	}
}

// newACLWorld seeds the graph and returns write deps whose reads are
// ACL-bound, exactly as appbuild/dataentry wire them in production.
func newACLWorld(t *testing.T) (store.Store, lua.WriteDeps) {
	t.Helper()
	ctx := context.Background()
	st := memstore.New()

	seed := []*entity.Entity{
		{ID: "TKT-1", Type: "ticket", Properties: map[string]any{"title": "Visible", "cost": "99"}},
		{ID: "SEC-1", Type: "secret", Properties: map[string]any{"title": "Classified"}},
		{ID: "P-1", Type: "person", Properties: map[string]any{"name": "Ann", "salary": "100000"}},
	}
	for _, e := range seed {
		if err := st.CreateEntity(ctx, e); err != nil {
			t.Fatalf("seed %s: %v", e.ID, err)
		}
	}
	for _, r := range [][3]string{{"TKT-1", "owns", "P-1"}, {"TKT-1", "owns", "SEC-1"}} {
		if _, err := st.CreateRelation(ctx, r[0], r[1], r[2], nil); err != nil {
			t.Fatalf("seed relation %v: %v", r, err)
		}
	}

	var policy acl.Policy
	if err := yaml.Unmarshal([]byte(aclWorldPolicy), &policy); err != nil {
		t.Fatalf("unmarshal policy: %v", err)
	}
	d, err := acl.NewDeclarative(&policy, acl.NewStoreGraph(st), st)
	if err != nil {
		t.Fatalf("NewDeclarative: %v", err)
	}
	resolver, err := affordances.New(aclWorldMeta(), noRelations{}, d)
	if err != nil {
		t.Fatalf("affordances.New: %v", err)
	}
	gate, err := visibility.NewDeclarativeGate(d)
	if err != nil {
		t.Fatalf("NewDeclarativeGate: %v", err)
	}
	redactor, err := visibility.NewPolicyRedactor(resolver)
	if err != nil {
		t.Fatalf("NewPolicyRedactor: %v", err)
	}
	reader, err := visibility.NewPolicyReader(gate, redactor, st)
	if err != nil {
		t.Fatalf("NewPolicyReader: %v", err)
	}
	scriptReader, err := visibility.NewScriptReader(reader, st, gate)
	if err != nil {
		t.Fatalf("NewScriptReader: %v", err)
	}
	visTracer, err := visibility.NewVisibleTracer(tracer.New(st), gate, redactor, st)
	if err != nil {
		t.Fatalf("NewVisibleTracer: %v", err)
	}

	return st, lua.WriteDeps{
		ReadDeps: lua.ReadDeps{
			VisibleReader: scriptReader,
			// RAW on purpose — the read-before-write handle. See AC6.
			Tracer:      visTracer,
			Meta:        aclWorldMeta(),
			ProjectRoot: t.TempDir(),
		},
		EntityManager: entitymanagertest.PanicOnUse{},
	}
}

// noRelations satisfies affordances.RelationLookup for a fixture whose
// policy uses no relation-dependent predicates.
type noRelations struct{}

func (noRelations) OutgoingCounts(context.Context, string) map[string]int { return nil }
func (noRelations) HasEdge(context.Context, string, string, string) bool  { return false }

// runAsAlice executes src under the fixture's restricted principal and
// returns what the script emitted — i.e. what would reach a prompt.
func runAsAlice(t *testing.T, deps lua.WriteDeps, src string) string {
	t.Helper()
	var out strings.Builder
	ctx := principal.With(context.Background(), principal.Principal{
		User: "alice", Tool: principal.ToolScheduler,
	})
	rt := lua.NewWriter(deps, &out, lua.WithContext(ctx), lua.WithPrincipal(principal.From(ctx)))
	defer rt.Close()
	if err := rt.RunString(src); err != nil {
		t.Fatalf("script error: %v\noutput so far: %s", err, out.String())
	}
	return out.String()
}

// TestScriptReads_HiddenFieldRedacted (AC1): a property the caller's policy
// hides never reaches the script — this is what keeps it out of a prompt.
func TestScriptReads_HiddenFieldRedacted(t *testing.T) {
	_, deps := newACLWorld(t)

	out := runAsAlice(t, deps, `
local p = rela.get_entity("P-1")
rela.output("name=" .. tostring(p.properties.name))
rela.output("salary=" .. tostring(p.properties.salary))
`)
	if !strings.Contains(out, "name=Ann") {
		t.Errorf("granted field missing: %q", out)
	}
	if !strings.Contains(out, "salary=nil") {
		t.Errorf("hidden field leaked to script: %q", out)
	}
}

// TestScriptReads_HiddenEntityInvisible (AC1/AC2): a row the caller cannot
// read is absent from both get_entity and list_entities.
func TestScriptReads_HiddenEntityInvisible(t *testing.T) {
	_, deps := newACLWorld(t)

	out := runAsAlice(t, deps, `
rela.output("get=" .. tostring(rela.get_entity("SEC-1")))
local n = 0
for _, e in ipairs(rela.list_entities("secret")) do n = n + 1 end
rela.output("listed=" .. n)
local t = 0
for _, e in ipairs(rela.list_entities("ticket")) do t = t + 1 end
rela.output("tickets=" .. t)
`)
	if !strings.Contains(out, "get=nil") {
		t.Errorf("hidden entity returned by get_entity: %q", out)
	}
	if !strings.Contains(out, "listed=0") {
		t.Errorf("hidden entity listed: %q", out)
	}
	if !strings.Contains(out, "tickets=1") {
		t.Errorf("readable type should still list: %q", out)
	}
}

// TestScriptReads_RelationsPeerGated (AC4, RR-7GDT1Y): a relation survives
// only when BOTH endpoints are visible — including under an explicit
// opts.from filter, where an empty result means "none you may see".
func TestScriptReads_RelationsPeerGated(t *testing.T) {
	_, deps := newACLWorld(t)

	out := runAsAlice(t, deps, `
local seen = {}
for _, r in ipairs(rela.get_relations({from = "TKT-1"})) do
  seen[#seen+1] = r.to
end
rela.output("peers=" .. table.concat(seen, ","))
`)
	if !strings.Contains(out, "peers=P-1") {
		t.Errorf("visible peer missing: %q", out)
	}
	if strings.Contains(out, "SEC-1") {
		t.Errorf("relation to hidden peer leaked: %q", out)
	}
}

// TestScriptReads_TraceGated (AC5): traversal prunes hidden nodes purely
// via the injected decorator — the bindings are unchanged.
func TestScriptReads_TraceGated(t *testing.T) {
	_, deps := newACLWorld(t)

	out := runAsAlice(t, deps, `
local function walk(node, acc)
  if node == nil then return acc end
  acc[#acc+1] = node.id
  for _, c in ipairs(node.children or {}) do walk(c, acc) end
  return acc
end
rela.output("nodes=" .. table.concat(walk(rela.trace_from("TKT-1", 2), {}), ","))
`)
	if !strings.Contains(out, "TKT-1") || !strings.Contains(out, "P-1") {
		t.Errorf("visible nodes missing from trace: %q", out)
	}
	if strings.Contains(out, "SEC-1") {
		t.Errorf("hidden node leaked into trace: %q", out)
	}
}

// TestScriptReads_UpdatePreservesHiddenProperties is AC6 — the
// data-destruction guard, and the single most important test in this
// ticket.
//
// The hazard: a read-modify-write whose READ is redacted produces a clone
// missing the caller's hidden properties, and the save then ERASES them.
//
// Since TKT-80EWGM the binding no longer performs that read at all — it
// builds an entity.Patch naming only what the script supplied and hands it
// to PatchEntity, which merges against the raw stored entity internally.
// The invariant this test pins is therefore now structural rather than
// conventional: there is no store handle on the binding to point at the
// wrong reader.
//
// The test is kept (and still asserts on the PERSISTED entity) because it
// is the end-to-end proof of the property that matters — a caller who
// cannot READ `salary` cannot ERASE it either — independent of which
// mechanism currently provides it. An earlier version asserting only on
// the deps handles would not have caught a regression here (RR-KYWIMZ).
func TestScriptReads_UpdatePreservesHiddenProperties(t *testing.T) {
	st, deps := newACLWorld(t)
	deps.EntityManager = &storeMutator{st: st}

	// alice may read `person` but not its `salary`. She updates `name`;
	// `salary` must survive untouched in the store.
	runAsAlice(t, deps, `rela.update_entity("P-1", {name = "Ann Updated"})`)

	after, err := st.GetEntity(context.Background(), "P-1")
	if err != nil {
		t.Fatalf("load after update: %v", err)
	}
	if got := after.Properties["salary"]; got != "100000" {
		t.Fatalf("update_entity ERASED the hidden property — the write-prep read is redacted; "+
			"salary=%v, full properties=%v", got, after.Properties)
	}
	if got := after.Properties["name"]; got != "Ann Updated" {
		t.Errorf("update did not apply: name=%v", got)
	}
}

// storeMutator is the minimal Mutator that actually persists, so the AC6
// test can assert on stored state rather than on a fixture's wiring.
type storeMutator struct{ st store.Store }

func (m *storeMutator) UpdateEntity(ctx context.Context, e *entity.Entity) (*entity.UpdateResult, error) {
	if err := m.st.UpdateEntity(ctx, e); err != nil {
		return nil, err
	}
	return &entity.UpdateResult{Entity: e}, nil
}

// PatchEntity mirrors the real manager: read the STORED entity, merge the
// patch onto it, save. The read is the raw store, which is exactly what
// entitymanager.PatchEntity does internally — so the AC6 test still
// exercises a genuine read-merge-save round trip and would still catch a
// redacted write-prep read.
func (m *storeMutator) PatchEntity(
	ctx context.Context, id string, p entity.Patch,
) (*entity.UpdateResult, error) {
	stored, err := m.st.GetEntity(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("entity not found: %s", id)
	}
	updated := stored.Clone()
	p.Apply(updated)
	if err := m.st.UpdateEntity(ctx, updated); err != nil {
		return nil, err
	}
	return &entity.UpdateResult{Entity: updated}, nil
}

func (m *storeMutator) CreateEntity(
	context.Context, *entity.Entity, entity.CreateOptions,
) (*entity.CreateResult, error) {
	return nil, errors.New("not used by this test")
}

func (m *storeMutator) DeleteEntity(context.Context, string, bool) (*entity.DeleteResult, error) {
	return nil, errors.New("not used by this test")
}

func (m *storeMutator) CreateRelation(
	context.Context, string, string, string, entity.RelationOptions,
) (*entity.Relation, error) {
	return nil, errors.New("not used by this test")
}

func (m *storeMutator) DeleteRelation(context.Context, string, string, string) error {
	return errors.New("not used by this test")
}

// TestScriptReads_EntityRefsGated closes the gap the IB review flagged
// (rela#1199): the RR-ZA452J fix — `rela.md.entity_refs` used
// context.Background(), so with a policy configured it resolved no
// principal and returned an EMPTY map for every user — shipped without a
// test exercising it under a real policy.
//
// Two things have to hold, and only asserting the second would let the
// original bug back in disguised as a pass:
//
//  1. entities the principal MAY read are present (the ctx carries a
//     usable identity — this is what the original defect broke), and
//  2. entities the principal may NOT read are absent (the gate applies).
func TestScriptReads_EntityRefsGated(t *testing.T) {
	_, deps := newACLWorld(t)

	out := runAsAlice(t, deps, `
local refs = rela.md.entity_refs()
local ids = {}
for id in pairs(refs) do ids[#ids+1] = id end
table.sort(ids)
rela.output("refs=" .. table.concat(ids, ","))
`)

	// alice may read ticket + person, not secret.
	if !strings.Contains(out, "TKT-1") || !strings.Contains(out, "P-1") {
		t.Errorf("readable entities missing from entity_refs — the binding resolved no "+
			"principal and fell closed for everyone (the RR-ZA452J regression): %q", out)
	}
	if strings.Contains(out, "SEC-1") {
		t.Errorf("entity_refs is NOT gated — a hidden entity leaked into the ref map: %q", out)
	}
}

// TestScriptReads_NilReaderDenies (AC9, RR-X9NVHI): a runtime wired
// without a reader DENIES. It must never fall back to the raw write-prep
// store — a forgotten wiring must not become an ACL bypass.
func TestScriptReads_NilReaderDenies(t *testing.T) {
	st := memstore.New()
	if err := st.CreateEntity(context.Background(),
		&entity.Entity{ID: "TKT-1", Type: "ticket", Properties: map[string]any{"title": "x"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	deps := lua.WriteDeps{
		ReadDeps: lua.ReadDeps{
			VisibleReader: nil, // the wiring omission under test
			Tracer:        tracer.New(st),
			Meta:          aclWorldMeta(),
			ProjectRoot:   t.TempDir(),
		},
		EntityManager: entitymanagertest.PanicOnUse{},
	}

	var out strings.Builder
	rt := lua.NewWriter(deps, &out)
	defer rt.Close()

	for _, src := range []string{
		`rela.get_entity("TKT-1")`,
		`rela.list_entities("ticket")`,
		`rela.get_relations({})`,
	} {
		if err := rt.RunString(src); err == nil {
			t.Errorf("%s with a nil reader succeeded — a wiring omission must deny, not fall back", src)
		}
	}
	if strings.Contains(out.String(), "TKT-1") {
		t.Errorf("nil-reader runtime leaked entity data: %q", out.String())
	}
}

// TestScriptReads_RedactedIsDistinguishableFromUnset (TKT-FJ6END): the
// motivating case. `salary` is hidden by policy and `nickname` was never
// set — both read as nil, so without a marker a script renders the same
// blank for "you may not see this" and "nobody filled it in".
func TestScriptReads_RedactedIsDistinguishableFromUnset(t *testing.T) {
	_, deps := newACLWorld(t)

	out := runAsAlice(t, deps, `
local p = rela.get_entity("P-1")
rela.output("salary_val=" .. tostring(p.properties.salary))
rela.output("salary_redacted=" .. tostring(p:is_redacted("salary")))
rela.output("nickname_val=" .. tostring(p.properties.nickname))
rela.output("nickname_redacted=" .. tostring(p:is_redacted("nickname")))
rela.output("name_redacted=" .. tostring(p:is_redacted("name")))
`)
	// Both values read nil...
	if !strings.Contains(out, "salary_val=nil") || !strings.Contains(out, "nickname_val=nil") {
		t.Fatalf("expected both to read nil: %q", out)
	}
	// ...but only the policy-hidden one is marked redacted.
	if !strings.Contains(out, "salary_redacted=true") {
		t.Errorf("hidden property not reported as redacted: %q", out)
	}
	if !strings.Contains(out, "nickname_redacted=false") {
		t.Errorf("never-set property wrongly reported as redacted: %q", out)
	}
	if !strings.Contains(out, "name_redacted=false") {
		t.Errorf("granted property wrongly reported as redacted: %q", out)
	}
}

// TestScriptReads_RedactedSetIsIterable pins the table form, for scripts
// that render every withheld field rather than probing one by name.
func TestScriptReads_RedactedSetIsIterable(t *testing.T) {
	_, deps := newACLWorld(t)

	out := runAsAlice(t, deps, `
local p = rela.get_entity("P-1")
local names = {}
for name in pairs(p.redacted) do names[#names+1] = name end
table.sort(names)
rela.output("redacted=" .. table.concat(names, ","))
`)
	if !strings.Contains(out, "redacted=salary") {
		t.Errorf("redacted set wrong: %q", out)
	}
}

// TestScriptReads_RedactedNeverCarriesValues is the security invariant:
// the marker discloses NAMES only. The value must appear nowhere.
func TestScriptReads_RedactedNeverCarriesValues(t *testing.T) {
	_, deps := newACLWorld(t)

	out := runAsAlice(t, deps, `
local p = rela.get_entity("P-1")
for name, v in pairs(p.redacted) do
  rela.output("entry=" .. name .. "/" .. tostring(v))
end
rela.output("direct=" .. tostring(p.properties.salary))
rela.output("via_prop=" .. tostring(p:prop("salary", "absent")))
`)
	// "100000" is P-1's salary in the fixture.
	if strings.Contains(out, "100000") {
		t.Fatalf("redacted VALUE leaked to script: %q", out)
	}
	if !strings.Contains(out, "entry=salary/true") {
		t.Errorf("redacted set should map name->true: %q", out)
	}
}

// TestScriptReads_ListEntitiesMarksRedacted covers the list path, which
// reaches Redact through a different branch (pushdown or Filter) than
// get_entity.
func TestScriptReads_ListEntitiesMarksRedacted(t *testing.T) {
	_, deps := newACLWorld(t)

	out := runAsAlice(t, deps, `
for _, e in ipairs(rela.list_entities("person")) do
  rela.output(e.id .. ":" .. tostring(e:is_redacted("salary")) .. ":" .. tostring(e.properties.salary))
end
`)
	if !strings.Contains(out, "P-1:true:nil") {
		t.Errorf("list path did not mark redaction: %q", out)
	}
}

// TestScriptReads_UngatedRuntimeReportsNothingRedacted pins the documented
// behavior of the operator-boundary runtimes (CLI, MCP, docs): they
// evaluate no policy, so nothing is redacted and every value is present.
// A false here means "not withheld", never "withheld but unreported".
func TestScriptReads_UngatedRuntimeReportsNothingRedacted(t *testing.T) {
	st, deps := newACLWorld(t)
	deps.VisibleReader = visibility.Unrestricted(st)

	out := runAsAlice(t, deps, `
local p = rela.get_entity("P-1")
rela.output("salary=" .. tostring(p.properties.salary))
rela.output("redacted=" .. tostring(p:is_redacted("salary")))
`)
	if !strings.Contains(out, "salary=100000") {
		t.Errorf("ungated runtime should see the raw value: %q", out)
	}
	if !strings.Contains(out, "redacted=false") {
		t.Errorf("ungated runtime should report nothing redacted: %q", out)
	}
}

// TestScriptWrites_ResultIsRedacted (RR-RKBUZU): the table a write returns is
// read back through the gated reader. The manager hands back the full stored
// entity, so returning it as-is would give a script every hidden property of
// any entity it may update.
func TestScriptWrites_ResultIsRedacted(t *testing.T) {
	st, deps := newACLWorld(t)
	deps.EntityManager = &storeMutator{st: st}

	out := runAsAlice(t, deps, `
local p = rela.update_entity("P-1", {name = "Ann Updated"})
rela.output("name=" .. tostring(p.properties.name))
rela.output("salary=" .. tostring(p.properties.salary))
`)
	if !strings.Contains(out, "name=Ann Updated") {
		t.Errorf("granted field missing from write result: %q", out)
	}
	if !strings.Contains(out, "salary=nil") {
		t.Errorf("hidden field leaked through the write result: %q", out)
	}
}

// TestScriptWrites_HiddenTargetIsNotFound (RR-T4G0S7): a write naming an
// entity the caller cannot read fails exactly like one naming an absent id.
func TestScriptWrites_HiddenTargetIsNotFound(t *testing.T) {
	st, deps := newACLWorld(t)
	deps.EntityManager = &storeMutator{st: st}

	run := func(id string) string {
		var out strings.Builder
		ctx := principal.With(context.Background(), principal.Principal{
			User: "alice", Tool: principal.ToolScheduler,
		})
		rt := lua.NewWriter(deps, &out, lua.WithContext(ctx), lua.WithPrincipal(principal.From(ctx)))
		defer rt.Close()
		err := rt.RunString(`rela.update_entity("` + id + `", {title = "x"})`)
		if err == nil {
			t.Fatalf("update_entity(%s) succeeded", id)
		}
		return strings.ReplaceAll(err.Error(), id, "ID")
	}
	if hidden, absent := run("SEC-1"), run("NOPE-1"); hidden != absent {
		t.Errorf("hidden and absent are distinguishable:\n hidden: %s\n absent: %s", hidden, absent)
	}
	after, err := st.GetEntity(context.Background(), "SEC-1")
	if err != nil {
		t.Fatalf("load SEC-1: %v", err)
	}
	if after.Properties["title"] != "Classified" {
		t.Errorf("hidden entity was written: %v", after.Properties)
	}
}
