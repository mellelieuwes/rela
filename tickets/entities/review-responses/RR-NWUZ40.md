---
id: RR-NWUZ40
type: review-response
title: crossesBlock misses setext rules and HTML blocks
finding: Lines of only = - * or _ and HTML block openers were not treated as block starts.
severity: minor
resolution: startsBlock now refuses rule lines and HTML block lines. Table cases added plus a case that allows a less-than sign in prose.
status: addressed
---
