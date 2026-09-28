---
id: RR-8CEI9P
type: review-response
title: Nested Tx edge case is speculative
finding: No manager op runs inside an open Tx today; all views join nested Tx. The real rule is that no public Manager call runs inside a Tx callback.
severity: minor
resolution: Rule stated in the tx helper godoc; edge case dropped from the test plan.
status: addressed
---

## Finding

No manager op runs inside an open Tx today; all views join nested Tx. The real
rule is that no public Manager call runs inside a Tx callback.
