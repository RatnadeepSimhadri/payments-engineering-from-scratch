---
name: checklist-shared-counter-review
description: Review checklist for any change that updates a number shared between threads — a balance cache, a rate limiter, an inventory count, an idempotency counter
phase: 4
lesson: 2
tags: [concurrency, locks, atomics, striping, review]
---

# Shared Counter Review Checklist

Use on any pull request where more than one thread, goroutine, or request handler updates the same in-memory number. One unchecked box is a reason to request changes.

## Which shape is it?

- [ ] The update is one of: (a) a critical section under a mutex, (b) a CAS retry loop on a single word, (c) a per-key operation under a striped or per-key lock. If it is none of these, it is `balance += delta` with extra steps, and it is wrong.
- [ ] The choice is justified in the PR description: mutex for multi-field invariants, CAS for a single word, striping when the invariant is per key and there are many keys.
- [ ] No `volatile` (Java) or plain attribute (Python) is relied on to make a read-modify-write atomic. Visibility is not atomicity.

## Check-then-act

- [ ] Every decision that depends on a value read earlier (`if balance >= amount`, `if !seen.contains(key)`, `if count < limit`) is in the same lock hold as the write it guards, or is a CAS whose expected value is the value the decision used.
- [ ] A CAS loop reloads and re-decides on failure; it never retries the write with the old decision.
- [ ] The refused path (insufficient funds, limit exceeded) leaves the value untouched, and a test asserts this.

## Locks

- [ ] Every `lock()` has an `unlock()` in a `finally` (Java), a `with` block (Python), or a `defer` (Go), including on the exception path of checked arithmetic.
- [ ] Reads of the guarded value also take the lock (or the field is `volatile` / an atomic). An unlocked read of a plain field has no happens-before edge to the writer.
- [ ] A striped structure derives the stripe from a stable hash (not Python's salted `hash()`, not identity hash), uses `floorMod` or equivalent so negative hashes do not index out of range, and never holds two stripes at once without an ordering rule.
- [ ] Any operation that reads across stripes (a total, a snapshot) either takes all stripe locks in a fixed order or is documented as not a snapshot.

## Tests

- [ ] The unsafe path, if it is kept for teaching or comparison, is tested through an injected interleaving hook, never by racing threads and asserting a loss.
- [ ] Concurrency tests assert the invariant (`lost == 0`, `exactly one success`, `balance never negative`), never the order of events or a throughput number.
- [ ] Threads are released together by a latch, barrier, or closed channel so the test maximizes overlap; no `sleep` is used to "let the threads run".
- [ ] The test rethrows worker exceptions on the test thread so a dead worker cannot produce a green run.
- [ ] Go: the tests pass under `go test -race ./...`, and the race detector is part of CI, not an occasional local run.

## Measurement

- [ ] Any benchmark checks the result before reporting a rate. A number without a `lost` column beside it is not evidence.
- [ ] CAS retries (or lock wait time) are counted and reported; wall-clock ops/s alone does not separate contention from per-operation cost.
- [ ] The time source is injected so the arithmetic (`ops / elapsed`) is unit-tested with a fake stopwatch, and live numbers are labelled as varying.

## Production equivalents considered

- [ ] If the value must survive the process, the in-memory counter is a cache in front of a durable, transactional store (`SELECT ... FOR UPDATE`, a conditional `UPDATE`, a conditional write), and the reconciliation between the two is specified.
- [ ] `LongAdder` or a stripe-by-stripe sum is used only for metrics, never for a balance that is read back and acted on.
