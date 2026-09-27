// Package comments holds user commentary attached to entities: a remark about
// a property or a view section, stored alongside the graph rather than in it.
//
// # Why this is not in the graph
//
// A comment is a remark *about* an entity, not a fact *in* the domain model the
// operator declared in schema.yaml. Modeling one as an entity would mean
// fabricating a type the operator never wrote, and would drag commentary
// through every mechanism the graph owns: the audit log, version capture, the
// search index, `analyze_*`, and `/_schema`. None of those are wanted for a
// note someone left on a field.
//
// So this is a separate service with its own backends, deliberately outside
// store.Store and entitymanager — the same call [internal/userstate] makes for
// snoozes, and for the same reason.
//
// # Authorship is stamped, never supplied
//
// [Comment.Author] and [Comment.ID] are written by the server from the request
// principal; a client-supplied value for either is ignored. This is the reason
// the service owns its own write path rather than routing through the graph:
// none of the in-graph seams can stamp a trustworthy identity (automation
// template vars resolve to the *git config* user, computed properties cannot
// see the principal, and the elevated Lua write handle deliberately exposes no
// entity write). A forgeable author on a comment is a forgeable attribution of
// what someone said.
//
// # Ordering and identity are part of the contract
//
// [Store.List] returns comments oldest-first, ties broken by ID, so a thread
// renders identically on every backend. Implementations must not return
// storage order. [commentstest.RunAll] pins this, along with the concurrency
// and re-key behavior below — a contract asserted in one place beats three
// backends that each behave slightly differently.
//
// # Lifecycle
//
// Comments are keyed by target entity ID, so the service tracks the graph:
// [Service.EntityRenamed] re-keys a target's comments and
// [Service.EntityDeleted] removes them. Rename emits exactly one store callback
// (never delete+put), so a service that ignored it would silently strand every
// comment on a renamed entity.
package comments

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Sourcehaven-BV/rela/internal/entity"
)

// Sentinel errors. Callers map these to transport-level responses; the HTTP
// layer turns [ErrNotFound] into a 404 and the validation errors into 400s.
var (
	// ErrNotFound reports that no comment with the given ID exists on the
	// target. Returned by Get, Update and Delete.
	ErrNotFound = errors.New("comments: not found")

	// ErrEmptyBody reports a comment whose body is empty or whitespace-only.
	ErrEmptyBody = errors.New("comments: body must not be empty")

	// ErrBodyTooLong reports a body over [MaxBodyBytes].
	ErrBodyTooLong = errors.New("comments: body too long")

	// ErrBodyControlChars reports a body containing control characters other
	// than newline and tab. The file backend serializes to YAML, where a NUL
	// is at best lossy and at worst breaks the document.
	ErrBodyControlChars = errors.New("comments: body contains control characters")

	// ErrTooManyComments reports that the target is at [MaxPerTarget].
	ErrTooManyComments = errors.New("comments: target has too many comments")

	// ErrUnknownAuthor reports an attempt to write with no resolved principal.
	// Refused rather than stored as a placeholder: an "unknown" author makes
	// every *-own permission check meaningless, since no one can prove
	// ownership of a comment nobody is recorded as having written.
	ErrUnknownAuthor = errors.New("comments: cannot attribute comment to an unknown principal")

	// ErrInvalidAnchor reports a structurally invalid anchor (unknown kind, or
	// an empty ref). Note this is NOT the "ref names nothing" case: an anchor
	// pointing at a property that has since been removed is a soft condition
	// per DEC-HWZHA and surfaces as a warning on read, never an error.
	ErrInvalidAnchor = errors.New("comments: invalid anchor")

	// ErrInvalidReplacement reports a suggested replacement that is too long,
	// carries characters that could disguise it, sits on an anchor that cannot
	// take one, or targets a quote that crosses a block boundary.
	ErrInvalidReplacement = errors.New("comments: invalid replacement")

	// ErrNoSuggestion reports an accept on a comment that carries no
	// replacement. A state conflict rather than malformed input: the request
	// is well-formed, the comment simply has nothing to apply.
	ErrNoSuggestion = errors.New("comments: comment has no suggested replacement")

	// ErrSuggestionStale reports that the quoted text can no longer be located
	// with enough certainty to replace it. Accepting then would splice the
	// replacement into text nobody suggested changing.
	ErrSuggestionStale = errors.New("comments: the suggested text no longer matches the body")
)

// Limits on a single comment and on one target's thread. Both exist to bound
// the file backend, which holds a target's whole thread in one document that is
// read in full on every List.
const (
	// MaxBodyBytes caps a single comment body.
	MaxBodyBytes = 16 * 1024

	// MaxPerTarget caps how many comments one entity may carry.
	//
	// ADVISORY on the database backends, not a hard limit. Service.Add checks
	// it by listing then counting, which is a check-then-act: with several
	// rela-server processes on one database (the topology pgcomments exists to
	// serve), N concurrent posts to a thread at the limit all read the same
	// count and all insert. The file and memory tiers are single-writer, so
	// there the check holds exactly.
	//
	// Left as-is deliberately. The cap exists to bound the FILE backend's
	// whole-thread document reads, so overshooting it by a handful of rows on a
	// backend that pages costs nothing; making it exact would mean a
	// conditional insert and a new method on comments.Store, which is a
	// contract change for an invariant that does not need to be exact.
	MaxPerTarget = 500

	// MinQuoteRunes is the shortest text selection that may be anchored.
	//
	// Matched to the matcher's own floor (it refuses queries under 5 bytes):
	// below this a quote is too generic to re-locate, so accepting one would
	// mint a comment that detaches on the next edit.
	MinQuoteRunes = 5

	// MaxQuoteBytes caps a stored quote. Generous for a sentence or two, and
	// bounded so a caller cannot store an entire body as an "anchor" — the
	// resolver's cost scales with quote length.
	MaxQuoteBytes = 2000
)

// AnchorKind identifies what part of an entity a comment is attached to.
//
// The set is deliberately open to extension: stage 2 adds a text-range kind
// carrying a quote and surrounding context, which is why [Anchor] is a struct
// with a kind discriminator rather than a bare property name. Adding a kind
// must not require migrating stored comments.
type AnchorKind string

const (
	// AnchorProperty attaches a comment to a named property. Ref is the
	// property name.
	AnchorProperty AnchorKind = "property"

	// AnchorSection attaches a comment to a view section. Ref is the
	// section's slug id, which is derived from the operator-authored view
	// heading rather than from user content — so it survives edits to the
	// entity body.
	AnchorSection AnchorKind = "section"

	// AnchorText attaches a comment to a RANGE of the entity body (stage 2).
	//
	// Unlike the two name-based kinds, this one anchors to content that the
	// user can edit out from under it. Ref is unused; [Anchor.Text] carries
	// the quote plus the surrounding context that lets it be re-located after
	// an edit, and a resolution that fails is reported as detached rather than
	// dropped (DEC-HWZHA).
	AnchorText AnchorKind = "text"
)

// TextAnchor is the stage-2 descriptor set for a body text range.
//
// The fields mirror github.com/vloothuis/textanchor's Anchor exactly, because
// they are handed to it verbatim on resolve. They are stored rather than a byte
// offset for the reason the whole feature exists: an offset is invalidated by
// any edit earlier in the body, and on the fs backend by a plain re-save, since
// fsstore reflows every body to 80 columns on write.
//
// Quote alone is not enough — the same words can occur twice — so Prefix and
// Suffix disambiguate, ContainingSentence rescues short generic quotes, and the
// structural pair (HeadingContext, ParagraphIndex) survives a rewrite of the
// quoted sentence itself.
type TextAnchor struct {
	Quote              string `json:"quote"                          yaml:"quote"`
	Prefix             string `json:"prefix,omitempty"               yaml:"prefix,omitempty"`
	Suffix             string `json:"suffix,omitempty"               yaml:"suffix,omitempty"`
	ContainingSentence string `json:"containing_sentence,omitempty"  yaml:"containing_sentence,omitempty"`
	HeadingContext     string `json:"heading_context,omitempty"      yaml:"heading_context,omitempty"`
	// ParagraphIndex is 0-based within the section named by HeadingContext,
	// or -1 when not applicable. Zero is a MEANINGFUL value (the first
	// paragraph), so this is never omitempty — dropping it would silently
	// retarget a comment to whatever paragraph the zero value implies.
	ParagraphIndex int `json:"paragraph_index" yaml:"paragraph_index"`
}

// Anchor locates a comment within its target entity.
//
// Both current kinds anchor by NAME (a property name, a section slug), never by
// offset, which is what makes them immune to edits of the entity body. That is
// the whole reason stage 1 ships these two kinds and defers text ranges.
type Anchor struct {
	Kind AnchorKind `json:"kind" yaml:"kind"`
	Ref  string     `json:"ref"  yaml:"ref"`

	// Text carries the descriptors for an [AnchorText] anchor, and is nil for
	// every other kind. A pointer so a property or section comment serializes
	// exactly as it did before this field existed — which is what lets stage 2
	// ship without migrating a single stored comment.
	Text *TextAnchor `json:"text,omitempty" yaml:"text,omitempty"`

	// Replacement is a suggested substitute for the quoted text of an
	// [AnchorText] anchor (TKT-S5C0K3), and nil for every other kind.
	//
	// A pointer because the empty string is a meaningful suggestion ("delete
	// this text") and must stay distinct from "no suggestion". It lives on the
	// anchor rather than the comment because, like the anchor, it is fixed at
	// creation: [Store.Update] never touches it, so a suggestion cannot be
	// swapped after someone has reviewed it. It is not on [TextAnchor], which
	// mirrors the locator library and holds only what finds the text.
	Replacement *string `json:"replacement,omitempty" yaml:"replacement,omitempty"`
}

// Validate reports whether the anchor is structurally usable.
//
// It checks shape only. Whether Ref names a property or section that currently
// exists is deliberately NOT checked: an entity may be edited, or hand-edited
// outside rela, so a ref that resolves to nothing is an expected state that
// surfaces as a warning on read (DEC-HWZHA).
func (a Anchor) Validate() error {
	switch a.Kind {
	case AnchorProperty, AnchorSection:
		if strings.TrimSpace(a.Ref) == "" {
			return fmt.Errorf("%w: ref must not be empty", ErrInvalidAnchor)
		}
	case AnchorText:
		// Ref is unused for a text anchor; the descriptors carry the location.
		if a.Text == nil {
			return fmt.Errorf("%w: text anchor requires a text descriptor", ErrInvalidAnchor)
		}
		// A quote shorter than this cannot be located reliably — the matcher
		// itself refuses queries under 5 bytes, and a 2-character quote would
		// match almost anywhere. Refusing at the boundary gives the caller a
		// 400 instead of a comment that is born detached.
		if len([]rune(strings.TrimSpace(a.Text.Quote))) < MinQuoteRunes {
			return fmt.Errorf("%w: quote must be at least %d characters",
				ErrInvalidAnchor, MinQuoteRunes)
		}
		if len(a.Text.Quote) > MaxQuoteBytes {
			return fmt.Errorf("%w: quote exceeds %d bytes", ErrInvalidAnchor, MaxQuoteBytes)
		}
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidAnchor, a.Kind)
	}
	if a.Replacement != nil {
		return a.validateReplacement()
	}
	return nil
}

// validateReplacement applies the suggestion rules. Called only for an anchor
// whose structural checks already passed.
func (a Anchor) validateReplacement() error {
	if a.Kind != AnchorText {
		return fmt.Errorf("%w: only a text comment can suggest a replacement", ErrInvalidReplacement)
	}
	repl := *a.Replacement
	if len(repl) > MaxBodyBytes {
		return fmt.Errorf("%w: %d bytes exceeds the %d-byte limit", ErrInvalidReplacement, len(repl), MaxBodyBytes)
	}
	if !utf8.ValidString(repl) {
		return fmt.Errorf("%w: not valid UTF-8", ErrInvalidReplacement)
	}
	for _, r := range repl {
		if r == '\n' || r == '\t' {
			continue
		}
		// Control characters are refused for the reason ValidateBody gives.
		// The invisible ones are refused because a reviewer approves what the
		// diff SHOWS: a bidi override or zero-width character makes the text
		// that lands in the body differ from the text that was approved.
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || isInvisibleFormat(r) {
			return fmt.Errorf("%w: U+%04X is not allowed", ErrInvalidReplacement, r)
		}
	}
	if !Replaceable(a.Text) {
		return fmt.Errorf("%w: the selection spans more than one paragraph or list item", ErrInvalidReplacement)
	}
	return nil
}

// isInvisibleFormat reports characters that render as nothing or reorder the
// text around them, so a diff showing them would not show what gets written.
//
// The whole format category (Cf) rather than a hand list: it covers zero-width
// characters, every bidi control, the soft hyphen and the tag block, which can
// spell hidden ASCII. Variation selectors, the Hangul fillers and the Unicode
// line/paragraph separators are outside Cf and render blank, so they are added.
func isInvisibleFormat(r rune) bool {
	switch {
	case unicode.In(r, unicode.Cf, unicode.Variation_Selector):
		return true
	case r == '\u115F', r == '\u1160', r == '\u3164', r == '\uFFA0', // Hangul fillers
		r == '\u2028', r == '\u2029', // line and paragraph separators
		r == '\u034F', r == '\u180E': // combining grapheme joiner, Mongolian vowel separator
		return true
	}
	return false
}

// Replaceable reports whether a suggested replacement may target t: it must
// lie within one markdown block. The same rule [Anchor.Validate] applies to a
// posted suggestion.
func Replaceable(t *TextAnchor) bool {
	return t != nil && !crossesBlock(t.Quote)
}

// crossesBlock reports whether a SOURCE quote spans a markdown block boundary.
//
// Replacing such a quote would replace the markup between the blocks too (a
// blank line, a list marker, a heading hash), so one plain-text replacement
// would merge or unformat blocks the suggester never saw as markup. A single
// newline inside a paragraph is fine: fsstore reflows every body at 80
// columns, so most multi-line quotes are one paragraph.
func crossesBlock(quote string) bool {
	lines := strings.Split(quote, "\n")
	for _, line := range lines[1:] {
		t := strings.TrimLeft(line, " \t")
		if t == "" || startsBlock(t) {
			return true
		}
	}
	return false
}

// startsBlock reports whether a line opens a new markdown block, or ends one
// (a setext heading underline or a thematic break).
//
// It errs toward refusing: a false positive only stops a suggestion from being
// posted, while a miss lets one replacement strip block markup.
func startsBlock(line string) bool {
	for _, p := range []string{"- ", "* ", "+ ", "#", ">", "|", "```", "~~~"} {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	if isRuleLine(line) {
		return true
	}
	// An HTML block: a tag, closing tag, comment or declaration.
	if len(line) > 1 && line[0] == '<' {
		c := line[1]
		if c == '/' || c == '!' || c == '?' || (c|0x20 >= 'a' && c|0x20 <= 'z') {
			return true
		}
	}
	// Ordered list item: digits then ". " or ") ".
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(line) && (line[i] == '.' || line[i] == ')') && line[i+1] == ' '
}

// isRuleLine reports a line made only of one of `=`, `-`, `*` or `_` (spaces
// allowed): a setext heading underline or a thematic break.
func isRuleLine(line string) bool {
	t := strings.TrimRight(line, " \t")
	if t == "" || !strings.ContainsRune("=-*_", rune(t[0])) {
		return false
	}
	return strings.Trim(t, string(t[0])+" \t") == ""
}

// Comment is one remark attached to one entity.
//
// ID, Author and CreatedAt are server-written on Add; a value supplied by a
// client for any of them is discarded (see the package doc). Body is markdown,
// stored verbatim and rendered — sanitized — by the caller.
type Comment struct {
	ID        string    `json:"id"         yaml:"id"`
	Author    string    `json:"author"     yaml:"author"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt time.Time `json:"updated_at,omitzero" yaml:"updated_at,omitempty"`
	Anchor    Anchor    `json:"anchor"     yaml:"anchor"`
	Body      string    `json:"body"       yaml:"body"`
	Resolved  bool      `json:"resolved"   yaml:"resolved"`
}

// Target identifies the entity a thread of comments belongs to.
//
// Type is carried alongside ID because the HTTP surface is addressed by
// (type, id) and the read gate needs both; it is not part of the storage key,
// since entity IDs are unique across types.
type Target struct {
	Type string
	ID   string
	// Face scopes the thread to one content state of the entity (FEAT-9CD2MX).
	// The zero value addresses the default face, which is also what a faceless
	// project always uses.
	//
	// Comments are PER FACE because a face is a distinct piece of content: a
	// remark on the draft ("this paragraph needs a source") is not a remark on
	// the published version, and surfacing it there would attach feedback to
	// text that may not even contain the quote. The read gate is per face too,
	// so a shared thread would also leak across a boundary the entity itself
	// maintains.
	Face entity.Face
}

// Key is the storage key for a target's thread.
//
// It is [entity.FormatStateRef], so the DEFAULT face serializes to the bare id
// — which is what lets faces arrive without migrating a single stored comment:
// every thread written before faces existed is already at its correct key.
func (t Target) Key() string {
	return entity.FormatStateRef(t.ID, t.Face)
}

// Store persists comments per target entity.
//
// Every implementation must pass [commentstest.RunAll], which pins the parts of
// this contract that are easy to get subtly different: List's ordering, the
// server-minted ID, and that concurrent Adds to one target both survive.
//
// Nil: no method returns a nil error with a nil result; List returns an empty
// slice rather than nil when a target has no comments, and Get reports a
// missing comment as [ErrNotFound] rather than a zero Comment.
type Store interface {
	// List returns the target's comments, oldest first, ties broken by ID.
	// A target with no comments yields an empty slice, not an error.
	List(ctx context.Context, target Target) ([]Comment, error)

	// Get returns one comment, or [ErrNotFound] if the target holds no
	// comment with that ID.
	//
	// Scoped to the target, so an ID is only ever resolved within one face:
	// a comment stored under `TKT-1@draft` must not be reachable through
	// `TKT-1`. That is the same scoping List and Delete use, and it is what
	// stops an authorization check reading a record from a face the request
	// never named.
	//
	// Separate from List because the database backends serve this from
	// `PRIMARY KEY (target_key, id)` as a single-row read. Authorizing an
	// edit needs one comment's author; going through List would fetch up to
	// [MaxPerTarget] rows with their bodies to find it.
	Get(ctx context.Context, target Target, id string) (Comment, error)

	// Add appends c to the target's thread. The caller has already set ID,
	// Author and CreatedAt; implementations persist them as given rather
	// than minting their own, so the values in an audit trail and the values
	// stored agree.
	Add(ctx context.Context, target Target, c Comment) error

	// Update replaces the body and resolved flag of the comment with the
	// given ID. Returns [ErrNotFound] if it does not exist. Author,
	// CreatedAt and Anchor are immutable — editing a comment must not let it
	// change who said it or what it was about.
	Update(ctx context.Context, target Target, id string, body string, resolved bool) error

	// SetResolved sets only the resolved flag, and only when it differs from
	// the stored value, as one atomic step. changed reports whether this call
	// flipped it; false means it already held that value. Returns
	// [ErrNotFound] if the comment does not exist.
	//
	// Conditional because accepting a suggestion claims the comment by
	// resolving it: two requests, possibly on two server processes sharing one
	// database, must not both believe they won. It leaves the body alone so a
	// concurrent edit to it is not overwritten.
	SetResolved(ctx context.Context, target Target, id string, resolved bool) (changed bool, err error)

	// Delete removes one comment. Returns [ErrNotFound] if absent.
	Delete(ctx context.Context, target Target, id string) error

	// DeleteTarget removes every comment on ONE target (one face). Deleting a
	// target with no comments is not an error.
	DeleteTarget(ctx context.Context, target Target) error

	// DeleteAllFaces removes every thread belonging to an entity id, across
	// all of its faces.
	//
	// Distinct from DeleteTarget: an entity delete takes the whole entity with
	// it, so leaving a faced thread behind would strand comments at an id
	// nothing can reach.
	DeleteAllFaces(ctx context.Context, entityID string) error

	// Rename re-keys a target's comments from oldID to newID, preserving
	// order. Renaming a target with no comments is not an error.
	Rename(ctx context.Context, oldID, newID string) error
}

// ValidateBody applies the size and character rules to a comment body.
//
// Allowlist-shaped: newline and tab are the only control characters permitted,
// because a body is prose. Everything else in the C0 range (and DEL) is
// refused rather than escaped, since the file backend round-trips through YAML
// where those bytes are lossy.
func ValidateBody(body string) error {
	if strings.TrimSpace(body) == "" {
		return ErrEmptyBody
	}
	if len(body) > MaxBodyBytes {
		return fmt.Errorf("%w: %d bytes exceeds the %d-byte limit", ErrBodyTooLong, len(body), MaxBodyBytes)
	}
	if !utf8.ValidString(body) {
		return fmt.Errorf("%w: body is not valid UTF-8", ErrBodyControlChars)
	}
	for _, r := range body {
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: U+%04X", ErrBodyControlChars, r)
		}
	}
	return nil
}

// FacePrefixPattern builds the SQL LIKE pattern matching every FACED thread of
// an entity id ("id@draft", never the bare "id"), escaped for `ESCAPE '\'`.
//
// Shared by the database backends for the reason [SortComments] and
// [MergeThreads] are: the escaping below is subtle, and two copies of subtle
// code are two chances to fix a bug in one of them. It lives here rather than
// in either backend because neither may import the other.
//
// The id is escaped because an entity id may legally contain an underscore
// (entity.ValidateID admits [A-Za-z0-9_-]), and an unescaped "_" is LIKE's
// single-character wildcard — so renaming "TKT_1" would also re-key "TKT-1",
// silently merging two unrelated threads. Backslash is escaped first so it
// cannot double-escape what follows; "%" cannot appear in a valid id but is
// escaped anyway, since this function's correctness should not depend on a
// grammar declared in another package.
//
// Note this handles WILDCARDS only. SQLite's LIKE is also ASCII
// case-insensitive, which no pattern can express; sqlitecomments pairs this
// with a byte-exact guard for that.
func FacePrefixPattern(id string) string {
	r := strings.NewReplacer(`\`, `\\`, `_`, `\_`, `%`, `\%`)
	return r.Replace(id) + entity.StateRefSeparator + "%"
}
