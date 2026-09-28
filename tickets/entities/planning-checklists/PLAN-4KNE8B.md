---
id: PLAN-4KNE8B
type: planning-checklist
title: 'Planning: Predicate engine: typed value selection with and/or (c and x or y)'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Understanding

- [x] Problem/requirements clearly understood
- [x] Scope defined (what's in/out documented below)
- [x] Acceptance criteria documented with specific test scenarios

**Scope:**

In scope:
- Typed value selection with `and`/`or` in `internal/predicate`, available in every profile (computed values, `--filter`, `condition:`, `when_condition:`, ACL `when:`, transitions, `visible_when`/`required_when`).
- Compile-time literal coercion of selection branches to int/date targets.
- Prefilter guard: only bool-typed `and` nodes are conjunctions.
- The browser evaluator (`frontend/src/utils/conditions.ts`) returns operand values for `and`/`or`, so a form condition means the same in the browser as on the server.
- A correct `if` hint and updated docs.

Out of scope:
- Table lookups `({k = v})[x]`, a `case(...)` built-in, `if` statements.
- Compile-time checking of enum literals against the enum's values.
- SQL lowering of selection (no lowering of predicate programs exists yet; portability metadata only).
- Treating a nil bool as false. A missing bool operand stays an eval error, as today.

**Acceptance Criteria:**
1. A computed string/enum property `(entity.method == 'password_otp' and 'medium') or (entity.method == 'passkey' and 'high') or 'low'` compiles and yields `medium`, `high`, `low` for the matching inputs and for a missing `method`.
2. `rela list <type> --filter "(entity.method == 'passkey' and 'high' or 'low') == 'high'"` compiles and returns exactly the passkey entities.
3. `entity.opt or 'none'` yields the property value when set and `'none'` when absent.
4. Int and date targets work with literal branches: `entity.a > 5 and 10 or 0` for an int property; `c and '2026-01-01' or entity.due` for a date property.
5. Compile errors: mixed branch types (`c and 'x' or 1`), a non-bool left operand of `and`, a bool/non-bool mix in `or`, a `nil` literal operand, a list or record operand.
6. A `related(...)` call inside a selection branch is still rejected in a computed property, and an attribute read only inside a branch appears in `Attributes("entity")`.
7. A program whose non-bool `and` sits under a comparison produces no prefilter equalities or conjuncts from that `and`.
8. The `if` hint names the working form; `docs/metamodel.md` and `docs/data-entry.md` describe value selection.
9. A `visible_when` using selection gives the same result in the browser evaluator as in the Go engine.

## Research

- [x] ~~For larger features: run `/research` to create a structured research doc~~ (N/A: approach settled in discussion with the user; three Lua forms compared)
- [x] Searched for existing libraries that solve this problem
- [x] Checked codebase for similar patterns or reusable code
- [x] Looked for reference implementations in other projects
- [x] Reviewed relevant rela concepts for prior art

**Research Doc:** N/A

**Existing Solutions:**
- No library needed: the change lives in the existing walker/IR/evaluator.
- `coerceLiteralOperands`/`coerceOneLiteral` (`internal/predicate/walk.go:92-131`) already retype literals to int/date for comparisons; selection reuses them.
- `logicalNode` (`internal/predicate/ir.go:73`) and `evalLogical` (`internal/predicate/eval.go:458`) already short-circuit Lua-style.
- `Program.inspect` (`internal/predicate/program.go:69`) already visits `logicalNode` children, so attributes, traversals and portability propagate without change.
- Reference: Lua 5.1 semantics of `and`/`or` (return an operand; only `nil`/`false` are falsy).
- Alternatives compared with the user: `case(...)` built-in (not Lua; needs a polymorphic node), table lookup (needs table literals and indexing), `if` statements (break the single-expression sandbox).

## Approach

- [x] Technical approach chosen and documented
- [x] Approach builds on existing patterns (not reinventing)
- [x] Alternatives considered (document why rejected)
- [x] Dependencies identified (packages, APIs, types)

**Technical Approach:**

Type rules in `walkLogical` (T is a scalar non-bool type: string, number, int,
date):

| Form | lhs | rhs | Result |
|---|---|---|---|
| `a and b`, `a or b` | bool | bool | bool (unchanged) |
| `c and x` | bool | T | T |
| `x or y` | T | T | T |

Everything else is a compile error with a specific reason. A `nil` literal
operand is rejected with a hint, because `c and nil or y` is Lua's falsy
pitfall.

1. `logicalNode` gets a `typ Type` field; `resultType()` returns it. Bool logic sets `BoolType`.
2. `walkLogical`: after walking both sides, apply `coerceLiteralOperands` for `or` (literal to the sibling's int/date type). Then classify with the table above.
3. `coerceOneLiteral` is extended to descend into selection nodes, so `(c and 10 or 0) == entity.a` and a top-level selection coerce their literal leaves. In `compile`, when the root's type does not match `profile.Expected`, try coercing the root to `Expected` before the type check.
4. `evalLogical`: when `n.typ` is bool, keep the current code. Otherwise, for `and`: evaluate `c` (must be Bool); false returns Nil, true returns the rhs value. For `or`: return the lhs unless it is Nil, else the rhs.
5. Prefilter: `collectConstEqualities` and `collectConjuncts` only treat `logicalNode` with bool type as a conjunction.
6. `compile.go:179`: hint becomes `if statements are not allowed (use 'c and x or y' to choose a value)`.
7. `frontend/src/utils/conditions.ts`: `logical` returns operand values. `and`: if `truthy(left)` (fail-safe, as today) return the right value, else NIL. `or`: return the left value unless it is NIL or `false`, else the right value. Fall-through deliberately does not use `truthy()`, which treats `''` and `0` as falsy; that would diverge from the Go engine. An EvalFail in a value branch propagates to the enclosing comparison; the top level still coerces through `truthy` (RR-T6819Y).
8. The result type of `x or y` takes the operand type that carries a date layout (RR-M0GJP6).
9. The `logicalNode` doc comment records that a nil condition is an eval error, unlike SQL `CASE WHEN NULL`, so a future lowering must keep that (RR-S8GVJI).
10. Root coercion also makes a bare literal root compile (`computed: 5` on an integer property). This only affects programs that fail today (RR-Z6GAIJ).

**Files to modify:**
- `internal/predicate/ir.go`, `walk.go`, `eval.go`, `compile.go`, `prefilter.go`
- `internal/predicate/value_expression_test.go`, `prefilter_test.go`, `compile_test.go`
- `internal/computed/computed_test.go`
- `internal/cli` list filter test (reporter's case)
- `frontend/src/utils/conditions.ts` and its test
- `docs/metamodel.md` (computed properties, expression conditions), `docs/data-entry.md` (condition expressions)

## Security Considerations

- [x] Input sources identified (user input, config, external APIs)
- [x] Input validation approach defined (allowlist preferred over blocklist)
- [x] Security-sensitive operations identified (file access, auth, crypto)
- [x] Error handling doesn't leak sensitive information

**Input Sources & Validation:**
- Expressions come from operator config (`schema.yaml`, `acl.yaml`, `data-entry.yaml`) and from the CLI `--filter` flag. The type checker is an allowlist: only the combinations in the table compile.
- No new syntax reaches the parser; depth and step budgets apply unchanged.

**Security-Sensitive Operations:**
- ACL `when:` affordances compile through the same engine. Selection cannot widen access by itself: the top level must still be bool, and a non-bool `and` is never treated as a conjunction by the prefilter (criterion 7), so it cannot turn into a pushed-down equality.
- Computed properties still reject `related(...)` inside any branch (criterion 6).

## Test Plan

- [x] Test scenarios documented for each acceptance criterion
- [x] Edge cases identified and documented
- [x] Negative test cases defined (invalid input, error conditions)
- [x] Integration test approach defined (not just unit tests)

**Test Scenarios:**
1. Table test in `value_expression_test.go`: enum mapping over each method value and a missing one (AC1, AC3).
2. Int and date literal branches, including a top-level all-literal selection and a selection under `==` (AC4).
3. Negative table: each rejected form with its error text (AC5).
4. `Attributes`/`Traversals` test with the field and `related()` only in a branch; computed_test asserts the load error (AC6).
5. Prefilter test: `ConstEqualities` and `Conjunction` on `(entity.m == 'x' and 'a' or 'b') == 'a'` (AC7).
6. Compile test for the hint text (AC8).
7. Integration: computed_test end to end through `Evaluate`; a CLI list test with the reporter's `--filter` (AC1, AC2).
8. Vitest: selection in `conditions.ts` returns the operand; boolean cases unchanged (AC9).
9. Manual: a scratch project with the reporter's schema; run `rela validate`, create entities, run the `--filter`.

**Edge Cases:**
- Missing property in a branch value: `c and entity.opt or 'x'` gives `'x'` even when `c` is true (Lua semantics). The docs state this rule with the guard form `c and (entity.opt or 'none') or 'x'`, and a test pins it (RR-7JU3ON).
- Browser: `form.opt or 'x'` with `form.opt == ''` gives `''`, and with `0` gives `0`, matching Go.
- Empty string is a value, not nil: `'' or 'x'` gives `''`.
- Chains with parentheses and without (`a and x or b and y or z`).
- Nested selection inside concatenation or arithmetic: `(c and 1 or 2) + entity.a`.
- Missing bool condition: eval error, unchanged from today.

**Negative Tests:** listed under AC5, plus the `if` statement hint.

## Risk Assessment

- [x] Technical risks assessed with mitigations
- [x] Security risks assessed (see Security Considerations)
- [x] Effort estimated (xs/s/m/l/xl)

**Risks:**
- Code outside `predicate` that assumes `logicalNode` is bool: only `prefilter.go` and `program.go` touch it (grep); the prefilter gets an explicit guard and test.
- Browser/server divergence for form conditions: addressed by updating `conditions.ts` in the same change (AC9).
- Existing expressions change meaning: none can, because every expression that compiles today is bool-only and keeps the bool path.

Effort: m.

## Documentation Planning

- [x] User-facing docs identified (skip if internal refactor)
- [x] Docs-checklist will be created when entering implementation

**Documentation Impact:**
- [x] docs/metamodel.md - computed properties and expression conditions
- [x] docs/data-entry.md - condition expressions table

## Design Review

- [x] Run `/design-review` before starting implementation
- [x] All critical/significant findings addressed in plan

**Design Review Findings:** RR-T6819Y (significant, browser fall-through),
RR-7JU3ON (significant, nil branch falls through), RR-Z6GAIJ, RR-M0GJP6,
RR-S8GVJI (minor). All addressed in the approach above.
