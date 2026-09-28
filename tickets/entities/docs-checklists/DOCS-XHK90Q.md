---
id: DOCS-XHK90Q
type: docs-checklist
title: 'Docs: Predicate engine: typed value selection with and/or'
status: done
---

## Code Documentation

- [x] Comments where logic isn't obvious
- [x] Function/type docs if public API

`logicalNode`, `logicalType`, `checkSelectable`, `preferLayout`,
`coerceSelection`, `evalSelect` carry doc comments; `internal/predicate/doc.go`
has a Value selection section and the extended literal coercion section. In the
browser engine, `markSelections` and `evalSelect` explain the syntactic
selection rule and the '' / 0 divergence.

## Project Documentation

- [x] ~~README updated (if applicable)~~ (N/A: no project-level change)
- [x] ~~CLAUDE.md updated (if new patterns)~~ (N/A: no new architectural pattern)
- [x] ~~Help text accurate (if CLI changes)~~ (N/A: no CLI flags or help text changed; `--filter` accepts more expressions)

User docs: `docs/metamodel.md` gains "Choosing a value with `and`/`or`" under
computed properties (rules, nil fall-through, unset-bool workaround, precedence)
and a pointer from Expression Conditions; `docs/data-entry.md` lists value
selection for form conditions and the browser fall-through.

## External Documentation

- [x] ~~Changelog entry added~~ (N/A: the repository keeps no changelog; release notes come from commits)
- [x] ~~API docs updated (if applicable)~~ (N/A: no HTTP or MCP API change)
