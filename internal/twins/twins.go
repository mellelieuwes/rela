// Package twins tracks an entity's counterparts in external systems — a
// Basecamp todo, a GitHub issue — and the agreed state of the last sync with
// each of them.
//
// rela never calls an external system. An agent does, with its own tools, and
// reports what it read ([Service.Pull]) or wrote ([Service.Pushed]). rela holds
// the contract (the metamodel's pact: which side owns which field), every
// [Twin], and the agreed [Snapshot] of the last sync, and it enforces field
// ownership on the rela side ([Service.OwnedFields] feeds entitymanager's
// write guard).
//
// # Why this is not in the graph
//
// A twin is bookkeeping ABOUT an entity, not a fact in the domain model the
// operator declared in schema.yaml — the same reasoning that keeps comments
// out of the graph. So it has its own [Store] with its own backends and never
// appears in store.Store, the audit log, /_schema entity lists, search or
// analysis. The one thing that does go through the graph is a sync WRITE to the
// twinned entity: [Service.Pull] patches it through entitymanager, so it is
// validated and audited like any other write. Twin state is machine state,
// like the search index: it describes this installation's link to an external
// system and does not travel with the project's content.
//
// # Lifecycle
//
// [Service.Link] records a twin in [StatePending] with no base: nothing has
// been agreed yet. The agent then loops: [Service.Pending] lists what needs
// attention (with the values to push and the version they were read at),
// [Service.Pull] reconciles what the external side holds ([Reconcile] decides
// per field), and [Service.Pushed] records that rela's values at a version now
// stand on the external side. [Service.MarkGone] records that the external item
// vanished; [Service.EntityDeleted] does the same when the entity leaves rela,
// keeping the twin so the agent can propagate the delete before
// [Service.Unlink] removes it. A gone twin no longer owns fields and no longer
// blocks linking the entity again. [Service.EntityRenamed] follows an entity to
// its new id.
//
// A conflict is resolved by a person. Either the two sides are made equal —
// editing rela (through a normal write) or the external item — and the next
// pull settles it, or the conflict is decided in rela: once the field's rela
// value is no longer the one the conflict recorded, it joins the push set,
// and the push that follows makes it the agreed value. There is no resolve
// verb.
//
// # Concurrency
//
// The CLI and a running server share one store. The service reads the twin
// and the entity, decides, and writes the entity without holding the store's
// lock — an entity write can re-enter the service through the ownership
// guard. It then records the outcome with [Store.Modify], which re-checks
// under the lock that the stored twin is still the one the decision was
// based on; a twin that a rename retargeted, another sync advanced, or that
// went gone meanwhile keeps its state, and the operation reports
// [ErrStale] or [ErrGone].
//
// # External ids
//
// An external id is unique per system and case-sensitive: "ABC" and "abc" are
// two items. An agent whose system has ids that are only unique per container
// namespaces them itself ("repo.12").
//
// # Ordering is part of the contract
//
// [Store.List] returns twins ordered by system then external id, and
// [Store.ForTarget] by system, so every backend renders identically.
// [twinstest.RunAll] pins that, together with the duplicate-target rule and
// the concurrency and round-trip behavior — a contract asserted in one place
// beats backends that each behave slightly differently.
package twins

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"
)

// Sentinel errors. Callers map them to transport-level responses.
var (
	// ErrNotFound reports that no twin exists for a (system, external id).
	ErrNotFound = errors.New("twins: not found")

	// ErrDuplicateTarget reports a second live twin of one entity in one
	// system. An entity has at most one counterpart per system, or the write
	// guard and every sync would have to choose between them. A gone twin
	// does not count.
	ErrDuplicateTarget = errors.New("twins: entity already has a twin in this system")

	// ErrExternalIDTaken reports a Create at a (system, external id) that
	// already has a twin — gone or not. Replacing it would silently discard
	// its sync state, so the caller must unlink first.
	ErrExternalIDTaken = errors.New("twins: external item is already linked")

	// ErrNoPact reports an entity type that declares no pact with the system.
	ErrNoPact = errors.New("twins: entity type has no pact for this system")

	// ErrVersionConflict reports a [Service.Pushed] whose entity version is no
	// longer current: rela changed after the agent read the values it pushed.
	ErrVersionConflict = errors.New("twins: entity changed since the pushed values were read")

	// ErrStalePull reports a pull of remote state older than the state the
	// twin already reconciled; applying it would roll the entity back.
	ErrStalePull = errors.New("twins: remote state is older than the last pull")

	// ErrEntityLocked reports a Link of an entity whose content cannot be read
	// (git-crypt): its base would be meaningless.
	ErrEntityLocked = errors.New("twins: entity is locked")

	// ErrInvalidExternalID reports an external id outside the grammar
	// [ValidateExternalID] enforces.
	ErrInvalidExternalID = errors.New("twins: invalid external id")

	// ErrGone reports a pull or push of a gone twin. Syncing it would revive
	// a twin whose external item or entity is gone (and could write the
	// entity first); it is refused until the twin is unlinked.
	ErrGone = errors.New("twins: twin is gone")

	// ErrStale reports a change decided against a twin that moved on before
	// it could be recorded: a rename retargeted it, or another pull or push
	// recorded a newer base. Nothing is recorded; the caller runs again.
	// [Store.Update] and [Store.Modify] return it for a replacement that
	// would move a twin to another entity, which only [Store.Retarget] does.
	ErrStale = errors.New("twins: twin changed concurrently")
)

// maxExternalIDLen bounds an external id; the file backend uses the id as a
// file name, and 128 bytes stays far below every filesystem's name limit even
// once escaped.
const maxExternalIDLen = 128

// State is where a twin stands in the sync loop.
type State string

// Twin states.
const (
	StateInSync   State = "in_sync"
	StatePending  State = "pending"  // changed on a side, or never synced
	StateConflict State = "conflict" // a shared field changed on both sides
	StateGone     State = "gone"     // entity deleted in rela, or external item gone
)

// Target identifies the twinned entity. Type is carried so the pact can be
// resolved without reading the entity; entity ids are unique across types.
type Target struct{ Type, ID string }

// Snapshot is the agreed value of every field at the last sync: all properties
// of the entity (computed and ref-like included; [Reconcile] classifies each
// through the pact) plus the body.
type Snapshot struct {
	Properties map[string]any `yaml:"properties,omitempty" json:"properties,omitempty"`
	Body       string         `yaml:"body,omitempty" json:"body,omitempty"`
}

// FindingKind classifies an open [Finding].
type FindingKind string

// Finding kinds.
const (
	FindingConflict    FindingKind = "conflict"     // shared field changed on both sides, differently
	FindingForeignEdit FindingKind = "foreign_edit" // external side changed an ours field
	FindingLocalDrift  FindingKind = "local_drift"  // rela side changed a theirs field (only via privileged writes)
	FindingRejected    FindingKind = "rejected"     // rela refused the sync write (validation, transition, unique)
)

// Finding is one field the last reconcile could not settle on its own.
type Finding struct {
	Field   string // property name or "body"; "" when a rejection names no field
	Kind    FindingKind
	Base    any    // nil when absent
	Ours    any    // rela value
	Theirs  any    // external value
	Propose bool   // the pact lists the field in propose (foreign_edit only)
	Message string // why rela refused the write (rejected only)
}

// Twin is one entity's counterpart in one external system.
type Twin struct {
	System, ExternalID, URL string
	Target                  Target
	State                   State
	// HasBase is false until the first pull or push: a linked twin has no
	// agreed values yet, and [Reconcile] treats every field accordingly.
	HasBase         bool
	Base            Snapshot
	BaseVersion     string    // the entity version the base was agreed at
	SyncedAt        time.Time // zero until the first pull/pushed
	RemoteUpdatedAt time.Time
	Findings        []Finding // open findings of the last reconcile; empty when in sync
}

// FieldValue is one field rela holds a value for that the external side does
// not have yet: the agent's push work.
type FieldValue struct {
	Field string // property name or "body"
	Base  any    // the agreed value; nil when absent or never agreed
	Local any    // the value to push
}

// Filter selects twins for [Store.List].
type Filter struct {
	System string  // empty = every system
	States []State // empty = every state
}

// Matches reports whether tw passes f. Shared by the backends so a filter
// means the same thing on each.
func (f Filter) Matches(tw Twin) bool {
	if f.System != "" && tw.System != f.System {
		return false
	}
	return len(f.States) == 0 || slices.Contains(f.States, tw.State)
}

// SortTwins orders twins by system, then external id — the order [Store.List]
// and [Store.ForTarget] promise. Shared by the backends for the reason
// [Filter.Matches] is.
func SortTwins(list []Twin) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].System != list[j].System {
			return list[i].System < list[j].System
		}
		return list[i].ExternalID < list[j].ExternalID
	})
}

// Store persists twins keyed by (System, ExternalID).
//
// Implementations serialize writes (a file backend across processes too) and
// must pass [twinstest.RunAll], which pins ordering, the identity rules, that
// returned values never alias stored state, and that concurrent writes of
// different twins all survive.
//
// Nil: no method returns a nil error with a nil result; ForTarget and List
// return an empty slice rather than nil when nothing matches, and Get reports
// a missing twin as [ErrNotFound] rather than a zero Twin.
type Store interface {
	// Get returns one twin, or [ErrNotFound].
	Get(ctx context.Context, system, externalID string) (Twin, error)

	// ForTarget returns every twin of an entity, sorted by System.
	ForTarget(ctx context.Context, entityID string) ([]Twin, error)

	// List returns the twins f matches, sorted by System, then ExternalID.
	List(ctx context.Context, f Filter) ([]Twin, error)

	// Create stores a new twin. It returns [ErrExternalIDTaken] when a twin
	// already exists at (tw.System, tw.ExternalID), and [ErrDuplicateTarget]
	// when tw is not gone and a twin of the same entity in the same system is
	// not gone either. Nothing is stored on error.
	Create(ctx context.Context, tw Twin) error

	// Update replaces the twin at (tw.System, tw.ExternalID), or returns
	// [ErrNotFound]. The replacement is checked like a [Store.Modify]
	// result.
	Update(ctx context.Context, tw Twin) error

	// Modify runs fn on the twin stored at (system, externalID) and stores
	// the twin fn returns, holding the store's write lock across both, so fn
	// decides on the twin as stored and no other write lands in between.
	// The lock is shared with other processes: fn must do no I/O.
	//
	// It returns the stored twin; [ErrNotFound] when there is none (fn is
	// not called); fn's error as is, storing nothing; and, storing nothing,
	// the errors of [CheckReplace] and the duplicate-target rule of Create
	// for the replacement.
	Modify(ctx context.Context, system, externalID string, fn func(Twin) (Twin, error)) (Twin, error)

	// Delete removes one twin, or returns [ErrNotFound] when absent.
	Delete(ctx context.Context, system, externalID string) error

	// Retarget rewrites Target.ID from oldID to newID on every twin of oldID
	// (an entity rename). It returns [ErrDuplicateTarget], changing nothing,
	// when both ids have a live twin in the same system: merging would have to
	// discard one of them.
	Retarget(ctx context.Context, oldID, newID string) error
}

// CheckReplace reports whether next may replace stored through [Store.Update]
// or [Store.Modify]: it must keep the key, and it must keep the target entity
// — only [Store.Retarget] moves a twin, so a replacement naming another
// entity was read before a rename and is refused with [ErrStale]. Shared by
// the backends so the rule means the same thing on each.
func CheckReplace(stored, next Twin) error {
	if next.System != stored.System || next.ExternalID != stored.ExternalID {
		return fmt.Errorf("twins: replacing %s/%s with %s/%s: the key cannot change",
			stored.System, stored.ExternalID, next.System, next.ExternalID)
	}
	if next.Target.ID != stored.Target.ID {
		return fmt.Errorf("%w: %s/%s targets %s, not %s", ErrStale,
			stored.System, stored.ExternalID, stored.Target.ID, next.Target.ID)
	}
	return nil
}

// ValidateExternalID checks the external id grammar: 1 to 128 characters of
// [A-Za-z0-9._-], and not "." or "..". The file backend uses the id as a file
// name, so the grammar is an allowlist; an agent holding an id outside it
// (a GitHub "owner/repo#12") encodes it before linking.
func ValidateExternalID(id string) error {
	if id == "" || len(id) > maxExternalIDLen || id == "." || id == ".." {
		return fmt.Errorf("%w: %q (want 1..%d of [A-Za-z0-9._-])", ErrInvalidExternalID, id, maxExternalIDLen)
	}
	for i := range len(id) {
		switch c := id[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return fmt.Errorf("%w: %q (want 1..%d of [A-Za-z0-9._-])", ErrInvalidExternalID, id, maxExternalIDLen)
		}
	}
	return nil
}

// Live reports whether tw still counts: a gone twin owns no fields and does
// not block another twin of its entity in its system.
func (tw Twin) Live() bool { return tw.State != StateGone }
