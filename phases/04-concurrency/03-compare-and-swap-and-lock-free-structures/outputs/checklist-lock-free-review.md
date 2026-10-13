---
name: checklist-lock-free-review
description: Review checklist for any pull request that adds a CAS loop, an AtomicReference-based structure, or claims to be lock-free
phase: 4
lesson: 3
tags: [concurrency, cas, lock-free, review]
---

# Lock-Free Code Review Checklist

Use on every pull request that introduces `compareAndSet`, `AtomicReference`, `AtomicStampedReference`, `atomic.Pointer`, or the words "lock-free" or "non-blocking". One unchecked box is a reason to request changes or a proof in the PR description.

## The CAS itself

- [ ] The shared word being CASed is the **only** place the structure's state is published. No plain writes to it anywhere.
- [ ] Every CAS is inside a loop that re-reads on failure. A single unlooped CAS whose failure is ignored has a documented reason (for example the Michael-Scott tail swing, where a helper may have done it).
- [ ] Any function passed to `updateAndGet` / `accumulateAndGet` / `compute` is pure and cheap; it may run more than once.
- [ ] Fields written before the CAS and read by other threads after it are written **before** the CAS in program order (publication through the CAS is the only happens-before edge).

## Stale reads and ABA

- [ ] List every value read between the load and the CAS (for a stack pop: `current.next`). For each one, state why it cannot be stale when the CAS succeeds.
- [ ] If any referenced object can be **reused** (object pool, free list, slab, arena, recycled buffer), the CAS carries a stamp or mark (`AtomicStampedReference`, `AtomicMarkableReference`, version field), or the PR explains why reuse cannot overlap with a live reader.
- [ ] If the structure is ported to a language without a garbage collector, there is a memory-reclamation plan (hazard pointers, epochs, RCU) or the port is explicitly out of scope.
- [ ] Wrap-around of the stamp is considered: 32 bits is fine for a hot path; a `long` is used when a reader can be stale for a very long time (persisted state, long-running batch).

## Progress and fairness

- [ ] The PR names the progress rung claimed: blocking, obstruction-free, lock-free, or wait-free. "No locks" is not a rung.
- [ ] If lock-free: a CAS fails only because another thread's CAS succeeded. There is no spin-wait on a flag another thread must set.
- [ ] If a bounded-retry fallback to a lock exists, the structure is documented as blocking, and the retry bound is a named constant.
- [ ] Retry counts are observable (a counter or metric), because they are the contention signal and the only way to know whether the lock-free design is helping.

## Bounds and backpressure

- [ ] The structure has a size bound, or it sits behind something that does (bounded queue, semaphore, rate limiter). An unbounded lock-free queue in front of a slow dependency is a memory leak with good throughput.
- [ ] `size()` on a lock-free structure is documented as weakly consistent and is not used for a capacity decision.

## Tests

- [ ] There is a deterministic test that reproduces the dangerous interleaving through an injected seam (a hook between the read and the CAS), not only a stress test.
- [ ] There is a stress test asserting **conservation**: every element pushed is popped exactly once, nothing lost, nothing duplicated.
- [ ] The stress test runs with more threads than cores at least once in CI, so the operating system is forced to deschedule a thread mid-operation.

## Before merging, ask

1. Would a `ReentrantLock` have been simpler, and did someone measure the difference under realistic contention?
2. If this structure hands out units of work that move money, what prevents a duplicated element from becoming a duplicated payment downstream (idempotency key, state machine guard)?
