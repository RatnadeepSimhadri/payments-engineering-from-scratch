---
name: checklist-cancellation-review
description: Review checklist for any handler that fans out to other services or threads, covering deadlines, cancellation propagation, cleanup, and the no-orphans test
phase: 4
lesson: 11
tags: [concurrency, cancellation, deadlines, review, structured-concurrency]
---

# Cancellation and Deadline Review Checklist

Use on every pull request that starts work on another thread, calls another service, or adds a timeout. One unchecked box is a reason to request changes.

## Lifetime

- [ ] Every forked task is owned by a scope (`StructuredScope`, `StructuredTaskScope`, `TaskGroup`, `WaitGroup` + `context`) that is joined before the enclosing function returns, on every path: normal return, exception, early `return`.
- [ ] No bare `executor.submit`, `new Thread(...).start()`, `go f()`, or `asyncio.create_task` whose result is not awaited by the function that created it.
- [ ] A test exists that cancels the scope while every child is blocked and asserts that zero children are still running when `close`/`join` returns.

## Deadlines

- [ ] The handler receives an absolute deadline (or computes one once at entry from the client's budget) and passes it down; it does not invent a fresh timeout per hop.
- [ ] Each downstream call's deadline is `min(inherited, now + own limit)`. Nothing ever extends a deadline.
- [ ] Before calling downstream, the remaining budget is checked against the time the call needs; if it is too small the handler fails fast instead of issuing a call whose answer cannot arrive in time.
- [ ] Deadlines live on an injected clock in tests; the tests advance the clock rather than sleeping.

## Cancellation

- [ ] Cancellation is idempotent and the first reason wins; the reason is logged with the request id.
- [ ] Cancelling a child never cancels the parent. Cancelling the parent reaches every descendant, including children created after the cancellation (they are born cancelled).
- [ ] Every blocking step in a body is interruptible or polls the token; any non-interruptible call is listed in the PR description with the reason it is acceptable.
- [ ] Cleanup (release a hold, close a stream, return a connection) is registered with `onCancel`/`defer`/`finally` **before** the resource is acquired, and runs exactly once whether cancellation arrives before or after registration.

## The step before the side effect

- [ ] Immediately before every irreversible action (place authorization, commit posting, send webhook, publish event), the code checks the token and aborts if cancelled.
- [ ] The irreversible action carries an idempotency key, because the check above can still lose a race with cancellation by a few microseconds.
- [ ] A cancelled request never returns a success body to the client; a timeout surfaces as a timeout, a failure as a failure, with the first cause, not the knock-on exception.

## Failure policy

- [ ] The scope's policy is stated: shutdown on first failure (all results needed) or shutdown on first success (redundant providers raced).
- [ ] The error returned to the caller is the first failure, with later cancellation-induced exceptions suppressed or attached as secondary.
- [ ] A subtask that ignores cancellation beyond a drain timeout is alarmed on, not silently waited for forever.
