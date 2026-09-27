---
id: PLAN-O49B7B
type: planning-checklist
title: 'Planning: Rebuild the @ mention menu against an agreed behaviour spec'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Understanding

- [x] Problem/requirements clearly understood
- [x] Scope defined (what's in/out documented below)
- [x] Acceptance criteria documented with specific test scenarios

**Scope:** In: the SPA form editor's `@` menu (stages, starting list, type scope
as document text, arming, no-match handling, insertion), recently viewed
tracking, and a `limit` parameter on `/_search`. Out: the sandboxed app editor's
menu and the "Create type 'xyz'" row (follow-up ticket).

**Acceptance Criteria:** The spec in `.ignored/at-menu-cases.md`, summarised in
the ticket body. Each row maps to a unit test (`mentionPlan`, `useMentionMenu`,
`mentionTrigger`, `mentionArm`, `mentionStartingList`) or an e2e test in
`markdown-editor-mention-autocomplete.spec.ts`.

## Research

- [x] ~~For larger features: run `/research` to create a structured research doc~~ (N/A: behaviour was researched and agreed with the user case by case in the spec)
- [x] Searched for existing libraries that solve this problem
- [x] Checked codebase for similar patterns or reusable code
- [x] Looked for reference implementations in other projects
- [x] Reviewed relevant rela concepts for prior art

**Research Doc:** N/A

**Existing Solutions:**
- Milkdown `SlashProvider` stays the positioning layer; the ProseMirror plugin
API supplies `handleTextInput` for arming and decorations for the chip.
- Reused `mentionMenuState.ts` (shared with the app editor), `rankMentions.ts`,
`searchEntities`, and entity GET with `include=*` for related entities.
- Reference behaviour from GitHub, Slack, Notion and Linear mention menus
(no-match handling, starting lists of recent items).

## Approach

- [x] Technical approach chosen and documented
- [x] Approach builds on existing patterns (not reinventing)
- [x] Alternatives considered (document why rejected)
- [x] Dependencies identified (packages, APIs, types)

**Technical Approach:** A pure `planMention(types, query)` decides the stage.
The controller re-plans on every keystroke and keeps no scope. The scope is
`@type:` text in the document, drawn as a chip by a decoration. An arm plugin
tracks the typed `@`. The starting list merges related, recently viewed and
recently modified entities.

Rejected: a scope held in menu memory (drifted from the document and needed a
Backspace handler reading a stale mirror); a new "recent" endpoint (a `limit` on
`/_search` with `sort:modified:desc` suffices).

**Files to modify:** `frontend/src/components/forms/milkdown/*mention*`,
`useEditorMention.ts`, `MentionMenu.vue`, `MilkdownEditor.vue`,
`insertEntityRef.ts`, `utils/recentEntities.ts`, `DynamicForm.vue`,
`EntityDetail.vue`, `api/entities.ts`, `internal/dataentry/api_v1.go`, docs and
e2e spec.

## Security Considerations

- [x] Input sources identified (user input, config, external APIs)
- [x] Input validation approach defined (allowlist preferred over blocklist)
- [x] Security-sensitive operations identified (file access, auth, crypto)
- [x] Error handling doesn't leak sensitive information

**Input Sources & Validation:**
- Query text: sent unmodified to the ACL-gated `/_search`.
- Type scope: resolved only against schema type names, so `?type=` stays an
allowlist.
- `limit`: integer 1-100, otherwise `400 invalid_limit`; applied after the read
gate so it counts visible rows only.
- localStorage recent list: IDs validated with `isValidEntityRefId`, corrupt
data ignored.

**Security-Sensitive Operations:** Recently viewed entities are stored as IDs
only and reloaded through the ACL-gated entity GET; a hidden entity yields no
row. Related entities come from `include=*`, which is already gated. No titles
are stored client-side.

## Test Plan

- [x] Test scenarios documented for each acceptance criterion
- [x] Edge cases identified and documented
- [x] Negative test cases defined (invalid input, error conditions)
- [x] Integration test approach defined (not just unit tests)

**Test Scenarios:** Stages, scope and insertion: unit tests plus e2e (bare `@`,
one letter, type choice writes `@feature:`, Backspace over `:`, Enter before
results, no matches then close, Escape then edit, cursor moving back into an old
query).

**Edge Cases:** Empty starting list, entity editing itself, hidden recent
entity, stale responses, token text after the cursor, punctuation after the
token.

**Negative Tests:** Invalid `limit` values; unknown `foo:` scope; pasted `@`;
email addresses.

## Risk Assessment

- [x] Technical risks assessed with mitigations
- [x] Security risks assessed (see Security Considerations)
- [x] Effort estimated (xs/s/m/l/xl)

**Risks:** Timing between SlashProvider's debounced update and key handling
(mitigated: keydown re-reads the document first; e2e repeated 5x). Effort: l.

## Documentation Planning

- [x] User-facing docs identified (skip if internal refactor)
- [x] Docs-checklist will be created when entering implementation

**Documentation Impact:**
- `docs/data-entry/api-reference.md`: search `limit`.
- `frontend/CLAUDE.md`: mention menu design notes.

## Design Review

- [x] ~~Run `/design-review` before starting implementation~~ (N/A: the behaviour spec was agreed with the user row by row before implementation; the review phase code review covers the design)
- [x] All critical/significant findings addressed in plan

**Design Review Findings:** N/A
