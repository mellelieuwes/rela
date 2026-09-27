---
id: RR-KHH7C8
type: review-response
title: 'Design review: GET /_twins findings leak values of visible:-redacted properties'
finding: 'The route is gated only by the entity read verdict (design:286-288). The response includes findings, and each finding carries Base, Ours and Theirs values (design:127-134). The service reads the raw store, and Base is a raw snapshot of every property. A principal who may read the entity but not a `visible:`-restricted field (FieldVerdicts.Visible, dataentry/affordances.go:250-255) therefore gets that field''s current, base and external values. That breaks CLAUDE.md ''Read-out paths go through visibility wrappers'' and field redaction (''hides property values''). Fix: in the handler, gate the row through the existing visibleReader, as comments do (comments_handler.go:547-566). Then compute the entity''s FieldVerdicts and drop the findings, or strip their values, for fields where !IsVisible, including the Theirs value, which is still that field''s data. Add a test pinning that a hidden field''s finding values never appear. owned_fields and field names may stay: config is not secret.'
severity: critical
resolution: GET /_twins gates the entity through the visibility reader and drops base/ours/theirs/message of findings on fields the principal cannot see. Test pins that a hidden field's values never appear in the response.
status: addressed
---
