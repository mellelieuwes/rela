---
id: RR-B1DZB7
type: review-response
title: Reload serialization not enforced
finding: schema.Reload publishes by load-then-store. Only the single watcher goroutine prevents two reloads overlapping; a second caller could publish the older file last.
severity: minor
resolution: Added App.reloadMu held for the whole of reloadConfig; fixed the stale App doc that mentioned a nonexistent Reload method.
status: addressed
---
