---
id: RR-HZ0O0N
type: review-response
title: Props map aliasing is undocumented
finding: Bind and BindTraversal return the receiver when there are no refs; sharing Props with the compiled program.
severity: nit
resolution: TraversalSpec.Props doc states the map is shared and must not be written to.
status: addressed
---
