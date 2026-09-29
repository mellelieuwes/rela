package dataentry

import (
	"context"
	"net/http"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/attachment"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// attachmentHandler serves the entity-attachment routes
// (/api/v1/{plural}/{id}/_attachments/...): upload, download, detach.
// Extracted from App (TKT-R68TV8) to shrink the god object.
//
// It holds the full store.Store because attachment.New (the shared HTTP/CLI
// write-policy service) requires it. The write handle is NOT the full manager:
// this handler never calls it, only passes it to attachment.New, so it holds
// exactly attachment's own one-method attachment.EntityPatcher (TKT-IVSJV6). The
// swappable collaborators (acl, audit sink, field resolver, command runner) are closures over
// App so tests that reassign app.acl / app.fieldResolver after construction
// stay effective — same rationale as affordanceService. gateRead is App's
// shared uniform-404 read gate (handlers_attachment and the entity read path
// must 404 identically for hidden and nonexistent ids).
type attachmentHandler struct {
	schema     func() *Schema
	store      store.Store
	manager    attachment.EntityPatcher
	runner     func() attachment.CommandRunner
	reader     entityReader
	serializer entitySerializer
	acl        func() acl.ACL
	audit      func() audit.Audit
	fields     func() FieldVerdictResolver
	gateRead   func(w http.ResponseWriter, r *http.Request, typeName, entityID string) bool
	// locker serializes writers to one (entity, property); shared with the
	// remote MCP attachment tools through [MCPHost].
	locker attachment.Locker
	// uploads bounds concurrent uploads; shared with the remote MCP tools.
	uploads *attachment.Limiter
	// owned answers twin ownership (twinOwnedFields): an upload or delete on
	// an externally owned file property is refused before any bytes change.
	owned ownedFieldsFunc

	// provision implements unmatched_principal: provision (TKT-ANUJDS), set by
	// App after construction. Called at the top of each attachment write; a
	// no-op unless an unmatched verified principal hits a provision policy.
	// See writeHandler.withProvision for the shared rationale.
	provision func(context.Context) context.Context
}

// withProvision runs the provision seam and returns the request the handler
// must use.
func (h *attachmentHandler) withProvision(r *http.Request) *http.Request {
	if h.provision != nil {
		return r.WithContext(h.provision(r.Context()))
	}
	return r
}
