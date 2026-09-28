// coverage-ignore: MCP tool handlers - tested via integration tests
package mcp

import (
	"context"
	"fmt"

	mcpgo "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/tracer"
)

// Values of the trace tool's direction argument.
const (
	traceBoth     = "both"
	traceUpstream = "upstream"
)

// traceHandler serves the trace / find_path tools. A type of its own rather
// than more methods on [Server] (the urlHelpers pattern, TKT-YUETL7): tracing
// needs the gated [GraphReader] for existence probes and path titles, the
// tracer for traversal, and the metamodel to resolve display titles.
// Identity still arrives on the ctx via Server.principalMiddleware; the
// handler holds no principal.
type traceHandler struct {
	store  GraphReader
	tracer tracer.Tracer
	meta   *metamodel.Metamodel
}

// handleTrace serves the trace tool. direction "both" is TraceFrom (outgoing
// and incoming edges); "upstream" is TraceTo (incoming edges only).
func (h traceHandler) handleTrace(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	args := newToolRequest(request)
	id, err := args.RequireString("id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	id = trimID(id)
	maxDepth := args.GetInt("max_depth", 0)

	traceFn, emptyMsg := h.tracer.TraceFrom, "No dependencies found"
	switch direction := args.GetString("direction", traceBoth); direction {
	case traceBoth:
	case traceUpstream:
		traceFn, emptyMsg = h.tracer.TraceTo, "No upstream dependencies found"
	default:
		return errorResult(fmt.Sprintf("unknown direction %q (use %s or %s)",
			direction, traceBoth, traceUpstream)), nil
	}

	// Existence probe BEFORE traversal. This must go through h.store —
	// the gated GraphReader — not a raw handle: under a networked wiring a
	// hidden entity's GetEntity returns not-found, so "hidden" and "absent"
	// produce the identical message and the probe is not an existence
	// oracle (RR-FTJUUE). A raw probe here would defeat the gated tracer
	// below, since it answers "is it in the store?" rather than "may this
	// principal see it?".
	if _, getErr := h.store.GetEntity(ctx, id); getErr != nil {
		return errorResult("entity not found: " + id), nil
	}

	result := traceFn(ctx, id, maxDepth)
	if result == nil {
		return textResult(emptyMsg), nil
	}

	text, err := convertTraceResult(result, h.meta)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	return textResult(text), nil
}

func (h traceHandler) handleFindPath(
	ctx context.Context, request *mcpgo.CallToolRequest,
) (*mcpgo.CallToolResult, error) {
	args := newToolRequest(request)
	from, err := args.RequireString("from")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	from = trimID(from)
	to, err := args.RequireString("to")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	to = trimID(to)

	st := h.store
	if _, fromErr := st.GetEntity(ctx, from); fromErr != nil {
		return errorResult("source entity not found: " + from), nil
	}
	if _, toErr := st.GetEntity(ctx, to); toErr != nil {
		return errorResult("target entity not found: " + to), nil
	}

	path := h.tracer.FindPath(ctx, from, to)
	if path == nil {
		return textResult(
			fmt.Sprintf("No path found between %s and %s", from, to)), nil
	}

	// A path is a handful of steps, so one gated read per step is cheap. It
	// also keeps the title honest: a step read through h.store carries only
	// the properties the caller may see.
	text, err := convertPathSteps(path, func(step tracer.PathStep) string {
		e, getErr := st.GetEntity(ctx, step.ID)
		if getErr != nil {
			return ""
		}
		return displayTitle(h.meta, e)
	})
	if err != nil {
		return errorResult(err.Error()), nil
	}
	return textResult(text), nil
}
