---
id: RR-WHHOIY
type: review-response
title: nil LuaCache becomes a typed-nil interface
finding: '[security] WithCache((*Cache)(nil)) registers rela.cache, which then panics (recovered) instead of being absent.'
severity: nit
resolution: WithCache ignores a nil *Cache, so rela.cache is not registered.
status: addressed
---
