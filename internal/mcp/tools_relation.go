// coverage-ignore: MCP tool handlers - tested via integration tests
package mcp

import (
	"context"
	"fmt"
	"strings"

	mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

func (s *Server) handleListRelations(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	args := newToolRequest(request)
	relType := args.GetString("type", "")
	from := args.GetString("from", "")
	to := args.GetString("to", "")
	limit := args.GetInt("limit", 0)
	offset := args.GetInt("offset", 0)

	st := s.deps().Store
	q := store.RelationQuery{Type: relType, From: from, To: to}

	all := make([]*entity.Relation, 0)
	for r, err := range st.ListRelations(ctx, q) {
		if err != nil {
			return errorResult(err.Error()), nil
		}
		all = append(all, r)
	}

	sortStoreRelations(all)

	// Apply offset/limit
	if offset > 0 {
		if offset >= len(all) {
			all = nil
		} else {
			all = all[offset:]
		}
	}
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}

	text, err := convertStoreRelationsList(all)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	return textResult(text), nil
}

func (s *Server) handleCreateRelation(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	args := newToolRequest(request)
	fromID, err := args.RequireString("from")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	fromID = trimID(fromID)
	relType, err := args.RequireString("type")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	relType = strings.TrimSpace(relType)
	toID, err := args.RequireString("to")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	toID = trimID(toID)

	// Treat an empty `content` string from the MCP request as "leave alone"
	// rather than "set body to empty". MCP clients can omit the field or
	// pass null to mean the same; an explicit "" today never reaches a
	// no-content-meant-empty case in practice.
	opts := entity.RelationOptions{
		Properties: extractProperties(request),
		Content:    nilIfEmpty(args.GetString("content", "")),
	}

	// Gate both endpoints first: the write path answers differently for a
	// hidden entity and a missing one, which would confirm it exists.
	for _, id := range []string{fromID, toID} {
		if !visibility.Readable(ctx, s.deps().Store, id) {
			return errorResult("entity not found: " + id), nil
		}
	}

	if _, createErr := s.deps().EntityManager.CreateRelation(ctx, fromID, relType, toID, opts); createErr != nil {
		return errorResult(createErr.Error()), nil
	}

	return textResult(
		fmt.Sprintf("Created link: %s --%s--> %s", fromID, relType, toID)), nil
}

func (s *Server) handleDeleteRelation(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	args := newToolRequest(request)
	fromID, err := args.RequireString("from")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	fromID = trimID(fromID)
	relType, err := args.RequireString("type")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	relType = strings.TrimSpace(relType)
	toID, err := args.RequireString("to")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	toID = trimID(toID)

	st := s.deps().Store
	if _, getErr := st.GetRelation(ctx, fromID, relType, toID); getErr != nil {
		return errorResult(
			fmt.Sprintf("relation not found: %s --%s--> %s", fromID, relType, toID)), nil
	}

	if delErr := s.deps().EntityManager.DeleteRelation(ctx, fromID, relType, toID); delErr != nil {
		return errorResult(delErr.Error()), nil
	}

	return textResult(
		fmt.Sprintf("Removed link: %s --%s--> %s", fromID, relType, toID)), nil
}

// nilIfEmpty returns nil when s is empty, else &s. Used to translate
// "absent / empty string" inputs from the MCP layer into the
// leave-alone semantic of entity.RelationOptions.Content.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
