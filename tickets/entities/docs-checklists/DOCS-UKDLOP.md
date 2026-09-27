---
id: DOCS-UKDLOP
type: docs-checklist
title: 'Docs: related() constraints match the final entity id and current_user.id'
status: done
---

## Code Documentation

- [x] Comments where logic isn't obvious: TraversalSpec.Bind, BindTraversal, the
Endpoints empty-set refusal in acl.lowerTraversal, and relresolve's
bind-before-key all say why they fail closed
- [x] Function/type docs if public API: VarRef, ConstraintID, TraversalSpec.ID /
Refs / Bind, predicatefns.BindTraversal, acl.TraversalHop.EndpointIDs

## Project Documentation

- [x] ~~README updated~~ (N/A: no README content covers related() constraints)
- [x] ~~CLAUDE.md updated~~ (N/A: no new pattern; the feature reuses the
existing per-request identity binding)
- [x] ~~Help text accurate~~ (N/A: no CLI changes)
- [x] docs/metamodel.md: "Filtering on related entities" documents the id key,
current_user.id values, a mijn example, identity rules, the ACL when: refusal,
the index note for id, and the pushable list
- [x] docs/data-entry.md: next-action current_user section lists the related()
form

## External Documentation

- [x] ~~Changelog entry added~~ (N/A: the repo keeps no changelog file; release
notes come from PRs)
- [x] ~~API docs updated~~ (N/A: no API shape change)
