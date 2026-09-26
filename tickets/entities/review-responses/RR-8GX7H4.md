---
id: RR-8GX7H4
type: review-response
title: Unbounded search limit amplifies per-hit gating cost
finding: 'MCP limit and Lua search limit are caller-chosen; 0 means unlimited, and the gated path does per-hit loads and verdicts. Fix: clamp the limit in the decorator to at most 1000.'
severity: minor
resolution: 'visibility.Searcher clamps the limit to (0, MaxSearchLimit=1000]. Test: TestSearcher_ClampsLimit.'
status: addressed
---
