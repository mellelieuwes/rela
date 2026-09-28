---
id: RR-WXISPG
type: review-response
title: search_entities leaks hidden entities over remote MCP
finding: The remote MCP searcher was the raw index, and a hit the caller could not read still returned its id, type and raw title. The index also matched hidden field values.
severity: critical
resolution: Added gatedSearcher in appbuild (row scope, field redaction, face check), exposed as GatedReadBundle.Searcher and wired into both MCP servers. The handler now drops unreadable hits. Pinned by TestGatedReads_SearchHidesRedactedFieldMatch and TestGatedReads_SearchHidesUnreadableRows.
status: addressed
---
