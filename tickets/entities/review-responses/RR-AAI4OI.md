---
id: RR-AAI4OI
type: review-response
title: Provisioning loser gets 403
finding: A racing stub create fails the unique principal_property check with a validation error, not ErrEntityAlreadyExists, so maybeProvision continues as unmatched and the request is denied. Already live on pg.
severity: significant
resolution: maybeProvision treats ValidationErrorUnique / UniquePropertyError like already-exists and re-resolves. AC10 asserts both racing requests succeed.
status: addressed
---

## Finding

A racing stub create fails the unique principal_property check with a validation
error, not ErrEntityAlreadyExists, so maybeProvision continues as unmatched and
the request is denied. Already live on pg.
