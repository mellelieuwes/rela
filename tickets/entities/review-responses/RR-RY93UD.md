---
id: RR-RY93UD
type: review-response
title: rela acl can accepts an address only without a policy
finding: runNoPolicy used GetEntityAt while the policy path (aclmap) calls GetEntity, so the command's contract depended on whether acl.yaml exists.
severity: minor
resolution: runNoPolicy reverted to GetEntity; both paths take a bare id. trace, restore and can-relation already do.
status: addressed
---
