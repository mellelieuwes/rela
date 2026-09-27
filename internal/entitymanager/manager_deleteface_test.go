package entitymanager_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// DeleteEntityFace removes ONE non-bare face and the edges tailed at it, and
// nothing else (TKT-SLFURL). The rule it must not blur is the store's own:
// outgoing edges of the face go with it, incoming edges point at the ENTITY
// and survive, the bare face and its edges are untouched.
func TestDeleteEntityFace_RemovesOnlyTheFaceAndItsTail(t *testing.T) {
	t.Parallel()
	mem := audit.NewMemory()
	st := memstore.New()
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       st,
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
	ctx := context.Background()
	published := entity.Face("published")

	// REQ-1 with a bare and a published face; DEC-1 as a neighbor.
	for _, e := range []*entity.Entity{
		entity.New("REQ-1", "requirement"),
		{ID: "REQ-1", Type: "requirement", Face: published, Properties: map[string]any{}},
		entity.New("DEC-1", "decision"),
	} {
		if cErr := st.CreateEntity(ctx, e); cErr != nil {
			t.Fatalf("seed %s@%q: %v", e.ID, e.Face, cErr)
		}
	}
	// An edge tailed at the PUBLISHED face, one at the BARE face, and an
	// INCOMING edge from DEC-1 to the entity.
	if _, rErr := st.CreateRelation(ctx, "REQ-1", "addresses", "DEC-1",
		&store.RelationData{FromFace: published}); rErr != nil {
		t.Fatalf("seed published-tail edge: %v", rErr)
	}
	if _, rErr := st.CreateRelation(ctx, "REQ-1", "addresses", "DEC-1", nil); rErr != nil {
		t.Fatalf("seed bare-tail edge: %v", rErr)
	}
	if _, rErr := st.CreateRelation(ctx, "DEC-1", "addresses", "REQ-1", nil); rErr != nil {
		t.Fatalf("seed incoming edge: %v", rErr)
	}

	before := len(mem.Records())
	res, err := mgr.DeleteEntityFace(ctx, "REQ-1", published)
	if err != nil {
		t.Fatalf("DeleteEntityFace: %v", err)
	}
	if len(res.DeletedEntities) != 1 || res.DeletedEntities[0].Face != published {
		t.Errorf("DeletedEntities = %+v, want the published face only", res.DeletedEntities)
	}
	if len(res.DeletedRelations) != 1 || res.DeletedRelations[0].FromFace != published {
		t.Errorf("DeletedRelations = %+v, want the one edge tailed at the published face", res.DeletedRelations)
	}

	if _, gErr := st.GetEntityState(ctx, "REQ-1", published); gErr == nil {
		t.Error("the published face must be gone")
	}
	if _, gErr := st.GetEntity(ctx, "REQ-1"); gErr != nil {
		t.Errorf("the bare face must survive: %v", gErr)
	}
	remaining := 0
	for _, rErr := range st.ListRelations(ctx, store.RelationQuery{EntityID: "REQ-1", Direction: store.DirectionBoth}) {
		if rErr != nil {
			t.Fatalf("list relations: %v", rErr)
		}
		remaining++
	}
	if remaining != 2 {
		t.Errorf("relations left on REQ-1 = %d, want 2 (the bare-tail edge and the incoming edge)", remaining)
	}

	// Audited like a delete: one entity record naming the face, one relation
	// record attributed to the face cascade.
	var entRecords, relRecords int
	for _, r := range mem.Records()[before:] {
		switch r.Op {
		case audit.OpDeleteEntity:
			entRecords++
			if !strings.Contains(r.Summary, "face published") {
				t.Errorf("delete-entity summary = %q, want it to name the face", r.Summary)
			}
		case audit.OpDeleteRelation:
			relRecords++
			if !strings.HasPrefix(r.TriggeredBy, "cascade:delete-face:REQ-1@published") {
				t.Errorf("relation record triggered_by = %q, want the face cascade", r.TriggeredBy)
			}
		}
	}
	if entRecords != 1 || relRecords != 1 {
		t.Errorf("audit records: entity=%d relation=%d, want 1 and 1", entRecords, relRecords)
	}
}

func TestDeleteEntityFace_RefusesTheBareFace(t *testing.T) {
	t.Parallel()
	st := memstore.New()
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       st,
		Meta:        parseMeta(t),
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
	ctx := context.Background()
	if cErr := st.CreateEntity(ctx, entity.New("REQ-1", "requirement")); cErr != nil {
		t.Fatal(cErr)
	}
	// "Delete the bare face" is either the whole entity or undefined; neither
	// is what a caller who spelled a face meant, so it is refused rather than
	// guessed — and nothing is removed.
	if _, dErr := mgr.DeleteEntityFace(ctx, "REQ-1", ""); dErr == nil {
		t.Fatal("deleting the bare face through DeleteEntityFace must be refused")
	}
	if _, gErr := st.GetEntity(ctx, "REQ-1"); gErr != nil {
		t.Errorf("a refused delete must leave the entity: %v", gErr)
	}
	if _, dErr := mgr.DeleteEntityFace(ctx, "REQ-1", "published"); dErr == nil {
		t.Fatal("a face the entity does not have must be not-found")
	}
}
