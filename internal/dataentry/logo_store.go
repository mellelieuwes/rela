package dataentry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/Sourcehaven-BV/rela/internal/state"
)

// logoStore owns the user-uploaded sidebar logo end to end: the persisted
// bytes/extension in the `.rela/theme/` KV store AND the in-memory cache the
// GET handler serves without hitting disk.
//
// It is self-synchronizing: a sync.RWMutex guards the cached triple so reads
// (Get/URL) never block each other and writes (Save/Delete) publish the new
// bytes atomically. This is deliberately NOT part of the App-wide AppState
// snapshot — the logo has exactly one reader path and one writer path, so it
// owns its own state rather than riding the shared snapshot (the
// pattern the AppState-decomposition arc pushes every peripheral service
// toward).
//
// Bytes live in-memory (≤256 KiB, MaxUserLogoBytes) so GET /_theme/logo
// doesn't hit disk on every request.
type logoStore struct {
	kv state.KV

	mu    sync.RWMutex
	bytes []byte
	ext   string
	hash  string
}

// newLogoStore builds a logoStore over the given KV and loads the persisted
// logo into the cache. A read error is surfaced (not masked) so a corrupt
// `.rela/theme/` doesn't get silently overwritten on the next save.
func newLogoStore(kv state.KV) (*logoStore, error) {
	s := &logoStore{kv: kv}
	bytes, ext, err := s.load()
	if err != nil {
		return nil, err
	}
	s.bytes = bytes
	s.ext = ext
	if ext != "" {
		s.hash = hashLogoBytes(bytes)
	}
	return s, nil
}

// Get returns the cached logo bytes, extension, and content hash. An empty
// ext means "no logo configured"; bytes/hash are populated together with ext.
//
//nolint:gocritic // unnamedResult: handler-style (bytes, ext, hash) is clearer than naming
func (s *logoStore) Get() ([]byte, string, string) {
	// A nil store reads as "no logo configured" so App values constructed
	// without a logo store (chiefly narrow test fixtures) behave like the
	// former zero-value AppState instead of panicking.
	if s == nil {
		return nil, "", ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bytes, s.ext, s.hash
}

// URL returns the public, cache-busting URL for the current logo, or nil when
// no logo is set. Single source of truth for handlers that surface the URL to
// the SPA, so a future change (signing, expiry) lands in one place.
func (s *logoStore) URL() *string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.hash == "" {
		return nil
	}
	u := logoURLForHash(s.hash)
	return &u
}

// Save persists the bytes + extension and updates the cache atomically. The
// on-disk write happens first; the cache is only advanced once the write
// succeeds, so a failed write leaves the served logo unchanged.
func (s *logoStore) Save(ctx context.Context, bytes []byte, ext string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.persist(ctx, bytes, ext); err != nil {
		return err
	}
	s.bytes = bytes
	s.ext = ext
	s.hash = hashLogoBytes(bytes)
	return nil
}

// Delete clears the persisted logo and the cache. Idempotent: deleting when no
// logo is set still succeeds (the on-disk Delete is itself idempotent).
func (s *logoStore) Delete(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.remove(ctx); err != nil {
		return err
	}
	s.bytes = nil
	s.ext = ""
	s.hash = ""
	return nil
}

// load reads the persisted logo bytes and extension. Returns (nil, "", nil)
// when no logo is set. A sidecar file present without matching bytes (or vice
// versa) is treated as "no logo set" so a half-written state during a crash
// doesn't trip up the boot path.
//
// Returns a non-nil error only when the kv layer reports an unexpected failure
// (corrupt filesystem, permission denied) — callers should surface those
// instead of silently masking them.
//
//nolint:gocritic // unnamedResult: handler-style (bytes, ext, err) is clearer than naming
func (s *logoStore) load() ([]byte, string, error) {
	if s.kv == nil {
		return nil, "", nil
	}
	ctx := context.Background()

	logoBytes, err := s.kv.Get(ctx, userLogoFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || os.IsNotExist(err) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("read %s: %w", userLogoFile, err)
	}

	extBytes, err := s.kv.Get(ctx, userLogoExtFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || os.IsNotExist(err) {
			// Bytes present but sidecar missing — treat as not-set. The user
			// can re-upload to recover; we don't proactively clean up so a
			// future fix can recover the bytes.
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("read %s: %w", userLogoExtFile, err)
	}

	ext := string(extBytes)
	if _, ok := allowedLogoExts[ext]; !ok {
		// Sidecar contains an unknown extension — treat as not-set. Same
		// reasoning as the missing-sidecar case.
		return nil, "", nil
	}

	return logoBytes, ext, nil
}

// persist writes the bytes and extension sidecar. Caller holds s.mu.
func (s *logoStore) persist(ctx context.Context, bytes []byte, ext string) error {
	if s.kv == nil {
		return errors.New("kv not configured")
	}
	if _, ok := allowedLogoExts[ext]; !ok {
		return fmt.Errorf("invalid logo extension %q", ext)
	}
	if err := s.kv.Put(ctx, userLogoFile, bytes); err != nil {
		return fmt.Errorf("write %s: %w", userLogoFile, err)
	}
	if err := s.kv.Put(ctx, userLogoExtFile, []byte(ext)); err != nil {
		return fmt.Errorf("write %s: %w", userLogoExtFile, err)
	}
	return nil
}

// remove deletes both the bytes file and the sidecar. Idempotent. Caller holds
// s.mu.
func (s *logoStore) remove(ctx context.Context) error {
	if s.kv == nil {
		return nil
	}
	if err := s.kv.Delete(ctx, userLogoFile); err != nil {
		return fmt.Errorf("delete %s: %w", userLogoFile, err)
	}
	if err := s.kv.Delete(ctx, userLogoExtFile); err != nil {
		return fmt.Errorf("delete %s: %w", userLogoExtFile, err)
	}
	return nil
}
