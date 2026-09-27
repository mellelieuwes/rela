package comments_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/comments"
	"github.com/Sourcehaven-BV/rela/internal/markdown"
)

// suggestion builds a text anchor over quote in doc carrying repl.
func suggestion(t *testing.T, doc, quote string, repl *string) comments.Anchor {
	t.Helper()
	return comments.Anchor{Kind: comments.AnchorText, Text: anchorFor(t, doc, quote), Replacement: repl}
}

func TestAnchorValidate_Replacement(t *testing.T) {
	doc := "First paragraph with some words.\n\n- item one here\n- item two here\n\nPlain **bold** text.\n"
	textAnchor := func(quote string) *comments.TextAnchor {
		return &comments.TextAnchor{Quote: quote, ParagraphIndex: -1}
	}

	tests := []struct {
		name    string
		anchor  comments.Anchor
		wantErr error
	}{
		{"text with replacement", suggestion(t, doc, "some words", new("other words")), nil},
		{"empty replacement deletes", suggestion(t, doc, "some words", new("")), nil},
		{"newline and tab allowed", suggestion(t, doc, "some words", new("a\n\tb")), nil},
		{"no replacement", suggestion(t, doc, "some words", nil), nil},
		{"reflowed single paragraph", comments.Anchor{
			Kind: comments.AnchorText, Text: textAnchor("wraps across\na line break"), Replacement: new("x"),
		}, nil},
		{"property anchor refused", comments.Anchor{
			Kind: comments.AnchorProperty, Ref: "title", Replacement: new("x"),
		}, comments.ErrInvalidReplacement},
		{"section anchor refused", comments.Anchor{
			Kind: comments.AnchorSection, Ref: "notes", Replacement: new("x"),
		}, comments.ErrInvalidReplacement},
		{"NUL refused", suggestion(t, doc, "some words", new("a\x00b")), comments.ErrInvalidReplacement},
		{"CR refused", suggestion(t, doc, "some words", new("a\rb")), comments.ErrInvalidReplacement},
		{"C1 control refused", suggestion(t, doc, "some words", new("a\u0085b")), comments.ErrInvalidReplacement},
		{"bidi override refused", suggestion(t, doc, "some words", new("a\u202Eb")), comments.ErrInvalidReplacement},
		{"bidi isolate refused", suggestion(t, doc, "some words", new("a\u2066b")), comments.ErrInvalidReplacement},
		{"zero-width space refused", suggestion(t, doc, "some words", new("a\u200Bb")), comments.ErrInvalidReplacement},
		{"BOM refused", suggestion(t, doc, "some words", new("\uFEFFab")), comments.ErrInvalidReplacement},
		{"Arabic letter mark refused", suggestion(t, doc, "some words", new("a\u061Cb")), comments.ErrInvalidReplacement},
		{"soft hyphen refused", suggestion(t, doc, "some words", new("a\u00ADb")), comments.ErrInvalidReplacement},
		{"word joiner refused", suggestion(t, doc, "some words", new("a\u2060b")), comments.ErrInvalidReplacement},
		{"variation selector refused", suggestion(t, doc, "some words", new("a\uFE0Fb")), comments.ErrInvalidReplacement},
		{"Hangul filler refused", suggestion(t, doc, "some words", new("a\u3164b")), comments.ErrInvalidReplacement},
		{"line separator refused", suggestion(t, doc, "some words", new("a\u2028b")), comments.ErrInvalidReplacement},
		{"Mongolian vowel separator refused", suggestion(t, doc, "some words", new("a\u180Eb")), comments.ErrInvalidReplacement},
		{"tag character refused", suggestion(t, doc, "some words", new("a\U000E0041b")), comments.ErrInvalidReplacement},
		{"accented and CJK text allowed", suggestion(t, doc, "some words", new("café 日本語")), nil},
		{"invalid UTF-8 refused", suggestion(t, doc, "some words", new("a\xffb")), comments.ErrInvalidReplacement},
		{"oversize refused", suggestion(t, doc, "some words",
			new(strings.Repeat("x", comments.MaxBodyBytes+1))), comments.ErrInvalidReplacement},
		{"blank line refused", comments.Anchor{
			Kind: comments.AnchorText, Text: textAnchor("some words.\n\n- item"), Replacement: new("x"),
		}, comments.ErrInvalidReplacement},
		{"across bullets refused", comments.Anchor{
			Kind: comments.AnchorText, Text: textAnchor("item one here\n- item two"), Replacement: new("x"),
		}, comments.ErrInvalidReplacement},
		{"across ordered items refused", comments.Anchor{
			Kind: comments.AnchorText, Text: textAnchor("step one\n2. step two"), Replacement: new("x"),
		}, comments.ErrInvalidReplacement},
		{"into heading refused", comments.Anchor{
			Kind: comments.AnchorText, Text: textAnchor("the end\n## Next"), Replacement: new("x"),
		}, comments.ErrInvalidReplacement},
		{"into setext underline refused", comments.Anchor{
			Kind: comments.AnchorText, Text: textAnchor("Title\n====="), Replacement: new("x"),
		}, comments.ErrInvalidReplacement},
		{"into setext h2 underline refused", comments.Anchor{
			Kind: comments.AnchorText, Text: textAnchor("Title\n---"), Replacement: new("x"),
		}, comments.ErrInvalidReplacement},
		{"into thematic break refused", comments.Anchor{
			Kind: comments.AnchorText, Text: textAnchor("end\n***"), Replacement: new("x"),
		}, comments.ErrInvalidReplacement},
		{"into underscore break refused", comments.Anchor{
			Kind: comments.AnchorText, Text: textAnchor("end\n_ _ _"), Replacement: new("x"),
		}, comments.ErrInvalidReplacement},
		{"into HTML block refused", comments.Anchor{
			Kind: comments.AnchorText, Text: textAnchor("end\n<div>"), Replacement: new("x"),
		}, comments.ErrInvalidReplacement},
		{"into HTML comment refused", comments.Anchor{
			Kind: comments.AnchorText, Text: textAnchor("end\n<!-- x -->"), Replacement: new("x"),
		}, comments.ErrInvalidReplacement},
		{"less-than in prose allowed", comments.Anchor{
			Kind: comments.AnchorText, Text: textAnchor("when a\n< b holds"), Replacement: new("x"),
		}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.anchor.Validate()
			if tc.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestApplyReplacement(t *testing.T) {
	tests := []struct {
		name  string
		doc   string
		quote string
		repl  string
		want  string
	}{
		{
			name: "replaces the quoted range", doc: body,
			quote: "the old id in the search index", repl: "a stale id in the index",
			want: strings.Replace(body, "the old id in the search index", "a stale id in the index", 1),
		},
		{
			name: "empty replacement deletes", doc: "Keep this. Remove this sentence. Keep that.\n",
			// The anchor trims the selection; one of the two spaces goes too.
			quote: "Remove this sentence.", repl: "",
			want: "Keep this. Keep that.\n",
		},
		{
			name: "replacement containing the quote", doc: "The brown fox sleeps.\n",
			quote: "brown fox", repl: "brown fox jumps",
			want: "The brown fox jumps sleeps.\n",
		},
		{
			name: "inside emphasis keeps markup", doc: "A **bold claim** here.\n",
			quote: "bold claim", repl: "strong claim",
			want: "A **strong claim** here.\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := comments.ApplyReplacement(tc.doc, suggestion(t, tc.doc, tc.quote, new(tc.repl)))
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestApplyReplacement_AcrossReflow pins that the splice covers the span the
// resolver found, not len(quote): fsstore inserts a newline inside the quote.
func TestApplyReplacement_AcrossReflow(t *testing.T) {
	quote := "until a restart, which is confusing"
	a := suggestion(t, body, quote, new("until restart, which confuses"))
	reflowed := markdown.FormatMarkdown(body)
	require.NotContains(t, reflowed, quote, "quote must straddle the wrap, or this proves nothing")

	got, err := comments.ApplyReplacement(reflowed, a)
	require.NoError(t, err)
	require.Contains(t, got, "until restart, which confuses")
	require.NotContains(t, got, "is confusing")
}

// TestApplyReplacement_UniqueQuoteWithEditedContext is the case the exact band
// alone would refuse: the neighboring text changed, lowering confidence, but
// the quote is unique so its location is not in doubt.
func TestApplyReplacement_UniqueQuoteWithEditedContext(t *testing.T) {
	orig := "Alpha beta gamma. The quoted sentence is here. Delta epsilon zeta.\n"
	a := suggestion(t, orig, "The quoted sentence is here.", new("The new sentence."))
	edited := "Completely rewritten lead. The quoted sentence is here. Different tail now.\n"

	m := comments.ResolveText(edited, a.Text)
	require.False(t, m.Detached)

	got, err := comments.ApplyReplacement(edited, a)
	require.NoError(t, err)
	require.Equal(t, "Completely rewritten lead. The new sentence. Different tail now.\n", got)
}

func TestApplyReplacement_Refusals(t *testing.T) {
	doc := "The brown fox sleeps under the old oak tree.\n"

	t.Run("no replacement", func(t *testing.T) {
		_, err := comments.ApplyReplacement(doc, suggestion(t, doc, "brown fox", nil))
		require.ErrorIs(t, err, comments.ErrNoSuggestion)
	})
	t.Run("quote removed", func(t *testing.T) {
		a := suggestion(t, doc, "old oak tree", new("birch"))
		_, err := comments.ApplyReplacement("Nothing like it remains in this text at all.\n", a)
		require.ErrorIs(t, err, comments.ErrSuggestionStale)
		require.False(t, comments.Acceptable("Nothing like it remains.\n", a))
	})
	t.Run("quote rewritten into a fuzzy match", func(t *testing.T) {
		a := suggestion(t, doc, "sleeps under the old oak tree", new("rests"))
		_, err := comments.ApplyReplacement("The brown fox sleeps beneath the old oak tree.\n", a)
		require.ErrorIs(t, err, comments.ErrSuggestionStale)
	})
	t.Run("blank line inserted inside the quote", func(t *testing.T) {
		d := "The quick brown fox jumps over the lazy dog today.\n"
		a := suggestion(t, d, "fox jumps over", new("cat leaps over"))
		split := "The quick brown fox jumps\n\nover the lazy dog today.\n"
		_, err := comments.ApplyReplacement(split, a)
		require.ErrorIs(t, err, comments.ErrSuggestionStale)
	})
	t.Run("non-text anchor", func(t *testing.T) {
		a := comments.Anchor{Kind: comments.AnchorProperty, Ref: "title", Replacement: new("x")}
		_, err := comments.ApplyReplacement(doc, a)
		require.ErrorIs(t, err, comments.ErrSuggestionStale)
	})
	t.Run("acceptable on the original body", func(t *testing.T) {
		require.True(t, comments.Acceptable(doc, suggestion(t, doc, "brown fox", new("red fox"))))
	})
}
