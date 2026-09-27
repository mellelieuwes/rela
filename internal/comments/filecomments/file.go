// Package filecomments stores comments as YAML, one file per target entity,
// under a directory the caller roots (conventionally `.rela/comments/`).
//
// This is the default backend, matching rela's file-first tier: a comment
// thread is readable, diffable and hand-editable like the entities it annotates.
//
// # Durability and concurrency
//
// A target's whole thread lives in one file, so a torn write would lose every
// comment on that entity rather than just the new one. Writes therefore go
// through [storage.SafeFS], which writes a temp file, fsyncs it, renames
// (atomic on POSIX) and fsyncs the parent directory.
//
// Every mutation is read-modify-write, so the store serializes writes with a
// single mutex. That is the same guarantee fsstore's Tx gives the entity store
// (DEC-8UIL0, "write mutex, mutual exclusion only"): it does not make a
// mutation transactional across targets, only free of lost updates. A
// cross-process writer is out of scope for this tier, exactly as it is for
// fsstore.
package filecomments

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/Sourcehaven-BV/rela/internal/comments"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/storage"
)

// filePerm is the mode for a comments file. Comments are no more sensitive
// than the entities they annotate, which fsstore writes 0644.
const filePerm = 0o644

// Store persists comment threads as per-target YAML files.
type Store struct {
	mu sync.Mutex
	// root is a RootedFS over a SafeFS, so a write is both contained
	// (traversal refused) and atomic (temp + fsync + rename).
	root *storage.RootedFS
}

// thread is the on-disk document.
//
// A struct rather than a bare slice so the format can gain fields (a schema
// version, say) without rewriting every stored file.
type thread struct {
	Comments []comments.Comment `yaml:"comments"`
}

// New returns a Store writing under root.
//
// root is created on first write, not here: an operator who never enables
// commenting should not find an empty directory in their project.
//
// Nil: base is required; it is the filesystem the store writes through.
func New(base storage.FS, root string) (*Store, error) {
	if base == nil {
		return nil, errors.New("filecomments: New requires a filesystem")
	}
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("filecomments: New requires a root directory")
	}
	// SafeFS first, then root it: RootedFS delegates writes to the FS it
	// wraps, so this order gives containment over atomic writes. The reverse
	// would give atomic writes over an uncontained filesystem.
	rooted, err := storage.NewRootedFS(storage.NewSafeFS(base), root)
	if err != nil {
		return nil, fmt.Errorf("filecomments: rooting at %q: %w", root, err)
	}
	return &Store{root: rooted}, nil
}

var _ comments.Store = (*Store)(nil)

// List returns the target's comments in the contract order.
func (s *Store) List(_ context.Context, target comments.Target) ([]comments.Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readThread(target.Key())
}

// Get returns one comment from the target's thread.
//
// Reads the whole thread and picks, which costs nothing here: a thread is a
// single YAML document, so reading one comment and reading all of them are the
// same file read. The database backends are the ones this distinction is for.
func (s *Store) Get(_ context.Context, target comments.Target, id string) (comments.Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	list, err := s.readThread(target.Key())
	if err != nil {
		return comments.Comment{}, err
	}
	for _, c := range list {
		if c.ID == id {
			return c, nil
		}
	}
	return comments.Comment{}, comments.ErrNotFound
}

// Add appends a comment to the target's thread.
func (s *Store) Add(_ context.Context, target comments.Target, c comments.Comment) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	list, err := s.readThread(target.Key())
	if err != nil {
		return err
	}
	return s.writeThread(target.Key(), append(list, c))
}

// Update replaces the mutable fields of one comment.
func (s *Store) Update(_ context.Context, target comments.Target, id, body string, resolved bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	list, err := s.readThread(target.Key())
	if err != nil {
		return err
	}
	for i := range list {
		if list[i].ID != id {
			continue
		}
		list[i].Body = body
		list[i].Resolved = resolved
		return s.writeThread(target.Key(), list)
	}
	return comments.ErrNotFound
}

// SetResolved flips the resolved flag only when it differs. The read-modify-
// write runs under s.mu, which makes it atomic within this process; the file
// backend is single-process, so that is the whole guarantee it needs.
func (s *Store) SetResolved(_ context.Context, target comments.Target, id string, resolved bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	list, err := s.readThread(target.Key())
	if err != nil {
		return false, err
	}
	for i := range list {
		if list[i].ID != id {
			continue
		}
		if list[i].Resolved == resolved {
			return false, nil
		}
		list[i].Resolved = resolved
		return true, s.writeThread(target.Key(), list)
	}
	return false, comments.ErrNotFound
}

// Delete removes one comment, dropping the file once its last comment goes so
// an emptied thread leaves no residue in the project tree.
func (s *Store) Delete(_ context.Context, target comments.Target, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	list, err := s.readThread(target.Key())
	if err != nil {
		return err
	}
	for i := range list {
		if list[i].ID != id {
			continue
		}
		remaining := make([]comments.Comment, 0, len(list)-1)
		remaining = append(remaining, list[:i]...)
		remaining = append(remaining, list[i+1:]...)
		if len(remaining) == 0 {
			return s.removeThread(target.Key())
		}
		return s.writeThread(target.Key(), remaining)
	}
	return comments.ErrNotFound
}

// DeleteTarget removes a target's whole thread.
func (s *Store) DeleteTarget(_ context.Context, target comments.Target) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeThread(target.Key())
}

// DeleteAllFaces removes every face's thread for an entity id.
func (s *Store) DeleteAllFaces(_ context.Context, entityID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	keys, err := s.threadKeysFor(entityID)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := s.removeThread(key); err != nil {
			return err
		}
	}
	return nil
}

// threadKeysFor lists the stored thread keys belonging to an entity id — the
// bare id plus any "id@face".
//
// Reads the directory rather than guessing at declared faces: the store must
// not know the metamodel, and a thread written under a face the schema has
// since dropped still has to be reachable for cleanup.
//
// Callers must hold s.mu.
func (s *Store) threadKeysFor(entityID string) ([]string, error) {
	// WalkAll rather than ReadDir("."): the rooted FS refuses "." as a key
	// (it reads as an empty/traversal segment), and WalkAll is the method that
	// exists for walking the root itself.
	var out []string
	err := s.root.WalkAll(func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		key, ok := strings.CutSuffix(path, ".yaml")
		if !ok {
			return nil
		}
		if key == entityID || strings.HasPrefix(key, entityID+entity.StateRefSeparator) {
			out = append(out, key)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("filecomments: listing threads: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

// Rename re-keys a thread, merging into any thread already at newID.
//
// Merging rather than replacing because rela permits ID reuse, so the
// destination is not guaranteed empty — and silently discarding the occupant's
// comments would destroy data nobody asked to remove.
func (s *Store) Rename(_ context.Context, oldID, newID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if oldID == newID {
		return nil
	}
	// Move EVERY face's thread: an entity with a draft and a published face has
	// a thread per face, and re-keying only the default one would strand the
	// rest at an id that no longer exists.
	keys, err := s.threadKeysFor(oldID)
	if err != nil {
		return err
	}
	for _, key := range keys {
		moving, err := s.readThread(key)
		if err != nil {
			return err
		}
		if len(moving) == 0 {
			continue
		}
		dest := newID + strings.TrimPrefix(key, oldID)
		existing, err := s.readThread(dest)
		if err != nil {
			return err
		}
		if err := s.writeThread(dest, comments.MergeThreads(existing, moving)); err != nil {
			return err
		}
		if err := s.removeThread(key); err != nil {
			return err
		}
	}
	return nil
}

// readThread loads and orders one target's comments. A missing file is an
// empty thread, not an error: "no comments yet" and "never commented on" are
// the same state to every caller.
//
// Callers must hold s.mu.
func (s *Store) readThread(targetID string) ([]comments.Comment, error) {
	name, err := threadFile(targetID)
	if err != nil {
		return nil, err
	}
	data, err := s.root.ReadFile(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			return []comments.Comment{}, nil
		}
		return nil, fmt.Errorf("filecomments: reading %s: %w", name, err)
	}
	var doc thread
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("filecomments: parsing %s: %w", name, err)
	}
	if doc.Comments == nil {
		doc.Comments = []comments.Comment{}
	}
	comments.SortComments(doc.Comments)
	return doc.Comments, nil
}

// writeThread persists a target's comments atomically.
//
// Callers must hold s.mu.
func (s *Store) writeThread(targetID string, list []comments.Comment) error {
	name, err := threadFile(targetID)
	if err != nil {
		return err
	}
	comments.SortComments(list)
	data, err := yaml.Marshal(thread{Comments: list})
	if err != nil {
		return fmt.Errorf("filecomments: encoding %s: %w", name, err)
	}
	if err := s.root.WriteFile(name, data, filePerm); err != nil {
		return fmt.Errorf("filecomments: writing %s: %w", name, err)
	}
	return nil
}

// removeThread deletes a target's file, treating an absent file as success.
//
// Callers must hold s.mu.
func (s *Store) removeThread(targetID string) error {
	name, err := threadFile(targetID)
	if err != nil {
		return err
	}
	if err := s.root.Remove(name); err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("filecomments: removing %s: %w", name, err)
	}
	return nil
}

// threadFile maps a target entity ID to its file name.
//
// The ID is validated here as well as at the HTTP boundary. RootedFS already
// refuses traversal, so this is defense in depth rather than the only guard —
// but it is cheap, and it keeps a non-HTTP caller (a future CLI command) from
// reaching the filesystem with an unchecked id.
func threadFile(targetID string) (string, error) {
	if !safeID(targetID) {
		return "", fmt.Errorf("filecomments: unsafe target id %q", targetID)
	}
	return targetID + ".yaml", nil
}

// safeID reports whether a thread KEY is usable as a single path segment.
//
// The key is `entity.FormatStateRef` output: a bare id for the default face,
// "id@face" otherwise.
//
// Allowlist: entity IDs are generated from a base36 alphabet with an operator
// prefix, or are manual IDs the metamodel already constrains, so the permitted
// set is deliberately narrow. Leading dots are refused so no id can produce a
// hidden file.
func safeID(id string) bool {
	if id == "" || strings.HasPrefix(id, ".") {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		// '@' joins an id to its face in the boundary serialization
		// ("PAGE-1@draft", entity.StateRefSeparator). Filename-legal, excluded
		// from the entity-ID grammar, and the reason a per-face thread needs no
		// separate directory level.
		case r == '@':
		default:
			return false
		}
	}
	return true
}
