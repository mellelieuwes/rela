// coverage-ignore: MCP resource handlers - tested via integration tests
package mcp

import (
	"context"
	"fmt"
	"strings"

	mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerResources() {
	// Static resource: metamodel
	s.mcp.AddResource(
		&mcpgo.Resource{
			URI:         "rela://metamodel",
			Name:        "Metamodel Schema",
			Description: "The project's metamodel definition (entity types, relations, properties)",
			MIMEType:    "application/json",
		},
		bind(s, selSchemaRes, schemaResourceHandler.handleReadMetamodel),
	)

	// Dynamic resource template: entities
	s.mcp.AddResourceTemplate(
		&mcpgo.ResourceTemplate{
			URITemplate: "rela://entity/{type}/{id}",
			Name:        "Entity",
			Description: "Read a specific entity with its properties, content, and relations",
			MIMEType:    "application/json",
		},
		bind(s, selSchemaRes, schemaResourceHandler.handleReadEntity),
	)

	// Dynamic resource template: relations
	s.mcp.AddResourceTemplate(
		&mcpgo.ResourceTemplate{
			URITemplate: "rela://relation/{from}/{type}/{to}",
			Name:        "Relation",
			Description: "Read a specific relation between two entities",
			MIMEType:    "application/json",
		},
		bind(s, selSchemaRes, schemaResourceHandler.handleReadRelation),
	)
}

func (h schemaResourceHandler) handleReadMetamodel(
	_ context.Context, _ *mcpgo.ReadResourceRequest,
) (*mcpgo.ReadResourceResult, error) {
	meta := h.meta
	result := map[string]any{
		"version":   meta.GetVersion(),
		"namespace": meta.GetNamespace(),
		"entities":  meta.GetEntities(),
		"relations": meta.GetRelations(),
		"types":     meta.GetTypes(),
	}
	if len(meta.Validations) > 0 {
		result["validations"] = meta.Validations
	}

	text, err := marshalJSON(result)
	if err != nil { // coverage-ignore: defensive: result holds metamodel version/namespace/entities/relations (JSON-
		// tagged types); json.Marshal cannot fail.
		return nil, fmt.Errorf("failed to marshal metamodel: %w", err)
	}

	return &mcpgo.ReadResourceResult{
		Contents: []*mcpgo.ResourceContents{{
			URI:      "rela://metamodel",
			MIMEType: "application/json",
			Text:     text,
		}},
	}, nil
}

func (h schemaResourceHandler) handleReadEntity(
	ctx context.Context, request *mcpgo.ReadResourceRequest,
) (*mcpgo.ReadResourceResult, error) {
	uri := request.Params.URI

	// Parse URI: rela://entity/{type}/{id}
	parts := strings.TrimPrefix(uri, "rela://entity/")
	segments := strings.SplitN(parts, "/", 2)
	if len(segments) != 2 {
		return nil, fmt.Errorf("invalid entity URI: %s", uri)
	}
	entityType, id := segments[0], segments[1]

	st := h.store
	e, getErr := st.GetEntity(ctx, id)
	if getErr != nil {
		return nil, fmt.Errorf("entity not found: %s", id)
	}
	if e.Type != entityType {
		return nil, fmt.Errorf("entity %s is type %s, not %s", id, e.Type, entityType)
	}

	text, err := convertStoreEntity(ctx, e, st, h.meta, entityView{relations: true, content: true})
	if err != nil {
		return nil, fmt.Errorf("failed to convert entity: %w", err)
	}

	return &mcpgo.ReadResourceResult{
		Contents: []*mcpgo.ResourceContents{{
			URI:      uri,
			MIMEType: "application/json",
			Text:     text,
		}},
	}, nil
}

func (h schemaResourceHandler) handleReadRelation(
	ctx context.Context, request *mcpgo.ReadResourceRequest,
) (*mcpgo.ReadResourceResult, error) {
	uri := request.Params.URI

	// Parse URI: rela://relation/{from}/{type}/{to}
	parts := strings.TrimPrefix(uri, "rela://relation/")
	segments := strings.SplitN(parts, "/", 3)
	if len(segments) != 3 {
		return nil, fmt.Errorf("invalid relation URI: %s", uri)
	}
	fromID, relType, toID := segments[0], segments[1], segments[2]

	st := h.store
	relation, getErr := st.GetRelation(ctx, fromID, relType, toID)
	if getErr != nil {
		return nil, fmt.Errorf("relation not found: %s --%s--> %s", fromID, relType, toID)
	}

	text, err := convertStoreRelation(relation)
	if err != nil {
		return nil, fmt.Errorf("failed to convert relation: %w", err)
	}

	return &mcpgo.ReadResourceResult{
		Contents: []*mcpgo.ResourceContents{{
			URI:      uri,
			MIMEType: "application/json",
			Text:     text,
		}},
	}, nil
}
