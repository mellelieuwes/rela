---
id: PLAN-BX5EYA
type: planning-checklist
title: 'Planning: Wire read-side ACL into the MCP server'
status: done
---

<!-- @managed: claude-workflow v1 -->

## Understanding

- [x] Problem/requirements clearly understood
- [x] Scope defined (what's in/out documented below)
- [x] Acceptance criteria documented with specific test scenarios

**Scope:**

In: the remote HTTP MCP endpoint (`rela-server -mcp`, `cmd/rela-server/mcp.go`).
Two of its handles bypass the read gate: `Deps.Searcher` (raw index) and
`Deps.LuaWriteDeps` (unrestricted reader, raw tracer, raw searcher).

Out: stdio `rela mcp` (operator trust boundary, documented in
`wireRemoteMCP`'s godoc); relation meta redaction (TKT-0RBFN0); per-request
`acl.Request` binding for the other MCP handlers (existing behaviour, only
a cost concern).

**Acceptance Criteria:**
1. `search_entities` over a gated server omits entities the principal cannot
   read. Test: two entities match; the principal may read one; one result.
2. `search_entities` omits a hit that matched only a `visible:`-hidden
   property. Test: query equals a hidden salary value; no result.
3. `search_entities` shows title and status from the redacted entity, never
   from the index. Test: hidden status is absent from the summary.
4. `lua_eval` over the remote wiring cannot read a hidden entity through
   `get_entity`, `list_entities`, `search` or `trace_from`. Test through
   `GatedReads().LuaReads` with a runtime.
5. With no `acl.yaml`, the gated searcher returns the same hits as the raw
   searcher (NopACL parity).
6. Lua `create_entity`/`update_entity` return values carry no hidden
   property. Test: update a row with a hidden salary; the returned table
   has no salary.
7. `rela://relation/A/t/B` and `delete_relation` answer "not found" when
   either endpoint is hidden. Test in `internal/mcp/acl_test.go`.
8. A write that names a hidden id (`rename_entity` incl. dry run,
   `create_relation` endpoints, Lua update/delete/create_relation) answers
   the same not-found as a missing id. Test: hidden vs absent id give
   identical errors.
9. `delete_entity` relation counts exclude edges to hidden entities.
10. The remote wiring itself is tested: extracted `remoteMCPDeps` under an
    `acl.yaml` fixture has a gated searcher, a non-unrestricted Lua reader,
    no elevated handles, and no Lua cache.

## Research

- [x] ~~For larger features: run `/research`~~ (N/A: the pattern exists; this applies it to two missed handles)
- [x] ~~Searched for existing libraries~~ (N/A: internal wiring)
- [x] Checked codebase for similar patterns or reusable code
- [x] ~~Looked for reference implementations in other projects~~ (N/A: internal wiring)
- [x] Reviewed relevant rela concepts for prior art

**Research Doc:** N/A

**Existing Solutions:**
- `internal/dataentry/helpers.go` `searchVisibleHits`: scope from
  `ReadQuery`, `SearchVisibleFields` with hidden fields, face gate, fail
  closed when the searcher cannot redact. This is the logic to reuse.
- `internal/dataentry/readgate.go` `SearchScope`: `ReadQueryResult` to
  `search.TypeScope`.
- `visibility.DeclarativeGate`: `Bind`, `ReadQueryFor`, `PermittedFaces`.
- `appbuild.Services.GatedReads`: the MCP read bundle; `scriptReads` and
  `scriptTracer` give the gated Lua reader and tracer.

## Approach

- [x] Technical approach chosen and documented
- [x] Approach builds on existing patterns (not reinventing)
- [x] Alternatives considered (document why rejected)
- [x] Dependencies identified (packages, APIs, types)

**Technical Approach:**

1. `internal/visibility/search.go`: `Searcher` implements `search.Searcher`.
   Built from a `search.VisibleSearcher`, a scope source (the
   `DeclarativeGate`), a `FieldRedactor` and the metamodel type names.
   `Search` binds one `acl.Request` (`gate.Bind`), builds the scope from
   `ReadQueryFor` per type (only `q.Types` when set), calls
   `SearchVisibleFields` with hidden fields from the redactor, and drops
   hits whose face `FaceAllowed` rejects. It fails closed: bind error,
   scope error, or a searcher that is not a `FieldVisibleSearcher` yields
   an error, never raw hits. An empty scope yields no hits.
2. `appbuild`: `GatedReadBundle` gains `Searcher search.Searcher` and
   `LuaReads lua.ReadDeps` (gated reader, tracer and searcher, zero
   capabilities). Under NopACL the searcher is the raw one (parity).
   The bundle validator's Lua deps use the gated searcher too.
3. `cmd/rela-server/mcp.go`: use `reads.Searcher` and
   `lua.WriteDeps{ReadDeps: reads.LuaReads, EntityManager: ...}`.
4. `internal/mcp/tools_entity.go` `handleSearchEntities`: hydrate each hit
   through the gated `Store`; skip a hit it does not return; take title and
   status from that entity. This also covers stdio, where the reader is raw.
5. `.go-arch-lint.yml`: allow `visibility` to depend on `search`.
6. Decorator details: hidden names are qualified with
   `search.PropFieldPrefix`; `Hit.Title` is cleared (consumers take titles
   from the gated reader); `q.Limit` is clamped to (0, 1000].
7. `gatedGraphReader.GetRelation`: gate both endpoints through the row
   reader; either hidden returns `store.ErrNotFound`. Meta redaction stays
   with TKT-0RBFN0.
8. Lua writer bindings (`internal/lua/runtime.go`): pre-gate every named id
   through `VisibleReader` (update/delete target, create_relation and
   delete_relation endpoints) and return the uniform not-found; return
   create/update results re-read through `VisibleReader`. The elevated
   `admin` handle is unchanged.
9. MCP handlers: pre-gate `rename_entity` (old id) and `create_relation`
   endpoints through the gated `Store`; count relations in `delete_entity`
   through gated `ListRelations`.
10. `cmd/rela-server/mcp.go`: extract `remoteMCPDeps(svc)`; pass a nil
    `LuaCache` (the shared cache is keyed without the principal).

Alternatives rejected:
- Hydrate-and-drop in the MCP handler only: leaves the hidden-field match
  oracle (AC2) and leaves the Lua searcher raw.
- Moving `searchVisibleHits` out of dataentry: it depends on the dataentry
  affordance service and ctx read gate; a second, smaller consumer in
  visibility is simpler. Converging dataentry onto it is a follow-up.
- A new exported `Services` method: bumps the plimsoll exported-method
  directive; the bundle already exists for exactly this consumer.

**Files to modify:**
- internal/visibility/search.go (new), search_test.go (new)
- internal/appbuild/appbuild.go, fieldredaction_test.go or a new test
- cmd/rela-server/mcp.go, mcp_test.go
- internal/mcp/tools_entity.go, tools_relation.go, acl_test.go
- internal/lua/runtime.go (+ tests)
- .go-arch-lint.yml
- docs/acl-security.md (MCP section)

## Security Considerations

- [x] Input sources identified (user input, config, external APIs)
- [x] Input validation approach defined (allowlist preferred over blocklist)
- [x] Security-sensitive operations identified (file access, auth, crypto)
- [x] Error handling doesn't leak sensitive information

**Input Sources & Validation:**
- MCP tool arguments (`query`, `type`, `limit`, Lua code) from a
  JWT-authenticated remote caller. The scope is an allowlist built from the
  principal's grants; types without a grant are absent (deny).

**Security-Sensitive Operations:**
- Search hit disclosure: gated by scope, field filter and face gate.
- Lua reads: gated reader, tracer and searcher; no elevated handles; zero
  capabilities (unchanged).
- Errors: a scope failure returns a generic "search failed" message with
  the wrapped error; it names no entity.

## Test Plan

- [x] Test scenarios documented for each acceptance criterion
- [x] Edge cases identified and documented
- [x] Negative test cases defined (invalid input, error conditions)
- [x] Integration test approach defined (not just unit tests)

**Test Scenarios:**
- AC1-3: `internal/mcp/acl_test.go` style test with a gated server and a
  gated searcher; plus `visibility` unit tests for the decorator.
- AC4: appbuild test building a Lua runtime from `GatedReads().LuaReads`
  on the redaction project fixture.
- AC5: appbuild test without `acl.yaml`; searcher is the raw one.

**Edge Cases:**
- `q.Types` names a type the principal cannot read: no hits, no error.
- Principal grants nothing: empty scope, no hits.
- Faced type with a `type@face` grant: other faces' hits dropped.
- Unstamped principal on ctx: `Bind` fails, search errors (deny).

**Negative Tests:**
- Searcher that is not a `FieldVisibleSearcher`: error, no hits.
- Gate error: error, no hits.

## Risk Assessment

- [x] Technical risks assessed with mitigations
- [x] Security risks assessed (see Security Considerations)
- [x] Effort estimated (xs/s/m/l/xl)

**Risks:**
- Diverging search rules between dataentry and visibility. Mitigation:
  same primitives (`ReadQuery`, `SearchVisibleFields`, `FaceAllowed`);
  follow-up ticket to converge dataentry.
- Cost: one scope per type per search. Bounded by the number of types and
  amortized by one bound `acl.Request`.

Effort: l.

## Documentation Planning

- [x] User-facing docs identified (skip if internal refactor)
- [x] Docs-checklist will be created when entering implementation

**Documentation Impact:**
- docs/acl-security.md: remote MCP search and Lua tools are gated.

## Design Review

- [x] Run `/design-review` before starting implementation
- [x] All critical/significant findings addressed in plan

**Design Review Findings:** RR-RKBUZU, RR-1J042K, RR-T4G0S7, RR-YCSFCZ,
RR-UXGUT7, RR-8IVSHW, RR-PG14A3, RR-8GX7H4, RR-2F9XNK. All are folded into
the approach (items 6-10) and acceptance criteria 6-10.

Residual, accepted: a write that names a NEW id (create with a custom id,
rename's `new_id`) still fails with "already exists" when a hidden entity
holds that id. Any answer there reveals the collision; the data-entry
create path has the same property.
