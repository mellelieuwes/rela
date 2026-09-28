package entitymanager_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/autocascade"
	"github.com/Sourcehaven-BV/rela/internal/automation"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// cascadeRecheckMeta declares a `unique:` natural key on an
// auto-ID type, so a cascade can create one and an on-create
// automation can then set the constrained property.
const cascadeRecheckMeta = `version: "1.0"
entities:
  ticket:
    label: Ticket
    plural: tickets
    id_type: manual
    properties:
      title:
        type: string
  persoon:
    label: Persoon
    plural: personen
    id_prefix: PERS
    properties:
      email:
        type: string
        unique: true
      naam:
        type: string
`

// duplicateEmailAutomation sets the unique `email` to an
// already-taken value whenever a persoon is created.
func duplicateEmailAutomation() automation.Automation {
	return automation.Automation{
		Name: "person-gets-duplicate-email",
		On:   automation.Trigger{Entity: []string{"persoon"}, Created: true},
		Do:   []automation.Action{{Set: "email", Value: "taken@example.com"}},
	}
}

// newRecheckManager wires a manager with the supplied automations and
// seeds one persoon already holding "taken@example.com".
func newRecheckManager(t *testing.T, autos []automation.Automation) (*entitymanager.Manager, store.Store) {
	t.Helper()
	meta, err := metamodel.Parse([]byte(cascadeRecheckMeta))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	st := memstore.New()

	existing := entity.New("PERS-EXISTING", "persoon")
	existing.SetString("email", "taken@example.com")
	if seedErr := st.CreateEntity(context.Background(), existing); seedErr != nil {
		t.Fatalf("seed: %v", seedErr)
	}

	engine := automation.NewEngine(autos)
	runner, err := autocascade.New(autocascade.Deps{Engine: engine})
	if err != nil {
		t.Fatalf("autocascade.New: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       st,
		Meta:        meta,
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Automations: engine,
		Cascade:     runner,
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	return mgr, st
}

// countHolders returns how many personen hold the guarded address.
func countHolders(t *testing.T, st store.Store) int {
	t.Helper()
	n := 0
	for e, err := range st.ListEntities(context.Background(), store.EntityQuery{Type: "persoon"}) {
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if e.GetString("email") == "taken@example.com" {
			n++
		}
	}
	return n
}

// TestCascadeWrite_UniqueRecheckedAfterAutomation is the BUG-KIMZRK
// regression test.
//
// `createCore` enforces `unique:` against the PRE-automation candidate. An
// on-create automation then sets the constrained property and the cascade
// re-writes the entity. Without a post-automation re-check that duplicate
// persists — silently, with no error and no AutomationErrors entry.
//
// The asymmetry is the defect: the top-level create path already re-runs
// checkUniqueProperties against post-automation values (manager.go, "the
// create path must not be the weaker one"). The cascade path must not be
// weaker either.
func TestCascadeWrite_UniqueRecheckedAfterAutomation(t *testing.T) {
	t.Parallel()
	mgr, st := newRecheckManager(t, []automation.Automation{
		{
			Name: "ticket-creates-person",
			On:   automation.Trigger{Entity: []string{"ticket"}, Created: true},
			Do: []automation.Action{{
				CreateEntity: &automation.CreateEntityAction{
					Type:       "persoon",
					Properties: map[string]string{"naam": "New Person"},
				},
			}},
		},
		duplicateEmailAutomation(),
	})

	trigger := entity.New("TKT-1", "ticket")
	trigger.SetString("title", "Trigger")
	res, err := mgr.CreateEntity(context.Background(), trigger, entity.CreateOptions{ID: "TKT-1"})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	if got := countHolders(t, st); got != 1 {
		t.Errorf("%d entities hold the unique email, want 1 — "+
			"an automation-set value bypassed the unique constraint", got)
	}

	// The rejection must be SURFACED, not swallowed. A silent skip would
	// leave the caller believing the cascade fully succeeded.
	if len(res.AutomationErrors) == 0 {
		t.Error("cascade reported no automation errors — a refused write must be surfaced")
	} else if !strings.Contains(strings.ToLower(strings.Join(res.AutomationErrors, " ")), "unique") {
		t.Errorf("automation errors do not mention the unique violation: %v", res.AutomationErrors)
	}
}

// TestTopLevelCreate_UniqueRecheckedAfterAutomation is the CONTROL.
//
// The defect is an ASYMMETRY between two paths, so pinning only the cascade
// side would miss a future regression that weakened both. This asserts the
// top-level path stays strict.
func TestTopLevelCreate_UniqueRecheckedAfterAutomation(t *testing.T) {
	t.Parallel()
	mgr, st := newRecheckManager(t, []automation.Automation{duplicateEmailAutomation()})

	p := entity.New("", "persoon")
	p.SetString("naam", "Direct")
	_, err := mgr.CreateEntity(context.Background(), p, entity.CreateOptions{})
	if err == nil {
		t.Error("top-level create accepted an automation-set duplicate of a unique property")
	}

	if got := countHolders(t, st); got != 1 {
		t.Errorf("%d entities hold the unique email, want 1", got)
	}
}

// TestCascadeWrite_ValidAutomationValueStillLands guards against
// over-correcting: the re-check must reject only genuine violations. An
// automation setting an unconstrained property must still work.
func TestCascadeWrite_ValidAutomationValueStillLands(t *testing.T) {
	t.Parallel()
	mgr, st := newRecheckManager(t, []automation.Automation{
		{
			Name: "ticket-creates-person",
			On:   automation.Trigger{Entity: []string{"ticket"}, Created: true},
			Do: []automation.Action{{
				CreateEntity: &automation.CreateEntityAction{
					Type:       "persoon",
					Properties: map[string]string{"naam": "New Person"},
				},
			}},
		},
		{
			Name: "person-gets-unique-email",
			On:   automation.Trigger{Entity: []string{"persoon"}, Created: true},
			Do:   []automation.Action{{Set: "email", Value: "fresh@example.com"}},
		},
	})

	trigger := entity.New("TKT-1", "ticket")
	trigger.SetString("title", "Trigger")
	res, err := mgr.CreateEntity(context.Background(), trigger, entity.CreateOptions{ID: "TKT-1"})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if len(res.AutomationErrors) != 0 {
		t.Errorf("a legitimate automation write was refused: %v", res.AutomationErrors)
	}

	found := false
	for e, err := range st.ListEntities(context.Background(), store.EntityQuery{Type: "persoon"}) {
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if e.GetString("email") == "fresh@example.com" {
			found = true
		}
	}
	if !found {
		t.Error("automation-set value did not persist — the re-check is too strict")
	}
}

// smMeta declares a state machine so an automation can attempt to set a
// non-entry value on a cascade-created entity.
const smMeta = `version: "1.0"
types:
  taak_status:
    values: [todo, doing, done]
    default: todo
    initial: todo
    transitions:
      - {move: start, from: todo, to: doing}
      - {move: finish, from: doing, to: done}
entities:
  ticket:
    label: Ticket
    plural: tickets
    id_type: manual
    properties:
      title:
        type: string
  taak:
    label: Taak
    plural: taken
    id_prefix: TAAK
    properties:
      status:
        type: taak_status
`

// TestCascadeWrite_TransitionRecheckedAfterAutomation probes the third
// consequence listed in BUG-KIMZRK: an automation setting a state-machine
// property to a non-entry value on a cascade-created entity.
func TestCascadeWrite_TransitionRecheckedAfterAutomation(t *testing.T) {
	t.Parallel()
	meta, err := metamodel.Parse([]byte(smMeta))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	machines, err := statemachine.Compile(meta)
	if err != nil {
		t.Fatalf("statemachine.Compile: %v", err)
	}
	st := memstore.New()

	autos := []automation.Automation{
		{
			Name: "ticket-creates-taak",
			On:   automation.Trigger{Entity: []string{"ticket"}, Created: true},
			Do: []automation.Action{{
				CreateEntity: &automation.CreateEntityAction{Type: "taak"},
			}},
		},
		{
			// "done" is neither the entry value nor reachable from todo in
			// one legal move.
			Name: "taak-jumps-to-done",
			On:   automation.Trigger{Entity: []string{"taak"}, Created: true},
			Do:   []automation.Action{{Set: "status", Value: "done"}},
		},
	}
	engine := automation.NewEngine(autos)
	runner, err := autocascade.New(autocascade.Deps{Engine: engine})
	if err != nil {
		t.Fatalf("autocascade.New: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store: st, Meta: meta, Templater: nopTemplater{},
		Audit: audit.Nop{}, ACL: acl.NopACL{},
		Automations: engine, Cascade: runner,
		Transitions: machines,
		FieldGate:   entitymanager.AllowAllFieldGate{},
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}

	trigger := entity.New("TKT-1", "ticket")
	trigger.SetString("title", "Trigger")
	res, err := mgr.CreateEntity(context.Background(), trigger, entity.CreateOptions{ID: "TKT-1"})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	t.Logf("automation errors: %v", res.AutomationErrors)

	for e, listErr := range st.ListEntities(context.Background(), store.EntityQuery{Type: "taak"}) {
		if listErr != nil {
			t.Fatalf("list: %v", listErr)
		}
		t.Logf("taak %s status=%q", e.ID, e.GetString("status"))
		if e.GetString("status") == "done" {
			t.Errorf("automation set an illegal state-machine value %q on a "+
				"cascade-created entity; entry is %q", "done", "todo")
		}
	}
}

// TestCascadeWrite_ComputedDependentOnAutomationValue pins that a cascade
// automation may set a property a computed property derives from. The
// computed value changes with it; that is a re-evaluation, not an authored
// computed write, and must not be rejected.
func TestCascadeWrite_ComputedDependentOnAutomationValue(t *testing.T) {
	withOrder := strings.Replace(computedMetaYAML, "relations: {}", `  order:
    label: Order
    plural: orders
    id_prefix: "O-"
    id_type: sequential
    properties:
      name:
        type: string
relations: {}`, 1)
	meta, err := metamodel.Parse([]byte(withOrder))
	if err != nil {
		t.Fatal(err)
	}
	st := memstore.New()
	engine := automation.NewEngine([]automation.Automation{
		{
			Name: "spawn-item",
			On:   automation.Trigger{Entity: []string{"order"}, Created: true},
			Do: []automation.Action{{CreateEntity: &automation.CreateEntityAction{
				Type: "item", Properties: map[string]string{"source": "2"},
			}}},
		},
		{
			Name: "item-source",
			On:   automation.Trigger{Entity: []string{"item"}, Created: true},
			Do:   []automation.Action{{Set: "source", Value: "4"}},
		},
	})
	runner, err := autocascade.New(autocascade.Deps{Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store: st, Meta: meta, Templater: nopTemplater{}, Audit: audit.Nop{},
		ACL: acl.NopACL{}, Automations: engine, Cascade: runner,
		Transitions: statemachine.EmptySet(), FieldGate: entitymanager.AllowAllFieldGate{},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := mgr.CreateEntity(context.Background(), entity.New("", "order"), entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	for _, msg := range res.AutomationErrors {
		t.Errorf("cascade error: %s", msg)
	}

	var child *entity.Entity
	for e, err := range st.ListEntities(context.Background(), store.EntityQuery{Type: "item"}) {
		if err != nil {
			t.Fatal(err)
		}
		child = e
	}
	if child == nil {
		t.Fatal("the cascade created no child")
	}
	if got := child.Properties["doubled"]; got != int64(8) {
		t.Errorf("child doubled = %v, want 8 (source 4 set by the cascade)", got)
	}
}
