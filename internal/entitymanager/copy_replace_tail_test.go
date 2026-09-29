package entitymanager_test

import (
	"context"
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

// replaceTailMetaYAML stages a copy between two named faces. The bug is
// visible whenever the tail a delete addresses differs from the tail its
// listing filtered by — the DEFAULT tail is what a dropped tail falls back
// to, so a target face that is not it is what exposes the drop.
const replaceTailMetaYAML = `
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
relations:
  references:
    label: References
    scope: content
    from: [page]
    to: [page]
copies:
  stage-review:
    from: page@live
    to: page@review
    fields: all
    relations:
      references: replace
    guard:
      permission: stage
`

// TestCopyReplace_DeletesTheTargetTail is named for the corruption it
// prevents, and it reproduces a bug that was live in PR-C.
//
// THE BUG: applyCopyEdges listed the target face's edges filtered BY TAIL,
// then called DeleteRelation with the tail DROPPED. Every backend's
// DeleteRelation is default-tail-only (pgstore `AND from_face = ”`,
// memstore defaultTailKey, fsstore's bare key), so the delete did not fail —
// it removed the DEFAULT tail's edge on the same triple and returned nil,
// while the edge being replaced survived.
//
// Verified against memstore before the fix:
//
//	delete returned: nil
//	edges surviving the intended delete of the PUBLISHED-tail edge:
//	  [PAGE-1@published--references--SPEC-1]
//
// So `relations: replace` into a non-default face failed its own job — and,
// where a default-tail edge existed, destroyed a bystander too — silently, in
// all three backends. `promote-page` with `relations: replace` is the headline
// use case in design doc §9.1.
//
// Both assertions matter and neither is redundant: the target-side one
// catches "the edge I meant to replace survived", the source-side one catches
// "I destroyed a bystander". Note the bystander half is weaker than it was:
// since BUG-HC6I2T a faced type stores no zero-coordinate row, so the tail a
// dropped tail falls back to holds nothing here and only the target-side
// assertion fires on the original bug. Mutation-checked: swapping
// DeleteRelationState for DeleteRelation still fails the target-side
// assertion.
func TestCopyReplace_DeletesTheTargetTail(t *testing.T) {
	var st store.Store = memstore.New()
	meta, err := metamodel.Parse([]byte(replaceTailMetaYAML))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       st,
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

	ctx := context.Background()
	seed := func(id string, p entity.Face, title string) {
		t.Helper()
		e := &entity.Entity{
			ID: id, Type: "page", Face: p,
			Properties: map[string]any{"title": title},
		}
		if cerr := st.CreateEntity(ctx, e); cerr != nil {
			t.Fatalf("seed %s@%s: %v", id, p, cerr)
		}
	}
	// `page` declares faces, so every row names one (BUG-HC6I2T): there is
	// no zero-coordinate row to head the family.
	seed("PAGE-1", "live", "live")
	seed("PAGE-1", "review", "staged")
	seed("OLD-1", "live", "old target")
	seed("NEW-1", "live", "new target")

	liveTail := entity.Face("live")
	reviewTail := entity.Face("review")
	// Two edges differing ONLY by tail — these are two distinct relations.
	// The SOURCE (live) face points at NEW-1; this is what gets copied.
	if _, cerr := st.CreateRelation(ctx, "PAGE-1", "references", "NEW-1",
		&store.RelationData{FromFace: liveTail}); cerr != nil {
		t.Fatalf("seed live-tail edge: %v", cerr)
	}
	// The TARGET (review) face points at OLD-1; `replace` must remove this.
	if _, cerr := st.CreateRelation(ctx, "PAGE-1", "references", "OLD-1",
		&store.RelationData{FromFace: reviewTail}); cerr != nil {
		t.Fatalf("seed review-tail edge: %v", cerr)
	}

	if _, cerr := mgr.CopyState(ctx, entitymanager.CopyRequest{
		Definition: "stage-review", SourceID: "PAGE-1",
	}); cerr != nil {
		t.Fatalf("copy: %v", cerr)
	}

	targets := func(p entity.Face) []string {
		t.Helper()
		q := store.RelationQuery{From: "PAGE-1", Type: "references"}
		var out []string
		for r, lerr := range st.ListRelations(ctx, q) {
			if lerr != nil {
				t.Fatalf("list: %v", lerr)
			}
			if r.FromFace == p {
				out = append(out, r.To)
			}
		}
		return out
	}

	// The TARGET face must now hold NEW-1 only: its OLD-1 edge was replaced.
	// Under the bug OLD-1 survived here, because the delete went elsewhere.
	if got := targets(reviewTail); len(got) != 1 || got[0] != "NEW-1" {
		t.Errorf("replace must swap the TARGET face's edges; got %v, want [NEW-1] "+
			"— a surviving OLD-1 means the delete addressed the wrong tail", got)
	}

	// The SOURCE (live) face's edge must be untouched. Under the bug this is
	// the edge that got deleted — a face the copy never addressed.
	if got := targets(liveTail); len(got) != 1 || got[0] != "NEW-1" {
		t.Errorf("the SOURCE face's edge must survive a replace on another face; "+
			"got %v, want [NEW-1] — losing it means the copy corrupted a bystander",
			got)
	}
}
