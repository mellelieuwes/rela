package dataentry

import (
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

// appearanceHandler owns the theme/settings/palette surface: the
// /_theme/logo CRUD (handlers_theme.go), the /_theme/export and
// /_theme/import package endpoints (handlers_theme_package.go), and the
// /_settings and /_palette CRUD (settings_handlers.go). Extracted from
// App (TKT-8AJ1PM, part of the TKT-R68TV8 decomposition arc).
//
// Collaborator rationale mirrors viewsHandler/writeHandler: fixed
// service handles by value (logo/palette/settings are self-synchronized
// services set once per wiring and never swapped by tests), the schema
// snapshot and Services bundle as closures so a live config reload
// propagates, and the same viewReader seam the App used so the settings
// page's relation-default candidates stay row-gated + field-redacted
// (DEC-ZBI39P). Every write here lands on a self-synchronized service —
// no entitymanager.
type appearanceHandler struct {
	schema func() *Schema
	// services returns the read bundle; the settings page's
	// relation-default candidate lookup reads through it exactly as the
	// App method did.
	services func() Services
	// logo owns the user-uploaded sidebar logo (persistence + served cache).
	logo *logoStore
	// palette owns the user palette override + resolved palette.
	palette *paletteService
	// settings owns the per-user default values.
	settings *settingsService
	// viewReader is the row-gating + field-redacting read-out seam
	// (DEC-ZBI39P) the settings page routes relation-target candidates
	// through, so an unreadable target is dropped and a hidden display
	// property never leaks into a picker title (BUG-R9EHKV).
	viewReader visibility.Reader
}

// newAppearanceHandler wires the handler over the App's current
// collaborators. Called from NewApp and from the test rebind path after
// the logo/palette/settings services and viewReader are set, so the
// fixed handles are the same instances the App holds.
func newAppearanceHandler(app *App) *appearanceHandler {
	return &appearanceHandler{
		schema:     app.State,
		services:   app.Services,
		logo:       app.logo,
		palette:    app.palette,
		settings:   app.settings,
		viewReader: app.viewReader,
	}
}
