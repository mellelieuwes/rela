---
id: BUG-7R27T6
type: bug
title: Milkdown's orphan timer fails the Frontend CI job after test teardown
description: 'The Frontend CI job fails with about 10 unhandled errors attributed to src/components/forms/milkdown/MilkdownEditor.test.ts while all 3207 tests pass. The error is "ReferenceError: removeEventListener is not defined" from @milkdown/ctx. Timer.start() always schedules setTimeout(type.timeout, default 3000 ms) and never clears it, even after the timer resolves. When a test file finishes sooner, happy-dom tears down its globals, and the orphan timeout then calls the bare global removeEventListener. Hit PR #1680 and #1681 on 2026-09-26 while develop was green; neither PR touched the editor. Fixed by a test.onUnhandledError filter in frontend/vitest.config.ts that drops only that error.'
priority: medium
effort: s
why1: An orphan setTimeout callback from @milkdown/ctx runs after the test file finished and calls the global removeEventListener, which happy-dom already removed at environment teardown.
why2: Milkdown's Timer.start() schedules setTimeout(type.timeout, default 3000 ms) on every start and never clears it, not even after the timer resolves.
why3: Test files that mount the editor can finish within 3 s, and nothing in the suite waits for or cancels third-party library timers before the environment is torn down.
why4: vitest reports an error thrown after environment teardown as an unhandled error and fails the whole run on it, even when every test passed.
why5: There was no policy for known third-party teardown noise, so the only options on a red run were to re-run it or to chase a failure in code the PR never touched.
prevention: The onUnhandledError filter in frontend/vitest.config.ts matches name, message and a @milkdown/ctx stack frame, and vitest drops an error only when the callback returns exactly false. Any other unhandled error, including a new third-party teardown error, still fails the run and must be filtered with the same precision or fixed at source (AM-vitest-teardown-filter-is-narrow).
status: done
---

## Symptom

The Frontend CI job fails with about 10 unhandled errors attributed to
`src/components/forms/milkdown/MilkdownEditor.test.ts`, while all 3207 tests
pass:

```text
ReferenceError: removeEventListener is not defined
```

It hit PR #1680 and PR #1681 on 2026-09-26. `develop` was green, and neither PR
touched the editor.

## Cause

`@milkdown/ctx` `Timer.start()` always schedules `setTimeout(type.timeout)`
(default 3000 ms) and never clears it, even after the timer resolves. When a
test file finishes sooner, happy-dom tears down its globals. The orphan timeout
then fires and calls the bare global `removeEventListener`, which no longer
exists.

It does not reproduce locally with the real editor. Whether the timeout fires
before the worker exits depends on CI worker shutdown timing.

## Fix

`frontend/vitest.config.ts` sets `test.onUnhandledError`. It returns `false`
only for a `ReferenceError` with the message `removeEventListener is not
defined` whose stack includes `@milkdown/ctx`. vitest drops an error only when
the callback returns exactly `false`, so every other unhandled error still fails
the run.

## Verification

- A stand-in test used the real `@milkdown/ctx` `Timer` with a 10 ms timeout
and deleted `globalThis.removeEventListener`. It reproduced the exact CI error
without the filter and passed with it.
- A different stray error still failed the run with the filter in place.
- The full frontend suite, typecheck and lint pass.

## Why it matters

It fails unrelated PRs, which trains reviewers to re-run red CI without reading
it. That habit is what lets a real failure through.
