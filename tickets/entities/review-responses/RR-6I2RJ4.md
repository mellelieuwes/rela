---
id: RR-6I2RJ4
type: review-response
title: 'Design review: twins package would need store and an unexported canonical helper; arch-lint boundaries'
finding: 'Link and Pending need store.VersionOf (design:147, 226, 234), and the comments precedent keeps side subsystems off `store` (.go-arch-lint.yml:1196-1208). The canonical normalization is unexported (canonical.go:257), and entitymanager may not import canonical (.go-arch-lint.yml:1047-1058). So the UpdateEntity diff check would fall back to reflect.DeepEqual, as computed.go:59 does, which differs from Reconcile''s equality. Fix: have the call-site EntityReader return (entity, version), with appbuild''s adapter calling store.VersionOf, so twins never imports store. Export canonical.EqualValue and add canonical to the twins component. Name the new arch-lint components (twins, memtwins, filetwins; twinstest excluded as at .go-arch-lint.yml:17) in the design.'
severity: minor
resolution: 'twins never imports store: the appbuild EntityReader adapter returns (entity, version). canonical exports the equality helpers; arch-lint components and deps are registered (twins, memtwins, filetwins; twinstest excluded). go-arch-lint passes.'
status: addressed
---
