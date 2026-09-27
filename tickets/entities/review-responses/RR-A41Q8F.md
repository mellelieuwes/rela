---
id: RR-A41Q8F
type: review-response
title: memcomments doc claims Comment is all value types
finding: False since Anchor.Text is a pointer; the new pointer adds another alias.
severity: nit
resolution: memcomments deep-copies pointers in Get/List and the doc is corrected. (implemented)
status: addressed
---
