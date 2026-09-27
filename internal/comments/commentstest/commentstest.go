// Package commentstest is the conformance suite every [comments.Store]
// implementation must pass.
//
// It exists because the parts of the contract most likely to diverge between
// backends are the ones nobody notices: List's ordering, whether a returned
// slice aliases stored state, and whether two concurrent Adds both survive. A
// backend that gets those subtly wrong still passes a hand-written smoke test
// and then behaves differently in production from the one the tests ran
// against.
package commentstest

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/comments"
	"github.com/Sourcehaven-BV/rela/internal/entity"
)

// Factory builds a fresh, empty store for one subtest.
type Factory func(t *testing.T) comments.Store

var base = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func target(id string) comments.Target {
	return comments.Target{Type: "ticket", ID: id}
}

// comment builds a comment with a fixed anchor, offsetting CreatedAt so
// ordering assertions are unambiguous.
func comment(id string, offset time.Duration, author, body string) comments.Comment {
	return comments.Comment{
		ID:        id,
		Author:    author,
		CreatedAt: base.Add(offset),
		Anchor:    comments.Anchor{Kind: comments.AnchorProperty, Ref: "status"},
		Body:      body,
	}
}

// RunAll runs every conformance suite against f.
func RunAll(t *testing.T, f Factory) {
	t.Helper()
	t.Run("Empty", func(t *testing.T) { RunEmptyTests(t, f) })
	t.Run("Ordering", func(t *testing.T) { RunOrderingTests(t, f) })
	t.Run("Get", func(t *testing.T) { RunGetTests(t, f) })
	t.Run("Update", func(t *testing.T) { RunUpdateTests(t, f) })
	t.Run("Delete", func(t *testing.T) { RunDeleteTests(t, f) })
	t.Run("Isolation", func(t *testing.T) { RunIsolationTests(t, f) })
	t.Run("Rename", func(t *testing.T) { RunRenameTests(t, f) })
	t.Run("Concurrency", func(t *testing.T) { RunConcurrencyTests(t, f) })
	t.Run("RoundTrip", func(t *testing.T) { RunRoundTripTests(t, f) })
	t.Run("Faces", func(t *testing.T) { RunFaceTests(t, f) })
}

// RunGetTests pins the single-comment read (TKT-4LG36M).
//
// Get exists so the database backends can serve an authorization check from
// their primary key instead of reading a whole thread. That makes it a SECOND
// read path onto the same rows, and a second path is exactly where backends
// drift: the risk is not that Get fails outright but that it disagrees with
// List in some corner — a different zone on a timestamp, a face it resolves
// through that List would not.
func RunGetTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("Fidelity", func(t *testing.T) { runGetFidelityTests(t, f) })

	t.Run("absent id is ErrNotFound", func(t *testing.T) {
		s := f(t)
		tgt := target("TKT-1")
		require.NoError(t, s.Add(ctx, tgt, comment("c1", 0, "alice", "present")))

		_, err := s.Get(ctx, tgt, "nope")
		require.ErrorIs(t, err, comments.ErrNotFound)
	})

	t.Run("a target with no thread at all is ErrNotFound", func(t *testing.T) {
		s := f(t)
		_, err := s.Get(ctx, target("TKT-never-commented"), "c1")
		require.ErrorIs(t, err, comments.ErrNotFound,
			"an absent thread must report a missing comment, not an infrastructure error")
	})

	t.Run("Scoping", func(t *testing.T) { runGetScopingTests(t, f) })

	t.Run("a deleted comment is gone", func(t *testing.T) {
		s := f(t)
		tgt := target("TKT-1")
		require.NoError(t, s.Add(ctx, tgt, comment("c1", 0, "alice", "doomed")))
		require.NoError(t, s.Delete(ctx, tgt, "c1"))

		_, err := s.Get(ctx, tgt, "c1")
		require.ErrorIs(t, err, comments.ErrNotFound)
	})
}

// runGetFidelityTests pins that Get decodes a comment exactly as List does.
//
// Get is a second decoder over the same columns, so the failure to guard against
// is not that it errors but that it quietly disagrees — a timestamp in the
// server's local zone rather than UTC, or an optional column read differently
// through its NULL path. Either ships two shapes of one comment to the same
// client depending on which route the request took.
func runGetFidelityTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("returns the stored comment", func(t *testing.T) {
		s := f(t)
		tgt := target("TKT-1")
		want := comments.Comment{
			ID:        "c1",
			Author:    "alice@example.com",
			CreatedAt: base,
			Anchor:    comments.Anchor{Kind: comments.AnchorSection, Ref: "acceptance-criteria"},
			Body:      "A body with **markdown**, a newline\nand a tab\there.",
			Resolved:  true,
		}
		require.NoError(t, s.Add(ctx, tgt, want))

		got, err := s.Get(ctx, tgt, "c1")
		require.NoError(t, err)
		require.Equal(t, want.ID, got.ID)
		require.Equal(t, want.Author, got.Author)
		require.True(t, want.CreatedAt.Equal(got.CreatedAt), "CreatedAt must round-trip")
		// Same reasoning as RunRoundTripTests: Equal compares instants and
		// ignores location, so without this a backend could return the right
		// moment in the server's local zone and serialize a different offset to
		// clients than List does for the very same comment.
		require.Equal(t, time.UTC, got.CreatedAt.Location(),
			"Get must return UTC, exactly as List does")
		require.Equal(t, want.Anchor, got.Anchor)
		require.Equal(t, want.Body, got.Body)
		require.Equal(t, want.Resolved, got.Resolved)
	})

	t.Run("agrees with List on every comment in a thread", func(t *testing.T) {
		s := f(t)
		tgt := target("TKT-1")
		require.NoError(t, s.Add(ctx, tgt, comment("c1", time.Hour, "alice", "first")))
		require.NoError(t, s.Add(ctx, tgt, comment("c2", 2*time.Hour, "bob", "second")))

		listed, err := s.List(ctx, tgt)
		require.NoError(t, err)
		require.Len(t, listed, 2)

		for _, want := range listed {
			got, err := s.Get(ctx, tgt, want.ID)
			require.NoError(t, err, "Get must find every comment List returns")
			require.Equal(t, want, got, "Get and List must agree on %q", want.ID)
		}
	})

	// UpdatedAt is the column most likely to diverge and the least likely to be
	// noticed: it is optional, so it travels a NULL path the others do not, and
	// each backend decodes it separately (a *time.Time on postgres, a *string
	// parsed from text on sqlite). Its absence is also meaningful — `omitzero`
	// makes a zero value mean "never edited" to every API client.
	t.Run("an unedited comment has a zero UpdatedAt", func(t *testing.T) {
		s := f(t)
		tgt := target("TKT-1")
		require.NoError(t, s.Add(ctx, tgt, comment("c1", 0, "alice", "never edited")))

		got, err := s.Get(ctx, tgt, "c1")
		require.NoError(t, err)
		require.True(t, got.UpdatedAt.IsZero(),
			"a comment nobody edited must report no edit time, not the zero date or the epoch")
	})

	t.Run("reflects an update", func(t *testing.T) {
		s := f(t)
		tgt := target("TKT-1")
		require.NoError(t, s.Add(ctx, tgt, comment("c1", 0, "alice", "before")))
		require.NoError(t, s.Update(ctx, tgt, "c1", "after", true))

		got, err := s.Get(ctx, tgt, "c1")
		require.NoError(t, err)
		require.Equal(t, "after", got.Body)
		require.True(t, got.Resolved)

		// Whether an edit STAMPS UpdatedAt is not asserted here: the database
		// backends set it and the file/memory ones do not (TKT-JZY2PM). What
		// this suite does pin is that however a backend answers that question,
		// its two read paths answer it identically — which is the risk a second
		// decoder introduces, and the only part of it that Get owns.
		listed, err := s.List(ctx, tgt)
		require.NoError(t, err)
		require.Len(t, listed, 1)
		require.Equal(t, listed[0], got, "Get and List must agree after an update")
		if !got.UpdatedAt.IsZero() {
			require.Equal(t, time.UTC, got.UpdatedAt.Location(),
				"a stamped UpdatedAt must come back in UTC, exactly as CreatedAt does")
		}
	})
}

// runGetScopingTests pins that Get resolves an ID only within the target it was
// asked about.
//
// Split out because these cases carry the security weight of the method.
// Get's result decides whether an *-own permission covers a mutation, so an ID
// that resolved outside the named target would have a request's permission
// decided against a record it never asked for — and, because the caller then
// mutates by the same (target, id) pair, the check and the write would be
// looking at different rows.
func runGetScopingTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("does not resolve across targets", func(t *testing.T) {
		s := f(t)
		require.NoError(t, s.Add(ctx, target("TKT-1"), comment("c1", 0, "alice", "on one")))

		_, err := s.Get(ctx, target("TKT-2"), "c1")
		require.ErrorIs(t, err, comments.ErrNotFound,
			"a comment id must not be reachable from another entity's target")
	})

	t.Run("does not resolve across faces", func(t *testing.T) {
		s := f(t)
		def := comments.Target{Type: "ticket", ID: "TKT-1"}
		draft := comments.Target{Type: "ticket", ID: "TKT-1", Face: "draft"}
		require.NoError(t, s.Add(ctx, draft, comment("c1", 0, "alice", "on the draft")))

		_, err := s.Get(ctx, def, "c1")
		require.ErrorIs(t, err, comments.ErrNotFound,
			"a comment on the draft face must not be reachable through the default face")

		got, err := s.Get(ctx, draft, "c1")
		require.NoError(t, err)
		require.Equal(t, "on the draft", got.Body)
	})

	// The reverse of the case above, and NOT redundant with it. Target.Key() is
	// entity.FormatStateRef, so the default face serializes to the bare id and a
	// named face to "id@face" — meaning "TKT-1" is a PREFIX of "TKT-1@draft" but
	// not the other way round. A backend matching by prefix rather than equality
	// fails in one direction only, so testing one direction tests half the bug.
	t.Run("the default face is not reachable through a named one", func(t *testing.T) {
		s := f(t)
		def := comments.Target{Type: "ticket", ID: "TKT-1"}
		draft := comments.Target{Type: "ticket", ID: "TKT-1", Face: "draft"}
		require.NoError(t, s.Add(ctx, def, comment("c1", 0, "alice", "on the default")))

		_, err := s.Get(ctx, draft, "c1")
		require.ErrorIs(t, err, comments.ErrNotFound,
			"a comment on the default face must not be reachable through the draft face")
	})

	// This is the case with the real security weight. Get's result decides
	// whether an *-own permission covers a mutation, so it is not enough that a
	// lookup finds A row — it must find THE row. With one comment per store the
	// assertions above are satisfied by any backend that merely scopes SOMEHOW;
	// two comments sharing an id across two faces is what distinguishes
	// "returns the right record" from "returns a plausible one".
	t.Run("one id on two faces returns each face's own comment", func(t *testing.T) {
		s := f(t)
		def := comments.Target{Type: "ticket", ID: "TKT-1"}
		draft := comments.Target{Type: "ticket", ID: "TKT-1", Face: "draft"}
		require.NoError(t, s.Add(ctx, def, comment("c1", 0, "alice", "on the default")))
		require.NoError(t, s.Add(ctx, draft, comment("c1", 0, "bob", "on the draft")))

		got, err := s.Get(ctx, def, "c1")
		require.NoError(t, err)
		require.Equal(t, "on the default", got.Body, "the default face must return its own comment")
		require.Equal(t, "alice", got.Author, "the author decides an *-own permission; it must be this face's")

		got, err = s.Get(ctx, draft, "c1")
		require.NoError(t, err)
		require.Equal(t, "on the draft", got.Body, "the draft face must return its own comment")
		require.Equal(t, "bob", got.Author)
	})

	// The same property across two entities rather than two faces.
	t.Run("one id on two targets returns each target's own comment", func(t *testing.T) {
		s := f(t)
		require.NoError(t, s.Add(ctx, target("TKT-1"), comment("c1", 0, "alice", "on one")))
		require.NoError(t, s.Add(ctx, target("TKT-2"), comment("c1", 0, "bob", "on two")))

		got, err := s.Get(ctx, target("TKT-1"), "c1")
		require.NoError(t, err)
		require.Equal(t, "on one", got.Body)
		require.Equal(t, "alice", got.Author)

		got, err = s.Get(ctx, target("TKT-2"), "c1")
		require.NoError(t, err)
		require.Equal(t, "on two", got.Body)
		require.Equal(t, "bob", got.Author)
	})
}

// RunFaceTests pins the per-face contract (FEAT-9CD2MX).
//
// Comments are scoped to a content state: a remark on the draft is not a remark
// on the published version, and the read gate is per face too. The key is
// entity.FormatStateRef, so the DEFAULT face keeps the bare id — which is what
// lets faces arrive without migrating a single stored comment.
func RunFaceTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	faced := func(id string, face entity.Face) comments.Target {
		return comments.Target{Type: "ticket", ID: id, Face: face}
	}

	t.Run("faces of one entity are independent threads", func(t *testing.T) {
		s := f(t)
		require.NoError(t, s.Add(ctx, faced("TKT-1", ""), comment("def", 0, "alice", "on default")))
		require.NoError(t, s.Add(ctx, faced("TKT-1", "draft"), comment("dft", 0, "bob", "on draft")))

		got, err := s.List(ctx, faced("TKT-1", ""))
		require.NoError(t, err)
		require.Equal(t, []string{"def"}, ids(got), "the default face must not see the draft's thread")

		got, err = s.List(ctx, faced("TKT-1", "draft"))
		require.NoError(t, err)
		require.Equal(t, []string{"dft"}, ids(got))
	})

	t.Run("the default face keeps the bare id, so stored threads need no migration", func(t *testing.T) {
		s := f(t)
		// Written WITHOUT a face, as every pre-faces comment was.
		require.NoError(t, s.Add(ctx, target("TKT-1"), comment("old", 0, "alice", "before faces")))

		// Read back through an explicit default face: same thread.
		got, err := s.List(ctx, faced("TKT-1", ""))
		require.NoError(t, err)
		require.Equal(t, []string{"old"}, ids(got))
	})

	t.Run("DeleteTarget removes one face, not the others", func(t *testing.T) {
		s := f(t)
		require.NoError(t, s.Add(ctx, faced("TKT-1", ""), comment("def", 0, "alice", "one")))
		require.NoError(t, s.Add(ctx, faced("TKT-1", "draft"), comment("dft", 0, "bob", "two")))

		require.NoError(t, s.DeleteTarget(ctx, faced("TKT-1", "draft")))

		got, err := s.List(ctx, faced("TKT-1", ""))
		require.NoError(t, err)
		require.Equal(t, []string{"def"}, ids(got), "deleting one face must leave the others")

		got, err = s.List(ctx, faced("TKT-1", "draft"))
		require.NoError(t, err)
		require.Empty(t, got)
	})

	t.Run("DeleteAllFaces removes every face's thread", func(t *testing.T) {
		s := f(t)
		require.NoError(t, s.Add(ctx, faced("TKT-1", ""), comment("def", 0, "alice", "one")))
		require.NoError(t, s.Add(ctx, faced("TKT-1", "draft"), comment("dft", 0, "bob", "two")))
		require.NoError(t, s.Add(ctx, faced("TKT-2", "draft"), comment("other", 0, "eve", "other")))

		require.NoError(t, s.DeleteAllFaces(ctx, "TKT-1"))

		for _, face := range []entity.Face{"", "draft"} {
			got, err := s.List(ctx, faced("TKT-1", face))
			require.NoError(t, err)
			require.Empty(t, got, "face %q should be gone", face)
		}
		// A different entity is untouched, including one sharing a face name.
		got, err := s.List(ctx, faced("TKT-2", "draft"))
		require.NoError(t, err)
		require.Equal(t, []string{"other"}, ids(got))
	})

	t.Run("Rename moves every face", func(t *testing.T) {
		s := f(t)
		require.NoError(t, s.Add(ctx, faced("TKT-1", ""), comment("def", 0, "alice", "one")))
		require.NoError(t, s.Add(ctx, faced("TKT-1", "draft"), comment("dft", 0, "bob", "two")))

		require.NoError(t, s.Rename(ctx, "TKT-1", "TKT-9"))

		// Renaming only the bare id would strand the draft thread at an id
		// that no longer resolves.
		got, err := s.List(ctx, faced("TKT-9", ""))
		require.NoError(t, err)
		require.Equal(t, []string{"def"}, ids(got))

		got, err = s.List(ctx, faced("TKT-9", "draft"))
		require.NoError(t, err)
		require.Equal(t, []string{"dft"}, ids(got), "the draft face must move with the rename")

		got, err = s.List(ctx, faced("TKT-1", "draft"))
		require.NoError(t, err)
		require.Empty(t, got, "nothing may remain at the old id")
	})
}

// RunEmptyTests pins the empty-target contract.
func RunEmptyTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("unknown target lists empty, not error", func(t *testing.T) {
		s := f(t)
		got, err := s.List(ctx, target("TKT-none"))
		require.NoError(t, err)
		require.Empty(t, got)
	})

	t.Run("empty slice not nil", func(t *testing.T) {
		// Callers marshal this straight to JSON, where nil becomes `null` and
		// an empty slice becomes `[]`. A client distinguishing the two would
		// see different shapes from different backends.
		s := f(t)
		got, err := s.List(ctx, target("TKT-none"))
		require.NoError(t, err)
		require.NotNil(t, got)
	})

	t.Run("DeleteTarget on empty target is not an error", func(t *testing.T) {
		s := f(t)
		require.NoError(t, s.DeleteTarget(ctx, target("TKT-none")))
	})
}

// RunOrderingTests pins the ordering contract: oldest first, ID breaking ties.
func RunOrderingTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("oldest first regardless of insertion order", func(t *testing.T) {
		s := f(t)
		tgt := target("TKT-1")
		// Inserted newest-first so storage order and contract order differ; a
		// backend returning insertion order fails here.
		require.NoError(t, s.Add(ctx, tgt, comment("c3", 2*time.Hour, "alice", "third")))
		require.NoError(t, s.Add(ctx, tgt, comment("c1", 0, "alice", "first")))
		require.NoError(t, s.Add(ctx, tgt, comment("c2", time.Hour, "bob", "second")))

		got, err := s.List(ctx, tgt)
		require.NoError(t, err)
		require.Len(t, got, 3)
		require.Equal(t, []string{"c1", "c2", "c3"}, ids(got))
	})

	t.Run("identical timestamps break ties by ID", func(t *testing.T) {
		// A coarse clock can stamp two comments in one tick; without the
		// tie-break the thread would reorder between reads.
		s := f(t)
		tgt := target("TKT-1")
		require.NoError(t, s.Add(ctx, tgt, comment("zzz", 0, "alice", "z")))
		require.NoError(t, s.Add(ctx, tgt, comment("aaa", 0, "alice", "a")))

		got, err := s.List(ctx, tgt)
		require.NoError(t, err)
		require.Equal(t, []string{"aaa", "zzz"}, ids(got))
	})
}

// runSetResolvedTests pins the conditional resolve flip an accepted
// suggestion claims its comment with.
func runSetResolvedTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("SetResolved flips only a differing flag", func(t *testing.T) {
		s := f(t)
		tgt := target("TKT-1")
		require.NoError(t, s.Add(ctx, tgt, comment("c1", 0, "alice", "body")))

		changed, err := s.SetResolved(ctx, tgt, "c1", true)
		require.NoError(t, err)
		require.True(t, changed)

		// The second claim loses: this is what stops a suggestion being
		// accepted twice.
		changed, err = s.SetResolved(ctx, tgt, "c1", true)
		require.NoError(t, err)
		require.False(t, changed)

		got, err := s.Get(ctx, tgt, "c1")
		require.NoError(t, err)
		require.True(t, got.Resolved)
		require.Equal(t, "body", got.Body, "SetResolved must not touch the body")

		changed, err = s.SetResolved(ctx, tgt, "c1", false)
		require.NoError(t, err)
		require.True(t, changed)
	})

	t.Run("SetResolved on an absent comment is ErrNotFound", func(t *testing.T) {
		s := f(t)
		_, err := s.SetResolved(ctx, target("TKT-1"), "nope", true)
		require.ErrorIs(t, err, comments.ErrNotFound)
	})

	t.Run("SetResolved concurrently has exactly one winner", func(t *testing.T) {
		s := f(t)
		tgt := target("TKT-1")
		require.NoError(t, s.Add(ctx, tgt, comment("c1", 0, "alice", "body")))

		const racers = 8
		var wg sync.WaitGroup
		wins := make(chan bool, racers)
		for range racers {
			wg.Go(func() {
				changed, err := s.SetResolved(ctx, tgt, "c1", true)
				assert.NoError(t, err)
				wins <- changed
			})
		}
		wg.Wait()
		close(wins)
		n := 0
		for w := range wins {
			if w {
				n++
			}
		}
		require.Equal(t, 1, n)
	})
}

// RunUpdateTests pins which fields an update may change.
func RunUpdateTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	runSetResolvedTests(t, f)

	t.Run("updates body and resolved", func(t *testing.T) {
		s := f(t)
		tgt := target("TKT-1")
		require.NoError(t, s.Add(ctx, tgt, comment("c1", 0, "alice", "before")))

		require.NoError(t, s.Update(ctx, tgt, "c1", "after", true))

		got, err := s.List(ctx, tgt)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, "after", got[0].Body)
		require.True(t, got[0].Resolved)
	})

	t.Run("does not change author, created_at or anchor", func(t *testing.T) {
		// An edit must not be able to rewrite who said something or what it
		// was about — otherwise "edit your own comment" becomes a way to
		// reattribute someone else's.
		s := f(t)
		tgt := target("TKT-1")
		orig := comment("c1", 0, "alice", "before")
		require.NoError(t, s.Add(ctx, tgt, orig))

		require.NoError(t, s.Update(ctx, tgt, "c1", "after", false))

		got, err := s.List(ctx, tgt)
		require.NoError(t, err)
		require.Equal(t, orig.Author, got[0].Author)
		require.True(t, orig.CreatedAt.Equal(got[0].CreatedAt))
		require.Equal(t, orig.Anchor, got[0].Anchor)
	})

	t.Run("does not change a suggested replacement", func(t *testing.T) {
		// A suggestion is reviewed as a diff; if an edit could swap it, what
		// gets accepted would not be what was reviewed.
		s := f(t)
		tgt := target("TKT-1")
		orig := comment("c1", 0, "alice", "before")
		orig.Anchor = suggestionAnchor("proposed text")
		require.NoError(t, s.Add(ctx, tgt, orig))

		require.NoError(t, s.Update(ctx, tgt, "c1", "after", true))

		got, err := s.Get(ctx, tgt, "c1")
		require.NoError(t, err)
		require.Equal(t, orig.Anchor, got.Anchor)
	})

	t.Run("missing comment reports ErrNotFound", func(t *testing.T) {
		s := f(t)
		err := s.Update(ctx, target("TKT-1"), "nope", "x", false)
		require.ErrorIs(t, err, comments.ErrNotFound)
	})
}

// RunDeleteTests pins deletion.
func RunDeleteTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("removes only the named comment", func(t *testing.T) {
		s := f(t)
		tgt := target("TKT-1")
		require.NoError(t, s.Add(ctx, tgt, comment("c1", 0, "alice", "one")))
		require.NoError(t, s.Add(ctx, tgt, comment("c2", time.Hour, "bob", "two")))

		require.NoError(t, s.Delete(ctx, tgt, "c1"))

		got, err := s.List(ctx, tgt)
		require.NoError(t, err)
		require.Equal(t, []string{"c2"}, ids(got))
	})

	t.Run("missing comment reports ErrNotFound", func(t *testing.T) {
		s := f(t)
		err := s.Delete(ctx, target("TKT-1"), "nope")
		require.ErrorIs(t, err, comments.ErrNotFound)
	})

	t.Run("DeleteTarget removes the whole thread", func(t *testing.T) {
		s := f(t)
		tgt := target("TKT-1")
		require.NoError(t, s.Add(ctx, tgt, comment("c1", 0, "alice", "one")))
		require.NoError(t, s.Add(ctx, tgt, comment("c2", time.Hour, "bob", "two")))

		require.NoError(t, s.DeleteTarget(ctx, tgt))

		got, err := s.List(ctx, tgt)
		require.NoError(t, err)
		require.Empty(t, got)
	})
}

// RunIsolationTests pins that targets do not bleed into each other.
func RunIsolationTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("targets are independent", func(t *testing.T) {
		s := f(t)
		require.NoError(t, s.Add(ctx, target("TKT-1"), comment("c1", 0, "alice", "one")))
		require.NoError(t, s.Add(ctx, target("TKT-2"), comment("c2", 0, "bob", "two")))

		got1, err := s.List(ctx, target("TKT-1"))
		require.NoError(t, err)
		require.Equal(t, []string{"c1"}, ids(got1))

		got2, err := s.List(ctx, target("TKT-2"))
		require.NoError(t, err)
		require.Equal(t, []string{"c2"}, ids(got2))
	})

	t.Run("DeleteTarget leaves other targets alone", func(t *testing.T) {
		s := f(t)
		require.NoError(t, s.Add(ctx, target("TKT-1"), comment("c1", 0, "alice", "one")))
		require.NoError(t, s.Add(ctx, target("TKT-2"), comment("c2", 0, "bob", "two")))

		require.NoError(t, s.DeleteTarget(ctx, target("TKT-1")))

		got, err := s.List(ctx, target("TKT-2"))
		require.NoError(t, err)
		require.Equal(t, []string{"c2"}, ids(got))
	})

	t.Run("returned slice does not alias stored state", func(t *testing.T) {
		// A backend handing out its backing array lets a caller mutate stored
		// comments in place — impossible for a persistent backend, so tests
		// written against such a store would not transfer.
		s := f(t)
		tgt := target("TKT-1")
		require.NoError(t, s.Add(ctx, tgt, comment("c1", 0, "alice", "original")))

		first, err := s.List(ctx, tgt)
		require.NoError(t, err)
		first[0].Body = "mutated"

		second, err := s.List(ctx, tgt)
		require.NoError(t, err)
		require.Equal(t, "original", second[0].Body)
	})
}

// RunRenameTests pins the re-key path.
//
// This is the behavior that keeps comments reachable across an entity rename:
// the store emits exactly one rename callback, so if this is wrong the comments
// remain on disk under an ID nothing resolves to.
func RunRenameTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("moves comments to the new id", func(t *testing.T) {
		s := f(t)
		require.NoError(t, s.Add(ctx, target("TKT-old"), comment("c1", 0, "alice", "one")))

		require.NoError(t, s.Rename(ctx, "TKT-old", "TKT-new"))

		moved, err := s.List(ctx, target("TKT-new"))
		require.NoError(t, err)
		require.Equal(t, []string{"c1"}, ids(moved))

		old, err := s.List(ctx, target("TKT-old"))
		require.NoError(t, err)
		require.Empty(t, old)
	})

	t.Run("preserves order across the rename", func(t *testing.T) {
		s := f(t)
		tgt := target("TKT-old")
		require.NoError(t, s.Add(ctx, tgt, comment("c1", 0, "alice", "one")))
		require.NoError(t, s.Add(ctx, tgt, comment("c2", time.Hour, "bob", "two")))

		require.NoError(t, s.Rename(ctx, "TKT-old", "TKT-new"))

		got, err := s.List(ctx, target("TKT-new"))
		require.NoError(t, err)
		require.Equal(t, []string{"c1", "c2"}, ids(got))
	})

	t.Run("renaming a target with no comments is not an error", func(t *testing.T) {
		s := f(t)
		require.NoError(t, s.Rename(ctx, "TKT-none", "TKT-new"))
	})

	t.Run("collides on a shared comment id without stranding the thread",
		func(t *testing.T) {
			// A destination holding a comment with the SAME id as one arriving.
			// Vanishingly unlikely (ids carry 80 bits of crypto/rand) but
			// reachable through a restored backup, a re-import, or a
			// filecomments migration — and the consequence is out of all
			// proportion to the odds, because entitymanager LOGS a failure from
			// EntityRenamed rather than returning it. A backend that aborts here
			// leaves the whole thread under a key nothing resolves to, with no
			// error reaching the user and no route left to the rows.
			//
			// The contract is: the destination's existing comment wins (a
			// reader may already have seen it), everything else still moves,
			// and nothing is stranded at the old key.
			s := f(t)
			require.NoError(t, s.Add(ctx, target("TKT-old"), comment("dup", 0, "alice", "from old")))
			require.NoError(t, s.Add(ctx, target("TKT-old"), comment("c1", time.Hour, "alice", "also from old")))
			require.NoError(t, s.Add(ctx, target("TKT-new"), comment("dup", 2*time.Hour, "bob", "already here")))

			require.NoError(t, s.Rename(ctx, "TKT-old", "TKT-new"))

			old, err := s.List(ctx, target("TKT-old"))
			require.NoError(t, err)
			require.Empty(t, old, "nothing may be left stranded at the old key")

			// Ordered by created_at, so the moved c1 (+1h) precedes the
			// surviving occupant (+2h).
			got, err := s.List(ctx, target("TKT-new"))
			require.NoError(t, err)
			require.Equal(t, []string{"c1", "dup"}, ids(got))
			require.Equal(t, "already here", got[1].Body,
				"the destination's comment wins on a collision")
		})
}

// RunKeyFidelityTests pins that a backend treats an entity id as EXACT BYTES:
// case is significant, and a multi-byte id re-keys correctly.
//
// Separate from [RunAll] because neither is a contract every backend can
// honor. filecomments keys on a filename, so on a case-insensitive filesystem
// (macOS, Windows) "TKT-1@draft.yaml" and "tkt-1@draft.yaml" ARE one file, and
// it refuses a non-ASCII id outright rather than build an unsafe path. Those
// are sound decisions for a file backend, not defects — but they mean the
// guarantee below can only be asked of the database backends.
//
// For those two it is a real hazard rather than a hypothetical. SQLite's LIKE
// is ASCII case-INSENSITIVE by default while "=" is byte-exact, so the two arms
// of one query silently match different row sets — and a different set again
// from PostgreSQL's COLLATE "C". Separately, both compute the moved key with
// SQL's substring()/substr(), which count CHARACTERS where Go's len() counts
// BYTES; slicing at the wrong offset re-keys a thread to a corrupt target and
// returns nil.
//
// Whether two ids differing only by case can coexist at all, and whether an id
// may hold a multi-byte rune, are questions for the store and for
// entity.ValidateID — answered elsewhere, by invariants these packages
// deliberately do not import. This fixes the answer the BACKENDS give, so it
// holds however those questions are later answered.
func RunKeyFidelityTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	upper := comments.Target{Type: "ticket", ID: "TKT-1", Face: "draft"}
	lower := comments.Target{Type: "ticket", ID: "tkt-1", Face: "draft"}
	seed := func(t *testing.T) comments.Store {
		t.Helper()
		s := f(t)
		require.NoError(t, s.Add(ctx, upper, comment("upper", 0, "alice", "on TKT-1")))
		require.NoError(t, s.Add(ctx, lower, comment("lower", time.Hour, "bob", "on tkt-1")))
		return s
	}

	t.Run("Get does not resolve a target differing only by case", func(t *testing.T) {
		// Get matches both key halves with "=", which is byte-exact in SQLite
		// where LIKE is not. That is a property of the SQL each backend wrote,
		// so it is asserted rather than reasoned about — the same reason the
		// rename and delete cases below exist.
		s := seed(t)

		got, err := s.Get(ctx, upper, "upper")
		require.NoError(t, err)
		require.Equal(t, "on TKT-1", got.Body)

		_, err = s.Get(ctx, lower, "upper")
		require.ErrorIs(t, err, comments.ErrNotFound,
			"tkt-1 must not resolve a comment stored on TKT-1")

		_, err = s.Get(ctx, upper, "lower")
		require.ErrorIs(t, err, comments.ErrNotFound,
			"TKT-1 must not resolve a comment stored on tkt-1")
	})

	t.Run("rename does not re-key a target differing only by case", func(t *testing.T) {
		s := seed(t)

		require.NoError(t, s.Rename(ctx, "tkt-1", "OTHER-9"))

		kept, err := s.List(ctx, upper)
		require.NoError(t, err)
		require.Equal(t, []string{"upper"}, ids(kept),
			"renaming tkt-1 must leave TKT-1's thread where it was")

		moved, err := s.List(ctx, comments.Target{Type: "ticket", ID: "OTHER-9", Face: "draft"})
		require.NoError(t, err)
		require.Equal(t, []string{"lower"}, ids(moved))
	})

	t.Run("moves a faced thread whose id is multi-byte", func(t *testing.T) {
		// The database backends compute the new key as "newID + everything
		// after the old id", and SQL's substring()/substr() count CHARACTERS
		// while Go's len() counts BYTES. Slicing at a byte offset would drop
		// the "@" and re-key the thread to a corrupt target — losing it
		// outright, and returning nil.
		//
		// entity.ValidateID admits only ASCII today, so the two units agree by
		// luck rather than construction. This test is what keeps them agreeing
		// if that ever changes: a backend must not be correct only by the grace
		// of a grammar declared in a package it does not import.
		s := f(t)
		faced := comments.Target{Type: "ticket", ID: "TKT-é", Face: "draft"}
		require.NoError(t, s.Add(ctx, faced, comment("c1", 0, "alice", "one")))

		require.NoError(t, s.Rename(ctx, "TKT-é", "TKT-9"))

		moved, err := s.List(ctx, comments.Target{Type: "ticket", ID: "TKT-9", Face: "draft"})
		require.NoError(t, err)
		require.Equal(t, []string{"c1"}, ids(moved),
			"a multi-byte id must re-key to id@face, not to a corrupt key")
	})

	t.Run("DeleteAllFaces does not reach a target differing only by case", func(t *testing.T) {
		// Same OR-of-two-matching-rules shape as Rename, so it needs the same
		// guarantee: a delete must not reach an entity the caller did not name.
		s := seed(t)

		require.NoError(t, s.DeleteAllFaces(ctx, "tkt-1"))

		kept, err := s.List(ctx, upper)
		require.NoError(t, err)
		require.Equal(t, []string{"upper"}, ids(kept),
			"deleting tkt-1's faces must not touch TKT-1")

		gone, err := s.List(ctx, lower)
		require.NoError(t, err)
		require.Empty(t, gone)
	})

	t.Run("merges into an occupied destination", func(t *testing.T) {
		// rela permits ID reuse, so the destination is not guaranteed empty.
		// Merging is the conservative choice: discarding the occupant's
		// comments would destroy data the operator never asked to remove.
		s := f(t)
		require.NoError(t, s.Add(ctx, target("TKT-old"), comment("c1", 0, "alice", "one")))
		require.NoError(t, s.Add(ctx, target("TKT-new"), comment("c2", time.Hour, "bob", "two")))

		require.NoError(t, s.Rename(ctx, "TKT-old", "TKT-new"))

		got, err := s.List(ctx, target("TKT-new"))
		require.NoError(t, err)
		require.Equal(t, []string{"c1", "c2"}, ids(got))
	})
}

// RunConcurrencyTests pins that concurrent writes to one target do not lose
// updates.
//
// This is the read-modify-write hazard: a backend that reads a thread, appends,
// and writes it back without serializing will drop comments under load — and
// will pass every sequential test.
func RunConcurrencyTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("concurrent adds to one target all survive", func(t *testing.T) {
		s := f(t)
		tgt := target("TKT-1")

		const n = 16
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i := range n {
			wg.Go(func() {
				c := comment(fmt.Sprintf("c%02d", i), time.Duration(i)*time.Minute, "alice", "body")
				errs[i] = s.Add(ctx, tgt, c)
			})
		}
		wg.Wait()
		for _, err := range errs {
			require.NoError(t, err)
		}

		got, err := s.List(ctx, tgt)
		require.NoError(t, err)
		require.Len(t, got, n, "every concurrent add must survive")
	})

	t.Run("concurrent adds across targets all survive", func(t *testing.T) {
		s := f(t)

		const n = 16
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				tgt := target(fmt.Sprintf("TKT-%02d", i))
				_ = s.Add(ctx, tgt, comment("c1", 0, "alice", "body"))
			})
		}
		wg.Wait()

		for i := range n {
			got, err := s.List(ctx, target(fmt.Sprintf("TKT-%02d", i)))
			require.NoError(t, err)
			require.Len(t, got, 1)
		}
	})
}

// RunRoundTripTests pins that every field survives storage unchanged.
func RunRoundTripTests(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("all fields round-trip", func(t *testing.T) {
		s := f(t)
		tgt := target("TKT-1")
		want := comments.Comment{
			ID:        "c1",
			Author:    "alice@example.com",
			CreatedAt: base,
			Anchor:    comments.Anchor{Kind: comments.AnchorSection, Ref: "acceptance-criteria"},
			Body:      "A body with **markdown**, a newline\nand a tab\there.",
			Resolved:  true,
		}
		require.NoError(t, s.Add(ctx, tgt, want))

		got, err := s.List(ctx, tgt)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, want.ID, got[0].ID)
		require.Equal(t, want.Author, got[0].Author)
		require.True(t, want.CreatedAt.Equal(got[0].CreatedAt), "CreatedAt must round-trip")
		// Equal compares INSTANTS and ignores location, so it would pass while
		// two backends returned the same moment in different zones. Comment
		// marshals to JSON with whatever offset it carries, so that difference
		// reaches clients: "Z" from one backend, "+01:00" from another, shifting
		// under the server's DST. UTC is the one answer all backends give.
		require.Equal(t, time.UTC, got[0].CreatedAt.Location(),
			"CreatedAt must come back in UTC, not the server's local zone")
		require.Equal(t, want.Anchor, got[0].Anchor)
		require.Equal(t, want.Body, got[0].Body)
		require.Equal(t, want.Resolved, got[0].Resolved)
	})

	t.Run("suggested replacement round-trips", func(t *testing.T) {
		// The empty string is the load-bearing case: it means "delete the
		// quoted text", so a backend that folded it into "no suggestion"
		// (omitempty on the dereferenced value) would silently turn a deletion
		// into a plain comment.
		for _, repl := range []string{"a better phrase\nwith a newline", ""} {
			s := f(t)
			tgt := target("TKT-1")
			want := comment("c1", 0, "alice", "suggest")
			want.Anchor = suggestionAnchor(repl)
			require.NoError(t, s.Add(ctx, tgt, want))

			got, err := s.Get(ctx, tgt, "c1")
			require.NoError(t, err)
			require.Equal(t, want.Anchor, got.Anchor)
			require.NotNil(t, got.Anchor.Replacement, "replacement %q must not read back as absent", repl)
		}
	})

	t.Run("returned anchor shares nothing with stored state", func(t *testing.T) {
		// Anchor holds pointers. An in-memory backend handing out shallow
		// copies would let a caller rewrite a stored suggestion in place.
		s := f(t)
		tgt := target("TKT-1")
		c := comment("c1", 0, "alice", "suggest")
		c.Anchor = suggestionAnchor("original")
		require.NoError(t, s.Add(ctx, tgt, c))
		*c.Anchor.Replacement = "mutated after add"

		got, err := s.Get(ctx, tgt, "c1")
		require.NoError(t, err)
		*got.Anchor.Replacement = "mutated after get"
		got.Anchor.Text.Quote = "mutated quote"

		list, err := s.List(ctx, tgt)
		require.NoError(t, err)
		require.Equal(t, "original", *list[0].Anchor.Replacement)
		require.Equal(t, "the quoted text", list[0].Anchor.Text.Quote)
	})

	t.Run("unicode body round-trips", func(t *testing.T) {
		s := f(t)
		tgt := target("TKT-1")
		body := "emoji 🎉, RTL \u202bمرحبا\u202c, CJK 日本語, quote \"x\" and 'y'"
		require.NoError(t, s.Add(ctx, tgt, comment("c1", 0, "alice", body)))

		got, err := s.List(ctx, tgt)
		require.NoError(t, err)
		require.Equal(t, body, got[0].Body)
	})
}

// suggestionAnchor is a text anchor carrying a replacement.
func suggestionAnchor(repl string) comments.Anchor {
	return comments.Anchor{
		Kind: comments.AnchorText,
		Text: &comments.TextAnchor{
			Quote:          "the quoted text",
			Prefix:         "before ",
			Suffix:         " after",
			ParagraphIndex: 0,
		},
		Replacement: &repl,
	}
}

func ids(list []comments.Comment) []string {
	out := make([]string, len(list))
	for i, c := range list {
		out[i] = c.ID
	}
	return out
}
