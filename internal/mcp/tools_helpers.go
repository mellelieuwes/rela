package mcp

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Sourcehaven-BV/rela/internal/metamodel"
)

// typeResolver maps user-supplied type names onto metamodel entity types —
// alias resolution, plural stripping, and property-name validation shared by
// the entity, analysis and prompt handlers.
//
// A type of its own rather than more methods on [Server] (the urlHelpers
// pattern, TKT-YUETL7): these helpers need exactly one thing from the server —
// the metamodel — no store, no tracer, no request state. Holding that one
// field makes the dependency visible where methods on Server let every helper
// reach all of deps.
type typeResolver struct {
	meta *metamodel.Metamodel
}

func (r typeResolver) resolveType(typeName string) string {
	typeName = strings.TrimSpace(typeName)
	meta := r.meta
	resolved := meta.ResolveAlias(typeName)
	if _, ok := meta.GetEntityDef(resolved); ok {
		return resolved
	}
	// Try stripping plural
	for _, suffix := range []string{"ies", "es", "s"} {
		replacements := map[string]string{"ies": "y", "es": "", "s": ""}
		if before, ok := strings.CutSuffix(typeName, suffix); ok {
			singular := before + replacements[suffix]
			resolved = meta.ResolveAlias(singular)
			if _, ok := meta.GetEntityDef(resolved); ok {
				return resolved
			}
		}
	}
	return typeName
}

// trimID trims whitespace from an entity ID
func trimID(id string) string {
	return strings.TrimSpace(id)
}

func (r typeResolver) resolveEntityType(typeName string) (string, *metamodel.EntityDef, error) {
	resolved := r.resolveType(typeName)
	def, ok := r.meta.GetEntityDef(resolved)
	if !ok {
		return "", nil, fmt.Errorf("unknown entity type: %s", typeName)
	}
	return resolved, def, nil
}

// extractProperties parses the `properties` argument and filters out both nil
// and empty-string entries (both treated as "no value"). Used by create paths.
func extractProperties(request *mcpgo.CallToolRequest) map[string]any {
	props, ok := parsePropertiesArg(request)
	if !ok {
		return nil
	}
	return filterProperties(props, false)
}

// extractPropertiesAllowNil parses the `properties` argument and preserves nil
// entries so update_entity can use them as a delete sentinel. Empty strings are
// still filtered (kept as a no-op for consistency with the create path).
// Returns nil iff the argument is missing/malformed or contains only empty strings.
func extractPropertiesAllowNil(request *mcpgo.CallToolRequest) map[string]any {
	props, ok := parsePropertiesArg(request)
	if !ok {
		return nil
	}
	return filterProperties(props, true)
}

// parsePropertiesArg extracts the raw properties map from a tool request, handling
// both the native map argument and the JSON-encoded string fallback. Returns
// (nil, false) when the argument is missing, of an unsupported type, or malformed/null JSON.
func parsePropertiesArg(request *mcpgo.CallToolRequest) (map[string]any, bool) {
	args := newToolRequest(request).GetArguments()
	propsRaw, ok := args["properties"]
	if !ok {
		return nil, false
	}

	switch p := propsRaw.(type) {
	case map[string]any:
		return p, true
	case string:
		var props map[string]any
		if err := json.Unmarshal([]byte(p), &props); err != nil {
			return nil, false
		}
		// JSON `null` unmarshals into a nil map; treat as malformed/missing.
		if props == nil {
			return nil, false
		}
		return props, true
	default:
		return nil, false
	}
}

// filterProperties removes empty-string values from a property map. When
// keepNil is false, nil values are also removed. Returns nil if the resulting
// map is empty.
func filterProperties(props map[string]any, keepNil bool) map[string]any {
	if props == nil {
		return nil
	}
	filtered := make(map[string]any, len(props))
	for k, v := range props {
		if !keepNil && v == nil {
			continue
		}
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		filtered[k] = v
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

// validatePropertyNames checks property names against the metamodel for the given entity type.
// Reports unknown property names and rejects nil values targeting required properties
// (a nil value means "delete" in update_entity; deleting a required property would leave
// the entity invalid, so we surface that as an actionable error rather than a misleading
// success that `analyze check=validations` later catches).
func (r typeResolver) validatePropertyNames(entityType string, properties map[string]any) *mcpgo.CallToolResult {
	if properties == nil {
		return nil
	}

	entityDef, ok := r.meta.GetEntityDef(entityType)
	if !ok {
		return nil // Type validation will catch this
	}

	var unknown, requiredDeletes, computed []string
	for propName, v := range properties {
		def, exists := entityDef.Properties[propName]
		if !exists {
			unknown = append(unknown, propName)
			continue
		}
		if def.Computed != "" {
			computed = append(computed, propName)
			continue
		}
		if v == nil && def.Required {
			requiredDeletes = append(requiredDeletes, propName)
		}
	}

	if len(unknown) > 0 {
		valid := make([]string, 0, len(entityDef.Properties))
		for name := range entityDef.Properties {
			valid = append(valid, name)
		}
		return errorResult(fmt.Sprintf(
			"unknown properties for %s: %s (valid: %s)",
			entityType, strings.Join(unknown, ", "), strings.Join(valid, ", ")))
	}

	if len(requiredDeletes) > 0 {
		return errorResult(fmt.Sprintf(
			"cannot delete required properties for %s: %s (set a new value instead)",
			entityType, strings.Join(requiredDeletes, ", ")))
	}
	if len(computed) > 0 {
		slices.Sort(computed)
		return errorResult(fmt.Sprintf(
			"computed properties are read-only for %s: %s",
			entityType, strings.Join(computed, ", ")))
	}

	return nil
}

// limitArg reads the `limit` argument. Zero or a negative value means the
// default, never "no limit": an unbounded answer is the cost the default
// exists to cap.
func limitArg(args toolRequest, def int) int {
	if limit := args.GetInt("limit", def); limit > 0 {
		return limit
	}
	return def
}

func applyPagination[T any](items []T, offset, limit int) []T {
	if offset > 0 {
		if offset >= len(items) {
			return nil
		}
		items = items[offset:]
	}
	if limit > 0 && limit < len(items) {
		items = items[:limit]
	}
	return items
}
