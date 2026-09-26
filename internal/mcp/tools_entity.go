// coverage-ignore: MCP tool handlers - tested via integration tests
package mcp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"

	"sort"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/natsort"
	"github.com/Sourcehaven-BV/rela/internal/search"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

func (s *Server) handleListEntities(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	args := newToolRequest(request)
	entityType := args.GetString("type", "")
	where := args.GetString("where", "")
	limit := args.GetInt("limit", 0)
	offset := args.GetInt("offset", 0)

	st := s.deps().Store
	q := store.EntityQuery{}
	if entityType != "" {
		q.Type = group(s, selTypes).resolveType(entityType)
	}

	entities := make([]*entity.Entity, 0)
	for e, err := range st.ListEntities(ctx, q) {
		if err != nil {
			return errorResult(err.Error()), nil
		}
		entities = append(entities, e)
	}

	// Apply filter
	if where != "" {
		filtered, filterErr := filterStoreEntities(entities, where)
		if filterErr != nil {
			return errorResult(fmt.Sprintf("invalid filter: %v", filterErr)), nil
		}
		entities = filtered
	}

	sortStoreEntitiesByID(entities)

	// Apply offset/limit
	if offset > 0 {
		if offset >= len(entities) {
			entities = nil
		} else {
			entities = entities[offset:]
		}
	}
	if limit > 0 && limit < len(entities) {
		entities = entities[:limit]
	}

	summaries := make([]map[string]any, len(entities))
	for i, e := range entities {
		summaries[i] = convertStoreEntitySummary(e)
	}
	text, err := marshalJSON(summaries)
	if err != nil { // coverage-ignore: defensive: summaries is []map[string]any of entity id/type/title/status strings;
		// json.Marshal cannot fail.
		return errorResult(err.Error()), nil
	}
	return textResult(text), nil
}

func (s *Server) handleShowEntity(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	args := newToolRequest(request)
	id, err := args.RequireString("id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	id = trimID(id)

	st := s.deps().Store
	e, getErr := st.GetEntity(ctx, id)
	if getErr != nil {
		return errorResult("entity not found: " + id), nil
	}

	text, err := convertStoreEntity(ctx, e, st, true)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	return textResult(text), nil
}

func (s *Server) handleSearchEntities(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	args := newToolRequest(request)
	query, err := args.RequireString("query")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	entityType := args.GetString("type", "")
	const defaultSearchLimit = 20
	limit := args.GetInt("limit", defaultSearchLimit)

	q := search.Query{Text: query, Limit: limit}
	if entityType != "" {
		q.Types = []string{group(s, selTypes).resolveType(entityType)}
	}

	var hits []search.Hit
	for hit, searchErr := range s.deps().Searcher.Search(ctx, q) {
		if searchErr != nil {
			return errorResult(fmt.Sprintf("search failed: %v", searchErr)), nil
		}
		hits = append(hits, hit)
	}
	summaries, err := hydrateHits(ctx, s.deps().Store, hits)
	if err != nil {
		return errorResult(fmt.Sprintf("search failed: %v", err)), nil
	}

	text, err := marshalJSON(summaries)
	if err != nil { // coverage-ignore: defensive: summaries is []map[string]any of search-hit id/type/title/status
		// strings; json.Marshal cannot fail.
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
func hydrateHits(ctx context.Context, st GraphReader, hits []search.Hit) ([]map[string]any, error) {
	summaries := make([]map[string]any, 0, len(hits))
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
		summary := map[string]any{"id": e.ID, "type": e.Type}
		if !e.Face.IsDefault() {
			summary["face"] = e.Face.String()
		}
		if title := e.Title(); title != "" {
			summary["title"] = title
		}
		if status := e.GetString("status"); status != "" {
			summary["status"] = status
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

func (s *Server) handleCreateEntity(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	args := newToolRequest(request)
	typeName, err := args.RequireString("type")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	content := args.GetString("content", "")
	customID := args.GetString("id", "")

	// Resolve type
	resolvedType, _, resolveErr := group(s, selTypes).resolveEntityType(typeName)
	if resolveErr != nil {
		return errorResult(resolveErr.Error()), nil
	}

	// Parse properties from the request
	properties := extractProperties(request)

	// Validate property names early for better error messages
	if errResult := group(s, selTypes).validatePropertyNames(resolvedType, properties); errResult != nil {
		return errResult, nil
	}

	result, createErr := s.deps().EntityManager.CreateEntity(ctx,
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

	st := s.deps().Store
	e, _ := st.GetEntity(ctx, created.ID)
	if e == nil {
		// Fallback: return minimal info
		return textResult(fmt.Sprintf("Created %s %s", resolvedType, created.ID)), nil
	}

	text, err := convertStoreEntity(ctx, e, st, false)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	body := fmt.Sprintf("Created %s %s\n\n%s", resolvedType, created.ID, text)
	return textResult(prefixWarnings(result.Warnings) + body), nil
}

func (s *Server) handleUpdateEntity(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	args := newToolRequest(request)
	id, err := args.RequireString("id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	id = trimID(id)

	st := s.deps().Store
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
	if errResult := group(s, selTypes).validatePropertyNames(e.Type, properties); errResult != nil {
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

	updateResult, updateErr := s.deps().EntityManager.PatchEntity(ctx, id, patch)
	if updateErr != nil {
		return errorResult(updateErr.Error()), nil
	}

	updated, _ := st.GetEntity(ctx, id)
	if updated == nil {
		return textResult(prefixWarnings(updateResult.Warnings) + "Updated " + id), nil
	}

	text, convertErr := convertStoreEntity(ctx, updated, st, true)
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
	args := newToolRequest(request)
	id, err := args.RequireString("id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	id = trimID(id)
	cascade := args.GetBool("cascade", false)

	st := s.deps().Store
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

	if _, delErr := s.deps().EntityManager.DeleteEntity(ctx, id, cascade); delErr != nil {
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
	if !visibility.Readable(ctx, s.deps().Store, oldID) {
		return errorResult("entity not found: " + oldID), nil
	}

	// Pause watcher during rename
	s.deps().Watcher.Pause()
	defer s.deps().Watcher.Resume()

	// Counted before the rename and through the gated store; the manager's
	// RelationsUpdated includes edges to hidden entities.
	visible := visibleRelationCount(ctx, s.deps().Store, oldID)
	result, renameErr := s.deps().EntityManager.RenameEntity(
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

// filterStoreEntities applies a where clause to entity.Entity slices.
func filterStoreEntities(entities []*entity.Entity, where string) ([]*entity.Entity, error) {
	parts := strings.SplitN(where, "=", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("expected property=value, got %q", where)
	}
	key, value := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	var filtered []*entity.Entity
	for _, e := range entities {
		if e.GetAttributeString(key) == value {
			filtered = append(filtered, e)
		}
	}
	return filtered, nil
}

// sortStoreEntitiesByID sorts entity.Entity slices by ID using natural ordering.
func sortStoreEntitiesByID(entities []*entity.Entity) {
	sort.Slice(entities, func(i, j int) bool {
		return natsort.Less(entities[i].ID, entities[j].ID)
	})
}
