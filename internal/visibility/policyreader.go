package visibility

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// PolicyReader is the policy-enforcing [Reader]: row-gate first, then
// field-redact a copy. Semantics are hoisted from dataentry's
// visibleReader/copyVisibleProperties (TKT-N26KLB) with one deliberate
// strengthening: the stored-type check lives here, in the package, never
// in consumers (RR-SRZK6X).
type PolicyReader struct {
	gate   RowGate
	redact FieldRedactor
	get    EntityGetter
}

// NewPolicyReader builds a PolicyReader. All collaborators are required
// (constructors-reject-nil rule).
func NewPolicyReader(gate RowGate, redact FieldRedactor, get EntityGetter) (*PolicyReader, error) {
	if gate == nil {
		return nil, errors.New("visibility: NewPolicyReader: gate must be non-nil")
	}
	if redact == nil {
		return nil, errors.New("visibility: NewPolicyReader: redact must be non-nil")
	}
	if get == nil {
		return nil, errors.New("visibility: NewPolicyReader: get must be non-nil")
	}
	return &PolicyReader{gate: gate, redact: redact, get: get}, nil
}

// Get implements [Reader]. Gate BEFORE load (hidden == missing, RR-NGMI),
// then verify the stored type matches the caller's claim (RR-SRZK6X),
// then redact a copy.
func (r *PolicyReader) Get(ctx context.Context, entityType, addr string) (*entity.Entity, bool, error) {
	id, face := parseAddress(addr)
	ok, err := r.gate.PermitsRead(ctx, entityType, id)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	e, gerr := r.get.GetEntityState(ctx, id, face)
	if gerr != nil {
		// Store miss == not-found; indistinguishable from a deny by design.
		return nil, false, nil //nolint:nilerr // store miss == not-found, by design
	}
	if e.Type != entityType {
		// The gate authorized the CLAIMED type; acting on a different
		// stored type would be the BUG-ZWTDH9 escalation. Same
		// indistinguishable miss.
		return nil, false, nil
	}
	// The face gate, necessarily AFTER the load: an entity's face is not known
	// until the row exists. Same indistinguishable miss, so a denied face
	// cannot be used to discover which faces exist.
	if !FaceAllowed(ctx, r.gate, entityType, e.Face) {
		return nil, false, nil
	}
	return r.redacted(ctx, e), true, nil
}

// Filter implements [Reader]: batched row-gate per type (one
// PermitsReadMany per distinct type, RR-FRK1 shape), fail-closed
// type-drop on gate error, then redaction of every survivor. Order is
// preserved and a fresh slice returned; nil for empty input.
func (r *PolicyReader) Filter(ctx context.Context, candidates []*entity.Entity) []*entity.Entity {
	if len(candidates) == 0 {
		return nil
	}
	byType := make(map[string][]string)
	for _, c := range candidates {
		if c == nil {
			continue // fail-closed: a nil candidate must not panic the filter
		}
		byType[c.Type] = append(byType[c.Type], c.ID)
	}
	allowed := r.permittedIDs(ctx, byType)

	out := make([]*entity.Entity, 0, len(candidates))
	for _, c := range candidates {
		if c != nil && allowed[c.ID] && FaceAllowed(ctx, r.gate, c.Type, c.Face) {
			out = append(out, c)
		}
	}
	ctx = PrimeTraversals(ctx, r.redact, out)
	for i, c := range out {
		out[i] = r.redacted(ctx, c)
	}
	return out
}

// FilterHeaders implements [HeaderFilterer]: the [PolicyReader.Filter] contract applied
// to content-free headers.
//
// Identical gating — one PermitsReadMany per distinct type, order preserved,
// fresh slice, fail-closed on gate error — because it is the SAME policy on
// the same (id, type) pairs. The row gate never consults an entity's body,
// so dropping the body cannot change a verdict.
//
// Field redaction still runs per surviving row: "may read every row of this
// type" is not "may see every property" (RR-OXE47R). Redacting a header
// strips hidden property names exactly as it does for an entity, and records
// them in Redacted, so a gated header read is never MORE revealing than a
// gated entity read.
func (r *PolicyReader) FilterHeaders(
	ctx context.Context, candidates []store.EntityHeader,
) []store.EntityHeader {
	if len(candidates) == 0 {
		return nil
	}
	byType := make(map[string][]string)
	for _, c := range candidates {
		byType[c.Type] = append(byType[c.Type], c.ID)
	}
	allowed := r.permittedIDs(ctx, byType)

	out := make([]store.EntityHeader, 0, len(candidates))
	probes := make([]*entity.Entity, 0, len(candidates))
	for _, c := range candidates {
		if allowed[c.ID] && FaceAllowed(ctx, r.gate, c.Type, c.Face) {
			out = append(out, c)
			probes = append(probes, headerProbe(c))
		}
	}
	ctx = PrimeTraversals(ctx, r.redact, probes)
	for i, c := range out {
		out[i] = RedactHeader(ctx, r.redact, c)
	}
	return out
}

// FilterRelations implements [Reader]: a relation survives only when BOTH
// endpoints are visible (FROM ∧ TO). Endpoint types are resolved with one
// load per distinct endpoint id; visibility with one PermitsReadMany per
// distinct type. A missing endpoint or a gate error hides fail-closed.
func (r *PolicyReader) FilterRelations(ctx context.Context, rels []*entity.Relation) []*entity.Relation {
	if len(rels) == 0 {
		return nil
	}
	byType := make(map[string][]string)
	seen := make(map[string]bool)
	for _, rel := range rels {
		if rel == nil {
			continue // fail-closed: a nil relation must not panic the filter
		}
		for _, id := range [2]string{rel.From, rel.To} {
			if seen[id] {
				continue
			}
			seen[id] = true
			e, err := r.get.GetEntityState(ctx, id, "")
			if err != nil {
				continue // missing endpoint: stays out of allowed → relation hidden
			}
			byType[e.Type] = append(byType[e.Type], id)
		}
	}
	allowed := r.permittedIDs(ctx, byType)

	out := make([]*entity.Relation, 0, len(rels))
	for _, rel := range rels {
		if rel != nil && allowed[rel.From] && allowed[rel.To] {
			out = append(out, rel)
		}
	}
	return out
}

// permittedIDs runs one PermitsReadMany per distinct type and returns the
// union allowed-id set. A gate error drops that whole type fail-closed —
// a read-ACL failure must never widen visibility — and is logged loud so
// operators see the cause rather than silently thinner results.
func (r *PolicyReader) permittedIDs(ctx context.Context, byType map[string][]string) map[string]bool {
	allowed := make(map[string]bool)
	for typeName, ids := range byType {
		perm, err := r.gate.PermitsReadMany(ctx, typeName, ids)
		if err != nil {
			slog.Warn("visibility: PermitsReadMany failed; dropping type fail-closed",
				"type", typeName, "candidates", len(ids), "err", err)
			continue
		}
		for id, ok := range perm {
			if ok {
				allowed[id] = true
			}
		}
	}
	return allowed
}

// redacted returns e with hidden properties stripped. When nothing is
// hidden the ORIGINAL face is returned (read-out contract: callers
// must not mutate) — this keeps the no-policy path allocation-free and
// byte-identical to a raw read. When redaction applies, the struct is
// shallow-copied with a fresh filtered Properties map; property VALUES
// still alias the originals, which is safe because read-out paths
// serialize before anything can alias (the copyVisibleProperties
// contract, TKT-IHC7D).
//
// No separate title fallback is needed here: unlike the wire DTO (whose
// precomputed _title stripHiddenProperties must rewrite), an
// entity.Entity carries no secondary title channel — DisplayTitle
// derivations recompute from Properties and fall back to the ID once a
// hidden display property is stripped.
//
// TODO(body-redaction): Content and Inaccessible pass through verbatim.
// Today that is correct — the `visible:` policy universe is
// metamodel-declared properties, so a body can't be policy-hidden — but
// if body-level redaction ever becomes policy-expressible
// (entity.InaccessibleFieldContent exists as the reserved marker), this
// is the spot that must learn about it, or the seam silently leaks
// bodies (RR-J6022V).
func (r *PolicyReader) redacted(ctx context.Context, e *entity.Entity) *entity.Entity {
	return Redact(ctx, r.redact, e)
}

// Redact returns e with the properties hidden from the ctx principal
// stripped, per red. When nothing is hidden the ORIGINAL face is
// returned (read-only contract); otherwise a shallow struct copy with a
// fresh filtered Properties map (the redacted() contract above — see its
// godoc for the copy semantics and the body-redaction TODO).
//
// Exported for consumers that hold an already-ROW-GATED, already-loaded
// entity where a type-claimed [Reader.Get] doesn't fit — e.g. redacting a
// visible neighbor before deriving its display title (the RR-5N4K35
// title-leak class). Redact performs NO row-gate of its own: callers own
// that decision.
//
// PRECONDITION: e must be a raw store entity, never the output of a prior
// Redact. On the nothing-hidden path the input is returned untouched, so a
// stale [entity.Entity.Redacted] from an earlier pass would survive and
// misreport as this redactor's verdict. Clearing it unconditionally would
// cost the allocation-free identity guarantee above, so the contract is the
// caller's to keep. No production caller stacks readers today (RR-Q1VCKR).
func Redact(ctx context.Context, red FieldRedactor, e *entity.Entity) *entity.Entity {
	if e == nil {
		return nil
	}
	hidden := red.HiddenProperties(ctx, e)
	if len(hidden) == 0 {
		return e
	}
	out := *e
	out.Properties = filterProps(e.Properties, hidden)
	// Record WHICH properties were withheld so a consumer can render
	// "[redacted]" instead of a blank that reads as "never set"
	// (TKT-FJ6END). Names only — the values stay stripped above.
	//
	// Freshly allocated and sorted, never appended to e.Redacted: the
	// shallow copy above aliases the original's slice header, so growing
	// it in place could write into the caller's backing array.
	out.Redacted = slices.Sorted(maps.Keys(hidden))
	return &out
}

// RedactHeader is [Redact] for a content-free [store.EntityHeader].
//
// Verdicts are resolved from the entity TYPE and the ctx principal — the
// production redactors read e.Type and never e.Content (affordances'
// FieldVerdicts resolves against the metamodel's declared fields) — so a
// header yields the same hidden set its entity would. The stand-in Entity
// below exists only to satisfy the [FieldRedactor] signature.
//
// Mirrors Redact's copy semantics: nothing hidden returns the input
// unchanged (no allocation on the no-policy path); otherwise Properties is
// a fresh filtered map and Redacted a freshly sorted slice, never appended
// to the input's (which may alias a caller's backing array).
func RedactHeader(ctx context.Context, red FieldRedactor, h store.EntityHeader) store.EntityHeader {
	hidden := red.HiddenProperties(ctx, headerProbe(h))
	if len(hidden) == 0 {
		return h
	}
	out := h
	out.Properties = filterProps(h.Properties, hidden)
	out.Redacted = slices.Sorted(maps.Keys(hidden))
	return out
}

// headerProbe is the entity a redactor sees for a header. It carries the face:
// a `when:` that cannot be answered on a named face must see that it is one.
func headerProbe(h store.EntityHeader) *entity.Entity {
	return &entity.Entity{ID: h.ID, Type: h.Type, Face: h.Face, Properties: h.Properties}
}

// filterProps returns a fresh map of props minus hidden. Never mutates
// props (which may alias live store state).
func filterProps(props map[string]any, hidden map[string]struct{}) map[string]any {
	out := make(map[string]any, len(props))
	for k, v := range props {
		if _, h := hidden[k]; h {
			continue
		}
		out[k] = v
	}
	return out
}
