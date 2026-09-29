---
id: RR-777AJW
type: review-response
title: 'Design review: Put upsert lets Link silently steal another entity''s twin'
finding: 'The store is keyed by (System, ExternalID), and Put is an upsert whose only duplicate check is on (Target.ID, System) (design:164-169). Linking BUG-1 to `github 12` while SC-1 is already linked to `github 12` overwrites SC-1''s twin. SC-1 silently loses its twin, its base and its ownership enforcement. The same key also collides whenever one system id is used by pacts with different scopes (issue #12 in two repos; the design names GitHub). On macOS''s default case-insensitive filesystem, `ABC.yaml` and `abc.yaml` are one file, so filetwins aliases external ids that differ only by case. Fix: Link must fail with a new ErrExternalIDTaken when (system, externalID) exists. Split Store.Put into Create and Update, or add a precondition. Document that external ids must be unique per system (the agent can namespace, e.g. `repo.12`). Either restrict ids to lowercase or encode case in file names. Add all of these to twinstest.'
severity: significant
resolution: Store.Put is split into Create (ErrExternalIDTaken, ErrDuplicateTarget for a live twin of the same entity in that system) and Update; filetwins encodes upper-case letters (^x) so ids differing by case never alias on case-insensitive filesystems. twinstest covers each.
status: addressed
---
