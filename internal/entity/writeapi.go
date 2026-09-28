package entity

import "maps"

// This file holds the shared write-API vocabulary used by every
// caller that mutates the graph: options, result envelopes, and the
// soft-validation Warning struct. The types live in `entity` rather
// than `entitymanager` because they're consumed by code that has no
// reason to import the entitymanager package — autocascade scripts,
// dataentry handlers, mcp tools, lua bindings, cli helpers — and
// keeping them here keeps the write-API vocabulary in a layer every
// component can already see.

// CreateOptions configure entity creation.
type CreateOptions struct {
	// ID is an optional explicit ID. If empty, the manager generates one.
	ID string
	// Prefix overrides the default ID prefix when the entity type declares
	// multiple via `id_prefixes`. Ignored when ID is set or when the entity
	// type uses manual IDs.
	Prefix string
	// Variant selects an entity template variant (empty = default).
	Variant string
	// Face is the content state to create, for a type that declares
	// `faces:`. Required for such a type and rejected for one without
	// (BUG-HC6I2T): a faced type stores no row at the zero coordinate, so
	// there is no default to fall back to and a silently-chosen face would
	// be a write to a row the caller did not name.
	//
	// The value is the face as declared in `faces:`, which IS its stored
	// coordinate. Callers resolve it from the address they were given; they
	// must never derive it from a world, because a world resolves through a
	// chain with a fallback and a write must name its target directly.
	Face Face
	// SkipAutomation suppresses on-create automations. Defaults to false.
	SkipAutomation bool
}

// Warning is a non-blocking finding surfaced to the caller alongside
// a successful write per DEC-HWZHA — a state the storage layer
// tolerated but that an analyze tool would also flag. Code values are
// stable and match the corresponding `analyze_*` finding codes where
// applicable. Path is an RFC 6901 JSON Pointer to the offending field.
//
// Warnings are NOT errors. The write succeeded; the warning is
// advisory. Consumers should surface them non-blockingly (HTTP body,
// MCP result text, CLI stderr, Lua second return).
type Warning struct {
	Code   string `json:"code"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Direction is "outgoing" by default. When the warning was emitted
	// under an inverse body key in the unified PATCH, it's "incoming".
	// Lets UIs disambiguate same-edge warnings without parsing the
	// (free-form) JSON Pointer path. See TKT-GFQK.
	Direction string `json:"direction,omitempty"`
}

// CreateResult describes the outcome of a create, including automation
// side-effects.
type CreateResult struct {
	Entity             *Entity
	RelationsCreated   []*Relation
	EntitiesCreated    []*Entity
	AutomationWarnings []string
	AutomationErrors   []string
	// Warnings collects DEC-HWZHA soft validation findings on the
	// post-write entity. Nil when there are none. Sorted by Path for
	// stable client-facing ordering.
	Warnings []Warning `json:"warnings,omitempty"`
}

// UpdateResult describes the outcome of an update.
type UpdateResult struct {
	Entity             *Entity
	RelationsCreated   []*Relation
	EntitiesCreated    []*Entity
	AutomationWarnings []string
	AutomationErrors   []string
	// Warnings collects DEC-HWZHA soft validation findings on the
	// post-write entity. Nil when there are none. Sorted by Path for
	// stable client-facing ordering.
	Warnings []Warning `json:"warnings,omitempty"`
}

// DeleteResult describes entities and relations removed by a delete.
type DeleteResult struct {
	DeletedEntities  []*Entity
	DeletedRelations []*Relation
}

// RenameOptions configure entity renames.
type RenameOptions struct {
	// DryRun plans the rename without applying changes.
	DryRun bool
}

// RenameResult describes what was changed during a rename.
type RenameResult struct {
	OldID            string
	NewID            string
	RelationsUpdated int
}

// RelationOptions configure relation creation and updates.
//
// CreateRelation: Properties is the initial property map. MetaUnset is
// ignored (no existing values to clear). If Content is non-nil, the body
// is set to *Content (including the empty string); if nil, the body is
// empty.
//
// UpdateRelation: Properties MERGES into the existing relation's
// properties (an Update with empty Properties does NOT clear existing
// keys — use MetaUnset for that). After the merge, MetaUnset removes
// the named keys. If Content is non-nil, the body is replaced with
// *Content (including the empty string clears the body); if nil, the
// existing body is left untouched.
//
// The pointer-vs-string distinction on Content is the only way to
// express "leave the body alone" vs "set the body to empty"; callers
// that want to clear must pass a pointer to "".
type RelationOptions struct {
	Properties map[string]any
	MetaUnset  []string
	Content    *string

	// FromFace names the SOURCE's content state for a `scope: content`
	// relation type, whose edges belong to one face rather than to the
	// entity as such (TKT-DOFYR1, BUG-64MU2Q). The zero value addresses
	// the default state, which is what an identity-scoped edge and a
	// faceless type always use.
	//
	// The tail is part of a relation's IDENTITY, not a filter: two edges
	// on the same triple with different tails are two relations. Dropping
	// this field on an update therefore does not address "roughly the
	// right edge" — it addresses the default face's, which is a different
	// one. `store.RelationWriter.DeleteRelationState` documents the same
	// reasoning on the delete path.
	//
	// There is no ToFace: targets are faceless by construction, which is
	// what makes cross-world dangling references inexpressible.
	FromFace Face
}

// Patch describes a TARGETED entity write: apply exactly these
// property changes and leave everything else alone. It is the entity-side
// analog of [RelationOptions] on the update path, and deliberately shares
// its vocabulary (Properties merges, MetaUnset removes, Content is a
// face tri-state).
//
// The contract that makes it worth having: **properties not named are
// preserved as-is, regardless of whether the caller could read them.** A
// caller who can see 3 of an entity's 10 properties can patch those 3
// without touching the other 7 — so forgetting a property yields a no-op,
// not an erasure. Contrast a whole-entity save, where every property the
// caller failed to supply is silently destroyed.
//
// Semantics:
//
//   - Properties MERGES into the stored entity's properties. An empty or
//     nil map changes nothing; it does NOT clear existing keys.
//   - MetaUnset removes the named keys and is applied AFTER the merge, so
//     a patch naming the same key in both ends with it removed. Unsetting
//     an absent key is a no-op, not an error.
//   - Content non-nil replaces the body with *Content (including the empty
//     string, which clears it); nil leaves the body untouched.
//
// The pointer-vs-string distinction on Content is the only way to express
// "leave the body alone" vs "set the body to empty" — same rationale as
// [RelationOptions.Content].
type Patch struct {
	Properties map[string]any
	MetaUnset  []string
	Content    *string

	// ExpectedVersion, when non-empty, makes the patch a COMPARE-AND-SWAP:
	// it applies only if the stored entity still matches the token the caller
	// last observed, and otherwise fails with a conflict the caller can
	// distinguish and retry on (TKT-34XS2R).
	//
	// The token is produced by the store (`store.VersionOf`) and is opaque
	// here. This field is deliberately a plain string rather than a
	// `store.EntityVersion`: `entity` is the domain package and must not
	// import `store`, which depends on it.
	//
	// Empty means "apply on top of whatever is stored". The manager still
	// writes it as a compare-and-swap against the row it merges into and
	// retries on a conflict, so a concurrent patch to another property is
	// never lost. ExpectedVersion adds the caller's own precondition: the
	// patch fails if the entity changed since the caller read it, which is
	// what a Content replacement computed from a base read, or an edit of
	// the same property, needs.
	ExpectedVersion string
}

// IsEmpty reports whether the patch would change nothing: no property
// upserts, no unsets, and no body replacement. Callers that treat a
// no-op patch specially (skipping the write entirely) can check this
// before dispatching.
func (p Patch) IsEmpty() bool {
	return len(p.Properties) == 0 && len(p.MetaUnset) == 0 && p.Content == nil
}

// Apply merges the patch into e, mutating it in place. Order is
// upserts-then-unset (see [Patch]); the body is replaced only when
// Content is non-nil.
//
// Values are assigned directly rather than through [Entity.SetString] so
// non-string property types (lists, numbers, bools) survive the round trip
// with their declared shape intact.
func (p Patch) Apply(e *Entity) {
	if e == nil {
		return
	}
	if len(p.Properties) > 0 && e.Properties == nil {
		e.Properties = make(map[string]any, len(p.Properties))
	}
	maps.Copy(e.Properties, p.Properties)
	for _, k := range p.MetaUnset {
		delete(e.Properties, k)
	}
	if p.Content != nil {
		e.Content = *p.Content
	}
}
