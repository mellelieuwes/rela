---
id: RR-RSA5EQ
type: review-response
title: Prefilter selects guards unreachable from Compile
finding: The selects() guards in prefilter.go cannot be reached from a bool program; a comment should say they are defensive.
severity: nit
resolution: Comments on both prefilter guards say they cannot be reached from a bool program and are defensive.
status: addressed
---
