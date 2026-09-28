---
id: RR-1EHLXB
type: review-response
title: A when over a hidden field can pass
finding: when binds a visible:-hidden property as unset, so a negated test such as entity.status ~= 'approved' passes for a caller who cannot see status, and the script runs on an entity the operator meant to exclude.
severity: significant
resolution: The check refuses a when whose compiled program reads any property hidden from the caller (EntityAttributes on the matcher). Pinned by the negated case in TestDetailAction_WhenSeesOnlyVisibleFields; mutation-verified. Docs updated.
status: addressed
---
