---
id: DOCS-TZT3A7
type: docs-checklist
title: 'Documentation: Twins stage 1: pacts, twins store, reconcile and ownership enforcement'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Code Documentation

- [x] Comments where logic isn't obvious
- [x] Function/type docs if public API

## Project Documentation

- [x] README updated (if applicable)
- [x] CLAUDE.md updated (if new patterns)
- [x] Help text accurate (if CLI changes)

## External Documentation

- [x] ~~Changelog entry added~~ (N/A: the repository keeps no changelog file)
- [x] API docs updated (if applicable)

New guide `docs/twins.md` (GUIDE-twins); `pacts:` in `docs/metamodel.md`; `rela
twin` in `docs/cli-reference.md`; README guide table regenerated;
`internal/entitymanager/CLAUDE.md` documents the twin guard and which paths it
covers; package docs for `internal/twins`, `filetwins` and `memtwins` describe
storage, concurrency and lifecycle; `GET /api/v1/_twins` and the
`content_writable` / field `reason` affordances are documented in the guide and
on the wire types.
