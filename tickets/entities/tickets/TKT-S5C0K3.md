---
id: TKT-S5C0K3
type: ticket
title: Replacement suggestions on text comments
kind: enhancement
priority: medium
effort: l
status: done
---

## Description

Let a commenter propose replacement text for a quoted range of an entity body,
like "suggesting" mode in a document editor. A text-anchored comment may carry
an optional `suggestion`: the text that should replace the quote. A principal
who may edit the entity can accept the suggestion. Accepting replaces the
anchored range in the body through `entitymanager.Manager.PatchEntity` and
resolves the comment.

## Scope

In scope:

- Optional `suggestion` field on text-anchored comments (all four comment
backends).
- An accept action on the comments API that applies the replacement and
resolves the comment.
- SPA: propose a replacement when commenting on a selection; show the
suggestion as a before/after diff; an Accept button when permitted.

Out of scope:

- Suggestions on property or section anchors.
- Multi-range or whole-document tracked changes.
- Rejecting as a distinct state (resolving without accepting covers it).

## Acceptance criteria

- A text comment can be created with a suggestion; property and section
comments with a suggestion are refused with 400.
- Accepting replaces exactly the resolved range, records the write in the
audit log under the accepting principal, and marks the comment resolved.
- Accept is refused when the anchor is detached or uncertain, when the body
changed since the range was resolved, or when the caller lacks entity update
rights or the comment read floor.
- Existing stored comments load unchanged (no migration of stored data).
