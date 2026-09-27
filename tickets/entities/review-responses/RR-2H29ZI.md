---
id: RR-2H29ZI
type: review-response
title: Resolve via svc.Update can clobber a concurrent comment edit
finding: Store.Update rewrites body and resolved; an edit between Get and Update is lost.
severity: minor
resolution: Accepted for v1 and documented in the handler comment; with resolve-first ordering a concurrent delete yields 404 before any body write. (implemented)
status: addressed
---
