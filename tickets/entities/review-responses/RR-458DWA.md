---
id: RR-458DWA
type: review-response
title: _actions contract test and dataentry CLAUDE.md assume verb keys
finding: 'affordances_contract_test asserts every _actions[v]==false implies a 403 on the write, and dataentry/CLAUDE.md says absent means render. action:<id> keys invert that: only true is emitted and absent means not offered. The plan must update the contract test (or scope it to verbs) and the CLAUDE.md rules.'
severity: minor
resolution: The contract test already iterates the named verbs only, so action:<id> keys are outside it and it needed no change. dataentry/CLAUDE.md now documents the true-only rule for action:<id> keys, and TestDetailAction_AffordanceAndGateAgree pins that affordance and gate agree.
status: addressed
---
