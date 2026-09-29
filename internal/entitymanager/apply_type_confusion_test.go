package entitymanager_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// typeConfusionMetamodel declares two manual-id entity types with no id_prefix,
// so the ID-prefix structural guard (a HARD validation error for prefixed,
// sequential types) does NOT fire — this is exactly the shape the BUG-ZWTDH9
// exploit relies on: a `manual` id_type target that skips the prefix check, so
// the ONLY defense against re-typing is the ACL/apply layer under test.
const typeConfusionMetamodel = `version: "1.0"
entities:
  secret:
    label: Secret
    plural: secrets
    id_type: manual
    properties:
      title:
        type: string
  note:
    label: Note
    plural: notes
    id_type: manual
    properties:
      title:
        type: string
`

func typeConfusionMeta(t *testing.T) *metamodel.Metamodel {
	t.Helper()
	m, err := metamodel.Parse([]byte(typeConfusionMetamodel))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	return m
}

// TestApplyEntity_RejectsTypeChangeOnUpdate is the BUG-ZWTDH9 PoC-as-test.
//
// A principal (mallory) is granted `update` on `note` and `read` on `secret`
// (a plausible least-privilege grant). She cannot update secrets. She issues an
// upsert against an EXISTING `secret`-typed entity with a body claiming type
// `note`. Before the fix, ApplyEntity authorized OpUpdate against the BODY type
// (note) — which she may update — and re-typed + overwrote the secret. The fix
// authorizes and validates against the STORED type and hard-rejects a body type
// that differs, so the upsert is rejected with ErrTypeImmutable and the stored
// entity is untouched (still a secret, unchanged title).
func TestApplyEntity_RejectsTypeChangeOnUpdate(t *testing.T) {
	st := memstore.New()
	meta := typeConfusionMeta(t)

	// Seed a secret via a NopACL manager (seeding bypasses authz).
	seedMgr, err := entitymanager.New(entitymanager.Deps{
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
		Store:     st, Meta: meta, Templater: nopTemplater{}, Audit: audit.Nop{}, ACL: acl.NopACL{}, Transitions: statemachine.EmptySet(),
	})
	if err != nil {
		t.Fatalf("seed New: %v", err)
	}
	if _, seedErr := seedMgr.ApplyEntity(context.Background(), &entity.Entity{
		ID: "SECRET-1", Type: "secret", Properties: map[string]any{"title": "top secret"},
	}); seedErr != nil {
		t.Fatalf("seed secret: %v", seedErr)
	}

	// Policy: mallory may UPDATE notes and READ secrets — but NOT update secrets.
	policy, err := acl.LoadPolicyBytes([]byte(`
roles:
  note-editor:
    update: [note]
    read: [note, secret]
assignments:
  mallory: note-editor
`))
	if err != nil {
		t.Fatalf("LoadPolicyBytes: %v", err)
	}
	declarative, err := acl.NewDeclarative(policy, acl.NewStoreGraph(st), st)
	if err != nil {
		t.Fatalf("NewDeclarative: %v", err)
	}
	sink := audit.NewMemory()
	mgr, err := entitymanager.New(entitymanager.Deps{
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
		// Not a copy test: opt out of the copy read gates explicitly, which is
		// what New requires of a policy-backed Deps (#1437).
		CopyReadGate:   entitymanager.AllowAllCopyReadGate{},
		CopyVisibility: allowAllCopyVisibility(t, st),
		Store:          st, Meta: meta, Templater: nopTemplater{}, Audit: sink, ACL: declarative, Transitions: statemachine.EmptySet(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	malloryCtx := principal.With(context.Background(),
		principal.Principal{User: "mallory", Tool: principal.ToolSync})

	// The exploit: overwrite + re-type the secret by claiming type `note`.
	_, applyErr := mgr.ApplyEntity(malloryCtx, &entity.Entity{
		ID: "SECRET-1", Type: "note", Properties: map[string]any{"title": "pwned"},
	})
	if applyErr == nil {
		t.Fatal("cross-type upsert succeeded — BUG-ZWTDH9 reproduced (authorized against body type, not stored type)")
	}
	if !errors.Is(applyErr, entitymanager.ErrTypeImmutable) {
		t.Fatalf("expected ErrTypeImmutable, got %T: %v", applyErr, applyErr)
	}

	// The stored entity must be UNCHANGED: still a secret, original title.
	got, err := st.GetEntity(context.Background(), "SECRET-1")
	if err != nil {
		t.Fatalf("GetEntity(SECRET-1): %v", err)
	}
	if got.Type != "secret" {
		t.Fatalf("stored type was mutated to %q; must remain \"secret\"", got.Type)
	}
	if got.GetString("title") != "top secret" {
		t.Fatalf("stored title was overwritten to %q; must remain \"top secret\"", got.GetString("title"))
	}

	// A note by that ID must NOT have been created as a side effect.
	notes := 0
	for e, err := range st.ListEntities(context.Background(), store.EntityQuery{Type: "note"}) {
		if err != nil {
			continue
		}
		if e.Type == "note" {
			notes++
		}
	}
	if notes != 0 {
		t.Fatalf("a note-typed entity was written despite rejection (%d found)", notes)
	}
}

// TestApplyEntity_SameTypeUpdateStillWorks pins that the fix does not break a
// legitimate same-type update: mallory CAN update a note she is permitted to
// edit, and the write lands.
func TestApplyEntity_SameTypeUpdateStillWorks(t *testing.T) {
	st := memstore.New()
	meta := typeConfusionMeta(t)

	seedMgr, err := entitymanager.New(entitymanager.Deps{
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
		Store:     st, Meta: meta, Templater: nopTemplater{}, Audit: audit.Nop{}, ACL: acl.NopACL{}, Transitions: statemachine.EmptySet(),
	})
	if err != nil {
		t.Fatalf("seed New: %v", err)
	}
	if _, seedErr := seedMgr.ApplyEntity(context.Background(), &entity.Entity{
		ID: "NOTE-1", Type: "note", Properties: map[string]any{"title": "v1"},
	}); seedErr != nil {
		t.Fatalf("seed note: %v", seedErr)
	}

	policy, err := acl.LoadPolicyBytes([]byte(`
roles:
  note-editor:
    update: [note]
    read: [note, secret]
assignments:
  mallory: note-editor
`))
	if err != nil {
		t.Fatalf("LoadPolicyBytes: %v", err)
	}
	declarative, err := acl.NewDeclarative(policy, acl.NewStoreGraph(st), st)
	if err != nil {
		t.Fatalf("NewDeclarative: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
		// Not a copy test: opt out of the copy read gates explicitly, which is
		// what New requires of a policy-backed Deps (#1437).
		CopyReadGate:   entitymanager.AllowAllCopyReadGate{},
		CopyVisibility: allowAllCopyVisibility(t, st),
		Store:          st, Meta: meta, Templater: nopTemplater{}, Audit: audit.Nop{}, ACL: declarative, Transitions: statemachine.EmptySet(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	malloryCtx := principal.With(context.Background(),
		principal.Principal{User: "mallory", Tool: principal.ToolSync})

	if _, updErr := mgr.ApplyEntity(malloryCtx, &entity.Entity{
		ID: "NOTE-1", Type: "note", Properties: map[string]any{"title": "v2"},
	}); updErr != nil {
		t.Fatalf("legitimate same-type update was rejected: %v", updErr)
	}
	got, err := st.GetEntity(context.Background(), "NOTE-1")
	if err != nil {
		t.Fatalf("GetEntity(NOTE-1): %v", err)
	}
	if got.GetString("title") != "v2" {
		t.Fatalf("same-type update did not land: title = %q", got.GetString("title"))
	}
}
