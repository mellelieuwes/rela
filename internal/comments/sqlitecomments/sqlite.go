// Package sqlitecomments is the SQLite-backed [comments.Store].
//
// The sqlite tier is single-process by construction (sqlitestore takes an
// exclusive sidecar lock, DEC-LFSYNY), so this backend is NOT here to fix a
// multi-writer bug the way pgcomments is. It exists so commentary travels with
// the rows it annotates.
//
// That is the same call content versioning made for this tier (TKT-4NU9ZD) and
// the opposite of the one state.KV made (TKT-L1A3PH), and the difference is the
// point: node-local state buys nothing a single process can observe, so the
// render cache stays in .rela/. But a comment is content ABOUT content — an
// operator who copies or ships rela.db expecting "the project" would find the
// entities present and every remark on them silently left behind in a directory
// they did not know to bring.
//
// # Relationship to the store
//
// This package does not depend on internal/store (arch-lint enforces it); see
// the pgcomments package doc for why commentary stays outside the graph. It
// shares the *sql.DB that sqlitedb already exposes for precisely this purpose
// ("config stored in this database can be read before a store exists and the
// two can share one connection" — sqlitedb.DB.DB), so there is one connection
// pool and one single-writer lock over the file, not two.
package sqlitecomments

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/comments"
	"github.com/Sourcehaven-BV/rela/internal/entity"
)

// timeFmt is the on-disk timestamp format.
//
// Deliberately NOT sqlitestore.timeFmt (RFC3339Nano), despite the two tables
// living in one file. List orders by this column, and the ordering happens in
// SQL as a STRING compare — which RFC3339Nano gets WRONG, because Go strips
// trailing zeros from the fractional part and omits it entirely at a whole
// second. So "12:00:00Z" sorts AFTER "12:00:00.000000005Z" ('Z' is 0x5A, '.'
// is 0x2E), and a thread would silently reorder between reads.
//
// This format is fixed-width: always nine fractional digits, zero-padded, so
// lexical and chronological order coincide. The store's own columns are not
// changed here — that is a separate concern (its sweep does the same string
// comparison against time windows, where the same flaw is a missed capture
// rather than a visible reordering).
const timeFmt = "2006-01-02T15:04:05.000000000Z07:00"

// DBTX is the database handle this store runs on: the *sql.DB owned by
// sqlitedb.DB in production.
//
// An interface rather than *sql.DB so a caller may supply a transaction or a
// wrapper, and so this package names no concrete database type it does not own.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	// BeginTx is needed by Rename alone, which is two statements that must not
	// be observable apart. pgcomments.DBTX carries the same capability, so the
	// two backends ask for the same thing.
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// Store is the SQLite comment backend.
type Store struct {
	db DBTX
}

var _ comments.Store = (*Store)(nil)

// New constructs a Store over the given handle.
//
// Nil: rejected, so a wiring mistake fails at construction rather than at the
// first comment anyone posts (CLAUDE.md: constructors reject nil required
// collaborators).
func New(db DBTX) (*Store, error) {
	if db == nil {
		return nil, errors.New("sqlitecomments: New requires a database handle")
	}
	return &Store{db: db}, nil
}

// columns is the read projection, ordered to match scanComment.
const columns = `id, author, created_at, updated_at, anchor, body, resolved`

// List returns the target's thread in contract order: oldest first, the
// server-minted id breaking ties so a coarse clock cannot make a thread
// reorder between reads.
func (s *Store) List(ctx context.Context, target comments.Target) ([]comments.Comment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+columns+`
		FROM comments
		WHERE target_key = ?
		ORDER BY created_at, id`, target.Key())
	if err != nil {
		return nil, fmt.Errorf("sqlitecomments: list %q: %w", target.Key(), err)
	}
	defer rows.Close()

	// Non-nil even when empty: callers marshal straight to JSON, where a nil
	// slice becomes `null` and an empty one `[]`.
	out := []comments.Comment{}
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlitecomments: list %q: %w", target.Key(), err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlitecomments: list %q: %w", target.Key(), err)
	}
	return out, nil
}

// Get returns one comment as a single-row read.
//
// Served by the `PRIMARY KEY (target_key, id)` index, so this is a lookup
// rather than the thread scan List performs. Both halves of the key are matched
// with `=`, which is byte-exact in SQLite — unlike LIKE, whose ASCII
// case-folding the face-prefix queries in this file have to defend against.
func (s *Store) Get(ctx context.Context, target comments.Target, id string) (comments.Comment, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+columns+`
		FROM comments
		WHERE target_key = ? AND id = ?`, target.Key(), id)

	c, err := scanComment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return comments.Comment{}, comments.ErrNotFound
	}
	if err != nil {
		return comments.Comment{}, fmt.Errorf("sqlitecomments: get %q from %q: %w", id, target.Key(), err)
	}
	return c, nil
}

// Add inserts one comment, persisting the service-supplied ID, Author and
// CreatedAt as given.
func (s *Store) Add(ctx context.Context, target comments.Target, c comments.Comment) error {
	anchor, err := json.Marshal(c.Anchor)
	if err != nil {
		return fmt.Errorf("sqlitecomments: encode anchor: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO comments
			(id, target_key, target_type, author, created_at, updated_at, anchor, body, resolved)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, target.Key(), target.Type, c.Author,
		c.CreatedAt.UTC().Format(timeFmt), formatOptionalTime(c.UpdatedAt),
		string(anchor), c.Body, c.Resolved)
	if err != nil {
		return fmt.Errorf("sqlitecomments: add to %q: %w", target.Key(), err)
	}
	return nil
}

// Update replaces the mutable fields of one comment.
//
// Author, created_at and anchor are absent from the SET list on purpose: an
// edit must not rewrite who said something or what it was about.
func (s *Store) Update(ctx context.Context, target comments.Target, id, body string, resolved bool) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE comments
		SET body = ?, resolved = ?, updated_at = ?
		WHERE target_key = ? AND id = ?`,
		body, resolved, time.Now().UTC().Format(timeFmt), target.Key(), id)
	if err != nil {
		return fmt.Errorf("sqlitecomments: update %q on %q: %w", id, target.Key(), err)
	}
	return requireAffected(res, comments.ErrNotFound)
}

// SetResolved flips the resolved flag only when it differs, with the condition
// in the UPDATE's WHERE clause so the check and the write are one statement.
func (s *Store) SetResolved(ctx context.Context, target comments.Target, id string, resolved bool) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE comments
		SET resolved = ?, updated_at = ?
		WHERE target_key = ? AND id = ? AND resolved <> ?`,
		resolved, time.Now().UTC().Format(timeFmt), target.Key(), id, resolved)
	if err != nil {
		return false, fmt.Errorf("sqlitecomments: set resolved %q on %q: %w", id, target.Key(), err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("sqlitecomments: rows affected: %w", err)
	}
	if n > 0 {
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
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM comments WHERE target_key = ? AND id = ?`, target.Key(), id)
	if err != nil {
		return fmt.Errorf("sqlitecomments: delete %q from %q: %w", id, target.Key(), err)
	}
	return requireAffected(res, comments.ErrNotFound)
}

// DeleteTarget removes one face's thread. An empty target is not an error.
func (s *Store) DeleteTarget(ctx context.Context, target comments.Target) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM comments WHERE target_key = ?`, target.Key()); err != nil {
		return fmt.Errorf("sqlitecomments: delete target %q: %w", target.Key(), err)
	}
	return nil
}

// DeleteAllFaces removes every thread belonging to an entity id — the bare id
// and every "id@face" — so an entity delete strands nothing.
func (s *Store) DeleteAllFaces(ctx context.Context, entityID string) error {
	args := faceArgs(entityID)
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM comments WHERE `+selectFaces, args...); err != nil {
		return fmt.Errorf("sqlitecomments: delete all faces of %q: %w", entityID, err)
	}
	return nil
}

// Rename re-keys every one of an entity's threads.
//
// Moves EVERY face (re-keying only the bare id would strand a draft thread at
// an id that no longer exists) and MERGES into an occupied destination, since
// rela permits id reuse and discarding the occupant's comments would destroy
// data nobody asked to remove. Both pinned by commentstest.RunRenameTests.
// A copy-then-delete rather than an UPDATE of the key, and a character-counted
// offset rather than a Go-side byte count. See the pgcomments.Store.Rename doc
// for both reasons in full — the hazards are identical and the two backends are
// held to one answer by commentstest.RunRenameTests.
func (s *Store) Rename(ctx context.Context, oldID, newID string) error {
	if oldID == newID {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlitecomments: rename %q to %q: %w", oldID, newID, err)
	}
	// Rolls back unless Commit already succeeded, in which case it is a no-op.
	defer func() { _ = tx.Rollback() }()

	// length() counts CHARACTERS, matching substr(); Go's len() counts bytes,
	// so computing the offset here keeps the two units agreeing by
	// construction rather than by the id grammar happening to be ASCII.
	args := faceArgs(oldID)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO comments
			(id, target_key, target_type, author, created_at, updated_at, anchor, body, resolved)
		SELECT id, ? || substr(target_key, length(?) + 1),
			target_type, author, created_at, updated_at, anchor, body, resolved
		FROM comments
		WHERE `+selectFaces+`
		ON CONFLICT (target_key, id) DO NOTHING`,
		append([]any{newID, oldID}, args...)...); err != nil {
		return fmt.Errorf("sqlitecomments: rename %q to %q: %w", oldID, newID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM comments WHERE `+selectFaces, args...); err != nil {
		return fmt.Errorf("sqlitecomments: rename %q to %q: %w", oldID, newID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlitecomments: rename %q to %q: %w", oldID, newID, err)
	}
	return nil
}

// selectFaces builds the WHERE clause matching every thread of an entity id —
// the bare id and every "id@face" — with its bound arguments.
//
// The substr() guard beside the LIKE is what makes the match case-EXACT, and
// it is load-bearing rather than belt-and-braces. SQLite's LIKE is ASCII
// case-insensitive by default while "=" is byte-exact, so without it the two
// arms of this clause would match different row sets: renaming "tkt-1" would
// also re-key "TKT-1", merging two entities' threads, and DeleteAllFaces would
// delete an entity the caller never named. PostgreSQL does not do this (its
// column is COLLATE "C"), so the guard is also what keeps the two backends
// answering alike — pinned by commentstest.RunRenameTests.
//
// Fixed here rather than with COLLATE or a PRAGMA: collation does not affect
// LIKE, and `PRAGMA case_sensitive_like` is global, so it would silently change
// sqlitestore's queries, which rely on the folding deliberately.
//
// The LIKE stays because it is what the index serves; substr() only filters the
// handful of rows LIKE already narrowed to.
const selectFaces = `(target_key = ? OR ` +
	`(target_key LIKE ? ESCAPE '\' AND substr(target_key, 1, ?) = ?))`

// faceArgs binds [selectFaces] for one entity id.
func faceArgs(id string) []any {
	prefix := id + entity.StateRefSeparator
	return []any{id, comments.FacePrefixPattern(id), len([]rune(prefix)), prefix}
}

// formatOptionalTime maps Go's zero time to SQL NULL, so "never edited" needs
// no sentinel instant. Storing the zero time would round-trip as year 1, which
// reads as a real edit.
func formatOptionalTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(timeFmt)
}

// requireAffected turns a zero-row write into notFound.
//
// SQLite reports RowsAffected reliably for these statements; an error from it
// is a driver failure, not a missing row, so it is returned as-is rather than
// folded into notFound.
func requireAffected(res sql.Result, notFound error) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlitecomments: rows affected: %w", err)
	}
	if n == 0 {
		return notFound
	}
	return nil
}

// rowScanner is the one method scanComment needs, satisfied by both *sql.Rows
// and *sql.Row — so the thread read and the single-row read share one decoder
// rather than keeping two copies of the timestamp parsing and anchor decoding
// in step.
//
// Named rowScanner, not scanner, because [sql.Scanner] is a different interface
// entirely (`Scan(src any) error`, implemented by destination types) and this
// file imports database/sql.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanComment reads one row in [columns] order.
func scanComment(row rowScanner) (comments.Comment, error) {
	var (
		c        comments.Comment
		created  string
		updated  *string
		anchor   string
		resolved bool
	)
	if err := row.Scan(&c.ID, &c.Author, &created, &updated, &anchor, &c.Body, &resolved); err != nil {
		return comments.Comment{}, err
	}
	c.Resolved = resolved

	t, err := time.Parse(timeFmt, created)
	if err != nil {
		return comments.Comment{}, fmt.Errorf("parse created_at of %q: %w", c.ID, err)
	}
	c.CreatedAt = t

	if updated != nil {
		u, err := time.Parse(timeFmt, *updated)
		if err != nil {
			return comments.Comment{}, fmt.Errorf("parse updated_at of %q: %w", c.ID, err)
		}
		c.UpdatedAt = u
	}
	if err := json.Unmarshal([]byte(anchor), &c.Anchor); err != nil {
		return comments.Comment{}, fmt.Errorf("decode anchor of %q: %w", c.ID, err)
	}
	return c, nil
}
