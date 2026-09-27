package dataentry

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	v1 "github.com/Sourcehaven-BV/rela/internal/apiwire/v1"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/twins"
)

// twinsPathPrefix is the reserved route segment for the twins read-out.
const twinsPathPrefix = "/api/v1/_twins/"

// twinsHandler owns the twins read-out route and answers field ownership for
// the affordance service.
//
// Extracted from App (the commentsHandler pattern) to keep App under its
// plimsoll method load line. Twins are edited only through `rela twin`; the
// data-entry app shows them and enforces what they own, nothing more.
type twinsHandler struct {
	// svc is nil when the schema declares no pact. Nil IS the "feature
	// absent" signal: the route 404s and nothing is owned.
	svc *twins.Service

	visibleReader visibleReader
	// hidden names the properties the request principal may not see on an
	// entity (field-level `visible:`), for redacting finding values.
	hidden func(ctx context.Context, e *entity.Entity) map[string]struct{}
}

// newTwinsHandler builds the handler over app's collaborators. A handler is
// returned even without twins, so the route stays registered and answers a
// JSON 404 rather than the stdlib's unregistered-route page.
func newTwinsHandler(app *App) *twinsHandler {
	return &twinsHandler{
		visibleReader: app.visibleReader,
		hidden: func(ctx context.Context, e *entity.Entity) map[string]struct{} {
			return app.affordances.hiddenProperties(ctx, e)
		},
	}
}

// SetTwins installs the twins service. A package function rather than an App
// method: App is at its plimsoll cap for exported methods.
//
// Nil: accepted — it means no pact is declared, so the `_twins` route 404s and
// no field is externally owned.
func SetTwins(a *App, svc *twins.Service) { a.twins.svc = svc }

// twinOwnedFields adapts app's twins handler to the affordance service's
// ownership lookup. Late-bound through app, so SetTwins (called after NewApp)
// and a test rebind of the handler are both picked up.
func twinOwnedFields(app *App) ownedFieldsFunc {
	return func(ctx context.Context, entityType, entityID string) (map[string][]string, error) {
		if app.twins == nil || app.twins.svc == nil {
			return nil, nil
		}
		return app.twins.svc.OwnedFields(ctx, entityType, entityID)
	}
}

// handleV1Twins serves `GET /api/v1/_twins/{type}/{id}`: the entity's twins,
// sorted by system.
//
// # Gating
//
// The entity goes through the visibility reader first, so a caller who cannot
// read it — and an id that does not exist — gets the same 404: a twin list
// must not become an existence oracle. A twin's finding carries field values
// (base, ours, theirs); for a field the caller may not see they are dropped,
// the field name stays (names are schema, values are data).
//
// # Disabled is absent, not forbidden
//
// With no pact declared the service is nil and the route 404s, like comments.
func (h *twinsHandler) handleV1Twins(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeV1Error(w, r, http.StatusNotFound, "not_found", "Not found", "")
		return
	}
	switch r.Method {
	case http.MethodGet:
	case http.MethodOptions:
		w.Header().Set("Allow", "GET, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		w.Header().Set("Allow", "GET, OPTIONS")
		writeV1Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}

	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, twinsPathPrefix), "/"), "/")
	if len(parts) != 2 || !isSafePathSegment(parts[0]) || !isSafePathSegment(parts[1]) {
		writeV1Error(w, r, http.StatusBadRequest, "invalid_path", "Path must be /_twins/{type}/{id}", "")
		return
	}
	typeName, entityID := parts[0], parts[1]

	ctx := r.Context()
	ent, found, err := h.visibleReader.getVisible(ctx, typeName, entityID)
	if err != nil {
		writeGateError(w, r, err)
		return
	}
	if !found || ent.Type != typeName {
		writeV1Error(w, r, http.StatusNotFound, "not_found", entityNotFoundTitle, "")
		return
	}

	out, err := h.twinsOf(ctx, ent)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		slog.Warn("dataentry: reading twins failed", "entity", ent.ID, "err", err)
		writeV1Error(w, r, http.StatusInternalServerError, "twins_failed", "Could not read twins", "")
		return
	}
	writeV1JSON(w, http.StatusOK, out)
}

// twinsOf builds the wire list for a readable entity.
func (h *twinsHandler) twinsOf(ctx context.Context, ent *entity.Entity) ([]v1.Twin, error) {
	list, err := h.svc.ForEntity(ctx, ent.ID)
	if err != nil {
		return nil, err
	}
	owned, err := h.svc.OwnedFields(ctx, ent.Type, ent.ID)
	if err != nil {
		return nil, err
	}
	hidden := h.hidden(ctx, ent)
	out := make([]v1.Twin, 0, len(list))
	for _, tw := range list {
		out = append(out, v1.Twin{
			System:          tw.System,
			ExternalID:      tw.ExternalID,
			URL:             tw.URL,
			State:           string(tw.State),
			HasBase:         tw.HasBase,
			SyncedAt:        wireTime(tw.SyncedAt),
			RemoteUpdatedAt: wireTime(tw.RemoteUpdatedAt),
			OwnedFields:     ownedBy(owned, tw.System),
			Findings:        wireFindings(tw.Findings, hidden),
		})
	}
	return out, nil
}

// ownedBy lists the fields of owned that system owns, sorted. A gone twin, or
// one whose pact the schema no longer declares, owns nothing.
func ownedBy(owned map[string][]string, system string) []string {
	out := []string{}
	for field, systems := range owned {
		if slices.Contains(systems, system) {
			out = append(out, field)
		}
	}
	slices.Sort(out)
	return out
}

// wireFindings projects findings onto the wire, dropping the values of every
// finding about a field in hidden.
//
// A rejection's message is the refused write's error, which can quote a
// value, so it is dropped with the values. A rejection that names no field
// cannot be attributed, so its message is dropped whenever the caller has any
// hidden field — failing closed rather than guessing what it quotes.
func wireFindings(findings []twins.Finding, hidden map[string]struct{}) []v1.TwinFinding {
	out := make([]v1.TwinFinding, 0, len(findings))
	for _, f := range findings {
		wf := v1.TwinFinding{Field: f.Field, Kind: string(f.Kind), Propose: f.Propose}
		_, fieldHidden := hidden[f.Field]
		switch {
		case fieldHidden:
		case f.Field == "" && len(hidden) > 0:
			wf.Base, wf.Ours, wf.Theirs = f.Base, f.Ours, f.Theirs
		default:
			wf.Base, wf.Ours, wf.Theirs, wf.Message = f.Base, f.Ours, f.Theirs, f.Message
		}
		out = append(out, wf)
	}
	return out
}

// wireTime renders t as RFC 3339 in UTC, or "" (omitted) when zero.
func wireTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
