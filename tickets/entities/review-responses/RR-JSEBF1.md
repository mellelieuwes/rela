---
id: RR-JSEBF1
type: review-response
title: Action keys on history and dry-run responses
finding: A historical snapshot and a dry-run create candidate carried action keys that the gate (which reads the live entity) would refuse.
severity: minor
resolution: 'computeDetailActions returns nil under affordances.IsHistoricalSubject; the dry-run handler strips action: keys. Tests: TestDetailAction_NotOfferedOnHistoricalSubject and TestDetailAction_NotOfferedOnDryRunCandidate.'
status: addressed
---
