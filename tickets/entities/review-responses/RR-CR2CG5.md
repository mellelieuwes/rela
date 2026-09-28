---
id: RR-CR2CG5
type: review-response
title: Gate runs before the write lock (TOCTOU)
finding: The plan checks type, face, when and permission before enterWrite. Another writer can change the entity (for example its kind or face) between the check and the script run, so the script runs against an entity that no longer qualifies. Resolve and gate under writeMu instead; the reads are cheap.
severity: significant
resolution: Plan moves resolution and gating under writeMu.
status: addressed
---
