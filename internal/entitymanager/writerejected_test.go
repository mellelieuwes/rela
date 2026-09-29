package entitymanager_test

import (
	"errors"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// writeRejected is the structural marker consumers that cannot import this
// package (the twins sync path) use to tell "the resulting entity is
// invalid" apart from conflicts, denials and I/O failures.
func writeRejected(err error) bool {
	var m interface{ WriteRejected() bool }
	return errors.As(err, &m) && m.WriteRejected()
}

// TestWriteErrors_WriteRejectedMarker pins which write failures carry the
// marker. Content refusals do; a CAS conflict, an authorization denial (row
// ACL or transition guard) and a missing entity must not, or a twin pull
// would record a retryable failure as a permanent rejection.
func TestWriteErrors_WriteRejectedMarker(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		write func(t *testing.T) error
		want  bool
	}{
		{name: "unique violation", want: true, write: func(t *testing.T) error {
			t.Helper()
			mgr := newUniqueManager(t)
			for id, email := range map[string]string{"P-1": "a@x", "P-2": "b@x"} {
				e := entity.New(id, "persoon")
				e.SetString("email", email)
				if _, err := mgr.CreateEntity(t.Context(), e, entity.CreateOptions{ID: id}); err != nil {
					t.Fatalf("seed %s: %v", id, err)
				}
			}
			_, err := mgr.PatchEntity(t.Context(), "P-2", entity.Patch{Properties: map[string]any{"email": "a@x"}})
			return err
		}},
		{name: "illegal transition", want: true, write: func(t *testing.T) error {
			t.Helper()
			mgr := newTransitionManager(t, allowAllGuard{})
			snap := seedSnapshot(t, mgr, "")
			snap.SetString("status", "established")
			_, err := mgr.UpdateEntity(t.Context(), snap)
			if !errors.Is(err, statemachine.ErrIllegalTransition) {
				t.Fatalf("err = %v, want ErrIllegalTransition still in the chain", err)
			}
			return err
		}},
		{name: "transition guard denial", want: false, write: func(t *testing.T) error {
			t.Helper()
			mgr := newTransitionManager(t, denyAllGuard{})
			snap := seedSnapshot(t, mgr, "")
			snap.SetString("status", "approved")
			_, err := mgr.UpdateEntity(t.Context(), snap)
			return err
		}},
		{name: "cas conflict", want: false, write: func(t *testing.T) error {
			t.Helper()
			st := memstore.New()
			mgr := newManagerOverStore(t, st)
			stored, _ := seedCASEntity(t, st)
			_, err := mgr.PatchEntity(t.Context(), stored.ID, entity.Patch{
				Properties: map[string]any{"title": "x"}, ExpectedVersion: "stale",
			})
			return err
		}},
		{name: "acl denial", want: false, write: func(t *testing.T) error {
			t.Helper()
			st := memstore.New()
			seedTask(t, st, "TASK-1", map[string]any{"title": "t"}, "")
			meta, err := metamodel.Parse([]byte(patchMetamodelYAML))
			if err != nil {
				t.Fatalf("metamodel.Parse: %v", err)
			}
			mgr, err := entitymanager.New(entitymanager.Deps{
				Store: st, Meta: meta, Templater: nopTemplater{}, Audit: audit.Nop{},
				ACL: acl.ReadOnlyACL{}, Transitions: statemachine.EmptySet(),
				FieldGate: entitymanager.AllowAllFieldGate{}, Twins: entitymanager.NoTwinOwnership{},
			})
			if err != nil {
				t.Fatalf("entitymanager.New: %v", err)
			}
			_, err = mgr.PatchEntity(t.Context(), "TASK-1", entity.Patch{Properties: map[string]any{"title": "x"}})
			return err
		}},
		{name: "entity not found", want: false, write: func(t *testing.T) error {
			t.Helper()
			mgr, _ := newPatchManager(t, nil)
			_, err := mgr.PatchEntity(t.Context(), "TASK-404", entity.Patch{Properties: map[string]any{"title": "x"}})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.write(t)
			if err == nil {
				t.Fatal("write succeeded; the case needs a failing write")
			}
			if got := writeRejected(err); got != tc.want {
				t.Fatalf("WriteRejected(%v) = %v, want %v", err, got, tc.want)
			}
		})
	}
}
