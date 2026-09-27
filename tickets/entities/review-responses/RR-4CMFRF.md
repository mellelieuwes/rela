---
id: RR-4CMFRF
type: review-response
title: Version token and splice base need a raw read
finding: visibleReader returns field-redacted entities; VersionOf over that hashes redacted properties, so accept would 412 whenever a property is hidden.
severity: significant
resolution: 'commentsHandler gets a raw ref reader used only for Content and VersionOf; refuses if raw content differs from visible content. Test with a hidden property: accept succeeds and the property survives. (implemented)'
status: addressed
---
