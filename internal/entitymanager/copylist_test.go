package entitymanager_test

import (
	"context"
	"sync"
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

// copyListMeta declares definitions whose sources differ, so a list for one
// face must not offer another face's copies. The ticket type's only definition
// is CROSS-ENTITY, which the affordance surface filters out — so a ticket face
// lists nothing, for two independent reasons.
const copyListMeta = `
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
  ticket:
    label: Ticket
    id_prefix: TKT
    properties:
      title: {type: string}
copies:
  promote-page:
    from: page@draft
    to: page@published
    fields: all
    label: Publish
    guard:
      permission: promote-page
  unpublish-page:
    from: page@published
    to: page@draft
    fields: all
    guard:
      permission: promote-page
  spawn-followup:
    from: ticket
    to: new ticket
    fields:
      title: "Follow-up"
`

// copyListOption adjusts the Deps a copy-list manager is built from, after
// the defaults are applied. See newCopyListManager.
type copyListOption func(t *testing.T, d *entitymanager.Deps)

// withMeta swaps the metamodel for one parsed from yaml. Tests whose fixture
// would perturb the shared copyListMeta offer counts use their own.
func withMeta(yaml string) copyListOption {
	return func(t *testing.T, d *entitymanager.Deps) {
		t.Helper()
		meta, err := metamodel.Parse([]byte(yaml))
		if err != nil {
			t.Fatalf("metamodel.Parse: %v", err)
		}
		d.Meta = meta
	}
}

func withAudit(a audit.Audit) copyListOption {
	return func(_ *testing.T, d *entitymanager.Deps) { d.Audit = a }
}

func withACL(a acl.ACL) copyListOption {
	return func(_ *testing.T, d *entitymanager.Deps) { d.ACL = a }
}

// newCopyListManager builds a manager over a fresh memstore and copyListMeta,
// with the given copy guard and any option overrides applied on top.
func newCopyListManager(
	t *testing.T, g entitymanager.CopyGuard, opts ...copyListOption,
) (*entitymanager.Manager, store.Store) {
	t.Helper()
	st := memstore.New()
	deps := entitymanager.Deps{
		Store: st, Templater: nopTemplater{},
		Audit: audit.Nop{}, ACL: acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
		CopyGuard:   g,
	}
	withMeta(copyListMeta)(t, &deps)
	for _, opt := range opts {
		opt(t, &deps)
	}
	mgr, err := entitymanager.New(deps)
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}
	return mgr, st
}

// seedPage creates the draft face of PAGE-1 — the source every Allowed
// assertion reads. The MATCHING assertions need no source at all.
func seedPage(ctx context.Context, t *testing.T, st store.Store) {
	t.Helper()
	if err := st.CreateEntity(ctx, &entity.Entity{
		ID: "PAGE-1", Type: "page", Face: entity.Face("draft"),
		Properties: map[string]any{"title": "Draft"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func mustOffers(
	ctx context.Context, t *testing.T, mgr *entitymanager.Manager,
	entityType, face, sourceID string,
) []entitymanager.CopyOffer {
	t.Helper()
	got, err := entitymanager.CopiesForSource(ctx, mgr, entityType, face, sourceID)
	if err != nil {
		t.Fatalf("CopiesForSource: %v", err)
	}
	return got
}

func offerNames(offers []entitymanager.CopyOffer) []string {
	out := make([]string, 0, len(offers))
	for _, o := range offers {
		out = append(out, o.Name)
	}
	return out
}

// TestCopiesForSource_MatchesTheFaceNotJustTheType pins the face-level match.
//
// `promote-page` reads the DRAFT face and `unpublish-page` reads the
// PUBLISHED one. Both are `page`, so a match on entity type alone would offer
// both on either face — and a UI would render an "unpublish" button on a draft
// that has never been published.
//
// A face's declared name IS its stored coordinate (BUG-HC6I2T), so the match
// is a direct string comparison against the row's face — there is no
// declared-vs-stored translation left to get wrong.
func TestCopiesForSource_MatchesTheFaceNotJustTheType(t *testing.T) {
	mgr, _ := newCopyListManager(t, allowGuard{allow: true})
	ctx := context.Background()

	t.Run("draft face offers only its own copy", func(t *testing.T) {
		got := mustOffers(ctx, t, mgr, "page", "draft", "PAGE-1")
		if len(got) != 1 || got[0].Name != "promote-page" {
			t.Fatalf("the draft face must match `from: page@draft` and nothing "+
				"else; got %v", offerNames(got))
		}
		if got[0].TargetFace != "page@published" {
			t.Errorf("TargetFace = %q, want the declared target", got[0].TargetFace)
		}
	})

	t.Run("published face offers only its own copy", func(t *testing.T) {
		got := mustOffers(ctx, t, mgr, "page", "published", "PAGE-1")
		if len(got) != 1 || got[0].Name != "unpublish-page" {
			t.Errorf("a face must offer only the copies whose `from:` addresses "+
				"IT, or a UI renders unpublish on a never-published draft; got %v",
				offerNames(got))
		}
	})

	t.Run("a different type offers nothing of page's", func(t *testing.T) {
		// Empty for two independent reasons — page's copies must not leak
		// across types, and ticket's only definition is cross-entity. The
		// sibling subtests above are what prove the emptiness is not "the
		// fixture offers nothing at all".
		if got := mustOffers(ctx, t, mgr, "ticket", "", "TKT-1"); len(got) != 0 {
			t.Errorf("got %v, want none", offerNames(got))
		}
	})
}

// TestCopiesForSource_ListsDeniedDefinitionsWithAllowedFalse is the
// config-is-not-secret rule, applied.
//
// A definition the principal may not invoke is still LISTED, with
// Allowed=false. Copy names are operator-authored config in schema.yaml —
// routinely a public repo — so hiding them would conceal something already
// disclosed. What is per-principal is the verdict, and it rides on the entry.
//
// Contrast the entity read gate, which DOES hide existence: whether an entity
// exists is a genuine secret. A definition is config; a row is data.
func TestCopiesForSource_ListsDeniedDefinitionsWithAllowedFalse(t *testing.T) {
	ctx := context.Background()

	t.Run("guard grants", func(t *testing.T) {
		mgr, st := newCopyListManager(t, allowGuard{allow: true})
		seedPage(ctx, t, st)
		got := mustOffers(ctx, t, mgr, "page", "draft", "PAGE-1")
		if len(got) != 1 || !got[0].Allowed {
			t.Fatalf("a permitted copy must be Allowed; got %+v", got)
		}
		if got[0].Reason != "" {
			t.Errorf("Reason must be empty when Allowed; got %q", got[0].Reason)
		}
	})

	t.Run("guard denies: still listed, Allowed=false", func(t *testing.T) {
		mgr, st := newCopyListManager(t, allowGuard{allow: false})
		seedPage(ctx, t, st)
		got := mustOffers(ctx, t, mgr, "page", "draft", "PAGE-1")
		if len(got) != 1 {
			t.Fatalf("a DENIED definition must still be listed — its name is "+
				"config, not a secret; got %v", offerNames(got))
		}
		if got[0].Allowed {
			t.Error("a denied copy must report Allowed=false")
		}
		if got[0].Reason == "" {
			t.Error("a denial should carry a reason for a tooltip")
		}
	})

	t.Run("no guard wired: fails closed, still listed", func(t *testing.T) {
		mgr, st := newCopyListManager(t, nil)
		seedPage(ctx, t, st)
		got := mustOffers(ctx, t, mgr, "page", "draft", "PAGE-1")
		if len(got) != 1 || got[0].Allowed {
			t.Errorf("a guarded copy with no guard wired must report "+
				"Allowed=false, matching the kernel's fail-closed rule; got %+v", got)
		}
	})
}

// TestCopiesForSource_AllowedAgreesWithInvoke is the invariant that makes the
// hint worth having, and the RULING 11 failure it exists to avoid.
//
// # Mutation-checked, and the first attempt found something about the CODE
//
// Deleting copyInvocable's `authorizeCopy` call killed nothing — not because
// this test is weak, but because `planCopy` calls `authorizeCopy` itself, so
// the second call was redundant. The redundancy is now removed and the reason
// documented at the call site.
//
// The real mechanism is `planCopy`, and it IS pinned: making planCopy stop
// authorizing fails 8 tests across this package. The `guard denies` and
// `no guard wired` rows are what make this one of them — with a permissive
// guard alone, "the check ran" and "the check was skipped" both yield
// allowed, and the assertion could not tell them apart.
//
// `_actions` said a published face was writable while the write path refused.
// The fix there was to make the map honest. This asserts the same property
// directly: for each guard verdict, Allowed and the actual CopyState outcome
// agree.
//
// It is written as a LOOP over both verdicts rather than a single case,
// because a hint that is always true and a write that always succeeds would
// agree vacuously.
func TestCopiesForSource_AllowedAgreesWithInvoke(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		guard entitymanager.CopyGuard
	}{
		{"guard grants", allowGuard{allow: true}},
		{"guard denies", allowGuard{allow: false}},
		{"no guard wired", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, st := newCopyListManager(t, tc.guard)
			seedPage(ctx, t, st)

			offers := mustOffers(ctx, t, mgr, "page", "draft", "PAGE-1")
			if len(offers) != 1 {
				t.Fatalf("expected one offer, got %v", offerNames(offers))
			}
			hint := offers[0].Allowed

			_, err := mgr.CopyState(ctx, entitymanager.CopyRequest{
				Definition: "promote-page", SourceID: "PAGE-1",
			})
			actuallyAllowed := err == nil

			if hint != actuallyAllowed {
				t.Errorf("the hint and the write must agree: Allowed=%v but "+
					"CopyState err=%v. An affordance that lies is a trap for "+
					"every future consumer (RULING 11)", hint, err)
			}
		})
	}
}

// TestCopiesForSource_LabelFallsBackToName pins RULING 9's operator-
// configurable text and its fallback.
//
// The name is a legible fallback because copy names are operator-authored and
// already read as actions (`promote-page`), the same reasoning
// metamodel.Transition.Label uses.
//
// It uses its OWN metamodel rather than the shared fixture: the unlabelled
// definition has to be SAME-ENTITY to be offered at all, and adding one to the
// shared fixture would change the offer counts every other test asserts.
func TestCopiesForSource_LabelFallsBackToName(t *testing.T) {
	const labelMeta = `
version: "1"
entities:
  page:
    label: Page
    id_prefix: PAGE
    faces:
      draft: {}
      published: {}
      archived: {}
    properties:
      title: {type: string}
copies:
  promote-page:
    from: page@draft
    to: page@published
    fields: all
    label: Publish
    guard:
      permission: promote-page
  archive-page:
    from: page@draft
    to: page@archived
    fields: all
    guard:
      permission: archive-page
`
	mgr, _ := newCopyListManager(t, allowGuard{allow: true}, withMeta(labelMeta))

	got := mustOffers(context.Background(), t, mgr, "page", "draft", "PAGE-1")
	byName := map[string]string{}
	for _, o := range got {
		byName[o.Name] = o.Label
	}
	if byName["promote-page"] != "Publish" {
		t.Errorf("an operator `label:` must be used verbatim; got %q",
			byName["promote-page"])
	}
	if byName["archive-page"] != "archive-page" {
		t.Errorf("with no `label:`, the definition NAME is the fallback; got %q",
			byName["archive-page"])
	}
}

// TestCopiesForSource_StableOrder pins name ordering, so a UI renders a stable
// button order rather than one that reshuffles with map iteration.
func TestCopiesForSource_StableOrder(t *testing.T) {
	mgr, _ := newCopyListManager(t, allowGuard{allow: true})
	ctx := context.Background()
	first := offerNames(mustOffers(ctx, t, mgr, "page", "draft", "PAGE-1"))
	for range 8 {
		got := offerNames(mustOffers(ctx, t, mgr, "page", "draft", "PAGE-1"))
		if len(got) != len(first) {
			t.Fatalf("unstable length: %v vs %v", got, first)
		}
		for i := range got {
			if got[i] != first[i] {
				t.Fatalf("unstable order: %v vs %v", got, first)
			}
		}
	}
}

// TestCopiesForSource_AffordanceProbeEmitsNoAuditRecords is the regression
// test for a defect code review found: a READ-ONLY list emitted `denied-write`
// audit records.
//
// # Why it happened
//
// `Allowed` is computed by running the real authorization path — that is what
// stops the hint drifting from the write. But that path AUDITS its denials, so
// asking the question recorded an answer as though someone had attempted a
// write. A SPA rendering an entity view would append N rows per page load.
//
// # Why it matters more than volume
//
// `op=denied-write` would stop meaning "someone tried to write and was
// refused" and start meaning "someone looked at a page". Anyone alerting on
// denied-write volume gets paged by ordinary browsing, and the real signal
// drowns. The audit log's value is that it does not lie.
//
// The verdict is still computed identically — only the RECORD is suppressed —
// so the hint cannot drift as a result of this fix.
//
// # Mutation-checked, and the result is worth recording
//
// Removing the suppression in authorizeAndAudit does NOT fail this test, and
// that is not a flaw in the assertion — it is because the OTHER fix from the
// same review (filtering cross-entity definitions out before authorization)
// removed the path that audited. The records came from planCopyEdges on
// CROSS-ENTITY definitions, and those are no longer probed at all.
//
// So today the suppression is BELT-AND-BRACES, not the mechanism. It stays
// because the reachable-path set is a property of the current call graph
// rather than of the design: anything that probes a definition performing a
// per-edge authorization re-opens the path immediately, and the failure would
// be silent pollution of an append-only log.
//
// This test therefore asserts the PROPERTY (a read-only query writes nothing),
// not the mechanism, and it would catch a regression from either direction.
func TestCopiesForSource_AffordanceProbeEmitsNoAuditRecords(t *testing.T) {
	ctx := context.Background()
	rec := &recordingAudit{}
	mgr, st := newCopyListManager(t, allowGuard{allow: true},
		withAudit(rec), withACL(denyAllACL{}))
	seedPage(ctx, t, st)

	mustOffers(ctx, t, mgr, "page", "draft", "PAGE-1")

	if n := rec.count(); n != 0 {
		t.Errorf("a read-only affordance query wrote %d audit record(s). "+
			"`denied-write` must mean an attempted write was refused, not "+
			"that someone rendered a page; records: %v", n, rec.ops())
	}
}

// TestCopiesForSource_CrossEntityIsNotOffered pins the affordance surface's
// scope: SAME-ENTITY definitions only, filtered BEFORE authorization. See the
// "Same-entity definitions only" section on CopiesForSource for why a filter
// and not a verdict — probing a `new <type>` target would authorize the empty
// id, the list=allowed / invoke=forbidden defect code review caught.
//
// The kernel is NOT narrowed: Manager.CopyState still handles a cross-entity
// copy given an explicit target id, and the last assertion pins that half.
func TestCopiesForSource_CrossEntityIsNotOffered(t *testing.T) {
	mgr, st := newCopyListManager(t, allowGuard{allow: true})
	ctx := context.Background()

	// The ticket face declares exactly one copy, and it is cross-entity
	// (`to: new ticket`).
	if got := mustOffers(ctx, t, mgr, "ticket", "", "TKT-1"); len(got) != 0 {
		t.Errorf("a cross-entity definition must not be OFFERED as an "+
			"affordance — its target has no id yet, so no honest verdict "+
			"exists; got %v", offerNames(got))
	}

	// Same-entity offers are unaffected: the filter is by shape, not a blanket
	// narrowing. Without this the test above would pass against a build that
	// offered nothing at all.
	if got := mustOffers(ctx, t, mgr, "page", "draft", "PAGE-1"); len(got) != 1 {
		t.Fatalf("same-entity definitions must still be offered; got %v",
			offerNames(got))
	}

	// And the KERNEL still runs a cross-entity copy when given a target id —
	// the capability is not removed, only unoffered.
	if err := st.CreateEntity(ctx, &entity.Entity{
		ID: "TKT-1", Type: "ticket", Properties: map[string]any{"title": "Source"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := mgr.CopyState(ctx, entitymanager.CopyRequest{
		Definition: "spawn-followup", SourceID: "TKT-1", TargetID: "TKT-2",
	}); err != nil {
		t.Errorf("the kernel must still perform a cross-entity copy given an "+
			"explicit target — the affordance is scoped, the capability is "+
			"not; got %v", err)
	}
}

// recordingAudit counts audit records so a test can assert that a read-only
// query wrote none.
type recordingAudit struct {
	mu      sync.Mutex
	records []string
}

func (r *recordingAudit) Record(rec audit.Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec.Op)
}

func (r *recordingAudit) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.records)
}

func (r *recordingAudit) ops() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.records...)
}

// denyAllACL refuses every write, so any authorization the affordance query
// performs produces a denial worth auditing — which is what makes the
// zero-records assertion meaningful rather than vacuous.
type denyAllACL struct{ acl.NopACL }

func (denyAllACL) AuthorizeWrite(_ context.Context, _ acl.WriteRequest) acl.Decision {
	return acl.Decision{Allow: false, RuleKind: "test", Reason: "denied for test"}
}

// TestCopiesForSource_GuardedCopyIntoTheBareFaceIsOffered is the affordance
// half of the guard-is-the-authorization rule.
//
// `unpublish-page` is guarded and targets the BARE face. The write check used
// to key its same-entity exemption on the target face rather than on the
// guard, so this copy demanded `update` on the type — and under a read-only
// ACL the button was both refused and, because the hint runs the real
// authorization path, shown as disabled. A deployment whose bare face is the
// ADOPTED text has no other route into it, so that was the whole control
// becoming unusable rather than one button greying out.
func TestCopiesForSource_GuardedCopyIntoTheBareFaceIsOffered(t *testing.T) {
	ctx := context.Background()
	mgr, st := newCopyListManager(t, allowGuard{allow: true}, withACL(acl.ReadOnlyACL{}))
	seedPage(ctx, t, st)
	if err := st.CreateEntity(ctx, &entity.Entity{
		ID: "PAGE-1", Type: "page", Face: "published",
		Properties: map[string]any{"title": "Published"},
	}); err != nil {
		t.Fatalf("seed published face: %v", err)
	}

	offers := mustOffers(ctx, t, mgr, "page", "published", "PAGE-1")
	if len(offers) != 1 || offers[0].Name != "unpublish-page" {
		t.Fatalf("expected the unpublish offer, got %v", offerNames(offers))
	}
	if !offers[0].Allowed {
		t.Errorf("a guarded copy into the bare face is authorized by its guard; "+
			"got Allowed=false reason=%q", offers[0].Reason)
	}

	// The hint must agree with the write, as RULING 11 requires.
	if _, err := mgr.CopyState(ctx, entitymanager.CopyRequest{
		Definition: "unpublish-page", SourceID: "PAGE-1",
	}); err != nil {
		t.Errorf("the write must agree with the hint; got %v", err)
	}
}
