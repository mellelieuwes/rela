package visibility

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/tracer"
)

// VisibleTracer is the visibility decorator over a pure [tracer.Tracer]
// (DEC-ZBI39P): it filters the BASE tracer's results post-hoc — the base
// stays ACL-free (the TKT-QU7REX precedent: whole-graph scans ungated,
// visibility applied to the results) — with hidden = nonexistent
// semantics:
//
//   - a hidden trace node is pruned WITH its entire subtree (if the node
//     doesn't exist for you, nothing reached through it does either);
//   - a path through a hidden intermediate is withheld, indistinguishable
//     from no-path;
//   - hidden orphans are dropped;
//   - HasCycle on a hidden start behaves as on a nonexistent start.
//
// Surviving nodes are still field-redacted: a visible entity can carry
// hidden properties, and TraceResult.Properties / .Title are built from
// the raw entity. Redaction always builds FRESH property maps — the base
// tracer's maps alias live store state (RR-6IL3X7) — and applies the ID
// title-fallback when the node's title property is hidden (RR-5N4K35).
type VisibleTracer struct {
	base   tracer.Tracer
	gate   RowGate
	redact FieldRedactor
	get    EntityGetter
}

// NewVisibleTracer builds the decorator. All collaborators are required.
func NewVisibleTracer(
	base tracer.Tracer, gate RowGate, redact FieldRedactor, get EntityGetter,
) (*VisibleTracer, error) {
	if base == nil {
		return nil, errors.New("visibility: NewVisibleTracer: base must be non-nil")
	}
	if gate == nil {
		return nil, errors.New("visibility: NewVisibleTracer: gate must be non-nil")
	}
	if redact == nil {
		return nil, errors.New("visibility: NewVisibleTracer: redact must be non-nil")
	}
	if get == nil {
		return nil, errors.New("visibility: NewVisibleTracer: get must be non-nil")
	}
	return &VisibleTracer{base: base, gate: gate, redact: redact, get: get}, nil
}

// TraceFrom implements [tracer.Tracer].
func (t *VisibleTracer) TraceFrom(ctx context.Context, id string, maxDepth int) *tracer.TraceResult {
	return t.filterTree(ctx, t.base.TraceFrom(ctx, id, maxDepth))
}

// TraceTo implements [tracer.Tracer].
func (t *VisibleTracer) TraceTo(ctx context.Context, id string, maxDepth int) *tracer.TraceResult {
	return t.filterTree(ctx, t.base.TraceTo(ctx, id, maxDepth))
}

// filterTree gates every node of the returned tree with ONE
// PermitsReadMany per distinct type, then rebuilds the tree pruning
// hidden nodes (and their subtrees) and redacting the survivors. A nil
// or hidden root yields nil — the same shape the base tracer returns for
// an unknown id.
func (t *VisibleTracer) filterTree(ctx context.Context, root *tracer.TraceResult) *tracer.TraceResult {
	if root == nil {
		return nil
	}
	byType := map[string][]string{}
	collectNodeIDs(root, byType, map[string]bool{})
	visible := t.permittedIDs(ctx, byType)
	return t.rebuild(ctx, root, visible)
}

// collectNodeIDs walks the tree gathering distinct node ids grouped by
// entity type (tree nodes carry Type), for the batched gate probe.
func collectNodeIDs(n *tracer.TraceResult, byType map[string][]string, seen map[string]bool) {
	if n == nil {
		return
	}
	if !seen[n.ID] {
		seen[n.ID] = true
		byType[n.Type] = append(byType[n.Type], n.ID)
	}
	for _, c := range n.Children {
		collectNodeIDs(c, byType, seen)
	}
}

// rebuild returns a filtered COPY of the tree: hidden nodes prune their
// whole subtree; surviving nodes are redacted onto fresh structs (the
// base tree's Properties maps alias live store state and are never
// mutated).
func (t *VisibleTracer) rebuild(
	ctx context.Context, n *tracer.TraceResult, visible map[string]bool,
) *tracer.TraceResult {
	if n == nil || !visible[n.ID] {
		return nil
	}
	out := *n
	out.Children = nil
	t.redactNode(ctx, &out)
	for _, c := range n.Children {
		if fc := t.rebuild(ctx, c, visible); fc != nil {
			out.Children = append(out.Children, fc)
		}
	}
	return &out
}

// redactNode strips hidden properties from one (visible) node onto a
// fresh map and applies the ID title-fallback when the title property is
// hidden. Verdicts are computed against a synthetic entity built from
// the node's own fields — the tree carries the full raw property map, so
// `when:` predicates see the same values a store load would provide.
func (t *VisibleTracer) redactNode(ctx context.Context, n *tracer.TraceResult) {
	synth := &entity.Entity{ID: n.ID, Type: n.Type, Properties: n.Properties}
	hidden := t.redact.HiddenProperties(ctx, synth)
	if len(hidden) == 0 {
		return
	}
	n.Properties = filterProps(n.Properties, hidden)
	if _, h := hidden["title"]; h {
		// TraceResult.Title is the literal `title` property baked in at
		// build time — the secondary channel redaction must also close.
		n.Title = n.ID
	}
}

// FindPath implements [tracer.Tracer]. Any hidden step withholds the
// WHOLE path — revealing "a path exists through something you cannot
// see" is itself a leak — returning nil exactly like the base's no-path
// result. (Withholding takes marginally longer than a genuine no-path
// BFS miss; accepted residual, impractical to exploit in-memory.)
func (t *VisibleTracer) FindPath(ctx context.Context, fromID, toID string) []tracer.PathStep {
	steps := t.base.FindPath(ctx, fromID, toID)
	if len(steps) == 0 {
		return nil
	}
	byType := map[string][]string{}
	seen := map[string]bool{}
	for _, s := range steps {
		if !seen[s.ID] {
			seen[s.ID] = true
			byType[s.Type] = append(byType[s.Type], s.ID)
		}
	}
	visible := t.permittedIDs(ctx, byType)
	for _, s := range steps {
		if !visible[s.ID] {
			return nil
		}
	}

	out := make([]tracer.PathStep, len(steps))
	copy(out, steps)
	for i := range out {
		if !t.redactStepTitle(ctx, &out[i]) {
			return nil
		}
	}
	return out
}

// redactStepTitle applies the ID title-fallback to one path step. Steps
// carry no property map, so the entity is loaded to evaluate the field
// verdict against real values (a synthetic entity without properties
// could flip a `when:` predicate open). Returns false — withhold the
// path — when the entity cannot be loaded (fail-closed).
func (t *VisibleTracer) redactStepTitle(ctx context.Context, s *tracer.PathStep) bool {
	e, err := t.get.GetEntityState(ctx, s.ID, "")
	if err != nil {
		return false
	}
	hidden := t.redact.HiddenProperties(ctx, e)
	if _, h := hidden["title"]; h {
		s.Title = s.ID
	}
	return true
}

// FindOrphans implements [tracer.Tracer]: the base's orphan ids minus
// the hidden ones. Each id's TYPE is resolved (the gate needs it) via
// VisibleTracer.typesOf, then ids are gated with one PermitsReadMany
// per distinct type (RR-MYLUSZ). A vanished entity drops fail-closed.
func (t *VisibleTracer) FindOrphans(ctx context.Context) ([]string, error) {
	ids, err := t.base.FindOrphans(ctx)
	if err != nil {
		return nil, err
	}
	byType := t.typesOf(ctx, ids)
	visible := t.permittedIDs(ctx, byType)

	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if visible[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

// typesOf resolves each id's entity type, grouped for a per-type gate
// probe.
//
// Prefers a batched header read: resolving 20k orphan ids one GetEntity at
// a time loaded 20k full entities — bodies included — to read one string
// field from each, which on a content-heavy project cost more than the
// orphan scan itself (TKT-1ESTYJ). Headers carry Type and no body.
//
// Falls back to per-id GetEntity when the getter cannot list headers,
// preserving the original behavior exactly. Both paths DROP an id whose
// entity cannot be resolved, so a vanished entity stays fail-closed: it
// never reaches byType, so permittedIDs never marks it visible.
func (t *VisibleTracer) typesOf(ctx context.Context, ids []string) map[string][]string {
	byType := map[string][]string{}
	if len(ids) == 0 {
		return byType
	}

	if er, ok := t.get.(store.EntityReader); ok {
		for h, err := range store.ListEntityHeaders(ctx, er, store.EntityQuery{IDs: ids}) {
			if err != nil {
				// Fail closed: a partial scan must not silently narrow the
				// orphan set to "whatever we managed to read". Fall back to
				// the per-id path, which drops only the ids it cannot load.
				return t.typesOfPerID(ctx, ids)
			}
			byType[h.Type] = append(byType[h.Type], h.ID)
		}
		return byType
	}
	return t.typesOfPerID(ctx, ids)
}

func (t *VisibleTracer) typesOfPerID(ctx context.Context, ids []string) map[string][]string {
	byType := map[string][]string{}
	for _, id := range ids {
		e, gerr := t.get.GetEntityState(ctx, id, "")
		if gerr != nil {
			continue
		}
		byType[e.Type] = append(byType[e.Type], id)
	}
	return byType
}

// HasCycle implements [tracer.Tracer]. A hidden (or missing, or
// gate-erroring) start returns false — the same result the base returns
// for a nonexistent start, so the bool is not an existence oracle for
// the start node. Note the documented residual: a VISIBLE start still
// reports a cycle whose loop passes through hidden nodes.
func (t *VisibleTracer) HasCycle(ctx context.Context, startID string) bool {
	e, err := t.get.GetEntityState(ctx, startID, "")
	if err != nil {
		return false
	}
	ok, gerr := t.gate.PermitsRead(ctx, e.Type, startID)
	if gerr != nil {
		slog.Warn("visibility: HasCycle gate failed; answering false fail-closed",
			"type", e.Type, "err", gerr)
		return false
	}
	if !ok {
		return false
	}
	return t.base.HasCycle(ctx, startID)
}

// permittedIDs mirrors PolicyReader.permittedIDs for the decorator's own
// gate probes: one PermitsReadMany per distinct type, fail-closed
// type-drop on error, loud log.
func (t *VisibleTracer) permittedIDs(ctx context.Context, byType map[string][]string) map[string]bool {
	allowed := make(map[string]bool)
	for typeName, ids := range byType {
		// The base tracer reads every node's DEFAULT face (store.GetEntity),
		// so a `type@face` grant that excludes the default face hides the
		// whole type here — the row gate alone would surface draft titles
		// and properties to a published-only principal.
		if !FaceAllowed(ctx, t.gate, typeName, "") {
			continue
		}
		perm, err := t.gate.PermitsReadMany(ctx, typeName, ids)
		if err != nil {
			slog.Warn("visibility: tracer PermitsReadMany failed; dropping type fail-closed",
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
