---
id: RR-T4G0S7
type: review-response
title: MCP and Lua write paths are existence oracles on hidden ids
finding: 'rename_entity (dry_run too), create_relation endpoints, and Lua update/delete/create_relation reach entitymanager, which returns Forbidden for an existing hidden id and NotFound for a missing one. Fix: pre-gate every id a write names through the gated reader and return the uniform not-found.'
severity: significant
resolution: 'rename_entity and create_relation (MCP) and update/delete_entity and create/delete_relation (Lua) pre-read their target ids through the gated reader and answer "entity not found" like an absent id. The new-id collision residual is documented in acl-security.md. Tests: TestACL_Writes_HiddenIdIsIndistinguishableFromAbsent, TestACL_LuaEval_WritesNamingHiddenIds, TestScriptWrites_HiddenTargetIsNotFound.'
status: addressed
---
