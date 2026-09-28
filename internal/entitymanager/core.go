package entitymanager

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"sort"
	"strings"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/templating"
)

// createCoreOpts configures core entity creation — the bare write
// path used by both [Manager.CreateEntity] (which wraps it with
// automation + cascade) and [cascadeHost.CreateEntity] (which calls
// it directly to avoid recursive cascade).
type createCoreOpts struct {
	ID              string         // Custom ID (empty = auto-generate)
	IDPrefix        string         // Prefix for auto-generated ID
	TemplateVariant string         // Template variant name (empty = default)
	Properties      map[string]any // Properties to set (overrides template defaults)
	Content         string         // Body content (overrides template content when non-empty)
	// Face is the content state to write. Carried here rather than read off
	// the caller's entity so the face this authorizes and the face it writes
	// are provably the same value (BUG-HC6I2T: they were populated from
	// different places, and only one was ever set).
	Face entity.Face
	// SkipIDGeneration tells [buildCandidateEntity] to skip real ID
	// allocation when ID is empty and the type uses auto-IDs.
	// [Manager.ValidateCreate] sets this so a per-keystroke dry-run does
	// not scan the entire store to pick a never-used ID. The resulting
	// entity gets a stable placeholder ID; validation that depends on
	// the actual ID (ID-prefix check) is not relevant in that path.
	SkipIDGeneration bool
	// mintSpread widens a sequential ID's pick to a random number in
	// [max+1, max+mintSpread]. Set by [createCore] after a lost mint; 0 or 1
	// picks max+1.
	mintSpread int
}

// resolveCandidateID returns the ID to use for the candidate entity
// being built by [buildCandidateEntity]. Three branches:
//   - User-supplied ID → validated (manual-ID types only).
//   - Empty ID + SkipIDGeneration → synthesized placeholder
//     (dry-run / validation path; no store scan).
//   - Empty ID + auto-ID → generated via the full-store scan.
//
// Extracted from [buildCandidateEntity] to keep that function's
// top-level flow flat (avoids the nestif lint warning).
func resolveCandidateID(
	ctx context.Context, deps Deps, entityType string, entityDef *metamodel.EntityDef, opts createCoreOpts,
) (string, error) {
	if opts.ID != "" {
		if !entityDef.IsManualID() {
			return "", customIDNotAllowedError(entityType, entityDef, opts.ID)
		}
		if err := entity.ValidateID(opts.ID); err != nil {
			return "", err
		}
		return opts.ID, nil
	}
	if opts.SkipIDGeneration {
		// Dry-run / validation path: skip the full-store scan
		// generateID would do. The placeholder must pass metamodel
		// ID-prefix validation; [candidatePlaceholderID] synthesizes
		// one from the type's first prefix. Never persisted — only
		// [Manager.ValidateCreate] sets SkipIDGeneration.
		return candidatePlaceholderID(entityDef, opts.IDPrefix), nil
	}
	return generateID(ctx, deps, entityType, opts.IDPrefix, opts.mintSpread)
}

// candidatePlaceholderID returns the synthetic ID to use for a dry-run
// candidate entity when no real ID is supplied. It pairs the requested
// prefix (or the type's first declared prefix) with a fixed suffix so
// metamodel ID-prefix validation passes. The result is never persisted
// — only [Manager.ValidateCreate] (which never writes) uses this path.
func candidatePlaceholderID(def *metamodel.EntityDef, requestedPrefix string) string {
	const candidateSuffix = "DRYRUN"
	if requestedPrefix != "" {
		return requestedPrefix + candidateSuffix
	}
	if prefixes := def.GetIDPrefixes(); len(prefixes) > 0 {
		return prefixes[0] + candidateSuffix
	}
	return candidateSuffix
}

// createCore is the shared bare-write entity-creation path: resolve
// ID, apply template defaults, apply caller properties, partition
// validation errors per DEC-HWZHA (hard errors abort; soft conditions
// proceed and are returned as warnings), persist. **No automation.**
//
// Free function over [Deps] (not a method on Manager) so cascadeHost
// can call it directly without constructing a half-initialized
// Manager view.
func createCore(
	ctx context.Context, deps Deps, entityType string, opts createCoreOpts,
) (*entity.Entity, []entity.Warning, error) {
	for attempt := 1; ; attempt++ {
		e, warnings, err := createCoreOnce(ctx, deps, entityType, opts)
		// A generated ID is minted from a scan, so a concurrent create of the
		// same type can take it first. The store's create is atomic on the ID,
		// so the loser lands here and re-mints against the new state. A
		// caller-supplied ID is the caller's choice: its collision is final.
		if opts.ID == "" && errors.Is(err, ErrEntityAlreadyExists) && attempt < idMintAttempts {
			// Every contender re-reads the same max, so a sequential re-mint
			// of max+1 lets only one of them win per round. Spreading the
			// retry over a window that doubles each round resolves a burst in
			// a few rounds, at the cost of a gap in the numbering, and only
			// when creates actually collided.
			opts.mintSpread = min(1<<attempt, maxMintSpread)
			continue
		}
		return e, warnings, err
	}
}

// idMintAttempts bounds [createCore]'s retry on a generated-ID collision.
// Each loss means another create of the same type committed in between.
// With the doubling spread, 10 rounds absorb a burst far larger than the
// write paths produce (the webhook admits 8 in flight).
const idMintAttempts = 10

// maxMintSpread caps the retry window, so a collision never skips more than
// this many sequential numbers.
const maxMintSpread = 64

// createCoreOnce is one attempt of [createCore].
func createCoreOnce(
	ctx context.Context, deps Deps, entityType string, opts createCoreOpts,
) (*entity.Entity, []entity.Warning, error) {
	e, warnings, err := buildCandidateEntity(ctx, deps, entityType, opts)
	if err != nil {
		return nil, nil, err
	}

	// Enforce state-machine entry BEFORE the durable write (TKT-E4LW2,
	// RR-HETEE): the candidate carries the resolved post-template property
	// values, so the entry-value check needs no persisted row. Running it
	// here — not after Store.CreateEntity — means an illegal entry is
	// rejected without ever emitting a store event (no orphaned index entry,
	// no SSE broadcast, no un-audited row).
	if enforceErr := deps.Transitions.EnforceCreate(ctx, e); enforceErr != nil {
		return nil, nil, enforceErr
	}

	// A create must never fall through to an update — that would
	// overwrite a colliding entity (a racing create, or a stale-scan
	// duplicate ID). Write with a direct CreateEntity and surface a
	// conflict as ErrEntityAlreadyExists. Every write path is now
	// create-XOR-update by resolved intent; there is no create-then-
	// update-on-conflict fallback anywhere (BUG-ZWTDH9).
	//
	// `unique: true` natural keys are enforced atomically with the write.
	// excludeSelfID is empty: on create there is no prior version of this
	// entity to exclude.
	err = writeWithUniqueCheck(ctx, deps, e, "", func(st store.Store) error {
		return st.CreateEntity(ctx, e)
	})
	if err != nil {
		// A derived unique-property index (pgstore, TKT-3Q0GP1) rejects a
		// duplicate PROPERTY value; surface it as the same 422 the pre-write
		// scan does. Check this BEFORE the ErrConflict branch: UniquePropertyError
		// satisfies errors.Is(_, ErrConflict) too, and the ID-collision message
		// would be wrong for a property duplicate.
		if ok, mapped := mapUniquePropertyConflict(err); ok {
			return nil, nil, mapped
		}
		var invalid *ValidationError
		if errors.As(err, &invalid) {
			return nil, nil, err
		}
		if errors.Is(err, store.ErrConflict) {
			return nil, nil, fmt.Errorf("%w: %s", ErrEntityAlreadyExists, e.ID)
		}
		return nil, nil, fmt.Errorf("write entity: %w", err)
	}

	return e, warnings, nil
}

// buildCandidateEntity resolves the ID, applies template + status
// defaults, merges caller properties, and partitions validation errors
// per DEC-HWZHA (hard errors abort; soft conditions return as warnings)
// — everything [createCore] does except the final persist. Shared by
// createCore (which then writes) and [Manager.ValidateCreate] (which
// returns the would-be entity + warnings without writing), so dry-run
// validation cannot drift from the real create path.
func buildCandidateEntity(
	ctx context.Context, deps Deps, entityType string, opts createCoreOpts,
) (*entity.Entity, []entity.Warning, error) {
	entityDef, ok := deps.Meta.GetEntityDef(entityType)
	if !ok {
		return nil, nil, fmt.Errorf("unknown entity type: %s", entityType)
	}

	entityID, err := resolveCandidateID(ctx, deps, entityType, entityDef, opts)
	if err != nil {
		return nil, nil, err
	}

	e := entity.New(entityID, entityType)
	e.Face = opts.Face

	tmpl, err := deps.Templater.EntityTemplate(ctx, entityType, opts.TemplateVariant)
	if err != nil {
		return nil, nil, fmt.Errorf("load template: %w", err)
	}
	if opts.TemplateVariant != "" && tmpl == nil {
		return nil, nil, fmt.Errorf("template variant %q not found for entity type %s", opts.TemplateVariant, entityType)
	}
	if tmpl != nil {
		e.Properties, e.Content = templating.ApplyEntity(e.Properties, e.Content, tmpl)
	}

	maps.Copy(e.Properties, opts.Properties)
	if err := rejectComputedPresent(deps, entityType, e.Properties); err != nil {
		return nil, nil, err
	}

	if opts.Content != "" {
		e.Content = opts.Content
	}

	if e.GetString("status") == "" {
		e.SetString("status", entityDef.GetDefaultStatus(deps.Meta))
	}
	if err := deps.Computed.Evaluate(ctx, e); err != nil {
		return nil, nil, err
	}

	// DEC-HWZHA: hard structural errors abort; soft conditions
	// (required-missing, type mismatch, invalid enum, malformed value)
	// ride along on the result as warnings.
	var warnings []entity.Warning
	if errs := deps.Meta.ValidateEntity(e.ID, e.Type, e.Properties); len(errs) > 0 {
		hard, soft := partitionValidationErrors(errs)
		if len(hard) > 0 {
			return nil, nil, newValidationError(hard)
		}
		warnings = soft
	}

	return e, warnings, nil
}

// generateID generates the next ID for the given entity type. If
// prefix is non-empty it overrides the metamodel-default prefix. spread is
// [createCoreOpts.mintSpread]; random short IDs ignore it.
func generateID(ctx context.Context, deps Deps, entityType, prefix string, spread int) (string, error) {
	entityDef, ok := deps.Meta.GetEntityDef(entityType)
	if !ok {
		return "", fmt.Errorf("unknown entity type: %s", entityType)
	}
	if entityDef.IsManualID() {
		return "", fmt.Errorf("entity type %s uses manual IDs", entityType)
	}
	if prefix == "" {
		prefixes := entityDef.GetIDPrefixes()
		if len(prefixes) == 0 {
			return "", fmt.Errorf("no ID prefixes defined for type %s", entityType)
		}
		prefix = prefixes[0]
	}

	existingIDs, err := collectAllIDs(ctx, deps.Store)
	if err != nil {
		return "", fmt.Errorf("collect existing IDs: %w", err)
	}
	if entityDef.IsShortID() {
		return entity.GenerateShortID(existingIDs, prefix, len(existingIDs), entityDef.GetIDCaps()), nil
	}
	if spread > 1 {
		//nolint:gosec // G404: spreading contenders over a window, not a secret
		skip := rand.IntN(spread)
		return entity.GenerateSequentialID(existingIDs, prefix, skip), nil
	}
	return entity.GenerateNextID(existingIDs, prefix), nil
}

// collectAllIDs returns every entity ID currently in the store.
// A partial scan must not feed ID generation: a truncated list can hide
// a high-numbered existing ID, so the generator would mint one that
// already exists. createCore then rejects that collision with
// ErrEntityAlreadyExists (a create never overwrites), so a truncated
// scan would surface as a spurious conflict rather than data loss — but
// fail loudly here regardless, so the generator is never fed bad data.
// AllStates, then deduped by bare id. Without it the scan sees only
// zero-coordinate rows, which a type declaring faces has none of — so the
// generator saw an EMPTY id set for such a type and minted the same id for
// every entity of it (BUG-HC6I2T). Counting a family once is what the query
// used to get from the zero coordinate; with no face privileged, dedup is
// what provides it.
func collectAllIDs(ctx context.Context, st store.Store) ([]string, error) {
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	for e, err := range st.ListEntities(ctx, store.EntityQuery{AllStates: true}) {
		if err != nil {
			return nil, err
		}
		if _, dup := seen[e.ID]; dup {
			continue
		}
		seen[e.ID] = struct{}{}
		ids = append(ids, e.ID)
	}
	return ids, nil
}

// collectIncidentRelations gathers a store's relations in the given
// direction for the given entity. The result gates the delete-safety
// check (refuse a non-cascade delete that would orphan relations), so a
// partial scan must not silently under-count — propagate the error and
// let the caller refuse the delete rather than proceed on partial data.
func collectIncidentRelations(
	ctx context.Context, st store.Store, id string, dir store.Direction,
) ([]*entity.Relation, error) {
	return collectRelations(ctx, st, store.RelationQuery{
		EntityID:  id,
		Direction: dir,
	})
}

// collectRelations drains a relation query into a slice, failing on the
// first store error rather than returning a partial set.
func collectRelations(
	ctx context.Context, st store.Store, q store.RelationQuery,
) ([]*entity.Relation, error) {
	out := make([]*entity.Relation, 0)
	for r, err := range st.ListRelations(ctx, q) {
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// findExistingRelationTarget locates an existing target entity of the
// given type that is the target of a relation from sourceID with the
// given relationType. Returns nil if none exists.
func findExistingRelationTarget(
	ctx context.Context, st store.Store, sourceID, relationType, targetType string,
) *entity.Entity {
	for rel, err := range st.ListRelations(ctx, store.RelationQuery{
		EntityID:  sourceID,
		Direction: store.DirectionOutgoing,
		Type:      relationType,
	}) {
		if err != nil {
			continue
		}
		target, getErr := st.GetEntity(ctx, rel.To)
		if getErr != nil {
			continue
		}
		if target.Type == targetType {
			return target
		}
	}
	return nil
}

// requireCreateFaceFor enforces that a create names exactly the faces its type
// declares: one of them for a faced type, none for a faceless one.
//
// The rule is symmetric on purpose. A faced type has no zero-coordinate row to
// default to, and a faceless type has no name for the single state it does
// store, so in both directions the only safe answer is to refuse rather than
// pick. Reads still resolve a bare address through the world chain; a WRITE
// never does, because a chain answers with a fallback and a write must name
// the row it changes (BUG-HC6I2T).
func (d Deps) requireCreateFaceFor(entityType string, face entity.Face) error {
	def, ok := d.Meta.GetEntityDef(entityType)
	if !ok {
		return fmt.Errorf("unknown entity type: %s", entityType)
	}
	if len(def.Faces) == 0 {
		if !face.IsDefault() {
			return fmt.Errorf("%w: %s declares no faces, so %q names nothing",
				ErrFaceNotDeclared, entityType, face)
		}
		return nil
	}
	if face.IsDefault() {
		return fmt.Errorf("%w: %s declares %s", ErrFaceRequired,
			entityType, strings.Join(sortedFaceNames(def), ", "))
	}
	if _, declared := def.Faces[face.String()]; !declared {
		return fmt.Errorf("%w: %s declares %s, not %q", ErrFaceNotDeclared,
			entityType, strings.Join(sortedFaceNames(def), ", "), face)
	}
	return nil
}

// requireRelationFaceFor rejects a source face that the relation type or the
// source entity type cannot carry. It is the relation-side counterpart of
// requireCreateFaceFor, but deliberately one-sided: it refuses a WRONG face
// and never demands one (see below).
//
// It guards the manager write path only. The cascade host writes relations
// straight to the store (cascadehost.go WriteRelation), so it does not pass
// through here — safe today because it supplies no face at all and therefore
// only ever writes the zero tail, which is legal. A future cascade that wants
// to choose a face must route through the manager or repeat this check.
//
//   - `scope: identity` edges attach to the entity as such, so they have no
//     per-face existence and the only valid tail is the zero face. Accepting
//     a named one would store a coordinate the readers never query — the edge
//     exists and nothing returns it (reads filter on the tail), so a caller's
//     "does this link exist" check answers false and re-creates it forever.
//   - `scope: content` edges belong to one face of the source, so a named tail
//     must be one the source type declares, and a faceless source must not
//     name one at all.
//
// **Rejects a wrong face; does NOT require one.** This is deliberately weaker
// than its entity-side twin, and the asymmetry is the point. An entity create
// with no face has no row to write — a faced type stores nothing at the zero
// coordinate, so refusing is the only option. A relation create with a zero
// tail writes a real, addressable, readable edge; it is simply attached at the
// identity coordinate. That is a far weaker failure, and demanding a face here
// would break every caller that cannot yet supply one — `rela link`
// (internal/cli/link.go), the MCP create_relation tool (whose schema has no
// face parameter), CalDAV membership writes, and the data-entry INCOMING-edge
// path, which passes a zero tail as a considered decision because the peer's
// face is not the request's to choose. Those surfaces gain a face with
// TKT-2RQMV4; until then they must keep working.
//
// Lives HERE rather than in each binding because the ACL is not a backstop
// for it: Manager.authorizeAndAudit returns early under `bypassACL`, so an
// elevated caller reaches the store with no grant check at all. A check below
// that short-circuit covers the elevated path, and every future client (MCP,
// CLI, caldav — see TKT-2RQMV4) inherits it the way entity creates already
// inherit requireCreateFaceFor.
//
// fromType is best-effort at the call sites (empty when the source does not
// exist yet, mirroring the authorization subject), so an unresolvable source
// is validated on the relation scope alone rather than refused here — the
// peer-existence checks that follow are what report a missing endpoint.
//
// Nil: never returns an error for a zero face on an identity-scoped type,
// which is the overwhelmingly common case.
func (d Deps) requireRelationFaceFor(relType, fromType string, face entity.Face) error {
	relDef, ok := d.Meta.GetRelationDef(relType)
	if !ok {
		// Unknown relation type: ValidateRelation reports it with a better
		// message than a face complaint would.
		return nil
	}
	// Branch through IsIdentity/IsContent, never by comparing to a constant:
	// identity scope has two spellings ("" and "identity").
	if relDef.Scope.IsIdentity() {
		if !face.IsDefault() {
			return fmt.Errorf("%w: relation %s is scope: identity, so it attaches to the "+
				"entity rather than to %q", ErrFaceNotDeclared, relType, face)
		}
		return nil
	}
	if fromType == "" {
		return nil
	}
	def, defOK := d.Meta.GetEntityDef(fromType)
	if !defOK {
		return nil
	}
	if len(def.Faces) == 0 {
		if !face.IsDefault() {
			return fmt.Errorf("%w: source type %s declares no faces, so %q names nothing",
				ErrFaceNotDeclared, fromType, face)
		}
		return nil
	}
	// A zero tail is ACCEPTED on a faced source — see the doc block. It means
	// the identity coordinate, which is a real and readable edge, not a
	// missing row.
	if face.IsDefault() {
		return nil
	}
	if _, declared := def.Faces[face.String()]; !declared {
		return fmt.Errorf("%w: source type %s declares %s, not %q", ErrFaceNotDeclared,
			fromType, strings.Join(sortedFaceNames(def), ", "), face)
	}
	return nil
}

// sortedFaceNames lists a type's declared faces in a stable order, so an
// error message names them the same way twice.
func sortedFaceNames(def *metamodel.EntityDef) []string {
	names := make([]string, 0, len(def.Faces))
	for name := range def.Faces {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// anyFaceOf returns any stored row of an entity, for the checks that ask a
// question every face of a family answers the same way — its TYPE.
//
// Relation endpoints are the motivating case. A relation attaches to the
// ENTITY, not to one of its content states (`scope: identity`), so validating
// `from`/`to` needs the family's type and nothing face-specific. Reading that
// with Store.GetEntity asks the ZERO coordinate, where a type declaring faces
// stores no row at all — so every relation touching a faced entity reported
// the entity as missing (BUG-HC6I2T).
//
// Prefers the addressed row when `id` carries a face, so a caller who named
// one is answered about it; otherwise takes the first row the family has.
// Which row that is does not matter: the callers use only the type, and every
// state of a family shares it (the store refuses a divergent one).
//
// FAILS CLOSED. Only a genuine [store.ErrNotFound] falls through to the next
// lookup; any other error is returned as-is. Swallowing a transient backend
// error here would report the entity as missing, and every caller's not-found
// branch skips the ACL check by design (existence is itself a secret), so a
// store hiccup would turn an ACL-gated operation into an ungated one — the
// defect TestRename_FailsClosedOnNonNotFoundFetchError pins.
//
// Nil: never returned with a nil error.
func anyFaceOf(ctx context.Context, st store.Store, id string) (*entity.Entity, error) {
	e, err := st.GetEntity(ctx, id)
	if err == nil {
		return e, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	base, face, perr := entity.ParseStateRef(id)
	if perr == nil && !face.IsDefault() {
		faced, serr := st.GetEntityState(ctx, base, face)
		if serr == nil {
			return faced, nil
		}
		if !errors.Is(serr, store.ErrNotFound) {
			return nil, serr
		}
	} else {
		base = id
	}
	// IDs-scoped, never a full scan: this runs on the relation write path,
	// once per endpoint, and an unbounded scan there is the per-row lookup
	// the collection-read rules exist to prevent.
	q := store.EntityQuery{IDs: []string{base}, AllStates: true}
	for e, err := range st.ListEntities(ctx, q) {
		if err != nil {
			return nil, err
		}
		if e.ID == base {
			return e, nil
		}
	}
	return nil, store.ErrNotFound
}

// getRelationOnFace returns the edge of this triple whose TAIL is exactly
// face, or [store.ErrNotFound].
//
// [store.RelationReader.GetRelation] cannot answer this: it addresses the
// default-tail edge only, so on a faced source it reports "no such relation"
// for an edge that exists, and hands back a DIFFERENT edge for one that does.
// The tail is part of a relation's identity, so the query filters on it
// rather than matching the triple approximately (BUG-64MU2Q).
//
// FAILS CLOSED: a query error is returned as-is, never flattened into
// not-found. Callers branch on not-found to mean "absent", and on the create
// path that branch decides whether a write proceeds.
//
// Nil: never returned with a nil error.
func getRelationOnFace(
	ctx context.Context, st store.Store, from string, face entity.Face, relType, to string,
) (*entity.Relation, error) {
	q := store.RelationQuery{
		From:     from,
		FromFace: &face,
		Type:     relType,
		To:       to,
	}
	for rel, err := range st.ListRelations(ctx, q) {
		if err != nil {
			return nil, err
		}
		// The query is the address; the comparison guards a backend that
		// treats any of these fields as a hint rather than a filter.
		if rel.From == from && rel.FromFace == face && rel.Type == relType && rel.To == to {
			return rel, nil
		}
	}
	return nil, store.ErrNotFound
}

// getEntityByRef resolves an entity ADDRESS — either a bare id or the fused
// boundary form `ID@face` — to the row it names.
//
// Store.GetEntity is GetEntityState(id, zero) in every backend, so it can only
// ever answer for the zero coordinate. A type declaring `faces:` stores no row
// there (BUG-HC6I2T removed the privileged face), which means a caller handed
// `POL-1@draft` and passing it straight to GetEntity gets ErrNotFound for a row
// that plainly exists.
//
// Fails closed on a non-not-found error, for the same reason [anyFaceOf] does:
// a caller's not-found branch typically returns before authorizing, so a
// transient store error reported as "absent" would skip an ACL check.
//
// Nil: never returned with a nil error.
func (m *Manager) getEntityByRef(ctx context.Context, ref string) (*entity.Entity, error) {
	base, face, perr := entity.ParseStateRef(ref)
	if perr != nil {
		// Not a parseable ref: let the store answer for the literal id, which
		// preserves the error the caller would have seen before.
		return m.deps.Store.GetEntity(ctx, ref)
	}
	if face.IsDefault() {
		return m.deps.Store.GetEntity(ctx, base)
	}
	return m.deps.Store.GetEntityState(ctx, base, face)
}
