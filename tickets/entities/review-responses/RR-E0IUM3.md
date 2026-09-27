---
id: RR-E0IUM3
type: review-response
title: Identity extraction repeated in three places
finding: relresolve.bindIdentity; appbuild/queryscopes.go and nextaction_matchers.go each read QueryIdentityFrom(ctx).ID().
severity: nit
resolution: 'Deferred: The three sites are one line each and read the same function; a helper is a small follow-up refactor outside this feature''s scope.'
reason: The three sites are one line each and read the same function; a helper is a small follow-up refactor outside this feature's scope.
status: deferred
---
