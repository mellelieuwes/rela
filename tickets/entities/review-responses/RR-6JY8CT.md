---
id: RR-6JY8CT
type: review-response
title: Accept can merge two paragraphs
finding: If a blank line was inserted inside the quoted span after the comment was made, the splice replaced the paragraph break and merged two blocks.
severity: significant
resolution: ApplyReplacement refuses with ErrSuggestionStale when the matched span crosses a block. Pinned by the subtest 'blank line inserted inside the quote'.
status: addressed
---
