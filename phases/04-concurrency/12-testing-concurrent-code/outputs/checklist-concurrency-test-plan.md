---
name: checklist-concurrency-test-plan
description: Test plan for any change to shared mutable state, choosing between stress, seeded exploration, exhaustive model checking, race detection, and specification
phase: 4
lesson: 12
tags: [concurrency, testing, model-checking, stress, review]
---

# Concurrency Test Plan

Attach to any pull request that adds or changes code touched by more than one thread, or that moves money inside such code. Fill in every section; "not applicable" needs one sentence of justification.

## 1. What is shared

- [ ] List every field, map entry, row, or cache key that more than one thread reads or writes.
- [ ] For each, name the synchronisation that protects it (lock, atomic, CAS loop, single-writer actor, database row lock) or state "none, and here is why that is safe".
- [ ] Mark every **check-then-act**: a read whose result decides whether a later write happens. Each one needs either a lock or a CAS over the whole decision, never a bare atomic for the write alone.

## 2. The model

- [ ] Write the hot path as a list of instructions in the Lesson 12 machine's vocabulary: load, store, add, atomic add, lock, unlock, branch. One instruction per step the real code takes between points where the scheduler could switch.
- [ ] Keep the model next to the code in the PR so the reviewer can check they match line for line. A re-read in the code that is missing from the model voids the result.
- [ ] State the invariant as a predicate on final memory (`balance >= 0`, `a + b == total`, `count == threads * ops`).
- [ ] Run the exhaustive explorer with 2 threads. Record `explored`, `violations`, `deadlocks`. If `explored` is above 10^6, cut the model down or switch to seeded random exploration and record the seed and sample size.
- [ ] If the fix is a lock, confirm the explored count collapsed (for example 252 to 2) and understand why.

## 3. The stress test

- [ ] Threads are released together with a barrier; without one the test often runs sequentially.
- [ ] Multiple rounds with a fresh instance each round, so a single lucky JIT compilation cannot mask the race.
- [ ] Assertions are **machine-independent**: the safe version is exact in every round; the unsafe version is asserted only for "never gains" and "completed", never for "always loses".
- [ ] No `sleep` to arrange timing. No retries of the test itself in CI.
- [ ] The stress test runs nightly or on demand, not as a gate, unless it is deterministic.

## 4. The race detector

- [ ] Go: `go test -race ./...` passes on every package touched. A report is a bug, not noise, even when the test still passed.
- [ ] Java: the change is exercised by at least one `jcstress` or Lincheck test when it is a new concurrent data structure; for ordinary service code the model in section 2 and a happens-before argument (Lesson 01) are recorded instead.
- [ ] Python: the test runs with `sys.setswitchinterval` lowered, and once under free-threaded CPython if the service will ever run there.

## 5. When a test fails

- [ ] Record the full counterexample trace (thread, instruction, final memory) in the ticket, or the seed and schedule index for a random exploration.
- [ ] Reproduce from the trace alone, not by re-running until it fails again.
- [ ] After the fix, re-run the exhaustive explorer and attach the new `violations = 0` result. Keep the failing model as a regression test.

## 6. Beyond the process

- [ ] If the shared state spans processes (two service instances, a database row), the in-process model is not enough: name the database-level guarantee (`SELECT ... FOR UPDATE`, unique constraint, conditional write) and the Phase 7 lesson that covers it.
- [ ] If the design is new rather than a fix, consider a TLA+ or PlusCal spec of the protocol before writing code; the model checker finds design bugs that no code-level test reaches.
