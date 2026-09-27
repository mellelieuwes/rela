---
id: DOCS-3QP1VW
type: docs-checklist
title: 'Docs: Replacement suggestions on text comments'
status: done
---

## Code Documentation

- [x] Comments where logic isn't obvious (resolve-first ordering and the raw-read version token in `commentAccept`; the source-quote prefill in `TextSelectionComment.vue`; the flush-before-accept in `EntityDetail.vue`)
- [x] Function/type docs if public API (`Anchor.Replacement`, `ApplyReplacement`, `Acceptable`, the new errors)

## Project Documentation

- [x] ~~README updated~~ (N/A: feature docs live in docs/comments.md)
- [x] ~~CLAUDE.md updated~~ (N/A: no new cross-cutting pattern)
- [x] ~~Help text accurate~~ (N/A: no CLI changes)

## External Documentation

- [x] ~~Changelog entry added~~ (N/A: the repo keeps no changelog file; release notes come from PR titles)
- [x] API docs updated (docs/comments.md: "Suggested changes" section, the accept route, status codes, permissions and limits)
