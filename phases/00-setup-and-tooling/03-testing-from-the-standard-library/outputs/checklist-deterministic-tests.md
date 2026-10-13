---
name: checklist-deterministic-tests
description: Review checklist for test code in any language, so a test is a verdict about the code and not a sample of the machine it ran on
phase: 0
lesson: 3
tags: [testing, determinism, flaky-tests, review]
---

# Deterministic Tests Checklist

Use on any pull request that adds or changes a test, and on any test that your CI has been configured to retry. A retried test is a failing test with a delay.

## The four inputs that must be injected

- [ ] **Time.** Code under test takes a `Clock` (one method, epoch millis) and never calls `System.currentTimeMillis()`, `time.time()`, `time.Now()`, or friends. Tests pass a `FakeClock` and move it with `advance` / `advanceTo`.
- [ ] **Randomness.** Code under test takes a generator object. Tests pass `SeededRandom(seed)` and assert exact outputs (the permutation, the delay sequence), never "it changed" or "it looks random".
- [ ] **I/O.** Network, filesystem, and subprocess boundaries are behind an interface (`CommandRunner`, `Network`, a writer) and the test supplies an in-memory implementation. No test opens a socket.
- [ ] **Scheduling.** Concurrency tests assert a conserved quantity (a total, a count, a set of observed values) after joining every worker, not an interleaving. Go tests pass under `go test -race ./...`.

## Smells that mean the world leaked in

- [ ] No `sleep` / `Thread.sleep` / `time.Sleep` in a test. If something must "happen later", the fake clock moves.
- [ ] No elapsed-time window assertions (`assert 1500 <= elapsed <= 4000`). Assert the computed delay, not the measured one.
- [ ] No assertion of the form "result differs from input" on a shuffle or a sample. Assert the exact result for a fixed seed.
- [ ] No dependence on wall-clock dates (a test that breaks on 1 January, in a leap year, or at month end).
- [ ] No dependence on map or set iteration order, hash seeds, or `os.listdir` order. Sort before asserting.
- [ ] No shared mutable state between tests (static fields, module globals, a `FakeClock` reused across test methods without reset).

## What a good test of a fake asserts

- [ ] The fake's invariant: `FakeClock` never goes backwards (negative `advance` and past `advanceTo` are rejected and leave the clock untouched).
- [ ] The fake's determinism: `SeededRandom(42)` produces the documented first values; the published reference vector for seed 1234567 holds.
- [ ] Boundaries: a deadline is expired at exactly its instant (`>=`), a backoff never reaches its cap, a bound of zero is rejected.
- [ ] Thread safety where the fake will be shared: N workers advancing by 1 M times yields exactly N*M.

## Harness hygiene

- [ ] **Java:** every `check` has a unique name; `checkEquals` is used where the actual value helps debugging; `checkThrows` names the exception type, never `Throwable`; `main` exits 1 on any failure.
- [ ] **Python:** one `TestCase` per concept; `setUp` builds fresh fixtures; loops use `subTest` so a failure names the row; negative tests use `with self.assertRaises(Type)`.
- [ ] **Go:** table-driven tests use `t.Run(tc.name, ...)`; assertion helpers call `t.Helper()`; `t.Fatalf` only when continuing would panic or mislead; the package passes `go vet` and `-race`.

## When a test is flaky

1. Do not add a retry. Record the failure output.
2. Find the uncontrolled input (time, random, I/O, scheduling, iteration order).
3. Inject it. The fakes in `phases/00-setup-and-tooling/03-testing-from-the-standard-library/code/` are the course's reference implementations; copy them.
4. Replace the sampled assertion with an exact one.
5. Run the test one hundred times in a loop (`for i in $(seq 100); do ...; done`) before merging. It should pass one hundred times in well under the time the old test took once.
