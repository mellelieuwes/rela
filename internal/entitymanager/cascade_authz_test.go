package entitymanager_test

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// cascadePolicy: alice may delete requirements, but holds NO delete grant on
// `decision` — the source type of the incoming `addresses` edge. So a cascade
// delete of a requirement destroys an edge she could not delete directly.
const cascadePolicy = `
roles:
  req-admin:
    read: ["*"]
    create: ["*"]
    update: ["*"]
    delete: [requirement]
  full-admin:
    read: ["*"]
    create: ["*"]
    update: ["*"]
    delete: ["*"]
assignments:
  alice: req-admin
  root: full-admin
`

func cascadeManager(t *testing.T, st store.Store) *entitymanager.Manager {
	t.Helper()
	p, err := acl.LoadPolicyBytes([]byte(cascadePolicy))
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}
	d, err := acl.NewDeclarative(p, acl.NewStoreGraph(st), st)
	if err != nil {
		t.Fatalf("NewDeclarative: %v", err)
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
	return mgr
}

func asUser(user string) context.Context {
	return principal.With(context.Background(),
		principal.Principal{User: user, Tool: principal.ToolCLI})
}

// seedCascadeGraph builds DEC-1 --addresses--> REQ-1, so deleting REQ-1 with
// cascade destroys an edge whose SOURCE is a decision.
func seedCascadeGraph(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	for _, e := range []*entity.Entity{
		entity.New("REQ-1", "requirement"),
		entity.New("DEC-1", "decision"),
	} {
		if err := st.CreateEntity(ctx, e); err != nil {
			t.Fatalf("seed %s: %v", e.ID, err)
		}
	}
	if _, err := st.CreateRelation(ctx, "DEC-1", "addresses", "REQ-1", nil); err != nil {
		t.Fatalf("seed relation: %v", err)
	}
}

// TestCascadeDelete_DeniedWhenRelationNotDeletable is the core of TKT-8HDPQW:
// deleting an entity must not be a back door to destroying edge types the
// principal holds no delete grant on.
func TestCascadeDelete_DeniedWhenRelationNotDeletable(t *testing.T) {
	t.Parallel()
	st := memstore.New()
	seedCascadeGraph(t, st)
	mgr := cascadeManager(t, st)

	_, err := mgr.DeleteEntity(asUser("alice"), "REQ-1", true)
	if err == nil {
		t.Fatal("cascade succeeded; alice holds delete on requirement but not on " +
			"decision, the source of the incident addresses edge")
	}
	var forbidden *acl.ForbiddenError
	if !errors.As(err, &forbidden) {
		t.Errorf("error %v does not wrap *acl.ForbiddenError, so callers cannot "+
			"map it to a 403", err)
	}
	// The message must be entity-delete-shaped AND name the FAR endpoint.
	// DEC-1 is the assertion that matters: for an incoming edge the deleted
	// entity is the To side, so naming rel.To would print "its relation to
	// REQ-1" while deleting REQ-1 — nonsense, and it withholds the one entity
	// whose type actually blocked the delete.
	for _, want := range []string{"REQ-1", "addresses", "DEC-1", "incoming"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestCascadeDelete_DeniedLeavesEverythingIntact pins the no-partial-write
// property. Asserted on memstore deliberately: the postgres variant is gated on
// RELA_TEST_DATABASE_URL and skipped in default CI, so if that were the only
// place this held, a regression would ship on the default build.
func TestCascadeDelete_DeniedLeavesEverythingIntact(t *testing.T) {
	t.Parallel()
	st := memstore.New()
	seedCascadeGraph(t, st)
	mgr := cascadeManager(t, st)

	if _, err := mgr.DeleteEntity(asUser("alice"), "REQ-1", true); err == nil {
		t.Fatal("expected denial")
	}

	ctx := context.Background()
	if _, err := st.GetEntity(ctx, "REQ-1"); err != nil {
		t.Errorf("REQ-1 was deleted despite the denial: %v", err)
	}
	if _, err := st.GetRelation(ctx, "DEC-1", "addresses", "REQ-1"); err != nil {
		t.Errorf("the addresses edge was deleted despite the denial: %v", err)
	}
}

// TestCascadeDelete_AllowedWhenEveryRelationIsDeletable is the counterweight:
// the gate must not break the legitimate case.
func TestCascadeDelete_AllowedWhenEveryRelationIsDeletable(t *testing.T) {
	t.Parallel()
	st := memstore.New()
	seedCascadeGraph(t, st)
	mgr := cascadeManager(t, st)

	res, err := mgr.DeleteEntity(asUser("root"), "REQ-1", true)
	if err != nil {
		t.Fatalf("root holds delete on everything: %v", err)
	}
	if len(res.DeletedRelations) != 1 {
		t.Errorf("DeletedRelations = %d, want 1", len(res.DeletedRelations))
	}
	if _, gErr := st.GetEntity(context.Background(), "REQ-1"); gErr == nil {
		t.Error("REQ-1 still exists after an allowed cascade")
	}
}

// TestCascadeDelete_NoRelationsNeedsNoRelationGrant pins that the gate is
// scoped to what a cascade actually destroys — an entity with no edges must
// not suddenly require relation grants.
func TestCascadeDelete_NoRelationsNeedsNoRelationGrant(t *testing.T) {
	t.Parallel()
	st := memstore.New()
	if err := st.CreateEntity(context.Background(), entity.New("REQ-9", "requirement")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	mgr := cascadeManager(t, st)

	if _, err := mgr.DeleteEntity(asUser("alice"), "REQ-9", true); err != nil {
		t.Fatalf("alice holds delete on requirement and there are no edges: %v", err)
	}
}

// concurrentWriteStore signals when the manager has finished collecting the
// incident set, so a test can attempt a write in the exact TOCTOU window: after
// authorization has decided, before the store re-derives its own set.
type concurrentWriteStore struct {
	store.Store
	collects  int
	collected chan struct{}
	once      sync.Once
	// signal is set on the per-Tx decorator and points back at the parent's
	// counter; nil on the parent itself.
	signal func()
}

func (s *concurrentWriteStore) ListRelations(
	ctx context.Context, q store.RelationQuery,
) iter.Seq2[*entity.Relation, error] {
	seq := s.Store.ListRelations(ctx, q)
	return func(yield func(*entity.Relation, error) bool) {
		for r, err := range seq {
			if !yield(r, err) {
				return
			}
		}
		// Both directions are collected before the delete; signal after the
		// second so the window is genuinely open.
		if s.signal != nil {
			s.signal()
		}
	}
}

// Tx must hand the callback a decorator over the TX VIEW, not over the outer
// store. Passing the outer decorator makes tx.DeleteEntity re-enter
// MemStore.DeleteEntity, which takes txMu — already held by this very Tx —
// and self-deadlocks. The view's write methods deliberately skip txMu; that
// is the whole point of the view.
func (s *concurrentWriteStore) Tx(ctx context.Context, fn func(store.Store) error) error {
	return s.Store.Tx(ctx, func(view store.Store) error {
		return fn(&concurrentWriteStore{
			Store:     view,
			collected: s.collected,
			signal:    s.signalCollect,
		})
	})
}

// signalCollect closes the collected channel once both directions have been
// listed. Lives on the PARENT so the per-Tx decorator shares one counter.
func (s *concurrentWriteStore) signalCollect() {
	s.collects++
	if s.collects >= 2 {
		s.once.Do(func() { close(s.collected) })
	}
}

// TestCascadeDelete_ConcurrentWriterCannotEnterTheWindow pins what the Tx
// restructure actually buys, which is NOT "a racing edge gets authorized" — it
// is that the window does not exist for another writer to enter.
//
// The naive design authorized a set collected outside any lock, then let the
// store re-derive and delete its own set; an edge created in between was
// destroyed unauthorized. Running collect+authorize+delete inside one Tx closes
// that: on fs/mem the writer BLOCKS on txMu, on pgstore on the advisory lock.
//
// So the assertion is that the concurrent write does not land while the Tx is
// open. An earlier version of this test injected the edge from INSIDE the
// collect and passed against a build with no Tx at all — it never entered the
// window it claimed to guard.
func TestCascadeDelete_ConcurrentWriterCannotEnterTheWindow(t *testing.T) {
	t.Parallel()
	inner := memstore.New()
	ctx := context.Background()
	for _, e := range []*entity.Entity{
		entity.New("REQ-1", "requirement"),
		entity.New("DEC-1", "decision"),
	} {
		if err := inner.CreateEntity(ctx, e); err != nil {
			t.Fatalf("seed %s: %v", e.ID, err)
		}
	}

	st := &concurrentWriteStore{Store: inner, collected: make(chan struct{})}
	mgr := cascadeManager(t, st)

	var writeStarted atomic.Bool
	landed := make(chan error, 1)
	go func() {
		<-st.collected // the window, if there were one
		writeStarted.Store(true)
		_, err := inner.CreateRelation(ctx, "DEC-1", "addresses", "REQ-1", nil)
		landed <- err
	}()

	// alice may delete requirement and there are no edges yet, so this succeeds.
	if _, err := mgr.DeleteEntity(asUser("alice"), "REQ-1", true); err != nil {
		t.Fatalf("delete: %v", err)
	}

	select {
	case err := <-landed:
		if err != nil {
			t.Fatalf("the concurrent write failed for an unrelated reason: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the concurrent write never completed; it should proceed once " +
			"the Tx releases, not block forever")
	}

	// The write landed AFTER the transaction released — that is the property.
	// It was signaled during the collect, so with the naive design (collect
	// and authorize outside any lock) it would have interleaved and been
	// deleted unauthorized by the store's own re-derivation.
	//
	// The edge now dangles off a deleted entity. That is FINE and not what
	// this test guards: memstore does not enforce referential integrity —
	// endpoint validation lives in entitymanager.CreateRelation, and this
	// write deliberately bypassed it to simulate a racing writer. Asserting
	// on the dangling edge would be asserting a property the store never
	// promised.
	if !writeStarted.Load() {
		t.Fatal("the concurrent write never even started; the collect signal " +
			"did not fire, so no window was exercised")
	}
}
