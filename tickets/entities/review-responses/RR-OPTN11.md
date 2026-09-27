---
id: RR-OPTN11
type: review-response
title: 'Design review: OwnedFields adds a twin-store lookup to every write and every rendered row'
finding: 'The call happens once per PatchEntity/UpdateEntity (the entity type is ignored), and again inside dataentry fieldVerdicts. fieldVerdicts runs per row in sections and in the entity serializer (dataentry/sections.go:284; entityserializer.go:143) and in every PATCH validation (affordances.go:391). With files keyed by (system, externalID), ForTarget has to scan `.rela/twins`. That is O(rows × twins) file I/O per page, and it also applies to writes on types that have no pact at all. Fix: change the call-site interface to OwnedFields(ctx, entityType, entityID). Return nil immediately when PactPolicy.Systems(entityType) is empty. Back ForTarget with an index, subject to the cross-process constraint in the rela-server finding above. When the lookup errors, fail closed (reject the write), per RR-X9NVHI''s ''forgotten wiring must not become a bypass''.'
severity: significant
resolution: 'The call-site interface is OwnedFields(ctx, entityType, entityID); the service returns immediately for types without a pact (no store I/O) and fails closed on a lookup error. Test: TestTwinOwnership_LookupErrorFailsClosed.'
status: addressed
---
