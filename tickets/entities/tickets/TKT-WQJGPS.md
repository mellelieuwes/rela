---
id: TKT-WQJGPS
type: ticket
title: 'Predicate engine: typed value selection with and/or (c and x or y)'
kind: enhancement
priority: medium
effort: m
status: done
---

## Description

A predicate expression cannot produce a value that depends on a condition. The
engine has no conditional construct, and `walkLogical`
(`internal/predicate/walk.go`) requires both operands of `and`/`or` to be bool.
The compile hint for `if` statements (`internal/predicate/compile.go`)
recommends `a and b or c`, which therefore never compiles.

This affects every surface on the shared engine: computed properties (mapping an
enum to another enum, a score to a band) and conditions such as `--filter`.

```text
$ rela list access_point --filter "(entity.method == 'passkey' and 'high' or 'low') == 'high'"
invalid --filter expression: predicate: compile error at line 1: 'and' requires bool on right, got string
```

## Approach

Widen `and`/`or` with type rules while keeping Lua semantics exactly:

| Expression | Operand types | Result |
|---|---|---|
| `a and b` / `a or b` | bool, bool | bool (unchanged) |
| `c and x` | bool, T (T not bool) | T, or nil when `c` is false |
| `x or y` | T, T (T not bool) | `x` unless nil, else `y` |
| other | | compile error |

- Lua's falsy pitfall cannot occur: non-bool selection requires a non-bool T, so the chosen value is never `false`.
- `x or 'default'` gives a default for missing values.
- SQL lowering is exact (`CASE WHEN`, `COALESCE`); the program stays SQL-portable when its children are.
- `Program.inspect` already walks `logicalNode` children, so dependency tracking and the computed-property `related()` ban keep working.
- The prefilter (`collectConstEqualities`, `collectConjuncts`) must treat only bool-typed `and` nodes as conjunctions.

Rejected alternatives: a `case(...)` built-in (not Lua), table lookups `({k =
v})[x]` (needs table literals and indexing; possible follow-up), `if` statements
(break the single-expression sandbox).

## Acceptance criteria

- A computed enum property defined as `(entity.method == 'password_otp' and 'medium') or (entity.method == 'passkey' and 'high') or 'low'` compiles and evaluates correctly.
- The reporter's `--filter` expression compiles and filters correctly.
- Mixed-type selection (`c and 'x' or 1`) and a non-bool left operand of `and` are compile errors.
- `related(...)` inside a selection branch is still rejected in a computed property; a field read only inside a branch is a dependency.
- A non-bool `and` is never used as a prefilter conjunction.
- The `if` hint is correct, and the predicate and computed-property docs describe value selection.
