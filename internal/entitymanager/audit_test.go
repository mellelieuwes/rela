package entitymanager_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/autocascade"
	"github.com/Sourcehaven-BV/rela/internal/automation"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// newManagerWithAudit builds a Manager wired with the supplied Audit
// backend (typically [audit.NewMemory] for assertion). Automations
// are optional — pass nil to disable.
func newManagerWithAudit(
	t *testing.T, sink audit.Audit, automations []automation.Automation,
) *entitymanager.Manager {
	t.Helper()
	deps := entitymanager.Deps{
		Store:       memstore.New(),
		Meta:        parseMeta(t),
		Templater:   nopTemplater{},
		Audit:       sink,
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	}
	if automations != nil {
		engine := automation.NewEngine(automations)
		runner, err := autocascade.New(autocascade.Deps{Engine: engine})
		if err != nil {
			t.Fatalf("autocascade.New: %v", err)
		}
		deps.Automations = engine
		deps.Cascade = runner
	}
	mgr, err := entitymanager.New(deps)
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	return mgr
}

// ctxWithPrincipal is a helper for tests that need to verify audit
// records carry the right Principal.
func ctxWithPrincipal(user, tool string) context.Context {
	return principal.With(context.Background(), principal.Principal{User: user, Tool: tool})
}

// --- AC1: every entity write produces one audit record ---

func TestAudit_AC1_EntityCreateRecordsOnce(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()
	mgr := newManagerWithAudit(t, mem, nil)

	e := entity.New("", "requirement")
	e.SetString("title", "AC1 entity")
	res, err := mgr.CreateEntity(ctxWithPrincipal("alice", principal.ToolCLI), e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	records := mem.Records()
	if len(records) != 1 {
		t.Fatalf("want 1 record, got %d", len(records))
	}
	r := records[0]
	if r.Op != audit.OpCreateEntity {
		t.Errorf("Op = %q, want %q", r.Op, audit.OpCreateEntity)
	}
	if r.Subject.Kind != "entity" {
		t.Errorf("Subject.Kind = %q, want entity", r.Subject.Kind)
	}
	if r.Subject.Type != "requirement" {
		t.Errorf("Subject.Type = %q, want requirement", r.Subject.Type)
	}
	if r.Subject.ID != res.Entity.ID {
		t.Errorf("Subject.ID = %q, want %q", r.Subject.ID, res.Entity.ID)
	}
	if r.Principal.User != "alice" || r.Principal.Tool != principal.ToolCLI {
		t.Errorf("Principal = %+v, want alice/cli", r.Principal)
	}
	if r.Summary != "created" {
		t.Errorf("Summary = %q, want 'created'", r.Summary)
	}
	if r.Time.IsZero() {
		t.Error("Time should be stamped")
	}
}

func TestAudit_AC1_EntityUpdateRecordsChangedPropertyNames(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()
	mgr := newManagerWithAudit(t, mem, nil)

	e := entity.New("", "requirement")
	e.SetString("title", "Initial")
	res, err := mgr.CreateEntity(context.Background(), e, entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	// Update changes title and flips status to a non-default value.
	// (The metamodel default for status is "draft"; setting "draft"
	// again wouldn't show up as a diff.)
	updated := res.Entity.Clone()
	updated.SetString("title", "Modified")
	updated.SetString("status", "accepted")
	if _, err := mgr.UpdateEntity(context.Background(), updated); err != nil {
		t.Fatalf("UpdateEntity: %v", err)
	}

	records := mem.Records()
	if len(records) != 2 {
		t.Fatalf("want 2 records (create + update), got %d", len(records))
	}
	updateRec := records[1]
	if updateRec.Op != audit.OpUpdateEntity {
		t.Errorf("Op = %q, want update-entity", updateRec.Op)
	}
	if !strings.HasPrefix(updateRec.Summary, "updated: ") {
		t.Errorf("Summary = %q, want prefix 'updated: '", updateRec.Summary)
	}
	// Both keys must appear; order is deterministic (sorted).
	if !strings.Contains(updateRec.Summary, "status") {
		t.Errorf("Summary missing 'status': %q", updateRec.Summary)
	}
	if !strings.Contains(updateRec.Summary, "title") {
		t.Errorf("Summary missing 'title': %q", updateRec.Summary)
	}
	// Values must NOT appear (secret-leak defense).
	if strings.Contains(updateRec.Summary, "Modified") || strings.Contains(updateRec.Summary, "accepted") {
		t.Errorf("Summary leaks property values: %q", updateRec.Summary)
	}
}

func TestAudit_AC1_EntityDeleteRecords(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()
	mgr := newManagerWithAudit(t, mem, nil)

	res, err := mgr.CreateEntity(context.Background(),
		entity.New("", "requirement"), entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	if _, err := mgr.DeleteEntity(context.Background(), res.Entity.ID, false); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}

	records := mem.Records()
	if len(records) != 2 {
		t.Fatalf("want 2 records (create + delete), got %d", len(records))
	}
	delRec := records[1]
	if delRec.Op != audit.OpDeleteEntity {
		t.Errorf("Op = %q, want delete-entity", delRec.Op)
	}
	if delRec.Subject.ID != res.Entity.ID {
		t.Errorf("Subject.ID = %q, want %q", delRec.Subject.ID, res.Entity.ID)
	}
	if delRec.Summary != "deleted" {
		t.Errorf("Summary = %q, want 'deleted'", delRec.Summary)
	}
}

func TestAudit_AC1_EntityRenameRecordsBeforeAfter(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()
	mgr := newManagerWithAudit(t, mem, nil)

	res, err := mgr.CreateEntity(context.Background(),
		entity.New("REQ-OLD", "requirement"), entity.CreateOptions{ID: "REQ-OLD"})
	if err != nil {
		// Sequential IDs reject custom — use whatever ID was assigned.
		res, err = mgr.CreateEntity(context.Background(),
			entity.New("", "requirement"), entity.CreateOptions{})
		if err != nil {
			t.Fatalf("CreateEntity: %v", err)
		}
	}
	oldID := res.Entity.ID

	// Use a custom-ID-capable type (decision) for rename; both use
	// sequential IDs but rename takes the operator-supplied new ID.
	_, err = mgr.RenameEntity(context.Background(), oldID, oldID+"-renamed", entity.RenameOptions{})
	if err != nil {
		t.Fatalf("RenameEntity: %v", err)
	}

	records := mem.Records()
	// We expect: 1 create + rename records.
	// (Rename may add records for incident-relation rewrites; this entity has none.)
	var renameRec *audit.Record
	for i := range records {
		if records[i].Op == audit.OpRenameEntity {
			renameRec = &records[i]
		}
	}
	if renameRec == nil {
		t.Fatalf("expected rename-entity record; got: %+v", records)
	}
	if renameRec.Before.ID != oldID {
		t.Errorf("Before.ID = %q, want %q", renameRec.Before.ID, oldID)
	}
	if renameRec.After.ID != oldID+"-renamed" {
		t.Errorf("After.ID = %q, want %q-renamed", renameRec.After.ID, oldID)
	}
	if renameRec.Before.Type != "requirement" || renameRec.After.Type != "requirement" {
		t.Errorf("expected type=requirement in Before/After, got %q/%q",
			renameRec.Before.Type, renameRec.After.Type)
	}
	// Subject must be nil for rename (Before/After carry the diff).
	if renameRec.Subject != nil {
		t.Errorf("rename should leave Subject nil, got %+v", *renameRec.Subject)
	}
	if renameRec.Before == nil || renameRec.After == nil {
		t.Fatalf("rename must populate both Before and After: before=%v after=%v",
			renameRec.Before, renameRec.After)
	}

	// Pin the JSON wire contract too: encoding/json must omit subject
	// (Subject is *Subject specifically so omitempty fires) and emit
	// before / after. A regression that changed Subject back to
	// non-face would still pass the nil-check above (Subject would
	// be a zero struct, not nil) but fail this JSON assertion.
	data, err := json.Marshal(*renameRec)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(data), `"subject"`) {
		t.Errorf("rename JSON must not include subject, got: %s", data)
	}
	if !strings.Contains(string(data), `"before"`) {
		t.Errorf("rename JSON must include before, got: %s", data)
	}
	if !strings.Contains(string(data), `"after"`) {
		t.Errorf("rename JSON must include after, got: %s", data)
	}
}

// --- AC2: every relation write produces one audit record ---

func TestAudit_AC2_RelationCreateRecordsWithRelationSubject(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()
	mgr := newManagerWithAudit(t, mem, nil)

	req, err := mgr.CreateEntity(context.Background(),
		entity.New("", "requirement"), entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity req: %v", err)
	}
	dec, err := mgr.CreateEntity(context.Background(),
		entity.New("", "decision"), entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity dec: %v", err)
	}

	startLen := len(mem.Records())

	rel, err := mgr.CreateRelation(context.Background(),
		dec.Entity.ID, "addresses", req.Entity.ID, entity.RelationOptions{})
	if err != nil {
		t.Fatalf("CreateRelation: %v", err)
	}

	records := mem.Records()
	if len(records) != startLen+1 {
		t.Fatalf("want 1 new record, got %d new (total=%d)", len(records)-startLen, len(records))
	}
	r := records[startLen]
	if r.Op != audit.OpCreateRelation {
		t.Errorf("Op = %q, want create-relation", r.Op)
	}
	if r.Subject.Kind != "relation" {
		t.Errorf("Subject.Kind = %q, want relation", r.Subject.Kind)
	}
	if r.Subject.RelationType != "addresses" {
		t.Errorf("Subject.RelationType = %q, want addresses", r.Subject.RelationType)
	}
	if r.Subject.FromID != rel.From || r.Subject.ToID != rel.To {
		t.Errorf("Subject endpoints = %s -> %s, want %s -> %s",
			r.Subject.FromID, r.Subject.ToID, rel.From, rel.To)
	}
}

func TestAudit_AC2_RelationDeleteRecords(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()
	mgr := newManagerWithAudit(t, mem, nil)

	req, _ := mgr.CreateEntity(context.Background(),
		entity.New("", "requirement"), entity.CreateOptions{})
	dec, _ := mgr.CreateEntity(context.Background(),
		entity.New("", "decision"), entity.CreateOptions{})
	_, _ = mgr.CreateRelation(context.Background(),
		dec.Entity.ID, "addresses", req.Entity.ID, entity.RelationOptions{})

	startLen := len(mem.Records())
	if err := mgr.DeleteRelation(context.Background(),
		dec.Entity.ID, "addresses", req.Entity.ID); err != nil {
		t.Fatalf("DeleteRelation: %v", err)
	}

	records := mem.Records()
	if len(records) != startLen+1 {
		t.Fatalf("want 1 new record, got %d", len(records)-startLen)
	}
	r := records[startLen]
	if r.Op != audit.OpDeleteRelation {
		t.Errorf("Op = %q, want delete-relation", r.Op)
	}
	if r.Subject.FromID != dec.Entity.ID || r.Subject.ToID != req.Entity.ID {
		t.Errorf("Subject endpoints wrong: %+v", r.Subject)
	}
}

// --- AC3: Principal flows from ctx into the record ---

func TestAudit_AC3_PrincipalFromCtx(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()
	mgr := newManagerWithAudit(t, mem, nil)

	ctx := ctxWithPrincipal("alice", principal.ToolMCP)
	_, err := mgr.CreateEntity(ctx, entity.New("", "requirement"), entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	r := mem.Records()[0]
	if r.Principal.User != "alice" {
		t.Errorf("Principal.User = %q, want alice", r.Principal.User)
	}
	if r.Principal.Tool != principal.ToolMCP {
		t.Errorf("Principal.Tool = %q, want mcp", r.Principal.Tool)
	}
}

func TestAudit_AC3_PrincipalDefaultsUnknownWhenAbsent(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()
	mgr := newManagerWithAudit(t, mem, nil)

	// ctx without WithPrincipal — should default to unknown/unknown.
	_, err := mgr.CreateEntity(context.Background(), entity.New("", "requirement"), entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	r := mem.Records()[0]
	if r.Principal.User != "unknown" || r.Principal.Tool != "unknown" {
		t.Errorf("Principal = %+v, want unknown/unknown", r.Principal)
	}
}

// --- AC7: delete-cascade produces 1+N records ---

func TestAudit_AC7_DeleteCascadeProduces1PlusNRecords(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()
	mgr := newManagerWithAudit(t, mem, nil)

	req, _ := mgr.CreateEntity(context.Background(),
		entity.New("", "requirement"), entity.CreateOptions{})
	dec1, _ := mgr.CreateEntity(context.Background(),
		entity.New("", "decision"), entity.CreateOptions{})
	dec2, _ := mgr.CreateEntity(context.Background(),
		entity.New("", "decision"), entity.CreateOptions{})

	_, _ = mgr.CreateRelation(context.Background(),
		dec1.Entity.ID, "addresses", req.Entity.ID, entity.RelationOptions{})
	_, _ = mgr.CreateRelation(context.Background(),
		dec2.Entity.ID, "addresses", req.Entity.ID, entity.RelationOptions{})

	startLen := len(mem.Records())

	if _, err := mgr.DeleteEntity(context.Background(), req.Entity.ID, true); err != nil {
		t.Fatalf("DeleteEntity cascade: %v", err)
	}

	// Expect 2 relation-delete records + 1 entity-delete = 3 new records.
	records := mem.Records()
	newRecords := records[startLen:]
	if len(newRecords) != 3 {
		t.Fatalf("want 3 new records (2 rel + 1 entity), got %d: %+v", len(newRecords), newRecords)
	}

	entityDeletes := 0
	relationDeletes := 0
	for _, r := range newRecords {
		switch r.Op {
		case audit.OpDeleteEntity:
			entityDeletes++
			if !strings.Contains(r.Summary, "cascade") {
				t.Errorf("entity-delete summary should mention cascade, got %q", r.Summary)
			}
		case audit.OpDeleteRelation:
			relationDeletes++
			expected := "cascade:delete-entity:" + req.Entity.ID
			if r.TriggeredBy != expected {
				t.Errorf("relation-delete TriggeredBy = %q, want %q", r.TriggeredBy, expected)
			}
		}
	}
	if entityDeletes != 1 {
		t.Errorf("want 1 entity-delete, got %d", entityDeletes)
	}
	if relationDeletes != 2 {
		t.Errorf("want 2 relation-deletes, got %d", relationDeletes)
	}
}

// --- AC5: triggered_by populated for automation-driven writes ---

func TestAudit_AC5_TriggeredByOnAutomationCascade(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()

	// Automation: when a requirement is created, auto-create a checklist
	// related via has-checklist.
	autos := []automation.Automation{{
		Name: "create-checklist-for-req",
		On: automation.Trigger{
			Entity:  []string{"requirement"},
			Created: true,
		},
		Do: []automation.Action{{
			CreateEntity: &automation.CreateEntityAction{
				Type:     "checklist",
				Relation: "has-checklist",
			},
		}},
	}}
	mgr := newManagerWithAudit(t, mem, autos)

	ctx := ctxWithPrincipal("alice", principal.ToolCLI)
	_, err := mgr.CreateEntity(ctx, entity.New("", "requirement"), entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	records := mem.Records()
	// Expect:
	//   - 1 create-entity record for the requirement (no triggered_by).
	//   - 1 create-entity record for the cascaded checklist.
	//   - 1 create-relation record for has-checklist.
	//
	// Both cascaded records name the ORIGINATING automation
	// (`automation:create-checklist-for-req`), not the generic `automation`
	// label. Tightened from "non-empty" by TKT-JJRVX9 — the generic label
	// could not tell an operator which of several on:created rules fired.
	const wantLabel = "automation:create-checklist-for-req"
	var direct, cascadedEntity, cascadedRelation int
	for _, r := range records {
		switch {
		case r.Op == audit.OpCreateEntity && r.TriggeredBy == "":
			direct++
		case r.Op == audit.OpCreateEntity && r.TriggeredBy != "":
			cascadedEntity++
			if r.TriggeredBy != wantLabel {
				t.Errorf("cascaded create-entity: want TriggeredBy=%q, got %q", wantLabel, r.TriggeredBy)
			}
		case r.Op == audit.OpCreateRelation && r.TriggeredBy != "":
			cascadedRelation++
			if r.TriggeredBy != wantLabel {
				t.Errorf("cascaded create-relation: want TriggeredBy=%q, got %q", wantLabel, r.TriggeredBy)
			}
		}
		// All records must inherit the user's Principal.
		if r.Principal.User != "alice" {
			t.Errorf("expected Principal.User=alice on every record, got %q on %s", r.Principal.User, r.Op)
		}
	}

	if direct != 1 {
		t.Errorf("want 1 direct create-entity, got %d", direct)
	}
	if cascadedEntity == 0 {
		t.Errorf("want >=1 cascaded create-entity records with TriggeredBy, got 0")
	}
	if cascadedRelation == 0 {
		t.Errorf("want >=1 cascaded create-relation records with TriggeredBy, got 0")
	}
}

// TestAudit_IfExistsReplaceAttributesDeleteToTheAutomation pins that BOTH
// halves of an `if_exists: replace` carry the same automation label
// (TKT-JJRVX9).
//
// Replace is one operation with two writes: delete the superseded entity, then
// create its replacement. Before this ticket the create carried the specific
// label and the delete carried the generic `automation`, because the runner
// derived its tagged ctx AFTER calling handleIfExists. Two labels on adjacent
// rows of one operation meant an operator filtering on the automation's name
// got the create and silently missed the delete — the exact question this
// ticket exists to make answerable.
//
// The cascaded relation-deletes underneath keep `cascade:delete-entity:<id>`;
// that is asserted by TestAudit_IfExistsReplaceUsesCascadeLabel below, and the
// two together describe the full label set for a replace.
func TestAudit_IfExistsReplaceAttributesDeleteToTheAutomation(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()

	const autoName = "replace-checklist-on-active"
	autos := []automation.Automation{{
		Name: autoName,
		On: automation.Trigger{
			Entity:   []string{"requirement"},
			Property: "status",
			Becomes:  "accepted",
		},
		Do: []automation.Action{{
			CreateEntity: &automation.CreateEntityAction{
				Type:     "checklist",
				Relation: "has-checklist",
				IfExists: automation.IfExistsReplace,
			},
		}},
	}}
	mgr := newManagerWithAudit(t, mem, autos)
	ctx := ctxWithPrincipal("alice", principal.ToolCLI)

	res, err := mgr.CreateEntity(ctx, entity.New("", "requirement"), entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	// Trip the automation, drop out of the triggering state, then trip it
	// again — the second run is the one that finds an existing checklist and
	// takes the replace path.
	req := res.Entity
	for _, status := range []string{"accepted", "draft", "accepted"} {
		req.SetString("status", status)
		if _, uerr := mgr.UpdateEntity(ctx, req); uerr != nil {
			t.Fatalf("UpdateEntity(status=%s): %v", status, uerr)
		}
	}

	var sawDelete bool
	for _, r := range mem.Records() {
		if r.Op != audit.OpDeleteEntity || r.Subject == nil || r.Subject.Type != "checklist" {
			continue
		}
		sawDelete = true
		if r.TriggeredBy != "automation:"+autoName {
			t.Errorf("replace-delete: want TriggeredBy=%q, got %q", "automation:"+autoName, r.TriggeredBy)
		}
	}
	if !sawDelete {
		t.Fatal("no delete-entity record for the superseded checklist; the replace path did not run")
	}
}

// TestAudit_IfExistsReplaceUsesCascadeLabel verifies that the
// IfExistsReplace path through autocascade.Runner →
// cascadeHost.DeleteEntity labels the cascaded relation-deletes with
// `cascade:delete-entity:<id>` — symmetric with the direct
// Manager.DeleteEntity path. Without this, replace operations would
// be indistinguishable from automation-generated relation deletes in
// the audit trail.
//
// The test drives the full production path (Manager.UpdateEntity →
// engine → Runner → host) so a ctx-threading regression in any of
// the intermediate hops fails this test, not just a direct
// cascadeHost call.
func TestAudit_IfExistsReplaceUsesCascadeLabel(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()

	// Automation: when requirement.status becomes "active", create a
	// new checklist via has-checklist with if_exists: replace. The
	// trigger is a status change so we can seed an existing checklist
	// first, then drive the replace.
	autos := []automation.Automation{{
		Name: "replace-checklist-on-active",
		On: automation.Trigger{
			Entity:   []string{"requirement"},
			Property: "status",
			Becomes:  "accepted",
		},
		Do: []automation.Action{{
			CreateEntity: &automation.CreateEntityAction{
				Type:     "checklist",
				Relation: "has-checklist",
				IfExists: automation.IfExistsReplace,
			},
		}},
	}}
	mgr := newManagerWithAudit(t, mem, autos)

	// Seed: create the requirement and an existing checklist with the
	// has-checklist relation in place. The automation's replace path
	// will delete the existing checklist, which cascades to the
	// has-checklist relation.
	req, err := mgr.CreateEntity(context.Background(),
		entity.New("", "requirement"), entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity req: %v", err)
	}
	cl, err := mgr.CreateEntity(context.Background(),
		entity.New("", "checklist"), entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity checklist: %v", err)
	}
	if _, err := mgr.CreateRelation(context.Background(),
		req.Entity.ID, "has-checklist", cl.Entity.ID, entity.RelationOptions{}); err != nil {
		t.Fatalf("CreateRelation: %v", err)
	}

	startLen := len(mem.Records())

	// Drive the replace via UpdateEntity → automation → cascade.
	updated := req.Entity.Clone()
	updated.SetString("status", "accepted")
	if _, err := mgr.UpdateEntity(context.Background(), updated); err != nil {
		t.Fatalf("UpdateEntity: %v", err)
	}

	newRecords := mem.Records()[startLen:]
	wantTrigger := "cascade:delete-entity:" + cl.Entity.ID

	var relDeletes int
	for _, r := range newRecords {
		if r.Op == audit.OpDeleteRelation {
			relDeletes++
			if r.TriggeredBy != wantTrigger {
				t.Errorf("relation-delete TriggeredBy = %q, want %q", r.TriggeredBy, wantTrigger)
			}
		}
	}
	if relDeletes == 0 {
		t.Fatalf("expected at least one relation-delete record from cascade; got records: %+v", newRecords)
	}
}

// TestAudit_CascadeWriteEntityIsSilent pins that
// autocascade.Host.WriteEntity is *not* audited from cascadeHost.
// The Runner uses WriteEntity to persist post-cascade property
// changes onto an entity that was already audited at CreateEntity
// time; emitting again would double-count the same entity creation.
// This test sets up an automation chain where a cascade-created
// entity itself triggers another automation that sets a property —
// which causes the Runner to call WriteEntity — and asserts no
// duplicate entity-create record appears.
func TestAudit_CascadeWriteEntityIsSilent(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()

	autos := []automation.Automation{
		{
			Name: "create-checklist-for-req",
			On: automation.Trigger{
				Entity:  []string{"requirement"},
				Created: true,
			},
			Do: []automation.Action{{
				CreateEntity: &automation.CreateEntityAction{
					Type:     "checklist",
					Relation: "has-checklist",
				},
			}},
		},
		{
			// When the cascade-created checklist fires its own
			// created-event, set status — this is what makes the
			// Runner call host.WriteEntity to persist the property.
			Name: "stamp-status-on-checklist",
			On: automation.Trigger{
				Entity:  []string{"checklist"},
				Created: true,
			},
			Do: []automation.Action{{
				Set:   "status",
				Value: "draft",
			}},
		},
	}
	mgr := newManagerWithAudit(t, mem, autos)

	_, err := mgr.CreateEntity(context.Background(),
		entity.New("", "requirement"), entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	records := mem.Records()

	// Expect exactly ONE create-entity record for the checklist
	// (not two: one for the create, one for the property-set
	// WriteEntity). The Set is silent by design.
	var checklistCreates int
	for _, r := range records {
		if r.Op == audit.OpCreateEntity && r.Subject != nil && r.Subject.Type == "checklist" {
			checklistCreates++
		}
	}
	if checklistCreates != 1 {
		t.Errorf("want exactly 1 create-entity record for checklist (WriteEntity from "+
			"property-set cascade should be silent), got %d", checklistCreates)
	}
}

// --- AC11: Nop is safe ---

func TestAudit_AC11_NopRecordsNothing(t *testing.T) {
	t.Parallel()
	// Construct with Nop — no panics, no observable side effects.
	mgr := newManagerWithAudit(t, audit.Nop{}, nil)
	_, err := mgr.CreateEntity(context.Background(),
		entity.New("", "requirement"), entity.CreateOptions{})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
}

// --- AC12: nil Audit is rejected at construction (already covered by
// TestNew_RejectsNilAudit in manager_test.go) ---

// TestAudit_PerAutomationAttribution is the discriminating test for
// TKT-JJRVX9: TWO automations on the same trigger, with different names, each
// producing a cascade write. Each resulting audit record must name ITS OWN
// automation.
//
// Two is the minimum that catches the bug this ticket is really about. With a
// single automation, an implementation that hoists the `triggered_by` ctx wrap
// out of the per-entry loop — attributing every write to whichever name it saw
// last — passes anyway. With two, it cannot.
func TestAudit_PerAutomationAttribution(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()

	// Both fire on requirement-created, and BOTH emit a create_relation. That
	// matters: Engine.Process aggregates every matching automation into ONE
	// Result, so the two relations arrive in a single RelationsToCreate slice.
	// A per-entry ctx tag attributes each correctly; a tag hoisted out of the
	// loop gives both whichever name came last. `spawn-checklist` also creates
	// an entity, covering the create_entity path in the same run.
	autos := []automation.Automation{
		{
			Name: "spawn-checklist",
			On:   automation.Trigger{Entity: []string{"requirement"}, Created: true},
			Do: []automation.Action{
				{
					CreateEntity: &automation.CreateEntityAction{
						Type:     "checklist",
						Relation: "has-checklist",
					},
				},
				{
					CreateRelation: &automation.CreateRelationAction{
						Relation: "informed-by",
						To:       "DEC-001",
					},
				},
			},
		},
		{
			Name: "link-decision",
			On:   automation.Trigger{Entity: []string{"requirement"}, Created: true},
			Do: []automation.Action{{
				CreateRelation: &automation.CreateRelationAction{
					Relation: "supersedes",
					To:       "DEC-001",
				},
			}},
		},
	}
	mgr := newManagerWithAudit(t, mem, autos)
	ctx := ctxWithPrincipal("alice", principal.ToolCLI)

	// The relation target must exist before the trigger fires.
	if _, err := mgr.CreateEntity(ctx, entity.New("DEC-001", "decision"), entity.CreateOptions{}); err != nil {
		t.Fatalf("seed decision: %v", err)
	}
	if _, err := mgr.CreateEntity(ctx, entity.New("", "requirement"), entity.CreateOptions{}); err != nil {
		t.Fatalf("CreateEntity requirement: %v", err)
	}

	// Collect the cascade-attributed records by relation type / entity type.
	var sawChecklistEntity, sawHasChecklist, sawInformedBy, sawSupersedes bool
	for _, r := range mem.Records() {
		if r.TriggeredBy == "" || r.Subject == nil {
			continue // direct writes
		}
		switch {
		case r.Op == audit.OpCreateEntity && r.Subject.Type == "checklist":
			sawChecklistEntity = true
			if r.TriggeredBy != "automation:spawn-checklist" {
				t.Errorf("checklist entity: want automation:spawn-checklist, got %q", r.TriggeredBy)
			}
		case r.Op == audit.OpCreateRelation && r.Subject.RelationType == "has-checklist":
			sawHasChecklist = true
			// The trigger relation belongs to the automation that created the
			// entity, not to whichever automation ran last.
			if r.TriggeredBy != "automation:spawn-checklist" {
				t.Errorf("has-checklist relation: want automation:spawn-checklist, got %q", r.TriggeredBy)
			}
		case r.Op == audit.OpCreateRelation && r.Subject.RelationType == "informed-by":
			sawInformedBy = true
			if r.TriggeredBy != "automation:spawn-checklist" {
				t.Errorf("informed-by relation: want automation:spawn-checklist, got %q", r.TriggeredBy)
			}
		case r.Op == audit.OpCreateRelation && r.Subject.RelationType == "supersedes":
			sawSupersedes = true
			if r.TriggeredBy != "automation:link-decision" {
				t.Errorf("supersedes relation: want automation:link-decision, got %q", r.TriggeredBy)
			}
		}
	}

	if !sawChecklistEntity {
		t.Error("no cascade-attributed create-entity record for the checklist")
	}
	if !sawHasChecklist {
		t.Error("no cascade-attributed create-relation record for has-checklist")
	}
	if !sawInformedBy {
		t.Error("no cascade-attributed create-relation record for informed-by")
	}
	if !sawSupersedes {
		t.Error("no cascade-attributed create-relation record for supersedes")
	}
}

// TestAudit_UnnamedAutomationKeepsGenericLabel pins the fallback (TKT-JJRVX9):
// a cascade entry carrying no automation name must record the generic
// "automation" label, NOT a dangling "automation:".
//
// This is reachable from a user-authored schema, not hypothetical: `name:` is
// an ordinary optional field on an `automations:` list entry, and no loader
// validation rejects an automation that omits it. So the test drives the FULL
// production path — Manager.CreateEntity through the engine, runner and audit
// sink — with a nameless automation, rather than poking the runner directly.
func TestAudit_UnnamedAutomationKeepsGenericLabel(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()

	// An automation with an EMPTY name: the engine plumbs "" through, and the
	// cascade must fall back rather than emit "automation:".
	autos := []automation.Automation{{
		Name: "",
		On:   automation.Trigger{Entity: []string{"requirement"}, Created: true},
		Do: []automation.Action{{
			CreateEntity: &automation.CreateEntityAction{
				Type:     "checklist",
				Relation: "has-checklist",
			},
		}},
	}}
	mgr := newManagerWithAudit(t, mem, autos)
	ctx := ctxWithPrincipal("alice", principal.ToolCLI)

	if _, err := mgr.CreateEntity(ctx, entity.New("", "requirement"), entity.CreateOptions{}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	var cascaded int
	for _, r := range mem.Records() {
		if r.TriggeredBy == "" {
			continue
		}
		cascaded++
		if r.TriggeredBy != "automation" {
			t.Errorf("unnamed automation: want generic %q, got %q", "automation", r.TriggeredBy)
		}
	}
	// Assert the exact count, not just non-zero: a future change that stopped
	// one of the two cascade writes firing would otherwise quietly reduce this
	// test's coverage instead of failing.
	if cascaded != 2 {
		t.Errorf("want 2 cascade-attributed records (checklist entity + has-checklist relation), got %d", cascaded)
	}
}

// TestAudit_NestedCascadeAttribution covers the multi-level case for
// TKT-JJRVX9: automation "outer" creates a checklist, which itself triggers
// automation "inner". Each write must name the automation that actually
// produced it, not the one that started the chain.
//
// This works because the cascade runner queues one item per created entity,
// each carrying its OWN automation Result — so the name travels with the work
// rather than being derived from the originating trigger. Pinned here because
// a future change that hoisted attribution to the cascade's entry point would
// silently collapse every level onto the outermost automation.
func TestAudit_NestedCascadeAttribution(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()

	autos := []automation.Automation{
		{
			Name: "outer",
			On:   automation.Trigger{Entity: []string{"requirement"}, Created: true},
			Do: []automation.Action{{
				CreateEntity: &automation.CreateEntityAction{
					Type:     "checklist",
					Relation: "has-checklist",
				},
			}},
		},
		{
			Name: "inner",
			On:   automation.Trigger{Entity: []string{"checklist"}, Created: true},
			Do: []automation.Action{{
				CreateRelation: &automation.CreateRelationAction{
					Relation: "covers",
					To:       "DEC-001",
				},
			}},
		},
	}
	mgr := newManagerWithAudit(t, mem, autos)
	ctx := ctxWithPrincipal("alice", principal.ToolCLI)

	if _, err := mgr.CreateEntity(ctx, entity.New("DEC-001", "decision"), entity.CreateOptions{}); err != nil {
		t.Fatalf("seed decision: %v", err)
	}
	if _, err := mgr.CreateEntity(ctx, entity.New("", "requirement"), entity.CreateOptions{}); err != nil {
		t.Fatalf("CreateEntity requirement: %v", err)
	}

	var sawOuterEntity, sawInnerRelation bool
	for _, r := range mem.Records() {
		if r.TriggeredBy == "" || r.Subject == nil {
			continue
		}
		switch {
		case r.Op == audit.OpCreateEntity && r.Subject.Type == "checklist":
			sawOuterEntity = true
			if r.TriggeredBy != "automation:outer" {
				t.Errorf("checklist entity: want automation:outer, got %q", r.TriggeredBy)
			}
		case r.Op == audit.OpCreateRelation && r.Subject.RelationType == "covers":
			sawInnerRelation = true
			// The decisive assertion: this write came from the SECOND-level
			// automation, so it must not inherit "outer".
			if r.TriggeredBy != "automation:inner" {
				t.Errorf("covers relation: want automation:inner, got %q", r.TriggeredBy)
			}
		}
	}

	if !sawOuterEntity {
		t.Error("no cascade-attributed create-entity record for the checklist")
	}
	if !sawInnerRelation {
		t.Error("no cascade-attributed create-relation record for covers (second-level automation did not fire)")
	}
}

// TestAudit_OuterLabelSurvivesCascade pins the composition policy for
// `triggered_by`: when a write has two true causes, the OUTERMOST one wins.
//
// The case that matters is a scheduler task whose write trips an on:created
// automation. "What did last night's task write?" is the most obvious audit
// query for a scheduler, and answering it requires the cascaded rows to keep
// `schedule:<task>` rather than being relabelled with the inner automation.
//
// This is a REGRESSION TEST in the literal sense: the first version of
// TKT-JJRVX9 tagged the cascade ctx unconditionally, and
// `audit.WithTriggeredBy` is an unconditional context.WithValue — so the
// cascaded rows silently changed from `schedule:nightly` to
// `automation:<name>`, shrinking that query's result set with nothing to
// error on. The guard lives in autocascade's triggeredByCtx.
//
// Note this could NOT be caught by TestAudit_IfExistsReplaceUsesCascadeLabel:
// that one asserts a label cascadeHost.DeleteEntity stamps internally,
// downstream of the runner's wrap, so it never exercises an enclosing label.
func TestAudit_OuterLabelSurvivesCascade(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()

	autos := []automation.Automation{{
		Name: "spawn-checklist",
		On:   automation.Trigger{Entity: []string{"requirement"}, Created: true},
		Do: []automation.Action{{
			CreateEntity: &automation.CreateEntityAction{
				Type:     "checklist",
				Relation: "has-checklist",
			},
		}},
	}}
	mgr := newManagerWithAudit(t, mem, autos)

	// Exactly what scheduler.go does before running a task.
	const outer = "schedule:nightly"
	ctx := audit.WithTriggeredBy(ctxWithPrincipal("alice", principal.ToolScheduler), outer)

	if _, err := mgr.CreateEntity(ctx, entity.New("", "requirement"), entity.CreateOptions{}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	records := mem.Records()
	if len(records) == 0 {
		t.Fatal("no audit records emitted")
	}
	// EVERY record in this run — the direct write and both cascaded ones —
	// must carry the schedule label, so a `triggered_by == "schedule:nightly"`
	// query returns the complete set of what the task did.
	var cascaded int
	for _, r := range records {
		if r.TriggeredBy != outer {
			t.Errorf("op=%s: want TriggeredBy=%q, got %q", r.Op, outer, r.TriggeredBy)
		}
		if r.Subject != nil && (r.Subject.Type == "checklist" || r.Subject.RelationType == "has-checklist") {
			cascaded++
		}
	}
	if cascaded != 2 {
		t.Errorf("want 2 cascaded records (checklist entity + has-checklist relation), got %d", cascaded)
	}
}

// partialCascadeStore is a store whose cascade DeleteEntity fails but reports
// the relations it removed first — the shape a non-transactional backend
// produces when a cascade aborts partway (fsstore, issue #929).
//
// Tx hands the callback THIS store rather than delegating, so the manager's
// delete runs against the failing method. Everything else is promoted from the
// embedded real store, so the pre-delete reads the manager performs still work.
type partialCascadeStore struct {
	store.Store
	removed []*entity.Relation
	err     error
}

func (p *partialCascadeStore) Tx(_ context.Context, fn func(store.Store) error) error {
	return fn(p)
}

func (p *partialCascadeStore) DeleteEntity(
	_ context.Context, _ string, _ bool,
) (*store.DeleteResult, error) {
	// DeletedEntities deliberately empty: the entity survived.
	return &store.DeleteResult{DeletedRelations: p.removed}, p.err
}

// TestAudit_PartialCascadeDelete_AuditsWhatWasRemoved pins issue #929: when a
// cascade delete fails partway, the relations already removed from disk must
// still reach the audit log.
//
// fsstore's Tx is a write mutex with no rollback, so those removals stick. The
// manager previously returned on the store error before reaching its audit
// loop, so the log denied a deletion that had really happened.
//
// Two assertions carry the ticket:
//   - the removed relation IS recorded, with the same
//     `cascade:delete-entity:<id>` label the success path uses, so a partial
//     and a complete cascade are indistinguishable except by row count;
//   - NO delete-entity record is emitted. The entity survived, and a log
//     claiming otherwise would be the opposite error — over-reporting is as
//     broken as under-reporting, and harder to notice.
func TestAudit_PartialCascadeDelete_AuditsWhatWasRemoved(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()

	// Seed the REAL relation, not just a fabricated one in the double: the
	// manager collects the incident set and runs authorizeCascadeRelations
	// before it ever calls the store, so a store double reporting a deletion
	// of a relation the manager just observed did not exist would exercise a
	// shape fsstore cannot produce — and would pass even with the ACL gate
	// deleted.
	backing := memstore.New()
	bg := context.Background()
	for _, e := range []*entity.Entity{
		entity.New("REQ-1", "requirement"),
		entity.New("CL-1", "checklist"),
	} {
		if err := backing.CreateEntity(bg, e); err != nil {
			t.Fatalf("seed %s: %v", e.ID, err)
		}
	}
	rel, rerr := backing.CreateRelation(bg, "REQ-1", "has-checklist", "CL-1", nil)
	if rerr != nil {
		t.Fatalf("seed relation: %v", rerr)
	}

	failing := &partialCascadeStore{
		Store:   backing,
		removed: []*entity.Relation{rel},
		err:     errors.New("simulated I/O failure on the second relation"),
	}

	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       failing,
		Meta:        parseMeta(t),
		Templater:   nopTemplater{},
		Audit:       mem,
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}

	ctx := ctxWithPrincipal("alice", principal.ToolCLI)
	if _, derr := mgr.DeleteEntity(ctx, "REQ-1", true); derr == nil {
		t.Fatal("DeleteEntity must still return the store error")
	}

	var relRecords, entityRecords int
	for _, r := range mem.Records() {
		switch r.Op {
		case audit.OpDeleteRelation:
			relRecords++
			if r.TriggeredBy != "cascade:delete-entity:REQ-1" {
				t.Errorf("partial-cascade relation delete: want TriggeredBy=%q, got %q",
					"cascade:delete-entity:REQ-1", r.TriggeredBy)
			}
		case audit.OpDeleteEntity:
			entityRecords++
		}
	}

	if relRecords != 1 {
		t.Errorf("want 1 delete-relation record for the relation actually removed, got %d", relRecords)
	}
	if entityRecords != 0 {
		t.Errorf("want NO delete-entity record — the entity survived — got %d", entityRecords)
	}
}

// TestAudit_PartialCascadeDelete_ReplacePathAlsoAudits pins that the
// if_exists:replace route through cascadeHost.DeleteEntity gets the same
// partial-cascade treatment as the direct Manager.DeleteEntity path
// (issue #929).
//
// The two are separate call sites into the same store method, so fixing only
// one would mean an operator sees the removed relation logged for a direct
// delete and NOT for a replace — the same failure wearing a different hat.
//
// Driven through the real automation so it exercises cascadeHost rather than a
// test-only export: the replace action needs an existing checklist to
// supersede, which is what the seeded relation provides.
func TestAudit_PartialCascadeDelete_ReplacePathAlsoAudits(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()
	bg := context.Background()

	backing := memstore.New()
	for _, e := range []*entity.Entity{
		entity.New("REQ-1", "requirement"),
		entity.New("CL-1", "checklist"),
	} {
		if err := backing.CreateEntity(bg, e); err != nil {
			t.Fatalf("seed %s: %v", e.ID, err)
		}
	}
	// The existing edge the replace action will find and supersede.
	if _, err := backing.CreateRelation(bg, "REQ-1", "has-checklist", "CL-1", nil); err != nil {
		t.Fatalf("seed relation: %v", err)
	}

	failing := &partialCascadeStore{
		Store:   backing,
		removed: []*entity.Relation{entity.NewRelation("REQ-1", "has-checklist", "CL-1")},
		err:     errors.New("simulated I/O failure on the second relation"),
	}

	autos := []automation.Automation{{
		Name: "replace-checklist",
		On: automation.Trigger{
			Entity:   []string{"requirement"},
			Property: "status",
			Becomes:  "accepted",
		},
		Do: []automation.Action{{
			CreateEntity: &automation.CreateEntityAction{
				Type:     "checklist",
				Relation: "has-checklist",
				IfExists: automation.IfExistsReplace,
			},
		}},
	}}
	engine := automation.NewEngine(autos)
	runner, rerr := autocascade.New(autocascade.Deps{Engine: engine})
	if rerr != nil {
		t.Fatalf("autocascade.New: %v", rerr)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       failing,
		Meta:        parseMeta(t),
		Templater:   nopTemplater{},
		Audit:       mem,
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

	ctx := ctxWithPrincipal("alice", principal.ToolCLI)
	req := entity.New("REQ-1", "requirement")
	req.SetString("status", "accepted")
	if _, uerr := mgr.UpdateEntity(ctx, req); uerr != nil {
		t.Fatalf("UpdateEntity: %v", uerr)
	}

	var relRecords, entityRecords int
	for _, r := range mem.Records() {
		switch r.Op {
		case audit.OpDeleteRelation:
			relRecords++
			if r.TriggeredBy != "cascade:delete-entity:CL-1" {
				t.Errorf("replace-path partial cascade: want TriggeredBy=%q, got %q",
					"cascade:delete-entity:CL-1", r.TriggeredBy)
			}
		case audit.OpDeleteEntity:
			entityRecords++
		}
	}
	if relRecords != 1 {
		t.Errorf("want 1 delete-relation record from the replace path, got %d", relRecords)
	}
	if entityRecords != 0 {
		t.Errorf("want NO delete-entity record — the superseded entity survived — got %d", entityRecords)
	}
}
