---
id: TKT-VVS16W
type: ticket
title: 'Actions on the entity detail page: available_on, when, permission and a confirm prompt'
kind: enhancement
priority: medium
effort: l
status: done
---

## Description

### Problem

A Lua action can run from a list (on selected rows), from the sidebar, as a
next-action offer, or from an embedded app. It cannot be offered on an entity's
detail page.

Commands can be offered there, but a command runs a shell process and does not
know who started it. Anything it writes is attributed to a fixed or system
principal, not to the user who clicked. For writes that must appear in the audit
log under the acting user, a command is the wrong tool.

A common case is a document generated from the graph that goes through a review
workflow. The type has a concept face and an approved face. A copy promotes
concept to approved. A Lua script regenerates the concept from live data
(statement of applicability from all controls; risk register from all risks;
management review from objectives, incidents and audits).

Each document kind needs a "Regenerate" button on its own detail page, visible
only on the concept face and only to people allowed to regenerate it. These
documents often share one entity type with a `kind` enum property, so offering
an action by entity type alone is not enough.

### Proposal

```yaml
actions:
  regenerate-soa:
    label: "Regenerate"
    script: regenerate-soa.lua
    available_on:
      entity_types: [document]
      faces: [concept]
    when: "kind = 'soa'"
    permission: documents:regenerate
    confirm: "The concept will be overwritten with current data. Continue?"
```

- `available_on.entity_types`: types on whose detail page the action appears. Without `available_on`, behaviour is unchanged (no detail page).
- `available_on.faces` (optional): faces on which it appears; omitted means every face.
- `when` (optional): predicate in the existing condition language (same dialect as list `condition:` and next-action conditions), evaluated against the entity at the served face after redaction.
- `permission` (optional): named global permission, as on commands and documents.
- `confirm`: string or boolean; a string is the dialog text.

### Server-side enforcement

`POST /api/v1/_action/<id>` with an `entity_id` re-checks type+face against
`available_on`, `when` against the caller's view, `permission`, and read access,
and refuses with the same error the list surface uses. The script does not run.

### Execution

Runs as the invoking principal (as list actions do); writes are ACL-checked and
audited under that principal. The `entity` global carries id, type, face,
properties, content. After success the detail page shows the message and reloads
the entity.

### Affordance

Detail-page actions are listed in the entity response `_actions`, filtered
server-side.

### Out of scope

Edit forms (conflicts with autosave / CAS precondition, TKT-34XS2R).

### Open question

5-second action timeout under the global write lock: keep and document, explicit
`timeout:` with a hard cap, or read without the lock.

## Acceptance criteria

1. Action appears on the detail page when `available_on`, `when` and `permission` all match, and not otherwise; tests for wrong type, wrong face, `when` false, permission missing.
2. A direct POST bypassing the UI is refused in each of those four cases; the script does not run.
3. The script's writes are audited under the invoking principal.
4. `confirm:` accepts string and boolean; the string is shown in the dialog.
5. The detail page shows the result message and reloads the entity after success.
6. Load time: `when` parsed and validated against the type's properties; `available_on.faces` must name declared faces; unknown permission reported as on commands.
7. `docs/data-entry.md` documents the new fields under "Actions" with a regenerate-document example.
