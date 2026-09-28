---
id: RR-I55QM7
type: review-response
title: Removed where argument is silently ignored
finding: A client still sending where got an unfiltered list it took for a filtered one.
severity: significant
resolution: addTool rejects any undeclared argument and names the valid ones. Pinned by TestDispatch_UnknownArgumentRejected.
status: addressed
---
