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
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// relationFaceYAML pairs a FACED source type with both relation scopes, so
// the symmetric rule can be exercised in every combination that matters:
// content-scoped edges carry a face, identity-scoped edges never do.
const relationFaceYAML = `
entities:
  beleid:
    label: Beleid
    id_prefix: "POL-"
    id_type: manual
    faces:
      concept: {label: Concept}
      vastgesteld: {label: Vastgesteld}
    properties:
      title: {type: string}
  bron:
    label: Bron
    id_prefix: "SRC-"
    id_type: manual
    properties:
      title: {type: string}
relations:
  citeert:
    label: Citeert
    scope: content
    from: [beleid]
    to: [bron]
  geschreven-door:
    label: Geschreven door
    scope: identity
    from: [beleid]
    to: [bron]
  bron-verwijst:
    label: Bron verwijst
    scope: content
    from: [bron]
    to: [bron]
`

func relationFaceManager(t *testing.T) (*entitymanager.Manager, *memstore.MemStore) {
	t.Helper()
	meta, err := metamodel.Parse([]byte(relationFaceYAML))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	st := memstore.New()
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store: st, Meta: meta, Templater: nopTemplater{}, Audit: audit.Nop{},
		ACL: acl.NopACL{}, Transitions: statemachine.EmptySet(),
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	return mgr, st
}

// seedRelationFaceGraph creates a faced source on both faces plus a faceless
// source and a target, so every case below has real endpoints.
func seedRelationFaceGraph(t *testing.T, mgr *entitymanager.Manager) {
	t.Helper()
	ctx := context.Background()
	for _, face := range []entity.Face{"concept", "vastgesteld"} {
		if _, err := mgr.CreateEntity(ctx, &entity.Entity{
			Type: "beleid", Properties: map[string]any{"title": "P"},
		}, entity.CreateOptions{ID: "POL-1", Face: face}); err != nil {
			t.Fatalf("seed beleid@%s: %v", face, err)
		}
	}
	if _, err := mgr.CreateEntity(ctx, &entity.Entity{
		Type: "bron", Properties: map[string]any{"title": "S"},
	}, entity.CreateOptions{ID: "SRC-1"}); err != nil {
		t.Fatalf("seed bron: %v", err)
	}
	if _, err := mgr.CreateEntity(ctx, &entity.Entity{
		Type: "bron", Properties: map[string]any{"title": "S2"},
	}, entity.CreateOptions{ID: "SRC-2"}); err != nil {
		t.Fatalf("seed bron 2: %v", err)
	}
}

// TestCreateRelation_FaceMustMatchScope is the rule RR-9LM7T7 found missing:
// before it, opts.FromFace reached the store with no metamodel consultation
// at all, so an identity-scoped edge could be filed at a face no reader ever
// queries — present in storage and invisible to every read.
func TestCreateRelation_FaceMustMatchScope(t *testing.T) {
	tests := []struct {
		name    string
		from    string
		relType string
		to      string
		face    entity.Face
		wantErr error
	}{
		{
			name: "content scope on faced source accepts a declared face",
			from: "POL-1", relType: "citeert", to: "SRC-1",
			face: "concept", wantErr: nil,
		},
		{
			name: "content scope on faced source rejects an undeclared face",
			from: "POL-1", relType: "citeert", to: "SRC-1",
			face: "nope", wantErr: entitymanager.ErrFaceNotDeclared,
		},
		{
			// A zero tail is ACCEPTED, unlike the entity-create rule. It
			// addresses the identity coordinate — a real, readable edge —
			// rather than a row that cannot exist. Requiring a face here
			// would break every caller that cannot yet supply one; see
			// TestCreateRelation_ZeroTailStillWorksForFacelessCallers.
			name: "content scope on faced source ACCEPTS a zero face",
			from: "POL-1", relType: "citeert", to: "SRC-1",
			face: "", wantErr: nil,
		},
		{
			// The case that motivated the check. `geschreven-door` attaches
			// to the entity, so a tail names a row that does not exist.
			name: "identity scope rejects any face",
			from: "POL-1", relType: "geschreven-door", to: "SRC-1",
			face: "concept", wantErr: entitymanager.ErrFaceNotDeclared,
		},
		{
			name: "identity scope accepts the zero face",
			from: "POL-1", relType: "geschreven-door", to: "SRC-1",
			face: "", wantErr: nil,
		},
		{
			name: "content scope on a FACELESS source rejects a face",
			from: "SRC-1", relType: "bron-verwijst", to: "SRC-2",
			face: "concept", wantErr: entitymanager.ErrFaceNotDeclared,
		},
		{
			name: "content scope on a faceless source accepts the zero face",
			from: "SRC-1", relType: "bron-verwijst", to: "SRC-2",
			face: "", wantErr: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr, st := relationFaceManager(t)
			seedRelationFaceGraph(t, mgr)
			ctx := context.Background()

			_, err := mgr.CreateRelation(ctx, tc.from, tc.relType, tc.to,
				entity.RelationOptions{FromFace: tc.face})

			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("CreateRelation: unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("CreateRelation error = %v, want %v", err, tc.wantErr)
			}
			// A refusal must also mean NO WRITE. An implementation that
			// validated after calling the store would satisfy the error
			// assertion above while having already filed the edge.
			for rel, lErr := range st.ListRelations(ctx, store.RelationQuery{}) {
				if lErr != nil {
					t.Fatalf("ListRelations: %v", lErr)
				}
				if rel.From == tc.from && rel.Type == tc.relType && rel.To == tc.to {
					t.Fatalf("refused relation was written anyway: %+v", rel)
				}
			}
		})
	}
}

// TestCreateRelation_ZeroTailStillWorksForFacelessCallers is the regression
// guard for RR-RELREG: an earlier draft of requireRelationFaceFor REQUIRED a
// face on a content-scoped edge from a faced source, which silently broke
// every caller that has no way to supply one — `rela link`
// (internal/cli/link.go), the MCP create_relation tool (no face in its
// schema), CalDAV membership writes, and the data-entry incoming-edge path,
// which passes a zero tail deliberately because the peer's face is not the
// request's to choose.
//
// Each subtest reproduces a real caller's exact option struct. The whole
// suite was green while this was broken, because nothing else exercises a
// content-scoped relation from a faced source.
func TestCreateRelation_ZeroTailStillWorksForFacelessCallers(t *testing.T) {
	body := "why"
	tests := []struct {
		name string
		opts entity.RelationOptions
	}{
		{
			// internal/cli/link.go:18 — `rela link POL-1 citeert SRC-1`
			name: "rela link passes a bare options struct",
			opts: entity.RelationOptions{},
		},
		{
			// internal/mcp/tools_relation.go:81 — the tool schema has no
			// face parameter at all, so this is the only shape it can send.
			name: "MCP create_relation passes properties and content only",
			opts: entity.RelationOptions{Content: &body},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr, _ := relationFaceManager(t)
			seedRelationFaceGraph(t, mgr)
			rel, err := mgr.CreateRelation(context.Background(),
				"POL-1", "citeert", "SRC-1", tc.opts)
			if err != nil {
				t.Fatalf("a caller that cannot name a face must keep working, got: %v", err)
			}
			if !rel.FromFace.IsDefault() {
				t.Errorf("FromFace = %q, want the zero face", rel.FromFace)
			}
		})
	}
}

// TestCreateRelation_FaceCheckPrecedesACL pins the ordering RR-3NNWNK turned
// on: the check must sit BELOW the ACL, because authorizeAndAudit returns
// early under bypassACL and would otherwise leave the elevated path with no
// validation between a Lua string and the store.
func TestCreateRelation_FaceCheckPrecedesACL(t *testing.T) {
	meta, err := metamodel.Parse([]byte(relationFaceYAML))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	st := memstore.New()
	gate := &recordingACL{}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store: st, Meta: meta, Templater: nopTemplater{}, Audit: audit.Nop{},
		ACL: gate, Transitions: statemachine.EmptySet(),
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	seedRelationFaceGraph(t, mgr)
	gate.relationCalls = 0

	_, err = mgr.CreateRelation(context.Background(), "POL-1", "geschreven-door", "SRC-1",
		entity.RelationOptions{FromFace: "concept"})
	if !errors.Is(err, entitymanager.ErrFaceNotDeclared) {
		t.Fatalf("CreateRelation error = %v, want ErrFaceNotDeclared", err)
	}
	if gate.relationCalls != 0 {
		t.Errorf("ACL was consulted %d times for a face the metamodel rejects; "+
			"the check must run BEFORE the subject is built, so the elevated "+
			"path (which skips the ACL entirely) is still covered",
			gate.relationCalls)
	}
}

// recordingACL counts relation authorizations so a test can assert the face
// check ran first.
type recordingACL struct {
	relationCalls int
}

func (a *recordingACL) AuthorizeWrite(_ context.Context, req acl.WriteRequest) acl.Decision {
	if _, ok := req.Subject.(acl.RelationSubject); ok {
		a.relationCalls++
	}
	return acl.Decision{Allow: true}
}
