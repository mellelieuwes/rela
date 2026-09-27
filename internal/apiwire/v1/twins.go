package v1

// Twin is one entity's counterpart in an external system, as served by
// `GET /api/v1/_twins/{type}/{id}` (a JSON array of these, sorted by system).
//
// Twins are edited only through `rela twin`; this is a read-out. Timestamps
// are RFC 3339 and omitted when the twin has never synced.
type Twin struct {
	System          string `json:"system"`
	ExternalID      string `json:"external_id"`
	URL             string `json:"url"`
	State           string `json:"state"` // in_sync | pending | conflict | gone
	HasBase         bool   `json:"has_base"`
	SyncedAt        string `json:"synced_at,omitempty"`
	RemoteUpdatedAt string `json:"remote_updated_at,omitempty"`
	// OwnedFields names the fields this twin's system owns on the entity
	// (the pact's `theirs`; "*" for every field, "body" for the content).
	// Empty for a gone twin, which owns nothing. Sorted.
	OwnedFields []string      `json:"owned_fields"`
	Findings    []TwinFinding `json:"findings"`
}

// TwinFinding is one field the last reconcile could not settle on its own.
//
// Base, Ours and Theirs are null when the caller may not see the field
// (field-level `visible:` policy): the field name stays, its values never
// reach the wire.
type TwinFinding struct {
	Field   string `json:"field"`
	Kind    string `json:"kind"` // conflict | foreign_edit | local_drift | rejected
	Base    any    `json:"base"`
	Ours    any    `json:"ours"`
	Theirs  any    `json:"theirs"`
	Propose bool   `json:"propose"`
	Message string `json:"message,omitempty"`
}
