package entitymanager_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/statemachine"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
)

// BenchmarkValidateCreate pins the dry-run validation hot path
// (TKT-9Y4ZWS). The data-entry SPA calls ValidateCreate per keystroke,
// and TestValidateCreate_SkipsIDGeneration (RR-8I07) pins that it must
// not scan the store. The store is seeded with 1000 entities so a
// broken no-scan contract is visible here too: ns/op tracking the seed
// count means dry-run cost became O(store).
// TestValidateCreate_AllocCeiling enforces the allocation contract in
// regular CI runs.
func BenchmarkValidateCreate(b *testing.B) {
	meta, err := metamodel.Parse([]byte(testMetamodelYAML))
	if err != nil {
		b.Fatal(err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       memstore.New(),
		Meta:        meta,
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()
	for i := range 1000 {
		e := entity.New("", "requirement")
		e.SetString("title", fmt.Sprintf("seed requirement %d", i))
		if _, err := mgr.CreateEntity(ctx, e, entity.CreateOptions{}); err != nil {
			b.Fatalf("seed %d: %v", i, err)
		}
	}

	candidate := entity.New("", "requirement")
	candidate.SetString("title", "draft being typed")

	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := mgr.ValidateCreate(ctx, candidate, entity.CreateOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}

// TestValidateCreate_AllocCeiling turns the benchmark's allocation
// measurement into an enforced contract: ns/op is machine-dependent
// and can't gate CI, but allocs/op is deterministic. Measured ~7
// allocs; the ceiling leaves headroom for Go-version drift while still
// catching an accidental O(store) regression (which would show up as
// hundreds of allocations from list iteration).
//
// NOT parallel: testing.AllocsPerRun panics inside parallel tests
// (concurrent allocations would corrupt the measurement).
func TestValidateCreate_AllocCeiling(t *testing.T) {
	meta, err := metamodel.Parse([]byte(testMetamodelYAML))
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := entitymanager.New(entitymanager.Deps{
		Store:       memstore.New(),
		Meta:        meta,
		Templater:   nopTemplater{},
		Audit:       audit.Nop{},
		ACL:         acl.NopACL{},
		Transitions: statemachine.EmptySet(),
		FieldGate:   entitymanager.AllowAllFieldGate{},
		Twins:       entitymanager.NoTwinOwnership{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := range 200 {
		e := entity.New("", "requirement")
		e.SetString("title", fmt.Sprintf("seed %d", i))
		if _, err := mgr.CreateEntity(ctx, e, entity.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	candidate := entity.New("", "requirement")
	candidate.SetString("title", "draft being typed")

	avg := testing.AllocsPerRun(100, func() {
		_, _, _ = mgr.ValidateCreate(ctx, candidate, entity.CreateOptions{})
	})
	if avg > 20 {
		t.Fatalf("ValidateCreate allocations regressed: %.0f allocs/op, ceiling 20 (measured ~7; "+
			"a jump usually means the dry-run path started scanning the store)", avg)
	}
}
