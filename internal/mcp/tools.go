package mcp

import mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"

// registerTools registers the tool set. It is kept small on purpose: an MCP
// server may be loaded into every session of a client, and each tool costs
// context for its name, description and schema whether or not it is used.
// Related operations therefore share one tool with a selector argument
// (trace direction, analyze check, schema type) rather than one tool each.
func (s *Server) registerTools() {
	// Entity tools
	addTool(s, toolListEntities(), s.handleListEntities)
	addTool(s, toolShowEntity(), s.handleShowEntity)
	addTool(s, toolSearchEntities(), s.handleSearchEntities)
	addTool(s, toolCreateEntity(), s.handleCreateEntity)
	addTool(s, toolUpdateEntity(), s.handleUpdateEntity)
	addTool(s, toolDeleteEntity(), s.handleDeleteEntity)
	addTool(s, toolRenameEntity(), s.handleRenameEntity)

	// Relation tools
	addTool(s, toolListRelations(), s.handleListRelations)
	addTool(s, toolCreateRelation(), s.handleCreateRelation)
	addTool(s, toolDeleteRelation(), s.handleDeleteRelation)

	// Graph tools
	addTool(s, toolTrace(), bind(s, selTrace, traceHandler.handleTrace))
	addTool(s, toolFindPath(), bind(s, selTrace, traceHandler.handleFindPath))

	// Analysis and schema
	addTool(s, toolAnalyze(), s.handleAnalyze)
	addTool(s, toolSchema(), bind(s, selSchemaRes, schemaResourceHandler.handleSchema))

	// Attachment tools
	addTool(s, toolListAttachments(), bind(s, selAttach, attachmentHandler.handleListAttachments))
	addTool(s, toolReadAttachment(), bind(s, selAttach, attachmentHandler.handleReadAttachment))
	addTool(s, toolAttachFile(), bind(s, selAttach, attachmentHandler.handleAttachFile))
	addTool(s, toolDeleteAttachment(), bind(s, selAttach, attachmentHandler.handleDeleteAttachment))

	// Lua scripting tools: stdio only (see WithLuaTools).
	if !s.luaTools {
		return
	}
	addTool(s, toolLuaEval(), bind(s, selLua, luaHandler.handleLuaEval))
	addTool(s, toolLuaRun(), bind(s, selLua, luaHandler.handleLuaRun))
}

// --- Tool Definitions ---
//
// Conventions shared by several tools (the WARNINGS prefix, null deletes a
// property, display titles) are stated once in the server instructions
// (see serverInstructions) rather than repeated in each description.

// Default page sizes. A list without an explicit limit must not dump a whole
// graph into the caller's context; `total` and `has_more` in the result tell
// the caller when to page.
const (
	defaultListLimit   = 50
	defaultSearchLimit = 20
)

func toolListEntities() *mcpgo.Tool {
	return newTool("list_entities",
		withDescription("List entities as {id,type,title,status} summaries, sorted by ID. "+
			"Result: {total,has_more,entities}."),
		withString("type", description("Entity type (required with filter)")),
		withString("filter", description(
			"Predicate expression, e.g. entity.status == 'open' and entity.priority ~= 'low', "+
				"or related(entity, 'implements', { status = 'open' })")),
		withNumber("limit", description("Max results (default 50)")),
		withNumber("offset", description("Results to skip")),
	)
}

func toolShowEntity() *mcpgo.Tool {
	return newTool("show_entity",
		withDescription("Get one entity: properties, markdown content, and relations grouped by type"),
		withString("id", required(), description("Entity ID")),
		withBoolean("content", description("Include the markdown body (default true)")),
	)
}

func toolSearchEntities() *mcpgo.Tool {
	return newTool("search_entities",
		withDescription("Full-text search across entity titles and properties"),
		withString("query", required(), description("Search text")),
		withString("type", description("Restrict to entity type")),
		withNumber("limit", description("Max results (default 20)")),
	)
}

func toolCreateEntity() *mcpgo.Tool {
	return newTool("create_entity",
		withDescription("Create an entity. Call schema with the type first to see its properties."),
		withString("type", required(), description("Entity type")),
		withObject("properties", required(), description("Property map")),
		withString("content", description("Markdown body")),
		withString("id", description("Custom ID (only for types with id_type manual)")),
	)
}

func toolUpdateEntity() *mcpgo.Tool {
	return newTool("update_entity",
		withDescription("Update an entity. Only the named properties change; null removes one."),
		withString("id", required(), description("Entity ID")),
		withObject("properties", description("Properties to set; null removes")),
		withString("content", description("New markdown body (replaces the old one)")),
	)
}

func toolDeleteEntity() *mcpgo.Tool {
	return newTool("delete_entity",
		withDescription("Delete an entity"),
		withString("id", required(), description("Entity ID")),
		withBoolean("cascade", description("Also delete its relations (default false)")),
	)
}

func toolRenameEntity() *mcpgo.Tool {
	return newTool("rename_entity",
		withDescription("Change an entity's ID and update every relation that references it"),
		withString("id", required(), description("Current ID")),
		withString("new_id", required(), description("New ID")),
		withBoolean("dry_run", description("Preview only (default false)")),
	)
}

func toolListRelations() *mcpgo.Tool {
	return newTool("list_relations",
		withDescription("List relations, filtered by type, source or target. Result: {total,has_more,relations}."),
		withString("type", description("Relation type")),
		withString("from", description("Source entity ID")),
		withString("to", description("Target entity ID")),
		withNumber("limit", description("Max results (default 50)")),
		withNumber("offset", description("Results to skip")),
	)
}

func toolCreateRelation() *mcpgo.Tool {
	return newTool("create_relation",
		withDescription("Create a relation between two entities"),
		withString("from", required(), description("Source entity ID")),
		withString("type", required(), description("Relation type")),
		withString("to", required(), description("Target entity ID")),
		withString("content", description("Markdown body")),
		withObject("properties", description("Property map")),
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

func toolTrace() *mcpgo.Tool {
	return newTool("trace",
		withDescription("Walk the graph from an entity and return the reachable tree. "+
			"direction both follows outgoing and incoming edges; upstream follows incoming edges only."),
		withString("id", required(), description("Start entity ID")),
		withString("direction", description("Default both"), enum(traceBoth, traceUpstream)),
		withNumber("max_depth", description("Max depth (default 0 = unlimited)")),
	)
}

func toolFindPath() *mcpgo.Tool {
	return newTool("find_path",
		withDescription("Find the shortest path between two entities"),
		withString("from", required(), description("Source entity ID")),
		withString("to", required(), description("Target entity ID")),
	)
}

func toolAnalyze() *mcpgo.Tool {
	return newTool("analyze",
		withDescription("Check the graph against the schema. "+
			"cardinality: relation min/max; properties: property values; validations: custom rules; "+
			"unique: duplicate unique values; orphans: unconnected entities; schema: unused types."),
		withString("check", required(), enum(analyzeChecks...)),
		withString("type", description("orphans only: restrict to entity type")),
		withNumber("threshold", description("schema only: report types with at most this many instances (default 0)")),
	)
}

func toolSchema() *mcpgo.Tool {
	return newTool("schema",
		withDescription("Describe the schema. Without type: every entity and relation type in one line each. "+
			"With an entity or relation type: its properties, allowed values and relations."),
		withString("type", description("Entity or relation type")),
	)
}
