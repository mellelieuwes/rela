---
id: RR-RWLE9X
type: review-response
title: Enum literals in computed branches not checked at load
finding: A mistyped enum literal in a computed branch only fails on a write that takes that branch.
severity: minor
reason: Out of scope as agreed during planning (enum literal check listed as a follow-up); it touches the metamodel type adapter.
status: deferred
---
