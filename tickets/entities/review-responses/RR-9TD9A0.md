---
id: RR-9TD9A0
type: review-response
title: World provenance check is duplicated
finding: The IsDefaultWorld check plus worldProvenance is repeated at three row builders.
severity: nit
reason: A shared helper touches the list and views handlers outside this fix; left for a refactor so this PR stays scoped to search.
status: deferred
---
