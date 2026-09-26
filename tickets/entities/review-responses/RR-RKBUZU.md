---
id: RR-RKBUZU
type: review-response
title: Lua write bindings return the raw write result, leaking visible:-hidden fields
finding: 'rela.create_entity/update_entity (internal/lua/runtime.go ~1786/1842) push EntityToTable(result.Entity), which entitymanager builds from a raw read. A remote lua_eval caller can read a hidden field via rela.update_entity(id, {...}).salary. Fix: re-read the result through VisibleReader.GetEntity and return that; ErrNotFound means written but not visible.'
severity: critical
resolution: 'create_entity and update_entity re-read the written entity through the gated VisibleReader (writtenEntityTable); if the re-read fails they return only id/type/face. Test: TestScriptWrites_ResultIsRedacted.'
status: addressed
---
