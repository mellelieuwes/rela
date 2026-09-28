---
id: DOCS-EYVORI
type: docs-checklist
title: 'Docs: Actions on the entity detail page'
status: done
---

## Code Documentation

- [x] Comments where logic isn't obvious — `detailactions.go` explains why `when` is judged on the redacted entity, why a `when` over a hidden field fails closed, and why action keys are emitted only as true; `actions.go` explains the 409 `config_reloaded` and why the entity is gated under `writeMu`; `webhook.go` explains why entity-bound actions are refused there
- [x] Function/type docs if public API — `ActionScope`, `ActionConfirm`, `ViewConditionMatcher.EntityAttributes` and `affordances.IsHistoricalSubject` are documented

## Project Documentation

- [x] ~~README updated~~ (N/A: the README does not describe data-entry actions)
- [x] CLAUDE.md updated — `internal/dataentry/CLAUDE.md` documents the true-only `action:<id>` keys and the single decision point `detailActionCheck.Allows`
- [x] ~~Help text accurate~~ (N/A: no CLI changes; `rela acl audit` already reports action permissions)

## External Documentation

- [x] ~~Changelog entry added~~ (N/A: the repo has no changelog file; release notes come from commits)
- [x] API docs updated — `docs/data-entry/api-reference.md` has a "Detail-page actions" section with the error table (403 `permission_required`, 404 `entity_not_found`, 403 `action_not_available`, 409 `config_reloaded`)
- [x] User guide — `docs/data-entry.md` documents `available_on`, `when`, `permission` and `confirm` under Actions, with a regenerate-document example, the rules, the 5 s timeout and the webhook exception
