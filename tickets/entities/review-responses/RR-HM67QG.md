---
id: RR-HM67QG
type: review-response
title: Prefill and diff must use the source quote, not the DOM selection
finding: The stored quote is markdown source; splicing user text derived from the rendered selection can eat emphasis markers or list markers.
severity: significant
resolution: Prefill and the old side of the diff use anchor.quote (source). A replacement is refused when the source quote crosses a block boundary (blank line). Tests for emphasis and cross-bullet quotes. (implemented)
status: addressed
---
