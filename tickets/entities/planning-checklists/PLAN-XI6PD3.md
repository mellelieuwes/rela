---
id: PLAN-XI6PD3
type: planning-checklist
title: 'Planning: related() constraints match the final entity id and current_user.id'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Understanding

- [x] Problem/requirements clearly understood
- [x] Scope defined (what's in/out documented below)
- [x] Acceptance criteria documented with specific test scenarios

**Scope:**

In scope: `id` as a `related()` constraint key (the final entity's id);
`current_user.id` as a constraint value for `id` and for a string-shaped
property; lowering `id` to `RelationPredicate.Endpoints`; pushdown and Go path
on every backend; docs.

Out of scope: `current_user` in an ACL `when:` traversal (refused at load: a
grant's traversal answers are primed per operation from the raw graph, and
binding them to an identity source other than the affordance resolver's would
be a second derivation of "who is calling"). Other `current_user` fields
(`tool`) are refused. Constraint values other than literals and
`current_user.id` stay refused.

**Acceptance Criteria:**
1. `related(entity, 'heeft_verantwoordelijke', { id = current_user.id })` compiles, validates and returns only the caller's rows (dataentry list test, memstore/sqlite/postgres).
2. An anonymous or unmapped principal gets the identity error; never rows (dataentry test).
3. A binding with an empty identity never lowers to an empty Endpoints set (acl lowering test, relresolve test, queryplan test).
4. A conjunction with such a traversal still lowers exactly (pushdown equivalence harness).
5. An `id` constraint derives no property index (queryplan test).

## Research

- [x] ~~For larger features: run `/research` to create a structured research doc~~ (N/A: extends TKT-CXQEV0/TKT-205V2N)
- [x] ~~Searched for existing libraries that solve this problem~~ (N/A: internal engine)
- [x] Checked codebase for similar patterns or reusable code
- [x] ~~Looked for reference implementations in other projects~~ (N/A: internal engine)
- [x] Reviewed relevant rela concepts for prior art

**Research Doc:** N/A

**Existing Solutions:**
- `predicatefns.BindCurrentUser` / `QueryIdentityFrom(ctx)`: the one source of `current_user.id`.
- `queryplan.LowerScope` already binds `current_user.id` equalities from its `identity` argument and declines when it is empty.
- `store.RelationPredicate.Endpoints` composes with `EndpointMatch` as a conjunction on every backend (storetest `composes_with_Endpoints_as_conjunction`).

## Approach

- [x] Technical approach chosen and documented
- [x] Approach builds on existing patterns (not reinventing)
- [x] Alternatives considered (document why rejected)
- [x] Dependencies identified (packages, APIs, types)

**Technical Approach:**
- `predicate.TraversalSpec` gains `ID Value` (literal id) and `Refs map[string]VarRef` (constraints whose value is a record field bound per evaluation). The compiler accepts `<var>.<field>` of string type as a constraint value; the traversal node keeps the attribute node so `References("current_user")` is true and the identity rules apply.
- `TraversalSpec.Bind(values)` returns a literal-only spec. `evalTraversal` evaluates the refs against the bindings and passes the bound spec to the resolver. Batch answering (relresolve) binds from `QueryIdentityFrom(ctx)` and keys answers by the bound spec, so a mismatch between the two is "not answered", an error.
- `predicatefns.ValidateTraversals` allows refs only to `current_user.id`, checks the `id` value.
- `relresolve.Hop` lowers `ID` into `acl.TraversalHop.Endpoints`; `acl.lowerTraversal` refuses an empty or blank-containing endpoint set.
- `queryplan.LowerScope` binds refs with the identity and declines when it is empty; index derivation skips `id`.
- affordances refuses a ref in a `when:` traversal.

Alternative rejected: a host function `is_related_to_me(...)`, which would taint SQL portability of the whole program.

**Files to modify:** internal/predicate/traversal.go, program.go; internal/predicatefns/traversal.go; internal/relresolve/relresolve.go; internal/acl/traversal.go; internal/queryplan/scopelower.go, traversal.go; internal/affordances/resolver.go; internal/appbuild (visibility checks); docs/metamodel.md; tests.

## Security Considerations

- [x] Input sources identified (user input, config, external APIs)
- [x] Input validation approach defined (allowlist preferred over blocklist)
- [x] Security-sensitive operations identified (file access, auth, crypto)
- [x] Error handling doesn't leak sensitive information

**Input Sources & Validation:**
Operator-authored schema.yaml scopes and data-entry conditions; allowlist of `current_user.id` refs, validated at load. The identity comes from the resolved request principal only.

**Security-Sensitive Operations:**
Endpoints with an empty set widens to "any endpoint": refused at bind (ErrNoCurrentUser) and again at lowering. The traversed type stays row-gated by `GateTraversal`; `id` is not a redactable field, so it adds no field-visibility check.

## Test Plan

- [x] Test scenarios documented for each acceptance criterion
- [x] Edge cases identified and documented
- [x] Negative test cases defined (invalid input, error conditions)
- [x] Integration test approach defined (not just unit tests)

**Test Scenarios:** predicate compile tests (id key, ref value, rejects entity refs and non-string refs); predicatefns validation (current_user.tool, enum ref, id literal empty); relresolve Hop/Answer (Endpoints populated, missing identity errors); acl lowering (empty Endpoints refused); queryplan LowerScope and index derivation; storetest inbound Endpoints+EndpointMatch; dataentry pushdown harness on memstore, sqlite, postgres; dataentry `mijn` scope with user_entity_type and anonymous.

**Edge Cases:** empty identity; identity naming an entity of another type; `not related(... current_user.id)`; the same traversal twice.

**Negative Tests:** anonymous principal errors; `current_user.tool` refused; `{ id = entity.x }` refused; ACL `when:` ref refused.

## Risk Assessment

- [x] Technical risks assessed with mitigations
- [x] Security risks assessed (see Security Considerations)
- [x] Effort estimated (xs/s/m/l/xl)

**Risks:** widening via empty Endpoints (mitigated twice, tested); answers cached across principals (answers are per call, keyed by the bound value). Effort m.

## Documentation Planning

- [x] User-facing docs identified (skip if internal refactor)
- [x] Docs-checklist will be created when entering implementation

**Documentation Impact:** docs/metamodel.md "Filtering on related entities".

## Design Review

- [x] ~~Run `/design-review` before starting implementation~~ (N/A: extends an existing reviewed design; code and security review run on the diff)
- [x] ~~All critical/significant findings addressed in plan~~ (N/A: see above)

**Design Review Findings:** N/A
