---
id: BUG-LFNK1W
type: bug
title: rela rename id fails with 'principal is unstamped' in any project with an acl.yaml
description: 'internal/cli/rename.go RenameIDCmd.Run called EntityManager.RenameEntity with context.Background() instead of the context the CLI stamps with the operator''s principal. With an acl.yaml every write is authorized against that principal, so `rela rename id` always failed with ''forbidden: acl.ForPrincipal: acl: principal is unstamped'', even for a user the policy grants everything; without a policy the rename was audited as nobody''s. RenameEntityCmd (rename entity) had the same context.Background() for its entity count.'
priority: medium
effort: s
why1: RenameIDCmd.Run passed context.Background() to RenameEntity, so the ACL request carried no principal and the policy refused it.
why2: The command's Run did not take a context.Context parameter, unlike create/update, so kong never handed it the stamped context and the author reached for context.Background().
why3: Nothing requires a write command's Run to accept the stamped context; the kong binding silently allows either signature.
why4: CLI tests of rename ran against projects without an acl.yaml, where an unstamped principal is inert, so the missing identity was never observable.
why5: 'Systemic: principal propagation in the CLI is a per-command convention with no test that runs every write command under a policy.'
prevention: Regression test TestRenameIDCommand_UnderACLPolicy runs the command under a policy and asserts the audit attributes the rename to the operator (AM-cli-rename-id-under-acl).
status: done
---
