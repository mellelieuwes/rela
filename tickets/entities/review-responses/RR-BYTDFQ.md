---
id: RR-BYTDFQ
type: review-response
title: SPA autosave can revert an accepted change
finding: Autosave sends no If-Match (useAutoSave.ts:18), so a debounced save of pre-accept text silently overwrites the accept.
severity: significant
resolution: 'The detail page writes the body only through checkbox autosave. Accept flushes pending saves first, is hidden while a save is in flight, and checkbox toggles are ignored while an accept runs. The accept response returns the new content, which the SPA applies before reloading the view. (implemented)'
status: addressed
---
