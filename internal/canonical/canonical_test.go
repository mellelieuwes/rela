package canonical_test

import (
	"testing"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/canonical"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/markdown"
)

// mustReflow models the body form fsstore stores on disk: it runs the content
// through the same markdown normalization fsstore applies before writing.
func mustReflow(s string) string {
	return markdown.FormatMarkdown(s)
}

func TestHashEntity_Deterministic(t *testing.T) {
	// Two separately constructed but equal entities must hash identically.
	mk := func() entity.Entity {
		return entity.Entity{
			ID:         "TKT-001",
			Type:       "ticket",
			Properties: map[string]any{"title": "Hello", "priority": 3},
			Content:    "Some body text.",
		}
	}
	if got, want := canonical.HashEntity(mk()), canonical.HashEntity(mk()); got != want {
		t.Fatalf("hash not deterministic: %s != %s", got, want)
	}
}

// TestHashEntity_TypeInvariance is the core guarantee: the same logical value
// produced by the YAML (fsstore) and JSONB (pgstore) decode paths must hash
// identically despite different concrete Go types.
func TestHashEntity_TypeInvariance(t *testing.T) {
	base := func(props map[string]any) entity.Entity {
		return entity.Entity{ID: "E1", Type: "ticket", Properties: props}
	}

	cases := []struct {
		name string
		a    map[string]any
		b    map[string]any
	}{
		{
			name: "int vs int64",
			a:    map[string]any{"n": int(5)},
			b:    map[string]any{"n": int64(5)},
		},
		{
			name: "int vs int32",
			a:    map[string]any{"n": int(5)},
			b:    map[string]any{"n": int32(5)},
		},
		{
			name: "string slice vs interface slice",
			a:    map[string]any{"tags": []string{"x", "y"}},
			b:    map[string]any{"tags": []any{"x", "y"}},
		},
		{
			name: "nested map key order",
			a:    map[string]any{"m": map[string]any{"a": 1, "b": 2}},
			b:    map[string]any{"m": map[string]any{"b": 2, "a": 1}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ha := canonical.HashEntity(base(tc.a))
			hb := canonical.HashEntity(base(tc.b))
			if ha != hb {
				t.Fatalf("type-variant inputs hashed differently:\n a=%s\n b=%s", ha, hb)
			}
		})
	}
}

// TestHashEntity_PropertyOrderInvariance asserts the top-level property map
// order does not affect the hash (Go map iteration order is random).
func TestHashEntity_PropertyOrderInvariance(t *testing.T) {
	a := entity.Entity{ID: "E1", Type: "t", Properties: map[string]any{
		"alpha": "1", "beta": "2", "gamma": "3", "delta": "4",
	}}
	b := entity.Entity{ID: "E1", Type: "t", Properties: map[string]any{
		"delta": "4", "gamma": "3", "beta": "2", "alpha": "1",
	}}
	if canonical.HashEntity(a) != canonical.HashEntity(b) {
		t.Fatal("property order affected the hash")
	}
}

// TestHashEntity_IgnoresStorageMetadata asserts UpdatedAt and Inaccessible do
// not participate in the content hash.
func TestHashEntity_IgnoresStorageMetadata(t *testing.T) {
	base := entity.Entity{ID: "E1", Type: "t", Properties: map[string]any{"k": "v"}, Content: "body"}
	withTime := base
	withTime.UpdatedAt = time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	withInacc := base
	withInacc.Inaccessible = []entity.InaccessibleField{{Name: "secret", Reason: entity.InaccessibleReasonGitCrypt}}

	if canonical.HashEntity(base) != canonical.HashEntity(withTime) {
		t.Error("UpdatedAt affected the hash")
	}
	if canonical.HashEntity(base) != canonical.HashEntity(withInacc) {
		t.Error("Inaccessible affected the hash")
	}
}

// TestHashEntity_NilVsEmptyProps asserts a nil property map and an empty
// (non-nil) map hash identically — pgstore yields a non-nil empty map, fsstore
// may yield nil for a frontmatter-less entity.
func TestHashEntity_NilVsEmptyProps(t *testing.T) {
	a := entity.Entity{ID: "E1", Type: "t", Properties: nil}
	b := entity.Entity{ID: "E1", Type: "t", Properties: map[string]any{}}
	if canonical.HashEntity(a) != canonical.HashEntity(b) {
		t.Fatal("nil and empty property maps hashed differently")
	}
}

// TestHashEntity_BodyIdempotent asserts the body-canonicalization is idempotent:
// fsstore stores an already-reflowed body, pgstore stores raw content. Both must
// converge. We model this by hashing a raw body and its once-reflowed form.
func TestHashEntity_BodyIdempotent(t *testing.T) {
	raw := "# Heading\n\nA paragraph that is reasonably long and might get wrapped by the markdown formatter at eighty columns when normalized.\n\n- item one\n- item two\n"
	rawEnt := entity.Entity{ID: "E1", Type: "t", Content: raw}

	// Hash once; then take the canonical body form and hash an entity built
	// from it. They must be equal (idempotency of FormatMarkdown).
	h1 := canonical.HashEntity(rawEnt)

	// Simulate fsstore having stored the reflowed body by reflowing twice.
	reflowedOnce := entity.Entity{ID: "E1", Type: "t", Content: mustReflow(raw)}
	h2 := canonical.HashEntity(reflowedOnce)

	if h1 != h2 {
		t.Fatalf("body canonicalization not idempotent:\n raw=%s\n reflowed=%s", h1, h2)
	}
}

func TestHashEntity_DistinctContentDistinctHash(t *testing.T) {
	a := entity.Entity{ID: "E1", Type: "t", Properties: map[string]any{"k": "v1"}}
	b := entity.Entity{ID: "E1", Type: "t", Properties: map[string]any{"k": "v2"}}
	if canonical.HashEntity(a) == canonical.HashEntity(b) {
		t.Fatal("different property values produced the same hash")
	}
}

// TestHashEntity_NoFieldCollision guards against concatenation collisions: a
// value that moves a delimiter-shaped string between fields must not collide.
func TestHashEntity_NoFieldCollision(t *testing.T) {
	// id="A" type="B"  vs  id="A\x1fB" type=""  must differ.
	a := entity.Entity{ID: "A", Type: "B"}
	b := entity.Entity{ID: "A\x1fB", Type: ""}
	if canonical.HashEntity(a) == canonical.HashEntity(b) {
		t.Fatal("field boundary collision")
	}
	// string "1" vs int 1 in a property must differ.
	s := entity.Entity{ID: "E", Type: "t", Properties: map[string]any{"k": "1"}}
	i := entity.Entity{ID: "E", Type: "t", Properties: map[string]any{"k": 1}}
	if canonical.HashEntity(s) == canonical.HashEntity(i) {
		t.Fatal("string and int values collided")
	}
}

// TestHashEntity_PropertyValueCollision is the regression for the critical
// review finding (RR-3A4I1Z): a property value containing delimiter bytes must
// not let one entity forge another's canonical form. Under the old
// delimiter-by-concatenation scheme these two collided; the length-prefixed
// encoding makes them distinct.
func TestHashEntity_PropertyValueCollision(t *testing.T) {
	// Two properties {a:"p", b:"q"} vs one property whose value embeds the
	// record/unit separators and the value sigil to imitate a second property.
	twoProps := entity.Entity{ID: "E", Type: "t", Properties: map[string]any{
		"a": "p", "b": "q",
	}}
	forged := entity.Entity{ID: "E", Type: "t", Properties: map[string]any{
		"a": "p\x1eb\x1fs:q",
	}}
	if canonical.HashEntity(twoProps) == canonical.HashEntity(forged) {
		t.Fatal("property-value delimiter injection produced a collision")
	}

	// A value that ends with what looks like the start of another value, and a
	// key that absorbs it, must also stay distinct.
	x := entity.Entity{ID: "E", Type: "t", Properties: map[string]any{"k": "v", "kk": "w"}}
	y := entity.Entity{ID: "E", Type: "t", Properties: map[string]any{"k": "v\x1fkk\x1fw"}}
	if canonical.HashEntity(x) == canonical.HashEntity(y) {
		t.Fatal("key/value boundary collision")
	}
}

// TestHashEntity_DateEqualsString is the regression for RR-QUXNPR: a date
// decoded as time.Time (fsstore) and the same value as the RFC3339 string
// pgstore reads back must hash identically. The package normalizes time.Time to
// its RFC3339 string, so a date and a user-typed identical string also agree —
// which is correct, since pg cannot distinguish them.
func TestHashEntity_DateEqualsString(t *testing.T) {
	d := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	asTime := entity.Entity{ID: "E", Type: "t", Properties: map[string]any{"due": d}}
	asString := entity.Entity{ID: "E", Type: "t", Properties: map[string]any{"due": "2026-06-19T00:00:00Z"}}
	if canonical.HashEntity(asTime) != canonical.HashEntity(asString) {
		t.Fatal("time.Time and its RFC3339 string hashed differently")
	}
}

// TestHashEntity_WholeFloatEqualsInt is the regression for RR-KTAK7N: a
// whole-valued float (fsstore's decode of "2.0") and the int pgstore folds it
// to must hash identically.
func TestHashEntity_WholeFloatEqualsInt(t *testing.T) {
	asFloat := entity.Entity{ID: "E", Type: "t", Properties: map[string]any{"n": 2.0}}
	asInt := entity.Entity{ID: "E", Type: "t", Properties: map[string]any{"n": 2}}
	if canonical.HashEntity(asFloat) != canonical.HashEntity(asInt) {
		t.Fatal("whole-valued float 2.0 and int 2 hashed differently")
	}
	// A genuinely fractional float must NOT collapse to an int.
	frac := entity.Entity{ID: "E", Type: "t", Properties: map[string]any{"n": 2.5}}
	if canonical.HashEntity(frac) == canonical.HashEntity(asInt) {
		t.Fatal("fractional float 2.5 collided with int 2")
	}
}

// TestHashEntity_NonStringKeyedMap is the regression for RR-N7D3OK: yaml decodes
// a non-string-keyed mapping to map[any]any. It must canonicalize the same as
// the string-keyed map pgstore produces for the equivalent JSON.
func TestHashEntity_NonStringKeyedMap(t *testing.T) {
	anyKeyed := entity.Entity{ID: "E", Type: "t", Properties: map[string]any{
		"m": map[any]any{1: "a", 2: "b"},
	}}
	stringKeyed := entity.Entity{ID: "E", Type: "t", Properties: map[string]any{
		"m": map[string]any{"1": "a", "2": "b"},
	}}
	if canonical.HashEntity(anyKeyed) != canonical.HashEntity(stringKeyed) {
		t.Fatal("map[any]any and map[string]any with equal logical content hashed differently")
	}
}

// TestCanonicalValue_AllKinds exercises every value kind the canonicalizer
// handles, so the type switch is fully covered and each kind is distinct from
// the others.
func TestCanonicalValue_AllKinds(t *testing.T) {
	mk := func(v any) string {
		return canonical.HashEntity(entity.Entity{ID: "E", Type: "t", Properties: map[string]any{"k": v}})
	}
	hashes := map[string]string{
		"nil":            mk(nil),
		"string":         mk("hello"),
		"bool-true":      mk(true),
		"bool-false":     mk(false),
		"int":            mk(int(7)),
		"int8":           mk(int8(7)),
		"int16":          mk(int16(7)),
		"int32":          mk(int32(7)),
		"int64":          mk(int64(7)),
		"uint":           mk(uint(7)),
		"uint8":          mk(uint8(7)),
		"uint16":         mk(uint16(7)),
		"uint32":         mk(uint32(7)),
		"uint64":         mk(uint64(7)),
		"float64-frac":   mk(3.5),
		"float32-frac":   mk(float32(3.5)),
		"slice-string":   mk([]string{"a"}),
		"slice-any":      mk([]any{"a"}),
		"map":            mk(map[string]any{"x": 1}),
		"unknown-struct": mk(struct{ A int }{A: 1}),
	}

	// All signed/unsigned integer widths of the same value 7 must agree.
	intKinds := []string{"int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64"}
	for _, k := range intKinds[1:] {
		if hashes[k] != hashes["int"] {
			t.Errorf("integer kind %q hashed differently from int", k)
		}
	}
	// float32(3.5) and float64(3.5) must agree (3.5 is exactly representable).
	if hashes["float32-frac"] != hashes["float64-frac"] {
		t.Error("float32 and float64 of 3.5 hashed differently")
	}
	// []string{"a"} and []any{"a"} must agree.
	if hashes["slice-string"] != hashes["slice-any"] {
		t.Error("[]string and []any hashed differently")
	}
	// Distinct kinds must not collide with each other (spot checks).
	distinct := []string{"nil", "string", "bool-true", "int", "float64-frac", "slice-any", "map", "unknown-struct"}
	for i := range distinct {
		for j := i + 1; j < len(distinct); j++ {
			if hashes[distinct[i]] == hashes[distinct[j]] {
				t.Errorf("kinds %q and %q collided", distinct[i], distinct[j])
			}
		}
	}
}

func TestHashRelation_Deterministic(t *testing.T) {
	mk := func() entity.Relation {
		return entity.Relation{
			From: "A", Type: "implements", To: "B",
			Properties: map[string]any{"weight": 2},
			Content:    "why",
		}
	}
	if got, want := canonical.HashRelation(mk()), canonical.HashRelation(mk()); got != want {
		t.Fatalf("relation hash not deterministic: %s != %s", got, want)
	}
}

func TestHashRelation_TypeInvariance(t *testing.T) {
	a := entity.Relation{From: "A", Type: "rel", To: "B", Properties: map[string]any{"w": int(1)}}
	b := entity.Relation{From: "A", Type: "rel", To: "B", Properties: map[string]any{"w": int64(1)}}
	if canonical.HashRelation(a) != canonical.HashRelation(b) {
		t.Fatal("relation type-variant inputs hashed differently")
	}
}

// TestHashRelation_DirectionMatters asserts A--rel-->B and B--rel-->A differ.
func TestHashRelation_DirectionMatters(t *testing.T) {
	a := entity.Relation{From: "A", Type: "rel", To: "B"}
	b := entity.Relation{From: "B", Type: "rel", To: "A"}
	if canonical.HashRelation(a) == canonical.HashRelation(b) {
		t.Fatal("relation direction did not affect the hash")
	}
}

// TestEntityRelationDisjoint asserts an entity and a relation with coincidentally
// similar fields never collide (the "entity"/"relation" prefix guards this).
func TestEntityRelationDisjoint(t *testing.T) {
	e := entity.Entity{ID: "A", Type: "rel"}
	r := entity.Relation{From: "A", Type: "rel", To: ""}
	if canonical.HashEntity(e) == canonical.HashRelation(r) {
		t.Fatal("entity and relation hashes collided")
	}
}

// TestEqualValue_AgreesWithHash pins that the value comparison twins
// reconcile with is the hash's notion of "same", in both directions: two
// values are equal exactly when an entity holding one hashes like an entity
// holding the other.
func TestEqualValue_AgreesWithHash(t *testing.T) {
	when := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		a, b any
		want bool
	}{
		{"int vs whole float", 3, 3.0, true},
		{"int vs int64", 3, int64(3), true},
		{"fractional floats differ", 3.5, 3.25, false},
		{"string slice vs any slice", []string{"x", "y"}, []any{"x", "y"}, true},
		{"list order matters", []string{"x", "y"}, []any{"y", "x"}, false},
		{"time vs its RFC3339 string", when, "2026-05-01T00:00:00Z", true},
		{"nil vs nil", nil, nil, true},
		{"nil vs empty string", nil, "", false},
		{"nil vs empty list", nil, []any{}, false},
		{"string vs number", "1", 1, false},
		{"bools", true, false, false},
		{"nested maps", map[string]any{"a": 1}, map[any]any{"a": 1.0}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canonical.EqualValue(tc.a, tc.b); got != tc.want {
				t.Fatalf("EqualValue(%#v, %#v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			ea := entity.Entity{ID: "E1", Type: "t", Properties: map[string]any{"p": tc.a}}
			eb := entity.Entity{ID: "E1", Type: "t", Properties: map[string]any{"p": tc.b}}
			if hashEqual := canonical.HashEntity(ea) == canonical.HashEntity(eb); hashEqual != tc.want {
				t.Fatalf("hash equality %v disagrees with EqualValue %v", hashEqual, tc.want)
			}
		})
	}
}

// TestEqualBody_IgnoresReflow pins that a body fsstore reflowed on write
// equals the text it was written from, while a real edit does not.
func TestEqualBody_IgnoresReflow(t *testing.T) {
	raw := "A long line that the formatter will certainly want to wrap because it runs well past eighty columns."
	if mustReflow(raw) == raw {
		t.Fatal("precondition: the formatter must change this body, or the test proves nothing")
	}
	if !canonical.EqualBody(raw, mustReflow(raw)) {
		t.Fatal("a reflowed body must equal its source")
	}
	if canonical.EqualBody(raw, raw+" More.") {
		t.Fatal("an edited body must not equal the original")
	}
}
