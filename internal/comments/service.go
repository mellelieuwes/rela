package comments

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/principal"
)

// idBytes is the entropy in a minted comment ID. 10 bytes -> 16 base32
// characters, which is far more than a per-target thread needs; comment IDs
// are opaque handles, never typed by a human, so there is no reason to trade
// collision headroom for brevity the way entity IDs do.
const idBytes = 10

// idEncoding is unpadded lowercase base32 — case-insensitive and free of the
// characters that would complicate a URL path segment.
var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// Clock supplies the current time. Injected rather than read from the wall
// clock so tests can pin CreatedAt and assert List's ordering deterministically
// — the same treatment userstate gives expiry, and for the same reason.
//
// Nil: never; [NewService] substitutes time.Now.
type Clock func() time.Time

// Service is the write path for comments: it stamps identity, mints IDs,
// validates input, and delegates persistence to a [Store].
//
// It is the only thing that constructs a [Comment], which is what makes the
// server-written fields trustworthy — there is no path where a client-supplied
// ID or Author reaches storage.
type Service struct {
	store Store
	now   Clock
}

// NewService returns a Service over st.
//
// Nil: st is required and rejected when nil — a Service with no store would
// silently discard every comment, and the failure would surface far from its
// cause. clock may be nil, in which case time.Now is used.
func NewService(st Store, clock Clock) (*Service, error) {
	if st == nil {
		return nil, errors.New("comments: NewService requires a Store")
	}
	if clock == nil {
		clock = time.Now
	}
	return &Service{store: st, now: clock}, nil
}

// AddRequest is the caller-supplied part of a new comment.
//
// Note what is absent: ID, Author and CreatedAt. They are not fields a caller
// may set, so they are not fields this struct carries — the wire type cannot
// express the forgery, rather than expressing it and having it stripped later.
type AddRequest struct {
	Anchor Anchor
	Body   string
}

// Add stores a new comment on target, attributed to the ctx principal.
//
// The author is taken from ctx and never from the request. An unstamped
// principal is refused ([ErrUnknownAuthor]) rather than recorded as "unknown":
// a comment nobody is recorded as having written can never satisfy an
// *-own permission check, so storing one creates a record its author can
// neither edit nor delete.
func (s *Service) Add(ctx context.Context, target Target, req AddRequest) (Comment, error) {
	author, err := authorFrom(ctx)
	if err != nil {
		return Comment{}, err
	}
	if vErr := req.Anchor.Validate(); vErr != nil {
		return Comment{}, vErr
	}
	if vErr := ValidateBody(req.Body); vErr != nil {
		return Comment{}, vErr
	}

	existing, err := s.store.List(ctx, target)
	if err != nil {
		return Comment{}, err
	}
	if len(existing) >= MaxPerTarget {
		return Comment{}, fmt.Errorf("%w: %d is the limit", ErrTooManyComments, MaxPerTarget)
	}

	id, err := mintID()
	if err != nil {
		return Comment{}, err
	}
	c := Comment{
		ID:        id,
		Author:    author,
		CreatedAt: s.now().UTC(),
		Anchor:    req.Anchor,
		Body:      req.Body,
	}
	if err := s.store.Add(ctx, target, c); err != nil {
		return Comment{}, err
	}
	return c, nil
}

// List returns the target's comments, oldest first.
//
// The ACL gate is the caller's job: this returns everything stored for the
// target. Callers on a served path must have resolved the target's read
// verdict and the comment:read permission first.
func (s *Service) List(ctx context.Context, target Target) ([]Comment, error) {
	return s.store.List(ctx, target)
}

// Get returns one comment by ID, or [ErrNotFound].
//
// It exists chiefly so a handler can resolve a comment's author *before*
// deciding whether an *-own permission covers the requested mutation.
//
// The ACL gate is the caller's job, as with List.
func (s *Service) Get(ctx context.Context, target Target, id string) (Comment, error) {
	return s.store.Get(ctx, target, id)
}

// Update replaces a comment's body and resolved flag.
//
// Author, CreatedAt and Anchor are not editable: an edit must not be able to
// change who said something or what it was said about. Authorization —
// including whether the ctx principal owns this comment — is the caller's job.
func (s *Service) Update(ctx context.Context, target Target, id, body string, resolved bool) error {
	if err := ValidateBody(body); err != nil {
		return err
	}
	return s.store.Update(ctx, target, id, body, resolved)
}

// SetResolved flips a comment's resolved flag only if it differs, reporting
// whether this call changed it. See [Store.SetResolved]. Authorization is the
// caller's job.
func (s *Service) SetResolved(ctx context.Context, target Target, id string, resolved bool) (bool, error) {
	return s.store.SetResolved(ctx, target, id, resolved)
}

// Delete removes one comment. Authorization is the caller's job.
func (s *Service) Delete(ctx context.Context, target Target, id string) error {
	return s.store.Delete(ctx, target, id)
}

// EntityRenamed re-keys the renamed entity's comments.
//
// This implements entitymanager's AliasRewriter hook rather than
// [store.EntityObserver], for the reason that hook documents: every store fires
// the observer as `_ = o.EntityRenamed(...)`, discarding the error. That is the
// right trade for a search index, which can be rebuilt from the store. It is
// the wrong trade here — comments exist ONLY in the comment store, so a
// swallowed re-key failure leaves a thread filed under an id nothing resolves
// to, still on disk and completely unreachable, with no signal anywhere.
//
// Load-bearing either way: rename emits exactly one notification, never a
// delete followed by a create, so a service that ignored it would strand every
// comment on every renamed entity.
func (s *Service) EntityRenamed(ctx context.Context, oldID, newID string) error {
	if oldID == newID {
		return nil
	}
	return s.store.Rename(ctx, oldID, newID)
}

// EntityDeleted drops the deleted entity's comments.
//
// Unlike the CalDAV alias service — which deliberately KEEPS its references so
// a stale client write can be refused — comments are dropped: rela permits id
// reuse, so a later entity taking this id would otherwise inherit the previous
// occupant's thread and present someone else's remarks as its own.
func (s *Service) EntityDeleted(ctx context.Context, entityID string) error {
	// All faces: the entity is gone, so a thread left on any face would be
	// stranded at an id nothing can reach.
	return s.store.DeleteAllFaces(ctx, entityID)
}

// EntityFaceDeleted drops the thread of one deleted face, leaving the entity's
// other faces untouched.
//
// Dropped for the reason EntityDeleted gives: a face of the same name created
// later would otherwise inherit remarks about content that no longer exists.
func (s *Service) EntityFaceDeleted(ctx context.Context, entityID string, face entity.Face) error {
	return s.store.DeleteTarget(ctx, Target{ID: entityID, Face: face})
}

// authorFrom resolves the comment author from ctx.
//
// Uses [principal.Stamped] rather than From because the two cases differ here:
// From's unknown/unknown default is a reasonable audit-log placeholder but an
// unusable comment author (see [Service.Add]). A reserved system identity is
// refused for the same reason — a system job has no business authoring
// commentary in a human's thread.
func authorFrom(ctx context.Context) (string, error) {
	p, ok := principal.Stamped(ctx)
	if !ok {
		return "", ErrUnknownAuthor
	}
	user := strings.TrimSpace(p.User)
	if user == "" || user == "unknown" {
		return "", ErrUnknownAuthor
	}
	if principal.IsReserved(user) {
		return "", fmt.Errorf("%w: %q is a reserved system identity", ErrUnknownAuthor, user)
	}
	return user, nil
}

// mintID returns a fresh opaque comment ID.
func mintID() (string, error) {
	buf := make([]byte, idBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("comments: generating id: %w", err)
	}
	return idEncoding.EncodeToString(buf), nil
}

// SortComments orders comments oldest-first with the ID as a tie-break.
//
// Shared by every backend so ordering is defined in ONE place: a thread must
// render identically regardless of which backend served it, and two
// implementations sorting "the same way" independently is how that guarantee
// quietly stops holding. The ID tie-break matters because a clock with
// coarse resolution can stamp two comments in one tick.
func SortComments(list []Comment) {
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].CreatedAt.Before(list[j].CreatedAt)
		}
		return list[i].ID < list[j].ID
	})
}

// MergeThreads appends arriving to occupying, dropping any arrival whose ID the
// destination already uses, and returns the result sorted.
//
// This is what a Rename into an OCCUPIED destination must do, and it is here
// for the same reason [SortComments] is: every backend owes the same answer,
// and two implementations agreeing independently is how that quietly stops
// being true. The database backends get it from ON CONFLICT DO NOTHING; the
// in-memory and file backends call this.
//
// The destination's own comment wins on a collision — it is the one a reader
// may already have seen. Dropping the arrival rather than keeping both matters
// because the service addresses a comment by (target, id): a thread holding one
// ID twice makes the second copy unreachable, and an Update or Delete aimed at
// it silently hits the first instead. Collisions need a restored backup or a
// re-import to arise at all (IDs carry 80 bits of crypto/rand), but the damage
// they do is lasting rather than transient.
func MergeThreads(occupying, arriving []Comment) []Comment {
	if len(occupying) == 0 {
		SortComments(arriving)
		return arriving
	}
	taken := make(map[string]struct{}, len(occupying))
	for _, c := range occupying {
		taken[c.ID] = struct{}{}
	}
	// Fresh array rather than appending in place: occupying may alias a slice
	// the caller still holds (a map value, a read buffer).
	merged := make([]Comment, 0, len(occupying)+len(arriving))
	merged = append(merged, occupying...)
	for _, c := range arriving {
		if _, dup := taken[c.ID]; !dup {
			merged = append(merged, c)
		}
	}
	SortComments(merged)
	return merged
}
