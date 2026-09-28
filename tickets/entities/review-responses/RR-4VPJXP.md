---
id: RR-4VPJXP
type: review-response
title: Free-text search ranks faces before the face gate
finding: '[security] search.Query carries no face allowlist, so the world ranks withheld faces and faceGatedHits then drops the hit. This drops entities the list shows and is a one-bit oracle.'
severity: significant
reason: 'Deferred to BUG-OJPVPG. The fix needs a face allowlist on search.Query honored by the bleve, linear, sqlite and pg backends, which is a separate change. The gap only narrows results: a withheld face''s values are never served.'
status: deferred
---
