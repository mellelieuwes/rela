---
id: RR-7NVGPR
type: review-response
title: 'when: related() and current_user handling unspecified'
finding: 'The plan compiles when with the request-scoped env like view conditions, but does not say how related(...) traversals are answered (ViewConditionMatcher.MatchesWith needs a traversal func and a gate) or how current_user is bound for evaluation (view conditions bind identity per request). Decide: refuse related() at load for action when (as forms do via refuseFormTraversal), and bind identity the way list conditions do.'
severity: significant
resolution: Plan refuses related() in action when at load and binds current_user per request like list conditions.
status: addressed
---
