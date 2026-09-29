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
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// copyMetaYAML declares two faced types and the copy definitions the
// security tests exercise.
const copyMetaYAML = `
version: "1"
entities:
  page:
    label: Page
    id_prefix: PAGE
    faces:
      draft: {}
      published: {}
    properties:
      title: {type: string}
      secret: {type: string}
  ticket:
    label: Ticket
    id_prefix: TKT
    properties:
      title: {type: string}
      secret: {type: string}
copies:
  promote-page:
    from: page@draft
    to: page@published
    fields: all
    guard:
      permission: promote-page
  spawn-followup:
    from: ticket
    to: new ticket
    fields:
      title: "Follow-up: {{new.title}}"
      # Deliberately maps the REDACTED field. A definition may name any
      # declared property — the metamodel cannot know which are visible to
      # a given principal — so the gate is what must stop the value
      # traveling, not the mapping. A definition that simply omitted the
      # field would make this test pass for the wrong reason.
      secret: "{{new.secret}}"
`

// redactingReader stands in for the caller's visibility gate: it drops the
// `secret` property, exactly as field-level `visible:` redaction would.
type redactingReader struct{ st *store.Store }

func (r redactingReader) Get(
	ctx context.Context, _, id string, face entity.Face,
) (*entity.Entity, bool, error) {
	e, gerr := (*r.st).GetEntityState(ctx, id, face)
	if gerr != nil {
		// Missing and denied are indistinguishable here, matching the real
		// gate's contract — whether an entity exists is a genuine secret.
		return nil, false, nil //nolint:nilerr // a miss reads as denied, by design
	}
	clone := *e
	clone.Properties = map[string]any{}
	for k, v := range e.Properties {
		if k == "secret" {
			continue // redacted: the principal may not read this field
		}
		clone.Properties[k] = v
	}
	return &clone, true, nil
}

type allowGuard struct{ allow bool }

func (g allowGuard) HoldsPermission(context.Context, string, string) bool { return g.allow }

// newCopyManager builds a Manager over one memstore, and hands the caller a
// face to it so a redacting gate can wrap the SAME store the manager
// writes through.
func newCopyManager(
	t *testing.T, vis func(*store.Store) entitymanager.CopyReader,
	guard entitymanager.CopyGuard,
) (*entitymanager.Manager, store.Store) {
	t.Helper()
	var st store.Store = memstore.New()
	var reader entitymanager.CopyReader
	if vis != nil {
		reader = vis(&st)
	}
	meta, err := metamodel.Parse([]byte(copyMetaYAML))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:          st,
		Meta:           meta,
		Templater:      nopTemplater{},
		Audit:          audit.Nop{},
		ACL:            acl.NopACL{},
		Transitions:    statemachine.EmptySet(),
		FieldGate:      entitymanager.AllowAllFieldGate{},
		Twins:          entitymanager.NoTwinOwnership{},
		CopyVisibility: reader,
		CopyGuard:      guard,
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	return mgr, st
}

// TestCopy_SameEntityIsElevated_HiddenFieldsSurvive pins the FIRST half of
// the §9.2 elevation split, and it is named for the failure it prevents.
//
// THE FAILURE: a promote that read the draft through the caller's
// visibility gate would copy only the fields that principal may see, and
// write them as the published face. Every hidden property would be silently
// destroyed on the way — the redacted read-modify-write this codebase
// forbids everywhere else, arriving through the copy door.
//
// WHY ELEVATION IS SAFE HERE: identity is preserved. The hidden fields
// travel with the SAME entity, the principal never sees them, and the same
// policy governs them on the target face. Identity preserved → policy
// follows. This is the positive form of the never-redact-a-write-prep rule.
func TestCopy_SameEntityIsElevated_HiddenFieldsSurvive(t *testing.T) {
	// A redacting gate IS wired, and must NOT be consulted for this copy.
	mgr, st := newCopyManager(t,
		func(s *store.Store) entitymanager.CopyReader { return redactingReader{st: s} },
		allowGuard{allow: true})
	ctx := context.Background()

	require := func(cond bool, format string, args ...any) {
		t.Helper()
		if !cond {
			t.Fatalf(format, args...)
		}
	}

	// A draft holding a field the caller cannot read.
	err := st.CreateEntity(ctx, &entity.Entity{
		ID: "PAGE-1", Type: "page", Face: entity.Face("draft"),
		Properties: map[string]any{"title": "Draft", "secret": "classified"},
	})
	require(err == nil, "seed draft: %v", err)

	res, err := mgr.CopyState(ctx, entitymanager.CopyRequest{
		Definition: "promote-page", SourceID: "PAGE-1",
	})
	require(err == nil, "promote: %v", err)
	require(res.Created, "the published face did not exist and must be created")

	published, err := st.GetEntityState(ctx, "PAGE-1", entity.Face("published"))
	require(err == nil, "read published face: %v", err)

	if got := published.Properties["secret"]; got != "classified" {
		t.Errorf("the hidden field did not survive the promote (got %v) — a "+
			"same-entity copy MUST read raw. Reading through the caller's gate "+
			"would silently destroy every property they cannot see, which is the "+
			"redacted read-modify-write failure the codebase forbids everywhere.",
			got)
	}
	if got := published.Properties["title"]; got != "Draft" {
		t.Errorf("visible field lost: %v", got)
	}
}

// TestCopy_CrossEntityReadsThroughCallersGate_NoLaundering pins the SECOND
// half, and is likewise named for its failure.
//
// THE FAILURE: an ELEVATED cross-entity copy would read fields the principal
// cannot see and write them into a DIFFERENT entity — one with a different
// audience, possibly one they can read freely. That launders hidden data
// past redaction, and it is the reason the two halves differ at all.
//
// WHY THE GATE IS RIGHT HERE: new identity → new audience → visible-only.
// The target is governed by its own policy, which has no relationship to the
// source's, so nothing justifies carrying what the caller could not read.
func TestCopy_CrossEntityReadsThroughCallersGate_NoLaundering(t *testing.T) {
	mgr, st := newCopyManager(t,
		func(s *store.Store) entitymanager.CopyReader { return redactingReader{st: s} },
		allowGuard{allow: true})
	ctx := context.Background()

	if err := st.CreateEntity(ctx, &entity.Entity{
		ID: "TKT-1", Type: "ticket",
		Properties: map[string]any{"title": "Source", "secret": "classified"},
	}); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	if _, err := mgr.CopyState(ctx, entitymanager.CopyRequest{
		Definition: "spawn-followup", SourceID: "TKT-1", TargetID: "TKT-2",
	}); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	target, err := st.GetEntity(ctx, "TKT-2")
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	// The definition MAPS `secret`, so the only thing preventing it from
	// traveling is that the source was read through the caller's gate and
	// arrived without it. An interpolation of a missing property renders
	// empty, which is the correct fail-closed outcome.
	if got, _ := target.Properties["secret"].(string); got != "" {
		t.Errorf("a field the caller cannot read was laundered into a DIFFERENT "+
			"entity (%v) — a cross-entity copy must read through the caller's "+
			"visibility gate, because the target has its own audience and nothing "+
			"justifies carrying what the principal could not see",
			target.Properties["secret"])
	}
	// The mapped field still carries, through the existing interpolation
	// grammar rather than a second one.
	if got, _ := target.Properties["title"].(string); !strings.Contains(got, "Source") {
		t.Errorf("mapped field did not interpolate: %q", got)
	}
}

// TestCopy_GuardDeniesWithoutPermission pins that the definition's guard is
// a real gate, and that a guarded copy with NO guard wired fails CLOSED —
// matching the statemachine's nil-guard rule, where a guarded edge with no
// guard is denied rather than waved through.
func TestCopy_GuardDeniesWithoutPermission(t *testing.T) {
	const guardedMeta = `
version: "1"
entities:
  page:
    label: Page
    id_prefix: PAGE
    faces:
      draft: {}
      published: {}
    properties:
      title: {type: string}
copies:
  promote-page:
    from: page@draft
    to: page@published
    fields: all
    guard:
      permission: promote-page
`
	newGuarded := func(t *testing.T, g entitymanager.CopyGuard) (*entitymanager.Manager, store.Store) {
		t.Helper()
		st := memstore.New()
		meta, err := metamodel.Parse([]byte(guardedMeta))
		if err != nil {
			t.Fatalf("metamodel.Parse: %v", err)
		}
		mgr, err := entitymanager.New(entitymanager.Deps{
			Store: st, Meta: meta, Templater: nopTemplater{},
			Audit: audit.Nop{}, ACL: acl.NopACL{},
			Transitions: statemachine.EmptySet(),
			FieldGate:   entitymanager.AllowAllFieldGate{},
			Twins:       entitymanager.NoTwinOwnership{},
			CopyGuard:   g,
		})
		if err != nil {
			t.Fatalf("entitymanager.New: %v", err)
		}
		return mgr, st
	}

	for _, tc := range []struct {
		name  string
		guard entitymanager.CopyGuard
		deny  bool
	}{
		{"guard grants", allowGuard{allow: true}, false},
		{"guard denies", allowGuard{allow: false}, true},
		{"NO guard wired: fails closed", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, st := newGuarded(t, tc.guard)
			ctx := context.Background()
			if err := st.CreateEntity(ctx, &entity.Entity{
				ID: "PAGE-1", Type: "page", Face: entity.Face("draft"),
				Properties: map[string]any{"title": "Draft"},
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			_, err := mgr.CopyState(ctx, entitymanager.CopyRequest{
				Definition: "promote-page", SourceID: "PAGE-1",
			})
			denied := errors.Is(err, acl.ErrForbidden)
			if denied != tc.deny {
				t.Errorf("denied = %v, want %v (err: %v)", denied, tc.deny, err)
			}
		})
	}
}

// TestCopy_UnknownDefinitionRefused pins the by-name-only contract: a
// request names a DEFINITION, never a mapping. If a caller could describe a
// copy, they could describe one whose guard is convenient, and the whole
// guard system would be decorative.
func TestCopy_UnknownDefinitionRefused(t *testing.T) {
	mgr, _ := newCopyManager(t, nil, allowGuard{allow: true})
	_, err := mgr.CopyState(context.Background(), entitymanager.CopyRequest{
		Definition: "not-declared", SourceID: "PAGE-1",
	})
	if !errors.Is(err, entitymanager.ErrUnknownCopy) {
		t.Errorf("an undeclared definition must be refused as caller input; got %v", err)
	}
}

// TestCopy_GuardedFaceIsWritableOnlyViaDefinition is the end-to-end property
// this whole arc exists to deliver, and the one Jeroen's demo turns on.
//
// A DIRECT write to the published face is refused, because no role holds
// update on it — Step 3 made `update: ["page@draft"]` name a face, and Step 4
// PR-A made the write path pass the face to the ACL. The copy definition is
// then the ONLY path in, and it carries its own guard.
//
// THREE halves are asserted, and the third is the one an earlier version of
// this test lacked. "The copy works" proves nothing if a direct write also
// works; "the direct write is refused" proves nothing if the copy is refused
// too; and BOTH prove nothing if the copy would succeed for EVERYONE. The
// first version wired an allow-everything guard against a definition that
// had no guard block at all, so part (b) passed because authorization was
// missing — the same passed-for-the-wrong-reason failure caught one test
// earlier on the laundering case.
func TestCopy_GuardedFaceIsWritableOnlyViaDefinition(t *testing.T) {
	st := memstore.New()
	meta, err := metamodel.Parse([]byte(copyMetaYAML))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	// A policy granting update on the DRAFT face only. Nobody holds update
	// on published — the §9.2 invariant.
	policy := &acl.Policy{
		Roles: map[string]acl.RoleDef{
			"author": {Read: []string{"page"}, Update: []string{"page@draft"}},
		},
		Assignments: map[string]string{"alice": "author"},
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("policy must load: %v", err)
	}
	d, derr := acl.NewDeclarative(policy, acl.NullGraph{}, acl.NullGraphQueryer{})
	if derr != nil {
		t.Fatalf("NewDeclarative: %v", derr)
	}
	mgr, merr := entitymanager.New(entitymanager.Deps{
		Store: st, Meta: meta, Templater: nopTemplater{},
		Audit: audit.Nop{}, ACL: d,
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
		CopyGuard:   allowGuard{allow: true},
		// The subject here is the GUARD and the write verdict, not the read
		// gates, so both are opted out explicitly — permissive, so a refusal
		// below can only come from the mechanism under test (#1437).
		CopyReadGate:   entitymanager.AllowAllCopyReadGate{},
		CopyVisibility: allowAllCopyVisibility(t, st),
	})
	if merr != nil {
		t.Fatalf("entitymanager.New: %v", merr)
	}

	ctx := principal.With(context.Background(),
		principal.Principal{User: "alice", Tool: principal.ToolCLI})
	if err := st.CreateEntity(ctx, &entity.Entity{
		ID: "PAGE-1", Type: "page", Face: entity.Face("draft"),
		Properties: map[string]any{"title": "Draft"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// (a) A DIRECT write to the guarded face is refused.
	_, directErr := mgr.UpdateEntity(ctx, &entity.Entity{
		ID: "PAGE-1", Type: "page", Face: entity.Face("published"),
		Properties: map[string]any{"title": "Snuck in"},
	})
	if !errors.Is(directErr, acl.ErrForbidden) {
		t.Errorf("a direct write to the published face must be REFUSED — no role "+
			"holds update on it, and that is what makes the copy definition the "+
			"only way in; got %v", directErr)
	}

	// (b) The copy definition succeeds, for the same principal.
	if _, err := mgr.CopyState(ctx, entitymanager.CopyRequest{
		Definition: "promote-page", SourceID: "PAGE-1",
	}); err != nil {
		t.Fatalf("the copy definition must be the way in; got %v", err)
	}
	published, perr := st.GetEntityState(ctx, "PAGE-1", entity.Face("published"))
	if perr != nil {
		t.Fatalf("the promote did not produce a published face: %v", perr)
	}
	if got := published.Properties["title"]; got != "Draft" {
		t.Errorf("published face holds %v, want the promoted draft content", got)
	}

	// (c) The guard is LOAD-BEARING: the same definition, same principal,
	// with a guard that denies, must refuse. Without this the test would pass
	// against a kernel that authorizes nothing at all.
	denyMgr, derr2 := entitymanager.New(entitymanager.Deps{
		Store: st, Meta: meta, Templater: nopTemplater{},
		Audit: audit.Nop{}, ACL: d,
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
		CopyGuard:   allowGuard{allow: false},
		// Permissive read gates, so the refusal below can only be the
		// denying guard — the point of case (c) (#1437).
		CopyReadGate:   entitymanager.AllowAllCopyReadGate{},
		CopyVisibility: allowAllCopyVisibility(t, st),
	})
	if derr2 != nil {
		t.Fatalf("entitymanager.New: %v", derr2)
	}
	if _, err := denyMgr.CopyState(ctx, entitymanager.CopyRequest{
		Definition: "promote-page", SourceID: "PAGE-1",
	}); !errors.Is(err, acl.ErrForbidden) {
		t.Errorf("a denying guard must refuse the copy — otherwise the definition "+
			"is a way in for anyone who can name it; got %v", err)
	}
}

// TestCopy_StrangerCannotPromote pins the hole an earlier cut of
// authorizeCopy shipped: a principal holding NOTHING could promote another
// user's draft, publishing fields they could not read.
//
// The reasoning that produced it was that §9.2 makes the definition the
// authority for a guarded face. It does — but "each carrying its own guard"
// is a CONDITION, not a license to skip authorization when a definition
// omits one. Elevation decides which FIELDS travel; it never decides whether
// the principal may touch the entity.
func TestCopy_StrangerCannotPromote(t *testing.T) {
	st := memstore.New()
	meta, err := metamodel.Parse([]byte(copyMetaYAML))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	policy := &acl.Policy{
		Roles: map[string]acl.RoleDef{
			"author": {Read: []string{"page"}, Update: []string{"page@draft"}},
		},
		Assignments: map[string]string{"alice": "author"},
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("policy must load: %v", err)
	}
	d, derr := acl.NewDeclarative(policy, acl.NullGraph{}, acl.NullGraphQueryer{})
	if derr != nil {
		t.Fatalf("NewDeclarative: %v", derr)
	}
	req, rerr := d.ForPrincipal(principal.Principal{User: "mallory", Tool: principal.ToolCLI})
	if rerr != nil {
		t.Fatalf("ForPrincipal: %v", rerr)
	}
	mgr, merr := entitymanager.New(entitymanager.Deps{
		Store: st, Meta: meta, Templater: nopTemplater{},
		Audit: audit.Nop{}, ACL: d,
		Transitions:  statemachine.EmptySet(),
		FieldGate:    entitymanager.AllowAllFieldGate{},
		Twins:        entitymanager.NoTwinOwnership{},
		CopyGuard:    allowGuard{allow: true}, // even a PERMISSIVE guard
		CopyReadGate: req,
		// This is a SAME-ENTITY copy, which never consults CopyVisibility
		// (it reads raw and elevated by design). Opted out explicitly rather
		// than left nil so the refusal under test can only come from
		// CopyReadGate above (#1437).
		CopyVisibility: allowAllCopyVisibility(t, st),
	})
	if merr != nil {
		t.Fatalf("entitymanager.New: %v", merr)
	}

	seed := principal.With(context.Background(),
		principal.Principal{User: "alice", Tool: principal.ToolCLI})
	if err := st.CreateEntity(seed, &entity.Entity{
		ID: "PAGE-1", Type: "page", Face: entity.Face("draft"),
		Properties: map[string]any{"title": "Draft", "secret": "classified"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mctx := principal.With(context.Background(),
		principal.Principal{User: "mallory", Tool: principal.ToolCLI})
	if _, err := mgr.CopyState(mctx, entitymanager.CopyRequest{
		Definition: "promote-page", SourceID: "PAGE-1",
	}); err == nil {
		t.Error("a principal holding NOTHING promoted another user's draft, " +
			"publishing a field they cannot read — a copy must check READ on the " +
			"source before either half of the elevation split runs")
	}
}

// TestCopy_EdgesLandOnTheTargetFace pins where a copy's edges land.
//
// A copy writes the target FACE, and its edges belong to that face: they must
// carry the target's tail, and a `replace` must clear the stale ones already
// there. Getting the tail wrong produces edges orphaned from the face they
// belong to AND invisible to the `replace` that should have removed the old
// ones — silent in both directions.
//
// This is also the only coverage of relation copying at all, alongside
// TestCopyReplace_DeletesTheTargetTailNotTheDefaultFacesEdge, which pins the
// delete side.
//
// Historical note: this test used to assert that edges land at the ZERO tail,
// because `bare_face: draft` made `draft` the zero coordinate. BUG-HC6I2T
// removed that mapping — a face's declared name IS its stored coordinate — so
// the assertion is now simply that they land at "draft".
func TestCopy_EdgesLandOnTheTargetFace(t *testing.T) {
	const meta = `
version: "1"
entities:
  page:
    label: Page
    id_prefix: PAGE
    faces:
      draft: {}
      published: {}
    properties:
      title: {type: string}
  spec:
    label: Spec
    id_prefix: SPEC
    properties:
      title: {type: string}
relations:
  cites:
    from: [page]
    to: [spec]
    scope: content
copies:
  revise-page:
    from: page@published
    to: page@draft
    fields: all
    relations:
      cites: replace
    guard:
      permission: revise-page
`
	st := memstore.New()
	m, err := metamodel.Parse([]byte(meta))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store: st, Meta: m, Templater: nopTemplater{},
		Audit: audit.Nop{}, ACL: acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
		CopyGuard:   allowGuard{allow: true},
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	ctx := context.Background()

	for _, id := range []string{"SPEC-9", "SPEC-12"} {
		if err := st.CreateEntity(ctx, &entity.Entity{ID: id, Type: "spec"}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	draft := entity.Face("draft")
	published := entity.Face("published")
	// The draft face cites SPEC-12; the published face cites SPEC-9.
	if err := st.CreateEntity(ctx, &entity.Entity{
		ID: "PAGE-1", Type: "page", Face: draft,
		Properties: map[string]any{"title": "Draft"},
	}); err != nil {
		t.Fatalf("seed draft: %v", err)
	}
	if err := st.CreateEntity(ctx, &entity.Entity{
		ID: "PAGE-1", Type: "page", Face: published,
		Properties: map[string]any{"title": "Published"},
	}); err != nil {
		t.Fatalf("seed published: %v", err)
	}
	if _, err := st.CreateRelation(ctx, "PAGE-1", "cites", "SPEC-12",
		&store.RelationData{FromFace: draft}); err != nil {
		t.Fatalf("seed draft edge: %v", err)
	}
	if _, err := st.CreateRelation(ctx, "PAGE-1", "cites", "SPEC-9",
		&store.RelationData{FromFace: published}); err != nil {
		t.Fatalf("seed published edge: %v", err)
	}

	// Revise: published -> draft.
	if _, err := mgr.CopyState(ctx, entitymanager.CopyRequest{
		Definition: "revise-page", SourceID: "PAGE-1",
	}); err != nil {
		t.Fatalf("revise: %v", err)
	}

	var atTarget, atZero, stale int
	for rel, err := range st.ListRelations(ctx, store.RelationQuery{From: "PAGE-1"}) {
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		switch {
		case rel.FromFace == draft && rel.To == "SPEC-9":
			atTarget++
		case rel.FromFace == draft && rel.To == "SPEC-12":
			stale++
		case rel.FromFace.IsDefault():
			atZero++
		}
	}
	if atZero != 0 {
		t.Errorf("%d edge(s) landed at the ZERO tail — `page` declares faces, so "+
			"no face lives there and those edges are orphaned from the face they "+
			"belong to", atZero)
	}
	if stale != 0 {
		t.Errorf("`replace` left %d stale edge(s) at the target tail — it queried "+
			"the wrong tail, found nothing, and deleted nothing", stale)
	}
	if atTarget != 1 {
		t.Errorf("want the copied edge at the target face's tail, got %d", atTarget)
	}
}
