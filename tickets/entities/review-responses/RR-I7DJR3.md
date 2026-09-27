---
id: RR-I7DJR3
type: review-response
title: Infrastructure errors on accept map to 422
finding: A store failure during the entity write surfaces as 422 through writePatchError.
severity: minor
reason: Consistent with the PATCH route which uses the same writePatchError. The reopen failure path now logs and returns 500.
status: wont-fix
---
