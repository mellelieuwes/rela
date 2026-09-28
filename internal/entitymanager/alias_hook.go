package entitymanager

import (
	"context"
	"log/slog"

	"github.com/Sourcehaven-BV/rela/internal/entity"
)

// AliasRewriter is notified when an entity's identity changes, so a subsystem
// that stores references BY ENTITY ID can rewrite them.
//
// Declared here at the consumer rather than next to its implementation
// (internal/caldavalias): Manager depends on exactly the two methods it calls.
// The wiring site supplies a CalDAV alias service when one is configured, and
// leaves this nil otherwise.
//
// # Why a hook and not an observer
//
// [store.EntityObserver.EntityRenamed] carries the same information, but every
// store fires it as `_ = o.EntityRenamed(...)` — the error is discarded, so a
// failed rewrite is silent. That is the right trade for a search index, which
// can be rebuilt. It is the wrong trade here: a lost CalDAV alias makes a
// client re-create the resource as a NEW entity, silently duplicating a user's
// to-do with no signal anywhere.
//
// # Best-effort, unlike the alias service itself
//
// A rewrite failure is LOGGED, not propagated: a rename that already touched
// the store cannot be unwound here, so failing the call would report an error
// for a write that did happen. The alias service returns its errors so this
// hook can surface them; the hook stops short of failing the rename. The
// residual risk — a rename that succeeds while its alias rewrite fails — leaves
// an orphaned alias that a client sees as a delete plus a create.
type AliasRewriter interface {
	// EntityRenamed rewrites references from oldID to newID.
	EntityRenamed(ctx context.Context, oldID, newID string) error
	// EntityDeleted NOTIFIES that an entity has left the graph.
	//
	// It deliberately does NOT require the implementation to drop its
	// references. The CalDAV alias service keeps them on purpose: an alias
	// pointing at a missing entity is the evidence that the entity was
	// deleted after being served, which is what lets a stale client PUT be
	// refused instead of silently resurrecting it. Dropping the reference
	// would destroy that evidence and the next write would read as a create.
	EntityDeleted(ctx context.Context, entityID string) error
	// EntityFaceDeleted NOTIFIES that one non-default face of an entity was
	// deleted while the entity itself remains.
	//
	// Separate from EntityDeleted because the scope differs: a subscriber
	// keyed per face (comments) must drop only that face's references, and
	// one keyed per entity (CalDAV) has nothing to do. Without it a face's
	// references outlive the face and resurface when a face of that name is
	// created again (BUG-R1PQY9).
	EntityFaceDeleted(ctx context.Context, entityID string, face entity.Face) error
}

// rewriteAliasesForRename notifies the alias rewriter of a completed rename.
// No-op when none is wired.
func (m *Manager) rewriteAliasesForRename(ctx context.Context, oldID, newID string) {
	if m.deps.AliasRewriter == nil {
		return
	}
	if err := m.deps.AliasRewriter.EntityRenamed(ctx, oldID, newID); err != nil {
		// Logged rather than returned: see the AliasRewriter doc. An operator
		// seeing this should expect a duplicated entry in the affected client.
		slog.Error("entitymanager: alias rewrite failed after rename; a synced client may duplicate this entry",
			"old_id", oldID, "new_id", newID, "error", err)
	}
}

// notifyAliasesOfDelete notifies the alias rewriter of a completed delete.
// No-op when none is wired.
//
// Named for notification, not removal: what the implementation does with the
// news is its own business, and the CalDAV one deliberately retains the alias.
func (m *Manager) notifyAliasesOfDelete(ctx context.Context, id string) {
	if m.deps.AliasRewriter == nil {
		return
	}
	if err := m.deps.AliasRewriter.EntityDeleted(ctx, id); err != nil {
		slog.Error("entitymanager: alias delete-notification failed",
			"id", id, "error", err)
	}
}

// notifyAliasesOfFaceDelete notifies rw of a completed face delete. No-op when
// rw is nil; failures are logged, as for a whole-entity delete.
//
// A function rather than a Manager method only to keep Manager under its
// method load line; it reads nothing else from the Manager.
func notifyAliasesOfFaceDelete(ctx context.Context, rw AliasRewriter, id string, face entity.Face) {
	if rw == nil {
		return
	}
	if err := rw.EntityFaceDeleted(ctx, id, face); err != nil {
		slog.Error("entitymanager: alias face-delete notification failed",
			"id", id, "face", face, "error", err)
	}
}
