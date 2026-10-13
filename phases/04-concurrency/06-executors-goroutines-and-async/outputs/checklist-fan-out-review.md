---
name: checklist-fan-out-review
description: Review checklist for code that calls several dependencies in parallel and combines the answers (quotes, replicas, risk signals, webhooks)
phase: 4
lesson: 6
tags: [concurrency, executors, timeouts, fan-out, review]
---

# Fan-Out / Fan-In Review Checklist

Use on any pull request that starts more than one I/O call and waits for the results: processor quotes, risk-signal lookups, replica reads, webhook deliveries. One unchecked box is a question to ask before approving.

## Who runs the waits

- [ ] The executor is named and sized on purpose. "The common pool" (`ForkJoinPool.commonPool()`, `supplyAsync` without an executor) is never used for blocking calls.
- [ ] If tasks block (HTTP, JDBC, `Future.get`, `join`), they run on virtual threads or a pool dedicated to that dependency. One slow dependency cannot exhaust the threads another needs.
- [ ] Virtual-thread code avoids holding a `synchronized` monitor across a blocking call (pinning); `ReentrantLock` is used instead.
- [ ] Concurrency per dependency is bounded (semaphore, bounded queue, connection-pool size), and the bound is a named constant with a reason.

## Deadlines

- [ ] Every call has a deadline chosen by the business ("a quote is worthless after 800 ms"), not inherited from a socket or connection default.
- [ ] The fan-out's deadline is shorter than the client library's socket timeout, so the fan-out decides when to stop waiting.
- [ ] The combined operation completes no later than the deadline, regardless of how many dependencies are silent.
- [ ] A deadline that passes produces a typed outcome (`TIMED_OUT`), not an exception that poisons the fan-in.

## Abandon vs cancel

- [ ] For every call, the PR states what happens to the in-flight request when the caller stops waiting: cancelled (how?), or abandoned.
- [ ] Every abandoned **write** (authorise, capture, refund, transfer) carries an idempotency key and has a reconciliation path, because it may still succeed.
- [ ] Alarms, timers, and scheduled timeouts are cancelled when the call completes first, so completed calls do not leak scheduled work.

## Fan-in semantics

- [ ] The combining rule is explicit: all, first OK, first K, quorum. "First to complete" (`anyOf`) is not used where "first to succeed" is meant.
- [ ] One failed or timed-out dependency cannot fail the whole operation unless the rule says it should.
- [ ] Results are matched to their source (ordered list or keyed map), never by position in an unordered completion stream.
- [ ] The all-failed case has a defined result (empty, default, error) and a test.

## Retries and health

- [ ] Retries inside the fan-out have a budget and backoff with jitter; a fan-out that retries each of N calls M times under load is an amplifier.
- [ ] Repeated timeouts from one dependency trip a circuit breaker or are at least visible as a metric per dependency.

## Tests

- [ ] Time is injected. Tests advance a manual timer; no `Thread.sleep`, no `await(…, SECONDS)` whose success depends on machine speed.
- [ ] There is a test where one dependency never answers and the combined result arrives at the deadline with the others' answers intact.
- [ ] There is a test where one dependency throws synchronously (misconfiguration) and the fan-out still returns a complete result list.
- [ ] If the code runs on a bounded pool, there is a deterministic starvation test (gate + latch) or a documented reason it cannot starve.
