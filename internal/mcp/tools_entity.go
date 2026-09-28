// coverage-ignore: MCP tool handlers - tested via integration tests
package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/natsort"
	"github.com/Sourcehaven-BV/rela/internal/predicate"
	"github.com/Sourcehaven-BV/rela/internal/predicatefns"
	"github.com/Sourcehaven-BV/rela/internal/relresolve"
	"github.com/Sourcehaven-BV/rela/internal/search"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

func (s *Server) handleListEntities(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	snap := s.state.current()
	args := newToolRequest(request)
	typeArg := args.GetString("type", "")
	filterExpr := strings.TrimSpace(args.GetString("filter", ""))
	limit := limitArg(args, defaultListLimit)
	offset := args.GetInt("offset", 0)

	d := snap.deps
	types := snap.handlers.types
	q := store.EntityQuery{}
	if typeArg != "" {
		resolved, _, err := types.resolveEntityType(typeArg)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		q.Type = resolved
	}

	var prog *predicate.Program
	if filterExpr != "" {
		if q.Type == "" {
			return errorResult("filter requires type"), nil
		}
		compiled, err := compileListFilter(d.Meta, q.Type, filterExpr, d.Traversals != nil)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		prog = compiled
	}

	entities := make([]*entity.Entity, 0)
	for e, err := range d.Store.ListEntities(ctx, q) {
		if err != nil {
			return errorResult(err.Error()), nil
		}
		entities = append(entities, e)
	}
	if prog != nil {
		filtered, err := filterEntities(ctx, d, prog, q.Type, entities)
		if err != nil {
			return errorResult("filter error: " + err.Error()), nil
		}
		entities = filtered
	}

	sortStoreEntitiesByID(entities)

	total := len(entities)
	entities = applyPagination(entities, offset, limit)
	summaries := make([]entitySummary, len(entities))
	for i, e := range entities {
		summaries[i] = convertStoreEntitySummary(d.Meta, e)
	}
	text, err := marshalJSON(struct {
		Total    int             `json:"total"`
		HasMore  bool            `json:"has_more"`
		Entities []entitySummary `json:"entities"`
	}{total, max(offset, 0)+len(summaries) < total, summaries})
	if err != nil { // coverage-ignore: defensive: summaries are string-only DTOs; json.Marshal cannot fail.
		return errorResult(err.Error()), nil
	}
	return textResult(text), nil
}

// compileListFilter compiles a list_entities filter expression against
// entityType. A related(...) traversal is refused when the wiring supplied no
// [TraversalBinder]: answering it ungated would reveal hidden edges.
func compileListFilter(
	meta *metamodel.Metamodel, entityType, expr string, canTraverse bool,
) (*predicate.Program, error) {
	prog, err := predicatefns.NewEvaluator(meta).Compile(entityType, expr)
	if err != nil {
		return nil, fmt.Errorf("invalid filter: %w", err)
	}
	if !canTraverse && len(relresolve.Specs(prog)) > 0 {
		return nil, errors.New("invalid filter: related(...) is not supported here; " +
			"use trace or list_relations instead")
	}
	// Compile accepts any constraint name; without this check a constraint on
	// a property the far type lacks matches nothing instead of failing.
	if err := predicatefns.ValidateTraversals(meta, entityType, prog); err != nil {
		return nil, fmt.Errorf("invalid filter: %w", err)
	}
	return prog, nil
}

// filterEntities keeps the entities that match prog. Traversals are answered
// once for the whole candidate set, not per row.
func filterEntities(
	ctx context.Context, d Deps, prog *predicate.Program, entityType string, entities []*entity.Entity,
) ([]*entity.Entity, error) {
	traversalFor := func(string) predicate.TraversalFunc { return nil }
	if d.Traversals != nil {
		ids := make([]string, len(entities))
		for i, e := range entities {
			ids[i] = e.ID
		}
		bound, err := d.Traversals.Bind(ctx, entityType, ids, prog)
		if err != nil {
			return nil, err
		}
		traversalFor = bound
	}

	ev := predicatefns.NewEvaluator(d.Meta)
	out := make([]*entity.Entity, 0, len(entities))
	for _, e := range entities {
		ok, err := ev.MatchesWithTraversals(ctx, prog, e.Type, e.ID, e.Properties, traversalFor(e.ID))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.ID, err)
		}
		if ok {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *Server) handleShowEntity(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	snap := s.state.current()
	args := newToolRequest(request)
	id, err := args.RequireString("id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	id = trimID(id)

	d := snap.deps
	e, getErr := d.Store.GetEntity(ctx, id)
	if getErr != nil {
		return errorResult("entity not found: " + id), nil
	}

	view := entityView{relations: true, content: args.GetBool("content", true)}
	text, err := convertStoreEntity(ctx, e, d.Store, d.Meta, view)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	return textResult(text), nil
}

func (s *Server) handleSearchEntities(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	snap := s.state.current()
	args := newToolRequest(request)
	query, err := args.RequireString("query")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	entityType := args.GetString("type", "")
	limit := limitArg(args, defaultSearchLimit)

	q := search.Query{Text: query, Limit: limit}
	if entityType != "" {
		resolved, _, resolveErr := snap.handlers.types.resolveEntityType(entityType)
		if resolveErr != nil {
			return errorResult(resolveErr.Error()), nil
		}
		q.Types = []string{resolved}
	}

	d := snap.deps
	var hits []search.Hit
	for hit, searchErr := range d.Searcher.Search(ctx, q) {
		if searchErr != nil {
			return errorResult(fmt.Sprintf("search failed: %v", searchErr)), nil
		}
		hits = append(hits, hit)
	}
	summaries, err := hydrateHits(ctx, d.Store, d.Meta, hits)
	if err != nil {
		return errorResult(fmt.Sprintf("search failed: %v", err)), nil
	}

	text, err := marshalJSON(summaries)
	if err != nil { // coverage-ignore: defensive: summaries are string-only DTOs; json.Marshal cannot fail.
		return errorResult(err.Error()), nil
	}
	return textResult(text), nil
}

// hitKey identifies one face of one entity.
type hitKey struct {
	id   string
	face entity.Face
}

// hydrateHits builds the search summaries from the store, in hit order. The
// hits are read in ONE query over every face, because a faced type has no
// default row and a per-hit GetEntity would miss it. A hit the store does not
// return is dropped. The title comes from the stored entity, which the gated
// store has redacted, and not from the index, which holds the raw value.
func hydrateHits(
	ctx context.Context, st GraphReader, meta *metamodel.Metamodel, hits []search.Hit,
) ([]entitySummary, error) {
	summaries := make([]entitySummary, 0, len(hits))
	if len(hits) == 0 {
		return summaries, nil
	}
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.ID)
	}
	found := make(map[hitKey]*entity.Entity, len(hits))
	for e, err := range st.ListEntities(ctx, store.EntityQuery{IDs: ids, AllStates: true}) {
		if err != nil {
			return nil, err
		}
		if e != nil {
			found[hitKey{e.ID, e.Face}] = e
		}
	}
	for _, h := range hits {
		e, ok := found[hitKey{h.ID, h.Face}]
		if !ok {
			continue
		}
		summaries = append(summaries, convertStoreEntitySummary(meta, e))
	}
	return summaries, nil
}

func (s *Server) handleCreateEntity(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	snap := s.state.current()
	args := newToolRequest(request)
	typeName, err := args.RequireString("type")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	content := args.GetString("content", "")
	customID := args.GetString("id", "")

	// Resolve type
	resolvedType, _, resolveErr := snap.handlers.types.resolveEntityType(typeName)
	if resolveErr != nil {
		return errorResult(resolveErr.Error()), nil
	}

	// Parse properties from the request
	properties := extractProperties(request)

	// Validate property names early for better error messages
	if errResult := snap.handlers.types.validatePropertyNames(resolvedType, properties); errResult != nil {
		return errResult, nil
	}

	result, createErr := snap.deps.EntityManager.CreateEntity(ctx,
		&entity.Entity{
			Type:       resolvedType,
			Properties: properties,
			Content:    content,
		},
		entity.CreateOptions{ID: customID},
	)
	if createErr != nil {
		return errorResult(createErr.Error()), nil
	}
	created := result.Entity

	d := snap.deps
	e, _ := d.Store.GetEntity(ctx, created.ID)
	if e == nil {
		// Fallback: return minimal info
		return textResult(fmt.Sprintf("Created %s %s", resolvedType, created.ID)), nil
	}

	// The caller just wrote the body; echoing it back only costs context.
	text, err := convertStoreEntity(ctx, e, d.Store, d.Meta, entityView{})
	if err != nil {
		return errorResult(err.Error()), nil
	}
	body := fmt.Sprintf("Created %s %s\n\n%s", resolvedType, created.ID, text)
	return textResult(prefixWarnings(result.Warnings) + body), nil
}

func (s *Server) handleUpdateEntity(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	snap := s.state.current()
	args := newToolRequest(request)
	id, err := args.RequireString("id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	id = trimID(id)

	d := snap.deps
	st := d.Store
	e, getErr := st.GetEntity(ctx, id)
	if getErr != nil {
		return errorResult("entity not found: " + id), nil
	}

	properties := extractPropertiesAllowNil(request)
	content := args.GetString("content", "")

	if len(properties) == 0 && content == "" {
		return errorResult("no updates specified"), nil
	}

	// Validate property names early for better error messages
	if errResult := snap.handlers.types.validatePropertyNames(e.Type, properties); errResult != nil {
		return errResult, nil
	}

	// Translate the wire shape into a targeted patch: MCP's in-band nil
	// sentinel means "delete", anything else upserts. Everything not named
	// is preserved by PatchEntity, so this tool can no longer clobber a
	// property it did not mention (TKT-80EWGM).
	patch := entity.Patch{Properties: make(map[string]any, len(properties))}
	for k, v := range properties {
		if v == nil {
			patch.MetaUnset = append(patch.MetaUnset, k)
			continue
		}
		patch.Properties[k] = v
	}
	// Deletion order does not affect the result, but a stable slice keeps
	// audit summaries and test expectations deterministic.
	slices.Sort(patch.MetaUnset)
	// Deliberately NOT a pointer-to-"": MCP has no sentinel for "clear the
	// body", and inventing one here would silently change the tool's
	// contract. An empty content string means "leave the body alone", as
	// the `content == ""` guard above already implies.
	if content != "" {
		patch.Content = &content
	}

	updateResult, updateErr := snap.deps.EntityManager.PatchEntity(ctx, id, patch)
	if updateErr != nil {
		return errorResult(updateErr.Error()), nil
	}

	updated, _ := st.GetEntity(ctx, id)
	if updated == nil {
		return textResult(prefixWarnings(updateResult.Warnings) + "Updated " + id), nil
	}

	// Relations are unchanged by an update and the body was just written by
	// the caller, so only the properties are echoed back.
	text, convertErr := convertStoreEntity(ctx, updated, st, d.Meta, entityView{})
	if convertErr != nil {
		return errorResult(convertErr.Error()), nil
	}
	body := fmt.Sprintf("Updated %s\n\n%s", id, text)
	return textResult(prefixWarnings(updateResult.Warnings) + body), nil
}

// prefixWarnings formats DEC-HWZHA soft-validation warnings as a
// leading section in MCP tool result text. Returns the empty string
// when there are no warnings (caller concatenates unconditionally).
//
// Format:
//
//	WARNINGS (n):
//	  <code>: <detail> (<path>)
//	  ...
//	---
//
// AI agents reading the tool result can detect warnings by checking
// for the literal "WARNINGS (" prefix without parsing the body. The
// tool's registered description documents this convention.
func prefixWarnings(warnings []entity.Warning) string {
	if len(warnings) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "WARNINGS (%d):\n", len(warnings))
	for _, w := range warnings {
		fmt.Fprintf(&sb, "  %s: %s (%s)\n", w.Code, w.Detail, w.Path)
	}
	sb.WriteString("---\n")
	return sb.String()
}

func (s *Server) handleDeleteEntity(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	snap := s.state.current()
	args := newToolRequest(request)
	id, err := args.RequireString("id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	id = trimID(id)
	cascade := args.GetBool("cascade", false)

	st := snap.deps.Store
	if _, getErr := st.GetEntity(ctx, id); getErr != nil {
		return errorResult("entity not found: " + id), nil
	}

	// Every count reported here is of the relations the caller can see. The
	// manager's own counts include edges to hidden entities, so reporting
	// them would disclose how many hidden neighbors the entity has.
	visible := visibleRelationCount(ctx, st, id)
	if !cascade && visible > 0 {
		return errorResult(
			fmt.Sprintf("entity %s has %d relation(s); set cascade=true to delete them too", id, visible)), nil
	}

	if _, delErr := snap.deps.EntityManager.DeleteEntity(ctx, id, cascade); delErr != nil {
		return errorResult(delErr.Error()), nil
	}

	msg := "Deleted " + id
	if cascade && visible > 0 {
		msg += fmt.Sprintf(" and %d relation(s)", visible)
	}
	return textResult(msg), nil
}

func (s *Server) handleRenameEntity(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	snap := s.state.current()
	args := newToolRequest(request)
	oldID, err := args.RequireString("id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	oldID = trimID(oldID)

	newID, err := args.RequireString("new_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	newID = trimID(newID)

	dryRun := args.GetBool("dry_run", false)

	// Gate the source id first: the write path answers "forbidden" for an
	// entity that exists but is hidden, which would confirm it exists.
	if !visibility.Readable(ctx, snap.deps.Store, oldID) {
		return errorResult("entity not found: " + oldID), nil
	}

	// Pause watcher during rename
	snap.deps.Watcher.Pause()
	defer snap.deps.Watcher.Resume()

	// Counted before the rename and through the gated store; the manager's
	// RelationsUpdated includes edges to hidden entities.
	visible := visibleRelationCount(ctx, snap.deps.Store, oldID)
	result, renameErr := snap.deps.EntityManager.RenameEntity(
		ctx, oldID, newID, entity.RenameOptions{DryRun: dryRun})
	if renameErr != nil {
		return errorResult(renameErr.Error()), nil
	}

	verb := "Renamed"
	if dryRun {
		verb = "Dry run — would rename"
	}
	return textResult(
		fmt.Sprintf("%s: %s → %s (%d relations updated)", verb, result.OldID, result.NewID, visible)), nil
}

// visibleRelationCount counts id's relations through st. On the gated store
// that is the edges whose both endpoints the caller may read. A read error
// ends the count early: the number is informational, and the write that
// follows reports its own errors.
func visibleRelationCount(ctx context.Context, st GraphReader, id string) int {
	n := 0
	for _, err := range st.ListRelations(ctx, store.RelationQuery{EntityID: id, Direction: store.DirectionBoth}) {
		if err != nil {
			break
		}
		n++
	}
	return n
}

// sortStoreEntitiesByID sorts entity.Entity slices by ID using natural ordering.
func sortStoreEntitiesByID(entities []*entity.Entity) {
	sort.Slice(entities, func(i, j int) bool {
		return natsort.Less(entities[i].ID, entities[j].ID)
	})
}
