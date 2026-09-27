package metamodel

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// Metamodel represents the full metamodel configuration
//
// TODO(TKT-N0IKN9): 30 exported methods, over the 20 exported-method line.
// This is the schema accessor — wide read-API by nature — but a ratchet
// candidate: group the type/relation/property lookups behind focused accessors
// (the attachment-scan accessors moved behind [AttachmentPolicy] this way).
//
// 32nd exported method is ShapeProjection (TKT-0C57FS), the data-shape
// sibling of RenderProjection.
//
//plimsoll:max-exported-methods=32
type Metamodel struct {
	Version   string `yaml:"version"`
	Namespace string `yaml:"namespace"`
	// Description is optional end-user prose describing what this deployment /
	// application is for. Display-only, surfaced by the generated documentation
	// (the `rela docs` generator — FEAT-G4VO53). Empty by default; the metamodel
	// does not otherwise consult it.
	Description string                 `yaml:"description,omitempty"`
	Includes    []string               `yaml:"includes,omitempty"`
	Types       map[string]CustomType  `yaml:"types"`
	Entities    map[string]EntityDef   `yaml:"entities"`
	Relations   map[string]RelationDef `yaml:"relations"`
	Validations []ValidationRule       `yaml:"validations,omitempty"`
	Automations []AutomationDef        `yaml:"automations,omitempty"`

	// Attachments holds the global attachment safety floor (MIME allowlist,
	// scan policy) applied to every `file` property unless overridden.
	Attachments *AttachmentsConfig `yaml:"attachments,omitempty"`

	// Comments holds the commentary-layer policy: whether commenting is
	// enabled and which entity types accept it. Policy only — comments are
	// not graph entities, so nothing here declares a type. See
	// internal/comments.
	Comments *CommentsConfig `yaml:"comments,omitempty"`

	// Transforms is the view-export registry: named markdown -> format
	// conversions run via an external command (e.g. pandoc). Registering a
	// transform here makes it available to every markdown-producing surface
	// (entity view, list view, Lua document) as an "Export as ..." format. Lives
	// in the metamodel (not data-entry config) so the CLI can reach it too. See
	// internal/transform.
	Transforms map[string]TransformDef `yaml:"transforms,omitempty"`

	// Worlds declares named resolution functions over content states
	// (TKT-WAV8XP, design doc §4). Keyed by world name.
	//
	// ABSENT means the project has exactly the implicit DEFAULT world —
	// every entity contributes its default state — which is today's
	// graph, byte-identically. The name "default" is reserved
	// ([DefaultWorldName]) because that world is implicit and total, so
	// a declaration under that name could only shadow or contradict it.
	Worlds map[string]WorldDef `yaml:"worlds,omitempty"`

	// Copies declares named copy definitions — mapped writes of one content
	// state into another (TKT-C1XUA8). See [CopyDef]; a request invokes one
	// BY NAME and can never submit its own mapping.
	Copies map[string]CopyDef `yaml:"copies,omitempty"`

	// Computed lookups (not from YAML)
	aliasMap      map[string]string // alias -> canonical name
	inverseOwners map[string]string // inverse name -> owning canonical relation name
}

// InverseOwner returns the canonical relation type that declares the
// given inverse name, if any. Populated at load time by the metamodel
// loader after rejecting collisions and canonical-name shadowing. The
// data-entry unified-PATCH wire format uses this to resolve inverse
// body keys without scanning the relation map on every request.
//
// For a relation declared `symmetric: true` with `inverse: <self>`,
// the inverse maps back to the same canonical relation — callers that
// care about direction should consult `RelationDef.Symmetric` and
// treat the path entity as source regardless of the body key.
func (m *Metamodel) InverseOwner(inverseName string) (string, bool) {
	owner, ok := m.inverseOwners[inverseName]
	return owner, ok
}

// ValidationRule defines a custom validation rule for entities
type ValidationRule struct {
	// Name is a unique identifier for the validation rule
	Name string `yaml:"name"`

	// Description explains what this validation checks
	Description string `yaml:"description"`

	// EntityType limits the validation to a specific entity type (optional)
	// If empty, the validation applies to all entity types
	EntityType string `yaml:"entity_type,omitempty"`

	// Faces limits the rule to specific content states, named as declared in
	// `faces:`. Empty means EVERY state, including the bare one.
	//
	// # Why the default is every state
	//
	// A validation rule is a correctness claim, and the safe direction for a
	// correctness claim is to check more rather than less: a rule that silently
	// skipped a state would let `rela validate` report a clean run over data it
	// never looked at, which is worse than no check because it is a claim
	// (TKT-4Y6CMV).
	//
	// The cost of that default is noise rather than danger — a rule may fire on
	// a state the operator did not have in mind — and this key is the remedy.
	//
	// # When to set it
	//
	// When the rule is genuinely about one state. "A published policy needs an
	// owner" is a claim about the published face; running it against a draft
	// reports every work-in-progress as a violation, and a validator that cries
	// wolf trains people to ignore it.
	//
	// Naming a face the type does not declare is a load error, not a silent
	// no-match: a rule scoped to nothing checks nothing, and would pass forever
	// while appearing to guard something.
	Faces []string `yaml:"faces,omitempty"`

	// When specifies filter conditions that select which entities this rule applies to
	// Uses the same syntax as --where filters (e.g., "status=approved")
	// Multiple conditions are ANDed together
	// If empty, the rule applies to all entities (of the specified type)
	When []string `yaml:"when,omitempty"`

	// Then specifies filter conditions that matching entities must satisfy
	// Uses the same syntax as --where filters (e.g., "owner!=")
	// Multiple conditions are ANDed together
	Then []string `yaml:"then,omitempty"`

	// WhenCondition is a predicate EXPRESSION selecting which entities the
	// rule applies to, ANDed with every When clause. Expression syntax
	// (unlike When's filter syntax) gets boolean composition and the
	// host-function stdlib, including date arithmetic.
	WhenCondition string `yaml:"when_condition,omitempty"`

	// ThenCondition is a predicate EXPRESSION that matching entities must
	// satisfy, ANDed with every Then clause.
	//
	// A rule may use any mix: `when:` + `then_condition:` is a filter
	// selecting entities that an expression then asserts over.
	ThenCondition string `yaml:"then_condition,omitempty"`

	// Content specifies validation rules for markdown body content
	Content *ContentRule `yaml:"content,omitempty"`

	// Relations specifies relation-cardinality constraints, keyed by
	// relation type. Evaluated only for entities that match `when`. Used
	// for workflow gates like "a done ticket must have at least one
	// has-review relation to a done review-checklist".
	//
	// See [RelationConstraint] for what each one asserts.
	Relations map[string]RelationConstraint `yaml:"relations,omitempty"`

	// Severity is the severity level of violations: "error" or "warning"
	// Defaults to "warning" if not specified
	Severity string `yaml:"severity,omitempty"`

	// Lua specifies inline Lua code for custom validation logic.
	// The code should return true if the entity is valid, or false/nil for a violation.
	// The entity being validated is available as the `entity` global variable.
	// Read-only workspace access is available via rela.get_entity(), rela.list_entities(), etc.
	Lua string `yaml:"lua,omitempty"`

	// LuaFile specifies a path to a Lua script file in the scripts/ directory.
	// The script should return true if valid, or false/nil for a violation.
	// Example: "validate-dates.lua" loads scripts/validate-dates.lua
	LuaFile string `yaml:"lua_file,omitempty"`

	// LuaArgs specifies arguments to pass to Lua validation scripts.
	// Available as rela.args in the Lua runtime.
	LuaArgs []string `yaml:"lua_args,omitempty"`
}

// GetSeverity returns the severity level, defaulting to "warning"
func (v *ValidationRule) GetSeverity() string {
	if v.Severity == "" {
		return "warning"
	}
	return v.Severity
}

// IsError returns true if this validation has error severity
func (v *ValidationRule) IsError() bool {
	return v.GetSeverity() == "error"
}

// TypeValidation defines a regex validation for a custom type.
type TypeValidation struct {
	Pattern string `yaml:"pattern"` // Regex pattern that values must match
	Error   string `yaml:"error"`   // User-friendly error message if pattern doesn't match

	// compiled is the pre-compiled regex, populated during metamodel load.
	// Not exported to prevent YAML serialization issues.
	compiled *regexp.Regexp
}

// Compiled returns the pre-compiled regex pattern.
// Returns nil if the pattern hasn't been compiled yet.
func (tv *TypeValidation) Compiled() *regexp.Regexp {
	return tv.compiled
}

// SetCompiled sets the pre-compiled regex pattern.
func (tv *TypeValidation) SetCompiled(re *regexp.Regexp) {
	tv.compiled = re
}

// CustomType defines a reusable type with optional enum values and/or regex validations.
type CustomType struct {
	Values []string          `yaml:"values,omitempty"` // Allowed values (makes this an enum type)
	Labels map[string]string `yaml:"labels,omitempty"` // Optional display labels keyed by value (display-only; value stays the identity)
	// Descriptions is optional per-value prose keyed by value: what each value
	// MEANS, as opposed to Labels (the short display text). Display-only,
	// surfaced by the generated documentation (FEAT-G4VO53) so a reader
	// understands e.g. what "blocked" or "in-review" signifies. A value with no
	// entry simply has no description. Distinct from the type-level Description
	// scalar below (which documents the type as a whole).
	Descriptions map[string]string `yaml:"descriptions,omitempty"`
	Default      string            `yaml:"default,omitempty"`     // Default value
	Description  string            `yaml:"description,omitempty"` // Documentation for the type
	Validations  []TypeValidation  `yaml:"validations,omitempty"` // Regex validations with error messages

	// Transitions declares the legal value→value moves for this enum,
	// making it a state machine (TKT-E4LW2). This is declarative source
	// data only — the metamodel does not enforce it. At startup
	// internal/statemachine.Compile reads these into an executable machine
	// that the entitymanager runs on the write path (legality 422, guard
	// 403, precondition 422). Empty means "any value may change to any
	// other" (the historical, unconstrained behavior). Only meaningful on a
	// named type — inline `type: enum` properties carry no transitions.
	Transitions []TransitionDef `yaml:"transitions,omitempty"`

	// Initial names the only legal entry value on entity create when this
	// type is a state machine. Empty falls back to Default. Consumed by
	// internal/statemachine at compile time.
	Initial string `yaml:"initial,omitempty"`
}

// TransitionDef is one edge in an enum state machine: a legal move from one
// value to another, optionally gated by an ACL permission (Guard) and/or a
// data precondition (When). This is declarative source data; the executable
// machine is built from it by internal/statemachine.Compile.
type TransitionDef struct {
	From string `yaml:"from"` // Source value; must be one of CustomType.Values
	To   string `yaml:"to"`   // Target value; must be one of CustomType.Values

	// Label is optional display text for the MOVE (the action), not the
	// destination state — e.g. "Start progress" for todo→doing rather than the
	// state noun "Doing". Purely presentational: a machine-aware status control
	// lists transitions as verbs. Empty falls back to the target value's display
	// label (CustomType.Labels[To]) and then the raw To value. Display-only; the
	// executable machine ignores it for enforcement.
	Label string `yaml:"label,omitempty"`

	// Help is optional longer prose explaining WHY or WHEN a user would make
	// this move, beyond the short verb Label — e.g. "Send for review once the
	// implementation is complete and tests pass." Display-only, surfaced by the
	// generated documentation (FEAT-G4VO53); the executable machine ignores it.
	Help string `yaml:"help,omitempty"`

	// Guard names an ACL permission the acting principal must hold for this
	// transition. Enforced only on served paths (a principal exists); inert
	// on direct CLI writes. Empty means the transition is legal for anyone
	// who may otherwise write the entity.
	Guard string `yaml:"guard,omitempty"`

	// When is an internal/predicate expression evaluated as a precondition
	// against the entity + graph at write time. False rejects the transition
	// (422). Empty means no precondition.
	When string `yaml:"when,omitempty"`
}

// EntityDef defines an entity type in the metamodel
//
// TODO(TKT-N0IKN9): 24 exported methods, over the 20 exported-method line.
// Schema value type; ratchet candidate alongside Metamodel. DisplayProperties
// (TKT-NJTBQX) is the 24th — it reports the property set backing the display
// title so the ACL locked-title guard can gate on templated display_property
// (see internal/dataentry mentions); ratchet back down when this type is
// decomposed.
//
//plimsoll:max-exported-methods=24
type EntityDef struct {
	Label         string                 `yaml:"label"`
	LabelPlural   string                 `yaml:"label_plural,omitempty"`
	Description   string                 `yaml:"description,omitempty"` // Documentation explaining intent/usage
	Plural        string                 `yaml:"plural,omitempty"`      // Used for directory names (e.g., "policies" for "policy")
	Aliases       []string               `yaml:"aliases,omitempty"`
	IDType        string                 `yaml:"id_type,omitempty"`     // "short" (default), "sequential", or "manual"
	IDCaps        string                 `yaml:"id_caps,omitempty"`     // "upper" (default) or "lower" - capitalization for short ID suffix
	IDPrefix      string                 `yaml:"id_prefix,omitempty"`   // Single ID prefix (sugar for single-element id_prefixes)
	IDPrefixes    []string               `yaml:"id_prefixes,omitempty"` // Multiple ID prefixes
	RDFType       string                 `yaml:"rdf_type,omitempty"`
	Properties    map[string]PropertyDef `yaml:"properties"`
	PropertyOrder []string               `yaml:"-"`                      // Order of properties as defined in YAML (computed at load)
	DefaultSort   []SortSpec             `yaml:"default_sort,omitempty"` // Default sort order for this entity type
	Color         string                 `yaml:"color,omitempty"`
	BorderColor   string                 `yaml:"border_color,omitempty"`
	// DisplayProperty names the property whose value renders as the
	// human-readable display name in lists, cards, link text, etc.
	// When empty, GetPrimaryProperty() falls back to the autoderivation
	// (priority list title/name/label, then alphabetical fallback).
	// Validated at metamodel-load time: must reference a defined
	// property and must not have leading/trailing whitespace.
	DisplayProperty string `yaml:"display_property,omitempty"`

	// Faces declares this type's content states (TKT-WAV8XP, design
	// doc §4.1). The map key IS the face coordinate ("draft",
	// "published") — declared name and stored coordinate are the same
	// string, with no face privileged by where it is stored.
	//
	// No face is addressed by the bare id. A type declaring faces has
	// no bare row to write, so every write to it names a face
	// (BUG-HC6I2T); the zero coordinate survives only as the identity
	// row a faceless type stores its single state in.
	//
	// ABSENT (the common case) means the type has no content states: it
	// contributes its single default state to EVERY world, needing no
	// per-type resolution. That is resolution rule 1, and it is why a
	// mixed graph (tickets without faces beside pages with them)
	// needs no special handling — and why a project that never writes
	// this key behaves byte-identically to the pre-worlds system.
	Faces map[string]FaceDef `yaml:"faces,omitempty"`

	// QueryScopes declares named, reusable membership predicates over this
	// type — the rule deciding whether a row belongs in a collection at all.
	// Values are [internal/predicate] expressions over `entity` (and
	// optionally `current_user`); a data-entry list or kanban references one
	// by name with `query_scope:`.
	//
	// The names `default` and `all` are special. `default` applies to a
	// presentation surface that names no scope; `all` is implicit, always
	// resolves to "no predicate", and is how a view withdraws the default.
	// Declaring `all` is a load error — it could only shadow the withdrawal.
	//
	// # Why this is not `worlds:`
	//
	// A world RANKS the faces of one entity and picks at most one, so it
	// never changes the row count. A query scope INCLUDES or EXCLUDES the
	// entity, so it does. "Archived" is the second kind: an archived task is
	// not a variant of a live task you would rank against it, which is why
	// it cannot be spelled as a face.
	//
	// # Why the expressions are not compiled here
	//
	// Only the raw source is held. Compiling needs [internal/predicatefns],
	// which already imports this package, and arch-lint keeps metamodel a
	// near-leaf regardless. So `internal/scopes` compiles them at assembly —
	// the same split, for the same reason, as `worlds:` and internal/worlds.
	//
	// ABSENT (the common case) means no scoping: every surface sees every
	// row, exactly as before this key existed.
	QueryScopes map[string]string `yaml:"query_scopes,omitempty"`

	// Pacts declares, per external system, which fields of this type that
	// system owns — the contract its Twins are synced under (see [PactDef]).
	// The map key is the system id.
	//
	// ABSENT (the common case) means the type has no twins, and the project
	// behaves byte-identically to one without this key.
	Pacts map[string]PactDef `yaml:"pacts,omitempty"`
}

// FaceDef declares one content state of an entity type.
//
// Near-empty by design: a state is IDENTIFIED by its coordinate and nothing
// else. The only knob beyond identity is display text, and the §4.5 "no
// template language" discipline holds — no interpolation, no per-state ACL
// hints, no retention policy added speculatively.
type FaceDef struct {

	// Label is operator-authored display text for this face — "Nederlands"
	// for `nl`, "Published" for `published`. Empty falls back to the
	// coordinate name, which is itself operator-authored and therefore an
	// honest (if terse) display string.
	//
	// Display-only, exactly as [CopyDef.Label] and [TransitionDef.Label]
	// are: a plain string with NO interpolation, never consulted for
	// resolution, equality or addressing. The coordinate stays the identity
	// — renaming a label cannot move an entity's stored face.
	//
	// It exists because three surfaces were showing raw coordinates while
	// the copy menu beside them showed operator prose: the face switcher
	// ("View fr"), the per-neighbor world badge, and the return-to-default
	// button, which hardcoded the ISMS word "draft" on types that have no
	// draft. All three wanted the same missing thing — a name for a face.
	//
	// Deliberately PER-FACE rather than per-axis. Axes are not an object
	// in this code yet (a type declares one flat `faces:` map), and a
	// per-face label is forward-compatible with an axis that later groups
	// them, where a `return_action_label:` on a face would not be.
	Label string `yaml:"label,omitempty"`

	// Messages is the operator's wording for the web app's chrome about THIS
	// face. Every entry is optional and an absent one renders NOTHING: rela
	// has no fallback sentence, because any it wrote would be its storage
	// vocabulary ("face", "bare", "default") shown to a reader who never
	// chose those words (TKT-5SZG2L).
	Messages FaceMessages `yaml:"messages,omitempty"`
}

// FaceMessages is operator-authored chrome text about one face. See
// [WorldMessages] for the placeholder grammar; every entry is plain text.
type FaceMessages struct {
	// ReadOnly is shown on a detail page or edit form that reached this face
	// while the reader may not write it — "Dit is de vastgestelde versie.
	// Bewerken doe je in het concept." Empty shows nothing: a face the
	// reader may not write then looks like any other permission denial, a
	// page without an Edit button. Substitutes every placeholder: `{face}`
	// is this face's label, `{title}` the entity's display title.
	ReadOnly string `yaml:"read_only,omitempty"`

	// Notice is shown on a detail page that reached this face, whatever the
	// reader may do with it — "Let op: dit is een concept en nog niet
	// vastgesteld." Empty shows nothing, like every entry here.
	//
	// The distinction from [FaceMessages.ReadOnly] is WHO the sentence is
	// about, and it is why a second key exists rather than a relaxed guard
	// on the first. ReadOnly is about the READER: you may not write this,
	// so it renders only when they may not. Notice is about the DOCUMENT:
	// this text is not in force, which is true of a draft whoever is
	// looking — including the editor who may freely rewrite it. That page
	// is the one carrying risk (a reader acting on an unadopted policy
	// follows something nobody agreed) and it was the unmarked one, because
	// a face writable by definition can never satisfy ReadOnly's guard.
	//
	// The two are independent and may both be declared on one face; the
	// detail page renders Notice first, the document's status before the
	// qualifier on the reader's permission. Substitutes every placeholder,
	// as ReadOnly does — a detail page knows every fact.
	Notice string `yaml:"notice,omitempty"`
}

// Otherwise is a world's policy for an entity whose type declares
// faces but none that the world selects (resolution rule 3).
//
// MANDATORY on every declared world, validated at load. A silent
// fallback here is precisely the leak content states exist to prevent —
// a `published` world quietly showing a draft face — so there is
// deliberately NO zero value that means "pick something sensible": the
// empty string is invalid and rejected.
type Otherwise string

const (
	// OtherwiseUnset is the invalid zero value; see [Otherwise].
	OtherwiseUnset Otherwise = ""
	// OtherwiseExclude contributes nothing. Public worlds say this.
	OtherwiseExclude Otherwise = "exclude"
	// OtherwiseDefault falls back to the type's default state. Internal
	// worlds may say this.
	OtherwiseDefault Otherwise = "default"
)

// IsValid reports whether the value is one of the two declared policies.
// The zero value is NOT valid — that is the point (see [Otherwise]).
func (o Otherwise) IsValid() bool {
	return o == OtherwiseExclude || o == OtherwiseDefault
}

// WorldDef declares one world: a resolution function mapping each entity
// to AT MOST ONE of its content states, the "prime" (design doc §4.1).
//
// The DEFAULT world is distinguished and needs no declaration — every
// entity contributes its default state, it is total by construction, and
// a metamodel with no `worlds:` block has exactly this world. All
// backward compatibility hangs on that.
type WorldDef struct {
	// Select is the ordered candidate chain; the first coordinate that
	// EXISTS for an entity is its prime. A bare string is accepted as a
	// one-element chain (see UnmarshalYAML).
	//
	// Order is the whole point: `[review, published]` means "the site as
	// it will look once pending reviews land". Chains keep resolution a
	// FUNCTION — they are how the single answer is computed, never a way
	// for one world to contain two faces of an entity.
	Select []string `yaml:"select,omitempty"`

	// Overrides replaces Select for named entity types. Keyed by entity
	// type; the value is that type's chain, same one-or-many spelling.
	Overrides map[string][]string `yaml:"overrides,omitempty"`

	// Otherwise is the mandatory rule-3 policy; see [Otherwise].
	Otherwise Otherwise `yaml:"otherwise,omitempty"`

	// Edits names the state that edits made from this world land in
	// (design doc §9.3, copy-on-write). PARSED BUT UNUSED in Step 2 —
	// the copy kernel is Step 4. Declared here so a project's schema
	// does not need rewriting when Step 4 lands, and validated as a
	// declared face so a typo surfaces now rather than then.
	Edits string `yaml:"edits,omitempty"`

	// Create names the face a create issued from this world lands in.
	//
	// A create names no face of its own: the form is generic and the same
	// form is reachable from several places, so which state a new entity
	// starts in is a property of the workflow that opened it. A faced type
	// has no default row to fall back to (BUG-HC6I2T), so without this key
	// a create from a world-bound list has no target at all and the server
	// refuses it with `face_required`.
	//
	// It is a SINGLE declared face, never a chain. `select:` may answer with
	// a fallback, which is why a write never rides a world (see
	// dataentry.attachWorld); this names one row directly, so that objection
	// does not apply. Deriving the face from `select[0]` would be actively
	// wrong: an ISMS world heading its ADOPTED face would then publish by
	// the act of creating, which is the bug this key exists to avoid.
	//
	// Empty means a create from this world names no face, which a faced type
	// refuses. Validated at load against the declared faces.
	Create string `yaml:"create,omitempty"`

	// PrimaryFor declares the faces this world is the canonical home of,
	// breaking a tie when SEVERAL worlds head the same face for a type
	// (TKT-MFVH03).
	//
	// # Why this is needed at all
	//
	// "Which world serves this face?" is the question a face-switcher asks:
	// the read grammar is `?world=`, and a face is not a world, so an
	// affordance offering "go to the Dutch version" must name a world. The
	// answer is INFERRED from the compiled chains — the world whose chain
	// HEADS that face — and for every schema written so far the inference is
	// unambiguous, so this key stays unset.
	//
	// It is needed only when two worlds head the same face for one type. That
	// is a genuine tie: both serve it as their first choice, and nothing in
	// the chains distinguishes them. Before this key the answer was whichever
	// world the metamodel map yielded first — insertion order, a property of
	// how the config happened to serialize rather than of what the operator
	// meant, and it could change when an UNRELATED world was added or renamed.
	//
	// # Why on the world, not on the face
	//
	// `overrides:` makes the answer per (type, face): one world can head `en`
	// for `guide` while another heads it for `policy`. A key on the face in
	// EntityDef.Faces is type-level and cannot express that. Worlds already
	// own the resolution vocabulary (select/overrides/otherwise/edits), so the
	// tie-break belongs beside them.
	//
	// # It may only CONFIRM, never contradict
	//
	// Naming a face this world does not head is a load error, not a silent
	// override (see validateWorlds). A declaration that disagreed with the
	// compiled chain would produce a control navigating to a world where the
	// face is not primary — an affordance that lies, which is the shape this
	// codebase refuses everywhere else.
	PrimaryFor []string `yaml:"primary_for,omitempty"`

	// Banner is operator-authored text announcing this world to a reader —
	// "DRAFT — not in force" on an editorial world, nothing on a language
	// world. Empty (the zero value) means announce nothing.
	//
	// Display-only, like [Transition.Label] and [CopyDef.Label]: no
	// interpolation, no conditionals. The world is already resolved by the
	// time this renders, so there is nothing to compute.
	//
	// It gates ONLY the announcement. A non-default world is read-only on
	// this API regardless (a write names its face by id and never a world —
	// see dataentry.attachWorld), so the read-only note and the way back to
	// the default face are properties of the request, not of the operator's
	// configuration, and are not suppressible here. Hiding them would leave
	// a reader on a page that silently refuses writes with no explanation
	// and no exit.
	//
	// Why per-world rather than per-face: a banner describes the VIEW a
	// reader is in, and a world is that view (one consistent resolution
	// across the whole graph). The same face can be reached from worlds
	// that warrant different text, or none.
	Banner string `yaml:"banner,omitempty"`

	// Messages is the operator's wording for what this world changes on a
	// screen. Every entry is optional and an absent one renders NOTHING — the
	// web app has no sentence of its own for any of these, because every one
	// it used to have was rela's storage vocabulary shown to a reader
	// (TKT-5SZG2L).
	Messages WorldMessages `yaml:"messages,omitempty"`

	// OnAbsent decides what happens when a reader opens an entity that has
	// no face in this world. Absent: the page shows whatever the world's
	// `otherwise:` resolves to, with [WorldMessages.Absent] if declared.
	// See [WorldOnAbsent].
	OnAbsent WorldOnAbsent `yaml:"on_absent,omitempty"`
}

// WorldMessages is operator-authored chrome text about one world.
//
// Each string is plain text with an allowlisted set of placeholders the web
// app substitutes — [ChromePlaceholders]: `{face}` (the served face's
// label), `{world}` (this world's name), `{title}` (the entity's display
// title). Anything else in
// braces is left as written. No markup, no conditionals — the text is the
// operator's sentence, and rendering it is the whole feature.
//
// Not every surface has every value. A list or board has no single entity,
// so `{face}` and `{title}` are left as written in Projection; a row's badge
// has no title, so `{title}` is left as written in StandIn. Each field says
// which it substitutes.
type WorldMessages struct {
	// Absent is shown on a detail page for an entity that has no face in
	// this world. Empty shows nothing. Substitutes `{face}` (the face
	// actually on screen), `{world}` and `{title}`.
	Absent string `yaml:"absent,omitempty"`
	// Projection is the note on a list or board of a type that declares
	// faces — that entities with no face here are not listed. Empty shows
	// nothing. Substitutes `{world}` only.
	Projection string `yaml:"projection,omitempty"`
	// StandIn is the badge text on a row or card whose face is a stand-in
	// for the world's first choice (a within-chain fallback or an
	// `otherwise: default` substitution) — typically `{face}`. Empty renders
	// no badge at all. Substitutes `{face}` and `{world}`.
	StandIn string `yaml:"stand_in,omitempty"`
}

// ChromePlaceholders is the allowlist of `{placeholder}` names the web app
// substitutes in [WorldMessages] and [FaceMessages] text, and in a copy's
// `on_success.message`. The SPA holds the same list in
// frontend/src/utils/worldText.ts; a test in internal/dataentry pins the two
// to each other, because a name added on one side only renders literally on
// screen with no failure anywhere.
var ChromePlaceholders = []string{"face", "world", "title"}

// WorldOnAbsent is the behavior for an entity with no face in a world.
type WorldOnAbsent struct {
	// Redirect names a declared world (or `default`) the web app navigates
	// to instead of rendering the absent page: an ISMS whose adopted world
	// has no adopted version of a policy yet sends the reader to the
	// concept rather than to a page explaining that there is nothing here.
	// Validated at load; empty means no redirect.
	Redirect string `yaml:"redirect,omitempty"`
}

// DefaultWorldName is reserved: the default world is implicit and total,
// so a declaration under this name could only shadow or contradict it.
const DefaultWorldName = "default"

// DefaultQueryScopeName is the query scope a presentation surface uses when
// it names none. Unlike [DefaultWorldName] this is DECLARED, not implicit:
// the whole point of the key is to let an operator say "and by default,
// hide the archived ones".
const DefaultQueryScopeName = "default"

// AllQueryScopeName is reserved and implicit: it always resolves to "no
// predicate", and is how a view withdraws [DefaultQueryScopeName]. Reserved
// rather than merely conventional because a declaration under this name
// could only shadow the withdrawal — leaving an operator no way to ask for
// the unfiltered set.
const AllQueryScopeName = "all"

// UnmarshalYAML accepts `select: published` as well as
// `select: [review, published]`.
//
// The one-element spelling is the overwhelmingly common case, and
// requiring a list for it is the kind of friction that gets worked
// around with copy-paste. This is sugar over the SAME type — not a
// second representation — so everything downstream sees a chain.
func (w *WorldDef) UnmarshalYAML(node *yaml.Node) error {
	// A shadow type without the method, so decoding does not recurse.
	type worldDefYAML struct {
		Select    oneOrMany            `yaml:"select,omitempty"`
		Overrides map[string]oneOrMany `yaml:"overrides,omitempty"`
		Otherwise Otherwise            `yaml:"otherwise,omitempty"`
		Edits     string               `yaml:"edits,omitempty"`
		Create    string               `yaml:"create,omitempty"`
		Banner    string               `yaml:"banner,omitempty"`
		// oneOrMany like Select: the common case is a single face, and
		// `primary_for: nl` should not have to be written as a list.
		PrimaryFor oneOrMany     `yaml:"primary_for,omitempty"`
		Messages   WorldMessages `yaml:"messages,omitempty"`
		OnAbsent   WorldOnAbsent `yaml:"on_absent,omitempty"`
	}
	var raw worldDefYAML
	if err := node.Decode(&raw); err != nil {
		return err
	}
	w.Select = raw.Select
	w.Otherwise = raw.Otherwise
	w.Edits = raw.Edits
	w.Create = raw.Create
	w.Banner = raw.Banner
	w.PrimaryFor = raw.PrimaryFor
	w.Messages = raw.Messages
	w.OnAbsent = raw.OnAbsent
	if len(raw.Overrides) > 0 {
		w.Overrides = make(map[string][]string, len(raw.Overrides))
		for typ, chain := range raw.Overrides {
			w.Overrides[typ] = chain
		}
	}
	return nil
}

// oneOrMany decodes either a scalar or a sequence into a string slice.
type oneOrMany []string

func (o *oneOrMany) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var one string
		if err := node.Decode(&one); err != nil {
			return err
		}
		*o = []string{one}
		return nil
	}
	var many []string
	if err := node.Decode(&many); err != nil {
		return err
	}
	*o = many
	return nil
}

// ChainFor returns the candidate chain this world applies to entityType:
// the per-type override when one is declared, else the world's Select.
//
// The second result reports whether a non-empty chain applies. The two
// branches coincide today — an empty chain and no chain both mean rule 3,
// take Otherwise — so the compiler discards it, and the loader separately
// rejects an explicitly empty `select:` or override chain. It is returned
// because a caller that must tell "declared but empty" from "not
// declared" cannot recover the difference from the chain alone.
func (w WorldDef) ChainFor(entityType string) (chain []string, ok bool) {
	if override, found := w.Overrides[entityType]; found {
		return override, len(override) > 0
	}
	return w.Select, len(w.Select) > 0
}

// PropertyDefs implements PropertySchema for EntityDef.
func (e *EntityDef) PropertyDefs() map[string]PropertyDef {
	return e.Properties
}

// HasContent implements PropertySchema for EntityDef.
// Entities always support markdown body content.
func (e *EntityDef) HasContent() bool {
	return true
}

// Ensure EntityDef implements PropertySchema
var _ PropertySchema = (*EntityDef)(nil)

// PropertySchema abstracts property definitions for entities and relations.
// Both EntityDef and RelationDef implement this interface, allowing shared
// validation and form generation logic.
type PropertySchema interface {
	// PropertyDefs returns the property definitions map
	PropertyDefs() map[string]PropertyDef
	// HasContent returns true if markdown body content is supported
	HasContent() bool
}

// PropertyDef defines a property on an entity or relation
type PropertyDef struct {
	Type        string            `yaml:"type"`
	Required    bool              `yaml:"required,omitempty"`
	Values      []string          `yaml:"values,omitempty"` // For inline enum types
	Labels      map[string]string `yaml:"labels,omitempty"` // Optional display labels keyed by value (display-only; value stays the identity)
	Default     string            `yaml:"default,omitempty"`
	Description string            `yaml:"description,omitempty"` // Documentation for the property
	Format      string            `yaml:"format,omitempty"`      // Date format (Go layout, e.g., "2006-01-02")
	List        bool              `yaml:"list,omitempty"`        // True for multi-select properties (allows multiple values)
	// Computed is a pure Lua-compatible scalar expression evaluated from the
	// entity's other properties on every write. Computed properties are
	// materialized but never caller-authored. Entity-local dependencies are
	// inferred from the compiled expression.
	Computed string `yaml:"computed,omitempty" json:"Computed,omitempty"`
	// Unique constrains the property to a natural key: no two entities of
	// the same type may carry the same non-empty value. Enforced at write
	// time by the entitymanager (a colliding create/update is rejected as
	// a validation error → 422). Empty values are exempt (a property is
	// unique among the entities that set it). Ignored on `list` properties
	// (a natural key is a scalar).
	//
	// Guarantee level is not uniform across backends. The write-path check
	// is a check-then-write, not an atomic constraint, so under concurrent
	// writers two racing creates with the same value can both commit on
	// ANY backend. For race-free enforcement an operator adds a store-level
	// unique index (a partial unique index on pgstore), which is the only
	// mechanism that makes the constraint atomic. Uniqueness is therefore
	// NOT part of the store conformance contract — a new store.Store
	// implementation is not required to enforce it. See
	// `internal/entitymanager` checkUniqueProperties and the ACL
	// `principal_property` gate, which requires the referenced property to
	// be unique (and non-list).
	Unique bool `yaml:"unique,omitempty"`
	// Max caps how many attachments a `file`-type property may hold.
	// Zero/unset means 1 (the default, single-attachment). When > 1 the
	// property holds a list of attachment paths and the data-entry UI
	// switches from replace-mode to multi-file add-mode. Only meaningful
	// for `type: file`.
	Max int `yaml:"max,omitempty"`

	// Accept narrows the MIME allowlist for this `file` property to these
	// sniffed MIME types (e.g. ["application/pdf"]). Empty means inherit the
	// global allowlist. Only meaningful for `type: file`.
	Accept []string `yaml:"accept,omitempty"`

	// Scan overrides the global virus-scan policy for this `file` property.
	// ScanUnset (the zero value) means inherit the global policy. Only
	// meaningful for `type: file`.
	Scan ScanPolicy `yaml:"scan,omitempty"`

	// ScanCmd is the external scan command (array args) run when the effective
	// scan policy is `required`. Empty inherits the global scan command. Only
	// meaningful for `type: file`. See [AttachmentsConfig.ScanCmd].
	ScanCmd []string `yaml:"scan_cmd,omitempty"`

	// Transform is the ordered list of byte transforms (each an external
	// command) applied to this `file` property's uploads. Only meaningful for
	// `type: file`.
	Transform []TransformStep `yaml:"transform,omitempty"`
}

// TransformStep is one entry in a `transform:` pipeline. A step is EITHER an
// external command (Cmd) OR a native, in-process image operation (Image) —
// exactly one must be set, enforced at load. The two kinds have very different
// trust models: a Cmd shells out to an operator-configured binary (sandboxed by
// internal/cmdexec), while an Image step runs a memory-safe pure-Go decoder
// in-process with no external tool and no sandbox. See the attachment-security
// guide.
type TransformStep struct {
	// Cmd is an external command (array args) that rewrites the bytes; it
	// receives templated {in}/{out} paths owned by the runner.
	Cmd []string `yaml:"cmd,omitempty"`
	// Image is a native in-process image transform (decode, orient, re-encode).
	// Mutually exclusive with Cmd.
	Image *ImageStep `yaml:"image,omitempty"`
}

// ImageStep is a native, in-process image transform: it decodes the upload with
// a memory-safe pure-Go decoder, bakes in EXIF orientation, and re-encodes to a
// canonical format, dropping all metadata. Supported inputs are PNG, JPEG, GIF,
// and WebP (decode); the output is always PNG or JPEG (there is no pure-Go WebP
// encoder). Because decoding is memory-safe, this needs no external tool and no
// OS sandbox. Resize/thumbnail is a later phase and deliberately absent here.
type ImageStep struct {
	// Reencode is the canonical output format: "jpeg" (default) or "png". Both
	// are always within the default-safe MIME allowlist, so the re-encoded
	// output is safe by construction.
	Reencode string `yaml:"reencode,omitempty"`
	// Quality is the JPEG quality (1..100) when Reencode is "jpeg". Zero uses
	// the package default. Ignored for PNG.
	Quality int `yaml:"quality,omitempty"`
}

// ImageReencode returns the effective re-encode target for the step, applying
// the "jpeg" default when unset.
func (s ImageStep) ImageReencode() string {
	if s.Reencode == "" {
		return "jpeg"
	}
	return s.Reencode
}

// Kind reports which of the two mutually-exclusive step kinds is set, or an
// empty string when the step is malformed (both/neither set).
func (t TransformStep) Kind() string {
	switch {
	case len(t.Cmd) > 0 && t.Image == nil:
		return "cmd"
	case t.Image != nil && len(t.Cmd) == 0:
		return "image"
	default:
		return ""
	}
}

// TransformDef is one entry in the top-level `transforms:` view-export registry.
// It converts From-format bytes (v1: always "markdown") to the Produces
// content-type by running Command as an argv array. Command may reference the
// {in}/{out} placeholders (temp paths owned by the runner); otherwise input is
// on stdin and output on stdout. Commands come from project config (this file),
// never from a request — a request may only name a registered transform.
//
// The mirror of this in internal/transform is transform.Def; the metamodel keeps
// its own YAML-tagged type so internal/transform need not be imported here.
// CopyDef declares one named COPY DEFINITION: a mapped write of one entity
// content state (face) into another (design doc §9.1, TKT-C1XUA8).
//
// A request may only invoke a definition BY NAME — never submit an ad-hoc
// mapping. That is the transforms-registry precedent and it is what keeps
// the guard system meaningful: if a caller could describe an arbitrary copy,
// every guard would be advisory.
//
// Two shapes, and the difference is a security boundary (§9.2):
//
//   - SAME-ENTITY (`from: page@draft`, `to: page@published`) — promote and
//     revise. Runs ELEVATED: hidden fields travel with the entity, the
//     principal never sees them, and the same policy governs them on the
//     target face. Identity preserved, so policy follows.
//   - CROSS-ENTITY (`from: ticket`, `to: new ticket`) — spawn-a-follow-up.
//     Reads through the CALLER'S visibility gate, so only fields they may
//     read carry. An elevated cross-entity copy would launder unreadable
//     fields into an entity with a different audience.
type CopyDef struct {
	// From and To address a face as `type` or `type@face`. A bare type on
	// both sides with `to` prefixed `new ` is the cross-entity form.
	From string `yaml:"from"`
	To   string `yaml:"to"`

	// Fields maps target property -> source expression. The literal string
	// "all" in AllFields copies every declared property; the two are
	// mutually exclusive.
	//
	// Deliberately dumb (§9.1): field selection, rename, and the existing
	// {{...}} interpolation grammar. No expressions, no conditionals — the
	// same slippery slope world templates refused, with the same answer.
	Fields map[string]string `yaml:"fields,omitempty"`

	// AllFields is `fields: all` — the full-replace promote case. Rejected
	// at load on a CROSS-ENTITY copy: that copy reads through the caller's
	// gate, so a whole-entity replace would write a REDACTED entity, which
	// is the read-modify-write failure the codebase forbids everywhere.
	AllFields bool `yaml:"-"`

	// Relations maps a relation type to `merge` (add missing) or `replace`
	// (swap the target face's edges of that type). An omitted type is not
	// copied — the safe default, since copying a role-conferring edge grants
	// roles on the target.
	Relations map[string]string `yaml:"relations,omitempty"`

	// Label is optional display text for the ACTION a copy performs — e.g.
	// "Publish" for promote-policy, or "Translate to Dutch" for
	// translate-to-nl. It is what RULING 9 calls the operator-configurable
	// text: a UI renders one affordance per definition whose `from:` matches
	// the face being viewed, and this names the button.
	//
	// Purely presentational, exactly like [Transition.Label], which this
	// follows: a copy button and a state-machine move are the same shape —
	// text for an action, not for a destination. Empty falls back to the
	// definition's own name, which is a legible last resort because copy
	// names are operator-authored (`promote-policy`, `translate-to-nl`).
	// Display-only; the kernel ignores it entirely.
	//
	// A PLAIN STRING, deliberately: no `{{...}}` interpolation, even though
	// Fields has it. This type is "deliberately dumb (§9.1) — no expressions,
	// no conditionals", and per-entity label rendering would reopen exactly
	// that refusal. An operator wanting per-entity wording wants a different
	// feature, not a widened one.
	Label string `yaml:"label,omitempty"`

	// Guard gates the copy. Reuses the STATEMACHINE's vocabulary — the
	// `guard:` permission (subject-aware, via HoldsPermissionForEntity) and
	// a `when:` predicate — rather than acl.yaml's `requires_permission`,
	// which is the delegate-X gate on role-relation writes and is checked
	// globally. "The owner of THIS doc may publish it" needs the former.
	Guard CopyGuard `yaml:"guard,omitempty"`

	// OnSuccess is what the web app says and where it goes after this copy
	// runs. Both optional; see [CopyOnSuccess].
	OnSuccess CopyOnSuccess `yaml:"on_success,omitempty"`
}

// CopyOnSuccess is the operator's follow-through for a successful copy
// (TKT-5SZG2L).
type CopyOnSuccess struct {
	// Message is the confirmation toast. Empty means the copy's own label
	// (the button the reader just pressed) — never rela's "Created the X
	// face". Placeholders as in [WorldMessages], plus `{face}` naming the
	// face written.
	Message string `yaml:"message,omitempty"`
	// Landing is where the web app navigates afterwards. See [CopyLanding];
	// the zero value is [LandingWritten].
	Landing CopyLanding `yaml:"landing,omitempty"`
}

// CopyLanding is where the web app lands after a copy: `written` (the face
// the copy wrote — the default, since an editor who adopts a policy usually
// wants to see the adopted text), `stay` (reload in place — a translator
// wants to stay on the source), or a declared world or face:
//
//	landing: stay
//	landing: { world: actueel }
//	landing: { face: vastgesteld }
//
// Exactly one of the fields is set; [CopyLanding.UnmarshalYAML] accepts the
// scalar and the mapping forms. The zero value means `written`.
type CopyLanding struct {
	// Mode is LandingWritten or LandingStay when a scalar was given.
	Mode  string `yaml:"-"`
	World string `yaml:"world,omitempty"`
	Face  string `yaml:"face,omitempty"`
}

// The scalar landing modes.
const (
	LandingWritten = "written"
	LandingStay    = "stay"
)

// IsZero reports whether no landing was declared (which means `written`).
func (l CopyLanding) IsZero() bool { return l.Mode == "" && l.World == "" && l.Face == "" }

// UnmarshalYAML accepts `landing: stay`, `landing: written`,
// `landing: {world: name}` and `landing: {face: name}`.
//
// The mapping form is decoded by walking its keys rather than into a struct,
// because a struct decode drops what it does not recognize: `{wrold: x}` or
// `{}` would come out as the zero value, which MEANS `written`, and the
// operator's declaration would be discarded without a word — the failure
// class BUG-I0N3YR closed for affordance grants. An unknown key, an empty
// mapping and an empty value are all refused here, at load.
func (l *CopyLanding) UnmarshalYAML(node *yaml.Node) error {
	const want = "want `written`, `stay`, `{world: <name>}` or `{face: <name>}`"
	switch node.Kind {
	case yaml.ScalarNode:
		l.Mode = node.Value
		return nil
	case yaml.MappingNode:
	default:
		return fmt.Errorf("landing: %s", want)
	}
	if len(node.Content) == 0 {
		return fmt.Errorf("landing: empty mapping; %s", want)
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, val := node.Content[i], node.Content[i+1]
		if val.Kind != yaml.ScalarNode || val.Value == "" {
			return fmt.Errorf("landing: `%s` needs a name", key.Value)
		}
		switch key.Value {
		case "world":
			l.World = val.Value
		case "face":
			l.Face = val.Value
		default:
			return fmt.Errorf("landing: unknown key %q; %s", key.Value, want)
		}
	}
	return nil
}

// CopyGuard is a copy definition's authorization, sharing the statemachine's
// declaration vocabulary (§9.1: shared guard machinery, separate declaration
// — a copy is not a property change).
type CopyGuard struct {
	// Permission is an ACL permission name checked against the SOURCE entity
	// via HoldsPermissionForEntity, so a role conferred by an ownership
	// relation satisfies it without a global grant.
	Permission string `yaml:"permission,omitempty"`

	// When is an internal/predicate expression evaluated against the source
	// face. False refuses the copy (422), matching a transition precondition.
	When string `yaml:"when,omitempty"`
}

// UnmarshalYAML accepts `fields: all` as a scalar alongside the mapping form.
func (c *CopyDef) UnmarshalYAML(unmarshal func(any) error) error {
	// Alias avoids infinite recursion into this method.
	//
	// NOTE: this shadow struct must list EVERY yaml-bearing field of CopyDef.
	// A field present on CopyDef but missing here is silently dropped at load
	// — no error, no warning, just a zero value — which is how `label:` was
	// ignored when it was first added. TestCopyDef_UnmarshalCoversEveryField
	// compares the two by reflection so the next added field fails loudly
	// instead.
	type raw struct {
		From      string            `yaml:"from"`
		To        string            `yaml:"to"`
		Label     string            `yaml:"label,omitempty"`
		Fields    any               `yaml:"fields,omitempty"`
		Relations map[string]string `yaml:"relations,omitempty"`
		Guard     CopyGuard         `yaml:"guard,omitempty"`
		OnSuccess CopyOnSuccess     `yaml:"on_success,omitempty"`
	}
	var r raw
	if err := unmarshal(&r); err != nil {
		return err
	}
	c.From, c.To, c.Label, c.Relations, c.Guard = r.From, r.To, r.Label, r.Relations, r.Guard
	c.OnSuccess = r.OnSuccess
	switch f := r.Fields.(type) {
	case nil:
	case string:
		if f != "all" {
			return fmt.Errorf("copy: fields: %q is not valid — use a mapping, or the literal \"all\"", f)
		}
		c.AllFields = true
	case map[string]any:
		c.Fields = make(map[string]string, len(f))
		for k, v := range f {
			c.Fields[k] = fmt.Sprint(v)
		}
	default:
		return errors.New("copy: fields: want a mapping or the literal \"all\"")
	}
	return nil
}

type TransformDef struct {
	From     string   `yaml:"from"`
	Command  []string `yaml:"command"`
	Produces string   `yaml:"produces"`
}

// FileMax returns the effective attachment cap for a file property:
// Max when set (>0), otherwise 1. Callers should use this rather than
// reading Max directly so the unset-means-one default lives in one place.
func (p PropertyDef) FileMax() int {
	if p.Max > 0 {
		return p.Max
	}
	return 1
}

// Built-in property types
const (
	PropertyTypeString   = "string"
	PropertyTypeDate     = "date"
	PropertyTypeDatetime = "datetime"
	PropertyTypeInteger  = "integer"
	PropertyTypeBoolean  = "boolean"
	PropertyTypeEnum     = "enum"
	PropertyTypeFile     = "file"
	PropertyTypeRrule    = "rrule"
)

// ID types for entities
const (
	IDTypeShort      = "short"      // IDs are random base36 strings (e.g., REQ-a3f8) - default
	IDTypeSequential = "sequential" // IDs are auto-generated with numeric suffix (e.g., REQ-001)
	IDTypeManual     = "manual"     // IDs are manually specified strings (e.g., auth-module)

	// Deprecated alias (still accepted for backwards compatibility)
	IDTypeString = "string" // Deprecated: use "manual" instead
)

// ID capitalization modes for short IDs
const (
	IDCapsUpper = "upper" // Random suffix is uppercase (e.g., REQ-A3F8) - default
	IDCapsLower = "lower" // Random suffix is lowercase (e.g., REQ-a3f8)
)

// ReservedPropertyNames contains property names that cannot be used in metamodel definitions
// because they conflict with built-in entity fields.
var ReservedPropertyNames = map[string]bool{
	"id":   true, // Entity.ID
	"type": true, // Entity.Type
}

// RelationScope declares what a relation type attaches to under content
// states (TKT-DOFYR1, design doc §2.2).
//
//   - identity: the edge attaches to the entity's bare id and is shared
//     by all its states. Ownership, containment, membership — a draft
//     does not get a different owner than its published face by
//     accident. The DEFAULT, so a project without faces behaves
//     identically either way.
//   - content: the edge attaches to a specific state on its TAIL side
//     (Relation.FromFace); a draft may cite different targets than
//     the published face. Heads stay entity-level (§2.3).
//
// DECLARATIVE-ONLY in Step 1: entity delete cascades every edge and
// rename re-keys bare ids regardless of scope, so no store behavior
// branches on it yet. Its consumers land with later steps — role
// conferral (worlds/ACL), cardinality subjects, and the copy kernel's
// per-relation-type merge semantics all read this one declaration.
type RelationScope string

const (
	// ScopeIdentity is the zero/default scope; see RelationScope.
	ScopeIdentity RelationScope = ""
	// ScopeIdentityExplicit is the spelled-out identity scope.
	ScopeIdentityExplicit RelationScope = "identity"
	// ScopeContent marks state-tailed edges; see RelationScope.
	ScopeContent RelationScope = "content"
)

// IsValid reports whether the scope is a declared value.
func (s RelationScope) IsValid() bool {
	switch s {
	case ScopeIdentity, ScopeIdentityExplicit, ScopeContent:
		return true
	default:
		return false
	}
}

// IsContent reports whether edges of this type attach to a specific
// state on their tail side.
func (s RelationScope) IsContent() bool { return s == ScopeContent }

// IsIdentity reports whether edges of this type attach to the entity's
// bare id. ALWAYS branch through IsIdentity/IsContent, never by
// comparing against [ScopeIdentity]: the identity scope has two
// spellings ("" and "identity"), so `scope == ScopeIdentity` is wrong
// for any metamodel that writes it out — and passes every test written
// against one that doesn't.
func (s RelationScope) IsIdentity() bool { return !s.IsContent() }

// OrderableMode controls which side(s) of a relation type are user-orderable.
type OrderableMode string

const (
	OrderableNone     OrderableMode = ""
	OrderableOutgoing OrderableMode = "outgoing"
	OrderableIncoming OrderableMode = "incoming"
	OrderableBoth     OrderableMode = "both"
)

// Reserved relation-property names that hold the user-controlled order value.
// The names are stable across mode changes so that promoting a relation from
// outgoing-only to both (or vice versa) keeps existing values on disk valid.
const (
	OrderPropertyOut = "_order_out"
	OrderPropertyIn  = "_order_in"
)

// IsValid reports whether the value is a recognized OrderableMode (including the
// empty "not orderable" value).
func (m OrderableMode) IsValid() bool {
	switch m {
	case OrderableNone, OrderableOutgoing, OrderableIncoming, OrderableBoth:
		return true
	}
	return false
}

// DefaultDateFormat is the default format for date properties (ISO 8601)
const DefaultDateFormat = "2006-01-02"

// DefaultDatetimeFormat is the default format for datetime properties (RFC3339,
// a time-bearing ISO 8601 instant). Unlike date, a datetime value carries a
// time-of-day and (canonically) a UTC offset.
const DefaultDatetimeFormat = time.RFC3339

// IsBuiltinType returns true if the type is a built-in property type
func IsBuiltinType(t string) bool {
	switch t {
	case PropertyTypeString, PropertyTypeDate, PropertyTypeDatetime, PropertyTypeInteger,
		PropertyTypeBoolean, PropertyTypeEnum, PropertyTypeFile, PropertyTypeRrule:
		return true
	}
	return false
}

// GetDateFormat returns the date format for a property, defaulting to ISO 8601.
// For datetime properties the default is RFC3339 (time-bearing); an explicit
// Format still overrides.
func (p *PropertyDef) GetDateFormat() string {
	if p.Format != "" {
		return p.Format
	}
	if p.Type == PropertyTypeDatetime {
		return DefaultDatetimeFormat
	}
	return DefaultDateFormat
}

// RelationDef defines a relation type in the metamodel
type RelationDef struct {
	Label       string      `yaml:"label"`
	Description string      `yaml:"description,omitempty"`
	From        []string    `yaml:"from"`
	To          []string    `yaml:"to"`
	Inverse     *InverseDef `yaml:"inverse,omitempty"`
	Symmetric   bool        `yaml:"symmetric,omitempty"`
	MinOutgoing *int        `yaml:"min_outgoing,omitempty"`
	MaxOutgoing *int        `yaml:"max_outgoing,omitempty"`
	MinIncoming *int        `yaml:"min_incoming,omitempty"`
	MaxIncoming *int        `yaml:"max_incoming,omitempty"`

	// Scope declares whether edges of this type attach to the entity
	// (identity, the default) or to a specific content state on the
	// tail side (content). See RelationScope. Unrelated to the Content
	// field below, which is about markdown bodies — the shared word is
	// an unfortunate collision, not a connection.
	Scope RelationScope `yaml:"scope,omitempty"`

	// Properties defines typed properties that can be attached to relations of this type.
	// Uses the same PropertyDef structure as entity properties.
	Properties map[string]PropertyDef `yaml:"properties,omitempty"`

	// Content indicates whether relations of this type support markdown body content.
	// When true, the data-entry UI will show a content editor for the relation.
	// Unrelated to Scope's "content" value above (state-tailed edges) —
	// same word, different concern.
	Content bool `yaml:"content,omitempty"`

	// Orderable declares which side(s) of this relation type are user-orderable.
	// When set, the data-entry UI offers drag-to-reorder controls on the enabled
	// side(s); the API returns relations sorted by the corresponding managed
	// order property (OrderPropertyOut / OrderPropertyIn).
	Orderable OrderableMode `yaml:"orderable,omitempty"`
}

// OutgoingOrderProperty returns the relation-property name that holds the
// outgoing-side order value, or "" if outgoing ordering is not enabled.
func (r *RelationDef) OutgoingOrderProperty() string {
	if r.Orderable == OrderableOutgoing || r.Orderable == OrderableBoth {
		return OrderPropertyOut
	}
	return ""
}

// IncomingOrderProperty returns the relation-property name that holds the
// incoming-side order value, or "" if incoming ordering is not enabled.
func (r *RelationDef) IncomingOrderProperty() string {
	if r.Orderable == OrderableIncoming || r.Orderable == OrderableBoth {
		return OrderPropertyIn
	}
	return ""
}

// PropertyDefs implements PropertySchema for RelationDef.
func (r *RelationDef) PropertyDefs() map[string]PropertyDef {
	return r.Properties
}

// HasContent implements PropertySchema for RelationDef.
func (r *RelationDef) HasContent() bool {
	return r.Content
}

// HasAdvancedFeatures returns true if this relation type has properties or content,
// indicating that the data-entry UI should use the advanced cards+modal interface.
func (r *RelationDef) HasAdvancedFeatures() bool {
	return len(r.Properties) > 0 || r.Content
}

// Ensure RelationDef implements PropertySchema
var _ PropertySchema = (*RelationDef)(nil)

// InverseDef defines the inverse of a relation.
// Can be unmarshaled from either a simple string (inverse identifier only)
// or an object with id and label fields.
type InverseDef struct {
	// ID is the identifier for the inverse relation (e.g., "addressedBy")
	ID string `yaml:"id,omitempty"`

	// Label is the display label for the inverse relation (e.g., "addressed by").
	// If not specified, the raw ID is displayed — labels are authored, never
	// derived (DEC-6C1NAA).
	Label string `yaml:"label,omitempty"`
}

// GetID returns the inverse relation identifier
func (i *InverseDef) GetID() string {
	return i.ID
}

// GetLabel returns the display label, falling back to the raw ID.
//
// A label is authored, never derived (DEC-6C1NAA). This used to convert
// camelCase to space-separated lowercase ("addressedBy" → "addressed by"),
// which bakes an English orthographic convention into a language-neutral
// metamodel. Write an explicit `label:` to control the display text.
func (i *InverseDef) GetLabel() string {
	if i.Label != "" {
		return i.Label
	}
	return i.ID
}

// UnmarshalYAML allows InverseDef to be unmarshaled from either a string or an object.
// String form: "addressedBy" (ID only; the ID doubles as the display label)
// Object form: { id: "addressedBy", label: "addressed by" }
func (i *InverseDef) UnmarshalYAML(unmarshal func(any) error) error {
	// First try to unmarshal as a string (simple form)
	var simpleForm string
	if err := unmarshal(&simpleForm); err == nil {
		i.ID = simpleForm
		// Label will be auto-derived by GetLabel()
		return nil
	}

	// Try to unmarshal as an object (expanded form)
	type inverseDefAlias InverseDef // Alias to avoid infinite recursion
	var objectForm inverseDefAlias
	if err := unmarshal(&objectForm); err != nil {
		return err
	}

	*i = InverseDef(objectForm)
	return nil
}

// AutomationDef defines a trigger-action automation rule.
type AutomationDef struct {
	Name        string             `yaml:"name"`
	Description string             `yaml:"description,omitempty"`
	On          AutomationTrigger  `yaml:"on"`
	Do          []AutomationAction `yaml:"do,omitempty"`
	Validate    []AutomationCheck  `yaml:"validate,omitempty"`
}

// AutomationTrigger specifies conditions that activate an automation.
type AutomationTrigger struct {
	Entity          StringOrSlice `yaml:"entity,omitempty"`
	Property        string        `yaml:"property,omitempty"`
	Becomes         string        `yaml:"becomes,omitempty"`
	From            string        `yaml:"from,omitempty"`
	Created         bool          `yaml:"created,omitempty"`
	RelationCreated string        `yaml:"relation_created,omitempty"`
	RelationRemoved string        `yaml:"relation_removed,omitempty"`

	// Faces limits the trigger to specific content states, named as declared
	// in `faces:`. Empty means every state — see automation.Trigger.Faces for
	// why that is the default and when to narrow it.
	Faces StringOrSlice `yaml:"faces,omitempty"`

	When []string `yaml:"when,omitempty"` // Property conditions that must match (AND logic)

	// Condition is a predicate EXPRESSION that must hold for the
	// automation to fire, ANDed with every When clause.
	//
	// When and Condition are separate keys because their syntaxes
	// overlap without erroring: filter.Parse accepts
	// "days_between(entity.due, today()) <= 7" as a filter on a property
	// literally named "days_between(entity.due, today())", which then
	// matches nothing, silently. Sniffing which dialect a string is
	// written in would guess, and guess quietly — so the operator says
	// which one they meant by choosing the key.
	//
	// `when:` is filter syntax (`status=todo`) transpiled to predicate on
	// load; `condition:` is predicate source evaluated as written, so it
	// gets boolean composition and the host-function stdlib — notably the
	// date arithmetic (today/days_between/date_add/rrule_next) that a
	// property filter cannot express.
	Condition string `yaml:"condition,omitempty"`
}

// AutomationAction specifies an operation to perform.
type AutomationAction struct {
	Set            string                `yaml:"set,omitempty"`
	Value          string                `yaml:"value,omitempty"`
	CreateRelation *CreateRelationAction `yaml:"create_relation,omitempty"`
	CreateEntity   *CreateEntityAction   `yaml:"create_entity,omitempty"`
	Lua            string                `yaml:"lua,omitempty"`      // Inline Lua code to execute
	LuaFile        string                `yaml:"lua_file,omitempty"` // Path to Lua script in scripts/ directory

	// AllowACLBypass unlocks rela.bypass_acl in this Lua action (TKT-D8T148).
	// Operator-only (lives in the schema file). When set, the script may call
	// rela.bypass_acl(fn) to obtain a closure-scoped elevated handle whose
	// access skips the ACL deny (still audited, real principal preserved).
	// Ignored for non-Lua actions.
	//
	// Since TKT-Y3JVFK this is an enum, not a bool: `read`, `write` or
	// `read+write` select which methods the handle carries. The legacy
	// `true` is refused at parse time with a message naming `read+write`;
	// `rela migrate` rewrites it.
	AllowACLBypass ACLBypass `yaml:"allow_acl_bypass,omitempty"`

	// Capabilities declares which ambient capabilities this Lua action may
	// reach: http, ai, write_file and named secrets (TKT-YH52OM). Omitting it
	// grants NONE of them — automations run on the write path of any HTTP
	// request, so they are not an operator-shell surface and do not get the
	// trusted default. Ignored for non-Lua actions.
	Capabilities Capabilities `yaml:"capabilities,omitempty"`
}

// CreateRelationAction specifies parameters for creating a relation.
type CreateRelationAction struct {
	Relation string `yaml:"relation"`
	To       string `yaml:"to"`
}

// CreateEntityAction specifies parameters for creating a new entity.
type CreateEntityAction struct {
	Type       string            `yaml:"type"`                 // Entity type to create
	Template   string            `yaml:"template,omitempty"`   // Optional: template variant, supports interpolation (e.g., "{{new.kind}}")
	Properties map[string]string `yaml:"properties,omitempty"` // Properties (values support interpolation)
	Relation   string            `yaml:"relation,omitempty"`   // Optional: relation FROM trigger TO created entity
	IfExists   string            `yaml:"if_exists,omitempty"`  // Behavior when relation already exists: skip (default), error, replace
}

// AutomationCheck specifies a validation condition.
type AutomationCheck struct {
	Check    string `yaml:"check"`
	Severity string `yaml:"severity,omitempty"`
	Message  string `yaml:"message"`
}

// ContentRule defines validation rules for markdown body content.
type ContentRule struct {
	// RequiredHeaders specifies headers that must appear in the content
	RequiredHeaders []HeaderCheck `yaml:"required-headers,omitempty"`

	// Checklist specifies validation rules for markdown checklists (task lists)
	Checklist *ChecklistRule `yaml:"checklist,omitempty"`
}

// ChecklistRule defines validation rules for markdown checklists.
type ChecklistRule struct {
	// AllChecked requires all checklist items to be checked
	AllChecked bool `yaml:"all-checked,omitempty"`

	// AllowSkipped treats strikethrough items as complete (e.g., "- [x] ~~task~~ (N/A: reason)")
	AllowSkipped bool `yaml:"allow-skipped,omitempty"`
}

// RelationConstraint is a relation-cardinality assertion on a validation
// rule, keyed in ValidationRule.Relations by relation type. It counts the
// entity's relations of that type — outgoing unless Direction says
// otherwise — whose far entity matches all of the Where filters and, when
// TargetType is set, is of that type. The count then has to satisfy Min
// and/or Max.
//
// Counting is per EDGE, not per distinct far entity: two edges to the same
// entity count two.
//
// Where reuses the same filter syntax as When/Then (e.g. "status=done"),
// matched against the FAR entity's properties. At least one of Min/Max
// must be set; the loader rejects a constraint with neither bound, an
// undeclared relation type, or bounds that nothing can satisfy.
//
// # The count is what the acting identity can see
//
// Relations and their targets are read through the ACL-gated reader, so a
// constraint counts VISIBLE relations, not all relations. A gate is
// therefore not a global invariant: two principals validating the same
// graph can legitimately reach different verdicts, because an edge is
// dropped when either endpoint is invisible to the reader.
//
// This is deliberate — the alternative leaks hidden entities through
// violation messages — and it is the same gating the Lua implementation
// this replaced already had. It matters mainly for `Max`, where an
// invisible target means a gate can pass for one principal and fail for
// another. The CLI and CI paths wire an unrestricted reader, so the
// authoritative verdict (the one enforcing the workflow) sees everything.
type RelationConstraint struct {
	// Where filters the entities at the far end of the edge. All conditions
	// are ANDed; empty means every one counts.
	Where []string `yaml:"where,omitempty"`

	// Direction selects which end of the edge the entity being validated
	// sits on: "outgoing" (the default) counts edges it is the SOURCE of,
	// "incoming" counts edges it is the TARGET of.
	//
	// Empty means outgoing, so every rule written before this key existed
	// keeps its meaning.
	//
	// # Incoming counts are entity-level
	//
	// An edge records the face of its tail but has no head face — heads are
	// entity-level by construction — so an incoming edge cannot be
	// attributed to one face of the entity it arrives at. Each face of a
	// faced entity is validated as its own row, so an incoming constraint
	// evaluates identically for every face and reports one violation per
	// face for a single underlying defect. That is noisy rather than wrong,
	// and narrowing the rule with `faces:` avoids it.
	//
	// Refused on a `symmetric: true` relation: symmetry is a presentation
	// convention over ONE stored row, not a reciprocal pair, so a query from
	// the other endpoint finds nothing. Honoring direction there would give
	// the two ends different counts for the same relationship, decided by
	// which way round the edge happened to be written.
	Direction string `yaml:"direction,omitempty"`

	// TargetType restricts the count to far entities of this type. Empty
	// counts every type the relation reaches.
	//
	// It exists because a relation may reach several types, and then a count
	// alone cannot say WHAT was found — "has a task" and "has a recurring
	// schedule" are different claims over one relation. `Where` cannot
	// express it: those clauses filter an entity's PROPERTIES, and a type is
	// not a property.
	//
	// Setting it also makes the far type known before any data is read, so
	// the loader can check `Where` against it rather than leaving a
	// mistyped property to fail per-entity at check time.
	TargetType string `yaml:"target_type,omitempty"`

	// Min requires at least this many matching relations (nil = no lower bound).
	Min *int `yaml:"min,omitempty"`

	// Max requires at most this many matching relations (nil = no upper bound).
	Max *int `yaml:"max,omitempty"`
}

// RelationConstraintDirection values, as spelled in schema.yaml.
const (
	RelationDirectionOutgoing = "outgoing"
	RelationDirectionIncoming = "incoming"
)

// IsIncoming reports whether the constraint counts edges arriving at the
// entity. Empty (the default) is outgoing.
func (c RelationConstraint) IsIncoming() bool {
	return c.Direction == RelationDirectionIncoming
}

// HeaderCheck specifies a header to check for in markdown content.
// Can be unmarshaled from either a simple string (exact match) or an object with pattern field.
type HeaderCheck struct {
	// Header is an exact header string to match (e.g., "## Context")
	Header string `yaml:"header,omitempty"`

	// Pattern is a regex pattern to match headers (e.g., "## (Alternative|Alternatives)")
	Pattern string `yaml:"pattern,omitempty"`
}

// IsPattern returns true if this is a regex pattern match
func (h *HeaderCheck) IsPattern() bool {
	return h.Pattern != ""
}

// GetMatchString returns the pattern or header string to match against
func (h *HeaderCheck) GetMatchString() string {
	if h.Pattern != "" {
		return h.Pattern
	}
	return h.Header
}

// UnmarshalYAML allows HeaderCheck to be unmarshaled from either a string or an object.
// String form: "## Context" (exact header match)
// Object form: { pattern: "## (Alternative|Alternatives)" }
func (h *HeaderCheck) UnmarshalYAML(unmarshal func(any) error) error {
	// First try to unmarshal as a string (simple form - exact match)
	var simpleForm string
	if err := unmarshal(&simpleForm); err == nil {
		h.Header = simpleForm
		return nil
	}

	// Try to unmarshal as an object (expanded form with pattern)
	type headerCheckAlias HeaderCheck // Alias to avoid infinite recursion
	var objectForm headerCheckAlias
	if err := unmarshal(&objectForm); err != nil {
		return err
	}

	*h = HeaderCheck(objectForm)
	return nil
}

// StringOrSlice is a YAML type that can be unmarshaled from either a string or []string.
type StringOrSlice []string

// UnmarshalYAML allows StringOrSlice to be unmarshaled from either a string or a slice.
func (s *StringOrSlice) UnmarshalYAML(unmarshal func(any) error) error {
	// Try string first
	var single string
	if err := unmarshal(&single); err == nil {
		*s = []string{single}
		return nil
	}
	// Try slice
	var slice []string
	if err := unmarshal(&slice); err != nil {
		return err
	}
	*s = slice
	return nil
}
