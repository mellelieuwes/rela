---
id: BUG-6OZBP9
type: bug
title: Data-migration face move and delete leave comment threads behind
description: Data-migration steps delete faces and entities on the raw store, below the entitymanager's AliasRewriter hook, so comment threads for the deleted address survive and a recreated face or entity inherits them.
priority: low
status: backlog
---

## Problem

Data-migration steps delete on the raw store, below the entitymanager:

- The face move deletes the source face with `DeleteEntityState`
(`internal/datamigration/steps.go`, around line 586).
- The delete step calls `DeleteEntity` (around line 1092).

Neither path fires `AliasRewriter`, so comment threads keyed on the deleted face
or entity survive. A face or entity recreated under the same address inherits
the old thread. This is the delete half of BUG-R1PQY9 on a second write path.

A face move could move the thread to the destination face rather than drop it;
that is the decision to make here.

Found in code review of BUG-R1PQY9 (RR-EST0W6).
