---
id: RR-52HZ4Z
type: review-response
title: Invisible-character list is incomplete
finding: The hand list missed U+061C, U+2060-2064, the soft hyphen, the tag block, variation selectors, Hangul fillers and U+2028/2029, so hidden text could pass as an ordinary diff.
severity: minor
resolution: isInvisibleFormat now rejects unicode.Cf and Variation_Selector plus the Hangul fillers, U+2028/2029, U+034F and U+180E. Table cases added, and a case confirming accented and CJK text is allowed.
status: addressed
---
