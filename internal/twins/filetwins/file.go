// Package filetwins stores twins as YAML, one file per twin, under a directory
// the caller roots (conventionally `.rela/twins/`):
//
//	<root>/<system>/<external-id>.yaml     one twin
//	<root>/_targets/<entity-id>            the twins of one entity, "system/external-id" per line
//	<root>/.lock                           the cross-process write lock
//
// This is the default backend, matching rela's file-first tier: a twin is
// readable, diffable and hand-editable like the entities it mirrors.
//
// # File names
//
// External and entity ids are case-sensitive, but a macOS or Windows
// filesystem is not, so "ABC" and "abc" must not share a file. A name
// therefore spells each uppercase letter as '^' followed by its lowercase
// ("ABC" → "^a^b^c"); '^' is outside both id grammars, so the encoding is
// unambiguous and every name is lowercase on disk.
//
// # Durability and concurrency
//
// Writes go through [storage.SafeFS] (temp file, fsync, rename, fsync of the
// directory), so a crash never leaves a torn file. Every read-modify-write
// (the identity checks, the index update, Modify's decision, Retarget) runs
// under an exclusive flock on `.lock`, so the CLI and a running server
// serialize against each other. Reads take no lock: each file is replaced
// atomically.
//
// Nothing is cached — every read goes to disk, so a twin written by another
// process is seen at once. ForTarget, which the write guard calls on every
// entity write, reads the entity's index file and its twins instead of the
// whole tree. The index is written BEFORE a twin is added under it and after
// one is removed, so a crash can leave an index line whose twin is missing or
// no longer targets the entity (ignored on read), never a twin its entity's
// index does not list — which would silently drop the ownership it enforces.
package filetwins

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Sourcehaven-BV/rela/internal/storage"
	"github.com/Sourcehaven-BV/rela/internal/twins"
)

const (
	// filePerm is the mode for every file the store writes. A twin is no more
	// sensitive than the entity it mirrors, which fsstore writes 0644.
	filePerm = 0o644

	// fileExt is the extension of a twin file. SafeFS's temp files end in
	// ".yaml.tmp", so matching on it also skips an interrupted write.
	fileExt = ".yaml"

	// targetsDir holds the per-entity index. A system id starts with a
	// letter, so no system directory can collide with it.
	targetsDir = "_targets"

	lockName = ".lock"

	// maxSystemLen bounds a system directory name, as the metamodel's pact
	// system grammar does.
	maxSystemLen = 32

	// maxNameLen bounds an encoded file name below every filesystem's limit.
	maxNameLen = 255

	// encodeSlack is the room encodeName reserves for '^' escapes beyond the
	// id's own length, so an id with a few uppercase letters never regrows.
	encodeSlack = 8
)

// Store persists twins as per-twin YAML files.
type Store struct {
	mu sync.Mutex
	// root is a RootedFS over a SafeFS, so a write is both contained
	// (traversal refused) and atomic (temp + fsync + rename).
	root *storage.RootedFS
	// lockPath is the absolute path of the cross-process flock file.
	lockPath string
}

// twinFile is the on-disk document. System and external id are the file's
// path, not its content, so a renamed file cannot disagree with itself.
type twinFile struct {
	URL             string         `yaml:"url,omitempty"`
	Target          targetFile     `yaml:"target"`
	State           twins.State    `yaml:"state"`
	HasBase         bool           `yaml:"has_base,omitempty"`
	Base            twins.Snapshot `yaml:"base"`
	BaseVersion     string         `yaml:"base_version,omitempty"`
	SyncedAt        time.Time      `yaml:"synced_at,omitempty"`
	RemoteUpdatedAt time.Time      `yaml:"remote_updated_at,omitempty"`
	Findings        []findingFile  `yaml:"findings,omitempty"`
}

type targetFile struct {
	Type string `yaml:"type"`
	ID   string `yaml:"id"`
}

type findingFile struct {
	Field   string            `yaml:"field,omitempty"`
	Kind    twins.FindingKind `yaml:"kind"`
	Base    any               `yaml:"base"`
	Ours    any               `yaml:"ours"`
	Theirs  any               `yaml:"theirs"`
	Propose bool              `yaml:"propose,omitempty"`
	Message string            `yaml:"message,omitempty"`
}

// New returns a Store writing under root.
//
// root is created on first write, not here: a project that declares no pact
// should not find an empty directory in its tree.
//
// Nil: base is required; it is the filesystem the store writes through, and
// it must be the OS's — atomic writes (SafeFS) and the cross-process lock both
// go through os, so over an in-memory filesystem they would touch the real
// disk behind its back. Such a base is refused.
func New(base storage.FS, root string) (*Store, error) {
	if base == nil {
		return nil, errors.New("filetwins: New requires a filesystem")
	}
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("filetwins: New requires a root directory")
	}
	// SafeFS first, then root it: RootedFS delegates writes to the FS it
	// wraps, so this order gives containment over atomic writes.
	rooted, err := storage.NewRootedFS(storage.NewSafeFS(base), root)
	if err != nil {
		return nil, fmt.Errorf("filetwins: rooting at %q: %w", root, err)
	}
	if !rooted.SupportsStreaming() { // true exactly for an OS-backed stack
		return nil, errors.New("filetwins: New requires the OS filesystem")
	}
	lockPath, err := rooted.AbsPath(lockName)
	if err != nil {
		return nil, fmt.Errorf("filetwins: lock path: %w", err)
	}
	return &Store{root: rooted, lockPath: lockPath}, nil
}

var _ twins.Store = (*Store)(nil)

// Get returns one twin.
func (s *Store) Get(_ context.Context, system, externalID string) (twins.Twin, error) {
	name, err := twinFileName(system, externalID)
	if err != nil {
		return twins.Twin{}, err
	}
	return s.read(name, system, externalID)
}

// ForTarget returns every twin of an entity, sorted by system.
func (s *Store) ForTarget(_ context.Context, entityID string) ([]twins.Twin, error) {
	return s.targetTwins(entityID)
}

// List returns the twins f matches in the contract order. A system filter
// reads only that system's directory.
func (s *Store) List(_ context.Context, f twins.Filter) ([]twins.Twin, error) {
	if f.System != "" && !validSystem(f.System) {
		return []twins.Twin{}, nil
	}
	all, err := s.readTree(f.System)
	if err != nil {
		return nil, err
	}
	out := []twins.Twin{}
	for _, tw := range all {
		if f.Matches(tw) {
			out = append(out, tw)
		}
	}
	return out, nil
}

// Create stores a new twin under the identity rules of [twins.Store.Create].
func (s *Store) Create(_ context.Context, tw twins.Twin) error {
	name, err := twinFileName(tw.System, tw.ExternalID)
	if err != nil {
		return err
	}
	return s.locked(func() error {
		if _, err := s.root.Stat(name); err == nil {
			return twins.ErrExternalIDTaken
		} else if !isNotExist(err) {
			return fmt.Errorf("filetwins: checking %s: %w", name, err)
		}
		if err := s.checkDuplicate(tw); err != nil {
			return err
		}
		if err := s.indexAdd(tw.Target.ID, tw); err != nil {
			return err
		}
		return s.write(name, tw)
	})
}

// Update replaces an existing twin.
func (s *Store) Update(ctx context.Context, tw twins.Twin) error {
	_, err := s.Modify(ctx, tw.System, tw.ExternalID, func(twins.Twin) (twins.Twin, error) { return tw, nil })
	return err
}

// Modify runs fn on the twin as stored and writes its result, both under the
// cross-process lock, so a decision fn makes cannot be overtaken by a write
// from another process.
func (s *Store) Modify(
	_ context.Context, system, externalID string, fn func(twins.Twin) (twins.Twin, error),
) (twins.Twin, error) {
	name, err := twinFileName(system, externalID)
	if err != nil {
		return twins.Twin{}, err
	}
	var next twins.Twin
	if err := s.locked(func() error {
		stored, err := s.read(name, system, externalID)
		if err != nil {
			return err
		}
		if next, err = fn(stored); err != nil {
			return err
		}
		if err := twins.CheckReplace(stored, next); err != nil {
			return err
		}
		if err := s.checkDuplicate(next); err != nil {
			return err
		}
		// The target cannot change here, so its index already lists the twin;
		// re-asserting the line costs a read and repairs an index a hand edit
		// of the twin's target left behind.
		if err := s.indexAdd(next.Target.ID, next); err != nil {
			return err
		}
		return s.write(name, next)
	}); err != nil {
		return twins.Twin{}, err
	}
	return next, nil
}

// Delete removes one twin, then its index line.
func (s *Store) Delete(_ context.Context, system, externalID string) error {
	name, err := twinFileName(system, externalID)
	if err != nil {
		return err
	}
	return s.locked(func() error {
		old, err := s.read(name, system, externalID)
		if err != nil {
			return err
		}
		if err := s.root.Remove(name); err != nil {
			return fmt.Errorf("filetwins: removing %s: %w", name, err)
		}
		return s.indexRemove(old.Target.ID, old)
	})
}

// Retarget moves every twin of oldID to newID. The collision check runs
// before any write, so a refused retarget changes nothing; each file is
// replaced atomically, and the index order keeps a crash midway recoverable
// (see the package doc).
func (s *Store) Retarget(_ context.Context, oldID, newID string) error {
	if oldID == newID {
		return nil
	}
	return s.locked(func() error {
		moving, err := s.targetTwins(oldID)
		if err != nil || len(moving) == 0 {
			return err
		}
		occupants, err := s.targetTwins(newID)
		if err != nil {
			return err
		}
		for _, tw := range moving {
			for _, o := range occupants {
				if tw.Live() && o.Live() && o.System == tw.System {
					return twins.ErrDuplicateTarget
				}
			}
		}
		for _, tw := range moving {
			if err := s.indexAdd(newID, tw); err != nil {
				return err
			}
		}
		for _, tw := range moving {
			tw.Target.ID = newID
			name, err := twinFileName(tw.System, tw.ExternalID)
			if err != nil {
				return err
			}
			if err := s.write(name, tw); err != nil {
				return err
			}
		}
		return s.removeIndex(oldID)
	})
}

// locked runs fn holding the cross-process flock (and the Store's mutex, which
// queues its own goroutines before they each open the lock file). The lock
// file lives in root, so taking it is what first creates the directory.
func (s *Store) locked(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.lockPath), 0o755); err != nil {
		return fmt.Errorf("filetwins: creating %s: %w", filepath.Dir(s.lockPath), err)
	}
	f, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, filePerm)
	if err != nil {
		return fmt.Errorf("filetwins: opening lock: %w", err)
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return fmt.Errorf("filetwins: taking lock: %w", err)
	}
	defer func() { _ = unlockFile(f) }()
	return fn()
}

// checkDuplicate refuses tw when it is live and its entity already has another
// live twin in its system. Callers hold the lock.
func (s *Store) checkDuplicate(tw twins.Twin) error {
	if !tw.Live() {
		return nil
	}
	siblings, err := s.targetTwins(tw.Target.ID)
	if err != nil {
		return err
	}
	for _, o := range siblings {
		if o.System == tw.System && o.ExternalID != tw.ExternalID && o.Live() {
			return twins.ErrDuplicateTarget
		}
	}
	return nil
}

// targetTwins reads an entity's index and the twins it lists, sorted by
// system. Lines whose twin is missing or targets another entity are the
// residue of an interrupted write and are skipped. An id no file name can
// hold has no twins: Create refuses it.
func (s *Store) targetTwins(entityID string) ([]twins.Twin, error) {
	out := []twins.Twin{}
	lines, err := s.readIndex(entityID)
	if err != nil {
		return nil, err
	}
	for _, line := range lines {
		system, externalID, _ := strings.Cut(line, "/")
		name, err := twinFileName(system, externalID)
		if err != nil {
			continue
		}
		tw, err := s.read(name, system, externalID)
		switch {
		case errors.Is(err, twins.ErrNotFound):
			continue
		case err != nil:
			return nil, err
		case tw.Target.ID == entityID:
			out = append(out, tw)
		}
	}
	twins.SortTwins(out)
	return out, nil
}

// readIndex returns an entity's index lines; a missing index is empty.
func (s *Store) readIndex(entityID string) ([]string, error) {
	name, ok := indexFileName(entityID)
	if !ok {
		return nil, nil
	}
	data, err := s.root.ReadFile(name)
	if err != nil {
		if isNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("filetwins: reading %s: %w", name, err)
	}
	return strings.Fields(string(data)), nil
}

// indexAdd lists tw under entityID, writing only when the line is new.
// Callers hold the lock.
func (s *Store) indexAdd(entityID string, tw twins.Twin) error {
	lines, err := s.readIndex(entityID)
	if err != nil {
		return err
	}
	line := tw.System + "/" + tw.ExternalID
	if slices.Contains(lines, line) {
		return nil
	}
	return s.writeIndex(entityID, append(lines, line))
}

// indexRemove drops tw's line from entityID's index. Callers hold the lock.
func (s *Store) indexRemove(entityID string, tw twins.Twin) error {
	lines, err := s.readIndex(entityID)
	if err != nil {
		return err
	}
	line := tw.System + "/" + tw.ExternalID
	kept := slices.DeleteFunc(lines, func(l string) bool { return l == line })
	return s.writeIndex(entityID, kept)
}

// writeIndex persists an index, removing the file once it lists nothing so an
// unlinked entity leaves no residue. Callers hold the lock.
func (s *Store) writeIndex(entityID string, lines []string) error {
	if len(lines) == 0 {
		return s.removeIndex(entityID)
	}
	name, ok := indexFileName(entityID)
	if !ok {
		return fmt.Errorf("filetwins: entity id %q cannot be stored", entityID)
	}
	slices.Sort(lines)
	if err := s.root.WriteFile(name, []byte(strings.Join(lines, "\n")+"\n"), filePerm); err != nil {
		return fmt.Errorf("filetwins: writing %s: %w", name, err)
	}
	return nil
}

// removeIndex deletes an entity's index, treating an absent one as removed.
func (s *Store) removeIndex(entityID string) error {
	name, ok := indexFileName(entityID)
	if !ok {
		return nil
	}
	if err := s.root.Remove(name); err != nil && !isNotExist(err) {
		return fmt.Errorf("filetwins: removing %s: %w", name, err)
	}
	return nil
}

// readTree loads every twin under system's directory, or under the whole root
// when system is empty, in the contract order. A missing directory is no
// twins, not an error. Files that are not twins this store could have written
// (a stray note, an interrupted temp file) are skipped.
func (s *Store) readTree(system string) ([]twins.Twin, error) {
	out := []twins.Twin{}
	visit := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == targetsDir {
				return fs.SkipDir
			}
			return nil
		}
		sys, externalID, ok := parseTwinPath(path)
		if !ok {
			return nil
		}
		tw, err := s.read(path, sys, externalID)
		if err != nil {
			return err
		}
		out = append(out, tw)
		return nil
	}
	var err error
	if system == "" {
		err = s.root.WalkAll(visit)
	} else {
		err = s.root.Walk(system, visit)
	}
	if err != nil && !isNotExist(err) {
		return nil, fmt.Errorf("filetwins: listing twins: %w", err)
	}
	twins.SortTwins(out)
	return out, nil
}

// read loads one twin file. A missing file is [twins.ErrNotFound]; a file that
// does not parse is an error, not an absent twin — silently dropping a twin
// would silently drop the ownership it enforces.
func (s *Store) read(name, system, externalID string) (twins.Twin, error) {
	data, err := s.root.ReadFile(name)
	if err != nil {
		if isNotExist(err) {
			return twins.Twin{}, twins.ErrNotFound
		}
		return twins.Twin{}, fmt.Errorf("filetwins: reading %s: %w", name, err)
	}
	var doc twinFile
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return twins.Twin{}, fmt.Errorf("filetwins: parsing %s: %w", name, err)
	}
	tw := twins.Twin{
		System:          system,
		ExternalID:      externalID,
		URL:             doc.URL,
		Target:          twins.Target{Type: doc.Target.Type, ID: doc.Target.ID},
		State:           doc.State,
		HasBase:         doc.HasBase,
		Base:            doc.Base,
		BaseVersion:     doc.BaseVersion,
		SyncedAt:        doc.SyncedAt,
		RemoteUpdatedAt: doc.RemoteUpdatedAt,
	}
	for _, f := range doc.Findings {
		tw.Findings = append(tw.Findings, twins.Finding{
			Field: f.Field, Kind: f.Kind, Base: f.Base, Ours: f.Ours, Theirs: f.Theirs,
			Propose: f.Propose, Message: f.Message,
		})
	}
	return tw, nil
}

// write persists one twin atomically. Callers hold the lock.
func (s *Store) write(name string, tw twins.Twin) error {
	doc := twinFile{
		URL:             tw.URL,
		Target:          targetFile{Type: tw.Target.Type, ID: tw.Target.ID},
		State:           tw.State,
		HasBase:         tw.HasBase,
		Base:            tw.Base,
		BaseVersion:     tw.BaseVersion,
		SyncedAt:        tw.SyncedAt,
		RemoteUpdatedAt: tw.RemoteUpdatedAt,
	}
	for _, f := range tw.Findings {
		doc.Findings = append(doc.Findings, findingFile{
			Field: f.Field, Kind: f.Kind, Base: f.Base, Ours: f.Ours, Theirs: f.Theirs,
			Propose: f.Propose, Message: f.Message,
		})
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("filetwins: encoding %s: %w", name, err)
	}
	if err := s.root.WriteFile(name, data, filePerm); err != nil {
		return fmt.Errorf("filetwins: writing %s: %w", name, err)
	}
	return nil
}

// twinFileName maps a twin's key to its root-relative file name.
//
// Both halves are validated here as well as upstream (the metamodel checks a
// pact's system, the service checks the external id). RootedFS already
// refuses traversal, so this is defense in depth — but it is cheap, and it
// keeps a caller that skipped the service from reaching the filesystem with
// an unchecked name.
func twinFileName(system, externalID string) (string, error) {
	if !validSystem(system) {
		return "", fmt.Errorf("filetwins: unsafe system %q", system)
	}
	if err := twins.ValidateExternalID(externalID); err != nil {
		return "", err
	}
	return system + "/" + encodeName(externalID) + fileExt, nil
}

// parseTwinPath splits a root-relative "system/encoded-id.yaml" key, rejecting
// anything twinFileName could not have produced.
func parseTwinPath(path string) (system, externalID string, ok bool) {
	system, file, found := strings.Cut(path, "/")
	if !found || !validSystem(system) {
		return "", "", false
	}
	encoded, found := strings.CutSuffix(file, fileExt)
	if !found {
		return "", "", false
	}
	externalID, ok = decodeName(encoded)
	if !ok || twins.ValidateExternalID(externalID) != nil {
		return "", "", false
	}
	return system, externalID, true
}

// indexFileName maps an entity id to its index file, or reports that no file
// name can hold it. Entity ids are drawn from [A-Za-z0-9._@-] ('@' joins a
// face); anything else, or a leading dot, is refused.
func indexFileName(entityID string) (string, bool) {
	if entityID == "" || entityID[0] == '.' {
		return "", false
	}
	for i := range len(entityID) {
		switch c := entityID[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-' || c == '@':
		default:
			return "", false
		}
	}
	name := encodeName(entityID)
	if len(name) > maxNameLen {
		return "", false
	}
	return targetsDir + "/" + name, true
}

// encodeName spells each uppercase ASCII letter as '^' plus its lowercase (see
// the package doc).
func encodeName(id string) string {
	var b strings.Builder
	b.Grow(len(id) + encodeSlack)
	for i := range len(id) {
		c := id[i]
		if c >= 'A' && c <= 'Z' {
			b.WriteByte('^')
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// decodeName reverses encodeName, rejecting a name it could not have
// produced (an uppercase letter, a '^' not followed by a lowercase one).
func decodeName(name string) (string, bool) {
	var b strings.Builder
	b.Grow(len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z':
			return "", false
		case c == '^':
			if i+1 == len(name) || name[i+1] < 'a' || name[i+1] > 'z' {
				return "", false
			}
			i++
			b.WriteByte(name[i] - ('a' - 'A'))
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), true
}

// validSystem reports whether system is usable as a directory name: 1 to 32
// characters of [a-z0-9_-], starting with a letter — the metamodel's pact
// system grammar, re-checked here because this is where it becomes a path.
func validSystem(system string) bool {
	if system == "" || len(system) > maxSystemLen || system[0] < 'a' || system[0] > 'z' {
		return false
	}
	for i := range len(system) {
		switch c := system[i]; {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist)
}
