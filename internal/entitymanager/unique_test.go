package entitymanager_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/autocascade"
	"github.com/Sourcehaven-BV/rela/internal/automation"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// uniqueMetamodelYAML declares a `persoon` type with a unique `email`
// property and a non-unique `nickname`, plus a manual-ID `account` type
// used to prove uniqueness is scoped per entity type.
const uniqueMetamodelYAML = `version: "1.0"
entities:
  persoon:
    label: Persoon
    plural: personen
    id_type: manual
    properties:
      email:
        type: string
        unique: true
      nickname:
        type: string
  account:
    label: Account
    plural: accounts
    id_type: manual
    properties:
      email:
        type: string
        unique: true
`

func newUniqueManager(t *testing.T) *entitymanager.Manager {
	t.Helper()
	meta, err := metamodel.Parse([]byte(uniqueMetamodelYAML))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       memstore.New(),
		Meta:        meta,
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	return mgr
}

// newUniqueManagerWithAutomation is like newUniqueManager but wires an
// automation engine so on-create actions (e.g. Set email=...) run and the
// post-automation uniqueness re-check is exercised.
func newUniqueManagerWithAutomation(t *testing.T, autos []automation.Automation) *entitymanager.Manager {
	t.Helper()
	meta, err := metamodel.Parse([]byte(uniqueMetamodelYAML))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	engine := automation.NewEngine(autos)
	runner, err := autocascade.New(autocascade.Deps{Engine: engine})
	if err != nil {
		t.Fatalf("autocascade.New: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       memstore.New(),
		Meta:        meta,
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
		Automations: engine,
		Cascade:     runner,
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	return mgr
}

func createPersoon(t *testing.T, mgr *entitymanager.Manager, id, email string) error {
	t.Helper()
	e := entity.New(id, "persoon")
	if email != "" {
		e.SetString("email", email)
	}
	_, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{ID: id})
	return err
}

func isValidationError(err error) bool {
	var ve *entitymanager.ValidationError
	return errors.As(err, &ve)
}

func TestUnique_CreateRejectsDuplicate(t *testing.T) {
	mgr := newUniqueManager(t)

	if err := createPersoon(t, mgr, "PERS-JV", "jv@example.com"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	err := createPersoon(t, mgr, "PERS-DUP", "jv@example.com")
	if err == nil {
		t.Fatal("expected duplicate-email create to be rejected, got nil")
	}
	if !isValidationError(err) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestUnique_CreateAllowsDistinctValues(t *testing.T) {
	mgr := newUniqueManager(t)

	if err := createPersoon(t, mgr, "PERS-JV", "jv@example.com"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := createPersoon(t, mgr, "PERS-TS", "ts@example.com"); err != nil {
		t.Fatalf("distinct-email create should succeed: %v", err)
	}
}

func TestUnique_EmptyValuesAreExempt(t *testing.T) {
	mgr := newUniqueManager(t)

	// Two personen with no email must both be creatable — a unique
	// property is unique only among the entities that set it.
	if err := createPersoon(t, mgr, "PERS-A", ""); err != nil {
		t.Fatalf("first empty-email create: %v", err)
	}
	if err := createPersoon(t, mgr, "PERS-B", ""); err != nil {
		t.Fatalf("second empty-email create should succeed: %v", err)
	}
}

func TestUnique_ScopedPerType(t *testing.T) {
	mgr := newUniqueManager(t)

	if err := createPersoon(t, mgr, "PERS-JV", "shared@example.com"); err != nil {
		t.Fatalf("persoon create: %v", err)
	}
	// Same email on a different type must not collide.
	acct := entity.New("ACC-1", "account")
	acct.SetString("email", "shared@example.com")
	if _, err := mgr.CreateEntity(context.Background(), acct, entity.CreateOptions{ID: "ACC-1"}); err != nil {
		t.Fatalf("account create with same email (different type) should succeed: %v", err)
	}
}

func TestUnique_UpdateSelfDoesNotCollide(t *testing.T) {
	mgr := newUniqueManager(t)

	if err := createPersoon(t, mgr, "PERS-JV", "jv@example.com"); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Re-save the same entity keeping its own email — must not self-collide.
	e := entity.New("PERS-JV", "persoon")
	e.SetString("email", "jv@example.com")
	e.SetString("nickname", "Jer")
	if _, err := mgr.UpdateEntity(context.Background(), e); err != nil {
		t.Fatalf("self-update should not collide: %v", err)
	}
}

// TestUnique_CreateRejectsAutomationSetDuplicate pins C1: the create-path
// uniqueness check must see POST-automation values. An automation that
// sets a `unique` property to a colliding value must be rejected — the
// create path must not be weaker than the update path.
func TestUnique_CreateRejectsAutomationSetDuplicate(t *testing.T) {
	// Every persoon create gets email forced to the same value by automation.
	autos := []automation.Automation{{
		Name: "force-email",
		On:   automation.Trigger{Entity: []string{"persoon"}, Created: true},
		Do:   []automation.Action{{Set: "email", Value: "forced@example.com"}},
	}}
	mgr := newUniqueManagerWithAutomation(t, autos)

	// First create: automation sets email=forced@… — unique, succeeds.
	if _, err := mgr.CreateEntity(context.Background(),
		entity.New("PERS-A", "persoon"), entity.CreateOptions{ID: "PERS-A"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	// Second create: automation forces the SAME email → must be rejected,
	// even though the caller supplied no email at all.
	_, err := mgr.CreateEntity(context.Background(),
		entity.New("PERS-B", "persoon"), entity.CreateOptions{ID: "PERS-B"})
	if err == nil {
		t.Fatal("expected automation-set duplicate email to be rejected on create, got nil")
	}
	if !isValidationError(err) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestUnique_UpdateRejectsCollisionWithOther(t *testing.T) {
	mgr := newUniqueManager(t)

	if err := createPersoon(t, mgr, "PERS-JV", "jv@example.com"); err != nil {
		t.Fatalf("create JV: %v", err)
	}
	if err := createPersoon(t, mgr, "PERS-TS", "ts@example.com"); err != nil {
		t.Fatalf("create TS: %v", err)
	}
	// Update TS to JV's email — must be rejected.
	e := entity.New("PERS-TS", "persoon")
	e.SetString("email", "jv@example.com")
	_, err := mgr.UpdateEntity(context.Background(), e)
	if err == nil {
		t.Fatal("expected update-into-duplicate to be rejected, got nil")
	}
	if !isValidationError(err) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}
