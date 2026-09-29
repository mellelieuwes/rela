package twins

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync/atomic"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/canonical"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
)

// EntityReader reads the twinned entity together with its compare-and-swap
// version token, so the version always describes exactly the values read.
//
// The service needs the raw stored entity: a redacted read would record a
// hidden value as absent in the base and report it as changed on the next
// sync. A missing entity must be reported as an error whose chain holds a
// value with an `EntityNotFound() bool` method returning true (the
// entitymanager convention, see lua.NotFoundError); the service then treats
// the twin as gone.
//
// VersionAt returns the version e would have if it were stored under id, all
// else equal. A version folds in the entity id, so a rename changes it
// without changing any content; the service uses VersionAt to tell a rename
// apart from an edit.
type EntityReader interface {
	GetEntityVersion(ctx context.Context, id string) (*entity.Entity, string, error)
	VersionAt(e *entity.Entity, id string) string
}

// SyncWriter writes a pulled change to the twinned entity. It is the
// entitymanager handle WITH the twin-sync capability: the one writer allowed
// to change a field an external system owns. Everything else about the write
// — ACL, validation, transitions, audit — still applies.
//
// The result carries the post-write entity (after automations) and its
// version. A write refused on its merits (validation, a transition rule, a
// unique constraint) must be reported as an error whose chain holds a value
// with a `WriteRejected() bool` method returning true: the service records it
// as a [FindingRejected] instead of failing the pull.
type SyncWriter interface {
	PatchEntity(ctx context.Context, id string, p entity.Patch) (*entity.UpdateResult, error)
}

// Service is the twin bookkeeping and sync path. See the package doc for the
// lifecycle it implements.
//
// Authorization is the caller's job, as with comments: the service trusts its
// caller to have decided who may link, pull and push.
type Service struct {
	store  Store
	meta   func() *metamodel.Metamodel
	reader EntityReader
	writer SyncWriter
	now    func() time.Time

	// pacts caches the compiled pact view of the last metamodel seen. A
	// metamodel is published as an immutable snapshot, so a new pointer is a
	// new schema and the only reason to recompile — which keeps the write
	// guard's per-write lookup free of allocation.
	pacts atomic.Pointer[compiledPacts]
}

type compiledPacts struct {
	meta   *metamodel.Metamodel
	policy metamodel.PactPolicy
}

// NewService returns a Service over st.
//
// meta supplies the live metamodel; it is called per operation, so a schema
// reload changes which pacts apply without rebuilding the service.
//
// Nil: st, meta, reader and writer are rejected — a service missing any of
// them would fail on first use, far from the wiring mistake. now may be nil,
// in which case time.Now is used.
func NewService(
	st Store, meta func() *metamodel.Metamodel, reader EntityReader, writer SyncWriter, now func() time.Time,
) (*Service, error) {
	switch {
	case st == nil:
		return nil, errors.New("twins: NewService requires a store")
	case meta == nil:
		return nil, errors.New("twins: NewService requires a metamodel source")
	case reader == nil:
		return nil, errors.New("twins: NewService requires an entity reader")
	case writer == nil:
		return nil, errors.New("twins: NewService requires a sync writer")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: st, meta: meta, reader: reader, writer: writer, now: now}, nil
}

// Link records a new twin of entityID in system.
//
// The entity's type must have a pact with system ([ErrNoPact]) and its content
// must be readable ([ErrEntityLocked]). The twin starts in [StatePending]
// without a base: nothing has been agreed until the first pull or push. An
// external item that already has a twin is refused ([ErrExternalIDTaken]), as
// is a second live twin of the entity in the system ([ErrDuplicateTarget]).
func (s *Service) Link(
	ctx context.Context, entityID, system, externalID, url string, remoteUpdatedAt time.Time,
) (Twin, error) {
	if err := ValidateExternalID(externalID); err != nil {
		return Twin{}, err
	}
	e, version, err := s.reader.GetEntityVersion(ctx, entityID)
	if err != nil {
		return Twin{}, fmt.Errorf("twins: reading %s: %w", entityID, err)
	}
	if e.IsLocked() {
		return Twin{}, fmt.Errorf("%w: %s", ErrEntityLocked, e.ID)
	}
	if _, err := s.pactFor(e.Type, system); err != nil {
		return Twin{}, err
	}
	tw := Twin{
		System:          system,
		ExternalID:      externalID,
		URL:             url,
		Target:          Target{Type: e.Type, ID: e.ID},
		State:           StatePending,
		BaseVersion:     version,
		RemoteUpdatedAt: remoteUpdatedAt.UTC(),
	}
	if err := s.store.Create(ctx, tw); err != nil {
		return Twin{}, err
	}
	return tw, nil
}

// Unlink removes a twin, or returns [ErrNotFound].
func (s *Service) Unlink(ctx context.Context, system, externalID string) error {
	if err := ValidateExternalID(externalID); err != nil {
		return err
	}
	return s.store.Delete(ctx, system, externalID)
}

// Get returns one twin, or [ErrNotFound].
func (s *Service) Get(ctx context.Context, system, externalID string) (Twin, error) {
	if err := ValidateExternalID(externalID); err != nil {
		return Twin{}, err
	}
	return s.store.Get(ctx, system, externalID)
}

// ForEntity returns every twin of an entity, sorted by system. When the
// entity no longer exists its live twins are marked gone first, so the answer
// never shows a twin as syncing an entity that is not there.
func (s *Service) ForEntity(ctx context.Context, entityID string) ([]Twin, error) {
	list, err := s.store.ForTarget(ctx, entityID)
	if err != nil || len(list) == 0 {
		return list, err
	}
	_, _, err = s.reader.GetEntityVersion(ctx, entityID)
	switch {
	case isEntityNotFound(err):
		return allGone(ctx, s.store, list)
	case err != nil:
		return nil, fmt.Errorf("twins: reading %s: %w", entityID, err)
	}
	return list, nil
}

// List returns the twins f matches, sorted by system then external id.
func (s *Service) List(ctx context.Context, f Filter) ([]Twin, error) {
	return s.store.List(ctx, f)
}

// PendingItem is one twin that needs the agent's attention.
type PendingItem struct {
	Twin         Twin
	LocalChanged bool         // entity version ≠ BaseVersion
	Version      string       // the entity version PushSet was read at; hand it back to [Service.Pushed]
	PushSet      []FieldValue // rela values to push: properties by name, then the body
	Reasons      []string     // human-readable: "never synced", "changed in rela", "conflict: status", ...
}

// Pending returns the agent's work list for system (every system when empty):
// each twin that is not in sync, or whose entity changed in rela since the
// last sync. Gone twins are always included — the agent has to propagate the
// deletion — and a twin whose entity no longer exists is marked gone here.
// A twin that a rename retargeted after it was listed is judged again
// against its new entity rather than marked gone.
//
// PushSet lists every ours or shared field whose rela value differs from the
// base (every one, for a twin never synced). A shared field in conflict
// waits for a human and is left out — until a person decides it in rela:
// once its rela value is no longer the one the conflict recorded, it is
// listed, and [Service.Pushed] settles the conflict.
func (s *Service) Pending(ctx context.Context, system string) ([]PendingItem, error) {
	list, err := s.store.List(ctx, Filter{System: system})
	if err != nil {
		return nil, err
	}
	policy := s.policy()
	out := []PendingItem{}
	for _, tw := range list {
		item, listed, err := s.pendingItem(ctx, policy, tw)
		if errors.Is(err, ErrStale) {
			if tw, err = s.store.Get(ctx, tw.System, tw.ExternalID); err == nil {
				item, listed, err = s.pendingItem(ctx, policy, tw)
			}
		}
		switch {
		case errors.Is(err, ErrNotFound): // unlinked since the listing
		case err != nil:
			return nil, err
		case listed:
			out = append(out, item)
		}
	}
	return out, nil
}

// pendingItem judges one twin for the work list; the bool is false when it
// needs nothing. A live twin whose entity is missing is marked gone, or
// reports [ErrStale] when a rename retargeted it since it was read.
func (s *Service) pendingItem(
	ctx context.Context, policy metamodel.PactPolicy, tw Twin,
) (PendingItem, bool, error) {
	item := PendingItem{Twin: tw}
	noPact := false
	if tw.Live() {
		e, version, err := s.reader.GetEntityVersion(ctx, tw.Target.ID)
		switch {
		case isEntityNotFound(err):
			if item.Twin, err = entityGone(ctx, s.store, tw); err != nil {
				return PendingItem{}, false, err
			}
		case err != nil:
			return PendingItem{}, false, fmt.Errorf("twins: reading %s: %w", tw.Target.ID, err)
		default:
			item.Version = version
			item.LocalChanged = version != tw.BaseVersion
			pact, ok := policy.Pact(e.Type, tw.System)
			noPact = !ok
			if ok {
				item.PushSet = pushSet(pact, tw, snapshotOf(e))
			}
		}
	}
	if item.Twin.State == StateInSync && !item.LocalChanged {
		return PendingItem{}, false, nil
	}
	item.Reasons = pendingReasons(item.Twin, item.LocalChanged, noPact)
	return item, true, nil
}

// pushSet lists the pushable fields of local that the external side does not
// hold yet, per the rules on [Service.Pending].
func pushSet(p metamodel.Pact, tw Twin, local Snapshot) []FieldValue {
	decided := decidedConflicts(tw.Findings, local)
	include := func(f string) bool {
		d, inConflict := decided[f]
		return pushable(p, f) && (!inConflict || d)
	}

	fields := make([]string, 0, len(local.Properties)+len(tw.Base.Properties))
	for f := range local.Properties {
		fields = append(fields, f)
	}
	for f := range tw.Base.Properties {
		if _, inLocal := local.Properties[f]; !inLocal {
			fields = append(fields, f)
		}
	}
	sort.Strings(fields)

	var out []FieldValue
	for _, f := range fields {
		l, b := local.Properties[f], tw.Base.Properties[f]
		if include(f) && (!tw.HasBase || decided[f] || !canonical.EqualValue(l, b)) {
			out = append(out, FieldValue{Field: f, Base: b, Local: l})
		}
	}
	body := metamodel.PactBodyField
	if include(body) && (!tw.HasBase || decided[body] || !canonical.EqualBody(local.Body, tw.Base.Body)) {
		fv := FieldValue{Field: body, Local: local.Body}
		if tw.HasBase {
			fv.Base = tw.Base.Body
		}
		out = append(out, fv)
	}
	return out
}

// pendingReasons explains why a twin is on the work list.
func pendingReasons(tw Twin, localChanged, noPact bool) []string {
	var reasons []string
	switch {
	case !tw.Live():
		reasons = append(reasons, "gone")
	case !tw.HasBase:
		reasons = append(reasons, "never synced")
	}
	if noPact {
		reasons = append(reasons, "no pact with "+tw.System)
	}
	if localChanged {
		reasons = append(reasons, "changed in rela")
	}
	for _, f := range tw.Findings {
		switch f.Kind {
		case FindingConflict:
			reasons = append(reasons, "conflict: "+f.Field)
		case FindingForeignEdit:
			if f.Propose {
				reasons = append(reasons, "proposed externally: "+f.Field)
			} else {
				reasons = append(reasons, "changed externally: "+f.Field)
			}
		case FindingLocalDrift:
			reasons = append(reasons, "local drift: "+f.Field)
		case FindingRejected:
			reasons = append(reasons, "rejected by rela: "+f.Message)
		}
	}
	if len(reasons) == 0 && tw.State == StatePending {
		// Pending after a sync with nothing else to say: a pushable field
		// differs from the base, so rela holds a value to push.
		reasons = append(reasons, "unpushed changes in rela")
	}
	return reasons
}

// PullResult is the outcome of [Service.Pull].
type PullResult struct {
	Twin    Twin
	Applied []string // fields written to the entity, sorted
}

// Pull reconciles what the agent read from the external item into rela.
//
// remoteUpdatedAt is required: a pull older than the last one is refused
// with [ErrStalePull] unless force is set, since applying it would roll the
// entity back to an earlier external state.
//
// The entity and its version are read once; [Reconcile] decides per field;
// the patch is written through the [SyncWriter] as a compare-and-swap against
// that version. The twin then records the new base, the post-write version,
// the findings, and the state derived against the post-write entity — an
// automation the write triggered may have moved a pushable field. With nothing
// to write, no write happens and the read version stands.
//
// A conflict stands while the remote still reports the value it conflicted
// with and rela does not agree with it: the field is left alone and its
// finding carried over as recorded, so a decision a person made in rela
// since (see [Service.Pending]) survives the pull until it is pushed.
//
// Failures:
//   - a gone twin is refused with [ErrGone] before anything is read;
//   - a concurrent edit (the CAS), an authorization failure or I/O is returned
//     as an error and leaves the twin untouched; the agent retries;
//   - a write rela refuses on its merits is recorded as a [FindingRejected]
//     with the base not advanced, and returned without an error, so the twin
//     is visibly stuck rather than silently so;
//   - an entity that no longer exists marks the twin gone;
//   - a twin that moved on while the pull ran (see [ErrStale]) keeps its
//     state; an entity write already made converges on the next pull.
func (s *Service) Pull(
	ctx context.Context, system, externalID string, remote Remote, remoteUpdatedAt time.Time, force bool,
) (PullResult, error) {
	if remoteUpdatedAt.IsZero() {
		return PullResult{}, errors.New("twins: pull requires the remote's updated-at time")
	}
	if _, ok := remote.Properties[metamodel.PactBodyField]; ok {
		return PullResult{}, fmt.Errorf("twins: remote property %q is reserved; send the body as the body",
			metamodel.PactBodyField)
	}
	tw, err := s.syncable(ctx, system, externalID)
	if err != nil {
		return PullResult{}, err
	}
	if !force && remoteUpdatedAt.Before(tw.RemoteUpdatedAt) {
		return PullResult{}, fmt.Errorf("%w: remote updated %s, last pull %s", ErrStalePull,
			remoteUpdatedAt.UTC().Format(time.RFC3339), tw.RemoteUpdatedAt.Format(time.RFC3339))
	}
	e, version, err := s.reader.GetEntityVersion(ctx, tw.Target.ID)
	if isEntityNotFound(err) {
		tw, err = entityGone(ctx, s.store, tw)
		return PullResult{Twin: tw}, err
	}
	if err != nil {
		return PullResult{}, fmt.Errorf("twins: reading %s: %w", tw.Target.ID, err)
	}
	pact, err := s.pactFor(e.Type, system)
	if err != nil {
		return PullResult{}, err
	}
	local := snapshotOf(e)
	remote, standing := standingConflicts(tw.Findings, local, remote)
	var base *Snapshot
	if tw.HasBase {
		base = &tw.Base
	}
	plan := Reconcile(pact, base, local, remote)
	plan.carry(standing)
	if !plan.Patch.IsEmpty() {
		patch := plan.Patch
		patch.ExpectedVersion = version
		var res *entity.UpdateResult
		res, err = s.writer.PatchEntity(ctx, e.ID, patch)
		switch {
		case isWriteRejected(err):
			return s.recordRejection(ctx, tw, plan, err)
		case isEntityNotFound(err):
			tw, err = entityGone(ctx, s.store, tw)
			return PullResult{Twin: tw}, err
		case err != nil:
			return PullResult{}, fmt.Errorf("twins: pull %s/%s: writing %s: %w", system, externalID, e.ID, err)
		case res == nil || res.Entity == nil:
			return PullResult{}, fmt.Errorf("twins: pull %s/%s: writing %s returned no entity", system, externalID, e.ID)
		}
		version = res.Version
		plan.State = plan.state(pact, snapshotOf(res.Entity))
	}

	syncedAt := s.now().UTC()
	tw, err = commit(ctx, s.store, tw, func(cur *Twin) {
		cur.Base = plan.NewBase
		cur.HasBase = true
		cur.BaseVersion = version
		cur.SyncedAt = syncedAt
		cur.RemoteUpdatedAt = remoteUpdatedAt.UTC()
		cur.Findings = plan.Findings
		cur.State = plan.State
	})
	if err != nil {
		return PullResult{}, err
	}
	return PullResult{Twin: tw, Applied: patchedFields(plan.Patch)}, nil
}

// recordRejection keeps the twin's base and sync stamps as they were and
// records why rela refused the write, next to the plan's other findings.
func (s *Service) recordRejection(ctx context.Context, tw Twin, plan Plan, cause error) (PullResult, error) {
	findings := slices.Concat(plan.Findings, []Finding{{Kind: FindingRejected, Message: cause.Error()}})
	state := StatePending
	if hasConflict(plan.Findings) {
		state = StateConflict
	}
	tw, err := commit(ctx, s.store, tw, func(cur *Twin) {
		cur.Findings = findings
		cur.State = state
	})
	if err != nil {
		return PullResult{}, err
	}
	return PullResult{Twin: tw}, nil
}

// Pushed records the agent's assertion that the external item now holds
// rela's values as of version — the [PendingItem.Version] it pushed from — for
// every field of that item's PushSet: every ours field, every shared field not
// in conflict, and every conflict a person decided in rela.
//
// A gone twin is refused with [ErrGone] before anything is read. A version
// that is no longer current is refused with [ErrVersionConflict]: rela
// changed after the agent read what it pushed, so the external side does not
// hold the current values. Otherwise the pushed fields take the values at
// that version as their base (theirs fields keep theirs: pushing never writes
// them; a twin never synced seeds them from rela too, the best agreement on
// record). A push settles foreign edits and the conflicts decided in rela, and
// clears their findings. It settles nothing about theirs fields, so local
// drift and rejected pulls keep their findings and the twin stays pending; a
// conflict still open keeps its finding and the twin in [StateConflict]. A
// zero remoteUpdatedAt keeps the stored one.
func (s *Service) Pushed(
	ctx context.Context, system, externalID, version string, remoteUpdatedAt time.Time,
) (Twin, error) {
	tw, err := s.syncable(ctx, system, externalID)
	if err != nil {
		return Twin{}, err
	}
	e, current, err := s.reader.GetEntityVersion(ctx, tw.Target.ID)
	if isEntityNotFound(err) {
		return entityGone(ctx, s.store, tw)
	}
	if err != nil {
		return Twin{}, fmt.Errorf("twins: reading %s: %w", tw.Target.ID, err)
	}
	if current != version {
		return Twin{}, fmt.Errorf("%w: pushed %s at version %q, now %q", ErrVersionConflict, e.ID, version, current)
	}
	pact, err := s.pactFor(e.Type, system)
	if err != nil {
		return Twin{}, err
	}
	local := snapshotOf(e)
	syncedAt := s.now().UTC()
	return commit(ctx, s.store, tw, func(cur *Twin) {
		recordPush(pact, cur, local)
		cur.BaseVersion = version
		cur.SyncedAt = syncedAt
		if !remoteUpdatedAt.IsZero() {
			cur.RemoteUpdatedAt = remoteUpdatedAt.UTC()
		}
	})
}

// recordPush folds a confirmed push of local into tw's base, findings and
// state, per [Service.Pushed].
func recordPush(p metamodel.Pact, tw *Twin, local Snapshot) {
	decided := decidedConflicts(tw.Findings, local)
	open := func(f string) bool { d, inConflict := decided[f]; return inConflict && !d }
	// A pushed field takes rela's value. Without a base every other field
	// does too, except a conflict still open: it has no agreed value yet.
	take := func(f string) bool { return !open(f) && (!tw.HasBase || pushable(p, f)) }

	base := Snapshot{Properties: maps.Clone(tw.Base.Properties), Body: tw.Base.Body}
	for f, v := range local.Properties {
		if take(f) {
			setProperty(&base, f, v)
		}
	}
	for f := range tw.Base.Properties {
		if _, inLocal := local.Properties[f]; !inLocal && take(f) {
			delete(base.Properties, f)
		}
	}
	if take(metamodel.PactBodyField) {
		base.Body = local.Body
	}

	var kept []Finding
	for _, f := range tw.Findings {
		switch f.Kind {
		case FindingConflict:
			if open(f.Field) {
				kept = append(kept, f)
			}
		case FindingLocalDrift, FindingRejected:
			kept = append(kept, f)
		case FindingForeignEdit:
			// The push put rela's value on the external side.
		}
	}

	tw.Base, tw.HasBase, tw.Findings = base, true, kept
	switch {
	case hasConflict(kept):
		tw.State = StateConflict
	case len(kept) > 0:
		tw.State = StatePending
	default:
		tw.State = StateInSync
	}
}

// MarkGone records that the external item no longer exists. The twin is kept
// so the agent (or a human) can decide what happens to the entity.
func (s *Service) MarkGone(ctx context.Context, system, externalID string) (Twin, error) {
	if err := ValidateExternalID(externalID); err != nil {
		return Twin{}, err
	}
	return s.store.Modify(ctx, system, externalID, func(tw Twin) (Twin, error) {
		tw.State = StateGone
		return tw, nil
	})
}

// syncable returns the twin a pull or push addresses. A gone twin is refused
// with [ErrGone] before anything else is read or written: syncing would
// revive it, and a pull would write its entity first.
func (s *Service) syncable(ctx context.Context, system, externalID string) (Twin, error) {
	tw, err := s.Get(ctx, system, externalID)
	if err != nil {
		return Twin{}, err
	}
	if !tw.Live() {
		return Twin{}, fmt.Errorf("%w: %s/%s", ErrGone, system, externalID)
	}
	return tw, nil
}

// commit records a bookkeeping change decided against tw — the twin as read
// before its entity was read and written — through [Store.Modify]. Under the
// store's lock it checks that the stored twin is still that twin: targeting
// the same entity, live, and not synced by another pull or push since.
// Otherwise the change was decided on a footing that no longer stands and is
// dropped, reporting [ErrStale] or [ErrGone]. The entity write, if any, has
// happened by then and never under the lock: a write re-enters the service
// through the ownership guard.
func commit(ctx context.Context, st Store, tw Twin, change func(*Twin)) (Twin, error) {
	key := tw.System + "/" + tw.ExternalID
	return st.Modify(ctx, tw.System, tw.ExternalID, func(cur Twin) (Twin, error) {
		switch {
		case cur.Target.ID != tw.Target.ID:
			return Twin{}, fmt.Errorf("%w: %s now targets %s, not %s", ErrStale, key, cur.Target.ID, tw.Target.ID)
		case !cur.Live():
			return Twin{}, fmt.Errorf("%w: %s", ErrGone, key)
		case cur.HasBase != tw.HasBase || cur.BaseVersion != tw.BaseVersion || !cur.SyncedAt.Equal(tw.SyncedAt):
			return Twin{}, fmt.Errorf("%w: %s was synced by another pull or push", ErrStale, key)
		}
		change(&cur)
		return cur, nil
	})
}

// entityGone marks tw gone because its entity no longer exists. Under the
// store's lock it checks that the twin still targets that entity: when a
// rename retargeted it since tw was read, the missing id is no longer its
// entity, and nothing changes ([ErrStale]). An already-gone twin is returned
// as read, without a write.
func entityGone(ctx context.Context, st Store, tw Twin) (Twin, error) {
	if !tw.Live() {
		return tw, nil
	}
	return st.Modify(ctx, tw.System, tw.ExternalID, func(cur Twin) (Twin, error) {
		if cur.Target.ID != tw.Target.ID {
			return Twin{}, fmt.Errorf("%w: %s/%s now targets %s, not the missing %s",
				ErrStale, tw.System, tw.ExternalID, cur.Target.ID, tw.Target.ID)
		}
		cur.State = StateGone
		return cur, nil
	})
}

// allGone marks the twins of a missing entity gone, as [entityGone] does. A
// twin that a rename moved to another entity or that was unlinked since list
// was read no longer belongs to the missing entity and is left out.
func allGone(ctx context.Context, st Store, list []Twin) ([]Twin, error) {
	out := list[:0]
	for _, tw := range list {
		gone, err := entityGone(ctx, st, tw)
		switch {
		case errors.Is(err, ErrStale), errors.Is(err, ErrNotFound):
		case err != nil:
			return nil, err
		default:
			out = append(out, gone)
		}
	}
	return out, nil
}

// OwnedFields answers the write guard: which fields of the entity an external
// system owns through a live twin. The result maps each theirs field to the
// systems owning it (sorted); a pact owning everything appears under
// [metamodel.PactAllFields]. Computed properties are never owned. A twin whose
// pact the schema no longer declares owns nothing.
//
// A type without any pact answers without touching the store, so the guard
// costs nothing on the entities twins cannot concern.
//
// Nil: returned when nothing of the entity is owned, so a caller can range
// over the result without a check.
func (s *Service) OwnedFields(ctx context.Context, entityType, entityID string) (map[string][]string, error) {
	policy := s.policy()
	if len(policy.Systems(entityType)) == 0 {
		return nil, nil //nolint:nilnil // a nil map is the documented "nothing owned" answer; see above.
	}
	list, err := s.store.ForTarget(ctx, entityID)
	if err != nil {
		return nil, err
	}
	var owned map[string][]string
	for _, tw := range list { // sorted by system, so each field's systems come out sorted
		if !tw.Live() {
			continue
		}
		pact, ok := policy.Pact(entityType, tw.System)
		if !ok {
			continue
		}
		for _, f := range pact.Theirs() {
			if pact.Computed(f) {
				continue
			}
			if owned == nil {
				owned = map[string][]string{}
			}
			owned[f] = append(owned[f], tw.System)
		}
	}
	return owned, nil
}

// EntityRenamed follows a renamed entity: its twins now target newID.
//
// A version folds in the entity id, so the rename alone would leave every
// twin's base version behind and report the twin as changed in rela with
// nothing to push. A twin whose base version is the renamed entity's version
// under oldID therefore moves on to its version under newID; a twin whose
// base was agreed at other content keeps it, as that change is still to push.
//
// It implements entitymanager's AliasRewriter hook, like the comment service.
// That hook runs after the rename and LOGS an error rather than failing the
// rename, so a failed retarget (a live twin of both ids in one system) leaves
// the twins on the old id: until an operator relinks them they own nothing on
// the renamed entity and sync against an id that no longer resolves.
func (s *Service) EntityRenamed(ctx context.Context, oldID, newID string) error {
	if err := s.store.Retarget(ctx, oldID, newID); err != nil {
		return err
	}
	e, version, err := s.reader.GetEntityVersion(ctx, newID)
	switch {
	case isEntityNotFound(err):
		return nil // renamed or deleted again since: that event moves the twins on
	case err != nil:
		return fmt.Errorf("twins: reading renamed %s: %w", newID, err)
	}
	renamedFrom := s.reader.VersionAt(e, oldID)
	list, err := s.store.ForTarget(ctx, newID)
	if err != nil {
		return err
	}
	for _, tw := range list {
		if tw.BaseVersion != renamedFrom {
			continue
		}
		_, err := s.store.Modify(ctx, tw.System, tw.ExternalID, func(cur Twin) (Twin, error) {
			// Under the lock: a sync or another rename since the list was
			// read has recorded its own footing, which stands.
			if cur.Target.ID == newID && cur.BaseVersion == renamedFrom {
				cur.BaseVersion = version
			}
			return cur, nil
		})
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}

// EntityFaceDeleted is a no-op. A twin belongs to a whole entity, never to one
// of its faces: pacts are refused on a type that declares faces (see
// metamodel's pact validation), so deleting a face cannot touch a twinned
// entity's content. It exists to satisfy entitymanager's AliasRewriter hook.
func (s *Service) EntityFaceDeleted(context.Context, string, entity.Face) error { return nil }

// EntityDeleted marks every twin of the deleted entity gone. Unlike comments
// the twins are kept: the external items still exist, and the agent must see
// them on the work list to propagate the delete before unlinking.
func (s *Service) EntityDeleted(ctx context.Context, id string) error {
	list, err := s.store.ForTarget(ctx, id)
	if err != nil {
		return err
	}
	_, err = allGone(ctx, s.store, list)
	return err
}

// policy returns the compiled pacts of the current metamodel.
func (s *Service) policy() metamodel.PactPolicy {
	m := s.meta()
	if c := s.pacts.Load(); c != nil && c.meta == m {
		return c.policy
	}
	c := &compiledPacts{meta: m, policy: metamodel.NewPactPolicy(m)}
	s.pacts.Store(c)
	return c.policy
}

// pactFor returns the pact between entityType and system, or [ErrNoPact].
func (s *Service) pactFor(entityType, system string) (metamodel.Pact, error) {
	pact, ok := s.policy().Pact(entityType, system)
	if !ok {
		return metamodel.Pact{}, fmt.Errorf("%w: %s has no pact with %q", ErrNoPact, entityType, system)
	}
	return pact, nil
}

// entityNotFound and writeRejected are the optional error capabilities
// documented on [EntityReader] and [SyncWriter]. Declared here at the consumer
// so twins depends on neither store nor entitymanager; matching structurally
// rather than on text, because rejection messages embed caller-supplied values.
type (
	entityNotFound interface{ EntityNotFound() bool }
	writeRejected  interface{ WriteRejected() bool }
)

func isEntityNotFound(err error) bool {
	var e entityNotFound
	return errors.As(err, &e) && e.EntityNotFound()
}

func isWriteRejected(err error) bool {
	var e writeRejected
	return errors.As(err, &e) && e.WriteRejected()
}

// decidedConflicts classifies the fields of the conflict findings against
// local: true when a person decided the conflict in rela since — rela's value
// is no longer the one the conflict recorded — false while it is open. A
// field without a conflict is absent.
func decidedConflicts(findings []Finding, local Snapshot) map[string]bool {
	out := map[string]bool{}
	for _, f := range findings {
		switch {
		case f.Kind != FindingConflict:
		case f.Field == metamodel.PactBodyField:
			ours, _ := f.Ours.(string)
			out[f.Field] = !canonical.EqualBody(local.Body, ours)
		default:
			out[f.Field] = !canonical.EqualValue(local.Properties[f.Field], f.Ours)
		}
	}
	return out
}

// standingConflicts takes out of remote the fields whose conflict stands (see
// [Service.Pull]): the remote still reports the value the conflict recorded,
// and rela's value differs from it. Reconciling such a field again would only
// re-record the conflict against rela's current value, forgetting a decision
// made in rela since; its finding is returned to be carried over instead.
// remote's map is not modified.
func standingConflicts(findings []Finding, local Snapshot, remote Remote) (Remote, []Finding) {
	var standing []Finding
	cloned := false
	for _, f := range findings {
		if f.Kind != FindingConflict {
			continue
		}
		if f.Field == metamodel.PactBodyField {
			theirs, _ := f.Theirs.(string)
			if r := remote.Body; r != nil && canonical.EqualBody(*r, theirs) && !canonical.EqualBody(local.Body, *r) {
				remote.Body = nil
				standing = append(standing, f)
			}
			continue
		}
		r, mapped := remote.Properties[f.Field]
		if mapped && canonical.EqualValue(r, f.Theirs) && !canonical.EqualValue(local.Properties[f.Field], r) {
			if !cloned {
				remote.Properties, cloned = maps.Clone(remote.Properties), true
			}
			delete(remote.Properties, f.Field)
			standing = append(standing, f)
		}
	}
	return remote, standing
}

// carry adds standing conflicts to the plan's findings, in the order
// [Reconcile] promises (properties by name, then the body), and keeps the
// plan in conflict.
func (plan *Plan) carry(standing []Finding) {
	if len(standing) == 0 {
		return
	}
	plan.Findings = append(plan.Findings, standing...)
	sort.SliceStable(plan.Findings, func(i, j int) bool {
		a, b := plan.Findings[i].Field, plan.Findings[j].Field
		if aBody, bBody := a == metamodel.PactBodyField, b == metamodel.PactBodyField; aBody != bBody {
			return bBody
		}
		return a < b
	})
	plan.State = StateConflict
}

// snapshotOf captures an entity's fields. The property map is copied so a
// later change to the entity cannot reach the snapshot through it.
func snapshotOf(e *entity.Entity) Snapshot {
	return Snapshot{Properties: maps.Clone(e.Properties), Body: e.Content}
}

// patchedFields names the fields a patch writes, sorted.
func patchedFields(p entity.Patch) []string {
	fields := make([]string, 0, len(p.Properties)+len(p.MetaUnset)+1)
	for f := range p.Properties {
		fields = append(fields, f)
	}
	fields = append(fields, p.MetaUnset...)
	if p.Content != nil {
		fields = append(fields, metamodel.PactBodyField)
	}
	sort.Strings(fields)
	return fields
}
