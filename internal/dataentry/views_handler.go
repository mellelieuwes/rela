package dataentry

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	v1 "github.com/Sourcehaven-BV/rela/internal/apiwire/v1"
	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	entityPkg "github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/natsort"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

// viewsHandler owns the read-only view-assembly surface: the view
// traversal engine (views.go), the section builders (sections.go), and
// the /_views, /_sidepanel, /_sidebar endpoints, plus the form-resolution
// helpers those builders share (editFormForType/createFormForType).
// Extracted from App (TKT-R68TV8).
//
// Collaborator rationale mirrors attachmentHandler/writeHandler: fixed
// service handles by value (store/reader/serializer/affordances/logo are
// set once in NewApp and never swapped), the schema snapshot and Services
// bundle as closures so every operation reads the current published
// state, and App's shared read gate as a closure so the uniform-404
// behavior can't drift between the read and view paths. This surface is
// read-only — no entitymanager.
type viewsHandler struct {
	schema      func() *Schema
	store       store.Store
	reader      entityReader
	serializer  entitySerializer
	affordances affordanceService
	// viewReader is the row-gating + field-redacting read-out seam
	// (DEC-ZBI39P). Every entity this surface hands to the section builders
	// passes through it, so a neighbor the principal cannot read is dropped
	// and a survivor's hidden fields are redacted.
	viewReader visibility.Reader
	// services returns the read bundle; view traversal and relation-column
	// resolution read through it exactly as the App methods did.
	services func() Services
	// logo backs the sidebar logo URL.
	logo *logoStore
	// gateRead is App.gateReadOrNotFound: ACL-gates an entity read,
	// writing the uniform 404 on denial (hidden = nonexistent).
	gateRead func(w http.ResponseWriter, r *http.Request, typeName, entityID string) bool
	// aclImpl resolves the active ACL for permitsNavEntry's sidebar filter.
	// A closure, not a captured value, for the same reason commandHandler
	// holds one: tests reassign app.acl AFTER construction, so a captured
	// value would go stale and the filter would consult the wrong policy.
	aclImpl func() acl.ACL
	// faceEdges reads the entry's edges from the FACE being served, via
	// [servedFaceEdges]. A closure for the same staleness reason aclImpl is
	// one, and more sharply: [SetWorldNeighbors] runs AFTER this handler is
	// constructed, so a captured worldNeighbors would be the nil it held at
	// construction time and every view would silently take the bare-id arm —
	// the exact face-merging bug this seam exists to close.
	faceEdges func(
		ctx context.Context, e *entityPkg.Entity,
	) ([]*entityPkg.Relation, error)
}

// currentACL resolves the active ACL, or nil when the handler was constructed
// without the closure. Callers must treat nil as "hide" (see permitsNavEntry)
// — a wiring omission has to fail closed, not panic and not show.
func (h *viewsHandler) currentACL() acl.ACL {
	if h.aclImpl == nil {
		return nil
	}
	return h.aclImpl()
}

// redactor is the field-redaction seam for this surface, mirroring
// appRedactor: hidden properties resolve through the current affordance
// service so a redaction decision always reflects live policy.
func (h *viewsHandler) redactor() visibility.FieldRedactor {
	return affRedactor{aff: func() affordanceService { return h.affordances }}
}

// sidePanelEntry resolves and gates the side panel's entry entity from its
// address, writing the uniform not-found itself. Returns ok=false when a
// response has been written.
//
// The id segment is an ADDRESS (`ID` or `ID@face`), the same grammar the
// entity view accepts — the form that mounts this panel is opened on the
// address of the row it edits. The row gate sees the bare id (ACL gate,
// TKT-6N9O1Y: gated BEFORE any read so a denied principal gets a 404
// indistinguishable from a missing id and the traversal never runs), and the
// face half of a `type@face` grant is applied to the row that came back
// (TKT-O7R2A1), with the same 404 so a denied face is indistinguishable from
// an absent one.
func (h *viewsHandler) sidePanelEntry(
	w http.ResponseWriter, r *http.Request, entityType, entityID string,
) (*entityPkg.Entity, bool) {
	ref, ok := parseEntityRef(entityID)
	if !ok {
		writeV1Error(w, r, http.StatusNotFound, "entity_not_found", "Entity not found", "")
		return nil, false
	}
	if !h.gateRead(w, r, entityType, ref.ID) {
		return nil, false
	}
	entry, found := h.reader.getEntityRef(r.Context(), ref)
	if !found || !faceReadable(r.Context(), entry.Type, entry.Face) {
		writeV1Error(w, r, http.StatusNotFound, "entity_not_found", "Entity not found", "")
		return nil, false
	}
	return entry, true
}

// handleV1SidePanel handles GET /api/v1/_sidepanel/{formId}/{entityId}.
func (h *viewsHandler) handleV1SidePanel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}

	// Parse path: /api/v1/_sidepanel/{formId}/{entityId}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/_sidepanel/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		writeV1Error(w, r, http.StatusBadRequest, "invalid_path", "Path must be /_sidepanel/{formId}/{entityId}", "")
		return
	}

	formID := parts[0]
	entityID := parts[1] // Get form config
	s := h.schema()
	form, ok := s.Cfg.Forms[formID]
	if !ok {
		writeV1Error(w, r, http.StatusNotFound, "form_not_found", "Form not found", "")
		return
	}

	// Check if form has side panel
	if form.SidePanel == nil {
		writeV1JSON(w, http.StatusOK, []v1.SidePanelSection{})
		return
	}

	entry, ok := h.sidePanelEntry(w, r, form.EntityType, entityID)
	if !ok {
		return
	}

	// Execute side panel traversal
	sections := h.executeSidePanel(r.Context(), form.SidePanel, entityID, form.EntityType)
	if sections == nil {
		writeV1JSON(w, http.StatusOK, []v1.SidePanelSection{})
		return
	}

	// Build a synthetic ViewConfig to resolve add/link buttons
	viewConfig := ViewConfig{
		Entry:    ViewEntry{Type: form.EntityType},
		Traverse: form.SidePanel.Traverse,
		Sections: form.SidePanel.Sections,
	}
	h.resolveSectionButtonsWithTraverse(r.Context(), viewConfig, sections, entry)

	// Convert to API response format
	result := make([]v1.SidePanelSection, 0, len(sections))
	for _, sec := range sections {
		apiSec := v1.SidePanelSection{
			Heading:      sec.Heading,
			SectionID:    sec.SectionID,
			Display:      sec.Display,
			IsEmpty:      sec.IsEmpty,
			EmptyMessage: sec.EmptyMessage,
		}

		// Convert fields
		for _, f := range sec.Fields {
			apiSec.Fields = append(apiSec.Fields, v1.SectionField(f))
		}

		// Convert entities
		for _, e := range sec.Entities {
			apiEnt := v1.SidePanelEntity{
				ID:         e.ID,
				Title:      e.Title,
				Type:       e.Type,
				EditFormID: e.EditFormID,
				Content:    e.Content,
				HasContent: e.HasContent,
			}
			for _, f := range e.Fields {
				apiEnt.Fields = append(apiEnt.Fields, v1.SectionField(f))
			}
			apiSec.Entities = append(apiSec.Entities, apiEnt)
		}

		// Convert add/link info
		if sec.AddInfo != nil {
			apiSec.AddInfo = &v1.ViewAddInfo{
				Relation: sec.AddInfo.Relation,
				LinkAs:   sec.AddInfo.LinkAs,
				PeerID:   sec.AddInfo.PeerID,
			}
			for _, t := range sec.AddInfo.Targets {
				// Field-by-field, not a struct conversion: the shared
				// SectionCreateTarget carries a Template the side panel has no
				// use for, and the wire type deliberately does not gain one.
				apiSec.AddInfo.Targets = append(apiSec.AddInfo.Targets, v1.ViewAddTarget{
					EntityType: t.EntityType,
					FormID:     t.FormID,
					Label:      t.Label,
				})
			}
		}
		if sec.LinkInfo != nil {
			apiSec.LinkInfo = &v1.ViewLinkInfo{
				Relation:    sec.LinkInfo.Relation,
				LinkAs:      sec.LinkInfo.LinkAs,
				PeerID:      sec.LinkInfo.PeerID,
				EntityTypes: sec.LinkInfo.EntityTypes,
			}
		}

		result = append(result, apiSec)
	}

	writeV1JSON(w, http.StatusOK, result)
}

// handleV1Sidebar returns denormalized sidebar data.
func (h *viewsHandler) handleV1Sidebar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}
	s := h.schema()

	// Build navigation. Entries the principal cannot use are
	// omitted (permitsNavEntry) — a UX filter, not a boundary; see its doc.
	// The ACL is resolved ONCE here rather than per entry, matching
	// resolveCommands.
	navigation := make([]v1.SidebarGroup, 0)
	aclImpl := h.currentACL()

	for _, entry := range s.Cfg.Navigation {
		if entry.IsGroup() {
			group := v1.SidebarGroup{
				Group:     entry.Group,
				Collapsed: entry.Collapsed,
				Items:     make([]v1.SidebarItem, 0),
			}
			for _, item := range entry.Items {
				if !permitsNavEntry(r.Context(), aclImpl, item) {
					continue
				}
				sidebarItem := navEntryToSidebarItem(item, s.Meta)
				group.Items = append(group.Items, sidebarItem)
			}
			// A group whose every item was filtered out is dropped rather than
			// rendered as a bare heading: an empty labeled group reads as a
			// rendering bug, and it outlines what the principal cannot reach
			// without being useful to them.
			if len(group.Items) == 0 {
				continue
			}
			navigation = append(navigation, group)
		} else {
			// Top-level item without group
			if !permitsNavEntry(r.Context(), aclImpl, entry) {
				continue
			}
			item := navEntryToSidebarItem(entry, s.Meta)
			navigation = append(navigation, v1.SidebarGroup{
				Items: []v1.SidebarItem{item},
			})
		}
	}

	resp := v1.SidebarResponse{
		App: v1.AppConfig{
			Name:        s.Cfg.App.Name,
			Description: s.Cfg.App.Description,
		},
		Navigation: navigation,
	}
	resp.LogoURL = h.logo.URL()
	resp.InlineCreate = h.inlineCreateForms(r.Context())

	writeV1JSON(w, http.StatusOK, resp)
}

// permitsNavEntry reports whether a navigation entry should appear in this
// principal's sidebar (TKT-TXDK8U). It is a thin wrapper over
// [permitsGatedUIElement]; the policy and its reasoning live there.
func permitsNavEntry(ctx context.Context, aclImpl acl.ACL, entry dataentryconfig.NavigationEntry) bool {
	return permitsGatedUIElement(ctx, aclImpl, entry.Permission)
}

// permitsGatedUIElement reports whether a `permission:`-gated UI element should
// be shown to this principal (TKT-TXDK8U for sidebar nav entries, TKT-53KICM
// for dashboard cards).
//
// This is a UX filter and NOTHING ELSE. It decides what to RENDER, never what
// to allow. If you want an authorization check, this is the wrong function —
// build one, and see authorizeCommand for the shape.
//
// Hiding an element changes no enforcement: its target is reachable by typing
// the URL and behaves exactly as it did before — a list still returns its
// ACL-scoped rows, which for a principal who may read none of them is simply an
// empty list, and a dashboard card's query still runs through the ACL-scoped
// search path. Nor does it conceal configuration: `/api/v1/_config` keeps
// serving the whole navigation tree and `dashboard:` block to every principal,
// deliberately (root CLAUDE.md, "The configuration is not a secret; the data
// is"). The goal is only to keep things a user cannot act on out of their way.
//
// Policy, keyed on the configured ACL implementation, mirroring
// authorizeCommand (DEC-EIHQSU):
//
//   - No `permission:` → show. The overwhelmingly common case, short-circuited
//     before any ACL work so an unconfigured menu costs nothing.
//   - [acl.NopACL] and [acl.ReadOnlyACL] → show. NEITHER carries a policy, so
//     there is no permission model to consult and no principal who could be
//     shown to hold anything: "no policy configured ⇒ no restrictions", the
//     same posture the read gate takes. See the read-only note below.
//   - [*acl.Declarative] → the principal must hold the permission.
//   - anything else → hide.
//
// Read-only deserves its own paragraph, because the obvious arm is wrong in
// two different ways and this predicate is a copy target.
//
// It is tempting to mirror authorizeCommand, which denies everything under
// ReadOnlyACL. That is right for commands — they SHELL OUT, a write-shaped act
// — but [acl.ReadOnlyACL] only implements AuthorizeWrite; it restricts no
// reads whatsoever. The gated elements are overwhelmingly read surfaces (list,
// kanban, dashboard, search, dashboard cards), and hiding them would remove
// things an observe-only principal can use perfectly well. It would also hide
// them from EVERYONE, since ReadOnlyACL has no identity to check — so
// `permission:` would silently
// change meaning from "hide from non-holders" to "hide from all" based on a
// process-wide flag about writes. An operator in post-incident forensic mode
// (a documented ReadOnlyACL use case) would lose exactly the audit-log entry
// they came for.
//
// The hazard the deny arm was reaching for is real but belongs elsewhere: under
// ReadOnlyACL no middleware attaches a read gate, so readGateFromContext hands
// back nopReadGate, whose HoldsPermission returns true unconditionally
// (RR-CWWJGW). Falling THROUGH to the gate would therefore show gated entries
// while looking like it had checked something. The explicit arm above is what
// prevents that: the answer is the same, but it is reached deliberately rather
// than by an accident that would keep working if the gate's behavior changed.
//
// The switch is closed by construction: an ACL implementation nobody taught
// this function about hides gated entries rather than showing them. Both value
// and face forms of the nop/read-only types are matched, because their
// AuthorizeWrite has a value receiver — matching only the value form would
// drop a `&acl.ReadOnlyACL{}` into the default arm.
func permitsGatedUIElement(ctx context.Context, aclImpl acl.ACL, permission string) bool {
	if permission == "" {
		return true
	}
	// A nil ACL means the handler was wired without one. Hide, for the same
	// fail-closed reason authorizeCommand denies.
	if aclImpl == nil {
		return false
	}

	switch a := aclImpl.(type) {
	// Grouped deliberately: both mean "no policy is configured", so neither
	// can answer whether a principal holds a permission. See the read-only
	// paragraph above before splitting these apart.
	case acl.NopACL, *acl.NopACL, acl.ReadOnlyACL, *acl.ReadOnlyACL:
		return true

	case *acl.Declarative:
		if a == nil {
			return false // misconfigured policy must not fail open
		}
		return readGateFromContext(ctx).HoldsPermission(ctx, permission)

	default:
		return false
	}
}

// navEntryToSidebarItem converts a navigation entry to a sidebar item.
//
// Nil: meta accepted — only an `entities:` entry reads it, to resolve the
// type's default_sort, and without it the entry's own sort (or id order) is
// served.
func navEntryToSidebarItem(entry dataentryconfig.NavigationEntry, meta *metamodel.Metamodel) v1.SidebarItem {
	item := v1.SidebarItem{
		Label: entry.Label,
	}

	// Glyph names come from icondefs.DerivedNames, not string literals. The SPA
	// resolves whatever this emits through a generated allowlist, so a renamed
	// table entry paired with a stale literal here would make EVERY entry of
	// that kind render the fallback glyph — silently, with no build or test
	// failure. Naming them makes that rename a compile error.
	derived := dataentryconfig.DerivedIconNames
	switch {
	case entry.List != "":
		item.Href = "/list/" + entry.List
		item.Icon = derived.List
	case entry.Kanban != "":
		item.Href = "/kanban/" + entry.Kanban
		item.Icon = derived.Kanban
	case entry.Calendar != "":
		item.Href = "/calendar/" + entry.Calendar
		item.Icon = derived.Calendar
	case entry.Gantt != "":
		item.Href = "/gantt/" + entry.Gantt
		item.Icon = derived.Gantt
	case entry.Dashboard:
		item.Href = "/"
		item.Icon = derived.Dashboard
	case entry.Search:
		item.Href = "/search"
		item.Icon = derived.Search
	case entry.Settings:
		item.Href = "/settings"
		item.Icon = derived.Settings
	case entry.Document != "":
		// Standalone documents only — validateNavEntry rejects an
		// entity-anchored document here, since this href has no entity id
		// segment to fill.
		item.Href = "/document/" + entry.Document
		item.Icon = derived.Document
	case entry.Entities != "":
		// Href stays empty: the SPA expands this into one link per row it
		// reads from the list endpoint.
		item.Entities = &v1.SidebarEntities{
			Type:       entry.Entities,
			QueryScope: entry.QueryScope,
			Sort:       dataentryconfig.SortParam(dataentryconfig.EffectiveNavSort(entry, meta)),
		}
		item.Icon = derived.Entity
	case entry.Action != "":
		item.Action = entry.Action
		// Href stays empty — frontend renders this as a button.
		//
		// An action DOES derive a glyph now. It used to derive none, which was
		// harmless while `icon:` could only override — but `icon: none` needs
		// something to fall back to when the collapsed sidebar hides labels,
		// and an empty fallback resolved to the generic document glyph. That
		// made a button which fires a mutation look like a link to a document,
		// sitting in a column of real document links.
		item.Icon = derived.Action
	}

	// An authored icon wins over the kind-derived default set above. Applied
	// after the switch so it covers every branch — including `action`, which
	// derives no icon of its own and would otherwise be the one entry kind
	// that could never have one.
	//
	// dataentryconfig.NoIcon travels to the client AS ITSELF rather than being
	// flattened to "". The field is `json:"icon,omitempty"`, so an empty string
	// is dropped from the payload entirely and becomes indistinguishable from
	// an entry that never had an icon — which is also what an unmatched switch
	// leaves behind. The client needs to tell "the author asked for no glyph"
	// apart from "no glyph was ever chosen", because only the first reserves
	// the icon column.
	if entry.Icon != "" {
		// Keep what the kind derived, for the collapsed sidebar to fall back on.
		// Only when suppressed: otherwise this would duplicate Icon on every
		// entry in the payload for no reader.
		if entry.Icon == dataentryconfig.NoIcon {
			item.DerivedIcon = item.Icon
		}
		item.Icon = entry.Icon
	}

	return item
}

// handleV1Views handles GET /api/v1/_views/{entityType}/{entityId}.
// Returns JSON with executed view data including entry and sections.
//
// View configs are looked up by entry.type. When no explicit ViewConfig
// is registered for entityType, a default is synthesized from the
// metamodel (see buildDefaultViewConfig) and executed through the same
// pipeline so the response shape is identical.
//
//nolint:gocognit,funlen // routes the views sub-API over method and path shape; each branch is a distinct view endpoint, not shared logic to factor out.
func (h *viewsHandler) handleV1Views(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}

	// Parse path: /api/v1/_views/{entityType}/{entityId}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/_views/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		writeV1Error(w, r, http.StatusBadRequest, "invalid_path", "Path must be /_views/{entityType}/{entityId}", "")
		return
	}

	entityType, entityID := parts[0], parts[1]
	s := h.schema()

	if _, ok := s.Meta.GetEntityDef(entityType); !ok {
		writeV1Error(w, r, http.StatusNotFound, "entity_type_not_found", "Entity type not found", entityType)
		return
	}

	// The id segment is an ADDRESS (`ID` or `ID@face`), parsed once here so
	// the row gate below sees the BARE id (it is face-blind by design and
	// matches nothing on a suffixed string) and the engine sees the face.
	ref, ok := parseEntityRef(entityID)
	if !ok {
		writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
		return
	}

	// ACL gate (TKT-BNX2PN): _views is an entity-read chokepoint just like
	// GET /{plural}/{id} — it serves _title, properties, and content body via
	// executeView + serializeEntityForWire. Gate BEFORE executeView so a hidden
	// id is indistinguishable from a missing one (404, no oracle) and the view
	// pipeline never runs for a denied principal.
	//
	// This gates only the ENTRY. The view's TRAVERSAL is separately
	// source-gated in loadViewEntities (BUG-9Z20WH), so a hidden intermediary
	// can neither be collected nor serve as a stepping-stone to a descendant
	// reachable only through it. Together the two gates make _views an
	// entity-read chokepoint for the entry AND for every entity the traversal
	// surfaces.
	if !h.gateRead(w, r, entityType, ref.ID) {
		return
	}

	viewCfg, ok := findViewByEntityType(s.Cfg.Views, entityType)
	if !ok {
		viewCfg, ok = buildDefaultViewConfig(s.Meta, entityType)
		if !ok {
			// Cannot happen — entity type already validated above —
			// but handled defensively to keep the contract clear.
			writeV1Error(w, r, http.StatusNotFound, "entity_type_not_found", "Entity type not found", entityType)
			return
		}
	}

	// Execute view. This is the ONE surface that opts into worlds
	// (TKT-WRLDAPI item 4b); the other two executeView callers pass
	// defaultViewWorld() explicitly. See the viewWorld doc for why the world
	// is a parameter rather than something the engine reads off ctx.
	result, err := h.executeViewRef(r.Context(), viewCfg, ref,
		viewWorldFromRequest(r.Context()))
	if errors.Is(err, errNoFaceInWorld) {
		// The entity EXISTS and this caller may read it; it simply has no face
		// in the world they asked for — the ordinary state of an unpublished
		// draft. Answer with the face that does exist plus a marker, so the
		// page can offer a way through rather than a dead end (BUG-1).
		h.writeWorldAbsentView(w, r, entityType, ref.ID)
		return
	}
	if err != nil {
		writeV1Error(w, r, http.StatusUnprocessableEntity, "view_execution_failed", "View execution failed", err.Error())
		return
	}

	// Build sections
	sections := h.buildSections(r.Context(), viewCfg.Sections, result)

	// The opt-in create affordance (TKT-R4BMJM). Runs on every section, but
	// only ever populates one whose config carries an explicit `create:` block,
	// so a view that did not opt in stays exactly as read-only as TKT-651W made
	// it. Note this is NOT resolveSectionButtonsWithTraverse, which also builds
	// an ungated link-existing affordance the view path must not carry.
	h.resolveSectionCreate(r.Context(), viewCfg, sections, result.Entry)

	// Build response
	entityDef := s.Meta.Entities[result.Entry.Type]
	plural := entityDef.GetPlural(result.Entry.Type)

	// The entry's relations: the edges OF THE FACE BEING SERVED, via the
	// shared seam every other surface uses (see [servedFaceEdges]).
	//
	// This replaces two behaviors that were both wrong in the same
	// direction. Under the DEFAULT world it used the bare-id reader, which
	// returns the UNION of every face's content-scoped edges — so a draft
	// entry carried the published face's links, duplicated where they
	// overlapped. Under a world it emitted NOTHING, as a deliberate
	// placeholder, which showed a resolved entry with no links at all.
	//
	// One face-scoped read answers both: the entry knows its own face, so
	// its edges are well-defined whether or not a world was named.
	entryRels, werr := h.faceEdges(r.Context(), result.Entry)
	if werr != nil {
		// A neighbor-resolution fault is an infrastructure failure, not an
		// empty link set — the same posture the entity GET takes (RR-4TFZNL).
		writeGateError(w, r, werr)
		return
	}
	resp := v1.ViewResponse{
		Entry:    h.serializer.forWire(r.Context(), result.Entry, entryRels, h.schema().Meta, plural),
		Sections: make([]v1.ViewSection, 0, len(sections)),
		Create:   sectionCreateMenuToV1(headerCreateMenu(sections)),
	}

	for _, sec := range sections {
		v1Sec := v1.ViewSection{
			Heading:      sec.Heading,
			SectionID:    sec.SectionID,
			Display:      sec.Display,
			IsEmpty:      sec.IsEmpty,
			EmptyMessage: sec.EmptyMessage,
			IsGrouped:    sec.IsGrouped,
			Content:      sec.Content,
			HasContent:   sec.HasContent,
		}

		// The opt-in create affordance. Nil for every section that did not
		// declare `create:`, which keeps the response byte-identical there.
		v1Sec.Create = sectionCreateToV1(sec.CreateInfo)

		// Convert fields
		for _, f := range sec.Fields {
			v1Sec.Fields = append(v1Sec.Fields, v1.SectionField(f))
		}

		// Convert entities
		for _, e := range sec.Entities {
			v1Sec.Entities = append(v1Sec.Entities, sectionEntityToV1(e))
		}

		// Convert columns
		for _, col := range sec.Columns {
			v1Sec.Columns = append(v1Sec.Columns, v1.ViewColumn{
				Property: col.Property,
				Label:    col.Label,
				Relation: col.Relation,
				Link:     col.Link,
			})
		}

		// Convert rows
		for _, row := range sec.Rows {
			v1Row := v1.ViewRow{
				EntityID:   row.EntityID,
				EntityType: row.EntityType,
				EditFormID: row.EditFormID,
				Content:    row.Content,
				Self:       row.Self,
			}
			for _, cell := range row.Cells {
				v1Row.Cells = append(v1Row.Cells, v1.ViewCell(cell))
			}
			v1Sec.Rows = append(v1Sec.Rows, v1Row)
		}

		// Convert groups
		for _, grp := range sec.Groups {
			v1Grp := v1.ViewGroup{
				GroupName: grp.GroupName,
			}
			for _, row := range grp.Rows {
				v1Row := v1.ViewRow{
					EntityID:   row.EntityID,
					EntityType: row.EntityType,
					EditFormID: row.EditFormID,
					Content:    row.Content,
				}
				for _, cell := range row.Cells {
					v1Row.Cells = append(v1Row.Cells, v1.ViewCell(cell))
				}
				v1Grp.Rows = append(v1Grp.Rows, v1Row)
			}
			for _, e := range grp.Entities {
				v1Grp.Entities = append(v1Grp.Entities, sectionEntityToV1(e))
			}
			v1Sec.Groups = append(v1Sec.Groups, v1Grp)
		}

		// Convert the nested tree
		v1Sec.Truncated = sec.Truncated
		for _, node := range sec.Tree {
			v1Sec.Tree = append(v1Sec.Tree, sectionTreeNodeToV1(node))
		}

		resp.Sections = append(resp.Sections, v1Sec)
	}

	resp.Mentions = collectMentions(
		r.Context(), h.store, h.viewReader, s.Meta, viewContentBlobs(result.Entry, sections)...)

	writeV1JSON(w, http.StatusOK, resp)
}

// viewContentBlobs gathers every markdown body that will be rendered by
// the SPA for a single view response: the entry's content, every section's
// own content, and every entity card's content (sections with display
// "content"/"cards" surface related entities, each carrying its own
// `Content` markdown that EntityDetail.vue renders with the same
// `refResolver`). Used to scope the mentions scan to text the user
// actually sees on this screen.
func viewContentBlobs(entry *entityPkg.Entity, sections []SectionData) []string {
	blobs := make([]string, 0, 1+len(sections))
	if entry != nil && entry.Content != "" {
		blobs = append(blobs, entry.Content)
	}
	for _, sec := range sections {
		if sec.HasContent && sec.Content != "" {
			blobs = append(blobs, sec.Content)
		}
		for _, ent := range sec.Entities {
			if ent.HasContent && ent.Content != "" {
				blobs = append(blobs, ent.Content)
			}
		}
		for _, grp := range sec.Groups {
			for _, ent := range grp.Entities {
				if ent.HasContent && ent.Content != "" {
					blobs = append(blobs, ent.Content)
				}
			}
		}
	}
	return blobs
}

// editFormForType returns the first edit form ID configured for the given entity type,
// or "" if no edit form is found. Forms with explicit mode="edit" are preferred.
func (h *viewsHandler) editFormForType(entityType string) string {
	s := h.schema()
	ids := make([]string, 0, len(s.Cfg.Forms))
	for id := range s.Cfg.Forms {
		ids = append(ids, id)
	}
	natsort.Strings(ids)
	// First pass: look for explicit edit mode
	for _, id := range ids {
		f := s.Cfg.Forms[id]
		if f.EntityType == entityType && f.Mode == "edit" {
			return id
		}
	}
	// Second pass: fall back to forms with no mode specified
	for _, id := range ids {
		f := s.Cfg.Forms[id]
		if f.EntityType == entityType && f.Mode == "" {
			return id
		}
	}
	return ""
}

// inlineCreateForms maps each entity type the principal may create inline to
// the form id to create it with. A type is present only when BOTH conditions of
// TKT-OMUD56 hold — a create form resolves for it, and the ACL permits
// `create` — so presence alone is the affordance and the SPA performs no
// permission arithmetic of its own.
//
// The form lookup runs first because it is a pure config read: a type nothing
// can create has no ACL question worth asking, so this also keeps the ACL
// evaluations to the types that could actually be offered.
//
// The `create` verdict is the same OpCreate question computeCollectionActions
// asks for a list response, so a type's verdict here cannot diverge from the
// one on GET /api/v1/{plural}. UI hint only: the write endpoint re-authorizes
// (affordances_contract_test.go pins that invariant).
//
// This loops over entity types, so it resolves the principal's Request ONCE and
// reuses it. Going through the top-level ACL entry point per type would rebuild
// that scope every iteration — and role resolution walks `member-of` through
// the store, so an N-type metamodel would pay N graph traversals on every app
// load. Reusing the scope is exactly what it exists for.
func (h *viewsHandler) inlineCreateForms(ctx context.Context) map[string]string {
	s := h.schema()

	// The middleware attaches a per-request scope; fall back to the unscoped
	// path when one is absent (tests and any non-HTTP caller), which is
	// correct-but-slower rather than a different answer.
	scope := acl.FromContext(ctx)
	mayCreate := func(entityType string) bool {
		req := translateVerb("create", entityType, "", "")
		if scope != nil {
			return scope.AuthorizeWrite(ctx, req).Allow
		}
		return h.currentACL().AuthorizeWrite(ctx, req).Allow
	}

	var out map[string]string
	for name := range s.Meta.Entities {
		// Form lookup first: it is a pure config read, so a type nothing can
		// create never costs an authorization.
		formID := h.createFormForType(name)
		if formID == "" {
			continue
		}
		if !mayCreate(name) {
			continue
		}
		if out == nil {
			out = make(map[string]string)
		}
		out[name] = formID
	}
	return out
}

// createFormForType returns the first form ID that can be used to create an entity
// of the given type. It prefers forms with mode "create" or unset, but falls back
// to edit-mode forms (which work for creation when no entity ID is provided).
func (h *viewsHandler) createFormForType(entityType string) string {
	s := h.schema()
	ids := make([]string, 0, len(s.Cfg.Forms))
	for id := range s.Cfg.Forms {
		ids = append(ids, id)
	}
	natsort.Strings(ids)
	fallback := ""
	for _, id := range ids {
		f := s.Cfg.Forms[id]
		if f.EntityType != entityType {
			continue
		}
		if f.Mode != "edit" {
			return id
		}
		if fallback == "" {
			fallback = id
		}
	}
	return fallback
}

// resolveLinkTarget resolves a link configuration value to a URL.
// Supported values:
//   - "" or empty: no link (returns "")
//   - "detail": link to entity detail view (/entity/{type}/{id})
//   - "document/<name>": link to document preview (/document/<name>/{id})
func resolveLinkTarget(link, entityType, entityID string) string {
	switch {
	case link == "":
		return ""
	case link == "detail":
		return "/entity/" + entityType + "/" + entityID
	case strings.HasPrefix(link, "document/"):
		docName := strings.TrimPrefix(link, "document/")
		return "/document/" + docName + "/" + entityID
	default:
		return ""
	}
}

func findListByEntityType(s *Schema, entries []NavigationEntry, entityType string) string {
	for _, nav := range entries {
		if nav.IsGroup() {
			if found := findListByEntityType(s, nav.Items, entityType); found != "" {
				return found
			}
			continue
		}
		if list, ok := s.Cfg.Lists[nav.List]; ok && list.EntityType == entityType {
			return nav.List
		}
	}
	return ""
}

// resolveRelationColumns returns, for every row and every relation column,
// the display titles of that row's related entities: result[rowID][columnIndex].
// Property columns have no entry.
//
// It is batched per section (TKT-1U8XYN): one relation query per (column,
// row type) — the row entity's own type anchors the direction inference, since
// a section's rows come from a traversal and the type is only known per row —
// then ONE content-free header read for every target of every column. The
// former shape was one relation query plus one entity load per row per
// column, which put a project view with a 40-row work table at over a
// thousand statements.
//
// Targets are gated and redacted through the same viewReader seam as before
// (DEC-ZBI39P), now over headers: a neighbor the principal may not read is
// dropped, and a survivor whose display property is hidden falls back to its
// id (BUG-R9EHKV). A viewReader without the header capability degrades to the
// whole-entity Filter over the same batch — strictly more data, never less
// gating.
func (h *viewsHandler) resolveRelationColumns(
	ctx context.Context, s *Schema, columns []dataentryconfig.ListColumn, rows []*entityPkg.Entity,
) map[string]map[int][]string {
	svc := h.services()
	targets, targetIDs := h.relationColumnTargets(ctx, svc, s, columns, rows)
	if len(targetIDs) == 0 {
		return targets
	}

	titles := h.visibleTitles(ctx, svc, targetIDs)
	out := make(map[string]map[int][]string, len(targets))
	for rowID, cols := range targets {
		out[rowID] = make(map[int][]string, len(cols))
		for ci, ids := range cols {
			vals := make([]string, 0, len(ids))
			for _, id := range ids {
				if title, ok := titles[id]; ok {
					vals = append(vals, title)
				}
			}
			out[rowID][ci] = vals
		}
	}
	return out
}

// relationColumnTargets runs one relation query per (relation column, row
// type) and returns targets[rowID][columnIndex] as the ordered neighbor ids
// of that cell, plus the distinct neighbor ids in first-seen order.
func (h *viewsHandler) relationColumnTargets(
	ctx context.Context, svc Services, s *Schema, columns []dataentryconfig.ListColumn, rows []*entityPkg.Entity,
) (targets map[string]map[int][]string, targetIDs []string) {
	byType := make(map[string][]string)
	for _, e := range rows {
		byType[e.Type] = append(byType[e.Type], e.ID)
	}
	targets = make(map[string]map[int][]string, len(rows))
	seenTarget := make(map[string]struct{})
	for ci, col := range columns {
		if col.Relation == "" {
			continue
		}
		for typ, ids := range byType {
			dir := resolveConfigDirection(s, typ, col.Relation, col.Direction)
			q := store.RelationQuery{EntityIDs: ids, Type: col.Relation, Direction: relationDirection(dir)}
			for r, err := range svc.Store.ListRelations(ctx, q) {
				if err != nil {
					// A view is a presentation surface; a section renders what it
					// could load, but never quietly — the operator sees the fault.
					slog.Warn("dataentry: view section relation column truncated; store read failed",
						"relation", col.Relation, "type", typ, "rows", len(ids), "err", err)
					break
				}
				rowID, targetID := r.From, r.To
				if dir.IsIncoming() {
					rowID, targetID = r.To, r.From
				}
				if targets[rowID] == nil {
					targets[rowID] = make(map[int][]string)
				}
				targets[rowID][ci] = append(targets[rowID][ci], targetID)
				if _, dup := seenTarget[targetID]; !dup {
					seenTarget[targetID] = struct{}{}
					targetIDs = append(targetIDs, targetID)
				}
			}
		}
	}
	return targets, targetIDs
}

// visibleTitles resolves ids to display titles for the ids the principal may
// read, in one header batch gated through the viewReader; ids the gate drops
// (or the store no longer has) are absent from the result.
func (h *viewsHandler) visibleTitles(ctx context.Context, svc Services, ids []string) map[string]string {
	var headers []store.EntityHeader
	for hd, err := range store.ListEntityHeaders(ctx, svc.Store, store.EntityQuery{IDs: ids}) {
		if err != nil {
			slog.Warn("dataentry: view section relation titles dropped; header read failed",
				"targets", len(ids), "err", err)
			return map[string]string{}
		}
		headers = append(headers, hd)
	}
	var visible []store.EntityHeader
	if hf, ok := h.viewReader.(visibility.HeaderFilterer); ok {
		visible = hf.FilterHeaders(ctx, headers)
	} else {
		// No header capability: gate the same batch as whole entities.
		ents := make([]*entityPkg.Entity, 0, len(headers))
		for e, err := range svc.Store.ListEntities(ctx, store.EntityQuery{IDs: ids}) {
			if err != nil {
				slog.Warn("dataentry: view section relation titles dropped; entity read failed",
					"targets", len(ids), "err", err)
				return map[string]string{}
			}
			ents = append(ents, e)
		}
		for _, e := range h.viewReader.Filter(ctx, ents) {
			visible = append(visible, store.HeaderOf(e))
		}
	}
	titles := make(map[string]string, len(visible))
	for _, hd := range visible {
		titles[hd.ID] = svc.Meta.DisplayTitle(hd.ID, hd.Type, hd.Properties)
	}
	return titles
}
