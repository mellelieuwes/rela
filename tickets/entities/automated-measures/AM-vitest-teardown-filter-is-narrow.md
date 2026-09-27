---
id: AM-vitest-teardown-filter-is-narrow
type: automated-measure
title: Vitest drops only the Milkdown orphan-timer teardown error; every other unhandled error still fails
description: 'test.onUnhandledError in frontend/vitest.config.ts returns false only for a ReferenceError with the message "removeEventListener is not defined" whose stack includes @milkdown/ctx. vitest drops an error only when the callback returns exactly false, so any other unhandled error, including a new third-party teardown error, still fails the Frontend CI job. Verified with a stand-in test (real @milkdown/ctx Timer, 10 ms timeout, globalThis.removeEventListener deleted) that reproduced the CI error without the filter and passed with it, and with a different stray error that still failed the run. That stand-in was not committed.'
kind: ci
location: frontend/vitest.config.ts
status: active
---

The filter matches three things: error name, exact message, and a
`@milkdown/ctx` frame in the stack. A new third-party teardown error must be
filtered with the same precision or fixed at source. A broad filter (for
example on the message alone) would hide real failures.
