// Package memtwins is an in-memory [twins.Store] for tests. Nothing survives
// process exit.
package memtwins

import (
	"context"
	"sync"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/twins"
)

// key is a twin's storage key.
type key struct{ system, externalID string }

// Store keeps every twin in one map under one mutex. Contention is irrelevant
// at the scale this backend serves, and Retarget and the duplicate-target
// check both need the whole map anyway.
type Store struct {
	mu    sync.Mutex
	twins map[key]twins.Twin
}

// New returns an empty store.
func New() *Store {
	return &Store{twins: map[key]twins.Twin{}}
}

var _ twins.Store = (*Store)(nil)

// Get returns one twin, as a deep copy (see clone).
func (s *Store) Get(_ context.Context, system, externalID string) (twins.Twin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tw, ok := s.twins[key{system, externalID}]
	if !ok {
		return twins.Twin{}, twins.ErrNotFound
	}
	return clone(tw), nil
}

// ForTarget returns every twin of an entity, sorted by system.
func (s *Store) ForTarget(_ context.Context, entityID string) ([]twins.Twin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []twins.Twin{}
	for _, tw := range s.twins {
		if tw.Target.ID == entityID {
			out = append(out, clone(tw))
		}
	}
	twins.SortTwins(out)
	return out, nil
}

// List returns the twins f matches in the contract order.
func (s *Store) List(_ context.Context, f twins.Filter) ([]twins.Twin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []twins.Twin{}
	for _, tw := range s.twins {
		if f.Matches(tw) {
			out = append(out, clone(tw))
		}
	}
	twins.SortTwins(out)
	return out, nil
}

// Create stores a new twin under the identity rules of [twins.Store.Create].
func (s *Store) Create(_ context.Context, tw twins.Twin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key{tw.System, tw.ExternalID}
	if _, taken := s.twins[k]; taken {
		return twins.ErrExternalIDTaken
	}
	if s.duplicates(k, tw) {
		return twins.ErrDuplicateTarget
	}
	s.twins[k] = clone(tw)
	return nil
}

// Update replaces an existing twin.
func (s *Store) Update(ctx context.Context, tw twins.Twin) error {
	_, err := s.Modify(ctx, tw.System, tw.ExternalID, func(twins.Twin) (twins.Twin, error) { return tw, nil })
	return err
}

// Modify runs fn on the stored twin and stores its result, both under s.mu.
func (s *Store) Modify(
	_ context.Context, system, externalID string, fn func(twins.Twin) (twins.Twin, error),
) (twins.Twin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key{system, externalID}
	stored, ok := s.twins[k]
	if !ok {
		return twins.Twin{}, twins.ErrNotFound
	}
	next, err := fn(clone(stored))
	if err != nil {
		return twins.Twin{}, err
	}
	if err := twins.CheckReplace(stored, next); err != nil {
		return twins.Twin{}, err
	}
	if s.duplicates(k, next) {
		return twins.Twin{}, twins.ErrDuplicateTarget
	}
	s.twins[k] = clone(next)
	return next, nil
}

// duplicates reports whether storing tw at k would give its entity a second
// live twin in its system. Callers must hold s.mu.
func (s *Store) duplicates(k key, tw twins.Twin) bool {
	if !tw.Live() {
		return false
	}
	for other, existing := range s.twins {
		if other != k && other.system == tw.System && existing.Target.ID == tw.Target.ID && existing.Live() {
			return true
		}
	}
	return false
}

// Delete removes one twin.
func (s *Store) Delete(_ context.Context, system, externalID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key{system, externalID}
	if _, ok := s.twins[k]; !ok {
		return twins.ErrNotFound
	}
	delete(s.twins, k)
	return nil
}

// Retarget moves every twin of oldID to newID, all or nothing.
func (s *Store) Retarget(_ context.Context, oldID, newID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if oldID == newID {
		return nil
	}
	occupied := map[string]bool{}
	var moving []key
	for k, tw := range s.twins {
		switch tw.Target.ID {
		case newID:
			if tw.Live() {
				occupied[k.system] = true
			}
		case oldID:
			moving = append(moving, k)
		}
	}
	for _, k := range moving {
		if s.twins[k].Live() && occupied[k.system] {
			return twins.ErrDuplicateTarget
		}
	}
	for _, k := range moving {
		tw := s.twins[k]
		tw.Target.ID = newID
		s.twins[k] = tw
	}
	return nil
}

// clone deep-copies the reference fields of a twin.
//
// A struct copy is not enough: the base properties and findings hold maps,
// slices and list values, so a shallow copy would let a caller mutate stored
// state through the value it was handed — which no persistent backend
// permits, so tests passing against this one would fail against those.
func clone(tw twins.Twin) twins.Twin {
	if tw.Base.Properties != nil {
		props := make(map[string]any, len(tw.Base.Properties))
		for k, v := range tw.Base.Properties {
			props[k] = entity.CloneValue(v)
		}
		tw.Base.Properties = props
	}
	if tw.Findings != nil {
		findings := make([]twins.Finding, len(tw.Findings))
		for i, f := range tw.Findings {
			f.Base, f.Ours, f.Theirs = entity.CloneValue(f.Base), entity.CloneValue(f.Ours), entity.CloneValue(f.Theirs)
			findings[i] = f
		}
		tw.Findings = findings
	}
	return tw
}
