---
id: RR-2F9XNK
type: review-response
title: AC4 tests the bundle, not the rela-server wiring
finding: 'A regression of LuaWriteDeps: svc.LuaWriteDeps() in wireRemoteMCP would pass every planned test. Fix: extract the Deps construction and test it under an acl.yaml fixture.'
severity: minor
resolution: TestRemoteMCPDeps_UsesGatedHandles builds Services from an acl.yaml project and asserts remoteMCPDeps hands out the gated searcher, reader, tracer and Lua reader with no elevated handle and no cache.
status: addressed
---
