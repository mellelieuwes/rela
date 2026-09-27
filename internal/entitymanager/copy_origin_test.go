package entitymanager_test

import (
	"context"
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

// originMetaYAML declares one same-entity copy (page@live -> page@review) and
// one CROSS-entity copy, so the boundary can be checked on both shapes: the
// cross-entity form is the one whose source id is a different row, which is
// what the read-out gate exists for.
const originMetaYAML = `
version: "1"
entities:
  page:
    label: Page
    id_prefix: PAGE
    faces:
      live: {}
      review: {}
    properties:
      title: {type: string}
  note:
    label: Note
    id_prefix: NOTE
    properties:
      title: {type: string}
  doc:
    label: Doc
    id_prefix: DOC
    faces:
      en: {}
      nl: {}
    properties:
      title: {type: string}
copies:
  stage-review:
    from: page@live
    to: page@review
    fields: all
    guard:
      permission: stage
  note-from-page:
    from: page@live
    to: note
    fields:
      title: "{{new.title}}"
  translate:
    from: doc@en
    to: doc@nl
    fields:
      title: "{{new.title}}"
    guard:
      permission: stage
`

// originRecordingStore wraps a store and records the store.Origin carried on
// the ctx of every entity write, so a test can assert what the WRITE BOUNDARY
// handed the store — which is the only sanctioned route provenance travels.
type originRecordingStore struct {
	store.Store
	created []store.Origin
	updated []store.Origin
}

func (s *originRecordingStore) CreateEntity(ctx context.Context, e *entity.Entity) error {
	s.created = append(s.created, store.OriginFrom(ctx))
	return s.Store.CreateEntity(ctx, e)
}

func (s *originRecordingStore) UpdateEntity(ctx context.Context, e *entity.Entity) error {
	s.updated = append(s.updated, store.OriginFrom(ctx))
	return s.Store.UpdateEntity(ctx, e)
}

// UpdateEntityIf must be recorded too: PatchEntity routes through the
// conditional write (TKT-34XS2R), so a recorder that only overrode
// UpdateEntity would see a patch as NO write at all and silently stop
// asserting anything about the patch path.
func (s *originRecordingStore) UpdateEntityIf(
	ctx context.Context, e *entity.Entity, cond store.UpdateCondition,
) (store.EntityVersion, error) {
	s.updated = append(s.updated, store.OriginFrom(ctx))
	return s.Store.UpdateEntityIf(ctx, e, cond)
}

// Tx must be re-implemented, not inherited, and the view it hands the callback
// must record into THIS recorder.
//
// A copy does every one of its writes through the transaction VIEW, never
// through the outer store (applyCopy's whole contract). Inheriting memstore's
// Tx would hand the callback memstore's own view, the wrapper would see no
// writes at all, and every assertion below would pass vacuously on an empty
// slice — the test would be measuring nothing.
func (s *originRecordingStore) Tx(ctx context.Context, fn func(store.Store) error) error {
	tr, ok := s.Store.(store.Transactor)
	if !ok {
		return fn(s)
	}
	return tr.Tx(ctx, func(view store.Store) error {
		return fn(&txRecordingStore{Store: view, rec: s})
	})
}

// txRecordingStore records a transaction view's writes back into the outer
// recorder, so assertions can read them after the transaction has committed.
type txRecordingStore struct {
	store.Store
	rec *originRecordingStore
}

func (s *txRecordingStore) CreateEntity(ctx context.Context, e *entity.Entity) error {
	s.rec.created = append(s.rec.created, store.OriginFrom(ctx))
	return s.Store.CreateEntity(ctx, e)
}

func (s *txRecordingStore) UpdateEntity(ctx context.Context, e *entity.Entity) error {
	s.rec.updated = append(s.rec.updated, store.OriginFrom(ctx))
	return s.Store.UpdateEntity(ctx, e)
}

func (s *txRecordingStore) UpdateEntityIf(
	ctx context.Context, e *entity.Entity, cond store.UpdateCondition,
) (store.EntityVersion, error) {
	s.rec.updated = append(s.rec.updated, store.OriginFrom(ctx))
	return s.Store.UpdateEntityIf(ctx, e, cond)
}

func newOriginRecorder(inner store.Store) *originRecordingStore {
	return &originRecordingStore{Store: inner}
}

// TestCopyStampsOriginOnItsWrites pins the boundary contract: a copy's writes
// carry a store.Origin naming the mechanism, the definition and the SOURCE
// face, while an ordinary write through the same manager carries none.
//
// This is the load-bearing half of "history can tell a copy from a hand edit".
// The store column and the sweep are downstream of it; if the boundary does
// not stamp, everything downstream records a hand edit.
func TestCopyStampsOriginOnItsWrites(t *testing.T) {
	tests := []struct {
		name       string
		definition string
		sourceID   string
		targetID   string
		want       store.Origin
		wantLabel  string
	}{
		{
			name:       "same-entity copy records the SOURCE face",
			definition: "stage-review",
			sourceID:   "PAGE-1",
			want: store.Origin{
				Kind: store.OriginCopy,
				// Same id as the target — the FACE is what differs, and the
				// pair is the source. Recording the id anyway keeps the
				// reader from having to know that.
				Source: "PAGE-1",
				// A face's name IS its stored coordinate (BUG-HC6I2T), so
				// the boundary records the coordinate and the label reads
				// back the way the operator spelled it in `copies:`.
				SourceFace: "live",
				SourceType: "page",
				Definition: "stage-review",
			},
			wantLabel: "PAGE-1@live",
		},
		{
			name: "a second faced type records its own face",
			// The control against a hardcoded spelling: a different type,
			// a different face name, same mechanism.
			definition: "translate",
			sourceID:   "DOC-1",
			want: store.Origin{
				Kind:       store.OriginCopy,
				Source:     "DOC-1",
				SourceFace: "en",
				SourceType: "doc",
				Definition: "translate",
			},
			wantLabel: "DOC-1@en",
		},
		{
			name:       "cross-entity copy names the source ENTITY",
			definition: "note-from-page",
			sourceID:   "PAGE-1",
			targetID:   "NOTE-1",
			want: store.Origin{
				Kind:       store.OriginCopy,
				Source:     "PAGE-1",
				SourceFace: "live",
				SourceType: "page",
				Definition: "note-from-page",
			},
			wantLabel: "PAGE-1@live",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, mgr := newOriginManager(t)
			ctx := principal.With(context.Background(),
				principal.Principal{User: "edith@example.com", Tool: "data-entry"})

			if _, err := mgr.CopyState(ctx, entitymanager.CopyRequest{
				Definition: tc.definition, SourceID: tc.sourceID, TargetID: tc.targetID,
			}); err != nil {
				t.Fatalf("copy: %v", err)
			}

			got := append(append([]store.Origin{}, rec.created...), rec.updated...)
			if len(got) == 0 {
				t.Fatal("copy performed no recorded entity write")
			}
			for i, o := range got {
				if o != tc.want {
					t.Errorf("write %d origin = %+v, want %+v", i, o, tc.want)
				}
				// The label is what a reader actually sees, and it is the
				// single spelling the CLI and the SPA share, so assert it
				// here rather than trusting the field alone.
				if got := o.SourceLabel(); got != tc.wantLabel {
					t.Errorf("write %d SourceLabel() = %q, want %q", i, got, tc.wantLabel)
				}
			}
		})
	}
}

// TestOrdinaryWriteCarriesNoOrigin is the other half of the user's ask:
// "manual edits should be marked similarly". The marking is the ABSENCE of an
// origin — there is deliberately no OriginKind("manual") — so this pins that a
// plain manager write leaves the ctx unmarked and the row records NULL.
//
// Without this test the copy assertion above would still pass if the boundary
// stamped every write, which would make the marker meaningless.
func TestOrdinaryWriteCarriesNoOrigin(t *testing.T) {
	rec, mgr := newOriginManager(t)
	ctx := principal.With(context.Background(),
		principal.Principal{User: "edith@example.com", Tool: "data-entry"})

	// The FACELESS `note` type, not PAGE-1: Manager.PatchEntity takes no face
	// and reads through store.GetEntity, which resolves the zero coordinate —
	// and since BUG-HC6I2T a faced type stores no row there, so a patch of
	// PAGE-1 fails not-found before it can stamp anything. The origin marker
	// under test is face-independent, so a faceless subject pins it honestly.
	if _, err := mgr.PatchEntity(ctx, "NOTE-9", entity.Patch{
		Properties: map[string]any{"title": "typed by hand"},
	}); err != nil {
		t.Fatalf("patch: %v", err)
	}

	all := append(append([]store.Origin{}, rec.created...), rec.updated...)
	if len(all) == 0 {
		t.Fatal("patch performed no recorded entity write")
	}
	for i, o := range all {
		if !o.IsZero() {
			t.Errorf("write %d carried origin %+v; a hand edit must carry NONE "+
				"(absence is the signal, and a marked hand edit would make "+
				"the copy marker meaningless)", i, o)
		}
	}
}

// newOriginManager builds a manager over an origin-recording memstore seeded
// with PAGE-1's default face.
func newOriginManager(t *testing.T) (*originRecordingStore, *entitymanager.Manager) {
	t.Helper()
	rec := newOriginRecorder(memstore.New())
	meta, err := metamodel.Parse([]byte(originMetaYAML))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       rec,
		Meta:        meta,
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
		CopyGuard:   allowGuard{allow: true},
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	// Both types declare faces, so every seeded row names one: since
	// BUG-HC6I2T there is no zero-coordinate row to head a family.
	for _, e := range []*entity.Entity{
		{ID: "PAGE-1", Type: "page", Face: "live", Properties: map[string]any{"title": "live"}},
		{ID: "DOC-1", Type: "doc", Face: "en", Properties: map[string]any{"title": "english"}},
		// A faceless subject for the hand-edit half — see
		// TestOrdinaryWriteCarriesNoOrigin.
		{ID: "NOTE-9", Type: "note", Properties: map[string]any{"title": "note"}},
	} {
		if cerr := rec.Store.CreateEntity(context.Background(), e); cerr != nil {
			t.Fatalf("seed %s@%s: %v", e.ID, e.Face, cerr)
		}
	}
	return rec, mgr
}
