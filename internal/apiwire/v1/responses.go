package v1

import (
	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	"github.com/Sourcehaven-BV/rela/internal/entity"
)

// Warning is the wire alias for a domain soft-validation warning, surfaced on
// mutation responses (DEC-HWZHA write-with-warnings).
type Warning = entity.Warning

// Entity is the JSON representation of an entity for API v1.
type Entity struct {
	ID           string              `json:"id"`
	Type         string              `json:"type"`
	Title        string              `json:"_title,omitempty"`
	Properties   map[string]any      `json:"properties"`
	Content      string              `json:"content,omitempty"`
	Relations    map[string][]string `json:"relations,omitempty"`
	Included     map[string]Entity   `json:"included,omitempty"`
	Self         string              `json:"_self,omitempty"`
	Actions      map[string]bool     `json:"_actions,omitempty"`
	Inaccessible []InaccessibleField `json:"inaccessible,omitempty"`
	// EntityWriteAffordances carries the per-entity write hints (`_fields`,
	// `_relations`, `_redacted`, `content_writable`), flattened onto the
	// entity on the wire.
	EntityWriteAffordances
	// Attachments maps a `file`-type property name to the LIST of files
	// currently attached to it (a property may hold several when its
	// metamodel `max` > 1). The value is always an array — even a
	// single-attachment property reports a 1-element list — matching how
	// rela's `list:` properties and `_relations` are always arrays. Only
	// properties that actually carry a file appear. Same face /
	// closed-world semantics as FieldAffordances: present (possibly empty)
	// on every per-entity response (GET, PATCH, POST create, clone — the
	// ones that run serializeEntityForWire), nil on list rows and other
	// non-per-entity shapes. The SPA's file widget reads this to render the
	// download links / previews instead of the raw stored path string(s).
	Attachments *map[string][]Attachment `json:"_attachments,omitempty"`
	// World carries the PROVENANCE of the face in this response: which world
	// resolved it, which coordinate it was stored at, and which resolution
	// rule chose it (TKT-WRLDAPI item 2).
	//
	// It exists because the bytes alone do not say. Under a world with a
	// fallback chain, "the Dutch face" and "the English face, because no
	// Dutch face exists" arrive byte-identically — same id, same type, same
	// shape — and a client rendering a publication badge, offering a
	// translate affordance, or deciding whether an edit is safe has to tell
	// them apart. Re-deriving it client-side would require the chain, the
	// per-type overrides and the fallback policy, which is a second
	// implementation of the semantics that decide which face a reader sees.
	//
	// Present on the single-entity GET ONLY — deliberately NOT on every
	// per-entity response the way FieldAffordances is. The distinction is
	// worth stating because a client will otherwise look for it on a PATCH:
	//
	//   - Writes are refused a `?world=` outright (worlds are read-only on
	//     this API; a write names its face by id instead), so a create /
	//     update / clone response answers for the face that was addressed
	//     and never for a world — a provenance block would be noise.
	//   - A history snapshot is rebuilt from a stored version and carries no
	//     face, so labeling it would state a coordinate the code cannot
	//     back — the "affordance map that lies" failure, one layer down.
	//   - Views, attachments and restore are not world-capable routes.
	//
	// The single-entity GET is the only response whose face is the
	// store's actual answer to a world query, which is why it is the only
	// one that carries this.
	World *EntityWorld `json:"_world,omitempty"`
	// Transitions maps a state-machine-typed property name to the LIST of its
	// outgoing transitions resolved for the requesting principal on this entity
	// (TKT-3G93B8): each carries the target value, an optional action label,
	// the guard permission (if any), whether the principal may perform it right
	// now, and — when not — which gate blocked it. Only properties whose type is
	// a state machine appear; a plain enum field has no entry, and the SPA falls
	// back to the ordinary enum control. Same face / closed-world semantics as
	// FieldAffordances: present (possibly empty) on every per-entity response
	// (GET / PATCH / POST create / clone) when the resolver can answer
	// transitions, nil on list rows and when no state machines are wired. It is a
	// UI hint, never authorization — the write path re-enforces every transition.
	Transitions *map[string][]Transition `json:"_transitions,omitempty"`
	// Mentions resolves the entity-ID code spans in `Content` to their
	// titles, so an EDIT surface can render them the way the read surface
	// already does. Same map shape and same semantics as
	// [ViewResponse.Mentions], produced by the same collectMentions call:
	// the two must not diverge, because a title that appears in the editor
	// and not in the rendered view (or the reverse) is a bug the user sees.
	//
	// It rides the single-entity GET only. A list row carries no content to
	// scan, and a write response is not a surface that renders references.
	//
	// Per-principal, like every mention map: an entity the caller may not
	// read has NO entry here (the code span stays plain), and one whose
	// display title is redacted arrives with `inaccessible` set and its ID
	// as the title. Deriving a title any other way would defeat the read
	// gate — see BUG-R9EHKV.
	Mentions map[string]Mention `json:"mentions,omitempty"`
	// Copies lists the declared copy definitions available FROM this entity's
	// current face — the promote / translate affordances (RULING 9). See
	// [CopyOffer] for the per-entry contract (a UI hint, never a boundary).
	//
	// It rides the entity response alongside [Entity.Actions] because that is
	// how every other affordance in this app is delivered: computed at read
	// time from data the client already fetched, rather than requiring a
	// second request keyed on (type, face, id). An earlier revision shipped
	// a `GET /_copies` list endpoint; it was removed for exactly that reason.
	//
	// Same pointer semantics as FieldAffordances: present (possibly empty) on
	// per-entity responses, nil on list rows — and nil when the server has no
	// copy capability wired at all, so a wiring gap never reads as "this face
	// declares no copies".
	Copies *[]CopyOffer `json:"_copies,omitempty"`
	// Faces lists the OTHER content states this entity actually has, so a
	// draft can link to its published face and vice versa.
	//
	// Existence only — no readability flag. Whether a principal may read a
	// world is a GLOBAL, role-level grant (acl.Request.PermitsWorld takes a
	// world name and nothing else), already served by `/_schema`.worlds; a
	// per-face flag here would re-answer a question the client has already
	// asked once for the whole session.
	//
	// The CURRENT face is excluded: this answers "where else can I go", and
	// the face you are on is not somewhere else.
	//
	// Face names are operator-authored config and public. WHICH faces a
	// given entity has is DATA, so this rides the entity's own read gate —
	// it appears only on a response the caller was already cleared to read.
	// Do not add a bulk endpoint that answers it outside that gate.
	//
	// Same face / closed-world semantics as the other affordance maps:
	// present (possibly empty) on per-entity responses, nil on list rows.
	Faces *[]Face `json:"_faces,omitempty"`
	// Warnings lists soft-condition findings surfaced by the write
	// path. Populated only by mutation responses (PATCH); read paths
	// leave it nil. Each warning has a stable `code`, an RFC 6901
	// JSON Pointer `path`, and a human-readable `detail`.
	Warnings []Warning `json:"warnings,omitempty"`
}

// EntityWriteAffordances groups the write-surface hints of a per-entity
// response. Embedded in [Entity], so every field serializes at the top level;
// the grouping is Go-side only and does not change the wire.
type EntityWriteAffordances struct {
	// FieldAffordances carries per-field write affordances on per-entity
	// GET responses. Sparse: only fields whose verdict deviates from the
	// permissive default appear. Hidden fields are omitted from
	// `Properties` AND from this map entirely. Pointer semantics
	// distinguish "absent on the wire" (nil pointer; list / mutation
	// responses) from "present and empty" (`{}`; per-entity GET with no
	// deviations under nop resolver — closed-world signal matching the
	// `_actions` precedent).
	FieldAffordances *map[string]FieldAffordance `json:"_fields,omitempty"`
	// RelationAffordances carries per-relation-type affordances on
	// per-entity GET responses. Same face / closed-world semantics
	// as FieldAffordances.
	RelationAffordances *map[string]RelationAffordance `json:"_relations,omitempty"`
	// Redacted names the properties withheld from `Properties` by
	// field-level ACL (`visible:`) on THIS response (DEC-T0XIWQ). It is the
	// field-level sibling of Inaccessible, which says the same thing
	// ("exists, value unreadable") for git-crypt-locked content.
	//
	// It exists because absence from `Properties` is ambiguous — a key can be
	// missing because it was redacted OR because it was never set — and a
	// WRITE surface has to tell those apart to know which inputs it may
	// offer. Read-out surfaces are happy to conflate them; an edit form is
	// not. Clients MUST NOT infer redaction from absence: consult this list.
	//
	// Disclosure boundary: this leaks property NAMES, never VALUES. That is
	// not a new disclosure — the metamodel endpoint already serves the
	// declared property names per type, and `visible:` redaction is defined
	// as hiding values only, making no claim to conceal which properties
	// exist. Row-level ACL is unaffected: whether an ENTITY exists remains a
	// genuine secret, and this list only ever rides a response the caller was
	// already authorized to read.
	//
	// Same face / closed-world semantics as FieldAffordances: present
	// (possibly empty) on per-entity responses — `[]` meaning "evaluated,
	// nothing redacted" — and nil on list rows and other non-per-entity
	// shapes, which carry no write affordances. Names are sorted for a
	// deterministic wire.
	Redacted *[]string `json:"_redacted,omitempty"`
	// ContentWritable reports whether a caller-authored write may change
	// `content`. False when an external system owns the body through a twin
	// (a pact's `theirs` names `body` or `*`); the write path then refuses a
	// changed body with 422. Same pointer semantics as FieldAffordances:
	// present on per-entity responses, nil on list rows. A UI hint, never
	// authorization — the write path re-enforces ownership.
	ContentWritable *bool `json:"content_writable,omitempty"`
}

// FieldAffordance describes per-field write / option affordances on
// the wire. Sparse: `Writable` is nil when the default (writable)
// holds; `Options` lists only the false entries (allowed options are
// implicit via the metamodel). See the closed-world contract in
// docs/data-entry/api-reference.md.
//
// Reason explains a `writable: false` verdict when the cause is more
// specific than the policy's read-only (today: "Owned by basecamp" for a
// field an external system owns through a twin). Empty otherwise. It is a
// finished sentence, displayed verbatim.
type FieldAffordance struct {
	Writable *bool           `json:"writable,omitempty"`
	Options  map[string]bool `json:"options,omitempty"`
	Reason   string          `json:"reason,omitempty"`
}

// RelationAffordance describes per-relation-type affordances on the
// wire. Sparse: `Creatable` and `Removable` are nil when the default
// (true) holds. `Fields` lists meta-field writability overrides, also
// sparse.
type RelationAffordance struct {
	Creatable *bool                      `json:"creatable,omitempty"`
	Removable *bool                      `json:"removable,omitempty"`
	Fields    map[string]FieldAffordance `json:"fields,omitempty"`
}

// Transition is one resolved outgoing move of a state-machine-typed property
// on a per-entity response (TKT-3G93B8). It is the wire projection of
// statemachine.TransitionVerdict.
//
// To is the target value. Label is optional display text for the MOVE (the
// action, e.g. "Start progress"); when empty the SPA falls back to the target
// value's display label. Guard is the ACL permission the move requires (empty
// when unguarded). Allowed reports whether the requesting principal may perform
// it now (guard held AND precondition met). Reason names the blocking gate when
// Allowed is false ("guard" or "precondition"); empty when Allowed is true. The
// SPA shows only Allowed transitions, so Reason is advisory (tooltip/CLI), not a
// rendering gate.
type Transition struct {
	To      string `json:"to"`
	Label   string `json:"label,omitempty"`
	Guard   string `json:"guard,omitempty"`
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

// Attachment describes one file attached to a `file`-type property, as
// surfaced on a per-entity GET response. ID is the file's identifier
// within the property (its normalized file name) — used to build the
// per-file download/delete URL. Href is the download URL for the bytes (an
// ACL-gated endpoint that inherits the owning entity's read permission).
// ContentType is inferred from the filename — the store does not persist
// it on every backend.
type Attachment struct {
	ID          string `json:"id"`
	FileName    string `json:"filename"`
	Size        int64  `json:"size"`
	ContentType string `json:"contentType"`
	Href        string `json:"href"`
}

// InaccessibleField describes a property that is known to exist but
// whose value is unreadable by the holder of the entity (e.g. the file
// is git-crypt encrypted and the key is not present locally).
type InaccessibleField struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// EntityWorld is the provenance of a resolved face — see [Entity.World] for
// why a client needs it.
//
// It describes the RESOLUTION that produced this response, never the entity's
// other faces. A client learns which coordinate it is looking at; it learns
// nothing about which coordinates exist for this id. That distinction is the
// existence oracle the read gate closes, and this shape stays on the safe
// side of it deliberately.
type EntityWorld struct {
	// Name is the world this response was resolved in — `default` for the
	// implicit total world. Matches a key of [Schema.Worlds].
	Name string `json:"name"`
	// Face is the coordinate the served state was stored at. EMPTY means
	// the default state, which is not the same claim as "the default world":
	// a fallback under `otherwise: default` also serves the default state.
	// Via is what separates those, which is why Face alone is not enough.
	Face string `json:"face"`
	// Via names the resolution rule that chose this face:
	//
	//   - "unscoped"         — this world applies no per-type resolution, so
	//                          the entity contributes its default state.
	//                          Covers BOTH a type that declares no content
	//                          states and the total default world, which
	//                          resolves everything this way — the default
	//                          world is exactly "no resolution applied", and
	//                          worldreader.Rule reports it identically.
	//   - "chain"            — a coordinate the world SELECTS exists; this is
	//                          the face the world asked for.
	//   - "fallback-default" — no selected coordinate exists, and the world's
	//                          `otherwise: default` stood the default state
	//                          in. The reader is seeing a substitute.
	//
	// The third value is the load-bearing one: it is the difference between
	// "published" and "not published, showing you the draft".
	//
	// There is deliberately no "excluded" value. A world that excludes an
	// entity produces no response to carry provenance on — it is a 404,
	// indistinguishable from a genuine miss, because existence in a world IS
	// the publication bit.
	//
	// Via alone does NOT tell a reader whether they got the world's FIRST
	// choice — see [EntityWorld.ChainPosition], which is the field for that.
	Via string `json:"via"`

	// ChainPosition is the 0-based index, within the world's candidate chain
	// for this entity type, of the coordinate actually served. It is present
	// only when Via is "chain"; the other rules did not resolve through the
	// chain at all.
	//
	// # Why Via is not enough on its own
	//
	// "chain" means SOME coordinate the world selects exists — never which.
	// Under `select: [published, draft]` a genuine published hit and a
	// fallback to the draft both report "chain" identically, so a reader
	// shown draft bytes under a `published` world had no way to know. That
	// was the leak content states exist to prevent, reported on a live demo:
	// the page said "read-only" and offered "Go to draft" while already
	// showing the draft.
	//
	// Position 0 is the world's first choice — what it asked for. Any
	// position > 0 is a WITHIN-CHAIN FALLBACK: a later candidate stood in
	// because every earlier one is absent. That is a substitute exactly as
	// "fallback-default" is, and clients must label it as one.
	//
	// # Why a position and not a boolean
	//
	// A chain is a ranked preference, not a pair. `select: [nl, en, de]`
	// serving `de` is a different statement from serving `en`, and the
	// operator vocabulary a UI needs ("showing German, no Dutch or English
	// available") needs the rank, not just "not first". A boolean would have
	// to be widened the first time a chain has three entries.
	//
	// It is a pointer so that ABSENT and 0 stay distinguishable: 0 is the
	// strongest claim this field makes (the world got exactly what it asked
	// for), and a plain int would make every non-chain rule report it by
	// default. Omitted from the wire when nil.
	ChainPosition *int `json:"chain_position,omitempty"`
}

// ListResponse is the response for listing entities.
type ListResponse struct {
	Data    []Entity        `json:"data"`
	Meta    ListMeta        `json:"meta"`
	Actions map[string]bool `json:"_actions,omitempty"`
}

// ListMeta contains pagination metadata.
type ListMeta struct {
	Total   int  `json:"total"`
	Page    int  `json:"page"`
	PerPage int  `json:"per_page"`
	HasMore bool `json:"has_more"`
}

// Schema is the JSON representation of the metamodel.
type Schema struct {
	Entities  map[string]EntityType   `json:"entities"`
	Relations map[string]RelationType `json:"relations"`
	Types     map[string]CustomType   `json:"types,omitempty"`
	// Worlds enumerates the declared worlds a client may pass to `?world=`,
	// keyed by world name, plus the implicit `default` world (TKT-WRLDAPI).
	//
	// Present even for a project declaring no `worlds:` block, where it holds
	// the single `default` entry: a client needs to distinguish "this server
	// has no other worlds" from "this server is too old to tell me", and an
	// omitted key cannot say the first.
	Worlds map[string]World `json:"worlds,omitempty"`
}

// World is the JSON representation of one declared world — a named
// resolution function picking at most one content state per entity (design
// doc §4.1).
//
// # The declared set is NOT filtered per principal
//
// Every world the schema declares appears here for every caller. World names
// are operator-authored config living in `schema.yaml`, routinely a public
// repo, so their contents are already disclosed and CLAUDE.md is explicit
// that code must not contort to conceal a config name. What IS per-principal
// is [World.Readable] — which says whether this caller may SELECT the world,
// never whether it exists.
//
// The distinction is load-bearing: what a world CONTAINS is secret (a denied
// world serves an empty result, never a 403, precisely so it cannot be told
// apart from a world holding nothing), while the fact that the operator
// declared a world named `published` is not.
//
// This carries no per-ENTITY information. Which faces a given entity has is
// exactly the existence oracle the read gate closes, and nothing here
// narrows it: a type's declared coordinates ([EntityType.Faces]) say what
// the schema permits, never what any row holds.
// Note the field set is deliberately incomplete: a world's `edits:` key
// (which state edits made from it land in) is NOT surfaced. It is a
// write-side concept, and the affordance a client actually needs — "which
// faces can I create from here" — is a separate gated query over the
// declared copy definitions (Ruling 9), not something derivable from a world.
type World struct {
	// Select is the ordered candidate chain: the first coordinate that
	// EXISTS for an entity is the face served. Empty for the default world,
	// which resolves every entity to its default state by construction.
	Select []string `json:"select,omitempty"`
	// Overrides replaces Select for the named entity types, keyed by type.
	Overrides map[string][]string `json:"overrides,omitempty"`
	// Otherwise is the rule-3 policy for a type that declares faces but
	// none this world selects: `exclude` (contributes nothing) or `default`
	// (falls back to the type's default state). Empty for the default world,
	// which never reaches rule 3.
	Otherwise string `json:"otherwise,omitempty"`
	// PrimaryFor names the faces this world is the canonical home of — the
	// operator's tie-break when several worlds head the same face for a type
	// (TKT-MFVH03). Absent for every unambiguous schema, which is all of them
	// until a second world leads a face someone already leads.
	//
	// A client resolving "which world serves this face" infers it from the
	// chains and consults this only to break a tie. It cannot CONTRADICT the
	// chains — the server refuses such a schema at load — so a client may
	// trust that a claimed face is one this world actually heads.
	//
	// Config, not data: like Select and Banner, this is operator-authored and
	// public, served identically to every principal.
	PrimaryFor []string `json:"primary_for,omitempty"`
	// Banner is operator-authored text announcing this world to a reader
	// ("DRAFT — not in force"). Empty means announce nothing — a language
	// world needs no banner, an editorial one does.
	//
	// Display-only, and it gates ONLY the announcement: a non-default world
	// is read-only on this API regardless, so the read-only note and the way
	// back to the default face are not suppressible by config.
	//
	// Config, not data: world names and their banners are operator-authored
	// and public, so this is served identically to every principal.
	Banner string `json:"banner,omitempty"`
	// Messages is the operator's wording for what this world changes on a
	// screen (TKT-5SZG2L). Each entry is optional and an absent one renders
	// nothing: the web app has no sentence of its own for any of them.
	// Placeholders `{face}`, `{world}` and `{title}` are substituted by the
	// client. Mirrors metamodel.WorldMessages.
	Messages *WorldMessages `json:"messages,omitempty"`
	// OnAbsent is the behavior for an entity with no face in this world.
	// Mirrors metamodel.WorldOnAbsent.
	OnAbsent *WorldOnAbsent `json:"on_absent,omitempty"`
	// Readable reports whether THIS caller may select the world via
	// `?world=`. False means a request naming it is served an empty result
	// rather than a 403 — so a client that respects this flag shows the user
	// an honest selector instead of a world that silently returns nothing.
	//
	// A UI hint, never a boundary: the server re-checks the grant on every
	// request (`resolveWorld`), so a client ignoring this learns nothing it
	// could not learn by asking.
	//
	// NO omitempty, unlike Default below, and the asymmetry is deliberate:
	// `false` is the load-bearing answer here — it is precisely what a
	// selector needs — and an omitted key would be indistinguishable from a
	// server too old to compute it. `default: false`, by contrast, is noise
	// on every declared world.
	Readable bool `json:"readable"`
	// Default marks the implicit default world — today's graph, total by
	// construction, always present and always selectable. Spelled as a flag
	// rather than left for the client to infer from the reserved name, so a
	// selector can label it without hardcoding the string.
	Default bool `json:"default,omitempty"`
}

// EntityType is the JSON representation of an entity type.
type EntityType struct {
	Label       string                 `json:"label"`
	Plural      string                 `json:"plural"`
	Description string                 `json:"description,omitempty"`
	Primary     string                 `json:"primary,omitempty"`
	IDType      string                 `json:"id_type,omitempty"`
	IDPrefix    string                 `json:"id_prefix,omitempty"`
	IDPrefixes  []string               `json:"id_prefixes,omitempty"`
	Properties  map[string]PropertyDef `json:"properties"`
	// Faces declares this type's content-state coordinates, keyed by
	// coordinate name ("draft", "published", "en", "nl"). ABSENT for the
	// common case of a type with no content states, which contributes its
	// single default state to every world.
	//
	// This is SCHEMA, not data: it says which coordinates the operator
	// declared for the type, never which faces any particular entity holds.
	// Two entities of the same type report identical faces whether one has
	// a published face and the other does not.
	//
	// DO NOT add the per-entity variant here — "which faces does THIS id
	// have" is the existence oracle the row gate exists to close, since
	// existence in a world IS the publication bit. An "other faces" indicator
	// is a real product need (Ruling 9 item 8), but it has to omit faces the
	// viewer may not read, which makes it a gated per-entity query rather
	// than a field on the type's schema.
	Faces map[string]FaceDef `json:"faces,omitempty"`

	// Commentable reports that this type accepts comments (TKT-FIO205), so the
	// SPA knows whether to offer a comment affordance at all. Policy, not
	// permission: it says commenting is POSSIBLE here, never that the current
	// principal may do it — the `comment:*` grants answer that, and the server
	// re-authorizes every call regardless of what this flag says.
	//
	// Omitted when false, so a project with no `comments:` block serves a
	// schema byte-identical to one built before the feature existed.
	Commentable bool `json:"commentable,omitempty"`

	// Pacts lists the external systems this type is twinned with and which
	// fields each owns, sorted by system, so the SPA can explain why a field
	// is read-only. Instructions (the agent brief) are not served.
	//
	// Omitted when the type declares no pacts, so a project without `pacts:`
	// serves a schema byte-identical to one built before the feature existed.
	Pacts []Pact `json:"pacts,omitempty"`
}

// Pact is the JSON representation of one entity type's contract with an
// external system, mirroring metamodel.Pact minus its instructions.
type Pact struct {
	System string `json:"system"`
	Scope  string `json:"scope"`
	// Theirs are the fields the external system owns; ["*"] means every
	// field except computed properties, which are always rela's. Shared are
	// edited by both sides. Propose are rela-owned fields whose external edits
	// are proposed rather than reverted. All sorted; empty lists are served as
	// [] rather than omitted.
	Theirs  []string `json:"theirs"`
	Shared  []string `json:"shared"`
	Propose []string `json:"propose"`
}

// WorldMessages is the operator's chrome text for one world. See
// [World.Messages].
type WorldMessages struct {
	// Absent is the detail-page note for an entity with no face in the world.
	Absent string `json:"absent,omitempty"`
	// Projection is the list/board note for a type that declares faces.
	Projection string `json:"projection,omitempty"`
	// StandIn is the badge text for a stand-in row or card; empty means no
	// badge.
	StandIn string `json:"stand_in,omitempty"`
}

// WorldOnAbsent is the behavior for an entity with no face in a world.
type WorldOnAbsent struct {
	// Redirect names the world the client navigates to instead of rendering
	// the absent page. Validated at schema load.
	Redirect string `json:"redirect,omitempty"`
}

// FaceMessages is the operator's chrome text for one face. See
// [FaceDef.Messages].
type FaceMessages struct {
	// ReadOnly is the note for a page or form showing this face while the
	// reader may not write it. Empty renders nothing.
	ReadOnly string `json:"read_only,omitempty"`
	// Notice is the note for a page showing this face whatever the reader
	// may do with it — a draft is not in force even for the editor
	// rewriting it. Empty renders nothing. Independent of ReadOnly; both
	// may be set, and the detail page renders Notice first.
	Notice string `json:"notice,omitempty"`
}

// Face is the JSON representation of one declared content state,
// mirroring metamodel.FaceDef.
//
// Near-empty by design: a state is IDENTIFIED by its coordinate, and the only
// knob beyond identity is display text. Per-state ACL hints and retention are
// not added speculatively.
type FaceDef struct {
	// Label is the operator's display text for this face, omitted when
	// undeclared so a client falls back to the coordinate name (which is
	// itself operator-authored config).
	//
	// Display-only. This is the TYPE's vocabulary, so a client can name a
	// face it has not fetched — a "return to the default face" button must
	// say "Go to English" before it has loaded anything English.
	Label string `json:"label,omitempty"`
	// Messages is the operator's chrome text about this face (TKT-5SZG2L).
	// Nil when none is declared. Mirrors metamodel.FaceMessages.
	Messages *FaceMessages `json:"messages,omitempty"`
}

// PropertyDef is the JSON representation of a property definition.
type PropertyDef struct {
	Type        string            `json:"type"`
	Required    bool              `json:"required"`
	Default     string            `json:"default,omitempty"`
	Values      []string          `json:"values,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"` // Display labels keyed by value; value stays the wire identity
	Description string            `json:"description,omitempty"`
	List        bool              `json:"list,omitempty"`
	// Max is the attachment cap for a `file` property (default 1). The SPA's
	// file widget reads it to switch between replace-mode and multi-file
	// add-mode. Omitted unless set above 1.
	Max int `json:"max,omitempty"`
}

// RelationType is the JSON representation of a relation type.
type RelationType struct {
	Label       string                 `json:"label"`
	Description string                 `json:"description,omitempty"`
	From        []string               `json:"from"`
	To          []string               `json:"to"`
	Inverse     *InverseDef            `json:"inverse,omitempty"`
	Symmetric   bool                   `json:"symmetric,omitempty"`
	MinOutgoing *int                   `json:"min_outgoing,omitempty"`
	MaxOutgoing *int                   `json:"max_outgoing,omitempty"`
	MinIncoming *int                   `json:"min_incoming,omitempty"`
	MaxIncoming *int                   `json:"max_incoming,omitempty"`
	Properties  map[string]PropertyDef `json:"properties,omitempty"`
	// Orderable, when set, declares that the frontend may offer drag-to-reorder
	// controls on the corresponding side. The managed property names are
	// always the reserved `_order_out` (outgoing) and `_order_in` (incoming).
	Orderable *RelationOrderable `json:"orderable,omitempty"`
}

// RelationOrderable describes per-side orderability for a relation type.
type RelationOrderable struct {
	Outgoing bool `json:"outgoing,omitempty"`
	Incoming bool `json:"incoming,omitempty"`
}

// InverseDef mirrors metamodel.InverseDef on the wire. The SPA reads
// `inverse.id` to find the inverse body key for incoming-direction
// edits routed through the unified PATCH (TKT-GFQK).
type InverseDef struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// CustomType is the JSON representation of a custom type.
type CustomType struct {
	Values  []string          `json:"values"`
	Labels  map[string]string `json:"labels,omitempty"` // Display labels keyed by value; value stays the wire identity
	Default string            `json:"default,omitempty"`
}

// Config is the JSON representation of the UI config.
type Config struct {
	App AppConfig `json:"app"`
	// AboutDescription is the deployment description shown by the SPA's global
	// "About" help (TKT-DUQBD0): the data-entry.yaml `app.description`, falling
	// back to the metamodel's top-level `description`. Distinct from
	// AppConfig.Description (which SettingsView renders as a plain one-liner) so
	// a multi-paragraph markdown metamodel description doesn't leak into that
	// view. Empty → the About button is hidden.
	AboutDescription string                                      `json:"about_description,omitempty"`
	Styles           map[string]map[string]string                `json:"styles"`
	Forms            map[string]dataentryconfig.Form             `json:"forms"`
	Lists            map[string]dataentryconfig.List             `json:"lists"`
	Views            map[string]dataentryconfig.ViewConfig       `json:"views"`
	EntityViews      map[string]dataentryconfig.EntityViewConfig `json:"entity_views,omitempty"`
	Kanbans          map[string]dataentryconfig.Kanban           `json:"kanbans"`
	Calendars        map[string]dataentryconfig.Calendar         `json:"calendars,omitempty"`
	Gantts           map[string]dataentryconfig.Gantt            `json:"gantts,omitempty"`
	Dashboard        *dataentryconfig.DashboardConfig            `json:"dashboard,omitempty"`
	Actions          map[string]dataentryconfig.Action           `json:"actions,omitempty"`
	Navigation       []dataentryconfig.NavigationEntry           `json:"navigation"`
	Documents        map[string]dataentryconfig.DocumentConfig   `json:"documents,omitempty"`
	Apps             map[string]App                              `json:"apps,omitempty"`
	Palette          *dataentryconfig.ResolvedPalette            `json:"palette,omitempty"`

	// NextActionBands is the operator's ordered priority vocabulary, so the
	// SPA can label a suggestion's band ("Someone is waiting") rather than
	// echoing a raw id.
	//
	// The SOURCES are deliberately NOT here. A suggestion arrives fully
	// resolved from /_next_action — message already interpolated, affordances
	// attached — so the SPA never needs the rules, and shipping them would
	// invite a client-side re-implementation of the engine. Same reasoning as
	// "no useACL() composable": the SPA renders what the server computed.
	NextActionBands []dataentryconfig.NextActionBand `json:"next_action_bands,omitempty"`
}

// App is the client-facing view of a custom app. It deliberately omits the
// on-disk File path and the csp_origins allow-list — the SPA only needs enough
// to render a sidebar entry and route to /app/{id}; the HTML is fetched from
// GET /api/v1/_apps/{id}.
type App struct {
	Title       string `json:"title,omitempty"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
}

// AppConfig is the JSON representation of the app config.
type AppConfig struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// PlantUMLServerURL is the configured PlantUML server base URL, or empty
	// when PlantUML rendering is disabled. The SPA treats a non-empty value as
	// the on switch for ```plantuml diagram rendering.
	PlantUMLServerURL string `json:"plantuml_server_url,omitempty"`
	// DefaultWorld names the world a request lands in when no `?world=` is
	// given. Empty means the default world (raw stored faces).
	//
	// A browsing default, not a grant: the world's read permission is
	// re-checked per request exactly as for an explicit `?world=`, so this
	// can only change which face a bare URL resolves to, never who may see
	// it. Validated at load against the declared worlds.
	DefaultWorld string `json:"default_world,omitempty"`
	// HistoryEnabled reports whether THIS DEPLOYMENT can serve version
	// history. Content versioning is a postgres-only, OPTIONAL store
	// capability (TKT-9INY0Y): fsstore and memstore do not implement
	// store.HistoryReader, and `/_history/...` answers them with a named
	// 501 (`history_unsupported`).
	//
	// It rides `/_config` because the capability is per-DEPLOYMENT, not
	// per-entity — putting it on every entity response would repeat one
	// constant on every row, and the SPA loads this config once.
	//
	// It exists so a client can render the affordance ABSENT rather than
	// present-and-broken. An ungated History button on an fs deployment can
	// only 501, which is the affordance-that-lies shape this app refuses
	// elsewhere (a denied copy is absent, not disabled). Not a permission —
	// history is still ACL-gated per entity when the capability exists.
	HistoryEnabled bool `json:"history_enabled"`
}

// Error is an RFC 7807 Problem Details response.
type Error struct {
	Type     string       `json:"type"`
	Title    string       `json:"title"`
	Status   int          `json:"status"`
	Detail   string       `json:"detail,omitempty"`
	Instance string       `json:"instance,omitempty"`
	Errors   []FieldError `json:"errors,omitempty"`
}

// FieldError represents a validation error on a specific field.
type FieldError struct {
	Source ErrorSource `json:"source"`
	Code   string      `json:"code"`
	Detail string      `json:"detail"`
}

// ErrorSource points to the location of an error: an RFC 6901 JSON pointer
// into the request body.
type ErrorSource struct {
	Pointer string `json:"pointer"`
}

// SidePanelSection represents a section in the side panel response.
type SidePanelSection struct {
	Heading      string            `json:"heading"`
	SectionID    string            `json:"sectionId"`
	Display      string            `json:"display"`
	IsEmpty      bool              `json:"isEmpty"`
	EmptyMessage string            `json:"emptyMessage,omitempty"`
	Fields       []SectionField    `json:"fields,omitempty"`
	Entities     []SidePanelEntity `json:"entities,omitempty"`
	AddInfo      *ViewAddInfo      `json:"addInfo,omitempty"`
	LinkInfo     *ViewLinkInfo     `json:"linkInfo,omitempty"`
}

// SectionField represents a field in a side panel section.
// Values is always an array so that list-typed properties retain per-item
// structure; scalar properties become a one-element array. Empty fields emit
// an empty array (omitted via omitempty when nil).
//
// Property carries the raw property name so consumers can correlate the
// field with metamodel data (e.g. inaccessibility lookup); Label is the
// human-readable rendering. Inaccessible is true when the underlying entity
// is git-crypt encrypted — the field is known to exist in the schema but
// its value cannot be read.
// Span is the field's width on the 12-column layout grid (0 = full width).
//
// Render is the already-resolved render mode ("display" | "input", TKT-HOIX1),
// set server-side from the section + field config. View sections and cards/list
// rows honor it; the side-panel renderer does not implement inline edit today
// and ignores it.
//
// Field order and types must stay in lockstep with dataentry.SectionFieldData:
// the handlers convert between them with a direct struct conversion, so the
// compiler is what keeps the wire surface and the internal DTO from drifting.
type SectionField struct {
	Property     string   `json:"property,omitempty"`
	Label        string   `json:"label"`
	Values       []string `json:"values,omitempty"`
	PropType     string   `json:"propType,omitempty"`
	Inaccessible bool     `json:"inaccessible,omitempty"`
	Span         int      `json:"span,omitempty"`
	Render       string   `json:"render,omitempty"`
	Widget       string   `json:"widget,omitempty"`
}

// SidePanelEntity represents an entity in a side panel section.
type SidePanelEntity struct {
	ID         string         `json:"id"`
	Title      string         `json:"title"`
	Type       string         `json:"type"`
	EditFormID string         `json:"editFormId,omitempty"`
	Fields     []SectionField `json:"fields,omitempty"`
	Content    string         `json:"content,omitempty"`
	HasContent bool           `json:"hasContent"`
}

// SidebarItem represents a navigation item.
type SidebarItem struct {
	Label string `json:"label"`
	Href  string `json:"href"`

	// Icon is the glyph to render, or the reserved "none" when the author asked
	// for no glyph at all. "none" travels as itself rather than as "" because
	// this field is omitempty: an empty string vanishes from the payload and
	// becomes indistinguishable from an entry that simply never had an icon.
	Icon string `json:"icon,omitempty"`

	// DerivedIcon carries the glyph this entry's KIND would have produced, and
	// is populated only when Icon suppresses it.
	//
	// The collapsed sidebar hides every label, so a row with no icon and no
	// label would be invisible yet still clickable. It falls back to this glyph
	// instead — which the client cannot reconstruct on its own, since the
	// suppression already happened by the time the payload is built.
	DerivedIcon string `json:"derivedIcon,omitempty"`

	Action string `json:"action,omitempty"`

	// Entities is set on an `entities:` entry (TKT-PEKL8L): the query the SPA
	// runs against the list endpoint, one sidebar link per row. Href and
	// Label stay empty; the rows supply both.
	//
	// Only the query definition travels here, never the rows. They are
	// entity content, so they come from the ACL-scoped list endpoint, and
	// this payload stays free of per-principal data beyond the permission
	// filter.
	Entities *SidebarEntities `json:"entities,omitempty"`
}

// SidebarEntities is the list query behind an `entities:` navigation entry,
// in the list endpoint's own parameter grammar.
type SidebarEntities struct {
	// Type is the entity type; the SPA maps it to the endpoint's URL segment.
	Type string `json:"type"`
	// QueryScope is sent as `?query_scope=`; empty applies the type's default.
	QueryScope string `json:"query_scope,omitempty"`
	// Sort is the effective order in `sort=` grammar (`title,-due`), already
	// resolved to the type's default_sort when the entry names none; empty
	// means id order.
	Sort string `json:"sort,omitempty"`
}

// SidebarGroup represents a navigation group with items.
type SidebarGroup struct {
	Group     string        `json:"group,omitempty"`
	Collapsed bool          `json:"collapsed,omitempty"`
	Items     []SidebarItem `json:"items"`
}

// DashboardResponse contains the dashboard page config with the cards this
// principal may see (TKT-53KICM).
//
// This exists as a separate endpoint from `_config` because the card list is
// per-principal while `_config` is deliberately identical for everyone (root
// CLAUDE.md, "The configuration is not a secret; the data is"). `_config` still
// carries the full `dashboard:` block; only this response is filtered, and only
// so a user is not offered a card they cannot act on.
//
// Cards is always a non-nil slice: a project with no `dashboard:` configured,
// one with an empty `cards:`, and one where every card was filtered all
// serialize as `[]`, so the SPA has a single "render what you got" path.
type DashboardResponse struct {
	Title       string                          `json:"title,omitempty"`
	Description string                          `json:"description,omitempty"`
	Cards       []dataentryconfig.DashboardCard `json:"cards"`
}

// SidebarResponse contains the sidebar data with app info and navigation.
type SidebarResponse struct {
	App        AppConfig      `json:"app"`
	Navigation []SidebarGroup `json:"navigation"`
	// LogoURL is the cache-busted URL of the user-uploaded sidebar logo,
	// or nil when no logo is set. Included here (rather than in
	// `_settings`) so the SPA can render the logo on first paint without
	// blocking on a settings fetch.
	LogoURL *string `json:"logoUrl,omitempty"`

	// InlineCreate maps an entity type to the form id the SPA should use
	// to create one inline from a relation field (TKT-OMUD56). A type
	// appears ONLY when both conditions hold: the principal may create it,
	// and a create form resolves for it. So presence alone is the offer —
	// the client needs no second lookup and no permission arithmetic.
	//
	// Sending the resolved form id (rather than letting the client find
	// its own) keeps `createFormForType`'s ordering authoritative in one
	// place; the natsort-and-prefer-non-edit rule is not reimplemented
	// client-side where it could silently diverge.
	//
	// It rides on the sidebar because this is the one boot-time payload
	// that is already principal-scoped: `_config` is pinned
	// principal-INDEPENDENT (TestNavPermission_ConfigUnfiltered) and
	// `_schema` is a pure metamodel projection.
	//
	// A UI hint, never authorization: POST /api/v1/{plural} re-authorizes,
	// so a stale or forged map can only surface a button that then 403s.
	InlineCreate map[string]string `json:"inline_create,omitempty"`
}

// ConflictItem represents a conflicted file.
type ConflictItem struct {
	Path        string `json:"path"`
	EntityType  string `json:"entity_type,omitempty"`
	EntityID    string `json:"entity_id,omitempty"`
	MarkerCount int    `json:"marker_count"`
}

// ConflictsResponse contains the list of conflicts.
type ConflictsResponse struct {
	Conflicts []ConflictItem `json:"conflicts"`
	Count     int            `json:"count"`
}

// PropertyDiff represents a property difference.
type PropertyDiff struct {
	Property    string `json:"property"`
	OursValue   string `json:"ours_value"`
	TheirsValue string `json:"theirs_value"`
	IsSame      bool   `json:"is_same"`
}

// ConflictDetail contains detailed info for resolving a conflict.
type ConflictDetail struct {
	Path          string         `json:"path"`
	EntityType    string         `json:"entity_type,omitempty"`
	EntityID      string         `json:"entity_id,omitempty"`
	PropertyDiffs []PropertyDiff `json:"property_diffs"`
	ContentSame   bool           `json:"content_same"`
	ContentOurs   string         `json:"content_ours,omitempty"`
	ContentTheirs string         `json:"content_theirs,omitempty"`
}

// ConflictResolveRequest contains the resolution choices.
type ConflictResolveRequest struct {
	Path            string            `json:"path"`
	PropertyChoices map[string]string `json:"property_choices"`
	ContentChoice   string            `json:"content_choice"`
	ManualContent   string            `json:"manual_content,omitempty"`
}

// DocumentResponse contains the rendered document content.
type DocumentResponse struct {
	HTML      string   `json:"html"`
	Cached    bool     `json:"cached"`
	EntityIDs []string `json:"entity_ids"` // IDs of entities involved in this document (for SSE filtering)
}

// Command is the JSON representation of an available command.
//
// There is deliberately no auto_open here. TKT-93FUCV removed the server-side
// launcher it selected, so the key would have advertised a capability the
// server no longer has. The YAML key is still parsed (and stripped by the
// `command-auto-open` migration) so existing projects keep loading.
type Command struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Confirm string `json:"confirm,omitempty"`
	Context string `json:"context"`
}

// Template represents a template for API responses.
type Template struct {
	Name       string             `json:"name"`
	Properties map[string]any     `json:"properties"`
	Content    string             `json:"content"`
	Relations  []TemplateRelation `json:"relations"`
}

// TemplateRelation represents a pre-filled relation in a template.
type TemplateRelation struct {
	Relation string `json:"relation"`
	Target   string `json:"target"`
}

// ViewResponse contains the executed view data.
//
// Mentions carries the implicit-relation set discovered by scanning the
// entry and section markdown contents for bare-content entity-ID code
// spans (see collectMentions). The SPA's markdown renderer consumes this
// map to rewrite those code spans into titled in-app links. Mirrors the
// Lua-side `rela.md.entity_refs` shape (TKT-LXYHQ) for SPA consumers
// that don't go through the Lua document path.
//
// Wire stability: `mentions` is part of the public v1 API. The set of
// `inaccessible_reason` values may grow as new locking mechanisms are
// added (today: `git-crypt`); clients must treat unknown reasons as
// opaque rather than enumerating them.
type ViewResponse struct {
	Entry    Entity             `json:"entry"`
	Sections []ViewSection      `json:"sections"`
	Mentions map[string]Mention `json:"mentions,omitempty"`
	// WorldAbsent marks a response for an entity that EXISTS but has no face
	// in the requested world — the ordinary state of an unpublished draft
	// under a filtering world, not an error.
	//
	// It is a 200 rather than a 4xx deliberately. The caller was already
	// cleared to read this entity (the row gate runs before view execution and
	// is world-independent by guard rule 1), and the useful response is not
	// "no" but "not here — here is where it is": `Entry` carries the face that
	// DOES exist, with its `_faces` and `_world`, so the page can offer a way
	// through instead of rendering a dead end. Rendered as an error, the SPA
	// discards the body and the user is told the thing they just created does
	// not exist.
	//
	// Omitted when false, so an ordinary view response is byte-identical to
	// what it was before this field existed.
	WorldAbsent bool `json:"_world_absent,omitempty"`

	// Create is the page-header create menu: the sections that opted into
	// `create.in: [header]`, deduped by relation and in section order
	// (TKT-R4BMJM).
	//
	// Assembled server-side so the SPA renders what it is given rather than
	// re-deriving which section maps to which relation. Omitted when no section
	// opted in — which is every view that predates this field.
	Create []ViewSectionCreate `json:"create,omitempty"`
}

// ViewSection represents a section with resolved data.
type ViewSection struct {
	Heading      string         `json:"heading"`
	SectionID    string         `json:"sectionId"`
	Display      string         `json:"display"`
	IsEmpty      bool           `json:"isEmpty"`
	EmptyMessage string         `json:"emptyMessage,omitempty"`
	Fields       []SectionField `json:"fields,omitempty"`
	Entities     []ViewEntity   `json:"entities,omitempty"`
	Columns      []ViewColumn   `json:"columns,omitempty"`
	Rows         []ViewRow      `json:"rows,omitempty"`
	Groups       []ViewGroup    `json:"groups,omitempty"`
	IsGrouped    bool           `json:"isGrouped"`
	Content      string         `json:"content,omitempty"`
	HasContent   bool           `json:"hasContent"`

	// Tree carries the parent rows of a `display: nested` section. Omitted for
	// every other display mode, so a response that has no nested section is
	// byte-identical to one produced before this field existed.
	Tree []ViewTreeNode `json:"tree,omitempty"`
	// Truncated reports that some VISIBLE row in Tree was not emitted — a
	// parent the node budget could not afford, or children past a parent's
	// preview cap. Never set merely because the budget ran out, so it cannot be
	// used to probe how much is being withheld.
	//
	// Coarser than a node's HasMoreChildren and implied by it: a parent over
	// the preview cap sets both. This flag exists for what HasMoreChildren
	// cannot express — a parent dropped entirely has no row to signal on.
	Truncated bool `json:"truncated,omitempty"`

	// Create is this section's opt-in create affordance (TKT-R4BMJM).
	//
	// Omitted unless the section's config carries a `create:` block AND the
	// principal may create at least one type the relation reaches, so a view that
	// did not opt in is byte-identical to what it was before this field existed.
	// That is what keeps TKT-651W's read-only default intact and lets
	// TestV1Views_NoAddOrLinkInfoOnSections keep asserting absence.
	Create *ViewSectionCreate `json:"create,omitempty"`
}

// ViewSectionCreate describes a section's create-related affordance.
//
// Deliberately NOT [ViewAddInfo], whose doc comment forbids reuse from a view
// response. The two also differ in substance: this one carries the flow and a
// per-target template, and its targets are ACL-gated.
//
// Relation, LinkAs and PeerID are computed server-side but travel to the client
// and back — in the page flow they land in an editable URL. They are a
// CONVENIENCE, not a trust anchor: the write path re-authorizes the create and
// the edge independently, and cannot verify that a section ever offered this
// particular triple.
type ViewSectionCreate struct {
	Relation string `json:"relation"`
	// LinkAs is the role of the NEW entity: "to" when the section was collected
	// by following outgoing edges from the entry, "from" for incoming.
	LinkAs string `json:"linkAs"`
	// PeerID is the entry entity the new entity gets linked to.
	PeerID string `json:"peerId"`
	// Flow is "modal" or "page".
	Flow string `json:"flow"`
	// Heading labels this entry in the page-header menu. Carried only on
	// ViewResponse.Create, where the section it came from is not otherwise named.
	Heading string                    `json:"heading,omitempty"`
	Targets []ViewSectionCreateTarget `json:"targets"`
}

// ViewSectionCreateTarget is one offerable entity type.
//
// Presence in the list IS the affordance: the server includes a type only when
// a form resolves for it AND the principal may create it, so the client does no
// permission arithmetic (the rule TKT-OMUD56 settled).
type ViewSectionCreateTarget struct {
	EntityType string `json:"entityType"`
	FormID     string `json:"formId"`
	Label      string `json:"label"`
	// Template preselects an entity template variant, or "" for the form's own
	// default.
	Template string `json:"template,omitempty"`
}

// ViewEntity represents an entity in a view section.
//
// Props and FieldAffordances (TKT-IHC7D) carry the typed property
// values and per-cell writability verdict that inline-edit hosts on
// cards/list view sections consume. Both are hidden-property-stripped
// — the consumer can assume:
//
//   - `keys(Props) ∩ hidden(e) == ∅` (hidden properties never leak via
//     this surface)
//   - `keys(FieldAffordances) ∩ hidden(e) == ∅` (same for the verdict)
//   - `FieldAffordances` may have keys absent from `Props` when the
//     property has no stored value but a non-default verdict (e.g.
//     `writable: false` on an unset field)
//
// The pointer-to-map idiom on `FieldAffordances` mirrors
// `Entity.FieldAffordances`: `nil` means "absent on the wire"
// (table rows / non-cards paths), `&{}` means "evaluated, no
// deviations" (closed-world signal matching `_actions`).
//
// `Props` is a plain map with `omitempty`: presence/absence is
// sufficient, no closed-world semantic is needed.
type ViewEntity struct {
	ID               string                      `json:"id"`
	Title            string                      `json:"title"`
	Type             string                      `json:"type"`
	EditFormID       string                      `json:"editFormId,omitempty"`
	Fields           []SectionField              `json:"fields,omitempty"`
	Content          string                      `json:"content,omitempty"`
	HasContent       bool                        `json:"hasContent"`
	Props            map[string]any              `json:"_props,omitempty"`
	FieldAffordances *map[string]FieldAffordance `json:"_fields,omitempty"`
	// World is the PROVENANCE of this collection entity's face — which world
	// resolved it, which coordinate it was stored at, and which rule chose it
	// (TKT-WRLDAPI item 4b).
	//
	// This is the slot the per-neighbor provenance deferred in item 4 belongs
	// in, and the reason 4b exists. A view collection carries whole ENTITIES,
	// unlike the entity GET's `relations` map, which carries bare id strings
	// and so had nowhere to put this without a wire-type change.
	//
	// It lets a client distinguish "the Dutch page" from "the English page,
	// because no Dutch face exists" for each item in a collection — the same
	// question [Entity.World] answers for a single entity, which arrives
	// byte-identically either way and cannot be re-derived client-side without
	// the chain and the fallback policy.
	//
	// Present only under a NON-DEFAULT world. Under the default world every
	// entity resolves to its default state by definition, so a provenance
	// block would be noise on every row of every existing view.
	World *EntityWorld `json:"_world,omitempty"`
	// Self addresses the ROW this collection entity is, face included — the
	// same contract as [Entity.Self]. Under a world a collection entity is a
	// neighbour's RESOLVED face, so a client that edits it in place (a row's
	// inline edit, its Edit button) must write to this address and not to
	// the bare id, or it edits a state the page is not showing.
	Self string `json:"_self,omitempty"`
}

// ViewColumn represents a column definition.
type ViewColumn struct {
	Property string `json:"property,omitempty"`
	Label    string `json:"label,omitempty"`
	Relation string `json:"relation,omitempty"`
	Link     string `json:"link,omitempty"`
}

// ViewRow represents a table row.
type ViewRow struct {
	EntityID   string     `json:"entityId"`
	EntityType string     `json:"entityType"`
	EditFormID string     `json:"editFormId,omitempty"`
	Cells      []ViewCell `json:"cells"`
	Content    string     `json:"content,omitempty"`
	// Self addresses the ROW this entry describes, face included — the same
	// contract as [Entity.Self]. See [ViewEntity.Self].
	Self string `json:"_self,omitempty"`
}

// ViewTreeNode is one row of a `display: nested` section: an entity, its
// configured cells, and the rows nested under it.
//
// Always exactly two levels deep in practice — a nested section renders
// parent → child — but the type is recursive so the wire shape does not have
// to change if a later ticket lifts that.
type ViewTreeNode struct {
	Entity ViewEntity `json:"entity"`
	// Columns are the columns declared for this node's LEVEL and entity type
	// (`parent_columns` / `child_columns`, both keyed by type).
	//
	// Per node rather than per section because a nested section has no single
	// column list: the two levels differ, and each may reach several entity
	// types. Absent when the node's type declares none.
	Columns []ViewColumn `json:"columns,omitempty"`
	// Cells are this node's values, positional over Columns above.
	Cells []ViewCell `json:"cells,omitempty"`
	// Children are the nodes nested under this one, omitted when there are
	// none — which is why HasMoreChildren below has to exist.
	Children []ViewTreeNode `json:"children,omitempty"`
	// ChildCount is how many children this node HAS, which exceeds
	// len(Children) once the node budget truncates. Counts only children the
	// caller may read.
	ChildCount int `json:"childCount,omitempty"`
	// HasMoreChildren reports children this response does not carry. Without
	// it a truncated node is byte-identical to a childless one, since Children
	// is omitempty — so a consumer could not tell a complete subtree from a
	// withheld one. Computed on the gated tree: it never reflects entities the
	// caller cannot read.
	HasMoreChildren bool `json:"hasMoreChildren,omitempty"`
}

// ViewCell represents a table cell.
type ViewCell struct {
	Values     []string `json:"values"`
	PropType   string   `json:"propType,omitempty"`
	Widget     string   `json:"widget,omitempty"`
	Link       string   `json:"link,omitempty"`
	EntityID   string   `json:"entityId,omitempty"`
	EntityType string   `json:"entityType,omitempty"`
}

// ViewGroup represents a group of rows.
type ViewGroup struct {
	GroupName string       `json:"groupName"`
	Rows      []ViewRow    `json:"rows,omitempty"`
	Entities  []ViewEntity `json:"entities,omitempty"`
}

// ViewAddInfo describes an add button configuration. Despite the "View"
// prefix this is now used only by SidePanelSection — see TKT-6ETQ for
// the rename to V1SidePanelAddInfo. Do not reach for this type from a new
// view-related response: the read-only-view invariant established by
// TKT-651W means no view section should carry add affordances.
//
// That invariant was narrowed, not lifted, by TKT-R4BMJM: a view section may
// carry a create affordance when its config explicitly opts in, and that rides
// [ViewSectionCreate] — a separate type. This one stays side-panel-only. The
// distinction is not pedantry: this type's targets are not ACL-gated in the
// same way, it carries a link-existing sibling the view path must not have, and
// RR-R8X6 predicted that the misleading "View" prefix would tempt exactly the
// reuse this sentence forbids.
type ViewAddInfo struct {
	Relation string          `json:"relation"`
	LinkAs   string          `json:"linkAs"`
	PeerID   string          `json:"peerId"`
	Targets  []ViewAddTarget `json:"targets"`
}

// ViewAddTarget represents a possible target for add action.
// Side-panel-only post TKT-651W; see TKT-6ETQ for the rename plan.
type ViewAddTarget struct {
	EntityType string `json:"entityType"`
	FormID     string `json:"formId"`
	Label      string `json:"label"`
}

// ViewLinkInfo describes a link existing button configuration.
// Side-panel-only post TKT-651W; see TKT-6ETQ for the rename plan.
type ViewLinkInfo struct {
	Relation    string   `json:"relation"`
	LinkAs      string   `json:"linkAs"`
	PeerID      string   `json:"peerId"`
	EntityTypes []string `json:"entityTypes"`
}

// PositionRef identifies a neighboring entity in a scope. Type is included
// because a scope (notably a search scope) can span entity types, so the SPA
// must build the target's detail route from *its* type, not the current
// entity's. ID alone would break cross-type prev/next.
type PositionRef struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// Position is the scope-navigator payload: the neighbors plus the counter
// the SPA needs, with no entity bodies shipped. current is 1-based; prev/next
// are nil at the ends of the set.
type Position struct {
	Prev    *PositionRef `json:"prev"`
	Next    *PositionRef `json:"next"`
	Current int          `json:"current"`
	Total   int          `json:"total"`
}

// ActionResponse mirrors script.ActionResponse for API JSON output.
// Has both successful response fields and error fields with correlation ID.
type ActionResponse struct {
	Redirect      string `json:"redirect,omitempty"`
	Message       string `json:"message,omitempty"`
	MessageType   string `json:"message_type,omitempty"`
	Error         string `json:"error,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`
}

// Mention is the resolved target of an entity-ID code span found in
// markdown content. Mirrors the Lua-side `rela.md.entity_refs`/`resolve_refs`
// semantics (TKT-LXYHQ): only bare-content code spans whose entire text is
// an entity ID are collected; the data-entry SPA uses this map to rewrite
// those code spans into titled in-app links.
//
// `Inaccessible` is true when the entity's display title is unreadable
// (e.g. the file is git-crypt encrypted) — the SPA renders such links
// with a lock affordance using the same tooltip copy as inaccessible
// properties. `InaccessibleReason` carries the matching
// `entity.InaccessibleReason` value as a string so the wire shape stays
// stable across reason-enum additions.
type Mention struct {
	Type               string `json:"type"`
	Title              string `json:"title"`
	Inaccessible       bool   `json:"inaccessible,omitempty"`
	InaccessibleReason string `json:"inaccessible_reason,omitempty"`
}

// Face is one content state an entity has, other than the one being served.
// See [Entity.Faces] for why it carries no readability flag.
type Face struct {
	// Face is the face's name, which is also the coordinate its row is
	// stored at (BUG-HC6I2T) — so what a client reads here is what it sends
	// back when addressing the row. Empty only for a type declaring no
	// faces, whose single state has no name.
	Face string `json:"face"`
	// Label is the display text for this face: the operator's
	// `faces.<name>.label:` when declared, else the declared face name
	// itself. Empty only when the coordinate has no declared name at all.
	//
	// Display-only, like [CopyOffer.Label] — a client addresses the face by
	// Face and never by this string.
	Label string `json:"label,omitempty"`
	// Ref is the face's ADDRESS: the path segment that reads this row
	// literally under any world — `POL-1@published`.
	//
	// It exists so a client can offer "view the published face" as a plain
	// link, without deriving which declared world happens to lead with that
	// face — a derivation that needs the chain, the per-type overrides and a
	// tie-break rule, and that produced a dead control for any face no world
	// headed. A type declaring no faces has one nameless state and falls back
	// to the bare id, which is literal only in the default world.
	Ref string `json:"ref,omitempty"`
}

// CopyOffer is one declared copy definition offered for a source face
// (TKT-WRLDAPI item 5), as the SPA needs it to render RULING 9's affordances.
type CopyOffer struct {
	// Name is the `copies:` key and the ONLY thing a client may send back to
	// invoke it. A request names a definition; it never supplies one.
	Name string `json:"name"`
	// Label is the operator-configured display text, falling back to Name.
	// Display-only.
	Label string `json:"label"`
	// TargetFace is the declared target (`policy@published`), for a UI that
	// wants to say what the action will produce. Always another face of the
	// SAME entity: cross-entity definitions are not offered (see
	// entitymanager.CopiesForSource), so an invoke needs no target id.
	TargetFace string `json:"targetFace"`
	// Allowed reports whether the requesting principal may invoke this copy on
	// this source right now.
	//
	// A HINT, never a boundary — the same contract as `_actions`. The invoke
	// endpoint re-authorizes through the kernel, so a client that ignores this
	// and POSTs anyway receives the same 403 it would have received regardless.
	// It is computed by running the kernel's own authorization path, not a
	// parallel implementation, so it cannot drift from what the write does.
	Allowed bool `json:"allowed"`
	// Reason names why Allowed is false, for a tooltip. Empty when allowed.
	// Advisory, and never carries content from an entity the caller cannot
	// read.
	Reason string `json:"reason,omitempty"`
	// OnSuccess is the operator's follow-through for this copy: the toast
	// and where the client lands (TKT-5SZG2L). Nil when nothing is declared,
	// in which case the client shows the copy's label and lands on the face
	// written. Mirrors metamodel.CopyOnSuccess.
	OnSuccess *CopyOnSuccess `json:"onSuccess,omitempty"`
}

// CopyOnSuccess is the operator's follow-through for a successful copy.
type CopyOnSuccess struct {
	// Message is the confirmation toast; empty means the copy's label.
	Message string `json:"message,omitempty"`
	// Landing is where the client goes afterwards.
	Landing CopyLanding `json:"landing"`
}

// CopyLanding is a copy's landing target, mirroring metamodel.CopyLanding
// with the default spelled out so a client never has to infer it.
type CopyLanding struct {
	// Mode is "written" (the face the copy wrote), "stay" (reload in
	// place), "world" or "face" (see the field of that name).
	Mode  string `json:"mode"`
	World string `json:"world,omitempty"`
	Face  string `json:"face,omitempty"`
}

// CopyResult reports what an invoked copy produced.
type CopyResult struct {
	// Definition is the name that was invoked.
	Definition string `json:"definition"`
	// EntityID is the entity whose face was written.
	EntityID string `json:"entityId"`
	// Face is the coordinate of the face that was written. Empty means the
	// default state.
	Face string `json:"face"`
	// Created is true when the copy brought the target face into existence
	// rather than overwriting one — the difference between "published for the
	// first time" and "re-published".
	Created bool `json:"created"`
}

// GanttResponse is the payload of GET /api/v1/_gantts/{id}: the gated,
// folded containment forest. Truncated is true when the node cap cut the
// emission short; it is computed on the ACL-filtered tree only, so it can
// never disclose that hidden nodes exist.
type GanttResponse struct {
	Roots     []GanttNode `json:"roots"`
	Truncated bool        `json:"truncated,omitempty"`
}

// GanttNode is one entity in the gantt tree. Planned (the entity's own
// declared window) and Rolled (the envelope of its descendants) are separate
// on purpose: merged, a child escaping its parent's window — the breach the
// view exists to show — would be silently absorbed. Nodes carry no relation
// properties: edge meta has no redaction on this path.
type GanttNode struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title,omitempty"`
	Color string `json:"color,omitempty"`
	// Planned is the entity's own start/end, absent when it declares none.
	Planned *GanttSpan `json:"planned,omitempty"`
	// Rolled is the descendants' envelope, absent for a childless node.
	Rolled *GanttSpan `json:"rolled,omitempty"`
	// Committed is an externally promised deadline (date string), or "".
	Committed string      `json:"committed,omitempty"`
	Breach    GanttBreach `json:"breach,omitzero"`
	// InCycle marks a node a containment loop closes back onto, under
	// on_cycle:"mark". The node renders in place with its first parent; the
	// edge that returned to it is cut, and HasMoreChildren reports that
	// withholding. Set only under that policy — "error" never returns a body
	// and "prune" omits the component entirely. Computed on the gated tree,
	// so it never signals a loop running through entities the principal
	// cannot see.
	InCycle  bool        `json:"in_cycle,omitempty"`
	Children []GanttNode `json:"children,omitempty"`
	// HasMoreChildren is true when this node has children the response does
	// not carry (the depth cap or node budget withheld them). Without it a
	// depth-capped node is byte-identical to a genuine leaf — Children is
	// omitempty — so a consumer could not tell a complete subtree from a
	// withheld one. Computed on the gated tree: it never reflects hidden
	// entities. Distinct from Truncated, which is the budget-cut signal for
	// the response as a whole.
	HasMoreChildren bool `json:"has_more_children,omitempty"`
	// Props carries the values of the gantt's configured tooltip properties,
	// keyed by property name. Values are read from the REDACTED entity, so a
	// `visible:`-hidden property is simply absent — never blank-but-present.
	Props map[string]string `json:"props,omitempty"`
}

// GanttSpan is a date interval; either bound may be empty.
type GanttSpan struct {
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
}

// GanttBreach flags a rolled span escaping the planned window, per direction.
type GanttBreach struct {
	Before bool `json:"before,omitempty"`
	After  bool `json:"after,omitempty"`
}

// IsZero lets omitzero keep an empty breach off the wire.
func (b GanttBreach) IsZero() bool { return !b.Before && !b.After }
