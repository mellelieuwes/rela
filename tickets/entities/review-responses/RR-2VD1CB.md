---
id: RR-2VD1CB
type: review-response
title: Cascade write rejected computed values set by automations
finding: cascadeHost.WriteEntity ran rejectComputedChanges, so a cascade entity whose computed field depends on a value an automation set failed the write.
severity: significant
resolution: The cascade write no longer rejects computed changes; the cascade is a system write that recomputes them. Pinned by TestCascadeWrite_ComputedDependentOnAutomationValue, verified to fail before the fix.
status: addressed
---

## Finding

cascadeHost.WriteEntity ran rejectComputedChanges, so a cascade entity whose
computed field depends on a value an automation set failed the write.
