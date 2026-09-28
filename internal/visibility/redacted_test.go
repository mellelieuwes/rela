package visibility_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

// hideRedactor hides a fixed set of property names.
type hideRedactor struct{ names []string }

func (h hideRedactor) HiddenProperties(_ context.Context, _ *entity.Entity) map[string]struct{} {
	out := make(map[string]struct{}, len(h.names))
	for _, n := range h.names {
		out[n] = struct{}{}
	}
	return out
}

func seedEntity() *entity.Entity {
	e := entity.New("T-1", "ticket")
	e.Properties = map[string]any{
		"title":  "visible",
		"salary": "secret",
		"notes":  "also secret",
	}
	return e
}

// TestRedact_RecordsWithheldNames is the core of TKT-FJ6END: a consumer
// must be able to tell a withheld property from one that was never set.
func TestRedact_RecordsWithheldNames(t *testing.T) {
	ctx := context.Background()
	e := seedEntity()

	got := visibility.Redact(ctx, hideRedactor{names: []string{"salary", "notes"}}, e)

	if want := []string{"notes", "salary"}; !slices.Equal(got.Redacted, want) {
		t.Fatalf("Redacted = %v, want %v (sorted)", got.Redacted, want)
	}
	// The VALUES must still be gone — this feature discloses names only.
	for _, name := range []string{"salary", "notes"} {
		if _, present := got.Properties[name]; present {
			t.Errorf("property %q leaked into a redacted read: %v", name, got.Properties)
		}
	}
	if got.Properties["title"] != "visible" {
		t.Errorf("visible property lost: %v", got.Properties)
	}
	// A withheld property and an unset one must be distinguishable.
	if !got.IsRedacted("salary") {
		t.Error("IsRedacted(salary) = false, want true")
	}
	if got.IsRedacted("nonexistent") {
		t.Error("IsRedacted(nonexistent) = true, want false for a never-set property")
	}
}

// TestRedact_NothingHiddenIsUnchanged pins the allocation-free fast path:
// the no-policy read stays byte-identical to a raw read (AC 3).
func TestRedact_NothingHiddenIsUnchanged(t *testing.T) {
	ctx := context.Background()
	e := seedEntity()

	got := visibility.Redact(ctx, visibility.NopRedactor{}, e)

	if got != e {
		t.Fatal("Redact allocated a copy when nothing was hidden; want the original face")
	}
	if len(got.Redacted) != 0 {
		t.Errorf("Redacted = %v, want empty when no policy hid anything", got.Redacted)
	}
}

// TestRedact_DoesNotMutateInput guards the store-aliased original: Redact
// returns a copy, and the caller's entity must be untouched.
func TestRedact_DoesNotMutateInput(t *testing.T) {
	ctx := context.Background()
	e := seedEntity()

	_ = visibility.Redact(ctx, hideRedactor{names: []string{"salary"}}, e)

	if len(e.Redacted) != 0 {
		t.Errorf("Redact marked the INPUT entity: %v", e.Redacted)
	}
	if e.Properties["salary"] != "secret" {
		t.Errorf("Redact stripped the INPUT's properties: %v", e.Properties)
	}
}

// TestRedact_RepeatedCallsDoNotAlias covers RR-1G0T3F: Redact shallow-copies
// the struct, so the copy initially aliases the original's slice header.
// Building a fresh slice each call is what keeps two redactions of the same
// entity independent.
func TestRedact_RepeatedCallsDoNotAlias(t *testing.T) {
	ctx := context.Background()
	e := seedEntity()

	first := visibility.Redact(ctx, hideRedactor{names: []string{"salary"}}, e)
	second := visibility.Redact(ctx, hideRedactor{names: []string{"notes"}}, e)

	first.Redacted[0] = "mutated"

	if second.Redacted[0] != "notes" {
		t.Fatalf("second read aliased the first: %v", second.Redacted)
	}
}

// TestRedact_AllPropertiesHidden is the fail-closed shape: a redactor that
// hides everything yields empty properties and a full name list.
func TestRedact_AllPropertiesHidden(t *testing.T) {
	ctx := context.Background()
	e := seedEntity()

	got := visibility.Redact(ctx, hideRedactor{names: []string{"title", "salary", "notes"}}, e)

	if len(got.Properties) != 0 {
		t.Errorf("Properties = %v, want empty", got.Properties)
	}
	if want := []string{"notes", "salary", "title"}; !slices.Equal(got.Redacted, want) {
		t.Errorf("Redacted = %v, want %v", got.Redacted, want)
	}
}

// TestRedact_DoesNotLock pins the separation from Inaccessible at the seam
// that populates it: the validator skips locked entities and the data-entry
// write path rejects them, so a gated read must never look locked.
func TestRedact_DoesNotLock(t *testing.T) {
	ctx := context.Background()
	e := seedEntity()

	got := visibility.Redact(ctx, hideRedactor{names: []string{"salary"}}, e)

	if got.IsLocked() {
		t.Fatal("a field-redacted entity reports IsLocked; it would be skipped by the validator")
	}
	if len(got.Inaccessible) != 0 {
		t.Errorf("Redact wrote to Inaccessible: %v", got.Inaccessible)
	}
}

// TestRedact_PreservesInaccessible covers the both-at-once case: a git-crypt
// locked entity that is ALSO field-redacted must keep both markers.
func TestRedact_PreservesInaccessible(t *testing.T) {
	ctx := context.Background()
	e := seedEntity()
	e.Inaccessible = []entity.InaccessibleField{
		{Name: "content", Reason: entity.InaccessibleReasonGitCrypt},
	}

	got := visibility.Redact(ctx, hideRedactor{names: []string{"salary"}}, e)

	if !got.IsLocked() {
		t.Error("genuine git-crypt inaccessibility was lost")
	}
	if !got.IsRedacted("salary") {
		t.Error("ACL redaction was lost")
	}
}

// TestPushdownBranch_MarksRedacted pins the pushdown path specifically.
// ScriptReader.ListEntities has two branches — ACL pushdown (RedactRow) and
// load-then-Filter — and the redaction MARK must match on both, not just the
// stripping. The existing pushdown coverage uses a stub redactor that strips
// without marking, so it cannot catch a divergence here (RR-4DG4KF).
func TestPushdownBranch_MarksRedacted(t *testing.T) {
	ctx := context.Background()
	red := hideRedactor{names: []string{"salary"}}

	// RedactRow is the seam the pushdown calls per yielded row; Redact is
	// what the Filter branch reaches via PolicyReader.redacted. Both must
	// produce the same marker for the same input.
	viaRedact := visibility.Redact(ctx, red, seedEntity())

	pr, err := visibility.NewPolicyReader(visibility.NopGate{}, red, stubGetter{})
	if err != nil {
		t.Fatalf("NewPolicyReader: %v", err)
	}
	viaRow := pr.RedactRow(ctx, seedEntity())

	if !slices.Equal(viaRow.Redacted, viaRedact.Redacted) {
		t.Fatalf("pushdown branch marked %v, Filter branch marked %v — branches diverged",
			viaRow.Redacted, viaRedact.Redacted)
	}
	if _, leaked := viaRow.Properties["salary"]; leaked {
		t.Error("pushdown branch leaked the hidden value")
	}
}

// stubGetter satisfies visibility.EntityGetter for a reader that is only
// used through RedactRow (which performs no load).
type stubGetter struct{}

func (stubGetter) GetEntityState(context.Context, string, entity.Face) (*entity.Entity, error) {
	return nil, errors.New("not used")
}
