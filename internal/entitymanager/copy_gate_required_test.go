package entitymanager_test

import (
	"context"
	"strings"
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

// allowAllCopyVisibility is the opt-out every non-copy test wants: ungated
// reads, built through the real constructor so the tests exercise the same
// validation production would. Failing the construction is a test bug, not an
// assertion, hence t.Fatalf rather than a returned error.
func allowAllCopyVisibility(t *testing.T, st store.Store) entitymanager.AllowAllCopyVisibility {
	t.Helper()
	v, err := entitymanager.NewAllowAllCopyVisibility(st)
	if err != nil {
		t.Fatalf("NewAllowAllCopyVisibility: %v", err)
	}
	return v
}

// declarativeForGateTest builds the production-shaped ACL: a compiled policy,
// which is the state that makes the copy read gates mandatory.
func declarativeForGateTest(t *testing.T) *acl.Declarative {
	t.Helper()
	policy := &acl.Policy{
		Roles:       map[string]acl.RoleDef{"author": {Read: []string{"page"}, Update: []string{"page@draft"}}},
		Assignments: map[string]string{"alice": "author"},
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("policy must load: %v", err)
	}
	d, err := acl.NewDeclarative(policy, acl.NullGraph{}, acl.NullGraphQueryer{})
	if err != nil {
		t.Fatalf("NewDeclarative: %v", err)
	}
	return d
}

// depsForGateTest is the minimal valid Deps, with the copy read gates left to
// the caller.
func depsForGateTest(t *testing.T, st store.Store, aclImpl acl.ACL) entitymanager.Deps {
	t.Helper()
	meta, err := metamodel.Parse([]byte(copyMetaYAML))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	return entitymanager.Deps{
		Store: st, Meta: meta, Templater: nopTemplater{},
		Audit: audit.Nop{}, ACL: aclImpl,
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	}
}

// TestNew_CopyGatesRequiredUnderPolicy is the regression test for
// sourcehaven-bv/rela#1437.
//
// # What was wrong
//
// Deps.CopyReadGate and Deps.CopyVisibility were silently nil-tolerant, while
// every other authorization dep in the same struct — Store, Meta, Templater,
// Audit, ACL, Transitions, Computed, FieldGate — hard-failed in New. The
// tolerance is correct for a deployment with no acl.yaml, where every read is
// raw anyway. It is NOT correct once a policy is present: authorizeCopy then
// skips its read check entirely and readCopySource takes the raw-store branch
// for cross-entity copies, so a principal reads a source entity, and the
// `visible:`-hidden fields on it, that every other read path would refuse.
//
// There was no error and no log line. The only marker was a code comment
// asserting the assumption ("nil gate means no ACL is wired"), with nothing
// enforcing it — and the sibling CopyGuard on the same struct already failed
// CLOSED, which is what made these two the odd ones out.
//
// # Why the subtest matrix is shaped this way
//
// The distinction the guard has to draw is between the legitimate state (no
// policy, gates absent) and the risky one (policy present, gates absent), so
// both directions are pinned. Nop and ReadOnly ACLs must still build without
// the gates — a check that just demanded them everywhere would be a different,
// noisier change, and would break the CLI's no-policy posture.
func TestNew_CopyGatesRequiredUnderPolicy(t *testing.T) {
	t.Parallel()

	st := memstore.New()

	t.Run("policy present and both gates nil is refused", func(t *testing.T) {
		t.Parallel()
		_, err := entitymanager.New(depsForGateTest(t, st, declarativeForGateTest(t)))
		if err == nil {
			t.Fatal("a compiled policy with no copy read gates must be REFUSED at " +
				"construction: the copy path would read its source ungated and " +
				"unredacted, silently. This is the forgotten-wiring ACL bypass " +
				"(RR-X9NVHI) that FieldGate next door already fails fast on")
		}
		// The message must name both, so an operator fixes the wiring in one
		// pass rather than rebuilding to discover the second.
		for _, want := range []string{"CopyReadGate", "CopyVisibility"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error must name the missing dep %q; got %v", want, err)
			}
		}
	})

	t.Run("policy present and only CopyVisibility nil is refused", func(t *testing.T) {
		t.Parallel()
		d := depsForGateTest(t, st, declarativeForGateTest(t))
		d.CopyReadGate = entitymanager.AllowAllCopyReadGate{}
		_, err := entitymanager.New(d)
		if err == nil {
			t.Fatal("a half-wired copy path must be refused too — the row gate and " +
				"the field redactor answer different questions, and a cross-entity " +
				"copy with no CopyVisibility launders `visible:`-hidden fields")
		}
		if strings.Contains(err.Error(), "CopyReadGate") {
			t.Errorf("the error must name only what is MISSING, or it sends the "+
				"operator to re-wire something already correct; got %v", err)
		}
	})

	t.Run("policy present and both gates supplied builds", func(t *testing.T) {
		t.Parallel()
		d := depsForGateTest(t, st, declarativeForGateTest(t))
		d.CopyReadGate = entitymanager.AllowAllCopyReadGate{}
		d.CopyVisibility = allowAllCopyVisibility(t, st)
		if _, err := entitymanager.New(d); err != nil {
			t.Fatalf("explicitly opting out must be ACCEPTED — the guard is about "+
				"forgotten wiring, not about forbidding allow-all; got %v", err)
		}
	})

	// The no-policy tier. Leaving the gates nil here is the documented CLI
	// posture, so the guard must not disturb it.
	for _, tc := range []struct {
		name    string
		aclImpl acl.ACL
	}{
		{"NopACL", acl.NopACL{}},
		{"ReadOnlyACL", acl.ReadOnlyACL{}},
	} {
		t.Run("no policy with "+tc.name+" still builds without the gates", func(t *testing.T) {
			t.Parallel()
			if _, err := entitymanager.New(depsForGateTest(t, st, tc.aclImpl)); err != nil {
				t.Fatalf("%s is the NO-POLICY case: every other read on such a "+
					"deployment is raw too, so requiring the copy gates here would "+
					"be theater that breaks the CLI; got %v", tc.name, err)
			}
		})
	}
}

// TestNewAllowAllCopyVisibility_RejectsNilStore keeps the opt-out from
// reintroducing the very failure the guard exists to prevent.
//
// A bare AllowAllCopyVisibility{} satisfies CopyReader, so it passes
// requireCopyGates and then nil-panics on the first cross-entity copy — a code
// path that may not run until long after deploy. That is the
// deferred-downstream-symptom shape CLAUDE.md's "constructors reject nil
// required fields" rule is about, which would be an awkward thing for THIS
// change to ship.
//
// The store field is unexported, so the constructor is the only way in and
// this is the only place the check can be bypassed. Note it is validated here
// rather than in requireCopyGates because a nil store is broken under ANY
// ACL, not only a policy-backed one.
func TestNewAllowAllCopyVisibility_RejectsNilStore(t *testing.T) {
	t.Parallel()

	if _, err := entitymanager.NewAllowAllCopyVisibility(nil); err == nil {
		t.Fatal("a nil store must be rejected at construction: the zero value " +
			"would satisfy CopyReader, pass New, and nil-panic on the first " +
			"cross-entity copy")
	}
}

// TestAllowAllCopyVisibility_ReportsAbsentAsMiss pins the opt-out's failure
// shape rather than only its happy path: a read gate must report a missing
// entity as a MISS, never as an error, because absent and denied are
// deliberately indistinguishable everywhere else on the read path. Getting
// this wrong would turn the opt-out into an existence oracle.
func TestAllowAllCopyVisibility_ReportsAbsentAsMiss(t *testing.T) {
	t.Parallel()

	st := memstore.New()
	v := allowAllCopyVisibility(t, st)
	ctx := context.Background()

	if err := st.CreateEntity(ctx, &entity.Entity{
		ID: "PAGE-1", Type: "page", Properties: map[string]any{"title": "Draft"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, ok, err := v.Get(ctx, "page", "PAGE-1", "")
	if err != nil || !ok {
		t.Fatalf("an existing entity must be a hit: ok=%v err=%v", ok, err)
	}
	if got.ID != "PAGE-1" {
		t.Errorf("Get returned %q, want PAGE-1", got.ID)
	}

	if _, ok, err := v.Get(ctx, "page", "PAGE-404", ""); err != nil || ok {
		t.Errorf("an absent entity must be a MISS with a nil error, matching every "+
			"other read gate; got ok=%v err=%v", ok, err)
	}
}
