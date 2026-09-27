// Package pgcomments is the PostgreSQL-backed [comments.Store].
//
// It exists because the default backend is node-local. filecomments writes one
// YAML document per target under the project's .rela/ directory and scopes
// itself out of the multi-writer problem explicitly ("a cross-process writer is
// out of scope for this tier, exactly as it is for fsstore"). That is the right
// call for the filesystem and desktop tiers, where one process owns the
// project. It is the wrong one for the deployment docs/postgres-backend.md
// describes — several rela-server processes behind a load balancer sharing one
// database — because there a comment posted through one node simply does not
// exist for the others, and nothing reports an error.
//
// # Relationship to the store
//
// This package does NOT depend on internal/store, and that is enforced by
// arch-lint rather than convention. A comment is not graph content; routing it
// through the store would drag it into the audit log, version capture, search
// and /_schema, none of which are wanted for a note someone left on a field
// (see the internal/comments package doc).
//
// What it shares with the store is the CONNECTION POOL, injected by the
// composition root exactly as it is into pgstore.New and
// pgstore.NewSearchBackend. Three consumers, one pool, one owner — the
// alternative, opening a second pool against the same database, doubles a
// process's connection footprint to isolate packages that are already isolated
// by their imports.
//
// # Concurrency
//
// There is no mutex here. filecomments needs one because it read-modify-writes
// a whole document; this backend stores one ROW per comment, so concurrent adds
// to one target are independent inserts and the database serializes them.
// commentstest.RunConcurrencyTests pins that they all survive.
package pgcomments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Sourcehaven-BV/rela/internal/comments"
)

// DBTX is the database handle this store runs on: a *pgxpool.Pool in
// production, or anything else offering the same four methods.
//
// Restated here rather than imported from pgstore because this package must not
// depend on the store layer (see the package doc). The duplication is four
// lines and buys an enforced boundary; the alternative is importing a store
// package for a type alias, which is exactly the coupling arch-lint forbids.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Store is the PostgreSQL comment backend.
type Store struct {
	db DBTX
}

var _ comments.Store = (*Store)(nil)

// New constructs a Store over the given handle.
//
// Nil: rejected. A nil handle would defer the failure to the first comment
// anyone posts, which is both far from the wiring mistake that caused it and
// user-visible; CLAUDE.md requires constructors to reject nil required
// collaborators up front.
func New(db DBTX) (*Store, error) {
	if db == nil {
		return nil, errors.New("pgcomments: New requires a database handle")
	}
	return &Store{db: db}, nil
}

// columns is the read projection, ordered to match scanComment.
const columns = `id, author, created_at, updated_at, anchor, body, resolved`

// List returns the target's thread in contract order.
//
// Ordering is the database's (the comments_thread_idx column order), not a
// re-sort in Go: the index makes it a range scan, and a backend that sorted
// after reading would still be correct but would pay for a sort on every read
// of every thread.
func (s *Store) List(ctx context.Context, target comments.Target) ([]comments.Comment, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+columns+`
		FROM comments
		WHERE target_key = $1
		ORDER BY created_at, id`, target.Key())
	if err != nil {
		return nil, fmt.Errorf("pgcomments: list %q: %w", target.Key(), err)
	}
	defer rows.Close()

	// Non-nil even when empty: callers marshal this straight to JSON, where a
	// nil slice becomes `null` and an empty one becomes `[]`. A client telling
	// the two apart would see different shapes from different backends.
	out := []comments.Comment{}
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, fmt.Errorf("pgcomments: list %q: %w", target.Key(), err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgcomments: list %q: %w", target.Key(), err)
	}
	return out, nil
}

// Get returns one comment as a single-row read.
//
// Served by the `PRIMARY KEY (target_key, id)` index, so this is an index
// lookup rather than the thread scan List performs — which is the whole point
// of the method: authorizing an edit needs one author, not every body in the
// thread.
func (s *Store) Get(ctx context.Context, target comments.Target, id string) (comments.Comment, error) {
	row := s.db.QueryRow(ctx, `
		SELECT `+columns+`
		FROM comments
		WHERE target_key = $1 AND id = $2`, target.Key(), id)

	c, err := scanComment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return comments.Comment{}, comments.ErrNotFound
	}
	if err != nil {
		return comments.Comment{}, fmt.Errorf("pgcomments: get %q from %q: %w", id, target.Key(), err)
	}
	return c, nil
}

// Add inserts one comment.
//
// ID, Author and CreatedAt arrive already set by the service and are persisted
// as given rather than re-minted here, so the values in an audit trail and the
// values stored agree (see comments.Store.Add).
func (s *Store) Add(ctx context.Context, target comments.Target, c comments.Comment) error {
	anchor, err := json.Marshal(c.Anchor)
	if err != nil {
		return fmt.Errorf("pgcomments: encode anchor: %w", err)
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO comments
			(id, target_key, target_type, author, created_at, updated_at, anchor, body, resolved)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		c.ID, target.Key(), target.Type, c.Author, c.CreatedAt,
		zeroTimeToNil(c.UpdatedAt), anchor, c.Body, c.Resolved)
	if err != nil {
		return fmt.Errorf("pgcomments: add to %q: %w", target.Key(), err)
	}
	return nil
}

// Update replaces the mutable fields of one comment.
//
// The SET list is the whole mutable surface: body, resolved, and the edit
// timestamp. Author, created_at and anchor are absent on purpose — an edit must
// not be able to rewrite who said something or what it was about, or "edit your
// own comment" becomes a way to reattribute someone else's (pinned by
// commentstest.RunUpdateTests).
func (s *Store) Update(ctx context.Context, target comments.Target, id, body string, resolved bool) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE comments
		SET body = $3, resolved = $4, updated_at = $5
		WHERE target_key = $1 AND id = $2`,
		target.Key(), id, body, resolved, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("pgcomments: update %q on %q: %w", id, target.Key(), err)
	}
	if tag.RowsAffected() == 0 {
		return comments.ErrNotFound
	}
	return nil
}

// SetResolved flips the resolved flag only when it differs. The condition sits
// in the UPDATE's WHERE clause, so two processes racing on one row cannot both
// see a change: the second finds the flag already set and affects nothing.
func (s *Store) SetResolved(ctx context.Context, target comments.Target, id string, resolved bool) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE comments
		SET resolved = $3, updated_at = $4
		WHERE target_key = $1 AND id = $2 AND resolved <> $3`,
		target.Key(), id, resolved, time.Now().UTC())
	if err != nil {
		return false, fmt.Errorf("pgcomments: set resolved %q on %q: %w", id, target.Key(), err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	// No row changed: either it already held the value, or it is absent.
	if _, err := s.Get(ctx, target, id); err != nil {
		return false, err
	}
	return false, nil
}

// Delete removes one comment, reporting [comments.ErrNotFound] if absent.
func (s *Store) Delete(ctx context.Context, target comments.Target, id string) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM comments WHERE target_key = $1 AND id = $2`, target.Key(), id)
	if err != nil {
		return fmt.Errorf("pgcomments: delete %q from %q: %w", id, target.Key(), err)
	}
	if tag.RowsAffected() == 0 {
		return comments.ErrNotFound
	}
	return nil
}

// DeleteTarget removes one face's thread. Deleting an empty target is not an
// error, so no RowsAffected check here.
func (s *Store) DeleteTarget(ctx context.Context, target comments.Target) error {
	if _, err := s.db.Exec(ctx,
		`DELETE FROM comments WHERE target_key = $1`, target.Key()); err != nil {
		return fmt.Errorf("pgcomments: delete target %q: %w", target.Key(), err)
	}
	return nil
}

// DeleteAllFaces removes every thread belonging to an entity id.
//
// Both the bare id (the default face) and every "id@face" thread, because an
// entity delete takes the whole entity with it — leaving a faced thread behind
// would strand comments at an id nothing can reach.
func (s *Store) DeleteAllFaces(ctx context.Context, entityID string) error {
	if _, err := s.db.Exec(ctx, `
		DELETE FROM comments
		WHERE target_key = $1 OR target_key LIKE $2 ESCAPE '\'`,
		entityID, comments.FacePrefixPattern(entityID)); err != nil {
		return fmt.Errorf("pgcomments: delete all faces of %q: %w", entityID, err)
	}
	return nil
}

// Rename re-keys every one of an entity's threads.
//
// Two properties this must preserve, both pinned by
// commentstest.RunRenameTests:
//
// It moves EVERY face, not just the bare id. An entity with a draft keeps a
// thread per face, and re-keying only the default one strands the rest at an id
// that no longer exists.
//
// It MERGES into an occupied destination rather than replacing. rela permits id
// reuse, so the destination is not guaranteed empty, and discarding the
// occupant's comments would destroy data nobody asked to remove. The rows join
// one thread and List's ORDER BY interleaves them by timestamp.
//
// # Why this is a copy-then-delete rather than an UPDATE of the key
//
// A plain `UPDATE ... SET target_key` merges correctly right up until the
// destination already holds a comment with the same id, where it violates
// PRIMARY KEY (target_key, id) and aborts — moving NOTHING while the entity
// rename that triggered it has already committed. The comment id is minted
// from 80 bits of crypto/rand, so this needs a restored backup, a re-import or
// a filecomments migration to happen at all; it is rare, not impossible, and
// the consequence is bad out of proportion to its odds. entitymanager LOGS a
// failure from EntityRenamed rather than returning it, so the thread would be
// stranded under a key nothing resolves to, with no error reaching the user
// and no route left to reach the rows.
//
// INSERT ... ON CONFLICT DO NOTHING makes the collision a no-op instead: the
// destination's existing comment wins (the conservative choice — it is the one
// a reader may already have seen), and the rest of the thread still moves.
func (s *Store) Rename(ctx context.Context, oldID, newID string) error {
	if oldID == newID {
		return nil
	}
	// Two statements, so they need a transaction: a failure between them would
	// duplicate the whole thread across both keys.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgcomments: rename %q to %q: %w", oldID, newID, err)
	}
	// Rolls back unless Commit already succeeded, in which case it is a no-op.
	defer func() { _ = tx.Rollback(ctx) }()

	// The key is "id" or "id@face", so the new key is the new id plus whatever
	// followed the old one. Computed in SQL rather than by reading the rows
	// back into Go: this runs on the entity write path, and a read-then-write
	// would race a concurrent add to the same thread.
	//
	// char_length($1) rather than a Go-side len(oldID): substring() counts
	// CHARACTERS and Go's len() counts BYTES, so a multi-byte id would slice at
	// the wrong offset and re-key every comment to a corrupt target — losing
	// them outright, with a nil error. Today entity.ValidateID admits only
	// ASCII, so the two agree by luck; computing both sides in SQL means they
	// agree by construction, which is the same standard facePrefixPattern holds
	// itself to just below.
	// $1 is the old id, $2 the face pattern; the INSERT adds the new id as $3.
	const selection = `target_key = $1 OR target_key LIKE $2 ESCAPE '\'`
	pattern := comments.FacePrefixPattern(oldID)
	if _, err := tx.Exec(ctx, `
		INSERT INTO comments
			(id, target_key, target_type, author, created_at, updated_at, anchor, body, resolved)
		SELECT id, $3 || substring(target_key from char_length($1::text) + 1),
			target_type, author, created_at, updated_at, anchor, body, resolved
		FROM comments
		WHERE `+selection+`
		ON CONFLICT (target_key, id) DO NOTHING`,
		oldID, pattern, newID); err != nil {
		return fmt.Errorf("pgcomments: rename %q to %q: %w", oldID, newID, err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM comments WHERE `+selection, oldID, pattern); err != nil {
		return fmt.Errorf("pgcomments: rename %q to %q: %w", oldID, newID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pgcomments: rename %q to %q: %w", oldID, newID, err)
	}
	return nil
}

// zeroTimeToNil maps Go's zero time to SQL NULL.
//
// comments.Comment.UpdatedAt is the zero value until the first edit, and the
// column is nullable so "never edited" needs no sentinel instant. Storing the
// zero time instead would round-trip as year 1, which reads as a real edit.
func zeroTimeToNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// scanComment reads one row in [columns] order.
func scanComment(row pgx.Row) (comments.Comment, error) {
	var (
		c         comments.Comment
		updatedAt *time.Time
		anchor    []byte
	)
	if err := row.Scan(&c.ID, &c.Author, &c.CreatedAt, &updatedAt, &anchor, &c.Body, &c.Resolved); err != nil {
		return comments.Comment{}, err
	}
	// Normalized to UTC because pgx returns a TIMESTAMPTZ in time.Local, and
	// Comment marshals to JSON with whatever offset it carries. Left alone, one
	// comment would serialize as "+01:00" from this node and "Z" from a sqlite
	// one, and the offset would shift under the server's DST — a difference
	// clients see even though both describe the same instant. sqlitecomments
	// already returns UTC, so this is what makes the two agree.
	c.CreatedAt = c.CreatedAt.UTC()
	if updatedAt != nil {
		c.UpdatedAt = updatedAt.UTC()
	}
	if err := json.Unmarshal(anchor, &c.Anchor); err != nil {
		return comments.Comment{}, fmt.Errorf("decode anchor of %q: %w", c.ID, err)
	}
	return c, nil
}
