---
id: RR-LV6BK1
type: review-response
title: MCP trace tools accept X@draft then trace the literal string
finding: The gated reader now resolves X@draft for the existence check, and the tracer then traces the literal address and returns an empty result instead of not-found.
severity: nit
reason: 'Pre-existing on fsstore (the default MCP backend); trace ids are bare and the tracer is face-blind by design. Not a disclosure: the result is empty.'
status: wont-fix
---
