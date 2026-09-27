package entitymanager_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// facedWriteYAML is the shape BUG-HC6I2T was reported against: the adopted
// text and the draft are two faces of one entity, and NEITHER is addressed by
// a bare id. `code` is the natural key an ISMS policy register would use.
const facedWriteYAML = `
entities:
  beleid:
    label: Beleid
    id_prefix: "POL-"
    faces:
      concept: {label: Concept}
      vastgesteld: {label: Vastgesteld}
    properties:
      title: {type: string}
      code: {type: string, unique: true}
`

func facedWriteManager(t *testing.T, gate acl.ACL) (*entitymanager.Manager, *memstore.MemStore) {
	t.Helper()
	meta, err := metamodel.Parse([]byte(facedWriteYAML))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	st := memstore.New()
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store: st, Meta: meta, Templater: nopTemplater{}, Audit: audit.Nop{},
		ACL: gate, Transitions: statemachine.EmptySet(),
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	return mgr, st
}

// conceptOnlyACL permits writes to `concept` and denies every other face —
// the atlas grant shape, `update: [beleid@concept]`. It RECORDS the faces it
// was asked about, which is the whole point: a test that only checks the
// verdict cannot tell an authorization of the right row from an
// authorization of the wrong one that happened to agree.
type conceptOnlyACL struct {
	asked    []entity.Face
	askedIDs []string
}

func (a *conceptOnlyACL) AuthorizeWrite(_ context.Context, req acl.WriteRequest) acl.Decision {
	es, ok := req.Subject.(acl.EntitySubject)
	if !ok {
		return acl.Decision{Allow: true}
	}
	a.asked = append(a.asked, es.Face())
	a.askedIDs = append(a.askedIDs, es.ID())
	if es.Face() == entity.Face("concept") {
		return acl.Decision{Allow: true, RuleKind: "test", RuleID: "concept-grant"}
	}
	return acl.Decision{
		Allow: false, RuleKind: "test", RuleID: "no-other-face",
		Reason: "this principal holds update on beleid@concept only",
	}
}

// The defect, stated as the property that failed: the face a create
// AUTHORIZES against and the face it WRITES must be the same row.
//
// Before BUG-HC6I2T they were read from different places — the ACL subject
// from the caller's carrier entity, the write from a createCoreOpts literal
// that had no face field at all — so the check answered about a row the write
// never touched. Under `bare_face` that row was a real face, which made this a
// confused deputy rather than a mere misfiling: the ACL said yes to
// `beleid@concept` and the content landed on the adopted face the principal
// was explicitly denied.
func TestCreate_AuthorizesTheFaceItWrites(t *testing.T) {
	gate := &conceptOnlyACL{}
	mgr, st := facedWriteManager(t, gate)
	ctx := context.Background()

	res, err := mgr.CreateEntity(ctx, &entity.Entity{
		Type: "beleid", Properties: map[string]any{"title": "Toegangsbeleid"},
	}, entity.CreateOptions{Face: entity.Face("concept")})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	stored, err := st.GetEntityState(ctx, res.Entity.ID, entity.Face("concept"))
	if err != nil {
		t.Fatalf("the row is not at the face that was authorized: %v", err)
	}
	if len(gate.asked) == 0 {
		t.Fatal("no authorization was performed")
	}
	if gate.asked[0] != stored.Face {
		t.Errorf("authorized face %q but wrote face %q", gate.asked[0], stored.Face)
	}
	if _, bareErr := st.GetEntity(ctx, res.Entity.ID); bareErr == nil {
		t.Error("a faced type must write no row at the zero coordinate")
	}
}

// The consequence the ticket was filed for: a principal denied the adopted
// face cannot reach it by creating.
func TestCreate_DeniedFaceIsRefused(t *testing.T) {
	gate := &conceptOnlyACL{}
	mgr, _ := facedWriteManager(t, gate)

	_, err := mgr.CreateEntity(context.Background(), &entity.Entity{
		Type: "beleid", Properties: map[string]any{"title": "Toegangsbeleid"},
	}, entity.CreateOptions{Face: entity.Face("vastgesteld")})

	var forbidden *acl.ForbiddenError
	if !errors.As(err, &forbidden) {
		t.Fatalf("want a ForbiddenError naming the denied face, got %v", err)
	}
}

// A faced type has no default to fall back to, so a create that names no face
// is refused rather than resolved. Choosing one silently is the defect.
func TestCreate_FacedTypeRequiresAFace(t *testing.T) {
	mgr, _ := facedWriteManager(t, acl.NopACL{})

	_, err := mgr.CreateEntity(context.Background(), &entity.Entity{
		Type: "beleid", Properties: map[string]any{"title": "x"},
	}, entity.CreateOptions{})

	if !errors.Is(err, entitymanager.ErrFaceRequired) {
		t.Fatalf("want ErrFaceRequired, got %v", err)
	}
}

func TestCreate_UndeclaredFaceIsRefused(t *testing.T) {
	mgr, _ := facedWriteManager(t, acl.NopACL{})

	_, err := mgr.CreateEntity(context.Background(), &entity.Entity{
		Type: "beleid", Properties: map[string]any{"title": "x"},
	}, entity.CreateOptions{Face: entity.Face("nonsuch")})

	if !errors.Is(err, entitymanager.ErrFaceNotDeclared) {
		t.Fatalf("want ErrFaceNotDeclared, got %v", err)
	}
}

// The mirror of TestCreate_FacedTypeRequiresAFace: a type declaring no faces
// has one state and no name for it, so naming a face is equally a mistake.
// Symmetric on purpose — in both directions the only safe answer is to refuse
// rather than pick.
func TestCreate_FacelessTypeRefusesAFace(t *testing.T) {
	meta, err := metamodel.Parse([]byte(`
entities:
  ticket:
    label: Ticket
    id_prefix: "TKT-"
    properties:
      title: {type: string}
`))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store: memstore.New(), Meta: meta, Templater: nopTemplater{}, Audit: audit.Nop{},
		ACL: acl.NopACL{}, Transitions: statemachine.EmptySet(),
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}

	_, cerr := mgr.CreateEntity(context.Background(), &entity.Entity{
		Type: "ticket", Properties: map[string]any{"title": "x"},
	}, entity.CreateOptions{Face: entity.Face("draft")})

	if !errors.Is(cerr, entitymanager.ErrFaceNotDeclared) {
		t.Fatalf("want ErrFaceNotDeclared, got %v", cerr)
	}
}

// UpdateEntity had the same authorize-here/read-there split as the create
// path: it authorized against e.Face and then read its pre-image with
// GetEntity, which is GetEntityState(id, ZERO). On a faced type that row does
// not exist, so every faced update returned not-found.
func TestUpdate_ReadsThePreImageAtTheAuthorizedFace(t *testing.T) {
	mgr, _ := facedWriteManager(t, acl.NopACL{})
	ctx := context.Background()

	res, err := mgr.CreateEntity(ctx, &entity.Entity{
		Type: "beleid", Properties: map[string]any{"title": "before"},
	}, entity.CreateOptions{Face: entity.Face("concept")})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	edited := res.Entity.Clone()
	edited.Properties["title"] = "after"
	updated, err := mgr.UpdateEntity(ctx, edited)
	if err != nil {
		t.Fatalf("UpdateEntity on a faced entity: %v", err)
	}
	if got := updated.Entity.GetString("title"); got != "after" {
		t.Errorf("title = %q, want after", got)
	}
	if updated.Entity.Face != entity.Face("concept") {
		t.Errorf("the update moved the row to face %q", updated.Entity.Face)
	}
}

// `unique:` scanned only zero-coordinate rows, which a faced type has none
// of, so a natural key was silently unenforced on exactly the types most
// likely to declare one.
//
// The rule is PER-FACE (TKT-HXT2P9 covers making it selectable): two
// ENTITIES may not share the value within one face.
func TestUnique_IsEnforcedWithinAFace(t *testing.T) {
	mgr, _ := facedWriteManager(t, acl.NopACL{})
	ctx := context.Background()
	create := func() error {
		_, err := mgr.CreateEntity(ctx, &entity.Entity{
			Type: "beleid", Properties: map[string]any{"title": "x", "code": "ISMS-1"},
		}, entity.CreateOptions{Face: entity.Face("concept")})
		return err
	}

	if err := create(); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := create(); err == nil {
		t.Error("a second entity took the same unique value in the same face")
	}
}

// The other half of per-face: two faces of ONE entity are not two entities,
// so an adopted version carrying the same code as its own draft is not a
// collision. Excluding by bare id rather than by row is what makes this true.
func TestUnique_TwoFacesOfOneEntityDoNotCollide(t *testing.T) {
	mgr, st := facedWriteManager(t, acl.NopACL{})
	ctx := context.Background()

	res, err := mgr.CreateEntity(ctx, &entity.Entity{
		Type: "beleid", Properties: map[string]any{"title": "x", "code": "ISMS-1"},
	}, entity.CreateOptions{Face: entity.Face("concept")})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if err := st.CreateEntity(ctx, &entity.Entity{
		ID: res.Entity.ID, Type: "beleid", Face: entity.Face("vastgesteld"),
		Properties: map[string]any{"title": "x", "code": "ISMS-1"},
	}); err != nil {
		t.Fatalf("seed the adopted face: %v", err)
	}

	edited := res.Entity.Clone()
	edited.Properties["title"] = "edited"
	if _, err := mgr.UpdateEntity(ctx, edited); err != nil {
		t.Errorf("an entity's own sibling face must not collide with it: %v", err)
	}
}

// ApplyEntity (the sync upsert) decided create-vs-update from a probe at the
// ZERO coordinate, so on a faced type it always resolved as CREATE. That made
// the update branch — and the ErrFaceImmutable guard it carries — unreachable,
// and left the body free to name the face it was authorized against.
//
// The probe now addresses the row the body names, so the op, the subject and
// the write all describe one row.
func TestApply_ProbesTheFaceTheBodyNames(t *testing.T) {
	gate := &conceptOnlyACL{}
	mgr, st := facedWriteManager(t, gate)
	ctx := context.Background()

	// An existing row at the face this principal may NOT write.
	if err := st.CreateEntity(ctx, &entity.Entity{
		ID: "POL-1", Type: "beleid", Face: entity.Face("vastgesteld"),
		Properties: map[string]any{"title": "adopted"},
	}); err != nil {
		t.Fatalf("seed the adopted face: %v", err)
	}

	_, err := mgr.ApplyEntity(ctx, &entity.Entity{
		ID: "POL-1", Type: "beleid", Face: entity.Face("vastgesteld"),
		Properties: map[string]any{"title": "hijacked"},
	})
	var forbidden *acl.ForbiddenError
	if !errors.As(err, &forbidden) {
		t.Fatalf("a denied face must be refused through the sync path too, got %v", err)
	}
	if len(gate.asked) == 0 || gate.asked[len(gate.asked)-1] != entity.Face("vastgesteld") {
		t.Errorf("authorized against %q, want the face the body named", gate.asked)
	}

	got, gerr := st.GetEntityState(ctx, "POL-1", entity.Face("vastgesteld"))
	if gerr != nil {
		t.Fatalf("the refusal removed the row: %v", gerr)
	}
	if title := got.GetString("title"); title != "adopted" {
		t.Errorf("the denied row's content changed to %q", title)
	}
}

// ID generation counted each family once by scanning the ZERO coordinate. A
// type declaring faces stores no row there (BUG-HC6I2T), so the generator saw
// an EMPTY id set and minted the same id for every entity of that type — two
// unrelated entities sharing the id the ACL row gate keys on, one entity's
// grant covering the other's content, and one delete destroying both.
func TestCreate_FacedEntitiesGetDistinctIDs(t *testing.T) {
	meta, err := metamodel.Parse([]byte(`
entities:
  beleid:
    label: Beleid
    id_prefix: "POL"
    id_type: sequential
    faces:
      concept: {label: Concept}
      vastgesteld: {label: Vastgesteld}
    properties:
      title: {type: string}
`))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store: memstore.New(), Meta: meta, Templater: nopTemplater{}, Audit: audit.Nop{},
		ACL: acl.NopACL{}, Transitions: statemachine.EmptySet(),
		FieldGate: entitymanager.AllowAllFieldGate{},
		Twins:     entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	ctx := context.Background()

	// Two DIFFERENT entities, created at different faces.
	first, err := mgr.CreateEntity(ctx, &entity.Entity{
		Type: "beleid", Properties: map[string]any{"title": "first"},
	}, entity.CreateOptions{Face: entity.Face("concept")})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := mgr.CreateEntity(ctx, &entity.Entity{
		Type: "beleid", Properties: map[string]any{"title": "second"},
	}, entity.CreateOptions{Face: entity.Face("vastgesteld")})
	if err != nil {
		t.Fatalf("second create: %v", err)
	}

	if first.Entity.ID == second.Entity.ID {
		t.Fatalf("two distinct entities were minted the same id %q — the id is the "+
			"ACL row-gate key, so one entity's grant would cover the other",
			first.Entity.ID)
	}
}

// TestPatch_AddressesTheFaceTheRefNames pins that PatchEntity resolves the
// fused boundary form rather than always asking the zero coordinate.
//
// Store.GetEntity is GetEntityState(id, zero) in every backend, so before this
// a patch of `POL-1@concept` reported "not found" for a row that plainly
// exists — and on a faceless type it worked, which is what kept the gap hidden.
//
// NOTE ON REACH. This test runs against memstore, which keys entities on
// FormatStateRef(id, face) — so GetEntity("POL-1@concept") builds the key
// "POL-1@concept" with a zero face and hits the stored row BY COINCIDENCE.
// fsstore keys the same way. The lookup fix is therefore NOT observable here:
// reverting getEntityByRef to a bare GetEntity leaves this test green
// (mutation-checked).
//
// pgstore is where it is real — it queries `id = $1 AND face = $2`, so the
// fused id matches nothing (verified directly against a live database). What
// this test genuinely pins is the ACL half: that the subject names the bare id
// with the face in its own field. The lookup half is covered by the worlds
// manual, which builds against postgres in CI and is what surfaced the bug.
//
// The ACL half matters as much as the lookup: the subject must name the BARE id
// with the face in its own field, exactly as UpdateEntity does. Passing the
// fused string as the subject ID would make the row-gate key disagree with
// every other write path.
func TestPatch_AddressesTheFaceTheRefNames(t *testing.T) {
	ctx := context.Background()
	gate := &conceptOnlyACL{}
	mgr, st := facedWriteManager(t, gate)

	seed := func(face, title string) {
		t.Helper()
		e := entity.New("POL-1", "beleid")
		e.Face = entity.Face(face)
		e.Properties = map[string]any{"title": title}
		if err := st.CreateEntity(ctx, e); err != nil {
			t.Fatalf("seed %s: %v", face, err)
		}
	}
	seed("draft", "draft text")
	seed("concept", "concept text")

	if _, err := mgr.PatchEntity(ctx, "POL-1@concept",
		entity.Patch{Properties: map[string]any{"title": "patched"}}); err != nil {
		t.Fatalf("patching a faced address must resolve that row; got %v", err)
	}

	got, err := st.GetEntityState(ctx, "POL-1", entity.Face("concept"))
	if err != nil || got.Properties["title"] != "patched" {
		t.Errorf("concept face = %v (err %v), want the patched title", got.Properties, err)
	}
	sib, err := st.GetEntityState(ctx, "POL-1", entity.Face("draft"))
	if err != nil || sib.Properties["title"] != "draft text" {
		t.Errorf("draft face = %v (err %v), want it untouched", sib.Properties, err)
	}
	if !slices.Contains(gate.asked, entity.Face("concept")) {
		t.Errorf("ACL was asked about %v, want the concept face", gate.asked)
	}
	if slices.Contains(gate.askedIDs, "POL-1@concept") {
		t.Errorf("the ACL subject must name the BARE id; got %v", gate.askedIDs)
	}
}
