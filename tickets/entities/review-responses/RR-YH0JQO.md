---
id: RR-YH0JQO
type: review-response
title: 'Code review: A rename left the twin pending with nothing to push'
finding: 'store.VersionOf folds in the entity id, so after `rela rename id` the entity version never equals the twin''s BaseVersion: the twin is listed as changed in rela with an empty push set until an unneeded pushed.'
severity: minor
resolution: 'EntityRenamed moves BaseVersion to the renamed entity''s version when the base was agreed at the entity''s content under the old id (EntityReader.VersionAt computes that version without importing store); a change made in rela before the rename stays pending. Tests: TestEntityRenamed_CarriesTheBaseVersion, TestTwinCLI_RenameKeepsTheTwinInSync (both fail without the fix).'
status: addressed
---
