---
id: RR-1WW94M
type: review-response
title: A missing identity is reported as a 500 search_failed
finding: writeListPipelineError maps predicatefns.ErrNoCurrentUser to 500 search_failed; naming a subsystem the request never reached.
severity: minor
resolution: 'Deferred: Pre-existing: an is_current_user or entity.x == current_user.id scope already takes the same default branch for an anonymous caller. dataentry cannot import predicatefns (arch-lint); so a proper mapping needs a dataentry sentinel wrapped at the appbuild adapters for scopes; view conditions and lists alike; that is its own change. The request still fails closed with no rows.'
reason: 'Pre-existing: an is_current_user or entity.x == current_user.id scope already takes the same default branch for an anonymous caller. dataentry cannot import predicatefns (arch-lint); so a proper mapping needs a dataentry sentinel wrapped at the appbuild adapters for scopes; view conditions and lists alike; that is its own change. The request still fails closed with no rows.'
status: deferred
---
