package visibility_test

import (
	"context"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

// Every reader here takes an ADDRESS (`ID` or `ID@face`), not an id
// (BUG-R1PQY9). memstore and fsstore used to answer GetEntity("ID@face") by
// coincidence, because they key their index on the formatted address, while
// pgstore and sqlitestore look up (id, face) and missed it. The stores now
// refuse a suffixed id, so each reader must split the address itself; one
// that forwards it whole reads nothing on any backend. That is what fails here
// if a reader regresses to GetEntity(addr): memstore refuses the suffixed id.
//
// The gate admits exactly the bare id, so a reader that hands the whole
// address to the row gate misses as well.
func TestReaders_ResolveAnAddressToItsFace(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	for _, e := range []*entity.Entity{
		{ID: "TKT-1", Type: "ticket", Properties: map[string]any{"title": "bare"}},
		{ID: "TKT-1", Type: "ticket", Face: "draft", Properties: map[string]any{"title": "draft"}},
	} {
		if err := st.CreateEntity(ctx, e); err != nil {
			t.Fatalf("seed %s@%q: %v", e.ID, e.Face, err)
		}
	}

	policy, err := visibility.NewPolicyReader(idGate{id: "TKT-1"}, visibility.NopRedactor{}, st)
	if err != nil {
		t.Fatalf("NewPolicyReader: %v", err)
	}
	allowAll, err := visibility.NewAllowAllReader(st)
	if err != nil {
		t.Fatalf("NewAllowAllReader: %v", err)
	}
	script, err := visibility.NewScriptReader(policy, st, nil)
	if err != nil {
		t.Fatalf("NewScriptReader: %v", err)
	}
	unrestricted := visibility.Unrestricted(st)

	type get func(addr string) (*entity.Entity, error)
	viaReader := func(r visibility.Reader) get {
		return func(addr string) (*entity.Entity, error) {
			e, ok, gerr := r.Get(ctx, "ticket", addr)
			if gerr != nil || !ok {
				return nil, gerr
			}
			return e, nil
		}
	}
	for _, tc := range []struct {
		name string
		get  get
	}{
		{"PolicyReader", viaReader(policy)},
		{"AllowAllReader", viaReader(allowAll)},
		{"ScriptReader", func(addr string) (*entity.Entity, error) { return script.GetEntity(ctx, addr) }},
		{"UnrestrictedReader", func(addr string) (*entity.Entity, error) { return unrestricted.GetEntity(ctx, addr) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for addr, want := range map[string]string{"TKT-1": "bare", "TKT-1@draft": "draft"} {
				e, gerr := tc.get(addr)
				if gerr != nil || e == nil {
					t.Fatalf("%s: got (%v, %v), want the %s row", addr, e, gerr, want)
				}
				if got := e.Properties["title"]; got != want {
					t.Errorf("%s: read the %v row, want %s", addr, got, want)
				}
			}
			if e, _ := tc.get("TKT-1@published"); e != nil {
				t.Errorf("a face with no row must miss, got %+v", e)
			}
		})
	}
}

// idGate admits exactly one id of any type.
type idGate struct{ id string }

func (g idGate) PermitsRead(_ context.Context, _, id string) (bool, error) { return id == g.id, nil }

func (g idGate) PermitsReadMany(_ context.Context, _ string, ids []string) (map[string]bool, error) {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = id == g.id
	}
	return out, nil
}
