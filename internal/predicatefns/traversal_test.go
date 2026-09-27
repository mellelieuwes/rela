package predicatefns

import (
	"errors"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/predicate"
)

// traversalMeta mirrors the shape that makes unions hard in the live tickets
// schema: `caused-by` points at four types, and `status` is declared on each
// with a DIFFERENT enum type — which is why a bare property reference through
// it cannot be typed.
func traversalMeta() *metamodel.Metamodel {
	return &metamodel.Metamodel{
		Types: map[string]metamodel.CustomType{
			"ticket_status":  {Values: []string{"open", "done"}},
			"concept_status": {Values: []string{"open", "done"}},
		},
		Entities: map[string]metamodel.EntityDef{
			"ticket": {Properties: map[string]metamodel.PropertyDef{
				"status":   {Type: "ticket_status"},
				"tags":     {Type: metamodel.PropertyTypeString, List: true},
				"estimate": {Type: metamodel.PropertyTypeInteger},
			}},
			"concept": {Properties: map[string]metamodel.PropertyDef{
				"status": {Type: "concept_status"},
			}},
			"person": {Properties: map[string]metamodel.PropertyDef{
				"name": {Type: metamodel.PropertyTypeString},
			}},
		},
		Relations: map[string]metamodel.RelationDef{
			// Single target: no ascription needed.
			"owned-by": {From: []string{"ticket", "concept"}, To: []string{"person"}},
			// Union target: ascription required.
			"caused-by": {From: []string{"ticket"}, To: []string{"concept", "ticket"}},
		},
	}
}

func compileFor(t *testing.T, src string) *predicate.Program {
	t.Helper()
	env := predicate.NewEnv()
	if err := env.DeclareVar("entity", predicate.RecordType{"status": predicate.StringType}); err != nil {
		t.Fatal(err)
	}
	if err := DeclareCurrentUser(env); err != nil {
		t.Fatal(err)
	}
	if err := env.DeclareVar("other", predicate.RecordType{"id": predicate.StringType}); err != nil {
		t.Fatal(err)
	}
	prog, err := predicate.Compile(env, src)
	if err != nil {
		t.Fatalf("compile %q: %v", src, err)
	}
	return prog
}

func TestValidateTraversals(t *testing.T) {
	meta := traversalMeta()
	meta.Relations["about"] = metamodel.RelationDef{From: []string{"concept"}, To: []string{"person"}}
	for _, tc := range []struct {
		name, src, wantErr string
	}{
		{
			name: "single-target relation needs no ascription",
			src:  `related(entity, 'owned-by', { name = 'alice' })`,
		},
		{
			name: "union target with a valid ascription",
			src:  `related(entity, 'caused-by', { type = 'concept', status = 'open' })`,
		},
		{
			name:    "union target without an ascription is refused",
			src:     `related(entity, 'caused-by', { status = 'open' })`,
			wantErr: "add type=",
		},
		{
			name:    "ascription naming a non-target is refused",
			src:     `related(entity, 'caused-by', { type = 'person' })`,
			wantErr: "is not a target of",
		},
		{
			name:    "unknown relation type",
			src:     `related(entity, 'no-such-rel')`,
			wantErr: "unknown relation type",
		},
		{
			// `about` starts from concept, so traversing it FROM a ticket can
			// never match. Refusing at load names the relation; letting it
			// through would return an empty result that reads as "no data".
			name:    "relation that does not start from this type",
			src:     `related(entity, 'about')`,
			wantErr: "does not start from",
		},
		{
			name:    "undeclared property on the target",
			src:     `related(entity, 'caused-by', { type = 'concept', nope = 'x' })`,
			wantErr: `has no property "nope"`,
		},
		{
			name:    "list property is refused",
			src:     `related(entity, 'caused-by', { type = 'ticket', tags = 'x' })`,
			wantErr: "is a list",
		},
		{
			// A typo would match nothing, so `not related` would match all.
			name:    "undeclared enum value is refused",
			src:     `related(entity, 'caused-by', { type = 'ticket', status = 'opne' })`,
			wantErr: "not a declared value",
		},
		{
			name:    "empty string literal is refused",
			src:     `related(entity, 'caused-by', { type = 'ticket', status = '' })`,
			wantErr: "empty string",
		},
		{
			// The store compares as a string; an integer would never match
			// in SQL while matching in Go.
			name:    "non-string-shaped property is refused",
			src:     `related(entity, 'caused-by', { type = 'ticket', estimate = '3' })`,
			wantErr: "cannot be compared in a traversal",
		},
		{
			name: "id literal",
			src:  `related(entity, 'owned-by', { id = 'alice' })`,
		},
		{
			name: "id from current_user",
			src:  `related(entity, 'owned-by', { id = current_user.id })`,
		},
		{
			name: "string property from current_user",
			src:  `related(entity, 'owned-by', { name = current_user.id })`,
		},
		{
			// The engine accepts any string field; this layer keeps the one
			// every surface binds identically.
			name:    "current_user.tool is refused",
			src:     `related(entity, 'owned-by', { id = current_user.tool })`,
			wantErr: "the only variable a constraint may read is current_user.id",
		},
		{
			name:    "another variable is refused",
			src:     `related(entity, 'owned-by', { id = other.id })`,
			wantErr: "the only variable a constraint may read is current_user.id",
		},
		{
			name:    "current_user.id on an undeclared property",
			src:     `related(entity, 'owned-by', { nope = current_user.id })`,
			wantErr: `has no property "nope"`,
		},
		{
			name:    "current_user.id on a non-string-shaped property",
			src:     `related(entity, 'caused-by', { type = 'ticket', estimate = current_user.id })`,
			wantErr: "cannot be compared in a traversal",
		},
		{
			name:    "current_user.id on a list property",
			src:     `related(entity, 'caused-by', { type = 'ticket', tags = current_user.id })`,
			wantErr: "is a list",
		},
		{
			// A user id is never a declared value: `not related` would match all.
			name:    "current_user.id on an enum property",
			src:     `related(entity, 'caused-by', { type = 'ticket', status = current_user.id })`,
			wantErr: "is an enum",
		},
		{
			name:    "intermediate union hop is refused",
			src:     `related(entity, { 'caused-by', 'owned-by' }, { type = 'person' })`,
			wantErr: "cannot be resolved",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTraversals(meta, "ticket", compileFor(t, tc.src))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// A chain through single-target relations resolves hop by hop, so the property
// is checked against the FINAL type rather than the starting one.
func TestValidateTraversals_ChainResolvesToTheFinalType(t *testing.T) {
	meta := traversalMeta()
	meta.Relations["about"] = metamodel.RelationDef{From: []string{"concept"}, To: []string{"person"}}

	// ticket -caused-by(concept)-> concept -about-> person, filtering person.name
	if err := ValidateTraversals(meta, "ticket",
		compileFor(t, `related(entity, { 'caused-by', 'about' }, { type = 'person', name = 'alice' })`)); err == nil {
		t.Fatal("expected refusal: the first hop is a union and cannot be resolved mid-chain")
	}

	// A fully single-target chain resolves.
	meta.Relations["derives"] = metamodel.RelationDef{From: []string{"ticket"}, To: []string{"concept"}}
	if err := ValidateTraversals(meta, "ticket",
		compileFor(t, `related(entity, { 'derives', 'about' }, { name = 'alice' })`)); err != nil {
		t.Fatalf("single-target chain must resolve: %v", err)
	}
	// ...and the property is checked on the final type, not the first hop's.
	err := ValidateTraversals(meta, "ticket",
		compileFor(t, `related(entity, { 'derives', 'about' }, { status = 'open' })`))
	if err == nil || !strings.Contains(err.Error(), `has no property "status"`) {
		t.Fatalf("expected the property to be checked on person, got %v", err)
	}
}

// A nil metamodel or program must not panic: callers compile before they have
// a schema in some paths.
func TestValidateTraversals_NilInputs(t *testing.T) {
	if err := ValidateTraversals(nil, "ticket", nil); err != nil {
		t.Fatalf("nil inputs must be a no-op, got %v", err)
	}
}

// BindTraversal is how every caller outside Eval binds current_user.id. A
// missing identity must be ErrNoCurrentUser, never a spec that still lowers:
// an unbound id constraint would reach the store as an empty endpoint set,
// which reads as "any endpoint".
func TestBindTraversal(t *testing.T) {
	spec := compileFor(t, `related(entity, 'owned-by', { id = current_user.id, name = current_user.id })`).
		Traversals()[0]

	got, err := BindTraversal(spec, "alice")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if got.ID != predicate.NewString("alice") || got.Props["name"] != predicate.NewString("alice") || len(got.Refs) != 0 {
		t.Fatalf("bound spec = %+v", got)
	}

	if _, err := BindTraversal(spec, ""); !errors.Is(err, ErrNoCurrentUser) {
		t.Fatalf("empty identity: err = %v, want ErrNoCurrentUser", err)
	}

	literal := compileFor(t, `related(entity, 'owned-by', { id = 'bob' })`).Traversals()[0]
	if got, err := BindTraversal(literal, ""); err != nil || got.Key() != literal.Key() {
		t.Fatalf("a spec with no refs must bind unchanged without an identity: %v %+v", err, got)
	}

	tool := compileFor(t, `related(entity, 'owned-by', { id = current_user.tool })`).Traversals()[0]
	if _, err := BindTraversal(tool, "alice"); err == nil {
		t.Fatal("current_user.tool must not bind")
	}
}
