package mcp

import mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"

func (s *Server) registerTools() {
	// Entity tools
	s.mcp.AddTool(toolListEntities(), s.handleListEntities)
	s.mcp.AddTool(toolShowEntity(), s.handleShowEntity)
	s.mcp.AddTool(toolSearchEntities(), s.handleSearchEntities)
	s.mcp.AddTool(toolCreateEntity(), s.handleCreateEntity)
	s.mcp.AddTool(toolUpdateEntity(), s.handleUpdateEntity)
	s.mcp.AddTool(toolDeleteEntity(), s.handleDeleteEntity)
	s.mcp.AddTool(toolRenameEntity(), s.handleRenameEntity)

	// Relation tools
	s.mcp.AddTool(toolListRelations(), s.handleListRelations)
	s.mcp.AddTool(toolCreateRelation(), s.handleCreateRelation)
	s.mcp.AddTool(toolDeleteRelation(), s.handleDeleteRelation)

	// Trace tools
	s.mcp.AddTool(toolTraceFrom(), bind(s, selTrace, traceHandler.handleTraceFrom))
	s.mcp.AddTool(toolTraceTo(), bind(s, selTrace, traceHandler.handleTraceTo))
	s.mcp.AddTool(toolFindPath(), bind(s, selTrace, traceHandler.handleFindPath))

	// Analysis tools
	s.mcp.AddTool(toolAnalyzeOrphans(), s.handleAnalyzeOrphans)
	s.mcp.AddTool(toolAnalyzeCardinality(), s.handleAnalyzeCardinality)
	s.mcp.AddTool(toolAnalyzeUnique(), s.handleAnalyzeUnique)
	s.mcp.AddTool(toolAnalyzeProperties(), s.handleAnalyzeProperties)
	s.mcp.AddTool(toolAnalyzeValidations(), s.handleAnalyzeValidations)
	s.mcp.AddTool(toolAnalyzeSchema(), s.handleAnalyzeSchema)

	// Schema tools (get_metamodel intentionally shares get_schema's
	// handler — see toolGetMetamodel)
	s.mcp.AddTool(toolGetSchema(), bind(s, selSchemaRes, schemaResourceHandler.handleGetSchema))
	s.mcp.AddTool(toolGetMetamodel(), bind(s, selSchemaRes, schemaResourceHandler.handleGetSchema))
	s.mcp.AddTool(toolListEntityTypes(), bind(s, selSchemaRes, schemaResourceHandler.handleListEntityTypes))
	s.mcp.AddTool(toolListRelationTypes(), bind(s, selSchemaRes, schemaResourceHandler.handleListRelationTypes))

	// Attachment tools
	s.mcp.AddTool(toolListAttachments(), bind(s, selAttach, attachmentHandler.handleListAttachments))
	s.mcp.AddTool(toolReadAttachment(), bind(s, selAttach, attachmentHandler.handleReadAttachment))
	s.mcp.AddTool(toolAttachFile(), bind(s, selAttach, attachmentHandler.handleAttachFile))
	s.mcp.AddTool(toolDeleteAttachment(), bind(s, selAttach, attachmentHandler.handleDeleteAttachment))

	// Utility tools
	s.mcp.AddTool(toolExport(), bind(s, selExport, exportHandler.handleExport))

	// Lua scripting tools: stdio only (see WithLuaTools).
	if !s.luaTools {
		return
	}
	s.mcp.AddTool(toolLuaEval(), bind(s, selLua, luaHandler.handleLuaEval))
	s.mcp.AddTool(toolLuaRun(), bind(s, selLua, luaHandler.handleLuaRun))
	s.mcp.AddTool(toolLuaList(), bind(s, selLua, luaHandler.handleLuaList))
}

// --- Tool Definitions ---

func toolListEntities() *mcpgo.Tool {
	return newTool("list_entities",
		withDescription("List entities, optionally filtered by type and property expressions"),
		withString("type", description("Entity type to filter by (e.g. requirement, decision)")),
		withString("where", description("Filter expression (e.g. status=accepted, priority!=low)")),
		withNumber("limit", description("Maximum number of results to return")),
		withNumber("offset", description("Number of results to skip")),
	)
}

func toolShowEntity() *mcpgo.Tool {
	return newTool("show_entity",
		withDescription("Get full entity details including properties, content, and relations"),
		withString("id", required(), description("Entity ID (e.g. REQ-001)")),
	)
}

func toolSearchEntities() *mcpgo.Tool {
	return newTool("search_entities",
		withDescription("Full-text search across entity titles and properties"),
		withString("query", required(), description("Search query string")),
		withString("type", description("Restrict search to entity type")),
		withNumber("limit", description("Maximum number of results (default 20)")),
	)
}

// warningsConvention is appended to tool descriptions whose handlers
// surface DEC-HWZHA soft-validation warnings as a leading section in
// the result text. Documenting this in the description primes AI
// agents to look for the prefix programmatically.
const warningsConvention = " The result text begins with `WARNINGS (n):` " +
	"when soft validation issues occurred (required field missing, " +
	"value out of enum, type mismatch, etc.). The write still " +
	"succeeded; warnings are advisory. Hard errors (unknown entity " +
	"type, bad ID prefix) still come back via the standard error channel."

func toolCreateEntity() *mcpgo.Tool {
	return newTool("create_entity",
		withDescription("Create a new entity of the specified type."+warningsConvention),
		withString("type", required(), description("Entity type (e.g. requirement, decision)")),
		withObject("properties", required(),
			description("Property map (e.g. {\"title\": \"...\", \"status\": \"draft\"})")),
		withString("content", description("Markdown body content")),
		withString("id", description("Custom entity ID (only valid when the type's id_type is manual; auto-generated otherwise)")),
	)
}

func toolUpdateEntity() *mcpgo.Tool {
	const propsDesc = "Properties to set or update. Set a property to null to remove it from the entity. " +
		"Empty string is treated as no value (silently ignored — use null to delete). " +
		"Clearing a required property succeeds with a warning per DEC-HWZHA; the " +
		"entity persists in a temporarily invalid state."
	return newTool("update_entity",
		withDescription("Update an existing entity's properties or content. "+
			"Set a property to null in `properties` to remove it from the entity. "+
			"Empty string is treated as no value (silently ignored — use null to delete). "+
			"Clearing a required property succeeds with a warning per DEC-HWZHA."+warningsConvention),
		withString("id", required(), description("Entity ID to update")),
		withObject("properties", description(propsDesc)),
		withString("content", description("New markdown body content")),
	)
}

func toolDeleteEntity() *mcpgo.Tool {
	return newTool("delete_entity",
		withDescription("Delete an entity and optionally its relations"),
		withString("id", required(), description("Entity ID to delete")),
		withBoolean("cascade", description("Also delete all relations (default false)")),
	)
}

func toolRenameEntity() *mcpgo.Tool {
	return newTool("rename_entity",
		withDescription("Rename an entity's ID, updating all relations that reference it"),
		withString("id", required(), description("Current entity ID")),
		withString("new_id", required(), description("New entity ID")),
		withBoolean("dry_run", description("Preview changes without applying (default false)")),
	)
}

func toolListRelations() *mcpgo.Tool {
	return newTool("list_relations",
		withDescription("List relations, optionally filtered by type, source, or target"),
		withString("type", description("Relation type to filter by")),
		withString("from", description("Source entity ID")),
		withString("to", description("Target entity ID")),
		withNumber("limit", description("Maximum number of results to return")),
		withNumber("offset", description("Number of results to skip")),
	)
}

func toolCreateRelation() *mcpgo.Tool {
	return newTool("create_relation",
		withDescription("Create a relation between two entities"),
		withString("from", required(), description("Source entity ID")),
		withString("type", required(), description("Relation type (e.g. addresses, implements)")),
		withString("to", required(), description("Target entity ID")),
		withString("content", description("Markdown content for the relation")),
		withObject("properties", description("Property map for the relation (e.g. {\"weight\": \"high\"})")),
	)
}

func toolDeleteRelation() *mcpgo.Tool {
	return newTool("delete_relation",
		withDescription("Delete a relation between two entities"),
		withString("from", required(), description("Source entity ID")),
		withString("type", required(), description("Relation type")),
		withString("to", required(), description("Target entity ID")),
	)
}

func toolTraceFrom() *mcpgo.Tool {
	return newTool("trace_from",
		withDescription("Trace all dependencies from an entity (both outgoing and incoming edges)"),
		withString("id", required(), description("Entity ID to trace from")),
		withNumber("max_depth", description("Maximum trace depth (0 = unlimited)")),
	)
}

func toolTraceTo() *mcpgo.Tool {
	return newTool("trace_to",
		withDescription("Trace upstream dependencies to an entity (following incoming edges)"),
		withString("id", required(), description("Entity ID to trace to")),
		withNumber("max_depth", description("Maximum trace depth (0 = unlimited)")),
	)
}

func toolFindPath() *mcpgo.Tool {
	return newTool("find_path",
		withDescription("Find the shortest path between two entities"),
		withString("from", required(), description("Source entity ID")),
		withString("to", required(), description("Target entity ID")),
	)
}

func toolAnalyzeOrphans() *mcpgo.Tool {
	return newTool("analyze_orphans",
		withDescription("Find entities with no connections (orphans)"),
		withString("type", description("Filter by entity type")),
	)
}

func toolAnalyzeCardinality() *mcpgo.Tool {
	return newTool("analyze_cardinality",
		withDescription("Check relation cardinality constraints defined in the metamodel"),
	)
}

func toolAnalyzeUnique() *mcpgo.Tool {
	return newTool("analyze_unique",
		withDescription("Find entities that violate a `unique: true` property constraint "+
			"(same-type entities sharing a value for a unique property) — e.g. pre-existing "+
			"duplicates that predate the constraint"),
	)
}

func toolAnalyzeProperties() *mcpgo.Tool {
	return newTool("analyze_properties",
		withDescription("Validate entity property values against the metamodel schema"),
	)
}

func toolAnalyzeValidations() *mcpgo.Tool {
	return newTool("analyze_validations",
		withDescription("Run custom validation rules defined in the metamodel"),
	)
}

func toolAnalyzeSchema() *mcpgo.Tool {
	return newTool("analyze_schema",
		withDescription("Analyze metamodel schema usage to find unused entity types, relation types, and custom types"),
		withNumber("threshold", description("Show types with instance count <= threshold (0 = only unused, default 0)")),
	)
}

func toolGetSchema() *mcpgo.Tool {
	return newTool("get_schema",
		withDescription("Get the full schema definition (entity types, relations, properties, validations)"),
	)
}

// toolGetMetamodel is the pre-rename alias for get_schema, kept registered
// because MCP clients pin tool names in their own config files — dropping it
// would break every existing client on upgrade. Deprecated: use get_schema.
func toolGetMetamodel() *mcpgo.Tool {
	return newTool("get_metamodel",
		withDescription("Deprecated alias for get_schema."),
	)
}

func toolListEntityTypes() *mcpgo.Tool {
	return newTool("list_entity_types",
		withDescription("List available entity types with their property schemas"),
	)
}

func toolListRelationTypes() *mcpgo.Tool {
	return newTool("list_relation_types",
		withDescription("List available relation types with their constraints"),
	)
}

func toolExport() *mcpgo.Tool {
	return newTool("export",
		withDescription("Export entities and relations in JSON, YAML, or CSV format"),
		withString("format", required(),
			description("Output format"), enum("json", "yaml", "csv")),
		withString("type", description("Entity type to export (omit for all)")),
	)
}
