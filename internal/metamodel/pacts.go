package metamodel

import (
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// PactDef is one entry of an entity type's `pacts:` block: the contract
// between rela and one external system (Basecamp, GitHub, …) that holds a
// counterpart — a Twin — of each entity of that type.
//
// It declares FIELD OWNERSHIP, nothing else. rela does not call the external
// system itself (not in this stage); an agent does, with its own tools. The
// pact tells that agent (and rela's write guard) which fields the external side
// owns (Theirs), which both sides edit (Shared), and for which of rela's own
// fields a foreign edit is to be proposed rather than reverted (Propose). Every
// field not in Theirs or Shared is ours, and a computed property is always
// ours: rela derives it, so no other side can own it.
//
// A field is a declared property name of the entity type, or [PactBodyField]
// for the markdown content. Relations are not fields.
type PactDef struct {
	// Scope is the http(s) URL of the external container the twins live in
	// (a Basecamp todolist, a GitHub repository). Required.
	Scope string `yaml:"scope"`

	// Theirs lists the fields the external system owns. The single entry
	// [PactAllFields] means every field except computed properties.
	Theirs []string `yaml:"theirs,omitempty"`

	// Shared lists the fields both sides edit; concurrent edits conflict.
	Shared []string `yaml:"shared,omitempty"`

	// Propose lists ours fields where an edit by the external side is
	// reported as a proposal instead of being reverted.
	Propose []string `yaml:"propose,omitempty"`

	// Instructions is optional markdown describing how the two sides map —
	// the agent's brief. Never served over /_schema.
	Instructions string `yaml:"instructions,omitempty"`
}

// pactKeys are the keys a pact mapping may contain, derived from PactDef's
// yaml tags so a field added to the struct is accepted without a second list
// to keep in step.
var pactKeys = yamlKeysOf(reflect.TypeFor[PactDef]())

func yamlKeysOf(t reflect.Type) []string {
	keys := make([]string, 0, t.NumField())
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name != "" && name != "-" {
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)
	return keys
}

// UnmarshalYAML decodes a pact STRICTLY: an unknown key is refused.
//
// A plain struct decode drops what it does not recognize, and for a pact that
// is the worst possible failure: `shraed: [status]` would load as "status is
// ours", and rela would silently revert every edit the external side makes to
// it. The keys are walked first — the [CopyLanding.UnmarshalYAML] approach —
// and the mapping is then decoded through a method-less alias of PactDef, so
// no shadow struct can fall out of step with the real one.
func (d *PactDef) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: pact: want a mapping with keys %s", node.Line, strings.Join(pactKeys, ", "))
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		if !slices.Contains(pactKeys, key.Value) {
			return fmt.Errorf("line %d: pact: unknown key %q%s (valid keys: %s)",
				key.Line, key.Value, didYouMean(key.Value, pactKeys), strings.Join(pactKeys, ", "))
		}
	}
	type plain PactDef
	return node.Decode((*plain)(d))
}

// PactBodyField is the reserved field name for an entity's markdown body.
const PactBodyField = "body"

// PactAllFields, as the sole entry of [PactDef.Theirs], hands every field
// except computed properties to the external system.
const PactAllFields = "*"

// Owner says which side of a pact owns a field.
type Owner int

// Field owners. The zero value is [OwnerOurs]: a field a pact does not name
// belongs to rela.
const (
	OwnerOurs Owner = iota
	OwnerTheirs
	OwnerShared
)

// String returns "ours", "theirs" or "shared".
func (o Owner) String() string {
	switch o {
	case OwnerTheirs:
		return "theirs"
	case OwnerShared:
		return "shared"
	default:
		return "ours"
	}
}

// Pact is the compiled, validated view of one [PactDef]. Obtain one from
// [PactPolicy.Pact]; the zero value owns nothing on the external side.
type Pact struct {
	entityType   string
	system       string
	scope        string
	instructions string
	allTheirs    bool
	theirs       []string // sorted, as declared (["*"] when allTheirs)
	shared       []string // sorted
	propose      []string // sorted
	computed     []string // sorted computed properties of the entity type
}

// compilePact builds the read view of def, declared on entity type et. Slices
// are copied so a caller cannot mutate the metamodel through an accessor, and
// sorted so accessors are deterministic.
func compilePact(entityType, system string, def PactDef, et EntityDef) Pact {
	var computed []string
	for name, prop := range et.Properties {
		if prop.Computed != "" {
			computed = append(computed, name)
		}
	}
	sort.Strings(computed)
	return Pact{
		entityType:   entityType,
		system:       system,
		scope:        def.Scope,
		instructions: def.Instructions,
		allTheirs:    slices.Contains(def.Theirs, PactAllFields),
		theirs:       sortedCopy(def.Theirs),
		shared:       sortedCopy(def.Shared),
		propose:      sortedCopy(def.Propose),
		computed:     computed,
	}
}

func sortedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := slices.Clone(in)
	sort.Strings(out)
	return out
}

// System returns the external system id (the key under `pacts:`).
func (p Pact) System() string { return p.system }

// EntityType returns the canonical entity type the pact is declared on.
func (p Pact) EntityType() string { return p.entityType }

// Scope returns the external container URL.
func (p Pact) Scope() string { return p.scope }

// Instructions returns the agent brief (markdown), possibly empty.
func (p Pact) Instructions() string { return p.instructions }

// Owner reports which side owns field. A computed property is always
// [OwnerOurs]; otherwise, with `theirs: ["*"]` every field is [OwnerTheirs],
// and without it a field not named in theirs or shared is [OwnerOurs].
func (p Pact) Owner(field string) Owner {
	switch {
	case p.Computed(field):
		return OwnerOurs
	case p.allTheirs:
		return OwnerTheirs
	case containsSorted(p.theirs, field):
		return OwnerTheirs
	case containsSorted(p.shared, field):
		return OwnerShared
	default:
		return OwnerOurs
	}
}

// Computed reports whether field is a computed property of the pact's entity
// type. rela derives such a field, so sync never compares or writes it:
// Reconcile skips it entirely.
func (p Pact) Computed(field string) bool { return containsSorted(p.computed, field) }

// Proposes reports whether an external edit of the ours field is proposed
// rather than reverted.
func (p Pact) Proposes(field string) bool { return containsSorted(p.propose, field) }

// Theirs returns the external-owned fields as declared (possibly
// [PactAllFields] alone), sorted. The caller owns the returned slice.
func (p Pact) Theirs() []string { return slices.Clone(p.theirs) }

// Shared returns the fields both sides edit, sorted. The caller owns the
// returned slice.
func (p Pact) Shared() []string { return slices.Clone(p.shared) }

// Propose returns the fields whose foreign edits are proposed, sorted. The
// caller owns the returned slice.
func (p Pact) Propose() []string { return slices.Clone(p.propose) }

func containsSorted(sorted []string, s string) bool {
	_, ok := slices.BinarySearch(sorted, s)
	return ok
}

// PactPolicy is the read view over every pact in a metamodel.
//
// Constructed with [NewPactPolicy] rather than living as methods on
// [Metamodel], whose exported surface is capped — the [CommentPolicy]
// precedent. The pacts are compiled once at construction, so lookups do not
// allocate.
type PactPolicy struct {
	meta  *Metamodel
	pacts map[string]map[string]Pact // canonical entity type → system → pact
}

// NewPactPolicy returns the pact view over m.
//
// Nil: a nil m yields a policy with no pacts, so callers on a
// partially-wired path degrade to "no twins" rather than panicking.
func NewPactPolicy(m *Metamodel) PactPolicy {
	p := PactPolicy{meta: m}
	if m == nil {
		return p
	}
	for typeName, def := range m.Entities {
		if len(def.Pacts) == 0 {
			continue
		}
		if p.pacts == nil {
			p.pacts = map[string]map[string]Pact{}
		}
		bySystem := make(map[string]Pact, len(def.Pacts))
		for system, pd := range def.Pacts {
			bySystem[system] = compilePact(typeName, system, pd, def)
		}
		p.pacts[typeName] = bySystem
	}
	return p
}

// Enabled reports whether any entity type declares a pact.
func (p PactPolicy) Enabled() bool { return len(p.pacts) > 0 }

// Pact returns the pact between entityType and system. entityType may be an
// alias; it resolves to the canonical type the way [CommentPolicy.Commentable]
// resolves it.
func (p PactPolicy) Pact(entityType, system string) (Pact, bool) {
	bySystem, ok := p.forType(entityType)
	if !ok {
		return Pact{}, false
	}
	pact, ok := bySystem[system]
	return pact, ok
}

// Systems returns the systems entityType (or its alias) has a pact with,
// sorted; nil when it has none.
func (p PactPolicy) Systems(entityType string) []string {
	bySystem, ok := p.forType(entityType)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(bySystem))
	for system := range bySystem {
		out = append(out, system)
	}
	sort.Strings(out)
	return out
}

func (p PactPolicy) forType(entityType string) (map[string]Pact, bool) {
	if len(p.pacts) == 0 {
		return nil, false
	}
	if bySystem, ok := p.pacts[entityType]; ok {
		return bySystem, true
	}
	bySystem, ok := p.pacts[p.meta.ResolveAlias(entityType)]
	return bySystem, ok
}

// pactSystemPattern constrains a system id: it becomes a directory name under
// .rela/twins and a CLI argument, so it is kept lowercase and path-safe.
var pactSystemPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// validatePacts checks every entity type's `pacts:` block.
//
// A bad pact fails the whole load, like a bad `comments:` block: a pact that
// half-applies would make rela enforce the wrong ownership — reverting edits
// the operator meant the external side to own, or letting through edits it
// meant to guard. Errors come in sorted entity and system order so identical
// loads report identically.
func validatePacts(m *Metamodel) []string {
	typeNames := make([]string, 0, len(m.Entities))
	for name, def := range m.Entities {
		if len(def.Pacts) > 0 {
			typeNames = append(typeNames, name)
		}
	}
	sort.Strings(typeNames)

	var errs []string
	for _, typeName := range typeNames {
		def := m.Entities[typeName]
		if len(def.Faces) > 0 {
			errs = append(errs, fmt.Sprintf(
				"entity %q: pacts are not supported yet on a type that declares faces", typeName))
			continue
		}
		if _, ok := def.Properties[PactBodyField]; ok {
			errs = append(errs, fmt.Sprintf(
				"entity %q: cannot declare pacts: it has a property named %q, which is ambiguous "+
					"with the reserved pact field for the markdown body", typeName, PactBodyField))
			continue
		}
		systems := make([]string, 0, len(def.Pacts))
		for system := range def.Pacts {
			systems = append(systems, system)
		}
		sort.Strings(systems)
		for _, system := range systems {
			prefix := fmt.Sprintf("entity %q: pact %q", typeName, system)
			errs = append(errs, validatePact(prefix, system, def.Pacts[system], def)...)
		}
	}
	return errs
}

// validatePact checks one pact of def; every error is prefixed with prefix.
func validatePact(prefix, system string, pact PactDef, def EntityDef) []string {
	v := &pactValidator{prefix: prefix, def: def, fieldNames: pactFieldNames(def), placed: map[string]string{}}
	if !pactSystemPattern.MatchString(system) {
		v.errs = append(v.errs, fmt.Sprintf(
			"%s: invalid system id (want %s)", prefix, pactSystemPattern.String()))
	}
	if msg := checkPactScope(pact.Scope); msg != "" {
		v.errs = append(v.errs, prefix+": "+msg)
	}
	allTheirs := slices.Contains(pact.Theirs, PactAllFields)
	v.checkOwned(pact, allTheirs)
	v.checkPropose(pact.Propose, allTheirs)
	return v.errs
}

// pactValidator accumulates the errors of one pact, in the order the lists
// are checked: theirs, shared, then propose.
type pactValidator struct {
	prefix     string
	def        EntityDef
	fieldNames []string
	placed     map[string]string // field → the list it was placed in (theirs/shared)
	errs       []string
}

// checkField reports whether name is a field list may name: the body, or a
// declared property that rela does not compute.
func (v *pactValidator) checkField(list, name string) bool {
	if name == PactBodyField {
		return true
	}
	prop, ok := v.def.Properties[name]
	if !ok {
		v.errs = append(v.errs, fmt.Sprintf("%s: %s names unknown field %q%s",
			v.prefix, list, name, didYouMean(name, v.fieldNames)))
		return false
	}
	if prop.Computed != "" {
		v.errs = append(v.errs, fmt.Sprintf(
			"%s: %s names computed property %q — rela derives it, so no other side can edit it",
			v.prefix, list, name))
		return false
	}
	return true
}

// place records that list owns name, refusing a field placed twice.
func (v *pactValidator) place(list, name string) {
	if _, dup := v.placed[name]; dup {
		v.errs = append(v.errs, fmt.Sprintf(
			"%s: field %q appears more than once across theirs and shared — a field has one owner",
			v.prefix, name))
		return
	}
	v.placed[name] = list
}

// checkOwned checks theirs and shared, placing every valid field.
func (v *pactValidator) checkOwned(pact PactDef, allTheirs bool) {
	for _, name := range pact.Theirs {
		if name == PactAllFields {
			if len(pact.Theirs) > 1 {
				v.errs = append(v.errs, v.prefix+`: theirs mixes "*" with named fields — "*" must stand alone`)
			}
			continue
		}
		if v.checkField("theirs", name) {
			v.place("theirs", name)
		}
	}
	if allTheirs && len(pact.Shared) > 0 {
		v.errs = append(v.errs,
			v.prefix+`: shared must be empty when theirs is ["*"] — every field is already theirs`)
	}
	for _, name := range pact.Shared {
		if name == PactAllFields {
			v.errs = append(v.errs, v.prefix+`: "*" is only allowed in theirs, not in shared`)
			continue
		}
		if v.checkField("shared", name) {
			v.place("shared", name)
		}
	}
}

// checkPropose checks propose against what checkOwned placed.
//
// Propose is a subset of OURS: it tells the agent that an external edit of
// one of rela's fields is a proposal rather than something to revert. A
// theirs field is written by the external side anyway, and a shared field
// has no single owner, so neither has anything to propose to.
func (v *pactValidator) checkPropose(propose []string, allTheirs bool) {
	proposed := map[string]bool{}
	for _, name := range propose {
		if name == PactAllFields {
			v.errs = append(v.errs,
				v.prefix+`: "*" is only allowed in theirs, not in propose — propose lists ours fields by name`)
			continue
		}
		if !v.checkField("propose", name) {
			continue
		}
		if proposed[name] {
			v.errs = append(v.errs, fmt.Sprintf("%s: propose lists %q more than once", v.prefix, name))
			continue
		}
		proposed[name] = true
		switch {
		case allTheirs:
			v.errs = append(v.errs, fmt.Sprintf(
				`%s: propose names %q, which theirs ["*"] hands to the external side — `+
					"propose applies only to ours fields", v.prefix, name))
		case v.placed[name] != "":
			v.errs = append(v.errs, fmt.Sprintf(
				"%s: propose names %s field %q — propose applies only to ours fields",
				v.prefix, v.placed[name], name))
		}
	}
}

// checkPactScope returns a problem with scope, or "" when it is an absolute
// http(s) URL.
func checkPactScope(scope string) string {
	if strings.TrimSpace(scope) == "" {
		return "scope is required (the http(s) URL of the external container)"
	}
	u, err := url.Parse(scope)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Sprintf("scope %q is not an http(s) URL", scope)
	}
	return ""
}

// pactFieldNames returns the names a pact of def may use, sorted: every
// declared property plus the reserved body field.
func pactFieldNames(def EntityDef) []string {
	names := make([]string, 0, len(def.Properties)+1)
	for name := range def.Properties {
		names = append(names, name)
	}
	names = append(names, PactBodyField)
	sort.Strings(names)
	return names
}

// didYouMean returns a " (did you mean ...?)" suffix naming the first of the
// sorted candidates close to name, or "" when none is.
//
// Close means [didYouMeanEntity]'s substring test in either direction (a
// plural, a truncation) or, for names longer than three characters, an edit
// distance of at most two (a transposition such as "shraed"). Shorter names
// are within two edits of almost anything, so the hint would be noise.
func didYouMean(name string, candidates []string) string {
	if name == "" {
		return ""
	}
	lower := strings.ToLower(name)
	for _, candidate := range candidates {
		lc := strings.ToLower(candidate)
		similar := strings.Contains(lc, lower) || strings.Contains(lower, lc) ||
			(len(lower) > 3 && editDistance(lower, lc) <= 2)
		if similar {
			return fmt.Sprintf(" (did you mean %q?)", candidate)
		}
	}
	return ""
}

// editDistance is the Levenshtein distance between a and b, over bytes —
// schema names are ASCII.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
