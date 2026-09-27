package entitymanager_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// TestCreateRelation_EmptyFromType_AgainstTypeScopedPolicy pins TKT-ZJXO19
// (gh#1129): a relation write whose SOURCE ENTITY DOES NOT EXIST must never be
// authorized more permissively than the same write with the real source type.
//
// # Why this needs its own test
//
// CreateRelation computes a best-effort — possibly empty — FromType before the
// ACL check, so authorization does not depend on peer existence (BUG-K6FEVB).
// The existing regression tests cover that only against ReadOnlyACL (denies
// everything) and NopACL (allows everything). Neither is type-scoped, so
// neither can show what an EMPTY FromType does against a realistic acl.yaml
// with specific per-type grants — which is the only configuration where the
// question has an answer.
//
// The reasoning that this is safe holds: grantsVerb matches `t == "*" ||
// t == target`, so an empty target can only hit a wildcard grant (already
// permissive without the change) or a literal empty-string grant (not a real
// configuration). But that was PROSE. Nothing failed if a future change to
// grantsVerb, or to how FromType is derived, made "" match something it should
// not — and the ACL has been in production since 2026-07-07.
//
// The table pairs each policy with both source states so the comparison is
// direct: for every policy, absent-source must be no more permissive than
// present-source.
//
// # What this does and does not catch
//
// Honest scope, established by mutation rather than assumed. Substituting "*"
// for an empty FromType inside authorizeRelationWrite — the concrete bypass
// shape the issue imagines — is caught only by the `create: [""]` row, because
// with a realistic `create: [decision]` grant an empty type fails to match
// either way. Catching the wildcard substitution on a realistic grant would
// need a client-baseline ceiling (the one mechanism that denies a type a role
// holds `*` on), which is a different subsystem and belongs with the ceiling's
// own tests.
//
// What this test DOES pin is the property the issue asked for: across every
// realistic grant shape, an unresolvable source is never authorized where the
// resolved one is refused — and the one configuration where that is untrue is
// named, not silently absent.
func TestCreateRelation_EmptyFromType_AgainstTypeScopedPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// create is the role's `create:` grant list. The relation gate keys on
		// the SOURCE ENTITY's type (see acl.authorizeRelationWrite), not the
		// relation type — so these are entity types, which is exactly why an
		// unresolvable source is the interesting case.
		create []string
		// wantWithSource: allowed when the source entity EXISTS, so FromType
		// resolves to "decision".
		wantWithSource bool
		// wantWithoutSource: allowed when the source is ABSENT, so FromType is
		// "". Must never be true where wantWithSource is false — except in the
		// one case flagged below.
		wantWithoutSource bool
		// exemptFromInvariant marks the single configuration where an absent
		// source IS more permissive than a present one. It exists only for a
		// grant nobody can write by accident; see that case's comment.
		exemptFromInvariant bool
	}{
		{
			// The realistic grant. Present source matches "decision" and is
			// allowed; absent source yields "" which matches nothing, so it
			// fails CLOSED — more restrictive, never more permissive.
			name:              "grant on the source entity type",
			create:            []string{"decision"},
			wantWithSource:    true,
			wantWithoutSource: false,
		},
		{
			name:              "no grant",
			create:            []string{},
			wantWithSource:    false,
			wantWithoutSource: false,
		},
		{
			// A grant on a different entity type must not leak: "" must not
			// make an unrelated grant match.
			name:              "grant on a different entity type",
			create:            []string{"requirement"},
			wantWithSource:    false,
			wantWithoutSource: false,
		},

		{
			// The ONE case where an empty type matches, and the point of
			// including it: a wildcard grant was already permissive before
			// this code path existed, so allowing it here adds nothing. This
			// is the whole of grantsVerb's `t == "*"` branch.
			name:              "wildcard grant",
			create:            []string{"*"},
			wantWithSource:    true,
			wantWithoutSource: true,
		},
		{
			// The other theoretical match from the prose: a literal
			// empty-string grant. Not a realistic acl.yaml — you cannot write
			// `create: [""]` by accident — but it is the second half of the
			// claim, so it is pinned rather than assumed. If this ever became
			// reachable from a real config, THIS is the case that would need
			// re-examining.
			name:                "literal empty-string grant (not a real config)",
			create:              []string{""},
			wantWithSource:      false,
			wantWithoutSource:   true,
			exemptFromInvariant: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gotWith := createRelationAllowed(t, tc.create, true)
			gotWithout := createRelationAllowed(t, tc.create, false)

			if gotWith != tc.wantWithSource {
				t.Errorf("source present: allowed=%v, want %v", gotWith, tc.wantWithSource)
			}
			if gotWithout != tc.wantWithoutSource {
				t.Errorf("source absent (empty FromType): allowed=%v, want %v",
					gotWithout, tc.wantWithoutSource)
			}
			// The invariant that actually matters, asserted independently of
			// the expectations above: an absent source may never be allowed
			// where a present source is denied.
			//
			// The `create: [""]` case is the sole exemption, and finding it is
			// half the value of this test. The issue's prose named it as a
			// theoretical match ("a literal empty-string grant — no realistic
			// acl.yaml configuration"); running it confirms the behavior is
			// real rather than hypothetical. It stays exempt rather than
			// "fixed" because no operator can write that grant by accident:
			// `create: [""]` is a deliberate, visible act in the policy file.
			// If empty-string grants ever became reachable another way — a
			// generator, a migration, a template — this exemption is where to
			// start.
			if gotWithout && !gotWith && !tc.exemptFromInvariant {
				t.Errorf("BYPASS: an absent source entity was authorized where the real " +
					"source type was denied — an empty FromType must only ever be more restrictive")
			}
			if tc.exemptFromInvariant && (!gotWithout || gotWith) {
				t.Error("the empty-string-grant exemption no longer applies; if the ACL " +
					"stopped honoring `create: [\"\"]`, delete the exemption and this case")
			}
		})
	}
}

// createRelationAllowed reports whether CreateRelation passes the ACL gate for
// a role holding the given `create:` grants, with the source entity either
// seeded or absent.
//
// It distinguishes an ACL denial from the not-found error that follows it: when
// the source is absent the write cannot succeed regardless, so "allowed" means
// "got past authorization", which is the thing under test. ErrEntityNotFound
// is reached only after the gate.
func createRelationAllowed(t *testing.T, createGrants []string, seedSource bool) bool {
	t.Helper()

	st := memstore.New()
	bg := context.Background()
	// The target always exists, so a failure is never about the peer.
	if err := st.CreateEntity(bg, entity.New("REQ-001", "requirement")); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if seedSource {
		if err := st.CreateEntity(bg, entity.New("DEC-001", "decision")); err != nil {
			t.Fatalf("seed source: %v", err)
		}
	}

	d, err := acl.NewDeclarative(&acl.Policy{
		Roles:       map[string]acl.RoleDef{"writer": {Create: createGrants}},
		Assignments: map[string]string{"alice": "writer"},
	}, acl.NewStoreGraph(st), st)
	if err != nil {
		t.Fatalf("acl.NewDeclarative: %v", err)
	}

	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       st,
		Meta:        parseMeta(t),
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         d,
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
		// Not a copy test: opt out of the copy read gates explicitly, which is
		// what New requires of a policy-backed Deps (#1437).
		CopyReadGate:   entitymanager.AllowAllCopyReadGate{},
		CopyVisibility: allowAllCopyVisibility(t, st),
	})
	if err != nil {
		t.Fatalf("entitymanager.New: %v", err)
	}

	ctx := principal.With(bg, principal.Principal{User: "alice", Tool: principal.ToolDataEntry})
	_, wErr := mgr.CreateRelation(ctx, "DEC-001", "addresses", "REQ-001", entity.RelationOptions{})

	// A ForbiddenError means the gate refused. Anything else — including the
	// not-found error for an absent source — means it got through.
	var fe *acl.ForbiddenError
	return !errors.As(wErr, &fe)
}
