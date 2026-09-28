---
id: AM-getentity-refuses-state-ref
type: automated-measure
title: 'Store conformance: GetEntity refuses a state-ref string'
description: Every backend's GetEntity(id) returns ErrNotFound for an id containing the state-ref separator. pgstore and sqlitestore already behave this way; memstore and fsstore resolve it by coincidence of their FormatStateRef index key, which is what let four unparsed-address bugs (BUG-64MU2Q, BUG-OOZBBK, BUG-VFHUWO, BUG-R1PQY9) pass the memstore-only dataentry suite. With this pinned, an unparsed ID@face reaching GetEntity fails in every test run instead of only on postgres.
kind: test
location: internal/store/storetest/states.go (AddressStringIsNotAnID)
status: active
---
