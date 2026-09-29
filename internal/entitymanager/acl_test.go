package entitymanager_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// newManagerWithACL wires a Manager with the supplied ACL + audit
// sink. Used by the deny-path tests below; the standard newManager
// fixture (manager_test.go) is parameterized on NopACL.
func newManagerWithACL(
	t *testing.T, aclImpl acl.ACL, sink audit.Audit,
) (*entitymanager.Manager, *countingStore) {
	t.Helper()
	cs := &countingStore{Store: memstore.New()}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       cs,
		Meta:        parseMeta(t),
		Templater:   nopTemplater{},
		Audit:       sink,
		ACL:         aclImpl,
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	return mgr, cs
}

// seedEntity inserts a fresh entity through a NopACL Manager so a
// subsequent deny test has something to act on. Without this, ACL
// gates fire after the entity-not-found lookup and we can't tell
// apart "ACL denied" from "no such entity."
func seedEntity(t *testing.T, store *countingStore, entityType, title string) {
	t.Helper()
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       store,
		Meta:        parseMeta(t),
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		t.Fatalf("seedEntity: New: %v", err)
	}
	e := entity.New("", entityType)
	e.SetString("title", title)
	if _, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{}); err != nil {
		t.Fatalf("seedEntity: CreateEntity: %v", err)
	}
}

// seedRelation inserts a fresh relation through a NopACL Manager.
func seedRelation(t *testing.T, store *countingStore, from, relType, to string) {
	t.Helper()
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       store,
		Meta:        parseMeta(t),
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		t.Fatalf("seedRelation: New: %v", err)
	}
	if _, err := mgr.CreateRelation(context.Background(), from, relType, to, entity.RelationOptions{}); err != nil {
		t.Fatalf("seedRelation: CreateRelation: %v", err)
	}
}

// AC1.5: ReadOnlyACL denies every write path, returns *ForbiddenError
// with the read-only Decision, and records a denied-write audit row.
// Table-driven across all 7 entry points.
func TestManager_ACLDenies_AllWritePathsBlocked(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// run exercises the write entry point against mgr. Returns the
		// error from the entry point. Pre-seeded data is set up by setup.
		setup func(t *testing.T, store *countingStore)
		run   func(t *testing.T, mgr *entitymanager.Manager) error
		// wantSubjectKind is "entity" or "relation".
		wantSubjectKind string
	}{
		{
			name: "CreateEntity denied",
			setup: func(t *testing.T, _ *countingStore) {
				t.Helper()
			},
			run: func(_ *testing.T, mgr *entitymanager.Manager) error {
				e := entity.New("", "requirement")
				e.SetString("title", "Denied")
				_, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
				return err
			},
			wantSubjectKind: "entity",
		},
		{
			name: "UpdateEntity denied",
			setup: func(t *testing.T, store *countingStore) {
				t.Helper()
				seedEntity(t, store, "requirement", "Original")
			},
			run: func(_ *testing.T, mgr *entitymanager.Manager) error {
				e := entity.New("REQ-001", "requirement")
				e.SetString("title", "Modified")
				_, err := mgr.UpdateEntity(context.Background(), e)
				return err
			},
			wantSubjectKind: "entity",
		},
		{
			name: "DeleteEntity denied",
			setup: func(t *testing.T, store *countingStore) {
				t.Helper()
				seedEntity(t, store, "requirement", "ToDelete")
			},
			run: func(_ *testing.T, mgr *entitymanager.Manager) error {
				_, err := mgr.DeleteEntity(context.Background(), "REQ-001", false)
				return err
			},
			wantSubjectKind: "entity",
		},
		{
			name: "RenameEntity denied",
			setup: func(t *testing.T, store *countingStore) {
				t.Helper()
				seedEntity(t, store, "requirement", "Original")
			},
			run: func(_ *testing.T, mgr *entitymanager.Manager) error {
				_, err := mgr.RenameEntity(context.Background(), "REQ-001", "REQ-002", entity.RenameOptions{})
				return err
			},
			wantSubjectKind: "entity",
		},
		{
			name: "CreateRelation denied",
			setup: func(t *testing.T, store *countingStore) {
				t.Helper()
				seedEntity(t, store, "decision", "FromEntity")
				seedEntity(t, store, "requirement", "ToEntity")
			},
			run: func(_ *testing.T, mgr *entitymanager.Manager) error {
				_, err := mgr.CreateRelation(context.Background(), "DEC-001", "addresses", "REQ-001", entity.RelationOptions{})
				return err
			},
			wantSubjectKind: "relation",
		},
		{
			name: "UpdateRelation denied",
			setup: func(t *testing.T, store *countingStore) {
				t.Helper()
				seedEntity(t, store, "decision", "FromEntity")
				seedEntity(t, store, "requirement", "ToEntity")
				seedRelation(t, store, "DEC-001", "addresses", "REQ-001")
			},
			run: func(_ *testing.T, mgr *entitymanager.Manager) error {
				_, err := mgr.UpdateRelation(context.Background(), "DEC-001", "addresses", "REQ-001", entity.RelationOptions{})
				return err
			},
			wantSubjectKind: "relation",
		},
		{
			name: "DeleteRelation denied",
			setup: func(t *testing.T, store *countingStore) {
				t.Helper()
				seedEntity(t, store, "decision", "FromEntity")
				seedEntity(t, store, "requirement", "ToEntity")
				seedRelation(t, store, "DEC-001", "addresses", "REQ-001")
			},
			run: func(_ *testing.T, mgr *entitymanager.Manager) error {
				return mgr.DeleteRelation(context.Background(), "DEC-001", "addresses", "REQ-001")
			},
			wantSubjectKind: "relation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Seed via NopACL on a shared store so the data exists when
			// the ReadOnlyACL Manager runs the entry point.
			sink := audit.NewMemory()
			roMgr, store := newManagerWithACL(t, acl.ReadOnlyACL{}, sink)
			tt.setup(t, store)

			// Snapshot write-side store counters BEFORE running the
			// denied op; setup may have legitimate writes.
			createsBefore := store.creates.Load()
			updatesBefore := store.updates.Load()
			deletesBefore := store.deletes.Load()

			// Snapshot audit count too — setup writes legitimate audit rows.
			seededRecords := len(sink.Records())

			err := tt.run(t, roMgr)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, acl.ErrForbidden) {
				t.Errorf("errors.Is(err, ErrForbidden) = false, got err = %v", err)
			}
			var fe *acl.ForbiddenError
			if !errors.As(err, &fe) {
				t.Fatalf("errors.As(err, *ForbiddenError) = false, got err = %v", err)
			}
			if fe.Decision.RuleKind != "read-only" {
				t.Errorf("Decision.RuleKind = %q, want %q", fe.Decision.RuleKind, "read-only")
			}

			// Pin AC1.5's hard invariant: zero new store writes.
			if got := store.creates.Load(); got != createsBefore {
				t.Errorf("CreateEntity store calls increased by %d, want 0", got-createsBefore)
			}
			if got := store.updates.Load(); got != updatesBefore {
				t.Errorf("UpdateEntity store calls increased by %d, want 0", got-updatesBefore)
			}
			if got := store.deletes.Load(); got != deletesBefore {
				t.Errorf("DeleteEntity store calls increased by %d, want 0", got-deletesBefore)
			}

			// Pin the audit invariant: exactly one new denied-write row.
			records := sink.Records()
			newRecords := records[seededRecords:]
			if len(newRecords) != 1 {
				t.Fatalf("new audit records = %d, want 1 (denied-write)", len(newRecords))
			}
			rec := newRecords[0]
			if rec.Op != audit.OpDeniedWrite {
				t.Errorf("audit record Op = %q, want %q", rec.Op, audit.OpDeniedWrite)
			}
			if rec.Subject == nil {
				t.Fatal("audit record Subject is nil")
			}
			if rec.Subject.Kind != tt.wantSubjectKind {
				t.Errorf("Subject.Kind = %q, want %q", rec.Subject.Kind, tt.wantSubjectKind)
			}
			if rec.Summary == "" {
				t.Error("audit record Summary is empty")
			}
		})
	}
}

// AC1.5 corollary: NopACL allows every write path — the existing
// manager_test.go suite already validates this end-to-end; this test
// just pins that the same 7 entry points succeed under NopACL using
// the exact same fixtures and seed data.
func TestManager_NopACLAllows_AllWritePathsSucceed(t *testing.T) {
	t.Parallel()
	sink := audit.NewMemory()
	mgr, store := newManagerWithACL(t, acl.NopACL{}, sink)

	// Smoke test for each entry point.
	e := entity.New("", "requirement")
	e.SetString("title", "Allowed")
	res, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	res.Entity.SetString("title", "Updated")
	if _, uerr := mgr.UpdateEntity(context.Background(), res.Entity); uerr != nil {
		t.Fatalf("UpdateEntity: %v", uerr)
	}
	dec := entity.New("", "decision")
	dec.SetString("title", "D")
	decRes, err := mgr.CreateEntity(context.Background(), dec, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity (decision): %v", err)
	}
	if _, err := mgr.CreateRelation(context.Background(), decRes.Entity.ID, "addresses", res.Entity.ID, entity.RelationOptions{}); err != nil {
		t.Fatalf("CreateRelation: %v", err)
	}
	if _, err := mgr.UpdateRelation(context.Background(), decRes.Entity.ID, "addresses", res.Entity.ID, entity.RelationOptions{}); err != nil {
		t.Fatalf("UpdateRelation: %v", err)
	}
	if err := mgr.DeleteRelation(context.Background(), decRes.Entity.ID, "addresses", res.Entity.ID); err != nil {
		t.Fatalf("DeleteRelation: %v", err)
	}
	if _, err := mgr.DeleteEntity(context.Background(), res.Entity.ID, false); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}

	// Sanity: no denied-write records appeared.
	for _, r := range sink.Records() {
		if r.Op == audit.OpDeniedWrite {
			t.Errorf("unexpected denied-write record under NopACL: %+v", r)
		}
	}
	_ = store
}

// TestManager_DeclarativeACL_LocalRoleAllowsWrite tells one
// end-to-end story:
//
//	A bug-tracking workspace has tickets. Every assigned engineer
//	can edit tickets they're assigned to — and only those. Alice is
//	assigned to TKT-001, not to TKT-002. Editing TKT-001 succeeds;
//	editing TKT-002 is denied. There is no global "editor" role;
//	authorisation flows entirely from the per-ticket assignment edge.
//
// What this pins. Production wiring — entitymanager.Manager picks up
// the v1 acl.Declarative configured with a store-backed Graph, and the
// per-entity Subject reaches the resolver. Without this test, a
// regression that drops the Subject field at the manager's call sites
// would silently fall back to v0 semantics: alice would lose her local
// grant without any test noticing, because v0 has no concept of
// per-entity authorisation.
func TestManager_DeclarativeACL_LocalRoleAllowsWrite(t *testing.T) {
	t.Parallel()
	// A bug-tracking metamodel: people, tickets, and an `assigned-to`
	// relation from people to tickets. The metamodel doesn't know about
	// authorisation — that lives in acl.yaml.
	metamodelYAML := `version: "1.0"
entities:
  person:
    label: Person
    plural: people
    id_type: manual
    properties:
      name: { type: string }
  ticket:
    label: Ticket
    plural: tickets
    id_prefix: "TKT-"
    id_type: sequential
    properties:
      title: { type: string, required: true }
      status: { type: status }
relations:
  assigned-to:
    label: Assigned to
    from: [person]
    to: [ticket]
types:
  status:
    values: [open, closed]
`
	meta, err := metamodel.Parse([]byte(metamodelYAML))
	if err != nil {
		t.Fatalf("parse metamodel: %v", err)
	}

	// The policy: assignment to a ticket via `assigned-to` confers the
	// editor role, which grants write on tickets. There is no global
	// editor assignment — alice's only path to editing tickets is
	// through her local edges.
	policy, err := acl.LoadPolicyBytes([]byte(`
roles:
  editor: { create: [ticket], update: [ticket], delete: [ticket], read: [ticket] }
role_relations:
  assigned-to: { confers: editor }
`))
	if err != nil {
		t.Fatalf("LoadPolicyBytes: %v", err)
	}

	// Setup: a store, a Manager wired with NopACL for the seeding phase
	// (so we can create people and tickets without authorizing them).
	// We rebuild the Manager with the real ACL once the data is in.
	store := &countingStore{Store: memstore.New()}
	seedMgr, err := entitymanager.New(entitymanager.Deps{
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
		Store:     store, Meta: meta, Templater: nopTemplater{},
		Audit: audit.Nop{}, ACL: acl.NopACL{}, Transitions: statemachine.EmptySet(),
	})
	if err != nil {
		t.Fatalf("seed Manager.New: %v", err)
	}

	// Alice is a person.
	alice := entity.New("alice", "person")
	alice.SetString("name", "Alice")
	if _, sErr := seedMgr.CreateEntity(context.Background(), alice,
		entity.CreateOptions{ID: "alice"}); sErr != nil {
		t.Fatalf("seed alice: %v", sErr)
	}

	// Two tickets exist.
	tkt1 := entity.New("", "ticket")
	tkt1.SetString("title", "Login button broken")
	tkt1Res, err := seedMgr.CreateEntity(context.Background(), tkt1, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("seed TKT-001: %v", err)
	}
	tkt1ID := tkt1Res.Entity.ID

	tkt2 := entity.New("", "ticket")
	tkt2.SetString("title", "Search slow on large queries")
	tkt2Res, err := seedMgr.CreateEntity(context.Background(), tkt2, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("seed TKT-002: %v", err)
	}
	tkt2ID := tkt2Res.Entity.ID

	// Alice is assigned to TKT-001 (the local role grant) but NOT to
	// TKT-002. After this edge exists, alice should be able to edit
	// TKT-001 and only TKT-001.
	if _, sErr := seedMgr.CreateRelation(context.Background(),
		"alice", "assigned-to", tkt1ID, entity.RelationOptions{}); sErr != nil {
		t.Fatalf("seed assigned-to: %v", sErr)
	}

	// Switch to the production ACL: Declarative with a store-backed
	// Graph. This is the exact wiring shape appbuild produces.
	sink := audit.NewMemory()
	declarative, err := acl.NewDeclarative(policy, acl.NewStoreGraph(store), store)
	if err != nil {
		t.Fatalf("NewDeclarative: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
		// Not a copy test: opt out of the copy read gates explicitly, which is
		// what New requires of a policy-backed Deps (#1437).
		CopyReadGate:   entitymanager.AllowAllCopyReadGate{},
		CopyVisibility: allowAllCopyVisibility(t, store),
		Store:          store, Meta: meta, Templater: nopTemplater{},
		Audit: sink, ACL: declarative, Transitions: statemachine.EmptySet(),
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}

	// Stamp alice as the principal — the entry-point binary would
	// normally do this; the test does it explicitly.
	aliceCtx := principal.With(context.Background(),
		principal.Principal{User: "alice", Tool: principal.ToolDataEntry})

	// Story step 1 — Alice edits the ticket she's assigned to. The v1
	// resolver follows `alice --assigned-to--> TKT-001`, finds the
	// `editor` role conferred locally, and allows the write.
	updateOK := entity.New(tkt1ID, "ticket")
	updateOK.SetString("title", "Login button broken (in progress)")
	if _, uErr := mgr.UpdateEntity(aliceCtx, updateOK); uErr != nil {
		t.Fatalf("Alice editing TKT-001 (her assigned ticket) was denied: %v", uErr)
	}

	// Story step 2 — Alice tries to edit a ticket she is NOT assigned
	// to. The v1 resolver finds no role granting write on this ticket
	// for her principal, and denies.
	updateDeny := entity.New(tkt2ID, "ticket")
	updateDeny.SetString("title", "should be denied")
	_, err = mgr.UpdateEntity(aliceCtx, updateDeny)
	if err == nil {
		t.Fatalf("Alice editing TKT-002 (not her ticket) succeeded; want deny")
	}
	var forbidden *acl.ForbiddenError
	if !errors.As(err, &forbidden) {
		t.Fatalf("UpdateEntity(TKT-002): error type = %T, want *acl.ForbiddenError", err)
	}

	// Verify the audit trail tells the story: one deny on TKT-002, none
	// on TKT-001 (which was allowed).
	denies := 0
	for _, rec := range sink.Records() {
		if rec.Op == audit.OpDeniedWrite {
			denies++
		}
	}
	if denies != 1 {
		t.Errorf("audit denied-write count = %d, want 1 (TKT-002)", denies)
	}

	// Verify TKT-001's title actually changed (the allow path landed).
	got, err := store.GetEntity(context.Background(), tkt1ID)
	if err != nil {
		t.Fatalf("post-update GetEntity(TKT-001): %v", err)
	}
	if title := got.GetString("title"); title != "Login button broken (in progress)" {
		t.Errorf("TKT-001 title = %q, want %q",
			title, "Login button broken (in progress)")
	}
}

// AC7: denied-write audit Summary carries the per-attribution Source
// (e.g. `role=viewer via group:viewers`) so operators answering "which
// roles did the resolver consider, and via which paths?" don't have to
// re-run the resolver. The wire 403 body stays opaque — only audit
// reads the Source chain.
//
// The scenario stamps Alice as a member of the `viewers` group; the
// group holds the `viewer` role which has read on tickets but no write.
// Alice tries to update a ticket — the resolver collects her
// attribution (`viewer via group:viewers`) and denies because no role
// grants write. The audit row must surface that attribution.
func TestManager_DeniedWrite_AuditCarriesSourceAttribution(t *testing.T) {
	t.Parallel()
	const metamodelYAML = `version: "1.0"
entities:
  person:
    label: Person
    plural: people
    id_type: manual
    properties:
      name: { type: string }
  team:
    label: Team
    plural: teams
    id_type: manual
    properties:
      name: { type: string }
  ticket:
    label: Ticket
    plural: tickets
    id_prefix: "TKT-"
    id_type: sequential
    properties:
      title: { type: string, required: true }
relations:
  member-of:
    label: Member of
    from: [person]
    to: [team]
`
	meta, err := metamodel.Parse([]byte(metamodelYAML))
	if err != nil {
		t.Fatalf("parse metamodel: %v", err)
	}

	policy, err := acl.LoadPolicyBytes([]byte(`
roles:
  viewer: { read: [ticket] }
assignments:
  viewers: viewer
`))
	if err != nil {
		t.Fatalf("LoadPolicyBytes: %v", err)
	}

	store := &countingStore{Store: memstore.New()}
	seedMgr, err := entitymanager.New(entitymanager.Deps{
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
		Store:     store, Meta: meta, Templater: nopTemplater{},
		Audit: audit.Nop{}, ACL: acl.NopACL{}, Transitions: statemachine.EmptySet(),
	})
	if err != nil {
		t.Fatalf("seed Manager.New: %v", err)
	}

	if _, sErr := seedMgr.CreateEntity(context.Background(),
		entity.New("alice", "person"),
		entity.CreateOptions{ID: "alice"}); sErr != nil {
		t.Fatalf("seed alice: %v", sErr)
	}
	if _, sErr := seedMgr.CreateEntity(context.Background(),
		entity.New("viewers", "team"),
		entity.CreateOptions{ID: "viewers"}); sErr != nil {
		t.Fatalf("seed viewers: %v", sErr)
	}
	if _, sErr := seedMgr.CreateRelation(context.Background(),
		"alice", "member-of", "viewers", entity.RelationOptions{}); sErr != nil {
		t.Fatalf("seed member-of: %v", sErr)
	}
	tkt := entity.New("", "ticket")
	tkt.SetString("title", "some ticket")
	tktRes, err := seedMgr.CreateEntity(context.Background(), tkt, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	tktID := tktRes.Entity.ID

	sink := audit.NewMemory()
	declarative, err := acl.NewDeclarative(policy, acl.NewStoreGraph(store), store)
	if err != nil {
		t.Fatalf("NewDeclarative: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
		// Not a copy test: opt out of the copy read gates explicitly, which is
		// what New requires of a policy-backed Deps (#1437).
		CopyReadGate:   entitymanager.AllowAllCopyReadGate{},
		CopyVisibility: allowAllCopyVisibility(t, store),
		Store:          store, Meta: meta, Templater: nopTemplater{},
		Audit: sink, ACL: declarative, Transitions: statemachine.EmptySet(),
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}

	aliceCtx := principal.With(context.Background(),
		principal.Principal{User: "alice", Tool: principal.ToolDataEntry})

	update := entity.New(tktID, "ticket")
	update.SetString("title", "should be denied")
	_, err = mgr.UpdateEntity(aliceCtx, update)
	if err == nil {
		t.Fatal("UpdateEntity succeeded; want acl.ForbiddenError")
	}
	var forbidden *acl.ForbiddenError
	if !errors.As(err, &forbidden) {
		t.Fatalf("error type = %T, want *acl.ForbiddenError", err)
	}

	// Wire-path invariant: the 403 body (Error()) must NOT carry the
	// attribution chain — that would leak group topology to the
	// unauthorized caller (the AC12-equivalent rule for write denies).
	msg := forbidden.Error()
	for _, leak := range []string{"viewer", "viewers", "group:"} {
		if strings.Contains(msg, leak) {
			t.Errorf("ForbiddenError leaked attribution token %q to wire: %q", leak, msg)
		}
	}

	// Audit-path invariant: exactly one denied-write row, Summary
	// includes `attribution=[role=viewer via group:viewers]`.
	var deniedRecord *audit.Record
	for i := range sink.Records() {
		r := sink.Records()[i]
		if r.Op == audit.OpDeniedWrite {
			if deniedRecord != nil {
				t.Fatalf("found multiple denied-write rows; want exactly 1")
			}
			deniedRecord = &r
		}
	}
	if deniedRecord == nil {
		t.Fatal("no denied-write audit row recorded")
	}
	want := "attribution=[role=viewer via group:viewers]"
	if !strings.Contains(deniedRecord.Summary, want) {
		t.Errorf("audit Summary missing attribution chain.\n  got:  %q\n  want substring: %q",
			deniedRecord.Summary, want)
	}
	// Existing rule_id / op fields still present.
	for _, want := range []string{"rule_kind=role-grant", "rule_id=-", "attempted op=update"} {
		if !strings.Contains(deniedRecord.Summary, want) {
			t.Errorf("audit Summary missing %q; got: %q", want, deniedRecord.Summary)
		}
	}
}
