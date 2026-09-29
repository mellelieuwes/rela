package entitymanager_test

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

const orderableMetamodelTemplate = `version: "1.0"
entities:
  recipe:
    label: Recipe
    plural: recipes
    id_prefix: "REC-"
    id_type: sequential
    properties:
      title:
        type: string
        required: true
  step:
    label: Step
    plural: steps
    id_prefix: "STP-"
    id_type: sequential
    properties:
      title:
        type: string
        required: true
relations:
  has-step:
    label: has step
    from: [recipe]
    to: [step]
%s
`

func orderableMetamodel(t *testing.T, mode string) *metamodel.Metamodel {
	t.Helper()
	suffix := ""
	if mode != "" {
		suffix = "    orderable: " + mode
	}
	yaml := strings.Replace(orderableMetamodelTemplate, "%s", suffix, 1)
	m, err := metamodel.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse metamodel: %v", err)
	}
	return m
}

func newOrderableManager(t *testing.T, mode string) (*entitymanager.Manager, store.Store) {
	t.Helper()
	mgr, st, _ := newOrderableManagerWithAudit(t, mode)
	return mgr, st
}

// newOrderableManagerWithAudit is newOrderableManager with a capturing audit
// sink so tests can assert which writes produced records (issue #886).
func newOrderableManagerWithAudit(t *testing.T, mode string) (*entitymanager.Manager, store.Store, *audit.Memory) {
	t.Helper()
	st := memstore.New()
	mem := audit.NewMemory()
	deps := entitymanager.Deps{
		Store:       st,
		Meta:        orderableMetamodel(t, mode),
		Templater:   nopTemplater{},
		Audit:       mem,
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	}
	mgr, err := entitymanager.New(deps)
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	return mgr, st, mem
}

func mkRecipe(t *testing.T, mgr *entitymanager.Manager, title string) *entity.Entity {
	t.Helper()
	e := entity.New("", "recipe")
	e.SetString("title", title)
	res, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("create recipe: %v", err)
	}
	return res.Entity
}

func mkStep(t *testing.T, mgr *entitymanager.Manager, title string) *entity.Entity {
	t.Helper()
	e := entity.New("", "step")
	e.SetString("title", title)
	res, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("create step: %v", err)
	}
	return res.Entity
}

func TestCreateRelation_AssignsOrder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		mode           string
		wantOutPresent bool
		wantInPresent  bool
	}{
		{"none — no managed order", "", false, false},
		{"outgoing — assigns _order_out only", "outgoing", true, false},
		{"incoming — assigns _order_in only", "incoming", false, true},
		{"both — assigns both", "both", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mgr, _ := newOrderableManager(t, tt.mode)
			ctx := context.Background()
			recipe := mkRecipe(t, mgr, "Soup")
			step1 := mkStep(t, mgr, "Boil water")
			step2 := mkStep(t, mgr, "Add salt")

			rel1, err := mgr.CreateRelation(ctx, recipe.ID, "has-step", step1.ID, entity.RelationOptions{})
			if err != nil {
				t.Fatalf("create rel1: %v", err)
			}
			rel2, err := mgr.CreateRelation(ctx, recipe.ID, "has-step", step2.ID, entity.RelationOptions{})
			if err != nil {
				t.Fatalf("create rel2: %v", err)
			}

			gotOut1, hasOut1 := rel1.Properties[metamodel.OrderPropertyOut]
			gotOut2, hasOut2 := rel2.Properties[metamodel.OrderPropertyOut]
			gotIn1, hasIn1 := rel1.Properties[metamodel.OrderPropertyIn]
			gotIn2, hasIn2 := rel2.Properties[metamodel.OrderPropertyIn]

			if hasOut1 != tt.wantOutPresent || hasOut2 != tt.wantOutPresent {
				t.Errorf("_order_out presence: rel1=%v rel2=%v, want %v", hasOut1, hasOut2, tt.wantOutPresent)
			}
			if hasIn1 != tt.wantInPresent || hasIn2 != tt.wantInPresent {
				t.Errorf("_order_in presence: rel1=%v rel2=%v, want %v", hasIn1, hasIn2, tt.wantInPresent)
			}

			if tt.wantOutPresent {
				if gotOut1 != 1.0 {
					t.Errorf("rel1 _order_out = %v, want 1.0", gotOut1)
				}
				if gotOut2 != 2.0 {
					t.Errorf("rel2 _order_out = %v, want 2.0", gotOut2)
				}
			}
			if tt.wantInPresent {
				if gotIn1 != 1.0 {
					t.Errorf("rel1 _order_in = %v, want 1.0", gotIn1)
				}
				if gotIn2 != 1.0 {
					t.Errorf("rel2 _order_in = %v, want 1.0", gotIn2)
				}
			}
		})
	}
}

func TestCreateRelation_ExplicitOrderRespected(t *testing.T) {
	t.Parallel()
	mgr, _ := newOrderableManager(t, "outgoing")
	ctx := context.Background()
	recipe := mkRecipe(t, mgr, "Stew")
	step := mkStep(t, mgr, "Chop")

	rel, err := mgr.CreateRelation(ctx, recipe.ID, "has-step", step.ID, entity.RelationOptions{
		Properties: map[string]any{metamodel.OrderPropertyOut: 42.5},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := rel.Properties[metamodel.OrderPropertyOut]; got != 42.5 {
		t.Errorf("explicit _order_out should be preserved: got %v, want 42.5", got)
	}
}

// Garbage value cases — non-finite or non-numeric values supplied through
// non-HTTP write paths must be overwritten with the auto-assigned next
// ordinal so the on-disk relation always has a sortable value.
func TestCreateRelation_GarbageOrderValueIsOverwritten(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value any
	}{
		{"non-numeric string", "abc"},
		{"explicit nil", nil},
		{"boolean", true},
		{"NaN", math.NaN()},
		{"+Inf", math.Inf(1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mgr, _ := newOrderableManager(t, "outgoing")
			ctx := context.Background()
			recipe := mkRecipe(t, mgr, "X")
			step := mkStep(t, mgr, "Y")

			rel, err := mgr.CreateRelation(ctx, recipe.ID, "has-step", step.ID, entity.RelationOptions{
				Properties: map[string]any{metamodel.OrderPropertyOut: tt.value},
			})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if got := rel.Properties[metamodel.OrderPropertyOut]; got != 1.0 {
				t.Errorf("_order_out = %v (%T), want 1.0 (caller-supplied garbage should be overwritten)", got, got)
			}
		})
	}
}

func TestUpdateRelation_BothMode_SidesIndependent(t *testing.T) {
	t.Parallel()
	mgr, st := newOrderableManager(t, "both")
	ctx := context.Background()
	recipe := mkRecipe(t, mgr, "X")
	step := mkStep(t, mgr, "Y")

	rel, err := mgr.CreateRelation(ctx, recipe.ID, "has-step", step.ID, entity.RelationOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if rel.Properties[metamodel.OrderPropertyOut] != 1.0 || rel.Properties[metamodel.OrderPropertyIn] != 1.0 {
		t.Fatalf("baseline: got out=%v in=%v, want both 1.0",
			rel.Properties[metamodel.OrderPropertyOut], rel.Properties[metamodel.OrderPropertyIn])
	}

	_, err = mgr.UpdateRelation(ctx, recipe.ID, "has-step", step.ID, entity.RelationOptions{
		Properties: map[string]any{metamodel.OrderPropertyOut: 5.5},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := st.GetRelation(ctx, recipe.ID, "has-step", step.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Properties[metamodel.OrderPropertyOut] != 5.5 {
		t.Errorf("_order_out = %v, want 5.5", got.Properties[metamodel.OrderPropertyOut])
	}
	if got.Properties[metamodel.OrderPropertyIn] != 1.0 {
		t.Errorf("_order_in changed unexpectedly: got %v, want 1.0", got.Properties[metamodel.OrderPropertyIn])
	}
}

func TestUpdateRelation_RejectsNonFiniteOrder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value any
	}{
		{"non-numeric string", "abc"},
		{"boolean", true},
		{"NaN", math.NaN()},
		{"+Inf", math.Inf(1)},
		{"-Inf", math.Inf(-1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mgr, st := newOrderableManager(t, "outgoing")
			ctx := context.Background()
			recipe := mkRecipe(t, mgr, "X")
			step := mkStep(t, mgr, "Y")
			if _, err := mgr.CreateRelation(ctx, recipe.ID, "has-step", step.ID, entity.RelationOptions{}); err != nil {
				t.Fatalf("create baseline: %v", err)
			}

			_, err := mgr.UpdateRelation(ctx, recipe.ID, "has-step", step.ID, entity.RelationOptions{
				Properties: map[string]any{metamodel.OrderPropertyOut: tt.value},
			})
			if err == nil {
				t.Fatalf("expected update to fail for value %v, got nil error", tt.value)
			}

			got, getErr := st.GetRelation(ctx, recipe.ID, "has-step", step.ID)
			if getErr != nil {
				t.Fatalf("get: %v", getErr)
			}
			if got.Properties[metamodel.OrderPropertyOut] != 1.0 {
				t.Errorf("_order_out should remain 1.0 after rejected update, got %v", got.Properties[metamodel.OrderPropertyOut])
			}
		})
	}
}

// TestRenumber_EmitsAuditRecords pins issue #886: engine-triggered renumber
// writes (maybeRenumberSide) must produce audit records, marked with a
// renumber: triggered-by so they are distinguishable from the user-initiated
// UpdateRelation that spawned them.
func TestRenumber_EmitsAuditRecords(t *testing.T) {
	t.Parallel()
	mgr, _, mem := newOrderableManagerWithAudit(t, "outgoing")
	ctx := context.Background()
	recipe := mkRecipe(t, mgr, "X")
	s1 := mkStep(t, mgr, "S1")
	s2 := mkStep(t, mgr, "S2")
	s3 := mkStep(t, mgr, "S3")

	// Steps get dense orders 1, 2, 3.
	for _, s := range []*entity.Entity{s1, s2, s3} {
		if _, err := mgr.CreateRelation(ctx, recipe.ID, "has-step", s.ID, entity.RelationOptions{}); err != nil {
			t.Fatalf("create relation: %v", err)
		}
	}

	// Collapse s3's order onto s2's (gap < threshold) -> triggers renumber of
	// the outgoing side. The update itself is one audited write; the renumber
	// writes are additional records.
	before := len(mem.Records())
	if _, err := mgr.UpdateRelation(ctx, recipe.ID, "has-step", s3.ID, entity.RelationOptions{
		Properties: map[string]any{metamodel.OrderPropertyOut: 2.0},
	}); err != nil {
		t.Fatalf("update relation: %v", err)
	}

	var update, renumber int
	for _, r := range mem.Records()[before:] {
		if r.Op != audit.OpUpdateRelation {
			continue
		}
		if strings.HasPrefix(r.TriggeredBy, "renumber:") {
			renumber++
		} else {
			update++
		}
	}

	if update < 1 {
		t.Errorf("want at least 1 user-initiated update-relation record, got %d", update)
	}
	if renumber < 1 {
		t.Errorf("want at least 1 renumber audit record (issue #886), got %d", renumber)
	}
}
