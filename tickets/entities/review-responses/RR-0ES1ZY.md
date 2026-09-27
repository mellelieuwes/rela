---
id: RR-0ES1ZY
type: review-response
title: 'Design review: NewService captures PactPolicy by value; codebase convention reads the metamodel live'
finding: 'NewService(store, pacts metamodel.PactPolicy, …) (design:221) freezes the pacts at boot. The comments handler reads its policy live on purpose so a metamodel swap is picked up (dataentry/comments_wiring.go:90-97), as do queryservice and viewcondition. Fix: take `func() *metamodel.Metamodel` (or a narrow PactSource) and build NewPactPolicy per call, which is cheap.'
severity: minor
resolution: twins.NewService takes func() *metamodel.Metamodel; appbuild passes the assembly's metamodel and a schema reload re-assembles the service.
status: addressed
---
