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

// Client attenuation at the WRITE gate (TKT-IAC8TX).
//
// manager.go:334 is the single chokepoint every write passes through, so this
// is where a ceiling either stops a write or does not. The dataentry-level
// equivalent cannot be written today: newTestAppV1 does not wire its
// entitymanager with the same ACL as the router (see the note in
// internal/dataentry/acl_write_test.go), so a dataentry test would exercise a
// NopACL manager and pass regardless.

const ceilingPolicy = `
roles:
  editor:
    read: [requirement]
    create: [requirement]
    update: [requirement]
    delete: [requirement]
assignments:
  alice: editor
client_baselines:
  apps:
    applies_to: [app]
    deny_write: ["*"]
scope_grants:
  rela.req.write:
    update: [requirement]
`

// ceilingManager builds a Manager gated by the attenuation policy above, plus a
// seeded requirement to act on.
func ceilingManager(t *testing.T) (mgr *entitymanager.Manager, seededID string) {
	t.Helper()
	policy, err := acl.LoadPolicyBytes([]byte(ceilingPolicy))
	if err != nil {
		t.Fatalf("LoadPolicyBytes: %v", err)
	}
	st := memstore.New()
	decl, err := acl.NewDeclarative(policy, acl.NewStoreGraph(st), st)
	if err != nil {
		t.Fatalf("NewDeclarative: %v", err)
	}

	seeded := entity.New("REQ-001", "requirement")
	seeded.SetString("title", "seeded")
	if cErr := st.CreateEntity(context.Background(), seeded); cErr != nil {
		t.Fatalf("seed: %v", cErr)
	}

	mgr, err = entitymanager.New(entitymanager.Deps{
		Store:       st,
		Meta:        parseMeta(t),
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         decl,
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
	return mgr, "REQ-001"
}

// ctxAsClient stamps a principal the way the verified-JWT gate does.
func ctxAsClient(principalType string, scopes []string) context.Context {
	return principal.With(context.Background(), principal.VerifiedFrom(
		"alice", principal.ToolDataEntry, principal.Claims{
			PrincipalType: principalType,
			Scopes:        scopes,
		}))
}

func updateTitle(
	ctx context.Context, t *testing.T, mgr *entitymanager.Manager, id, title string,
) error {
	t.Helper()
	_, err := mgr.PatchEntity(ctx, id, entity.Patch{
		Properties: map[string]any{"title": title},
	})
	return err
}

// TestCeiling_DenyWriteBlocksTheWrite: Alice is a full editor, but a client
// under a deny_write baseline cannot write as her.
func TestCeiling_DenyWriteBlocksTheWrite(t *testing.T) {
	t.Parallel()
	mgr, id := ceilingManager(t)

	err := updateTitle(ctxAsClient("app", nil), t, mgr, id, "edited by the app")
	if err == nil {
		t.Fatal("update succeeded under deny_write: [\"*\"]")
	}
	if !errors.Is(err, acl.ErrForbidden) {
		t.Fatalf("error = %v, want acl.ErrForbidden", err)
	}

	// The denial must name the CEILING, not a missing role. An operator told
	// "no role grants update" would go add a grant that is already there and
	// watch it keep failing.
	var fe *acl.ForbiddenError
	if !errors.As(err, &fe) {
		t.Fatalf("error is not a *acl.ForbiddenError: %v", err)
	}
	if fe.Decision.RuleKind != "client-ceiling" {
		t.Errorf("RuleKind = %q, want client-ceiling (got reason %q)", fe.Decision.RuleKind, fe.Decision.Reason)
	}
	if fe.Decision.RuleID != "apps" {
		t.Errorf("RuleID = %q, want the baseline name apps", fe.Decision.RuleID)
	}
}

// TestCeiling_InteractiveUserWritesFine is the control: the same user, the same
// policy, arriving as an interactive principal type, is unaffected. A naive
// "restrict everything" implementation breaks exactly here.
func TestCeiling_InteractiveUserWritesFine(t *testing.T) {
	t.Parallel()
	mgr, id := ceilingManager(t)

	if err := updateTitle(ctxAsClient("user", nil), t, mgr, id, "edited by alice"); err != nil {
		t.Fatalf("interactive user was attenuated: %v", err)
	}
}

// TestCeiling_ScopeReopensTheWrite: the scope restores exactly what it names.
func TestCeiling_ScopeReopensTheWrite(t *testing.T) {
	t.Parallel()
	mgr, id := ceilingManager(t)

	ctx := ctxAsClient("app", []string{"rela.req.write"})
	if err := updateTitle(ctx, t, mgr, id, "edited via scope"); err != nil {
		t.Fatalf("rela.req.write did not re-open update: %v", err)
	}

	// The scope named update only — delete stays denied.
	if _, err := mgr.DeleteEntity(ctx, id, false); !errors.Is(err, acl.ErrForbidden) {
		t.Errorf("delete error = %v, want ErrForbidden (the scope re-opened only update)", err)
	}
}

// TestCeiling_NeverGrantsPastTheUser is the safety property that makes a scoped
// token safe to hand out: the SAME token used by a user without the underlying
// grant still cannot write. The ceiling is an upper bound, never a source.
func TestCeiling_NeverGrantsPastTheUser(t *testing.T) {
	t.Parallel()
	mgr, id := ceilingManager(t)

	// bob holds no assignment at all, but presents the write scope.
	ctx := principal.With(context.Background(), principal.VerifiedFrom(
		"bob", principal.ToolDataEntry, principal.Claims{
			PrincipalType: "app",
			Scopes:        []string{"rela.req.write"},
		}))

	if err := updateTitle(ctx, t, mgr, id, "edited by bob"); !errors.Is(err, acl.ErrForbidden) {
		t.Errorf("error = %v, want ErrForbidden — a scope must not grant past the acting user", err)
	}
}
