---
name: checklist-deterministic-simulation
description: Checklist for adding a deterministic simulation harness to a payment service and for reviewing a pull request that claims to fix an interleaving bug
phase: 14
lesson: 10
tags: [testing, simulation, determinism, faults, invariants]
---

# Deterministic Simulation Checklist

Two uses. Part A is for the engineer introducing a harness into a service that has never had one. Part B is for the reviewer of any pull request whose description contains "race", "retry", "timeout", "crash", or "could not reproduce".

## Part A: Making a service simulable

### Seams

- [ ] No production code path calls `Instant.now()`, `System.currentTimeMillis()`, `System.nanoTime()`, `time.time()`, or `time.Now()` directly. A clock (`InstantSource`, a `Clock` interface, a function value) is injected at construction.
- [ ] No production code path constructs an unseeded random generator, UUID, or jittered delay inline. A `RandomGenerator` or equivalent is injected.
- [ ] Every outbound call (processor, bank, webhook, database if it is remote) goes through one interface that has a real implementation and a simulated one. The simulated one can delay, drop, duplicate, and refuse.
- [ ] Timers and timeouts are scheduled through the injected clock, not through `ScheduledExecutorService`, `threading.Timer`, or `time.AfterFunc` bound to wall time.
- [ ] Volatile and durable state are distinguishable in code (separate objects or clearly named fields), so a simulated crash can discard exactly the volatile part.

### The simulator

- [ ] One master seed; one child stream per component, derived in a fixed documented order. Changing the number of draws in one component does not change another component's sequence.
- [ ] The event queue orders by `(time, sequence)`. Equal timestamps have a defined order.
- [ ] The simulated network checks partitions before consuming a random draw, and documents the order of every draw it makes.
- [ ] A message already in flight when a partition begins is still delivered. A partition blocks both directions unless the test asks for one.
- [ ] Faults are generated from the seed before the run and armed as events. At least: dependency outage (silence, not an error), dependency slowdown, partition on each link, process crash.
- [ ] At least one fault is biased toward an interesting state (crash while a request is in flight; outage that begins between send and response).
- [ ] The run has a drain phase (`runUntilIdle`) with an event budget, so an infinite retry loop fails the test instead of hanging it.

### Invariants

- [ ] Each invariant has a name that appears verbatim in the violation message and in the bug tracker.
- [ ] Safety invariants (checked during the run) are separated from quiescence invariants (checked only after the drain). Each is labelled as one or the other in code.
- [ ] Money invariants are present: trial balance zero; every external approval posted exactly once; receivables equal approved amounts; one payment per idempotency key.
- [ ] Agreement invariants are present: what the client believes equals what the service recorded; no key ever received two different terminal statuses.
- [ ] The checkers are O(state) and run on a schedule (per virtual day, per N events), not after every event.

### Reporting

- [ ] A run produces a report with: seed, configuration, event count, every fault counter, the first violation and its virtual time, and a trace fingerprint.
- [ ] A failing run can be reproduced from the report alone with one command.
- [ ] Two runs of the same seed on two machines produce the same fingerprint. This is asserted in CI.
- [ ] CI runs a fixed set of seeds on every commit and a rolling window of new seeds nightly; nightly failures are filed with the seed.

## Part B: Reviewing a fix for an interleaving bug

- [ ] The pull request names the seed (or seeds) that reproduced the bug before the fix.
- [ ] The test asserts that the named seed now passes AND that reverting the fix makes it fail with the expected invariant name. A test that only passes proves nothing about this bug.
- [ ] The fix does not change any random draw order. If it does, the old failing seed no longer means the same universe; the author must find a new failing seed on the pre-fix code and use that.
- [ ] The fix does not add a sleep, a real-time wait, or a thread. If the production fix needs a timer, the timer is scheduled through the injected clock.
- [ ] If the fix touches a retry, the retry reuses the original idempotency key toward the dependency, and a test asserts the dependency saw at most one decision per payment.
- [ ] If the fix touches crash recovery, a test crashes the service with work in flight and asserts every in-flight item reaches a terminal state after the drain.
- [ ] If the fix touches a timeout, a test checks the three guards: the response already arrived; a later attempt superseded this timer; the process restarted since the timer was armed.
- [ ] The postmortem's "could not reproduce" line is replaced by the seed.

## The one-line bug report

```text
reproduce with: Harness.run(SimConfig.standard(<seed>, <hours>)); first violation at t=<ms>: [<invariant>] <detail>
```

If a report in your tracker cannot be reduced to this shape, the harness is not yet doing its job.
