package computed_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/computed"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
)

func meta(props map[string]metamodel.PropertyDef) *metamodel.Metamodel {
	return &metamodel.Metamodel{Entities: map[string]metamodel.EntityDef{
		"item": {Properties: props},
	}}
}

func TestCompileEvaluate_ChainedAndPortable(t *testing.T) {
	m := meta(map[string]metamodel.PropertyDef{
		"a":     {Type: metamodel.PropertyTypeInteger},
		"b":     {Type: metamodel.PropertyTypeInteger, Computed: "entity.a * 2"},
		"c":     {Type: metamodel.PropertyTypeInteger, Computed: "entity.b + 1"},
		"name":  {Type: metamodel.PropertyTypeString},
		"label": {Type: metamodel.PropertyTypeString, Computed: "entity.name .. '!'"},
	})
	set, err := computed.Compile(m)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	e := &entity.Entity{ID: "I-1", Type: "item", Properties: map[string]any{"a": 4, "name": "Ada"}}
	if err := set.Evaluate(context.Background(), e); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if e.Properties["b"] != int64(8) || e.Properties["c"] != int64(9) || e.Properties["label"] != "Ada!" {
		t.Fatalf("properties = %#v", e.Properties)
	}
	if !set.SQLPortable("item", "c") {
		t.Fatal("c should be SQL-portable")
	}
}

func TestCompile_RruleIsValidButNotPortable(t *testing.T) {
	m := meta(map[string]metamodel.PropertyDef{
		"rule":  {Type: metamodel.PropertyTypeRrule},
		"start": {Type: metamodel.PropertyTypeDate},
		"next":  {Type: metamodel.PropertyTypeDate, Computed: "rrule_next(entity.rule, entity.start)"},
	})
	set, err := computed.CompileWithClock(m, func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if set.SQLPortable("item", "next") {
		t.Fatal("rrule_next must be host-only")
	}
	e := &entity.Entity{ID: "I-1", Type: "item", Properties: map[string]any{
		"rule": "FREQ=DAILY;COUNT=3", "start": "2026-01-01",
	}}
	if err := set.Evaluate(context.Background(), e); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got := e.Properties["next"]; got != "2026-01-02" {
		t.Fatalf("next = %v", got)
	}
}

func TestCompile_RejectsCyclesAndTypeMismatch(t *testing.T) {
	_, err := computed.Compile(meta(map[string]metamodel.PropertyDef{
		"a": {Type: metamodel.PropertyTypeInteger, Computed: "entity.b + 1"},
		"b": {Type: metamodel.PropertyTypeInteger, Computed: "entity.a + 1"},
	}))
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error = %v", err)
	}
	_, err = computed.Compile(meta(map[string]metamodel.PropertyDef{
		"a": {Type: metamodel.PropertyTypeInteger, Computed: "'wrong'"},
	}))
	if err == nil || !strings.Contains(err.Error(), "must be int") {
		t.Fatalf("type error = %v", err)
	}
}

// related() would fail every write (nothing binds a store) and a stored value
// could not follow later changes to the related entities: refuse at load.
func TestCompile_RejectsRelated(t *testing.T) {
	m := meta(map[string]metamodel.PropertyDef{
		"blocked": {Type: metamodel.PropertyTypeBoolean, Computed: "related(entity, 'blocks')"},
	})
	m.Relations = map[string]metamodel.RelationDef{"blocks": {From: []string{"item"}, To: []string{"item"}}}
	_, err := computed.Compile(m)
	if err == nil || !strings.Contains(err.Error(), "related") {
		t.Fatalf("err = %v, want a related() refusal", err)
	}
}

// Value selection with `c and x or y` maps one enum to another, and every
// property read inside a branch is a dependency.
func TestCompileEvaluate_ValueSelection(t *testing.T) {
	m := meta(map[string]metamodel.PropertyDef{
		"method": {Type: metamodel.PropertyTypeEnum, Values: []string{"password", "password_otp", "passkey"}},
		"base":   {Type: metamodel.PropertyTypeEnum, Values: []string{"low", "medium", "high"}, Computed: "entity.method == 'password' and 'low' or 'medium'"},
		"level": {
			Type: metamodel.PropertyTypeEnum, Values: []string{"low", "medium", "high"},
			Computed: "(entity.method == 'passkey' and 'high') or entity.base",
		},
		"weight": {Type: metamodel.PropertyTypeInteger, Computed: "entity.method == 'passkey' and 3 or 1"},
	})
	set, err := computed.Compile(m)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	tests := []struct {
		method, level string
		weight        int64
	}{
		{"password", "low", 1},
		{"password_otp", "medium", 1},
		{"passkey", "high", 3},
	}
	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			e := &entity.Entity{ID: "I-1", Type: "item", Properties: map[string]any{"method": tc.method}}
			if err := set.Evaluate(context.Background(), e); err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if e.Properties["level"] != tc.level || e.Properties["weight"] != tc.weight {
				t.Fatalf("properties = %#v", e.Properties)
			}
		})
	}
	if !set.SQLPortable("item", "level") {
		t.Fatal("level should be SQL-portable")
	}
}

// A related() call hidden in a selection branch is still refused.
func TestCompile_RejectsRelatedInsideSelection(t *testing.T) {
	m := meta(map[string]metamodel.PropertyDef{
		"name":  {Type: metamodel.PropertyTypeString},
		"label": {Type: metamodel.PropertyTypeString, Computed: "(related(entity, 'blocks') and 'blocked') or entity.name"},
	})
	m.Relations = map[string]metamodel.RelationDef{"blocks": {From: []string{"item"}, To: []string{"item"}}}
	_, err := computed.Compile(m)
	if err == nil || !strings.Contains(err.Error(), "related") {
		t.Fatalf("err = %v, want a related() refusal", err)
	}
}

// An unset boolean condition fails the write rather than choosing the
// fallback; the docs name `entity.flag == true and ...` as the guard.
func TestEvaluate_SelectionOnUnsetBoolFails(t *testing.T) {
	set, err := computed.Compile(meta(map[string]metamodel.PropertyDef{
		"flag":  {Type: metamodel.PropertyTypeBoolean},
		"state": {Type: metamodel.PropertyTypeString, Computed: "entity.flag and 'on' or 'off'"},
	}))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	e := &entity.Entity{ID: "I-1", Type: "item", Properties: map[string]any{}}
	if err := set.Evaluate(context.Background(), e); err == nil {
		t.Fatal("want an evaluation error for an unset boolean condition")
	}
}
