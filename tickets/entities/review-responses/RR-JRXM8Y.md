---
id: RR-JRXM8Y
type: review-response
title: 'Code review: Resolve owned_fields in `twin show` from the entity''s current type'
finding: '`TwinShowCmd` calls `tw.OwnedFields(ctx, list[0].Target.Type, c.EntityID)` (cli/twin.go:117). Target.Type is the type recorded at link time, and nothing updates it: neither an entity type change through UpdateEntity nor `rela rename` of the type (renametype adds no alias and does not touch twins). Once the stored type differs from the live one, `OwnedFields` short-circuits on `policy.Systems(oldType) == nil`, so `show` prints `owned_fields: []`. Meanwhile the write guard, which uses the entity''s live type (entitymanager passes old.Type), still refuses those fields. The agent is told nothing is owned while writes are refused. Fix: have `show` read the entity (for example via `svc.EntityManager().GetEntity` or the service reader) and pass its current type, or have `Service.ForEntity` refresh Target.Type from the entity it already reads.'
severity: minor
resolution: '`rela twin show` resolves owned_fields from the entity''s current type instead of the type recorded at link time.'
status: addressed
---
