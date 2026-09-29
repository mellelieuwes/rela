---
id: RR-8DGO7V
type: review-response
title: 'Design review: Deletes and renames that skip the AliasRewriter leave twins pointing at nothing'
finding: 'EntityDeleted and EntityRenamed only fire from Manager.DeleteEntity and RenameEntity, after commit, and failures are only logged (entitymanager/alias_hook.go:49-75). Automation replace-deletes go straight to the store and never notify (cascadehost.go:197-252). The same goes for fs hand edits and git pulls (fsstore/watcher.go) and `rela sync` ApplyEntity. Those twins stay in_sync or pending for a missing entity, and Pull/Pushed then fail with not-found. A failed Retarget leaves the twin on the old id, so the guard fails open on the renamed entity. Fix: Pending, Pull, Pushed and Show must treat entity-not-found as gone (mark and persist it). Document the fail-open window after a failed rename. Add a pending test that deletes the entity through the store.'
severity: significant
resolution: Pending, pull, pushed and show treat a missing entity as gone and persist that; gone twins no longer block re-linking; link refuses git-crypt-locked entities; the window after a failed retarget is documented in the guide.
status: addressed
---
