package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool-definition shim for the go-sdk migration (TKT-UIR41P).
//
// The 26 tool definitions are written in mark3labs' builder style:
//
//	mcp.NewTool("show_entity",
//	    mcp.WithDescription("..."),
//	    mcp.WithString("id", mcp.Required(), mcp.Description("...")))
//
// The go-sdk instead reflects a schema from a typed In struct. Rather than
// hand-convert 26 schemas — the step most likely to silently change the tool
// contract — this shim keeps the builder style and emits the SAME
// `inputSchema` JSON the previous library produced, which the committed
// goldens pin byte-for-byte.
//
// Schema shape reproduced (see testdata/tools_list.golden.json):
//
//	{"type":"object","properties":{...},"required":[...]}
//
// with `required` always present — an empty array when no argument is
// required, not omitted.

// schemaProperty is one entry under `properties`. Field order in the emitted
// JSON is irrelevant (encoding/json sorts map keys), but the key set must
// match the old output exactly.
type schemaProperty struct {
	Type        string          `json:"type"`
	Description string          `json:"description,omitempty"`
	Enum        []string        `json:"enum,omitempty"`
	Properties  json.RawMessage `json:"properties,omitempty"`
	Items       json.RawMessage `json:"items,omitempty"`
}

// toolSpec accumulates a tool definition as the builder options are applied.
type toolSpec struct {
	name        string
	description string
	properties  map[string]schemaProperty
	required    []string
}

// toolOption mutates a toolSpec under construction.
type toolOption func(*toolSpec)

// propOption configures a single schema property.
type propOption func(*schemaProperty, *toolSpec, string)

// newTool builds a tool definition. The result is an *mcpgo.Tool whose
// InputSchema is the raw JSON object described above.
func newTool(name string, opts ...toolOption) *mcpgo.Tool {
	spec := &toolSpec{
		name:       name,
		properties: map[string]schemaProperty{},
		required:   []string{},
	}
	for _, opt := range opts {
		opt(spec)
	}

	schema := map[string]any{
		"type":       "object",
		"properties": spec.properties,
		"required":   spec.required,
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		// Only unmarshalable values could fail here, and every value is a
		// literal built above; a failure is a programming error at init time.
		panic("mcp: marshal input schema for tool " + name + ": " + err.Error())
	}

	return &mcpgo.Tool{
		Name:        spec.name,
		Description: spec.description,
		InputSchema: json.RawMessage(raw),
	}
}

// withDescription sets the tool's human-readable description.
func withDescription(d string) toolOption {
	return func(s *toolSpec) { s.description = d }
}

// addProp registers a property of the given JSON type, applying its options.
func addProp(jsonType, name string, opts ...propOption) toolOption {
	return func(s *toolSpec) {
		p := schemaProperty{Type: jsonType}
		for _, opt := range opts {
			opt(&p, s, name)
		}
		s.properties[name] = p
	}
}

func withString(name string, opts ...propOption) toolOption {
	return addProp("string", name, opts...)
}

func withNumber(name string, opts ...propOption) toolOption {
	return addProp("number", name, opts...)
}

func withBoolean(name string, opts ...propOption) toolOption {
	return addProp("boolean", name, opts...)
}

// withObject declares a free-form object property. The old builder emitted an
// empty `"properties": {}` for these, which the goldens pin.
func withObject(name string, opts ...propOption) toolOption {
	return addProp("object", name, append([]propOption{func(p *schemaProperty, _ *toolSpec, _ string) {
		p.Properties = json.RawMessage(`{}`)
	}}, opts...)...)
}

// withArray declares an array property with an unconstrained element type,
// matching the previous library's output for the one array argument in the
// tool set (lua_run's `args`), which carried no `items`.
func withArray(name string, opts ...propOption) toolOption {
	return addProp("array", name, opts...)
}

// required marks the property as required on the enclosing tool.
func required() propOption {
	return func(_ *schemaProperty, s *toolSpec, name string) {
		s.required = append(s.required, name)
	}
}

// description sets a property's description.
func description(d string) propOption {
	return func(p *schemaProperty, _ *toolSpec, _ string) { p.Description = d }
}

// enum constrains a string property to a fixed value set.
func enum(values ...string) propOption {
	return func(p *schemaProperty, _ *toolSpec, _ string) { p.Enum = values }
}

// addTool registers tool with a handler that first refuses any argument the
// tool does not declare. Without this a misspelled or removed argument (such
// as list_entities' old `where`) is dropped silently, and the caller receives
// an unfiltered answer it takes for a filtered one. A free function, like [setDeps], to keep
// Server under its plimsoll load line.
func addTool(s *Server, tool *mcpgo.Tool, handler mcpgo.ToolHandler) {
	known := declaredArguments(tool)
	s.mcp.AddTool(tool, func(ctx context.Context, req *mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		var unknown []string
		for name := range newToolRequest(req).GetArguments() {
			if _, ok := known[name]; !ok {
				unknown = append(unknown, name)
			}
		}
		if len(unknown) > 0 {
			slices.Sort(unknown)
			return errorResult(fmt.Sprintf("unknown argument(s): %s; %s takes: %s",
				strings.Join(unknown, ", "), tool.Name, strings.Join(slices.Sorted(maps.Keys(known)), ", "))), nil
		}
		return handler(ctx, req)
	})
}

// declaredArguments returns the argument names in tool's input schema.
func declaredArguments(tool *mcpgo.Tool) map[string]struct{} {
	raw, ok := tool.InputSchema.(json.RawMessage)
	if !ok {
		panic("mcp: tool " + tool.Name + " has no raw input schema") // coverage-ignore: invariant: newTool sets it
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		panic("mcp: parse input schema for tool " + tool.Name + ": " + err.Error()) // coverage-ignore: invariant: newTool marshals it
	}
	known := make(map[string]struct{}, len(schema.Properties))
	for name := range schema.Properties {
		known[name] = struct{}{}
	}
	return known
}
