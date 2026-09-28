package affordances

import "context"

// historicalSubjectKey marks a context as resolving a HISTORICAL entity
// snapshot (a version read of a possibly-deleted or drifted entity) rather
// than the live entity. See [WithHistoricalSubject].
type historicalSubjectKey struct{}

// WithHistoricalSubject marks ctx as resolving field visibility for a
// historical snapshot (TKT-73C6B2). The live store no longer holds the entity's
// as-of-version subject-world state, so trusting it would let a `visible:` grant
// that was DENIED at write time flip OPEN for a deleted/drifted entity and leak
// a field. Under this marker the resolver neuters every subject-world input:
//
//   - has_relation / count_relations (via outgoingCounts) resolve as NO edges,
//     so a grant conditioned on them evaluates false and the field FAILS CLOSED.
//   - the effective role set is reduced to GLOBALS-ONLY (resolveViaDeclarative):
//     local roles conferred by live `role_relations` edges and ancestor roles
//     from `inherit_roles_through` are dropped, since they too come from the
//     live graph and would otherwise let a role conferred AFTER capture both
//     select a `visible:` block and satisfy has_role.
//   - to stop that reduced role set from silently defaulting to all-visible, a
//     type gated by `visible:` gets a type-level closed-world in history
//     (FieldVerdicts): a field is shown only if a globally-held role
//     affirmatively grants it visible.
//
// Reader-side inputs stay LIVE — current_user and the reader's global roles are
// assignment-based, not subject-world, so per-reader redaction still applies and
// a reader who gains a global role gains historical visibility (intended).
//
// The history read path sets this before serializing a snapshot; a reader
// holding acl.PermHistoryReadRedacted bypasses redaction entirely at the handler
// (OVERRIDE reveal), so this marker only governs the fail-closed default for
// ordinary readers.
func WithHistoricalSubject(ctx context.Context) context.Context {
	return context.WithValue(ctx, historicalSubjectKey{}, true)
}

// IsHistoricalSubject reports whether ctx was marked by [WithHistoricalSubject].
func IsHistoricalSubject(ctx context.Context) bool {
	v, _ := ctx.Value(historicalSubjectKey{}).(bool)
	return v
}
