---
id: AM-cli-rename-id-under-acl
type: automated-measure
title: rela rename id runs and is audited under an ACL policy
description: 'Runs `rela rename id` with an acl.yaml that grants the CLI user and asserts the rename succeeds and the audit record names that user via the cli tool. Mutation-checked: passing context.Background() again fails it with ''principal is unstamped''.'
kind: test
location: internal/cli (TestRenameIDCommand_UnderACLPolicy)
status: active
---
