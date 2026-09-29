---
id: RR-Z06BW0
type: review-response
title: 'Design review: Manager is at its plimsoll cap; TwinSync() would be method 42'
finding: 'Manager carries //plimsoll:max-methods=41 (entitymanager/manager.go:77), and it already has exactly 41 methods. copy.go:28-31 calls the load line ''a ratchet to narrow, not a budget to spend''. Adding `func (m *Manager) TwinSync()` fails `just plimsoll`. Fix: expose the handle as a package function, e.g. `entitymanager.TwinSyncWriter(m *Manager) *Manager`, or replace the bool flags with a small handle-options value shared with elevated(). Keep the ownership check a free function over Deps, like computed.go.'
severity: minor
resolution: The sync handle is the package function entitymanager.TwinSyncWriter(m); the Manager method count is unchanged and plimsoll passes.
status: addressed
---
