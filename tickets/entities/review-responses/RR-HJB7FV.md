---
id: RR-HJB7FV
type: review-response
title: SPA search callers do not send the page world
finding: searchEntities never sent world, so palette, search page and picker ignored an explicit page world, and dashboard cards silently moved to the publication world.
severity: significant
resolution: searchEntities takes an optional world. Palette, SearchView and EntityPicker pass useWorld().worldParam. Dashboard cards pass the default world explicitly, keeping their previous counts.
status: addressed
---
