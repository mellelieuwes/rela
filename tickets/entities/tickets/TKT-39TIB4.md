---
id: TKT-39TIB4
type: ticket
title: Rebuild the @ mention menu against an agreed behaviour spec
kind: enhancement
priority: medium
effort: l
status: done
---

## Description

Rebuild the editor's `@` mention menu against an agreed behaviour spec, written
before implementation (`.ignored/at-menu-cases.md`, summarised below).

## Spec summary

- **Bare `@`** shows a starting list: entities related to the one being edited,
then entities this user recently viewed or mentioned (browser-local, loaded
through the ACL-gated API), then globally recently modified. Deduplicated, at
most 8, never the entity itself.
- **1-2 characters** show types whose name or ID prefix starts with the query.
With no type match, 2 characters search.
- **3 characters** show matching types then search results; no type match
means search only. **4+** show search results first, types in a compact row.
- **Type scope is document text**: choosing a type rewrites `@ti` to
`@ticket:` (styled as a chip). The characters typed to find the type are
consumed. `@ticket:` can be typed; a known ID prefix (`@TKT-`) scopes too. An
empty scoped query shows the starting list for that type.
- **No spaces** in a query; `-` separates words.
- **No results**: "No matches", Enter/Tab keep normal editor behaviour, and the
menu closes after 3 more non-matching characters.
- The menu opens only when `@` is typed; moving the cursor, pasting, blur and
undo do not reopen it. Escape closes; editing the query reopens it.
- Insertion replaces the whole `@token` (also past the cursor) and adds no
space before punctuation or an existing space.

## Out of scope

- "Create *type* 'xyz'" row (follow-up).
- The sandboxed app editor (`<rela-editor>`) keeps its own menu. It shares the
search state machine and query parsing, with their defaults, but has no type
rows, starting list or scope: it runs under `connect-src 'none'` and cannot
reach the schema or the recent-entity sources.
