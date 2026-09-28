---
id: BUG-N34ZL8
type: bug
title: ID gap analysis enumerates manual ids with dashes and lists gaps without bound
description: 'analysis.FindGaps and dataentry analyzeGaps excluded manual-ID types by comparing the declared id_prefix (e.g. `tw-`) with the prefix parsed from each ID. entity.ParseEntityID takes the longest letters-and-dashes run before the trailing digits, so a manual id like `tw-basecamp-10098661922` parses to `tw-basecamp-` and was treated as a numbered sequence. Every number between two such ids was then enumerated one by one: 31 Basecamp-linked ids produced ~20 million ''missing'' ids, `rela analyze all` took 74 s and the data-entry analysis page and dashboard validation card ran the same loop. Even for real sequences the enumeration had no bound (REQ-001 next to REQ-1000000 listed a million ids).'
priority: high
effort: s
why1: The manual-ID exclusion compared the declared prefix with the parsed prefix, which differ as soon as a manual id contains more dashes than its declared prefix.
why2: The analyzers inferred an entity's ID scheme from the shape of its ID string instead of reading its type's id_type, which every entity carries.
why3: Gap enumeration materialized every missing number before the cap, so a wrong or merely large gap cost time and memory proportional to the gap, not to the entity count.
why4: Tests only covered manual ids whose declared prefix matched the parsed one (C-001 with prefix C-) and small gaps.
why5: 'Systemic: analyses classify entities by parsing IDs rather than by schema facts, and caps are applied to results after the work instead of bounding the work.'
prevention: 'Both analyzers now select by the entity''s type and bound the enumeration (CLI: 100 listed + an Unlisted count; data entry: stop at the section cap). Regression tests with dashed manual ids and a huge gap (AM-analyze-gaps-by-type-and-bounded).'
status: done
---
