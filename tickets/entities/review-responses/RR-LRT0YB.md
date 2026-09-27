---
id: RR-LRT0YB
type: review-response
title: 'Design review: Body ownership cannot be shown read-only through the existing verdict path'
finding: 'Acceptance criterion 6 relies on ''owned fields render read-only automatically through the existing verdict path''. The wire affordance exists only per property (apiwire/v1/responses.go:179-182, FieldAffordance{Writable, Options}), and nothing on the wire covers the content body. For body-theirs or `*` twins, the SPA body editor stays editable, and the user''s typed text is rejected on save. Fix: add a content affordance, e.g. `content_writable` on v1.Entity next to FieldAffordances. Mirror it in frontend/src/types, disable the body editor in the SPA, and add a test. Alternatively, explicitly move body read-only to out of scope and adjust acceptance criterion 6.'
severity: significant
resolution: The v1 entity gains `content_writable`; the SPA disables the body editor, the task-list checkboxes, the link tooltip and suggestion accept when it is false. Real (unstubbed) MilkdownEditor tests and EntityDetail tests pin it.
status: addressed
---
