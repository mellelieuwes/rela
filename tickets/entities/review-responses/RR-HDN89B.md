---
id: RR-HDN89B
type: review-response
title: 'Design review: Elevation and automation Lua script actions are not addressed by the guard spec'
finding: 'PatchEntity skips the FieldGate under bypassACL (manager.go:1067-1071), and entitymanager/CLAUDE.md:54-57 says ''Elevation is total''. The design says TwinSync skips only the theirs check (design:274-276), but it never says whether an elevated handle (Manager.Elevated(), used by allow_acl_bypass scripts, manager.go:106) skips it. If the check lands next to the FieldGate, elevated writes bypass ownership. Recommendation: treat ownership like computed properties, which rejectComputedPatch enforces even under elevation (manager.go:1074), and state it explicitly. Also, ''automation / cascade writes NOT gated'' (design:272-273) is only true for `set` actions. Automation Lua script actions write through m.gated().PatchEntity (manager.go:1204; autocascade/runner.go:92), so they will be gated. That matches FieldGate practice, but the doc must say it. gated() today returns the receiver whenever !bypassACL (manager.go:115-119), so its condition must change to strip twinSync as well. Add a nested-cascade leak test mirroring TestManager_Elevated_DoesNotLeakIntoNestedCascade (acl_bypass_test.go:116).'
severity: significant
resolution: 'The guard is a free function applied on PatchEntity and UpdateEntity even for elevated handles; only the handle from entitymanager.TwinSyncWriter skips it, and gated() strips it so nested cascades never inherit it. Automation set actions and ApplyEntity stay ungated, Lua script actions are gated; documented in entitymanager/CLAUDE.md. Test: TestTwinSyncWriter_DoesNotLeakIntoNestedCascade.'
status: addressed
---
