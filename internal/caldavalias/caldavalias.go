// Package caldavalias maps CalDAV resources to rela entities.
//
// # Why this exists
//
// The rela→CalDAV direction is derivable: a resource href and UID are minted
// from the entity's type and id. The inbound direction is not. A to-do created
// in a client arrives with a client-minted identifier — verified against Apple
// Reminders, which uses a bare UUID (`D8AAE77A-89CB-46D2-BDA4-F319D2014D6B`) as
// both the UID and the resource filename. rela entity ids must start with a
// letter or digit, so such a UUID can never BE an entity id. The link has to be
// stored.
//
// # Shape
//
// This is its own service rather than a field on an existing one, and it is
// deliberately NOT a [store.EntityObserver] registered on the store. It follows
// store.VersionService: an injected concern the composition root threads
// through, which consumers bind to via narrow interfaces declared at their own
// call sites. Versioning already made this move — appbuild takes the service
// "rather than type-asserting the store — versioning is a separate injected
// concern, not a store capability."
//
// # What it does not store
//
// The entity TYPE is absent by design. A CalDAV collection declares exactly one
// entity_type (see dataentryconfig.CalDAVCollection), so a request already knows
// the type from the collection before it consults an alias. The alias answers
// only WHICH entity.
package caldavalias

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/state"
)

// stateKey is the [state.KV] key this service persists under. Hierarchical to
// match the convention other state consumers use ("documents/<hash>.html").
const stateKey = "caldav/aliases.json"

// ErrCorrupt is returned by [New] when the stored aliases cannot be parsed.
//
// This is a HARD failure, deliberately unlike scheduler state (which treats a
// corrupt file as empty). An empty alias table is not a degraded state — every
// client resource loses its entity link, so the next sync re-creates every
// to-do as a NEW entity and the user's list silently doubles. That is the same
// reasoning internal/cli/sync applies to its own state: silently discarding it
// "would re-push every local record as a blind create."
var ErrCorrupt = errors.New("caldavalias: stored aliases are corrupt")

// Alias is one CalDAV resource's link to a rela entity.
//
// Deliberately carries NO cached ETag. One lived here so an If-Match could be
// answered without re-rendering, and it was a data-loss bug: only a CalDAV
// write refreshed it, so any rela-side edit (SPA, CLI, MCP, automation, git
// pull) left it stale. The client then either presented the freshly-served tag
// and got a permanent 412, or presented the stale one and silently overwrote
// the newer edit. The tag a conditional request compares against must be
// derived from entity content at the moment of the request — see
// caldavBackend.currentETag.
type Alias struct {
	// Collection is the caldav: config key the resource belongs to.
	Collection string `json:"collection"`
	// Principal is the identity whose client minted this href.
	//
	// Part of the KEY, not decoration. An href is a client's own bookkeeping
	// (Apple mints a bare UUID), so there is no reason two principals should
	// share that namespace even when they sync the same collection — and once
	// a `where:` clause can bind to the caller (an `@me` filter), "one
	// collection" stops being true at all: the same config key resolves to a
	// different member set per principal, so the alias table must be able to
	// represent that they are different collections.
	//
	// Empty is a legitimate value: an unauthenticated single-user deployment
	// has no principal, and all its aliases share the empty key space.
	Principal string `json:"principal,omitempty"`
	// Href is the resource path segment (the ".ics" filename), which is the
	// CalDAV primary key: PUT, DELETE and If-Match all address this.
	Href string `json:"href"`
	// UID is the iCalendar UID inside the resource body. Independent of Href —
	// a client chooses both, and they need not agree.
	UID string `json:"uid"`
	// EntityID is the rela entity this resource maps to.
	EntityID string `json:"entity_id"`
}

// Service stores CalDAV↔rela aliases.
//
// Safe for concurrent use. It holds the whole table in memory behind a mutex
// and persists on every mutation, because [state.KV] offers no read-modify-write
// primitive: FSKV writes are individually atomic (temp→fsync→rename via SafeFS)
// but nothing coordinates two concurrent Puts. The in-process mutex is what
// makes a merge safe here, exactly as settingsService, paletteService and
// logoStore each do for their own state.
//
// CROSS-PROCESS CAVEAT: that mutex does not span processes. Two rela servers on
// one project can still clobber each other's alias writes. Acceptable today
// because CalDAV is served by one process; a multi-writer deployment wants the
// postgres-backed variant noted on TKT-WAA092.
type Service struct {
	kv state.KV

	mu sync.RWMutex
	// byKey is indexed by (principal, collection, href) — the CalDAV primary
	// key, scoped to the identity that owns the href.
	byKey map[string]Alias
}

// New loads the alias table from kv.
//
// A missing table is normal (first run) and yields an empty service. A corrupt
// one is [ErrCorrupt]: see that error's doc for why this refuses rather than
// starting fresh.
func New(ctx context.Context, kv state.KV) (*Service, error) {
	if kv == nil {
		return nil, errors.New("caldavalias: kv is nil")
	}
	s := &Service{kv: kv, byKey: map[string]Alias{}}

	data, err := kv.Get(ctx, stateKey)
	switch {
	case err != nil && os.IsNotExist(err):
		return s, nil // first run
	case err != nil:
		return nil, fmt.Errorf("caldavalias: read aliases: %w", err)
	}

	var stored []Alias
	if unmarshalErr := json.Unmarshal(data, &stored); unmarshalErr != nil {
		return nil, fmt.Errorf("%w: %w", ErrCorrupt, unmarshalErr)
	}
	for _, a := range stored {
		s.byKey[key(a.Principal, a.Collection, a.Href)] = a
	}
	return s, nil
}

// Lookup returns the alias for a resource, if one exists.
func (s *Service) Lookup(principal, collection, href string) (Alias, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.byKey[key(principal, collection, href)]
	return a, ok
}

// LookupByEntity returns the alias mapping to an entity within a collection.
//
// The reverse direction, needed when a rela-side change must be reported
// against the href a client already knows. Linear in the collection's size:
// callers are per-resource request paths, not bulk listings, so a second index
// would be state to keep consistent for no measured gain.
// Selection is DETERMINISTIC (lowest href wins). Go randomizes map iteration,
// so returning the first match made the served href flip between polls whenever
// an entity had more than one alias — and a CalDAV client reads a changed href
// as delete-plus-create, so the to-do vanishes and reappears at random. The
// ctag hashes content, not hrefs, so it does not change across such a flip and
// a polling client never learns to resync. [Service.Put] also evicts the older
// alias to stop the ambiguity arising; this ordering is the belt to that
// braces, and keeps the choice stable for tables written before that existed.
func (s *Service) LookupByEntity(principal, collection, entityID string) (Alias, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	best, found := Alias{}, false
	for _, a := range s.byKey {
		// Principal-scoped: another identity's href for the same entity is not
		// ours to serve. Without this the href one client minted would surface
		// in another principal's listing.
		mine := a.Principal == principal && a.Collection == collection
		if !mine || !sameEntityID(a.EntityID, entityID) {
			continue
		}
		if !found || a.Href < best.Href {
			best, found = a, true
		}
	}
	return best, found
}

// Put records or replaces an alias and persists the table.
//
// Recording an alias EVICTS any other href in the same collection pointing at
// the same entity. One entity must have exactly one resource: two hrefs for one
// to-do is a state no client can render sanely, and whichever is served first
// makes the other dangle. The newest write wins, because that is the href the
// client just used.
func (s *Service) Put(ctx context.Context, a Alias) error {
	if a.Collection == "" || a.Href == "" || a.EntityID == "" {
		return fmt.Errorf("caldavalias: incomplete alias %+v", a)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Eviction is scoped to THIS principal: two identities holding different
	// hrefs for one entity is the normal state of a shared collection, not the
	// ambiguity this guards against. Evicting across principals would delete
	// another client's alias and make its to-do vanish and reappear.
	var stale []string
	for k, existing := range s.byKey {
		sameEntity := existing.Principal == a.Principal &&
			existing.Collection == a.Collection &&
			sameEntityID(existing.EntityID, a.EntityID)
		if sameEntity && existing.Href != a.Href {
			stale = append(stale, k)
		}
	}
	return s.commitLocked(ctx, func() {
		for _, k := range stale {
			delete(s.byKey, k)
		}
		s.byKey[key(a.Principal, a.Collection, a.Href)] = a
	})
}

// Delete removes a resource's alias. Removing an absent alias is not an error.
func (s *Service) Delete(ctx context.Context, principal, collection, href string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(principal, collection, href)
	if _, ok := s.byKey[k]; !ok {
		return nil
	}
	return s.commitLocked(ctx, func() { delete(s.byKey, k) })
}

// EntityRenamed rewrites every alias pointing at oldID to point at newID.
//
// This implements the rename hook and is the load-bearing method of the whole
// service. The entitymanager states the constraint plainly: "Only the
// choke-point knows old→new; a later sweep sees the renamed entity as an
// ordinary update and cannot reconstruct this link." A missed rename orphans
// the alias, and the client then sees a delete plus a create — the user's
// to-do silently duplicates.
//
// Unlike version capture, which is explicitly best-effort, this returns its
// error for the caller to act on: losing an alias corrupts a user's list rather
// than losing an audit record.
func (s *Service) EntityRenamed(ctx context.Context, oldID, newID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var affected []string
	for k, a := range s.byKey {
		if sameEntityID(a.EntityID, oldID) {
			affected = append(affected, k)
		}
	}
	if len(affected) == 0 {
		return nil
	}
	return s.commitLocked(ctx, func() {
		for _, k := range affected {
			a := s.byKey[k]
			a.EntityID = newID
			s.byKey[k] = a
		}
	})
}

// EntityDeleted records that an entity has left the graph.
//
// It deliberately does NOT remove the aliases pointing at it. An alias is the
// durable record that this server once served that resource, and CalDAV reads
// "alias exists, entity does not" as proof of a deliberate deletion — the
// signal that lets a stale client PUT be refused with 404 instead of silently
// resurrecting the entity. Dropping the alias here would destroy that evidence:
// the next PUT would find nothing, read as a create, and undo the delete.
//
// This is therefore a no-op today, kept because it is the AliasRewriter half
// of the rename/delete pair and the obvious place for future bookkeeping (a
// deletion timestamp to prune on). Callers rely on the graph, not this table,
// for what currently exists.
//
// NOTE the entitymanager hook is NOT the only way an entity disappears — a
// `rm`, a `git pull`, or an edit while the server is stopped bypasses it
// entirely. That is precisely why the tombstone is an INFERENCE from live state
// rather than a record written on deletion: nothing needs to observe the delete
// for it to hold.
func (s *Service) EntityDeleted(_ context.Context, _ string) error {
	return nil
}

// EntityFaceDeleted is a no-op: an alias names an entity, not one of its faces,
// and the entity survives a face delete.
func (s *Service) EntityFaceDeleted(_ context.Context, _ string, _ entity.Face) error {
	return nil
}

// commitLocked applies mutate to the in-memory table and persists the result,
// rolling the mutation back if the write fails. Callers hold s.mu.
//
// The rollback is the point: mutating memory first and persisting after would
// leave a service whose in-memory table disagrees with disk on any write error,
// so a later rename would rewrite an alias that was never stored — and a
// restart would silently lose it. Either both happen or neither does.
func (s *Service) commitLocked(ctx context.Context, mutate func()) error {
	before := maps.Clone(s.byKey)
	mutate()
	if err := s.persistLocked(ctx); err != nil {
		s.byKey = before // roll back
		return err
	}
	return nil
}

// persistLocked writes the whole table. Callers hold s.mu.
//
// Whole-table rather than per-key: the table is small (one entry per synced
// resource) and a single atomic write keeps it internally consistent, where
// per-key files could half-apply a rename across a crash.
func (s *Service) persistLocked(ctx context.Context) error {
	all := slices.Collect(maps.Values(s.byKey))
	// Sort for a stable file: an unordered rewrite churns the on-disk bytes on
	// every save and makes a diff useless when debugging.
	slices.SortFunc(all, func(a, b Alias) int {
		if c := strings.Compare(a.Collection, b.Collection); c != 0 {
			return c
		}
		return strings.Compare(a.Href, b.Href)
	})
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return fmt.Errorf("caldavalias: encode aliases: %w", err)
	}
	if err := s.kv.Put(ctx, stateKey, data); err != nil {
		return fmt.Errorf("caldavalias: write aliases: %w", err)
	}
	return nil
}

// key builds the map key from the CalDAV primary key.
// key is the alias primary key.
//
// NUL-separated because none of the three components can contain one: a
// principal comes from a verified JWT subject or a header, and collection/href
// are URL path segments that splitPath has already rejected separators in. That
// makes the join unambiguous without escaping.
func key(principal, collection, href string) string {
	return principal + "\x00" + collection + "\x00" + href
}

// sameEntityID compares entity ids case-insensitively.
//
// Entity id identity is case-insensitive since pgstore migration 0007 ("abc"
// and "ABC" are one entity). An alias keyed case-sensitively would fragment on
// a case-only rename and orphan the resource.
func sameEntityID(a, b string) bool { return strings.EqualFold(a, b) }
