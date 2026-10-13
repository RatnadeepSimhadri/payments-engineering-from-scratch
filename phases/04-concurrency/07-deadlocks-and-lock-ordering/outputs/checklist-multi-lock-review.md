---
name: checklist-multi-lock-review
description: Review checklist for any code path that acquires more than one lock, database row, lease, or semaphore before releasing the first
phase: 4
lesson: 7
tags: [concurrency, deadlock, lock-ordering, review, databases]
---

# Multi-Lock Review Checklist

Use on any pull request where a thread, goroutine, or transaction can hold one lock while acquiring another. This includes two in-memory locks, two `SELECT ... FOR UPDATE` rows, a cache lock plus a database row, a `synchronized` method that calls another object's `synchronized` method, and a lease in one service plus a lock in another. One unchecked box is a reason to request changes.

## Identify the lock set

- [ ] The PR description lists every lock the path can hold at once, including locks taken indirectly (a method call that locks internally, a database statement that takes a row lock, a library call that synchronizes).
- [ ] Each lock in the set is either (a) ordered, (b) acquired with try-lock and release-before-retry, or (c) covered by a detector that aborts a victim. The strategy is named, not implied.
- [ ] Same-resource cases are handled before any lock is taken (transfer to the same account, update of the same row twice), and the lock is known to be reentrant or the double acquisition is impossible.

## If ordering

- [ ] The order is total, documented next to the lock declarations, and derived from something stable (account id, primary key, a fixed stripe index), not from argument position or call order.
- [ ] Every path that takes two or more of these locks sorts them first. A test exercises at least one call with the arguments in the "wrong" order and asserts, via a tracker or log, that the locks were still taken in canonical order.
- [ ] The identity of every lock is known before the first one is taken. If the second lock depends on data read under the first, ordering is not possible here; use try-lock or detection.
- [ ] Database statements that lock multiple rows do so in primary-key order (`ORDER BY id` on the locking select, batch updates sorted by key).

## If try-lock

- [ ] On failure to acquire the second lock, every lock already held is released before any pause, backoff, or retry.
- [ ] The backoff is randomized and exponential (with a cap), so two symmetric retriers do not livelock.
- [ ] There is a maximum attempt count or a deadline, and exhausting it produces a specific error, not an infinite loop.
- [ ] The retry count is observable (a counter or metric) so contention can be measured rather than guessed.

## If detection

- [ ] The detector runs before the thread blocks (or the database's detector is relied on, with its timeout understood: PostgreSQL `deadlock_timeout` default 1 s).
- [ ] The refused or aborted operation unwinds cleanly: locks already held are released in a `finally`/`defer`, partial writes are rolled back, and the caller receives a distinct error type that names the cycle.
- [ ] The application retries the victim with backoff. "Deadlock detected" is a retryable error, not a 500.
- [ ] Which party becomes the victim is not assumed by any test; tests assert one abort and one success, conservation of money, and no lock left held.

## Waits

- [ ] No wait on a lock, row, or lease is unbounded on a request-serving path. `tryLock(timeout)`, `acquire(timeout=)`, `SET lock_timeout`, or a context deadline bounds it.
- [ ] Nothing blocks on a lock while holding a connection-pool connection, unless the pool size and the lock's maximum hold time have been reasoned about together.
- [ ] A watchdog exists for the process: JVM `ThreadMXBean.findDeadlockedThreads` on a timer, Go `go test -timeout` in CI, Python `faulthandler.dump_traceback_later` in tests.

## Tests

- [ ] The deadlock-prone interleaving is forced with a barrier or latch between the first and second acquisition, never waited for with a sleep or a loop count.
- [ ] Contention tests use a join-with-timeout (or `select` with `time.After`) so a real deadlock fails the test instead of hanging the suite.
- [ ] The detector is tested on hand-built graphs: a two-cycle, a three-cycle, a chain, a waiter behind a cycle, a diamond, and a self-request on a held lock.
- [ ] Go: the suite passes under `go test -race ./...`.

## Cross-process

- [ ] If two services each lock a resource in a different store and can run in opposite orders, the PR says so and names the mechanism that prevents a distributed deadlock (saga ordering, a single coordinator, idempotent compensation), because no single-process detector can see it.
